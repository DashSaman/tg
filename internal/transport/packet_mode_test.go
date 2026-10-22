package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"io"
	"testing"
	"time"
)

func TestPacketAndLegacyWireRejectOversizeBeforeWriteOrDecode(t *testing.T) {
	for _, mode := range []bool{false, true} {
		var output bytes.Buffer
		w := &httpWire{writer: &output, packetMode: mode}
		size := w.frameLimit() + 1
		if err := w.Send(make([]byte, size)); err == nil || output.Len() != 0 || w.sendBuffer != nil {
			t.Fatal("oversize write consumed data or storage", mode)
		}
		encoded := make([]byte, size+2)
		binary.BigEndian.PutUint16(encoded, uint16(size))
		w.reader = io.NopCloser(bytes.NewReader(encoded))
		if _, err := w.Receive(context.Background()); err == nil {
			t.Fatal("oversize receive accepted", mode)
		}
	}
}

func TestPacketModeRealTLSQUICNegotiationAndFullSizeRoundTrip(t *testing.T) {
	for _, kind := range []string{"https", "quic-stream"} {
		for _, serverMode := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/legacy", true: "/packet"}[serverMode], func(t *testing.T) {
				edge, exit := fixture(t)
				edge.Redundant = true
				exit.Redundant = true
				b, _ := json.Marshal(map[string]bool{"stream_packets": serverMode})
				if err := json.Unmarshal(b, &exit); err != nil {
					t.Fatal(err)
				}
				p := config.Carrier{Name: kind, Kind: kind, Address: "127.0.0.1:0", Path: "/packet-test"}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				addr, stop, err := Listen(ctx, exit, p, 100, func(ctx context.Context, w Wire, peer uint64) error {
					data, err := w.Receive(ctx)
					if err != nil {
						return err
					}
					if err = w.Send(data); err != nil {
						return err
					}
					<-ctx.Done()
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				defer stop()
				p.Address = addr.String()
				b, _ = json.Marshal(map[string]bool{"stream_packets": !serverMode})
				_ = json.Unmarshal(b, &edge)
				if w, _, err := Dial(ctx, edge, p, 99); err == nil {
					w.Close()
					t.Fatal("mismatched packet mode connected")
				}
				b, _ = json.Marshal(map[string]bool{"stream_packets": serverMode})
				_ = json.Unmarshal(b, &edge)
				w, _, err := Dial(ctx, edge, p, 99)
				if err != nil {
					t.Fatal(err)
				}
				defer w.Close()
				size := session.MaxFrame
				if serverMode {
					size = session.Header + session.MaxPacket
				}
				payload := bytes.Repeat([]byte{91}, size)
				if err := w.Send(payload); err != nil {
					t.Fatal("full-size packet frame rejected", err)
				}
				got, err := w.Receive(ctx)
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatal("corrupt packet roundtrip", err)
				}
			})
		}
	}
}
