package engine

import (
	"errors"
	"github.com/DashSaman/tg/internal/config"
	"golang.org/x/net/ipv4"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"time"
)

type application struct {
	conn                *net.UDPConn
	exit                bool
	mu                  sync.Mutex
	peer                netip.AddrPort
	readBuffer          []byte
	batch               *ipv4.PacketConn
	writeMessages       [32]ipv4.Message
	writeBuffers        [32][1][]byte
	readMessages        [32]ipv4.Message
	readBuffers         [32][1][]byte
	readStorage         []byte
	readNext, readCount int
	readError           error
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
	_ = a.conn.SetReadBuffer(4 << 20)
	a.batch = ipv4.NewPacketConn(a.conn)
	for i := range a.writeMessages {
		a.writeMessages[i].Buffers = a.writeBuffers[i][:]
	}
	if runtime.GOOS == "linux" {
		a.readStorage = make([]byte, len(a.readMessages)*(c.MaxPacketBytes+1))
		for i := range a.readMessages {
			a.readBuffers[i][0] = a.readStorage[i*(c.MaxPacketBytes+1) : (i+1)*(c.MaxPacketBytes+1)]
			a.readMessages[i].Buffers = a.readBuffers[i][:]
		}
	}
	return a, nil
}

// Linux uses sendmmsg; other platforms may consume one message per call.
// The caller retains every buffer until this synchronous method returns.
func (a *application) writeBatch(packets [][]byte, timeout time.Duration) (int, error) {
	if len(packets) == 1 {
		err := a.write(packets[0], timeout)
		if err != nil {
			return 0, err
		}
		return 1, nil
	}
	if len(packets) == 0 || len(packets) > len(a.writeMessages) {
		return 0, errors.New("invalid application batch")
	}
	if runtime.GOOS != "linux" {
		for i, p := range packets {
			if err := a.write(p, timeout); err != nil {
				return i, err
			}
		}
		return len(packets), nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var peer net.Addr
	if !a.exit {
		if !a.peer.IsValid() {
			return 0, errors.New("no application peer yet")
		}
		peer = net.UDPAddrFromAddrPort(a.peer)
	}
	_ = a.conn.SetWriteDeadline(time.Now().Add(timeout))
	for i, p := range packets {
		a.writeBuffers[i][0] = p
		a.writeMessages[i].Addr = peer
	}
	defer func() {
		for i := range packets {
			a.writeBuffers[i][0] = nil
			a.writeMessages[i].Addr = nil
		}
	}()
	sent := 0
	for sent < len(packets) {
		n, err := a.batch.WriteBatch(a.writeMessages[sent:len(packets)], 0)
		// x/net forwards the raw Linux syscall's -1 on an error. It must
		// not subtract a previously consumed prefix from our result.
		if n > 0 {
			sent += n
		}
		if err != nil {
			return sent, err
		}
		if n == 0 {
			return sent, errors.New("empty application batch write")
		}
	}
	return sent, nil
}
func (a *application) read(limit int) ([]byte, error) {
	b := a.readBuffer[:limit+1]
	var n int
	var peer netip.AddrPort
	var err error
	if runtime.GOOS == "linux" {
		if a.readNext == a.readCount {
			if a.readError != nil {
				err = a.readError
				a.readError = nil
				return nil, err
			}
			a.readNext = 0
			a.readCount, a.readError = a.batch.ReadBatch(a.readMessages[:], 0)
			if a.readCount <= 0 {
				err = a.readError
				a.readError = nil
				a.readCount = 0
				if err == nil {
					err = errors.New("empty application read batch")
				}
				return nil, err
			}
		}
		m := &a.readMessages[a.readNext]
		a.readNext++
		n = m.N
		b = m.Buffers[0]
		if addr, ok := m.Addr.(*net.UDPAddr); ok {
			peer = addr.AddrPort()
		}
		m.Addr = nil
	} else {
		n, peer, err = a.conn.ReadFromUDPAddrPort(b)
	}
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
