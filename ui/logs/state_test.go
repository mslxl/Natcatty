package logs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

type fakeSource struct {
	logs       []domainhistory.ConnectionLog
	entries    []domainhistory.Entry
	revision   uint64
	listeners  []func()
	deleteErr  error
	entriesErr error
	// clearErr makes ClearThisDevice remove clearRemoved owned rows and then
	// fail, simulating a batched clear that committed part of its work.
	clearErr     error
	clearRemoved int
	cleared      int
	pruned       int
}

func (f *fakeSource) ConnectionLogs(context.Context) ([]domainhistory.ConnectionLog, error) {
	return append([]domainhistory.ConnectionLog(nil), f.logs...), nil
}

func (f *fakeSource) Entries(context.Context) ([]domainhistory.Entry, error) {
	if f.entriesErr != nil {
		return nil, f.entriesErr
	}
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

func (f *fakeSource) ClearThisDevice(context.Context) (int, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	removed := 0
	kept := make([]domainhistory.Entry, 0, len(f.entries))
	for _, entry := range f.entries {
		if entry.IsLocallyOwned() {
			if f.clearErr != nil && removed >= f.clearRemoved {
				kept = append(kept, entry)
				continue
			}
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	f.entries = kept
	f.cleared = removed
	f.markChanged()
	if f.clearErr != nil {
		return removed, f.clearErr
	}
	return removed, nil
}

func (f *fakeSource) PruneHistoryEntries(_ context.Context, ids []string) (int, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	kept := make([]domainhistory.Entry, 0, len(f.entries))
	removed := 0
	for _, entry := range f.entries {
		if wanted[entry.ID] {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	f.entries = kept
	f.pruned = removed
	f.markChanged()
	return removed, nil
}

func (f *fakeSource) PruneHistory(_ context.Context, filter domainhistory.EntryFilter, limit int) (int, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	kept := make([]domainhistory.Entry, 0, len(f.entries))
	matched := make([]domainhistory.Entry, 0, len(f.entries))
	for _, entry := range f.entries {
		if filter.Match(entry) {
			matched = append(matched, entry)
			continue
		}
		kept = append(kept, entry)
	}
	if limit > 0 && len(matched) > limit {
		// The service selects the oldest matches; the fake keeps the newest.
		domainhistory.SortEntries(matched)
		kept = append(kept, matched[:len(matched)-limit]...)
		matched = matched[len(matched)-limit:]
	}
	f.entries = kept
	f.pruned = len(matched)
	f.markChanged()
	return len(matched), nil
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

type fakeCommandActions struct {
	err      error
	commands []string
}

func (f *fakeCommandActions) InsertCommand(command string) error {
	if f.err != nil {
		return f.err
	}
	f.commands = append(f.commands, command)
	return nil
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

	source.entries = append(source.entries, entryAt("atuin", "", "cd /srv",
		domainhistory.SourceAtuinImport, now))
	state.reload()
	state.SetSourceFilter(domainhistory.SourceAtuinImport)
	if got := state.FilteredEntries(); len(got) != 1 || got[0].ID != "atuin" {
		t.Fatalf("Atuin import source filter = %+v", got)
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

// TestClearThisDeviceLeavesImportedHistory proves Clear this device removes
// only locally owned rows; imported and synced remote-origin rows stay.
func TestClearThisDeviceLeavesImportedHistory(t *testing.T) {
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("local", "", "ls", domainhistory.SourceSession, now),
		entryAt("imported", "", "uptime", domainhistory.SourceAtuinImport, now),
		entryAt("synced", "", "df -h", domainhistory.SourceAtuinSync, now),
	}}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	state.RequestClear()
	state.ConfirmClear()

	if state.TotalEntries() != 2 || state.Cleared() != 1 {
		t.Fatalf("entries = %d, cleared = %d; want 2 imported/synced left",
			state.TotalEntries(), state.Cleared())
	}
	for _, entry := range source.entries {
		if entry.Source == domainhistory.SourceSession {
			t.Fatalf("Clear this device removed the local row %+v", entry)
		}
	}
}

// TestClearThisDeviceReportsPartialFailure proves a batched clear that
// committed some rows keeps that count, preserves the error, and still reloads
// and notifies the autocomplete reloader.
func TestClearThisDeviceReportsPartialFailure(t *testing.T) {
	now := time.Now()
	source := &fakeSource{
		clearErr:     errors.New("disk"),
		clearRemoved: 1,
		entries: []domainhistory.Entry{
			entryAt("local-1", "", "ls", domainhistory.SourceSession, now),
			entryAt("local-2", "", "pwd", domainhistory.SourceSession, now),
			entryAt("local-3", "", "whoami", domainhistory.SourceSession, now),
		},
	}
	reloads := 0
	state := NewState(source, nil, WithMutationHook(func() { reloads++ }))
	state.Refresh()
	state.SelectTab(TabHistory)
	state.RequestClear()
	state.ConfirmClear()

	if state.Problem() != ProblemClear {
		t.Fatalf("problem = %v, want ProblemClear", state.Problem())
	}
	if state.Cleared() != 1 {
		t.Fatalf("cleared = %d, want the committed row", state.Cleared())
	}
	if state.TotalEntries() != 2 {
		t.Fatalf("entries = %d, want the two rows left by the failure", state.TotalEntries())
	}
	if reloads != 1 {
		t.Fatalf("autocomplete reloads = %d, want a reload after partial clear", reloads)
	}
}

func TestPrunePreviewAndConfirmOnlyMatchedRows(t *testing.T) {
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("git-1", "", "git status", domainhistory.SourceSession, now),
		entryAt("git-2", "", "git log", domainhistory.SourceSession, now.Add(-time.Hour)),
		entryAt("make", "", "make test", domainhistory.SourceSession, now.Add(-2*time.Hour)),
	}}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	state.SetSearch("git")
	state.RequestPrune()

	if !state.PendingPrune() {
		t.Fatal("the prune dialog did not open")
	}
	if got := state.PrunePreview(); got != 2 {
		t.Fatalf("preview = %d, want the two git rows", got)
	}
	state.ConfirmPrune()
	if state.PendingPrune() || state.Problem() != ProblemNone {
		t.Fatalf("pending = %v, problem = %v", state.PendingPrune(), state.Problem())
	}
	if state.Pruned() != 2 || state.TotalEntries() != 1 {
		t.Fatalf("pruned = %d, entries = %d; want only make test left",
			state.Pruned(), state.TotalEntries())
	}
	if source.entries[0].Command != "make test" {
		t.Fatalf("remaining row = %+v, want make test", source.entries[0])
	}
}

// TestPruneAppliesExactlyThePreviewedRows pins the race fix: a matching row
// that arrives after the preview is not swept in by the confirm.
func TestPruneAppliesExactlyThePreviewedRows(t *testing.T) {
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("match-1", "", "match status", domainhistory.SourceSession, now),
		entryAt("match-2", "", "match log", domainhistory.SourceSession, now.Add(-time.Hour)),
	}}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	state.SetSearch("match")
	state.RequestPrune()
	if got := state.PrunePreview(); got != 2 {
		t.Fatalf("preview = %d, want the two matched rows", got)
	}

	// A new matching command lands between preview and confirm.
	source.entries = append(source.entries, entryAt(
		"late", "", "match late", domainhistory.SourceSession, now.Add(time.Minute)))

	state.ConfirmPrune()
	if state.Pruned() != 2 {
		t.Fatalf("pruned = %d, want exactly the previewed rows", state.Pruned())
	}
	if len(source.entries) != 1 || source.entries[0].ID != "late" {
		t.Fatalf("remaining rows = %+v, want the late row untouched", source.entries)
	}
}

// TestPruneConfirmsThePreviewSnapshotAcrossARefresh pins the row-ID fix: the
// surface's per-frame Refresh can load a new matching row while the dialog is
// open, so ConfirmPrune must delete the row IDs the preview was requested with
// rather than recomputing from the reloaded list. The live preview count still
// reflects the refreshed list; only the confirmed set is frozen.
func TestPruneConfirmsThePreviewSnapshotAcrossARefresh(t *testing.T) {
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("match-1", "", "match status", domainhistory.SourceSession, now),
		entryAt("match-2", "", "match log", domainhistory.SourceSession, now.Add(-time.Hour)),
	}}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	state.SetSearch("match")
	state.RequestPrune()
	if got := state.PrunePreview(); got != 2 {
		t.Fatalf("preview = %d, want 2", got)
	}

	// A matching command lands and the frame Refresh loads it before the click.
	source.entries = append(source.entries, entryAt(
		"late", "", "match late", domainhistory.SourceSession, now.Add(time.Minute)))
	source.markChanged()
	state.Refresh()
	if got := state.PrunePreview(); got != 3 {
		t.Fatalf("refreshed preview = %d, want the live count 3", got)
	}

	state.ConfirmPrune()
	if state.Pruned() != 2 {
		t.Fatalf("pruned = %d; the confirm swept in a row the preview never showed", state.Pruned())
	}
	if len(source.entries) != 1 || source.entries[0].ID != "late" {
		t.Fatalf("remaining rows = %+v; want the late row untouched", source.entries)
	}
}

