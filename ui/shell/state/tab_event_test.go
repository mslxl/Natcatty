package state

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/motion"
)

// mustEvent returns the latest motion event, failing when none was recorded.
func mustEvent(t *testing.T, s *State) motion.Event {
	t.Helper()
	event, ok := s.MotionEvent()
	if !ok {
		t.Fatalf("no motion event was recorded")
	}
	return event
}

// mustTab makes n work Tabs and returns their IDs in strip order. The last
// one is active.
func mustTab(t *testing.T, g *workspaceGraph, n int) []model.TabID {
	t.Helper()
	for range n {
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	ids := make([]model.TabID, 0, n)
	for index := range n {
		id, ok := g.state.TabID(index)
		if !ok {
			t.Fatalf("no Tab at index %d", index)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestTabOpenRecordsTheNewTab checks the open path: every Tab creator records
// TabOpen for the Tab it makes and leaves it active.
func TestTabOpenRecordsTheNewTab(t *testing.T) {
	g := newWorkspaceGraph(t)
	ids := mustTab(t, g, 2)
	event := mustEvent(t, g.state)
	if event.Kind != motion.TabOpen || event.TabID != ids[1] {
		t.Fatalf("the second Tab's event = %+v, want TabOpen of %q", event, ids[1])
	}
	if event.TargetTabID != "" {
		t.Fatalf("a TabOpen names a replacement Tab: %+v", event)
	}
	if g.state.ActiveTab() != 1 {
		t.Fatalf("the new Tab is not active")
	}

	// A duplicate is a new Tab too, inserted after its source and selected.
	if err := g.state.DuplicateTab(0); err != nil {
		t.Fatalf("DuplicateTab: %v", err)
	}
	event = mustEvent(t, g.state)
	if event.Kind != motion.TabOpen {
		t.Fatalf("DuplicateTab recorded %+v, want TabOpen", event)
	}
	duplicate, ok := g.state.TabID(1)
	if !ok || event.TabID != duplicate {
		t.Fatalf("DuplicateTab recorded %q, want the copy %q", event.TabID, duplicate)
	}
	if g.state.ActiveTab() != 1 {
		t.Fatalf("the duplicate is not active")
	}
}

// TestTabSwitchRecordsDirectionIncludingWrapping checks SelectTab's direction:
// +1 for a target later in the strip, -1 for one before it, and index order
// across the ends.
func TestTabSwitchRecordsDirectionIncludingWrapping(t *testing.T) {
	g := newWorkspaceGraph(t)
	ids := mustTab(t, g, 3)

	for _, tc := range []struct {
		index int
		want  int
	}{
		{0, -1}, // last to first
		{2, 1},  // first to last
		{0, -1},
		{1, 1},
	} {
		g.state.SelectTab(tc.index)
		event := mustEvent(t, g.state)
		if event.Kind != motion.TabSwitch || event.TabID != ids[tc.index] {
			t.Fatalf("SelectTab(%d) recorded %+v, want TabSwitch of %q", tc.index, event, ids[tc.index])
		}
		if event.Direction != tc.want {
			t.Fatalf("SelectTab(%d) direction = %d, want %d", tc.index, event.Direction, tc.want)
		}
	}

	// Choosing the already-active Tab changes no body and records nothing.
	before := mustEvent(t, g.state)
	g.state.SelectTab(g.state.ActiveTab())
	if after := mustEvent(t, g.state); after.Seq != before.Seq {
		t.Fatalf("selecting the active Tab recorded a new event: %+v", after)
	}
}

// TestFocusSessionRecordsTabSwitch checks the focus paths that do not go
// through SelectTab: focusing a Session in another Tab is a switch, while
// focusing one in the active Tab is not.
func TestFocusSessionRecordsTabSwitch(t *testing.T) {
	g := newWorkspaceGraph(t)
	ids := mustTab(t, g, 2)
	// Focus a Session in the active Tab: the body does not change.
	if !g.state.FocusSession(g.sessions[1].ID()) {
		t.Fatalf("FocusSession in the active Tab failed")
	}
	if event := mustEvent(t, g.state); event.Kind == motion.TabSwitch {
		t.Fatalf("focusing a Session in the active Tab recorded a TabSwitch: %+v", event)
	}
	// Focus the first Tab's Session: this changes the active Tab.
	if !g.state.FocusSession(g.sessions[0].ID()) {
		t.Fatalf("FocusSession in the other Tab failed")
	}
	event := mustEvent(t, g.state)
	if event.Kind != motion.TabSwitch || event.TabID != ids[0] || event.Direction != -1 {
		t.Fatalf("focusing a Pane in the previous Tab recorded %+v, want TabSwitch(-1) of %q", event, ids[0])
	}
}

// TestTabCloseRecordsTheReplacementAndDirection covers closing the current
// Tab onto its right neighbour, closing a last Tab onto its left one, and
// closing the only Tab onto Home.
func TestTabCloseRecordsTheReplacementAndDirection(t *testing.T) {
	g := newWorkspaceGraph(t)
	ids := mustTab(t, g, 3)

	// Close the middle, current Tab: the right neighbour replaces it.
	g.state.SelectTab(1)
	if !g.state.CloseActiveTab() {
		t.Fatalf("CloseActiveTab failed")
	}
	event := mustEvent(t, g.state)
	if event.Kind != motion.TabClose || event.TabID != ids[1] {
		t.Fatalf("closing the middle Tab recorded %+v, want TabClose of %q", event, ids[1])
	}
	if event.TargetTabID != ids[2] || event.Direction != 1 {
		t.Fatalf("the middle close targets %q with direction %d, want the right neighbour %q and +1",
			event.TargetTabID, event.Direction, ids[2])
	}
	g.state.ReleaseTabGhosts()

	// Close the last, current Tab: the left neighbour replaces it.
	if !g.state.CloseActiveTab() {
		t.Fatalf("closing the last Tab failed")
	}
	event = mustEvent(t, g.state)
	if event.TabID != ids[2] || event.TargetTabID != ids[0] || event.Direction != -1 {
		t.Fatalf("closing the last Tab recorded %+v, want TabClose of %q onto %q with -1",
			event, ids[2], ids[0])
	}
	g.state.ReleaseTabGhosts()

	// Close the only Tab: no replacement, so Home appears.
	if !g.state.CloseActiveTab() {
		t.Fatalf("closing the only Tab failed")
	}
	event = mustEvent(t, g.state)
	if event.TabID != ids[0] || event.TargetTabID != "" || event.Direction != -1 {
		t.Fatalf("closing the only Tab recorded %+v, want an empty target with -1", event)
	}
	g.state.ReleaseTabGhosts()
}

// TestTabCloseOfANonCurrentTabRecordsIt checks that closing a background Tab
// still records the TabClose the strip animates, without selecting it first.
func TestTabCloseOfANonCurrentTabRecordsIt(t *testing.T) {
	g := newWorkspaceGraph(t)
	ids := mustTab(t, g, 3)
	active := g.state.ActiveTab()
	if active != 2 {
		t.Fatalf("the last Tab is not active: %d", active)
	}
	if !g.state.CloseTab(0) {
		t.Fatalf("CloseTab(0) failed")
	}
	if g.state.ActiveTab() != 1 {
		t.Fatalf("closing a non-current Tab changed the active index to %d, want 1", g.state.ActiveTab())
	}
	if id, _ := g.state.TabID(1); id != ids[2] {
		t.Fatalf("closing a non-current Tab left the wrong Tab active: %q, want %q", id, ids[2])
	}
	event := mustEvent(t, g.state)
	if event.Kind != motion.TabClose || event.TabID != ids[0] {
		t.Fatalf("closing a non-current Tab recorded %+v, want TabClose of %q", event, ids[0])
	}
	if _, ok := g.state.TakeTabGhost(ids[0]); ok {
		t.Fatalf("closing a non-current Tab retained a body no view can draw")
	}
}

// TestCloseTabsRightRecordsOneCloseOrNone covers the two strip outcomes:
// closing exactly the active right neighbour records one TabClose, while
// closing several Tabs warps and records nothing.
func TestCloseTabsRightRecordsOneCloseOrNone(t *testing.T) {
	g := newWorkspaceGraph(t)
	ids := mustTab(t, g, 3)
	if !g.state.CloseTabsRight(1) {
		t.Fatalf("CloseTabsRight(1) failed")
	}
	event := mustEvent(t, g.state)
	if event.Kind != motion.TabClose || event.TabID != ids[2] || event.TargetTabID != ids[1] || event.Direction != -1 {
		t.Fatalf("closing one right Tab recorded %+v, want TabClose of %q onto %q with -1",
			event, ids[2], ids[1])
	}
	g.state.ReleaseTabGhosts()

	multi := newWorkspaceGraph(t)
	multiIDs := mustTab(t, multi, 3)
	// Make the leftmost Tab active so three Tabs close at once.
	multi.state.SelectTab(0)
	before := mustEvent(t, multi.state)
	if !multi.state.CloseTabsRight(0) {
		t.Fatalf("CloseTabsRight(0) failed")
	}
	if after := mustEvent(t, multi.state); after.Seq != before.Seq {
		t.Fatalf("closing several Tabs recorded %+v, want no per-Tab animation", after)
	}
	if multi.state.TabCount() != 1 {
		t.Fatalf("closing every right Tab left %d Tabs, want 1", multi.state.TabCount())
	}
	if left, _ := multi.state.TabID(0); left != multiIDs[0] {
		t.Fatalf("the kept Tab = %q, want %q", left, multiIDs[0])
	}
}

// TestBatchTabCloseRaisesTheBodyWarpSignal covers the dedicated no-event
// signal: closing several Tabs at once advances the body warp sequence, while
// every path that records a TabClose event leaves it alone. The motion event
// stream itself keeps its no-event expectation, so existing strip tests stand.
func TestBatchTabCloseRaisesTheBodyWarpSignal(t *testing.T) {
	t.Run("CloseOtherTabs", func(t *testing.T) {
		g := newWorkspaceGraph(t)
		ids := mustTab(t, g, 3)
		g.state.SelectTab(0)
		eventBefore := mustEvent(t, g.state)
		warpBefore := g.state.BodyWarpSeq()
		if !g.state.CloseOtherTabs(0) {
			t.Fatalf("CloseOtherTabs failed")
		}
		if event := mustEvent(t, g.state); event.Seq != eventBefore.Seq {
			t.Fatalf("CloseOtherTabs recorded %+v, want no motion event", event)
		}
		if got := g.state.BodyWarpSeq(); got != warpBefore+1 {
			t.Fatalf("CloseOtherTabs body warp seq = %d, want %d", got, warpBefore+1)
		}
		if g.state.TabCount() != 1 {
			t.Fatalf("CloseOtherTabs left %d Tabs, want 1", g.state.TabCount())
		}
		if id, _ := g.state.TabID(0); id != ids[0] {
			t.Fatalf("CloseOtherTabs kept %q, want %q", id, ids[0])
		}
	})

	t.Run("CloseTabsRight several", func(t *testing.T) {
		g := newWorkspaceGraph(t)
		ids := mustTab(t, g, 3)
		g.state.SelectTab(0)
		eventBefore := mustEvent(t, g.state)
		warpBefore := g.state.BodyWarpSeq()
		if !g.state.CloseTabsRight(0) {
			t.Fatalf("CloseTabsRight(0) failed")
		}
		if event := mustEvent(t, g.state); event.Seq != eventBefore.Seq {
			t.Fatalf("closing several right Tabs recorded %+v, want no motion event", event)
		}
		if got := g.state.BodyWarpSeq(); got != warpBefore+1 {
			t.Fatalf("the multi right close body warp seq = %d, want %d", got, warpBefore+1)
		}
		if g.state.TabCount() != 1 {
			t.Fatalf("closing every right Tab left %d Tabs, want 1", g.state.TabCount())
		}
		if id, _ := g.state.TabID(0); id != ids[0] {
			t.Fatalf("the kept Tab = %q, want %q", id, ids[0])
		}
	})

	t.Run("single closes record no warp", func(t *testing.T) {
		g := newWorkspaceGraph(t)
		mustTab(t, g, 3)
		warpBefore := g.state.BodyWarpSeq()
		// Exactly one right Tab: the single-Tab close animates through its
		// TabClose event, so the body warp signal must not advance.
		if !g.state.CloseTabsRight(1) {
			t.Fatalf("CloseTabsRight(1) failed")
		}
		g.state.ReleaseTabGhosts()
		if got := g.state.BodyWarpSeq(); got != warpBefore {
			t.Fatalf("a single right close advanced the body warp seq to %d, want %d", got, warpBefore)
		}
		// A close that records TabClose, current or not, is the event stream's
		// job, not the warp signal's.
		if !g.state.CloseTab(0) {
			t.Fatalf("CloseTab(0) failed")
		}
		g.state.ReleaseTabGhosts()
		if got := g.state.BodyWarpSeq(); got != warpBefore {
			t.Fatalf("a single close advanced the body warp seq to %d, want %d", got, warpBefore)
		}
	})
}

// TestCrossTabPaneMoveRecordsNoTabSwitch checks that moving a Pane into
// another Tab activates that Tab for ticket 18's Pane open but does not play
// a Tab switch over it. The PaneOpen names the source Tab so a view never
// ghosts the source position or makes the target's other Panes room.
func TestCrossTabPaneMoveRecordsNoTabSwitch(t *testing.T) {
	g := newWorkspaceGraph(t)
	ids := mustTab(t, g, 2)
	g.state.SelectTab(0)
	before := mustEvent(t, g.state)
	warpBefore := g.state.BodyWarpSeq()
	pane := panesOf(t, g.state, 0)[0]
	target := panesOf(t, g.state, 1)[0]
	if !g.state.MovePane(ids[0], pane, ids[1], target, domainworkspace.SideRight) {
		t.Fatalf("MovePane across Tabs failed")
	}
	// The source Tab held one Pane, so it ceased to exist and the target moved
	// to index 0; it is the Tab now active.
	if g.state.ActiveTab() != 0 {
		t.Fatalf("a cross-Tab pane move left active Tab %d, want the target at 0", g.state.ActiveTab())
	}
	if id, _ := g.state.TabID(0); id != ids[1] {
		t.Fatalf("the active Tab after the move = %q, want the target %q", id, ids[1])
	}
	after := mustEvent(t, g.state)
	if after.Kind == motion.TabSwitch {
		t.Fatalf("a cross-Tab pane move recorded a TabSwitch: %+v", after)
	}
	if after.Kind != motion.PaneOpen {
		t.Fatalf("a cross-Tab pane move recorded %+v, want one PaneOpen", after)
	}
	if after.TabID != ids[1] || after.SourceTabID != ids[0] || after.PaneID != pane || after.TargetID != target {
		t.Fatalf("the cross-Tab PaneOpen = %+v, want target Tab %q, source Tab %q, Pane %q beside %q",
			after, ids[1], ids[0], pane, target)
	}
	if after.Seq == before.Seq {
		t.Fatalf("the cross-Tab move did not record a new sequence")
	}
	if got := g.state.BodyWarpSeq(); got == warpBefore {
		t.Fatalf("the cross-Tab move did not raise the body warp signal")
	}
	if focused, _ := g.state.FocusedPaneID(0); focused != pane {
		t.Fatalf("focus after the cross-Tab move = %q, want the moved Pane %q", focused, pane)
	}

	// A magnified target inserts the Pane hidden behind its zoom. That is not
	// a Pane open a view could draw, so the move records no Pane event; the
	// body warp signal still ends a run the layout change interrupted.
	g = newWorkspaceGraph(t)
	ids = mustTab(t, g, 2)
	if err := g.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	zoom, _ := g.state.FocusedPaneID(1)
	if !g.state.SetMagnifiedPane(1, zoom) {
		t.Fatalf("SetMagnifiedPane(%q) failed", zoom)
	}
	g.state.SelectTab(0)
	before = mustEvent(t, g.state)
	warpBefore = g.state.BodyWarpSeq()
	pane = panesOf(t, g.state, 0)[0]
	if !g.state.MovePane(ids[0], pane, ids[1], zoom, domainworkspace.SideRight) {
		t.Fatalf("MovePane onto a magnified Tab failed")
	}
	after = mustEvent(t, g.state)
	if after.Seq != before.Seq || after.Kind != motion.TabSwitch {
		t.Fatalf("a move into a magnified Tab recorded %+v, want no new motion event", after)
	}
	if got := g.state.BodyWarpSeq(); got == warpBefore {
		t.Fatalf("the move into a magnified Tab did not raise the body warp signal")
	}
	active := g.state.ActiveTab()
	if id, ok := g.state.TabID(active); !ok || id != ids[1] {
		t.Fatalf("the active Tab after the move = %q, want the magnified target %q", id, ids[1])
	}
	if got := g.state.MagnifiedPaneID(active); got != zoom {
		t.Fatalf("the magnified Tab lost its zoom: %q, want %q", got, zoom)
	}
	if focused, _ := g.state.FocusedPaneID(active); focused != zoom {
		t.Fatalf("focus in the magnified Tab = %q, want the zoomed Pane %q", focused, zoom)
	}
}

// TestReattachRecordsTabSwitch checks Follow's reattach path: bringing a
// detached Tab back activates it and is a switch like any other.
func TestReattachRecordsTabSwitch(t *testing.T) {
	g := newWorkspaceGraph(t)
	ids := mustTab(t, g, 2)
	g.state.SelectTab(0)
	if !g.state.DetachTab(1) {
		t.Fatalf("DetachTab failed")
	}
	if !g.state.ReattachDetachedTab(ids[1]) {
		t.Fatalf("ReattachDetachedTab failed")
	}
	event := mustEvent(t, g.state)
	if event.Kind != motion.TabSwitch || event.TabID != ids[1] {
		t.Fatalf("reattaching recorded %+v, want TabSwitch of %q", event, ids[1])
	}
}
