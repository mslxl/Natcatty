package phoneshell

// Tablet and unfolded-phone layout tests. They drive the real Shell and
// composition seam with the offscreen ui.Tester at a tablet size and at the
// ordinary 390x844 phone size, and change the window size between frames the
// way rotation, a fold, or a resize does.

import (
	"testing"

	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// setWindow changes the tester's window and settles the frames that follow, so
// the next Find sees the layout that size produces.
func (f *phoneFixture) setWindow(t *testing.T, width, height int) {
	t.Helper()
	f.tester.SetSize(width, height)
	f.tester.Frame()
	f.settle()
	f.tester.Frame()
}

// openSplitPair opens one Host, adds a second as another Pane of the same Tab,
// and waits for both Sessions. The split tree is the phone's usual side-by-side
// one: the second Pane is inserted to the right of the first.
func (f *phoneFixture) openSplitPair(t *testing.T) (first, second *fakeHandle) {
	t.Helper()
	first = f.openSession(t, "prod box")
	f.await(t, "the first Pane", func() bool { return f.tester.HasText("prod output") })
	f.addPaneToTab(t, "prod box", "console")
	f.await(t, "the second Pane", func() bool { return f.opener.count() == 2 && f.opener.liveViews() == 1 })
	return first, f.opener.last()
}

// tabTree returns the first Tab's split tree and Pane count.
func (f *phoneFixture) tabTree(t *testing.T) (*domainworkspace.Node, int) {
	t.Helper()
	f.shell.sessions.mu.Lock()
	defer f.shell.sessions.mu.Unlock()
	if len(f.shell.sessions.tabs) == 0 {
		t.Fatal("the shell has no Tab")
	}
	return f.shell.sessions.tabs[0].root, len(f.shell.sessions.tabs[0].panes)
}

// TestTabletRendersSplitTreeInItsDirection pins the first tablet story: the
// tree's leaves are all on screen, and a vertical split puts them side by side
// while a horizontal split stacks them.
func TestTabletRendersSplitTreeInItsDirection(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	fixture.setWindow(t, 1024, 768)
	fixture.openSplitPair(t)

	fixture.await(t, "both Pane tiles", func() bool {
		return fixture.tester.HasText("Pane prod box") && fixture.tester.HasText("Pane console")
	})
	// The in-Tab switcher is the narrow layout; the split is not both at once.
	if fixture.tester.HasText("Show prod box") {
		t.Fatalf("the tablet split also drew the Pane switcher; texts: %q", fixture.tester.Texts())
	}
	// Only the focused Pane owns the live terminal view, so only its output is
	// drawn. The other tile names its Pane and says it keeps running.
	if !fixture.tester.HasText("console output") {
		t.Fatalf("the focused Pane's output is missing; texts: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText("prod output") {
		t.Fatal("the hidden Pane's terminal was drawn next to the focused one")
	}
	if !fixture.tester.HasText("Tap to focus. This Session keeps running.") {
		t.Fatalf("the hidden tile does not say it keeps running; texts: %q", fixture.tester.Texts())
	}

	prodRect, ok := fixture.tester.Find("Pane prod box")
	if !ok {
		t.Fatalf("no tile for the first Pane; texts: %q", fixture.tester.Texts())
	}
	consoleRect, ok := fixture.tester.Find("Pane console")
	if !ok {
		t.Fatalf("no tile for the second Pane; texts: %q", fixture.tester.Texts())
	}
	if prodRect.X+prodRect.W > consoleRect.X+1 {
		t.Fatalf("a vertical split did not put the first Pane to the left: %+v then %+v", prodRect, consoleRect)
	}
	if prodRect.Y != consoleRect.Y {
		t.Fatalf("a vertical split put the panes at different heights: %+v then %+v", prodRect, consoleRect)
	}

	// The same leaves under a horizontal split stack instead.
	fixture.shell.sessions.mu.Lock()
	tab := fixture.shell.sessions.tabs[0]
	tab.root = domainworkspace.NewSplitNode("split-h", domainworkspace.Horizontal, 50,
		domainworkspace.NewPaneNode(tab.panes[0].id),
		domainworkspace.NewPaneNode(tab.panes[1].id))
	fixture.shell.sessions.mu.Unlock()
	fixture.tester.Frame()

	prodRect, _ = fixture.tester.Find("Pane prod box")
	consoleRect, _ = fixture.tester.Find("Pane console")
	if prodRect.Y+prodRect.H > consoleRect.Y+1 {
		t.Fatalf("a horizontal split did not stack the first Pane above the second: %+v then %+v", prodRect, consoleRect)
	}
	if prodRect.X != consoleRect.X {
		t.Fatalf("a horizontal split put the panes at different columns: %+v then %+v", prodRect, consoleRect)
	}
}

// TestTabletPaneTapMovesFocusAndKeepsHiddenPaneRunning pins the second tablet
// story: tapping a tile moves focus, the one live view follows it, and the
// Pane that lost focus keeps running its Session and replays when focused.
func TestTabletPaneTapMovesFocusAndKeepsHiddenPaneRunning(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	fixture.setWindow(t, 1024, 768)
	prod, console := fixture.openSplitPair(t)
	fixture.await(t, "both Pane tiles", func() bool {
		return fixture.tester.HasText("Pane prod box") && fixture.tester.HasText("Pane console")
	})

	// The hidden Pane produced output while out of focus. It is not drawn, its
	// transport is alive, and the phone still holds exactly one live view.
	prod.emit("prod kept running\r\n")
	fixture.tester.Frame()
	if fixture.tester.HasText("prod kept running") {
		t.Fatal("a hidden tile drew its Pane's output")
	}
	if prod.isEnded() {
		t.Fatal("hiding the first Pane in the split ended its Session")
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens in the split, want the focused Pane's", got)
	}

	// A tap on the tile moves focus; the one live view moves with it and the
	// hidden Pane's output is replayed without another dial.
	if err := fixture.tester.Click("Pane prod box"); err != nil {
		t.Fatalf("focus the first Pane: %v", err)
	}
	fixture.await(t, "the first Pane's replay", func() bool {
		return prod.isAttached() && !console.isAttached() &&
			opener.count() == 2 && fixture.tester.HasText("prod kept running")
	})
	if prod.isEnded() || console.isEnded() {
		t.Fatal("switching focus in the split ended a Session")
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens after the tap, want 1", got)
	}

	// And back, to prove focus moves both ways and neither view is duplicated.
	if err := fixture.tester.Click("Pane console"); err != nil {
		t.Fatalf("focus the second Pane: %v", err)
	}
	fixture.await(t, "the second Pane's return", func() bool {
		return console.isAttached() && !prod.isAttached() && opener.count() == 2
	})
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens after switching back, want 1", got)
	}
}

// TestTabletDividerDragWritesRatioToSharedTree pins the third story: dragging
// the divider changes the visible proportion and writes the new ratio into the
// shared split tree and the persisted Workspace payload.
func TestTabletDividerDragWritesRatioToSharedTree(t *testing.T) {
	store := &fakeSessionStore{}
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener, WithSessionStore(store))
	fixture.setWindow(t, 1024, 768)
	fixture.openSplitPair(t)
	fixture.await(t, "both Pane tiles", func() bool {
		return fixture.tester.HasText("Pane prod box") && fixture.tester.HasText("Pane console")
	})

	before, ok := fixture.tester.Find("Pane prod box")
	if !ok {
		t.Fatalf("no tile before the drag; texts: %q", fixture.tester.Texts())
	}
	divider, ok := fixture.tester.Find("Divider")
	if !ok {
		t.Fatalf("the split drew no draggable divider; texts: %q", fixture.tester.Texts())
	}
	fromX, fromY := divider.X+divider.W/2, divider.Y+divider.H/2
	toX := fromX + 160
	fixture.tester.Press(fromX, fromY)
	for step := 1; step <= 4; step++ {
		fixture.tester.Move(fromX+(toX-fromX)*float32(step)/4, fromY)
	}
	fixture.tester.Release(toX, fromY)
	fixture.tester.Frame()

	tree, panes := fixture.tabTree(t)
	if panes != 2 {
		t.Fatalf("the drag left %d Panes, want 2", panes)
	}
	if tree == nil || tree.IsPane() {
		t.Fatalf("the drag collapsed the split tree: %+v", tree)
	}
	if tree.Ratio <= domainworkspace.DefaultRatio {
		t.Fatalf("dragging right left the first child's ratio at %v, want above 50", tree.Ratio)
	}
	payload := store.last()
	if len(payload.Tabs) != 1 || payload.Tabs[0].Root == nil {
		t.Fatalf("the drag persisted no split tree: %+v", payload)
	}
	if got := payload.Tabs[0].Root.Ratio; got != tree.Ratio {
		t.Fatalf("the persisted ratio is %v, the tree says %v", got, tree.Ratio)
	}

	after, ok := fixture.tester.Find("Pane prod box")
	if !ok {
		t.Fatalf("no tile after the drag; texts: %q", fixture.tester.Texts())
	}
	if after.W <= before.W {
		t.Fatalf("the dragged first Pane is %v wide, not wider than its %v", after.W, before.W)
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens after the drag, want 1", got)
	}
}

