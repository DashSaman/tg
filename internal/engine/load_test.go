package engine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"hash/crc32"
	"io"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/pki"
)

var loadEnabled = flag.Bool("tg.load", false, "opt-in real encrypted loopback load test")
var loadDuration = flag.Duration("tg.duration", 10*time.Second, "measurement duration, excluding warmup")
var loadRate = flag.Float64("tg.mbps", 350, "offered aggregate application Mbps")
var loadCarrier = flag.String("tg.carrier", "quic", "quic, https or both")
var loadDirection = flag.String("tg.direction", "forward", "forward, reverse or duplex")
var loadFault = flag.String("tg.fault", "none", "none or cycle (requires both and >=36s)")
var loadReport = flag.String("tg.report", "", "JSON receipt relative to repository root or absolute")
var loadMinimum = flag.Float64("tg.min-mbps", 300, "minimum receiver Mbps; zero disables rate gate")
var loadPacket = flag.Int("tg.packet", 1400, "application UDP payload bytes")
var loadLoss = flag.Float64("tg.loss", 0, "deterministic outer UDP datagram loss percent, 0..10")
var loadQueue = flag.Int("tg.queue-frames", 0, "override configured frame queue for controlled experiments")
var loadReadBuffer = flag.Int("tg.read-buffer", 0, "request application socket receive buffer for controlled experiments")

type loadReceiver struct {
	Packets                       uint64    `json:"packets"`
	Bytes                         uint64    `json:"bytes"`
	Duplicates                    uint64    `json:"duplicates"`
	Corrupt                       uint64    `json:"corrupt"`
	Late                          uint64    `json:"late"`
	SourceChanges                 uint64    `json:"source_changes"`
	Mbps                          float64   `json:"received_mbps"`
	LossPercent                   float64   `json:"loss_percent"`
	SecondMbps                    []float64 `json:"second_mbps"`
	Bins100ms                     []uint64  `json:"packets_per_100ms"`
	P50MS, P95MS, P99MS, MaxGapMS float64
	seen                          []uint64
	latency                       [10001]uint64
	last                          time.Time
}
type loadSample struct {
	Seconds    float64  `json:"seconds"`
	Edge       Snapshot `json:"edge"`
	Exit       Snapshot `json:"exit"`
	HeapBytes  uint64   `json:"heap_bytes"`
	Goroutines int      `json:"goroutines"`
}
type loadEvent struct {
	Seconds float64 `json:"seconds"`
	Action  string  `json:"action"`
}

