package engine

import (
	"context"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"testing"
	"time"
)

func TestApplicationPacerCancellationDoesNotWaitForRateBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := applicationPacer{mbps: 1, next: time.Now().Add(time.Hour)}
	done := make(chan error, 1)
	go func() { done <- p.wait(ctx, 1280) }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal("cancellation lost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pacing blocked shutdown")
	}
	p.close()
}

func TestPacedDeliveryCancellationReleasesCurrentOwnedPacket(t *testing.T) {
	r := session.NewReassembler(1, time.Second)
	frames, _ := session.Fragment([]byte("owned-before-cancel"), 1)
	packet, ok, err := r.Push(time.Now(), frames[0])
	if !ok || err != nil {
		t.Fatal("fixture", err)
	}
	e := &Engine{cfg: config.Defaults(), pacer: applicationPacer{mbps: 1, next: time.Now().Add(time.Hour)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.flushDeliveries(ctx, []delivery{{packet: packet, owner: r}}, make([][]byte, 1)); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("delivery did not cancel")
	}
	e.pacer.close()
	frames, _ = session.Fragment([]byte("after-cancel"), 2)
	reused, ok, err := r.Push(time.Now(), frames[0])
	if !ok || err != nil || &reused[0] != &packet[0] {
		t.Fatal("cancelled pacing retained the reassembly owner", err)
	}
	r.Release(reused)
	if e.counters.outputBytes.Load() != 0 || e.counters.appErrors.Load() != 0 {
		t.Fatal("cancellation counted a send or application failure")
	}
}

func TestApplicationPacerBoundsBurstAndCatchesUpWithoutPermanentDebt(t *testing.T) {
	p := applicationPacer{mbps: 500}
	now := time.Unix(12345, 0)
	// Eight 32KiB batches consume the 256KiB initial burst credit.
	for i := 0; i < 8; i++ {
		if d := p.reserve(now, 32<<10); d != 0 {
			t.Fatal("initial bounded burst delayed", i, d)
		}
	}
	if d := p.reserve(now, 32<<10); d < 500*time.Microsecond || d > 530*time.Microsecond {
		t.Fatal("burst beyond credit was not paced at 500Mbps", d)
	}
	if d := p.reserve(now, 32<<10); d < time.Millisecond || d > 1060*time.Microsecond {
		t.Fatal("subsequent batch did not share the rate budget", d)
	}
	if d := p.reserve(now.Add(time.Second), 32<<10); d != 0 {
		t.Fatal("idle time did not reset old debt", d)
	}
	// Idle time cannot accumulate an unbounded future burst.
	for i := 0; i < 7; i++ {
		p.reserve(now.Add(time.Second), 32<<10)
	}
	if d := p.reserve(now.Add(time.Second), 32<<10); d == 0 {
		t.Fatal("idle credit grew without bound")
	}
}
