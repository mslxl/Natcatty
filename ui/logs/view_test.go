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

func textsContain(tt *ui.Tester, want string) bool {
	for _, text := range tt.Texts() {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}
