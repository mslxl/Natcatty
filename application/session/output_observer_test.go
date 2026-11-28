package session

import (
	"bytes"
	"sync"
	"testing"
)

func TestOutputObserversReceiveOrderedCopiesAndCanUnsubscribe(t *testing.T) {
	var mu sync.Mutex
	var first, second bytes.Buffer
	s := &Session{}
	removeFirst := s.SubscribeOutput(func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		first.Write(data)
	})
	removeSecond := s.SubscribeOutput(func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		second.Write(data)
	})

	s.publishOutput([]byte("one"))
	removeFirst()
	removeFirst()
	s.publishOutput([]byte("two"))
	removeSecond()
	s.publishOutput([]byte("three"))

	mu.Lock()
	defer mu.Unlock()
	if got := first.String(); got != "one" {
		t.Fatalf("first observer = %q, want one", got)
	}
	if got := second.String(); got != "onetwo" {
		t.Fatalf("second observer = %q, want onetwo", got)
	}
}

func TestOutputObserverReplayPrecedesFutureOutput(t *testing.T) {
	var got bytes.Buffer
	s := &Session{}
	s.publishOutput([]byte("history"))
	remove := s.SubscribeOutputReplay(func(data []byte) {
		got.Write(data)
	})
	defer remove()
	s.publishOutput([]byte("future"))
	if got.String() != "historyfuture" {
		t.Fatalf("replayed observer = %q, want historyfuture", got.String())
	}
}

func TestOutputFilterReplayStripsPayloadAndReplaysCleanHistory(t *testing.T) {
	s := &Session{}
	s.publishOutput([]byte("before"))

	var seen bytes.Buffer
	remove := s.SubscribeOutputFilterReplay(func(data []byte) []byte {
		seen.Write(data)
		return bytes.ReplaceAll(data, []byte("BINARY"), nil)
	})

	if got := string(s.publishOutput([]byte("headBINARYtail"))); got != "headtail" {
		t.Fatalf("filtered chunk = %q, want headtail", got)
	}
	if seen.String() != "beforeheadBINARYtail" {
		t.Fatalf("filter saw = %q, want beforeheadBINARYtail", seen.String())
	}
	remove()

	// A later replay subscriber sees the cleaned stream, not the stripped binary.
	var replayed bytes.Buffer
	remove2 := s.SubscribeOutputReplay(func(data []byte) {
		replayed.Write(data)
	})
	defer remove2()
	if replayed.String() != "beforeheadtail" {
		t.Fatalf("outputReplay = %q, want beforeheadtail", replayed.String())
	}
}
