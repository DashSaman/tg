package engine

import (
	"bytes"
	"context"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"testing"
	"time"
)

type overwritePacketWire struct {
	frames  [][]byte
	scratch []byte
	next    int
}

func (w *overwritePacketWire) Send([]byte) error { return nil }
func (w *overwritePacketWire) Close() error      { return nil }
func (w *overwritePacketWire) Receive(context.Context) ([]byte, error) {
	clear(w.scratch)
	if w.next == len(w.frames) {
		return nil, context.Canceled
	}
	b := w.frames[w.next]
	w.next++
	copy(w.scratch, b)
	return w.scratch[:len(b)], nil
}
func packetReceiveFixture(mode bool, w *overwritePacketWire) (*Engine, *link) {
	c := config.Defaults()
	c.Redundant = true
	c.StreamPackets = mode
	c.MaxPacketBytes = session.MaxPacket
	e := &Engine{cfg: c, ctx: context.Background(), links: make(map[string]*link), receive: session.NewRedundantReassembler(8, time.Second), output: make(chan delivery, 8)}
	l := newLink(e, context.Background(), c.Carriers[0], w)
	e.links[l.carrier.Name] = l
	return e, l
}
func TestQueuedPacketReceiveSurvivesNextWireRead(t *testing.T) {
	w := &overwritePacketWire{scratch: make([]byte, session.MaxStreamFrame)}
	for i, value := range []byte{7, 91} {
		b, err := session.Encode(session.Frame{Kind: session.Packet, ID: uint64(i + 1), Count: 1, Total: 7200, Payload: bytes.Repeat([]byte{value}, 7200)})
		if err != nil {
			t.Fatal(err)
		}
		w.frames = append(w.frames, b)
	}
	e, l := packetReceiveFixture(true, w)
	defer l.cancel()
	l.receive()
	if len(e.output) != 2 {
		t.Fatal("packet receive lost queued delivery")
	}
	for _, value := range []byte{7, 91} {
		d := <-e.output
		if !bytes.Equal(d.packet, bytes.Repeat([]byte{value}, 7200)) {
			t.Fatal("queued output aliases overwritten carrier buffer")
		}
		d.owner.Release(d.packet)
	}
	if prefix, high := e.receive.AckState(); prefix != 2 || high != 2 {
		t.Fatal(prefix, high)
	}
}
func TestMismatchedPacketKindClosesLinkBeforeAckOrDedupeMutation(t *testing.T) {
	for _, mode := range []bool{false, true} {
		kind := session.Packet
		if mode {
			kind = session.Data
		}
		b, err := session.Encode(session.Frame{Kind: kind, ID: 1, Count: 1, Total: 16, Payload: make([]byte, 16)})
		if err != nil {
			t.Fatal(err)
		}
		w := &overwritePacketWire{frames: [][]byte{b}, scratch: make([]byte, session.MaxStreamFrame)}
		e, l := packetReceiveFixture(mode, w)
		l.receive()
		l.cancel()
		if e.counters.malformed.Load() != 1 || len(e.output) != 0 {
			t.Fatal("mismatched frame entered receive state")
		}
		if prefix, high := e.receive.AckState(); prefix != 0 || high != 0 {
			t.Fatal("mismatched frame advanced ACK", prefix, high)
		}
	}
}
