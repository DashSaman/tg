package engine

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
)

func ackFrame(prefix, high uint64) session.Frame {
	p := make([]byte, session.AckBytes)
	binary.BigEndian.PutUint64(p, high)
	return session.Frame{Kind: session.Ack, ID: prefix, Payload: p}
}

func TestRetainedPacketSurvivesSoleAdmittedCarrierFailure(t *testing.T) {
	e, sender := redundantReader(t, "web")
	sender.Write([]byte("survivor"))
	until(t, func() bool { return e.counters.queuedBytes.Load() > 0 })
	original := <-e.links["udp"].data
	e.mu.Lock()
	if len(e.retained) != 1 {
		t.Fatal("unacknowledged packet was not retained")
	}
	e.links["udp"].closed.Store(true)
	for len(e.links["web"].data) > 0 {
		<-e.links["web"].data
	}
	e.repairLocked(time.Now().Add(time.Second))
	e.mu.Unlock()
	select {
	case replay := <-e.links["web"].repair:
		if replay.ID != original.ID || string(replay.Payload) != "survivor" {
			t.Fatal("repair lost identity/payload")
		}
	default:
		t.Fatal("sole queued copy disappeared when carrier failed")
	}
	e.onAck(e.links["web"], ackFrame(original.ID, original.ID))
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.retained) != 0 {
		t.Fatal("ACK did not release packet")
	}
}

func TestRetainedAdmissionBoundAndAckValidation(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &Engine{cfg: c, ctx: ctx, links: make(map[string]*link)}
	l := &link{carrier: config.Carrier{Name: "web"}}
	e.links["web"] = l
	for i := 0; i < retainedCapacity; i++ {
		if !e.admitRedundantLocked([]byte("bounded"), time.Now()) {
			t.Fatal("early admission rejection", i)
		}
	}
	if e.admitRedundantLocked([]byte("overflow"), time.Now()) || e.sequence.Load() != retainedCapacity {
		t.Fatal("overflow allocated ID or grew cache")
	}
	e.onAck(&link{carrier: l.carrier}, ackFrame(retainedCapacity, retainedCapacity))
	e.onAck(l, ackFrame(retainedCapacity+1, retainedCapacity+1))
	if len(e.retained) != retainedCapacity {
		t.Fatal("stale/impossible ACK released data")
	}
	e.onAck(l, ackFrame(1, retainedCapacity))
	if len(e.retained) != retainedCapacity-1 {
		t.Fatal("ACK released packets beyond cumulative prefix")
	}
	if !e.admitRedundantLocked([]byte("next"), time.Now()) || e.sequence.Load() != retainedCapacity+1 {
		t.Fatal("admission left sequence hole")
	}
	e.onAck(l, ackFrame(0, 0))
	if e.acked != 1 {
		t.Fatal("old ACK moved prefix backwards")
	}
}

func TestAckSlotCoalescesWithoutUsingHealthQueue(t *testing.T) {
	e := &Engine{cfg: config.Defaults()}
	l := newLink(e, context.Background(), config.Carrier{}, nil)
	defer l.cancel()
	for i := uint64(0); i < 100; i++ {
		l.sendAck(ackFrame(i, i))
	}
	if len(l.control) != 0 || len(l.ack) != 1 {
		t.Fatal("ACKs flooded health queue")
	}
	if f := <-l.ack; f.ID != 99 {
		t.Fatal("latest cumulative ACK was lost")
	}
}

func TestRepairDoesNotAmplifyWholeUnacknowledgedWindow(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	now := time.Now()
	e := &Engine{cfg: c, links: make(map[string]*link), retained: make(map[uint64]*retainedPacket), ackHighest: 2000}
	l := &link{carrier: c.Carriers[0], data: make(chan session.Frame, 512), repair: make(chan session.Frame, 512), lastPong: now, rtt: time.Millisecond}
	e.links[l.carrier.Name] = l
	e.sequence.Store(2000)
	for id := uint64(1); id <= 2000; id++ {
		sent := now.Add(-time.Second)
		if id <= session.AckBits {
			sent = now
		}
		e.retained[id] = &retainedPacket{packet: []byte("data"), lastSent: sent}
	}
	e.repairLocked(now)
	if len(l.repair) != 0 {
		t.Fatal("repair skipped the in-flight prefix and replayed later received packets")
	}
	e.repairLocked(now.Add(6 * time.Millisecond))
	if len(l.repair) != 128 {
		t.Fatal("repair burst bound changed", len(l.repair))
	}
}

