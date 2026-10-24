package engine

import (
	"github.com/DashSaman/tg/internal/session"
	"github.com/DashSaman/tg/internal/transport"
	"time"
)

// Only negotiated complete packets take this path. Keep control frames in
// arrival order and process the valid prefix before a malformed frame/error.
func (l *link) receiveBatches(receiver transport.BatchReceiver) {
	var wireFrames [transport.MaxReceiveFrames][]byte
	var packets [transport.MaxReceiveFrames]session.Frame
	for l.ctx.Err() == nil && l.engine.ctx.Err() == nil {
		n, readErr := receiver.ReceiveBatch(l.ctx, wireFrames[:])
		if n < 0 || n > len(wireFrames) || (n == 0 && readErr == nil) {
			return
		}
		count := 0
		flush := func() {
			if count > 0 {
				l.engine.onPacketBatch(l, packets[:count])
				clear(packets[:count])
				count = 0
			}
		}
		for _, b := range wireFrames[:n] {
			l.engine.counters.wireReceived.Add(uint64(len(b)))
			f, err := session.DecodePacketStream(b)
			if err != nil {
				flush()
				l.engine.counters.malformed.Add(1)
				return
			}
			if f.Kind == session.Packet {
				packets[count] = f
				count++
			} else {
				flush()
				if !l.handleFrame(f) {
					return
				}
			}
		}
		flush()
		clear(wireFrames[:n])
		if readErr != nil {
			return
		}
	}
}

// Retain owned packet copies under one receive/engine lock per ready group.
// Release e.mu before publishing to a potentially full UDP output queue. A
// maximum32 packets and the existing64KiB transport buffer bound each group.
func (e *Engine) onPacketBatch(l *link, frames []session.Frame) {
	e.deliveryMu.Lock()
	defer e.deliveryMu.Unlock()
	if e.ctx.Err() != nil {
		return
	}
	var pending [transport.MaxReceiveFrames]delivery
	count := 0
	ack := false
	e.mu.Lock()
	if e.links[l.carrier.Name] != l {
		e.mu.Unlock()
		return
	}
	r := e.receive
	now := time.Now()
	for _, f := range frames {
		if int(f.Total) > e.cfg.MaxPacketBytes {
			e.counters.dropped.Add(1)
			continue
		}
		packet, complete, err := r.Push(now, f)
		if err != nil {
			e.counters.dropped.Add(1)
			continue
		}
		if complete {
			pending[count] = delivery{id: f.ID, packet: packet, owner: r}
			count++
			ack = true
		} else if r.Completed(f.ID) {
			ack = true
		}
	}
	e.mu.Unlock()
	for i := range count {
		select {
		case e.output <- pending[i]:
		case <-e.ctx.Done():
			for _, d := range pending[i:count] {
				d.owner.Release(d.packet)
			}
			return
		}
	}
	if ack {
		e.mu.Lock()
		e.acknowledgeLocked(r)
		e.mu.Unlock()
	}
}
