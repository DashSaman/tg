package transport

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestConnectReadAheadIsLazyAndDoesNotDelaySmallInput(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	c := &connectConn{Conn: left}
	readOne := func(value byte) {
		t.Helper()
		go func() { _, _ = right.Write([]byte{value}) }()
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		var b [1]byte
		if _, err := io.ReadFull(c, b[:]); err != nil || b[0] != value {
			t.Fatal("sparse input waited for batching", err)
		}
	}
	readOne(7)
	if c.reader != nil {
		t.Fatal("unauthenticated handshake allocated read-ahead")
	}
	c.enableReadAhead()
	if c.reader != nil {
		t.Fatal("activation eagerly allocated idle buffer")
	}
	readOne(9)
	if c.reader == nil || c.reader.Size() != 256<<10 {
		t.Fatal("read-ahead exceeded fixed budget")
	}
}

func TestConnectBlockedReadDoesNotBlockWriteAndCloseInterruptsRead(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	c := &connectConn{Conn: left}
	c.enableReadAhead()
	done := make(chan error, 1)
	go func() { var b [1]byte; _, err := c.Read(b[:]); done <- err }()
	got := make(chan []byte, 1)
	go func() { b := make([]byte, 5); _, _ = io.ReadFull(right, b); got <- b }()
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte("probe")); err != nil {
		t.Fatal("read locked independent write", err)
	}
	if b := <-got; !bytes.Equal(b, []byte("probe")) {
		t.Fatal(string(b))
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not interrupt pending read")
	}
}
