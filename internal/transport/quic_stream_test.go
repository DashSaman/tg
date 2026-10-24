package transport

import (
	"bytes"
	"context"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"testing"
	"time"
)

func TestQUICStreamOutlivesHandshakeDeadlineAndPreservesBatch(t *testing.T) {
	edge, exit := fixture(t)
	p := config.Carrier{Name: "stream", Kind: "quic-stream", Address: "127.0.0.1:0"}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	frames := [][]byte{[]byte("first"), bytes.Repeat([]byte{17}, session.MaxFrame), []byte("last")}
	addr, stop, err := Listen(ctx, exit, p, 100, func(ctx context.Context, w Wire, epoch uint64) error {
		for _, want := range frames {
			got, err := w.Receive(ctx)
			if err != nil {
				return err
			}
			if !bytes.Equal(got, want) {
				t.Error("stream changed a frame boundary")
			}
		}
		if err := w.Send([]byte("done")); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	p.Address = addr.String()
	w, _, err := Dial(ctx, edge, p, 99)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	time.Sleep(3200 * time.Millisecond) // Beyond the absolute 3s handshake deadline.
	batch, ok := w.(BatchWire)
	if !ok {
		t.Fatal("stream cannot batch")
	}
	if err := batch.SendBatch(frames); err != nil {
		t.Fatal(err)
	}
	got, err := w.Receive(ctx)
	if err != nil || string(got) != "done" {
		t.Fatal("handshake deadline leaked into stream lifetime", err)
	}
}

func TestQUICStreamCloseInterruptsFlowControlBlockedWrite(t *testing.T) {
	edge, exit := fixture(t)
	p := config.Carrier{Name: "stream", Kind: "quic-stream", Address: "127.0.0.1:0"}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	addr, stop, err := Listen(ctx, exit, p, 100, func(ctx context.Context, w Wire, epoch uint64) error { <-ctx.Done(); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	p.Address = addr.String()
	w, _, err := Dial(ctx, edge, p, 99)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	done := make(chan error, 1)
	go func() {
		packet := make([]byte, session.MaxFrame)
		for i := 0; i < 20000; i++ {
			if err := w.Send(packet); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	time.Sleep(60 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatal("write did not reach flow control", err)
	default:
	}
	w.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed writer completed successfully")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not interrupt blocked stream write")
	}
}
