package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/histfile"
	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/application/autocomplete"
	apphistory "github.com/mslxl/natcatty/application/history"
	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

var commandTestLocal = commandLocalIdentity{Host: "workstation", User: "alice"}

// TestCommandEntryFromEventResolvesHostScopeOriginAndCaptureFields proves the
// app resolves the row's Host context, scope, origin, author, duration, and
// tier from the completion event and this machine's identity.
func TestCommandEntryFromEventResolvesHostScopeOriginAndCaptureFields(t *testing.T) {
	started := time.Date(2026, 11, 7, 9, 30, 0, 0, time.Local)
	finished := started.Add(1500 * time.Millisecond)
	base := appsession.CommandEvent{
		SessionID: "session-1", Command: "git status", CWD: "/srv/app", Shell: "zsh",
		StartedAt: started, FinishedAt: finished, ExitCode: 0, HasExitCode: true,
		CaptureTier: domainhistory.CaptureTierNativeOSC,
	}
	tests := []struct {
		name        string
		event       appsession.CommandEvent
		wantScope   string
		wantOrigin  string
		wantHost    string
		wantLabel   string
		wantUnsaved bool
	}{
		{
			name: "local shell uses the OS hostname and username",
			event: func() appsession.CommandEvent {
				event := base
				event.Kind = appsession.KindLocal
				event.HostLabel = "Local Terminal"
				event.Scope = "local"
				return event
			}(),
			wantScope: "local", wantOrigin: "workstation:alice",
		},
		{
			name: "saved host uses its network hostname and SSH username",
			event: func() appsession.CommandEvent {
				event := base
				event.Kind = appsession.KindSSH
				event.HostID = "host-1"
				event.HostLabel = "web-1"
				event.Hostname = "10.0.0.5"
				event.Username = "deploy"
				event.Scope = "host:host-1"
				return event
			}(),
			wantScope: "host:host-1", wantOrigin: "10.0.0.5:deploy",
			wantHost: "host-1", wantLabel: "web-1",
		},
		{
			name: "unsaved host scope carries the captured origin",
			event: func() appsession.CommandEvent {
				event := base
				event.Kind = appsession.KindSSH
				event.HostLabel = "deploy@typed.example"
				event.Hostname = "typed.example"
				event.Username = "deploy"
				// The Session resolves an unsaved Host's scope at open; the row
				// keeps the same value so autocomplete reads it from that scope.
				event.Scope = "host:unsaved:typed.example:deploy"
				return event
			}(),
			wantScope:  "host:unsaved:typed.example:deploy",
			wantOrigin: "typed.example:deploy",
			wantLabel:  "deploy@typed.example", wantUnsaved: true,
		},
		{
			name: "serial leaves origin empty and is not local or unsaved",
			event: func() appsession.CommandEvent {
				event := base
				event.Kind = appsession.KindSerial
				event.HostLabel = "Serial: usbserial-1"
				event.Scope = "serial:tty.usbserial-1"
				return event
			}(),
			wantScope: "serial:tty.usbserial-1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := commandEntryFromEvent(test.event, commandTestLocal)
			if entry.Scope != test.wantScope || entry.Origin != test.wantOrigin {
				t.Fatalf("scope/origin = %q/%q, want %q/%q",
					entry.Scope, entry.Origin, test.wantScope, test.wantOrigin)
			}
			if string(entry.HostID) != test.wantHost || entry.HostLabel != test.wantLabel ||
				entry.UnsavedHost != test.wantUnsaved {
				t.Fatalf("host context = %q/%q/%v, want %q/%q/%v",
					entry.HostID, entry.HostLabel, entry.UnsavedHost,
					test.wantHost, test.wantLabel, test.wantUnsaved)
			}
			if entry.Command != "git status" || entry.CWD != "/srv/app" ||
				entry.Shell != "zsh" || !entry.RecordedAt.Equal(started) ||
				!entry.FinishedAt.Equal(finished) || entry.Duration != 1500*time.Millisecond ||
				!entry.HasExitCode || entry.ExitCode != 0 ||
				entry.CaptureTier != domainhistory.CaptureTierNativeOSC ||
				entry.Source != domainhistory.SourceSession {
				t.Fatalf("capture fields = %+v", entry)
			}
			normalized := domainhistory.NormalizeEntry(entry)
			if normalized.AuthorKind != domainhistory.AuthorKindUser ||
				normalized.Author != originUser(test.wantOrigin) {
				t.Fatalf("author = %q/%q", normalized.Author, normalized.AuthorKind)
			}
		})
	}
}