// TestPruneConfirmUsesAnEditedDraft proves the snapshot is not stale after the
// user edits the dialog: a changed draft freezes a new set from the current
// list, while an unchanged draft keeps the old snapshot.
func TestPruneConfirmUsesAnEditedDraft(t *testing.T) {
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("match-1", "", "match status", domainhistory.SourceSession, now),
		entryAt("match-2", "", "match log", domainhistory.SourceSession, now.Add(-time.Hour)),
	}}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	state.SetSearch("match")
	state.RequestPrune()

	state.PruneFilter().Query = "log"
	if got := state.PrunePreview(); got != 1 {
		t.Fatalf("edited preview = %d, want 1", got)
	}
	state.ConfirmPrune()
	if state.Pruned() != 1 {
		t.Fatalf("pruned = %d, want the edited draft's one row", state.Pruned())
	}
	if len(source.entries) != 1 || source.entries[0].ID != "match-1" {
		t.Fatalf("remaining rows = %+v; want match status untouched", source.entries)
	}
}

// TestPruneEditFreezesTheSetBeforeALaterRefresh proves an edited draft freezes
// the rows its preview counted. A matching row loaded by a later Refresh must
// still survive, exactly like the original preview's rows do.
func TestPruneEditFreezesTheSetBeforeALaterRefresh(t *testing.T) {
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("match-1", "", "match status", domainhistory.SourceSession, now),
		entryAt("match-2", "", "match log", domainhistory.SourceSession, now.Add(-time.Hour)),
	}}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	state.SetSearch("match")
	state.RequestPrune()

	// The user narrows the draft; the preview recomputes and freezes one row.
	state.PruneFilter().Query = "status"
	if got := state.PrunePreview(); got != 1 {
		t.Fatalf("edited preview = %d, want 1", got)
	}

	// A matching command lands after the edited preview was displayed.
	source.entries = append(source.entries, entryAt(
		"late", "", "status late", domainhistory.SourceSession, now.Add(time.Minute)))
	source.markChanged()
	state.Refresh()
	if got := state.PrunePreview(); got != 2 {
		t.Fatalf("refreshed preview = %d, want the live count 2", got)
	}

	state.ConfirmPrune()
	if state.Pruned() != 1 {
		t.Fatalf("pruned = %d; the confirm used a set later than the edited preview", state.Pruned())
	}
	if len(source.entries) != 2 || source.entries[0].ID != "match-2" ||
		source.entries[1].ID != "late" {
		t.Fatalf("remaining rows = %+v; want the edited preview's row only", source.entries)
	}
}

