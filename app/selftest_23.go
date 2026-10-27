//go:build selftest

package app

// Ticket 23 self-test. Press F11, then 2, then 3. The check proves connection
// logs and shell/command history through the real SQLite store, the real
// history service, the real Logs view, and the real shell chrome:
//
//   - Recording: successful, failed, and local connect attempts and command
//     executions are recorded with Host, status, error, source, and time.
//   - Filtering: Host, status, source, text, and date ranges narrow both
//     lists; a malformed range reports a localized problem and matches
//     nothing.
//   - Retention: a count limit keeps exactly that many newest records, an age
//     limit removes older records, and a second store handle sees the same
//     rows, proving persistence.
//   - Row action: a history row focuses the related live terminal; a row whose
//     Host was deleted shows the localized missing-Host problem.
//   - Clear-all: Cancel leaves the rows, Confirm removes every connection log
//     and later every command without touching the other family, and a second
//     store handle sees the deletions.
//   - Layout: the production shell, sidebar, and tab strip render the surface
//     at 1100x720, 720x480, and 720x300 without horizontal overflow; a real
//     wheel, PageDown, and End move the surface through its own scroll state,
//     and the last row ends up inside the window.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-23.json. Every store, Session, and
// temporary directory is closed and removed, so F11,2,3 passes twice in one
// process.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	apphistory "github.com/mslxl/natcatty/application/history"
	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	logsui "github.com/mslxl/natcatty/ui/logs"
	"github.com/mslxl/natcatty/ui/selftest"
	shellui "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

const selftest23Ticket = 23

// selftest23LayoutCommands is more than one page (logsui.PageSize), so the
// surface overflows at every layout size.
const selftest23LayoutCommands = 130

type selftest23 struct {
	dir      string
	dbPath   string
	store    *store.Store
	service  *apphistory.Service
	logs     *logsui.State
	shell    *shellstate.State
	sessions *appsession.Service
	factory  *selftest21Factory
	tester   *ui.Tester

	base         time.Time
	clock        int64
	nextID       int
	commandIndex int
	retention    apphistory.Retention
	// openPhase tracks the asynchronous "open a new Session" sub-check across
	// frames instead of blocking the UI thread while the dial completes.
	openPhase    int
	openDeadline time.Time
}

func (t *selftest23) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest23Ticket,
		Name:   "Connection logs and shell/command history",
		Steps: []selftest.Step{
			{Name: "record-and-filter", Run: t.recordStep},
			{Name: "persistence-and-retention", Run: t.retentionStep},
			{Name: "open-related-terminal", Run: t.openStep},
			{Name: "clear-all", Run: t.clearStep},
			{Name: "layout", Run: t.layoutStep},
		},
	}
}

