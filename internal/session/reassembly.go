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
	contiguous     uint64
	seen           []uint64
	free           chan []byte
	packetBytes    int
	oldest, newest *assembly
}

func NewReassembler(limit int, ttl time.Duration) *Reassembler {
	return newReassembler(limit, ttl, 4096)
}

// Delayed replicas must not evict a unique surviving packet after only 4096 newer
// completions. This keeps a bounded 2 MiB ID history; payload cache limits do not grow.
func NewRedundantReassembler(limit int, ttl time.Duration) *Reassembler {
	return newReassembler(limit, ttl, 262144)
}

// Only explicitly negotiated packet streams use a larger reusable payload.
// The caller validates this fixed ceiling; traffic cannot enlarge it.
func NewPacketReassembler(limit int, ttl time.Duration, packetBytes int) *Reassembler {
	if packetBytes < 1 || packetBytes > MaxStreamPacket {
		panic("invalid complete packet buffer budget")
	}
	r := NewRedundantReassembler(limit, ttl)
	r.packetBytes = packetBytes
	return r
}
func newReassembler(limit int, ttl time.Duration, window int) *Reassembler {
	return &Reassembler{limit: max(1, limit), ttl: ttl, packetBytes: MaxPacket, pending: make(map[uint64]*assembly, max(1, limit)), free: make(chan []byte, min(128, max(1, limit))), seen: make([]uint64, window)}
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
// The cache is bounded to128 configured packet buffers, independent of traffic
// volume. Legacy buffers remain8192 bytes; packet mode has an explicit ceiling.
func (r *Reassembler) Release(packet []byte) {
	if cap(packet) != r.packetBytes {
		return
	}
	select {
	case r.free <- packet[:r.packetBytes]:
	default:
	}
}

// AckState and Completed share Push's external receive lock. Cumulative ACKs
// never release a missing packet just because a newer packet completed.
func (r *Reassembler) AckState() (uint64, uint64) { return r.contiguous, r.highest }
func (r *Reassembler) Completed(id uint64) bool {
	return id != 0 && (id <= r.contiguous || r.seen[id%uint64(len(r.seen))] == id)
}

func (r *Reassembler) Push(now time.Time, f Frame) ([]byte, bool, error) {
	if err := f.validate(); err != nil {
		return nil, false, err
	}
	if f.Kind != Data && f.Kind != Packet {
		return nil, false, errors.New("not a data frame")
	}
	window := uint64(len(r.seen))
	if (r.highest >= window && f.ID <= r.highest-window) || r.seen[f.ID%window] == f.ID {
		return nil, false, nil
	}
	for r.oldest != nil && now.Sub(r.oldest.born) >= r.ttl {
		r.discardOldest()
	}
	if f.Kind == Packet {
		if len(f.Payload) > r.packetBytes {
			return nil, false, errors.New("complete packet exceeds receiver budget")
		}
		if r.pending[f.ID] != nil {
			return nil, false, errors.New("complete packet conflicts with pending fragments")
		}
		var data []byte
		select {
		case data = <-r.free:
		default:
			data = make([]byte, r.packetBytes)
		}
		copy(data, f.Payload)
		r.complete(f.ID)
		return data[:len(f.Payload)], true, nil
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
	r.complete(f.ID)
	return a.data, true, nil
}

func (r *Reassembler) complete(id uint64) {
	window := uint64(len(r.seen))
	r.seen[id%window] = id
	r.highest = max(r.highest, id)
	for r.contiguous < r.highest && r.seen[(r.contiguous+1)%window] == r.contiguous+1 {
		r.contiguous++
	}
}
