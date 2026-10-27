//go:build selftest

package app

// Ticket 47 self-test. Press F11, then 4, then 7.
//
// The check runs the real Session surface, the production browser panel and
// its application layer, and the shell at once, with fake Host transport and
// page seams:
//
//   - the browser opens from an SSH Session's menu, and its panel starts
//     exactly one dedicated tunnel for that Session's saved Host;
//   - typing localhost:8080 opens http://localhost:8080/, while
//     example.com/docs opens https://example.com/docs; both pages load a
//     loopback gateway URL, never the remote address directly;
//   - Back, Forward, Reload, and Stop reach the page, and tabs open, close,
//     and activate their own pages;
//   - a tunnel disconnect shows the disconnected state with Retry; Retry
//     opens a second dedicated tunnel and returns to the same page;
//   - connected and disconnected frames are captured at 1100x720 and
//     720x480 in English and zh-CN, with every control inside the content
//     column;
//   - closing the browser returns the terminal and releases every page and
//     every dedicated tunnel.
//
// Every wait observes panel, page, or service state with a hard deadline;
// there is no sleep.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-47.json and
// %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-47-shots\*.png.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	browserui "github.com/mslxl/natcatty/ui/browser"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	sessionui "github.com/mslxl/natcatty/ui/session"
	uishell "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

const (
	selftest47Ticket = 47
	selftest47Wait   = 8 * time.Second
	selftest47Host   = "selftest-47-host"
	selftest47Local  = "http://localhost:8080/"
	selftest47Public = "https://example.com/docs"
)

type selftest47 struct {
	service   *appsession.Service
	factory   *selftest47Factory
	shell     *shellstate.State
	connector *selftest47Connector
	pages     *selftest47Pages
	tester    *ui.Tester

	session *appsession.Session
	surface *sessionui.Surface
	core    *appbrowser.Panel
	panel   *browserui.Panel

	browserOpening bool
	browserOpened  bool
	openDeadline   time.Time

	localTyped  bool
	localDone   bool
	publicTab   bool
	publicTyped bool
	publicDone  bool
	controls    bool
	tabsDone    bool

	disconnectStage    int
	disconnectDeadline time.Time

	directory string
	variant   int
	captured  int

	closed          bool
	cleanupDeadline time.Time
}

func (t *selftest47) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest47Ticket,
		Name:   "Host-routed browser tunnel and panel",
		Steps: []selftest.Step{
			{Name: "open-session", Run: t.openSessionStep},
			{Name: "open-browser", Run: t.openBrowserStep},
			{Name: "navigate-local", Run: t.navigateLocalStep},
			{Name: "navigate-public-and-controls", Run: t.navigatePublicStep},
			{Name: "tabs", Run: t.tabsStep},
			{Name: "disconnect-and-retry", Run: t.disconnectStep},
			{Name: "render-and-capture", Run: t.renderStep},
			{Name: "close-and-release", Run: t.cleanupStep},
		},
	}
}

// resetRunState rebuilds the Session service, shell, panel seams, and every
// per-run stage so pressing F11,4,7 again in the same process starts from the
// same conditions as the first run. The harness registers one set of steps, so
// without it a second run reuses opened flags, the first run's tunnel
// counters and deadlines, and a closed shell/panel; disconnect-and-retry then
// times out waiting for Retry to open a tunnel the old core can no longer
// request. It follows tickets 44 and 45 and runs on every entry of the first
// step, including after a run that failed before close-and-release.
func (t *selftest47) resetRunState() {
	// Close the browser panel and its dedicated tunnel explicitly: a run that
	// failed before close-and-release leaves both live, and shell teardown
	// alone must not be the only thing that discards them. Both closes are
	// idempotent.
	if t.panel != nil {
		t.panel.Close()
	}
	if t.core != nil {
		t.core.Close()
	}
	if t.shell != nil {
		t.shell.Shutdown()
		t.shell.CloseAllSessions()
	}
	if t.service != nil {
		t.service.CloseAll()
	}
	if t.directory != "" {
		_ = os.RemoveAll(t.directory)
		t.directory = ""
	}
	t.connector = &selftest47Connector{}
	t.pages = &selftest47Pages{}
	t.factory = &selftest47Factory{}
	t.service = appsession.NewService(t.factory)
	t.shell = shellstate.NewState(shellstate.WithSessions(t.service, t.newSurface))
	t.tester = nil

	t.session = nil
	t.surface = nil
	t.core = nil
	t.panel = nil

	t.browserOpening = false
	t.browserOpened = false
	t.openDeadline = time.Time{}
	t.localTyped = false
	t.localDone = false
	t.publicTab = false
	t.publicTyped = false
	t.publicDone = false
	t.controls = false
	t.tabsDone = false
	t.disconnectStage = 0
	t.disconnectDeadline = time.Time{}
	t.variant = 0
	t.captured = 0
	t.closed = false
	t.cleanupDeadline = time.Time{}
}