func TestPruneLimitSelectsTheOldestMatches(t *testing.T) {
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("newest", "", "deploy", domainhistory.SourceSession, now),
		entryAt("middle", "", "deploy", domainhistory.SourceSession, now.Add(-time.Hour)),
		entryAt("oldest", "", "deploy", domainhistory.SourceSession, now.Add(-2*time.Hour)),
	}}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	state.RequestPrune()
	*state.PruneLimit() = "2"
	if got := state.PrunePreview(); got != 2 {
		t.Fatalf("preview = %d, want the limit", got)
	}
	state.ConfirmPrune()
	if state.Pruned() != 2 || state.TotalEntries() != 1 {
		t.Fatalf("pruned = %d, entries = %d", state.Pruned(), state.TotalEntries())
	}
	if len(source.entries) != 1 || source.entries[0].ID != "newest" {
		t.Fatalf("remaining rows = %+v, want the newest", source.entries)
	}
}

func TestPruneInvalidLimitKeepsTheDialogOpen(t *testing.T) {
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("one", "", "ls", domainhistory.SourceSession, now),
	}}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	state.RequestPrune()
	*state.PruneLimit() = "many"
	if !state.PruneLimitProblem() {
		t.Fatal("a malformed limit was accepted")
	}
	if got := state.PrunePreview(); got != 0 {
		t.Fatalf("preview with a malformed limit = %d, want 0", got)
	}
	state.ConfirmPrune()
	if !state.PendingPrune() || state.TotalEntries() != 1 {
		t.Fatalf("pending = %v, entries = %d; want no prune",
			state.PendingPrune(), state.TotalEntries())
	}
}

