package engine

import (
	"bytes"
	"context"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"net"
	"testing"
	"time"
)

// Exercise actual datagram boundaries, ordering, and the stable source socket.
func TestApplicationBatchPreservesDatagrams(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(address, func(t *testing.T) {
			receiver, err := net.ListenPacket("udp", address)
			if err != nil {
				t.Skip(err)
			}
			defer receiver.Close()
			c := config.Defaults()
			c.Role = "exit"
			c.AppAddress = receiver.LocalAddr().String()
			app, err := openApplication(c)
			if err != nil {
				t.Fatal(err)
			}
			defer app.close()
			packets := make([][]byte, 32)
			for i := range packets {
				packets[i] = bytes.Repeat([]byte{byte(i)}, 80+i*31)
			}
			if n, err := app.writeBatch(packets, time.Second); err != nil || n != len(packets) {
				t.Fatalf("batch n=%d error=%v", n, err)
			}
			receiver.SetReadDeadline(time.Now().Add(time.Second))
			buffer := make([]byte, 2048)
			source := ""
			for _, want := range packets {
				n, peer, err := receiver.ReadFrom(buffer)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(buffer[:n], want) {
					t.Fatal("datagram boundary or order changed")
				}
				if source == "" {
					source = peer.String()
				}
				if source != peer.String() {
					t.Fatal("application source socket changed")
				}
			}
		})
	}
}

// Reusing a completed buffer when enqueueing corrupts an earlier queued packet.
// A peer epoch change must also retain the original releasing owner.
func TestQueuedDeliveryRetainsOwnershipAcrossEpochChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &Engine{cfg: config.Defaults(), ctx: ctx, output: make(chan delivery, 256), links: make(map[string]*link), receive: session.NewReassembler(128, time.Second)}
	l := &link{carrier: config.Carrier{Name: "test"}}
	e.links["test"] = l
	owners := make([]*session.Reassembler, 0, 64)
	for i := 0; i < 64; i++ {
		if i == 32 {
			e.receive = session.NewReassembler(128, time.Second)
		}
		owners = append(owners, e.receive)
		f, _ := session.Fragment(bytes.Repeat([]byte{byte(i + 1)}, 900), uint64(i+1))
		e.onData(l, f[0])
	}
	for i := 0; i < 64; i++ {
		d := <-e.output
		if d.owner != owners[i] || !bytes.Equal(d.packet, bytes.Repeat([]byte{byte(i + 1)}, 900)) {
			t.Fatal("queued packet reused or wrong epoch owner")
		}
		d.owner.Release(d.packet)
	}
}

func TestCancellationUnblocksFullDeliveryQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &Engine{cfg: config.Defaults(), ctx: ctx, output: make(chan delivery, 1), links: make(map[string]*link), receive: session.NewReassembler(128, time.Second)}
	l := &link{carrier: config.Carrier{Name: "test"}}
	e.links["test"] = l
	first, _ := session.Fragment([]byte("first"), 1)
	e.onData(l, first[0])
	second, _ := session.Fragment([]byte("second"), 2)
	done := make(chan struct{})
	go func() { e.onData(l, second[0]); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("full queue blocked carrier shutdown")
	}
	d := <-e.output
	d.owner.Release(d.packet)
}

func TestQueuePressureDoesNotSprayPacketsAcrossHealthyCarriers(t *testing.T) {
	c := config.Defaults()
	c.Role = "edge"
	c.AppAddress = "127.0.0.1:0"
	app, err := openApplication(c)
	if err != nil {
		t.Fatal(err)
	}
	defer app.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &Engine{cfg: c, app: app, ctx: ctx, links: make(map[string]*link), selector: session.Selector{Timeout: time.Second, HoldDown: time.Second, Margin: 10 * time.Millisecond}}
	now := time.Now()
	for _, p := range c.Carriers {
		e.links[p.Name] = &link{ctx: ctx, carrier: p, data: make(chan session.Frame, 4), lastPong: now, rtt: time.Millisecond}
	}
	e.selector.Choose(now, e.paths(now)) // Select the cheap UDP path before it fills.
	for i := 0; i < 4; i++ {
		e.links["udp"].data <- session.Frame{}
	}
	sender, err := net.Dial("udp", app.conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	done := make(chan struct{})
	go func() { e.readApplication(ctx); close(done) }()
	defer func() { cancel(); app.close(); <-done }()
	sender.Write([]byte("first"))
	until(t, func() bool { return e.counters.dropped.Load() == 1 || e.counters.queuedBytes.Load() > 0 })
	if len(e.links["web"].data) != 0 || e.counters.queuedBytes.Load() != 0 {
		t.Fatal("queue pressure sprayed a packet onto a different healthy carrier")
	}
	// A bounded burst can drop at capacity; it must not alternate adjacent
	// encrypted packets between paths with different delay and ordering.
	for len(e.links["udp"].data) > 0 {
		<-e.links["udp"].data
	}
	sender.Write([]byte("second"))
	until(t, func() bool { return e.counters.queuedBytes.Load() == 6 })
	if e.Snapshot().ActiveCarrier != "udp" {
		t.Fatal("original healthy path was not retained after burst")
	}
}

func TestApplicationBatchReportsPartialKernelWrite(t *testing.T) {
	receiver, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	c := config.Defaults()
	c.Role = "exit"
	c.AppAddress = receiver.LocalAddr().String()
	app, err := openApplication(c)
	if err != nil {
		t.Fatal(err)
	}
	defer app.close()
	first, last := []byte("consumed"), []byte("unsent")
	// The kernel accepts the first datagram and rejects the oversized second.
	n, err := app.writeBatch([][]byte{first, make([]byte, 65536), last}, time.Second)
	if n != 1 || err == nil {
		t.Fatalf("partial count=%d error=%v", n, err)
	}
	receiver.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 100)
	size, _, err := receiver.ReadFrom(buffer)
	if err != nil || !bytes.Equal(buffer[:size], first) {
		t.Fatal("valid prefix not delivered", err)
	}
	receiver.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if _, _, err := receiver.ReadFrom(buffer); err == nil {
		t.Fatal("tail was sent past kernel error")
	}
	// A failed batch cannot retain old payload aliases or corrupt a later one.
	if n, err := app.writeBatch([][]byte{last, first}, time.Second); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	receiver.SetReadDeadline(time.Now().Add(time.Second))
	for _, want := range [][]byte{last, first} {
		n, _, err := receiver.ReadFrom(buffer)
		if err != nil || !bytes.Equal(buffer[:n], want) {
			t.Fatal("later batch corrupted", err)
		}
	}
}
