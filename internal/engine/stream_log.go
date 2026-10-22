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
		e.retained = make(map[uint64]*retainedPacket, retainedCapacity)
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
func (l *link) fillLog(batch *[transport.MaxBatchFrames][]byte, buffers *[transport.MaxBatchFrames][session.MaxFrame]byte, count int) (int, int) {
	e := l.engine
	e.mu.Lock()
	defer e.mu.Unlock()
	l.logNext = max(l.logNext, e.acked+1)
	size := 0
	for l.logNext <= e.sequence.Load() {
		p := e.retained[l.logNext]
		if p == nil || p.received {
			l.logNext++
			continue
		}
		needed := (len(p.packet) + session.Chunk - 1) / session.Chunk
		if len(batch)-count < needed {
			break
		}
		var storage [session.MaxPacket / session.Chunk]session.Frame
		frames, err := session.FragmentInto(storage[:0], p.packet, l.logNext)
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

func (l *link) transmitLog() {
	var batch [transport.MaxBatchFrames][]byte
	var buffers [transport.MaxBatchFrames][session.MaxFrame]byte
	batchWire, canBatch := l.wire.(transport.BatchWire)
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
		n, dataBytes := l.fillLog(&batch, &buffers, count)
		count = n
		size += dataBytes
		if count == 0 {
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