func (t *selftest23) recordStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")
	t.retention = apphistory.Retention{}

	if err := t.recordBaseline(); err != nil {
		return t.fail(err)
	}
	t.logs.Refresh()
	// The shell opened one local Tab in start, and the installed connection
	// hook recorded it; the three baseline attempts add to it.
	if t.logs.TotalLogs() != 4 || t.logs.TotalEntries() != 3 {
		return t.fail(fmt.Errorf("recorded %d logs and %d entries, want 4 and 3",
			t.logs.TotalLogs(), t.logs.TotalEntries()))
	}

	t.logs.SetHostKey("host-2")
	if got := len(t.logs.FilteredLogs()); got != 1 {
		return t.fail(fmt.Errorf("Host filter matched %d logs, want 1", got))
	}
	t.logs.SetHostKey(domainhistory.LocalHostKey)
	if got := len(t.logs.FilteredLogs()); got != 2 {
		return t.fail(fmt.Errorf("local filter matched %d logs, want 2", got))
	}
	t.logs.SetHostKey("")
	t.logs.SetStatus(domainhistory.ConnectionFailed)
	if got := len(t.logs.FilteredLogs()); got != 1 {
		return t.fail(fmt.Errorf("status filter matched %d logs, want 1", got))
	}
	t.logs.SetStatus("")
	t.logs.SetSearch("refused")
	if got := len(t.logs.FilteredLogs()); got != 1 {
		return t.fail(fmt.Errorf("text filter matched %d logs, want 1", got))
	}
	t.logs.SetSearch("")
	t.logs.SetSourceFilter(domainhistory.SourceShellHistory)
	if got := len(t.logs.FilteredEntries()); got != 1 {
		return t.fail(fmt.Errorf("source filter matched %d entries, want 1", got))
	}
	t.logs.SetSourceFilter("")
	t.logs.SetSearch("git")
	if got := len(t.logs.FilteredEntries()); got != 1 {
		return t.fail(fmt.Errorf("command search matched %d entries, want 1", got))
	}
	t.logs.SetSearch("")

	day := t.base.Format("2006-01-02")
	t.logs.SetDateRange(day, day)
	if got := len(t.logs.FilteredLogs()); got != 4 {
		return t.fail(fmt.Errorf("date filter matched %d logs, want 4", got))
	}
	t.logs.SetDateRange("2026-13-40", "")
	if got := len(t.logs.FilteredLogs()); got != 0 || !t.logs.DateProblem() {
		return t.fail(fmt.Errorf("malformed date matched %d logs, problem %v",
			got, t.logs.DateProblem()))
	}
	t.logs.SetDateRange("", "")

	t.shell.ShowLogs()
	t.tester.Frame()
	if !t.tester.HasText(m.LogsTitle()) ||
		!t.tester.HasText(m.LogsTabConnections(t.logs.TotalLogs())) ||
		!t.tester.HasText("web-1") {
		return t.fail(fmt.Errorf("the Logs surface did not render the records: %v",
			t.tester.Texts()))
	}
	if err := selftest23NoOverflow(t.tester, 1100, 720); err != nil {
		return t.fail(err)
	}
	return selftest.Passed("4 logs, 3 commands, five filters",
		"apphistory.Service + store.Store + logsui.View + ui.Tester")
}

func (t *selftest23) retentionStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	// A second handle on the same database proves the rows are persisted, not
	// only held in memory.
	second, err := store.Open(context.Background(), t.dbPath)
	if err != nil {
		return t.fail(fmt.Errorf("reopen the store: %w", err))
	}
	reader := apphistory.NewService(second)
	logs, err := reader.ConnectionLogs(context.Background())
	if err != nil {
		_ = second.Close()
		return t.fail(fmt.Errorf("read logs from the second handle: %w", err))
	}
	entries, err := reader.Entries(context.Background())
	_ = second.Close()
	if err != nil {
		return t.fail(fmt.Errorf("read history from the second handle: %w", err))
	}
	if len(logs) != 4 || len(entries) != 3 {
		return t.fail(fmt.Errorf("reopened store has %d logs and %d entries, want 4 and 3",
			len(logs), len(entries)))
	}

	// Count retention keeps exactly the configured number, never fewer.
	t.retention.History.Count = 2
	if err := t.recordEntries(4); err != nil {
		return t.fail(err)
	}
	if got := len(t.mustEntries()); got != 2 {
		return t.fail(fmt.Errorf("history count limit kept %d entries, want exactly 2", got))
	}
	t.retention.History = domainhistory.Retention{Count: 3}
	if err := t.recordEntries(5); err != nil {
		return t.fail(err)
	}
	if got := len(t.mustEntries()); got != 3 {
		return t.fail(fmt.Errorf("history count limit kept %d entries, want exactly 3", got))
	}

	// Age retention removes an old record and keeps a fresh one.
	t.retention.History = domainhistory.Retention{MaxAge: time.Hour}
	old := domainhistory.Entry{
		SessionID: "", HostID: "", Command: "old-command", Source: domainhistory.SourceSession,
		RecordedAt: t.base.Add(-2 * time.Hour),
	}
	if err := t.service.Record(context.Background(), old); err != nil {
		return t.fail(fmt.Errorf("record an old command: %w", err))
	}
	if err := t.recordEntries(1); err != nil {
		return t.fail(err)
	}
	for _, entry := range t.mustEntries() {
		if entry.Command == "old-command" {
			return t.fail(fmt.Errorf("age retention kept an entry older than one hour"))
		}
	}

	// Log count retention, then log age retention.
	t.retention = apphistory.Retention{Logs: domainhistory.Retention{Count: 4}}
	if err := t.recordLogs(6); err != nil {
		return t.fail(err)
	}
	if got := len(t.mustLogs()); got != 4 {
		return t.fail(fmt.Errorf("log count limit kept %d logs, want exactly 4", got))
	}
	t.retention = apphistory.Retention{Logs: domainhistory.Retention{MaxAge: time.Hour}}
	oldLog := domainhistory.ConnectionLog{
		HostID: "host-old", HostLabel: "old", Hostname: "10.0.0.9",
		Kind: "ssh", Status: domainhistory.ConnectionConnected,
		StartedAt: t.base.Add(-2 * time.Hour), EndedAt: t.base.Add(-2 * time.Hour),
	}
	if err := t.service.RecordConnection(context.Background(), oldLog); err != nil {
		return t.fail(fmt.Errorf("record an old connection: %w", err))
	}
	if err := t.recordLogs(1); err != nil {
		return t.fail(err)
	}
	cutoff := t.base.Add(-time.Hour)
	for _, log := range t.mustLogs() {
		if log.StartedAt.Before(cutoff) {
			return t.fail(fmt.Errorf("age retention kept a log from %v", log.StartedAt))
		}
	}

	// The retained rows survive another handle.
	second, err = store.Open(context.Background(), t.dbPath)
	if err != nil {
		return t.fail(fmt.Errorf("reopen after pruning: %w", err))
	}
	reader = apphistory.NewService(second)
	logs, logsErr := reader.ConnectionLogs(context.Background())
	entries, entriesErr := reader.Entries(context.Background())
	_ = second.Close()
	if logsErr != nil || entriesErr != nil {
		return t.fail(fmt.Errorf("read retained rows: %v / %v", logsErr, entriesErr))
	}
	if len(logs) != len(t.mustLogs()) || len(entries) != len(t.mustEntries()) {
		return t.fail(fmt.Errorf("reopened store has %d/%d rows, want %d/%d",
			len(logs), len(entries), len(t.mustLogs()), len(t.mustEntries())))
	}
	return selftest.Passed("count and age limits prune exactly; rows survive reopen",
		"apphistory.Service + store.Store + domain/history.Prune*")
}

