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

type quicWire struct {
	conn       datagramConnection
	sendMu     sync.Mutex
	sendBuffer [1400]byte
	limit      int
	retryMTU   time.Time
	received   []byte
	offset     int
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
		for last < len(frames) && n+2+len(frames[last]) <= w.limit {
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
	if w.offset == len(w.received) {
		b, err := w.conn.ReceiveDatagram(ctx)
		if err != nil {
			return nil, err
		}
		if len(b) == 0 || len(b) > len(w.sendBuffer) {
			return nil, errors.New("invalid QUIC bundle size")
		}
		count := 0
		for at := 0; at < len(b); {
			if len(b)-at < 2 {
				return nil, errors.New("truncated QUIC frame length")
			}
			size := int(binary.BigEndian.Uint16(b[at : at+2]))
			at += 2
			count++
			if size == 0 || size > session.MaxFrame || size > len(b)-at || count > MaxBatchFrames {
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
func (w *quicWire) Close() error { return w.conn.CloseWithError(0, "closed") }
func quicConfig() *quic.Config {
	return &quic.Config{EnableDatagrams: true, HandshakeIdleTimeout: 3 * time.Second, MaxIdleTimeout: 15 * time.Second, MaxIncomingStreams: 1, MaxIncomingUniStreams: -1}
}

func listenQUIC(ctx context.Context, c config.Config, p config.Carrier, epoch uint64, accept Accept) (net.Addr, func(), error) {
	tc, err := pki.Server(c.TLS, true)
	if err != nil {
		return nil, nil, err
	}
	tc.NextProtos = []string{alpn}
	l, err := quic.ListenAddr(p.Address, tc, quicConfig())
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
				_ = stream.Close()
				stream.CancelRead(0)
				_ = accept(local, &quicWire{conn: conn}, peerEpoch)
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
	tc.NextProtos = []string{alpn}
	tc.MinVersion = tls.VersionTLS13
	dialCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(dialCtx, p.Address, tc, quicConfig())
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
	_ = stream.Close()
	stream.CancelRead(0)
	other := binary.BigEndian.Uint64(ack[:])
	state := conn.ConnectionState()
	if other == 0 || !state.SupportsDatagrams.Remote || !state.SupportsDatagrams.Local {
		return fail(errors.New("peer cannot use datagram session"))
	}
	return &quicWire{conn: conn}, other, nil
}
