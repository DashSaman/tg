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
	if _, _, err := r.Push(now, next[0]); err != nil || len(r.pending) > 1 {
		t.Fatal("bounded fresh-packet admission failed", err)
	}
	if _, _, err := r.Push(now.Add(2*time.Second), next[0]); err != nil {
		t.Fatal("expired assembly not released", err)
	}
}

func TestIncompletePacketLossDoesNotBlockNewCompletePackets(t *testing.T) {
	r := NewReassembler(2, 2*time.Second)
	now := time.Now()
	for _, id := range []uint64{1, 2} {
		frames, _ := Fragment(make([]byte, 1400), id)
		_, _, _ = r.Push(now, frames[0])
	}
	frames, _ := Fragment(bytes.Repeat([]byte{9}, 1400), 3)
	var complete bool
	for _, f := range frames {
		packet, ok, err := r.Push(now.Add(time.Millisecond), f)
		if err != nil {
			t.Fatal("old incomplete packets blocked fresh traffic", err)
		}
		if ok {
			complete = true
			r.Release(packet)
		}
	}
	if !complete || len(r.pending) > 2 {
		t.Fatal("fresh packet lost or memory bound exceeded")
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

func TestEncodeIntoReusesCallerBuffer(t *testing.T) {
	frames, _ := Fragment(bytes.Repeat([]byte{7}, 1400), 3)
	var scratch [MaxFrame]byte
	for _, f := range frames {
		want, _ := Encode(f)
		got, err := EncodeInto(scratch[:0], f)
		if err != nil || !bytes.Equal(want, got) {
			t.Fatal("different reusable encoding", err)
		}
	}
	if got, err := EncodeInto(scratch[:0], Frame{Kind: Pong, ID: 0}); err == nil || len(got) != 0 {
		t.Fatal("invalid frame encoded")
	}
	if allocations := testing.AllocsPerRun(100, func() { _, _ = EncodeInto(scratch[:0], frames[0]) }); allocations != 0 {
		t.Fatal("encoder allocated with enough capacity", allocations)
	}
}

func TestFragmentIntoDoesNotRetainFrameStorage(t *testing.T) {
	packet := bytes.Repeat([]byte{8}, 2048)
	var scratch [8]Frame
	got, err := FragmentInto(scratch[:0], packet, 12)
	if err != nil || len(got) != 2 {
		t.Fatal(err)
	}
	copied := got[0]
	_, _ = FragmentInto(scratch[:0], []byte{9}, 13)
	if copied.ID != 12 || copied.Total != 2048 || !bytes.Equal(copied.Payload, packet[:Chunk]) {
		t.Fatal("previously enqueued frame was overwritten")
	}
	if n := testing.AllocsPerRun(100, func() { _, _ = FragmentInto(scratch[:0], packet, 14) }); n != 0 {
		t.Fatal("fragment storage allocated", n)
	}
}

func TestReassemblyBuffersAreOwnedUntilRelease(t *testing.T) {
	r := NewReassembler(2, time.Second)
	now := time.Now()
	complete := func(id uint64, value byte) []byte {
		t.Helper()
		frames, _ := Fragment(bytes.Repeat([]byte{value}, 1400), id)
		var packet []byte
		for _, f := range frames {
			b, ok, err := r.Push(now, f)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				packet = b
			}
		}
		return packet
	}
	first := complete(1, 3)
	second := complete(2, 4)
	if !bytes.Equal(first, bytes.Repeat([]byte{3}, 1400)) {
		t.Fatal("borrowed buffer reused before release")
	}
	r.Release(first)
	r.Release(second)
	third := complete(3, 9)
	if !bytes.Equal(third, bytes.Repeat([]byte{9}, 1400)) {
		t.Fatal("recycled buffer leaked previous payload")
	}
	r.Release(third)
	if len(r.free) > 2 {
		t.Fatal("unbounded cache")
	}
}
