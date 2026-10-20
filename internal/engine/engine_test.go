package engine

import (
	"bytes"
	"context"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/pki"
	"github.com/DashSaman/tg/internal/session"
	"math/rand"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func until(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

type blockedWire struct {
	started, closed chan struct{}
	once            sync.Once
}

func (w *blockedWire) Send([]byte) error {
	w.once.Do(func() { close(w.started) })
	<-w.closed
	return context.Canceled
}
func (w *blockedWire) Receive(ctx context.Context) ([]byte, error) {
	<-w.closed
	return nil, context.Canceled
}
func (w *blockedWire) Close() error {
	select {
	case <-w.closed:
	default:
		close(w.closed)
	}
	return nil
}

func TestCancellationInterruptsBlockedCarrierWrite(t *testing.T) {
	c := config.Defaults()
	e := &Engine{cfg: c}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &blockedWire{started: make(chan struct{}), closed: make(chan struct{})}
	l := newLink(e, ctx, c.Carriers[0], w)
	done := make(chan struct{})
	go func() { l.run(); close(done) }()
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("probe write never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked writer prevented cancellation")
	}
}
func TestCarrierOutagePreservesApplicationSocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if err := pki.Generate(dir, "localhost"); err != nil {
		t.Fatal(err)
	}
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	var latest atomic.Value
	go func() {
		b := make([]byte, 9000)
		for {
			n, a, e := echo.ReadFromUDP(b)
			if e != nil {
				return
			}
			latest.Store(a.String())
			_, _ = echo.WriteToUDP(b[:n], a)
		}
	}()
	c := config.Defaults()
	c.Role = "exit"
	c.SessionID = strings.Repeat("1", 32)
	c.AppAddress = echo.LocalAddr().String()
	c.Metrics = ""
	c.ProbeIntervalMS = 40
	c.ProbeTimeoutMS = 240
	c.HoldDownMS = 200
	c.TLS.CA = filepath.Join(dir, "ca.crt")
	c.TLS.Cert = filepath.Join(dir, "server.crt")
	c.TLS.Key = filepath.Join(dir, "server.key")
	c.TokenFile = filepath.Join(dir, "web.token")
	for i := range c.Carriers {
		c.Carriers[i].Address = "127.0.0.1:0"
	}
	c.Carriers[0].CostMS = 100
	exit, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exitDone := make(chan error, 1)
	go func() { exitDone <- exit.Run(ctx) }()
	until(t, func() bool { return len(exit.Snapshot().Addresses) == 2 })
	edgeCfg := c
	edgeCfg.Role = "edge"
	edgeCfg.AppAddress = "127.0.0.1:0"
	edgeCfg.TLS.Cert = filepath.Join(dir, "edge.crt")
	edgeCfg.TLS.Key = filepath.Join(dir, "edge.key")
	edgeCfg.Carriers = append([]config.Carrier{}, c.Carriers...)
	for i := range edgeCfg.Carriers {
		edgeCfg.Carriers[i].Address = exit.Snapshot().Addresses[edgeCfg.Carriers[i].Name]
	}
	proxy, drop := udpProxy(t, edgeCfg.Carriers[1].Address)
	defer proxy.Close()
	edgeCfg.Carriers[1].Address = proxy.LocalAddr().String()
	edge, err := New(edgeCfg)
	if err != nil {
		t.Fatal(err)
	}
	edgeDone := make(chan error, 1)
	go func() { edgeDone <- edge.Run(ctx) }()
	until(t, func() bool { return edge.Snapshot().HealthyCarriers == 2 })
	app, err := net.Dial("udp", edge.Snapshot().AppAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	exchange := func(payload []byte) {
		t.Helper()
		if err := app.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := app.Write(payload); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 9000)
		n, e := app.Read(b)
		if e != nil || !bytes.Equal(b[:n], payload) {
			t.Fatalf("data integrity: bytes=%d err=%v", n, e)
		}
	}
	exchange(bytes.Repeat([]byte{3, 0, 99, 17}, 500))
	before := latest.Load()
	if edge.Snapshot().ActiveCarrier != "udp" {
		t.Fatal("QUIC was not primary", edge.Snapshot())
	}
	drop.Store(true)
	until(t, func() bool { return edge.Snapshot().HealthyCarriers == 1 })
	exchange([]byte("still-the-same-inner-session"))
	if edge.Snapshot().ActiveCarrier != "web" {
		t.Fatal("failed to use HTTPS")
	}
	if latest.Load() != before {
		t.Fatal("carrier switch changed backend UDP source socket")
	}
	drop.Store(false)
	until(t, func() bool { return edge.Snapshot().HealthyCarriers == 2 })
	// Probe and reconnect recovery must survive repeated outage, not just startup.
	exchange([]byte("recovered"))
	drop.Store(true)
	until(t, func() bool { return edge.Snapshot().HealthyCarriers == 1 })
	exchange([]byte("second-outage"))
	cancel()
	for _, ch := range []chan error{edgeDone, exitDone} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("shutdown leaked workers")
		}
	}
}

