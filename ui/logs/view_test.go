package logs

import (
	"context"
	"errors"
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
	"github.com/mslxl/natcatty/ui/internal/styletest"
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

func TestHistorySurfaceOpensTheAtuinAccountPanel(t *testing.T) {
	m := locales.In("en")
	opened := 0
	state := NewState(nil, nil, WithAccountPanel(func() { opened++ }))
	tester := ui.NewTester(View(state), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	if !tester.HasText(m.LogsAtuinAccount()) {
		t.Fatalf("the History surface has no account entry point: %v", tester.Texts())
	}
	if err := tester.Click(m.LogsAtuinAccount()); err != nil {
		t.Fatal(err)
	}
	if opened != 1 {
		t.Fatalf("account panel requests = %d, want 1", opened)
	}

	without := NewState(nil, nil)
	tester = ui.NewTester(View(without), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	if tester.HasText(m.LogsAtuinAccount()) {
		t.Fatal("an unconfigured surface showed an account entry point")
	}
}

// newProductionLogsView builds the real shell, opens one work Tab so the tab
// strip renders, and shows Logs. This is the chrome the production window
// gives the surface, not a bare component.
func newProductionLogsView(t *testing.T, width, height int,
	source *fakeSource, actions TerminalActions) (*ui.Tester, *State) {
	t.Helper()
	return newProductionLogsViewWithOptions(t, width, height, source, actions)
}

func newProductionLogsViewWithOptions(t *testing.T, width, height int,
	source *fakeSource, actions TerminalActions, options ...Option) (*ui.Tester, *State) {
	t.Helper()
	state := NewState(source, actions, options...)
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
	styletest.Each(t, func(t *testing.T) {
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
	})
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
	styletest.Each(t, func(t *testing.T) {
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
		if !textsContain(tt, m.LogsProvenanceThisDevice()) {
			t.Fatalf("the history provenance is not rendered, texts %q", tt.Texts())
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
	})
}

// TestHistoryRowsLabelAtuinImportAndSyncProvenance proves downloaded and
// imported Atuin rows are labelled by provenance, not reported as local
// Sessions, and that the source filter separates the two.
func TestHistoryRowsLabelAtuinImportAndSyncProvenance(t *testing.T) {
	m := locales.In("en")
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		{
			ID: "imported", Scope: "local", Command: "imported-command",
			Origin: "alice@laptop", RecordedAt: now,
			Source: domainhistory.SourceAtuinImport,
		},
		{
			ID: "synced", Scope: "local", Command: "synced-command",
			Origin: "bob@desktop", RecordedAt: now.Add(-time.Minute),
			Source: domainhistory.SourceAtuinSync,
		},
	}}
	tt, state := newProductionLogsView(t, 1100, 720, source, &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	for _, want := range []string{
		m.LogsSourceAtuinImport(), m.LogsProvenanceAtuinSync(),
		"imported-command", "synced-command",
	} {
		if !textsContain(tt, want) {
			t.Fatalf("%q is not rendered: %v", want, tt.Texts())
		}
	}
	state.SetSourceFilter(domainhistory.SourceAtuinSync)
	tt.Frame()
	if textsContain(tt, "imported-command") || !textsContain(tt, "synced-command") {
		t.Fatalf("source filter did not isolate sync rows: %v", tt.Texts())
	}
}

// TestHistoryRowsShowCapturedFieldsAndUnknownLabels proves the History list
// shows cwd, duration, and start time plus provenance, exit, and tier badges,
// and labels a field the Session never observed instead of showing a
// misleading empty one.
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

	captured := m.LogsHistoryRowMeta("web-1", "/srv/app", "1.5s", timestamp(recorded))
	if !textsContain(tt, captured) {
		t.Fatalf("captured fields are not rendered: %v", tt.Texts())
	}
	if !textsContain(tt, m.LogsProvenanceThisDevice()) {
		t.Fatalf("the captured row lost its provenance badge: %v", tt.Texts())
	}
	if !textsContain(tt, m.LogsHistoryExitBadge(2)) {
		t.Fatalf("the captured row lost its exit badge: %v", tt.Texts())
	}
	if !textsContain(tt, m.LogsHistoryTierBadge(2)) {
		t.Fatalf("the captured row lost its tier badge: %v", tt.Texts())
	}

	// The partial row labels what the Session never observed instead of
	// showing a misleading empty value, and an unknown exit is not success.
	unknown := m.LogsHistoryUnknown()
	partial := m.LogsHistoryRowMeta(m.LogsHostLocal(), unknown, unknown, timestamp(recorded))
	if !textsContain(tt, partial) {
		t.Fatalf("unknown captured fields are not labelled: %v", tt.Texts())
	}
	if !textsContain(tt, m.LogsHistoryExitUnknown()) {
		t.Fatalf("the unknown exit status is not labelled: %v", tt.Texts())
	}
	assertNoHorizontalOverflow(t, tt, 1100, 720)
}

// An imported Host Atuin row carries its own provenance instead of being
// mislabelled as a Netcatty Session.
func TestSourceLabelDistinguishesAnAtuinImport(t *testing.T) {
	m := locales.In("en")
	if got := sourceLabel(m, domainhistory.SourceAtuinImport); got != m.LogsSourceAtuinImport() {
		t.Fatalf("Atuin import source label = %q, want %q", got, m.LogsSourceAtuinImport())
	}
	if got := sourceLabel(m, domainhistory.SourceSession); got != m.LogsSourceSession() {
		t.Fatalf("Session source label = %q, want %q", got, m.LogsSourceSession())
	}
	if got := sourceLabel(m, domainhistory.SourceShellHistory); got != m.LogsSourceShell() {
		t.Fatalf("shell source label = %q, want %q", got, m.LogsSourceShell())
	}
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
	tt, state := newProductionLogsView(t, 1100, 1400, source, &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	if err := tt.Click(m.LogsHistoryDetails()); err != nil {
		t.Fatalf("open details: %v", err)
	}
	tt.Frame()

	assertVisible(t, tt, m.LogsHistoryDetailTitle(), 1100, 1400)
	assertVisible(t, tt, "make test --all", 1100, 1400)
	unknown := m.LogsHistoryUnknown()
	fields := []string{
		m.LogsHistoryDetailField(m.LogsHistoryFieldHost(), "web-1"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldHostID(), "host-1"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldHostLabel(), "web-1"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldUnsaved(), m.LogsHistoryNo()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldScope(), "host:host-1"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldOrigin(), "deploy@web-1"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldID(), "captured"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldSession(), "session-1"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldSessionUUID(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldCwd(), "/srv/app"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldShell(), "zsh"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldStart(), timestamp(recorded)),
		m.LogsHistoryDetailField(m.LogsHistoryFieldFinish(), timestamp(finished)),
		m.LogsHistoryDetailField(m.LogsHistoryFieldDuration(), "1.5s"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldExit(), "137"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldSource(), m.LogsSourceSession()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldTier(), "2"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldAuthor(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldAuthorKind(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldIntent(), unknown),
	}
	for _, field := range fields {
		assertVisible(t, tt, field, 1100, 1400)
	}
	// The detail reuses the row's provenance, exit, and tier badges.
	assertVisible(t, tt, m.LogsProvenanceThisDevice(), 1100, 1400)
	assertVisible(t, tt, m.LogsHistoryExitBadge(137), 1100, 1400)
	assertVisible(t, tt, m.LogsHistoryTierBadge(2), 1100, 1400)
	assertNoHorizontalOverflow(t, tt, 1100, 1400)

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
	tt, state := newProductionLogsView(t, 1100, 1400, source, &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	if err := tt.Click(m.LogsHistoryDetails()); err != nil {
		t.Fatalf("open details: %v", err)
	}
	tt.Frame()

	unknown := m.LogsHistoryUnknown()
	fields := []string{
		m.LogsHistoryDetailField(m.LogsHistoryFieldHost(), m.LogsHostLocal()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldHostID(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldHostLabel(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldUnsaved(), m.LogsHistoryNo()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldScope(), "local"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldOrigin(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldID(), "partial"),
		m.LogsHistoryDetailField(m.LogsHistoryFieldSession(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldSessionUUID(), unknown),
		// A field the shell never reported explains why it is missing
		// instead of showing a generic empty value.
		m.LogsHistoryDetailField(m.LogsHistoryFieldCwd(), m.LogsHistoryMissingCwd()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldShell(), m.LogsHistoryMissingShell()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldFinish(), m.LogsHistoryMissingFinish()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldDuration(), m.LogsHistoryMissingDuration()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldExit(), m.LogsHistoryMissingExit()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldTier(), m.LogsHistoryMissingTier()),
		m.LogsHistoryDetailField(m.LogsHistoryFieldAuthor(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldAuthorKind(), unknown),
		m.LogsHistoryDetailField(m.LogsHistoryFieldIntent(), unknown),
	}
	for _, field := range fields {
		assertVisible(t, tt, field, 1100, 1400)
	}
	// The start time is known, so it is shown rather than labelled unknown.
	assertVisible(t, tt,
		m.LogsHistoryDetailField(m.LogsHistoryFieldStart(), timestamp(recorded)),
		1100, 1400)
	// Each missing captured field is explained, not only labelled.
	for _, field := range []string{
		m.LogsHistoryFieldCwd(), m.LogsHistoryFieldExit(), m.LogsHistoryFieldDuration(),
	} {
		assertVisible(t, tt,
			m.LogsHistoryMissingField(field, m.LogsHistoryMissingReasonUnknown()),
			1100, 1400)
	}
	assertNoHorizontalOverflow(t, tt, 1100, 1400)
}

// TestHistoryFilterFieldsRenderAndNarrowTheRows drives the History filter
// surface through its state and proves each dimension changes the rendered
// result set, including a combined Host/origin scope.
func TestHistoryFilterFieldsRenderAndNarrowTheRows(t *testing.T) {
	m := locales.In("en")
	recorded := time.Date(2026, 11, 7, 9, 30, 0, 0, time.Local)
	source := &fakeSource{entries: []domainhistory.Entry{
		{ID: "e1", SessionID: "s1", HostID: "host-1", HostLabel: "web-1",
			Scope: "host:host-1", Command: "deploy web", CWD: "/srv/app",
			Shell: "zsh", Origin: "web-1:deploy", RecordedAt: recorded,
			ExitCode: 0, HasExitCode: true, Source: domainhistory.SourceSession},
		{ID: "e2", SessionID: "s2", HostID: "host-1", HostLabel: "web-1",
			Scope: "host:host-1", Command: "make test", CWD: "/srv/app",
			Shell: "bash", Origin: "web-1:deploy", RecordedAt: recorded.Add(-time.Minute),
			ExitCode: 2, HasExitCode: true, Source: domainhistory.SourceSession},
		{ID: "e3", SessionID: "s3", HostID: "host-2", HostLabel: "db-1",
			Scope: "host:host-2", Command: "kubectl get pods", CWD: "/var/log",
			Shell: "fish", Origin: "db-1:admin", RecordedAt: recorded.Add(-2 * time.Minute),
			Source: domainhistory.SourceAtuinImport},
		{ID: "e4", SessionUUID: "uuid-4", Scope: "remote:remote:deploy",
			Command: "remote-sync", Origin: "remote:deploy",
			RecordedAt: recorded.Add(-3 * time.Minute),
			Source:     domainhistory.SourceAtuinSync},
	}}
	tt, state := newProductionLogsView(t, 1100, 900, source, &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	// Every dimension has a labelled control on the History tab.
	for _, label := range []string{
		m.LogsSessionFilter(), m.LogsCwdFilter(), m.LogsShellFilter(),
		m.LogsExitFilter(), m.LogsSourceFilter(), m.LogsHostFilter(),
		m.LogsFrom(), m.LogsTo(),
	} {
		assertVisible(t, tt, label, 1100, 900)
	}
	for _, command := range []string{"deploy web", "make test", "kubectl get pods", "remote-sync"} {
		if !textsContain(tt, command) {
			t.Fatalf("%q is missing from the unfiltered list: %v", command, tt.Texts())
		}
	}

	state.SetCwdFilter("/srv/app")
	tt.Frame()
	if textsContain(tt, "kubectl get pods") || textsContain(tt, "remote-sync") {
		t.Fatalf("the cwd filter kept another directory's rows: %v", tt.Texts())
	}
	state.SetExitFilter(domainhistory.ExitFailure)
	tt.Frame()
	if textsContain(tt, "deploy web") || !textsContain(tt, "make test") {
		t.Fatalf("the exit filter kept a success: %v", tt.Texts())
	}
	state.SetCwdFilter("")
	state.SetExitFilter(domainhistory.ExitAny)
	state.SetSessionFilter("uuid-4")
	tt.Frame()
	if !textsContain(tt, "remote-sync") || textsContain(tt, "make test") {
		t.Fatalf("the Session UUID filter did not isolate the synced row: %v", tt.Texts())
	}
	state.SetSessionFilter("")
	state.SetShellFilter("FISH")
	tt.Frame()
	if !textsContain(tt, "kubectl get pods") || textsContain(tt, "make test") {
		t.Fatalf("the case-insensitive shell filter did not isolate fish: %v", tt.Texts())
	}
	state.SetShellFilter("")
	state.SetSourceFilter(domainhistory.SourceAtuinImport)
	tt.Frame()
	if !textsContain(tt, "kubectl get pods") || textsContain(tt, "deploy web") {
		t.Fatalf("the source filter did not isolate the import: %v", tt.Texts())
	}
	state.SetSourceFilter("")
	originKey := domainhistory.OriginHostKey("remote:deploy")
	state.SetHostKey(originKey)
	tt.Frame()
	if !textsContain(tt, "remote-sync") || textsContain(tt, "kubectl get pods") {
		t.Fatalf("the origin scope did not isolate the synced row: %v", tt.Texts())
	}
	// The Host selector names the origin rather than showing a raw filter key.
	assertVisible(t, tt, m.LogsHostOrigin("remote:deploy"), 1100, 900)
	assertNoHorizontalOverflow(t, tt, 1100, 900)
}

// TestHistoryEmptyAndLoadErrorStates proves the History tab distinguishes an
// empty journal from a failed load.
func TestHistoryEmptyAndLoadErrorStates(t *testing.T) {
	m := locales.In("en")
	empty, state := newProductionLogsView(t, 1100, 720, &fakeSource{}, &fakeActions{})
	state.SelectTab(TabHistory)
	empty.Frame()
	assertVisible(t, empty, m.LogsEmptyHistory(), 1100, 720)

	failing := &fakeSource{entriesErr: errors.New("disk")}
	broken, brokenState := newProductionLogsView(t, 1100, 720, failing, &fakeActions{})
	brokenState.SelectTab(TabHistory)
	broken.Frame()
	if brokenState.Problem() != ProblemLoad {
		t.Fatalf("problem = %v, want ProblemLoad", brokenState.Problem())
	}
	assertVisible(t, broken, m.LogsProblemLoad(), 1100, 720)
}

// TestGlobalHistorySearchFocusesTheFieldAndFilters proves OpenGlobalSearch
// arms the search input: typing without clicking or any extra chord filters
// the History rows.
func TestGlobalHistorySearchFocusesTheFieldAndFilters(t *testing.T) {
	source := &fakeSource{entries: []domainhistory.Entry{
		{ID: "e1", Scope: "local", Command: "deploy web",
			Source: domainhistory.SourceSession, RecordedAt: time.Now()},
		{ID: "e2", Scope: "local", Command: "make test",
			Source: domainhistory.SourceSession, RecordedAt: time.Now().Add(-time.Minute)},
	}}
	tt, state := newProductionLogsView(t, 1100, 900, source, &fakeActions{})

	state.OpenGlobalSearch()
	tt.Frame()
	if active, _ := tt.TextInputActive(); !active {
		t.Fatal("the global History search did not focus a text input")
	}
	tt.Type("deploy")
	tt.Frame()
	if got := *state.Search(); got != "deploy" {
		t.Fatalf("typed search = %q", got)
	}
	if !textsContain(tt, "deploy web") || textsContain(tt, "make test") {
		t.Fatalf("typing into the focused search did not filter: %v", tt.Texts())
	}
}

// TestHistoryDetailExplainsImportedMissingFields proves a row that came from
// shell history names the record, not a shell that never ran on this device.
func TestHistoryDetailExplainsImportedMissingFields(t *testing.T) {
	m := locales.In("en")
	recorded := time.Date(2026, 11, 7, 9, 30, 0, 0, time.Local)
	source := &fakeSource{entries: []domainhistory.Entry{{
		ID: "imported", Scope: "local", Command: "imported-command",
		RecordedAt: recorded, Source: domainhistory.SourceShellHistory,
	}}}
	tt, state := newProductionLogsView(t, 1100, 1400, source, &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	if err := tt.Click(m.LogsHistoryDetails()); err != nil {
		t.Fatalf("open details: %v", err)
	}
	tt.Frame()
	for _, field := range []string{
		m.LogsHistoryFieldCwd(), m.LogsHistoryFieldExit(), m.LogsHistoryFieldDuration(),
	} {
		assertVisible(t, tt,
			m.LogsHistoryMissingField(field, m.LogsHistoryMissingReasonImported()),
			1100, 1400)
	}
	assertNoHorizontalOverflow(t, tt, 1100, 1400)
}

// TestHistoryDetailExplainsMissingFieldsByCaptureTier proves the explanation
// names the capture path that could not observe the field, so a Tier 3 typed
// fallback row reads differently from a Tier 2 native-OSC row.
func TestHistoryDetailExplainsMissingFieldsByCaptureTier(t *testing.T) {
	m := locales.In("en")
	cases := []struct {
		name   string
		tier   int
		reason string
	}{
		{name: "hook", tier: domainhistory.CaptureTierHook, reason: m.LogsHistoryMissingReasonHook()},
		{name: "native", tier: domainhistory.CaptureTierNativeOSC, reason: m.LogsHistoryMissingReasonNative()},
		{name: "typed", tier: domainhistory.CaptureTierFallback, reason: m.LogsHistoryMissingReasonTyped()},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			recorded := time.Date(2026, 11, 7, 9, 30, 0, 0, time.Local)
			source := &fakeSource{entries: []domainhistory.Entry{{
				ID: "partial", SessionID: "session-1", Scope: "local",
				Command: "partial-command", RecordedAt: recorded,
				Source: domainhistory.SourceSession, CaptureTier: testCase.tier,
			}}}
			tt, state := newProductionLogsView(t, 1100, 1400, source, &fakeActions{})
			state.SelectTab(TabHistory)
			tt.Frame()

			if err := tt.Click(m.LogsHistoryDetails()); err != nil {
				t.Fatalf("open details: %v", err)
			}
			tt.Frame()
			for _, field := range []string{
				m.LogsHistoryFieldCwd(), m.LogsHistoryFieldExit(), m.LogsHistoryFieldDuration(),
			} {
				assertVisible(t, tt, m.LogsHistoryMissingField(field, testCase.reason), 1100, 1400)
			}
			assertNoHorizontalOverflow(t, tt, 1100, 1400)
		})
	}
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

// TestDeleteEverywhereIsSeparateFromClearAndExplainsUnseenRows proves the
// History surface offers a distinct bulk action, asks before deleting,
// explains that rows this device never downloaded are out of reach, and leaves
// the journal untouched on cancel. Confirming removes imported and synced rows
// as well, unlike Clear this device.
func TestDeleteEverywhereIsSeparateFromClearAndExplainsUnseenRows(t *testing.T) {
	m := locales.In("en")
	now := time.Now()
	source := &fakeSource{entries: []domainhistory.Entry{
		entryAt("local", "", "ls", domainhistory.SourceSession, now),
		entryAt("imported", "", "uptime", domainhistory.SourceAtuinImport, now),
		entryAt("synced", "", "df -h", domainhistory.SourceAtuinSync, now),
	}}
	state := NewState(source, nil)
	tester := ui.NewTester(View(state), 900, 600)
	tester.SetLocale("en")
	state.SelectTab(TabHistory)
	tester.Frame()

	if !tester.HasText(m.LogsClearHistory()) || !tester.HasText(m.LogsDeleteEverywhere()) {
		t.Fatalf("the two bulk actions are not both visible: %v", tester.Texts())
	}
	if m.LogsClearHistory() == m.LogsDeleteEverywhere() {
		t.Fatal("Delete everywhere reused the Clear this device label")
	}
	if !strings.Contains(m.LogsDeleteEverywhereConfirm(), "never downloaded") {
		t.Fatalf("the confirmation does not name the unseen-row limit: %q",
			m.LogsDeleteEverywhereConfirm())
	}

	if err := tester.Click(m.LogsDeleteEverywhere()); err != nil {
		t.Fatalf("click Delete everywhere: %v", err)
	}
	tester.Frame()
	assertVisible(t, tester, m.LogsDeleteEverywhereConfirm(), 900, 600)
	if state.TotalEntries() != 3 || state.DeletedEverywhere() != 0 {
		t.Fatalf("the first click removed rows: entries=%d deleted=%d",
			state.TotalEntries(), state.DeletedEverywhere())
	}
	if err := tester.Click(m.LogsCancel()); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	tester.Frame()
	if state.PendingDeleteEverywhere() || state.TotalEntries() != 3 {
		t.Fatalf("cancel changed the journal: pending=%v entries=%d",
			state.PendingDeleteEverywhere(), state.TotalEntries())
	}

	if err := tester.Click(m.LogsDeleteEverywhere()); err != nil {
		t.Fatalf("reopen Delete everywhere: %v", err)
	}
	tester.Frame()
	if err := tester.Click(m.LogsDeleteEverywhereYes()); err != nil {
		t.Fatalf("confirm Delete everywhere: %v", err)
	}
	tester.Frame()
	if state.PendingDeleteEverywhere() || state.TotalEntries() != 0 ||
		state.DeletedEverywhere() != 3 {
		t.Fatalf("after confirmation: pending=%v entries=%d deleted=%d",
			state.PendingDeleteEverywhere(), state.TotalEntries(), state.DeletedEverywhere())
	}
	assertVisible(t, tester, m.LogsDeleteEverywhereDone(3), 900, 600)
}

func TestHistoryRowCopyAndInsertAreExplicitActions(t *testing.T) {
	m := locales.In("en")
	commands := &fakeCommandActions{}
	tt, state := newProductionLogsViewWithOptions(t, 1100, 720, seededSource(),
		&fakeActions{}, WithCommandActions(commands))
	state.SelectTab(TabHistory)
	tt.Frame()

	// Rendering the rows must not insert anything; only the explicit action
	// does.
	if len(commands.commands) != 0 {
		t.Fatalf("rendering inserted commands: %+v", commands.commands)
	}
	if err := tt.Click(m.LogsCopy()); err != nil {
		t.Fatalf("click Copy: %v", err)
	}
	tt.Frame()
	if got := tt.Clipboard(); got != "command-00 --run" {
		t.Fatalf("clipboard = %q, want the first row's command", got)
	}

	if err := tt.Click(m.LogsInsert()); err != nil {
		t.Fatalf("click Insert: %v", err)
	}
	tt.Frame()
	if len(commands.commands) != 1 || commands.commands[0] != "command-00 --run" {
		t.Fatalf("inserted commands = %+v, want the first row", commands.commands)
	}
	if state.OpenProblem() != ProblemNone {
		t.Fatalf("insert problem = %v", state.OpenProblem())
	}
}

// TestHistoryRowDeleteWaitsForConfirmation proves the row action is not a
// one-click destructive action: the first click opens a confirmation, Cancel
// leaves the row, and only the confirming click removes it.
func TestHistoryRowDeleteWaitsForConfirmation(t *testing.T) {
	m := locales.In("en")
	tt, state := newProductionLogsView(t, 1100, 720, seededSource(), &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	if err := tt.Click(m.LogsDelete()); err != nil {
		t.Fatalf("click Delete: %v", err)
	}
	tt.Frame()
	if state.PendingDeleteEntry() != "entry-00" {
		t.Fatalf("pending delete = %q, want entry-00", state.PendingDeleteEntry())
	}
	if state.TotalEntries() != 30 {
		t.Fatalf("entries = %d, want none removed before confirmation", state.TotalEntries())
	}
	assertVisible(t, tt, m.LogsDeleteConfirm(), 1100, 720)

	if err := tt.Click(m.LogsCancel()); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	tt.Frame()
	if state.PendingDeleteEntry() != "" || state.TotalEntries() != 30 {
		t.Fatalf("cancel changed the journal: pending=%q entries=%d",
			state.PendingDeleteEntry(), state.TotalEntries())
	}

	if err := tt.Click(m.LogsDelete()); err != nil {
		t.Fatalf("click Delete again: %v", err)
	}
	tt.Frame()
	// The row's Delete is replaced by the confirmation strip, so the only
	// Delete button is the confirming one.
	if err := tt.Click(m.LogsDelete()); err != nil {
		t.Fatalf("confirm Delete: %v", err)
	}
	tt.Frame()
	if state.PendingDeleteEntry() != "" {
		t.Fatalf("pending delete = %q after confirmation", state.PendingDeleteEntry())
	}
	if state.TotalEntries() != 29 {
		t.Fatalf("entries = %d, want the confirmed row removed", state.TotalEntries())
	}
}

func TestPruneDialogShowsAPreviewAndOnlyAppliesMatchedRows(t *testing.T) {
	m := locales.In("en")
	tt, state := newProductionLogsView(t, 1100, 900, seededSource(), &fakeActions{})
	state.SelectTab(TabHistory)
	state.SetSearch("command-01")
	tt.Frame()

	if err := tt.Click(m.LogsPrune()); err != nil {
		t.Fatalf("click Prune matching: %v", err)
	}
	tt.Frame()
	if !state.PendingPrune() {
		t.Fatal("the prune dialog did not open")
	}
	if !tt.HasText(m.LogsPruneTitle()) {
		t.Fatalf("the prune dialog title is not rendered: %v", tt.Texts())
	}
	if !tt.HasText(m.LogsPrunePreview(1)) {
		t.Fatalf("the preview does not report one matched row: %v", tt.Texts())
	}
	if err := tt.Click(m.LogsCancel()); err != nil {
		t.Fatalf("click Cancel in the prune dialog: %v", err)
	}
	tt.Frame()
	if state.PendingPrune() {
		t.Fatal("Cancel did not close the prune dialog")
	}
	if state.TotalEntries() != 30 {
		t.Fatalf("Cancel changed the journal: %d entries", state.TotalEntries())
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

// TestHistoryStatisticsViewSummarizesAndTracksFilters opens the Statistics
// view through its selector and proves the unfiltered, filtered, and empty
// result sets each render the summary of the same rows the History list uses.
func TestHistoryStatisticsViewSummarizesAndTracksFilters(t *testing.T) {
	m := locales.In("en")
	tt, state := newProductionLogsView(t, 1100, 900, statisticsSource(), &fakeActions{})
	state.SelectTab(TabHistory)
	tt.Frame()

	// The command list is the default view; the summary is a separate view
	// selected explicitly.
	if textsContain(tt, m.LogsStatisticsTotal(3)) {
		t.Fatalf("the statistics summary rendered before it was selected: %v", tt.Texts())
	}
	if err := tt.Click(m.LogsStatisticsTitle()); err != nil {
		t.Fatalf("open statistics: %v", err)
	}
	tt.Frame()
	if state.HistoryMode() != HistoryModeStatistics {
		t.Fatalf("mode = %v, want statistics", state.HistoryMode())
	}

	for _, want := range []string{
		m.LogsStatisticsTotal(3),
		m.LogsStatisticsUnique(3),
		m.LogsStatisticsFailures(1),
		m.LogsStatisticsFailureRate(formatRate(0.5)),
		m.LogsStatisticsAverageDuration("2.0s"),
		m.LogsStatisticsDurationExcluded(1),
		m.LogsStatisticsTopCommands(),
		m.LogsStatisticsRow("git status", 1),
		m.LogsStatisticsRow("make test", 1),
		m.LogsStatisticsRow("ls", 1),
		m.LogsStatisticsTopCwds(),
		m.LogsStatisticsRow("/srv/app", 2),
		m.LogsStatisticsRow("/var/log", 1),
		m.LogsStatisticsBusiestHour("09:00", 3),
		m.LogsStatisticsHosts(),
		m.LogsStatisticsRow("web-1", 2),
		m.LogsStatisticsRow("db-1", 1),
	} {
		if !textsContain(tt, want) {
			t.Fatalf("%q is missing from the summary: %v", want, tt.Texts())
		}
	}
	assertNoHorizontalOverflow(t, tt, 1100, 900)

	// Every filter narrows the summary exactly as it narrows the rows.
	state.SetCwdFilter("/srv/app")
	tt.Frame()
	for _, want := range []string{
		m.LogsStatisticsTotal(2),
		m.LogsStatisticsUnique(2),
		m.LogsStatisticsFailures(1),
		m.LogsStatisticsFailureRate(formatRate(0.5)),
		m.LogsStatisticsAverageDuration("2.0s"),
		m.LogsStatisticsDurationAllKnown(2),
		m.LogsStatisticsBusiestHour("09:00", 2),
		m.LogsStatisticsRow("web-1", 2),
	} {
		if !textsContain(tt, want) {
			t.Fatalf("%q is missing from the filtered summary: %v", want, tt.Texts())
		}
	}
	if textsContain(tt, m.LogsStatisticsTotal(3)) ||
		textsContain(tt, m.LogsStatisticsRow("db-1", 1)) {
		t.Fatalf("the summary ignored the cwd filter: %v", tt.Texts())
	}

	state.SetCwdFilter("")
	state.SetSearch("no-match")
	tt.Frame()
	assertVisible(t, tt, m.LogsStatisticsEmpty(), 1100, 900)
	if textsContain(tt, m.LogsStatisticsTotal(0)) {
		t.Fatalf("an empty result set rendered summary counts: %v", tt.Texts())
	}

	// Switching back to Commands shows the row list again.
	if err := tt.Click(m.LogsHistoryModeCommands()); err != nil {
		t.Fatalf("return to commands: %v", err)
	}
	state.SetSearch("")
	tt.Frame()
	if state.HistoryMode() != HistoryModeCommands {
		t.Fatalf("mode = %v, want commands", state.HistoryMode())
	}
	if !textsContain(tt, "git status") {
		t.Fatalf("the command list did not return: %v", tt.Texts())
	}
}

// TestHistoryStatisticsViewExplainsPartialData proves the summary labels data
// the shell never captured instead of presenting zeros as measurements: no
// known duration, no known exit, and no working directory are each explained.
func TestHistoryStatisticsViewExplainsPartialData(t *testing.T) {
	m := locales.In("en")
	recorded := time.Date(2026, 11, 7, 9, 30, 0, 0, time.Local)
	source := &fakeSource{entries: []domainhistory.Entry{
		{ID: "a", Scope: "local", Command: "ls", RecordedAt: recorded,
			Source: domainhistory.SourceSession},
		{ID: "b", Scope: "local", Command: "pwd",
			RecordedAt: recorded.Add(-time.Minute), Source: domainhistory.SourceSession},
	}}
	tt, state := newProductionLogsView(t, 720, 480, source, &fakeActions{})
	state.SelectTab(TabHistory)
	state.SetHistoryMode(HistoryModeStatistics)
	tt.Frame()

	// The summary is scrollable, so assert its text without requiring it all
	// to fit at once.
	for _, want := range []string{
		m.LogsStatisticsTotal(2),
		m.LogsStatisticsFailureRateUnknown(),
		m.LogsStatisticsAverageDurationUnknown(),
		m.LogsStatisticsNoCwds(),
	} {
		if !textsContain(tt, want) {
			t.Fatalf("%q is missing from the partial summary: %v", want, tt.Texts())
		}
	}
}

// TestStatisticsHostLabelsQualifySharedSavedHostLabels proves two saved Hosts
// with the same recorded label stay distinguishable in the summary, while
// marker and origin groups keep their own labels.
func TestStatisticsHostLabelsQualifySharedSavedHostLabels(t *testing.T) {
	m := locales.In("en")
	hosts := []domainhistory.HostStat{
		{Key: "host-1", Label: "web", Count: 3},
		{Key: "host-2", Label: "web", Count: 1},
		{Key: domainhistory.LocalHostKey, Count: 1},
		{Key: domainhistory.UnsavedHostKey, Label: "typed.example", Count: 1},
		{Key: domainhistory.OriginHostKey("remote.example:deploy"),
			Label: "remote.example:deploy", Count: 1},
	}
	want := []string{
		"web (host-1)", "web (host-2)", m.LogsHostLocal(),
		"typed.example", "remote.example:deploy",
	}
	got := statisticsHostLabels(m, hosts)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("host labels = %v, want %v", got, want)
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
