package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

func openHistoryStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func at(hour int) time.Time {
	return time.Date(2026, 3, 1, hour, 0, 0, 0, time.UTC)
}

func TestConnectionLogsRoundTripInNewestFirstOrder(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	for index := 0; index < 3; index++ {
		if err := store.SaveConnectionLog(ctx, domainhistory.ConnectionLog{
			ID: fmt.Sprintf("log-%d", index), HostID: "host-1", HostLabel: "web-1",
			Hostname: "10.0.0.1", Username: "deploy", Kind: "ssh",
			Status: domainhistory.ConnectionConnected, StartedAt: at(index),
			EndedAt: at(index),
		}); err != nil {
			t.Fatalf("SaveConnectionLog: %v", err)
		}
	}
	logs, err := store.ListConnectionLogs(ctx)
	if err != nil {
		t.Fatalf("ListConnectionLogs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("logs = %d, want 3", len(logs))
	}
	if logs[0].ID != "log-2" || logs[2].ID != "log-0" {
		t.Fatalf("order = %s..%s, want newest first", logs[0].ID, logs[2].ID)
	}
	if logs[1].HostLabel != "web-1" || logs[1].Status != domainhistory.ConnectionConnected {
		t.Fatalf("round trip = %+v", logs[1])
	}
}

func TestHistoryEntriesRoundTripAndDeleteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	entry := domainhistory.Entry{
		ID: "entry-1", SessionID: "session-1", HostID: "host-1",
		HostLabel: "web-1", Scope: "host:host-1", Command: "git status",
		Source: domainhistory.SourceSession, RecordedAt: at(9),
	}
	if err := store.SaveHistoryEntry(ctx, entry); err != nil {
		t.Fatalf("SaveHistoryEntry: %v", err)
	}
	entries, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].Command != "git status" {
		t.Fatalf("entries = %+v", entries)
	}
	if err := store.DeleteHistoryEntry(ctx, "missing"); err != nil {
		t.Fatalf("deleting a missing row: %v", err)
	}
	if err := store.DeleteHistoryEntry(ctx, "entry-1"); err != nil {
		t.Fatalf("DeleteHistoryEntry: %v", err)
	}
	entries, _ = store.ListHistoryEntries(ctx)
	if len(entries) != 0 {
		t.Fatalf("entries remain: %+v", entries)
	}
}

func TestPruneConnectionLogsKeepsExactlyTheConfiguredCount(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	for index := 0; index < 6; index++ {
		_ = store.SaveConnectionLog(ctx, domainhistory.ConnectionLog{
			ID: fmt.Sprintf("log-%d", index), Kind: "ssh",
			Status: domainhistory.ConnectionConnected, StartedAt: at(index),
		})
	}
	removed, err := store.PruneConnectionLogs(ctx,
		domainhistory.Retention{Count: 4}, at(12))
	if err != nil {
		t.Fatalf("PruneConnectionLogs: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	logs, _ := store.ListConnectionLogs(ctx)
	if len(logs) != 4 || logs[0].ID != "log-5" || logs[3].ID != "log-2" {
		t.Fatalf("kept = %+v, want log-5..log-2", logs)
	}
}

func TestPruneHistoryEntriesAppliesAgeThenCount(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	now := at(12)
	for index := 0; index < 6; index++ {
		_ = store.SaveHistoryEntry(ctx, domainhistory.Entry{
			ID: fmt.Sprintf("entry-%d", index), Scope: "local",
			Command: "command", RecordedAt: now.Add(-time.Duration(index) * 4 * time.Hour),
		})
	}
	// entry-4 and entry-5 are older than six hours; the count keeps two.
	removed, err := store.PruneHistoryEntries(ctx, domainhistory.Retention{
		Count: 2, MaxAge: 6 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("PruneHistoryEntries: %v", err)
	}
	if removed != 4 {
		t.Fatalf("removed = %d, want 4", removed)
	}
	entries, _ := store.ListHistoryEntries(ctx)
	if len(entries) != 2 || entries[0].ID != "entry-0" || entries[1].ID != "entry-1" {
		t.Fatalf("kept = %+v", entries)
	}
}

func TestZeroRetentionPruneRemovesNothing(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	_ = store.SaveHistoryEntry(ctx, domainhistory.Entry{
		ID: "entry-1", Scope: "local", Command: "ls", RecordedAt: at(9),
	})
	for _, retention := range []domainhistory.Retention{
		{}, {Count: 0, MaxAge: 0},
	} {
		removed, err := store.PruneHistoryEntries(ctx, retention, at(12))
		if err != nil || removed != 0 {
			t.Fatalf("prune(%+v) = %d, %v; want 0", retention, removed, err)
		}
	}
	entries, _ := store.ListHistoryEntries(ctx)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
}

func TestClearHistoryReportsTheRemovedCount(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	for index := 0; index < 3; index++ {
		_ = store.SaveHistoryEntry(ctx, domainhistory.Entry{
			ID: fmt.Sprintf("entry-%d", index), Scope: "local",
			Command: "ls", RecordedAt: at(index),
		})
	}
	removed, err := store.ClearHistoryEntries(ctx)
	if err != nil || removed != 3 {
		t.Fatalf("ClearHistoryEntries = %d, %v; want 3", removed, err)
	}
	entries, _ := store.ListHistoryEntries(ctx)
	if len(entries) != 0 {
		t.Fatalf("entries remain: %d", len(entries))
	}
}
