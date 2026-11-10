package app

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/application/autocomplete"
	apphistory "github.com/mslxl/natcatty/application/history"
	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

func TestTransientCommandsBoundCopyAndDrop(t *testing.T) {
	registry := newTransientCommands()
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for index := 0; index < maxTransientCommands+5; index++ {
		registry.Remember("session-1", domainhistory.Entry{
			Command: fmt.Sprintf("command-%d", index), Scope: "host:host-1", RecordedAt: at,
		})
	}

	entries := registry.Entries("session-1")
	if len(entries) != maxTransientCommands {
		t.Fatalf("entries = %d, want the bound %d", len(entries), maxTransientCommands)
	}
	if entries[0].Command != "command-5" ||
		entries[len(entries)-1].Command != fmt.Sprintf("command-%d", maxTransientCommands+4) {
		t.Fatalf("bounded entries = %q .. %q, want the newest window",
			entries[0].Command, entries[len(entries)-1].Command)
	}

	// Entries returns a copy: the caller cannot mutate the registry.
	entries[0].Command = "mutated"
	if got := registry.Entries("session-1")[0].Command; got != "command-5" {
		t.Fatalf("the returned slice aliases the registry: %q", got)
	}

	// Empty Session identities are ignored, for both writes and reads.
	registry.Remember("", domainhistory.Entry{Command: "orphan"})
	if got := registry.Entries(""); got != nil {
		t.Fatalf("empty Session entries = %+v", got)
	}

	registry.Drop("session-1")
	if got := registry.Entries("session-1"); got != nil {
		t.Fatalf("after Drop entries = %+v", got)
	}
}

// Drop is terminal: a completion published by a concurrent prompt recheck
// after teardown must not re-create a buffer that no watcher remains to drop.
func TestTransientCommandsLateRememberAfterDropIsIgnored(t *testing.T) {
	registry := newTransientCommands()
	done := make(chan struct{})
	registry.Watch("session-1", done)
	registry.Watch("session-1", done)
	registry.Remember("session-1", domainhistory.Entry{Command: "git status"})
	close(done)

	deadline := time.Now().Add(3 * time.Second)
	for registry.Entries("session-1") != nil {
		if time.Now().After(deadline) {
			t.Fatal("the watched Session's entries were not dropped")
		}
		time.Sleep(time.Millisecond)
	}

	registry.Remember("session-1", domainhistory.Entry{Command: "late"})
	if got := registry.Entries("session-1"); got != nil {
		t.Fatalf("a late Remember after Drop re-created entries: %+v", got)
	}
}

// Watch ignores an empty Session identity and a nil done channel; it must not
// touch a valid Session's buffer in either case.
func TestTransientCommandsWatchIgnoresEmptyAndNilInputs(t *testing.T) {
	registry := newTransientCommands()
	registry.Watch("", make(chan struct{}))
	registry.Watch("session-1", nil)
	registry.Watch("session-1", nil)
	registry.Remember("session-1", domainhistory.Entry{Command: "git status"})

	if got := registry.Entries("session-1"); len(got) != 1 || got[0].Command != "git status" {
		t.Fatalf("entries = %+v, want the remembered command", got)
	}
}

// The Drop tombstones are bounded: more than maxClosedTombstones distinct
// Sessions must not grow the map without bound. The newest tombstone still
// blocks a late Remember; the oldest is forgotten and no longer blocks.
func TestTransientCommandsBoundsClosedTombstones(t *testing.T) {
	registry := newTransientCommands()
	total := maxClosedTombstones + 10
	for index := 0; index < total; index++ {
		registry.Drop(model.SessionID(fmt.Sprintf("session-%d", index)))
	}

	registry.mu.Lock()
	closed := len(registry.closed)
	order := len(registry.closedOrder)
	registry.mu.Unlock()
	if closed > maxClosedTombstones {
		t.Fatalf("closed tombstones = %d, want at most %d", closed, maxClosedTombstones)
	}
	if order > maxClosedTombstones {
		t.Fatalf("closed tombstone order = %d, want at most %d", order, maxClosedTombstones)
	}

	oldest := model.SessionID("session-0")
	registry.Remember(oldest, domainhistory.Entry{Command: "oldest"})
	if got := registry.Entries(oldest); len(got) != 1 || got[0].Command != "oldest" {
		t.Fatalf("the forgotten oldest tombstone still blocked Remember: %+v", got)
	}

	newest := model.SessionID(fmt.Sprintf("session-%d", total-1))
	registry.Remember(newest, domainhistory.Entry{Command: "newest"})
	if got := registry.Entries(newest); got != nil {
		t.Fatalf("the newest tombstone no longer blocked a late Remember: %+v", got)
	}
}