func TestQueueAdmissionNeverEnqueuesPartialPacket(t *testing.T) {
	l := &link{data: make(chan session.Frame, 8)}
	f, err := session.Fragment(make([]byte, 2048), 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		l.data <- f[0]
	}
	if l.enqueue(f) || len(l.data) != 7 {
		t.Fatal("partial packet admitted under queue pressure")
	}
	<-l.data
	if !l.enqueue(f) || len(l.data) != 8 {
		t.Fatal("whole packet did not fit exact capacity")
	}
	l.closed.Store(true)
	<-l.data
	<-l.data
	if l.enqueue(f) {
		t.Fatal("closed link admitted data")
	}
}

func TestQueueWaitAbsorbsBurstWithoutPartialAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &link{ctx: ctx, data: make(chan session.Frame, 4), space: make(chan struct{}, 1)}
	for i := 0; i < 4; i++ {
		l.data <- session.Frame{ID: 99}
	}
	frames, _ := session.Fragment(make([]byte, 2048), 1)
	done := make(chan bool, 1)
	go func() { done <- l.enqueueWait(ctx, frames, time.Second) }()
	<-l.data
	l.notifySpace()
	select {
	case <-done:
		t.Fatal("partial capacity admitted a fragmented packet")
	case <-time.After(5 * time.Millisecond):
	}
	<-l.data
	l.notifySpace()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("bounded burst was treated as persistent congestion")
		}
	case <-time.After(time.Second):
		t.Fatal("space notification lost")
	}
	if len(l.data) != 4 {
		t.Fatal("whole packet did not occupy exactly two slots")
	}
	cancel()
	if l.enqueueWait(ctx, frames, time.Second) {
		t.Fatal("cancelled wait admitted a packet")
	}
}

func TestPeerRestartResetsDedupWithoutReplacingExitSocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if err := pki.Generate(dir, "localhost"); err != nil {
		t.Fatal(err)
	}
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	var source atomic.Value
	go func() {
		b := make([]byte, 9000)
		for {
			n, a, e := echo.ReadFromUDP(b)
			if e != nil {
				return
			}
			source.Store(a.String())
			_, _ = echo.WriteToUDP(b[:n], a)
		}
	}()
	c := config.Defaults()
	c.Role = "exit"
	c.SessionID = strings.Repeat("2", 32)
	c.AppAddress = echo.LocalAddr().String()
	c.Metrics = ""
	c.ProbeIntervalMS = 40
	c.ProbeTimeoutMS = 240
	c.TLS.CA = filepath.Join(dir, "ca.crt")
	c.TLS.Cert = filepath.Join(dir, "server.crt")
	c.TLS.Key = filepath.Join(dir, "server.key")
	c.TokenFile = filepath.Join(dir, "web.token")
	c.Carriers = c.Carriers[:1]
	c.Carriers[0].Address = "127.0.0.1:0"
	ex, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ex.Run(ctx) }()
	until(t, func() bool { return len(ex.Snapshot().Addresses) == 1 })
	var original any
	for i := 0; i < 2; i++ {
		edgeCfg := c
		edgeCfg.Role = "edge"
		edgeCfg.AppAddress = "127.0.0.1:0"
		edgeCfg.TLS.Cert = filepath.Join(dir, "edge.crt")
		edgeCfg.TLS.Key = filepath.Join(dir, "edge.key")
		edgeCfg.Carriers = append([]config.Carrier{}, c.Carriers...)
		edgeCfg.Carriers[0].Address = ex.Snapshot().Addresses["web"]
		edge, err := New(edgeCfg)
		if err != nil {
			t.Fatal(err)
		}
		ec, stop := context.WithCancel(ctx)
		ed := make(chan error, 1)
		go func() { ed <- edge.Run(ec) }()
		until(t, func() bool { return edge.Snapshot().HealthyCarriers == 1 })
		app, err := net.Dial("udp", edge.Snapshot().AppAddress)
		if err != nil {
			t.Fatal(err)
		}
		_ = app.SetDeadline(time.Now().Add(2 * time.Second))
		payload := []byte{byte(i + 1), 9, 8, 7}
		_, _ = app.Write(payload)
		b := make([]byte, 16)
		n, err := app.Read(b)
		_ = app.Close()
		if err != nil || !bytes.Equal(b[:n], payload) {
			t.Fatalf("epoch %d packet lost: %v", i, err)
		}
		if i == 0 {
			original = source.Load()
		} else if source.Load() != original {
			t.Fatal("peer restart replaced exit application socket")
		}
		stop()
		select {
		case err := <-ed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("edge did not stop")
		}
		until(t, func() bool { return ex.Snapshot().HealthyCarriers == 0 })
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exit did not stop")
	}
}

func udpProxy(t *testing.T, target string) (*net.UDPConn, *atomic.Bool) {
	return udpProxyWithLoss(t, target, 0)
}
func udpProxyWithLoss(t *testing.T, target string, lossPercent float64) (*net.UDPConn, *atomic.Bool) {
	t.Helper()
	remote, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		t.Fatal(err)
	}
	p, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	drop := &atomic.Bool{}
	go func() {
		b := make([]byte, 65536)
		var client *net.UDPAddr
		random := rand.New(rand.NewSource(7))
		for {
			n, a, e := p.ReadFromUDP(b)
			if e != nil {
				return
			}
			if drop.Load() || (lossPercent > 0 && random.Float64()*100 < lossPercent) {
				continue
			}
			if a.String() == remote.String() {
				if client != nil {
					_, _ = p.WriteToUDP(b[:n], client)
				}
			} else {
				client = a
				_, _ = p.WriteToUDP(b[:n], remote)
			}
		}
	}()
	return p, drop
}
