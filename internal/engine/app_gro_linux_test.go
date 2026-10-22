//go:build linux

package engine

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
	"unsafe"

	"github.com/DashSaman/tg/internal/config"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

func groTestControl(kind, size int) []byte {
	b := make([]byte, unix.CmsgSpace(size))
	h := (*unix.Cmsghdr)(unsafe.Pointer(&b[0]))
	h.Level, h.Type = unix.IPPROTO_UDP, int32(kind)
	h.SetLen(unix.CmsgLen(size))
	return b
}

func TestApplicationGROIsOptIn(t *testing.T) {
	c := config.Defaults()
	c.Role, c.AppAddress = "edge", "127.0.0.1:0"
	a, err := openApplication(c)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if a.groEnabled || len(a.readStorage) != 32*(c.MaxPacketBytes+1) {
		t.Fatal("default allocates large GRO storage without measured coalesced traffic")
	}
}

func TestApplicationGROPreservesSegmentsAndOwnedCopies(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(address, func(t *testing.T) { testApplicationGROSegments(t, address) })
	}
}

func testApplicationGROSegments(t *testing.T, address string) {
	c := config.Defaults()
	c.AppUDPGRO = true
	c.Role, c.AppAddress = "edge", address
	a, err := openApplication(c)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	raw, err := a.conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var enabled int
	var optionErr error
	if err = raw.Control(func(fd uintptr) {
		enabled, optionErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_GRO)
	}); err != nil {
		t.Fatal(err)
	}
	if optionErr != nil {
		t.Skipf("kernel UDP GRO unavailable: %v", optionErr)
	}
	if enabled != 1 {
		t.Fatal("application socket does not enable UDP GRO")
	}
	sender, err := net.DialUDP("udp", nil, a.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	packets := make([][]byte, 9)
	for i := range packets {
		size := 1200
		if i == 8 {
			size = 317
		}
		packets[i] = bytes.Repeat([]byte{byte(i + 1)}, size)
	}
	oob := groTestControl(unix.UDP_SEGMENT, 2)
	binary.NativeEndian.PutUint16(oob[unix.CmsgLen(0):], 1200)
	if _, _, err = sender.WriteMsgUDP(bytes.Join(packets, nil), oob, nil); err != nil {
		t.Fatalf("UDP segmentation write: %v", err)
	}
	_ = a.conn.SetReadDeadline(time.Now().Add(time.Second))
	var retained [][]byte
	for _, want := range packets {
		got, err := a.read(c.MaxPacketBytes)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("segment changed: bytes=%d err=%v", len(got), err)
		}
		retained = append(retained, got)
	}
	for i, got := range retained {
		if !bytes.Equal(got, packets[i]) {
			t.Fatal("GRO slice reused while asynchronous consumer still owns it")
		}
	}
	if a.groBatches.Load() != 1 || a.groPackets.Load() != 9 {
		t.Fatalf("real socket did not exercise coalesced input: batches=%d packets=%d", a.groBatches.Load(), a.groPackets.Load())
	}
}

type applicationReadFixture struct {
	applicationBatchConn
	messages []ipv4.Message
	calls    int
}

func (b *applicationReadFixture) ReadBatch(messages []ipv4.Message, _ int) (int, error) {
	b.calls++
	if b.calls != 1 {
		return 0, io.EOF
	}
	copy(messages, b.messages)
	return len(b.messages), io.ErrUnexpectedEOF
}

func TestApplicationGRORejectsTruncationBeforeUpdatingPeer(t *testing.T) {
	gro := func(size uint32) []byte {
		b := groTestControl(unix.UDP_GRO, 4)
		binary.NativeEndian.PutUint32(b[unix.CmsgLen(0):], size)
		return b
	}
	for _, kind := range []string{"data-truncated", "control-truncated", "short-control", "zero-segment", "large-segment", "negative-segment", "short-header", "duplicate-control", "oversized-normal"} {
		t.Run(kind, func(t *testing.T) {
			c := config.Defaults()
			c.Role, c.AppAddress = "edge", "127.0.0.1:0"
			a, err := openApplication(c)
			if err != nil {
				t.Fatal(err)
			}
			defer a.close()
			old := netip.MustParseAddrPort("127.0.0.1:7777")
			a.peer = old
			control := gro(1200)
			bad := ipv4.Message{Buffers: [][]byte{make([]byte, 2400)}, N: 2400, OOB: control, NN: len(control), Addr: net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:9999"))}
			switch kind {
			case "data-truncated":
				bad.Flags = unix.MSG_TRUNC
			case "control-truncated":
				bad.Flags = unix.MSG_CTRUNC
			case "short-control":
				bad.OOB = groTestControl(unix.UDP_GRO, 1)
				bad.NN = len(bad.OOB)
			case "zero-segment":
				bad.OOB = gro(0)
			case "large-segment":
				bad.OOB = gro(3000)
			case "negative-segment":
				bad.OOB = gro(0xffff04b0)
			case "short-header":
				bad.OOB = []byte{1}
				bad.NN = 1
			case "duplicate-control":
				bad.OOB = append(control, control...)
				bad.NN = len(bad.OOB)
			case "oversized-normal":
				bad.OOB = nil
				bad.NN = 0
			}
			good := ipv4.Message{Buffers: [][]byte{[]byte("valid")}, N: 5, Addr: net.UDPAddrFromAddrPort(old)}
			fixture := &applicationReadFixture{applicationBatchConn: a.batch, messages: []ipv4.Message{bad, good}}
			a.batch = fixture
			packet, err := a.read(2048)
			if err != nil || len(packet) <= 2048 || a.peer != old {
				t.Fatal("malformed bundle escaped as a valid packet or replaced peer", kind, len(packet), err)
			}
			packet, err = a.read(2048)
			if err != nil || string(packet) != "valid" {
				t.Fatal("invalid message discarded a later valid datagram", err)
			}
			_, err = a.read(2048)
			if err != io.ErrUnexpectedEOF || fixture.calls != 1 {
				t.Fatal("partial-batch error not delayed until every message was consumed", err)
			}
		})
	}
}

func TestApplicationGROSetupFailureKeepsBoundedOrdinaryBuffers(t *testing.T) {
	c := config.Defaults()
	c.Role, c.AppAddress = "edge", "127.0.0.1:0"
	a, err := openApplication(c)
	if err != nil {
		t.Fatal(err)
	}
	a.close() // Force socket control failure without changing host policy.
	a.setupReadBatch(512, true)
	if a.groEnabled || len(a.readStorage) != 32*513 {
		t.Fatal("unsupported socket did not retain bounded ordinary receive layout")
	}
	for _, m := range a.readMessages {
		if len(m.OOB) != 0 || len(m.Buffers[0]) != 513 {
			t.Fatal("ordinary fallback retained GRO control or large buffers")
		}
	}
}
