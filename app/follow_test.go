package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

// installMainShellView mirrors the composition root's main content provider:
// the shell view while the main window owns the Workspace surfaces, and a
// neutral placeholder while the Dropdown Panel owns them.
func installMainShellView(q *quakeGraph) {
	mainShellView := shell.View(q.state)
	q.manager.SetMainContentProvider(func() func(*ui.Context) {
		return func(c *ui.Context) {
			if q.manager.DropdownOwnsMain() {
				shell.MainPlaceholder(c)
				return
			}
			mainShellView(c)
		}
	})
}

// followTextCount counts exact text nodes in a rendered frame, so a test can
// prove one Surface is drawn by exactly one window.
func followTextCount(texts []string, want string) int {
	count := 0
	for _, text := range texts {
		if text == want {
			count++
		}
	}
	return count
}

// TestFollowModeTransfersOwnershipAndRestoresTheMainWindow is the central
// follow contract: showing the Panel hides the main window and moves the
// Workspace surfaces into the Panel; hiding the Panel returns them without
// changing the Tab order, active index, or Session identity.
func TestFollowModeTransfersOwnershipAndRestoresTheMainWindow(t *testing.T) {
	q := newQuakeGraph(t)
	installMainShellView(q)
	q.source = appdesktop.QuakeSourceFollow
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	main.Show()
	main.Focus()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	tabID, _ := q.state.TabID(0)
	root, _, _ := q.state.TabTree(0)
	pane, _ := q.state.FocusedPaneID(0)
	info, _ := q.state.PaneInfoAt(0, pane)
	surface := info.Surface
	q.state.RenamePane(0, pane, "follow-alpha")

	q.manager.ShowDropdown()
	dropdown := dropdownWindow(t, q.windowGraph)
	if main.IsVisible() {
		t.Fatal("follow mode left the main window visible while the Panel owns the Workspace")
	}
	if !q.manager.DropdownOwnsMain() {
		t.Fatal("follow mode did not transfer the Workspace surfaces to the Panel")
	}
	if !dropdown.IsVisible() {
		t.Fatal("follow mode did not show the Panel")
	}
	if q.opener.opens != 0 || q.quake.TabCount() != 0 {
		t.Fatalf("follow mode created a Quake workspace: %d opens, %d Tabs",
			q.opener.opens, q.quake.TabCount())
	}

	// The main window is a neutral placeholder; the Panel owns the only
	// rendering of the Workspace Surface.
	mainTester := render(main)
	panelTester := render(dropdown)
	mainTexts := mainTester.Texts()
	panelTexts := panelTester.Texts()
	if got := followTextCount(mainTexts, "terminal"); got != 0 {
		t.Fatalf("the main placeholder rendered the Surface %d times: %v", got, mainTexts)
	}
	if got := followTextCount(panelTexts, "terminal"); got != 1 {
		t.Fatalf("the follow Panel rendered the Surface %d times, want 1: %v", got, panelTexts)
	}
	if !panelTester.HasText("follow-alpha") {
		t.Fatalf("the follow Panel has no mini Tab strip entry: %v", panelTexts)
	}

	// Hiding returns ownership, restores the visible main window, and leaves
	// the Workspace exactly as it was.
	q.manager.HideDropdown()
	if q.manager.DropdownOwnsMain() {
		t.Fatal("hiding the Panel did not return the Workspace surfaces")
	}
	if !main.IsVisible() || !main.IsFocused() {
		t.Fatal("hiding the follow Panel did not restore the visible main window")
	}
	if dropdown.IsVisible() {
		t.Fatal("hiding the follow Panel left it on screen")
	}
	if got := followTextCount(render(main).Texts(), "terminal"); got != 1 {
		t.Fatalf("the restored main window rendered the Surface %d times, want 1", got)
	}
	if q.state.TabCount() != 1 || q.state.ActiveTab() != 0 {
		t.Fatalf("hiding changed the Workspace: %d Tabs, active %d", q.state.TabCount(), q.state.ActiveTab())
	}
	if id, _ := q.state.TabID(0); id != tabID {
		t.Fatal("hiding changed the Tab identity")
	}
	if after, _, _ := q.state.TabTree(0); after != root {
		t.Fatal("hiding replaced the Tab's split tree")
	}
	afterInfo, _ := q.state.PaneInfoAt(0, pane)
	if afterInfo.Surface != surface {
		t.Fatal("hiding replaced the Tab's Session surface")
	}
	if len(q.state.DetachedTabIDs()) != 0 {
		t.Fatalf("hiding detached Tabs: %v", q.state.DetachedTabIDs())
	}

	// The next show reuses the same native Panel and transfers again.
	q.manager.ShowDropdown()
	if dropdownWindow(t, q.windowGraph) != dropdown || dropdown.IsDestroyed() {
		t.Fatal("the second follow show recreated the native Panel")
	}
	if main.IsVisible() || !q.manager.DropdownOwnsMain() {
		t.Fatal("the second follow show did not transfer ownership again")
	}
	q.manager.HideDropdown()
	if !main.IsVisible() || q.manager.DropdownOwnsMain() {
		t.Fatal("the second hide did not return ownership and show the main window")
	}
}

