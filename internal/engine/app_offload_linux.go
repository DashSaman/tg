//go:build linux

package engine

import (
	"encoding/binary"
	"errors"
	"net"
	"unsafe"

	"github.com/DashSaman/tg/internal/session"
	"golang.org/x/sys/unix"
)

// Only already available datagrams for the same destination are grouped.
// All segments except the optional final short one have identical lengths.
// UDP_SEGMENT preserves datagram boundaries; this is not UDP_CORK.
func (a *application) prepareOffloadedWrite(packets [][]byte, peer net.Addr) int {
	count, used := 0, 0
	for first := 0; first < len(packets); {
		size, end, total := len(packets[first]), first+1, len(packets[first])
		if size > 0 && size <= session.MaxPacket {
			for end < len(packets) && len(packets[end]) > 0 && len(packets[end]) <= size && total+len(packets[end]) <= 65507 {
				last := len(packets[end]) < size
				total += len(packets[end])
				end++
				if last {
					break
				}
			}
		}
		m := &a.writeMessages[count]
		m.Addr, m.OOB = peer, nil
		if end-first > 1 {
			if a.offload.storage == nil {
				a.offload.storage = make([]byte, 32*session.MaxPacket)
			}
			start := used
			for _, p := range packets[first:end] {
				used += copy(a.offload.storage[used:], p)
			}
			a.writeBuffers[count][0] = a.offload.storage[start:used]
			control := a.offload.control[count][:unix.CmsgSpace(2)]
			clear(control)
			header := (*unix.Cmsghdr)(unsafe.Pointer(&control[0]))
			header.Level, header.Type = unix.IPPROTO_UDP, unix.UDP_SEGMENT
			header.SetLen(unix.CmsgLen(2))
			binary.NativeEndian.PutUint16(control[unix.CmsgLen(0):], uint16(size))
			m.OOB = control
		} else {
			a.writeBuffers[count][0] = packets[first]
		}
		a.offload.ends[count] = end
		count++
		first = end
	}
	return count
}

func unsupportedApplicationGSO(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOPROTOOPT) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EIO) || errors.Is(err, unix.EMSGSIZE)
}
