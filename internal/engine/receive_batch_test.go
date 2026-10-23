package engine

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
)

type borrowedBatchWire struct {
	frames                  [][]byte
	arena                   []byte
	batchCalls, singleCalls int
	e                       *Engine
	tailErr                 error
}

func (w *borrowedBatchWire) Send([]byte) error { return nil }
func (w *borrowedBatchWire) Close() error      { return nil }
func (w *borrowedBatchWire) Receive(context.Context) ([]byte, error) {
	w.singleCalls++
	return nil, context.Canceled
}
func (w *borrowedBatchWire) ReceiveBatch(ctx context.Context, dst [][]byte) (int, error) {
	w.batchCalls++
	if w.e != nil {
		if !w.e.mu.TryLock() {
			panic("engine lock held during carrier read")
		}
		w.e.mu.Unlock()
	}
	clear(w.arena)
	if len(w.frames) == 0 {
		return 0, context.Canceled
	}
	n, at := min(len(dst), len(w.frames)), 0
	for i, frame := range w.frames[:n] {
		copy(w.arena[at:], frame)
		dst[i] = w.arena[at : at+len(frame) : at+len(frame)]
		at += len(frame)
	}
	w.frames = w.frames[n:]
	if len(w.frames) == 0 {
		return n, w.tailErr
	}
	return n, nil
}

func batchPacket(t *testing.T, kind byte, id uint64, value byte) []byte {
	t.Helper()
	f := session.Frame{Kind: kind, ID: id}
	if kind == session.Packet || kind == session.Data {
		f.Count = 1
		f.Total = 16
		f.Payload = bytes.Repeat([]byte{value}, 16)
	}
	b, err := session.Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func batchEngine(t *testing.T, w *borrowedBatchWire) (*Engine, *link) {
	t.Helper()
	e, old := packetReceiveFixture(true, &overwritePacketWire{})
	old.cancel()
	p := old.carrier
	p.Kind = "https-connect"
	l := newLink(e, e.ctx, p, w)
	e.links[l.carrier.Name] = l
	w.e = e
	return e, l
}

func TestBatchPacketReceiveOwnsBytesDedupesAndPreservesControlOrder(t *testing.T) {
	w := &borrowedBatchWire{arena: make([]byte, 65536)}
	for _, f := range []struct {
		kind  byte
		id    uint64
		value byte
	}{
		{session.Packet, 1, 7}, {session.Packet, 2, 8}, {session.Packet, 1, 7},
		{session.Probe, 9, 0}, {session.Packet, 3, 9}, {session.Data, 4, 10}, {session.Packet, 5, 11},
	} {
		w.frames = append(w.frames, batchPacket(t, f.kind, f.id, f.value))
	}
	e, l := batchEngine(t, w)
	defer l.cancel()
	l.receive()
	clear(w.arena)
	if w.batchCalls != 1 || w.singleCalls != 0 {
		t.Fatalf("batch worker not used: batch=%d single=%d", w.batchCalls, w.singleCalls)
	}
	if len(e.output) != 3 || e.counters.malformed.Load() != 1 {
		t.Fatal("batch accepted malformed mode or lost valid prefix", len(e.output), e.counters.malformed.Load())
	}
	for i := range 3 {
		d := <-e.output
		if d.id != uint64(i+1) || !bytes.Equal(d.packet, bytes.Repeat([]byte{byte(i + 7)}, 16)) {
			t.Fatal("delivery aliases borrowed carrier or changes order", d.id)
		}
		d.owner.Release(d.packet)
	}
	if prefix, high := e.receive.AckState(); prefix != 3 || high != 3 {
		t.Fatal("invalid tail mutated ACK", prefix, high)
	}
	select {
	case f := <-l.control:
		if f.Kind != session.Pong || f.ID != 9 {
			t.Fatal(f)
		}
	default:
		t.Fatal("probe response missing")
	}
}

func TestBatchPacketReceiveCancellationUnblocksOutputWithoutEngineLock(t *testing.T) {
	w := &borrowedBatchWire{arena: make([]byte, 65536)}
	for i := range 32 {
		w.frames = append(w.frames, batchPacket(t, session.Packet, uint64(i+1), byte(i)))
	}
	e, l := batchEngine(t, w)
	defer l.cancel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.ctx = ctx
	e.output = make(chan delivery, 1)
	done := make(chan struct{})
	go func() { defer close(done); l.receive() }()
	deadline := time.Now().Add(time.Second)
	for len(e.output) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(e.output) != 1 {
		t.Fatal("no first batch delivery")
	}
	if !e.mu.TryLock() {
		t.Fatal("engine lock held while output is blocked")
	}
	e.mu.Unlock()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled batch retained blocked output")
	}
	d := <-e.output
	d.owner.Release(d.packet)
}

