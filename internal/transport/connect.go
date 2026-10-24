// HTTP/1.1 CONNECT inside authenticated TLS1.3. This is an explicit carrier,
// separate from HTTPS/2; it shares bounded TG framing and reconnect continuity.
package transport

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/pki"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// A CONNECT tunnel transfers ownership of the authenticated TLS socket.
// Abort the underlying connection so shutdown interrupts a backpressured write
// instead of waiting for a TLS close-notify exchange.
type connectReadCloser struct {
	io.Reader
	close func() error
}

func (r connectReadCloser) Close() error { return r.close() }
func abortConnect(conn net.Conn) error {
	if tc, ok := conn.(*tls.Conn); ok {
		return tc.NetConn().Close()
	}
	return conn.Close()
}

func listenConnect(ctx context.Context, c config.Config, p config.Carrier, epoch uint64, accept Accept) (net.Addr, func(), error) {
	tc, err := pki.Server(c.TLS, true)
	if err != nil {
		return nil, nil, err
	}
	tc.NextProtos = []string{"http/1.1"}
	token, err := config.ReadToken(c.TokenFile)
	if err != nil {
		return nil, nil, err
	}
	want := sha256.Sum256([]byte("Bearer " + token))
	listener, err := net.Listen("tcp", p.Address)
	if err != nil {
		return nil, nil, err
	}
	listener = connectListener{Listener: listener, batch: c.TLSRecordBatch}
	local, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	conns := make(map[net.Conn]bool)
	srv := &http.Server{TLSConfig: tc, TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)), ReadHeaderTimeout: 3 * time.Second, MaxHeaderBytes: 8192, IdleTimeout: 10 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	srv.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		peer, e := strconv.ParseUint(r.Header.Get("X-Tg-Epoch"), 16, 64)
		if r.Method != "CONNECT" || r.ProtoMajor != 1 || r.ProtoMinor != 1 || subtle.ConstantTimeCompare(got[:], want[:]) != 1 || r.Header.Get("X-Tg-Session") != c.SessionID || r.Header.Get("X-Tg-Path") != p.Path || r.Header.Get("X-Tg-Mode") != sessionMode(c) || e != nil || peer == 0 {
			http.NotFound(rw, r)
			return
		}
		hijacker, ok := rw.(http.Hijacker)
		if !ok {
			http.Error(rw, "unavailable", http.StatusServiceUnavailable)
			return
		}
		conn, rwbuf, e := hijacker.Hijack()
		if e != nil {
			return
		}
		if tc, ok := conn.(*tls.Conn); ok {
			if raw, ok := tc.NetConn().(*connectConn); ok {
				raw.enableReadAhead()
			}
		}
		mu.Lock()
		if local.Err() != nil {
			mu.Unlock()
			_ = abortConnect(conn)
			return
		}
		conns[conn] = true
		mu.Unlock()
		defer func() { _ = abortConnect(conn); mu.Lock(); delete(conns, conn); mu.Unlock() }()
		_ = conn.SetWriteDeadline(time.Now().Add(c.Timeout()))
		if _, e = fmt.Fprintf(rwbuf, "HTTP/1.1 200 Connection Established\r\nX-Tg-Epoch: %x\r\nX-Tg-Mode: %s\r\n\r\n", epoch, sessionMode(c)); e != nil {
			return
		}
		if e = rwbuf.Flush(); e != nil {
			return
		}
		_ = conn.SetDeadline(time.Time{})
		w := &httpWire{reader: connectReadCloser{rwbuf.Reader, func() error { return abortConnect(conn) }}, writer: conn, packetMode: c.StreamPackets, packetBytes: c.MaxPacketBytes,
			beforeWrite: func() error { return conn.SetWriteDeadline(time.Now().Add(c.Timeout())) }, cancel: func() { _ = abortConnect(conn) }}
		defer w.Close()
		_ = accept(local, w, peer)
	})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = srv.Close()
			_ = listener.Close()
			mu.Lock()
			for conn := range conns {
				_ = abortConnect(conn)
			}
			mu.Unlock()
		})
	}
	go func() { <-local.Done(); stop() }()
	go func() { _ = srv.ServeTLS(listener, "", ""); stop() }()
	return listener.Addr(), stop, nil
}

func dialConnect(ctx context.Context, c config.Config, p config.Carrier, epoch uint64) (Wire, uint64, error) {
	tc, err := pki.Client(c.TLS, true)
	if err != nil {
		return nil, 0, err
	}
	tc.NextProtos = []string{"http/1.1"}
	token, err := config.ReadToken(c.TokenFile)
	if err != nil {
		return nil, 0, err
	}
	raw, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", p.Address)
	if err != nil {
		return nil, 0, err
	}
	if c.TLSRecordBatch {
		raw = newRecordBatchConn(raw, 200*time.Microsecond)
	}
	transportConn := &connectConn{Conn: raw}
	raw = transportConn
	conn := tls.Client(raw, tc)
	local, cancel := context.WithCancel(ctx)
	var once sync.Once
	closeAll := func() { once.Do(func() { cancel(); _ = raw.Close() }) }
	go func() { <-local.Done(); closeAll() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err = conn.HandshakeContext(local); err != nil {
		closeAll()
		return nil, 0, err
	}
	if conn.ConnectionState().NegotiatedProtocol != "http/1.1" {
		closeAll()
		return nil, 0, errors.New("CONNECT ALPN rejected")
	}
	req := &http.Request{Method: "CONNECT", URL: &url.URL{Opaque: p.Address}, Host: p.Address, Header: make(http.Header)}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Tg-Session", c.SessionID)
	req.Header.Set("X-Tg-Path", p.Path)
	req.Header.Set("X-Tg-Epoch", strconv.FormatUint(epoch, 16))
	req.Header.Set("X-Tg-Mode", sessionMode(c))
	if err = req.Write(conn); err != nil {
		closeAll()
		return nil, 0, err
	}
	// Bound the response header before transferring the same buffered bytes
	// (which may already include peer probes) to the tunnel reader.
	limited := &io.LimitedReader{R: conn, N: 8192}
	reader := bufio.NewReaderSize(limited, 64<<10)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		closeAll()
		return nil, 0, err
	}
	peer, e := strconv.ParseUint(resp.Header.Get("X-Tg-Epoch"), 16, 64)
	if resp.StatusCode != 200 || resp.ProtoMajor != 1 || resp.ProtoMinor != 1 || e != nil || peer == 0 || resp.Header.Get("X-Tg-Mode") != sessionMode(c) {
		closeAll()
		return nil, 0, errors.New("CONNECT session rejected")
	}
	limited.N = 1<<63 - 1
	transportConn.enableReadAhead()
	_ = conn.SetDeadline(time.Time{})
	return &httpWire{reader: connectReadCloser{reader, raw.Close}, writer: conn, packetMode: c.StreamPackets, packetBytes: c.MaxPacketBytes,
		beforeWrite: func() error { return conn.SetWriteDeadline(time.Now().Add(c.Timeout())) }, cancel: closeAll}, peer, nil
}