// newSurface builds the production Session surface with the browser opener
// wired to the self-test connector and fake pages. The panel itself is the
// real UI and application panel.
func (t *selftest47) newSurface(session *appsession.Session) (shellstate.Surface, error) {
	surface, err := sessionui.New(session, sessionui.WithBrowser(
		func(onClose func()) (sessionui.BrowserPanel, error) {
			core := appbrowser.New(appbrowser.Config{
				HostID:    session.HostID(),
				Label:     session.Label(),
				Connector: t.connector,
			})
			panel := browserui.New(core, t.pages,
				browserui.WithCloseRequest(onClose))
			t.core = core
			t.panel = panel
			return panel, nil
		}))
	if err != nil {
		return nil, err
	}
	t.session = session
	t.surface = surface
	return surface, nil
}

// openSessionStep opens the Session in one call, as the first step of every
// run. It resets first, then drives frames until the surface attaches, so the
// step never re-enters and re-resets mid-run.
func (t *selftest47) openSessionStep(*ui.Context) selftest.Outcome {
	t.resetRunState()
	t.tester = ui.NewTester(uishell.View(t.shell), 1100, 720)
	t.tester.SetLocale("en")
	host := domainvault.Host{
		ID:       model.HostID(selftest47Host),
		Label:    "web-1",
		Hostname: "web-1.selftest",
		Port:     22,
		Username: "root",
		Protocol: domainvault.ProtocolSSH,
	}
	if err := t.shell.OpenHost(host); err != nil {
		return selftest.Failed(fmt.Errorf("open the self-test Host: %w", err))
	}
	deadline := time.Now().Add(selftest47Wait)
	for {
		t.shell.Flush()
		t.tester.Frame()
		if t.session != nil && t.surface != nil && t.shell.TabCount() == 1 {
			return selftest.Passed(
				"an SSH Session opened through the production shell and its surface factory",
				"shellstate.WithSessions")
		}
		if time.Now().After(deadline) {
			return selftest.Failed(fmt.Errorf(
				"timed out after %s waiting for the SSH Session to attach to its Tab",
				selftest47Wait))
		}
	}
}

func (t *selftest47) openBrowserStep(*ui.Context) selftest.Outcome {
	if t.browserOpened {
		return selftest.Passed("the browser panel is connected", "browserui.New")
	}
	m := locales.In("en")
	if !t.browserOpening {
		t.shell.Flush()
		t.tester.Frame()
		if err := t.tester.Click(m.SessionMenuFor(1)); err != nil {
			return selftest.Failed(fmt.Errorf("open the Session menu: %w", err))
		}
		t.tester.Frame()
		if err := t.tester.ChooseMenuItem(m.SessionMenuOpenBrowser()); err != nil {
			return selftest.Failed(fmt.Errorf("choose Open Browser: %w", err))
		}
		t.browserOpening = true
		t.openDeadline = time.Now().Add(selftest47Wait)
	}
	t.tester.Frame()
	if t.core == nil {
		return pending(t.openDeadline, "the browser panel to be created")
	}
	state := t.core.State()
	if state.Route != appbrowser.RouteConnected {
		return pending(t.openDeadline, "the dedicated browser tunnel to connect")
	}
	calls, hosts, tunnels := t.connector.snapshot()
	if calls != 1 || len(tunnels) != 1 {
		return selftest.Failed(fmt.Errorf(
			"opening one panel made %d connector calls and %d tunnels", calls, len(tunnels)))
	}
	if len(hosts) != 1 || hosts[0] != t.session.HostID() {
		return selftest.Failed(fmt.Errorf(
			"the browser tunnel target = %v, want the Session's Host %q",
			hosts, t.session.HostID()))
	}
	if len(state.Tabs) != 1 || state.ActiveID == "" {
		return selftest.Failed(fmt.Errorf("the new panel has no blank tab: %+v", state))
	}
	if t.tester.HasText(m.BrowserNewTab()) == false {
		return selftest.Failed(fmt.Errorf("the browser is not rendered in the Session: %v",
			t.tester.Texts()))
	}
	t.browserOpened = true
	return selftest.Passed(
		"the browser opened from the Session menu with one dedicated tunnel to "+selftest47Host,
		"shellstate + browserui + appbrowser")
}

