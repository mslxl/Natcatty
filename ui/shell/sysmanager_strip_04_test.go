package shell

import (
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
	uisysmanager "github.com/mslxl/natcatty/ui/sysmanager"
)

// TestSidePanelToolStripKeepsItsSixToolsAndHasNoMore proves the System Tab
// Strip work did not add a tmux or Docker Side Panel Tool, a Side Panel More,
// or any other change to the panel's six fixed tools. Choosing System still
// shows the System Tool below the same strip.
func TestSidePanelToolStripKeepsItsSixToolsAndHasNoMore(t *testing.T) {
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	sideView := func(text string) func(state.SideTarget, float32) func(*ui.Context) {
		return func(state.SideTarget, float32) func(*ui.Context) {
			return func(c *ui.Context) { ui.Text(c, text) }
		}
	}
	s := state.NewState(
		state.WithSessions(service, func(session *appsession.Session) (state.Surface, error) {
			return sessionui.New(session)
		}),
		state.WithFilesPanel(state.FilesPanel{View: func(float32) func(*ui.Context) {
			return func(c *ui.Context) { ui.Text(c, "files-panel") }
		}}),
		state.WithSideTools(
			state.SideTool{ID: state.SideScripts, View: sideView("scripts-panel")},
			state.SideTool{ID: state.SideHistory, View: sideView("history-panel")},
			state.SideTool{ID: state.SideTheme, View: sideView("theme-panel")},
			state.SideTool{ID: state.SideSystem, View: sideView("system-panel")},
			state.SideTool{ID: state.SideNotes, View: sideView("notes-panel")},
		),
	)
	graph := &workspaceViewGraph{state: s}
	if err := s.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 1)

	want := []state.SideToolID{
		state.SideSFTP, state.SideScripts, state.SideHistory,
		state.SideTheme, state.SideSystem, state.SideNotes,
	}
	if !s.ToggleActiveFiles() {
		t.Fatal("the side panel did not open")
	}
	active, _ := s.TabID(s.ActiveTab())
	if got := s.SideTools(active); !slices.Equal(got, want) {
		t.Fatalf("the side panel tools are %v, want the six fixed tools %v", got, want)
	}

	tt := ui.NewTester(View(s), 1280, 800)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()
	m := locales.In("en")
	for _, label := range []string{
		"SFTP", m.SidePanelScripts(), m.SidePanelHistory(),
		m.SidePanelTheme(), m.SidePanelSystem(), m.SidePanelNotes(),
	} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("the side panel tool strip lacks %q: %v", label, tt.Texts())
		}
	}
	if _, ok := tt.Find(m.SysManagerMoreTabs()); ok {
		t.Fatalf("the side panel tool strip has a More trigger: %v", tt.Texts())
	}
	if tt.HasText(m.SysManagerCollapsedTabs()) {
		t.Fatalf("the side panel tool strip shows the Collapsed Tabs menu: %v", tt.Texts())
	}
	for _, label := range []string{m.SysManagerTabTmux(), m.SysManagerTabDocker()} {
		if tt.HasText(label) {
			t.Fatalf("the side panel tool strip gained the %q System Tab as a tool: %v",
				label, tt.Texts())
		}
	}

	if err := tt.Click(m.SidePanelSystem()); err != nil {
		t.Fatalf("choose the System tool: %v", err)
	}
	tt.Frame()
	if !tt.HasText("system-panel") {
		t.Fatalf("the System tool did not open below the strip: %v", tt.Texts())
	}
}