func (t *selftest23) openStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")
	if t.openPhase == 0 {
		t.shell.ShowLogs()
		t.logs.SelectTab(logsui.TabHistory)
		t.logs.Refresh()
		t.tester.Frame()
		if err := t.tester.Click(m.LogsOpenTerminal()); err != nil {
			return t.fail(fmt.Errorf("click Open terminal: %w", err))
		}
		t.tester.Frame()
		if t.shell.LogsVisible() {
			return t.fail(fmt.Errorf("the row action did not reveal the related terminal"))
		}
		if t.shell.TabCount() != 1 {
			return t.fail(fmt.Errorf("tabs = %d, want the live local Tab", t.shell.TabCount()))
		}

		// A record whose Host was deleted reports a localized problem instead
		// of inventing a terminal.
		if err := t.service.RecordConnection(context.Background(), domainhistory.ConnectionLog{
			HostID: "deleted-host", HostLabel: "gone-1", Hostname: "10.0.0.44",
			Kind: "ssh", Status: domainhistory.ConnectionFailed, Error: "host removed",
			StartedAt: t.base, EndedAt: t.base,
		}); err != nil {
			return t.fail(fmt.Errorf("record the deleted-Host attempt: %w", err))
		}
		t.shell.ShowLogs()
		t.logs.SelectTab(logsui.TabConnections)
		t.logs.Refresh()
		t.tester.Frame()
		if err := t.tester.Click(m.LogsOpenTerminal()); err != nil {
			return t.fail(fmt.Errorf("click the deleted-Host row action: %w", err))
		}
		t.tester.Frame()
		if !t.tester.HasText(m.LogsProblemHostMissing()) {
			return t.fail(fmt.Errorf("missing-Host problem is not shown: %v", t.tester.Texts()))
		}

		// A Host record with no live Session dials it instead: the row action
		// opens a new Tab rather than refusing.
		if err := t.service.Record(context.Background(), domainhistory.Entry{
			HostID: "host-new", HostLabel: "new-host", Scope: "host:host-new",
			Command: "uptime", Source: domainhistory.SourceSession,
		}); err != nil {
			return t.fail(fmt.Errorf("record the new-Host command: %w", err))
		}
		t.openPhase = 1
		return selftest.Pending
	}

	if t.openPhase == 1 {
		t.shell.ShowLogs()
		t.logs.SelectTab(logsui.TabHistory)
		t.logs.Refresh()
		t.tester.Frame()
		if err := t.tester.Click(m.LogsOpenTerminal()); err != nil {
			return t.fail(fmt.Errorf("click the new-Host row action: %w", err))
		}
		t.openPhase = 2
		t.openDeadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	}

	// OpenHost dials off the UI thread; apply its outcome on this frame.
	t.shell.Flush()
	t.tester.Frame()
	if t.shell.TabCount() == 2 {
		if t.shell.LogsVisible() {
			return t.fail(fmt.Errorf("the new-Session action did not reveal its Tab"))
		}
		return selftest.Passed(
			"live local Tab focused; deleted Host reported; new Host dialed",
			"shellstate.OpenHistoryTarget + logsui.View + ui.Tester")
	}
	if t.shell.TabCount() > 2 {
		return t.fail(fmt.Errorf("tabs = %d, want 2", t.shell.TabCount()))
	}
	return pending(t.openDeadline, "the new Host Session")
}