func (t *selftest47) navigateLocalStep(*ui.Context) selftest.Outcome {
	if t.localDone {
		return selftest.Passed("localhost:8080 defaulted to HTTP on the Host route",
			"domainbrowser.NormalizeURL")
	}
	if t.core == nil || t.core.State().Route != appbrowser.RouteConnected {
		return selftest.Pending
	}
	if !t.localTyped {
		t.tester.Key(ui.Cmd, ui.KeyL)
		t.tester.Frame()
		t.tester.Type("localhost:8080")
		t.tester.Frame()
		t.tester.Key(0, ui.KeyEnter)
		t.tester.Frame()
		t.localTyped = true
	}
	tab, ok := t.core.ActiveTab()
	if !ok {
		return selftest.Failed(errors.New("the panel lost its active tab"))
	}
	if tab.URL != selftest47Local {
		return selftest.Failed(fmt.Errorf("localhost:8080 opened as %q, want %q",
			tab.URL, selftest47Local))
	}
	pages := t.pages.snapshot()
	if len(pages) != 1 {
		return selftest.Failed(fmt.Errorf("pages = %d, want 1", len(pages)))
	}
	loads := pages[0].loads()
	if len(loads) != 1 || !strings.HasPrefix(loads[0], "http://127.0.0.2:") {
		return selftest.Failed(fmt.Errorf("the page loaded %v, want one loopback gateway URL", loads))
	}
	pages[0].report(browserui.PageTitle, "", "Local console", "")
	pages[0].setFlags(false, false, true)
	pages[0].report(browserui.PageLoading, "", "", "")
	t.tester.Frame()
	if !insideShell(t.tester, "Local console", 1100, 720) {
		return selftest.Failed(fmt.Errorf("the local tab title is missing: %v", t.tester.Texts()))
	}
	t.localDone = true
	return selftest.Passed(
		"localhost:8080 defaulted to "+selftest47Local+" and loaded "+loads[0],
		"domainbrowser.NormalizeURL + loopback gateway")
}

func (t *selftest47) navigatePublicStep(*ui.Context) selftest.Outcome {
	if t.publicDone {
		return selftest.Passed("example.com defaulted to HTTPS; Back, Forward, Reload, and Stop reached the page",
			"browserui.Panel + fake page")
	}
	m := locales.In("en")
	if !t.publicTab {
		if err := t.tester.Click(m.BrowserNewTab()); err != nil {
			return selftest.Failed(fmt.Errorf("open a browser tab: %w", err))
		}
		t.tester.Frame()
		if state := t.core.State(); len(state.Tabs) != 2 {
			return selftest.Failed(fmt.Errorf("tabs = %d, want 2", len(state.Tabs)))
		}
		t.publicTab = true
	}
	if !t.publicTyped {
		t.tester.Key(ui.Cmd, ui.KeyL)
		t.tester.Frame()
		t.tester.Type("example.com/docs")
		t.tester.Frame()
		t.tester.Key(0, ui.KeyEnter)
		t.tester.Frame()
		t.publicTyped = true
	}
	tab, ok := t.core.ActiveTab()
	if !ok {
		return selftest.Failed(errors.New("the second tab is not active"))
	}
	if tab.URL != selftest47Public {
		return selftest.Failed(fmt.Errorf("example.com/docs opened as %q, want %q",
			tab.URL, selftest47Public))
	}
	pages := t.pages.snapshot()
	if len(pages) != 2 {
		return selftest.Failed(fmt.Errorf("pages = %d, want 2", len(pages)))
	}
	page := pages[1]
	loads := page.loads()
	if len(loads) != 1 || !strings.HasPrefix(loads[0], "http://127.0.0.2:") {
		return selftest.Failed(fmt.Errorf("the public page loaded %v, want one loopback gateway URL", loads))
	}
	page.report(browserui.PageTitle, "", "Public console", "")
	page.setFlags(true, true, true)
	page.report(browserui.PageLoading, "", "", "")
	t.tester.Frame()
	if !insideShell(t.tester, "Public console", 1100, 720) {
		return selftest.Failed(fmt.Errorf("the public tab title is missing: %v", t.tester.Texts()))
	}
	if !t.controls {
		for _, control := range []struct {
			label string
			count func() int
		}{
			{m.BrowserBack(), page.backCount},
			{m.BrowserForward(), page.forwardCount},
			{m.BrowserReload(), page.reloadCount},
			{m.BrowserStop(), page.stopCount},
		} {
			if err := t.tester.Click(control.label); err != nil {
				return selftest.Failed(fmt.Errorf("click %s: %w", control.label, err))
			}
			t.tester.Frame()
			if got := control.count(); got != 1 {
				return selftest.Failed(fmt.Errorf("%s reached the page %d times, want 1",
					control.label, got))
			}
		}
		t.controls = true
	}
	t.publicDone = true
	return selftest.Passed(
		"example.com/docs defaulted to "+selftest47Public+" and loaded "+loads[0]+
			"; Back, Forward, Reload, and Stop each reached the page once",
		"browserui.Panel + fake page")
}