// TestSidePanelSystemStripScrollsAtTheMinimumWidth drives the System Tool in
// the real side panel at its 280-DIP minimum: even the icon-only strip
// overflows there, so choosing the last tab reveals it wholly before the tab
// layout button and a Collapsed Tabs trigger, both outside the scroll.
func TestSidePanelSystemStripScrollsAtTheMinimumWidth(t *testing.T) {
	m := locales.In("en")
	prefs := &sysManagerLayoutPrefs{settings: domainpreferences.Defaults()}
	managerService := &sysManagerLayoutService{snapshot: sysManagerLayoutFixture(4)}
	manager := uisysmanager.NewState(managerService,
		sysManagerLayoutHosts{items: []domainvault.Host{
			{ID: "h1", Label: "Alpha", Hostname: "host-1"},
		}}, prefs, uisysmanager.WithClock(func() time.Time { return sysManagerLayoutNow }))
	t.Cleanup(manager.Close)

	sessions := appsession.NewService(multiFactory{})
	t.Cleanup(sessions.CloseAll)
	s := state.NewState(
		state.WithSessions(sessions, func(session *appsession.Session) (state.Surface, error) {
			return sessionui.New(session)
		}),
		state.WithFilesPanel(state.FilesPanel{View: func(float32) func(*ui.Context) {
			return func(c *ui.Context) { ui.Text(c, "files-panel") }
		}}),
		state.WithSideTools(state.SideTool{ID: state.SideSystem,
			View: func(target state.SideTarget, width float32) func(*ui.Context) {
				host := string(target.Host)
				if host == "" {
					host = "h1" // the local Session's panel samples this computer
				}
				manager.ShowHost(host)
				return uisysmanager.SideView(manager, width)
			},
			Hide: manager.Hide}),
	)
	graph := &workspaceViewGraph{state: s}
	if err := s.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 1)
	if !s.ToggleActiveFiles() {
		t.Fatal("the side panel did not open")
	}
	active, _ := s.TabID(s.ActiveTab())
	s.SetSideTool(active, state.SideSystem)
	*s.FilesWidth(1280) = 280

	tt := ui.NewTester(View(s), 1280, 800)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Find(m.SysManagerSystemTabs()); !ok {
		t.Fatalf("the side panel does not show the System Tab Strip: %v", tt.Texts())
	}

	manager.SetTab(appsysmanager.TabGPU)
	tt.Frame()
	tt.Frame()
	strip, ok := tt.Find(m.SysManagerSystemTabs())
	if !ok || strip.W <= 0 {
		t.Fatalf("the side strip is not rendered: %v", tt.Texts())
	}
	if strip.W > 280 {
		t.Fatalf("the side strip is %.0f DIPs wide, past the panel minimum: %+v",
			strip.W, strip)
	}
	gpu, ok := tt.Find(m.SysManagerTabGPU())
	if !ok || gpu.W <= 0 {
		t.Fatalf("the active GPU tab is not rendered: %v", tt.Texts())
	}
	layout, ok := tt.Find(m.SysManagerTabsLayout())
	if !ok || layout.W <= 0 {
		t.Fatalf("the tab layout button is not rendered: %v", tt.Texts())
	}
	if gpu.X <= strip.X+0.5 {
		t.Fatalf("the side strip did not scroll to the active GPU tab: GPU at %.0f, strip at %.0f",
			gpu.X, strip.X)
	}
	if gpu.X < strip.X-0.5 || gpu.X+gpu.W > layout.X+0.5 {
		t.Fatalf("GPU %.0f..%.0f is not whole before the layout button %.0f..%.0f",
			gpu.X, gpu.X+gpu.W, layout.X, layout.X+layout.W)
	}
	if layout.X+layout.W > strip.X+strip.W+0.5 {
		t.Fatalf("the layout button %.0f..%.0f crosses the side strip %.0f..%.0f",
			layout.X, layout.X+layout.W, strip.X, strip.X+strip.W)
	}

	// Collapsing Docker draws the trigger after the layout button, and the
	// menu it opens is titled as an overflow of the strip, not a Side Panel
	// More.
	if err := tt.Click(m.SysManagerTabsLayout()); err != nil {
		t.Fatalf("open the tab layout menu: %v", err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem(m.SysManagerTabDocker(), m.SysManagerCollapseTab()); err != nil {
		t.Fatalf("choose Docker > Collapse: %v", err)
	}
	tt.Frame()
	more, ok := tt.Find(m.SysManagerMoreTabs())
	if !ok || more.W <= 0 {
		t.Fatalf("the Collapsed Tabs trigger is not rendered: %v", tt.Texts())
	}
	layout, ok = tt.Find(m.SysManagerTabsLayout())
	if !ok || more.X < layout.X+layout.W-0.5 {
		t.Fatalf("the Collapsed Tabs trigger at %.0f does not follow the layout button %+v",
			more.X, layout)
	}
	if more.X+more.W > strip.X+strip.W+0.5 {
		t.Fatalf("the Collapsed Tabs trigger %.0f..%.0f crosses the side strip %.0f..%.0f",
			more.X, more.X+more.W, strip.X, strip.X+strip.W)
	}
	if err := tt.Click(m.SysManagerMoreTabs()); err != nil {
		t.Fatalf("open the Collapsed Tabs menu: %v", err)
	}
	tt.Frame()
	if !tt.HasText(m.SysManagerCollapsedTabs()) {
		t.Fatalf("the Collapsed Tabs menu has no title: %v", tt.Texts())
	}
}
