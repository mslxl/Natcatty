package logs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/locales"
	shellui "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

// logsTransport keeps the fake Session alive so the shell renders its
// production work Tab strip above the Logs surface.
type logsTransport struct {
	closed chan struct{}
	once   sync.Once
}

func newLogsTransport() *logsTransport { return &logsTransport{closed: make(chan struct{})} }

func (t *logsTransport) Read([]byte) (int, error) {
	<-t.closed
	return 0, io.EOF
}

func (t *logsTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *logsTransport) Resize(int, int) error       { return nil }
func (t *logsTransport) Wait() (int, error)          { return 0, nil }

func (t *logsTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

type logsFactory struct{}

func (logsFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return newLogsTransport(), nil
}

type logsSurface struct{}

func (logsSurface) View(c *ui.Context) ui.Element { return ui.Text(c, "terminal") }
func (logsSurface) Close()                        {}

func seededSource() *fakeSource {
	now := time.Now()
	source := &fakeSource{}
	for index := 0; index < 30; index++ {
		status := domainhistory.ConnectionConnected
		if index%3 == 0 {
			status = domainhistory.ConnectionFailed
		}
		source.logs = append(source.logs, domainhistory.ConnectionLog{
			ID:        fmt.Sprintf("log-%02d", index),
			SessionID: "session-1",
			HostID:    "host-1",
			HostLabel: fmt.Sprintf("web-%02d", index),
			Hostname:  fmt.Sprintf("10.0.0.%d", index+1),
			Username:  "deploy",
			Kind:      "ssh",
			Status:    status,
			Error:     map[bool]string{true: "connection refused", false: ""}[status == domainhistory.ConnectionFailed],
			StartedAt: now.Add(-time.Duration(index) * time.Minute),
		})
		source.entries = append(source.entries, domainhistory.Entry{
			ID:         fmt.Sprintf("entry-%02d", index),
			SessionID:  "session-1",
			HostID:     "host-1",
			HostLabel:  "web-01",
			Scope:      "host:host-1",
			Command:    fmt.Sprintf("command-%02d --run", index),
			Source:     domainhistory.SourceSession,
			RecordedAt: now.Add(-time.Duration(index) * time.Minute),
		})
	}
	return source
}

// seededLogSource records count connection attempts, enough to exercise
// paging and the scroll path a real wheel and keyboard use.
func seededLogSource(count int) *fakeSource {
	now := time.Now()
	source := &fakeSource{}
	for index := 0; index < count; index++ {
		source.logs = append(source.logs, domainhistory.ConnectionLog{
			ID:        fmt.Sprintf("log-%03d", index),
			SessionID: "session-1",
			HostID:    "host-1",
			HostLabel: fmt.Sprintf("web-%03d", index),
			Hostname:  fmt.Sprintf("10.1.0.%d", index+1),
			Username:  "deploy",
			Kind:      "ssh",
			Status:    domainhistory.ConnectionConnected,
			StartedAt: now.Add(-time.Duration(index) * time.Minute),
		})
	}
	return source
}

// newProductionLogsView builds the real shell, opens one work Tab so the tab
// strip renders, and shows Logs. This is the chrome the production window
// gives the surface, not a bare component.
func newProductionLogsView(t *testing.T, width, height int,
	source *fakeSource, actions TerminalActions) (*ui.Tester, *State) {
	t.Helper()
	state := NewState(source, actions)
	sessions := appsession.NewService(logsFactory{})
	t.Cleanup(sessions.CloseAll)
	shell := shellstate.NewState(
		shellstate.WithSessions(sessions, func(*appsession.Session) (shellstate.Surface, error) {
			return logsSurface{}, nil
		}),
		shellstate.WithLogs(View(state)),
	)
	if err := shell.NewTab(); err != nil {
		t.Fatalf("open a work Tab for the production chrome: %v", err)
	}
	shell.ShowLogs()
	tt := ui.NewTester(shellui.View(shell), width, height)
	tt.SetLocale("en")
	tt.Frame()
	return tt, state
}

func scrollToEnd(tt *ui.Tester, width, height int) {
	// Scroll below the filter fields so the wheel lands on the record list.
	// The window's center can sit on an input at the shorter size.
	x, y := float32(width)/2, float32(height)-80
	tt.Move(x, y)
	tt.Scroll(x, y, 0, 100000)
	tt.Frame()
}

func assertVisible(t *testing.T, tt *ui.Tester, text string, width, height int) {
	t.Helper()
	rect, ok := tt.Find(text)
	if !ok {
		t.Fatalf("%q is not rendered, texts %q", text, tt.Texts())
	}
	if rect.Y < -0.5 || rect.Y+rect.H > float32(height)+0.5 ||
		rect.X < -0.5 || rect.X+rect.W > float32(width)+0.5 {
		t.Fatalf("%q is outside the %dx%d window: %+v", text, width, height, rect)
	}
}

func assertNoHorizontalOverflow(t *testing.T, tt *ui.Tester, width, height int) {
	t.Helper()
	for _, text := range tt.Texts() {
		rect, ok := tt.Find(text)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			continue
		}
		if rect.X < -0.5 || rect.X+rect.W > float32(width)+0.5 {
			t.Fatalf("%dx%d: %q overflows horizontally: %+v", width, height, text, rect)
		}
	}
}

