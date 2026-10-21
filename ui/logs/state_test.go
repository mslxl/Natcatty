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
	source := &fakeSource{
		logs: []domainhistory.ConnectionLog{
			logAt("1", "host-1", domainhistory.ConnectionConnected, now.Add(-time.Hour)),
			logAt("2", "host-2", domainhistory.ConnectionFailed, now.Add(-48*time.Hour)),
			logAt("3", "", domainhistory.ConnectionConnected, now.Add(-2*time.Hour)),
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

func TestHostOptionsDistinguishLocalAndDeletedHosts(t *testing.T) {
	source := &fakeSource{
		logs: []domainhistory.ConnectionLog{
			logAt("1", "host-1", domainhistory.ConnectionConnected, time.Now()),
			logAt("2", "", domainhistory.ConnectionConnected, time.Now()),
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
