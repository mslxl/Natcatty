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
	logs    []domainhistory.ConnectionLog
	entries []domainhistory.Entry
	fail    error
	saves   int
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
	out := append([]domainhistory.Entry(nil), s.entries...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].RecordedAt.After(out[j].RecordedAt) })
	return out, nil
}

func (s *memoryStore) SaveHistoryEntry(_ context.Context, entry domainhistory.Entry) error {
	if s.fail != nil {
		return s.fail
	}
	s.saves++
	s.entries = replaceEntry(s.entries, entry)
	return nil
}

func (s *memoryStore) DeleteHistoryEntry(_ context.Context, id string) error {
	for index, entry := range s.entries {
		if entry.ID == id {
			s.entries = append(s.entries[:index], s.entries[index+1:]...)
			return nil
		}
	}
	return nil
}

func (s *memoryStore) ClearHistoryEntries(context.Context) (int, error) {
	removed := len(s.entries)
	s.entries = nil
	return removed, nil
}

func (s *memoryStore) PruneHistoryEntries(_ context.Context, retention domainhistory.Retention, now time.Time) (int, error) {
	kept := domainhistory.PruneEntries(s.entries, retention, now)
	removed := len(s.entries) - len(kept)
	s.entries = kept
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

func TestRetentionPrunesAtTheConfiguredLimitOnRecord(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{
		Logs:    domainhistory.Retention{Count: 2},
		History: domainhistory.Retention{Count: 2},
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
	if len(store.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(store.entries))
	}
	if len(store.logs) != 2 {
		t.Fatalf("logs = %d, want 2", len(store.logs))
	}
	for _, entry := range store.entries {
		if entry.Command != "command-4" && entry.Command != "command-3" {
			t.Fatalf("kept %q, want the newest two", entry.Command)
		}
	}
}

func TestZeroRetentionDoesNotPrune(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{})
	for index := 0; index < 5; index++ {
		if err := service.Record(context.Background(), domainhistory.Entry{
			Command: fmt.Sprintf("command-%d", index),
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	if len(store.entries) != 5 {
		t.Fatalf("entries = %d, want all 5", len(store.entries))
	}
}

func TestPruneAppliesLoweredRetentionWithoutRecording(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(store, Retention{History: domainhistory.Retention{Count: 2}})
	for index := 0; index < 5; index++ {
		_ = store.SaveHistoryEntry(context.Background(), domainhistory.Entry{
			ID: fmt.Sprintf("old-%d", index), Scope: "local", Command: "x",
			RecordedAt: at(index),
		})
	}
	result, err := service.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Entries != 3 {
		t.Fatalf("pruned %d entries, want 3", result.Entries)
	}
	if len(store.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(store.entries))
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
	entries, _ := service.Load(context.Background())
	if err := service.DeleteEntry(context.Background(), entries[0].ID); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	if removed, err := service.ClearHistory(context.Background()); err != nil || removed != 1 {
		t.Fatalf("ClearHistory = %d, %v; want 1", removed, err)
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
