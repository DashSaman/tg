package engine

import (
	"bytes"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/session"
	"testing"
	"time"
)

func TestPacketReceiverOwnsLargeUDPDatagramsWithoutChangingLegacy(t *testing.T) {
	for _, size := range []int{60032, 65507} {
		c := config.Defaults()
		c.Redundant = true
		c.StreamPackets = true
		c.MaxPacketBytes = size
		c.RetainedPackets = 2048
		r := newReceiver(c)
		original := bytes.Repeat([]byte{71}, size)
		encoded, err := session.Encode(session.Frame{Kind: session.Packet, ID: 1, Count: 1, Total: uint16(size), Payload: original})
		if err != nil {
			t.Fatal("large packet codec rejected", size, err)
		}
		f, err := session.DecodePacketStream(encoded)
		if err != nil {
			t.Fatal(err)
		}
		p, complete, err := r.Push(time.Now(), f)
		if err != nil || !complete || !bytes.Equal(p, original) {
			t.Fatal("large packet truncated", size, len(p), err)
		}
		clear(encoded)
		if p[0] != 71 || p[size-1] != 71 {
			t.Fatal("large packet retained borrowed bytes")
		}
		r.Release(p)
		f.ID = 2
		q, complete, err := r.Push(time.Now(), f)
		if err != nil || !complete || len(q) != size || &p[0] != &q[0] {
			t.Fatal("large completed buffer was not reusable", size, err)
		}
		r.Release(q)
		if _, err := session.Fragment(original, 2); err == nil {
			t.Fatal("legacy format accepted large packet")
		}
	}
}
