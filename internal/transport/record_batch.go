package transport

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Optional batching of already encrypted records; TLS itself is unchanged.
// Short/control records flush immediately. A full-record tail has a200us
// fallback; at most262496 bytes are allocated lazily. No plaintext is read.
const fullTLS13Record = 5 + 16384 + 1 + 16

type recordBatchConn struct {
	net.Conn
	mu            sync.Mutex
	buffer        []byte
	tailDelay     time.Duration
	used          int
	timer         *time.Timer
	generation    uint64
	closed        bool
	failure       error
	writeDeadline time.Time
}

func newRecordBatchConn(conn net.Conn, delay time.Duration) *recordBatchConn {
	if delay <= 0 {
		delay = 200 * time.Microsecond
	}
	return &recordBatchConn{Conn: conn, tailDelay: delay}
}
func (c *recordBatchConn) stopTimerLocked() {
	c.generation++
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}
func (c *recordBatchConn) flushLocked() error {
	c.stopTimerLocked()
	if c.used == 0 {
		return nil
	}
	n, err := c.Conn.Write(c.buffer[:c.used])
	if err == nil && n != c.used {
		err = io.ErrShortWrite
	}
	c.used = 0
	if err != nil {
		c.failure = err
		_ = c.Conn.Close()
	}
	return err
}
func (c *recordBatchConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	if c.failure != nil {
		return 0, c.failure
	}
	if !c.writeDeadline.IsZero() && !time.Now().Before(c.writeDeadline) {
		return 0, os.ErrDeadlineExceeded
	}
	full := len(b) == fullTLS13Record && b[0] == 23 && int(binary.BigEndian.Uint16(b[3:5])) == len(b)-5
	if !full {
		if c.used > 0 && len(b) <= len(c.buffer)-c.used {
			c.used += copy(c.buffer[c.used:], b)
			if err := c.flushLocked(); err != nil {
				return 0, err
			}
			return len(b), nil
		}
		if err := c.flushLocked(); err != nil {
			return 0, err
		}
		n, err := c.Conn.Write(b)
		if err == nil && n != len(b) {
			err = io.ErrShortWrite
		}
		if err != nil {
			c.failure = err
			_ = c.Conn.Close()
		}
		return n, err
	}
	if c.buffer == nil {
		c.buffer = make([]byte, 16*fullTLS13Record)
	}
	c.used += copy(c.buffer[c.used:], b)
	if c.used == len(c.buffer) {
		if err := c.flushLocked(); err != nil {
			return 0, err
		}
	} else if c.timer == nil {
		c.generation++
		generation := c.generation
		c.timer = time.AfterFunc(c.tailDelay, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if !c.closed && generation == c.generation {
				_ = c.flushLocked()
			}
		})
	}
	return len(b), nil
}
func (c *recordBatchConn) Close() error {
	err := c.Conn.Close() // interrupt a blocked flush before acquiring its mutex
	c.mu.Lock()
	c.closed = true
	c.used = 0
	c.stopTimerLocked()
	c.mu.Unlock()
	return err
}
func (c *recordBatchConn) SetWriteDeadline(t time.Time) error {
	err := c.Conn.SetWriteDeadline(t)
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return err
}
func (c *recordBatchConn) SetDeadline(t time.Time) error {
	err := c.Conn.SetReadDeadline(t)
	other := c.SetWriteDeadline(t)
	if err != nil {
		return err
	}
	return other
}

type recordBatchListener struct{ net.Listener }

func (l recordBatchListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return newRecordBatchConn(c, 200*time.Microsecond), nil
}
