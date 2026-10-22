package engine

import (
	"context"
	"time"
)

// Local application egress shaping; independent from native WAN congestion control.
type applicationPacer struct {
	next  time.Time
	mbps  int
	timer *time.Timer
}

func (p *applicationPacer) reserve(now time.Time, bytes int) time.Duration {
	if p.mbps <= 0 || bytes <= 0 {
		return 0
	}
	// Permit a bounded 256KiB burst, enough for one maximum-size UDP batch.
	// No sleeping while holding application, engine or transport locks.
	if p.next.Before(now) {
		p.next = now
	}
	p.next = p.next.Add(time.Duration(int64(bytes) * 8 * int64(time.Second) / (int64(p.mbps) * 1000000)))
	burst := time.Duration(int64(256<<10) * 8 * int64(time.Second) / (int64(p.mbps) * 1000000))
	return max(time.Duration(0), p.next.Sub(now)-burst)
}

func (p *applicationPacer) wait(ctx context.Context, bytes int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delay := p.reserve(time.Now(), bytes)
	if delay == 0 {
		return nil
	}
	if p.timer == nil {
		p.timer = time.NewTimer(delay)
	} else {
		p.timer.Reset(delay)
	}
	defer p.timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.timer.C:
		return nil
	}
}

func (p *applicationPacer) close() {
	if p.timer != nil {
		p.timer.Stop()
	}
}
