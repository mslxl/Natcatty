package shell

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsearch "github.com/mslxl/natcatty/application/search"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	uisearch "github.com/mslxl/natcatty/ui/search"
	"github.com/mslxl/natcatty/ui/shell/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	"github.com/mslxl/natcatty/ui/vault/section"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// emptyVaultHosts is the Vault's repository port with no Hosts, so the shell
// tests render the real Vault surface and its New control.
type emptyVaultHosts struct{}

func (emptyVaultHosts) List(context.Context) ([]domainvault.Host, error) { return nil, nil }
func (emptyVaultHosts) Create(context.Context, domainvault.Draft) (domainvault.Host, error) {
	return domainvault.Host{}, nil
}
func (emptyVaultHosts) Update(context.Context, model.HostID, domainvault.Draft) (domainvault.Host, error) {
	return domainvault.Host{}, nil
}
func (emptyVaultHosts) Delete(context.Context, model.HostID) error { return nil }

// fakeTransport keeps a Session alive until the test ends it.
type fakeTransport struct {
	output    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{output: make(chan []byte), closed: make(chan struct{})}
}

func (f *fakeTransport) Read(p []byte) (int, error) {
	select {
	case data := <-f.output:
		return copy(p, data), nil
	case <-f.closed:
		return 0, io.EOF
	}
}

func (f *fakeTransport) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeTransport) Resize(int, int) error       { return nil }
func (f *fakeTransport) Wait() (int, error)          { return 6, nil }

func (f *fakeTransport) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeTransport) end() {
	f.closeOnce.Do(func() { close(f.closed) })
}

type fakeFactory struct{ transport *fakeTransport }

func (f *fakeFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

type fakeSurface struct{}

func (fakeSurface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "terminal-surface").Fill()
}

func (fakeSurface) Close() {}

// edgeSurface puts a marker at the right edge of the Pane, like the Session
// chrome's logging controls. It shows whether a Pane is as wide as the
// content column or wider than the window.
type edgeSurface struct{}

func (edgeSurface) View(c *ui.Context) ui.Element {
	return ui.Row(c).Fill().Children(func() {
		ui.Text(c, "terminal-surface").Grow(1)
		ui.Text(c, "pane-right-edge")
	})
}

func (edgeSurface) Close() {}

type testGraph struct {
	state     *state.State
	transport *fakeTransport
	sessions  []*appsession.Session
}

func newTestGraph(t *testing.T) *testGraph {
	t.Helper()
	graph := &testGraph{transport: newFakeTransport()}
	service := appsession.NewService(&fakeFactory{transport: graph.transport})
	t.Cleanup(service.CloseAll)
	graph.state = state.NewState(state.WithSessions(service, func(session *appsession.Session) (state.Surface, error) {
		graph.sessions = append(graph.sessions, session)
		return fakeSurface{}, nil
	}))
	return graph
}

// TestShellLayoutFitsAtDefaultAndMinimumSizes proves the responsive layout:
// the tab strip, the Vault's New control, and the terminal Pane stay inside
// the content column (right of the fixed sidebar) and inside the window, and
// the Pane does not overlap the tab strip.
func TestShellLayoutFitsAtDefaultAndMinimumSizes(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}, {1300, 800}} {
		service := appsession.NewService(&fakeFactory{transport: newFakeTransport()})
		t.Cleanup(service.CloseAll)
		vault := vaultstate.NewState(emptyVaultHosts{})
		shellState := state.NewState(
			state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
				return fakeSurface{}, nil
			}),
			state.WithVault(vaultui.View(vault)),
		)
		tt := ui.NewTester(View(shellState), size[0], size[1])
		tt.SetLocale("en")
		tt.Frame()

		// The Vault lists Hosts and offers New Host. At the minimum size the
		// header actions wrap below the title, so the two only overlap when
		// their boxes intersect on both axes.
		tt.Frame()
		newHost := assertContentInside(t, tt, size[0], size[1], m.VaultHostNew())
		listTitle := assertContentInside(t, tt, size[0], size[1], m.VaultHostListTitle(0))
		if overlaps(listTitle, newHost) {
			t.Fatalf("%dx%d New Host overlaps the host list title: %+v, %+v",
				size[0], size[1], listTitle, newHost)
		}

		if err := shellState.NewTab(); err != nil {
			t.Fatal(err)
		}
		shellState.ShowVault()
		tt.Frame()

		// The Tab sits in the window's Tab bar and its title is readable.
		title := assertInTabBar(t, tt, size[0], "Local Session 1")
		if title.W < 60 {
			t.Fatalf("%dx%d tab title is squeezed to %.1f DIPs: %+v",
				size[0], size[1], title.W, title)
		}
		assertContentInside(t, tt, size[0], size[1], m.VaultHostNew())

		shellState.SelectTab(0)
		tt.Frame()
		// A terminal Tab has no sidebar: its Pane takes nearly the window's
		// width.
		pane := assertContentInside(t, tt, size[0], size[1], "terminal-surface")
		if pane.W < float32(size[0])*0.9 {
			t.Fatalf("%dx%d terminal Pane is squeezed to %.1f DIPs: %+v",
				size[0], size[1], pane.W, pane)
		}
		if pane.Y < title.Y+title.H {
			t.Fatalf("%dx%d terminal Pane overlaps the tab strip: pane %+v, tab %+v",
				size[0], size[1], pane, title)
		}
	}
}