// TestCommandEntryFromEventLabelsUnknownsAndClampsDuration proves missing
// values fall back rather than being fabricated.
func TestCommandEntryFromEventLabelsUnknownsAndClampsDuration(t *testing.T) {
	started := time.Date(2026, 11, 7, 9, 30, 0, 0, time.Local)
	event := appsession.CommandEvent{
		SessionID: "session-1", Kind: appsession.KindSSH, HostID: "host-1",
		Command: "uptime", StartedAt: started,
		FinishedAt: started.Add(-time.Second), CaptureTier: domainhistory.CaptureTierFallback,
	}
	entry := commandEntryFromEvent(event, commandTestLocal)
	if entry.Origin != "unknown:unknown" {
		t.Fatalf("origin = %q, want unknown:unknown", entry.Origin)
	}
	if entry.Duration != 0 {
		t.Fatalf("negative duration = %v, want zero", entry.Duration)
	}
	if entry.Scope != "host:host-1" || entry.HostLabel != "" {
		t.Fatalf("scope/label = %q/%q", entry.Scope, entry.HostLabel)
	}

	event = appsession.CommandEvent{
		SessionID: "session-2", Kind: appsession.KindLocal, HostLabel: "Local Terminal",
		Command: "pwd", StartedAt: started, CaptureTier: domainhistory.CaptureTierFallback,
	}
	entry = commandEntryFromEvent(event, commandLocalIdentity{})
	if entry.Origin != "Local Terminal:unknown" {
		t.Fatalf("local fallback origin = %q", entry.Origin)
	}
	if entry.Duration != 0 || !entry.FinishedAt.IsZero() {
		t.Fatalf("unknown finish invented a duration: %+v", entry)
	}

	event = appsession.CommandEvent{
		SessionID: "session-3", Kind: appsession.KindTelnet,
		Command: "1+1", StartedAt: started, CaptureTier: domainhistory.CaptureTierFallback,
	}
	entry = commandEntryFromEvent(event, commandTestLocal)
	if entry.Scope != "host:unsaved:unknown:unknown" {
		t.Fatalf("unsaved fallback scope = %q", entry.Scope)
	}

	// A serial scope is never local even though it has no Host identity.
	event = appsession.CommandEvent{
		SessionID: "session-4", Kind: appsession.KindSerial,
		Scope: "serial:COM3", Command: "at", StartedAt: started,
	}
	entry = commandEntryFromEvent(event, commandTestLocal)
	if entry.IsLocal() || entry.IsUnsaved() || entry.Origin != "" {
		t.Fatalf("serial entry classified wrongly: %+v", entry)
	}
}

// TestCommandCompletionHookWritesOneRowAndAutocompleteReadsIt drives the real
// journal write path with a temporary SQLite store: the event becomes exactly
// one row, autocomplete suggests that row, and deleting it removes the
// suggestion because both read the same journal.
func TestCommandCompletionHookWritesOneRowAndAutocompleteReadsIt(t *testing.T) {
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

	started := time.Date(2026, 11, 7, 9, 30, 0, 0, time.Local)
	hook := commandCompletionHook(autocompleteService, commandTestLocal)
	hook(appsession.CommandEvent{
		SessionID: "session-1", Kind: appsession.KindLocal, Scope: "local",
		HostLabel: "Local Terminal", Command: "git status", CWD: "/srv/app",
		Shell: "zsh", StartedAt: started, FinishedAt: started.Add(250 * time.Millisecond),
		ExitCode: 0, HasExitCode: true,
		CaptureTier: domainhistory.CaptureTierNativeOSC,
	})

	entries, err := historyService.Entries(ctx)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("one completion wrote %d rows: %+v", len(entries), entries)
	}
	entry := entries[0]
	if entry.Command != "git status" || entry.Scope != "local" ||
		entry.Origin != "workstation:alice" || entry.CWD != "/srv/app" ||
		entry.Shell != "zsh" || entry.Duration != 250*time.Millisecond ||
		entry.CaptureTier != domainhistory.CaptureTierNativeOSC {
		t.Fatalf("journal row = %+v", entry)
	}

	suggestions := autocompleteService.Suggestions("git", "local")
	if len(suggestions) != 1 || suggestions[0].Text != "git status" {
		t.Fatalf("autocomplete did not read the same row: %+v", suggestions)
	}

	if err := historyService.DeleteEntry(ctx, entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	autocompleteService.Load(ctx)
	if suggestions := autocompleteService.Suggestions("git", "local"); len(suggestions) != 0 {
		t.Fatalf("a deleted row is still suggested: %+v", suggestions)
	}
}

