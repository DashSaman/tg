// Package session implements transport-independent datagram continuity.
package session

import (
	"encoding/binary"
	"errors"
)

const (
	Chunk                = 1024
	Header               = 16
	MaxPacket            = 8192
	MaxStreamPacket      = 65507 // common IPv4 UDP payload ceiling; opt-in reliable mode only
	MaxFrame             = Chunk + Header
	MaxStreamFrame       = MaxStreamPacket + Header
	AckBits              = 1024
	AckBytes             = 8 + AckBits/8 // highest completed ID, followed by selective completion bits
	Data            byte = 1
	Probe           byte = 2
	Pong            byte = 3
	Ack             byte = 4
	Packet          byte = 5
)

type Frame struct {
	Kind                byte
	ID                  uint64
	Index, Count, Total uint16
	Payload             []byte
}

func (f Frame) validate() error {
	if f.Kind == Ack {
		if f.Index != 0 || f.Count != 0 || f.Total != 0 || len(f.Payload) != AckBytes || binary.BigEndian.Uint64(f.Payload) < f.ID {
			return errors.New("malformed acknowledgement")
		}
		high := binary.BigEndian.Uint64(f.Payload)
		for bit := 0; bit < AckBits; bit++ {
			if f.Payload[8+bit/8]&(byte(1)<<uint(bit%8)) != 0 && (bit == 0 || uint64(bit)+1 > high-f.ID) {
				return errors.New("impossible selective acknowledgement")
			}
		}
		return nil
	}
	if f.ID == 0 {
		return errors.New("zero frame sequence")
	}
	if f.Kind == Probe || f.Kind == Pong {
		if f.Index != 0 || f.Count != 0 || f.Total != 0 || len(f.Payload) != 0 {
			return errors.New("malformed control frame")
		}
		return nil
	}
	if f.Kind == Packet {
		if f.Index != 0 || f.Count != 1 || f.Total == 0 || f.Total > MaxStreamPacket || len(f.Payload) != int(f.Total) {
			return errors.New("invalid complete packet shape")
		}
		return nil
	}
	if f.Kind != Data || f.Total == 0 || f.Total > MaxPacket || f.Count != uint16((int(f.Total)+Chunk-1)/Chunk) || f.Index >= f.Count {
		return errors.New("invalid data fragment shape")
	}
	size := min(Chunk, int(f.Total)-int(f.Index)*Chunk)
	if len(f.Payload) != size {
		return errors.New("invalid fragment length")
	}
	return nil
}

func Encode(f Frame) ([]byte, error) {
	return EncodeInto(nil, f)
}

// EncodeInto overwrites dst's storage when capacity permits. The returned bytes
// belong to the caller and must remain unchanged until the carrier consumes them.
func EncodeInto(dst []byte, f Frame) ([]byte, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	size := Header + len(f.Payload)
	if cap(dst) < size {
		dst = make([]byte, size)
	}
	b := dst[:size]
	b[0] = 1
	b[1] = f.Kind
	binary.BigEndian.PutUint64(b[2:10], f.ID)
	binary.BigEndian.PutUint16(b[10:12], f.Index)
	binary.BigEndian.PutUint16(b[12:14], f.Count)
	binary.BigEndian.PutUint16(b[14:16], f.Total)
	copy(b[Header:], f.Payload)
	return b, nil
}

func Decode(b []byte) (Frame, error) {
	return decodeMode(b, false)
}

// DecodePacketStream is only used after explicit complete-packet negotiation.
// Reject fragments even when they fit, rather than silently mixing protocols.
func DecodePacketStream(b []byte) (Frame, error) { return decodeMode(b, true) }
func decodeMode(b []byte, packets bool) (Frame, error) {
	limit := MaxFrame
	if packets {
		limit = MaxStreamFrame
	}
	if len(b) < Header || len(b) > limit || b[0] != 1 {
		return Frame{}, errors.New("invalid frame envelope")
	}
	f := Frame{Kind: b[1], ID: binary.BigEndian.Uint64(b[2:10]), Index: binary.BigEndian.Uint16(b[10:12]), Count: binary.BigEndian.Uint16(b[12:14]), Total: binary.BigEndian.Uint16(b[14:16]), Payload: b[Header:]}
	if (!packets && f.Kind == Packet) || (packets && f.Kind == Data) {
		return Frame{}, errors.New("frame kind does not match negotiated stream mode")
	}
	return f, f.validate()
}

func Fragment(packet []byte, id uint64) ([]Frame, error) {
	return FragmentInto(nil, packet, id)
}

// FragmentInto reuses frame descriptors, never the application's payload buffer.
// Queues must copy descriptors and retain payload ownership until transmission.
func FragmentInto(dst []Frame, packet []byte, id uint64) ([]Frame, error) {
	if len(packet) == 0 || len(packet) > MaxPacket || id == 0 {
		return nil, errors.New("invalid application packet")
	}
	count := (len(packet) + Chunk - 1) / Chunk
	frames := dst[:0]
	if cap(frames) < count {
		frames = make([]Frame, 0, count)
	}
	for i := 0; i < count; i++ {
		frames = append(frames, Frame{Kind: Data, ID: id, Index: uint16(i), Count: uint16(count), Total: uint16(len(packet)), Payload: packet[i*Chunk : min((i+1)*Chunk, len(packet))]})
	}
	return frames, nil
}
