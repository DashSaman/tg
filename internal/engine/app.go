package engine

import (
	"errors"
	"github.com/DashSaman/tg/internal/config"
	"net"
	"net/netip"
	"sync"
	"time"
)

type application struct {
	conn       *net.UDPConn
	exit       bool
	mu         sync.Mutex
	peer       netip.AddrPort
	readBuffer []byte
}

func openApplication(c config.Config) (*application, error) {
	addr, err := udpAddress(c.AppAddress)
	if err != nil {
		return nil, err
	}
	a := &application{exit: c.Role == "exit", readBuffer: make([]byte, c.MaxPacketBytes+1)}
	if a.exit {
		a.conn, err = net.DialUDP("udp", nil, addr)
	} else {
		a.conn, err = net.ListenUDP("udp", addr)
	}
	if err != nil {
		return nil, err
	}
	// A per-socket hint, never a global sysctl change. The OS may clamp it.
	// Absorb short scheduler bursts independently of the bounded frame queue.
	_ = a.conn.SetReadBuffer(1 << 20)
	return a, nil
}
func (a *application) read(limit int) ([]byte, error) {
	b := a.readBuffer[:limit+1]
	n, peer, err := a.conn.ReadFromUDPAddrPort(b)
	if err != nil {
		return nil, err
	}
	if !a.exit && n > 0 && n <= limit {
		a.mu.Lock()
		a.peer = peer
		a.mu.Unlock()
	}
	if n == 0 || n > limit {
		return b[:n], nil
	}
	// The asynchronous transmit queue owns this exact-size copy. Never enqueue
	// the reusable read buffer, which the next socket read will overwrite.
	return append([]byte(nil), b[:n]...), nil
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
	if !a.peer.IsValid() {
		return errors.New("no application peer yet")
	}
	_, err := a.conn.WriteToUDPAddrPort(b, a.peer)
	return err
}
func (a *application) close() { _ = a.conn.Close() }
