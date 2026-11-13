package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
	quakeui "github.com/mslxl/natcatty/ui/quake"
)

// quakeTestSurface is the fake terminal the Quake workspace mounts. It records
// focus requests so a test can prove the active Tab takes the keyboard.
type quakeTestSurface struct {
	id      int
	focuses int
	closes  int
}

func (s *quakeTestSurface) View(c *ui.Context) ui.Element {
	return ui.Text(c, fmt.Sprintf("quake-terminal-%d", s.id)).Fill()
}

func (s *quakeTestSurface) ViewFocused(c *ui.Context, focused bool) ui.Element {
	if focused {
		s.focuses++
	}
	return s.View(c)
}

func (s *quakeTestSurface) Close() { s.closes++ }

// graphQuakeOpener is the Quake SessionOpener over the graph's shared Session
// service. It counts opens because the first-show contract is exactly "one
// local Shell, reused until the workspace empties".
type graphQuakeOpener struct {
	service *appsession.Service
	opens   int
}

func (o *graphQuakeOpener) OpenLocal() (*appsession.Session, error) {
	o.opens++
	return o.service.OpenLocal(context.Background())
}

// quakeGraph is a window graph with the composition root's Quake wiring
// mirrored: the same show hook, bar, and content route the app installs.
type quakeGraph struct {
	*windowGraph
	quake    *quakeui.State
	opener   *graphQuakeOpener
	surfaces []*quakeTestSurface
	mode     appdesktop.HotkeyMode
}

func newQuakeGraph(t *testing.T) *quakeGraph {
	t.Helper()
	graph := newWindowGraph(t, nil)
	screen := &fakeScreen{cursor: mygo.Point{X: 400, Y: 300}, displays: []mygo.Display{primaryDisplay()}}
	graph.manager.SetScreenProvider(screen)
	q := &quakeGraph{
		windowGraph: graph,
		opener:      &graphQuakeOpener{service: graph.service},
		mode:        appdesktop.HotkeyModeQuake,
	}
	q.quake = quakeui.NewState(q.opener, func(*appsession.Session) (quakeui.Surface, error) {
		surface := &quakeTestSurface{id: len(q.surfaces) + 1}
		q.surfaces = append(q.surfaces, surface)
		return surface, nil
	},
		quakeui.WithRedraw(func() { graph.manager.InvalidateAll() }),
		quakeui.WithEmpty(func() { graph.manager.HideDropdown() }),
	)
	t.Cleanup(q.quake.Shutdown)
	wireDropdownQuake(graph.manager, q.quake, func() appdesktop.HotkeyMode { return q.mode })
	return q
}

// TestDropdownQuakeOpensOneLocalShellAndReusesItAcrossToggles covers the first
// show, the reused Session and Surface, and the unchanged main Workspace.
func TestDropdownQuakeOpensOneLocalShellAndReusesItAcrossToggles(t *testing.T) {
	q := newQuakeGraph(t)
	graph := q.windowGraph
	if graph.state.TabCount() != 0 {
		t.Fatalf("the main Workspace started with %d Tabs", graph.state.TabCount())
	}

	graph.manager.ToggleDropdown()
	if !graph.manager.DropdownVisible() {
		t.Fatal("the first Quake toggle did not show the Panel")
	}
	if q.quake.TabCount() != 1 {
		t.Fatalf("Quake Tabs = %d, want the first local Shell", q.quake.TabCount())
	}
	if q.opener.opens != 1 {
		t.Fatalf("local Shell opens = %d, want 1", q.opener.opens)
	}
	firstTab, _ := q.quake.Active()
	firstSession := firstTab.Session
	window := dropdownWindow(t, graph)
	tester := render(window)
	if !tester.HasText(locales.In("en").LocalSessionTab(1)) {
		t.Fatalf("the Panel's first frame has no Quake Tab: %v", tester.Texts())
	}
	if tester.HasText("quake-terminal-1") == false {
		t.Fatalf("the Panel's first frame has no terminal: %v", tester.Texts())
	}
	if q.surfaces[0].focuses == 0 {
		t.Fatal("the active Quake terminal never took the keyboard")
	}

	// Hiding and showing again reuses the same native window, Tab, Session,
	// and Surface.
	graph.manager.ToggleDropdown()
	if graph.manager.DropdownVisible() {
		t.Fatal("the second toggle did not hide the Panel")
	}
	graph.manager.ToggleDropdown()
	if !graph.manager.DropdownVisible() {
		t.Fatal("the third toggle did not show the Panel again")
	}
	if dropdownWindow(t, graph) != window {
		t.Fatal("the second show created a new native window")
	}
	if q.opener.opens != 1 || q.quake.TabCount() != 1 {
		t.Fatalf("a re-show opened %d shells and left %d Tabs, want the original one",
			q.opener.opens, q.quake.TabCount())
	}
	again, _ := q.quake.Active()
	if again.Session != firstSession || again.Surface != firstTab.Surface {
		t.Fatal("a re-show replaced the Session or Surface")
	}

	// Own mode never touches the main Workspace.
	if graph.state.TabCount() != 0 || graph.state.ActiveTab() != -1 {
		t.Fatalf("the main Workspace changed: %d Tabs, active %d",
			graph.state.TabCount(), graph.state.ActiveTab())
	}
}

