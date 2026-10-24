package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/session"
)

type receiveBatchFixture interface {
	ReceiveBatch(context.Context, [][]byte) (int, error)
}

func batchReceiver(t *testing.T, w *httpWire) receiveBatchFixture {
	t.Helper()
	b, ok := any(w).(receiveBatchFixture)
	if !ok {
		t.Fatal("stream wire cannot return already-buffered frames as one bounded batch")
	}
	return b
}

func appendStreamFrame(dst, frame []byte) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(frame)))
	return append(dst, frame...)
}

func TestReceiveBatchDrainsOnlyAvailableFramesAndBoundsCount(t *testing.T) {
	var input []byte
	for i := range 65 {
		input = appendStreamFrame(input, []byte{byte(i)})
	}
	r := &countedReader{Reader: bytes.NewReader(input)}
	w := &httpWire{reader: r, packetMode: true, packetBytes: 2048}
	b := batchReceiver(t, w)
	var frames [32][]byte
	for first := 0; first < 65; {
		n, err := b.ReceiveBatch(context.Background(), frames[:])
		if err != nil || n != min(32, 65-first) {
			t.Fatalf("first=%d n=%d err=%v", first, n, err)
		}
		for i, frame := range frames[:n] {
			if !bytes.Equal(frame, []byte{byte(first + i)}) {
				t.Fatal("frame order or payload changed")
			}
		}
		first += n
	}
	if r.calls != 1 {
		t.Fatalf("already-buffered input needed %d reads", r.calls)
	}
	if n, err := b.ReceiveBatch(context.Background(), frames[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatal(n, err)
	}
}

func TestReceiveBatchDoesNotWaitForNextFrame(t *testing.T) {
	r, writer := io.Pipe()
	defer r.Close()
	defer writer.Close()
	w := &httpWire{reader: r, packetMode: true, packetBytes: 2048}
	b := batchReceiver(t, w)
	done := make(chan error, 1)
	go func() { _, err := writer.Write(appendStreamFrame(nil, []byte("probe"))); done <- err }()
	got := make(chan error, 1)
	go func() {
		var frames [32][]byte
		n, err := b.ReceiveBatch(context.Background(), frames[:])
		if err == nil && (n != 1 || string(frames[0]) != "probe") {
			err = errors.New("unexpected short batch")
		}
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("receive waited for a second frame")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReceiveBatchKeepsPartialTailForNextCall(t *testing.T) {
	r, writer := io.Pipe()
	defer r.Close()
	defer writer.Close()
	w := &httpWire{reader: r, packetMode: true, packetBytes: 2048}
	b := batchReceiver(t, w)
	first := appendStreamFrame(nil, []byte("first"))
	next := appendStreamFrame(nil, []byte("second"))
	go func() { _, _ = writer.Write(append(first, next[:4]...)) }()
	var frames [32][]byte
	if n, err := b.ReceiveBatch(context.Background(), frames[:]); err != nil || n != 1 || string(frames[0]) != "first" {
		t.Fatal(n, err)
	}
	go func() { _, _ = writer.Write(next[4:]) }()
	if n, err := b.ReceiveBatch(context.Background(), frames[:]); err != nil || n != 1 || string(frames[0]) != "second" {
		t.Fatal(n, err)
	}
}

func TestReceiveBatchMaximumFrameAndInvalidTail(t *testing.T) {
	want := bytes.Repeat([]byte{71}, session.MaxStreamFrame)
	input := appendStreamFrame(nil, want)
	input = appendStreamFrame(input, []byte("last"))
	input = append(input, 0, 0)
	w := &httpWire{reader: &countedReader{Reader: bytes.NewReader(input)}, packetMode: true, packetBytes: session.MaxStreamPacket}
	b := batchReceiver(t, w)
	var frames [32][]byte
	if n, err := b.ReceiveBatch(context.Background(), frames[:]); n != 2 || err == nil || !bytes.Equal(frames[0], want) || string(frames[1]) != "last" {
		t.Fatal("valid prefix must precede malformed tail", n, err)
	}
}

func TestReceiveBatchRejectsInvalidDestinationAndCancellation(t *testing.T) {
	w := &httpWire{reader: &countedReader{Reader: bytes.NewReader(nil)}, packetMode: true, packetBytes: 2048}
	b := batchReceiver(t, w)
	for _, size := range []int{0, 33} {
		if _, err := b.ReceiveBatch(context.Background(), make([][]byte, size)); err == nil {
			t.Fatal("unbounded destination accepted", size)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.ReceiveBatch(ctx, make([][]byte, 32)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
