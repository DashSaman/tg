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

func TestConnectIsReliableAndSharesTCPAddressBudget(t *testing.T) {
	c := valid()
	c.Redundant = true
	c.StreamPackets = true
	c.Carriers[0].Kind = "https-connect"
	if err := c.Validate(); err != nil {
		t.Fatal("authenticated CONNECT carrier rejected", err)
	}
	other := c.Carriers[0]
	other.Name = "h2"
	other.Kind = "https"
	c.Carriers = append(c.Carriers, other)
	if c.Validate() == nil {
		t.Fatal("HTTPS2 and CONNECT can occupy the same TCP endpoint")
	}
	c.Carriers = c.Carriers[:2]
	c.Carriers[0].Path = "/"
	if c.Validate() == nil {
		t.Fatal("CONNECT omitted private path guard")
	}
}

func TestCompletePacketModeRequiresAcknowledgedReliableCarriers(t *testing.T) {
	c := valid()
	if err := json.Unmarshal([]byte(`{"stream_packets":true}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Validate() == nil {
		t.Fatal("packet mode accepted without acknowledged replay")
	}
	c.Redundant = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Carriers[1].Kind = "quic"
	if c.Validate() == nil {
		t.Fatal("packet mode accepted DATAGRAM carrier")
	}
}

func TestCapacityWindowsAreBoundedBeforeUse(t *testing.T) {
	for key, values := range map[string][]int{"retained_packets": {0, 127, 131073}, "stream_window_bytes": {-1, 1, (256 << 20) + 1}} {
		for _, value := range values {
			c := valid()
			b, _ := json.Marshal(c)
			var fields map[string]any
			_ = json.Unmarshal(b, &fields)
			fields[key] = value
			b, _ = json.Marshal(fields)
			_ = json.Unmarshal(b, &c)
			if c.Validate() == nil {
				t.Fatalf("invalid %s=%d capacity accepted", key, value)
			}
		}
	}
}

func TestReplayBudgetSupportsSmallPacketsWithoutUnboundedJumboMemory(t *testing.T) {
	c := valid()
	c.RetainedPackets = 131072
	c.MaxPacketBytes = 2048
	if err := c.Validate(); err != nil {
		t.Fatal("bounded ordinary-packet high-BDP budget rejected", err)
	}
	c.MaxPacketBytes = 8192
	if c.Validate() == nil {
		t.Fatal("replay payload budget above 256 MiB accepted")
	}
	c.RetainedPackets = 32768
	if err := c.Validate(); err != nil {
		t.Fatal("256 MiB jumbo boundary rejected", err)
	}
}

func TestOrderedOutputBudgetFitsItsReorderWindow(t *testing.T) {
	c := valid()
	c.Redundant = true
	c.OrderedOutput = true
	c.RetainedPackets = 32768
	if c.Validate() == nil {
		t.Fatal("sender can exceed bounded ordered receiver capacity")
	}
	c.RetainedPackets = 16384
	c.StreamWindowBytes = 256 << 20
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.OrderedOutput = false
	c.RetainedPackets = 32768
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
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

func TestLargeUDPPacketBudgetIsOptInAndReplayBytesRemainBounded(t *testing.T) {
	c := valid()
	c.Redundant = true
	c.StreamPackets = true
	c.MaxPacketBytes = 65507
	c.RetainedPackets = 2048
	if err := c.Validate(); err != nil {
		t.Fatal("large packet mode rejected", err)
	}
	c.StreamPackets = false
	if err := c.Validate(); err == nil {
		t.Fatal("legacy accepted large packets")
	}
	c.StreamPackets = true
	c.RetainedPackets = 8192
	if err := c.Validate(); err == nil {
		t.Fatal("large packets bypassed256MiB replay budget")
	}
	c.RetainedPackets = 2048
	c.MaxPacketBytes = 65508
	if err := c.Validate(); err == nil {
		t.Fatal("oversized UDP payload accepted")
	}
}
