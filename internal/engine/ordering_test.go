package engine

import (
	"context"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"net"
	"testing"
	"time"
)

func orderedWriter(t *testing.T) (*Engine, net.PacketConn, *link) {
	return applicationWriter(t, true)
}

func applicationWriter(t *testing.T, ordered bool) (*Engine, net.PacketConn, *link) {
	t.Helper()
	receiver, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := config.Defaults()
	c.Role = "exit"
	c.AppAddress = receiver.LocalAddr().String()
	c.Redundant = true
	c.OrderedOutput = ordered
	app, err := openApplication(c)
	if err != nil {
		receiver.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{cfg: c, ctx: ctx, app: app, output: make(chan delivery, 8), links: make(map[string]*link), receive: newReceiver(c)}
	l := &link{carrier: config.Carrier{Name: "path"}}
	e.links["path"] = l
	done := make(chan struct{})
	go func() { e.writeApplication(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		app.close()
		receiver.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("ordered output failed to cancel")
		}
		for len(e.output) > 0 {
			d := <-e.output
			d.owner.Release(d.packet)
		}
	})
	return e, receiver, l
}

func TestRedundantDefaultOutputDoesNotBlockIndependentDatagramBehindGap(t *testing.T) {
	e, receiver, l := applicationWriter(t, false)
	second, _ := session.Fragment([]byte("second"), 2)
	first, _ := session.Fragment([]byte("first"), 1)
	e.onData(l, second[0])
	receiver.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 100)
	n, _, err := receiver.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "second" {
		t.Fatal("independent datagram blocked behind another flow's missing packet", err)
	}
	e.onData(l, first[0])
	n, _, err = receiver.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "first" {
		t.Fatal("late repaired datagram was lost", err)
	}
}

func TestRedundantOutputWaitsBrieflyForEarlierPacketAcrossCarriers(t *testing.T) {
	e, receiver, l := orderedWriter(t)
	second, _ := session.Fragment([]byte("second"), 2)
	first, _ := session.Fragment([]byte("first"), 1)
	e.onData(l, second[0])
	receiver.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
	buf := make([]byte, 100)
	if _, _, err := receiver.ReadFrom(buf); err == nil {
		t.Fatal("newer copy reached inner TCP before the earlier packet")
	}
	e.onData(l, first[0])
	receiver.SetReadDeadline(time.Now().Add(time.Second))
	for _, want := range []string{"first", "second"} {
		n, _, err := receiver.ReadFrom(buf)
		if err != nil || string(buf[:n]) != want {
			t.Fatal("packet order/integrity changed", want, err)
		}
	}
}

func TestRedundantOutputDoesNotSilentlyExpireMissingPacket(t *testing.T) {
	e, receiver, l := orderedWriter(t)
	second, _ := session.Fragment([]byte("second"), 2)
	first, _ := session.Fragment([]byte("late-first"), 1)
	e.onData(l, second[0])
	receiver.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	buf := make([]byte, 100)
	if _, _, err := receiver.ReadFrom(buf); err == nil {
		t.Fatal("missing accepted packet was skipped before repair")
	}
	e.onData(l, first[0])
	receiver.SetReadDeadline(time.Now().Add(time.Second))
	for _, want := range []string{"late-first", "second"} {
		n, _, err := receiver.ReadFrom(buf)
		if err != nil || string(buf[:n]) != want {
			t.Fatal("repair did not release ordered packets", want, err)
		}
	}
}

func TestRedundantOutputEpochChangeDiscardsHeldOldPacket(t *testing.T) {
	e, receiver, l := orderedWriter(t)
	old, _ := session.Fragment([]byte("old-second"), 2)
	e.onData(l, old[0])
	e.mu.Lock()
	e.receive = newReceiver(e.cfg)
	e.mu.Unlock()
	fresh, _ := session.Fragment([]byte("new-first"), 1)
	e.onData(l, fresh[0])
	receiver.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 100)
	n, _, err := receiver.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "new-first" {
		t.Fatal("new epoch output blocked/corrupted by held old data", err)
	}
	receiver.SetReadDeadline(time.Now().Add(70 * time.Millisecond))
	if _, _, err := receiver.ReadFrom(buf); err == nil {
		t.Fatal("held old epoch packet survived reset")
	}
}
