package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

type memoryStore struct {
	logs      []domainhistory.ConnectionLog
	entries   []domainhistory.Entry
	records   []domainhistory.Record
	metadata  domainhistory.Metadata
	writer    string
	fail      error
	failWrite error
	saves     int
}

func (s *memoryStore) ListConnectionLogs(context.Context) ([]domainhistory.ConnectionLog, error) {
	if s.fail != nil {
		return nil, s.fail
	}
	out := append([]domainhistory.ConnectionLog(nil), s.logs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

func (s *memoryStore) SaveConnectionLog(_ context.Context, log domainhistory.ConnectionLog) error {
	if s.fail != nil {
		return s.fail
	}
	s.saves++
	s.logs = replaceLog(s.logs, log)
	return nil
}

func (s *memoryStore) DeleteConnectionLog(_ context.Context, id string) error {
	for index, log := range s.logs {
		if log.ID == id {
			s.logs = append(s.logs[:index], s.logs[index+1:]...)
			return nil
		}
	}
	return nil
}

func (s *memoryStore) ClearConnectionLogs(context.Context) (int, error) {
	removed := len(s.logs)
	s.logs = nil
	return removed, nil
}

func (s *memoryStore) PruneConnectionLogs(_ context.Context, retention domainhistory.Retention, now time.Time) (int, error) {
	kept := domainhistory.PruneConnectionLogs(s.logs, retention, now)
	removed := len(s.logs) - len(kept)
	s.logs = kept
	return removed, nil
}

func (s *memoryStore) ListHistoryEntries(context.Context) ([]domainhistory.Entry, error) {
	if s.fail != nil {
		return nil, s.fail
	}
	out := make([]domainhistory.Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry.DeletedAt.IsZero() {
			out = append(out, entry)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RecordedAt.After(out[j].RecordedAt) })
	return out, nil
}

func (s *memoryStore) HistoryEntry(_ context.Context, id string) (domainhistory.Entry, bool, error) {
	if s.fail != nil {
		return domainhistory.Entry{}, false, s.fail
	}
	for _, entry := range s.entries {
		if entry.ID == id {
			return entry, true, nil
		}
	}
	return domainhistory.Entry{}, false, nil
}

func (s *memoryStore) RecordHistoryEntry(_ context.Context, entry domainhistory.Entry, record *domainhistory.Record) error {
	if s.fail != nil {
		return s.fail
	}
	if s.failWrite != nil {
		return s.failWrite
	}
	s.saves++
	s.entries = replaceEntry(s.entries, entry)
	if record != nil {
		s.appendRecord(*record)
	}
	return nil
}

func (s *memoryStore) SoftDeleteHistoryEntry(_ context.Context, id string, deletedAt time.Time, record *domainhistory.Record) error {
	if s.fail != nil {
		return s.fail
	}
	if s.failWrite != nil {
		return s.failWrite
	}
	for index := range s.entries {
		if s.entries[index].ID != id || !s.entries[index].DeletedAt.IsZero() {
			continue
		}
		s.entries[index].DeletedAt = deletedAt
		if record != nil {
			s.appendRecord(*record)
		}
		return nil
	}
	return nil
}

func (s *memoryStore) appendRecord(record domainhistory.Record) {
	var next int64
	for _, stored := range s.records {
		if stored.SeriesHost == record.SeriesHost && stored.Tag == record.Tag {
			next = stored.Index + 1
		}
	}
	record.Index = next
	s.records = append(s.records, record)
}

func (s *memoryStore) EnsureWriterUUID(_ context.Context, generate func() string) (string, error) {
	if s.fail != nil {
		return "", s.fail
	}
	if s.writer == "" {
		s.writer = generate()
	}
	return s.writer, nil
}

func (s *memoryStore) HistoryMetadata(context.Context) (domainhistory.Metadata, error) {
	if s.fail != nil {
		return domainhistory.Metadata{}, s.fail
	}
	return s.metadata, nil
}

func (s *memoryStore) ListHistoryEntriesIncludingDeleted(context.Context) ([]domainhistory.Entry, error) {
	if s.fail != nil {
		return nil, s.fail
	}
	out := append([]domainhistory.Entry(nil), s.entries...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].RecordedAt.After(out[j].RecordedAt) })
	return out, nil
}

func (s *memoryStore) ImportShellHistory(_ context.Context, imports []ShellHistoryImport) error {
	if s.fail != nil {
		return s.fail
	}
	if s.failWrite != nil {
		return s.failWrite
	}
	if s.metadata.ShellHistoryImported {
		return nil
	}
	for _, imported := range imports {
		s.entries = replaceEntry(s.entries, imported.Entry)
		s.appendRecord(imported.Record)
	}
	s.metadata.ShellHistoryImported = true
	s.saves += len(imports)
	return nil
}

func (s *memoryStore) ClearHistoryEntries(context.Context) (int, error) {
	removed := len(s.entries)
	s.entries = nil
	return removed, nil
}

func replaceLog(logs []domainhistory.ConnectionLog, log domainhistory.ConnectionLog) []domainhistory.ConnectionLog {
	for index := range logs {
		if logs[index].ID == log.ID {
			logs[index] = log
			return logs
		}
	}
	return append(logs, log)
}

func replaceEntry(entries []domainhistory.Entry, entry domainhistory.Entry) []domainhistory.Entry {
	for index := range entries {
		if entries[index].ID == entry.ID {
			entries[index] = entry
			return entries
		}
	}
	return append(entries, entry)
}

func at(hour int) time.Time {
	return time.Date(2026, 3, 1, hour, 0, 0, 0, time.UTC)
}