// This opt-in test exercises real UDP/TLS/HTTP2/QUIC, not a codec microbenchmark.
// All listeners are loopback and no host network rules are modified.
func TestLocalLoad(t *testing.T) {
	if !*loadEnabled {
		t.Skip("enable with -tg.load")
	}
	if *loadDuration < 3*time.Second || *loadDuration > time.Hour || *loadRate <= 0 || *loadRate > 2000 || *loadPacket < 64 || *loadPacket > 8192 || *loadLoss < 0 || *loadLoss > 10 || (*loadLoss > 0 && *loadCarrier == "https") {
		t.Fatal("invalid bounded load parameters")
	}
	if *loadCarrier != "quic" && *loadCarrier != "https" && *loadCarrier != "both" {
		t.Fatal("invalid carrier")
	}
	if *loadDirection != "forward" && *loadDirection != "reverse" && *loadDirection != "duplex" {
		t.Fatal("invalid direction")
	}
	if *loadFault != "none" && (*loadFault != "cycle" || *loadCarrier != "both" || *loadDuration < 36*time.Second) {
		t.Fatal("cycle needs both carriers and >=36s")
	}
	dir := filepath.Join(t.TempDir(), "pki")
	if err := pki.Generate(dir, "localhost"); err != nil {
		t.Fatal(err)
	}
	sink, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	_ = sink.SetReadBuffer(4 << 20)
	_ = sink.SetWriteBuffer(4 << 20)
	c := config.Defaults()
	if *loadQueue > 0 {
		c.QueueFrames = *loadQueue
	}
	c.Role = "exit"
	c.SessionID = strings.Repeat("3", 32)
	c.Metrics = ""
	c.AppAddress = sink.LocalAddr().String()
	c.MaxPacketBytes = max(2048, *loadPacket)
	c.TLS.CA = filepath.Join(dir, "ca.crt")
	c.TLS.Cert = filepath.Join(dir, "server.crt")
	c.TLS.Key = filepath.Join(dir, "server.key")
	c.TokenFile = filepath.Join(dir, "web.token")
	if *loadCarrier == "quic" {
		c.Carriers = c.Carriers[1:]
	} else if *loadCarrier == "https" {
		c.Carriers = c.Carriers[:1]
	}
	for i := range c.Carriers {
		c.Carriers[i].Address = "127.0.0.1:0"
	}
	ex, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exDone := make(chan error, 1)
	go func() { exDone <- ex.Run(ctx) }()
	until(t, func() bool { return len(ex.Snapshot().Addresses) == len(c.Carriers) })
	ec := c
	ec.Role = "edge"
	ec.AppAddress = "127.0.0.1:0"
	ec.TLS.Cert = filepath.Join(dir, "edge.crt")
	ec.TLS.Key = filepath.Join(dir, "edge.key")
	ec.Carriers = append([]config.Carrier{}, c.Carriers...)
	for i := range ec.Carriers {
		ec.Carriers[i].Address = ex.Snapshot().Addresses[ec.Carriers[i].Name]
	}
	var udpDrop *atomic.Bool
	var gate *loadTCPGate
	if *loadFault != "none" || *loadLoss > 0 {
		for i := range ec.Carriers {
			p := &ec.Carriers[i]
			if p.Kind == "quic" {
				proxy, drop := udpProxyWithLoss(t, p.Address, *loadLoss)
				defer proxy.Close()
				udpDrop = drop
				p.Address = proxy.LocalAddr().String()
			} else if *loadFault != "none" {
				gate = newLoadTCPGate(t, p.Address)
				defer gate.close()
				p.Address = gate.listener.Addr().String()
			}
		}
	}
	edge, err := New(ec)
	if err != nil {
		t.Fatal(err)
	}
	edDone := make(chan error, 1)
	go func() { edDone <- edge.Run(ctx) }()
	until(t, func() bool {
		return edge.Snapshot().HealthyCarriers == len(c.Carriers) && ex.Snapshot().HealthyCarriers == len(c.Carriers)
	})
	if *loadReadBuffer > 0 {
		for _, e := range []*Engine{edge, ex} {
			if err := e.app.conn.SetReadBuffer(*loadReadBuffer); err != nil {
				t.Fatal(err)
			}
		}
	}
	appAddr, err := net.ResolveUDPAddr("udp", edge.Snapshot().AppAddress)
	if err != nil {
		t.Fatal(err)
	}
	app, err := net.DialUDP("udp", nil, appAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	_ = app.SetReadBuffer(4 << 20)
	_ = app.SetWriteBuffer(4 << 20)
	// Prime both application return addresses, outside the measurement window.
	_, _ = app.Write([]byte("prime"))
	_ = sink.SetReadDeadline(time.Now().Add(3 * time.Second))
	prime := make([]byte, 64)
	_, returnAddr, err := sink.ReadFromUDP(prime)
	if err != nil {
		t.Fatal("prime", err)
	}
	_ = sink.SetReadDeadline(time.Time{})
	start := time.Now().Add(2 * time.Second)
	end := start.Add(*loadDuration)
	finish := end.Add(300 * time.Millisecond)
	bitCount := uint64(*loadRate*1e6/8/float64(*loadPacket)*(*loadDuration+4*time.Second).Seconds()) + 4096
	if bitCount > 100_000_000 {
		t.Fatal("receipt tracking limit exceeded; reduce rate/duration or increase packet size")
	}
	receive := func(conn *net.UDPConn, ch chan<- *loadReceiver) {
		result := loadReceiver{seen: make([]uint64, (bitCount+63)/64), SecondMbps: make([]float64, int(math.Ceil(loadDuration.Seconds()))), Bins100ms: make([]uint64, int(math.Ceil(loadDuration.Seconds()*10)))}
		buffer := make([]byte, *loadPacket+1)
		var source netip.AddrPort
		_ = conn.SetReadDeadline(finish)
		for {
			n, from, e := conn.ReadFromUDPAddrPort(buffer)
			if e != nil {
				break
			}
			now := time.Now()
			if source.IsValid() && from != source {
				result.SourceChanges++
			}
			source = from
			if n != *loadPacket {
				if now.After(start) {
					result.Corrupt++
				}
				continue
			}
			stamp := time.Duration(int64(binary.BigEndian.Uint64(buffer[8:16])))
			if stamp < 0 || stamp >= *loadDuration {
				continue
			}
			if now.After(end) {
				result.Late++
				continue
			}
			if now.Before(start) {
				continue
			}
			id := binary.BigEndian.Uint64(buffer[:8])
			if id >= bitCount || crc32.ChecksumIEEE(buffer[20:n]) != binary.BigEndian.Uint32(buffer[16:20]) {
				result.Corrupt++
				continue
			}
			word, bit := id/64, uint64(1)<<(id%64)
			if result.seen[word]&bit != 0 {
				result.Duplicates++
				continue
			}
			result.seen[word] |= bit
			result.Packets++
			result.Bytes += uint64(n)
			elapsed := now.Sub(start)
			result.SecondMbps[min(len(result.SecondMbps)-1, int(elapsed/time.Second))] += float64(n) * 8 / 1e6
			result.Bins100ms[min(len(result.Bins100ms)-1, int(elapsed/(100*time.Millisecond)))]++
			latency := elapsed - stamp
			if latency < 0 {
				result.Corrupt++
				continue
			}
			result.latency[min(10000, int(latency/(100*time.Microsecond)))]++
			if !result.last.IsZero() {
				result.MaxGapMS = max(result.MaxGapMS, float64(now.Sub(result.last))/float64(time.Millisecond))
			}
			result.last = now
		}
		quantile := func(q float64) float64 {
			goal := uint64(math.Ceil(float64(result.Packets) * q))
			var total uint64
			for i, n := range result.latency {
				total += n
				if total >= goal {
					return float64(i) / 10
				}
			}
			return 1000
		}
		result.P50MS = quantile(.5)
		result.P95MS = quantile(.95)
		result.P99MS = quantile(.99)
		result.Mbps = float64(result.Bytes) * 8 / loadDuration.Seconds() / 1e6
		result.seen = nil
		ch <- &result
	}
	forwardCh, reverseCh := make(chan *loadReceiver, 1), make(chan *loadReceiver, 1)
	go receive(sink, forwardCh)
	go receive(app, reverseCh)
	var forwardSent, reverseSent uint64
	var sendErrors atomic.Uint64
	var sendWG sync.WaitGroup
	send := func(conn *net.UDPConn, target *net.UDPAddr, mbps float64, count *uint64) {
		defer sendWG.Done()
		buffer := make([]byte, *loadPacket)
		for i := 20; i < len(buffer); i++ {
			buffer[i] = byte(i*31 + 17)
		}
		binary.BigEndian.PutUint32(buffer[16:20], crc32.ChecksumIEEE(buffer[20:]))
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		var seq uint64
		origin := start.Add(-2 * time.Second)
		for now := range ticker.C {
			if !now.Before(end) {
				return
			}
			goal := uint64(now.Sub(origin).Seconds() * mbps * 1e6 / 8 / float64(*loadPacket))
			limit := min(goal, seq+128)
			for seq < limit {
				seq++
				sentAt := time.Now()
				if !sentAt.Before(end) {
					return
				}
				binary.BigEndian.PutUint64(buffer[:8], seq)
				binary.BigEndian.PutUint64(buffer[8:16], uint64(sentAt.Sub(start)))
				var err error
				if target == nil {
					_, err = conn.Write(buffer)
				} else {
					_, err = conn.WriteToUDP(buffer, target)
				}
				if err != nil {
					sendErrors.Add(1)
				} else if !sentAt.Before(start) {
					*count++
				}
			}
		}
	}
	perDirection := *loadRate
	if *loadDirection == "duplex" {
		perDirection /= 2
	}
	if *loadDirection != "reverse" {
		sendWG.Add(1)
		go send(app, nil, perDirection, &forwardSent)
	}
	if *loadDirection != "forward" {
		sendWG.Add(1)
		go send(sink, returnAddr, perDirection, &reverseSent)
	}
	monitorDone := make(chan struct{})
	var samples []loadSample
	var events []loadEvent
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		event := 0
		actions := []struct {
			at     float64
			action string
			apply  func()
		}{}
		if *loadFault == "cycle" {
			actions = []struct {
				at     float64
				action string
				apply  func()
			}{{5, "udp_blackhole", func() { udpDrop.Store(true) }}, {11, "udp_restore", func() { udpDrop.Store(false) }}, {16, "https_disconnect", func() { gate.setDrop(true) }}, {21, "https_restore", func() { gate.setDrop(false) }}, {25, "both_blackhole", func() { udpDrop.Store(true); gate.setDrop(true) }}, {28, "both_restore", func() { udpDrop.Store(false); gate.setDrop(false) }}}
		}
		for now := range ticker.C {
			seconds := now.Sub(start).Seconds()
			if now.After(end) {
				return
			}
			if seconds < 0 {
				continue
			}
			for event < len(actions) && seconds >= actions[event].at {
				actions[event].apply()
				events = append(events, loadEvent{Seconds: seconds, Action: actions[event].action})
				event++
			}
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			samples = append(samples, loadSample{Seconds: seconds, Edge: edge.Snapshot(), Exit: ex.Snapshot(), HeapBytes: memory.HeapAlloc, Goroutines: runtime.NumGoroutine()})
		}
	}()
	var memoryBefore, memoryAfter runtime.MemStats
	runtime.ReadMemStats(&memoryBefore)
	sendWG.Wait()
	<-monitorDone
	forward := <-forwardCh
	reverse := <-reverseCh
	runtime.ReadMemStats(&memoryAfter)
	loss := func(sent, received uint64) float64 {
		if sent == 0 {
			return 0
		}
		return 100 * float64(int64(sent)-int64(received)) / float64(sent)
	}
	forward.LossPercent = loss(forwardSent, forward.Packets)
	reverse.LossPercent = loss(reverseSent, reverse.Packets)
	beforeStopEdge, beforeStopExit := edge.Snapshot(), ex.Snapshot()
	cancel()
	for _, ch := range []chan error{edDone, exDone} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("load shutdown leaked workers")
		}
	}
	total := forward.Mbps + reverse.Mbps
	var minimumWindow *float64
	if *loadDuration >= 10*time.Second {
		minimum := math.Inf(1)
		for i := 0; i+10 <= len(forward.SecondMbps); i++ {
			sum := 0.0
			for j := i; j < i+10; j++ {
				sum += forward.SecondMbps[j] + reverse.SecondMbps[j]
			}
			minimum = min(minimum, sum/10)
		}
		minimumWindow = &minimum
	}
	if *loadFault != "none" {
		if beforeStopEdge.HealthyCarriers == 0 || beforeStopExit.HealthyCarriers == 0 {
			t.Error("carriers did not recover after outage")
		}
		for i := max(0, len(forward.SecondMbps)-5); i < len(forward.SecondMbps); i++ {
			if forward.SecondMbps[i]+reverse.SecondMbps[i] < *loadMinimum {
				t.Error("post-recovery window below target", i)
			}
		}
	}
	receipt := map[string]any{"scope": "same-host encrypted UDP application relay; no WireGuard, WAN or DPI", "carrier": *loadCarrier, "direction": *loadDirection, "fault": *loadFault, "duration_seconds": loadDuration.Seconds(), "packet_bytes": *loadPacket, "offered_mbps": *loadRate, "forward_sent_packets": forwardSent, "reverse_sent_packets": reverseSent, "send_errors": sendErrors.Load(), "forward": forward, "reverse": reverse, "aggregate_receiver_mbps": total, "edge": beforeStopEdge, "exit": beforeStopExit, "samples": samples, "events": events, "go_version": runtime.Version(), "cpus": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "allocated_bytes": memoryAfter.TotalAlloc - memoryBefore.TotalAlloc, "gc_cycles": memoryAfter.NumGC - memoryBefore.NumGC, "heap_end_bytes": memoryAfter.HeapAlloc}
	receipt["outer_udp_loss_percent"] = *loadLoss
	receipt["queue_frames"] = c.QueueFrames
	receipt["requested_application_read_buffer"] = *loadReadBuffer
	receipt["minimum_10s_receiver_mbps"] = minimumWindow
	receipt["warmup_seconds"] = 2
	if *loadReport != "" {
		path := *loadReport
		if !filepath.IsAbs(path) {
			path = filepath.Join("../..", path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(receipt, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("carrier=%s direction=%s aggregate_receiver=%.2f Mbps forward_loss=%.3f%% reverse_loss=%.3f%% send_errors=%d", *loadCarrier, *loadDirection, total, forward.LossPercent, reverse.LossPercent, sendErrors.Load())
	if forward.Corrupt+reverse.Corrupt+forward.Duplicates+reverse.Duplicates+forward.SourceChanges+reverse.SourceChanges != 0 {
		t.Fatal("corrupted or duplicate application data")
	}
	if *loadFault == "none" && *loadMinimum > 0 && total < *loadMinimum {
		t.Fatalf("receiver %.2f below target %.2f Mbps", total, *loadMinimum)
	}
	if *loadFault == "none" && minimumWindow != nil && *minimumWindow < *loadMinimum {
		t.Fatal("rolling receiver window below target", *minimumWindow)
	}
	if *loadFault == "none" && *loadLoss == 0 && max(forward.LossPercent, reverse.LossPercent) > 1 {
		t.Fatal("steady-state receiver loss above 1%")
	}
}

type loadTCPGate struct {
	listener    net.Listener
	target      string
	mu          sync.Mutex
	drop        bool
	connections map[net.Conn]bool
}

func newLoadTCPGate(t *testing.T, target string) *loadTCPGate {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &loadTCPGate{listener: listener, target: target, connections: make(map[net.Conn]bool)}
	go func() {
		for {
			in, e := listener.Accept()
			if e != nil {
				return
			}
			go g.copy(in)
		}
	}()
	return g
}
func (g *loadTCPGate) copy(in net.Conn) {
	defer in.Close()
	g.mu.Lock()
	if g.drop {
		g.mu.Unlock()
		return
	}
	g.connections[in] = true
	g.mu.Unlock()
	defer func() { g.mu.Lock(); delete(g.connections, in); g.mu.Unlock() }()
	out, err := net.DialTimeout("tcp", g.target, time.Second)
	if err != nil {
		return
	}
	defer out.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(out, in); _ = out.Close(); close(done) }()
	_, _ = io.Copy(in, out)
	_ = in.Close()
	<-done
}
func (g *loadTCPGate) setDrop(drop bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.drop = drop
	if drop {
		for conn := range g.connections {
			_ = conn.Close()
		}
	}
}
func (g *loadTCPGate) close() { _ = g.listener.Close(); g.setDrop(true) }