// TestShellPaneStaysWithinTheWindowWithManyTabs keeps the Pane width tied to
// the content column. The tab strip can be wider than the window; it must not
// stretch the content column with it, or the Pane's right-aligned chrome (the
// logging controls) is pushed outside the window.
func TestShellPaneStaysWithinTheWindowWithManyTabs(t *testing.T) {
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		service := appsession.NewService(&fakeFactory{transport: newFakeTransport()})
		t.Cleanup(service.CloseAll)
		shellState := state.NewState(
			state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
				return edgeSurface{}, nil
			}),
		)
		for index := 0; index < 8; index++ {
			if err := shellState.NewTab(); err != nil {
				t.Fatalf("%dx%d NewTab %d: %v", size[0], size[1], index, err)
			}
		}
		shellState.SelectTab(0)

		tt := ui.NewTester(View(shellState), size[0], size[1])
		tt.SetLocale("en")
		tt.Frame()

		edge, ok := tt.Find("pane-right-edge")
		if !ok {
			t.Fatalf("%dx%d does not render the Pane edge: %v", size[0], size[1], tt.Texts())
		}
		const epsilon = 0.5
		if edge.X < sidebarWidth-epsilon || edge.X+edge.W > float32(size[0])+epsilon {
			t.Fatalf("%dx%d pushes the Pane's right edge outside the window: %+v",
				size[0], size[1], edge)
		}
	}
}

// overlaps reports whether a and b intersect on both axes, tolerating the
// sub-DIP gap that fractional layout can leave between abutting boxes.
func overlaps(a, b ui.Rect) bool {
	const epsilon = 0.5
	return a.X < b.X+b.W-epsilon && b.X < a.X+a.W-epsilon &&
		a.Y < b.Y+b.H-epsilon && b.Y < a.Y+a.H-epsilon
}

// assertInTabBar finds text and asserts its box is inside the window's Tab
// bar.
func assertInTabBar(t *testing.T, tester *ui.Tester, width int, text string) ui.Rect {
	t.Helper()
	rect, ok := tester.Find(text)
	if !ok {
		t.Fatalf("%d wide does not show %q: %v", width, text, tester.Texts())
	}
	const epsilon = 0.5
	if rect.X < -epsilon || rect.Y < -epsilon ||
		rect.X+rect.W > float32(width)+epsilon || rect.Y+rect.H > topBarHeight+epsilon {
		t.Fatalf("%q is outside the Tab bar at %d wide: box %+v", text, width, rect)
	}
	return rect
}

// assertContentInside finds text and asserts its box is below the Tab bar
// and inside the window.
func assertContentInside(t *testing.T, tester *ui.Tester, width, height int, text string) ui.Rect {
	t.Helper()
	rect, ok := tester.Find(text)
	if !ok {
		t.Fatalf("%dx%d does not show %q: %v", width, height, text, tester.Texts())
	}
	const epsilon = 0.5
	if rect.X < -epsilon || rect.Y < topBarHeight-epsilon ||
		rect.X+rect.W > float32(width)+epsilon || rect.Y+rect.H > float32(height)+epsilon {
		t.Fatalf("%q is clipped at %dx%d: box %+v", text, width, height, rect)
	}
	return rect
}

