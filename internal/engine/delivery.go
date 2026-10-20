package engine

import (
	"context"
	"github.com/DashSaman/tg/internal/session"
)

type delivery struct {
	packet []byte
	owner  *session.Reassembler
}

// One writer owns completed buffers until the UDP syscall has consumed them.
// No batching delay: drain only packets already available, at most 32 at once.
// Receive queues can keep draining while a previous batch enters WireGuard.
func (e *Engine) writeApplication(ctx context.Context) {
	var pending [32]delivery
	var packets [32][]byte
	for {
		select {
		case <-ctx.Done():
			return
		case pending[0] = <-e.output:
		}
		count := 1
	drain:
		for count < len(pending) {
			select {
			case pending[count] = <-e.output:
				count++
			default:
				break drain
			}
		}
		for i := 0; i < count; i++ {
			packets[i] = pending[i].packet
		}
		sent, err := e.app.writeBatch(packets[:count], e.cfg.Timeout())
		for i := 0; i < count; i++ {
			if i < sent {
				e.counters.outputBytes.Add(uint64(len(packets[i])))
			}
			pending[i].owner.Release(packets[i])
			pending[i] = delivery{}
			packets[i] = nil
		}
		if err != nil {
			e.counters.appErrors.Add(uint64(count - sent))
		}
	}
}