// TestTabletHorizontalDividerDragWritesRatio covers the other direction: a
// top/bottom split's divider moves vertically and writes its ratio back just
// as the side-by-side one does.
func TestTabletHorizontalDividerDragWritesRatio(t *testing.T) {
	store := &fakeSessionStore{}
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener, WithSessionStore(store))
	fixture.setWindow(t, 1024, 768)
	fixture.openSplitPair(t)
	fixture.await(t, "both Pane tiles", func() bool {
		return fixture.tester.HasText("Pane prod box") && fixture.tester.HasText("Pane console")
	})

	fixture.shell.sessions.mu.Lock()
	tab := fixture.shell.sessions.tabs[0]
	tab.root = domainworkspace.NewSplitNode("split-h", domainworkspace.Horizontal, 50,
		domainworkspace.NewPaneNode(tab.panes[0].id),
		domainworkspace.NewPaneNode(tab.panes[1].id))
	fixture.shell.sessions.mu.Unlock()
	fixture.tester.Frame()

	before, ok := fixture.tester.Find("Pane prod box")
	if !ok {
		t.Fatalf("no tile before the drag; texts: %q", fixture.tester.Texts())
	}
	divider, ok := fixture.tester.Find("Divider")
	if !ok {
		t.Fatalf("the horizontal split drew no draggable divider; texts: %q", fixture.tester.Texts())
	}
	fromX, fromY := divider.X+divider.W/2, divider.Y+divider.H/2
	toY := fromY + 120
	fixture.tester.Press(fromX, fromY)
	for step := 1; step <= 4; step++ {
		fixture.tester.Move(fromX, fromY+(toY-fromY)*float32(step)/4)
	}
	fixture.tester.Release(fromX, toY)
	fixture.tester.Frame()

	tree, panes := fixture.tabTree(t)
	if panes != 2 {
		t.Fatalf("the drag left %d Panes, want 2", panes)
	}
	if tree == nil || tree.IsPane() || tree.Direction != domainworkspace.Horizontal {
		t.Fatalf("the horizontal split tree changed shape: %+v", tree)
	}
	if tree.Ratio <= domainworkspace.DefaultRatio {
		t.Fatalf("dragging down left the first child's ratio at %v, want above 50", tree.Ratio)
	}
	payload := store.last()
	if len(payload.Tabs) != 1 || payload.Tabs[0].Root == nil {
		t.Fatalf("the drag persisted no split tree: %+v", payload)
	}
	if got := payload.Tabs[0].Root.Ratio; got != tree.Ratio {
		t.Fatalf("the persisted ratio is %v, the tree says %v", got, tree.Ratio)
	}
	after, ok := fixture.tester.Find("Pane prod box")
	if !ok {
		t.Fatalf("no tile after the drag; texts: %q", fixture.tester.Texts())
	}
	if after.H <= before.H {
		t.Fatalf("the dragged top Pane is %v high, not higher than its %v", after.H, before.H)
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens after the drag, want 1", got)
	}
}