func TestInsertEntryIsExplicitAndReportsFailure(t *testing.T) {
	commands := &fakeCommandActions{}
	state := NewState(&fakeSource{}, nil, WithCommandActions(commands))
	state.InsertEntry("git status")
	if len(commands.commands) != 1 || commands.commands[0] != "git status" {
		t.Fatalf("inserted = %+v, want git status", commands.commands)
	}
	if state.OpenProblem() != ProblemNone {
		t.Fatalf("insert problem = %v", state.OpenProblem())
	}

	commands.err = errors.New("no session")
	state.InsertEntry("git stash")
	if state.OpenProblem() != ProblemInsert {
		t.Fatalf("insert problem = %v, want ProblemInsert", state.OpenProblem())
	}

	// A refused multi-line insert has its own explanation, not the generic
	// missing-Session one.
	commands.err = ErrInsertUnsafe
	state.InsertEntry("touch marker\necho SHOULD_NOT_RUN")
	if state.OpenProblem() != ProblemInsertUnsafe {
		t.Fatalf("unsafe insert problem = %v, want ProblemInsertUnsafe", state.OpenProblem())
	}

	// A surface without the action seam never fakes success.
	bare := NewState(&fakeSource{}, nil)
	bare.InsertEntry("ls")
	if bare.OpenProblem() != ProblemInsert {
		t.Fatalf("bare insert problem = %v, want ProblemInsert", bare.OpenProblem())
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

// TestDeleteRowConfirmationGatesTheStore proves the History row action opens a
// confirmation first and that switching tabs discards it.
func TestDeleteRowConfirmationGatesTheStore(t *testing.T) {
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("e1", "", "ls", domainhistory.SourceSession, time.Now()),
	}}
	state := NewState(source, nil)

	state.RequestDeleteEntry("e1")
	if state.PendingDeleteEntry() != "e1" {
		t.Fatalf("pending delete = %q, want e1", state.PendingDeleteEntry())
	}
	if len(source.entries) != 1 {
		t.Fatal("requesting a delete removed the row")
	}
	state.SelectTab(TabHistory)
	if state.PendingDeleteEntry() != "" {
		t.Fatalf("pending delete = %q after switching tabs", state.PendingDeleteEntry())
	}

	state.RequestDeleteEntry("e1")
	state.ConfirmDeleteEntry()
	if state.PendingDeleteEntry() != "" {
		t.Fatalf("pending delete = %q after confirmation", state.PendingDeleteEntry())
	}
	if len(source.entries) != 0 {
		t.Fatalf("entries = %+v, want the confirmed row removed", source.entries)
	}
}

