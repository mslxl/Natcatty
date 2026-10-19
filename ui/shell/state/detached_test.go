package state

import (
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// waitClosed reports whether the Session ended within a short deadline.
func waitClosed(t *testing.T, done <-chan struct{}) bool {
	t.Helper()
	select {
	case <-done:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

func TestDetachPaneKeepsItsSessionAndReturnsOnReattach(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "both Pane surfaces", func() bool {
		return len(graph.surfaces) == 2
	})
	panes := panesOf(t, graph.state, 0)
	right := panes[1]
	rightSession := graph.sessions[1]

	if !graph.state.DetachPane(0, right) {
		t.Fatal("DetachPane returned false")
	}
	if graph.state.TabCount() != 1 || len(graph.state.DetachedTabIDs()) != 1 {
		t.Fatalf("tabs = %d, detached = %v; want 1 and one detached Tab",
			graph.state.TabCount(), graph.state.DetachedTabIDs())
	}
	tabID := graph.state.DetachedTabIDs()[0]
	if got := graph.state.ActiveSessionCountDetached(tabID); got != 1 {
		t.Fatalf("detached active sessions = %d, want 1", got)
	}
	if got := graph.state.ActiveSessionCountMain(); got != 1 {
		t.Fatalf("main active sessions = %d, want 1", got)
	}
	info, ok := graph.state.DetachedTabInfo(tabID)
	if !ok || len(info.Panes) != 1 {
		t.Fatalf("DetachedTabInfo = %+v, %v; want one Pane", info, ok)
	}
	root, _, ok := graph.state.DetachedTabTree(tabID)
	if !ok || !root.IsPane() || root.PaneID != right {
		t.Fatalf("detached tree = %+v, %v; want the detached Pane", root, ok)
	}
	if paneInfo, ok := graph.state.DetachedPaneInfoAt(tabID, right); !ok || paneInfo.Surface == nil {
		t.Fatalf("detached pane = %+v, %v; want its live surface", paneInfo, ok)
	} else if !paneInfo.Focused {
		t.Fatalf("detached pane = %+v, want the sole Pane focused", paneInfo)
	}

	// Moving the Session back lands it in the main Workspace with the same
	// live Session, and removes the detached owner.
	if !graph.state.ReattachDetachedTab(tabID) {
		t.Fatal("ReattachDetachedTab returned false")
	}
	if graph.state.TabCount() != 2 || len(graph.state.DetachedTabIDs()) != 0 {
		t.Fatalf("tabs = %d, detached = %v; want 2 and none",
			graph.state.TabCount(), graph.state.DetachedTabIDs())
	}
	if len(graph.reattached) != 1 || graph.reattached[0] != tabID {
		t.Fatalf("reattach handler = %v, want %q", graph.reattached, tabID)
	}
	if got := graph.state.ActiveSessionCountMain(); got != 2 {
		t.Fatalf("main active sessions = %d, want 2", got)
	}
	if waitClosed(t, rightSession.Done()) {
		t.Fatal("reattaching closed the Session")
	}
}

func TestDuplicateTabToWindowOpensFreshSessionsAndLeavesTheSource(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the source Session", func() bool {
		return len(graph.surfaces) == 1
	})
	before := graph.factory.specCount()
	sourcePaneID := panesOf(t, graph.state, 0)[0]

	if err := graph.state.DuplicateTabToWindow(0); err != nil {
		t.Fatalf("DuplicateTabToWindow: %v", err)
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("main tabs = %d, want the source to remain", graph.state.TabCount())
	}
	ids := graph.state.DetachedTabIDs()
	if len(ids) != 1 {
		t.Fatalf("detached = %v, want one Tab", ids)
	}
	if len(graph.detached) != 1 || graph.detached[0] != ids[0] {
		t.Fatalf("detach handler = %v, want the new window Tab", graph.detached)
	}
	info, _ := graph.state.DetachedTabInfo(ids[0])
	if info.Copy != 1 {
		t.Fatalf("copy generation = %d, want 1", info.Copy)
	}
	if len(info.Panes) != 1 {
		t.Fatalf("copied Panes = %v, want one", info.Panes)
	}
	graph.flushUntil(t, "the copied Session", func() bool {
		pane, ok := graph.state.DetachedPaneInfoAt(ids[0], info.Panes[0])
		return ok && pane.Surface != nil
	})
	if graph.factory.specCount() != before+1 {
		t.Fatalf("Session opens = %d, want %d", graph.factory.specCount(), before+1)
	}
	sourceInfo, _ := graph.state.PaneInfoAt(0, sourcePaneID)
	copiedInfo, _ := graph.state.DetachedPaneInfoAt(ids[0], info.Panes[0])
	if sourceInfo.Surface == nil || copiedInfo.Surface == nil || sourceInfo.Surface == copiedInfo.Surface {
		t.Fatal("the new window shares the source Session's surface")
	}
}

func TestCloseDetachedTabEndsOnlyItsSessions(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "both Sessions", func() bool {
		return len(graph.sessions) == 2
	})
	mainSession, detachedSession := graph.sessions[0], graph.sessions[1]
	right := panesOf(t, graph.state, 0)[1]
	if !graph.state.DetachPane(0, right) {
		t.Fatal("DetachPane returned false")
	}
	tabID := graph.state.DetachedTabIDs()[0]

	if !graph.state.CloseDetachedTab(tabID) {
		t.Fatal("CloseDetachedTab returned false")
	}
	if !waitClosed(t, detachedSession.Done()) {
		t.Fatal("closing the detached Tab left its Session running")
	}
	if waitClosed(t, mainSession.Done()) {
		t.Fatal("closing the detached Tab ended the main window's Session")
	}
	if got := graph.state.ActiveSessionCountMain(); got != 1 {
		t.Fatalf("main active sessions = %d, want 1", got)
	}
}

func TestActiveSessionCountsFallWhenSessionsExit(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the Session", func() bool {
		return len(graph.sessions) == 1
	})
	// The fake transport's Wait reports a normal exit, so ending it moves the
	// Session out of the active count.
	if err := graph.sessions[0].Close(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the closed Session", func() bool {
		return graph.state.ActiveSessionCountMain() == 0
	})
	if graph.state.ActiveSessionCount() != 0 {
		t.Fatalf("active sessions = %d after the Session ended, want 0", graph.state.ActiveSessionCount())
	}
}

func TestDetachedWindowCloseRequestIsReported(t *testing.T) {
	var requested []model.TabID
	service := appsession.NewService(&workspaceFactory{})
	t.Cleanup(service.CloseAll)
	shellState := NewState(
		WithSessions(service, func(*appsession.Session) (Surface, error) {
			return &fakeSurface{}, nil
		}),
		WithDetachedCloseRequest(func(id model.TabID) {
			requested = append(requested, id)
		}),
	)
	// Detached close requests come from the view; the handler decides whether
	// the window or its Tab closes.
	if err := shellState.NewTab(); err != nil {
		t.Fatal(err)
	}
	if !shellState.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	tabID := shellState.DetachedTabIDs()[0]
	shellState.RequestDetachedClose(tabID)
	if len(requested) != 1 || requested[0] != tabID {
		t.Fatalf("close requests = %v, want %q", requested, tabID)
	}
}