// TestWidthChangeConvertsSplitAndSwitcherWithoutLosingPanes pins the fourth
// story: narrowing a tablet, folding an unfolded phone, and unfolding again
// convert between the split and the in-Tab switcher without losing Panes, and
// the hidden Panes keep running across every conversion.
func TestWidthChangeConvertsSplitAndSwitcherWithoutLosingPanes(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	fixture.setWindow(t, 1024, 768)
	prod, console := fixture.openSplitPair(t)
	fixture.await(t, "the tablet split", func() bool {
		return fixture.tester.HasText("Pane prod box") && fixture.tester.HasText("Pane console")
	})

	// A width change is the whole layout rule: the same Tab now draws the
	// in-Tab switcher, and no Pane or Session was lost.
	fixture.setWindow(t, 390, 844)
	fixture.await(t, "the phone switcher", func() bool {
		return fixture.tester.HasText("Show prod box") && fixture.tester.HasText("Show console")
	})
	if fixture.tester.HasText("Pane prod box") || fixture.tester.HasText("Pane console") {
		t.Fatalf("the phone layout still drew Pane tiles; texts: %q", fixture.tester.Texts())
	}
	if _, panes := fixture.tabTree(t); panes != 2 {
		t.Fatalf("narrowing lost Panes: %d left", panes)
	}
	if opener.count() != 2 || prod.isEnded() || console.isEnded() {
		t.Fatal("narrowing dialed or ended a Session")
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens on the phone, want 1", got)
	}

	// Output arriving while folded still keeps both Sessions running.
	prod.emit("prod while folded\r\n")
	fixture.tester.Frame()
	if prod.isEnded() || console.isEnded() {
		t.Fatal("a folded layout ended a Session")
	}

	// Unfolding is the reverse conversion: the tiles come back with the same
	// Panes, and the hidden Pane's output is still there to focus.
	fixture.setWindow(t, 780, 844)
	fixture.await(t, "the unfolded split", func() bool {
		return fixture.tester.HasText("Pane prod box") && fixture.tester.HasText("Pane console")
	})
	if _, panes := fixture.tabTree(t); panes != 2 {
		t.Fatalf("unfolding lost Panes: %d left", panes)
	}
	if err := fixture.tester.Click("Pane prod box"); err != nil {
		t.Fatalf("focus the Pane that ran while folded: %v", err)
	}
	fixture.await(t, "the folded Pane's replay", func() bool {
		return prod.isAttached() && !console.isAttached() && fixture.tester.HasText("prod while folded")
	})

	// A landscape phone is wide but short; it is still a phone, not a tablet.
	fixture.setWindow(t, 844, 390)
	fixture.await(t, "the landscape phone switcher", func() bool {
		return fixture.tester.HasText("Show prod box") && fixture.tester.HasText("Show console")
	})
	if fixture.tester.HasText("Pane prod box") {
		t.Fatalf("a landscape phone drew the tablet split; texts: %q", fixture.tester.Texts())
	}
	if opener.count() != 2 || prod.isEnded() || console.isEnded() {
		t.Fatal("landscape conversion dialed or ended a Session")
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens in landscape, want 1", got)
	}
}
