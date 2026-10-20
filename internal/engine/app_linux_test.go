//go:build linux

package engine

import (
	"encoding/binary"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/config"
)

// A receiver stall of a few milliseconds must not lose a bounded ingress burst
// merely because the kernel default is much smaller than the requested buffer.
func TestApplicationBurstFitsRequestedReceiveBuffer(t *testing.T) {
	raw, err := os.ReadFile("/proc/sys/net/core/rmem_max")
	if err != nil {
		t.Skip("kernel receive limit unavailable")
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if limit < 1<<20 {
		t.Skip("host caps receive buffers below the requested 1MiB")
	}
	c := config.Defaults()
	c.Role = "edge"
	c.AppAddress = "127.0.0.1:0"
	app, err := openApplication(c)
	if err != nil {
		t.Fatal(err)
	}
	defer app.close()
	sender, err := net.DialUDP("udp4", nil, app.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	packet := make([]byte, 1400)
	for i := 0; i < 512; i++ {
		binary.BigEndian.PutUint32(packet, uint32(i))
		if _, err := sender.Write(packet); err != nil {
			t.Fatal(err)
		}
	}
	_ = app.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var seen [512]bool
	for i := 0; i < 512; i++ {
		b, err := app.read(c.MaxPacketBytes)
		if err != nil {
			t.Fatalf("bounded ingress burst lost after %d packets: %v", i, err)
		}
		id := binary.BigEndian.Uint32(b)
		if len(b) != 1400 || id >= 512 || seen[id] {
			t.Fatal("burst integrity failed")
		}
		seen[id] = true
	}
}
