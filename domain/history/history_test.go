package history

import (
	"fmt"
	"testing"
	"time"
)

func at(hour int) time.Time {
	return time.Date(2026, 3, 1, hour, 0, 0, 0, time.UTC)
}

func TestConnectionFilterMatchesHostStatusTextAndRange(t *testing.T) {
	logs := []ConnectionLog{
		{ID: "a", HostID: "host-1", HostLabel: "web-1", Hostname: "10.0.0.1",
			Username: "deploy", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(9)},
		{ID: "b", HostID: "host-1", HostLabel: "web-1", Hostname: "10.0.0.1",
			Username: "deploy", Kind: "ssh", Status: ConnectionFailed,
			Error: "permission denied", StartedAt: at(10)},
		{ID: "c", HostLabel: "Local Terminal", Kind: "local",
			Status: ConnectionConnected, StartedAt: at(11)},
	}
	tests := []struct {
		name   string
		filter ConnectionFilter
		want   []string
	}{
		{"all", ConnectionFilter{}, []string{"a", "b", "c"}},
		{"host", ConnectionFilter{Host: "host-1"}, []string{"a", "b"}},
		{"local", ConnectionFilter{Host: LocalHostKey}, []string{"c"}},
		{"status", ConnectionFilter{Status: ConnectionFailed}, []string{"b"}},
		{"query host label", ConnectionFilter{Query: "WEB"}, []string{"a", "b"}},
		{"query error", ConnectionFilter{Query: "denied"}, []string{"b"}},
		{"from", ConnectionFilter{From: at(10)}, []string{"b", "c"}},
		{"to exclusive", ConnectionFilter{To: at(11)}, []string{"a", "b"}},
		{"combined", ConnectionFilter{Host: "host-1", Status: ConnectionConnected,
			From: at(8), To: at(10)}, []string{"a"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for _, log := range logs {
				if test.filter.Match(log) {
					got = append(got, log.ID)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("matches = %v, want %v", got, test.want)
			}
		})
	}
}

func TestUnsavedHostIsNotLocal(t *testing.T) {
	unsaved := ConnectionLog{
		ID: "unsaved", HostLabel: "deploy@typed.example", Hostname: "typed.example",
		Username: "deploy", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(9),
	}
	local := ConnectionLog{ID: "local", Kind: KindLocal, Status: ConnectionConnected, StartedAt: at(9)}
	saved := ConnectionLog{
		ID: "saved", HostID: "host-1", HostLabel: "web-1", Kind: "ssh",
		Status: ConnectionConnected, StartedAt: at(9),
	}

	if unsaved.IsLocal() || !unsaved.IsUnsaved() {
		t.Fatalf("unsaved classification: local = %v, unsaved = %v",
			unsaved.IsLocal(), unsaved.IsUnsaved())
	}
	if !local.IsLocal() || local.IsUnsaved() {
		t.Fatalf("local classification: local = %v, unsaved = %v",
			local.IsLocal(), local.IsUnsaved())
	}
	if saved.IsLocal() || saved.IsUnsaved() {
		t.Fatalf("saved classification: local = %v, unsaved = %v",
			saved.IsLocal(), saved.IsUnsaved())
	}
	if !(ConnectionFilter{Host: UnsavedHostKey}).Match(unsaved) {
		t.Fatal("the unsaved filter did not match a typed Host")
	}
	if (ConnectionFilter{Host: LocalHostKey}).Match(unsaved) {
		t.Fatal("the Local filter matched a typed Host")
	}
	if (ConnectionFilter{Host: LocalHostKey}).Match(saved) {
		t.Fatal("the Local filter matched a saved Host")
	}

	// A row written before UnsavedHost existed is still recognized by kind.
	legacy := unsaved
	legacy.UnsavedHost = false
	if !legacy.IsUnsaved() || legacy.IsLocal() {
		t.Fatalf("legacy unsaved classification: unsaved = %v, local = %v",
			legacy.IsUnsaved(), legacy.IsLocal())
	}
}

func TestEntryFilterMatchesEveryHistoryField(t *testing.T) {
	entries := []Entry{
		{ID: "a", SessionID: "session-1", SessionUUID: "uuid-1", HostID: "host-1",
			HostLabel: "web-1", Scope: "host:host-1", CWD: "/srv/app", Shell: "zsh",
			Origin: "web-1:deploy", Command: "git status", Source: SourceSession,
			ExitCode: 0, HasExitCode: true, RecordedAt: at(9)},
		{ID: "b", SessionID: "session-2", SessionUUID: "uuid-2", HostID: "host-2",
			HostLabel: "db-1", Scope: "host:host-2", CWD: "/var/log", Shell: "bash",
			Origin: "db-1:admin", Command: "kubectl get pods --watch",
			Source: SourceSession, ExitCode: 2, HasExitCode: true, RecordedAt: at(10)},
		{ID: "c", SessionUUID: "uuid-remote", Scope: "remote:remote:deploy",
			Origin: "remote:deploy", Command: "make test", Source: SourceAtuinSync,
			RecordedAt: at(11)},
		{ID: "d", Scope: "local", Command: "ls -la", Source: SourceShellHistory,
			RecordedAt: at(12)},
	}
	tests := []struct {
		name   string
		filter EntryFilter
		want   []string
	}{
		{"all", EntryFilter{}, []string{"a", "b", "c", "d"}},
		{"host", EntryFilter{Host: "host-1"}, []string{"a"}},
		{"local", EntryFilter{Host: LocalHostKey}, []string{"d"}},
		{"origin key", EntryFilter{Host: OriginHostKey("remote:deploy")}, []string{"c"}},
		{"session id", EntryFilter{Session: "session-2"}, []string{"b"}},
		{"session uuid", EntryFilter{Session: "uuid-remote"}, []string{"c"}},
		{"cwd", EntryFilter{CWD: "/srv/app"}, []string{"a"}},
		{"success", EntryFilter{Exit: ExitSuccess}, []string{"a"}},
		{"failure", EntryFilter{Exit: ExitFailure}, []string{"b"}},
		{"unknown exit", EntryFilter{Exit: ExitUnknown}, []string{"c", "d"}},
		{"shell", EntryFilter{Shell: "ZSH"}, []string{"a"}},
		{"source", EntryFilter{Source: SourceSession}, []string{"a", "b"}},
		{"remote source", EntryFilter{Source: SourceAtuinSync}, []string{"c"}},
		{"query", EntryFilter{Query: "KUBECTL"}, []string{"b"}},
		{"query all tokens", EntryFilter{Query: "GIT   status"}, []string{"a"}},
		{"query rejects a missing token", EntryFilter{Query: "git status extra"}, nil},
		{"to exclusive", EntryFilter{To: at(11)}, []string{"a", "b"}},
		{"combined", EntryFilter{Host: "host-2", Session: "session-2",
			CWD: "/var/log", Exit: ExitFailure, Shell: "bash", Source: SourceSession,
			Query: "pods watch", From: at(8), To: at(11)}, []string{"b"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for _, entry := range entries {
				if test.filter.Match(entry) {
					got = append(got, entry.ID)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("matches = %v, want %v", got, test.want)
			}
		})
	}
}

// TestEntryFilterRejectsAnUnknownExitValue proves a typo in the exit filter
// matches nothing instead of silently showing every row.
func TestEntryFilterRejectsAnUnknownExitValue(t *testing.T) {
	entry := Entry{ID: "a", Scope: "local", Command: "ls", Source: SourceSession,
		RecordedAt: at(9)}
	if (EntryFilter{Exit: ExitFilter("bogus")}).Match(entry) {
		t.Fatal("an unknown exit filter matched a row")
	}
}

// TestSortEntriesIsDeterministic ties equal timestamps to descending ID and
// pins newest-first ordering regardless of the input order.
func TestSortEntriesIsDeterministic(t *testing.T) {
	entries := []Entry{
		{ID: "b", RecordedAt: at(9)},
		{ID: "a", RecordedAt: at(10)},
		{ID: "c", RecordedAt: at(9)},
	}
	SortEntries(entries)
	if got := fmt.Sprint([]string{entries[0].ID, entries[1].ID, entries[2].ID}); got != "[a c b]" {
		t.Fatalf("sorted IDs = %s, want [a c b]", got)
	}
}

// TestSortEntriesForQueryRanksBestMatchFirst proves a search ranks an exact
// command above a prefix, a word prefix, and a substring even when the weaker
// match is newer, and that equal scores keep the deterministic time-and-ID
// order. Whitespace and case in the query do not change the result.
func TestSortEntriesForQueryRanksBestMatchFirst(t *testing.T) {
	entries := []Entry{
		{ID: "substring", Command: "legit", RecordedAt: at(12)},
		{ID: "word", Command: "run git", RecordedAt: at(11)},
		{ID: "prefix", Command: "git status", RecordedAt: at(10)},
		{ID: "exact", Command: "GIT", RecordedAt: at(9)},
	}
	SortEntriesForQuery(entries, "  Git  ")
	ids := make([]string, len(entries))
	for index, entry := range entries {
		ids[index] = entry.ID
	}
	if got := fmt.Sprint(ids); got != "[exact prefix word substring]" {
		t.Fatalf("query order = %s", got)
	}
}

// TestSortEntriesForQueryBreaksTiesByTimeAndIdentity proves two equally good
// matches still page deterministically.
func TestSortEntriesForQueryBreaksTiesByTimeAndIdentity(t *testing.T) {
	entries := []Entry{
		{ID: "b", Command: "run git", RecordedAt: at(9)},
		{ID: "a", Command: "use git", RecordedAt: at(10)},
		{ID: "c", Command: "git", RecordedAt: at(9)},
	}
	SortEntriesForQuery(entries, "git")
	if got := fmt.Sprint([]string{entries[0].ID, entries[1].ID, entries[2].ID}); got != "[c a b]" {
		t.Fatalf("query tie order = %s, want [c a b]", got)
	}
}

func TestEntryFilterSeparatesUnsavedHostsFromLocal(t *testing.T) {
	entries := []Entry{
		{ID: "local", Command: "ls", Scope: "local", Source: SourceSession, RecordedAt: at(9)},
		{ID: "unsaved", HostLabel: "deploy@typed.example", UnsavedHost: true,
			Command: "uptime", Scope: "local", Source: SourceSession, RecordedAt: at(9)},
		{ID: "saved", HostID: "host-1", HostLabel: "web-1",
			Command: "df", Scope: "host:host-1", Source: SourceSession, RecordedAt: at(9)},
	}
	if got := matchingEntryIDs(entries, EntryFilter{Host: LocalHostKey}); fmt.Sprint(got) != "[local]" {
		t.Fatalf("Local filter matched %v, want only the local shell", got)
	}
	if got := matchingEntryIDs(entries, EntryFilter{Host: UnsavedHostKey}); fmt.Sprint(got) != "[unsaved]" {
		t.Fatalf("unsaved filter matched %v, want only the typed Host", got)
	}
	if got := matchingEntryIDs(entries, EntryFilter{Host: "host-1"}); fmt.Sprint(got) != "[saved]" {
		t.Fatalf("Host filter matched %v, want the saved Host", got)
	}
}

func matchingEntryIDs(entries []Entry, filter EntryFilter) []string {
	var matched []string
	for _, entry := range entries {
		if filter.Match(entry) {
			matched = append(matched, entry.ID)
		}
	}
	return matched
}

func TestNormalizeEntryDefaultsCapturedAuthorFromOrigin(t *testing.T) {
	captured := NormalizeEntry(Entry{
		Command: "ls", Source: SourceSession, Origin: " web.example.com:deploy ",
	})
	if captured.AuthorKind != AuthorKindUser || captured.Author != "deploy" {
		t.Fatalf("captured author = %q/%q, want user/deploy", captured.AuthorKind, captured.Author)
	}
	if got := AuthorFromOrigin("[2001:db8::1]:admin"); got != "admin" {
		t.Fatalf("IPv6 origin author = %q, want admin", got)
	}
	if got := AuthorFromOrigin("just-a-host"); got != "" {
		t.Fatalf("origin without a user = %q, want empty", got)
	}
	remote := NormalizeEntry(Entry{
		Command: "ls", Source: SourceAtuinSync, Origin: "other.example:root",
	})
	if remote.AuthorKind != "" || remote.Author != "" {
		t.Fatalf("remote author = %q/%q, want the Atuin record preserved", remote.AuthorKind, remote.Author)
	}
}

func TestCaptureOriginFallsBackWithoutInventingAValue(t *testing.T) {
	tests := []struct {
		name                      string
		hostname, username, label string
		want                      string
	}{
		{"hostname and user", "web.example.com", "deploy", "web-1", "web.example.com:deploy"},
		{"label fallback", "", "deploy", "web-1", "web-1:deploy"},
		{"unknown host and user", "", "", "", "unknown:unknown"},
		{"unknown user", "web.example.com", "", "web-1", "web.example.com:unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CaptureOrigin(test.hostname, test.username, test.label); got != test.want {
				t.Fatalf("CaptureOrigin = %q, want %q", got, test.want)
			}
		})
	}
}

