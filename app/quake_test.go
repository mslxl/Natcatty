package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appsession "github.com/mslxl/natcatty/application/session"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
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
	service   *appsession.Service
	opens     int
	hostOpens []domainvault.Host
}

func (o *graphQuakeOpener) OpenLocal() (*appsession.Session, error) {
	o.opens++
	return o.service.OpenLocal(context.Background())
}

func (o *graphQuakeOpener) OpenHost(host domainvault.Host) (*appsession.Session, error) {
	o.hostOpens = append(o.hostOpens, host)
	return o.service.Open(context.Background(), appsession.StartSpec{
		Kind:     appsession.KindSSH,
		Label:    host.Label,
		HostID:   host.ID,
		Hostname: host.Hostname,
		Port:     host.Port,
		Username: host.Username,
	})
}

// quakeGraph is a window graph with the composition root's Quake wiring
// mirrored: the same show hook, bar, and content route the app installs.
type quakeGraph struct {
	*windowGraph
	quake    *quakeui.State
	opener   *graphQuakeOpener
	surfaces []*quakeTestSurface
	mode     appdesktop.HotkeyMode
	source   appdesktop.QuakeSource
	// settings carries the dropdown geometry and hide switches; the mode and
	// source fields above stay authoritative for routing, as the app's
	// service does.
	settings appdesktop.Settings
	hosts    []domainvault.Host
}

// Hosts implements the Quake Host picker's consumer-owned port with the
// graph's fake Host list.
func (q *quakeGraph) Hosts() []domainvault.Host { return q.hosts }

func newQuakeGraph(t *testing.T) *quakeGraph {
	t.Helper()
	return newQuakeGraphWithGate(t, nil)
}

func newQuakeGraphWithGate(t *testing.T, gate *uiapplock.Gate) *quakeGraph {
	t.Helper()
	graph := newWindowGraph(t, gate)
	screen := &fakeScreen{cursor: mygo.Point{X: 400, Y: 300}, displays: []mygo.Display{primaryDisplay()}}
	graph.manager.SetScreenProvider(screen)
	q := &quakeGraph{
		windowGraph: graph,
		opener:      &graphQuakeOpener{service: graph.service},
		mode:        appdesktop.HotkeyModeQuake,
		source:      appdesktop.QuakeSourceOwn,
		settings:    appdesktop.Defaults("windows"),
	}
	q.quake = quakeui.NewState(q.opener, func(*appsession.Session) (quakeui.Surface, error) {
		surface := &quakeTestSurface{id: len(q.surfaces) + 1}
		q.surfaces = append(q.surfaces, surface)
		return surface, nil
	},
		quakeui.WithHosts(q),
		quakeui.WithRedraw(func() { graph.manager.InvalidateAll() }),
		quakeui.WithEmpty(func() { graph.manager.HideDropdown() }),
	)
	t.Cleanup(q.quake.Shutdown)
	wireDropdownQuake(graph.manager, q.quake, graph.state,
		func() appdesktop.HotkeyMode { return q.mode },
		func() appdesktop.QuakeSource { return q.source },
		func() appdesktop.Settings {
			current := q.settings
			current.HotkeyMode = q.mode
			current.QuakeSource = q.source
			return current
		},
		func() {
			graph.manager.HideDropdown()
			graph.manager.FocusMain()
		},
	)
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

// quakeTestHosts returns two saved Hosts for the Host picker.
func quakeTestHosts() []domainvault.Host {
	return []domainvault.Host{
		{ID: "host-alpha", Label: "Alpha", Hostname: "alpha.example", Port: 22, Username: "ops"},
		{ID: "host-beta", Label: "Beta", Hostname: "beta.example", Port: 2222, Username: "root"},
	}
}

// TestDropdownQuakeHostPickerOpensAHostTabAndLeavesTheMainWorkspaceUntouched
// covers a Host selection through the real Panel route: the shared opener is
// used, the Tab is labelled by its Host, and the main Workspace keeps its Tab
// order and Session.
func TestDropdownQuakeHostPickerOpensAHostTabAndLeavesTheMainWorkspaceUntouched(t *testing.T) {
	q := newQuakeGraph(t)
	graph := q.windowGraph
	q.hosts = quakeTestHosts()
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	mainTab := graph.state.ActiveTab()
	mainSnapshot, _ := graph.state.ActiveSnapshot()

	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)
	tester := render(window)
	m := locales.In("en")
	if err := tester.Click(m.QuakeHostPicker()); err != nil {
		t.Fatalf("opening the Host picker: %v", err)
	}
	tester.Frame()
	if !q.quake.PickerOpen() {
		t.Fatalf("the Host picker did not open: %v", tester.Texts())
	}
	if !tester.HasText("Beta") {
		t.Fatalf("the picker lists no saved Hosts: %v", tester.Texts())
	}
	if err := tester.Click("Beta"); err != nil {
		t.Fatalf("selecting the Beta Host: %v", err)
	}
	tester.Frame()

	if len(q.opener.hostOpens) != 1 || q.opener.hostOpens[0].ID != "host-beta" {
		t.Fatalf("shared opener Hosts = %+v, want Beta", q.opener.hostOpens)
	}
	if q.quake.TabCount() != 2 {
		t.Fatalf("Quake Tabs = %d, want the local Shell and the Host", q.quake.TabCount())
	}
	tabs := q.quake.Tabs()
	if tabs[1].Label != "Beta" || tabs[1].HostID != "host-beta" {
		t.Fatalf("the Host Tab = %+v, want Beta", tabs[1])
	}
	if !window.shown {
		t.Fatal("selecting a Host hid the Panel")
	}
	if !tester.HasText("Beta") || !tester.HasText("quake-terminal-2") {
		t.Fatalf("the Host Tab or its terminal is missing: %v", tester.Texts())
	}

	if graph.state.TabCount() != 1 || graph.state.ActiveTab() != mainTab {
		t.Fatalf("the main Workspace changed: %d Tabs, active %d",
			graph.state.TabCount(), graph.state.ActiveTab())
	}
	after, _ := graph.state.ActiveSnapshot()
	if after.ID != mainSnapshot.ID {
		t.Fatalf("the main active Session changed: %+v, want %s", after.ID, mainSnapshot.ID)
	}
}

