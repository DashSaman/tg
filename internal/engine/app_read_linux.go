//go:build linux

package engine

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"

	"golang.org/x/sys/unix"
)

const applicationGROBuffer = 65535

func (a *application) setupReadBatch(maxPacket int, gro bool) {
	if raw, err := a.conn.SyscallConn(); gro && err == nil {
		var optionErr error
		controlErr := raw.Control(func(fd uintptr) {
			optionErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_GRO, 1)
		})
		a.groEnabled = controlErr == nil && optionErr == nil
	}
	size := maxPacket + 1
	if a.groEnabled {
		size = applicationGROBuffer
	}
	// One fixed batch, independent of traffic volume; no extra batching timer.
	a.readStorage = make([]byte, len(a.readMessages)*size)
	for i := range a.readMessages {
		a.readBuffers[i][0] = a.readStorage[i*size : (i+1)*size]
		a.readMessages[i].Buffers = a.readBuffers[i][:]
		a.readMessages[i].OOB = nil
		if a.groEnabled {
			a.readMessages[i].OOB = a.readControl[i][:]
		}
	}
}

// Parse without allocating a control-message slice for each received bundle.
// Linux UDP_GRO supplies a native-endian 32-bit int (UDP_SEGMENT sends a u16).
func applicationGROSize(control []byte) (int, error) {
	size := 0
	for len(control) > 0 {
		if len(control) < unix.SizeofCmsghdr {
			return 0, errors.New("short UDP control header")
		}
		header, data, rest, err := unix.ParseOneSocketControlMessage(control)
		if err != nil {
			return 0, err
		}
		if header.Level == unix.IPPROTO_UDP && header.Type == unix.UDP_GRO {
			if size != 0 || len(data) != 4 {
				return 0, errors.New("invalid UDP GRO control")
			}
			size = int(binary.NativeEndian.Uint32(data))
			if size == 0 || size > applicationGROBuffer {
				return 0, errors.New("invalid UDP GRO segment size")
			}
		}
		control = rest
	}
	return size, nil
}

func (a *application) readBatchPacket(limit int) ([]byte, netip.AddrPort, error) {
	if a.readNext == a.readCount {
		if a.readError != nil {
			err := a.readError
			a.readError = nil
			return nil, netip.AddrPort{}, err
		}
		a.readNext = 0
		a.readCount, a.readError = a.batch.ReadBatch(a.readMessages[:], 0)
		if a.readCount <= 0 {
			err := a.readError
			a.readError, a.readCount = nil, 0
			if err == nil {
				err = errors.New("empty application read batch")
			}
			return nil, netip.AddrPort{}, err
		}
	}
	m := &a.readMessages[a.readNext]
	advance := func() { a.readNext++; a.readOffset, a.readSegment = 0, 0; m.Addr = nil }
	invalid := func() ([]byte, netip.AddrPort, error) {
		advance()
		// Preserve the ordinary oversized-packet drop contract. No valid-looking
		// partial packet or peer update escapes a truncated/malformed bundle.
		return a.readBuffer[:limit+1], netip.AddrPort{}, nil
	}
	if a.readOffset == 0 {
		if m.N < 0 || m.N > len(m.Buffers[0]) || m.NN < 0 || m.NN > len(m.OOB) || m.Flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
			return invalid()
		}
		segment, err := applicationGROSize(m.OOB[:m.NN])
		if err != nil || segment > limit || (segment == 0 && m.N > limit) {
			return invalid()
		}
		a.readSegment = segment
		if segment != 0 {
			a.groBatches.Add(1)
		}
	}
	peer := netip.AddrPort{}
	if address, ok := m.Addr.(*net.UDPAddr); ok {
		peer = address.AddrPort()
	}
	end := m.N
	if a.readSegment > 0 {
		end = min(end, a.readOffset+a.readSegment)
		a.groPackets.Add(1)
	}
	packet := m.Buffers[0][a.readOffset:end]
	a.readOffset = end
	if end == m.N {
		advance()
	}
	return packet, peer, nil
}