func (t *selftest47) tabsStep(*ui.Context) selftest.Outcome {
	if t.tabsDone {
		return selftest.Passed("tabs opened, closed, and activated their own pages",
			"appbrowser.Panel tabs")
	}
	m := locales.In("en")
	pages := t.pages.snapshot()
	if len(pages) != 2 {
		return selftest.Failed(fmt.Errorf("pages = %d, want 2", len(pages)))
	}
	if err := t.tester.Click(m.BrowserCloseTab()); err != nil {
		return selftest.Failed(fmt.Errorf("close the first tab: %w", err))
	}
	t.tester.Frame()
	state := t.core.State()
	if len(state.Tabs) != 1 {
		return selftest.Failed(fmt.Errorf("tabs after close = %d, want 1", len(state.Tabs)))
	}
	if !pages[0].isDestroyed() {
		return selftest.Failed(errors.New("closing the first tab kept its page alive"))
	}
	if active, ok := t.core.ActiveTab(); !ok || active.URL != selftest47Public {
		return selftest.Failed(fmt.Errorf("the remaining tab active URL = %q, want %q",
			active.URL, selftest47Public))
	}
	if err := t.tester.Click(m.BrowserNewTab()); err != nil {
		return selftest.Failed(fmt.Errorf("open a blank tab: %w", err))
	}
	t.tester.Frame()
	if state := t.core.State(); len(state.Tabs) != 2 {
		return selftest.Failed(fmt.Errorf("tabs = %d, want 2", len(state.Tabs)))
	}
	if active, ok := t.core.ActiveTab(); !ok || active.URL != "" {
		return selftest.Failed(fmt.Errorf("the new blank tab URL = %q, want empty", active.URL))
	}
	if !t.tester.HasText(m.BrowserBlankTab()) {
		return selftest.Failed(fmt.Errorf("the blank tab label is missing: %v", t.tester.Texts()))
	}
	if err := t.tester.Click("Public console"); err != nil {
		return selftest.Failed(fmt.Errorf("activate the public tab: %w", err))
	}
	t.tester.Frame()
	if active, ok := t.core.ActiveTab(); !ok || active.URL != selftest47Public {
		return selftest.Failed(fmt.Errorf("activating the public tab left URL %q", active.URL))
	}
	if len(t.pages.snapshot()) != 2 {
		return selftest.Failed(errors.New("activating a tab recreated a page"))
	}
	t.tabsDone = true
	return selftest.Passed(
		"closing the local tab destroyed only its page; a new blank tab activated, then the public tab again",
		"appbrowser.Panel tabs")
}