func newTestService(store *memoryStore, retention Retention) *Service {
	next := 0
	return NewService(store,
		WithRetention(func() Retention { return retention }),
		WithClock(func() time.Time { return at(12) }),
		WithIDGenerator(func() string {
			next++
			return fmt.Sprintf("id-%02d", next)
		}),
	)
}

func TestRecordConnectionDerivesIdentityStatusAndTimestamps(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{})
	if err := service.RecordConnection(context.Background(), domainhistory.ConnectionLog{
		HostID: "host-1", HostLabel: " web-1 ", Kind: "ssh", StartedAt: at(9),
	}); err != nil {
		t.Fatalf("RecordConnection: %v", err)
	}
	if len(store.logs) != 1 {
		t.Fatalf("logs = %+v, want one", store.logs)
	}
	log := store.logs[0]
	if log.ID == "" || log.Status != domainhistory.ConnectionConnected {
		t.Fatalf("log = %+v, want an id and connected status", log)
	}
	if log.HostLabel != "web-1" {
		t.Fatalf("label = %q, want trimmed", log.HostLabel)
	}
	if log.EndedAt.IsZero() {
		t.Fatalf("EndedAt is zero")
	}

	if err := service.RecordConnection(context.Background(), domainhistory.ConnectionLog{
		HostID: "host-1", Kind: "ssh", Error: "permission denied", StartedAt: at(10),
	}); err != nil {
		t.Fatalf("RecordConnection failure: %v", err)
	}
	if store.logs[1].Status != domainhistory.ConnectionFailed {
		t.Fatalf("status = %q, want failed", store.logs[1].Status)
	}
}

func TestRecordAssignsUUIDv7AndStableSessionUUID(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store, WithClock(func() time.Time { return at(12) }))
	if err := service.Record(context.Background(), domainhistory.Entry{
		SessionID: "session-1", HostID: "host-1", Command: "git status",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	entry := store.entries[0]
	if !domainhistory.IsUUID(entry.ID) || entry.ID[14] != '7' {
		t.Fatalf("captured identity = %q, want a UUIDv7", entry.ID)
	}
	if entry.SessionUUID != domainhistory.SessionUUID("session-1") {
		t.Fatalf("SessionUUID = %q, want the stable Session derivation", entry.SessionUUID)
	}
	if !entry.HasUUIDIdentity() {
		t.Fatal("a captured row did not report a UUID identity")
	}
}

func TestRecordKeepsAtuinUUIDsAndDerivesOnlyMissingIdentities(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store, WithClock(func() time.Time { return at(12) }))
	ctx := context.Background()

	const atuinID = "9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	imported := domainhistory.Entry{
		ID: atuinID, Source: domainhistory.SourceAtuinImport, Scope: "remote:example.com:deploy",
		Command: "uptime", Origin: "example.com:deploy", RecordedAt: at(9),
	}
	if err := service.Record(ctx, imported); err != nil {
		t.Fatalf("Record imported: %v", err)
	}
	if store.entries[0].ID != atuinID {
		t.Fatalf("imported identity = %q, want the Atuin UUID", store.entries[0].ID)
	}

	legacy := domainhistory.Entry{
		ID: "legacy-local", Source: domainhistory.SourceSession, Scope: "local",
		Command: "ls", RecordedAt: at(9),
	}
	if err := service.Record(ctx, legacy); err != nil {
		t.Fatalf("Record legacy: %v", err)
	}
	if store.entries[1].ID != "legacy-local" || store.entries[1].HasUUIDIdentity() {
		t.Fatalf("legacy identity = %q, want it preserved without a fabricated UUID", store.entries[1].ID)
	}

	missing := domainhistory.Entry{
		ID: "not-a-uuid", Source: domainhistory.SourceAtuinSync,
		Scope: "remote:other.example:admin", Command: "df -h",
		Origin: "other.example:admin", RecordedAt: at(10),
	}
	if err := service.Record(ctx, missing); err != nil {
		t.Fatalf("Record derived: %v", err)
	}
	want := domainhistory.DerivedEntryUUID(missing)
	if store.entries[2].ID != want || !store.entries[2].HasUUIDIdentity() {
		t.Fatalf("derived identity = %q, want %q", store.entries[2].ID, want)
	}

	// Re-recording the same remote row is stable and does not duplicate it.
	missing.ID = ""
	if err := service.Record(ctx, missing); err != nil {
		t.Fatalf("Record derived again: %v", err)
	}
	if len(store.entries) != 3 {
		t.Fatalf("entries = %d, want the same three rows", len(store.entries))
	}
}

func TestDerivedUUIDIsStableWhenAnImportHasNoTimestamp(t *testing.T) {
	store := &memoryStore{}
	calls := 0
	service := NewService(store, WithClock(func() time.Time {
		calls++
		return at(calls)
	}))
	entry := domainhistory.Entry{
		Source: domainhistory.SourceAtuinSync, Scope: "remote:example.com:deploy",
		Command: "ls", Origin: "example.com:deploy",
	}
	want := domainhistory.DerivedEntryUUID(entry)
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.Record(context.Background(), entry); err != nil {
			t.Fatalf("Record attempt %d: %v", attempt, err)
		}
	}
	if len(store.entries) != 1 {
		t.Fatalf("entries = %d, want one row after a repeated import", len(store.entries))
	}
	if store.entries[0].ID != want {
		t.Fatalf("derived identity = %q, want the stable %q", store.entries[0].ID, want)
	}
	if !store.entries[0].RecordedAt.Equal(at(2)) {
		t.Fatalf("RecordedAt = %v, want the clock default", store.entries[0].RecordedAt)
	}
}