// TestFollowModeLeavesAPreviouslyHiddenMainWindowHidden proves hiding the
// Panel restores only the main window's previous state: a main window the user
// had hidden stays hidden, while ownership still returns.
func TestFollowModeLeavesAPreviouslyHiddenMainWindowHidden(t *testing.T) {
	q := newQuakeGraph(t)
	installMainShellView(q)
	q.source = appdesktop.QuakeSourceFollow
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	q.manager.ShowDropdown()
	if main.IsVisible() {
		t.Fatal("the follow show made a previously hidden main window visible")
	}
	if !q.manager.DropdownOwnsMain() {
		t.Fatal("the follow show did not transfer ownership")
	}

	q.manager.HideDropdown()
	if q.manager.DropdownOwnsMain() {
		t.Fatal("hiding did not return ownership")
	}
	if main.IsVisible() {
		t.Fatal("hiding the Panel popped open a main window that was hidden before")
	}
}

// TestQuakeOwnModeKeepsTheMainWindowVisible proves the own source never
// transfers ownership: the main window renders its Workspace while the Panel
// renders the separate Quake Workspace.
func TestQuakeOwnModeKeepsTheMainWindowVisible(t *testing.T) {
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

	q.manager.ShowDropdown()
	dropdown := dropdownWindow(t, q.windowGraph)
	if !main.IsVisible() {
		t.Fatal("quake + own hid the main window")
	}
	if q.manager.DropdownOwnsMain() {
		t.Fatal("quake + own transferred the Workspace surfaces")
	}
	mainTester := render(main)
	panelTester := render(dropdown)
	if got := followTextCount(mainTester.Texts(), "terminal"); got != 1 {
		t.Fatalf("the main window rendered its Surface %d times, want 1", got)
	}
	if !panelTester.HasText("quake-terminal-1") {
		t.Fatalf("the Panel did not render the Quake workspace: %v", panelTester.Texts())
	}
	if panelTester.HasText("main-alpha") {
		t.Fatal("the Quake Panel rendered the main Workspace")
	}

	q.manager.HideDropdown()
	if !main.IsVisible() || q.manager.DropdownOwnsMain() {
		t.Fatal("hiding the own-mode Panel moved the main window or ownership")
	}
}

