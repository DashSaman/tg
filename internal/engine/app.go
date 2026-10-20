package engine

import (
	"errors"
	"github.com/DashSaman/tg/internal/config"
	"net"
	"sync"
	"time"
)

type application struct {
	conn *net.UDPConn
	exit bool
	mu   sync.Mutex
	peer *net.UDPAddr
}

func openApplication(c config.Config) (*application, error) {
	addr, err := udpAddress(c.AppAddress)
	if err != nil {
		return nil, err
	}
	a := &application{exit: c.Role == "exit"}
	if a.exit {
		a.conn, err = net.DialUDP("udp", nil, addr)
	} else {
		a.conn, err = net.ListenUDP("udp", addr)
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}
func (a *application) read(limit int) ([]byte, error) {
	b := make([]byte, limit+1)
	n, peer, err := a.conn.ReadFromUDP(b)
	if err != nil {
		return nil, err
	}
	if !a.exit && n > 0 && n <= limit {
		a.mu.Lock()
		a.peer = peer
		a.mu.Unlock()
	}
	return b[:n], nil
}
func (a *application) write(b []byte, timeout time.Duration) error {
	// Serialize the write deadline and peer snapshot, independent of carrier locks.
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = a.conn.SetWriteDeadline(time.Now().Add(timeout))
	if a.exit {
		_, err := a.conn.Write(b)
		return err
	}
	if a.peer == nil {
		return errors.New("no application peer yet")
	}
	_, err := a.conn.WriteToUDP(b, a.peer)
	return err
}
func (a *application) close() { _ = a.conn.Close() }