// TestHistoryMutationsNotifyTheAutocompleteReloader pins the seam that makes
// deleting or clearing a Command History row remove its suggestion: the
// composition root installs WithMutationHook to reload the shared journal.
func TestHistoryMutationsNotifyTheAutocompleteReloader(t *testing.T) {
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("e1", "", "ls", domainhistory.SourceSession, time.Now()),
	}}
	reloads := 0
	state := NewState(source, nil, WithMutationHook(func() { reloads++ }))

	state.DeleteEntry("e1")
	if reloads != 1 {
		t.Fatalf("delete reloaded autocomplete %d times, want 1", reloads)
	}
	state.SelectTab(TabHistory)
	state.RequestClear()
	state.ConfirmClear()
	if reloads != 2 {
		t.Fatalf("clear reloaded autocomplete %d times, want 2", reloads)
	}
}

// facetEntries builds one deterministic result set for the facet and filter
// tests: two Hosts, a remote origin, equal timestamps, and every exit status.
func facetEntries() []domainhistory.Entry {
	recorded := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	return []domainhistory.Entry{
		{ID: "a", SessionID: "session-1", SessionUUID: "uuid-1", HostID: "host-1",
			HostLabel: "web-1", Scope: "host:host-1", Command: "git status",
			CWD: "/srv/app", Shell: "zsh", Origin: "web-1:deploy",
			RecordedAt: recorded, ExitCode: 0, HasExitCode: true,
			Source: domainhistory.SourceSession, CaptureTier: 2},
		{ID: "b", SessionID: "session-2", SessionUUID: "uuid-2", HostID: "host-1",
			HostLabel: "web-1", Scope: "host:host-1", Command: "make test",
			CWD: "/srv/app", Shell: "bash", Origin: "web-1:deploy",
			RecordedAt: recorded, ExitCode: 2, HasExitCode: true,
			Source: domainhistory.SourceSession, CaptureTier: 1},
		{ID: "c", SessionID: "session-3", SessionUUID: "uuid-3", HostID: "host-2",
			HostLabel: "db-1", Scope: "host:host-2", Command: "kubectl get pods",
			CWD: "/var/log", Shell: "fish", Origin: "db-1:admin",
			RecordedAt: recorded.Add(-time.Minute), Source: domainhistory.SourceSession},
		{ID: "d", SessionUUID: "uuid-remote", Scope: "remote:remote:deploy",
			Command: "remote-cmd", CWD: "/root", Shell: "zsh",
			Origin: "remote:deploy", RecordedAt: recorded.Add(-2 * time.Minute),
			Source: domainhistory.SourceAtuinSync},
		{ID: "e", Scope: "local", Command: "ls", Source: domainhistory.SourceShellHistory,
			RecordedAt: recorded.Add(-3 * time.Minute)},
	}
}

func TestHistoryFiltersCoverEveryDimensionTogether(t *testing.T) {
	state := NewState(&fakeSource{entries: facetEntries()}, nil)
	state.Refresh()
	state.SelectTab(TabHistory)

	// Equal RecordedAt values tie-break by descending identity, so the
	// ordering does not depend on the Store's row order.
	if got := entryIDs(state.FilteredEntries()); fmt.Sprint(got) != "[b a c d e]" {
		t.Fatalf("unfiltered = %v", got)
	}
	state.SetSessionFilter("session-2")
	if got := entryIDs(state.FilteredEntries()); fmt.Sprint(got) != "[b]" {
		t.Fatalf("session filter = %v", got)
	}
	state.SetSessionFilter("uuid-remote")
	if got := entryIDs(state.FilteredEntries()); fmt.Sprint(got) != "[d]" {
		t.Fatalf("session UUID filter = %v", got)
	}
	state.SetSessionFilter("")
	state.SetCwdFilter("/srv/app")
	if got := entryIDs(state.FilteredEntries()); fmt.Sprint(got) != "[b a]" {
		t.Fatalf("cwd filter = %v", got)
	}
	state.SetCwdFilter("")
	state.SetExitFilter(domainhistory.ExitUnknown)
	if got := entryIDs(state.FilteredEntries()); fmt.Sprint(got) != "[c d e]" {
		t.Fatalf("unknown exit filter = %v", got)
	}
	state.SetExitFilter(domainhistory.ExitFailure)
	if got := entryIDs(state.FilteredEntries()); fmt.Sprint(got) != "[b]" {
		t.Fatalf("failure filter = %v", got)
	}
	state.SetExitFilter("")
	state.SetShellFilter("ZSH")
	if got := entryIDs(state.FilteredEntries()); fmt.Sprint(got) != "[a d]" {
		t.Fatalf("shell filter = %v", got)
	}
	state.SetShellFilter("")
	state.SetHostKey(domainhistory.OriginHostKey("remote:deploy"))
	if got := entryIDs(state.FilteredEntries()); fmt.Sprint(got) != "[d]" {
		t.Fatalf("origin filter = %v", got)
	}
	state.SetHostKey("host-1")
	state.SetSourceFilter(domainhistory.SourceSession)
	state.SetExitFilter(domainhistory.ExitFailure)
	if got := entryIDs(state.FilteredEntries()); fmt.Sprint(got) != "[b]" {
		t.Fatalf("combined filter = %v", got)
	}
}

