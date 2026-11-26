package state

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/motion"
)

// paneCloseMotion is a motion setting whose Pane close animates, so
// CloseFocusedPane retains the exiting Pane's Surface for a ghost.
func paneCloseMotion() domainpreferences.Motion {
	setting := domainpreferences.Defaults().Appearance.Motion
	setting.Enabled = true
	setting.PaneClose = true
	return setting
}

// splitAndCloseOnePane opens a Tab with two Panes and closes the focused one
// without a view consuming the ghost. It returns the closed Pane and the
// surface factory's surfaces in creation order.
func splitAndCloseOnePane(t *testing.T) (*workspaceGraph, model.PaneID) {
	t.Helper()
	graph := newWorkspaceGraphWithOptions(t, WithMotion(paneCloseMotion))
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the split Pane", func() bool { return len(graph.surfaces) == 2 })
	closed, _ := graph.state.FocusedPaneID(0)
	if !graph.state.CloseFocusedPane() {
		t.Fatal("CloseFocusedPane returned false")
	}
	if len(graph.surfaces) != 2 {
		t.Fatalf("the split created %d surfaces, want 2", len(graph.surfaces))
	}
	return graph, closed
}

// TestPaneGhostKeepsTheSurfaceUntilTheViewTakesIt proves the close retains the
// exiting Pane's Surface and the view's TakePaneGhost hands it out exactly
// once; Release destroys it, and a second Release is harmless.
func TestPaneGhostKeepsTheSurfaceUntilTheViewTakesIt(t *testing.T) {
	graph, closed := splitAndCloseOnePane(t)
	exiting := graph.surfaces[1]
	if exiting.closed {
		t.Fatal("the exiting Pane's Surface was closed before its ghost ran")
	}
	ghost, ok := graph.state.TakePaneGhost(closed)
	if !ok {
		t.Fatal("TakePaneGhost found no ghost for the closed Pane")
	}
	if ghost.ID != closed || ghost.Info.Surface != exiting {
		t.Fatalf("the ghost = id %q, surface %v; want %q and the exiting surface",
			ghost.ID, ghost.Info.Surface, closed)
	}
	if _, ok := graph.state.TakePaneGhost(closed); ok {
		t.Fatal("TakePaneGhost handed the same ghost out twice")
	}
	ghost.Release()
	if !exiting.closed {
		t.Fatal("Release did not close the retained Surface")
	}
	ghost.Release()
	if !exiting.closed {
		t.Fatal("the second Release changed the closed Surface")
	}
	// A nil ghost is harmless, as the view calls Release on optional ghosts.
	var none *PaneGhost
	none.Release()
}

// TestPaneGhostIsReleasedWhenSupersededOrShutDown keeps an untaken ghost from
// leaking: another layout action and Shutdown both destroy its Surface.
func TestPaneGhostIsReleasedWhenSupersededOrShutDown(t *testing.T) {
	t.Run("superseded", func(t *testing.T) {
		graph, closed := splitAndCloseOnePane(t)
		exiting := graph.surfaces[1]
		if err := graph.state.SplitRight(); err != nil {
			t.Fatal(err)
		}
		if !exiting.closed {
			t.Fatal("a superseding split left the exiting Surface alive")
		}
		if _, ok := graph.state.TakePaneGhost(closed); ok {
			t.Fatal("the superseded ghost is still offered")
		}
	})
	t.Run("shutdown", func(t *testing.T) {
		graph, _ := splitAndCloseOnePane(t)
		exiting := graph.surfaces[1]
		graph.state.Shutdown()
		if !exiting.closed {
			t.Fatal("Shutdown left the exiting Surface alive")
		}
	})
}

// TestPaneCloseWhileMagnifiedRecordsTheFullWindowStart checks the state's half
// of the magnified contract: the event names the closed magnified Pane and
// carries Magnified so the view starts its ghost from the full pane region.
// The last-Pane path still records no PaneClose; it records the TabClose that
// took the Pane's Tab away.
func TestPaneCloseWhileMagnifiedRecordsTheFullWindowStart(t *testing.T) {
	graph := newWorkspaceGraphWithOptions(t, WithMotion(paneCloseMotion))
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the split", func() bool { return len(graph.state.WorkspacePayload().Panes) == 2 })
	left, right := panesOf(t, graph.state, 0)[0], panesOf(t, graph.state, 0)[1]
	if !graph.state.SetMagnifiedPane(0, left) {
		t.Fatal("SetMagnifiedPane rejected the left Pane")
	}
	if !graph.state.CloseFocusedPane() {
		t.Fatal("CloseFocusedPane returned false")
	}
	event, ok := graph.state.MotionEvent()
	if !ok {
		t.Fatal("the magnified close recorded no motion event")
	}
	if event.Kind != motion.PaneClose || event.PaneID != left || event.TargetID != right || !event.Magnified {
		t.Fatalf("the event = %+v, want a magnified PaneClose of %q onto %q", event, left, right)
	}
	ghost, ok := graph.state.TakePaneGhost(left)
	if !ok {
		t.Fatal("the magnified close retained no ghost")
	}
	ghost.Release()

	if !graph.state.CloseFocusedPane() {
		t.Fatal("closing the last Pane returned false")
	}
	if got := graph.state.TabCount(); got != 0 {
		t.Fatalf("the last Pane's close left %d Tabs, want 0", got)
	}
	after, ok := graph.state.MotionEvent()
	if !ok || after.Kind != motion.TabClose || after.TabID != event.TabID || after.TargetTabID != "" {
		t.Fatalf("the last Pane's close recorded %+v, want the TabClose of %q with no replacement", after, event.TabID)
	}
	if after.Kind == motion.PaneClose {
		t.Fatalf("the last Pane's close recorded a PaneClose: %+v", after)
	}
	// The whole body was snapshotted before the Pane left the Tab, so the Tab
	// close's exit shows the closing Pane rather than a blank body.
	body, ok := graph.state.TakeTabGhost(after.TabID)
	if !ok {
		t.Fatal("the last Pane's Tab close retained no whole body")
	}
	if len(body.Panes) != 1 || body.Panes[0].Info.ID != right {
		t.Fatalf("the retained body has %d Panes, want the closing Pane %q", len(body.Panes), right)
	}
	body.Release()
	graph.state.ReleaseTabGhosts()
}
