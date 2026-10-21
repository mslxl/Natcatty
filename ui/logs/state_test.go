package logs

import (
	"context"
	"errors"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

type fakeSource struct {
	logs      []domainhistory.ConnectionLog
	entries   []domainhistory.Entry
	revision  uint64
	listeners []func()
	deleteErr error
	cleared   int
}

func (f *fakeSource) ConnectionLogs(context.Context) ([]domainhistory.ConnectionLog, error) {
	return append([]domainhistory.ConnectionLog(nil), f.logs...), nil
}

func (f *fakeSource) Entries(context.Context) ([]domainhistory.Entry, error) {
	return append([]domainhistory.Entry(nil), f.entries...), nil
}

func (f *fakeSource) DeleteConnectionLog(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for index, log := range f.logs {
		if log.ID == id {
			f.logs = append(f.logs[:index], f.logs[index+1:]...)
			f.markChanged()
			return nil
		}
	}
	return nil
}

func (f *fakeSource) DeleteEntry(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for index, entry := range f.entries {
		if entry.ID == id {
			f.entries = append(f.entries[:index], f.entries[index+1:]...)
			f.markChanged()
			return nil
		}
	}
	return nil
}

func (f *fakeSource) ClearConnectionLogs(context.Context) (int, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	removed := len(f.logs)
	f.logs = nil
	f.cleared = removed
	f.markChanged()
	return removed, nil
}

func (f *fakeSource) ClearHistory(context.Context) (int, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	removed := len(f.entries)
	f.entries = nil
	f.cleared = removed
	f.markChanged()
	return removed, nil
}

func (f *fakeSource) Revision() uint64 { return f.revision }

func (f *fakeSource) Subscribe(fn func()) func() {
	f.listeners = append(f.listeners, fn)
	return func() {}
}

func (f *fakeSource) markChanged() {
	f.revision++
	for _, listener := range f.listeners {
		listener()
	}
}

type fakeActions struct {
	err      error
	sessions []model.SessionID
	hosts    []model.HostID
}

func (f *fakeActions) OpenTerminal(sessionID model.SessionID, hostID model.HostID) error {
	f.sessions = append(f.sessions, sessionID)
	f.hosts = append(f.hosts, hostID)
	return f.err
}

func logAt(id string, hostID model.HostID, status domainhistory.ConnectionStatus,
	started time.Time) domainhistory.ConnectionLog {
	return domainhistory.ConnectionLog{
		ID: id, HostID: hostID, HostLabel: "host-" + id, Hostname: "10.0.0." + id,
		Kind: "ssh", Status: status, StartedAt: started,
	}
}

func entryAt(id string, hostID model.HostID, command, source string,
	at time.Time) domainhistory.Entry {
	return domainhistory.Entry{
		ID: id, HostID: hostID, HostLabel: "host-" + id, Scope: "local",
		Command: command, Source: source, RecordedAt: at,
	}
}

func TestFiltersNarrowByHostStatusTextAndDate(t *testing.T) {
	now := time.Now()
	local := logAt("3", "", domainhistory.ConnectionConnected, now.Add(-2*time.Hour))
	local.Kind = "local"
	source := &fakeSource{
		logs: []domainhistory.ConnectionLog{
			logAt("1", "host-1", domainhistory.ConnectionConnected, now.Add(-time.Hour)),
			logAt("2", "host-2", domainhistory.ConnectionFailed, now.Add(-48*time.Hour)),
			local,
		},
	}
	state := NewState(source, nil)
	state.Refresh()
	if got := len(state.FilteredLogs()); got != 3 {
		t.Fatalf("unfiltered logs = %d, want 3", got)
	}

	state.SetHostKey("host-2")
	if got := state.FilteredLogs(); len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("host filter = %+v", got)
	}
	state.SetHostKey(domainhistory.LocalHostKey)
	if got := state.FilteredLogs(); len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("local filter = %+v", got)
	}
	state.SetHostKey("")
	state.SetStatus(domainhistory.ConnectionFailed)
	if got := state.FilteredLogs(); len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("status filter = %+v", got)
	}
	state.SetStatus("")
	state.SetSearch("10.0.0.3")
	if got := state.FilteredLogs(); len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("text filter = %+v", got)
	}
	state.SetSearch("")

	yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	today := time.Now().Format("2006-01-02")
	state.SetDateRange(yesterday, today)
	if got := state.FilteredLogs(); len(got) != 2 {
		t.Fatalf("date filter = %+v, want the two recent logs", got)
	}
	state.SetDateRange("not-a-date", "")
	if len(state.FilteredLogs()) != 0 || !state.DateProblem() {
		t.Fatalf("invalid range matched %d records, problem %v",
			len(state.FilteredLogs()), state.DateProblem())
	}
	state.SetDateRange(today, yesterday)
	if !state.DateProblem() {
		t.Fatal("an inverted range did not report a problem")
	}
}

