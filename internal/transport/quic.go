package transport

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/pki"
	"github.com/DashSaman/tg/internal/session"
	"github.com/quic-go/quic-go"
	"io"
	"net"
	"sync"
	"time"
)

// v2 carries bounded length-prefixed TG frames in each QUIC DATAGRAM.
// Both peers must upgrade together; v1 peers fail ALPN negotiation.
const alpn = "tg-dgram-v2"

// The DATAGRAM wire protocol keeps its original 16-frame bundle limit even
// when one internal application batch spans several complete datagrams.
const maxDatagramBundleFrames = 16

func quicALPN(kind string, redundant bool) string {
	if kind == "quic-stream" {
		if redundant {
			return "tg-repair-v1"
		}
		return "tg-stream-v1"
	}
	if redundant {
		return "tg-repair-dgram-v1"
	}
	return alpn
}

func quicModeConfig(p config.Carrier) *quic.Config {
	cfg := quicConfig()
	if p.Kind == "quic-stream" {
		cfg.EnableDatagrams = false
		cfg.MaxStreamReceiveWindow = 8 << 20
		cfg.MaxConnectionReceiveWindow = 16 << 20
	}
	return cfg
}

// Reuse bounded two-byte stream framing and batching. No HTTP is sent here:
// this is a full-duplex authenticated native QUIC stream with flow control.
func newQUICStreamWire(conn *quic.Conn, stream *quic.Stream, timeout time.Duration) Wire {
	return &httpWire{reader: stream, writer: stream,
		beforeWrite: func() error { return stream.SetWriteDeadline(time.Now().Add(timeout)) },
		cancel:      func() { stream.CancelRead(0); stream.CancelWrite(0); _ = conn.CloseWithError(0, "closed") },
	}
}

type quicWire struct {
	conn         datagramConnection
	sendMu       sync.Mutex
	sendBuffer   [1400]byte
	limit        int
	retryMTU     time.Time
	received     []byte
	offset       int
	readerOnce   sync.Once
	closeOnce    sync.Once
	cancelRead   context.CancelFunc
	receiveQueue chan quicReceived
	readerDone   chan struct{}
	closeErr     error
}

type quicReceived struct {
	packet []byte
	err    error
}

// The native DATAGRAM receive queue has only 128 slots. Drain it independently
// of frame decoding, reassembly and local UDP delivery. Storage is bounded to
// 2048*1400 bytes per connection; ownership transfers without an additional copy.
func (w *quicWire) startReader() {
	w.readerOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		w.cancelRead = cancel
		var nativeClosed <-chan struct{}
		if c, ok := w.conn.(interface{ Context() context.Context }); ok {
			nativeClosed = c.Context().Done()
		}
		w.receiveQueue = make(chan quicReceived, 2048)
		w.readerDone = make(chan struct{})
		go func() {
			defer close(w.readerDone)
			defer close(w.receiveQueue)
			for {
				b, err := w.conn.ReceiveDatagram(ctx)
				if err != nil {
					b = nil
				} else if len(b) == 0 || len(b) > len(w.sendBuffer) {
					b = nil
					err = errors.New("invalid QUIC bundle size")
				}
				select {
				case <-ctx.Done():
					return
				case <-nativeClosed:
					return
				case w.receiveQueue <- quicReceived{packet: b, err: err}:
				}
				if err != nil {
					return
				}
			}
		}()
	})
}

type datagramConnection interface {
	SendDatagram([]byte) error
	ReceiveDatagram(context.Context) ([]byte, error)
	CloseWithError(quic.ApplicationErrorCode, string) error
}

func (w *quicWire) Send(b []byte) error { return w.SendBatch([][]byte{b}) }
func (w *quicWire) SendBatch(frames [][]byte) error {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	if len(frames) == 0 || len(frames) > MaxBatchFrames {
		return errors.New("QUIC batch limit exceeded")
	}
	for _, f := range frames {
		if len(f) == 0 || len(f) > session.MaxFrame {
			return errors.New("invalid QUIC frame size")
		}
	}
	now := time.Now()
	if w.limit == 0 || !now.Before(w.retryMTU) {
		w.limit = len(w.sendBuffer)
		w.retryMTU = now.Add(time.Second)
	}
	for first := 0; first < len(frames); {
		n, last := 0, first
		for last < len(frames) && last-first < maxDatagramBundleFrames && n+2+len(frames[last]) <= w.limit {
			b := frames[last]
			binary.BigEndian.PutUint16(w.sendBuffer[n:n+2], uint16(len(b)))
			n += 2
			copy(w.sendBuffer[n:], b)
			n += len(b)
			last++
		}
		if last == first {
			return &quic.DatagramTooLargeError{MaxDatagramPayloadSize: int64(w.limit - 2)}
		}
		err := w.conn.SendDatagram(w.sendBuffer[:n])
		if err != nil {
			var size *quic.DatagramTooLargeError
			if errors.As(err, &size) && size.MaxDatagramPayloadSize < int64(w.limit) && size.MaxDatagramPayloadSize > 2 {
				// SendDatagram rejected this datagram before queueing; retry only these
				// unsent frames at the current path limit. Earlier bundles stay consumed.
				w.limit = int(size.MaxDatagramPayloadSize)
				continue
			}
			return err
		}
		first = last
	}
	return nil
}
func (w *quicWire) Receive(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if w.offset == len(w.received) {
		w.startReader()
		var item quicReceived
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case received, ok := <-w.receiveQueue:
			if !ok {
				return nil, net.ErrClosed
			}
			item = received
		}
		if item.err != nil {
			return nil, item.err
		}
		b := item.packet
		count := 0
		for at := 0; at < len(b); {
			if len(b)-at < 2 {
				return nil, errors.New("truncated QUIC frame length")
			}
			size := int(binary.BigEndian.Uint16(b[at : at+2]))
			at += 2
			count++
			if size == 0 || size > session.MaxFrame || size > len(b)-at || count > maxDatagramBundleFrames {
				return nil, errors.New("invalid QUIC bundled frame")
			}
			at += size
		}
		w.received = b
		w.offset = 0
	}
	size := int(binary.BigEndian.Uint16(w.received[w.offset : w.offset+2]))
	w.offset += 2
	frame := w.received[w.offset : w.offset+size]
	w.offset += size
	return frame, nil
}
func (w *quicWire) Close() error {
	w.startReader()
	w.closeOnce.Do(func() {
		w.cancelRead()
		w.closeErr = w.conn.CloseWithError(0, "closed")
		<-w.readerDone
		// Release prefetched ownership on shutdown; Receive may also be draining.
		for range w.receiveQueue {
		}
	})
	return w.closeErr
}
func quicConfig() *quic.Config {
	return &quic.Config{EnableDatagrams: true, HandshakeIdleTimeout: 3 * time.Second, MaxIdleTimeout: 15 * time.Second, MaxIncomingStreams: 1, MaxIncomingUniStreams: -1}
}

