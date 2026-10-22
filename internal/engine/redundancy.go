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
	repairs  uint8
	copies   uint32
}

// Admission, numbering, ACK release and repair share e.mu. Application packets
// stay immutable while retained or queued by any link.
func (e *Engine) admitRedundantLocked(packet []byte, now time.Time) bool {
	if len(packet) == 0 || len(packet) > e.cfg.MaxPacketBytes || len(e.retained) >= retainedCapacity {
		return false
	}
	if e.streamLog() {
		return e.admitLogLocked(packet, now)
	}
	id := e.sequence.Load() + 1
	var storage [session.MaxPacket / session.Chunk]session.Frame
	frames, err := session.FragmentInto(storage[:0], packet, id)
	if err != nil {
		return false
	}
	copies := e.mirrorLocked(frames, now)
	if copies == 0 {
		return false
	}
	if e.retained == nil {
		e.retained = make(map[uint64]*retainedPacket, retainedCapacity)
	}
	e.sequence.Store(id)
	e.retained[id] = &retainedPacket{packet: packet, born: now, lastSent: now}
	e.counters.extraCopies.Add(uint64(copies - 1))
	return true
}

// Called and returned with e.mu held. Backpressure the UDP reader before it
// creates a train of unsent IDs when every ordinary carrier queue is full.
// ACK/repair and carrier workers keep running while the bounded wait unlocks mu.
func (e *Engine) admitRedundantWaitLocked(ctx context.Context, packet []byte) bool {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for ctx.Err() == nil {
		if e.admitRedundantLocked(packet, time.Now()) {
			return true
		}
		var spaces [4]<-chan struct{}
		available := 0
		for _, carrier := range e.cfg.Carriers {
			if l := e.links[carrier.Name]; l != nil && !l.closed.Load() {
				spaces[available] = l.space
				available++
			}
		}
		if timer == nil {
			timer = time.NewTimer(50 * time.Millisecond)
		}
		e.mu.Unlock()
		ready := true
		select {
		case <-ctx.Done():
			ready = false
		case <-timer.C:
			ready = false
		case <-e.admissionSpace:
		case <-spaces[0]:
		case <-spaces[1]:
		case <-spaces[2]:
		case <-spaces[3]:
		}
		e.mu.Lock()
		if !ready {
			return false
		}
	}
	return false
}

func (e *Engine) notifyAdmission() {
	select {
	case e.admissionSpace <- struct{}{}:
	default:
	}
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
	// Probe priority can hide ordinary data-queue delay. Measure actual session
	// feedback too, excluding ambiguous retransmitted samples (Karn's rule).
	if p := e.retained[f.ID]; p != nil && p.repairs == 0 && !p.born.IsZero() {
		sample := time.Since(p.born)
		if e.sessionRTT == 0 {
			e.sessionRTT = sample
		} else {
			delta := sample - e.sessionRTT
			if delta < 0 {
				delta = -delta
			}
			e.sessionJitter = (7*e.sessionJitter + delta) / 8
			e.sessionRTT = (7*e.sessionRTT + sample) / 8
		}
	}
	for id := e.acked + 1; id <= f.ID; id++ {
		delete(e.retained, id)
	}
	e.acked = f.ID
	e.notifyAdmission()
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
	rto = min(time.Second, max(rto, 4*e.sessionRTT+4*e.sessionJitter))
	var storage [session.MaxPacket / session.Chunk]session.Frame
	sent := 0
	// A stalled cumulative prefix does not imply the entire later window is
	// missing. Replaying beyond the selectively reported range amplifies congestion while
	// the first repair is still in flight, delaying its ACK even further.
	last := min(e.sequence.Load(), e.acked+session.AckBits)
	fastLast := e.acked
	for id := e.acked + 1; id <= min(e.ackHighest, e.acked+128); id++ {
		p := e.retained[id]
		if p == nil || p.received {
			break
		}
		fastLast = id
	}
	for id := e.acked + 1; id <= last && sent < 128; id++ {
		p := e.retained[id]
		if p == nil || p.received {
			continue
		}
		delay := rto
		if id <= fastLast && p.repairs == 0 {
			delay = 5 * time.Millisecond
		}
		if p.repairs > 0 {
			delay = min(time.Second, rto<<min(p.repairs-1, 5))
		}
		if !p.lastSent.IsZero() && now.Sub(p.lastSent) < delay {
			continue
		}
		frames, _ := session.FragmentInto(storage[:0], p.packet, id)
		first := p.lastSent.IsZero()
		// Deferred first admissions must stay behind older ordinary data. Putting
		// them on the repair priority queue manufactures receive gaps, then the
		// resulting repair traffic starves the very originals needed to fill them.
		copies := e.offerCopiesLocked(frames, now, !first)
		if copies == 0 {
			break
		}
		p.lastSent = now
		e.notifyAdmission()
		if !first {
			p.repairs = min(p.repairs+1, 32)
		}
		sent++
		if !first {
			e.counters.replayed.Add(1)
		} else {
			e.counters.deferred.Add(1)
		}
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
		if !e.streamLog() {
			e.repairLocked(time.Now())
		}
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