func TestHistoryFiltersAndPaging(t *testing.T) {
	now := time.Now()
	source := &fakeSource{}
	for index := 0; index < PageSize+2; index++ {
		id := string(rune('a' + index%26))
		source.entries = append(source.entries, entryAt(id, "", "command",
			domainhistory.SourceSession, now.Add(-time.Duration(index)*time.Minute)))
	}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	if len(state.VisibleEntries()) != PageSize || !state.HasMore() {
		t.Fatalf("visible = %d, has more = %v", len(state.VisibleEntries()), state.HasMore())
	}
	state.ShowMore()
	if len(state.VisibleEntries()) != PageSize+2 || state.HasMore() {
		t.Fatalf("after show more: visible = %d, has more = %v",
			len(state.VisibleEntries()), state.HasMore())
	}

	source.entries = append(source.entries, entryAt("shell", "", "ls",
		domainhistory.SourceShellHistory, now))
	state.reload()
	state.SetSourceFilter(domainhistory.SourceShellHistory)
	if got := state.FilteredEntries(); len(got) != 1 || got[0].ID != "shell" {
		t.Fatalf("source filter = %+v", got)
	}
}

func TestRefreshFollowsTheSourceRevision(t *testing.T) {
	source := &fakeSource{logs: []domainhistory.ConnectionLog{
		logAt("1", "host-1", domainhistory.ConnectionConnected, time.Now()),
	}}
	redraws := 0
	state := NewState(source, nil, WithRefreshRequest(func() { redraws++ }))
	state.Refresh()
	if state.TotalLogs() != 1 {
		t.Fatalf("logs = %d, want 1", state.TotalLogs())
	}
	source.logs = append(source.logs, logAt("2", "host-2",
		domainhistory.ConnectionFailed, time.Now()))
	source.markChanged()
	if redraws != 1 {
		t.Fatalf("redraw requests = %d, want 1", redraws)
	}
	state.Refresh()
	if state.TotalLogs() != 2 {
		t.Fatalf("logs after revision = %d, want 2", state.TotalLogs())
	}
}

func TestRowActionsDeleteClearAndReportProblems(t *testing.T) {
	source := &fakeSource{
		logs: []domainhistory.ConnectionLog{
			logAt("1", "host-1", domainhistory.ConnectionConnected, time.Now()),
		},
		entries: []domainhistory.Entry{
			entryAt("e1", "", "ls", domainhistory.SourceSession, time.Now()),
		},
	}
	actions := &fakeActions{}
	state := NewState(source, actions)
	state.Refresh()

	state.OpenTerminal("session-1", "host-1")
	if len(actions.sessions) != 1 || actions.sessions[0] != "session-1" ||
		actions.hosts[0] != "host-1" {
		t.Fatalf("actions = %+v", actions)
	}
	if state.OpenProblem() != ProblemNone {
		t.Fatalf("open problem = %v", state.OpenProblem())
	}
	actions.err = ErrHostMissing
	state.OpenTerminal("", "deleted")
	if state.OpenProblem() != ProblemHostMissing {
		t.Fatalf("missing Host problem = %v", state.OpenProblem())
	}
	actions.err = errors.New("boom")
	state.OpenTerminal("", "host-1")
	if state.OpenProblem() != ProblemOpen {
		t.Fatalf("open problem = %v", state.OpenProblem())
	}

	state.DeleteLog("1")
	if state.TotalLogs() != 0 || state.Problem() != ProblemNone {
		t.Fatalf("logs = %d, problem = %v", state.TotalLogs(), state.Problem())
	}

	state.SelectTab(TabHistory)
	state.RequestClear()
	if !state.PendingClear() {
		t.Fatal("clear confirmation did not open")
	}
	state.ConfirmClear()
	if state.TotalEntries() != 0 || state.Cleared() != 1 {
		t.Fatalf("entries = %d, cleared = %d", state.TotalEntries(), state.Cleared())
	}

	source.deleteErr = errors.New("disk")
	state.DeleteEntry("missing")
	if state.Problem() != ProblemDelete {
		t.Fatalf("delete problem = %v", state.Problem())
	}
	state.ConfirmClear()
	if state.Problem() != ProblemClear {
		t.Fatalf("clear problem = %v", state.Problem())
	}
}

