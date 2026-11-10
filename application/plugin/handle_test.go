package plugin

import (
	"errors"
	"testing"
)

func TestHandleTableIssuesResolvesAndReleases(t *testing.T) {
	table := NewHandleTable(2)
	first, err := table.Issue("first")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if table.Len() != 1 {
		t.Fatalf("live handles = %d, want 1", table.Len())
	}
	value, err := table.Resolve(first)
	if err != nil || value != "first" {
		t.Fatalf("Resolve = %v, %v, want first", value, err)
	}
	if err := table.Release(first); err != nil {
		t.Fatalf("Release: %v", err)
	}
	var invalid *InvalidHandleError
	if _, err := table.Resolve(first); !errors.As(err, &invalid) {
		t.Fatalf("Resolve released handle = %v, want InvalidHandleError", err)
	}

	reused, err := table.Issue("second")
	if err != nil {
		t.Fatalf("Issue after release: %v", err)
	}
	if reused.Slot() != first.Slot() {
		t.Fatalf("reused slot = %d, want %d", reused.Slot(), first.Slot())
	}
	if reused.Generation() == first.Generation() {
		t.Fatal("a reused slot kept the old generation, so the stale handle would resolve")
	}
	if _, err := table.Resolve(first); !errors.As(err, &invalid) {
		t.Fatalf("stale handle resolved after slot reuse: %v", err)
	}
	if value, err := table.Resolve(reused); err != nil || value != "second" {
		t.Fatalf("Resolve reused = %v, %v, want second", value, err)
	}
}

func TestHandleTableEnforcesTheQuota(t *testing.T) {
	table := NewHandleTable(1)
	if _, err := table.Issue("one"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, err := table.Issue("two")
	var limit *LimitError
	if !errors.As(err, &limit) {
		t.Fatalf("Issue over quota = %v, want LimitError", err)
	}
	if limit.Resource != "plugin handles" || limit.Limit != 1 {
		t.Fatalf("limit = %+v, want the handle quota", limit)
	}
}

func TestHandleTableRevokeAllInvalidatesEveryHandle(t *testing.T) {
	table := NewHandleTable(4)
	first, err := table.Issue("first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := table.Issue("second")
	if err != nil {
		t.Fatal(err)
	}
	if table.Len() != 2 {
		t.Fatalf("live handles = %d, want 2", table.Len())
	}
	table.RevokeAll()
	if table.Len() != 0 {
		t.Fatalf("live handles after revocation = %d, want 0", table.Len())
	}
	var invalid *InvalidHandleError
	for _, handle := range []Handle{first, second} {
		if _, err := table.Resolve(handle); !errors.As(err, &invalid) {
			t.Fatalf("Resolve %#x after revocation = %v, want InvalidHandleError", uint64(handle), err)
		}
	}

	// Revoked slots are reusable, but old handles stay stale.
	fresh, err := table.Issue("fresh")
	if err != nil {
		t.Fatalf("Issue after revocation: %v", err)
	}
	if value, err := table.Resolve(fresh); err != nil || value != "fresh" {
		t.Fatalf("Resolve fresh = %v, %v", value, err)
	}
	if _, err := table.Resolve(first); !errors.As(err, &invalid) {
		t.Fatalf("an old handle resolved after revocation: %v", err)
	}
	if _, err := NewHandleTable(0).Issue("default"); err != nil {
		t.Fatalf("Issue with the default quota: %v", err)
	}
}

func TestHandleTableRejectsInventedHandles(t *testing.T) {
	table := NewHandleTable(2)
	var invalid *InvalidHandleError
	if _, err := table.Resolve(Handle(0)); !errors.As(err, &invalid) {
		t.Fatalf("Resolve zero handle = %v, want InvalidHandleError", err)
	}
	if _, err := table.Resolve(Handle(1<<32 | 99)); !errors.As(err, &invalid) {
		t.Fatalf("Resolve out-of-range slot = %v, want InvalidHandleError", err)
	}
	if err := table.Release(Handle(1 << 32)); !errors.As(err, &invalid) {
		t.Fatalf("Release invented handle = %v, want InvalidHandleError", err)
	}
}
