package transport

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/pki"
)

func TestConnectRejectsAuthenticationAndSessionMismatches(t *testing.T) {
	edge, exit := fixture(t)
	p := config.Carrier{Name: "tcp", Kind: "https-connect", Address: "127.0.0.1:0", Path: "/connect-test"}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var accepted atomic.Int32
	addr, stop, err := Listen(ctx, exit, p, 100, func(ctx context.Context, w Wire, peer uint64) error { accepted.Add(1); return w.Close() })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	p.Address = addr.String()
	for _, badCase := range []string{"token", "session", "path", "zero-epoch", "client-identity"} {
		t.Run(badCase, func(t *testing.T) {
			bad := edge
			path := p
			epoch := uint64(99)
			switch badCase {
			case "token":
				bad.TokenFile = filepath.Join(t.TempDir(), "wrong.token")
				if err := os.WriteFile(bad.TokenFile, []byte(strings.Repeat("0", 64)), 0600); err != nil {
					t.Fatal(err)
				}
			case "session":
				bad.SessionID = strings.Repeat("0", 32)
			case "path":
				path.Path = "/other"
			case "zero-epoch":
				epoch = 0
			case "client-identity":
				bad.TLS.Cert = exit.TLS.Cert
				bad.TLS.Key = exit.TLS.Key
			}
			if w, _, err := Dial(ctx, bad, path, epoch); err == nil {
				w.Close()
				t.Fatal("unauthenticated or mismatched CONNECT accepted")
			}
		})
	}
	if accepted.Load() != 0 {
		t.Fatal("invalid CONNECT reached engine", accepted.Load())
	}
}

func TestHTTPSDialCancellationInterruptsUnconsumedHTTP1RequestBody(t *testing.T) {
	edge, exit := fixture(t)
	tc, err := pki.Server(exit.TLS, false)
	if err != nil {
		t.Fatal(err)
	}
	tc.NextProtos = []string{"http/1.1"}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{TLSConfig: tc, TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler))}
	srv.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		conn, _, err := rw.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		// Wait for the streaming request writer to block on the empty pipe,
		// then reject/close without consuming its never-ending body.
		time.Sleep(30 * time.Millisecond)
		_ = abortConnect(conn)
	})
	go func() { _ = srv.ServeTLS(listener, "", "") }()
	defer srv.Close()
	defer listener.Close()
	p := config.Carrier{Name: "h2", Kind: "https", Address: listener.Addr().String(), Path: "/private"}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		w, _, err := Dial(ctx, edge, p, 99)
		if w != nil {
			w.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("HTTPS2 accepted HTTP1")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled HTTPS dial is stuck reading its empty request-body pipe")
	}
}

func TestConnectListenerStopClosesHijackedConnections(t *testing.T) {
	edge, exit := fixture(t)
	edge.TLSRecordBatch = true
	exit.TLSRecordBatch = true
	p := config.Carrier{Name: "tcp", Kind: "https-connect", Address: "127.0.0.1:0", Path: "/connect-test"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	addr, stop, err := Listen(ctx, exit, p, 100, func(ctx context.Context, w Wire, peer uint64) error {
		close(entered)
		_, err := w.Receive(ctx)
		done <- err
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	p.Address = addr.String()
	w, _, err := Dial(ctx, edge, p, 99)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("CONNECT not attached")
	}
	stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stopped connection produced data")
		}
	case <-time.After(time.Second):
		t.Fatal("hijacked connection survived listener stop")
	}
}

func TestConnectAndHTTPS2CannotSilentlyNegotiateEachOther(t *testing.T) {
	for _, serverKind := range []string{"https", "https-connect"} {
		t.Run(serverKind, func(t *testing.T) {
			edge, exit := fixture(t)
			p := config.Carrier{Name: "tcp", Kind: serverKind, Address: "127.0.0.1:0", Path: "/connect-test"}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			var accepted atomic.Int32
			addr, stop, err := Listen(ctx, exit, p, 100, func(ctx context.Context, w Wire, peer uint64) error { accepted.Add(1); return w.Close() })
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			p.Address = addr.String()
			p.Kind = "https-connect"
			if serverKind == "https-connect" {
				p.Kind = "https"
			}
			if w, _, err := Dial(ctx, edge, p, 99); err == nil {
				w.Close()
				t.Fatal("different TCP protocols silently mixed")
			}
			if accepted.Load() != 0 {
				t.Fatal("mismatched protocol reached engine")
			}
		})
	}
}
