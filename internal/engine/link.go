package engine

import (
	"context"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"github.com/DashSaman/tg/internal/transport"
	"sync"
	"sync/atomic"
	"time"
)

type link struct {
	engine         *Engine
	carrier        config.Carrier
	wire           transport.Wire
	ctx            context.Context
	cancel         context.CancelFunc
	data, control  chan session.Frame
	enqueueMu      sync.Mutex
	once           sync.Once
	closed         atomic.Bool
	mu             sync.Mutex
	born, lastPong time.Time
	rtt, jitter    time.Duration
	pending        map[uint64]time.Time
	challenge      uint64
}

func newLink(e *Engine, ctx context.Context, p config.Carrier, w transport.Wire) *link {
	local, cancel := context.WithCancel(ctx)
	return &link{engine: e, carrier: p, wire: w, ctx: local, cancel: cancel, data: make(chan session.Frame, e.cfg.QueueFrames), control: make(chan session.Frame, 8), born: time.Now(), pending: make(map[uint64]time.Time)}
}
func (l *link) stop() { l.once.Do(func() { l.closed.Store(true); l.cancel(); _ = l.wire.Close() }) }
func (l *link) run() {
	var wg sync.WaitGroup
	for _, fn := range []func(){l.transmit, l.receive, l.probe} {
		wg.Add(1)
		go func() { defer wg.Done(); defer l.stop(); fn() }()
	}
	wg.Wait()
}
func (l *link) enqueue(frames []session.Frame) bool {
	l.enqueueMu.Lock()
	defer l.enqueueMu.Unlock()
	if l.closed.Load() || cap(l.data)-len(l.data) < len(frames) {
		return false
	}
	for _, f := range frames {
		l.data <- f
	}
	return true
}
func (l *link) sendControl(f session.Frame) bool {
	select {
	case l.control <- f:
		return true
	default:
		return false
	}
}
func (l *link) path() session.Path {
	l.mu.Lock()
	defer l.mu.Unlock()
	return session.Path{ID: l.carrier.Name, Ready: !l.closed.Load(), LastPong: l.lastPong, RTT: l.rtt, Jitter: l.jitter, Cost: time.Duration(l.carrier.CostMS) * time.Millisecond, Queued: len(l.data), Capacity: cap(l.data)}
}
func (l *link) transmit() {
	batchWire, canBatch := l.wire.(transport.BatchWire)
	var batch [transport.MaxBatchFrames][]byte
	var buffers [transport.MaxBatchFrames][session.MaxFrame]byte
	for {
		var f session.Frame
		select {
		case <-l.ctx.Done():
			return
		case f = <-l.control:
		default:
			select {
			case <-l.ctx.Done():
				return
			case f = <-l.control:
			case f = <-l.data:
			}
		}
		b, err := session.EncodeInto(buffers[0][:0], f)
		if err != nil {
			return
		}
		batch[0] = b
		count := 1
		size := len(b)
		if canBatch {
		drain:
			for count < len(batch) {
				select {
				case <-l.ctx.Done():
					return
				case f = <-l.control:
				default:
					select {
					case f = <-l.control:
					case f = <-l.data:
					default:
						break drain
					}
				}
				b, err = session.EncodeInto(buffers[count][:0], f)
				if err != nil {
					return
				}
				batch[count] = b
				count++
				size += len(b)
			}
		}
		if canBatch {
			err = batchWire.SendBatch(batch[:count])
		} else {
			err = l.wire.Send(b)
		}
		if err != nil {
			return
		}
		for i := 0; i < count; i++ {
			batch[i] = nil
		}
		l.engine.counters.wireSent.Add(uint64(size))
	}
}
func (l *link) receive() {
	for {
		b, err := l.wire.Receive(l.ctx)
		if err != nil {
			return
		}
		l.engine.counters.wireReceived.Add(uint64(len(b)))
		f, err := session.Decode(b)
		if err != nil {
			l.engine.counters.malformed.Add(1)
			return
		}
		switch f.Kind {
		case session.Data:
			l.engine.onData(l, f)
		case session.Probe:
			if !l.sendControl(session.Frame{Kind: session.Pong, ID: f.ID}) {
				return
			}
		case session.Pong:
			l.mu.Lock()
			if sent, ok := l.pending[f.ID]; ok {
				sample := time.Since(sent)
				if l.lastPong.IsZero() {
					l.rtt = sample
				} else {
					delta := sample - l.rtt
					if delta < 0 {
						delta = -delta
					}
					l.jitter = (l.jitter*7 + delta) / 8
					l.rtt = (l.rtt*7 + sample) / 8
				}
				l.lastPong = time.Now()
				delete(l.pending, f.ID)
			}
			l.mu.Unlock()
		}
	}
}
func (l *link) probe() {
	ticker := time.NewTicker(l.engine.cfg.Interval())
	defer ticker.Stop()
	for {
		now := time.Now()
		l.mu.Lock()
		since := l.lastPong
		if since.IsZero() {
			since = l.born
		}
		if now.Sub(since) > l.engine.cfg.Timeout() {
			l.mu.Unlock()
			return
		}
		for id, sent := range l.pending {
			if now.Sub(sent) >= l.engine.cfg.Timeout() {
				delete(l.pending, id)
			}
		}
		// Bound outstanding probes independently of the configurable timeout ratio.
		if len(l.pending) >= 16 {
			var oldest uint64
			var at time.Time
			for id, t := range l.pending {
				if at.IsZero() || t.Before(at) {
					oldest = id
					at = t
				}
			}
			delete(l.pending, oldest)
		}
		l.challenge++
		id := l.challenge
		l.pending[id] = now
		l.mu.Unlock()
		if !l.sendControl(session.Frame{Kind: session.Probe, ID: id}) {
			return
		}
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
