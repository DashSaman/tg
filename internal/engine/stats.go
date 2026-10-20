package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type counters struct{ inputBytes, queuedBytes, outputBytes, wireSent, wireReceived, dropped, appErrors, malformed, reconnects, switches atomic.Uint64 }
type PathStatus struct {
	Name               string  `json:"name"`
	Healthy            bool    `json:"healthy"`
	RTTMilliseconds    float64 `json:"rtt_ms"`
	JitterMilliseconds float64 `json:"rtt_variation_ms"`
	QueuedFrames       int     `json:"queued_frames"`
}
type Snapshot struct {
	Scope           string            `json:"health_scope"`
	Addresses       map[string]string `json:"addresses"`
	AppAddress      string            `json:"app_address"`
	HealthyCarriers int               `json:"healthy_carriers"`
	ActiveCarrier   string            `json:"active_carrier"`
	Paths           []PathStatus      `json:"paths"`
	InputBytes      uint64            `json:"app_input_bytes"`
	QueuedBytes     uint64            `json:"app_queued_bytes"`
	OutputBytes     uint64            `json:"app_output_bytes"`
	WireSent        uint64            `json:"frame_sent_bytes"`
	WireReceived    uint64            `json:"frame_received_bytes"`
	Dropped         uint64            `json:"dropped_packets"`
	AppErrors       uint64            `json:"app_errors"`
	Malformed       uint64            `json:"malformed_frames"`
	Reconnects      uint64            `json:"reconnect_attempts"`
	Switches        uint64            `json:"carrier_switches"`
}

func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Snapshot{Scope: "authenticated_carrier_round_trip", Addresses: make(map[string]string), ActiveCarrier: e.active, Paths: []PathStatus{}}
	for k, v := range e.addresses {
		s.Addresses[k] = v
	}
	if e.app != nil {
		s.AppAddress = e.app.conn.LocalAddr().String()
	}
	now := time.Now()
	for _, p := range e.paths(now) {
		h := p.Healthy(now, e.cfg.Timeout())
		if h {
			s.HealthyCarriers++
		}
		s.Paths = append(s.Paths, PathStatus{Name: p.ID, Healthy: h, RTTMilliseconds: float64(p.RTT) / float64(time.Millisecond), JitterMilliseconds: float64(p.Jitter) / float64(time.Millisecond), QueuedFrames: p.Queued})
	}
	sort.Slice(s.Paths, func(i, j int) bool { return s.Paths[i].Name < s.Paths[j].Name })
	c := &e.counters
	s.InputBytes = c.inputBytes.Load()
	s.QueuedBytes = c.queuedBytes.Load()
	s.OutputBytes = c.outputBytes.Load()
	s.WireSent = c.wireSent.Load()
	s.WireReceived = c.wireReceived.Load()
	s.Dropped = c.dropped.Load()
	s.AppErrors = c.appErrors.Load()
	s.Malformed = c.malformed.Load()
	s.Reconnects = c.reconnects.Load()
	s.Switches = c.switches.Load()
	return s
}
func (e *Engine) serveMetrics(ctx context.Context) (func(), error) {
	listener, err := net.Listen("tcp", e.cfg.Metrics)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(e.Snapshot())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		s := e.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		if s.HealthyCarriers == 0 {
			w.WriteHeader(503)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Scope   string `json:"scope"`
			Healthy int    `json:"healthy_carriers"`
		}{s.Scope, s.HealthyCarriers})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		s := e.Snapshot()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "tg_healthy_carriers %d\ntg_app_input_bytes_total %d\ntg_app_queued_bytes_total %d\ntg_app_output_bytes_total %d\ntg_frame_sent_bytes_total %d\ntg_frame_received_bytes_total %d\ntg_dropped_packets_total %d\ntg_app_errors_total %d\ntg_malformed_frames_total %d\ntg_reconnect_attempts_total %d\ntg_carrier_switches_total %d\n", s.HealthyCarriers, s.InputBytes, s.QueuedBytes, s.OutputBytes, s.WireSent, s.WireReceived, s.Dropped, s.AppErrors, s.Malformed, s.Reconnects, s.Switches)
		for _, p := range s.Paths {
			h := 0
			if p.Healthy {
				h = 1
			}
			_, _ = fmt.Fprintf(w, "tg_carrier_healthy{carrier=%q} %d\ntg_carrier_rtt_seconds{carrier=%q} %g\ntg_carrier_rtt_variation_seconds{carrier=%q} %g\ntg_carrier_queued_frames{carrier=%q} %d\n", p.Name, h, p.Name, p.RTTMilliseconds/1000, p.Name, p.JitterMilliseconds/1000, p.Name, p.QueuedFrames)
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	var once sync.Once
	stop := func() { once.Do(func() { _ = srv.Close(); _ = listener.Close() }) }
	go func() { <-ctx.Done(); stop() }()
	go func() { _ = srv.Serve(listener) }()
	return stop, nil
}
