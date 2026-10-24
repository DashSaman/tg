//go:build linux

package engine

import (
	"bytes"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/config"
	"golang.org/x/net/ipv4"
)

type stalledApplicationBatch struct {
	applicationBatchConn
	entered chan string
	release chan struct{}
}

func (b *stalledApplicationBatch) WriteBatch(messages []ipv4.Message, _ int) (int, error) {
	b.entered <- messages[0].Addr.String()
	<-b.release
	return len(messages), nil
}

func TestApplicationBackpressuredWriteDoesNotBlockOppositeRead(t *testing.T) {
	c := config.Defaults()
	c.Role = "edge"
	c.AppAddress = "127.0.0.1:0"
	a, err := openApplication(c)
	if err != nil {
		t.Fatal(err)
	}
	a.peer = netip.MustParseAddrPort("127.0.0.1:9")
	stalled := &stalledApplicationBatch{applicationBatchConn: a.batch, entered: make(chan string, 1), release: make(chan struct{})}
	a.batch = stalled
	writeDone := make(chan error, 1)
	go func() {
		n, err := a.writeBatch([][]byte{[]byte("one"), []byte("two")}, time.Second)
		if err == nil && n != 2 {
			err = net.ErrClosed
		}
		writeDone <- err
	}()
	readFinished := make(chan struct{})
	readStarted := false
	defer func() {
		close(stalled.release)
		if err := <-writeDone; err != nil {
			t.Error(err)
		}
		a.close()
		if readStarted {
			<-readFinished
		}
	}()
	select {
	case peer := <-stalled.entered:
		if peer != "127.0.0.1:9" {
			t.Fatal("write did not snapshot the original peer")
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not enter the controlled stall")
	}
	sender, err := net.DialUDP("udp4", nil, a.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	want := []byte("opposite-direction input while output is stalled")
	if _, err = sender.Write(want); err != nil {
		t.Fatal(err)
	}
	type readResult struct {
		packet []byte
		err    error
	}
	readDone := make(chan readResult, 1)
	readStarted = true
	go func() {
		defer close(readFinished)
		packet, err := a.read(c.MaxPacketBytes)
		readDone <- readResult{packet, err}
	}()
	select {
	case got := <-readDone:
		if got.err != nil || !bytes.Equal(got.packet, want) {
			t.Fatalf("opposite read changed: %q %v", got.packet, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("opposite input waited behind a backpressured UDP write")
	}
	a.mu.Lock()
	peer := a.peer
	a.mu.Unlock()
	if peer != sender.LocalAddr().(*net.UDPAddr).AddrPort() {
		t.Fatal("new valid input peer was not recorded during the output stall")
	}
}
