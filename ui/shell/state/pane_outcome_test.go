package state

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	appview "github.com/mslxl/natcatty/application/view"
	"github.com/mslxl/natcatty/domain/model"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// openViewTargetMixedTab opens a View in Tab placement, then drags a fresh
// Session Pane from its own Tab into the View Tab. The merged Tab keeps the
// canonical view:<instanceId> id because it still shows the View.
func openViewTargetMixedTab(t *testing.T, fixture *mixedPlacementFixture,
	contextKey string) (model.TabID, *appview.Instance, model.PaneID, model.PaneID) {
	t.Helper()
	viewTabID, err := fixture.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, ContextKey: contextKey,
	})
	if err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := fixture.instanceWithContext(t, contextKey)
	viewPane := panesOf(t, fixture.state, tabIndexByID(t, fixture.state, viewTabID))[0]
	if err := fixture.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	sessionIndex := fixture.state.TabCount() - 1
	sessionTabID, _ := fixture.state.TabID(sessionIndex)
	sessionPane := panesOf(t, fixture.state, sessionIndex)[0]
	if !fixture.state.MovePane(sessionTabID, sessionPane, viewTabID, viewPane,
		domainworkspace.SideRight) {
		t.Fatal("MovePane into the View Tab returned false")
	}
	if got, _ := fixture.state.TabID(tabIndexByID(t, fixture.state, viewTabID)); got !=
		model.TabID("view:"+instance.ID()) {
		t.Fatalf("mixed Tab id = %q, want the canonical View id", got)
	}
	return viewTabID, instance, viewPane, sessionPane
}

// startHeldSplit focuses the Session Pane and starts a split whose async
// Session open is held at the factory. It returns the new, still Pending Pane
// id and waits until the open is actually in flight.
func startHeldSplit(t *testing.T, st *State, tabIndex int,
	sessionPane model.PaneID, started chan struct{}) model.PaneID {
	t.Helper()
	if !st.FocusPaneID(tabIndex, sessionPane) {
		t.Fatalf("FocusPaneID(%d, %q) returned false", tabIndex, sessionPane)
	}
	if err := st.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	splitPane, ok := st.FocusedPaneID(tabIndex)
	if !ok || splitPane == sessionPane || splitPane == "" {
		t.Fatalf("split Pane = %q ok=%v", splitPane, ok)
	}
	if info, ok := st.PaneInfoAt(tabIndex, splitPane); !ok || !info.Pending {
		t.Fatalf("new split Pane = %+v ok=%v, want Pending", info, ok)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the split Session open never started")
	}
	return splitPane
}

// assertMainPaneAttached waits for Flush to apply the queued outcome and
// checks the Pane owns a live Session. A stale Tab key leaves the Pane Pending
// and closes the Session, so Pending false plus a surface is the regression
// signal.
func assertMainPaneAttached(t *testing.T, st *State, tabIndex int, paneID model.PaneID) {
	t.Helper()
	var info PaneInfo
	waitFor(t, "the split Pane to attach", func() bool {
		st.Flush()
		var ok bool
		info, ok = st.PaneInfoAt(tabIndex, paneID)
		return ok && !info.Pending && info.Surface != nil
	})
	if info.Err != nil {
		t.Fatalf("split Pane error = %v, want a live Session", info.Err)
	}
}

// TestT14PendingPaneOutcomeSurvivesClosingAViewRename is the round-4
// regression: the split Session starts while the mixed Tab is still identified
// as view:<instanceId>; closing the View Pane re-identifies the Tab before the
// open completes. The outcome must reach the Pane in the renamed Tab instead of
// closing the Session and leaving the Pane Pending forever.
func TestT14PendingPaneOutcomeSurvivesClosingAViewRename(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	oldID, instance, viewPane, sessionPane := openViewTargetMixedTab(t, fixture, "global")

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fixture.transport.holdOpens(started, release)
	splitPane := startHeldSplit(t, fixture.state, 0, sessionPane, started)

	if !fixture.state.FocusPaneID(0, viewPane) {
		t.Fatal("FocusPaneID on the View Pane returned false")
	}
	if !fixture.state.CloseFocusedPane() {
		t.Fatal("CloseFocusedPane returned false")
	}
	renamed, _ := fixture.state.TabID(0)
	if renamed == oldID || strings.HasPrefix(string(renamed), "view:") {
		t.Fatalf("Tab id = %q after closing its View, want a runtime id", renamed)
	}
	if !instance.Closed() {
		t.Fatal("the closed View instance is still live")
	}
	// The View is gone; only the original Session and the Pending split remain.
	if panes := panesOf(t, fixture.state, 0); len(panes) != 2 {
		t.Fatalf("panes after closing the View = %v, want 2", panes)
	}

	close(release)
	assertMainPaneAttached(t, fixture.state, 0, splitPane)
	if active := fixture.state.ActiveSessionCountMain(); active != 2 {
		t.Fatalf("active Sessions = %d, want the original and the split", active)
	}
}