// TestDropdownQuakeEscapeClosesThePickerBeforeThePanel pins the Escape order:
// the first request dismisses the open picker and keeps the Panel visible,
// and only the next one hides the Panel. A press inside the picker never
// hides the Panel.
func TestDropdownQuakeEscapeClosesThePickerBeforeThePanel(t *testing.T) {
	q := newQuakeGraph(t)
	graph := q.windowGraph
	q.hosts = quakeTestHosts()
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)
	tester := render(window)
	m := locales.In("en")

	if err := tester.Click(m.QuakeHostPicker()); err != nil {
		t.Fatalf("opening the Host picker: %v", err)
	}
	tester.Frame()
	if !q.quake.PickerOpen() {
		t.Fatal("the Host picker did not open")
	}

	// The first Escape, as the window receives it when the page leaves it
	// unhandled, closes the picker and does not hide the Panel.
	window.Close()
	if q.quake.PickerOpen() {
		t.Fatal("the first Escape left the picker open")
	}
	if !window.shown {
		t.Fatal("the first Escape hid the Panel instead of closing the picker")
	}
	if q.quake.TabCount() != 1 {
		t.Fatalf("cancelling the picker changed the Tabs: %d", q.quake.TabCount())
	}

	// The second Escape hides the same Panel without destroying it.
	window.Close()
	if window.shown {
		t.Fatal("the second Escape did not hide the Panel")
	}
	if window.destroyed {
		t.Fatal("the Escape close request destroyed the Panel")
	}

	// Re-open the Panel and the picker. Pressing inside the picker keeps both
	// it and the Panel open.
	graph.manager.ToggleDropdown()
	tester = render(window)
	if err := tester.Click(m.QuakeHostPicker()); err != nil {
		t.Fatalf("re-opening the Host picker: %v", err)
	}
	tester.Frame()
	if err := tester.Click(m.QuakeHostsLabel()); err != nil {
		t.Fatalf("pressing inside the picker: %v", err)
	}
	tester.Frame()
	if !q.quake.PickerOpen() {
		t.Fatal("a press inside the picker closed it")
	}
	if !window.shown {
		t.Fatal("a press inside the picker hid the Panel")
	}
	if q.quake.TabCount() != 1 {
		t.Fatalf("a press inside the picker changed the Tabs: %d", q.quake.TabCount())
	}

	// The view's own Escape closes the picker without hiding the Panel, which
	// is the path a focused picker takes.
	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	if q.quake.PickerOpen() {
		t.Fatal("the picker's Escape did not close it")
	}
	if !window.shown {
		t.Fatal("the picker's Escape hid the Panel")
	}
}