var logsSizes = []struct{ width, height int }{{1100, 720}, {720, 480}}

func TestLogsSurfaceIsReadableAndScrollableAtBothSizes(t *testing.T) {
	m := locales.In("en")
	for _, size := range logsSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			tt, _ := newProductionLogsView(t, size.width, size.height,
				seededSource(), &fakeActions{})

			assertVisible(t, tt, m.LogsTitle(), size.width, size.height)
			assertVisible(t, tt, m.LogsTabConnections(30), size.width, size.height)
			assertVisible(t, tt, m.LogsOpenTerminal(), size.width, size.height)
			assertVisible(t, tt, m.NavLogs(), size.width, size.height)
			assertVisible(t, tt, m.NavSettings(), size.width, size.height)
			assertNoHorizontalOverflow(t, tt, size.width, size.height)

			scrollToEnd(tt, size.width, size.height)
			assertVisible(t, tt, "web-29", size.width, size.height)
			assertNoHorizontalOverflow(t, tt, size.width, size.height)
		})
	}
}

func TestNetworkProviderRowRendersItsPhaseAndHasNoTerminalAction(t *testing.T) {
	m := locales.In("en")
	source := &fakeSource{}
	source.logs = []domainhistory.ConnectionLog{{
		ID:                   "provider-1",
		Kind:                 "tailscale",
		Status:               domainhistory.ConnectionFailed,
		Error:                "tailscale: machine authorization required",
		StartedAt:            time.Now(),
		NetworkProfileID:     "net-1",
		NetworkProfileLabel:  "Office",
		NetworkProviderKind:  "tailscale",
		NetworkProviderPhase: "needs_login",
		NetworkAddress:       "natcatty-office.tailnet.ts.net",
	}}
	tt, _ := newProductionLogsView(t, 720, 480, source, &fakeActions{})

	assertVisible(t, tt, "Office", 720, 480)
	assertVisible(t, tt, m.LogsProviderMeta(m.NetworkProviderPhaseNeedsLogin(), "tailscale"), 720, 480)
	assertVisible(t, tt, "natcatty-office.tailnet.ts.net", 720, 480)
	assertVisible(t, tt, "tailscale: machine authorization required", 720, 480)
	// A provider row has no Session to reopen, so it must not offer the
	// terminal action a Host row shows.
	if textsContain(tt, m.LogsOpenTerminal()) {
		t.Fatalf("provider row rendered the terminal action, texts %q", tt.Texts())
	}
}

func TestWireGuardProviderRowRendersHandshakeAndTraffic(t *testing.T) {
	m := locales.In("en")
	handshake := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{}
	source.logs = []domainhistory.ConnectionLog{{
		ID:                   "wg-row-1",
		Kind:                 "wireguard",
		Status:               domainhistory.ConnectionConnected,
		StartedAt:            time.Now(),
		NetworkProfileID:     "wg-1",
		NetworkProfileLabel:  "Office WG",
		NetworkProviderKind:  "wireguard",
		NetworkProviderPhase: "running",
		NetworkAddress:       "203.0.113.7:51820",
		NetworkLastHandshake: handshake,
		NetworkRxBytes:       1536,
		NetworkTxBytes:       512,
	}}
	tt, _ := newProductionLogsView(t, 720, 600, source, &fakeActions{})

	assertVisible(t, tt, "Office WG", 720, 600)
	assertVisible(t, tt, "203.0.113.7:51820", 720, 600)
	assertVisible(t, tt, uiconnection.WireGuardDetail(m, true, handshake, 1536, 512), 720, 600)
	if textsContain(tt, m.LogsOpenTerminal()) {
		t.Fatalf("WireGuard provider row rendered the terminal action, texts %q", tt.Texts())
	}
}