func entryIDs(entries []domainhistory.Entry) []string {
	out := make([]string, len(entries))
	for index, entry := range entries {
		out[index] = entry.ID
	}
	return out
}

func TestFacetsCountTheCurrentResultSet(t *testing.T) {
	state := NewState(&fakeSource{entries: facetEntries()}, nil)
	state.Refresh()
	state.SelectTab(TabHistory)

	facets := state.Facets()
	if counts := facetCounts(facets.Sessions); counts["session-1"] != 1 ||
		counts["uuid-remote"] != 1 {
		t.Fatalf("session facets = %v", facets.Sessions)
	}
	if counts := facetCounts(facets.Cwds); counts["/srv/app"] != 2 ||
		counts["/var/log"] != 1 {
		t.Fatalf("cwd facets = %v", facets.Cwds)
	}
	if counts := facetCounts(facets.Shells); counts["zsh"] != 2 || counts["fish"] != 1 {
		t.Fatalf("shell facets = %v", facets.Shells)
	}
	if counts := facetCounts(facets.Exits); counts[string(domainhistory.ExitSuccess)] != 1 ||
		counts[string(domainhistory.ExitFailure)] != 1 ||
		counts[string(domainhistory.ExitUnknown)] != 3 {
		t.Fatalf("exit facets = %v", facets.Exits)
	}
	if counts := facetCounts(facets.Sources); counts[domainhistory.SourceAtuinSync] != 1 ||
		counts[domainhistory.SourceShellHistory] != 1 {
		t.Fatalf("source facets = %v", facets.Sources)
	}
	wantOrigin := domainhistory.OriginHostKey("remote:deploy")
	foundOrigin := false
	for _, host := range facets.Hosts {
		if host.Key == wantOrigin && host.Count == 1 {
			foundOrigin = true
		}
	}
	if !foundOrigin {
		t.Fatalf("host facets = %+v, want origin key %q", facets.Hosts, wantOrigin)
	}

	// Filtering by cwd narrows the other dimensions but keeps the cwd facet
	// itself complete, so another directory stays selectable.
	state.SetCwdFilter("/var/log")
	facets = state.Facets()
	if counts := facetCounts(facets.Sessions); counts["session-3"] != 1 || len(counts) != 1 {
		t.Fatalf("filtered session facets = %v", facets.Sessions)
	}
	if counts := facetCounts(facets.Cwds); counts["/srv/app"] != 2 || counts["/var/log"] != 1 {
		t.Fatalf("filtered cwd facets = %v", facets.Cwds)
	}
}

func facetCounts(values []FacetValue) map[string]int {
	counts := make(map[string]int, len(values))
	for _, value := range values {
		counts[value.Value] = value.Count
	}
	return counts
}

