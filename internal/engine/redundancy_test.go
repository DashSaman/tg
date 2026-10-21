package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
)

func redundantReader(t *testing.T, full string) (*Engine, net.Conn) {
	t.Helper()
	c := config.Defaults()
	c.Role = "edge"
	c.AppAddress = "127.0.0.1:0"
	if err := json.Unmarshal([]byte(`{"redundant":true}`), &c); err != nil {
		t.Fatal(err)
	}
	app, err := openApplication(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{cfg: c, app: app, ctx: ctx, links: make(map[string]*link), selector: session.Selector{Timeout: time.Second}}
	for _, p := range c.Carriers {
		l := &link{ctx: ctx, carrier: p, data: make(chan session.Frame, 4), repair: make(chan session.Frame, 1024), space: make(chan struct{}, 1), lastPong: time.Now(), rtt: time.Millisecond}
		e.links[p.Name] = l
	}
	e.selector.Choose(time.Now(), e.paths(time.Now()))
	if full != "" {
		for i := 0; i < 4; i++ {
			e.links[full].data <- session.Frame{ID: 99}
		}
	}
	sender, err := net.Dial("udp", app.conn.LocalAddr().String())
	if err != nil {
		cancel()
		app.close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { e.readApplication(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		sender.Close()
		app.close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("reader shutdown blocked")
		}
	})
	return e, sender
}

func TestRedundantReceiverAcceptsDelayedUniquePacketBeyondLegacyWindow(t *testing.T) {
	c := config.Defaults()
	c.Role = "edge"
	c.SessionID = strings.Repeat("1", 32)
	json.Unmarshal([]byte(`{"redundant":true}`), &c)
	e, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.ctx = ctx
	e.output = make(chan delivery, 1)
	a := &link{carrier: config.Carrier{Name: "a"}}
	e.links["a"] = a
	for id := uint64(2); id <= 4097; id++ {
		frames, _ := session.Fragment([]byte("newer"), id)
		e.onData(a, frames[0])
		d := <-e.output
		d.owner.Release(d.packet)
	}
	frames, _ := session.Fragment([]byte("delayed-survivor"), 1)
	e.onData(a, frames[0])
	if len(e.output) != 1 {
		t.Fatal("legacy replay window discarded the only delayed copy")
	}
	d := <-e.output
	defer d.owner.Release(d.packet)
	if string(d.packet) != "delayed-survivor" {
		t.Fatal("late packet changed")
	}
	e.onData(a, frames[0])
	if len(e.output) != 0 {
		t.Fatal("delayed packet delivered twice")
	}
}

func TestRedundantAdmissionCopiesWholePacketWithSameID(t *testing.T) {
	e, sender := redundantReader(t, "")
	payload := bytes.Repeat([]byte{37}, 2048)
	if _, err := sender.Write(payload); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { return e.counters.queuedBytes.Load() > 0 })
	if len(e.links["web"].data) != 2 || len(e.links["udp"].data) != 2 {
		t.Fatal("both healthy carriers must receive the complete packet")
	}
	var id uint64
	for _, name := range []string{"web", "udp"} {
		a, b := <-e.links[name].data, <-e.links[name].data
		if id == 0 {
			id = a.ID
		}
		if a.ID != id || b.ID != id || !bytes.Equal(append(a.Payload, b.Payload...), payload) {
			t.Fatal("copies changed packet identity or payload")
		}
	}
	if e.counters.queuedBytes.Load() != uint64(len(payload)) {
		t.Fatal("logical goodput counted the replica twice")
	}
}

func TestRedundantAdmissionSkipsFullCarrierWithoutDroppingHealthyCopy(t *testing.T) {
	for _, full := range []string{"udp", "web"} {
		t.Run(full, func(t *testing.T) {
			e, sender := redundantReader(t, full)
			if _, err := sender.Write(make([]byte, 2048)); err != nil {
				t.Fatal(err)
			}
			until(t, func() bool { return e.counters.queuedBytes.Load() > 0 || e.counters.dropped.Load() > 0 })
			other := "web"
			if full == "web" {
				other = "udp"
			}
			if e.counters.dropped.Load() != 0 || len(e.links[other].data) != 2 {
				t.Fatal("one blocked carrier prevented delivery on the healthy carrier")
			}
			if len(e.links[full].data) != 4 {
				t.Fatal("partial copy admitted or bounded queue changed")
			}
		})
	}
}

func TestConcurrentCarriersDeduplicateBeforeApplicationDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &Engine{cfg: config.Defaults(), ctx: ctx, output: make(chan delivery, 8), links: make(map[string]*link), receive: session.NewReassembler(128, time.Second)}
	a := &link{carrier: config.Carrier{Name: "a"}}
	b := &link{carrier: config.Carrier{Name: "b"}}
	e.links["a"] = a
	e.links["b"] = b
	frames, _ := session.Fragment(bytes.Repeat([]byte{61}, 2048), 1)
	done := make(chan struct{}, 2)
	for _, l := range []*link{a, b} {
		go func() {
			for _, f := range frames {
				e.onData(l, f)
			}
			done <- struct{}{}
		}()
	}
	<-done
	<-done
	if len(e.output) != 1 {
		t.Fatal("duplicate packet reached application output")
	}
	d := <-e.output
	defer d.owner.Release(d.packet)
	if !bytes.Equal(d.packet, bytes.Repeat([]byte{61}, 2048)) {
		t.Fatal("cross-carrier reassembly corrupted data")
	}
}
