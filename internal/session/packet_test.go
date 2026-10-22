package session

import (
	"bytes"
	"testing"
	"time"
)

func TestCompletePacketEnvelopeAndLegacyRejection(t *testing.T) {
	for _, size := range []int{1, 1536, 7200, MaxPacket, 60032, MaxStreamPacket} {
		f := Frame{Kind: 5, ID: 1, Count: 1, Total: uint16(size), Payload: bytes.Repeat([]byte{37}, size)}
		b, err := Encode(f)
		if err != nil {
			t.Fatal("complete packet rejected", size, err)
		}
		if len(b) != Header+size {
			t.Fatal("packet was fragmented or truncated")
		}
		if _, err := Decode(b); err == nil {
			t.Fatal("legacy decoder accepted packet-mode frame", size)
		}
		got, err := DecodePacketStream(b)
		if err != nil || !bytes.Equal(got.Payload, f.Payload) {
			t.Fatal("packet decoder failed", err)
		}
	}
	if _, err := Encode(Frame{Kind: Packet, ID: 1, Count: 1, Total: MaxStreamPacket + 1, Payload: make([]byte, MaxStreamPacket+1)}); err == nil {
		t.Fatal("packet larger than UDP ceiling encoded")
	}
	for _, alter := range []func(*Frame){func(f *Frame) { f.Index = 1 }, func(f *Frame) { f.Count = 2 }, func(f *Frame) { f.Total = 0 }, func(f *Frame) { f.Total = 2 }, func(f *Frame) { f.ID = 0 }} {
		f := Frame{Kind: 5, ID: 1, Count: 1, Total: 1, Payload: []byte{1}}
		alter(&f)
		if _, err := Encode(f); err == nil {
			t.Fatal("malformed complete packet accepted")
		}
	}
}

func TestCompletePacketDecoderAndPendingAssemblyDoNotMixFormats(t *testing.T) {
	frames, err := Fragment(bytes.Repeat([]byte{8}, 2048), 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encode(frames[0])
	if _, err := DecodePacketStream(b); err == nil {
		t.Fatal("packet-mode decoder accepted legacy fragment")
	}
	r := NewRedundantReassembler(8, time.Second)
	now := time.Now()
	if _, ok, err := r.Push(now, frames[0]); ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, _, err := r.Push(now, Frame{Kind: Packet, ID: 1, Count: 1, Total: 1, Payload: []byte{1}}); err == nil {
		t.Fatal("packet overwrote pending fragments")
	}
	if _, ok, err := r.Push(now, frames[1]); !ok || err != nil {
		t.Fatal("pending assembly corrupted", err)
	}
}

func TestCompletePacketOwnsBytesDedupesAndAdvancesAckWithoutAssembly(t *testing.T) {
	r := NewRedundantReassembler(8, time.Second)
	now := time.Now()
	payload := bytes.Repeat([]byte{7}, MaxPacket)
	f := Frame{Kind: 5, ID: 2, Count: 1, Total: MaxPacket, Payload: payload}
	p, ok, err := r.Push(now, f)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if len(r.pending) != 0 {
		t.Fatal("complete packet allocated pending assembly")
	}
	payload[0] = 99
	if p[0] != 7 {
		t.Fatal("retained borrowed input bytes")
	}
	if _, ok, err := r.Push(now, f); err != nil || ok {
		t.Fatal("duplicate completed packet delivered", err)
	}
	if prefix, high := r.AckState(); prefix != 0 || high != 2 {
		t.Fatal(prefix, high)
	}
	q, ok, err := r.Push(now, Frame{Kind: 5, ID: 1, Count: 1, Total: 1, Payload: []byte{1}})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	r.Release(q)
	if prefix, high := r.AckState(); prefix != 2 || high != 2 {
		t.Fatal("gap fill lost cumulative ACK", prefix, high)
	}
	r.Release(p)
}