func (t *selftest47) disconnectStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	switch t.disconnectStage {
	case 0:
		pages := t.pages.snapshot()
		if len(pages) != 2 {
			return selftest.Failed(fmt.Errorf("pages before disconnect = %d, want 2", len(pages)))
		}
		if !t.connector.disconnectLast() {
			return selftest.Failed(errors.New("there is no dedicated tunnel to disconnect"))
		}
		t.disconnectStage = 1
		t.disconnectDeadline = time.Now().Add(selftest47Wait)
		return selftest.Pending
	case 1:
		t.tester.Frame()
		state := t.core.State()
		if state.Route == appbrowser.RouteConnected {
			return pending(t.disconnectDeadline, "the tunnel disconnect to reach the panel")
		}
		if state.Route != appbrowser.RouteDisconnected {
			return pending(t.disconnectDeadline, "the panel to finish disconnecting")
		}
		if state.Failure != appbrowser.FailureTunnel {
			return selftest.Failed(fmt.Errorf("disconnect failure = %q, want %q",
				state.Failure, appbrowser.FailureTunnel))
		}
		// The tunnel reports Done on its own goroutine. A frame that began
		// while the panel was still connected renders the page even though
		// State already reports the disconnect, so wait for a frame that shows
		// it rather than failing on the first observation.
		if !insideShell(t.tester, m.BrowserDisconnected(), 1100, 720) ||
			!insideShell(t.tester, m.BrowserRetry(), 1100, 720) {
			return pending(t.disconnectDeadline,
				"the disconnected state and Retry to render")
		}
		pages := t.pages.snapshot()
		if len(pages) < 2 || !pages[1].isDestroyed() {
			return pending(t.disconnectDeadline,
				"the disconnected panel to destroy its page")
		}
		if err := t.tester.Click(m.BrowserRetry()); err != nil {
			return selftest.Failed(fmt.Errorf("click Retry: %w", err))
		}
		t.tester.Frame()
		t.disconnectStage = 2
		return selftest.Pending
	default:
		t.tester.Frame()
		state := t.core.State()
		if state.Route != appbrowser.RouteConnected {
			return pending(t.disconnectDeadline, "Retry to open a fresh dedicated tunnel")
		}
		if len(t.pages.snapshot()) <= 2 {
			return pending(t.disconnectDeadline, "the retried route to load the active page")
		}
		calls, _, tunnels := t.connector.snapshot()
		if calls != 2 || len(tunnels) != 2 {
			return selftest.Failed(fmt.Errorf(
				"Retry made %d connector calls and %d tunnels, want 2 and 2", calls, len(tunnels)))
		}
		active, ok := t.core.ActiveTab()
		if !ok || active.URL != selftest47Public {
			return selftest.Failed(fmt.Errorf("the retried active URL = %q, want %q",
				active.URL, selftest47Public))
		}
		pages := t.pages.snapshot()
		loads := pages[len(pages)-1].loads()
		if len(loads) != 1 || !strings.HasPrefix(loads[0], "http://127.0.0.2:") {
			return selftest.Failed(fmt.Errorf(
				"the retried page loaded %v, want one loopback gateway URL", loads))
		}
		t.disconnectStage = 3
		return selftest.Passed(
			"the disconnect was visible with Retry; Retry opened a second dedicated tunnel and reloaded the public page",
			"appbrowser.Panel retry + browserui status")
	}
}

