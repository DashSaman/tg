package transport

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/pki"
	"github.com/quic-go/quic-go"
	"io"
	"net"
	"sync"
	"time"
)

const alpn = "tg-dgram-v1"

type quicWire struct{ conn *quic.Conn }

func (w *quicWire) Send(b []byte) error                         { return w.conn.SendDatagram(b) }
func (w *quicWire) Receive(ctx context.Context) ([]byte, error) { return w.conn.ReceiveDatagram(ctx) }
func (w *quicWire) Close() error                                { return w.conn.CloseWithError(0, "closed") }
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
	return &quicWire{conn}, other, nil
}