// TestHistoryPagingIsStableWithDeterministicOrder proves the visible page is
// the same slice of a deterministic ordering when the source returns rows out
// of order, and that the next page continues without duplicates.
func TestHistoryPagingIsStableWithDeterministicOrder(t *testing.T) {
	now := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	source := &fakeSource{}
	for index := 0; index < PageSize+3; index++ {
		// Equal timestamps force the identity tie-break; alternating insertion
		// order models a Store that returned rows in another order.
		id := fmt.Sprintf("entry-%03d", index)
		source.entries = append([]domainhistory.Entry{{
			ID: id, Scope: "local", Command: "cmd " + id,
			Source: domainhistory.SourceSession, RecordedAt: now,
		}}, source.entries...)
	}
	state := NewState(source, nil)
	state.Refresh()
	state.SelectTab(TabHistory)

	first := entryIDs(state.VisibleEntries())
	if len(first) != PageSize {
		t.Fatalf("first page = %d rows", len(first))
	}
	// Newest-first at equal timestamps means descending IDs.
	if first[0] != "entry-102" || first[len(first)-1] != "entry-003" {
		t.Fatalf("first page runs %s..%s", first[0], first[len(first)-1])
	}
	state.ShowMore()
	second := entryIDs(state.VisibleEntries())
	if len(second) != PageSize+3 {
		t.Fatalf("second page = %d rows", len(second))
	}
	seen := make(map[string]bool, len(second))
	for _, id := range second {
		if seen[id] {
			t.Fatalf("paging repeated %q", id)
		}
		seen[id] = true
	}
	if second[len(second)-1] != "entry-000" {
		t.Fatalf("last row = %q", second[len(second)-1])
	}
}

