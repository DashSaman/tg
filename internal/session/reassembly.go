package session

import (
	"errors"
	"time"
)

type assembly struct {
	born         time.Time
	total, count uint16
	mask         uint16
	data         []byte
}

// Reassembler is confined to a hub's receive lock; it does not retain caller buffers.
type Reassembler struct {
	limit   int
	ttl     time.Duration
	pending map[uint64]*assembly
	highest uint64
	seen    [4096]uint64
}

func NewReassembler(limit int, ttl time.Duration) *Reassembler {
	return &Reassembler{limit: max(1, limit), ttl: ttl, pending: make(map[uint64]*assembly)}
}

func (r *Reassembler) Push(now time.Time, f Frame) ([]byte, bool, error) {
	if err := f.validate(); err != nil {
		return nil, false, err
	}
	if f.Kind != Data {
		return nil, false, errors.New("not a data frame")
	}
	if (r.highest >= 4096 && f.ID <= r.highest-4096) || r.seen[f.ID%4096] == f.ID {
		return nil, false, nil
	}
	for id, a := range r.pending {
		if now.Sub(a.born) >= r.ttl {
			delete(r.pending, id)
		}
	}
	a := r.pending[f.ID]
	if a == nil {
		if len(r.pending) >= r.limit {
			return nil, false, errors.New("reassembly capacity reached")
		}
		a = &assembly{born: now, total: f.Total, count: f.Count, data: make([]byte, int(f.Total))}
		r.pending[f.ID] = a
	}
	if a.total != f.Total || a.count != f.Count {
		return nil, false, errors.New("inconsistent packet fragments")
	}
	bit := uint16(1) << f.Index
	if a.mask&bit != 0 {
		return nil, false, nil
	}
	copy(a.data[int(f.Index)*Chunk:], f.Payload)
	a.mask |= bit
	if a.mask != (uint16(1)<<a.count)-1 {
		return nil, false, nil
	}
	delete(r.pending, f.ID)
	r.seen[f.ID%4096] = f.ID
	r.highest = max(r.highest, f.ID)
	return a.data, true, nil
}