func (t *selftest47) renderStep(*ui.Context) selftest.Outcome {
	variants := []struct {
		width, height int
		locale        string
	}{
		{1100, 720, "en"},
		{1100, 720, "zh-CN"},
		{720, 480, "en"},
		{720, 480, "zh-CN"},
	}
	if t.directory == "" {
		t.directory = filepath.Join(selftestResultDir, "natcatty-selftest-47-shots")
		if err := os.RemoveAll(t.directory); err != nil {
			return selftest.Failed(fmt.Errorf("clear the screenshot directory: %w", err))
		}
		if err := os.MkdirAll(t.directory, 0o755); err != nil {
			return selftest.Failed(fmt.Errorf("create the screenshot directory: %w", err))
		}
	}
	if t.variant >= len(variants) {
		// Leave the app in its normal English size for cleanup.
		t.tester.SetSize(1100, 720)
		t.tester.SetLocale("en")
		t.tester.Frame()
		return selftest.Passed(fmt.Sprintf(
			"captured %d browser frames across 1100x720 and 720x480 in en and zh-CN",
			t.captured), "ui.Tester + capture")
	}
	variant := variants[t.variant]
	m := locales.In(variant.locale)
	prefix := fmt.Sprintf("browser-%dx%d-%s", variant.width, variant.height, variant.locale)
	t.tester.SetSize(variant.width, variant.height)
	t.tester.SetLocale(variant.locale)
	t.tester.Frame()
	t.tester.Frame()

	if err := t.waitFrames(selftest47Wait, func() bool {
		return t.core.State().Route == appbrowser.RouteConnected
	}, "the panel to be connected before capturing"); err != nil {
		return selftest.Failed(fmt.Errorf("%s: %v", prefix, err))
	}
	// The route changes on the tunnel goroutine; one frame renders it.
	t.tester.Frame()
	if active, ok := t.core.ActiveTab(); !ok || active.URL != selftest47Public {
		return selftest.Failed(fmt.Errorf("%s: the active page is not the public page", prefix))
	}
	for _, text := range []string{
		m.BrowserBack(), m.BrowserForward(), m.BrowserReload(), m.BrowserStop(),
		m.BrowserNewTab(), m.BrowserClose(), "Public console", selftest47Public,
	} {
		if !insideShell(t.tester, text, variant.width, variant.height) {
			return selftest.Failed(fmt.Errorf("%s connected: %q is missing or clipped: %v",
				prefix, text, t.tester.Texts()))
		}
	}
	if err := capture(t.tester, filepath.Join(t.directory, prefix+"-connected.png")); err != nil {
		return selftest.Failed(err)
	}
	t.captured++

	before := len(t.pages.snapshot())
	if !t.connector.disconnectLast() {
		return selftest.Failed(fmt.Errorf("%s: there is no tunnel to disconnect", prefix))
	}
	if err := t.waitFrames(selftest47Wait, func() bool {
		return t.core.State().Route == appbrowser.RouteDisconnected
	}, "the panel to show the tunnel disconnect"); err != nil {
		return selftest.Failed(fmt.Errorf("%s: %v", prefix, err))
	}
	// The route changes on the tunnel goroutine; one frame renders the
	// disconnected status before the frame is inspected and captured.
	t.tester.Frame()
	for _, text := range []string{
		m.BrowserDisconnected(), m.BrowserRetry(),
		m.BrowserNewTab(), m.BrowserClose(), "Public console",
	} {
		if !insideShell(t.tester, text, variant.width, variant.height) {
			return selftest.Failed(fmt.Errorf("%s disconnected: %q is missing or clipped: %v",
				prefix, text, t.tester.Texts()))
		}
	}
	if err := capture(t.tester, filepath.Join(t.directory, prefix+"-disconnected.png")); err != nil {
		return selftest.Failed(err)
	}
	t.captured++

	if err := t.tester.Click(m.BrowserRetry()); err != nil {
		return selftest.Failed(fmt.Errorf("%s: click Retry: %w", prefix, err))
	}
	if err := t.waitFrames(selftest47Wait, func() bool {
		return t.core.State().Route == appbrowser.RouteConnected &&
			len(t.pages.snapshot()) > before
	}, "Retry to reconnect the captured panel"); err != nil {
		return selftest.Failed(fmt.Errorf("%s: %v", prefix, err))
	}
	pages := t.pages.snapshot()
	page := pages[len(pages)-1]
	page.setFlags(true, true, false)
	page.report(browserui.PageTitle, "", "Public console", "")
	page.report(browserui.PageLoading, "", "", "")
	t.tester.Frame()
	t.variant++
	return selftest.Pending
}

func (t *selftest47) cleanupStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	if !t.closed {
		t.tester.SetSize(1100, 720)
		t.tester.SetLocale("en")
		t.tester.Frame()
		if err := t.tester.Click(m.BrowserClose()); err != nil {
			return selftest.Failed(fmt.Errorf("close the browser: %w", err))
		}
		t.closed = true
		t.cleanupDeadline = time.Now().Add(selftest47Wait)
	}
	t.tester.Frame()
	if t.tester.HasText(m.BrowserNewTab()) {
		return pending(t.cleanupDeadline, "the browser panel to close and the terminal to return")
	}
	if t.core.State().Route != appbrowser.RouteDisconnected {
		return pending(t.cleanupDeadline, "the browser tunnel to stop")
	}
	if t.shell.TabCount() != 1 {
		return selftest.Failed(fmt.Errorf("the Session Tab disappeared after closing the browser"))
	}
	if !t.connector.allDiscarded() {
		return selftest.Failed(errors.New("closing the browser left a dedicated tunnel unreleased"))
	}
	t.shell.Shutdown()
	t.shell.CloseAllSessions()
	t.service.CloseAll()
	if err := waitUntil(selftest47Wait, func() bool {
		return t.factory.allClosed() && t.pages.allDestroyed()
	}, "the Session transport and every browser page to close"); err != nil {
		return selftest.Failed(err)
	}
	return selftest.Passed(fmt.Sprintf(
		"the terminal returned; %d dedicated tunnels were discarded and %d pages destroyed",
		len(t.connector.tunnelList()), len(t.pages.snapshot())),
		"browserui.Panel.Close + shellstate.CloseAllSessions")
}

