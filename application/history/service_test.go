package history

import (
	"context"
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