// TestCommandCompletionHookKeepsTransientCommandsOutOfTheJournal drives the
// real journal write path with a temporary SQLite store: a transient completion
// feeds suggestions from memory but writes neither a history row nor a create
// record, while a normal completion still writes exactly one of each.
func TestCommandCompletionHookKeepsTransientCommandsOutOfTheJournal(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	historyService := apphistory.NewService(database)
	autocompleteService := autocomplete.NewService(historyService, func() autocomplete.Config {
		return autocomplete.Config{
			Enabled:        true,
			MinChars:       1,
			MaxSuggestions: 5,
			Scope:          domainterminal.AutocompleteHistoryHost,
		}
	})
	autocompleteService.Load(ctx)
	transient := newTransientCommands()
	hook := commandCompletionHook(autocompleteService, commandTestLocal, transient)

	started := time.Date(2026, 11, 10, 9, 30, 0, 0, time.Local)
	hook(appsession.CommandEvent{
		SessionID: "session-host", Kind: appsession.KindSSH, HostID: "host-1",
		Hostname: "web-1.example.test", Username: "deploy", Scope: "host:host-1",
		Command: "git status", CWD: "/srv/app", Shell: "zsh",
		StartedAt: started, FinishedAt: started.Add(time.Second),
		ExitCode: 0, HasExitCode: true, Transient: true,
		CaptureTier: domainhistory.CaptureTierFallback,
	})

	entries, err := historyService.Entries(ctx)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a transient completion was journaled: %+v", entries)
	}
	records, err := database.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("a transient completion created a sync record: %+v", records)
	}

	transientEntries := transient.Entries("session-host")
	if len(transientEntries) != 1 || transientEntries[0].Command != "git status" {
		t.Fatalf("transient entries = %+v", transientEntries)
	}
	suggestions := autocompleteService.SuggestionsInWith(
		"git", "host:host-1", "/srv/app", transient.Entries("session-host"))
	if len(suggestions) != 1 || suggestions[0].Text != "git status" {
		t.Fatalf("transient suggestions = %+v", suggestions)
	}
	if plain := autocompleteService.Suggestions("git", "host:host-1"); len(plain) != 0 {
		t.Fatalf("the transient command leaked into the journal view: %+v", plain)
	}

	hook(appsession.CommandEvent{
		SessionID: "session-local", Kind: appsession.KindLocal, Scope: "local",
		HostLabel: "Local Terminal", Command: "make test", CWD: "/srv/app",
		StartedAt: started, FinishedAt: started.Add(2 * time.Second),
		ExitCode: 0, HasExitCode: true,
		CaptureTier: domainhistory.CaptureTierFallback,
	})
	entries, err = historyService.Entries(ctx)
	if err != nil {
		t.Fatalf("entries after a normal completion: %v", err)
	}
	if len(entries) != 1 || entries[0].Command != "make test" {
		t.Fatalf("journal rows = %+v, want the normal completion only", entries)
	}
	records, err = database.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("records after a normal completion: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want one create record", len(records))
	}
	if got := len(transient.Entries("session-host")); got != 1 {
		t.Fatalf("the normal completion changed the transient buffer: %d", got)
	}
}

// TestAtuinHistoryRecorderReloadsOnlyWhenRowsAreImported proves the autocomplete
// view refreshes after a real import writes rows, but not after a checkpoint-only
// re-import that writes nothing.
func TestAtuinHistoryRecorderReloadsOnlyWhenRowsAreImported(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	historyService := apphistory.NewService(database)
	reloads := 0
	recorder := atuinHistoryRecorder{
		history: historyService,
		reload:  func() { reloads++ },
	}
	recorded := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	rows := []domainhistory.AtuinRow{{
		ID:        "9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
		Timestamp: recorded.UnixNano(), Command: "git status",
	}}

	imported, err := recorder.ImportHostAtuin(ctx, "host-1", rows, "web-1.example.test:deploy")
	if err != nil || imported != 1 {
		t.Fatalf("first import = %d, %v; want 1", imported, err)
	}
	if reloads != 1 {
		t.Fatalf("reloads after the first import = %d, want 1", reloads)
	}

	imported, err = recorder.ImportHostAtuin(ctx, "host-1", rows, "web-1.example.test:deploy")
	if err != nil || imported != 0 {
		t.Fatalf("repeated import = %d, %v; want 0", imported, err)
	}
	if reloads != 1 {
		t.Fatalf("a checkpoint-only import reloaded the view: %d reloads", reloads)
	}
}

// TestAtuinImportBecomesSuggestibleWithoutAnExplicitReload drives the real
// journal, history service, autocomplete service, and recorder: an import that
// writes a row must refresh the autocomplete view itself, so the Host's Atuin
// command is suggestible without any further Load. The imported row's scope is
// remote:<origin>, so global scope sees it and Host scope filters it out.
func TestAtuinImportBecomesSuggestibleWithoutAnExplicitReload(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	historyService := apphistory.NewService(database)
	scope := domainterminal.AutocompleteHistoryGlobal
	autocompleteService := autocomplete.NewService(historyService, func() autocomplete.Config {
		return autocomplete.Config{
			Enabled:        true,
			MinChars:       1,
			MaxSuggestions: 5,
			Scope:          scope,
		}
	})
	// Start from the empty journal; only the import's reload can add a row.
	autocompleteService.Load(ctx)
	recorder := atuinHistoryRecorder{
		history: historyService,
		reload:  func() { autocompleteService.Load(context.Background()) },
	}
	recorded := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	rows := []domainhistory.AtuinRow{{
		ID:        "6f1a2b3c-4d5e-4f60-8a9b-0c1d2e3f4a5b",
		Timestamp: recorded.UnixNano(), Command: "kubectl get pods",
	}}

	imported, err := recorder.ImportHostAtuin(ctx, "host-1", rows, "web-1.example.test:deploy")
	if err != nil || imported != 1 {
		t.Fatalf("import = %d, %v; want 1", imported, err)
	}

	suggestions := autocompleteService.Suggestions("kubectl", "host:host-1")
	if len(suggestions) != 1 || suggestions[0].Text != "kubectl get pods" {
		t.Fatalf("global-scope suggestions after import = %+v, want the imported command", suggestions)
	}

	scope = domainterminal.AutocompleteHistoryHost
	if got := autocompleteService.Suggestions("kubectl", "host:host-1"); len(got) != 0 {
		t.Fatalf("Host scope suggested an imported remote row: %+v", got)
	}
}
