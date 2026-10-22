//go:build linux

package engine

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/config"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// Verify the actual socket offload, rather than only a constructed control buffer.
func TestApplicationBatchReachesKernelAsGSOSegments(t *testing.T) {
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	raw, err := receiver.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var optionErr error
	if err = raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_GRO, 1) }); err != nil {
		t.Fatal(err)
	}
	if optionErr != nil {
		t.Skipf("kernel UDP GRO unavailable: %v", optionErr)
	}
	c := config.Defaults()
	c.Role = "exit"
	c.AppAddress = receiver.LocalAddr().String()
	a, err := openApplication(c)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	packets := make([][]byte, 8)
	for i := range packets {
		packets[i] = bytes.Repeat([]byte{byte(i + 1)}, 1200)
	}
	if n, err := a.writeBatch(packets, time.Second); err != nil || n != len(packets) {
		t.Fatalf("write %d: %v", n, err)
	}
	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 65535)
	control := make([]byte, 128)
	n, oobn, flags, _, err := receiver.ReadMsgUDP(buffer, control)
	if err != nil {
		t.Fatal(err)
	}
	if n != 8*1200 || oobn == 0 || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		t.Fatalf("batch was not offloaded intact: bytes=%d control=%d flags=%d", n, oobn, flags)
	}
	if !bytes.Equal(buffer[:n], bytes.Join(packets, nil)) {
		t.Fatal("offloaded payload changed")
	}
}

type offloadFallbackBatch struct {
	applicationBatchConn
	t     *testing.T
	calls int
}

func (b *offloadFallbackBatch) WriteBatch(messages []ipv4.Message, _ int) (int, error) {
	b.calls++
	switch b.calls {
	case 1:
		if len(messages) != 2 || len(messages[0].Buffers[0]) != 31 || len(messages[0].OOB) == 0 {
			b.t.Fatal("initial GSO groups changed")
		}
		return 1, nil // first group consumes THREE original datagrams
	case 2:
		if len(messages) != 1 || len(messages[0].Buffers[0]) != 40 {
			b.t.Fatal("partially consumed GSO prefix was replayed")
		}
		return -1, unix.EOPNOTSUPP
	case 3:
		if len(messages) != 2 {
			b.t.Fatal("fallback did not retry exactly the unsent suffix")
		}
		for i, m := range messages {
			if len(m.OOB) != 0 || !bytes.Equal(m.Buffers[0], bytes.Repeat([]byte{byte(4 + i)}, 20)) {
				b.t.Fatal("fallback changed datagram boundaries or payload")
			}
		}
		return 2, nil
	default:
		b.t.Fatal("unexpected extra batch")
		return 0, nil
	}
}

func TestApplicationGSOFallbackNeverRepeatsConsumedPrefix(t *testing.T) {
	c := config.Defaults()
	c.Role = "edge"
	c.AppAddress = "127.0.0.1:0"
	a, err := openApplication(c)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	a.peer = a.conn.LocalAddr().(*net.UDPAddr).AddrPort()
	fake := &offloadFallbackBatch{applicationBatchConn: a.batch, t: t}
	a.batch = fake
	packets := [][]byte{bytes.Repeat([]byte{1}, 12), bytes.Repeat([]byte{2}, 12), bytes.Repeat([]byte{3}, 7), bytes.Repeat([]byte{4}, 20), bytes.Repeat([]byte{5}, 20)}
	if n, err := a.writeBatch(packets, time.Second); n != 5 || err != nil {
		t.Fatalf("logical write count=%d error=%v", n, err)
	}
	if !a.offload.disabled || fake.calls != 3 || a.gsoBatches.Load() != 1 || a.gsoPackets.Load() != 3 || a.gsoFallbacks.Load() != 1 {
		t.Fatal("offload fallback accounting")
	}
	for _, m := range a.writeMessages {
		if m.OOB != nil || m.Addr != nil || m.Buffers[0] != nil {
			t.Fatal("batch retained caller buffers after return")
		}
	}
}
