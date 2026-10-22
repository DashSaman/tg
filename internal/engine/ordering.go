package engine

import (
	"context"
	"github.com/DashSaman/tg/internal/session"
)

const reorderCapacity = 16384

// One output worker owns this bounded reorder window. Waiting for a missing copy
// never holds engine/transport locks. Missing admitted packets are repaired from
// the sender's smaller retained window; no timer silently skips their sequence.
func (e *Engine) writeOrderedApplication(ctx context.Context) {
	waiting := make(map[uint64]delivery, 256)
	var owner *session.Reassembler
	next := uint64(1)
	releaseWaiting := func() {
		for id, d := range waiting {
			d.owner.Release(d.packet)
			delete(waiting, id)
		}
	}
	defer releaseWaiting()
	stage := func(d delivery) {
		if owner != d.owner {
			releaseWaiting()
			owner = d.owner
			next = 1
		}
		if d.id < next {
			d.owner.Release(d.packet)
			e.counters.reorderLate.Add(1)
			return
		}
		if _, exists := waiting[d.id]; exists {
			d.owner.Release(d.packet)
			return
		}
		if len(waiting) >= reorderCapacity && d.id != next {
			d.owner.Release(d.packet)
			e.counters.dropped.Add(1)
			return
		}
		if _, ready := waiting[next]; !ready && d.id != next {
			// A gap can retain many small datagrams. Keep exact-size owned copies,
			// returning the larger reassembly buffer immediately to its bounded cache.
			packet := append([]byte(nil), d.packet...)
			d.owner.Release(d.packet)
			d.packet = packet
			e.counters.reorderAhead.Add(1)
		}
		waiting[d.id] = d
		if uint64(len(waiting)) > e.counters.reorderPeak.Load() {
			e.counters.reorderPeak.Store(uint64(len(waiting)))
		}
	}
	var batch [32]delivery
	var packets [32][]byte
	for ctx.Err() == nil {
	drain:
		for i := 0; i < len(batch); i++ {
			select {
			case d := <-e.output:
				stage(d)
			default:
				break drain
			}
		}
		count := 0
		for count < len(batch) {
			d, ok := waiting[next]
			if !ok {
				break
			}
			delete(waiting, next)
			next++
			batch[count] = d
			count++
		}
		if count > 0 {
			e.flushDeliveries(ctx, batch[:count], packets[:count])
			continue
		}
		select {
		case <-ctx.Done():
			return
		case d := <-e.output:
			stage(d)
		}
	}
}
