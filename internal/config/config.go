package config

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type TLS struct {
	CA         string `json:"ca"`
	Cert       string `json:"cert"`
	Key        string `json:"key"`
	ServerName string `json:"server_name"`
	PeerName   string `json:"peer_name"`
}
type Carrier struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Address string `json:"address"`
	Path    string `json:"path,omitempty"`
	CostMS  int    `json:"cost_ms"`
}
type Config struct {
	Role            string    `json:"role"`
	SessionID       string    `json:"session_id"`
	AppAddress      string    `json:"app_address"`
	Metrics         string    `json:"metrics"`
	TLS             TLS       `json:"tls"`
	TokenFile       string    `json:"token_file"`
	Carriers        []Carrier `json:"carriers"`
	ProbeIntervalMS int       `json:"probe_interval_ms"`
	ProbeTimeoutMS  int       `json:"probe_timeout_ms"`
	HoldDownMS      int       `json:"hold_down_ms"`
	AutoUpgrade     bool      `json:"auto_upgrade"`
	QueueFrames     int       `json:"queue_frames"`
	MaxPacketBytes  int       `json:"max_packet_bytes"`
}

func Defaults() Config {
	return Config{AppAddress: "127.0.0.1:31820", Metrics: "127.0.0.1:19090", TLS: TLS{CA: "ca.crt", Cert: "edge.crt", Key: "edge.key", ServerName: "localhost", PeerName: "tg-edge"}, TokenFile: "web.token", ProbeIntervalMS: 1000, ProbeTimeoutMS: 4000, HoldDownMS: 5000, QueueFrames: 1024, MaxPacketBytes: 2048, Carriers: []Carrier{{Name: "web", Kind: "https", Address: "127.0.0.1:8443", Path: "/private-channel", CostMS: 20}, {Name: "udp", Kind: "quic-stream", Address: "127.0.0.1:8444"}}}
}

func validAddress(address string, loopback bool) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return false
	}
	if loopback {
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
	return host != ""
}

func (c Config) Validate() error {
	if c.Role != "edge" && c.Role != "exit" {
		return errors.New("role must be edge or exit")
	}
	id, err := hex.DecodeString(c.SessionID)
	if err != nil || len(id) != 16 {
		return errors.New("session_id must encode 16 bytes as hex")
	}
	if !validAddress(c.AppAddress, true) {
		return errors.New("app_address must be a loopback IP and port")
	}
	if c.Metrics != "" && !validAddress(c.Metrics, true) {
		return errors.New("metrics must be loopback or disabled")
	}
	if c.QueueFrames < 8 || c.QueueFrames > 4096 || c.MaxPacketBytes < 512 || c.MaxPacketBytes > 8192 {
		return errors.New("packet/queue limits out of range")
	}
	if c.ProbeIntervalMS < 20 || c.ProbeIntervalMS > 60000 || c.ProbeTimeoutMS < 3*c.ProbeIntervalMS || c.ProbeTimeoutMS > 15*c.ProbeIntervalMS || c.ProbeTimeoutMS > 300000 || c.HoldDownMS < 0 || c.HoldDownMS > 600000 {
		return errors.New("probe timing out of range")
	}
	if c.TLS.CA == "" || c.TLS.Cert == "" || c.TLS.Key == "" || c.TLS.ServerName == "" || c.TLS.PeerName == "" {
		return errors.New("complete TLS identity configuration required")
	}
	if len(c.Carriers) < 1 || len(c.Carriers) > 4 {
		return errors.New("one to four carriers required")
	}
	names := map[string]bool{}
	addresses := map[string]bool{}
	for _, p := range c.Carriers {
		if p.Name == "" || len(p.Name) > 32 || names[p.Name] || !safeName(p.Name) {
			return errors.New("invalid or duplicate carrier name")
		}
		names[p.Name] = true
		if p.Kind != "https" && p.Kind != "quic" && p.Kind != "quic-stream" {
			return errors.New("carrier kind must be https, quic-stream or quic")
		}
		if !validAddress(p.Address, false) || p.CostMS < 0 || p.CostMS > 10000 {
			return errors.New("invalid carrier address/cost")
		}
		network := p.Kind
		if network == "quic-stream" {
			network = "quic"
		}
		key := network + "/" + p.Address
		if addresses[key] {
			return errors.New("duplicate carrier endpoint")
		}
		addresses[key] = true
		if p.Kind == "https" {
			u, e := url.ParseRequestURI(p.Path)
			if e != nil || u.Path != p.Path || len(p.Path) < 2 || !strings.HasPrefix(p.Path, "/") || c.TokenFile == "" {
				return errors.New("HTTPS needs a private path and token_file")
			}
		}
	}
	return nil
}

func safeName(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func (c Config) Interval() time.Duration { return time.Duration(c.ProbeIntervalMS) * time.Millisecond }
func (c Config) Timeout() time.Duration  { return time.Duration(c.ProbeTimeoutMS) * time.Millisecond }

func Load(path string) (Config, error) {
	var zero Config
	b, err := os.ReadFile(path)
	if err != nil {
		return zero, errors.New("cannot read configuration")
	}
	if len(b) > 65536 {
		return zero, errors.New("configuration too large")
	}
	c := Defaults()
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return zero, errors.New("invalid configuration JSON or unknown field")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return zero, errors.New("trailing configuration content")
	}
	if err := c.Validate(); err != nil {
		return zero, err
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return zero, err
	}
	for _, p := range []*string{&c.TLS.CA, &c.TLS.Cert, &c.TLS.Key, &c.TokenFile} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	return c, nil
}

func ReadToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot read web token")
	}
	s := strings.TrimSpace(string(b))
	key, err := hex.DecodeString(s)
	if err != nil || len(key) != 32 {
		return "", fmt.Errorf("web token must be 32 random bytes encoded as hex")
	}
	return s, nil
}