func TestImportedAndSyncedRowsAreNeverTheLocalShell(t *testing.T) {
	remote := []string{SourceAtuinImport, SourceAtuinSync}
	for _, source := range remote {
		entry := Entry{ID: UUIDv7(), Scope: "local", Command: "ls", Source: source, RecordedAt: at(9)}
		if entry.IsLocal() {
			t.Fatalf("a %s row was reported as the local shell", source)
		}
	}
	local := Entry{ID: UUIDv7(), Scope: "local", Command: "ls", Source: SourceSession, RecordedAt: at(9)}
	if !local.IsLocal() {
		t.Fatal("a captured local row was not reported as local")
	}
	shellHistory := local
	shellHistory.Source = SourceShellHistory
	if !shellHistory.IsLocal() {
		t.Fatal("a shell-history local row was not reported as local")
	}
	scoped := local
	scoped.Scope = "host:host-1"
	if scoped.IsLocal() {
		t.Fatal("a Host-scoped row was reported as local")
	}
}

func TestNormalizeConnectionLogClassifiesLocalAndUnsavedHosts(t *testing.T) {
	local := NormalizeConnectionLog(ConnectionLog{Kind: "", HostLabel: "Local Terminal"})
	if !local.IsLocal() || local.HostLabel != "" {
		t.Fatalf("default-kind log classified as local = %v with label %q",
			local.IsLocal(), local.HostLabel)
	}
	if !NormalizeConnectionLog(ConnectionLog{Kind: "LOCAL"}).IsLocal() {
		t.Fatal("the local kind is case-insensitive")
	}

	unsaved := NormalizeConnectionLog(ConnectionLog{
		Kind: "ssh", HostLabel: " deploy@typed.example ", Hostname: " typed.example ",
	})
	if !unsaved.IsUnsaved() || unsaved.IsLocal() {
		t.Fatalf("typed Host classified as unsaved = %v, local = %v",
			unsaved.IsUnsaved(), unsaved.IsLocal())
	}
	if unsaved.HostLabel != "deploy@typed.example" || unsaved.Hostname != "typed.example" {
		t.Fatalf("typed target was not trimmed: %+v", unsaved)
	}
	if NormalizeConnectionLog(ConnectionLog{HostID: "host-1", Kind: "ssh"}).IsUnsaved() {
		t.Fatal("a saved Host was classified as unsaved")
	}
}

