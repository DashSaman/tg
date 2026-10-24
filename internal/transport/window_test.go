package transport

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/pki"
)

const testHTTP2Preface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

func TestQUICExplicitWindowsBoundInitialAndMaximumCredit(t *testing.T) {
	c := config.Defaults()
	if httpWindowConfig(c) != nil {
		t.Fatal("default HTTP2 configuration changed")
	}
	p := config.Carrier{Kind: "quic-stream"}
	legacy := quicModeConfig(p, c)
	if legacy.MaxStreamReceiveWindow != 8<<20 || legacy.MaxConnectionReceiveWindow != 16<<20 {
		t.Fatal("legacy QUIC windows changed")
	}
	c.StreamWindowBytes = 32 << 20
	q := quicModeConfig(p, c)
	if q.InitialStreamReceiveWindow != 32<<20 || q.MaxStreamReceiveWindow != 32<<20 || q.InitialConnectionReceiveWindow != 64<<20 || q.MaxConnectionReceiveWindow != 64<<20 {
		t.Fatal("explicit QUIC receive credit was silently ignored")
	}
	q = quicModeConfig(config.Carrier{Kind: "quic"}, c)
	if q.InitialStreamReceiveWindow != 0 {
		t.Fatal("DATAGRAM mode received unnecessary data-stream buffers")
	}
}

func sendTestSettings(conn net.Conn, ack bool) error {
	frame := []byte{0, 0, 0, 4, 0, 0, 0, 0, 0}
	if ack {
		frame[4] = 1
	}
	return writeAll(conn, frame)
}

// JSON keeps the regression runnable on the previous Config type: the old
// implementation ignores this opt-in field here and advertises its real 1MiB
// default. Assert actual TLS/HTTP2 frames, not merely an intended Go setting.
func withWindow(t *testing.T, c config.Config, bytes int) config.Config {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	fields["stream_window_bytes"] = bytes
	b, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func readWindowCredit(conn net.Conn) (uint32, uint32, error) {
	stream, connection := uint32(0), uint32(65535)
	for range 8 {
		var header [9]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return 0, 0, err
		}
		length := int(header[0])<<16 | int(header[1])<<8 | int(header[2])
		if length > 65536 {
			return 0, 0, fmt.Errorf("unexpected large control frame")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return 0, 0, err
		}
		id := binary.BigEndian.Uint32(header[5:]) & 0x7fffffff
		switch header[3] {
		case 4: // SETTINGS
			if header[4]&1 == 0 {
				if id != 0 || length%6 != 0 {
					return 0, 0, fmt.Errorf("invalid SETTINGS frame")
				}
				for at := 0; at < length; at += 6 {
					if binary.BigEndian.Uint16(payload[at:]) == 4 {
						stream = binary.BigEndian.Uint32(payload[at+2:])
					}
				}
				if err := sendTestSettings(conn, true); err != nil {
					return 0, 0, err
				}
			}
		case 8: // WINDOW_UPDATE
			if id == 0 && length == 4 {
				connection += binary.BigEndian.Uint32(payload) & 0x7fffffff
			}
		}
		if stream != 0 && connection > 65535 {
			return stream, connection, nil
		}
	}
	return 0, 0, fmt.Errorf("missing receive window credit")
}

func TestHTTPServerAdvertisesSelectedWindowOnActualTLSConnection(t *testing.T) {
	edge, exit := fixture(t)
	exit = withWindow(t, exit, 32<<20)
	p := config.Carrier{Name: "web", Kind: "https", Address: "127.0.0.1:0", Path: "/window"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	address, stop, err := Listen(ctx, exit, p, 100, func(context.Context, Wire, uint64) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	tc, err := pki.Client(edge.TLS, false)
	if err != nil {
		t.Fatal(err)
	}
	tc.NextProtos = []string{"h2"}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address.String(), tc)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err = writeAll(conn, []byte(testHTTP2Preface)); err != nil {
		t.Fatal(err)
	}
	if err = sendTestSettings(conn, false); err != nil {
		t.Fatal(err)
	}
	stream, connection, err := readWindowCredit(conn)
	if err != nil {
		t.Fatal(err)
	}
	if stream != 32<<20 || connection != 64<<20 {
		t.Fatalf("actual advertised stream/connection credit=%d/%d, need32MiB/64MiB", stream, connection)
	}
}

func TestHTTPClientAdvertisesSelectedWindowOnActualTLSConnection(t *testing.T) {
	edge, exit := fixture(t)
	edge = withWindow(t, edge, 32<<20)
	tc, err := pki.Server(exit.TLS, false)
	if err != nil {
		t.Fatal(err)
	}
	tc.NextProtos = []string{"h2"}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", tc)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	type result struct {
		stream, connection uint32
		err                error
	}
	observed := make(chan result, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			observed <- result{err: err}
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		preface := make([]byte, len(testHTTP2Preface))
		if _, err = io.ReadFull(conn, preface); err != nil {
			observed <- result{err: err}
			return
		}
		if string(preface) != testHTTP2Preface {
			observed <- result{err: fmt.Errorf("missing HTTP2 preface")}
			return
		}
		if err = sendTestSettings(conn, false); err != nil {
			observed <- result{err: err}
			return
		}
		stream, connection, err := readWindowCredit(conn)
		observed <- result{stream, connection, err}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	p := config.Carrier{Name: "web", Kind: "https", Address: listener.Addr().String(), Path: "/window"}
	done := make(chan error, 1)
	go func() {
		w, _, err := Dial(ctx, edge, p, 200)
		if w != nil {
			_ = w.Close()
		}
		done <- err
	}()
	select {
	case got := <-observed:
		if got.err != nil {
			t.Fatal(got.err)
		}
		// Go1.26 adds client connection credit to HTTP2's initial65535 bytes.
		if got.stream != 32<<20 || got.connection != (64<<20)+65535 {
			t.Fatalf("actual client stream/connection credit=%d/%d", got.stream, got.connection)
		}
	case <-ctx.Done():
		t.Fatal("client did not send receive credit")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probe Dial did not clean up")
	}
}
