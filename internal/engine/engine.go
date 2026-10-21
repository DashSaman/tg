// Package engine keeps the application socket independent of carrier lifetimes.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"github.com/DashSaman/tg/internal/transport"
)

type Engine struct {
	cfg               config.Config
	epoch             uint64
	mu                sync.Mutex
	deliveryMu        sync.Mutex
	started, closing  bool
	ctx               context.Context
	links             map[string]*link
	addresses         map[string]string
	app               *application
	peerEpoch         uint64
	receive           *session.Reassembler
	selector          session.Selector
	active            string
	workers           sync.WaitGroup
	sequence          atomic.Uint64
	counters          counters
	output            chan delivery
	retained          map[uint64]*retainedPacket
	acked, ackHighest uint64
	repairWake        chan struct{}
	lastAck           time.Time
	ackPending        bool
}

func New(c config.Config) (*Engine, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var b [8]byte
	for binary.BigEndian.Uint64(b[:]) == 0 {
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
	}
	return &Engine{cfg: c, epoch: binary.BigEndian.Uint64(b[:]), links: make(map[string]*link), addresses: make(map[string]string), receive: newReceiver(c), selector: session.Selector{AutoUpgrade: c.AutoUpgrade, Timeout: c.Timeout(), HoldDown: time.Duration(c.HoldDownMS) * time.Millisecond, Margin: 10 * time.Millisecond, Confirmations: 2}}, nil
}

func newReceiver(c config.Config) *session.Reassembler {
	if c.Redundant {
		return session.NewRedundantReassembler(128, 2*time.Second)
	}
	return session.NewReassembler(128, 2*time.Second)
}

// Run owns all sockets until cancellation and is intentionally single-use.
func (e *Engine) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("engine already started")
	}
	e.started = true
	local, cancel := context.WithCancel(ctx)
	e.ctx = local
	// The sentinel keeps Add safe while listener callbacks can register workers.
	e.workers.Add(1)
	e.mu.Unlock()
	var stops []func()
	defer func() {
		e.mu.Lock()
		e.closing = true
		links := make([]*link, 0, len(e.links))
		for _, l := range e.links {
			links = append(links, l)
		}
		a := e.app
		e.mu.Unlock()
		cancel()
		for _, stop := range stops {
			stop()
		}
		for _, l := range links {
			l.stop()
		}
		if a != nil {
			a.close()
		}
		e.workers.Done()
		e.workers.Wait()
		e.mu.Lock()
		clear(e.retained)
		e.mu.Unlock()
		for len(e.output) > 0 {
			d := <-e.output
			d.owner.Release(d.packet)
		}
	}()
	a, err := openApplication(e.cfg)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.app = a
	e.output = make(chan delivery, 256)
	e.repairWake = make(chan struct{}, 1)
	e.mu.Unlock()
	if e.cfg.Redundant {
		e.workers.Add(1)
		go func() { defer e.workers.Done(); e.repairSession(local) }()
	}
	e.workers.Add(1)
	go func() { defer e.workers.Done(); e.writeApplication(local) }()
	if e.cfg.Metrics != "" {
		stop, err := e.serveMetrics(local)
		if err != nil {
			return err
		}
		stops = append(stops, stop)
	}
	if e.cfg.Role == "exit" {
		for _, p := range e.cfg.Carriers {
			addr, stop, err := transport.Listen(local, e.cfg, p, e.epoch, func(ctx context.Context, w transport.Wire, peer uint64) error { return e.attach(ctx, p, w, peer) })
			if err != nil {
				return err
			}
			stops = append(stops, stop)
			e.mu.Lock()
			e.addresses[p.Name] = addr.String()
			e.mu.Unlock()
		}
	} else {
		for _, p := range e.cfg.Carriers {
			e.workers.Add(1)
			go func() { defer e.workers.Done(); e.supervise(local, p) }()
		}
	}
	e.workers.Add(1)
	go func() { defer e.workers.Done(); e.readApplication(local) }()
	<-local.Done()
	return nil
}

func (e *Engine) attach(ctx context.Context, p config.Carrier, w transport.Wire, peer uint64) error {
	e.mu.Lock()
	if e.closing || e.ctx.Err() != nil {
		e.mu.Unlock()
		_ = w.Close()
		return context.Canceled
	}
	if peer == 0 || (e.peerEpoch != 0 && peer != e.peerEpoch && len(e.links) > 0) {
		e.mu.Unlock()
		_ = w.Close()
		return errors.New("peer epoch conflict")
	}
	if peer != e.peerEpoch {
		if e.cfg.Redundant && e.peerEpoch != 0 {
			clear(e.retained)
			e.sequence.Store(0)
			e.acked, e.ackHighest = 0, 0
			e.lastAck = time.Time{}
			e.ackPending = false
		}
		e.peerEpoch = peer
		e.receive = newReceiver(e.cfg)
	}
	// Refuse a duplicate live carrier rather than let delayed reconnects evict it.
	if e.links[p.Name] != nil {
		e.mu.Unlock()
		_ = w.Close()
		return errors.New("carrier already attached")
	}
	l := newLink(e, ctx, p, w)
	e.links[p.Name] = l
	e.workers.Add(1)
	e.mu.Unlock()
	defer e.workers.Done()
	l.run()
	e.mu.Lock()
	if e.links[p.Name] == l {
		delete(e.links, p.Name)
	}
	e.mu.Unlock()
	return nil
}