func (t *selftest23) clearStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")
	t.retention = apphistory.Retention{}
	if err := t.recordBaseline(); err != nil {
		return t.fail(err)
	}
	t.logs.Refresh()
	logsBefore := t.logs.TotalLogs()
	entriesBefore := t.logs.TotalEntries()
	if logsBefore == 0 || entriesBefore == 0 {
		return t.fail(fmt.Errorf("clear-all has no records to clear: %d logs, %d entries",
			logsBefore, entriesBefore))
	}

	// Cancel leaves the rows in place.
	t.logs.SelectTab(logsui.TabConnections)
	t.logs.RequestClear()
	t.logs.CancelClear()
	if t.logs.PendingClear() || t.logs.TotalLogs() != logsBefore {
		return t.fail(fmt.Errorf("Cancel: pending = %v, logs = %d",
			t.logs.PendingClear(), t.logs.TotalLogs()))
	}

	// Confirm removes every connection log and leaves command history alone.
	t.logs.RequestClear()
	t.logs.ConfirmClear()
	if t.logs.TotalLogs() != 0 || t.logs.Cleared() != logsBefore ||
		t.logs.ClearedTab() != logsui.TabConnections {
		return t.fail(fmt.Errorf("cleared logs: remaining = %d, cleared = %d on tab %v",
			t.logs.TotalLogs(), t.logs.Cleared(), t.logs.ClearedTab()))
	}
	if t.logs.TotalEntries() != entriesBefore {
		return t.fail(fmt.Errorf("clearing connection logs left %d of %d commands",
			t.logs.TotalEntries(), entriesBefore))
	}
	t.shell.ShowLogs()
	t.tester.Frame()
	if !t.tester.HasText(m.LogsConnectionsCleared(logsBefore)) {
		return t.fail(fmt.Errorf("the connection-clear banner is not shown: %v",
			t.tester.Texts()))
	}

	// A second handle proves the deletion reached the store and the commands
	// were not part of it.
	logs, entries, err := t.readBack()
	if err != nil {
		return t.fail(err)
	}
	if logs != 0 || entries != entriesBefore {
		return t.fail(fmt.Errorf("store after clearing logs has %d logs and %d entries, want 0 and %d",
			logs, entries, entriesBefore))
	}

	// The history tab owns the same confirmation for commands.
	t.logs.SelectTab(logsui.TabHistory)
	t.logs.RequestClear()
	t.logs.ConfirmClear()
	if t.logs.TotalEntries() != 0 || t.logs.Cleared() != entriesBefore ||
		t.logs.ClearedTab() != logsui.TabHistory {
		return t.fail(fmt.Errorf("cleared history: remaining = %d, cleared = %d on tab %v",
			t.logs.TotalEntries(), t.logs.Cleared(), t.logs.ClearedTab()))
	}
	t.tester.Frame()
	if !t.tester.HasText(m.LogsHistoryCleared(entriesBefore)) {
		return t.fail(fmt.Errorf("the history-clear banner is not shown: %v",
			t.tester.Texts()))
	}

	logs, entries, err = t.readBack()
	if err != nil {
		return t.fail(err)
	}
	if logs != 0 || entries != 0 {
		return t.fail(fmt.Errorf("store after clearing history has %d logs and %d entries",
			logs, entries))
	}

	t.cleanup()
	return selftest.Passed(
		"Cancel kept rows; Confirm cleared each family only; reopen saw the deletions",
		"logsui.State + apphistory.Service + store.Store")
}

