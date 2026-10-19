package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainconn "github.com/mslxl/natcatty/domain/connection"
)

func TestKnownHostsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "natcatty.db")

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1700000000, 0).UTC()
	record := domainconn.KnownHost{
		ID: "kh-1", Hostname: "Example.COM", Port: 2222, KeyType: "ssh-ed25519",
		PublicKey: "ssh-ed25519 AAAA", Fingerprint: "fingerprint-1",
		DiscoveredAt: when, LastSeen: when,
	}
	if err := first.UpsertKnownHost(ctx, record); err != nil {
		t.Fatalf("UpsertKnownHost: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	records, err := second.ListKnownHosts(ctx)
	if err != nil {
		t.Fatalf("ListKnownHosts: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v, want one", records)
	}
	got := records[0]
	if got.ID != record.ID || got.Hostname != record.Hostname || got.Port != record.Port ||
		got.KeyType != record.KeyType || got.PublicKey != record.PublicKey ||
		got.Fingerprint != record.Fingerprint {
		t.Fatalf("Known Host after reopen = %+v, want %+v", got, record)
	}
	if !got.DiscoveredAt.Equal(when) || !got.LastSeen.Equal(when) {
		t.Fatalf("timestamps = %v / %v, want %v", got.DiscoveredAt, got.LastSeen, when)
	}
}

func TestUpsertKnownHostMatchesTheEndpointAndKeepsIdentity(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)

	discovered := time.Unix(1700000000, 0).UTC()
	first := domainconn.KnownHost{
		ID: "kh-first", Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519",
		Fingerprint: "old", DiscoveredAt: discovered, LastSeen: discovered,
	}
	if err := store.UpsertKnownHost(ctx, first); err != nil {
		t.Fatal(err)
	}

	rotated := time.Unix(1700000100, 0).UTC()
	update := domainconn.KnownHost{
		ID: "kh-second", Hostname: "EXAMPLE.com", Port: 22, KeyType: "ssh-ed25519",
		Fingerprint: "new", DiscoveredAt: rotated, LastSeen: rotated,
	}
	if err := store.UpsertKnownHost(ctx, update); err != nil {
		t.Fatal(err)
	}

	records, err := store.ListKnownHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v, want one for the endpoint and key type", records)
	}
	got := records[0]
	if got.ID != "kh-first" {
		t.Fatalf("ID = %q, want the first record's id", got.ID)
	}
	if got.Fingerprint != "new" {
		t.Fatalf("Fingerprint = %q, want new", got.Fingerprint)
	}
	if !got.DiscoveredAt.Equal(discovered) {
		t.Fatalf("DiscoveredAt = %v, want %v", got.DiscoveredAt, discovered)
	}
	if !got.LastSeen.Equal(rotated) {
		t.Fatalf("LastSeen = %v, want %v", got.LastSeen, rotated)
	}
}

func TestUpsertKnownHostByIDDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	when := time.Unix(1700000000, 0).UTC()

	record := domainconn.KnownHost{
		ID: "kh-1", Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519",
		Fingerprint: "one", DiscoveredAt: when, LastSeen: when,
	}
	if err := store.UpsertKnownHost(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.Fingerprint = "two"
	if err := store.UpsertKnownHost(ctx, record); err != nil {
		t.Fatal(err)
	}

	records, err := store.ListKnownHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Fingerprint != "two" {
		t.Fatalf("records = %+v, want one updated record", records)
	}
}
