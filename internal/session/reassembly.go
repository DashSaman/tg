package session

import (
	"errors"
	"time"
)

type assembly struct {
	id           uint64
	born         time.Time
	total, count uint16
	mask         uint16
	data         []byte
	prev, next   *assembly
}

// Reassembler is confined to a hub's receive lock; it does not retain caller buffers.
type Reassembler struct {
	limit          int
	ttl            time.Duration
	pending        map[uint64]*assembly
	highest        uint64
	seen           [4096]uint64
	free           chan []byte
	oldest, newest *assembly
}

func NewReassembler(limit int, ttl time.Duration) *Reassembler {
	return &Reassembler{limit: max(1, limit), ttl: ttl, pending: make(map[uint64]*assembly, max(1, limit)), free: make(chan []byte, min(128, max(1, limit)))}
}

func (r *Reassembler) remove(a *assembly) {
	delete(r.pending, a.id)
	if a.prev != nil {
		a.prev.next = a.next
	} else {
		r.oldest = a.next
	}
	if a.next != nil {
		a.next.prev = a.prev
	} else {
		r.newest = a.prev
	}
	a.next = nil
	a.prev = nil
}
func (r *Reassembler) discardOldest() { a := r.oldest; r.remove(a); r.Release(a.data) }

// Release returns a completed buffer after its last synchronous consumer. No slice
// aliases may be read or written after release; incomplete buffers stay internal.
// The cache is bounded to at most 128*8192 bytes, independent of traffic volume.
func (r *Reassembler) Release(packet []byte) {
	if cap(packet) != MaxPacket {
		return
	}
	select {
	case r.free <- packet[:MaxPacket]:
	default:
	}
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
	for r.oldest != nil && now.Sub(r.oldest.born) >= r.ttl {
		r.discardOldest()
	}
	a := r.pending[f.ID]
	if a == nil {
		if len(r.pending) >= r.limit {
			// Losing one fragment must not reserve a slot for the entire TTL while
			// all fresh traffic is rejected. Evict one oldest incomplete packet.
			r.discardOldest()
		}
		var data []byte
		select {
		case data = <-r.free:
		default:
			data = make([]byte, MaxPacket)
		}
		a = &assembly{id: f.ID, born: now, total: f.Total, count: f.Count, data: data[:int(f.Total)], prev: r.newest}
		if r.newest != nil {
			r.newest.next = a
		} else {
			r.oldest = a
		}
		r.newest = a
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
	r.remove(a)
	r.seen[f.ID%4096] = f.ID
	r.highest = max(r.highest, f.ID)
	return a.data, true, nil
}