// TestUnsavedHostAttemptsAreNotLocal proves a typed Host with no saved Vault
// record keeps its own identity: the Local filter must not match it, and a
// separate unsaved filter must. The row carries no UnsavedHost marker here,
// so classification falls back to its non-local kind, as it does for rows
// recorded before the marker existed.
func TestUnsavedHostAttemptsAreNotLocal(t *testing.T) {
	now := time.Now()
	source := &fakeSource{logs: []domainhistory.ConnectionLog{
		{
			ID: "unsaved", HostLabel: "deploy@typed.example", Hostname: "typed.example",
			Username: "deploy", Kind: "ssh", Status: domainhistory.ConnectionConnected,
			StartedAt: now,
		},
		{
			ID: "local", Kind: "local", Status: domainhistory.ConnectionConnected,
			StartedAt: now,
		},
		{
			ID: "saved", HostID: "host-1", HostLabel: "web-1", Hostname: "10.0.0.1",
			Kind: "ssh", Status: domainhistory.ConnectionConnected, StartedAt: now,
		},
	}}
	state := NewState(source, nil)
	state.Refresh()

	state.SetHostKey(domainhistory.LocalHostKey)
	if got := state.FilteredLogs(); len(got) != 1 || got[0].ID != "local" {
		t.Fatalf("Local filter matched %+v, want only the local shell", got)
	}
	// The persisted filter vocabulary is shared with old rows.
	state.SetHostKey(domainhistory.UnsavedHostKey)
	if got := state.FilteredLogs(); len(got) != 1 || got[0].ID != "unsaved" {
		t.Fatalf("unsaved filter matched %+v, want only the typed Host", got)
	}
	state.SetHostKey("")

	options := state.HostOptions()
	foundUnsaved := false
	for _, option := range options {
		if option.Key == domainhistory.UnsavedHostKey {
			foundUnsaved = true
		}
		if option.Key == domainhistory.LocalHostKey && option.Label != "" {
			t.Fatalf("Local option carries a record label: %+v", option)
		}
	}
	if !foundUnsaved {
		t.Fatalf("Host options = %+v, want an unsaved entry", options)
	}
}

// TestClearOnConnectionsRemovesOnlyConnectionLogs proves the Connections tab
// owns a clear-all with the same confirmation as history, and clearing one
// family leaves the other untouched.
func TestClearOnConnectionsRemovesOnlyConnectionLogs(t *testing.T) {
	now := time.Now()
	source := &fakeSource{
		logs: []domainhistory.ConnectionLog{
			logAt("1", "host-1", domainhistory.ConnectionConnected, now),
			logAt("2", "host-2", domainhistory.ConnectionFailed, now),
		},
		entries: []domainhistory.Entry{
			entryAt("e1", "", "ls", domainhistory.SourceSession, now),
			entryAt("e2", "", "pwd", domainhistory.SourceSession, now),
		},
	}
	state := NewState(source, nil)
	state.Refresh()

	state.RequestClear()
	if !state.PendingClear() {
		t.Fatal("clear confirmation did not open on the Connections tab")
	}
	state.ConfirmClear()
	if state.TotalLogs() != 0 || state.Cleared() != 2 {
		t.Fatalf("logs = %d, cleared = %d", state.TotalLogs(), state.Cleared())
	}
	if state.TotalEntries() != 2 {
		t.Fatalf("clearing connection logs removed %d history entries, want 2 left",
			state.TotalEntries())
	}

	state.SelectTab(TabHistory)
	state.RequestClear()
	state.ConfirmClear()
	if state.TotalEntries() != 0 || state.Cleared() != 2 {
		t.Fatalf("entries = %d, cleared = %d", state.TotalEntries(), state.Cleared())
	}
	if state.TotalLogs() != 0 {
		t.Fatalf("the history clear recreated %d connection logs", state.TotalLogs())
	}
}

