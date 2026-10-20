package transport

import (
	"bytes"
	"context"
	"github.com/DashSaman/tg/internal/session"
	"github.com/quic-go/quic-go"
	"io"
	"testing"
	"time"
)

// Native SendDatagram copies input and rejects oversize data before enqueueing.
type datagramFixture struct {
	limit       int
	packets     [][]byte
	shrinkAfter int
	shrinkTo    int
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
	f := &datagramFixture{limit: 1400, shrinkAfter: 1, shrinkTo: 1150}
	w := &quicWire{conn: f}
	frames := [][]byte{bytes.Repeat([]byte{1}, 1040), bytes.Repeat([]byte{2}, 1040), bytes.Repeat([]byte{3}, 304)}
	if err := w.SendBatch(frames); err != nil {
		t.Fatal(err)
	}
	if len(f.packets) != 3 {
		t.Fatalf("got %d datagrams after mid-batch shrink", len(f.packets))
	}
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
func (f *datagramFixture) ReceiveDatagram(context.Context) ([]byte, error) {
	if len(f.packets) == 0 {
		return nil, io.EOF
	}
	b := f.packets[0]
	f.packets = f.packets[1:]
	return b, nil
}
func (f *datagramFixture) CloseWithError(quic.ApplicationErrorCode, string) error { return nil }

func TestQUICBatchAdaptsPathMTUWithoutDuplicates(t *testing.T) {
	f := &datagramFixture{limit: 1150}
	w := &quicWire{conn: f}
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
		w := &quicWire{conn: &datagramFixture{packets: [][]byte{packet}}}
		if _, err := w.Receive(context.Background()); err == nil {
			t.Fatalf("accepted malformed bundle %x", packet)
		}
	}
}
