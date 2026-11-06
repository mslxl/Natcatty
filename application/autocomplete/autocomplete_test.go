package autocomplete

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

func journalEntryFor(command, scope string, at time.Time) domainhistory.Entry {
	return domainhistory.Entry{Command: command, Scope: scope, RecordedAt: at}
}

func defaultConfig() Config {
	return Config{
		Enabled:        true,
		MinChars:       1,
		MaxSuggestions: 50,
		DebounceMs:     100,
		Scope:          domainterminal.AutocompleteHistoryHost,
	}
}

func newTestService(t *testing.T, journal Journal, config Config, opts ...Option) *Service {
	t.Helper()
	service := NewService(journal, func() Config { return config }, opts...)
	service.Load(context.Background())
	return service
}

func TestSuggestionsComeFromTheJournal(t *testing.T) {
	journal := &journalMemory{}
	service := newTestService(t, journal, defaultConfig())
	service.Record("local", "git status")
	service.Record("local", "git status")
	service.Record("local", "git stash")
	service.Record("local", "make test")

	got := service.Suggestions("git s", "local")
	if len(got) != 2 {
		t.Fatalf("suggestions = %+v, want git status and git stash", got)
	}
	if got[0].Text != "git status" || got[0].Frequency != 2 {
		t.Fatalf("first = %+v, want the twice-used command", got[0])
	}
	if got[1].Text != "git stash" {
		t.Fatalf("second = %+v", got[1])
	}
	for _, suggestion := range got {
		if suggestion.Source != SourceHistory {
			t.Fatalf("source = %q, want %q", suggestion.Source, SourceHistory)
		}
	}
}

func TestSuggestionsRespectTheHistoryScope(t *testing.T) {
	journal := &journalMemory{}
	config := defaultConfig()
	service := NewService(journal, func() Config { return config })
	service.Load(context.Background())
	service.Record("host:a", "deploy staging")
	service.Record("host:b", "deploy production")

	if got := service.Suggestions("deploy", "host:a"); len(got) != 1 ||
		got[0].Text != "deploy staging" {
		t.Fatalf("host scope = %+v, want only host:a", got)
	}
	config.Scope = domainterminal.AutocompleteHistoryGlobal
	if got := service.Suggestions("deploy", "host:a"); len(got) != 2 {
		t.Fatalf("global scope = %+v, want both hosts", got)
	}
}

func TestSuggestionsHonorEnabledMinimumCharsAndLimit(t *testing.T) {
	journal := &journalMemory{}
	config := defaultConfig()
	config.MinChars = 3
	config.MaxSuggestions = 2
	service := NewService(journal, func() Config { return config })
	service.Load(context.Background())
	for _, command := range []string{"alpha one", "alpha two", "alpha three"} {
		service.Record("local", command)
	}

	if got := service.Suggestions("al", "local"); got != nil {
		t.Fatalf("short input returned %+v", got)
	}
	got := service.Suggestions("alpha", "local")
	if len(got) != 2 {
		t.Fatalf("limit = %+v, want 2", got)
	}
	if got[0].Text != "alpha one" || got[1].Text != "alpha two" {
		t.Fatalf("order = %+v", got)
	}

	config.Enabled = false
	if got := service.Suggestions("alpha", "local"); got != nil {
		t.Fatalf("disabled autocomplete returned %+v", got)
	}
}

func TestSuggestionsRankPrefixAboveSubstringAndRecentAboveOld(t *testing.T) {
	journal := &journalMemory{}
	service := NewService(journal, defaultConfig, WithClock(func() time.Time {
		return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	}))
	service.Load(context.Background())
	service.RecordAt("local", "echo git", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	service.RecordAt("local", "git rebase", time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC))
	service.RecordAt("local", "git status", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))

	got := service.Suggestions("git", "local")
	if len(got) != 3 {
		t.Fatalf("suggestions = %+v", got)
	}
	if got[0].Text != "git status" || got[1].Text != "git rebase" || got[2].Text != "echo git" {
		t.Fatalf("order = %+v, want prefix matches newest first and the substring last", got)
	}
}

func TestLoadKeepsTheNewestBoundedHistory(t *testing.T) {
	journal := &journalMemory{}
	for index := 0; index < maxHistoryEntries+10; index++ {
		journal.entries = append(journal.entries, journalEntryFor(
			fmt.Sprintf("command-%d", index), "local", time.Time{}))
	}
	service := newTestService(t, journal, defaultConfig())
	if len(service.entries) != maxHistoryEntries {
		t.Fatalf("loaded entries = %d, want %d", len(service.entries), maxHistoryEntries)
	}
}

func TestLoadFailureKeepsThePreviousView(t *testing.T) {
	journal := &journalMemory{}
	service := newTestService(t, journal, defaultConfig())
	service.Record("local", "pwd")
	journal.err = errors.New("disk")
	service.Load(context.Background())
	if got := service.Suggestions("pw", "local"); len(got) != 1 {
		t.Fatalf("suggestions after a load failure = %+v", got)
	}
}

// TestJournalIsTheOnlySuggestionSource pins the single-source behaviour: the
// only rows that can surface are the journal's, and any row that is not in the
// journal can never become a suggestion.
func TestJournalIsTheOnlySuggestionSource(t *testing.T) {
	journal := &journalMemory{}
	journal.entries = []domainhistory.Entry{
		journalEntryFor("git status", "local", time.Now()),
	}
	service := newTestService(t, journal, defaultConfig())

	// A command that exists only outside the journal is never suggested.
	if got := service.Suggestions("kubectl", "local"); got != nil {
		t.Fatalf("suggestions outside the journal = %+v", got)
	}
	got := service.Suggestions("git", "local")
	if len(got) != 1 || got[0].Text != "git status" || got[0].Source != SourceHistory {
		t.Fatalf("suggestions = %+v, want the journal row", got)
	}
}
