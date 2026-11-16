package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

// waitForTabPanes waits until every Pane of a Tab has its live Surface.
func waitForTabPanes(t *testing.T, s *shellstate.State, tab, want int) {
	t.Helper()
	waitFor(t, "the Tab's Pane surfaces", func() bool {
		s.Flush()
		root, _, ok := s.TabTree(tab)
		if !ok {
			return false
		}
		live := 0
		for _, id := range domainworkspace.CollectPaneIDs(root) {
			if info, ok := s.PaneInfoAt(tab, id); ok && info.Surface != nil {
				live++
			}
		}
		return live == want
	})
}

// tabPaneSurfaces snapshots one Tab's Pane surfaces, so a test can prove the
// restore kept the same Sessions.
func tabPaneSurfaces(t *testing.T, s *shellstate.State, tab int) map[model.PaneID]shellstate.Surface {
	t.Helper()
	root, _, ok := s.TabTree(tab)
	if !ok {
		t.Fatalf("no Tab at %d", tab)
	}
	surfaces := make(map[model.PaneID]shellstate.Surface, len(domainworkspace.CollectPaneIDs(root)))
	for _, id := range domainworkspace.CollectPaneIDs(root) {
		info, ok := s.PaneInfoAt(tab, id)
		if !ok || info.Surface == nil {
			t.Fatalf("the Pane %q has no Surface", id)
		}
		surfaces[id] = info.Surface
	}
	return surfaces
}

// nativeStyle is a test Interface Style that asks for the platform's own look,
// so a renderer can report which style a frame ran under.
type nativeStyle struct{ kit.Style }

func (nativeStyle) Native() bool { return true }

// TestTopdownModeShowsTheFullShell proves the top-down route renders the
// complete application in the Panel: the Tab bar, the home and SFTP Tabs, and
// the active terminal. It must not start or render the separate Quake
// Workspace, and the hidden main window must draw only its neutral
// placeholder, so one Surface is never rendered twice.
func TestTopdownModeShowsTheFullShell(t *testing.T) {
	q := newQuakeGraph(t)
	installMainShellView(q)
	q.mode = appdesktop.HotkeyModeTopdown
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	main.Show()
	main.Focus()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	q.state.RenamePane(0, mustFocusedPane(t, q.state, 0), "main-alpha")

	q.manager.ShowDropdown()
	panel := dropdownWindow(t, q.windowGraph)
	if main.IsVisible() {
		t.Fatal("top-down left the main window visible while the Panel owns the Workspace")
	}
	if !q.manager.DropdownOwnsMain() {
		t.Fatal("top-down did not transfer the Workspace surfaces to the Panel")
	}
	if q.opener.opens != 0 || q.quake.TabCount() != 0 {
		t.Fatalf("top-down started a Quake workspace: %d opens, %d Tabs",
			q.opener.opens, q.quake.TabCount())
	}

	m := locales.In("en")
	tester := render(panel)
	texts := tester.Texts()
	if !tester.HasText("main-alpha") {
		t.Fatalf("the top-down Panel did not render the main shell's Pane header: %v", texts)
	}
	if !tester.HasText("terminal") {
		t.Fatalf("the top-down Panel did not render the main Workspace's Surface: %v", texts)
	}
	if !tester.HasText(m.NavVault()) || !tester.HasText(m.NavSftp()) {
		t.Fatalf("the top-down Panel lacks the window's Tab bar: %v", texts)
	}
	if tester.HasText("quake-terminal-1") {
		t.Fatalf("the top-down Panel rendered the Quake workspace: %v", texts)
	}
	if got := followTextCount(render(main).Texts(), "terminal"); got != 0 {
		t.Fatalf("the hidden main window rendered the Surface %d times", got)
	}
	if got := followTextCount(texts, "terminal"); got != 1 {
		t.Fatalf("the top-down Panel rendered the Surface %d times, want 1: %v", got, texts)
	}

	// MyGo rebuilds the routed subtree every frame: the focus request must be
	// repeated rather than left to a pruned element's state.
	surface := q.shellSurfaces[0]
	if surface.focuses == 0 {
		t.Fatal("the top-down Panel never focused the main terminal")
	}
	before := surface.focuses
	render(panel)
	if got := surface.focuses; got <= before {
		t.Fatalf("focus requests = %d after another frame, want more than %d", got, before)
	}
}

