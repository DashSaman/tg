package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func valid() Config {
	c := Defaults()
	c.Role = "edge"
	c.SessionID = strings.Repeat("1", 32)
	return c
}

func TestValidateBoundsAndExposure(t *testing.T) {
	for _, alter := range []func(*Config){
		func(c *Config) { c.AppAddress = "0.0.0.0:31820" }, func(c *Config) { c.Metrics = "0.0.0.0:19090" },
		func(c *Config) { c.QueueFrames = 1000000 }, func(c *Config) { c.ProbeTimeoutMS = 1 },
		func(c *Config) { c.ProbeTimeoutMS = 16000 },
		func(c *Config) { c.MaxPacketBytes = 8193 }, func(c *Config) { c.Carriers[1].Name = c.Carriers[0].Name },
		func(c *Config) { c.SessionID = "not-a-session" }, func(c *Config) { c.Carriers[0].Path = "/" },
	} {
		c := valid()
		alter(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("invalid config accepted: %+v", c)
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadStrictAndConfigRelativePaths(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	c := valid()
	b, _ := json.Marshal(c)
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TLS.CA != filepath.Join(dir, "ca.crt") {
		t.Fatal("CA path not relative to config", loaded.TLS.CA)
	}
	if err := os.WriteFile(file, []byte(`{"role":"edge","unknown":"private-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatal("strict/redacted parsing failed", err)
	}
	if err := os.WriteFile(file, append(b, []byte(` {}`)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestNativeQUICModesCannotShareUDPListenerEndpoint(t *testing.T) {
	c := valid()
	other := c.Carriers[1]
	other.Name = "datagram"
	other.Kind = "quic"
	c.Carriers = append(c.Carriers, other)
	if err := c.Validate(); err == nil {
		t.Fatal("QUIC modes can conflict on one UDP listener")
	}
}

func TestRedundancyRequiresMultipleCarriersAndCanRepairDatagrams(t *testing.T) {
	c := valid()
	c.Redundant = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Carriers = c.Carriers[:1]
	if err := c.Validate(); err == nil {
		t.Fatal("single carrier accepted for redundant setup")
	}
	c = valid()
	c.Redundant = true
	c.Carriers[1].Kind = "quic"
	if err := c.Validate(); err != nil {
		t.Fatal("session repair must allow the unordered DATAGRAM carrier", err)
	}
}

func TestOrderedOutputNeedsSessionRepair(t *testing.T) {
	c := valid()
	c.OrderedOutput = true
	if err := c.Validate(); err == nil {
		t.Fatal("ordered barrier accepted without repair")
	}
	c.Redundant = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