// TestFollowMiniTabsOnlyChangeTheActiveIndex proves selecting a mini Tab is a
// plain selection: no detach, no Session restart, no split replacement.
func TestFollowMiniTabsOnlyChangeTheActiveIndex(t *testing.T) {
	q := newQuakeGraph(t)
	installMainShellView(q)
	q.source = appdesktop.QuakeSourceFollow
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	main.Show()
	for index := 0; index < 2; index++ {
		if err := q.state.NewTab(); err != nil {
			t.Fatalf("main NewTab %d: %v", index, err)
		}
	}
	waitForSurface(t, q.state, 0)
	waitForSurface(t, q.state, 1)
	for index, title := range []string{"alpha", "beta"} {
		q.state.RenamePane(index, mustFocusedPane(t, q.state, index), title)
	}
	ids := []model.TabID{followTabID(t, q.state, 0), followTabID(t, q.state, 1)}
	root1, _, _ := q.state.TabTree(0)
	root2, _, _ := q.state.TabTree(1)
	info1, _ := q.state.PaneInfoAt(0, mustFocusedPane(t, q.state, 0))
	info2, _ := q.state.PaneInfoAt(1, mustFocusedPane(t, q.state, 1))

	q.manager.ShowDropdown()
	dropdown := dropdownWindow(t, q.windowGraph)
	tester := render(dropdown)
	if !tester.HasText("alpha") || !tester.HasText("beta") {
		t.Fatalf("the mini Tab strip lacks both Tabs: %v", tester.Texts())
	}
	if err := tester.Click("alpha"); err != nil {
		t.Fatalf("clicking the first mini Tab: %v", err)
	}
	tester.Frame()

	if active := q.state.ActiveTab(); active != 0 {
		t.Fatalf("active Tab = %d after selecting the first mini Tab", active)
	}
	if followTabID(t, q.state, 0) != ids[0] || followTabID(t, q.state, 1) != ids[1] {
		t.Fatal("selecting a mini Tab changed the Workspace Tab order")
	}
	if after, _, _ := q.state.TabTree(0); after != root1 {
		t.Fatal("selecting a mini Tab replaced the first split tree")
	}
	if after, _, _ := q.state.TabTree(1); after != root2 {
		t.Fatal("selecting a mini Tab replaced the second split tree")
	}
	if q.state.TabCount() != 2 || len(q.state.DetachedTabIDs()) != 0 {
		t.Fatalf("tabs = %d, detached = %v; selection must not detach",
			q.state.TabCount(), q.state.DetachedTabIDs())
	}
	if after, _ := q.state.PaneInfoAt(0, mustFocusedPane(t, q.state, 0)); after.Surface != info1.Surface {
		t.Fatal("selecting a mini Tab replaced the first Session surface")
	}
	if after, _ := q.state.PaneInfoAt(1, mustFocusedPane(t, q.state, 1)); after.Surface != info2.Surface {
		t.Fatal("selecting a mini Tab replaced the second Session surface")
	}
	if dropdownWindow(t, q.windowGraph) != dropdown {
		t.Fatal("selecting a mini Tab recreated the native Panel")
	}
}

// TestFollowModeEmptyWorkspaceOffersTheMainWindow covers the non-terminal
// empty state and its action: it returns to the main window without creating a
// Tab or leaving the Panel owning an empty Workspace.
func TestFollowModeEmptyWorkspaceOffersTheMainWindow(t *testing.T) {
	q := newQuakeGraph(t)
	installMainShellView(q)
	q.source = appdesktop.QuakeSourceFollow
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	main.Show()
	main.Focus()

	q.manager.ShowDropdown()
	dropdown := dropdownWindow(t, q.windowGraph)
	messages := locales.In("en")
	tester := render(dropdown)
	if !tester.HasText(messages.FollowEmptyTitle()) || !tester.HasText(messages.FollowOpenMain()) {
		t.Fatalf("the follow empty state is missing: %v", tester.Texts())
	}
	if err := tester.Click(messages.FollowOpenMain()); err != nil {
		t.Fatalf("clicking Open main window: %v", err)
	}
	if dropdown.IsVisible() {
		t.Fatal("Open main window left the follow Panel visible")
	}
	if q.manager.DropdownOwnsMain() {
		t.Fatal("Open main window did not return the Workspace surfaces")
	}
	if !main.IsVisible() || !main.IsFocused() {
		t.Fatal("Open main window did not show and focus the main window")
	}
	if q.state.TabCount() != 0 || q.opener.opens != 0 {
		t.Fatalf("Open main window changed the Workspace: %d Tabs, %d Quake opens",
			q.state.TabCount(), q.opener.opens)
	}
}