func TestNetworkProviderRowsAreNeitherLocalNorUnsaved(t *testing.T) {
	log := NormalizeConnectionLog(ConnectionLog{
		ID:                   "provider-1",
		Kind:                 "tailscale",
		Status:               ConnectionConnected,
		StartedAt:            at(9),
		NetworkProfileID:     " net-1 ",
		NetworkProfileLabel:  " Office ",
		NetworkProviderKind:  " tailscale ",
		NetworkProviderPhase: " running ",
		NetworkAddress:       " natcatty-office.tailnet.ts.net ",
	})
	if !log.IsProvider() {
		t.Fatal("a row with a provider phase was not classified as a provider row")
	}
	if log.IsLocal() || log.IsUnsaved() || log.UnsavedHost {
		t.Fatalf("provider row classified as Host/local: %+v", log)
	}
	if log.HostLabel != "Office" || log.NetworkProfileLabel != "Office" {
		t.Fatalf("provider labels = %q/%q", log.HostLabel, log.NetworkProfileLabel)
	}
	if log.NetworkProfileID != "net-1" || log.NetworkProviderPhase != "running" ||
		log.NetworkAddress != "natcatty-office.tailnet.ts.net" {
		t.Fatalf("provider metadata was not trimmed: %+v", log)
	}

	for _, key := range []string{LocalHostKey, UnsavedHostKey} {
		if (ConnectionFilter{Host: key}).Match(log) {
			t.Fatalf("the %q filter matched a provider row", key)
		}
	}
	for _, query := range []string{"office", "running", "tailnet.ts.net", "tailscale"} {
		if !(ConnectionFilter{Query: query}).Match(log) {
			t.Fatalf("query %q did not match the provider row", query)
		}
	}
}

