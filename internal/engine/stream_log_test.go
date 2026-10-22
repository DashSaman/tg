package engine

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
)

type logBatchWire struct{ *logCaptureWire }

func (w *logBatchWire) SendBatch(batch [][]byte) error {
	last, err := session.Decode(batch[len(batch)-1])
	if err != nil {
		return err
	}
	// Release ALL retained owners before inspecting the encoded bytes. Native
	// transport I/O owns these bytes independently of the sender's ACK lifetime.
	w.e.onAck(w.link, ackFrame(last.ID, last.ID))
	for _, b := range batch {
		if err := w.Send(b); err != nil {
			return err
		}
	}
	return nil
}

func TestReliableLogMaximumPacketsSurviveAckDuringBatchSend(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	c.MaxPacketBytes = session.MaxPacket
	e := &Engine{cfg: c, links: make(map[string]*link)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &logBatchWire{&logCaptureWire{e: e, frames: make(chan session.Frame, 16), closed: make(chan struct{})}}
	l := newLink(e, ctx, c.Carriers[0], w)
	w.link = l
	l.lastPong = time.Now()
	e.links[l.carrier.Name] = l
	first, second := bytes.Repeat([]byte{37}, session.MaxPacket), bytes.Repeat([]byte{91}, session.MaxPacket)
	if !e.admitRedundantLocked(first, time.Now()) || !e.admitRedundantLocked(second, time.Now()) {
		t.Fatal("admission failed")
	}
	done := make(chan struct{})
	go func() { l.transmit(); close(done) }()
	defer func() {
		l.stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("batch transmitter failed to stop")
		}
	}()
	for i := 0; i < 16; i++ {
		select {
		case f := <-w.frames:
			want := first
			if i >= 8 {
				want = second
			}
			index := i % 8
			if f.ID != uint64(i/8+1) || int(f.Index) != index || !bytes.Equal(f.Payload, want[index*session.Chunk:(index+1)*session.Chunk]) {
				t.Fatal("encoded batch changed after ACK release", i)
			}
		case <-time.After(time.Second):
			t.Fatal("maximum-size packet truncated or stranded")
		}
	}
}

type logCaptureWire struct {
	e      *Engine
	link   *link
	frames chan session.Frame
	closed chan struct{}
	once   sync.Once
}

func (w *logCaptureWire) Send(b []byte) error {
	if !w.e.mu.TryLock() {
		panic("engine mutex held across native send")
	}
	w.e.mu.Unlock()
	f, err := session.Decode(b)
	if err != nil {
		return err
	}
	f.Payload = append([]byte(nil), f.Payload...)
	if f.Kind == session.Data {
		// Real readers can acknowledge while a native send is still in progress.
		w.e.onAck(w.link, ackFrame(f.ID, f.ID))
	}
	select {
	case w.frames <- f:
		return nil
	case <-w.closed:
		return context.Canceled
	}
}
func (w *logCaptureWire) Receive(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (w *logCaptureWire) Close() error { w.once.Do(func() { close(w.closed) }); return nil }

func TestReliableCarrierReplaysOldestRetainedBeforeNewTraffic(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	e := &Engine{cfg: c, links: make(map[string]*link)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newLink(e, ctx, c.Carriers[0], nil)
	defer a.cancel()
	a.lastPong = time.Now()
	e.links[a.carrier.Name] = a
	if !e.admitRedundantLocked([]byte("old-unacknowledged"), time.Now()) {
		t.Fatal("first admission failed")
	}
	a.closed.Store(true) // the sole original carrier never completed Send

	w := &logCaptureWire{e: e, frames: make(chan session.Frame, 4), closed: make(chan struct{})}
	b := newLink(e, ctx, c.Carriers[1], w)
	w.link = b
	b.lastPong = time.Now()
	e.links[b.carrier.Name] = b
	// A per-link ordinary queue may be full of stale frames; reliable replay
	// must read the shared log instead of losing packet 1 or jumping to 2.
	if !e.admitRedundantLocked([]byte("new-traffic"), time.Now()) {
		t.Fatal("second admission failed")
	}
	done := make(chan struct{})
	go func() { b.transmit(); close(done) }()
	defer func() {
		b.stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("transmitter did not stop")
		}
	}()
	for i, want := range []string{"old-unacknowledged", "new-traffic"} {
		select {
		case f := <-w.frames:
			if f.Kind != session.Data || f.ID != uint64(i+1) || string(f.Payload) != want {
				t.Fatalf("replay order/payload: got ID=%d %q, want ID=%d %q", f.ID, f.Payload, i+1, want)
			}
		case <-time.After(time.Second):
			t.Fatal("retained packet stranded on failed carrier")
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.retained) != 0 {
		t.Fatal("native-send feedback did not release acknowledged payloads")
	}
}

func TestReliableLogDoesNotDependOnOrdinaryCarrierQueueSpace(t *testing.T) {
	c := config.Defaults()
	c.Redundant = true
	c.QueueFrames = 4
	e := &Engine{cfg: c, links: make(map[string]*link)}
	l := newLink(e, context.Background(), c.Carriers[0], nil)
	defer l.cancel()
	l.lastPong = time.Now()
	e.links[l.carrier.Name] = l
	for i := 0; i < cap(l.data); i++ {
		l.data <- session.Frame{ID: 99}
	}
	if !e.admitRedundantLocked([]byte("shared-queue"), time.Now()) || e.sequence.Load() != 1 || len(e.retained) != 1 {
		t.Fatal("reliable carrier still depends on independent skipping queues")
	}
}