func TestConcurrentBatchCarriersDeliverUniquePacketsOnce(t *testing.T) {
	w := &borrowedBatchWire{arena: make([]byte, 65536)}
	for i := range 32 {
		w.frames = append(w.frames, batchPacket(t, session.Packet, uint64(i+1), byte(i)))
	}
	w2 := &borrowedBatchWire{arena: make([]byte, 65536), frames: append([][]byte(nil), w.frames...)}
	e, l := batchEngine(t, w)
	defer l.cancel()
	e.output = make(chan delivery, 64)
	p := l.carrier
	p.Name = "second"
	l2 := newLink(e, e.ctx, p, w2)
	defer l2.cancel()
	e.links[p.Name] = l2
	// Another carrier may legitimately hold e.mu during this worker's read.
	w.e = nil
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); l.receive() }()
	go func() { defer wg.Done(); l2.receive() }()
	wg.Wait()
	if len(e.output) != 32 {
		t.Fatal("duplicates or missing packets", len(e.output))
	}
	for i := range 32 {
		d := <-e.output
		if d.id != uint64(i+1) {
			t.Fatal(d.id)
		}
		d.owner.Release(d.packet)
	}
}

func TestBatchPacketReceiveProcessesValidPrefixWithReadError(t *testing.T) {
	w := &borrowedBatchWire{arena: make([]byte, 65536), tailErr: io.ErrUnexpectedEOF}
	w.frames = [][]byte{batchPacket(t, session.Packet, 1, 71), batchPacket(t, session.Packet, 2, 72)}
	e, l := batchEngine(t, w)
	defer l.cancel()
	l.receive()
	clear(w.arena)
	if w.batchCalls != 1 || len(e.output) != 2 {
		t.Fatal("valid prefix lost or read continued after error", w.batchCalls, len(e.output))
	}
	for i := range 2 {
		d := <-e.output
		if d.id != uint64(i+1) || !bytes.Equal(d.packet, bytes.Repeat([]byte{byte(i + 71)}, 16)) {
			t.Fatal("valid prefix corrupted")
		}
		d.owner.Release(d.packet)
	}
}

func TestExistingHTTPSCarrierKeepsOriginalSingleReceivePath(t *testing.T) {
	w := &borrowedBatchWire{arena: make([]byte, 65536)}
	e, l := batchEngine(t, w)
	defer l.cancel()
	l.carrier.Kind = "https"
	l.receive()
	if w.batchCalls != 0 || w.singleCalls != 1 || len(e.output) != 0 {
		t.Fatal("unproven batching enabled on existing HTTPS carrier")
	}
}

func TestBatchPacketReceiveResetWhileOutputBlockedKeepsOldOwnerAndAckIsolated(t *testing.T) {
	w := &borrowedBatchWire{arena: make([]byte, 65536), tailErr: io.EOF}
	w.frames = [][]byte{batchPacket(t, session.Packet, 1, 7), batchPacket(t, session.Packet, 2, 8)}
	e, l := batchEngine(t, w)
	defer l.cancel()
	e.output = make(chan delivery, 1)
	done := make(chan struct{})
	go func() { defer close(done); l.receive() }()
	deadline := time.Now().Add(time.Second)
	for len(e.output) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(e.output) != 1 {
		t.Fatal("batch did not reach full output")
	}
	e.mu.Lock()
	old := e.receive
	e.receive = newReceiver(e.cfg)
	e.lastAck = time.Time{}
	e.ackPending = false
	e.mu.Unlock()
	for i := range 2 {
		select {
		case d := <-e.output:
			if d.owner != old || d.owner == e.receive || d.id != uint64(i+1) || !bytes.Equal(d.packet, bytes.Repeat([]byte{byte(i + 7)}, 16)) {
				t.Fatal("reset invalidated queued ownership")
			}
			d.owner.Release(d.packet)
		case <-time.After(time.Second):
			t.Fatal("reset stalled publication")
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("batch worker stuck after reset")
	}
	if prefix, high := e.receive.AckState(); prefix != 0 || high != 0 {
		t.Fatal("old batch mutated replacement receiver", prefix, high)
	}
	if e.ackPending || len(l.ack) != 0 {
		t.Fatal("stale receiver sent ACK into new epoch")
	}
}

// This isolates receive ownership/ACK/publication costs, excluding encryption,
// networking and kernel WireGuard. End-to-end receipts remain authoritative.
func BenchmarkPacketReceiveGroup(b *testing.B) {
	for _, batched := range []bool{false, true} {
		name := "single"
		if batched {
			name = "batch32"
		}
		b.Run(name, func(b *testing.B) {
			c := config.Defaults()
			c.Redundant = true
			c.StreamPackets = true
			c.MaxPacketBytes = 2048
			e := &Engine{cfg: c, ctx: context.Background(), links: make(map[string]*link), receive: newReceiver(c), output: make(chan delivery, 32)}
			l := newLink(e, e.ctx, c.Carriers[0], nil)
			defer l.cancel()
			e.links[l.carrier.Name] = l
			var frames [32]session.Frame
			payload := bytes.Repeat([]byte{17}, 1440)
			for i := range frames {
				frames[i] = session.Frame{Kind: session.Packet, Count: 1, Total: 1440, Payload: payload}
			}
			b.ReportAllocs()
			b.SetBytes(32 * 1440)
			b.ResetTimer()
			for iteration := range b.N {
				for i := range frames {
					frames[i].ID = uint64(iteration)*32 + uint64(i) + 1
				}
				if batched {
					e.onPacketBatch(l, frames[:])
				} else {
					for _, f := range frames {
						e.onData(l, f)
					}
				}
				for range frames {
					d := <-e.output
					d.owner.Release(d.packet)
				}
			}
		})
	}
}