func TestShellShowsVaultAndSettingsNavigation(t *testing.T) {
	tt := ui.NewTester(View(state.NewState()), 900, 600)

	if !tt.HasText("Vault") {
		t.Errorf("shell does not show Vault navigation, texts %q", tt.Texts())
	}
	if !tt.HasText("Settings") {
		t.Errorf("shell does not show Settings navigation, texts %q", tt.Texts())
	}
	if !tt.HasText("Your Hosts, keys, and notes will appear here.") {
		t.Errorf("shell does not show the Vault surface, texts %q", tt.Texts())
	}
}

func TestShellShowsDesktopTargetNotice(t *testing.T) {
	shellState := state.NewState()
	shellState.SetDesktopNotice(state.DesktopNoticeUnsupportedTarget)
	tt := ui.NewTester(View(shellState), 900, 600)
	if !tt.HasText(locales.In("en").DeepLinkProblemUnsupported()) {
		t.Fatalf("the shell does not show the unsupported-target notice: %v", tt.Texts())
	}

	shellState.SetDesktopNotice(state.DesktopNoticeInvalidTarget)
	tt.Frame()
	if !tt.HasText(locales.In("en").DeepLinkProblemInvalid()) {
		t.Fatalf("the shell does not show the invalid-target notice: %v", tt.Texts())
	}

	shellState.SetDesktopNotice(state.DesktopNoticeNone)
	tt.Frame()
	if tt.HasText(locales.In("en").DeepLinkProblemInvalid()) {
		t.Fatalf("the notice did not clear: %v", tt.Texts())
	}
}

func TestShellSettingsNavigationRequestsTheSettingsWindow(t *testing.T) {
	var requests int
	shellState := state.NewState(state.WithSettingsRequest(func() { requests++ }))
	tt := ui.NewTester(View(shellState), 900, 600)

	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("settings requests = %d after clicking Settings, want 1", requests)
	}
}

func TestShellSwitchesAllFiveLocalesWithoutRestart(t *testing.T) {
	tt := ui.NewTester(View(state.NewState()), 900, 600)

	tests := []struct {
		tag      string
		settings string
	}{
		{"en", "Settings"},
		{"es", "Configuración"},
		{"ru", "Настройки"},
		{"zh-CN", "设置"},
		{"zh-TW", "設定"},
	}
	for _, test := range tests {
		tt.SetLocale(test.tag)
		if !tt.HasText(test.settings) {
			t.Errorf("locale %s does not show %q, texts %q", test.tag, test.settings, tt.Texts())
		}
	}
}

func TestShellShowsOpenLocalSessionTabsAndTheirTerminal(t *testing.T) {
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	for _, want := range []string{"Local Session 1", "Local Session 2", "terminal-surface"} {
		if !tt.HasText(want) {
			t.Errorf("shell does not show %q, texts %q", want, tt.Texts())
		}
	}
	// A connected Session needs no status: the live terminal says so.
	if tt.HasText("Active") {
		t.Errorf("shell labels a connected Session Active, texts %q", tt.Texts())
	}

	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	tt.Frame()
	if tt.HasText("Local Session 2") {
		t.Errorf("shell still shows closed Local Session 2, texts %q", tt.Texts())
	}
	if !tt.HasText("Local Session 1") {
		t.Errorf("shell does not show Local Session 1 after closing Local Session 2, texts %q", tt.Texts())
	}
}

func TestShellShowsExitStatusAndCode(t *testing.T) {
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("Active") {
		t.Fatalf("shell labels a connected Session Active, texts %q", tt.Texts())
	}

	graph.transport.end()
	select {
	case <-graph.sessions[0].Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not exit")
	}
	tt.Frame()

	want := locales.In("en").SessionExitedWithCode(6)
	if !tt.HasText(want) {
		t.Errorf("shell does not show %q, texts %q", want, tt.Texts())
	}
	if tt.HasText("Active") {
		t.Errorf("shell still shows Active after the session exited, texts %q", tt.Texts())
	}
}