// TestDropdownQuakeClosingTheLastTabHidesThePanel proves the empty workspace
// hides the same Panel, and the next show opens a fresh local Shell.
func TestDropdownQuakeClosingTheLastTabHidesThePanel(t *testing.T) {
	q := newQuakeGraph(t)
	graph := q.windowGraph
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)

	tester := render(window)
	if err := tester.Click(locales.In("en").MenuCloseTab()); err != nil {
		t.Fatalf("clicking the Quake Tab close control: %v", err)
	}
	if q.quake.TabCount() != 0 {
		t.Fatalf("Quake Tabs = %d after closing the last one, want 0", q.quake.TabCount())
	}
	if window.shown {
		t.Fatal("closing the last Quake Tab left the Panel visible")
	}
	if !window.destroyed && dropdownWindow(t, graph) != window {
		t.Fatal("closing the last Tab replaced the native window")
	}

	graph.manager.ToggleDropdown()
	if q.opener.opens != 2 {
		t.Fatalf("local Shell opens = %d after re-showing, want a new one", q.opener.opens)
	}
	if q.quake.TabCount() != 1 {
		t.Fatalf("Quake Tabs = %d after re-showing, want 1", q.quake.TabCount())
	}
}

// TestDropdownQuakeLeavesTheMainWorkspaceUntouched proves Quake Tabs are a
// separate Workspace: the main Tab order, active index, and Session identity
// survive every Quake action.
func TestDropdownQuakeLeavesTheMainWorkspaceUntouched(t *testing.T) {
	q := newQuakeGraph(t)
	graph := q.windowGraph
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	mainTab := graph.state.ActiveTab()
	mainSnapshot, ok := graph.state.ActiveSnapshot()
	if !ok {
		t.Fatal("the main Workspace has no active Session")
	}

	graph.manager.ToggleDropdown()
	if err := q.quake.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	q.quake.SelectTab(0)
	graph.manager.ToggleDropdown()
	graph.manager.ToggleDropdown()

	if graph.state.TabCount() != 1 || graph.state.ActiveTab() != mainTab {
		t.Fatalf("the main Workspace changed: %d Tabs, active %d, want 1 and %d",
			graph.state.TabCount(), graph.state.ActiveTab(), mainTab)
	}
	after, ok := graph.state.ActiveSnapshot()
	if !ok || after.ID != mainSnapshot.ID {
		t.Fatalf("the main active Session changed: %+v, want %s", after.ID, mainSnapshot.ID)
	}
}

// TestDropdownQuakeRoutesLiveAndKeepsItsWorkspaceAcrossModes proves the
// panel's root content is routed live: quake mode shows the Quake Workspace,
// another mode keeps the neutral placeholder, and switching away and back
// reuses the same native Panel, Tab, Session, and Surface.
func TestDropdownQuakeRoutesLiveAndKeepsItsWorkspaceAcrossModes(t *testing.T) {
	q := newQuakeGraph(t)
	graph := q.windowGraph

	graph.manager.ToggleDropdown()
	if q.opener.opens != 1 || q.quake.TabCount() != 1 {
		t.Fatalf("quake mode opened %d shells and %d Tabs, want one", q.opener.opens, q.quake.TabCount())
	}
	before, _ := q.quake.Active()
	window := dropdownWindow(t, graph)
	if tester := render(window); !tester.HasText(locales.In("en").LocalSessionTab(1)) {
		t.Fatalf("quake mode did not route the Quake workspace: %v", tester.Texts())
	}

	// The app hides the Panel when the mode changes. Top-down mode renders
	// the neutral placeholder and must not start a Quake shell by itself.
	q.mode = appdesktop.HotkeyModeTopdown
	graph.manager.HideDropdown()
	graph.manager.ToggleDropdown()
	if q.opener.opens != 1 {
		t.Fatalf("top-down mode reopened a Quake shell: %d opens", q.opener.opens)
	}
	if tester := render(window); tester.HasText(locales.In("en").LocalSessionTab(1)) {
		t.Fatalf("top-down mode rendered the Quake workspace: %v", tester.Texts())
	}

	// Switching back restores the same workspace in the same native window.
	q.mode = appdesktop.HotkeyModeQuake
	graph.manager.HideDropdown()
	graph.manager.ToggleDropdown()
	if dropdownWindow(t, graph) != window {
		t.Fatal("switching modes recreated the native Panel")
	}
	if q.opener.opens != 1 || q.quake.TabCount() != 1 {
		t.Fatalf("returning to quake mode opened %d shells and left %d Tabs, want the original one",
			q.opener.opens, q.quake.TabCount())
	}
	after, _ := q.quake.Active()
	if after.Session != before.Session || after.Surface != before.Surface {
		t.Fatal("a mode switch replaced the Quake Session or Surface")
	}
	if tester := render(window); !tester.HasText(locales.In("en").LocalSessionTab(1)) {
		t.Fatalf("quake mode did not route the Quake workspace after the switch: %v", tester.Texts())
	}
}