// TestTopdownModeTransfersOwnershipWithAPlaceholder covers this ticket's
// share of top-down mode: it moves the surfaces so the main window does not
// render them twice, while its own renderer is still a later ticket. It must
// not activate a Quake workspace.
func TestTopdownModeTransfersOwnershipWithAPlaceholder(t *testing.T) {
	q := newQuakeGraph(t)
	installMainShellView(q)
	q.mode = appdesktop.HotkeyModeTopdown
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	main.Show()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	q.state.RenamePane(0, mustFocusedPane(t, q.state, 0), "main-alpha")

	q.manager.ShowDropdown()
	dropdown := dropdownWindow(t, q.windowGraph)
	if main.IsVisible() || !q.manager.DropdownOwnsMain() {
		t.Fatal("top-down did not transfer the Workspace surfaces")
	}
	tester := render(dropdown)
	if tester.HasText("terminal") || tester.HasText("main-alpha") {
		t.Fatalf("top-down rendered a Workspace instead of its placeholder: %v", tester.Texts())
	}
	if q.opener.opens != 0 || q.quake.TabCount() != 0 {
		t.Fatalf("top-down started a Quake workspace: %d opens, %d Tabs",
			q.opener.opens, q.quake.TabCount())
	}

	q.manager.HideDropdown()
	if !main.IsVisible() || q.manager.DropdownOwnsMain() {
		t.Fatal("hiding top-down did not restore the main window and ownership")
	}
}

// TestQuakeSourceChangeRetiresThePanelAndReturnsOwnership wires the production
// listener: changing the source while the follow Panel owns the surfaces hides
// it and restores the main window, rather than leaving the user with no visible
// window.
func TestQuakeSourceChangeRetiresThePanelAndReturnsOwnership(t *testing.T) {
	controller, graph, _, _, _, _, _ := newDesktopTestController(t)
	graph.manager.SetDropdownOwnershipProvider(func() bool {
		settings := controller.service.Settings()
		return settings.HotkeyMode == appdesktop.HotkeyModeTopdown ||
			(settings.HotkeyMode == appdesktop.HotkeyModeQuake &&
				settings.QuakeSource == appdesktop.QuakeSourceFollow)
	})
	main := graph.manager.Main()
	main.Show()
	main.Focus()

	if err := controller.service.SetQuakeSource(appdesktop.QuakeSourceFollow); err != nil {
		t.Fatalf("SetQuakeSource(follow): %v", err)
	}
	graph.manager.ShowDropdown()
	if main.IsVisible() || !graph.manager.DropdownOwnsMain() {
		t.Fatal("follow did not transfer ownership in the desktop wiring")
	}
	dropdown := graph.manager.dropdown

	if err := controller.service.SetQuakeSource(appdesktop.QuakeSourceOwn); err != nil {
		t.Fatalf("SetQuakeSource(own): %v", err)
	}
	if graph.manager.DropdownVisible() {
		t.Fatal("a source change left the old Panel on screen")
	}
	if graph.manager.DropdownOwnsMain() {
		t.Fatal("a source change did not return the Workspace surfaces")
	}
	if !main.IsVisible() || !main.IsFocused() {
		t.Fatal("a source change did not restore the main window")
	}

	// The next show routes the new source through the same native Panel.
	graph.manager.ShowDropdown()
	if graph.manager.dropdown != dropdown || dropdown.IsDestroyed() {
		t.Fatal("a source change recreated the native Panel")
	}
	if graph.manager.DropdownOwnsMain() || !main.IsVisible() {
		t.Fatal("own after a source change took the surfaces or hid the main window")
	}
	graph.manager.HideDropdown()
}

func mustFocusedPane(t *testing.T, s *shellstate.State, index int) model.PaneID {
	t.Helper()
	pane, ok := s.FocusedPaneID(index)
	if !ok {
		t.Fatalf("no focused Pane at %d", index)
	}
	return pane
}

func followTabID(t *testing.T, s *shellstate.State, index int) model.TabID {
	t.Helper()
	id, ok := s.TabID(index)
	if !ok {
		t.Fatalf("no Tab at %d", index)
	}
	return id
}
