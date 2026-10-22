package engine

import (
	"time"

	"github.com/DashSaman/tg/internal/session"
	"github.com/DashSaman/tg/internal/transport"
)

// Reliable carriers own retransmission within a connection. A shared bounded
// packet log owns replay across connections. Each carrier walks this log in
// sequence: an independently full queue can never silently omit an old packet
// while admitting its successors. Datagram carriers use the experimental ARQ
// backend because they cannot provide this per-connection delivery contract.
func (e *Engine) streamLog() bool {
	if !e.cfg.Redundant {
		return false
	}
	for _, c := range e.cfg.Carriers {
		if c.Kind != "quic-stream" && c.Kind != "https" {
			return false
		}
	}
	return true
}

func (e *Engine) admitLogLocked(packet []byte, now time.Time) bool {
	healthy := false
	for _, l := range e.links {
		if l.path().Healthy(now, e.cfg.Timeout()) {
			healthy = true
			break
		}
	}
	if !healthy {
		return false
	}
	if e.retained == nil {
		e.retained = make(map[uint64]*retainedPacket, min(retainedCapacity, e.retainedLimit()))
	}
	id := e.sequence.Add(1)
	e.retained[id] = &retainedPacket{packet: packet, born: now, lastSent: now}
	for _, l := range e.links {
		select {
		case l.logWake <- struct{}{}:
		default:
		}
	}
	return true
}

// Encode under engine.mu into transmitter-owned storage, then release the lock
// BEFORE native I/O. ACK release cannot invalidate encoded frames. Whole packets
// fit within one batch (at most 8 frames), including on a non-batching test wire.
func (l *link) fillLog(batch *[transport.MaxBatchFrames][]byte, buffers *[transport.MaxBatchFrames][]byte, count int) (int, int) {
	e := l.engine
	e.mu.Lock()
	defer e.mu.Unlock()
	l.logNext = max(l.logNext, e.acked+1)
	l.logDelay = 0
	// Fixed operator priority avoids RTT-driven leader oscillation. The grace
	// bounds failover even when a primary blackhole has not timed out yet.
	grace := time.Duration(0)
	if e.cfg.ReplicaDelayMS > 0 {
		for _, c := range e.cfg.Carriers {
			if c.CostMS > l.carrier.CostMS || (c.CostMS == l.carrier.CostMS && c.Name >= l.carrier.Name) {
				continue
			}
			other := e.links[c.Name]
			if other != nil && other.path().Healthy(time.Now(), e.cfg.Timeout()) {
				grace = time.Duration(e.cfg.ReplicaDelayMS) * time.Millisecond
				break
			}
		}
	}
	size := 0
	for l.logNext <= e.sequence.Load() {
		p := e.retained[l.logNext]
		if p == nil || p.received {
			l.logNext++
			continue
		}
		if remaining := grace - time.Since(p.born); remaining > 0 {
			l.logDelay = remaining
			break
		}
		// A frame count alone would make packet-mode writes up to eight times
		// larger than legacy writes. Bound bytes too, so control/ACK processing
		// can interleave with bulk without waiting for a multi-MiB batch.
		if e.cfg.StreamPackets && size+session.Header+len(p.packet) > 256<<10 {
			break
		}
		needed := (len(p.packet) + session.Chunk - 1) / session.Chunk
		if e.cfg.StreamPackets {
			needed = 1
		}
		if len(batch)-count < needed {
			break
		}
		var storage [session.MaxPacket / session.Chunk]session.Frame
		var frames []session.Frame
		var err error
		if e.cfg.StreamPackets {
			storage[0] = session.Frame{Kind: session.Packet, ID: l.logNext, Count: 1, Total: uint16(len(p.packet)), Payload: p.packet}
			frames = storage[:1]
		} else {
			frames, err = session.FragmentInto(storage[:0], p.packet, l.logNext)
		}
		if err != nil {
			panic("invalid retained packet")
		}
		for _, f := range frames {
			b, err := session.EncodeInto(buffers[count][:0], f)
			if err != nil {
				panic("invalid retained frame")
			}
			batch[count] = b
			count++
			size += len(b)
		}
		if p.copies > 0 {
			e.counters.extraCopies.Add(1)
		}
		p.copies++
		l.logNext++
	}
	return count, size
}

// Every row has a full slice bound, so no encoder can consume a neighbouring
// frame's storage. Only opted-in packet mode allocates the larger arena.
func logBuffers(packets bool) *[transport.MaxBatchFrames][]byte {
	size := session.MaxFrame
	if packets {
		size = session.MaxStreamFrame
	}
	arena := make([]byte, transport.MaxBatchFrames*size)
	rows := new([transport.MaxBatchFrames][]byte)
	for i := range rows {
		begin, end := i*size, (i+1)*size
		rows[i] = arena[begin:end:end]
	}
	return rows
}

func (l *link) transmitLog() {
	var batch [transport.MaxBatchFrames][]byte
	buffers := logBuffers(l.engine.cfg.StreamPackets)
	batchWire, canBatch := l.wire.(transport.BatchWire)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for l.ctx.Err() == nil {
		count, size := 0, 0
		add := func(f session.Frame) bool {
			b, err := session.EncodeInto(buffers[count][:0], f)
			if err != nil {
				return false
			}
			batch[count] = b
			count++
			size += len(b)
			return true
		}
		// Health and one coalesced ACK cannot be starved by bulk data. Control
		// traffic is itself bounded, leaving room for one maximum-size packet.
		for i := 0; i < 2; i++ {
			select {
			case f := <-l.control:
				if !add(f) {
					return
				}
			default:
			}
		}
		select {
		case f := <-l.ack:
			if !add(f) {
				return
			}
		default:
		}
		n, dataBytes := l.fillLog(&batch, buffers, count)
		count = n
		size += dataBytes
		if count == 0 {
			if l.logDelay > 0 {
				// New admissions cannot interrupt the age grace on its oldest
				// packet. Otherwise an idle backup busy-spins at ingress rate.
				timer.Reset(l.logDelay)
				select {
				case <-l.ctx.Done():
					return
				case <-timer.C:
					continue
				case f := <-l.control:
					if !add(f) {
						return
					}
				case f := <-l.ack:
					if !add(f) {
						return
					}
				}
				timer.Stop()
			} else {
				select {
				case <-l.ctx.Done():
					return
				case <-l.logWake:
					continue
				case f := <-l.control:
					if !add(f) {
						return
					}
				case f := <-l.ack:
					if !add(f) {
						return
					}
				}
			}
		}
		if canBatch {
			if err := batchWire.SendBatch(batch[:count]); err != nil {
				return
			}
		} else {
			for _, b := range batch[:count] {
				if err := l.wire.Send(b); err != nil {
					return
				}
			}
		}
		l.engine.counters.wireSent.Add(uint64(size))
		for i := 0; i < count; i++ {
			batch[i] = nil
		}
	}
}