func TestPruneKeepsTheNewestCountAndIsDeterministic(t *testing.T) {
	logs := make([]ConnectionLog, 0, 5)
	for index := 0; index < 5; index++ {
		logs = append(logs, ConnectionLog{
			ID: fmt.Sprintf("log-%d", index), Kind: "ssh", Status: ConnectionConnected,
			StartedAt: at(index),
		})
	}
	// Feed them oldest-first to prove pruning does not depend on input order.
	got := PruneConnectionLogs(logs, Retention{Count: 3}, at(12))
	want := []string{"log-4", "log-3", "log-2"}
	if len(got) != len(want) {
		t.Fatalf("kept %d logs, want %d: %+v", len(got), len(want), got)
	}
	for index, id := range want {
		if got[index].ID != id {
			t.Fatalf("kept[%d] = %q, want %q", index, got[index].ID, id)
		}
	}

	sameTime := []ConnectionLog{
		{ID: "a", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(9)},
		{ID: "c", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(9)},
		{ID: "b", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(9)},
	}
	got = PruneConnectionLogs(sameTime, Retention{Count: 2}, at(12))
	if got[0].ID != "c" || got[1].ID != "b" {
		t.Fatalf("tie-break = %q, %q; want c, b", got[0].ID, got[1].ID)
	}
}

