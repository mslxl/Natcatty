package autocomplete

import (
	"context"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

// journalMemory is the shared history port backed by memory. It records every
// execution, unlike Store which receives the aggregated set.
type journalMemory struct {
	entries  []domainhistory.Entry
	loads    int
	recorded []domainhistory.Entry
}

func (j *journalMemory) Load(context.Context) ([]domainhistory.Entry, error) {
	j.loads++
	return append([]domainhistory.Entry(nil), j.entries...), nil
}

func (j *journalMemory) Record(_ context.Context, entry domainhistory.Entry) error {
	entry.ID = "journal-" + entry.Command + entry.Scope
	j.entries = append(j.entries, entry)
	j.recorded = append(j.recorded, entry)
	return nil
}

func TestJournalReceivesEveryExecution(t *testing.T) {
	journal := &journalMemory{}
	service := NewService(nil, func() Config { return defaultConfig() },
		WithJournal(journal))
	service.Load(context.Background())

	service.Record("host:a", "git status")
	service.Record("host:a", "git status")
	service.Record("host:a", "git stash")

	if len(journal.recorded) != 3 {
		t.Fatalf("journal recorded %d rows, want 3 per-execution rows", len(journal.recorded))
	}
	first := journal.recorded[0]
	if first.Command != "git status" || first.Scope != "host:a" ||
		first.Source != domainhistory.SourceSession || first.RecordedAt.IsZero() {
		t.Fatalf("journal row = %+v", first)
	}

	// Suggestions still aggregate the executions.
	got := service.Suggestions("git s", "host:a")
	if len(got) != 2 || got[0].Text != "git status" || got[0].Frequency != 2 {
		t.Fatalf("suggestions = %+v", got)
	}
}

func TestLoadAggregatesTheSharedJournal(t *testing.T) {
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	journal := &journalMemory{entries: []domainhistory.Entry{
		{Command: "git status", Scope: "host:a", RecordedAt: base},
		{Command: "git status", Scope: "host:a", RecordedAt: base.Add(time.Hour)},
		{Command: "git stash", Scope: "host:a", RecordedAt: base.Add(2 * time.Hour)},
		{Command: "make test", Scope: "local", RecordedAt: base.Add(3 * time.Hour)},
	}}
	service := NewService(nil, func() Config { return defaultConfig() },
		WithJournal(journal))
	service.Load(context.Background())
	if journal.loads != 1 {
		t.Fatalf("journal loads = %d, want 1", journal.loads)
	}

	got := service.Suggestions("git s", "host:a")
	if len(got) != 2 {
		t.Fatalf("suggestions = %+v", got)
	}
	if got[0].Text != "git status" || got[0].Frequency != 2 {
		t.Fatalf("first = %+v, want the twice-used command", got[0])
	}
	if scope := service.Suggestions("make", "host:a"); len(scope) != 0 {
		t.Fatalf("host scope saw a local command: %+v", scope)
	}
}

func TestRecordEntryCarriesTheSessionHostAndSource(t *testing.T) {
	journal := &journalMemory{}
	service := NewService(nil, func() Config { return defaultConfig() },
		WithJournal(journal))
	service.Load(context.Background())
	service.RecordEntry(domainhistory.Entry{
		SessionID: "session-1", HostID: "host-1", HostLabel: "web-1",
		Command: "deploy", Source: domainhistory.SourceShellHistory,
	})
	if len(journal.recorded) != 1 {
		t.Fatalf("journal rows = %d, want 1", len(journal.recorded))
	}
	got := journal.recorded[0]
	if got.SessionID != "session-1" || got.HostID != "host-1" ||
		got.HostLabel != "web-1" || got.Scope != "host:host-1" ||
		got.Source != domainhistory.SourceShellHistory {
		t.Fatalf("journal row = %+v", got)
	}
}