func TestSelectiveAckRepairsOnlyMissingPackets(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	now := time.Now()
	e := &Engine{cfg: c, links: make(map[string]*link), retained: make(map[uint64]*retainedPacket)}
	l := &link{carrier: c.Carriers[0], data: make(chan session.Frame, 256), repair: make(chan session.Frame, 256), lastPong: now, rtt: time.Millisecond}
	e.links[l.carrier.Name] = l
	e.sequence.Store(128)
	for id := uint64(1); id <= 128; id++ {
		e.retained[id] = &retainedPacket{packet: []byte("data"), lastSent: now.Add(-time.Second)}
	}
	f := ackFrame(0, 128)
	for bit := 1; bit < 128; bit++ {
		f.Payload[8+bit/8] |= byte(1) << uint(bit%8)
	}
	e.onAck(l, f)
	e.repairLocked(now)
	if len(l.repair) != 1 {
		t.Fatal("selectively completed packets were replayed", len(l.repair))
	}
	if f := <-l.repair; f.ID != 1 {
		t.Fatal("first missing packet not repaired")
	}
}

func TestReceiverSelectiveAckDescribesHoleAndCompletion(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	e := &Engine{cfg: c, ctx: context.Background(), links: make(map[string]*link), output: make(chan delivery, session.AckBits), receive: newReceiver(c)}
	l := &link{carrier: c.Carriers[0], ack: make(chan session.Frame, 1)}
	e.links[l.carrier.Name] = l
	for id := uint64(2); id <= session.AckBits; id++ {
		frames, _ := session.Fragment([]byte("data"), id)
		e.onData(l, frames[0])
	}
	e.lastAck = time.Time{}
	e.acknowledgeLocked(e.receive)
	f := <-l.ack
	if f.ID != 0 || binary.BigEndian.Uint64(f.Payload) != session.AckBits || f.Payload[8] != 254 || f.Payload[session.AckBytes-1] != 255 {
		t.Fatal("SACK did not identify only packet 1 as missing")
	}
	frames, _ := session.Fragment([]byte("data"), 1)
	e.onData(l, frames[0])
	e.lastAck = time.Time{}
	e.acknowledgeLocked(e.receive)
	f = <-l.ack
	if f.ID != session.AckBits {
		t.Fatal("repair did not advance cumulative prefix")
	}
	for len(e.output) > 0 {
		d := <-e.output
		d.owner.Release(d.packet)
	}
}

func TestRepairBypassesFullOrdinaryQueue(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	c.QueueFrames = 4
	e := &Engine{cfg: c, links: make(map[string]*link)}
	l := newLink(e, context.Background(), c.Carriers[0], nil)
	defer l.cancel()
	l.lastPong = time.Now()
	e.links[l.carrier.Name] = l
	for i := 0; i < cap(l.data); i++ {
		l.data <- session.Frame{Kind: session.Data, ID: 99}
	}
	e.admitRedundantLocked([]byte("missing"), time.Now())
	e.repairLocked(time.Now().Add(time.Second))
	select {
	case f := <-l.repair:
		if f.ID != 1 || string(f.Payload) != "missing" {
			t.Fatal("invalid repair")
		}
	default:
		t.Fatal("repair trapped behind ordinary backlog")
	}
	if len(l.data) != 4 {
		t.Fatal("ordinary queue evicted or grew")
	}
}

func TestSelectiveRepairPipelineReachesMissingPacketBeyondFirst128(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	now := time.Now()
	e := &Engine{cfg: c, links: make(map[string]*link), retained: make(map[uint64]*retainedPacket)}
	l := &link{carrier: c.Carriers[0], repair: make(chan session.Frame, 256), lastPong: now, rtt: time.Millisecond}
	e.links[l.carrier.Name] = l
	e.sequence.Store(512)
	for id := uint64(1); id <= 512; id++ {
		e.retained[id] = &retainedPacket{packet: []byte("data"), lastSent: now.Add(-time.Second)}
	}
	e.retained[1].lastSent = now // first repair is already in flight
	f := ackFrame(0, 512)
	for bit := 1; bit < 512; bit++ {
		if bit != 128 {
			f.Payload[8+bit/8] |= byte(1) << uint(bit%8)
		}
	}
	e.onAck(l, f)
	e.repairLocked(now)
	if len(l.repair) != 1 {
		t.Fatal("repair pipeline stalled behind the first in-flight packet", len(l.repair))
	}
	if f := <-l.repair; f.ID != 129 {
		t.Fatal("wrong missing packet repaired", f.ID)
	}
}

func TestCoalescedAckFlushesEvenAfterTrafficStops(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &Engine{cfg: c, ctx: ctx, links: make(map[string]*link), receive: newReceiver(c), lastAck: time.Now()}
	l := &link{carrier: c.Carriers[0], ack: make(chan session.Frame, 1)}
	e.links[l.carrier.Name] = l
	e.acknowledgeLocked(e.receive)
	if !e.ackPending || len(l.ack) != 0 {
		t.Fatal("ACK was not coalesced")
	}
	done := make(chan struct{})
	go func() { e.repairSession(ctx); close(done) }()
	select {
	case <-l.ack:
	case <-time.After(time.Second):
		t.Error("last ACK stranded without more input")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("repair worker failed to cancel")
	}
}
