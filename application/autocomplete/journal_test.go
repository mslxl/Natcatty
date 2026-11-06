package autocomplete

import (
	"context"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

// journalMemory is the shared history port backed by memory. It records every
// execution and can delete one row, like the history service the app uses.
type journalMemory struct {
	entries []domainhistory.Entry
	loads   int
	err     error
}

func (j *journalMemory) Load(context.Context) ([]domainhistory.Entry, error) {
	j.loads++
	if j.err != nil {
		return nil, j.err
	}
	return append([]domainhistory.Entry(nil), j.entries...), nil
}

func (j *journalMemory) Record(_ context.Context, entry domainhistory.Entry) error {
	entry.ID = "journal-" + entry.Command + entry.Scope
	j.entries = append(j.entries, entry)
	return nil
}

func (j *journalMemory) delete(id string) {
	kept := j.entries[:0]
	for _, entry := range j.entries {
		if entry.ID != id {
			kept = append(kept, entry)
		}
	}
	j.entries = kept
}

func TestJournalReceivesEveryExecution(t *testing.T) {
	journal := &journalMemory{}
	service := NewService(journal, func() Config { return defaultConfig() })
	service.Load(context.Background())

	service.Record("host:a", "git status")
	service.Record("host:a", "git status")
	service.Record("host:a", "git stash")

	if len(journal.entries) != 3 {
		t.Fatalf("journal recorded %d rows, want 3 per-execution rows", len(journal.entries))
	}
	first := journal.entries[0]
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
	service := NewService(journal, func() Config { return defaultConfig() })
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
	service := NewService(journal, func() Config { return defaultConfig() })
	service.Load(context.Background())
	service.RecordEntry(domainhistory.Entry{
		SessionID: "session-1", HostID: "host-1", HostLabel: "web-1",
		Command: "deploy", Source: domainhistory.SourceShellHistory,
	})
	if len(journal.entries) != 1 {
		t.Fatalf("journal rows = %d, want 1", len(journal.entries))
	}
	got := journal.entries[0]
	if got.SessionID != "session-1" || got.HostID != "host-1" ||
		got.HostLabel != "web-1" || got.Scope != "host:host-1" ||
		got.Source != domainhistory.SourceShellHistory {
		t.Fatalf("journal row = %+v", got)
	}
}

// TestDeletingAJournalRowRemovesItsSuggestion is the single-journal deletion
// contract: the History surface's mutation hook reloads autocomplete after a
// delete, and the deleted command can no longer be suggested.
func TestDeletingAJournalRowRemovesItsSuggestion(t *testing.T) {
	journal := &journalMemory{}
	service := NewService(journal, func() Config { return defaultConfig() })
	service.Record("local", "git status")
	service.Record("local", "git stash")
	service.Load(context.Background())

	if got := service.Suggestions("git status", "local"); len(got) != 1 {
		t.Fatalf("suggestions before delete = %+v", got)
	}
	journal.delete(journal.entries[0].ID)
	service.Load(context.Background())

	if got := service.Suggestions("git status", "local"); got != nil {
		t.Fatalf("deleted command is still suggested: %+v", got)
	}
	if got := service.Suggestions("git stash", "local"); len(got) != 1 {
		t.Fatalf("surviving command is missing: %+v", got)
	}
}
