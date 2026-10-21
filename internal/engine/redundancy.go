package engine

import (
	"context"
	"encoding/binary"
	"github.com/DashSaman/tg/internal/session"
	"time"
)

const retainedCapacity = 8192

type retainedPacket struct {
	packet   []byte
	lastSent time.Time
	received bool
	born     time.Time
}

// Admission, numbering, ACK release and repair share e.mu. Application packets
// stay immutable while retained or queued by any link.
func (e *Engine) admitRedundantLocked(packet []byte, now time.Time) bool {
	if len(packet) == 0 || len(packet) > e.cfg.MaxPacketBytes || len(e.retained) >= retainedCapacity {
		return false
	}
	if e.retained == nil {
		e.retained = make(map[uint64]*retainedPacket, retainedCapacity)
	}
	id := e.sequence.Add(1)
	frames, err := session.Fragment(packet, id)
	if err != nil {
		return false
	}
	p := &retainedPacket{packet: packet, born: now}
	e.retained[id] = p
	if copies := e.mirrorLocked(frames, now); copies > 0 {
		p.lastSent = now
		e.counters.extraCopies.Add(uint64(copies - 1))
	}
	return true
}

// Never wait for one congested replica while another can carry the packet.
func (e *Engine) mirrorLocked(frames []session.Frame, now time.Time) int {
	return e.offerCopiesLocked(frames, now, false)
}
func (e *Engine) offerCopiesLocked(frames []session.Frame, now time.Time, repair bool) int {
	available, admitted := 0, 0
	for _, p := range e.cfg.Carriers {
		l := e.links[p.Name]
		if l == nil || !l.path().Healthy(now, e.cfg.Timeout()) {
			continue
		}
		available++
		queue := l.data
		if repair {
			queue = l.repair
		}
		if l.enqueueTo(queue, frames) {
			admitted++
		}
	}
	e.counters.replicaSkips.Add(uint64(available - admitted))
	return admitted
}

func (e *Engine) onAck(l *link, f session.Frame) {
	if !e.cfg.Redundant || len(f.Payload) != session.AckBytes {
		return
	}
	high := binary.BigEndian.Uint64(f.Payload)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.links[l.carrier.Name] != l || high < f.ID || high > e.sequence.Load() || f.ID < e.acked {
		return
	}
	for id := e.acked + 1; id <= f.ID; id++ {
		delete(e.retained, id)
	}
	e.acked = f.ID
	e.ackHighest = max(e.ackHighest, high)
	for bit := 0; bit < session.AckBits; bit++ {
		if f.Payload[8+bit/8]&(byte(1)<<uint(bit%8)) == 0 {
			continue
		}
		id := f.ID + uint64(bit) + 1
		if id > high || id <= f.ID {
			continue
		}
		if p := e.retained[id]; p != nil {
			p.received = true
		}
	}
	if high > f.ID {
		e.wakeRepair()
	}
}

func (e *Engine) wakeRepair() {
	select {
	case e.repairWake <- struct{}{}:
	default:
	}
}

func (e *Engine) repairLocked(now time.Time) {
	rto := time.Second
	for _, l := range e.links {
		p := l.path()
		if p.Healthy(now, e.cfg.Timeout()) {
			rto = min(rto, max(20*time.Millisecond, 4*p.RTT+4*p.Jitter))
		}
	}
	var storage [session.MaxPacket / session.Chunk]session.Frame
	sent := 0
	// A stalled cumulative prefix does not imply the entire later window is
	// missing. Replaying beyond the selectively reported range amplifies congestion while
	// the first repair is still in flight, delaying its ACK even further.
	last := min(e.sequence.Load(), e.acked+session.AckBits)
	for id := e.acked + 1; id <= last && sent < 128; id++ {
		p := e.retained[id]
		if p == nil || p.received {
			continue
		}
		delay := rto
		if id <= e.ackHighest {
			delay = 5 * time.Millisecond
		}
		if !p.lastSent.IsZero() && now.Sub(p.lastSent) < delay {
			continue
		}
		frames, _ := session.FragmentInto(storage[:0], p.packet, id)
		copies := e.offerCopiesLocked(frames, now, true)
		if copies == 0 {
			break
		}
		p.lastSent = now
		sent++
		e.counters.replayed.Add(1)
		if copies > 1 {
			e.counters.extraCopies.Add(uint64(copies - 1))
		}
	}
}

func (e *Engine) repairSession(ctx context.Context) {
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-e.repairWake:
		}
		e.mu.Lock()
		if e.ackPending {
			e.acknowledgeLocked(e.receive)
		}
		e.repairLocked(time.Now())
		e.mu.Unlock()
	}
}

// Completion notifications are coalesced to at most one bitmap per millisecond.
// The repair tick flushes the final pending ACK even after input traffic stops.
// ACK denotes reassembly ownership, not successful application/kernel delivery.
func (e *Engine) acknowledgeLocked(r *session.Reassembler) {
	if !e.cfg.Redundant || e.receive != r {
		return
	}
	e.ackPending = true
	now := time.Now()
	if now.Sub(e.lastAck) < time.Millisecond {
		return
	}
	e.lastAck = now
	e.ackPending = false
	prefix, high := r.AckState()
	payload := make([]byte, session.AckBytes)
	binary.BigEndian.PutUint64(payload, high)
	for bit := 0; bit < session.AckBits && uint64(bit)+1 <= high-prefix; bit++ {
		if r.Completed(prefix + uint64(bit) + 1) {
			payload[8+bit/8] |= byte(1) << uint(bit%8)
		}
	}
	f := session.Frame{Kind: session.Ack, ID: prefix, Payload: payload}
	for _, l := range e.links {
		l.sendAck(f)
	}
}