func (t *selftest23) layoutStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")
	t.retention = apphistory.Retention{}
	t.commandIndex = 0
	if err := t.recordEntries(selftest23LayoutCommands); err != nil {
		return t.fail(err)
	}
	for _, size := range []struct{ width, height int }{
		{1100, 720}, {720, 480}, {720, 300},
	} {
		t.shell.ShowLogs()
		t.logs.SelectTab(logsui.TabHistory)
		t.logs.ShowMore()
		t.logs.Refresh()
		scroll := t.logs.Scroll()
		scroll.Y = 0
		tester := ui.NewTester(shellui.View(t.shell), size.width, size.height)
		tester.SetLocale("en")
		tester.Frame()
		if !tester.HasText(m.LogsTitle()) ||
			!tester.HasText(m.LogsTabHistory(t.logs.TotalEntries())) {
			return t.fail(fmt.Errorf("%dx%d: the Logs surface is not rendered: %v",
				size.width, size.height, tester.Texts()))
		}
		if err := selftest23NoOverflow(tester, size.width, size.height); err != nil {
			return t.fail(err)
		}

		// Drive the same input a user does: the pointer over the list, then a
		// wheel, PageDown, and End. Each must move the surface's own offset.
		x, y := float32(size.width)/2, float32(size.height)*3/4
		tester.Move(x, y)
		start := scroll.Y
		tester.Scroll(x, y, 0, 200)
		tester.Frame()
		if scroll.Y <= start {
			return t.fail(fmt.Errorf("%dx%d: the wheel did not scroll (offset %.1f of %.1f)",
				size.width, size.height, scroll.Y, scroll.MaxY))
		}
		afterWheel := scroll.Y
		tester.Key(0, ui.KeyPageDown)
		tester.Frame()
		if scroll.Y <= afterWheel {
			return t.fail(fmt.Errorf("%dx%d: PageDown did not scroll (offset %.1f of %.1f)",
				size.width, size.height, scroll.Y, scroll.MaxY))
		}
		tester.Key(0, ui.KeyEnd)
		tester.Frame()
		if scroll.MaxY <= 0 || scroll.Y < scroll.MaxY-0.5 {
			return t.fail(fmt.Errorf("%dx%d: End stopped at %.1f of %.1f",
				size.width, size.height, scroll.Y, scroll.MaxY))
		}

		last := fmt.Sprintf("layout-command-%02d", selftest23LayoutCommands-1)
		if !tester.HasText(last) {
			return t.fail(fmt.Errorf("%dx%d: %q is not visible after scrolling: %v",
				size.width, size.height, last, tester.Texts()))
		}
		if err := selftest23Visible(tester, last, size.width, size.height); err != nil {
			return t.fail(err)
		}
		if err := selftest23NoOverflow(tester, size.width, size.height); err != nil {
			return t.fail(err)
		}
	}
	t.cleanup()
	return selftest.Passed(
		"1100x720, 720x480, and 720x300: wheel, PageDown, and End move the surface to the last row",
		"shellui.View + logsui.View + ui.Tester")
}