// TestTopdownModeActsOnTheSameShellState proves the Panel is not a copy of
// the application: its Tab bar and home Tabs drive the one main Workspace, so
// a click changes the active index or opens SFTP exactly as it does in the
// main window.
func TestTopdownModeActsOnTheSameShellState(t *testing.T) {
	q := newQuakeGraph(t)
	installMainShellView(q)
	q.mode = appdesktop.HotkeyModeTopdown
	q.manager.OpenMain()
	q.manager.Main().Show()
	for index, name := range []string{"main-alpha", "main-beta"} {
		if err := q.state.NewTab(); err != nil {
			t.Fatalf("main NewTab %d: %v", index, err)
		}
		waitForSurface(t, q.state, index)
		q.state.RenamePane(index, mustFocusedPane(t, q.state, index), name)
	}
	q.state.SelectTab(1)

	q.manager.ShowDropdown()
	panel := dropdownWindow(t, q.windowGraph)
	tester := render(panel)
	if q.state.ActiveTab() != 1 {
		t.Fatalf("active Tab = %d before the click, want 1", q.state.ActiveTab())
	}
	if err := tester.Click("main-alpha"); err != nil {
		t.Fatalf("clicking the first work Tab in the Panel: %v", err)
	}
	if q.state.ActiveTab() != 0 {
		t.Fatalf("active Tab = %d after clicking the Panel's first Tab, want 0", q.state.ActiveTab())
	}
	if q.state.TabCount() != 2 {
		t.Fatalf("Tabs = %d after selecting in the Panel, want 2", q.state.TabCount())
	}

	tester = render(panel)
	if err := tester.Click(locales.In("en").NavSftp()); err != nil {
		t.Fatalf("clicking the SFTP Tab in the Panel: %v", err)
	}
	if !q.state.SFTPVisible() {
		t.Fatal("the Panel's SFTP Tab did not open the main Workspace's SFTP surface")
	}
}