// TestT14PendingPaneOutcomeSurvivesDetachPaneRename covers the DetachPane
// reconciliation path: removing the sibling Session leaves the View alone, so
// the Tab takes the canonical view:<instanceId> id, and the queued split
// outcome must follow it.
func TestT14PendingPaneOutcomeSurvivesDetachPaneRename(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	oldID, instance, _, sessionPane := fixture.openMixedTab(t, "global")
	if strings.HasPrefix(string(oldID), "view:") {
		t.Fatalf("fixture Tab id = %q, want a runtime Session id", oldID)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fixture.transport.holdOpens(started, release)
	splitPane := startHeldSplit(t, fixture.state, 0, sessionPane, started)

	if !fixture.state.DetachPane(0, sessionPane) {
		t.Fatal("DetachPane returned false")
	}
	wantID := model.TabID("view:" + instance.ID())
	if got, _ := fixture.state.TabID(0); got != wantID {
		t.Fatalf("Tab id after detaching the sibling Session = %q, want %q", got, wantID)
	}
	if detached := fixture.state.DetachedTabIDs(); len(detached) != 1 {
		t.Fatalf("detached tabs = %v, want the removed Session Pane", detached)
	}

	close(release)
	assertMainPaneAttached(t, fixture.state, 0, splitPane)
	if active := fixture.state.ActiveSessionCount(); active != 2 {
		t.Fatalf("active Sessions = %d, want the detached original and the split", active)
	}
}

// TestT14PendingPaneOutcomeSurvivesTmuxDetachRename drives the background
// closeDetachedSessions path: a tmux detach removes the sibling Session while a
// split open is in flight, leaving the View alone and renaming the Tab.
func TestT14PendingPaneOutcomeSurvivesTmuxDetachRename(t *testing.T) {
	factory := &gatedEndingFactory{end: appsession.End{
		Reason: appsession.EndReasonDetached, ExitCode: 0, HasExitCode: true,
	}}
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	registry := appview.NewRegistry()
	t.Cleanup(registry.Close)
	if _, err := registry.Register(appview.Owner{PluginID: "acme.view"}, domainplugin.ViewContribution{
		ID:         "counter",
		Title:      "Counter",
		Placements: allPlacements,
		Instances:  domainplugin.InstanceSingleton,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	st := NewState(
		WithSessions(service, func(*appsession.Session) (Surface, error) {
			return &fakeSurface{}, nil
		}),
		WithViews(registry, func(*appview.Instance) (Surface, error) {
			return &fakeViewSurface{}, nil
		}),
	)
	if err := st.OpenHost(domainvaultHostForPaneOutcomeTest()); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the Host Tab", func() bool {
		st.Flush()
		return st.TabCount() == 1 && st.ActiveSurface() != nil
	})
	oldID, _ := st.TabID(0)
	hostPane := panesOf(t, st, 0)[0]
	if _, err := st.OpenView(appview.OpenRequest{
		ViewID: counterViewID, ContextKey: "global", Placement: domainplugin.PlacementTab,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := registry.Instances()[0]
	viewTabID, _ := st.TabID(1)
	viewPane := panesOf(t, st, 1)[0]
	if !st.MovePane(viewTabID, viewPane, oldID, hostPane, domainworkspace.SideRight) {
		t.Fatal("MovePane into the Host Tab returned false")
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	factory.holdOpens(started, release)
	splitPane := startHeldSplit(t, st, 0, hostPane, started)

	if err := factory.last().Close(); err != nil {
		t.Fatalf("close the detached transport: %v", err)
	}
	wantID := model.TabID("view:" + instance.ID())
	waitFor(t, "the tmux detach to leave the View alone", func() bool {
		st.Flush()
		got, _ := st.TabID(0)
		return got == wantID
	})
	if panes := panesOf(t, st, 0); len(panes) != 2 {
		t.Fatalf("panes after the tmux detach = %v, want the View and the split", panes)
	}

	close(release)
	assertMainPaneAttached(t, st, 0, splitPane)
	// The View shares the split with the surviving Session Pane, so the
	// reconciliation at detach time records the Pane placement.
	if instance.Placement() != domainplugin.PlacementPane {
		t.Fatalf("leftover placement = %s, want pane", instance.Placement())
	}
}

// TestT14PendingPaneOutcomeSurvivesMovePaneSourceRename moves the View out of
// the mixed Tab while a split open is in flight. The source Table loses its
// view: identity and the outcome must follow the Session Pane.
func TestT14PendingPaneOutcomeSurvivesMovePaneSourceRename(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	oldID, instance, viewPane, sessionPane := openViewTargetMixedTab(t, fixture, "global")

	// Build the move target before holding opens so its synchronous local
	// Session does not block on the gate.
	if err := fixture.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	targetIndex := fixture.state.TabCount() - 1
	targetID, _ := fixture.state.TabID(targetIndex)
	targetPane := panesOf(t, fixture.state, targetIndex)[0]

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fixture.transport.holdOpens(started, release)
	sourceIndex := tabIndexByID(t, fixture.state, oldID)
	fixture.state.SelectTab(sourceIndex)
	splitPane := startHeldSplit(t, fixture.state, sourceIndex, sessionPane, started)

	if !fixture.state.MovePane(oldID, viewPane, targetID, targetPane,
		domainworkspace.SideRight) {
		t.Fatal("MovePane of the View out of its mixed Tab returned false")
	}
	if got, _ := fixture.state.TabID(sourceIndex); got == oldID ||
		strings.HasPrefix(string(got), "view:") {
		t.Fatalf("source Tab id = %q after its View left, want a runtime id", got)
	}

	close(release)
	assertMainPaneAttached(t, fixture.state, sourceIndex, splitPane)
	if instance.Placement() != domainplugin.PlacementPane {
		t.Fatalf("moved View placement = %s, want pane", instance.Placement())
	}
}

// TestT14PendingPaneOutcomeSurvivesMoveViewOutOfMixedTab covers the remove
// closure a View placement move runs: the Tab that showed the View loses its
// view: identity before the queued split outcome is applied.
func TestT14PendingPaneOutcomeSurvivesMoveViewOutOfMixedTab(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	oldID, instance, _, sessionPane := openViewTargetMixedTab(t, fixture, "global")

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fixture.transport.holdOpens(started, release)
	splitPane := startHeldSplit(t, fixture.state, 0, sessionPane, started)

	if err := fixture.state.MoveView(instance.ID(), domainplugin.PlacementPage); err != nil {
		t.Fatalf("MoveView to page: %v", err)
	}
	if got, _ := fixture.state.TabID(0); got == oldID ||
		strings.HasPrefix(string(got), "view:") {
		t.Fatalf("source Tab id = %q after the View moved out, want a runtime id", got)
	}
	if _, ok := fixture.state.PageView(counterViewID); !ok {
		t.Fatal("the View did not land in the Page")
	}

	close(release)
	assertMainPaneAttached(t, fixture.state, 0, splitPane)
}

// TestT14PendingPaneOutcomeFollowsItsPaneIntoADetachedWindow detaches the
// Pending Pane itself while its Session is still opening. The outcome belongs
// to the Pane, so it must land in the window that now owns it.
func TestT14PendingPaneOutcomeFollowsItsPaneIntoADetachedWindow(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	_, _, _, sessionPane := openViewTargetMixedTab(t, fixture, "global")

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fixture.transport.holdOpens(started, release)
	splitPane := startHeldSplit(t, fixture.state, 0, sessionPane, started)

	if !fixture.state.DetachPane(0, splitPane) {
		t.Fatal("DetachPane of the Pending Pane returned false")
	}
	detached := fixture.state.DetachedTabIDs()
	if len(detached) != 1 {
		t.Fatalf("detached tabs = %v, want the Pending Pane's window", detached)
	}

	close(release)
	var info PaneInfo
	waitFor(t, "the detached split Pane to attach", func() bool {
		fixture.state.Flush()
		var ok bool
		info, ok = fixture.state.DetachedPaneInfoAt(detached[0], splitPane)
		return ok && !info.Pending && info.Surface != nil
	})
	if info.Err != nil {
		t.Fatalf("detached split Pane error = %v, want a live Session", info.Err)
	}
}

// TestT14TwoPendingPaneOutcomesDoNotCrossTabs queues one held split outcome per
// Tab, renames both Tabs, then applies them. Each Pane must attach in its own
// Tab; neither outcome may land in the other.
func TestT14TwoPendingPaneOutcomesDoNotCrossTabs(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceMulti)
	firstID, _, firstView, firstSession := openViewTargetMixedTab(t, fixture, "first")
	secondID, _, secondView, secondSession := openViewTargetMixedTab(t, fixture, "second")
	firstIndex := tabIndexByID(t, fixture.state, firstID)
	secondIndex := tabIndexByID(t, fixture.state, secondID)

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	fixture.transport.holdOpens(started, release)

	fixture.state.SelectTab(firstIndex)
	firstSplit := startHeldSplit(t, fixture.state, firstIndex, firstSession, started)
	fixture.state.SelectTab(secondIndex)
	secondSplit := startHeldSplit(t, fixture.state, secondIndex, secondSession, started)

	for _, tab := range []struct {
		index int
		view  model.PaneID
		oldID model.TabID
	}{
		{firstIndex, firstView, firstID},
		{secondIndex, secondView, secondID},
	} {
		fixture.state.SelectTab(tab.index)
		if !fixture.state.FocusPaneID(tab.index, tab.view) {
			t.Fatalf("FocusPaneID(%d, %q) returned false", tab.index, tab.view)
		}
		if !fixture.state.CloseFocusedPane() {
			t.Fatalf("CloseFocusedPane in Tab %d returned false", tab.index)
		}
		if got, _ := fixture.state.TabID(tab.index); got == tab.oldID ||
			strings.HasPrefix(string(got), "view:") {
			t.Fatalf("Tab %d id = %q after removing its View, want a runtime id", tab.index, got)
		}
	}

	close(release)
	assertMainPaneAttached(t, fixture.state, firstIndex, firstSplit)
	assertMainPaneAttached(t, fixture.state, secondIndex, secondSplit)
	for _, index := range []int{firstIndex, secondIndex} {
		if panes := panesOf(t, fixture.state, index); len(panes) != 2 {
			t.Fatalf("Tab %d panes = %v, want the original Session and its split", index, panes)
		}
	}
	if got := fixture.state.ActiveSessionCountMain(); got != 4 {
		t.Fatalf("active Sessions = %d, want both originals and both splits", got)
	}
}

// gatedEndingFactory returns transports whose End classification the test
// chooses and holds each Open until released.
type gatedEndingFactory struct {
	mu         sync.Mutex
	end        appsession.End
	transports []*endingTransport
	gate       chan struct{}
	started    chan struct{}
}

func (f *gatedEndingFactory) holdOpens(started chan struct{}, release chan struct{}) {
	f.mu.Lock()
	f.gate, f.started = release, started
	f.mu.Unlock()
}

func (f *gatedEndingFactory) Open(ctx context.Context, _ appsession.StartSpec) (appsession.Transport, error) {
	f.mu.Lock()
	gate, started := f.gate, f.started
	f.mu.Unlock()
	if gate != nil {
		if started != nil {
			started <- struct{}{}
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	transport := &endingTransport{fakeTransport: newFakeTransport(), end: f.end}
	f.mu.Lock()
	f.transports = append(f.transports, transport)
	f.mu.Unlock()
	return transport, nil
}

func (f *gatedEndingFactory) last() *endingTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transports[len(f.transports)-1]
}

func domainvaultHostForPaneOutcomeTest() domainvault.Host {
	return domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH, AutoAttachTmux: true,
	}
}
