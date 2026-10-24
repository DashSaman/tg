package transport

import (
	"context"
	"encoding/hex"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/pki"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (config.Config, config.Config) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pki")
	if err := pki.Generate(dir, "localhost"); err != nil {
		t.Fatal(err)
	}
	edge := config.Defaults()
	edge.Role = "edge"
	edge.SessionID = hex.EncodeToString([]byte("1234567890abcdef"))
	edge.TokenFile = filepath.Join(dir, "web.token")
	edge.TLS.CA = filepath.Join(dir, "ca.crt")
	edge.TLS.Cert = filepath.Join(dir, "edge.crt")
	edge.TLS.Key = filepath.Join(dir, "edge.key")
	exit := edge
	exit.Role = "exit"
	exit.TLS.Cert = filepath.Join(dir, "server.crt")
	exit.TLS.Key = filepath.Join(dir, "server.key")
	return edge, exit
}

func TestSecureCarriersAndAuthentication(t *testing.T) {
	for _, kind := range []string{"https", "https-connect", "quic", "quic-stream"} {
		t.Run(kind, func(t *testing.T) {
			edge, exit := fixture(t)
			p := config.Carrier{Name: kind, Kind: kind, Address: "127.0.0.1:0", Path: "/private-path"}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			addr, stop, err := Listen(ctx, exit, p, 100, func(ctx context.Context, w Wire, epoch uint64) error {
				if epoch != 99 {
					t.Errorf("peer epoch %d", epoch)
				}
				b, e := w.Receive(ctx)
				if e != nil {
					return e
				}
				if string(b) != "request" {
					t.Error("corrupt request")
				}
				if e = w.Send([]byte("response")); e != nil {
					return e
				}
				<-ctx.Done()
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			p.Address = addr.String()
			w, epoch, err := Dial(ctx, edge, p, 99)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if epoch != 100 {
				t.Fatal(epoch)
			}
			if err = w.Send([]byte("request")); err != nil {
				t.Fatal(err)
			}
			b, err := w.Receive(ctx)
			if err != nil || string(b) != "response" {
				t.Fatal(string(b), err)
			}
			bad := edge
			bad.TLS.ServerName = "wrong.invalid"
			if badWire, _, err := Dial(ctx, bad, p, 99); err == nil {
				badWire.Close()
				t.Fatal("wrong server identity accepted")
			}
			if kind == "https" {
				bad = edge
				bad.TokenFile = filepath.Join(t.TempDir(), "wrong.token")
				if err := os.WriteFile(bad.TokenFile, []byte(strings.Repeat("0", 64)), 0600); err != nil {
					t.Fatal(err)
				}
				if badWire, _, err := Dial(ctx, bad, p, 99); err == nil {
					badWire.Close()
					t.Fatal("wrong token accepted")
				}
			} else {
				bad = edge
				bad.TLS.Cert = exit.TLS.Cert
				bad.TLS.Key = exit.TLS.Key
				if badWire, _, err := Dial(ctx, bad, p, 99); err == nil {
					badWire.Close()
					t.Fatal("wrong client certificate accepted")
				}
			}
		})
	}
}
