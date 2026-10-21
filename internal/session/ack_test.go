package session

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestAckShapeAndZeroPrefix(t *testing.T) {
	payload := make([]byte, AckBytes)
	binary.BigEndian.PutUint64(payload, 9)
	for _, id := range []uint64{0, 9} {
		encoded, err := Encode(Frame{Kind: Ack, ID: id, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		f, err := Decode(encoded)
		if err != nil || f.ID != id || binary.BigEndian.Uint64(f.Payload) != 9 {
			t.Fatal(f, err)
		}
	}
	for _, f := range []Frame{{Kind: Ack, ID: 10, Payload: payload}, {Kind: Ack}, {Kind: Ack, Count: 1, Payload: payload}, {Kind: Data, Payload: payload}} {
		if _, err := Encode(f); err == nil {
			t.Fatal("accepted malformed ACK/data", f)
		}
	}
	bad := make([]byte, AckBytes)
	bad[8] = 2
	if _, err := Encode(Frame{Kind: Ack, Payload: bad}); err == nil {
		t.Fatal("ACK bitmap acknowledged an ID above highest")
	}
	binary.BigEndian.PutUint64(bad, 1)
	bad[8] = 1
	if _, err := Encode(Frame{Kind: Ack, Payload: bad}); err == nil {
		t.Fatal("ACK bitmap contradicts its cumulative prefix")
	}
}

func TestAckPrefixDoesNotAcknowledgeHole(t *testing.T) {
	r := NewRedundantReassembler(128, time.Second)
	for _, id := range []uint64{2, 3, 1} {
		frames, _ := Fragment([]byte("data"), id)
		p, complete, err := r.Push(time.Now(), frames[0])
		if err != nil || !complete {
			t.Fatal(err)
		}
		r.Release(p)
		prefix, high := r.AckState()
		want := uint64(0)
		if id == 1 {
			want = 3
		}
		if high < id {
			t.Fatal("highest completion moved backwards")
		}
		if prefix != want {
			t.Fatalf("prefix %d, wanted %d", prefix, want)
		}
		if !r.Completed(id) || r.Completed(4) {
			t.Fatal("completion state changed")
		}
	}
	prefix, high := r.AckState()
	if prefix != 3 || high != 3 {
		t.Fatal(prefix, high)
	}
}
