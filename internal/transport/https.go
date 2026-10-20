package transport

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/pki"
	"github.com/DashSaman/tg/internal/session"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type httpWire struct {
	reader      io.ReadCloser
	writer      io.Writer
	flush       func() error
	cancel      func()
	once        sync.Once
	mu          sync.Mutex
	beforeWrite func() error
	sendBuffer  [MaxBatchFrames * (session.MaxFrame + 2)]byte
	readBuffer  [session.MaxFrame]byte
}

func (w *httpWire) Send(b []byte) error {
	return w.SendBatch([][]byte{b})
}
func (w *httpWire) SendBatch(frames [][]byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(frames) == 0 || len(frames) > MaxBatchFrames {
		return errors.New("HTTP batch limit exceeded")
	}
	n := 0
	for _, b := range frames {
		if len(b) == 0 || len(b) > session.MaxFrame {
			return errors.New("invalid carrier frame size")
		}
		binary.BigEndian.PutUint16(w.sendBuffer[n:n+2], uint16(len(b)))
		n += 2
		copy(w.sendBuffer[n:], b)
		n += len(b)
	}
	if w.beforeWrite != nil {
		if err := w.beforeWrite(); err != nil {
			return err
		}
	}
	if err := writeAll(w.writer, w.sendBuffer[:n]); err != nil {
		return err
	}
	if w.flush != nil {
		return w.flush()
	}
	return nil
}
func (w *httpWire) Receive(ctx context.Context) ([]byte, error) {
	if _, err := io.ReadFull(w.reader, w.readBuffer[:2]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(w.readBuffer[:2]))
	if size < 1 || size > session.MaxFrame {
		return nil, errors.New("carrier frame size out of bounds")
	}
	b := w.readBuffer[:size]
	_, err := io.ReadFull(w.reader, b)
	return b, err
}
func (w *httpWire) Close() error { w.once.Do(func() { w.cancel(); _ = w.reader.Close() }); return nil }

func listenHTTP(ctx context.Context, c config.Config, p config.Carrier, epoch uint64, accept Accept) (net.Addr, func(), error) {
	tc, err := pki.Server(c.TLS, false)
	if err != nil {
		return nil, nil, err
	}
	token, err := config.ReadToken(c.TokenFile)
	if err != nil {
		return nil, nil, err
	}
	want := sha256.Sum256([]byte("Bearer " + token))
	listener, err := net.Listen("tcp", p.Address)
	if err != nil {
		return nil, nil, err
	}
	local, cancel := context.WithCancel(ctx)
	srv := &http.Server{TLSConfig: tc, ReadHeaderTimeout: 3 * time.Second, MaxHeaderBytes: 8192, IdleTimeout: 10 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	srv.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if r.Method != "POST" || r.URL.Path != p.Path || r.ProtoMajor != 2 || subtle.ConstantTimeCompare(got[:], want[:]) != 1 || r.Header.Get("X-Tg-Session") != c.SessionID {
			if r.Method == "GET" && r.URL.Path == "/" {
				rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
				_, _ = io.WriteString(rw, "Service online.\n")
			} else {
				http.NotFound(rw, r)
			}
			return
		}
		peerEpoch, e := strconv.ParseUint(r.Header.Get("X-Tg-Epoch"), 16, 64)
		if e != nil || peerEpoch == 0 {
			http.NotFound(rw, r)
			return
		}
		controller := http.NewResponseController(rw)
		if e = controller.SetWriteDeadline(time.Now().Add(c.Timeout())); e != nil {
			http.Error(rw, "unavailable", 503)
			return
		}
		rw.Header().Set("Content-Type", "application/octet-stream")
		rw.Header().Set("Cache-Control", "no-store")
		rw.Header().Set("X-Tg-Epoch", strconv.FormatUint(epoch, 16))
		rw.WriteHeader(200)
		if e = controller.Flush(); e != nil {
			return
		}
		w := &httpWire{reader: r.Body, writer: rw, beforeWrite: func() error { return controller.SetWriteDeadline(time.Now().Add(c.Timeout())) }, flush: controller.Flush, cancel: func() {
			_ = controller.SetReadDeadline(time.Now().Add(-time.Second))
			_ = controller.SetWriteDeadline(time.Now().Add(-time.Second))
		}}
		defer w.Close()
		_ = accept(r.Context(), w, peerEpoch)
	})
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); _ = srv.Close(); _ = listener.Close() }) }
	go func() { <-local.Done(); stop() }()
	go func() { _ = srv.ServeTLS(listener, "", ""); stop() }()
	return listener.Addr(), stop, nil
}

func dialHTTP(ctx context.Context, c config.Config, p config.Carrier, epoch uint64) (Wire, uint64, error) {
	tc, err := pki.Client(c.TLS, false)
	if err != nil {
		return nil, 0, err
	}
	token, err := config.ReadToken(c.TokenFile)
	if err != nil {
		return nil, 0, err
	}
	local, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	tr := &http.Transport{TLSClientConfig: tc, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second, DisableCompression: true}
	closeAll := func() {
		cancel()
		_ = pw.CloseWithError(context.Canceled)
		_ = pr.CloseWithError(context.Canceled)
		tr.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(local, "POST", "https://"+p.Address+p.Path, pr)
	if err != nil {
		closeAll()
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Tg-Session", c.SessionID)
	req.Header.Set("X-Tg-Epoch", strconv.FormatUint(epoch, 16))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		closeAll()
		return nil, 0, err
	}
	other, e := strconv.ParseUint(resp.Header.Get("X-Tg-Epoch"), 16, 64)
	if resp.StatusCode != 200 || resp.ProtoMajor != 2 || e != nil || other == 0 {
		resp.Body.Close()
		closeAll()
		return nil, 0, errors.New("HTTPS session rejected")
	}
	return &httpWire{reader: resp.Body, writer: pw, cancel: closeAll}, other, nil
}
