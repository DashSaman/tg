package transport

import (
	"bufio"
	"net"
	"sync"
	"time"
)

// Read-ahead is activated only AFTER TLS identity and CONNECT/session checks.
// Unauthenticated sockets never allocate the256KiB read buffer. It performs a
// normal Read, never waits to fill the buffer, and never blocks a duplex Write.
type connectConn struct {
	net.Conn
	readMu  sync.Mutex
	enabled bool
	reader  *bufio.Reader
}

func (c *connectConn) enableReadAhead() {
	c.readMu.Lock()
	c.enabled = true
	c.readMu.Unlock()
}
func (c *connectConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if !c.enabled {
		return c.Conn.Read(b)
	}
	if c.reader == nil {
		c.reader = bufio.NewReaderSize(c.Conn, 256<<10)
	}
	return c.reader.Read(b)
}

type connectListener struct {
	net.Listener
	batch bool
}

func (l connectListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.batch {
		conn = newRecordBatchConn(conn, 200*time.Microsecond)
	}
	return &connectConn{Conn: conn}, nil
}