// TestDropdownQuakeEscapeHidesThePanelWhenTheRouteHasNoPicker pins the
// cross-mode close order: a picker left open when the Panel hid without
// dismissing it is not rendered by the follow or top-down route, so the first
// Escape must hide the Panel instead of being consumed by an invisible picker.
func TestDropdownQuakeEscapeHidesThePanelWhenTheRouteHasNoPicker(t *testing.T) {
	routes := []struct {
		name  string
		apply func(*quakeGraph)
	}{
		{name: "follow source", apply: func(q *quakeGraph) { q.source = appdesktop.QuakeSourceFollow }},
		{name: "topdown mode", apply: func(q *quakeGraph) { q.mode = appdesktop.HotkeyModeTopdown }},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			q := newQuakeGraph(t)
			graph := q.windowGraph
			q.hosts = quakeTestHosts()

			graph.manager.ToggleDropdown()
			window := dropdownWindow(t, graph)
			q.quake.OpenPicker()
			if !q.quake.PickerOpen() {
				t.Fatal("the Host picker did not open")
			}

			// Blur and the hotkey hide the Panel without cancelling its
			// flyouts, leaving the picker open in the Quake state.
			graph.manager.HideDropdown()
			if !q.quake.PickerOpen() {
				t.Fatal("hiding the Panel closed the Host picker")
			}

			route.apply(q)
			graph.manager.ShowDropdown()
			if !window.shown {
				t.Fatal("the Panel did not show in the switched route")
			}

			// The switched route renders no picker, so this Escape is the
			// Panel's own close request.
			window.Close()
			if window.shown {
				t.Fatal("the first Escape was swallowed by a picker that is not rendered in this route")
			}
			if window.destroyed {
				t.Fatal("the Escape close request destroyed the Panel")
			}

			// Returning to the own route starts without the stale picker.
			q.mode = appdesktop.HotkeyModeQuake
			q.source = appdesktop.QuakeSourceOwn
			graph.manager.ShowDropdown()
			if q.quake.PickerOpen() {
				t.Fatal("the stale picker reappeared over the own workspace")
			}
			if tester := render(window); !tester.HasText(locales.In("en").LocalSessionTab(1)) {
				t.Fatalf("the own route did not render its workspace: %v", tester.Texts())
			}
		})
	}
}

// TestDropdownQuakeEscapeHidesAPanelLockedOverAnOpenPicker proves a picker
// hidden behind the App Lock screen does not consume the first Escape: the
// gate replaces the Quake content with the lock screen, so the Panel's own
// close request must hide it.
func TestDropdownQuakeEscapeHidesAPanelLockedOverAnOpenPicker(t *testing.T) {
	var q *quakeGraph
	gate := lockedGate(t, func() {
		if q != nil {
			q.manager.InvalidateAll()
		}
	})
	q = newQuakeGraphWithGate(t, gate)
	graph := q.windowGraph
	q.hosts = quakeTestHosts()

	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)
	q.quake.OpenPicker()
	if !q.quake.PickerOpen() {
		t.Fatal("the Host picker did not open")
	}

	window.Close()
	if window.shown {
		t.Fatal("the first Escape was swallowed by a picker hidden behind the App Lock screen")
	}
	if window.destroyed {
		t.Fatal("the Escape close request destroyed the Panel")
	}
}