func TestLogsFiltersUpdateTheRenderedRows(t *testing.T) {
	m := locales.In("en")
	tt, state := newProductionLogsView(t, 720, 480, seededSource(), &fakeActions{})

	state.SetStatus(domainhistory.ConnectionFailed)
	tt.Frame()
	if !textsContain(tt, "connection refused") {
		t.Fatalf("failed filter lost the failure rows, texts %q", tt.Texts())
	}
	if textsContain(tt, "web-01") {
		t.Fatalf("failed filter kept a connected row, texts %q", tt.Texts())
	}

	state.SetStatus("")
	state.SetSearch("web-07")
	tt.Frame()
	if !textsContain(tt, "web-07") || textsContain(tt, "web-08") {
		t.Fatalf("search filter did not narrow the rows, texts %q", tt.Texts())
	}

	state.SetSearch("nothing-matches")
	tt.Frame()
	assertVisible(t, tt, m.LogsNoMatches(), 720, 480)
}

func TestHistoryTabRendersRowsAndOpensTheRelatedTerminal(t *testing.T) {
	m := locales.In("en")
	actions := &fakeActions{}
	tt, state := newProductionLogsView(t, 1100, 720, seededSource(), actions)
	state.SelectTab(TabHistory)
	tt.Frame()

	historyLabel := m.LogsTabHistory(30)
	if err := tt.Click(historyLabel); err != nil {
		t.Fatalf("click the history tab: %v", err)
	}
	tt.Frame()
	if !textsContain(tt, "command-00 --run") {
		t.Fatalf("history rows are not rendered, texts %q", tt.Texts())
	}
	if !textsContain(tt, m.LogsSourceSession()) {
		t.Fatalf("the history meta is not rendered, texts %q", tt.Texts())
	}

	if err := tt.Click(m.LogsOpenTerminal()); err != nil {
		t.Fatalf("click Open terminal: %v", err)
	}
	tt.Frame()
	if len(actions.sessions) != 1 || actions.sessions[0] != "session-1" ||
		actions.hosts[0] != "host-1" {
		t.Fatalf("row action = %+v", actions)
	}
	assertNoHorizontalOverflow(t, tt, 1100, 720)
}

