package state

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/motion"
)

// reorderIDs lists the active Tab's Pane IDs in tree order.
func reorderIDs(t *testing.T, g *workspaceGraph) []model.PaneID {
	t.Helper()
	root, _, ok := g.state.TabTree(0)
	if !ok || root == nil {
		t.Fatalf("the Tab has no tree")
	}
	ids := domainworkspace.CollectPaneIDs(root)
	if len(ids) != 2 {
		t.Fatalf("the Tab has %d Panes, want the test's split of 2", len(ids))
	}
	return ids
}

// TestSameTabMoveRecordsOnePaneReorder is ticket 18's event model: a move in
// one Tab is one PaneReorder whose PaneID is the moved Pane and TargetID the
// drop target. Recording three events would let latest-wins collapse two of
// the three parts.
func TestSameTabMoveRecordsOnePaneReorder(t *testing.T) {
	g := newWorkspaceGraph(t)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	ids := reorderIDs(t, g)
	moved, target := ids[1], ids[0]
	tabID, _ := g.state.TabID(0)
	before, ok := g.state.MotionEvent()
	if !ok {
		t.Fatalf("the split recorded no event to count from")
	}

	if !g.state.MovePane(tabID, moved, tabID, target, domainworkspace.SideLeft) {
		t.Fatalf("MovePane in one Tab returned false")
	}
	event, ok := g.state.MotionEvent()
	if !ok {
		t.Fatalf("the move recorded no event")
	}
	if event.Kind != motion.PaneReorder {
		t.Fatalf("event kind = %v, want PaneReorder", event.Kind)
	}
	if event.PaneID != moved || event.TargetID != target {
		t.Fatalf("event = %+v, want moved %q beside %q", event, moved, target)
	}
	if event.TabID != tabID {
		t.Fatalf("event Tab = %q, want %q", event.TabID, tabID)
	}
	if event.FromMagnified != "" || event.ToMagnified != "" || event.IsZoom() {
		t.Fatalf("a same-Tab move carried zoom fields: %+v", event)
	}
	if event.Seq != before.Seq+1 {
		t.Fatalf("move seq = %d, want exactly one event after %d", event.Seq, before.Seq)
	}
	if _, ok := g.state.FocusedPaneID(0); !ok {
		t.Fatalf("the moved Pane is not focused")
	}
}

// TestTogglePaneZoomRecordsZoomReorders checks both directions: one
// PaneReorder per toggle, naming the Pane that grows or shrinks and the
// magnification before and after.
func TestTogglePaneZoomRecordsZoomReorders(t *testing.T) {
	g := newWorkspaceGraph(t)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	ids := reorderIDs(t, g)
	zoomed := ids[0]
	if !g.state.FocusPaneID(0, zoomed) {
		t.Fatalf("FocusPaneID(%q) returned false", zoomed)
	}
	before, _ := g.state.MotionEvent()

	if !g.state.TogglePaneZoom() {
		t.Fatalf("TogglePaneZoom returned false")
	}
	event, _ := g.state.MotionEvent()
	if event.Kind != motion.PaneReorder || event.PaneID != zoomed ||
		event.FromMagnified != "" || event.ToMagnified != zoomed {
		t.Fatalf("zoom-in event = %+v, want PaneReorder of %q from no zoom", event, zoomed)
	}
	if event.Seq != before.Seq+1 || event.TargetID != "" {
		t.Fatalf("zoom-in event = %+v, want one event and no drop target", event)
	}

	if !g.state.TogglePaneZoom() {
		t.Fatalf("the restoring TogglePaneZoom returned false")
	}
	event, _ = g.state.MotionEvent()
	if event.Kind != motion.PaneReorder || event.PaneID != zoomed ||
		event.FromMagnified != zoomed || event.ToMagnified != "" {
		t.Fatalf("zoom-out event = %+v, want PaneReorder of %q back to the split", event, zoomed)
	}
}

// TestSetMagnifiedPaneRecordsDirectMagnification covers A→B: one event names
// B as the zoomed Pane and carries A as the Pane it covers, so the view does
// not play an unzoom followed by a zoom.
func TestSetMagnifiedPaneRecordsDirectMagnification(t *testing.T) {
	g := newWorkspaceGraph(t)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	ids := reorderIDs(t, g)
	first, second := ids[0], ids[1]
	if !g.state.SetMagnifiedPane(0, first) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", first)
	}
	event, _ := g.state.MotionEvent()
	if event.PaneID != first || event.FromMagnified != "" || event.ToMagnified != first {
		t.Fatalf("first zoom event = %+v, want %q from no zoom", event, first)
	}

	if !g.state.SetMagnifiedPane(0, second) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", second)
	}
	event, _ = g.state.MotionEvent()
	if event.Kind != motion.PaneReorder {
		t.Fatalf("A→B event kind = %v, want PaneReorder", event.Kind)
	}
	if event.PaneID != second || event.FromMagnified != first || event.ToMagnified != second {
		t.Fatalf("A→B event = %+v, want %q covering %q", event, second, first)
	}
	if focus, _ := g.state.FocusedPaneID(0); focus != second {
		t.Fatalf("focus after A→B = %q, want the visible %q", focus, second)
	}

	lastSeq := event.Seq
	if !g.state.SetMagnifiedPane(0, second) {
		t.Fatalf("repeating the same zoom returned false")
	}
	if after, _ := g.state.MotionEvent(); after.Seq != lastSeq {
		t.Fatalf("setting the same magnified Pane recorded a no-op event: %+v", after)
	}

	if !g.state.SetMagnifiedPane(0, "") {
		t.Fatalf("clearing the zoom returned false")
	}
	event, _ = g.state.MotionEvent()
	if event.PaneID != second || event.FromMagnified != second || event.ToMagnified != "" {
		t.Fatalf("zoom-out event = %+v, want %q back to the split", event, second)
	}
}

// TestSplitFromMagnifiedRecordsReorderThenOpen is the magnified split's
// event order: a PaneReorder records the old full-window Pane shrinking and
// the PaneOpen that follows carries the same zoom context, so the view plays
// one continuous open. The sequence advances twice, but only the PaneOpen is
// latest and visible.
func TestSplitFromMagnifiedRecordsReorderThenOpen(t *testing.T) {
	g := newWorkspaceGraph(t)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	ids := reorderIDs(t, g)
	old := ids[0]
	if !g.state.SetMagnifiedPane(0, old) {
		t.Fatalf("SetMagnifiedPane(%q) returned false", old)
	}
	before, _ := g.state.MotionEvent()

	if err := g.state.SplitFocusedPane(domainworkspace.SideRight); err != nil {
		t.Fatal(err)
	}
	after, ok := g.state.MotionEvent()
	if !ok {
		t.Fatalf("the magnified split recorded no event")
	}
	newest, _ := g.state.FocusedPaneID(0)
	if after.Kind != motion.PaneOpen {
		t.Fatalf("latest event kind = %v, want the PaneOpen the view animates", after.Kind)
	}
	if after.PaneID != newest || after.TargetID != old || after.Direction != 1 {
		t.Fatalf("open event = %+v, want new Pane %q splitting %q as the second child",
			after, newest, old)
	}
	if after.FromMagnified != old {
		t.Fatalf("open event FromMagnified = %q, want the old full-window Pane %q",
			after.FromMagnified, old)
	}
	if after.Seq != before.Seq+2 {
		t.Fatalf("the split advanced the sequence by %d, want the reorder and the open",
			after.Seq-before.Seq)
	}
	if g.state.MagnifiedPaneID(0) != "" {
		t.Fatalf("the split left the Tab magnified")
	}
}
