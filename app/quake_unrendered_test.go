package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	"github.com/mslxl/natcatty/ui/locales"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

// TestQuakeRouteProvidersMarkTheBypassedViews proves the production providers
// tell each Quake runtime when its route drew no frame: a Tab opened while the
// follow route replaced both Quake views is warped by the first frame back,
// never replayed as stale strip or body motion.
func TestQuakeRouteProvidersMarkTheBypassedViews(t *testing.T) {
	q := newQuakeGraph(t)
	if err := q.quake.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	bar := q.manager.dropdownBar()
	content := q.manager.dropdownContent()
	if bar == nil || content == nil {
		t.Fatal("the composition root installed no bar or content provider")
	}
	barTester := ui.NewTester(bar, 800, 120)
	barTester.SetLocale("en")
	contentTester := ui.NewTester(content, 800, 300)
	contentTester.SetLocale("en")
	for range 2 {
		barTester.Frame()
		contentTester.Frame()
	}

	// The follow route renders the main Workspace instead; neither Quake view
	// builds a frame, so the providers mark both as unrendered.
	q.source = appdesktop.QuakeSourceFollow
	barTester.Frame()
	contentTester.Frame()

	// A Tab event is recorded while no Quake runtime renders it.
	if err := q.quake.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	waitForQuakeTabCount(t, q, 2)

	// The first own-route frame must warp both runtimes. Without the provider
	// notice the open would animate and leave a frame pending.
	q.source = appdesktop.QuakeSourceOwn
	barTester.Frame()
	if barTester.FramePending() {
		t.Fatal("the bypassed Quake bar replayed a stale Tab event")
	}
	contentTester.Frame()
	if contentTester.FramePending() {
		t.Fatal("the bypassed Quake body replayed a stale Tab event")
	}
	if !barTester.HasText(locales.In("en").LocalSessionTab(2)) {
		t.Fatalf("the restored bar did not draw the final row: %v", barTester.Texts())
	}
	if !contentTester.HasText("quake-terminal-2") || contentTester.HasText("quake-terminal-1") {
		t.Fatalf("the restored body did not draw the final Tab: %v", contentTester.Texts())
	}
}

// TestQuakeShowHookMarksViewsHiddenWhileThePanelWasClosed proves the show hook
// marks the Quake runtimes on every show, not only the own route's Activate.
// The testers never observe the window's hide event, so only the panel-level
// notice can make the first frame back warp.
func TestQuakeShowHookMarksViewsHiddenWhileThePanelWasClosed(t *testing.T) {
	q := newQuakeGraph(t)
	q.manager.ShowDropdown()
	bar := q.manager.dropdownBar()
	content := q.manager.dropdownContent()
	if bar == nil || content == nil {
		t.Fatal("the composition root installed no bar or content provider")
	}
	barTester := ui.NewTester(bar, 800, 120)
	barTester.SetLocale("en")
	contentTester := ui.NewTester(content, 800, 300)
	contentTester.SetLocale("en")
	for range 2 {
		barTester.Frame()
		contentTester.Frame()
	}

	q.manager.HideDropdown()
	if err := q.quake.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	waitForQuakeTabCount(t, q, 2)
	q.manager.ShowDropdown()

	barTester.Frame()
	if barTester.FramePending() {
		t.Fatal("the shown Panel's bar replayed the Tab event recorded while closed")
	}
	contentTester.Frame()
	if contentTester.FramePending() {
		t.Fatal("the shown Panel's body replayed the Tab event recorded while closed")
	}
	if !barTester.HasText(locales.In("en").LocalSessionTab(2)) {
		t.Fatalf("the shown bar did not draw the final row: %v", barTester.Texts())
	}
	if !contentTester.HasText("quake-terminal-2") {
		t.Fatalf("the shown body did not draw the final Tab: %v", contentTester.Texts())
	}
}

// mainFollowTester settles a main window into the follow route and returns its
// tester. The Panel is not shown yet, so the main view owns the Workspace.
func mainFollowTester(t *testing.T, q *quakeGraph) *ui.Tester {
	t.Helper()
	installMainShellView(q)
	q.source = appdesktop.QuakeSourceFollow
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	main.Show()
	main.Focus()
	tester := ui.NewTester(main.spec.Content, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	tester.Frame()
	return tester
}

// TestDropdownOwnershipMarksTheMainViewUnrendered proves the ownership rule
// records the main view's bypass at transfer: a split recorded while the Panel
// owns the Workspace is warped by the first restored main frame, not replayed.
func TestDropdownOwnershipMarksTheMainViewUnrendered(t *testing.T) {
	q := newQuakeGraph(t)
	mainTester := mainFollowTester(t, q)

	q.manager.ShowDropdown()
	if !q.manager.DropdownOwnsMain() {
		t.Fatal("the follow route did not take the Workspace surfaces")
	}
	if err := q.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	waitForTabPanes(t, q.state, 0, 2)

	q.manager.HideDropdown()
	mainTester.Frame()
	if mainTester.FramePending() {
		t.Fatal("the restored main window replayed the Pane event it never rendered")
	}
	if got := followTextCount(mainTester.Texts(), "terminal"); got != 2 {
		t.Fatalf("the restored main window drew %d Pane surfaces, want the final 2; texts %v",
			got, mainTester.Texts())
	}
}

// TestMainPlaceholderMarksTheMainViewUnrendered proves the main content
// provider itself records the bypass on every placeholder frame. The transfer
// notice is consumed first, so only the placeholder frame's own mark can make
// the restored frame warp.
func TestMainPlaceholderMarksTheMainViewUnrendered(t *testing.T) {
	q := newQuakeGraph(t)
	mainTester := mainFollowTester(t, q)

	q.manager.ShowDropdown()
	if !q.manager.DropdownOwnsMain() {
		t.Fatal("the follow route did not take the Workspace surfaces")
	}
	// Consume the ownership rule's notice; the placeholder frame below must
	// record the gap on its own.
	if !q.state.TakeMotionViewUnrendered(shellstate.MotionViewMain) {
		t.Fatal("the ownership rule did not mark the main view at transfer")
	}
	mainTester.Frame() // the separator's neutral placeholder

	if err := q.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	waitForTabPanes(t, q.state, 0, 2)

	q.manager.HideDropdown()
	mainTester.Frame()
	if mainTester.FramePending() {
		t.Fatal("the restored main window replayed the Pane event its placeholder did not mark")
	}
	if got := followTextCount(mainTester.Texts(), "terminal"); got != 2 {
		t.Fatalf("the restored main window drew %d Pane surfaces, want the final 2; texts %v",
			got, mainTester.Texts())
	}
}
