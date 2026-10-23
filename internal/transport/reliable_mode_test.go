package transport

import (
	"context"
	"github.com/DashSaman/tg/internal/config"
	"testing"
	"time"
)

func TestReliableModeNegotiationAndMismatch(t *testing.T) {
	for _, kind := range []string{"https", "https-connect", "quic-stream", "quic"} {
		for _, serverMode := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/legacy", true: "/repair"}[serverMode], func(t *testing.T) {
				edge, exit := fixture(t)
				exit.Redundant = serverMode
				p := config.Carrier{Name: kind, Kind: kind, Address: "127.0.0.1:0", Path: "/private"}
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
				edge.Redundant = !serverMode
				if w, _, err := Dial(ctx, edge, p, 99); err == nil {
					w.Close()
					t.Fatal("mismatched repair mode accepted")
				}
				edge.Redundant = serverMode
				w, _, err := Dial(ctx, edge, p, 99)
				if err != nil {
					t.Fatal(err)
				}
				defer w.Close()
				if err = w.Send([]byte("negotiated")); err != nil {
					t.Fatal(err)
				}
				b, err := w.Receive(ctx)
				if err != nil || string(b) != "negotiated" {
					t.Fatal(string(b), err)
				}
			})
		}
	}
}
