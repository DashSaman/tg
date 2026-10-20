package transport

import (
	"bytes"
	"context"
	"github.com/DashSaman/tg/internal/session"
	"github.com/quic-go/quic-go"
	"io"
	"net"
	"testing"
	"time"
)

// Native SendDatagram copies input and rejects oversize data before enqueueing.
type datagramFixture struct {
	limit       int
	packets     [][]byte
	shrinkAfter int
	shrinkTo    int
	incoming    chan []byte
	nativeCtx   context.Context
}

func (f *datagramFixture) SendDatagram(b []byte) error {
	if f.shrinkAfter > 0 && len(f.packets) >= f.shrinkAfter {
		f.limit = f.shrinkTo
	}
	if len(b) > f.limit {
		return &quic.DatagramTooLargeError{MaxDatagramPayloadSize: int64(f.limit)}
	}
	f.packets = append(f.packets, bytes.Clone(b))
	return nil
}

func TestQUICMTUShrinkDoesNotReplayEarlierSuccessfulBundle(t *testing.T) {
	f := &datagramFixture{limit: 1400, shrinkAfter: 1, shrinkTo: 1150, incoming: make(chan []byte, 128)}
	w := &quicWire{conn: f}
	defer w.Close()
	frames := [][]byte{bytes.Repeat([]byte{1}, 1040), bytes.Repeat([]byte{2}, 1040), bytes.Repeat([]byte{3}, 304)}
	if err := w.SendBatch(frames); err != nil {
		t.Fatal(err)
	}
	if len(f.packets) != 3 {
		t.Fatalf("got %d datagrams after mid-batch shrink", len(f.packets))
	}
	f.queueSent()
	for _, want := range frames {
		got, err := w.Receive(context.Background())
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("earlier successful bundle was replayed", err)
		}
	}
	if len(f.packets) != 0 || w.offset != len(w.received) {
		t.Fatal("extra frames after shrink")
	}
}
func (f *datagramFixture) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.Context().Done():
		return nil, net.ErrClosed
	case b, ok := <-f.incoming:
		if !ok {
			return nil, io.EOF
		}
		return b, nil
	}
}
func (f *datagramFixture) Context() context.Context {
	if f.nativeCtx != nil {
		return f.nativeCtx
	}
	return context.Background()
}
func (f *datagramFixture) queueSent() {
	for _, p := range f.packets {
		f.incoming <- p
	}
	f.packets = nil
}
func (f *datagramFixture) CloseWithError(quic.ApplicationErrorCode, string) error { return nil }

func TestQUICBatchAdaptsPathMTUWithoutDuplicates(t *testing.T) {
	f := &datagramFixture{limit: 1150, incoming: make(chan []byte, 128)}
	w := &quicWire{conn: f}
	defer w.Close()
	frames := [][]byte{bytes.Repeat([]byte{1}, session.MaxFrame), bytes.Repeat([]byte{2}, 304)}
	for _, limit := range []int{1150, 1400, 1150} {
		f.limit = limit
		w.retryMTU = time.Time{}
		if err := w.SendBatch(frames); err != nil {
			t.Fatal(err)
		}
		want := 2
		if limit == 1400 {
			want = 1
		}
		if len(f.packets) != want {
			t.Fatalf("limit %d: got %d datagrams, want %d", limit, len(f.packets), want)
		}
		for _, packet := range f.packets {
			if len(packet) > limit {
				t.Fatal("path limit exceeded")
			}
		}
		f.queueSent()
		for _, want := range frames {
			got, err := w.Receive(context.Background())
			if err != nil || !bytes.Equal(got, want) {
				t.Fatal("changed datagram, boundary or order", err)
			}
		}
		if len(f.packets) != 0 || w.offset != len(w.received) {
			t.Fatal("duplicate or trailing data")
		}
	}
	// Validate the complete batch before sending any of it.
	if err := w.SendBatch([][]byte{[]byte("valid"), nil}); err == nil || len(f.packets) != 0 {
		t.Fatal("partial invalid batch admitted")
	}
}

func TestQUICRejectsMalformedBundleBeforeDeliveringPrefix(t *testing.T) {
	for _, packet := range [][]byte{nil, {0}, {0, 0}, {255, 255}, {0, 1, 7, 0}, {0, 2, 7}, make([]byte, 1401), bytes.Repeat([]byte{0, 1, 7}, 17)} {
		f := &datagramFixture{incoming: make(chan []byte, 128)}
		f.incoming <- packet
		w := &quicWire{conn: f}
		if _, err := w.Receive(context.Background()); err == nil {
			t.Fatalf("accepted malformed bundle %x", packet)
		}
		w.Close()
	}
}

func TestQUICDrainsNativeQueueWhileFrameConsumerPauses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &datagramFixture{incoming: make(chan []byte, 128)}
	w := &quicWire{conn: f}
	defer w.Close()
	f.incoming <- []byte{0, 1, 7}
	if _, err := w.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	// Pause application processing. The native 128-slot queue must still drain
	// into bounded wire-owned storage, without requiring another frame Receive.
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := 0; i < 2048; i++ {
			select {
			case <-ctx.Done():
				return
			case f.incoming <- []byte{0, 2, byte(i >> 8), byte(i)}:
			}
		}
	}()
	select {
	case <-sent:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("native QUIC queue stalled behind frame processing")
	}
	for i := 0; i < 2048; i++ {
		b, err := w.Receive(ctx)
		if err != nil || !bytes.Equal(b, []byte{byte(i >> 8), byte(i)}) {
			t.Fatal("prefetch reordered or changed datagram", i, err)
		}
	}
}

func TestQUICReceiveDeadlineDoesNotCancelWireReader(t *testing.T) {
	f := &datagramFixture{incoming: make(chan []byte, 128)}
	w := &quicWire{conn: f}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := w.Receive(ctx); err == nil {
		t.Fatal("receive ignored deadline")
	}
	f.incoming <- []byte{0, 2, 8, 9}
	next, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if b, err := w.Receive(next); err != nil || !bytes.Equal(b, []byte{8, 9}) {
		t.Fatal("per-call deadline poisoned connection", err)
	}
}

func TestQUICCloseInterruptsFullPrefetch(t *testing.T) {
	f := &datagramFixture{incoming: make(chan []byte, 4096)}
	for i := 0; i < 4096; i++ {
		f.incoming <- []byte{0, 1, 7}
	}
	w := &quicWire{conn: f}
	if _, err := w.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Leave the consumer paused while the independent reader fills its budget.
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() { w.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("full prefetch prevented close")
	}
	if err := w.Close(); err != nil {
		t.Fatal("repeated close", err)
	}
}

func TestNativeQUICCloseUnblocksPausedPrefetch(t *testing.T) {
	native, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &datagramFixture{nativeCtx: native, incoming: make(chan []byte, 4096)}
	for i := 0; i < 4096; i++ {
		f.incoming <- []byte{0, 1, 7}
	}
	w := &quicWire{conn: f}
	defer w.Close()
	if _, err := w.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(w.receiveQueue) < cap(w.receiveQueue) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(w.receiveQueue) != cap(w.receiveQueue) {
		t.Fatal("prefetch budget did not fill")
	}
	cancel() // Native connection closure while downstream processing is paused.
	select {
	case <-w.readerDone:
	case <-time.After(time.Second):
		t.Fatal("native closure leaked blocked prefetch reader")
	}
}
