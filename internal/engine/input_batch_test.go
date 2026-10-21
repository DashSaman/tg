package engine

import (
	"bytes"
	"github.com/DashSaman/tg/internal/config"
	"net"
	"testing"
	"time"
)

func TestApplicationInputRetainsOwnedPacketsAndLastValidPeer(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(address, func(t *testing.T) {
			c := config.Defaults()
			c.Role = "edge"
			c.AppAddress = address
			c.MaxPacketBytes = 512
			app, err := openApplication(c)
			if err != nil {
				t.Skip(err)
			}
			defer app.close()
			sender, err := net.Dial("udp", app.conn.LocalAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			want := make([][]byte, 65)
			for i := range want {
				want[i] = bytes.Repeat([]byte{byte(i)}, 100+i)
				if _, err := sender.Write(want[i]); err != nil {
					t.Fatal(err)
				}
			}
			app.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			got := make([][]byte, len(want))
			for i := range got {
				got[i], err = app.read(512)
				if err != nil {
					t.Fatal(err)
				}
			}
			// Verify after multiple reads/refills, not just immediately after each call.
			for i := range got {
				if !bytes.Equal(got[i], want[i]) {
					t.Fatal("input boundary/order changed or earlier owned buffer overwritten", i)
				}
			}
			rogue, err := net.Dial("udp", app.conn.LocalAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer rogue.Close()
			for _, invalid := range [][]byte{make([]byte, 513), {}} {
				if _, err := rogue.Write(invalid); err != nil {
					t.Fatal(err)
				}
				got, err := app.read(512)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) > 0 && len(got) <= 512 {
					t.Fatal("invalid datagram admitted")
				}
			}
			if err := app.write([]byte("last-valid-peer"), time.Second); err != nil {
				t.Fatal(err)
			}
			sender.SetReadDeadline(time.Now().Add(time.Second))
			buf := make([]byte, 100)
			n, err := sender.Read(buf)
			if err != nil || string(buf[:n]) != "last-valid-peer" {
				t.Fatal("invalid packet replaced the application's peer", err)
			}
		})
	}
}