// TestCancelKeepsConnectionLogs proves Cancel leaves every row in place.
func TestCancelKeepsConnectionLogs(t *testing.T) {
	source := &fakeSource{logs: []domainhistory.ConnectionLog{
		logAt("1", "host-1", domainhistory.ConnectionConnected, time.Now()),
	}}
	state := NewState(source, nil)
	state.Refresh()
	state.RequestClear()
	state.CancelClear()
	if state.PendingClear() || state.TotalLogs() != 1 {
		t.Fatalf("cancel: pending = %v, logs = %d", state.PendingClear(), state.TotalLogs())
	}
}

// TestClearProblemRemembersTheFamilyThatFailed proves a failed clear reports
// the family it acted on, even after the user switches tabs, and that it does
// not leave the previous successful clear's banner in place.
func TestClearProblemRemembersTheFamilyThatFailed(t *testing.T) {
	source := &fakeSource{
		logs: []domainhistory.ConnectionLog{
			logAt("1", "host-1", domainhistory.ConnectionConnected, time.Now()),
		},
		entries: []domainhistory.Entry{
			entryAt("e1", "", "ls", domainhistory.SourceSession, time.Now()),
		},
	}
	state := NewState(source, nil)
	state.Refresh()
	state.ConfirmClear()
	if state.Cleared() != 1 || state.ClearedTab() != TabConnections {
		t.Fatalf("successful clear: cleared = %d on tab %v", state.Cleared(), state.ClearedTab())
	}

	source.deleteErr = errors.New("disk")
	state.SelectTab(TabHistory)
	state.ConfirmClear()
	if state.Problem() != ProblemClear || state.ProblemTab() != TabHistory {
		t.Fatalf("failed clear: problem = %v on tab %v", state.Problem(), state.ProblemTab())
	}
	if state.Cleared() != 0 {
		t.Fatalf("failed clear kept the stale banner count %d", state.Cleared())
	}

	state.SelectTab(TabConnections)
	if state.ProblemTab() != TabHistory {
		t.Fatalf("the failure was reattributed to %v after switching tabs", state.ProblemTab())
	}
}

func TestHostOptionsQualifyDuplicateLabels(t *testing.T) {
	source := &fakeSource{logs: []domainhistory.ConnectionLog{
		{ID: "1", HostID: "host-1", HostLabel: "web", Hostname: "10.0.0.1",
			Kind: "ssh", Status: domainhistory.ConnectionConnected, StartedAt: time.Now()},
		{ID: "2", HostID: "host-2", HostLabel: "web", Hostname: "10.0.0.2",
			Kind: "ssh", Status: domainhistory.ConnectionConnected, StartedAt: time.Now()},
	}}
	state := NewState(source, nil)
	state.Refresh()
	var labels []string
	for _, option := range state.HostOptions() {
		if option.Key != "" && option.Key != domainhistory.LocalHostKey {
			labels = append(labels, option.Label)
		}
	}
	if len(labels) != 2 || labels[0] != "web (10.0.0.1)" || labels[1] != "web (10.0.0.2)" {
		t.Fatalf("duplicate labels = %v, want both qualified by address", labels)
	}
}