// TestHistorySearchRanksBestMatchFirst proves the surface applies the same
// relevance ordering as the domain: an exact command match outranks a prefix,
// a word prefix, and a substring even when the weaker match is newer.
func TestHistorySearchRanksBestMatchFirst(t *testing.T) {
	now := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	state := NewState(&fakeSource{entries: []domainhistory.Entry{
		{ID: "substring", Scope: "local", Command: "legit",
			Source: domainhistory.SourceSession, RecordedAt: now},
		{ID: "word", Scope: "local", Command: "run git",
			Source: domainhistory.SourceSession, RecordedAt: now.Add(-time.Minute)},
		{ID: "prefix", Scope: "local", Command: "git status",
			Source: domainhistory.SourceSession, RecordedAt: now.Add(-2 * time.Minute)},
		{ID: "exact", Scope: "local", Command: "git",
			Source: domainhistory.SourceSession, RecordedAt: now.Add(-3 * time.Minute)},
	}}, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	state.SetSearch("git")
	if got := fmt.Sprint(entryIDs(state.FilteredEntries())); got != "[exact prefix word substring]" {
		t.Fatalf("search order = %s", got)
	}
}

// TestOriginFilterOptionsAreHistoryOnly proves the Connections tab cannot
// offer an origin value that matches no connection log.
func TestOriginFilterOptionsAreHistoryOnly(t *testing.T) {
	state := NewState(&fakeSource{entries: facetEntries()}, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	originKey := domainhistory.OriginHostKey("remote:deploy")
	if !hostOptionPresent(state.HostOptions(), originKey) {
		t.Fatalf("history Host options = %+v, want %q", state.HostOptions(), originKey)
	}
	state.SelectTab(TabConnections)
	if hostOptionPresent(state.HostOptions(), originKey) {
		t.Fatalf("connections Host options = %+v, want no origin keys", state.HostOptions())
	}
}

func TestOpenGlobalSearchSelectsHistoryAndClearsHostScope(t *testing.T) {
	state := NewState(&fakeSource{entries: facetEntries()}, nil)
	state.Refresh()
	state.SelectTab(TabConnections)
	state.SetHostKey("host-1")
	state.SetHistoryMode(HistoryModeStatistics)

	state.OpenGlobalSearch()
	if state.Tab() != TabHistory {
		t.Fatalf("tab = %v, want History", state.Tab())
	}
	if state.HistoryMode() != HistoryModeCommands {
		t.Fatalf("mode = %v, want the command list", state.HistoryMode())
	}
	if state.HostKey() != "" {
		t.Fatalf("Host scope = %q, want global", state.HostKey())
	}
	if state.focusRequest != focusHistorySearch {
		t.Fatalf("focus request = %v, want the search field", state.focusRequest)
	}
}

func TestRefreshReportsAHistoryLoadError(t *testing.T) {
	state := NewState(&fakeSource{entriesErr: errors.New("disk")}, nil)
	state.Refresh()
	if state.Problem() != ProblemLoad {
		t.Fatalf("problem = %v, want ProblemLoad", state.Problem())
	}
}

// statisticsSource is the History result set used by the summary tests: two
// saved Hosts, two working directories, one failure, and one row whose finish
// time the shell never reported.
func statisticsSource() *fakeSource {
	recorded := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	return &fakeSource{entries: []domainhistory.Entry{
		{ID: "a", HostID: "host-1", HostLabel: "web-1", Scope: "host:host-1",
			Command: "git status", CWD: "/srv/app", RecordedAt: recorded,
			FinishedAt: recorded.Add(time.Second), Duration: time.Second,
			ExitCode: 0, HasExitCode: true, Source: domainhistory.SourceSession},
		{ID: "b", HostID: "host-1", HostLabel: "web-1", Scope: "host:host-1",
			Command: "make test", CWD: "/srv/app", RecordedAt: recorded.Add(-time.Minute),
			FinishedAt: recorded.Add(-time.Minute + 3*time.Second),
			Duration:   3 * time.Second, ExitCode: 2, HasExitCode: true,
			Source: domainhistory.SourceSession},
		{ID: "c", HostID: "host-2", HostLabel: "db-1", Scope: "host:host-2",
			Command: "ls", CWD: "/var/log", RecordedAt: recorded.Add(-2 * time.Minute),
			Source: domainhistory.SourceSession},
	}}
}

// TestHistoryStatisticsFollowTheActiveFilters proves the summary is computed
// from the same result set as the rows: every filter changes it, and the mode
// selector is a separate view rather than another filter.
func TestHistoryStatisticsFollowTheActiveFilters(t *testing.T) {
	state := NewState(statisticsSource(), nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	if state.HistoryMode() != HistoryModeCommands {
		t.Fatalf("default mode = %v, want commands", state.HistoryMode())
	}
	state.SetHistoryMode(HistoryModeStatistics)
	if state.HistoryMode() != HistoryModeStatistics {
		t.Fatalf("mode = %v, want statistics", state.HistoryMode())
	}

	stats := state.Statistics()
	if stats.Total != 3 || stats.Unique != 3 {
		t.Fatalf("unfiltered total = %d, unique = %d; want 3 and 3",
			stats.Total, stats.Unique)
	}
	if stats.Failures != 1 || stats.KnownExits != 2 || stats.FailureRate != 0.5 {
		t.Fatalf("unfiltered failures = %d/%d (%v), want 1/2 (0.5)",
			stats.Failures, stats.KnownExits, stats.FailureRate)
	}
	if stats.AverageDuration != 2*time.Second ||
		stats.DurationsKnown != 2 || stats.DurationsUnknown != 1 {
		t.Fatalf("unfiltered durations = %v (%d known, %d unknown)",
			stats.AverageDuration, stats.DurationsKnown, stats.DurationsUnknown)
	}

	state.SetExitFilter(domainhistory.ExitFailure)
	if stats = state.Statistics(); stats.Total != 1 || stats.Failures != 1 {
		t.Fatalf("failure-filtered statistics = %+v", stats)
	}
	state.SetExitFilter(domainhistory.ExitAny)

	state.SetCwdFilter("/srv/app")
	if stats = state.Statistics(); stats.Total != 2 || stats.Unique != 2 {
		t.Fatalf("cwd-filtered statistics = %+v", stats)
	}
	state.SetCwdFilter("")

	state.SetSearch("no-match")
	if stats = state.Statistics(); stats.Total != 0 || stats.Unique != 0 {
		t.Fatalf("empty statistics = %+v", stats)
	}
}

func TestOriginFilterResetsOnConnections(t *testing.T) {
	state := NewState(&fakeSource{entries: facetEntries()}, nil)
	state.Refresh()
	state.SelectTab(TabHistory)
	state.SetHostKey(domainhistory.OriginHostKey("remote:deploy"))
	state.SelectTab(TabConnections)
	if state.HostKey() != "" {
		t.Fatalf("origin filter survived the tab switch: %q", state.HostKey())
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