func TestShellShowsTheVaultSurfaceAndReturnsToIt(t *testing.T) {
	service := appsession.NewService(&fakeFactory{transport: newFakeTransport()})
	t.Cleanup(service.CloseAll)
	shellState := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithVault(func(c *ui.Context) {
			ui.Text(c, "vault-surface")
		}),
	)
	tt := ui.NewTester(View(shellState), 900, 600)

	if !tt.HasText("vault-surface") {
		t.Fatalf("shell does not show the Vault surface, texts %q", tt.Texts())
	}
	if err := shellState.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("vault-surface") {
		t.Fatalf("shell still shows the Vault after opening a Tab, texts %q", tt.Texts())
	}

	// The Vault Tab, first in the Tab bar, returns to it.
	if err := tt.Click(locales.In("en").NavVault()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("vault-surface") {
		t.Fatalf("clicking the Vault Tab did not show the Vault, texts %q", tt.Texts())
	}
}

// TestShellSidebarChoosesTheVaultPage proves each Vault page in the sidebar
// shows the Vault at that page, which the Vault surface reads to draw it.
func TestShellSidebarChoosesTheVaultPage(t *testing.T) {
	m := locales.In("en")
	shellState := state.NewState(state.WithVault(func(c *ui.Context) {
		ui.Text(c, "vault-surface")
	}))
	tt := ui.NewTester(View(shellState), 1100, 720)
	tt.SetLocale("en")
	if shellState.VaultSection() != section.Hosts {
		t.Fatalf("initial Vault page = %q, want Hosts", shellState.VaultSection())
	}
	shellState.ShowLogs()
	tt.Frame()
	for _, page := range []struct {
		label string
		want  section.Section
	}{
		{m.NavKeychain(), section.Keychain},
		{m.NavProxies(), section.Proxies},
		{m.NavForwarding(), section.Forwarding},
		{m.NavSnippets(), section.Snippets},
		{m.NavNotes(), section.Notes},
		{m.NavKnownHosts(), section.KnownHosts},
		{m.NavHosts(), section.Hosts},
	} {
		if err := tt.Click(page.label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if got := shellState.VaultSection(); got != page.want || !shellState.VaultVisible() {
			t.Fatalf("clicking %s: page = %q visible = %v, want %q shown",
				page.label, got, shellState.VaultVisible(), page.want)
		}
	}
}

func TestShellLanguageControlRequestsLocale(t *testing.T) {
	var requested []string
	shellState := state.NewState(state.WithLocaleRequest(func(tag string) {
		requested = append(requested, tag)
	}))
	tt := ui.NewTester(View(shellState), 900, 600)

	if err := tt.Click(locales.In("en").NavLanguage()); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("español"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(requested) != 1 || requested[0] != "es" {
		t.Fatalf("requested locales = %v, want [es]", requested)
	}
}

func TestStatusTextShowsConnectionIndicators(t *testing.T) {
	m := locales.In("en")
	cases := []struct {
		connection appsession.ConnectionStatus
		want       string
	}{
		{appsession.ConnectionStatusActive, m.ConnectionStatusActive()},
		{appsession.ConnectionStatusConnecting, m.ConnectionStatusConnecting()},
		{appsession.ConnectionStatusReconnecting, m.ConnectionStatusReconnecting()},
		{appsession.ConnectionStatusError, m.ConnectionStatusError()},
		{"", m.ConnectionStatusActive()},
	}
	for _, test := range cases {
		got := statusText(m, appsession.Snapshot{
			Status:     appsession.StatusRunning,
			Connection: test.connection,
		})
		if got != test.want {
			t.Errorf("statusText(%q) = %q, want %q", test.connection, got, test.want)
		}
	}

	// A permanent reconnect failure leaves the Session exited; the error
	// indicator must not be hidden by the exit placeholder.
	if got := statusText(m, appsession.Snapshot{
		Status:      appsession.StatusExited,
		Connection:  appsession.ConnectionStatusError,
		ExitCode:    255,
		HasExitCode: true,
	}); got != m.ConnectionStatusError() {
		t.Errorf("exited connection error = %q, want %q", got, m.ConnectionStatusError())
	}
}

func TestNoticeTextLocalizesX11Failures(t *testing.T) {
	m := locales.In("en")
	if got := noticeText(m, nil); got != "" {
		t.Fatalf("noticeText(nil) = %q, want empty", got)
	}
	if got := noticeText(m, &appsession.Notice{
		Code:   appsession.NoticeX11LocalDisplay,
		Target: "localhost:6000",
	}); got != m.ConnectionX11DisplayUnavailable("localhost:6000") {
		t.Fatalf("local-display notice = %q", got)
	}
	if got := noticeText(m, &appsession.Notice{
		Code:   appsession.NoticeX11Channel,
		Target: "localhost:6000",
	}); got != m.ConnectionX11ChannelFailed("localhost:6000") {
		t.Fatalf("channel notice = %q", got)
	}
	if got := noticeText(m, &appsession.Notice{
		Code:   appsession.NoticeX11Cookie,
		Target: "localhost:6000",
	}); got != m.ConnectionX11CookieUnavailable("localhost:6000") {
		t.Fatalf("cookie notice = %q", got)
	}
	if got := noticeText(m, &appsession.Notice{Code: "unknown"}); got != "" {
		t.Fatalf("unknown notice = %q, want empty", got)
	}
}

// TestShellViewRendersTheSearchOverlayAboveEverySurface proves the Quick
// Switcher and the command palette are part of the shell surface: the overlay
// is invisible while closed, appears over the content when its state opens,
// and closes on Escape.
func TestShellViewRendersTheSearchOverlayAboveEverySurface(t *testing.T) {
	search := uisearch.New(uisearch.WithSource(
		func(uisearch.Mode, string) []appsearch.Record { return nil }))
	shellState := state.NewState(state.WithSearch(search))
	tester := ui.NewTester(View(shellState), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if tester.HasText(m.SearchJumpTo()) {
		t.Fatalf("the overlay rendered while closed: %v", tester.Texts())
	}
	search.Open(uisearch.ModeQuickSwitcher)
	tester.Frame()
	if !tester.HasText(m.SearchJumpTo()) || !tester.HasText(m.SearchEmpty()) {
		t.Fatalf("the Quick Switcher did not render above the shell: %v", tester.Texts())
	}
	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	if search.IsOpen() {
		t.Fatal("Escape did not close the Quick Switcher")
	}
	if tester.HasText(m.SearchJumpTo()) {
		t.Fatalf("the closed overlay still renders: %v", tester.Texts())
	}
}

// TestTabTitleNamesASummaryLikeTheTabStrip proves the Quick Switcher labels a
// Session exactly as the tab strip does, copy suffix included.
func TestTabTitleNamesASummaryLikeTheTabStrip(t *testing.T) {
	m := locales.In("en")
	if got := TabTitle(m, state.TabSummary{Number: 3}); got != "Local Session 3" {
		t.Fatalf("unnamed Tab = %q, want %q", got, "Local Session 3")
	}
	if got := TabTitle(m, state.TabSummary{Label: "prod-db"}); got != "prod-db" {
		t.Fatalf("named Tab = %q", got)
	}
	if got, want := TabTitle(m, state.TabSummary{Label: "prod-db", Copy: 1}),
		m.TabCopyTitle("prod-db", 1); got != want {
		t.Fatalf("copied Tab = %q, want %q", got, want)
	}
}

func TestTopBarFollowsTheShowSFTPTabSetting(t *testing.T) {
	m := locales.In("en")
	shown := true
	s := state.NewState(
		state.WithSFTP(func(c *ui.Context) { ui.Text(c, "sftp surface") }),
		state.WithSFTPTabShown(func() bool { return shown }),
	)
	tester := ui.NewTester(View(s), 1100, 720)
	tester.SetLocale("en")
	if err := tester.Click(m.NavSftp()); err != nil {
		t.Fatalf("the SFTP Tab is missing by default: %v", err)
	}
	tester.Frame()
	if !s.SFTPVisible() || !tester.HasText("sftp surface") {
		t.Fatalf("the SFTP Tab did not open SFTP: %v", tester.Texts())
	}

	shown = false
	tester.Frame()
	tester.Frame()
	if s.SFTPVisible() || tester.HasText(m.NavSftp()) || tester.HasText("sftp surface") {
		t.Fatalf("hiding the SFTP Tab kept it or its surface: %v", tester.Texts())
	}
}