func TestZeroRetentionKeepsEveryLog(t *testing.T) {
	logs := []ConnectionLog{
		{ID: "a", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(1)},
		{ID: "b", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(2)},
	}
	if got := PruneConnectionLogs(logs, Retention{}, at(100)); len(got) != 2 {
		t.Fatalf("count-zero retention kept %d, want 2", len(got))
	}
}

func TestNormalizeEntryTrimsAndBoundsTheCommand(t *testing.T) {
	long := make([]byte, MaxCommandLength+10)
	for index := range long {
		long[index] = 'x'
	}
	entry := NormalizeEntry(Entry{Command: "  " + string(long) + "  ", Scope: " local "})
	if len(entry.Command) != MaxCommandLength {
		t.Fatalf("command length = %d, want %d", len(entry.Command), MaxCommandLength)
	}
	if entry.Scope != "local" {
		t.Fatalf("scope = %q", entry.Scope)
	}
	if entry.Source != SourceSession {
		t.Fatalf("source = %q, want %q", entry.Source, SourceSession)
	}
}

func TestNormalizeConnectionLogDerivesStatus(t *testing.T) {
	if got := NormalizeConnectionLog(ConnectionLog{}); got.Status != ConnectionConnected {
		t.Fatalf("empty error status = %q, want connected", got.Status)
	}
	if got := NormalizeConnectionLog(ConnectionLog{Error: "dial failed"}); got.Status != ConnectionFailed {
		t.Fatalf("error status = %q, want failed", got.Status)
	}
}
