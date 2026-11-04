// SPDX-License-Identifier: Apache-2.0
package etcp

import (
	"bytes"
	"strings"
	"testing"
)

func filledRing(limit, n, size int) *ring {
	r := &ring{limit: limit}
	for i := range n {
		r.push(bytes.Repeat([]byte{byte(i)}, size))
	}
	return r
}

func TestRingNextCountsPushes(t *testing.T) {
	r := filledRing(1<<20, 5, 10)
	if got := r.next(); got != 5 {
		t.Fatalf("next() = %d, want 5", got)
	}
}

func TestRingTrimKeepsUnsent(t *testing.T) {
	r := filledRing(25, 10, 10) // 100 bytes held, limit 25
	r.trim(4, 60)               // entries 0..3 were written; 4..9 (60 bytes) were not
	if r.first != 2 {
		t.Fatalf("first = %d, want 2 (trim written bytes to the limit, ignoring the backlog)", r.first)
	}
	r.trim(4, 60) // the written 20 bytes are already within the limit
	if r.first != 2 {
		t.Fatalf("first = %d after a second trim, want 2", r.first)
	}
	r.trim(10, 0)
	if r.first != 8 || r.bytes != 20 {
		t.Fatalf("first, bytes = %d, %d; want 8, 20", r.first, r.bytes)
	}
}

// While held, trim keeps the entries the peer has not acknowledged, but only
// up to twice limit of written bytes: a server that never catches up must
// not grow the ring without bound.
func TestRingTrimHoldCeiling(t *testing.T) {
	r := filledRing(25, 10, 10) // 100 bytes held, limit 25
	r.held, r.hold = true, 3
	r.trim(10, 0)
	if r.first != 5 || r.bytes != 50 {
		t.Fatalf("first, bytes = %d, %d; want 5, 50 (trim unacknowledged entries only down to twice the limit)", r.first, r.bytes)
	}
	r.hold = 7
	r.trim(10, 0)
	if r.first != 7 {
		t.Fatalf("first = %d, want 7 (acknowledged entries trim down to the limit, not below the hold)", r.first)
	}
}

func TestRingSince(t *testing.T) {
	r := filledRing(25, 10, 10)
	r.trim(10, 0) // retains 8 and 9
	tests := []struct {
		name string
		from int64
		want int
		ok   bool
	}{
		{name: "whole window", from: 8, want: 2, ok: true},
		{name: "peer up to date", from: 10, want: 0, ok: true},
		{name: "peer behind the window", from: 7, ok: false},
		{name: "peer ahead of us", from: 11, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := r.since(tt.from, r.next())
			if ok != tt.ok || len(got) != tt.want {
				t.Fatalf("since(%d) = %d entries, %v; want %d, %v", tt.from, len(got), ok, tt.want, tt.ok)
			}
			if ok && tt.want > 0 && got[0][0] != byte(tt.from) {
				t.Fatalf("first entry holds %d, want %d", got[0][0], tt.from)
			}
		})
	}
}

// since returns a copy: trim, which clears the slots it drops, leaves the
// entries since returned intact, as writeRecover relies on after releasing
// the lock.
func TestRingSinceIsACopy(t *testing.T) {
	r := filledRing(20, 5, 10)
	got, ok := r.since(r.first, r.next())
	if !ok || len(got) != 5 {
		t.Fatalf("since(first, next) = %d entries, %v; want 5, true", len(got), ok)
	}
	r.trim(r.next(), 0)
	if r.first == 0 {
		t.Fatal("trim dropped nothing; the test needs it to drop entries")
	}
	for i, e := range got {
		if want := bytes.Repeat([]byte{byte(i)}, 10); !bytes.Equal(e, want) {
			t.Fatalf("entry %d after trim = %v, want %v", i, e, want)
		}
	}
}

func TestRingBytesBetweenAndRange(t *testing.T) {
	r := filledRing(1<<20, 6, 10)
	if got := r.bytesBetween(2, 5); got != 30 {
		t.Fatalf("bytesBetween(2, 5) = %d, want 30", got)
	}
	got := r.appendRange(nil, 1, 3)
	if len(got) != 2 || got[0][0] != 1 || got[1][0] != 2 {
		t.Fatalf("appendRange(1, 3) = %v", got)
	}
}

// The replay window is upstream's MAX_BACKUP_BYTES: exactly 64 MiB of sealed
// written packets (src/base/BackedWriter.hpp:32 at et-v7.0.0). A peer at the
// oldest retained sequence is still replayable; one sequence older is outside
// the window and must be refused, which is what writeRecover reports as
// ErrReplayExceeded on reconnect.
func TestRingReplaysAtThe64MiBBoundary(t *testing.T) {
	if defaultReplayLimit != 64<<20 {
		t.Fatalf("defaultReplayLimit = %d, want upstream's 64 MiB", defaultReplayLimit)
	}
	const entry = 8 << 20
	r := filledRing(defaultReplayLimit, 9, entry) // 72 MiB: one entry over the window
	r.trim(9, 0)
	if r.first != 1 || r.bytes != 8*entry {
		t.Fatalf("after trim: first, bytes = %d, %d; want 1, %d", r.first, r.bytes, 8*entry)
	}
	if got, ok := r.since(r.first, r.next()); !ok || len(got) != 8 {
		t.Fatalf("since(oldest retained) = %d entries, %v; want the full 64 MiB window, true", len(got), ok)
	}
	if _, ok := r.since(r.first-1, r.next()); ok {
		t.Fatal("since(one sequence before the window) succeeded; a too-old sequence must be refused")
	}
}

// A Dialer without ReplayLimit is the transport's default: the replay ring
// retains upstream's 64 MiB, not a test-sized window.
func TestDefaultDialerRetainsThe64MiBWindow(t *testing.T) {
	c := (&Dialer{}).newConn("127.0.0.1:2022", "client", strings.Repeat("k", 32))
	if c.ring.limit != 64<<20 {
		t.Fatalf("default ring limit = %d, want 64 MiB", c.ring.limit)
	}
}