// TestHistoryRowsShowCapturedFieldsAndUnknownLabels proves the History list
// shows cwd, finish, duration, exit, shell, tier, and origin, and labels a
// field the Session never observed instead of showing a misleading empty one.
func TestHistoryRowsShowCapturedFieldsAndUnknownLabels(t *testing.T) {
	m := locales.In("en")
	recorded := time.Date(2026, 11, 7, 9, 30, 0, 0, time.Local)
	finished := recorded.Add(1500 * time.Millisecond)
	source := &fakeSource{entries: []domainhistory.Entry{
		{
			ID: "captured", SessionID: "session-1", HostID: "host-1",
			HostLabel: "web-1", Scope: "host:host-1", Command: "make test",
			CWD: "/srv/app", Shell: "zsh", Origin: "deploy@web-1",
			RecordedAt: recorded, FinishedAt: finished,
			Duration: 1500 * time.Millisecond, ExitCode: 2, HasExitCode: true,
			Source: domainhistory.SourceSession, CaptureTier: 2,
		},
		{
			ID: "partial", Scope: "local", Command: "legacy-command",
			RecordedAt: recorded, Source: domainhistory.SourceSession,
		},
	}}
	tt, state := newProductionLogsView(t, 1100, 720, source, &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	captured := m.LogsHistoryCaptured(
		"/srv/app", timestamp(finished), "1.5s", "2", "zsh", "2", "deploy@web-1")
	if !textsContain(tt, captured) {
		t.Fatalf("captured fields are not rendered: %v", tt.Texts())
	}
	unknown := m.LogsHistoryUnknown()
	partial := m.LogsHistoryCaptured(unknown, unknown, unknown, unknown, unknown, unknown, unknown)
	if !textsContain(tt, partial) {
		t.Fatalf("unknown captured fields are not labelled: %v", tt.Texts())
	}
	assertNoHorizontalOverflow(t, tt, 1100, 720)
}

// TestHistoryDetailShowsEveryCapturedField opens the inline detail panel for a
// fully populated row and proves it renders the full command and every
// captured field, then closes it again.
func TestHistoryDetailShowsEveryCapturedField(t *testing.T) {
	m := locales.In("en")
	recorded := time.Date(2026, 11, 7, 9, 30, 0, 0, time.Local)
	finished := recorded.Add(1500 * time.Millisecond)
	source := &fakeSource{entries: []domainhistory.Entry{{
		ID: "captured", SessionID: "session-1", HostID: "host-1",
		HostLabel: "web-1", Scope: "host:host-1", Command: "make test --all",
		CWD: "/srv/app", Shell: "zsh", Origin: "deploy@web-1",
		RecordedAt: recorded, FinishedAt: finished,
		Duration: 1500 * time.Millisecond, ExitCode: 137, HasExitCode: true,
		Source: domainhistory.SourceSession, CaptureTier: 2,
	}}}
	tt, state := newProductionLogsView(t, 1100, 900, source, &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	if err := tt.Click(m.LogsHistoryDetails()); err != nil {
		t.Fatalf("open details: %v", err)
	}
	tt.Frame()

	assertVisible(t, tt, m.LogsHistoryDetailTitle(), 1100, 900)
	assertVisible(t, tt, "make test --all", 1100, 900)
	fields := []string{
		m.LogsHistoryDetailField(m.LogsHistoryFieldHost(), "web-1"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldScope(), "host:host-1"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldOrigin(), "deploy@web-1"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldSession(), "session-1"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldCwd(), "/srv/app"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldShell(), "zsh"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldStart(), timestamp(recorded)),
		m.LogsHistoryDetailField(m.LogsHistoryFieldFinish(), timestamp(finished)),
		m.LogsHistoryDetailField(m.LogsHistoryFieldDuration(), "1.5s"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldExit(), "137"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldSource(), m.LogsSourceSession()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldTier(), "2"),
	}
	for _, field := range fields {
		assertVisible(t, tt, field, 1100, 900)
	}
	assertNoHorizontalOverflow(t, tt, 1100, 900)

	if err := tt.Click(m.LogsHistoryDetailClose()); err != nil {
		t.Fatalf("close details: %v", err)
	}
	tt.Frame()
	for _, text := range tt.Texts() {
		if text == m.LogsHistoryDetailTitle() {
			t.Fatalf("the detail panel did not close: %v", tt.Texts())
		}
	}
}

// TestHistoryDetailLabelsUnknownCapturedFields proves a row with no captured
// cwd, shell, finish, duration, exit, origin, Session, or tier labels each
// value instead of rendering an empty string.
func TestHistoryDetailLabelsUnknownCapturedFields(t *testing.T) {
	m := locales.In("en")
	recorded := time.Date(2026, 11, 7, 9, 30, 0, 0, time.Local)
	source := &fakeSource{entries: []domainhistory.Entry{{
		ID: "partial", Scope: "local", Command: "legacy-command",
		RecordedAt: recorded, Source: domainhistory.SourceSession,
	}}}
	tt, state := newProductionLogsView(t, 1100, 900, source, &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	if err := tt.Click(m.LogsHistoryDetails()); err != nil {
		t.Fatalf("open details: %v", err)
	}
	tt.Frame()

	unknown := m.LogsHistoryUnknown()
	fields := []string{
		m.LogsHistoryDetailField(m.LogsHistoryFieldOrigin(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldSession(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldCwd(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldShell(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldFinish(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldDuration(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldExit(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldTier(), unknown),
	}
	for _, field := range fields {
		assertVisible(t, tt, field, 1100, 900)
	}
	// The start time is known, so it is shown rather than labelled unknown.
	assertVisible(t, tt,
		m.LogsHistoryDetailField(m.LogsHistoryFieldStart(), timestamp(recorded)),
		1100, 900)
	assertNoHorizontalOverflow(t, tt, 1100, 900)
}

func TestFormatDurationShowsZeroAndCoarseUnits(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{0, "0ms"},
		{350 * time.Millisecond, "350ms"},
		{1500 * time.Millisecond, "1.5s"},
		{65 * time.Second, "1m05s"},
		{-time.Second, "0ms"},
	}
	for _, test := range tests {
		if got := formatDuration(test.duration); got != test.want {
			t.Errorf("formatDuration(%v) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestConnectionRowActionReportsAMissingHost(t *testing.T) {
	m := locales.In("en")
	actions := &fakeActions{err: ErrHostMissing}
	tt, _ := newProductionLogsView(t, 720, 480, seededSource(), actions)

	if err := tt.Click(m.LogsOpenTerminal()); err != nil {
		t.Fatalf("click Open terminal: %v", err)
	}
	tt.Frame()
	assertVisible(t, tt, m.LogsProblemHostMissing(), 720, 480)
	assertNoHorizontalOverflow(t, tt, 720, 480)
}

func TestClearHistoryConfirmationIsReachable(t *testing.T) {
	m := locales.In("en")
	tt, state := newProductionLogsView(t, 720, 480, seededSource(), &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	if err := tt.Click(m.LogsClearHistory()); err != nil {
		t.Fatalf("click Clear history: %v", err)
	}
	tt.Frame()
	assertVisible(t, tt, m.LogsClearConfirm(), 720, 480)
	if err := tt.Click(m.LogsCancel()); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	tt.Frame()
	if state.PendingClear() {
		t.Fatal("Cancel did not close the clear confirmation")
	}
}

// TestLogsSurfaceReachesTheEndWithRealInputAtAShortHeight drives the same
// input path a user does (wheel, PageDown, End) at a height where the fixed
// title, tabs, and filters once consumed the whole list viewport. Every step
// must move the list and the last row must end up inside the window.
func TestLogsSurfaceReachesTheEndWithRealInputAtAShortHeight(t *testing.T) {
	tt, state := newProductionLogsView(t, 720, 300, seededLogSource(130), &fakeActions{})
	state.ShowMore()
	tt.Frame()

	// The pointer sits in the list viewport, away from the filter fields.
	x, y := float32(360), float32(260)
	tt.Move(x, y)
	tt.Scroll(x, y, 0, 400)
	tt.Frame()
	if !textsContain(tt, "web-000") {
		t.Fatalf("the wheel did not reveal the first rows: %v", tt.Texts())
	}

	tt.Key(0, ui.KeyPageDown)
	tt.Frame()
	tt.Key(0, ui.KeyEnd)
	tt.Frame()
	assertVisible(t, tt, "web-129", 720, 300)
}

// TestUnsavedHostRowKeepsItsTypedLabel proves a typed Host with no saved record
// is not a local shell: its row shows what the user typed, the Local filter
// excludes it, and the unsaved filter finds it.
func TestUnsavedHostRowKeepsItsTypedLabel(t *testing.T) {
	m := locales.In("en")
	source := &fakeSource{logs: []domainhistory.ConnectionLog{{
		ID: "unsaved", HostLabel: "deploy@typed.example", Hostname: "typed.example",
		Username: "deploy", Kind: "ssh", Status: domainhistory.ConnectionConnected,
		StartedAt: time.Now(),
	}}}
	tt, state := newProductionLogsView(t, 1100, 720, source, &fakeActions{})
	if !textsContain(tt, "deploy@typed.example") {
		t.Fatalf("the typed label is not rendered: %v", tt.Texts())
	}
	if textsContain(tt, m.LogsLocalTerminal()) {
		t.Fatalf("the unsaved Host row is labelled Local terminal: %v", tt.Texts())
	}

	state.SetHostKey(domainhistory.LocalHostKey)
	tt.Frame()
	if textsContain(tt, "deploy@typed.example") {
		t.Fatalf("the Local filter matched the unsaved Host: %v", tt.Texts())
	}
	state.SetHostKey(domainhistory.UnsavedHostKey)
	tt.Frame()
	if !textsContain(tt, "deploy@typed.example") {
		t.Fatalf("the unsaved filter lost the unsaved Host: %v", tt.Texts())
	}
}

// TestHistoryUnsavedHostRowKeepsItsTypedLabel proves the History tab names a
// typed Host by what the user typed, not by either the local shell or a saved
// Host identity.
func TestHistoryUnsavedHostRowKeepsItsTypedLabel(t *testing.T) {
	source := &fakeSource{entries: []domainhistory.Entry{{
		ID: "unsaved", HostLabel: "deploy@typed.example", UnsavedHost: true,
		Scope: "local", Command: "uptime", Source: domainhistory.SourceSession,
		RecordedAt: time.Now(),
	}}}
	tt, state := newProductionLogsView(t, 1100, 720, source, &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()
	if !textsContain(tt, "deploy@typed.example") {
		t.Fatalf("the History row lost the typed Host label: %v", tt.Texts())
	}

	state.SetHostKey(domainhistory.LocalHostKey)
	tt.Frame()
	if textsContain(tt, "uptime") {
		t.Fatalf("the Local filter matched the typed Host command: %v", tt.Texts())
	}
}

// TestClearConnectionLogsConfirmationAndBanner proves the Connections tab has
// the same two-step clear as history: Cancel leaves the rows and Confirm
// removes them, leaves command history alone, and shows a localized banner.
func TestClearConnectionLogsConfirmationAndBanner(t *testing.T) {
	m := locales.In("en")
	tt, state := newProductionLogsView(t, 720, 480, seededSource(), &fakeActions{})

	if err := tt.Click(m.LogsClearConnections()); err != nil {
		t.Fatalf("click Clear connection logs: %v", err)
	}
	tt.Frame()
	assertVisible(t, tt, m.LogsClearConnectionsConfirm(), 720, 480)
	if err := tt.Click(m.LogsCancel()); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	tt.Frame()
	if state.PendingClear() || state.TotalLogs() != 30 || state.TotalEntries() != 30 {
		t.Fatalf("Cancel changed records: pending = %v, logs = %d, entries = %d",
			state.PendingClear(), state.TotalLogs(), state.TotalEntries())
	}

	if err := tt.Click(m.LogsClearConnections()); err != nil {
		t.Fatalf("reopen Clear connection logs: %v", err)
	}
	tt.Frame()
	if err := tt.Click(m.LogsClearYes()); err != nil {
		t.Fatalf("click Clear: %v", err)
	}
	tt.Frame()
	if state.TotalLogs() != 0 || state.TotalEntries() != 30 {
		t.Fatalf("after clearing logs: logs = %d, entries = %d",
			state.TotalLogs(), state.TotalEntries())
	}
	assertVisible(t, tt, m.LogsConnectionsCleared(30), 720, 480)
	assertVisible(t, tt, m.LogsEmptyConnections(), 720, 480)
	assertNoHorizontalOverflow(t, tt, 720, 480)
}

// TestClearErrorNamesTheHistoryFamily proves a failed history clear says so
// even though the confirmation and banner code is shared with connections.
func TestClearErrorNamesTheHistoryFamily(t *testing.T) {
	m := locales.In("en")
	source := &fakeSource{}
	tt, state := newProductionLogsView(t, 720, 480, source, &fakeActions{})
	state.SelectTab(TabHistory)
	source.deleteErr = fmt.Errorf("disk")
	state.ConfirmClear()
	tt.Frame()
	if !textsContain(tt, m.LogsProblemClear()) {
		t.Fatalf("the history clear failure is not shown: %v", tt.Texts())
	}
	if textsContain(tt, m.LogsProblemClearConnections()) {
		t.Fatalf("the history clear failure was labelled a connection-log failure: %v", tt.Texts())
	}
}

func textsContain(tt *ui.Tester, want string) bool {
	for _, text := range tt.Texts() {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

// TestDateFilterOpensCalendarBeforeSettingTheDay drives the From filter: the
// first click opens a calendar and leaves the filter open-ended; the filter
// is set only once a day is chosen, and choosing it closes the calendar.
func TestDateFilterOpensCalendarBeforeSettingTheDay(t *testing.T) {
	state := NewState(seededSource(), nil)
	tt := ui.NewTester(View(state), 880, 680)
	tt.Frame()
	if err := tt.Click("From"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if *state.From() != "" {
		t.Fatalf("opening the calendar set the start to %q", *state.From())
	}
	if _, ok := tt.Find("12"); !ok {
		t.Fatal("the calendar did not open")
	}
	if err := tt.Click("12"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !strings.HasSuffix(*state.From(), "-12") {
		t.Fatalf("choosing the 12th set the start to %q", *state.From())
	}
	if state.pickers[0].open {
		t.Fatal("the calendar stayed open after a day was chosen")
	}
}