func TestConnectionLogsKeepTheirNonUUIDIdentity(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store, WithClock(func() time.Time { return at(12) }))
	if err := service.RecordConnection(context.Background(), domainhistory.ConnectionLog{
		Kind: "ssh", StartedAt: at(9),
	}); err != nil {
		t.Fatalf("RecordConnection: %v", err)
	}
	if domainhistory.IsUUID(store.logs[0].ID) {
		t.Fatalf("connection log identity = %q, want the existing format", store.logs[0].ID)
	}
}

func TestRecordAppendsEveryExecutionAndDefaultsScope(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{})
	for index := 0; index < 3; index++ {
		if err := service.Record(context.Background(), domainhistory.Entry{
			HostID: "host-1", Command: "git status",
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	entries, err := service.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 per-execution rows", len(entries))
	}
	for _, entry := range entries {
		if entry.Scope != "host:host-1" {
			t.Fatalf("scope = %q, want host:host-1", entry.Scope)
		}
		if entry.Source != domainhistory.SourceSession {
			t.Fatalf("source = %q, want session", entry.Source)
		}
		if entry.RecordedAt.IsZero() {
			t.Fatal("RecordedAt is zero")
		}
	}
}

func TestConnectionLogRetentionPrunesOnRecordButHistoryDoesNot(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{
		Logs: domainhistory.Retention{Count: 2},
	})
	for index := 0; index < 5; index++ {
		if err := service.Record(context.Background(), domainhistory.Entry{
			Command: fmt.Sprintf("command-%d", index), RecordedAt: at(index),
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
		if err := service.RecordConnection(context.Background(), domainhistory.ConnectionLog{
			Kind: "ssh", StartedAt: at(index),
		}); err != nil {
			t.Fatalf("RecordConnection: %v", err)
		}
	}
	// Connection Logs keep their configured bound.
	if len(store.logs) != 2 {
		t.Fatalf("logs = %d, want 2", len(store.logs))
	}
	// Command History is never pruned automatically.
	if len(store.entries) != 5 {
		t.Fatalf("entries = %d, want all 5", len(store.entries))
	}
}

func TestPruneAppliesOnlyConnectionLogRetention(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{Logs: domainhistory.Retention{Count: 2}})
	for index := 0; index < 5; index++ {
		_ = store.SaveConnectionLog(context.Background(), domainhistory.ConnectionLog{
			ID: fmt.Sprintf("log-%d", index), Kind: "ssh",
			Status: domainhistory.ConnectionConnected, StartedAt: at(index),
		})
		_ = store.RecordHistoryEntry(context.Background(), domainhistory.Entry{
			ID: fmt.Sprintf("entry-%d", index), Scope: "local", Command: "x",
			RecordedAt: at(index),
		}, nil)
	}
	result, err := service.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.ConnectionLogs != 3 {
		t.Fatalf("pruned %d logs, want 3", result.ConnectionLogs)
	}
	if len(store.logs) != 2 {
		t.Fatalf("logs = %d, want 2", len(store.logs))
	}
	if len(store.entries) != 5 {
		t.Fatalf("entries = %d, want all 5", len(store.entries))
	}
}

func TestClearConnectionLogsRemovesOnlyLogsAndNotifies(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{})
	var notifications int
	unsubscribe := service.Subscribe(func() { notifications++ })
	defer unsubscribe()

	for index := 0; index < 2; index++ {
		if err := service.RecordConnection(context.Background(), domainhistory.ConnectionLog{
			HostID: "host-1", HostLabel: "web-1", Kind: "ssh",
			Status: domainhistory.ConnectionConnected,
		}); err != nil {
			t.Fatalf("RecordConnection: %v", err)
		}
	}
	if err := service.Record(context.Background(), domainhistory.Entry{Command: "ls"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	removed, err := service.ClearConnectionLogs(context.Background())
	if err != nil || removed != 2 {
		t.Fatalf("ClearConnectionLogs = %d, %v; want 2", removed, err)
	}
	if len(store.logs) != 0 {
		t.Fatalf("logs remain: %d", len(store.logs))
	}
	if len(store.entries) != 1 {
		t.Fatalf("clearing logs removed %d commands, want 1 left", len(store.entries))
	}
	if notifications < 4 {
		t.Fatalf("notifications = %d, want at least 4", notifications)
	}
}

func TestDeleteAndClearNotifySubscribers(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{})
	var notifications int
	unsubscribe := service.Subscribe(func() { notifications++ })
	defer unsubscribe()
	_ = service.Record(context.Background(), domainhistory.Entry{Command: "one"})
	_ = service.Record(context.Background(), domainhistory.Entry{Command: "two"})
	entries, err := service.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := service.DeleteEntry(context.Background(), entries[0].ID); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	if removed, err := service.ClearHistory(context.Background()); err != nil || removed != 2 {
		t.Fatalf("ClearHistory = %d, %v; want 2", removed, err)
	}
	if notifications < 4 {
		t.Fatalf("notifications = %d, want at least 4", notifications)
	}
	if service.Revision() < 4 {
		t.Fatalf("revision = %d, want at least 4", service.Revision())
	}
}

func TestInvalidRecordsAreRejectedAndStoreErrorsPropagate(t *testing.T) {
	service := newTestService(&memoryStore{}, Retention{})
	if err := service.Record(context.Background(), domainhistory.Entry{Command: "  "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty command error = %v, want ErrInvalid", err)
	}
	if err := service.RecordConnection(context.Background(), domainhistory.ConnectionLog{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty log error = %v, want ErrInvalid", err)
	}

	failing := &memoryStore{fail: errors.New("disk full")}
	service = newTestService(failing, Retention{})
	if err := service.Record(context.Background(), domainhistory.Entry{Command: "x"}); err == nil {
		t.Fatal("Record succeeded despite a store failure")
	}
}

func newUUIDService(store *memoryStore) *Service {
	return NewService(store, WithClock(func() time.Time { return at(12) }))
}

func TestRecordWritesOneCreateRecordInTheWriterSeries(t *testing.T) {
	store := &memoryStore{}
	service := newUUIDService(store)
	ctx := context.Background()
	for index := 0; index < 2; index++ {
		if err := service.Record(ctx, domainhistory.Entry{
			SessionID: "session-1", Command: fmt.Sprintf("command-%d", index),
		}); err != nil {
			t.Fatalf("Record %d: %v", index, err)
		}
	}
	if len(store.records) != 2 {
		t.Fatalf("records = %d, want exactly one create per command", len(store.records))
	}
	for index, record := range store.records {
		if record.Kind != domainhistory.RecordKindCreate {
			t.Fatalf("record %d kind = %q, want create", index, record.Kind)
		}
		if record.SeriesHost != store.writer || !domainhistory.IsUUID(store.writer) {
			t.Fatalf("series host = %q, want the per-install writer UUID %q", record.SeriesHost, store.writer)
		}
		if record.Index != int64(index) {
			t.Fatalf("record %d index = %d, want %d", index, record.Index, index)
		}
		if record.HistoryID != store.entries[index].ID {
			t.Fatalf("record %d history id = %q, want %q", index, record.HistoryID, store.entries[index].ID)
		}
		if record.Direction != domainhistory.RecordDirectionLocal || !record.UploadedAt.IsZero() {
			t.Fatalf("record %d = %+v, want a pending local record", index, record)
		}
	}
}

func TestRecordDoesNotCreateWireRecordsForRemoteOrLegacyRows(t *testing.T) {
	store := &memoryStore{}
	service := newUUIDService(store)
	ctx := context.Background()

	imported := domainhistory.Entry{
		ID:     "9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
		Source: domainhistory.SourceAtuinImport, Scope: "remote:web-1:deploy",
		Origin: "web-1:deploy", Command: "uptime", RecordedAt: at(9),
	}
	if err := service.Record(ctx, imported); err != nil {
		t.Fatalf("Record imported: %v", err)
	}
	synced := imported
	synced.ID = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e30"
	synced.Source = domainhistory.SourceAtuinSync
	if err := service.Record(ctx, synced); err != nil {
		t.Fatalf("Record synced: %v", err)
	}
	legacy := domainhistory.Entry{
		ID: "legacy-local-1", Source: domainhistory.SourceSession,
		Scope: "local", Command: "ls", RecordedAt: at(10),
	}
	if err := service.Record(ctx, legacy); err != nil {
		t.Fatalf("Record legacy: %v", err)
	}
	if len(store.entries) != 3 {
		t.Fatalf("entries = %d, want the three projections", len(store.entries))
	}
	if len(store.records) != 0 {
		t.Fatalf("records = %+v, want none for remote-origin or legacy rows", store.records)
	}
}

func TestDeleteEntrySoftDeletesAndAppendsOneTombstonePerUUIDRow(t *testing.T) {
	store := &memoryStore{}
	service := newUUIDService(store)
	ctx := context.Background()
	if err := service.Record(ctx, domainhistory.Entry{Command: "ls", Scope: "local"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	id := store.entries[0].ID

	if err := service.DeleteEntry(ctx, id); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	stored, found, err := store.HistoryEntry(ctx, id)
	if err != nil || !found || stored.DeletedAt.IsZero() {
		t.Fatalf("row = %+v, found=%v, err=%v; want a soft-deleted row", stored, found, err)
	}
	live, err := service.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("deleted row is still suggested: %+v", live)
	}
	if len(store.records) != 2 {
		t.Fatalf("records = %d, want create and delete", len(store.records))
	}
	tombstone := store.records[1]
	if tombstone.Kind != domainhistory.RecordKindDelete || tombstone.HistoryID != id ||
		tombstone.Index != 1 || tombstone.SeriesHost != store.writer {
		t.Fatalf("tombstone = %+v", tombstone)
	}
	if !tombstone.RecordedAt.Equal(stored.DeletedAt) {
		t.Fatalf("tombstone time = %v, want the deletion time %v", tombstone.RecordedAt, stored.DeletedAt)
	}

	// A repeated delete is a no-op and must not fork the tombstone.
	if err := service.DeleteEntry(ctx, id); err != nil {
		t.Fatalf("repeated DeleteEntry: %v", err)
	}
	if len(store.records) != 2 {
		t.Fatalf("records = %d after a repeated delete, want 2", len(store.records))
	}
}

func TestDeleteEntryOfALegacyRowStaysLocal(t *testing.T) {
	store := &memoryStore{}
	service := newUUIDService(store)
	ctx := context.Background()
	if err := service.Record(ctx, domainhistory.Entry{
		ID: "legacy-local-1", Source: domainhistory.SourceSession,
		Scope: "local", Command: "ls", RecordedAt: at(9),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := service.DeleteEntry(ctx, "legacy-local-1"); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	stored, found, err := store.HistoryEntry(ctx, "legacy-local-1")
	if err != nil || !found || stored.DeletedAt.IsZero() {
		t.Fatalf("legacy row = %+v, found=%v, err=%v; want it soft-deleted locally", stored, found, err)
	}
	if len(store.records) != 0 {
		t.Fatalf("legacy row produced wire records: %+v", store.records)
	}
}

func TestDeleteEntryOfAMissingRowAppendsNothing(t *testing.T) {
	store := &memoryStore{}
	service := newUUIDService(store)
	if err := service.DeleteEntry(context.Background(), "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e40"); err != nil {
		t.Fatalf("DeleteEntry missing: %v", err)
	}
	if len(store.records) != 0 {
		t.Fatalf("a missing row produced records: %+v", store.records)
	}
}

func TestDeleteEntryOfAnImportedRowAppendsATombstone(t *testing.T) {
	store := &memoryStore{}
	service := newUUIDService(store)
	ctx := context.Background()
	imported := domainhistory.Entry{
		ID:     "9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
		Source: domainhistory.SourceAtuinImport, Scope: "remote:web-1:deploy",
		Origin: "web-1:deploy", Command: "uptime", RecordedAt: at(9),
	}
	if err := service.Record(ctx, imported); err != nil {
		t.Fatalf("Record imported: %v", err)
	}
	if err := service.DeleteEntry(ctx, imported.ID); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("records = %d, want exactly the delete tombstone", len(store.records))
	}
	tombstone := store.records[0]
	if tombstone.Kind != domainhistory.RecordKindDelete || tombstone.HistoryID != imported.ID {
		t.Fatalf("tombstone = %+v, want a delete for the imported history id", tombstone)
	}
}

func TestFailedRecordLeavesProjectionAndRecordLogUnchanged(t *testing.T) {
	store := &memoryStore{failWrite: errors.New("disk full")}
	service := newUUIDService(store)
	if err := service.Record(context.Background(), domainhistory.Entry{Command: "ls"}); err == nil {
		t.Fatal("Record succeeded despite a failed store write")
	}
	if len(store.entries) != 0 || len(store.records) != 0 {
		t.Fatalf("failed record changed state: entries=%+v records=%+v", store.entries, store.records)
	}
}

func TestStatisticsCoverUnfilteredFilteredAndEmptySets(t *testing.T) {
	store := &memoryStore{entries: []domainhistory.Entry{
		{ID: "a", HostID: "host-1", Scope: "host:host-1", Command: "git status",
			CWD: "/srv/app", RecordedAt: at(9), FinishedAt: at(9).Add(time.Second),
			Duration: time.Second, ExitCode: 0, HasExitCode: true,
			Source: domainhistory.SourceSession},
		{ID: "b", HostID: "host-1", Scope: "host:host-1", Command: "make test",
			CWD: "/srv/app", RecordedAt: at(9).Add(time.Minute),
			FinishedAt: at(9).Add(time.Minute + 3*time.Second), Duration: 3 * time.Second,
			ExitCode: 2, HasExitCode: true, Source: domainhistory.SourceSession},
		{ID: "c", HostID: "host-2", Scope: "host:host-2", Command: "ls",
			RecordedAt: at(10), Source: domainhistory.SourceSession},
		// A soft-deleted row is not live and must not be summarized.
		{ID: "deleted", Scope: "local", Command: "secret",
			RecordedAt: at(10), DeletedAt: at(11), Source: domainhistory.SourceSession},
	}}
	service := newTestService(store, Retention{})
	ctx := context.Background()

	all, err := service.Statistics(ctx, domainhistory.EntryFilter{})
	if err != nil {
		t.Fatalf("Statistics: %v", err)
	}
	if all.Total != 3 || all.Unique != 3 {
		t.Fatalf("unfiltered total = %d, unique = %d; want 3 and 3",
			all.Total, all.Unique)
	}
	if all.Failures != 1 || all.KnownExits != 2 || all.FailureRate != 0.5 {
		t.Fatalf("unfiltered failures = %d/%d (%v), want 1/2 (0.5)",
			all.Failures, all.KnownExits, all.FailureRate)
	}
	if all.AverageDuration != 2*time.Second ||
		all.DurationsKnown != 2 || all.DurationsUnknown != 1 {
		t.Fatalf("unfiltered durations = %v (%d known, %d unknown)",
			all.AverageDuration, all.DurationsKnown, all.DurationsUnknown)
	}

	filtered, err := service.Statistics(ctx, domainhistory.EntryFilter{Host: "host-1"})
	if err != nil {
		t.Fatalf("filtered Statistics: %v", err)
	}
	if filtered.Total != 2 || filtered.Unique != 2 || filtered.Failures != 1 ||
		filtered.AverageDuration != 2*time.Second {
		t.Fatalf("filtered statistics = %+v", filtered)
	}

	empty, err := service.Statistics(ctx, domainhistory.EntryFilter{Query: "no-match"})
	if err != nil {
		t.Fatalf("empty Statistics: %v", err)
	}
	if empty.Total != 0 || empty.Unique != 0 || empty.Failures != 0 ||
		empty.KnownExits != 0 || empty.BusiestHour != nil ||
		len(empty.TopCommands) != 0 || len(empty.Hosts) != 0 {
		t.Fatalf("empty statistics = %+v", empty)
	}

	store.fail = errors.New("disk")
	if _, err := service.Statistics(ctx, domainhistory.EntryFilter{}); err == nil {
		t.Fatal("Statistics succeeded despite a store failure")
	}
}

func TestFailedDeleteLeavesProjectionAndRecordLogUnchanged(t *testing.T) {
	store := &memoryStore{}
	service := newUUIDService(store)
	ctx := context.Background()
	if err := service.Record(ctx, domainhistory.Entry{Command: "ls", Scope: "local"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	id := store.entries[0].ID

	store.failWrite = errors.New("disk full")
	if err := service.DeleteEntry(ctx, id); err == nil {
		t.Fatal("DeleteEntry succeeded despite a failed store write")
	}
	stored, found, err := store.HistoryEntry(ctx, id)
	if err != nil || !found {
		t.Fatalf("HistoryEntry = %v, %v", found, err)
	}
	if !stored.DeletedAt.IsZero() {
		t.Fatalf("failed delete changed the projection: %+v", stored)
	}
	if len(store.records) != 1 || store.records[0].Kind != domainhistory.RecordKindCreate {
		t.Fatalf("failed delete changed the record log: %+v", store.records)
	}
}

func TestImportShellHistoryWritesRowsAndCreateRecordsOnce(t *testing.T) {
	store := &memoryStore{}
	service := newUUIDService(store)
	ctx := context.Background()
	loads := 0
	source := func() []domainhistory.ShellHistoryCommand {
		loads++
		return []domainhistory.ShellHistoryCommand{
			{Command: " git status ", Shell: "zsh", RecordedAt: at(9)},
			{Command: "deploy staging", Shell: "bash"},
		}
	}
	if err := service.ImportShellHistory(ctx, source, "workstation:alice"); err != nil {
		t.Fatalf("ImportShellHistory: %v", err)
	}
	if loads != 1 {
		t.Fatalf("source loads = %d, want 1", loads)
	}
	if len(store.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(store.entries))
	}
	for _, entry := range store.entries {
		if !domainhistory.IsUUID(entry.ID) || entry.ID[14] != '7' {
			t.Fatalf("identity = %q, want a UUIDv7", entry.ID)
		}
		if entry.Source != domainhistory.SourceShellHistory || entry.Scope != "local" {
			t.Fatalf("provenance = %q/%q, want shell-history local", entry.Source, entry.Scope)
		}
		if entry.Origin != "workstation:alice" || entry.Author != "alice" ||
			entry.AuthorKind != domainhistory.AuthorKindUser {
			t.Fatalf("origin/author = %q/%q/%q", entry.Origin, entry.Author, entry.AuthorKind)
		}
		if entry.CWD != "" || entry.HasExitCode || entry.ExitCode != 0 ||
			!entry.FinishedAt.IsZero() || entry.Duration != 0 || entry.CaptureTier != 0 {
			t.Fatalf("unknown execution fields were invented: %+v", entry)
		}
	}
	timestamped := store.entries[0]
	if timestamped.Command != "git status" || timestamped.Shell != "zsh" ||
		!timestamped.RecordedAt.Equal(at(9)) {
		t.Fatalf("timestamped row = %+v", timestamped)
	}
	untimed := store.entries[1]
	if untimed.Command != "deploy staging" || untimed.Shell != "bash" ||
		!untimed.RecordedAt.Equal(at(12)) {
		t.Fatalf("untimed row = %+v", untimed)
	}
	if len(store.records) != 2 {
		t.Fatalf("records = %d, want one create per row", len(store.records))
	}
	for index, record := range store.records {
		if record.Kind != domainhistory.RecordKindCreate ||
			record.HistoryID != store.entries[index].ID ||
			record.SeriesHost != store.writer ||
			record.Direction != domainhistory.RecordDirectionLocal ||
			!record.UploadedAt.IsZero() {
			t.Fatalf("record %d = %+v, want a pending local create", index, record)
		}
		var payload domainhistory.Entry
		if err := json.Unmarshal([]byte(record.Payload), &payload); err != nil {
			t.Fatalf("record %d payload: %v", index, err)
		}
		if payload.ID != store.entries[index].ID || payload.Command != store.entries[index].Command {
			t.Fatalf("record %d payload = %+v", index, payload)
		}
	}
	if !store.metadata.ShellHistoryImported {
		t.Fatal("the durable import marker was not set")
	}

	// The marker makes the import one-time: a later startup does not even
	// read the configured files.
	if err := service.ImportShellHistory(ctx, func() []domainhistory.ShellHistoryCommand {
		loads++
		return []domainhistory.ShellHistoryCommand{{Command: "fresh", Shell: "fish"}}
	}, "workstation:alice"); err != nil {
		t.Fatalf("second ImportShellHistory: %v", err)
	}
	if loads != 1 {
		t.Fatalf("source loads = %d after the marker was set, want 1", loads)
	}
	if len(store.entries) != 2 || len(store.records) != 2 {
		t.Fatalf("a later import changed state: entries=%d records=%d",
			len(store.entries), len(store.records))
	}
}

func TestImportShellHistorySetsMarkerForAnEmptyFileSet(t *testing.T) {
	store := &memoryStore{}
	service := newUUIDService(store)
	ctx := context.Background()
	loads := 0
	source := func() []domainhistory.ShellHistoryCommand {
		loads++
		return nil
	}
	if err := service.ImportShellHistory(ctx, source, "workstation:alice"); err != nil {
		t.Fatalf("ImportShellHistory: %v", err)
	}
	if loads != 1 {
		t.Fatalf("source loads = %d, want 1", loads)
	}
	if !store.metadata.ShellHistoryImported {
		t.Fatal("an empty file set did not set the import marker")
	}
	if len(store.entries) != 0 || len(store.records) != 0 {
		t.Fatalf("an empty file set wrote entries=%d records=%d",
			len(store.entries), len(store.records))
	}

	if err := service.ImportShellHistory(ctx, func() []domainhistory.ShellHistoryCommand {
		loads++
		return []domainhistory.ShellHistoryCommand{{Command: "ls", Shell: "zsh"}}
	}, "workstation:alice"); err != nil {
		t.Fatalf("second ImportShellHistory: %v", err)
	}
	if loads != 1 || len(store.entries) != 0 {
		t.Fatalf("the empty-set marker did not prevent a later import: loads=%d entries=%d",
			loads, len(store.entries))
	}
}

func TestImportShellHistorySkipsDuplicatesIncludingSoftDeletedRows(t *testing.T) {
	store := &memoryStore{entries: []domainhistory.Entry{
		{
			ID: "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e10", Scope: "local",
			Command: "git status", Source: domainhistory.SourceSession,
			RecordedAt: at(9).Add(500 * time.Millisecond),
		},
		{
			ID: "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e11", Scope: "local",
			Command: "rm secret", Source: domainhistory.SourceSession,
			RecordedAt: at(8), DeletedAt: at(9),
		},
	}}
	service := newUUIDService(store)
	ctx := context.Background()
	source := func() []domainhistory.ShellHistoryCommand {
		return []domainhistory.ShellHistoryCommand{
			{Command: "git status", Shell: "zsh", RecordedAt: at(9)},
			{Command: "rm secret", Shell: "bash", RecordedAt: at(8)},
			{Command: "rm secret", Shell: "fish"},
			{Command: "fresh", Shell: "fish"},
		}
	}
	if err := service.ImportShellHistory(ctx, source, "workstation:alice"); err != nil {
		t.Fatalf("ImportShellHistory: %v", err)
	}
	if len(store.entries) != 3 {
		t.Fatalf("entries = %d, want the two existing rows and one import", len(store.entries))
	}
	fresh := 0
	for _, entry := range store.entries {
		if entry.Command == "rm secret" && entry.DeletedAt.IsZero() {
			t.Fatalf("a soft-deleted command was resurrected: %+v", entry)
		}
		if entry.Command == "fresh" {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh rows = %d, want 1", fresh)
	}
	if len(store.records) != 1 || store.records[0].Kind != domainhistory.RecordKindCreate {
		t.Fatalf("records = %+v, want one create for the fresh row", store.records)
	}
	if !store.metadata.ShellHistoryImported {
		t.Fatal("the import marker was not set after skipping duplicates")
	}
}

func TestFailedShellHistoryImportLeavesMarkerUnset(t *testing.T) {
	store := &memoryStore{failWrite: errors.New("disk full")}
	service := newUUIDService(store)
	err := service.ImportShellHistory(context.Background(), func() []domainhistory.ShellHistoryCommand {
		return []domainhistory.ShellHistoryCommand{{Command: "ls", Shell: "zsh"}}
	}, "workstation:alice")
	if err == nil {
		t.Fatal("ImportShellHistory succeeded despite a failed store write")
	}
	if store.metadata.ShellHistoryImported {
		t.Fatal("a failed import set the durable marker")
	}
	if len(store.entries) != 0 || len(store.records) != 0 {
		t.Fatalf("a failed import changed state: entries=%d records=%d",
			len(store.entries), len(store.records))
	}
}

func stringPtr(value string) *string { return &value }

func int64Ptr(value int64) *int64 { return &value }

func TestImportHostAtuinMapsRowsAndPreservesAtuinIDs(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{})
	const atuinID = "9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	recorded := time.Date(2026, 3, 1, 9, 0, 0, 123, time.UTC)
	rows := []domainhistory.AtuinRow{{
		ID: atuinID, Timestamp: recorded.UnixNano(), Command: "git status",
		Duration: int64Ptr(int64(150 * time.Millisecond)), Exit: int64Ptr(0),
		CWD: stringPtr("/srv/app"), Hostname: stringPtr("web-1.example.test"),
		Author: stringPtr("deploy"), AuthorKind: stringPtr(domainhistory.AuthorKindUser),
		Shell: stringPtr("zsh"),
	}}

	result, err := service.ImportHostAtuin(context.Background(), "host-1", rows, "web-1.example.test:deploy")
	if err != nil {
		t.Fatalf("ImportHostAtuin: %v", err)
	}
	if result.Imported != 1 || result.Skipped != 0 {
		t.Fatalf("result = %+v", result)
	}
	if want := (domainhistory.ImportCheckpoint{Timestamp: recorded, HistoryID: atuinID}); result.Checkpoint != want {
		t.Fatalf("checkpoint = %+v, want %+v", result.Checkpoint, want)
	}
	if len(store.entries) != 1 {
		t.Fatalf("entries = %+v", store.entries)
	}
	entry := store.entries[0]
	if entry.ID != atuinID {
		t.Fatalf("ID = %q, want the preserved Atuin UUID", entry.ID)
	}
	if entry.Source != domainhistory.SourceAtuinImport ||
		entry.Scope != "remote:web-1.example.test" {
		t.Fatalf("source/scope = %q/%q", entry.Source, entry.Scope)
	}
	if !entry.RecordedAt.Equal(recorded) || entry.Duration != 150*time.Millisecond ||
		!entry.HasExitCode || entry.ExitCode != 0 {
		t.Fatalf("mapped row = %+v", entry)
	}
	if entry.CWD != "/srv/app" || entry.Author != "deploy" ||
		entry.AuthorKind != domainhistory.AuthorKindUser || entry.Shell != "zsh" {
		t.Fatalf("mapped metadata = %+v", entry)
	}
	if len(store.records) != 0 {
		t.Fatalf("an import appended create records: %+v", store.records)
	}
	if got := store.metadata.HostImports["host-1"]; got != result.Checkpoint {
		t.Fatalf("stored checkpoint = %+v, want %+v", got, result.Checkpoint)
	}
}

func TestImportHostAtuinSkipsCheckpointedRowsAndNeverDuplicates(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{})
	ctx := context.Background()
	first := domainhistory.AtuinRow{ID: "first", Timestamp: at(9).UnixNano(), Command: "ls"}
	second := domainhistory.AtuinRow{ID: "second", Timestamp: at(10).UnixNano(), Command: "pwd"}

	result, err := service.ImportHostAtuin(ctx, "host-1", []domainhistory.AtuinRow{second, first}, "")
	if err != nil || result.Imported != 2 {
		t.Fatalf("first import = %+v, %v", result, err)
	}
	if len(store.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(store.entries))
	}

	repeated, err := service.ImportHostAtuin(ctx, "host-1", []domainhistory.AtuinRow{first, second}, "")
	if err != nil {
		t.Fatalf("repeated import: %v", err)
	}
	if repeated.Imported != 0 || repeated.Skipped != 2 {
		t.Fatalf("repeated import = %+v", repeated)
	}
	if len(store.entries) != 2 {
		t.Fatalf("a repeated import appended rows: %d", len(store.entries))
	}

	newer := domainhistory.AtuinRow{ID: "newer", Timestamp: at(11).UnixNano(), Command: "uptime"}
	extended, err := service.ImportHostAtuin(ctx, "host-1",
		[]domainhistory.AtuinRow{first, second, newer}, "")
	if err != nil {
		t.Fatalf("extended import: %v", err)
	}
	if extended.Imported != 1 || extended.Skipped != 2 {
		t.Fatalf("extended import = %+v", extended)
	}
	if len(store.entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(store.entries))
	}
	if !extended.Checkpoint.Timestamp.Equal(at(11)) {
		t.Fatalf("checkpoint = %+v, want the newest row's timestamp", extended.Checkpoint)
	}
	newest := false
	for _, entry := range store.entries {
		if entry.Command == "uptime" && entry.ID == extended.Checkpoint.HistoryID {
			newest = true
		}
	}
	if !newest {
		t.Fatalf("checkpoint identity = %q, want the newest row's id", extended.Checkpoint.HistoryID)
	}

	// A row older than the checkpoint is skipped even on its first import.
	older := domainhistory.AtuinRow{ID: "older", Timestamp: at(8).UnixNano(), Command: "old"}
	ignored, err := service.ImportHostAtuin(ctx, "host-1", []domainhistory.AtuinRow{older}, "")
	if err != nil || ignored.Imported != 0 || ignored.Skipped != 1 {
		t.Fatalf("older import = %+v, %v", ignored, err)
	}
}

func TestImportHostAtuinKeepsTheCheckpointWhenTheStoreFails(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{})
	ctx := context.Background()
	rows := []domainhistory.AtuinRow{
		{ID: "first", Timestamp: at(9).UnixNano(), Command: "ls"},
	}

	store.failWrite = errors.New("disk full")
	result, err := service.ImportHostAtuin(ctx, "host-1", rows, "")
	if err == nil {
		t.Fatal("ImportHostAtuin succeeded despite a failed store write")
	}
	if result.Imported != 0 {
		t.Fatalf("failed import reported rows: %+v", result)
	}
	if _, found := store.metadata.HostImports["host-1"]; found {
		t.Fatal("a failed import advanced the checkpoint")
	}

	store.failWrite = nil
	retried, err := service.ImportHostAtuin(ctx, "host-1", rows, "")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retried.Imported != 1 {
		t.Fatalf("retry = %+v, want the row imported from the unchanged checkpoint", retried)
	}
}

func TestImportHostAtuinFailsClosedOnAMalformedRowBeforeWriting(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{})
	rows := []domainhistory.AtuinRow{
		{ID: "good", Timestamp: at(9).UnixNano(), Command: "ls"},
		{ID: "bad", Timestamp: at(10).UnixNano(), Command: "   "},
	}

	if _, err := service.ImportHostAtuin(context.Background(), "host-1", rows, ""); !errors.Is(err, domainhistory.ErrInvalidAtuinRow) {
		t.Fatalf("error = %v, want ErrInvalidAtuinRow", err)
	}
	if len(store.entries) != 0 {
		t.Fatalf("a malformed snapshot was partly imported: %+v", store.entries)
	}
	if _, found := store.metadata.HostImports["host-1"]; found {
		t.Fatal("a malformed snapshot advanced the checkpoint")
	}
}

func TestImportHostAtuinRejectsAnEmptyHostIdentity(t *testing.T) {
	service := newTestService(&memoryStore{}, Retention{})
	if _, err := service.ImportHostAtuin(context.Background(), "", nil, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestMarkHostAtuinNoticedIsOneTimePerHost(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{})
	ctx := context.Background()

	first, err := service.MarkHostAtuinNoticed(ctx, "host-1")
	if err != nil || !first {
		t.Fatalf("first = %t, %v; want true", first, err)
	}
	second, err := service.MarkHostAtuinNoticed(ctx, "host-1")
	if err != nil || second {
		t.Fatalf("second = %t, %v; want false", second, err)
	}
	other, err := service.MarkHostAtuinNoticed(ctx, "host-2")
	if err != nil || !other {
		t.Fatalf("other Host = %t, %v; want true", other, err)
	}
}
