package engine

import (
	"bytes"
	"context"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"testing"
	"time"
)

type packetAckWire struct {
	e        *Engine
	l        *link
	original [][]byte
	received chan []session.Frame
}

func (w *packetAckWire) Send(b []byte) error { return w.SendBatch([][]byte{b}) }
func (w *packetAckWire) Receive(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (w *packetAckWire) Close() error { return nil }
func (w *packetAckWire) SendBatch(batch [][]byte) error {
	if !w.e.mu.TryLock() {
		panic("engine mutex held during native packet send")
	}
	w.e.mu.Unlock()
	last, err := session.DecodePacketStream(batch[len(batch)-1])
	if err != nil {
		return err
	}
	w.e.onAck(w.l, ackFrame(last.ID, last.ID))
	for _, p := range w.original {
		clear(p)
	} // ACK released owners while native I/O still reads encoded frames.
	var frames []session.Frame
	for _, b := range batch {
		f, err := session.DecodePacketStream(b)
		if err != nil {
			return err
		}
		f.Payload = append([]byte(nil), f.Payload...)
		frames = append(frames, f)
	}
	w.received <- frames
	return nil
}
func TestCompletePacketLogSendsOneFramePerPacketAndOwnsAckReleasedBytes(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	c.StreamPackets = true
	c.MaxPacketBytes = session.MaxPacket
	e := &Engine{cfg: c, links: make(map[string]*link)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := bytes.Repeat([]byte{7}, session.MaxPacket), bytes.Repeat([]byte{91}, 7200)
	w := &packetAckWire{e: e, original: [][]byte{a, b}, received: make(chan []session.Frame, 1)}
	l := newLink(e, ctx, c.Carriers[0], w)
	w.l = l
	l.lastPong = time.Now()
	e.links[l.carrier.Name] = l
	if !e.admitRedundantLocked(a, time.Now()) || !e.admitRedundantLocked(b, time.Now()) {
		t.Fatal("admission failed")
	}
	done := make(chan struct{})
	go func() { l.transmit(); close(done) }()
	defer func() {
		l.stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("packet transmitter did not stop")
		}
	}()
	select {
	case frames := <-w.received:
		if len(frames) != 2 {
			t.Fatal("complete packets were fragmented", len(frames))
		}
		for i, f := range frames {
			size, value := session.MaxPacket, byte(7)
			if i == 1 {
				size, value = 7200, 91
			}
			if f.Kind != session.Packet || f.ID != uint64(i+1) || !bytes.Equal(f.Payload, bytes.Repeat([]byte{value}, size)) {
				t.Fatal("packet changed after ACK owner release", i)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("packet-mode log stranded data")
	}
}
