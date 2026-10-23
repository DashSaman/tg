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
	ack            chan session.Frame
	repair         chan session.Frame
	ackMu          sync.Mutex
	space          chan struct{}
	enqueueMu      sync.Mutex
	once           sync.Once
	closed         atomic.Bool
	mu             sync.Mutex
	born, lastPong time.Time
	rtt, jitter    time.Duration
	pending        map[uint64]time.Time
	challenge      uint64
	logNext        uint64 // guarded by engine.mu
	logWake        chan struct{}
	logDelay       time.Duration // transmitter-owned next eligibility delay
}

func newLink(e *Engine, ctx context.Context, p config.Carrier, w transport.Wire) *link {
	local, cancel := context.WithCancel(ctx)
	return &link{engine: e, carrier: p, wire: w, ctx: local, cancel: cancel, data: make(chan session.Frame, e.cfg.QueueFrames), repair: make(chan session.Frame, 1024), control: make(chan session.Frame, 8), ack: make(chan session.Frame, 1), space: make(chan struct{}, 1), born: time.Now(), pending: make(map[uint64]time.Time), logNext: e.acked + 1, logWake: make(chan struct{}, 1)}
}
func (l *link) stop() {
	l.once.Do(func() { l.closed.Store(true); l.cancel(); l.notifySpace(); _ = l.wire.Close() })
}
func (l *link) run() {
	var wg sync.WaitGroup
	for _, fn := range []func(){l.transmit, l.receive, l.probe} {
		wg.Add(1)
		go func() { defer wg.Done(); defer l.stop(); fn() }()
	}
	wg.Wait()
}
func (l *link) enqueue(frames []session.Frame) bool {
	return l.enqueueTo(l.data, frames)
}
func (l *link) enqueueTo(queue chan session.Frame, frames []session.Frame) bool {
	l.enqueueMu.Lock()
	defer l.enqueueMu.Unlock()
	if l.closed.Load() || cap(queue)-len(queue) < len(frames) {
		return false
	}
	for _, f := range frames {
		queue <- f
	}
	return true
}

func (l *link) notifySpace() {
	select {
	case l.space <- struct{}{}:
	default:
	}
}

// Backpressure brief application bursts before changing carriers. Wait only on
// this link's queue; engine and application locks must not be held by the caller.
func (l *link) enqueueWait(ctx context.Context, frames []session.Frame, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		if ctx.Err() != nil || l.ctx.Err() != nil {
			return false
		}
		if l.enqueue(frames) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-l.ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-l.space:
		}
	}
}
func (l *link) sendControl(f session.Frame) bool {
	select {
	case l.control <- f:
		return true
	default:
		return false
	}
}

// ACK storage is independent of health probes and bounded to one latest prefix.
func (l *link) sendAck(f session.Frame) {
	l.ackMu.Lock()
	defer l.ackMu.Unlock()
	select {
	case <-l.ack:
	default:
	}
	select {
	case l.ack <- f:
	default:
	}
}
func (l *link) path() session.Path {
	l.mu.Lock()
	defer l.mu.Unlock()
	return session.Path{ID: l.carrier.Name, Ready: !l.closed.Load(), LastPong: l.lastPong, RTT: l.rtt, Jitter: l.jitter, Cost: time.Duration(l.carrier.CostMS) * time.Millisecond, Queued: len(l.data), Capacity: cap(l.data)}
}

func (l *link) queuedFrame() (session.Frame, bool) {
	select {
	case f := <-l.control:
		return f, true
	default:
	}
	select {
	case f := <-l.repair:
		return f, true
	default:
	}
	select {
	case f := <-l.data:
		return f, true
	default:
		return session.Frame{}, false
	}
}
func (l *link) transmit() {
	if l.engine.streamLog() {
		l.transmitLog()
		return
	}
	batchWire, canBatch := l.wire.(transport.BatchWire)
	var batch [transport.MaxBatchFrames][]byte
	var buffers [transport.MaxBatchFrames][session.MaxFrame]byte
	for {
		if l.ctx.Err() != nil {
			return
		}
		f, queued := l.queuedFrame()
		if !queued {
			select {
			case <-l.ctx.Done():
				return
			case f = <-l.control:
			case f = <-l.data:
			case f = <-l.repair:
			case f = <-l.ack:
			}
		}
		b, err := session.EncodeInto(buffers[0][:0], f)
		if err != nil {
			return
		}
		batch[0] = b
		count := 1
		ackIncluded := f.Kind == session.Ack
		size := len(b)
		if canBatch {
			limit := len(batch)
			if l.engine.cfg.Redundant {
				limit--
			}
		drain:
			for count < limit {
				if l.ctx.Err() != nil {
					return
				}
				f, queued = l.queuedFrame()
				if !queued {
					break drain
				}
				b, err = session.EncodeInto(buffers[count][:0], f)
				if err != nil {
					return
				}
				batch[count] = b
				count++
				size += len(b)
			}
			if l.engine.cfg.Redundant && !ackIncluded {
				select {
				case f = <-l.ack:
					b, err = session.EncodeInto(buffers[count][:0], f)
					if err != nil {
						return
					}
					batch[count] = b
					count++
					size += len(b)
				default:
				}
			}
		}
		l.notifySpace()
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
	if receiver, ok := l.wire.(transport.BatchReceiver); ok && l.engine.cfg.StreamPackets && l.carrier.Kind == "https-connect" {
		l.receiveBatches(receiver)
		return
	}
	decode := session.Decode
	if l.engine.cfg.StreamPackets {
		decode = session.DecodePacketStream
	}
	for {
		b, err := l.wire.Receive(l.ctx)
		if err != nil {
			return
		}
		l.engine.counters.wireReceived.Add(uint64(len(b)))
		f, err := decode(b)
		if err != nil {
			l.engine.counters.malformed.Add(1)
			return
		}
		if !l.handleFrame(f) {
			return
		}
	}
}

func (l *link) handleFrame(f session.Frame) bool {
	switch f.Kind {
	case session.Data, session.Packet:
		l.engine.onData(l, f)
	case session.Ack:
		if !l.engine.cfg.Redundant {
			l.engine.counters.malformed.Add(1)
			return false
		}
		l.engine.onAck(l, f)
	case session.Probe:
		if !l.sendControl(session.Frame{Kind: session.Pong, ID: f.ID}) {
			return false
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
			l.engine.notifyAdmission()
			delete(l.pending, f.ID)
		}
		l.mu.Unlock()
	}
	return true
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
