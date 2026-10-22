package transport

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/session"
)

type countedWriter struct {
	bytes.Buffer
	calls int
}

func (w *countedWriter) Write(b []byte) (int, error) { w.calls++; return w.Buffer.Write(b) }

func TestHTTPBatchPreservesFramingWithOneWriteAndFlush(t *testing.T) {
	var output countedWriter
	flushes := 0
	w := &httpWire{writer: &output, flush: func() error { flushes++; return nil }}
	frames := [][]byte{[]byte("first"), bytes.Repeat([]byte{7}, session.MaxFrame), []byte("last")}
	if err := w.SendBatch(frames); err != nil {
		t.Fatal(err)
	}
	if output.calls != 1 || flushes != 1 {
		t.Fatalf("writes=%d flushes=%d", output.calls, flushes)
	}
	for _, want := range frames {
		got, err := recvStream(&output)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("changed stream framing", err)
		}
	}
	if output.Len() != 0 {
		t.Fatal("extra stream bytes")
	}
	if err := w.SendBatch([][]byte{[]byte("valid"), make([]byte, session.MaxFrame+1)}); err == nil || output.Len() != 0 {
		t.Fatal("invalid batch partially written")
	}
}

func TestCarrierBatchCanCarryFullApplicationUDPSegmentVector(t *testing.T) {
	var output countedWriter
	flushes := 0
	w := &httpWire{writer: &output, flush: func() error { flushes++; return nil }}
	// WG MTU 1280 yields 1312-byte UDP payloads, each needing two TG fragments.
	frames := make([][]byte, 0, 64)
	for i := range 32 {
		fragments, err := session.Fragment(bytes.Repeat([]byte{byte(i + 1)}, 1312), uint64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fragments {
			b, err := session.Encode(f)
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, b)
		}
	}
	if err := w.SendBatch(frames); err != nil {
		t.Fatalf("full 32-datagram vector was split into small carrier batches: %v", err)
	}
	if output.calls != 1 || flushes != 1 {
		t.Fatalf("writes=%d flushes=%d", output.calls, flushes)
	}
	for _, want := range frames {
		got, err := recvStream(&output)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("large batch changed frame boundaries", err)
		}
	}
	if output.Len() != 0 {
		t.Fatal("trailing batch bytes")
	}
}

func TestHTTPReceiveBufferValidUntilNextReceive(t *testing.T) {
	var encoded bytes.Buffer
	_ = sendStream(&encoded, []byte("one"))
	_ = sendStream(&encoded, []byte("two"))
	w := &httpWire{reader: io.NopCloser(&encoded)}
	for _, want := range []string{"one", "two"} {
		got, err := w.Receive(context.Background())
		if err != nil || string(got) != want {
			t.Fatal("buffered receive", err)
		}
	}
}

func TestHTTPBatchAndReceiveBounds(t *testing.T) {
	var output countedWriter
	w := &httpWire{writer: &output}
	for _, frames := range [][][]byte{nil, make([][]byte, MaxBatchFrames+1)} {
		if err := w.SendBatch(frames); err == nil || output.calls != 0 {
			t.Fatal("out of bounds batch admitted")
		}
	}
	for _, encoded := range [][]byte{{0, 0}, {255, 255}, {0, 2, 1}} {
		r := &httpWire{reader: io.NopCloser(bytes.NewReader(encoded))}
		if _, err := r.Receive(context.Background()); err == nil {
			t.Fatal("invalid framed stream accepted")
		}
	}
}

func TestHTTPCloseInterruptsBlockedBatch(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	started := make(chan struct{})
	w := &httpWire{reader: io.NopCloser(bytes.NewReader(nil)), writer: writer, beforeWrite: func() error { close(started); return nil }, cancel: func() { _ = writer.CloseWithError(context.Canceled) }}
	done := make(chan error, 1)
	go func() { done <- w.SendBatch([][]byte{[]byte("blocked"), []byte("batch")}) }()
	<-started
	select {
	case <-done:
		t.Fatal("batch was not blocked on the pipe")
	default:
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed batch succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("close failed to interrupt batch write")
	}
}