func (e *Engine) supervise(ctx context.Context, p config.Carrier) {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		e.counters.reconnects.Add(1)
		w, peer, err := transport.Dial(ctx, e.cfg, p, e.epoch)
		if err == nil {
			started := time.Now()
			_ = e.attach(ctx, p, w, peer)
			if time.Since(started) > e.cfg.Timeout() {
				backoff = 100 * time.Millisecond
			}
		}
		// Independent retry delay; healthy links keep their own probe workers.
		var random [1]byte
		_, _ = rand.Read(random[:])
		delay := backoff + time.Duration(random[0])*backoff/512
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(2*time.Second, backoff*2)
	}
}

func (e *Engine) readApplication(ctx context.Context) {
	var frameStorage [session.MaxPacket / session.Chunk]session.Frame
	var pathStorage [4]session.Path
	for ctx.Err() == nil {
		packet, err := e.app.read(e.cfg.MaxPacketBytes)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.counters.appErrors.Add(1)
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		if len(packet) == 0 || len(packet) > e.cfg.MaxPacketBytes {
			e.counters.dropped.Add(1)
			continue
		}
		e.counters.inputBytes.Add(uint64(len(packet)))
		if e.cfg.Redundant {
			e.mu.Lock()
			if e.admitRedundantLocked(packet, time.Now()) {
				e.active = "redundant"
				e.counters.queuedBytes.Add(uint64(len(packet)))
			} else {
				e.counters.dropped.Add(1)
			}
			e.mu.Unlock()
			continue
		}
		frames, err := session.FragmentInto(frameStorage[:0], packet, e.sequence.Add(1))
		if err != nil {
			e.counters.dropped.Add(1)
			continue
		}
		e.mu.Lock()
		chosen := ""
		if !e.cfg.Redundant {
			chosen = e.selector.Choose(time.Now(), e.pathsInto(pathStorage[:0]))
		}
		sent := false
		if l := e.links[chosen]; l != nil {
			sent = l.enqueue(frames)
			if !sent {
				e.mu.Unlock()
				// Preserve the pending whole packet across a brief scheduler or
				// flow-control stall. Two ms caused sparse burst drops even on
				// a healthy reliable carrier, collapsing inner TCP for seconds.
				// This wait adds no queue slots and remains cancellation-aware.
				sent = l.enqueueWait(ctx, frames, 50*time.Millisecond)
				e.mu.Lock()
			}
		}
		// A single opaque WireGuard aggregate cannot safely be striped by
		// individual overflow packets. Keep one active carrier; the selector
		// handles failed paths and confirmed healthy replacements on the next
		// packet. At persistent capacity pressure, drop a whole packet.
		if sent {
			if e.active != "" && e.active != chosen {
				e.counters.switches.Add(1)
			}
			e.active = chosen
			e.counters.queuedBytes.Add(uint64(len(packet)))
		} else {
			e.counters.dropped.Add(1)
		}
		e.mu.Unlock()
	}
}

// paths must be called with e.mu held. Link locks never acquire e.mu.
func (e *Engine) paths(now time.Time) []session.Path {
	return e.pathsInto(make([]session.Path, 0, len(e.links)))
}
func (e *Engine) pathsInto(out []session.Path) []session.Path {
	for _, l := range e.links {
		out = append(out, l.path())
	}
	return out
}
func (e *Engine) onData(l *link, f session.Frame) {
	// Keep completion and publishing in one order across concurrent carriers.
	// Never hold the engine lock while waiting for application output space.
	e.deliveryMu.Lock()
	defer e.deliveryMu.Unlock()
	if e.ctx.Err() != nil {
		return
	}
	if int(f.Total) > e.cfg.MaxPacketBytes {
		e.counters.dropped.Add(1)
		return
	}
	e.mu.Lock()
	if e.links[l.carrier.Name] != l {
		e.mu.Unlock()
		return
	}
	reassembler := e.receive
	packet, complete, err := reassembler.Push(time.Now(), f)
	if !complete && reassembler.Completed(f.ID) {
		e.acknowledgeLocked(reassembler)
	}
	e.mu.Unlock()
	if err != nil {
		e.counters.dropped.Add(1)
		return
	}
	if !complete {
		return
	}
	select {
	case e.output <- delivery{id: f.ID, packet: packet, owner: reassembler}:
		e.mu.Lock()
		e.acknowledgeLocked(reassembler)
		e.mu.Unlock()
	case <-e.ctx.Done():
		reassembler.Release(packet)
	}
}

// Resolve explicitly to keep UDP application sockets on loopback.
func udpAddress(s string) (*net.UDPAddr, error) { return net.ResolveUDPAddr("udp", s) }
