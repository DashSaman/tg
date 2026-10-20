package session

import (
	"bytes"
	"testing"
	"time"
)

func TestPongWireAndMalformedFrames(t *testing.T) {
	raw, err := Encode(Frame{Kind: Pong, ID: 7})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 3, 0, 0, 0, 0, 0, 0, 0, 7, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(raw, want) {
		t.Fatalf("wire %x", raw)
	}
	for _, b := range [][]byte{nil, want[:15], append(append([]byte{}, want...), 1), {2, 3, 0, 0, 0, 0, 0, 0, 0, 7, 0, 0, 0, 0, 0, 0}} {
		if _, err := Decode(b); err == nil {
			t.Fatalf("accepted malformed %x", b)
		}
	}
}

func TestFragmentReorderDuplicateAndExpiry(t *testing.T) {
	now := time.Unix(100, 0)
	payload := bytes.Repeat([]byte{0x43, 0x19}, 1500)
	frames, err := Fragment(payload, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("fragments=%d", len(frames))
	}
	r := NewReassembler(1, time.Second)
	for _, i := range []int{2, 0, 0} {
		if b, ok, e := r.Push(now, frames[i]); e != nil || ok || b != nil {
			t.Fatalf("premature %v %v", ok, e)
		}
	}
	b, ok, err := r.Push(now, frames[1])
	if err != nil || !ok || !bytes.Equal(b, payload) {
		t.Fatal("reassembly lost data", err)
	}
	if _, ok, _ := r.Push(now, frames[0]); ok {
		t.Fatal("replayed packet")
	}
	incomplete, _ := Fragment(payload, 10)
	_, _, _ = r.Push(now, incomplete[0])
	next, _ := Fragment(payload, 11)
	if _, _, err := r.Push(now, next[0]); err == nil {
		t.Fatal("unfinished-packet limit not enforced")
	}
	if _, _, err := r.Push(now.Add(2*time.Second), next[0]); err != nil {
		t.Fatal("expired assembly not released", err)
	}
}

func TestRejectOversizeAndInconsistentFragment(t *testing.T) {
	if _, err := Fragment(make([]byte, 8193), 1); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := Fragment([]byte{1}, 0); err == nil {
		t.Fatal("zero sequence accepted")
	}
	f := Frame{Kind: Data, ID: 2, Index: 0, Count: 8, Total: 1, Payload: []byte{1}}
	if _, err := Encode(f); err == nil {
		t.Fatal("inconsistent fragment accepted")
	}
	r := NewReassembler(4, time.Second)
	for _, id := range []uint64{5000, 1, 5000} {
		f, _ := Fragment([]byte{9}, id)
		_, ok, _ := r.Push(time.Now(), f[0])
		if id != 5000 && ok {
			t.Fatal("old packet accepted")
		}
	}
}

func BenchmarkFragmentEncode(b *testing.B) {
	payload := make([]byte, 1400)
	b.SetBytes(1400)
	for i := 0; i < b.N; i++ {
		fs, _ := Fragment(payload, uint64(i+1))
		for _, f := range fs {
			_, _ = Encode(f)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte{1, 3, 0, 0, 0, 0, 0, 0, 0, 7, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		frame, err := Decode(b)
		if err != nil {
			return
		}
		if len(frame.Payload) > Chunk {
			t.Fatal("unbounded frame payload")
		}
		encoded, err := Encode(frame)
		if err != nil || !bytes.Equal(encoded, b) {
			t.Fatal("noncanonical wire accepted", err)
		}
	})
}
