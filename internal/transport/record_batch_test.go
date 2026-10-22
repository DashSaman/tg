package transport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

type batchTestConn struct {
	net.Conn
	mu        sync.Mutex
	writes    [][]byte
	received  chan []byte
	closed    chan struct{}
	once      sync.Once
	started   chan struct{}
	startOnce sync.Once
	block     bool
	failure   error
	short     bool
}

func newBatchTestConn() *batchTestConn {
	return &batchTestConn{received: make(chan []byte, 32), closed: make(chan struct{}), started: make(chan struct{})}
}
func (f *batchTestConn) Write(b []byte) (int, error) {
	f.startOnce.Do(func() { close(f.started) })
	if f.block {
		<-f.closed
		return 0, net.ErrClosed
	}
	if f.failure != nil {
		return 0, f.failure
	}
	select {
	case <-f.closed:
		return 0, net.ErrClosed
	default:
	}
	p := append([]byte(nil), b...)
	f.mu.Lock()
	f.writes = append(f.writes, p)
	f.mu.Unlock()
	f.received <- p
	if f.short {
		return len(b) - 1, nil
	}
	return len(b), nil
}
func (f *batchTestConn) Close() error                     { f.once.Do(func() { close(f.closed) }); return nil }
func (f *batchTestConn) SetReadDeadline(time.Time) error  { return nil }
func (f *batchTestConn) SetWriteDeadline(time.Time) error { return nil }
func batchRecord(value byte) []byte {
	b := bytes.Repeat([]byte{value}, fullTLS13Record)
	b[0] = 23
	b[1] = 3
	b[2] = 3
	binary.BigEndian.PutUint16(b[3:5], uint16(len(b)-5))
	return b
}
func waitBatch(t *testing.T, f *batchTestConn) []byte {
	t.Helper()
	select {
	case b := <-f.received:
		return b
	case <-time.After(time.Second):
		t.Fatal("ciphertext not flushed")
		return nil
	}
}
func TestTLSRecordBatchOwnsFullRecordsAndFlushesShortControlImmediately(t *testing.T) {
	f := newBatchTestConn()
	c := newRecordBatchConn(f, time.Hour)
	defer c.Close()
	control := []byte{1, 2, 3}
	if _, err := c.Write(control); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(waitBatch(t, f), control) || len(c.buffer) != 0 {
		t.Fatal("short record buffered or allocated bulk storage")
	}
	b := batchRecord(71)
	original := append([]byte(nil), b...)
	if n, err := c.Write(b); n != len(b) || err != nil {
		t.Fatal(n, err)
	}
	clear(b)
	if _, err := c.Write(control); err != nil {
		t.Fatal(err)
	}
	got := waitBatch(t, f)
	if !bytes.Equal(got, append(original, control...)) {
		t.Fatal("ciphertext ownership or immediate control flush failed")
	}

}
func TestTLSRecordBatchSixteenFullRecordsUseOneWrite(t *testing.T) {
	f := newBatchTestConn()
	c := newRecordBatchConn(f, time.Hour)
	defer c.Close()
	var expected []byte
	for i := 0; i < 16; i++ {
		b := batchRecord(byte(i))
		expected = append(expected, b...)
		if _, err := c.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	got := waitBatch(t, f)
	f.mu.Lock()
	writes := len(f.writes)
	f.mu.Unlock()
	if writes != 1 || !bytes.Equal(got, expected) || len(c.buffer) != 16*fullTLS13Record {
		t.Fatal("wrong bounded ciphertext batch", writes, len(got), len(c.buffer))
	}
}
func TestTLSRecordBatchTailEventuallyFlushesWithoutMoreInput(t *testing.T) {
	f := newBatchTestConn()
	c := newRecordBatchConn(f, time.Millisecond)
	defer c.Close()
	b := batchRecord(7)
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(waitBatch(t, f), b) {
		t.Fatal("tail changed")
	}
}
func TestTLSRecordBatchCloseInterruptsBlockedFlush(t *testing.T) {
	f := newBatchTestConn()
	f.block = true
	c := newRecordBatchConn(f, time.Hour)
	done := make(chan error, 1)
	go func() {
		for range 16 {
			if _, err := c.Write(batchRecord(7)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case <-f.started:
	case <-time.After(time.Second):
		t.Fatal("flush never started")
	}
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close deadlocked behind flush mutex")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked flush reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not stop")
	}
}
func TestTLSRecordBatchAsyncFailureIsStickyAndClosesTransport(t *testing.T) {
	f := newBatchTestConn()
	f.failure = io.ErrClosedPipe
	c := newRecordBatchConn(f, time.Millisecond)
	defer c.Close()
	if _, err := c.Write(batchRecord(7)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("failed tail did not close transport")
	}
	if n, err := c.Write([]byte{1}); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("async failure lost", n, err)
	}
}
func TestTLSRecordBatchDeadlineRejectsBufferedAcceptance(t *testing.T) {
	f := newBatchTestConn()
	c := newRecordBatchConn(f, time.Hour)
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Write(batchRecord(7)); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("accepted data after deadline", n, err)
	}
	if len(c.buffer) != 0 {
		t.Fatal("expired write allocated buffer")
	}
}
func TestTLSRecordBatchReadRemainsIndependentOfPendingWrite(t *testing.T) {
	a, b := net.Pipe()
	c := newRecordBatchConn(a, time.Hour)
	defer c.Close()
	defer b.Close()
	if _, err := c.Write(batchRecord(7)); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = b.Write([]byte{91}) }()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var got [1]byte
	if _, err := io.ReadFull(c, got[:]); err != nil || got[0] != 91 {
		t.Fatal("pending ciphertext blocked reverse read", err)
	}
}
func TestTLSRecordBatchRejectsShortUnderlyingFlush(t *testing.T) {
	f := newBatchTestConn()
	f.short = true
	c := newRecordBatchConn(f, time.Hour)
	defer c.Close()
	for range 15 {
		if _, err := c.Write(batchRecord(7)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Write(batchRecord(7)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short physical write lost", err)
	}
	select {
	case <-f.closed:
	default:
		t.Fatal("short flush did not close transport")
	}
}

func TestTLSRecordBatchRejectsShortImmediateControlWrite(t *testing.T) {
	f := newBatchTestConn()
	f.short = true
	c := newRecordBatchConn(f, time.Hour)
	defer c.Close()
	if _, err := c.Write([]byte{1, 2}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short control write lost", err)
	}
	select {
	case <-f.closed:
	default:
		t.Fatal("short control write did not close transport")
	}
}