// waitFrames advances frames until ok or the deadline passes. It is used inside
// a step for a short transition; the condition is always observed, never a
// delay.
func (t *selftest47) waitFrames(timeout time.Duration, ok func() bool, what string) error {
	deadline := time.Now().Add(timeout)
	for {
		if ok() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", timeout, what)
		}
		t.tester.Frame()
	}
}

func waitUntil(timeout time.Duration, ok func() bool, what string) error {
	deadline := time.Now().Add(timeout)
	for {
		if ok() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", timeout, what)
		}
		runtime.Gosched()
	}
}

// selftest47Factory opens a quiet in-process SSH transport. No bytes flow;
// the check observes Session attachment and teardown only.
type selftest47Factory struct {
	mu         sync.Mutex
	transports []*selftest47Transport
}

func (f *selftest47Factory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if spec.HostID != model.HostID(selftest47Host) {
		return nil, fmt.Errorf("self-test transport got Host %q", spec.HostID)
	}
	transport := &selftest47Transport{done: make(chan struct{})}
	f.mu.Lock()
	f.transports = append(f.transports, transport)
	f.mu.Unlock()
	return transport, nil
}

func (f *selftest47Factory) allClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, transport := range f.transports {
		if !transport.isClosed() {
			return false
		}
	}
	return true
}

type selftest47Transport struct {
	done chan struct{}
	once sync.Once
}

func (t *selftest47Transport) Read([]byte) (int, error) {
	<-t.done
	return 0, io.EOF
}

func (t *selftest47Transport) Write(p []byte) (int, error) { return len(p), nil }

func (t *selftest47Transport) Resize(int, int) error { return nil }

func (t *selftest47Transport) Close() error {
	t.once.Do(func() { close(t.done) })
	return nil
}

func (t *selftest47Transport) isClosed() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// selftest47Tunnel is one dedicated Host transport. Closing done is a tunnel
// disconnect; Discard counts the panel's teardown.
type selftest47Tunnel struct {
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	disc int
}

func (t *selftest47Tunnel) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("the self-test pages do not make HTTP requests")
}

func (t *selftest47Tunnel) Done() <-chan struct{} { return t.done }

func (t *selftest47Tunnel) Discard() {
	t.mu.Lock()
	t.disc++
	t.mu.Unlock()
	t.disconnect()
}

func (t *selftest47Tunnel) disconnect() { t.once.Do(func() { close(t.done) }) }

func (t *selftest47Tunnel) discarded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.disc > 0
}

type selftest47Connector struct {
	mu      sync.Mutex
	calls   int
	hosts   []model.HostID
	tunnels []*selftest47Tunnel
}

func (c *selftest47Connector) Connect(_ context.Context, hostID model.HostID) (appbrowser.Tunnel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	tunnel := &selftest47Tunnel{done: make(chan struct{})}
	c.hosts = append(c.hosts, hostID)
	c.tunnels = append(c.tunnels, tunnel)
	return tunnel, nil
}

func (c *selftest47Connector) snapshot() (int, []model.HostID, []*selftest47Tunnel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, append([]model.HostID(nil), c.hosts...),
		append([]*selftest47Tunnel(nil), c.tunnels...)
}

func (c *selftest47Connector) tunnelList() []*selftest47Tunnel {
	_, _, tunnels := c.snapshot()
	return tunnels
}

