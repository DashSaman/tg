// Package transport provides encrypted, authenticated carrier channels.
package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"io"
	"net"
)

type Wire interface {
	// Send consumes b before returning. Receive bytes remain valid until the next Receive.
	Send([]byte) error
	Receive(context.Context) ([]byte, error)
	Close() error
}

// BatchWire coalesces already available frames without changing their wire format.
// Implementations consume every input slice before returning, including on failure.
type BatchWire interface{ SendBatch([][]byte) error }

const MaxBatchFrames = 16

type Accept func(context.Context, Wire, uint64) error

func Listen(ctx context.Context, c config.Config, p config.Carrier, epoch uint64, accept Accept) (net.Addr, func(), error) {
	if p.Kind == "https" {
		return listenHTTP(ctx, c, p, epoch, accept)
	}
	if p.Kind == "quic" {
		return listenQUIC(ctx, c, p, epoch, accept)
	}
	return nil, nil, errors.New("unsupported carrier")
}
func Dial(ctx context.Context, c config.Config, p config.Carrier, epoch uint64) (Wire, uint64, error) {
	if p.Kind == "https" {
		return dialHTTP(ctx, c, p, epoch)
	}
	if p.Kind == "quic" {
		return dialQUIC(ctx, c, p, epoch)
	}
	return nil, 0, errors.New("unsupported carrier")
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func sendStream(w io.Writer, b []byte) error {
	if len(b) == 0 || len(b) > session.MaxFrame {
		return errors.New("invalid carrier frame size")
	}
	var n [2]byte
	binary.BigEndian.PutUint16(n[:], uint16(len(b)))
	if err := writeAll(w, n[:]); err != nil {
		return err
	}
	return writeAll(w, b)
}
func recvStream(r io.Reader) ([]byte, error) {
	var n [2]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(n[:]))
	if size < 1 || size > session.MaxFrame {
		return nil, errors.New("carrier frame size out of bounds")
	}
	b := make([]byte, size)
	_, err := io.ReadFull(r, b)
	return b, err
}