func (t *selftest23) start() error {
	if t.state() {
		return nil
	}
	dir, err := os.MkdirTemp("", "natcatty-selftest-23-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	t.dir = dir
	t.dbPath = filepath.Join(dir, "history.db")
	database, err := store.Open(context.Background(), t.dbPath)
	if err != nil {
		t.cleanup()
		return fmt.Errorf("open store: %w", err)
	}
	t.store = database
	t.base = time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local)
	service := apphistory.NewService(database,
		apphistory.WithRetention(func() apphistory.Retention { return t.retention }),
		apphistory.WithClock(func() time.Time {
			t.clock++
			return t.base.Add(time.Duration(t.clock) * time.Second)
		}),
		apphistory.WithIDGenerator(func() string {
			t.nextID++
			return fmt.Sprintf("rec-%06d", t.nextID)
		}),
	)
	t.service = service

	factory := &selftest21Factory{transports: make(map[*selftest21Transport]bool)}
	sessions := appsession.NewService(factory,
		// The same connection hook the app installs: opening a Session
		// records the attempt without the self-test calling the service
		// directly.
		appsession.WithConnectionHook(func(event appsession.ConnectionEvent) {
			log := connectionLogFromEvent(event)
			if event.HostID == "" {
				// The local Session opened in start uses the wall clock. Pin it
				// to the scenario time so the date-range assertion does not
				// depend on the day the self-test runs. A typed Host has no
				// identity either, and is pinned for the same reason.
				log.StartedAt, log.EndedAt = t.base, t.base
			}
			_ = service.RecordConnection(context.Background(), log)
		}),
	)
	var shell *shellstate.State
	actions := terminalActionsFunc(func(sessionID model.SessionID, hostID model.HostID) error {
		err := shell.OpenHistoryTarget(sessionID, hostID)
		if errors.Is(err, shellstate.ErrHostUnavailable) {
			return logsui.ErrHostMissing
		}
		return err
	})
	logsState := logsui.NewState(service, actions)
	shell = shellstate.NewState(
		shellstate.WithSessions(sessions, func(*appsession.Session) (shellstate.Surface, error) {
			return selftest21Surface{}, nil
		}),
		shellstate.WithLogs(logsui.View(logsState)),
		shellstate.WithWorkspace(nil, func(_ context.Context, id model.HostID) (domainvault.Host, bool) {
			if id == "host-new" {
				return domainvault.Host{
					ID: "host-new", Label: "new-host", Hostname: "10.0.0.50", Port: 22,
				}, true
			}
			return domainvault.Host{}, false
		}),
	)
	if err := shell.NewTab(); err != nil {
		t.cleanup()
		return fmt.Errorf("open the local Session: %w", err)
	}
	shell.ShowLogs()
	tester := ui.NewTester(shellui.View(shell), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	t.sessions, t.factory, t.shell, t.logs, t.tester = sessions, factory, shell, logsState, tester
	return nil
}

// state reports whether the resource graph is already built. It is separate
// from a nil check on integer fields so a partially built run can clean up.
func (t *selftest23) state() bool { return t.shell != nil }

func (t *selftest23) recordBaseline() error {
	logs := []domainhistory.ConnectionLog{
		{
			HostID: "host-1", HostLabel: "web-1", Hostname: "10.0.0.1",
			Username: "deploy", Kind: "ssh", Status: domainhistory.ConnectionConnected,
			StartedAt: t.base, EndedAt: t.base.Add(time.Second),
		},
		{
			HostID: "host-2", HostLabel: "db-1", Hostname: "10.0.0.2",
			Username: "postgres", Kind: "ssh", Status: domainhistory.ConnectionFailed,
			Error: "connection refused", StartedAt: t.base, EndedAt: t.base,
		},
		{
			HostID: "", HostLabel: "", Hostname: "", Kind: "local",
			Status: domainhistory.ConnectionConnected, StartedAt: t.base,
			EndedAt: t.base,
		},
	}
	for _, log := range logs {
		if err := t.service.RecordConnection(context.Background(), log); err != nil {
			return fmt.Errorf("record a connection: %w", err)
		}
	}
	entries := []domainhistory.Entry{
		{
			HostID: "host-1", HostLabel: "web-1", Scope: "host:host-1",
			Command: "git status", Source: domainhistory.SourceSession,
		},
		{
			HostID: "", HostLabel: "", Scope: "local",
			Command: "ls -la", Source: domainhistory.SourceSession,
		},
		{
			HostID: "host-1", HostLabel: "web-1", Scope: "host:host-1",
			Command: "tail -f app.log", Source: domainhistory.SourceShellHistory,
		},
	}
	for _, entry := range entries {
		if err := t.service.Record(context.Background(), entry); err != nil {
			return fmt.Errorf("record a command: %w", err)
		}
	}
	return nil
}

func (t *selftest23) recordLogs(count int) error {
	for index := 0; index < count; index++ {
		if err := t.service.RecordConnection(context.Background(), domainhistory.ConnectionLog{
			HostID: "host-3", HostLabel: "worker", Hostname: "10.0.0.3",
			Kind: "ssh", Status: domainhistory.ConnectionConnected,
			StartedAt: t.base, EndedAt: t.base,
		}); err != nil {
			return fmt.Errorf("record a log: %w", err)
		}
	}
	return nil
}

func (t *selftest23) recordEntries(count int) error {
	for index := 0; index < count; index++ {
		if err := t.service.Record(context.Background(), domainhistory.Entry{
			HostID: "", HostLabel: "", Scope: "local",
			Command: fmt.Sprintf("layout-command-%02d", t.commandIndex),
			Source:  domainhistory.SourceSession,
		}); err != nil {
			return fmt.Errorf("record a command: %w", err)
		}
		t.commandIndex++
	}
	return nil
}

func (t *selftest23) mustEntries() []domainhistory.Entry {
	entries, _ := t.service.Entries(context.Background())
	return entries
}

func (t *selftest23) mustLogs() []domainhistory.ConnectionLog {
	logs, _ := t.service.ConnectionLogs(context.Background())
	return logs
}

// readBack opens a second handle on the same database and counts both record
// families, proving the UI's mutations reached persisted storage.
func (t *selftest23) readBack() (int, int, error) {
	second, err := store.Open(context.Background(), t.dbPath)
	if err != nil {
		return 0, 0, fmt.Errorf("reopen the store: %w", err)
	}
	reader := apphistory.NewService(second)
	logs, logsErr := reader.ConnectionLogs(context.Background())
	entries, entriesErr := reader.Entries(context.Background())
	_ = second.Close()
	if logsErr != nil || entriesErr != nil {
		return 0, 0, fmt.Errorf("read reopened rows: %v / %v", logsErr, entriesErr)
	}
	return len(logs), len(entries), nil
}

func (t *selftest23) fail(err error) selftest.Outcome {
	t.cleanup()
	return selftest.Failed(err)
}

func (t *selftest23) cleanup() {
	if t.shell != nil {
		t.shell.Shutdown()
		t.shell.CloseAllSessions()
	}
	if t.sessions != nil {
		t.sessions.CloseAll()
	}
	if t.factory != nil {
		t.factory.closeAll()
	}
	if t.store != nil {
		_ = t.store.Close()
	}
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
	}
	t.dir, t.dbPath, t.store, t.service = "", "", nil, nil
	t.shell, t.sessions, t.factory, t.logs, t.tester = nil, nil, nil, nil, nil
	t.clock, t.nextID, t.commandIndex = 0, 0, 0
	t.openPhase, t.openDeadline = 0, time.Time{}
	t.retention = apphistory.Retention{}
}

func selftest23Visible(tester *ui.Tester, text string, width, height int) error {
	rect, ok := tester.Find(text)
	if !ok {
		return fmt.Errorf("%q is not rendered", text)
	}
	if rect.Y < -0.5 || rect.Y+rect.H > float32(height)+0.5 ||
		rect.X < -0.5 || rect.X+rect.W > float32(width)+0.5 {
		return fmt.Errorf("%q is outside %dx%d: %+v", text, width, height, rect)
	}
	return nil
}

func selftest23NoOverflow(tester *ui.Tester, width, height int) error {
	for _, text := range tester.Texts() {
		rect, ok := tester.Find(text)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			continue
		}
		if rect.X < -0.5 || rect.X+rect.W > float32(width)+0.5 {
			return fmt.Errorf("%dx%d: %q overflows horizontally: %+v",
				width, height, text, rect)
		}
	}
	return nil
}
