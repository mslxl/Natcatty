package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
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
	if err := store.RecordHistoryEntry(ctx, entry, nil); err != nil {
		t.Fatalf("RecordHistoryEntry: %v", err)
	}
	entries, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].Command != "git status" {
		t.Fatalf("entries = %+v", entries)
	}
	if err := store.SoftDeleteHistoryEntry(ctx, "missing", at(10), nil); err != nil {
		t.Fatalf("deleting a missing row: %v", err)
	}
	if err := store.SoftDeleteHistoryEntry(ctx, "entry-1", at(10), nil); err != nil {
		t.Fatalf("SoftDeleteHistoryEntry: %v", err)
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

func TestClearConnectionLogsReportsTheRemovedCount(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	for index := 0; index < 3; index++ {
		_ = store.SaveConnectionLog(ctx, domainhistory.ConnectionLog{
			ID: fmt.Sprintf("log-%d", index), Kind: "ssh",
			Status: domainhistory.ConnectionConnected, StartedAt: at(index),
		})
	}
	_ = store.RecordHistoryEntry(ctx, domainhistory.Entry{
		ID: "entry-1", Scope: "local", Command: "ls", RecordedAt: at(1),
	}, nil)
	removed, err := store.ClearConnectionLogs(ctx)
	if err != nil || removed != 3 {
		t.Fatalf("ClearConnectionLogs = %d, %v; want 3", removed, err)
	}
	logs, _ := store.ListConnectionLogs(ctx)
	if len(logs) != 0 {
		t.Fatalf("logs remain: %d", len(logs))
	}
	entries, _ := store.ListHistoryEntries(ctx)
	if len(entries) != 1 {
		t.Fatalf("clearing logs removed %d commands, want 1 left", len(entries))
	}
}

func TestHistoryEntryFullFieldSetRoundTripsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entry := domainhistory.Entry{
		ID:          "9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
		SessionID:   "session-1",
		SessionUUID: domainhistory.SessionUUID("session-1"),
		HostID:      "host-1",
		HostLabel:   "web-1",
		Scope:       "host:host-1",
		Command:     "git status",
		CWD:         "/srv/app",
		Shell:       "zsh",
		RecordedAt:  at(9),
		FinishedAt:  at(9).Add(1500 * time.Millisecond),
		Duration:    1500 * time.Millisecond,
		ExitCode:    2,
		HasExitCode: true,
		Origin:      "web-1.example.com:deploy",
		Author:      "deploy",
		AuthorKind:  domainhistory.AuthorKindUser,
		Intent:      "check the working tree",
		DeletedAt:   at(10),
		Source:      domainhistory.SourceSession,
		CaptureTier: domainhistory.CaptureTierHook,
	}
	if err := first.RecordHistoryEntry(ctx, entry, nil); err != nil {
		t.Fatalf("RecordHistoryEntry: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	// The row is soft-deleted, so normal listing excludes it; its canonical
	// data still round-trips through the by-identity read.
	stored, found, err := second.HistoryEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("HistoryEntry: %v", err)
	}
	if !found {
		t.Fatal("entry was lost after reopen")
	}
	if !reflect.DeepEqual(stored, entry) {
		t.Fatalf("entry after reopen = %+v, want %+v", stored, entry)
	}

	// The projection columns, not just the canonical JSON, must carry the
	// values the History surface and sync queries read.
	rows, err := second.db.Query(ctx, `SELECT session_id, session_uuid, cwd, shell,
			finished_at, duration, exit_code, has_exit_code, origin, author,
			author_kind, intent, deleted_at, capture_tier
		FROM history_entries WHERE id = ?`, entry.ID)
	if err != nil {
		t.Fatalf("projection query: %v", err)
	}
	if len(rows.Values) != 1 {
		t.Fatalf("projection rows = %d, want 1", len(rows.Values))
	}
	got := rows.Values[0]
	want := []any{
		"session-1", entry.SessionUUID, "/srv/app", "zsh",
		entry.FinishedAt.UnixMilli(), int64(entry.Duration), int64(2), int64(1),
		"web-1.example.com:deploy", "deploy", domainhistory.AuthorKindUser,
		"check the working tree", entry.DeletedAt.UnixMilli(), int64(domainhistory.CaptureTierHook),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection = %#v, want %#v", got, want)
	}
}

func TestPreviousBuildHistoryRowOpensWithZeroNewFields(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.db")
	legacyData := `{"id":"legacy-1","sessionId":"session-1","hostId":"host-1",` +
		`"hostLabel":"web-1","scope":"host:host-1","command":"make test",` +
		`"source":"session","recordedAt":"2026-03-01T09:00:00Z"}`

	previous, err := platformsqlite.Open(ctx, path, platformsqlite.OpenOptions{})
	if err != nil {
		t.Fatalf("open previous database: %v", err)
	}
	// The exact schema the previous build's AutoMigrate produced.
	statements := []string{
		"CREATE TABLE `history_entries` (`id` text,`session_id` text NOT NULL DEFAULT \"\"," +
			"`host_id` text NOT NULL DEFAULT \"\",`host_label` text NOT NULL DEFAULT \"\"," +
			"`scope` text NOT NULL DEFAULT \"\",`command` text NOT NULL," +
			"`source` text NOT NULL DEFAULT \"\",`recorded_at` integer NOT NULL," +
			"`data` text NOT NULL,PRIMARY KEY (`id`))",
		"CREATE INDEX `history_entries_scope` ON `history_entries`(`scope`,`recorded_at` desc,`id` desc)",
		"CREATE INDEX `history_entries_host` ON `history_entries`(`host_id`,`recorded_at` desc,`id` desc)",
		"CREATE INDEX `history_entries_recorded` ON `history_entries`(`recorded_at` desc,`id` desc)",
	}
	for _, statement := range statements {
		if _, err := previous.Execute(ctx, statement); err != nil {
			t.Fatalf("previous schema: %v", err)
		}
	}
	if _, err := previous.Execute(ctx, `INSERT INTO history_entries
			(id, session_id, host_id, host_label, scope, command, source, recorded_at, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-1", "session-1", "host-1", "web-1", "host:host-1", "make test",
		"session", at(9).UnixMilli(), legacyData); err != nil {
		t.Fatalf("previous row: %v", err)
	}
	if err := previous.Close(); err != nil {
		t.Fatalf("close previous database: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open after AutoMigrate: %v", err)
	}
	defer store.Close()
	entries, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want the previous row preserved", len(entries))
	}
	entry := entries[0]
	if entry.ID != "legacy-1" || entry.Command != "make test" ||
		entry.HostID != "host-1" || !entry.RecordedAt.Equal(at(9)) {
		t.Fatalf("previous fields changed: %+v", entry)
	}
	if entry.SessionUUID != "" || entry.CWD != "" || entry.Shell != "" ||
		!entry.FinishedAt.IsZero() || entry.Duration != 0 || entry.ExitCode != 0 ||
		entry.HasExitCode || entry.Origin != "" || entry.Author != "" ||
		entry.AuthorKind != "" || entry.Intent != "" || !entry.DeletedAt.IsZero() ||
		entry.CaptureTier != 0 {
		t.Fatalf("new fields did not decode as zero: %+v", entry)
	}

	// AutoMigrate must not rewrite the canonical JSON of an existing row.
	rows, err := store.db.Query(ctx, `SELECT data FROM history_entries WHERE id = ?`, "legacy-1")
	if err != nil {
		t.Fatalf("read canonical data: %v", err)
	}
	if len(rows.Values) != 1 || rows.Values[0][0] != legacyData {
		t.Fatalf("canonical data changed: %#v", rows.Values)
	}
	// Every new projection column exists and holds its zero default.
	rows, err = store.db.Query(ctx, `SELECT session_uuid, cwd, shell, finished_at,
			duration, exit_code, has_exit_code, origin, author, author_kind,
			intent, deleted_at, capture_tier
		FROM history_entries WHERE id = ?`, "legacy-1")
	if err != nil {
		t.Fatalf("projection query: %v", err)
	}
	want := []any{"", "", "", int64(0), int64(0), int64(0), int64(0),
		"", "", "", "", int64(0), int64(0)}
	if !reflect.DeepEqual(rows.Values[0], want) {
		t.Fatalf("legacy projection = %#v, want %#v", rows.Values[0], want)
	}
}

func TestHistoryProjectionIndexesServeTheHistoryAndSyncQueries(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	for index := 0; index < 40; index++ {
		entry := domainhistory.Entry{
			ID:          fmt.Sprintf("entry-%02d", index),
			SessionID:   model.SessionID(fmt.Sprintf("session-%d", index%10)),
			SessionUUID: fmt.Sprintf("session-uuid-%d", index%10),
			HostID:      model.HostID(fmt.Sprintf("host-%d", index%10)),
			Scope:       fmt.Sprintf("host:host-%d", index%10),
			Command:     fmt.Sprintf("command-%d", index),
			Origin:      fmt.Sprintf("origin-%d.example:deploy", index%10),
			RecordedAt:  at(index % 24),
			Source:      domainhistory.SourceSession,
		}
		if index%10 == 0 {
			entry.DeletedAt = at(index % 24).Add(time.Minute)
		}
		if err := store.RecordHistoryEntry(ctx, entry, nil); err != nil {
			t.Fatalf("RecordHistoryEntry: %v", err)
		}
		if _, err := store.db.Execute(ctx, `UPDATE history_entries SET recorded_at = ?
				WHERE id = ?`, at(index%24).UnixMilli()+int64(index), entry.ID); err != nil {
			t.Fatalf("spread recorded times: %v", err)
		}
	}

	required := []string{
		"history_entries_recorded",
		"history_entries_host",
		"history_entries_scope",
		"history_entries_session",
		"history_entries_session_uuid",
		"history_entries_origin",
		"history_entries_deleted",
	}
	indexes := indexNames(t, store)
	for _, name := range required {
		if !indexes[name] {
			t.Fatalf("history_entries is missing index %q (have %v)", name, indexes)
		}
	}

	queries := []struct {
		name  string
		sql   string
		index string
	}{
		{"recorded time", `SELECT data FROM history_entries ORDER BY recorded_at DESC, id DESC LIMIT 5`, "history_entries_recorded"},
		{"host", `SELECT data FROM history_entries WHERE host_id = 'host-3' ORDER BY recorded_at DESC, id DESC LIMIT 5`, "history_entries_host"},
		{"scope", `SELECT data FROM history_entries WHERE scope = 'host:host-3' ORDER BY recorded_at DESC, id DESC LIMIT 5`, "history_entries_scope"},
		{"session", `SELECT data FROM history_entries WHERE session_id = 'session-3' LIMIT 5`, "history_entries_session"},
		{"session uuid", `SELECT data FROM history_entries WHERE session_uuid = 'session-uuid-3' LIMIT 5`, "history_entries_session_uuid"},
		{"origin", `SELECT data FROM history_entries WHERE origin = 'origin-3.example:deploy' LIMIT 5`, "history_entries_origin"},
		{"deletion state", `SELECT data FROM history_entries WHERE deleted_at > 0 LIMIT 5`, "history_entries_deleted"},
	}
	for _, query := range queries {
		rows, err := store.db.Query(ctx, "EXPLAIN QUERY PLAN "+query.sql)
		if err != nil {
			t.Fatalf("%s query plan: %v", query.name, err)
		}
		var details []string
		for _, row := range rows.Values {
			details = append(details, fmt.Sprint(row[len(row)-1]))
		}
		plan := strings.Join(details, "\n")
		if !strings.Contains(plan, query.index) {
			t.Fatalf("%s query did not use %s:\n%s", query.name, query.index, plan)
		}
	}
}

func indexNames(t *testing.T, store *Store) map[string]bool {
	t.Helper()
	rows, err := store.db.Query(context.Background(), `PRAGMA index_list('history_entries')`)
	if err != nil {
		t.Fatalf("index_list: %v", err)
	}
	names := make(map[string]bool, len(rows.Values))
	for _, row := range rows.Values {
		// pragma index_list: seq, name, unique, origin, partial
		if len(row) >= 2 {
			names[fmt.Sprint(row[1])] = true
		}
	}
	return names
}

func TestSoftDeleteHistoryEntriesMarksEveryLiveRowInOneTransaction(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	deletions := make([]domainhistory.EntryDeletion, 0, 3)
	for index := 0; index < 3; index++ {
		entry := domainhistory.Entry{
			ID: fmt.Sprintf("entry-%d", index), Scope: "local",
			Command: "ls", RecordedAt: at(index),
		}
		_ = store.RecordHistoryEntry(ctx, entry, nil)
		deletions = append(deletions, domainhistory.EntryDeletion{ID: entry.ID})
	}
	// A missing identity is skipped rather than failing the batch.
	deletions = append(deletions, domainhistory.EntryDeletion{ID: "missing"})
	removed, err := store.SoftDeleteHistoryEntries(ctx, deletions, at(10))
	if err != nil || removed != 3 {
		t.Fatalf("SoftDeleteHistoryEntries = %d, %v; want 3", removed, err)
	}
	entries, _ := store.ListHistoryEntries(ctx)
	if len(entries) != 0 {
		t.Fatalf("live entries remain: %d", len(entries))
	}
	for _, deletion := range deletions[:3] {
		stored, found, err := store.HistoryEntry(ctx, deletion.ID)
		if err != nil || !found || stored.DeletedAt.IsZero() {
			t.Fatalf("row %q = %+v, found=%v, err=%v; want it soft-deleted",
				deletion.ID, stored, found, err)
		}
	}
}