func TestHostOptionsQualifySharedLabelsByAddressThenIdentity(t *testing.T) {
	source := &fakeSource{logs: []domainhistory.ConnectionLog{
		{ID: "1", HostID: "host-1", HostLabel: "web", Hostname: "10.0.0.9",
			Kind: "ssh", Status: domainhistory.ConnectionConnected, StartedAt: time.Now()},
		{ID: "2", HostID: "host-2", HostLabel: "web", Hostname: "10.0.0.9",
			Kind: "ssh", Status: domainhistory.ConnectionConnected, StartedAt: time.Now()},
	}}
	state := NewState(source, nil)
	state.Refresh()
	var labels []string
	for _, option := range state.HostOptions() {
		if option.Key != "" && option.Key != domainhistory.LocalHostKey {
			labels = append(labels, option.Label)
		}
	}
	if len(labels) != 2 || labels[0] != "web (host-1)" || labels[1] != "web (host-2)" {
		t.Fatalf("labels sharing an address = %v, want identity qualifiers", labels)
	}
}

// TestHistoryTabClassifiesUnsavedHostsAndResetsTheFilter proves command rows
// recorded against a typed Host are not local: the Local filter excludes them,
// the unsaved filter finds them, the unsaved option only appears on the tab
// that has such rows, and switching tabs drops that tab-specific filter.
func TestHistoryTabClassifiesUnsavedHostsAndResetsTheFilter(t *testing.T) {
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		{ID: "local", Scope: "local", Command: "ls", Source: domainhistory.SourceSession,
			RecordedAt: now},
		{ID: "unsaved", HostLabel: "deploy@typed.example", UnsavedHost: true,
			Scope: "local", Command: "uptime", Source: domainhistory.SourceSession,
			RecordedAt: now},
		{ID: "saved", HostID: "host-1", HostLabel: "web-1", Scope: "host:host-1",
			Command: "df", Source: domainhistory.SourceSession, RecordedAt: now},
	}}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)

	state.SetHostKey(domainhistory.LocalHostKey)
	if got := state.FilteredEntries(); len(got) != 1 || got[0].ID != "local" {
		t.Fatalf("Local filter matched %+v, want only the local shell", got)
	}
	state.SetHostKey(domainhistory.UnsavedHostKey)
	if got := state.FilteredEntries(); len(got) != 1 || got[0].ID != "unsaved" {
		t.Fatalf("unsaved filter matched %+v, want only the typed Host", got)
	}

	foundUnsaved := false
	for _, option := range state.HostOptions() {
		if option.Key == domainhistory.UnsavedHostKey {
			foundUnsaved = true
		}
	}
	if !foundUnsaved {
		t.Fatalf("history Host options = %+v, want an unsaved entry", state.HostOptions())
	}

	state.SelectTab(TabConnections)
	if state.HostKey() != "" {
		t.Fatalf("switching tabs kept the tab-specific Host filter %q", state.HostKey())
	}
	for _, option := range state.HostOptions() {
		if option.Key == domainhistory.UnsavedHostKey {
			t.Fatal("the Connections tab offered an unsaved option with no unsaved logs")
		}
	}
}

func TestHostOptionsDistinguishLocalAndDeletedHosts(t *testing.T) {
	local := logAt("2", "", domainhistory.ConnectionConnected, time.Now())
	local.Kind = "local"
	source := &fakeSource{
		logs: []domainhistory.ConnectionLog{
			logAt("1", "host-1", domainhistory.ConnectionConnected, time.Now()),
			local,
		},
		entries: []domainhistory.Entry{
			entryAt("e1", "host-2", "ls", domainhistory.SourceSession, time.Now()),
		},
	}
	state := NewState(source, nil)
	state.Refresh()
	options := state.HostOptions()
	if len(options) != 4 {
		t.Fatalf("host options = %+v, want all, local, and two hosts", options)
	}
	if options[0].Key != "" || options[1].Key != domainhistory.LocalHostKey {
		t.Fatalf("host option prefixes = %+v", options[:2])
	}
	if options[2].Label != "host-1" || options[3].Label != "host-e1" {
		t.Fatalf("host labels = %+v", options[2:])
	}
}
