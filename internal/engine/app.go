package engine

import (
	"errors"
	"github.com/DashSaman/tg/internal/config"
	"golang.org/x/net/ipv4"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type application struct {
	conn                *net.UDPConn
	exit                bool
	mu                  sync.Mutex // peer only; never held across socket I/O
	writeMu             sync.Mutex
	peer                netip.AddrPort
	readBuffer          []byte
	batch               applicationBatchConn
	writeMessages       [32]ipv4.Message
	writeBuffers        [32][1][]byte
	readMessages        [32]ipv4.Message
	readBuffers         [32][1][]byte
	readStorage         []byte
	readNext, readCount int
	readError           error
	readOffset          int
	readSegment         int
	readControl         [32][64]byte
	groEnabled          bool // immutable after socket setup
	groBatches          atomic.Uint64
	groPackets          atomic.Uint64
	offload             applicationOffload
	gsoBatches          atomic.Uint64
	gsoPackets          atomic.Uint64
	gsoFallbacks        atomic.Uint64
}

type applicationBatchConn interface {
	ReadBatch([]ipv4.Message, int) (int, error)
	WriteBatch([]ipv4.Message, int) (int, error)
}

type applicationOffload struct {
	disabled bool // writer-owned; unsupported kernels permanently use sendmmsg
	storage  []byte
	control  [32][64]byte
	ends     [32]int // exclusive original-packet index for each syscall message
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
	a.setupReadBatch(c.MaxPacketBytes)
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
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	var peer net.Addr
	if !a.exit {
		a.mu.Lock()
		address := a.peer
		a.mu.Unlock()
		if !address.IsValid() {
			return 0, errors.New("no application peer yet")
		}
		peer = net.UDPAddrFromAddrPort(address)
	}
	_ = a.conn.SetWriteDeadline(time.Now().Add(timeout))
	defer func() {
		for i := range a.writeMessages {
			a.writeBuffers[i][0] = nil
			a.writeMessages[i].Addr = nil
			a.writeMessages[i].OOB = nil
		}
	}()
	sent := 0
	for sent < len(packets) {
		count := a.prepareWriteBatch(packets[sent:], peer)
		base := sent
		for group := 0; group < count; {
			n, err := a.batch.WriteBatch(a.writeMessages[group:count], 0)
			// x/net can return -1 on failure. Count only consumed messages,
			// translating GSO groups back to original datagrams.
			if n > 0 {
				for j := group; j < group+n; j++ {
					begin := 0
					if j > 0 {
						begin = a.offload.ends[j-1]
					}
					if len(a.writeMessages[j].OOB) > 0 {
						a.gsoBatches.Add(1)
						a.gsoPackets.Add(uint64(a.offload.ends[j] - begin))
					}
				}
				group += n
				sent = base + a.offload.ends[group-1]
			}
			if err != nil {
				if !a.offload.disabled && unsupportedApplicationGSO(err) {
					a.offload.disabled = true
					a.gsoFallbacks.Add(1)
					break // retry only the unsent suffix, without offload
				}
				return sent, err
			}
			if n == 0 {
				return sent, errors.New("empty application batch write")
			}
		}
	}
	return sent, nil
}

func (a *application) prepareWriteBatch(packets [][]byte, peer net.Addr) int {
	if !a.offload.disabled {
		if n := a.prepareOffloadedWrite(packets, peer); n > 0 {
			return n
		}
	}
	for i, p := range packets {
		a.writeBuffers[i][0] = p
		a.writeMessages[i].Addr = peer
		a.writeMessages[i].OOB = nil
		a.offload.ends[i] = i + 1
	}
	return len(packets)
}
func (a *application) read(limit int) ([]byte, error) {
	b := a.readBuffer[:limit+1]
	var n int
	var peer netip.AddrPort
	var err error
	if runtime.GOOS == "linux" {
		b, peer, err = a.readBatchPacket(limit)
		n = len(b)
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
	// Serialize writers and their deadlines, while peer updates remain independent
	// of a potentially backpressured socket write in the opposite direction.
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_ = a.conn.SetWriteDeadline(time.Now().Add(timeout))
	if a.exit {
		_, err := a.conn.Write(b)
		return err
	}
	a.mu.Lock()
	peer := a.peer
	a.mu.Unlock()
	if !peer.IsValid() {
		return errors.New("no application peer yet")
	}
	_, err := a.conn.WriteToUDPAddrPort(b, peer)
	return err
}
func (a *application) close() { _ = a.conn.Close() }