// TestTopdownModeRestoresTheWorkspaceOnHide proves hiding the Panel returns
// the main window and leaves the Workspace untouched: Tab order, active index,
// split tree, and Session identity are the same as before the transfer.
func TestTopdownModeRestoresTheWorkspaceOnHide(t *testing.T) {
	q := newQuakeGraph(t)
	installMainShellView(q)
	q.mode = appdesktop.HotkeyModeTopdown
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	main.Show()
	main.Focus()

	if err := q.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	if err := q.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	waitForTabPanes(t, q.state, 0, 2)
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("second main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 1)
	q.state.SelectTab(0)

	tabID, _ := q.state.TabID(0)
	root, _, _ := q.state.TabTree(0)
	surfaces := tabPaneSurfaces(t, q.state, 0)
	active := q.state.ActiveTab()

	q.manager.ShowDropdown()
	panel := dropdownWindow(t, q.windowGraph)
	if !panel.IsVisible() {
		t.Fatal("top-down did not show the Panel")
	}
	q.manager.HideDropdown()

	if q.manager.DropdownOwnsMain() {
		t.Fatal("hiding the top-down Panel did not return the Workspace surfaces")
	}
	if !main.IsVisible() || !main.IsFocused() {
		t.Fatal("hiding the top-down Panel did not restore the visible main window")
	}
	if panel.IsVisible() {
		t.Fatal("hiding the top-down Panel left it on screen")
	}
	if q.state.TabCount() != 2 || q.state.ActiveTab() != active {
		t.Fatalf("hiding changed the Workspace: %d Tabs, active %d, want 2 and %d",
			q.state.TabCount(), q.state.ActiveTab(), active)
	}
	if id, _ := q.state.TabID(0); id != tabID {
		t.Fatal("hiding changed the first Tab identity")
	}
	if after, _, _ := q.state.TabTree(0); after != root {
		t.Fatal("hiding replaced the Tab's split tree")
	}
	after := tabPaneSurfaces(t, q.state, 0)
	for pane, surface := range surfaces {
		if after[pane] != surface {
			t.Fatalf("hiding replaced the Surface of Pane %q", pane)
		}
	}
	if got := followTextCount(render(main).Texts(), "terminal"); got != 2 {
		t.Fatalf("the restored main window rendered %d Pane surfaces, want 2", got)
	}

	// The next show reuses the same native Panel and transfers again.
	q.manager.ShowDropdown()
	if dropdownWindow(t, q.windowGraph) != panel || panel.IsDestroyed() {
		t.Fatal("the second top-down show recreated the native Panel")
	}
	if main.IsVisible() || !q.manager.DropdownOwnsMain() {
		t.Fatal("the second top-down show did not transfer ownership")
	}
	q.manager.HideDropdown()
}

// TestTopdownModeUsesTheTopdownDefaultHeight pins the geometry contract:
// top-down defaults to 80% of the work area, remains top-aligned and full
// width, and Quake keeps its own 50% default.
func TestTopdownModeUsesTheTopdownDefaultHeight(t *testing.T) {
	q := newQuakeGraph(t)
	q.mode = appdesktop.HotkeyModeTopdown
	q.manager.ShowDropdown()
	want := windowBounds{X: 0, Y: 25, Width: 1920, Height: 832}
	if got := dropdownWindow(t, q.windowGraph).bounds; got != want {
		t.Fatalf("top-down bounds = %+v, want the work area's full width and 80%% height %+v", got, want)
	}

	q.mode = appdesktop.HotkeyModeQuake
	q.source = appdesktop.QuakeSourceOwn
	q.manager.HideDropdown()
	q.manager.ShowDropdown()
	if got := dropdownWindow(t, q.windowGraph).bounds.Height; got != 520 {
		t.Fatalf("quake height = %d after the top-down show, want its own 50%% default 520", got)
	}
}

// TestTopdownModeReroutesTheSamePanelAcrossModes proves the Panel keeps one
// native window and one root Content: switching between the Quake workspace,
// the top-down full shell, and the follow view only changes what the root
// renders.
func TestTopdownModeReroutesTheSamePanelAcrossModes(t *testing.T) {
	q := newQuakeGraph(t)
	installMainShellView(q)
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	main.Show()
	main.Focus()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	q.state.RenamePane(0, mustFocusedPane(t, q.state, 0), "main-alpha")
	pane := mustFocusedPane(t, q.state, 0)
	before, _ := q.state.PaneInfoAt(0, pane)
	surface := before.Surface

	q.mode = appdesktop.HotkeyModeQuake
	q.source = appdesktop.QuakeSourceOwn
	q.manager.ShowDropdown()
	panel := dropdownWindow(t, q.windowGraph)
	if !render(panel).HasText("quake-terminal-1") {
		t.Fatalf("quake mode did not render its workspace: %v", render(panel).Texts())
	}
	q.manager.HideDropdown()

	q.mode = appdesktop.HotkeyModeTopdown
	q.manager.ShowDropdown()
	if dropdownWindow(t, q.windowGraph) != panel || panel.IsDestroyed() {
		t.Fatal("switching to top-down recreated the native Panel")
	}
	tester := render(panel)
	if !tester.HasText("main-alpha") || !tester.HasText("terminal") {
		t.Fatalf("top-down did not render the full main shell: %v", tester.Texts())
	}
	if tester.HasText("quake-terminal-1") {
		t.Fatalf("top-down kept rendering the Quake workspace: %v", tester.Texts())
	}
	q.manager.HideDropdown()

	q.mode = appdesktop.HotkeyModeQuake
	q.source = appdesktop.QuakeSourceFollow
	q.manager.ShowDropdown()
	if dropdownWindow(t, q.windowGraph) != panel || panel.IsDestroyed() {
		t.Fatal("switching to follow recreated the native Panel")
	}
	if tester := render(panel); !tester.HasText("main-alpha") {
		t.Fatalf("follow did not render the main active Tab after top-down: %v", tester.Texts())
	}
	// The routed subtree is pruned between frames, but the Session and its
	// Surface live on the app state and survive every route change.
	if after, ok := q.state.PaneInfoAt(0, pane); !ok || after.Surface != surface {
		t.Fatal("a mode switch replaced the main Workspace's Surface")
	}
	if q.state.TabCount() != 1 {
		t.Fatalf("a mode switch changed the main Workspace to %d Tabs", q.state.TabCount())
	}
	q.manager.HideDropdown()

	// Returning to Quake's own workspace reuses the Panel and its Session.
	q.source = appdesktop.QuakeSourceOwn
	q.manager.ShowDropdown()
	if dropdownWindow(t, q.windowGraph) != panel || panel.IsDestroyed() {
		t.Fatal("returning to quake mode recreated the native Panel")
	}
	if tester := render(panel); !tester.HasText("quake-terminal-1") {
		t.Fatalf("quake mode did not render its workspace again: %v", tester.Texts())
	}
	q.manager.HideDropdown()
}

// TestTopdownModeFollowsLiveThemeAndStyle proves the Panel's top-down content
// takes the live widget theme and Interface Style on every frame, and the
// focused terminal keeps taking the keyboard.
func TestTopdownModeFollowsLiveThemeAndStyle(t *testing.T) {
	q := newQuakeGraph(t)
	installMainShellView(q)
	q.mode = appdesktop.HotkeyModeTopdown
	q.manager.OpenMain()
	q.manager.Main().Show()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)

	theme := ui.LightTheme()
	theme.Background = ui.RGB(1, 2, 3)
	q.manager.SetThemeProvider(func() *ui.Theme { return theme })
	q.manager.ShowDropdown()
	panel := dropdownWindow(t, q.windowGraph)
	surface := q.shellSurfaces[0]

	render(panel)
	if surface.themeBackground != theme.Background {
		t.Fatalf("top-down terminal theme = %v, want the live theme's %v",
			surface.themeBackground, theme.Background)
	}

	theme = ui.LightTheme()
	theme.Background = ui.RGB(4, 5, 6)
	render(panel)
	if surface.themeBackground != theme.Background {
		t.Fatalf("top-down terminal theme after the live change = %v, want %v",
			surface.themeBackground, theme.Background)
	}

	style := nativeStyle{Style: kit.Classic}
	q.manager.SetStyleProvider(func() kit.Style { return style })
	render(panel)
	if surface.style == nil || !surface.style.Native() {
		t.Fatalf("top-down renderer ran under %v, want the live native style", surface.style)
	}
}

// TestTopdownModeShowsTheAppLockGate proves the Panel's top-down content is
// behind the same shared App Lock gate as every window: the lock screen
// replaces the shell, and unlocking brings the same shell back.
func TestTopdownModeShowsTheAppLockGate(t *testing.T) {
	var q *quakeGraph
	gate := lockedGate(t, func() {
		if q != nil {
			q.manager.InvalidateAll()
		}
	})
	q = newQuakeGraphWithGate(t, gate)
	installMainShellView(q)
	q.mode = appdesktop.HotkeyModeTopdown
	q.manager.OpenMain()
	q.manager.Main().Show()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)

	q.manager.ShowDropdown()
	panel := dropdownWindow(t, q.windowGraph)
	tester := render(panel)
	m := locales.In("en")
	if !tester.HasText(m.AppLockUnlock()) {
		t.Fatalf("the locked top-down Panel did not show the gate: %v", tester.Texts())
	}
	if tester.HasText("terminal") {
		t.Fatalf("the locked top-down Panel rendered the shell: %v", tester.Texts())
	}

	tester.Type("secret")
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if !tester.HasText("terminal") {
		t.Fatalf("unlocking did not release the top-down shell: %v", tester.Texts())
	}
}
