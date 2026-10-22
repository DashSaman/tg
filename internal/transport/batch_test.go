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

type countedReader struct {
	*bytes.Reader
	calls int
}

func (r *countedReader) Read(b []byte) (int, error) { r.calls++; return r.Reader.Read(b) }
func (r *countedReader) Close() error               { return nil }

func TestStreamReceiveAmortizesUnderlyingReadsWithoutChangingFrames(t *testing.T) {
	var encoded bytes.Buffer
	want := bytes.Repeat([]byte{29}, session.MaxFrame)
	for range 64 {
		if err := sendStream(&encoded, want); err != nil {
			t.Fatal(err)
		}
	}
	input := &countedReader{Reader: bytes.NewReader(encoded.Bytes())}
	w := &httpWire{reader: input}
	for range 64 {
		got, err := w.Receive(context.Background())
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("buffering changed original frame", err)
		}
	}
	if input.calls > 2 {
		t.Fatalf("64 adjacent frames triggered %d underlying stream reads; expected at most two bounded refills", input.calls)
	}
}

func TestBufferedStreamReceiveDoesNotWaitForFullBuffer(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	w := &httpWire{reader: reader}
	done := make(chan error, 1)
	go func() { done <- sendStream(writer, []byte("small-control")) }()
	type result struct {
		data []byte
		err  error
	}
	received := make(chan result, 1)
	go func() { data, err := w.Receive(context.Background()); received <- result{data, err} }()
	select {
	case got := <-received:
		if got.err != nil || string(got.data) != "small-control" {
			t.Fatal("small frame changed", got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("read-ahead waited for data beyond the available small frame")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBufferedStreamCloseInterruptsPendingRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	w := &httpWire{reader: reader, cancel: func() { _ = writer.CloseWithError(context.Canceled) }}
	done := make(chan error, 1)
	go func() { _, err := w.Receive(context.Background()); done <- err }()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed reader returned a valid frame")
		}
	case <-time.After(time.Second):
		t.Fatal("read-ahead blocked shutdown")
	}
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

func TestCarrierBatchCanCarryThirtyTwoJumboWireGuardPackets(t *testing.T) {
	var output countedWriter
	flushes := 0
	w := &httpWire{writer: &output, flush: func() error { flushes++; return nil }}
	var frames [][]byte
	for i := range 32 {
		fragments, err := session.Fragment(bytes.Repeat([]byte{byte(i + 1)}, 7200), uint64(i+1))
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
		t.Fatalf("32-packet jumbo vector split into small stream batches: %v", err)
	}
	if output.calls != 1 || flushes != 1 {
		t.Fatal("jumbo vector was not one bounded write/flush", output.calls, flushes)
	}
	for _, want := range frames {
		got, err := recvStream(&output)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("jumbo vector changed framing", err)
		}
	}
	if output.Len() != 0 {
		t.Fatal("extra bytes after jumbo vector")
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
