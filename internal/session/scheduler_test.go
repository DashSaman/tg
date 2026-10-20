package session

import (
	"testing"
	"time"
)

func TestSelectionDebouncesAndImmediatelyEscapesDeadPath(t *testing.T) {
	now := time.Unix(100, 0)
	s := Selector{Timeout: time.Second, HoldDown: 2 * time.Second, Margin: 5 * time.Millisecond, Confirmations: 2}
	paths := []Path{{ID: "quic", Ready: true, LastPong: now, RTT: 10 * time.Millisecond}, {ID: "https", Ready: true, LastPong: now, RTT: 40 * time.Millisecond}}
	if got := s.Choose(now, paths); got != "quic" {
		t.Fatal(got)
	}
	paths[1].RTT = time.Millisecond
	if got := s.Choose(now.Add(100*time.Millisecond), paths); got != "quic" {
		t.Fatal("flapped during hold", got)
	}
	now = now.Add(3 * time.Second)
	for i := range paths {
		paths[i].LastPong = now
	}
	if got := s.Choose(now, paths); got != "quic" {
		t.Fatal("unconfirmed upgrade", got)
	}
	paths[1].LastPong = now.Add(time.Millisecond)
	if got := s.Choose(now.Add(time.Millisecond), paths); got != "https" {
		t.Fatal("upgrade not selected", got)
	}
	paths[1].Ready = false
	if got := s.Choose(now.Add(2*time.Millisecond), paths); got != "quic" {
		t.Fatal("dead path held by cooldown", got)
	}
	if got := s.Choose(now.Add(2*time.Second), paths); got != "" {
		t.Fatal("stale path selected", got)
	}
}

func TestUnprobedPathCannotCarryTraffic(t *testing.T) {
	s := Selector{Timeout: time.Second}
	if got := s.Choose(time.Now(), []Path{{ID: "new", Ready: true}}); got != "" {
		t.Fatal(got)
	}
}

func TestJitterPenalizesUnstableCarrier(t *testing.T) {
	now := time.Now()
	s := Selector{Timeout: time.Second}
	paths := []Path{{ID: "variable", Ready: true, LastPong: now, RTT: 10 * time.Millisecond, Jitter: 30 * time.Millisecond}, {ID: "stable", Ready: true, LastPong: now, RTT: 30 * time.Millisecond}}
	if got := s.Choose(now, paths); got != "stable" {
		t.Fatal("ignored latency variation", got)
	}
}