// TestShellHistoryImportRunsOnceAcrossRestartAndDeletion drives the startup
// import with the real reader and a temporary SQLite store: configured files
// become shell-history rows with create records once, and a restart after a
// row was deleted neither re-imports nor resurrects it.
func TestShellHistoryImportRunsOnceAcrossRestartAndDeletion(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	zshPath := filepath.Join(home, ".zsh_history")
	if err := os.WriteFile(zshPath,
		[]byte(": 1700000000:0;git status\n: 1700000002:0;make test\n"), 0o644); err != nil {
		t.Fatalf("write zsh history: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bash_history"),
		[]byte("deploy staging\n"), 0o644); err != nil {
		t.Fatalf("write bash history: %v", err)
	}

	path := filepath.Join(t.TempDir(), "history.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	importAt := time.Date(2026, 11, 7, 12, 0, 0, 0, time.Local)
	service := apphistory.NewService(database,
		apphistory.WithClock(func() time.Time { return importAt }))
	importShellHistory(ctx, service, histfile.Default(home), commandTestLocal)

	entries, err := service.Entries(ctx)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("imported %d rows, want 3: %+v", len(entries), entries)
	}
	byCommand := make(map[string]domainhistory.Entry, len(entries))
	for _, entry := range entries {
		byCommand[entry.Command] = entry
		if entry.Source != domainhistory.SourceShellHistory || entry.Scope != "local" {
			t.Fatalf("row provenance = %q/%q, want shell-history local", entry.Source, entry.Scope)
		}
		if entry.Origin != "workstation:alice" || entry.Author != "alice" ||
			entry.AuthorKind != domainhistory.AuthorKindUser {
			t.Fatalf("row origin/author = %q/%q/%q", entry.Origin, entry.Author, entry.AuthorKind)
		}
		if entry.CWD != "" || entry.HasExitCode || !entry.FinishedAt.IsZero() ||
			entry.Duration != 0 || entry.CaptureTier != 0 {
			t.Fatalf("row invented execution fields: %+v", entry)
		}
		if !entry.HasUUIDIdentity() {
			t.Fatalf("row identity = %q, want a UUID", entry.ID)
		}
	}
	status := byCommand["git status"]
	if status.Shell != "zsh" || !status.RecordedAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("git status row = %+v", status)
	}
	makeTest := byCommand["make test"]
	if makeTest.Shell != "zsh" || !makeTest.RecordedAt.Equal(time.Unix(1700000002, 0)) {
		t.Fatalf("make test row = %+v", makeTest)
	}
	deploy := byCommand["deploy staging"]
	if deploy.Shell != "bash" || !deploy.RecordedAt.Equal(importAt) {
		t.Fatalf("timestampless bash row = %+v", deploy)
	}

	records, err := database.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want one create per imported row", len(records))
	}
	for _, record := range records {
		if record.Kind != domainhistory.RecordKindCreate ||
			record.Direction != domainhistory.RecordDirectionLocal ||
			!record.UploadedAt.IsZero() || !domainhistory.IsUUID(record.SeriesHost) {
			t.Fatalf("record = %+v, want a pending local create", record)
		}
	}

	// Deleting an imported row must not bring it back on the next startup.
	if err := service.DeleteEntry(ctx, status.ID); err != nil {
		t.Fatalf("delete imported row: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Change the files so a re-import would be visible.
	if err := os.WriteFile(zshPath, []byte(": 1700000003:0;after restart\n"), 0o644); err != nil {
		t.Fatalf("rewrite zsh history: %v", err)
	}
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	restarted := apphistory.NewService(reopened,
		apphistory.WithClock(func() time.Time { return importAt.Add(time.Hour) }))
	importShellHistory(ctx, restarted, histfile.Default(home), commandTestLocal)

	entries, err = restarted.Entries(ctx)
	if err != nil {
		t.Fatalf("entries after restart: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("rows after restart = %d, want the two live imports", len(entries))
	}
	for _, entry := range entries {
		if entry.Command == "after restart" {
			t.Fatal("a completed import was repeated after restart")
		}
	}
	deleted, found, err := reopened.HistoryEntry(ctx, status.ID)
	if err != nil || !found || deleted.DeletedAt.IsZero() {
		t.Fatalf("deleted import = %+v, found=%v, err=%v; want it still soft-deleted",
			deleted, found, err)
	}
	records, err = reopened.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("records after restart: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("records after restart = %d, want the three creates and one tombstone", len(records))
	}
}

// originUser mirrors the app's author derivation for assertions.
func originUser(origin string) string {
	for index := len(origin) - 1; index >= 0; index-- {
		if origin[index] == ':' {
			return origin[index+1:]
		}
	}
	return ""
}