func (c *selftest47Connector) disconnectLast() bool {
	c.mu.Lock()
	if len(c.tunnels) == 0 {
		c.mu.Unlock()
		return false
	}
	tunnel := c.tunnels[len(c.tunnels)-1]
	c.mu.Unlock()
	tunnel.disconnect()
	return true
}

func (c *selftest47Connector) allDiscarded() bool {
	for _, tunnel := range c.tunnelList() {
		if !tunnel.discarded() {
			return false
		}
	}
	return true
}

// selftest47Pages records every page the panel makes.
type selftest47Pages struct {
	mu    sync.Mutex
	items []*selftest47Page
}

func (f *selftest47Pages) NewPage() (browserui.Page, error) {
	page := &selftest47Page{}
	f.mu.Lock()
	f.items = append(f.items, page)
	f.mu.Unlock()
	return page, nil
}

func (f *selftest47Pages) snapshot() []*selftest47Page {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*selftest47Page(nil), f.items...)
}

func (f *selftest47Pages) allDestroyed() bool {
	for _, page := range f.snapshot() {
		if !page.isDestroyed() {
			return false
		}
	}
	return true
}

// selftest47Page records loads and commands and reports events the check
// triggers. Native is nil, so the panel renders its placeholder: the headless
// tester cannot host a real WebView.
type selftest47Page struct {
	mu         sync.Mutex
	listeners  []func(browserui.PageEvent)
	loadsSeen  []string
	url        string
	canBack    bool
	canForward bool
	loading    bool
	backs      int
	forwards   int
	reloads    int
	stops      int
	destroyed  bool
}

func (p *selftest47Page) Native() ui.NativeWebView { return nil }

func (p *selftest47Page) Load(rawURL string) error {
	p.mu.Lock()
	if p.destroyed {
		p.mu.Unlock()
		return nil
	}
	p.loadsSeen = append(p.loadsSeen, rawURL)
	p.url = rawURL
	p.loading = true
	p.mu.Unlock()
	p.emit(browserui.PageEvent{Kind: browserui.PageNavigated, URL: rawURL})
	return nil
}

func (p *selftest47Page) Reload() {
	p.mu.Lock()
	p.reloads++
	p.mu.Unlock()
}

func (p *selftest47Page) Stop() {
	p.mu.Lock()
	p.stops++
	p.loading = false
	p.mu.Unlock()
}

func (p *selftest47Page) Back() {
	p.mu.Lock()
	p.backs++
	p.mu.Unlock()
}

func (p *selftest47Page) Forward() {
	p.mu.Lock()
	p.forwards++
	p.mu.Unlock()
}

func (p *selftest47Page) CanGoBack() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.canBack
}

func (p *selftest47Page) CanGoForward() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.canForward
}

func (p *selftest47Page) Loading() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.loading
}

func (p *selftest47Page) URL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.url
}

func (p *selftest47Page) Destroy() {
	p.mu.Lock()
	p.destroyed = true
	p.mu.Unlock()
}

func (p *selftest47Page) OnEvent(fn func(browserui.PageEvent)) func() {
	p.mu.Lock()
	index := len(p.listeners)
	p.listeners = append(p.listeners, fn)
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if index < len(p.listeners) {
			p.listeners[index] = nil
		}
	}
}

func (p *selftest47Page) SetNavigationGuard(func(string) (string, bool)) {}

func (p *selftest47Page) emit(event browserui.PageEvent) {
	p.mu.Lock()
	listeners := append([]func(browserui.PageEvent){}, p.listeners...)
	p.mu.Unlock()
	for _, listener := range listeners {
		if listener != nil {
			listener(event)
		}
	}
}

func (p *selftest47Page) report(kind browserui.PageEventKind, rawURL, title, errText string) {
	p.emit(browserui.PageEvent{Kind: kind, URL: rawURL, Title: title, Error: errText})
}

func (p *selftest47Page) setFlags(back, forward, loading bool) {
	p.mu.Lock()
	p.canBack = back
	p.canForward = forward
	p.loading = loading
	p.mu.Unlock()
}

func (p *selftest47Page) backCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.backs
}

func (p *selftest47Page) forwardCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.forwards
}

func (p *selftest47Page) reloadCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reloads
}

func (p *selftest47Page) stopCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stops
}

func (p *selftest47Page) loads() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.loadsSeen...)
}

func (p *selftest47Page) isDestroyed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.destroyed
}