func listenQUIC(ctx context.Context, c config.Config, p config.Carrier, epoch uint64, accept Accept) (net.Addr, func(), error) {
	tc, err := pki.Server(c.TLS, true)
	if err != nil {
		return nil, nil, err
	}
	tc.NextProtos = []string{quicALPN(p.Kind, c.Redundant)}
	l, err := quic.ListenAddr(p.Address, tc, quicModeConfig(p))
	if err != nil {
		return nil, nil, err
	}
	local, cancel := context.WithCancel(ctx)
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); _ = l.Close() }) }
	go func() { <-local.Done(); stop() }()
	go func() {
		defer stop()
		for {
			conn, e := l.Accept(local)
			if e != nil {
				return
			}
			go func() {
				defer conn.CloseWithError(0, "closed")
				stopOnCancel := context.AfterFunc(local, func() { _ = conn.CloseWithError(0, "closed") })
				defer stopOnCancel()
				handshake, done := context.WithTimeout(local, 3*time.Second)
				defer done()
				stream, e := conn.AcceptStream(handshake)
				if e != nil {
					return
				}
				_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
				var hello [24]byte
				if _, e = io.ReadFull(stream, hello[:]); e != nil {
					return
				}
				id, e := hex.DecodeString(c.SessionID)
				if e != nil || string(id) != string(hello[:16]) {
					return
				}
				peerEpoch := binary.BigEndian.Uint64(hello[16:])
				if peerEpoch == 0 {
					return
				}
				var ack [8]byte
				binary.BigEndian.PutUint64(ack[:], epoch)
				if e = writeAll(stream, ack[:]); e != nil {
					return
				}
				if p.Kind == "quic-stream" {
					if e = stream.SetDeadline(time.Time{}); e != nil {
						return
					}
					w := newQUICStreamWire(conn, stream, c.Timeout())
					defer w.Close()
					_ = accept(local, w, peerEpoch)
					return
				}
				_ = stream.Close()
				stream.CancelRead(0)
				w := &quicWire{conn: conn}
				defer w.Close()
				_ = accept(local, w, peerEpoch)
			}()
		}
	}()
	return l.Addr(), stop, nil
}

func dialQUIC(ctx context.Context, c config.Config, p config.Carrier, epoch uint64) (Wire, uint64, error) {
	tc, err := pki.Client(c.TLS, true)
	if err != nil {
		return nil, 0, err
	}
	tc.NextProtos = []string{quicALPN(p.Kind, c.Redundant)}
	tc.MinVersion = tls.VersionTLS13
	dialCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(dialCtx, p.Address, tc, quicModeConfig(p))
	if err != nil {
		return nil, 0, err
	}
	fail := func(e error) (Wire, uint64, error) { _ = conn.CloseWithError(1, "handshake failed"); return nil, 0, e }
	stream, err := conn.OpenStreamSync(dialCtx)
	if err != nil {
		return fail(err)
	}
	_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
	id, err := hex.DecodeString(c.SessionID)
	if err != nil || len(id) != 16 {
		return fail(errors.New("invalid session ID"))
	}
	hello := make([]byte, 24)
	copy(hello, id)
	binary.BigEndian.PutUint64(hello[16:], epoch)
	if err = writeAll(stream, hello); err != nil {
		return fail(err)
	}
	var ack [8]byte
	if _, err = io.ReadFull(stream, ack[:]); err != nil {
		return fail(err)
	}
	other := binary.BigEndian.Uint64(ack[:])
	if other == 0 {
		return fail(errors.New("zero peer epoch"))
	}
	if p.Kind == "quic-stream" {
		if err = stream.SetDeadline(time.Time{}); err != nil {
			return fail(err)
		}
		return newQUICStreamWire(conn, stream, c.Timeout()), other, nil
	}
	_ = stream.Close()
	stream.CancelRead(0)
	state := conn.ConnectionState()
	if other == 0 || !state.SupportsDatagrams.Remote || !state.SupportsDatagrams.Local {
		return fail(errors.New("peer cannot use datagram session"))
	}
	return &quicWire{conn: conn}, other, nil
}
