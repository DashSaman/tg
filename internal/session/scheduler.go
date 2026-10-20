package session

import "time"

type Path struct {
	ID                string
	Ready             bool
	LastPong          time.Time
	RTT, Jitter, Cost time.Duration
	Queued, Capacity  int
}
type Selector struct {
	Timeout, HoldDown, Margin time.Duration
	Confirmations             int
	active, candidate         string
	candidatePong             time.Time
	wins                      int
	switched                  time.Time
}

// Fallback commits successful whole-packet admission on a healthy alternate.
// Subsequent packets obey the same hold-down as a measured path switch rather
// than oscillating back as soon as the old queue has one free slot.
func (s *Selector) Fallback(now time.Time, id string) {
	if s.active == id {
		return
	}
	s.active = id
	s.candidate = ""
	s.wins = 0
	s.candidatePong = time.Time{}
	s.switched = now
}

func (p Path) Healthy(now time.Time, timeout time.Duration) bool {
	return p.Ready && !p.LastPong.IsZero() && now.Sub(p.LastPong) <= timeout && p.RTT >= 0
}
func (p Path) score() time.Duration {
	v := p.RTT + 2*p.Jitter + p.Cost
	if p.Capacity > 0 {
		v += time.Duration(p.Queued) * 50 * time.Millisecond / time.Duration(p.Capacity)
	}
	return v
}

func (s *Selector) Choose(now time.Time, paths []Path) string {
	var best, active *Path
	for i := range paths {
		p := &paths[i]
		if !p.Healthy(now, s.Timeout) {
			continue
		}
		if p.ID == s.active {
			active = p
		}
		if best == nil || p.score() < best.score() || (p.score() == best.score() && p.ID < best.ID) {
			best = p
		}
	}
	switchTo := func(id string) string { s.active = id; s.candidate = ""; s.wins = 0; s.switched = now; return id }
	if best == nil {
		return switchTo("")
	}
	if active == nil {
		return switchTo(best.ID)
	}
	// Static cost expresses initial/failure preference, not evidence that a
	// healthy fallback has become worse. Otherwise the cost alone repeatedly
	// sends bulk TCP back to a congested carrier after every hold-down.
	measuredGain := (active.score() - active.Cost) - (best.score() - best.Cost)
	if best.ID == active.ID || measuredGain < s.Margin || now.Sub(s.switched) < s.HoldDown {
		s.candidate = ""
		s.wins = 0
		return s.active
	}
	if s.candidate != best.ID {
		s.candidate = best.ID
		s.wins = 1
		s.candidatePong = best.LastPong
	} else if best.LastPong.After(s.candidatePong) {
		s.wins++
		s.candidatePong = best.LastPong
	}
	if s.wins >= max(2, s.Confirmations) {
		return switchTo(best.ID)
	}
	return s.active
}
