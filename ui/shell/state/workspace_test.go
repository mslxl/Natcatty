package state

import (
	"context"
	"errors"
	"sync"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	appworkspace "github.com/mslxl/natcatty/application/workspace"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// workspaceFactory opens a fresh fake transport per Session so split and
// restore tests can keep several Sessions alive at once.
type workspaceFactory struct {
	mu         sync.Mutex
	specs      []appsession.StartSpec
	transports []*fakeTransport
	fail       func(appsession.StartSpec) error
}

func (f *workspaceFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	if f.fail != nil {
		if err := f.fail(spec); err != nil {
			return nil, err
		}
	}
	transport := newFakeTransport()
	f.transports = append(f.transports, transport)
	return transport, nil
}

func (f *workspaceFactory) lastSpec() appsession.StartSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.specs) == 0 {
		return appsession.StartSpec{}
	}
	return f.specs[len(f.specs)-1]
}

func (f *workspaceFactory) specCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.specs)
}

type memoryWorkspaceRepository struct {
	payload domainworkspace.Payload
	saves   int
}

func (m *memoryWorkspaceRepository) Load(context.Context) (domainworkspace.Payload, error) {
	return m.payload, nil
}

func (m *memoryWorkspaceRepository) Save(_ context.Context, payload domainworkspace.Payload) error {
	m.payload = payload
	m.saves++
	return nil
}

func (m *memoryWorkspaceRepository) Clear(context.Context) error {
	m.payload = domainworkspace.EmptyPayload()
	return nil
}

type workspaceGraph struct {
	state      *State
	factory    *workspaceFactory
	sessions   []*appsession.Session
	surfaces   []*fakeSurface
	repo       *memoryWorkspaceRepository
	service    *appsession.Service
	hosts      map[model.HostID]domainvault.Host
	detached   []model.TabID
	reattached []model.TabID
}

func newWorkspaceGraph(t *testing.T) *workspaceGraph {
	t.Helper()
	graph := &workspaceGraph{
		factory: &workspaceFactory{},
		repo:    &memoryWorkspaceRepository{payload: domainworkspace.EmptyPayload()},
		hosts:   map[model.HostID]domainvault.Host{},
	}
	graph.service = appsession.NewService(graph.factory)
	t.Cleanup(graph.service.CloseAll)
	graph.state = NewState(
		WithSessions(graph.service, func(session *appsession.Session) (Surface, error) {
			graph.sessions = append(graph.sessions, session)
			surface := &fakeSurface{}
			graph.surfaces = append(graph.surfaces, surface)
			return surface, nil
		}),
		WithWorkspace(appworkspace.NewService(graph.repo),
			func(_ context.Context, id model.HostID) (domainvault.Host, bool) {
				host, ok := graph.hosts[id]
				return host, ok
			}),
		WithDetachHandler(func(id model.TabID) {
			graph.detached = append(graph.detached, id)
		}),
		WithDetachReattachHandler(func(id model.TabID) {
			graph.reattached = append(graph.reattached, id)
		}),
	)
	return graph
}

func (g *workspaceGraph) flushUntil(t *testing.T, what string, ready func() bool) {
	t.Helper()
	waitFor(t, what, func() bool {
		g.state.Flush()
		return ready()
	})
}

func panesOf(t *testing.T, state *State, index int) []model.PaneID {
	t.Helper()
	root, _, ok := state.TabTree(index)
	if !ok {
		t.Fatalf("Tab %d does not exist", index)
	}
	return domainworkspace.CollectPaneIDs(root)
}

func TestSplitFocusedPaneBuildsRecursiveTreeAndFocus(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	root, focus, _ := graph.state.TabTree(0)
	if root == nil || root.Direction != domainworkspace.Vertical {
		t.Fatalf("root = %+v, want a vertical split", root)
	}
	if len(focus) != 1 || focus[0] != 1 {
		t.Fatalf("focus path = %v, want the new right Pane", focus)
	}
	if len(panesOf(t, graph.state, 0)) != 2 {
		t.Fatalf("panes = %v, want 2", panesOf(t, graph.state, 0))
	}

	if err := graph.state.SplitDown(); err != nil {
		t.Fatalf("SplitDown: %v", err)
	}
	root, focus, _ = graph.state.TabTree(0)
	if root.Second == nil || root.Second.Type != domainworkspace.SplitNode || root.Second.Direction != domainworkspace.Horizontal {
		t.Fatalf("right subtree = %+v, want a horizontal split", root.Second)
	}
	if len(focus) != 2 || focus[0] != 1 || focus[1] != 1 {
		t.Fatalf("focus path = %v, want the new bottom-right Pane", focus)
	}

	graph.flushUntil(t, "three Pane surfaces", func() bool {
		return graph.state.ActiveSurface() != nil && len(graph.surfaces) == 3
	})
	if graph.factory.specCount() != 3 {
		t.Fatalf("Session opens = %d, want 3", graph.factory.specCount())
	}
	for _, id := range panesOf(t, graph.state, 0) {
		info, ok := graph.state.PaneInfoAt(0, id)
		if !ok || info.Surface == nil || info.Pending || info.Err != nil {
			t.Fatalf("pane %q = %+v, %v; want a live surface", id, info, ok)
		}
	}
}

func TestCloseFocusedPaneCollapsesTheSiblingSubtree(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	first, _ := graph.state.FocusedPaneID(0)
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the split Pane", func() bool {
		id, _ := graph.state.FocusedPaneID(0)
		info, ok := graph.state.PaneInfoAt(0, id)
		return ok && info.Surface != nil
	})
	second, _ := graph.state.FocusedPaneID(0)
	if second == first {
		t.Fatal("focus did not move to the split Pane")
	}

	if !graph.state.CloseFocusedPane() {
		t.Fatal("CloseFocusedPane returned false")
	}
	root, focus, _ := graph.state.TabTree(0)
	if root == nil || !root.IsPane() || root.PaneID != first {
		t.Fatalf("root = %+v, want the surviving Pane", root)
	}
	if len(focus) != 0 {
		t.Fatalf("focus path = %v, want the root Pane", focus)
	}
	if got, ok := graph.state.FocusedPaneID(0); !ok || got != first {
		t.Fatalf("focused Pane = %q, %v; want %q", got, ok, first)
	}

	if !graph.state.CloseFocusedPane() {
		t.Fatal("closing the last Pane returned false")
	}
	if graph.state.TabCount() != 0 || !graph.state.VaultVisible() {
		t.Fatalf("tabs = %d, vault = %v; want an empty Workspace showing the Vault",
			graph.state.TabCount(), graph.state.VaultVisible())
	}
}

func TestDirectionalFocusMovesAcrossSplitGeometry(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitDown(); err != nil {
		t.Fatal(err)
	}
	if got, _ := graph.state.FocusedPaneID(0); got == "" {
		t.Fatal("no focused Pane")
	}

	if !graph.state.FocusPane(domainworkspace.FocusUp) {
		t.Fatal("FocusUp did not move")
	}
	up, _ := graph.state.FocusedPaneID(0)
	if path := focusPathOf(t, graph.state); len(path) != 2 || path[0] != 1 || path[1] != 0 {
		t.Fatalf("focus path after FocusUp = %v, want [1 0]", path)
	}

	if !graph.state.FocusPane(domainworkspace.FocusLeft) {
		t.Fatal("FocusLeft did not move")
	}
	left, _ := graph.state.FocusedPaneID(0)
	if path := focusPathOf(t, graph.state); len(path) != 1 || path[0] != 0 {
		t.Fatalf("focus path after FocusLeft = %v, want [0]", path)
	}
	if left == up {
		t.Fatal("focus did not leave the right column")
	}

	if !graph.state.FocusPane(domainworkspace.FocusRight) {
		t.Fatal("FocusRight did not move")
	}
	if path := focusPathOf(t, graph.state); len(path) != 2 || path[0] != 1 {
		t.Fatalf("focus path after FocusRight = %v, want a right-column Pane", path)
	}

	// A single-Pane Tab has nowhere to move.
	single := newWorkspaceGraph(t)
	if err := single.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if single.state.FocusPane(domainworkspace.FocusLeft) {
		t.Fatal("FocusLeft moved focus in a single-Pane Tab")
	}
}

func focusPathOf(t *testing.T, state *State) []int {
	t.Helper()
	_, focus, ok := state.TabTree(state.ActiveTab())
	if !ok {
		t.Fatal("no active Tab")
	}
	return focus
}

func TestTabActionsCloseOthersCloseRightDuplicateAndDetach(t *testing.T) {
	graph := newWorkspaceGraph(t)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	if !graph.state.CloseTabsRight(1) {
		t.Fatal("CloseTabsRight reported no close")
	}
	if graph.state.TabCount() != 2 {
		t.Fatalf("tabs = %d after CloseTabsRight, want 2", graph.state.TabCount())
	}
	if !graph.state.CloseOtherTabs(0) {
		t.Fatal("CloseOtherTabs reported no close")
	}
	if graph.state.TabCount() != 1 || graph.state.ActiveTab() != 0 {
		t.Fatalf("tabs = %d, active = %d; want 1 and 0", graph.state.TabCount(), graph.state.ActiveTab())
	}

	sourceID, _ := graph.state.TabID(0)
	before := graph.factory.specCount()
	if err := graph.state.DuplicateTab(0); err != nil {
		t.Fatalf("DuplicateTab: %v", err)
	}
	if graph.state.TabCount() != 2 {
		t.Fatalf("tabs = %d after DuplicateTab, want 2", graph.state.TabCount())
	}
	copyID, _ := graph.state.TabID(1)
	if copyID == sourceID {
		t.Fatal("the duplicate reused the source Tab identity")
	}
	info, _ := graph.state.TabInfo(1)
	if info.Copy != 1 {
		t.Fatalf("duplicate Copy = %d, want 1", info.Copy)
	}
	graph.flushUntil(t, "the duplicate Session", func() bool {
		info, ok := graph.state.PaneInfoAt(1, panesOf(t, graph.state, 1)[0])
		return ok && info.Surface != nil
	})
	if graph.factory.specCount() != before+1 {
		t.Fatalf("Session opens = %d, want %d", graph.factory.specCount(), before+1)
	}
	paneID := panesOf(t, graph.state, 0)[0]
	original, _ := graph.state.PaneInfoAt(0, paneID)
	copied, _ := graph.state.PaneInfoAt(1, panesOf(t, graph.state, 1)[0])
	if original.Surface == nil || copied.Surface == nil || original.Surface == copied.Surface {
		t.Fatal("the duplicate shares the source Pane's surface")
	}

	if !graph.state.DetachTab(1) {
		t.Fatal("DetachTab returned false")
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d after DetachTab, want 1", graph.state.TabCount())
	}
	detached := graph.state.DetachedTabIDs()
	if len(detached) != 1 || detached[0] != copyID {
		t.Fatalf("detached = %v, want %q", detached, copyID)
	}
	if len(graph.detached) != 1 || graph.detached[0] != copyID {
		t.Fatalf("detach handler got %v, want %q", graph.detached, copyID)
	}
	if !graph.state.CloseDetachedTab(copyID) {
		t.Fatal("CloseDetachedTab returned false")
	}
	if len(graph.state.DetachedTabIDs()) != 0 {
		t.Fatal("the detached Tab was not removed")
	}
}

func TestMoveTabReordersAndFollowsTheActiveTab(t *testing.T) {
	graph := newWorkspaceGraph(t)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := graph.state.TabID(0)
	middle, _ := graph.state.TabID(1)
	last, _ := graph.state.TabID(2)
	graph.state.SelectTab(2)

	if !graph.state.MoveTab(last, first, false) {
		t.Fatal("MoveTab returned false")
	}
	got := idsOf(graph.state)
	want := []model.TabID{last, first, middle}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("tab order = %v, want %v", got, want)
		}
	}
	if graph.state.ActiveTab() != 0 {
		t.Fatalf("active = %d, want the moved Tab at 0", graph.state.ActiveTab())
	}
	if graph.state.MoveTab(last, last, false) {
		t.Fatal("MoveTab accepted a drop onto itself")
	}
	if graph.state.MoveTab("missing", first, false) {
		t.Fatal("MoveTab accepted an unknown Tab")
	}
}

func idsOf(state *State) []model.TabID {
	ids := make([]model.TabID, state.TabCount())
	for index := range ids {
		ids[index], _ = state.TabID(index)
	}
	return ids
}

func TestMovePaneWithinAndAcrossTabs(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the split", func() bool {
		return len(graph.state.WorkspacePayload().Panes) == 2
	})
	tabID, _ := graph.state.TabID(0)
	panes := panesOf(t, graph.state, 0)
	left, right := panes[0], panes[1]

	if graph.state.MovePane(tabID, left, tabID, left, domainworkspace.SideRight) {
		t.Fatal("MovePane accepted a Pane dropped on itself")
	}
	if !graph.state.MovePane(tabID, right, tabID, left, domainworkspace.SideLeft) {
		t.Fatal("MovePane within the Tab returned false")
	}
	root, focus, _ := graph.state.TabTree(0)
	if root.First == nil || !root.First.IsPane() || root.First.PaneID != right {
		t.Fatalf("root.First = %+v, want the moved Pane", root.First)
	}
	if path := focus; len(path) != 1 || path[0] != 0 {
		t.Fatalf("focus path = %v, want the moved Pane", path)
	}

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	secondID, _ := graph.state.TabID(1)
	secondPane := panesOf(t, graph.state, 1)[0]
	if !graph.state.MovePane(tabID, right, secondID, secondPane, domainworkspace.SideRight) {
		t.Fatal("MovePane across Tabs returned false")
	}
	if graph.state.TabCount() != 2 {
		t.Fatalf("tabs = %d, want the source Tab to remain with one Pane", graph.state.TabCount())
	}
	if got := len(panesOf(t, graph.state, 1)); got != 2 {
		t.Fatalf("target panes = %d, want 2", got)
	}
	if got, _ := graph.state.FocusedPaneID(1); got != right {
		t.Fatalf("focused Pane = %q, want the moved Pane", got)
	}

	// Moving the Pane back leaves the target Tab with its original Pane.
	if !graph.state.MovePane(secondID, right, tabID, left, domainworkspace.SideRight) {
		t.Fatal("moving the Pane back returned false")
	}
	if graph.state.TabCount() != 2 || len(panesOf(t, graph.state, 1)) != 1 {
		t.Fatalf("tabs = %d, target panes = %d; want the source Tab to keep one Pane",
			graph.state.TabCount(), len(panesOf(t, graph.state, 1)))
	}
	// Moving the last Pane out removes the now-empty Tab.
	if !graph.state.MovePane(secondID, secondPane, tabID, left, domainworkspace.SideRight) {
		t.Fatal("second cross-Tab move returned false")
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d after emptying a Tab, want 1", graph.state.TabCount())
	}
}

func TestWorkspacePayloadRoundTripsThroughRestore(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the split", func() bool {
		return len(graph.state.WorkspacePayload().Panes) == 2
	})
	graph.state.SetTabDirty(0, true)
	activePane, _ := graph.state.FocusedPaneID(0)
	payload := graph.state.WorkspacePayload()
	if payload.ActiveTab != 0 || len(payload.Tabs) != 1 || len(payload.Panes) != 2 {
		t.Fatalf("payload = %+v, want one Tab with two Panes", payload)
	}
	if payload.Tabs[0].FocusPath == nil || len(payload.Tabs[0].Root.First.PaneID) == 0 {
		t.Fatalf("payload does not describe the focus or tree: %+v", payload.Tabs[0])
	}

	restored := newWorkspaceGraph(t)
	restored.repo.payload = payload
	restored.state.Restore(context.Background())
	if restored.state.TabCount() != 1 || restored.state.ActiveTab() != 0 {
		t.Fatalf("restored tabs = %d, active = %d; want 1 and 0",
			restored.state.TabCount(), restored.state.ActiveTab())
	}
	if got, _ := restored.state.FocusedPaneID(0); got != activePane {
		t.Fatalf("restored focus = %q, want %q", got, activePane)
	}
	restored.flushUntil(t, "the restored Panes", func() bool {
		for _, id := range panesOf(t, restored.state, 0) {
			info, ok := restored.state.PaneInfoAt(0, id)
			if !ok || info.Surface == nil {
				return false
			}
		}
		return true
	})
	if restored.factory.specCount() != 2 {
		t.Fatalf("restored Session opens = %d, want 2", restored.factory.specCount())
	}
}

func TestRestoreDegradesMissingHostAndInvalidWorkingDirectory(t *testing.T) {
	invalidDir := "Z:\\natcatty-missing-directory\\does-not-exist"
	root, err := domainworkspace.SplitAt(
		domainworkspace.NewPaneNode("pane-local"), "pane-local", "pane-ssh",
		"split-restored", domainworkspace.SideRight)
	if err != nil {
		t.Fatal(err)
	}
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-restored", Number: 7, Root: root, FocusPath: []int{1},
		}},
		Panes: []domainworkspace.RestoredPane{
			{ID: "pane-local", Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionLocal, Number: 7, Dir: invalidDir,
			}},
			{ID: "pane-ssh", Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, Label: "gone",
				HostID: "host-gone", Hostname: "gone.example.com", Port: 22,
			}},
		},
	}

	graph := newWorkspaceGraph(t)
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restore outcomes", func() bool {
		local, localOK := graph.state.PaneInfoAt(0, "pane-local")
		remote, remoteOK := graph.state.PaneInfoAt(0, "pane-ssh")
		return localOK && remoteOK && !local.Pending && !remote.Pending
	})

	local, _ := graph.state.PaneInfoAt(0, "pane-local")
	if local.Err != nil || local.Surface == nil {
		t.Fatalf("local Pane = %+v, want a live surface despite the invalid directory", local)
	}
	if got := graph.factory.lastSpec().Dir; got != "" {
		t.Fatalf("local start directory = %q, want the invalid path cleared", got)
	}
	remote, _ := graph.state.PaneInfoAt(0, "pane-ssh")
	if !errors.Is(remote.Err, ErrHostUnavailable) || remote.Surface != nil {
		t.Fatalf("missing-Host Pane = %+v, want an error surface", remote)
	}
	if len(panesOf(t, graph.state, 0)) != 2 {
		t.Fatal("the missing Host removed its Pane from the layout")
	}
}

func TestRestoreKeepsNumbersActiveTabAndFocus(t *testing.T) {
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 1,
		Tabs: []domainworkspace.RestoredTab{
			{ID: "tab-a", Number: 4, Root: domainworkspace.NewPaneNode("a1")},
			{ID: "tab-b", Number: 9, Root: domainworkspace.NewPaneNode("b1"), FocusPath: nil},
		},
		Panes: []domainworkspace.RestoredPane{
			{ID: "a1", Session: domainworkspace.SessionDescriptor{Kind: domainworkspace.SessionLocal, Number: 4}},
			{ID: "b1", Session: domainworkspace.SessionDescriptor{Kind: domainworkspace.SessionLocal, Number: 9}},
		},
	}
	graph := newWorkspaceGraph(t)
	graph.repo.payload = payload
	graph.state.Restore(context.Background())

	if got := graph.state.TabNumbers(); len(got) != 2 || got[0] != 4 || got[1] != 9 {
		t.Fatalf("tab numbers = %v, want [4 9]", got)
	}
	if graph.state.ActiveTab() != 1 {
		t.Fatalf("active = %d, want 1", graph.state.ActiveTab())
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if got := graph.state.TabNumbers(); got[len(got)-1] != 10 {
		t.Fatalf("next tab number = %d, want 10 after restoring 9", got[len(got)-1])
	}
}

func TestSetTabDirtyAndSelectClearsIt(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.state.SetTabDirty(0, true)
	if info, _ := graph.state.TabInfo(0); !info.Dirty {
		t.Fatal("SetTabDirty did not mark the Tab")
	}
	graph.state.SelectTab(0)
	if info, _ := graph.state.TabInfo(0); info.Dirty {
		t.Fatal("SelectTab did not clear the activity marker")
	}
}

func TestOpenHostPersistsItsRestoreDescriptor(t *testing.T) {
	graph := newWorkspaceGraph(t)
	host := domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 2222,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the SSH Tab", func() bool { return graph.state.TabCount() == 1 })

	payload := graph.state.WorkspacePayload()
	if len(payload.Panes) != 1 {
		t.Fatalf("payload panes = %d, want 1", len(payload.Panes))
	}
	descriptor := payload.Panes[0].Session
	if descriptor.Kind != domainworkspace.SessionSSH || descriptor.HostID != "host-1" ||
		descriptor.Hostname != "example.com" || descriptor.Port != 2222 ||
		descriptor.Username != "root" || descriptor.Label != "prod" {
		t.Fatalf("descriptor = %+v, want the Host's restore data", descriptor)
	}
	if graph.repo.saves == 0 {
		t.Fatal("opening a Tab did not persist the layout")
	}
}

func TestOpenHostPersistsATelnetRestoreDescriptor(t *testing.T) {
	graph := newWorkspaceGraph(t)
	host := domainvault.Host{
		ID: "host-telnet", Label: "console", Hostname: "console.local",
		Port: 23, Username: "admin", Protocol: domainvault.ProtocolTelnet,
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the Telnet Tab", func() bool { return graph.state.TabCount() == 1 })

	payload := graph.state.WorkspacePayload()
	if len(payload.Panes) != 1 {
		t.Fatalf("payload panes = %d, want 1", len(payload.Panes))
	}
	descriptor := payload.Panes[0].Session
	if descriptor.Kind != domainworkspace.SessionTelnet || descriptor.HostID != "host-telnet" ||
		descriptor.Hostname != "console.local" || descriptor.Port != 23 ||
		descriptor.Username != "admin" || descriptor.Label != "console" {
		t.Fatalf("descriptor = %+v, want the Telnet Host's restore data", descriptor)
	}
	if descriptor.X11Forwarding {
		t.Fatal("the Telnet descriptor carries an SSH-only X11 preference")
	}
}

func TestRestoreReopensATelnetPane(t *testing.T) {
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-telnet", Number: 1, Root: domainworkspace.NewPaneNode("pane-telnet"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-telnet",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionTelnet, HostID: "host-telnet",
				Hostname: "console.local", Port: 23, Username: "admin", Label: "console",
			},
		}},
	}
	graph := newWorkspaceGraph(t)
	graph.hosts["host-telnet"] = domainvault.Host{
		ID: "host-telnet", Hostname: "console.local", Port: 23,
		Username: "admin", Protocol: domainvault.ProtocolTelnet,
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restored Telnet Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-telnet")
		return ok && !info.Pending
	})

	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindTelnet || spec.Telnet == nil ||
		spec.Telnet.Hostname != "console.local" || spec.Telnet.Port != 23 {
		t.Fatalf("restored spec = %+v, want the Telnet endpoint", spec)
	}
	if spec.Reconnect == nil {
		t.Fatal("a restored Telnet Pane has no manual reconnect policy")
	}
}

type directorySurface struct {
	fakeSurface
	dir string
}

func (d *directorySurface) Dir() string { return d.dir }

func TestReportedWorkingDirectoryIsPersisted(t *testing.T) {
	repo := &memoryWorkspaceRepository{payload: domainworkspace.EmptyPayload()}
	service := appsession.NewService(&workspaceFactory{})
	t.Cleanup(service.CloseAll)
	surface := &directorySurface{}
	state := NewState(
		WithSessions(service, func(*appsession.Session) (Surface, error) {
			return surface, nil
		}),
		WithWorkspace(appworkspace.NewService(repo), nil),
	)
	if err := state.NewTab(); err != nil {
		t.Fatal(err)
	}
	surface.dir = "/home/me/project"
	state.Flush()

	panes := state.WorkspacePayload().Panes
	if len(panes) != 1 || panes[0].Session.Dir != "/home/me/project" {
		t.Fatalf("payload Panes = %+v, want the reported directory", panes)
	}
	if got := repo.payload.Panes[0].Session.Dir; got != "/home/me/project" {
		t.Fatalf("persisted directory = %q, want /home/me/project", got)
	}
}

func TestRestoreReplaysTheRemoteWorkingDirectory(t *testing.T) {
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-ssh", Number: 1, Root: domainworkspace.NewPaneNode("pane-ssh"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-ssh",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, HostID: "host-1",
				Hostname: "example.com", Port: 22, Username: "root",
				Dir: "/srv/app dir",
			},
		}},
	}
	graph := newWorkspaceGraph(t)
	graph.hosts["host-1"] = domainvault.Host{
		ID: "host-1", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restored SSH Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-ssh")
		return ok && info.Surface != nil
	})

	graph.factory.mu.Lock()
	transport := graph.factory.transports[0]
	graph.factory.mu.Unlock()
	waitFor(t, "the replayed cd command", func() bool {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		return string(transport.input) == "cd -- '/srv/app dir'\n"
	})
}

func TestQueuedSessionActivityMarksTheTabOnFlush(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	id := graph.sessions[0].Snapshot().ID

	graph.state.NoteSessionActivity(id)
	if info, _ := graph.state.TabInfo(0); info.Dirty {
		t.Fatal("activity was applied off the UI thread")
	}
	graph.state.Flush()
	if info, _ := graph.state.TabInfo(0); !info.Dirty {
		t.Fatal("Flush did not mark the off-screen Tab")
	}
	if info, _ := graph.state.TabInfo(1); info.Dirty {
		t.Fatal("the active Tab was marked dirty")
	}
}

func TestMovePaneToTabUsesTheTargetFocus(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	moving := panesOf(t, graph.state, 1)[0]
	sourceID, _ := graph.state.TabID(1)
	targetID, _ := graph.state.TabID(0)

	if !graph.state.MovePaneToTab(sourceID, moving, targetID) {
		t.Fatal("MovePaneToTab returned false")
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want the emptied source Tab removed", graph.state.TabCount())
	}
	if got := panesOf(t, graph.state, 0); len(got) != 2 {
		t.Fatalf("target panes = %v, want two", got)
	}
	if graph.state.ActiveTab() != 0 {
		t.Fatalf("active = %d, want the target Tab", graph.state.ActiveTab())
	}
}

func TestRemoteWorkingDirectoryEligibility(t *testing.T) {
	tests := []struct {
		dir  string
		want bool
	}{
		{"/srv/app", true},
		{"/", true},
		{"~", true},
		{"~/app", true},
		{"", false},
		{"//server/share", false},
		{"~user/app", false},
		{"relative/path", false},
		{`C:\Users\me`, false},
	}
	for _, test := range tests {
		if got := remoteDirEligible(test.dir); got != test.want {
			t.Errorf("remoteDirEligible(%q) = %v, want %v", test.dir, got, test.want)
		}
	}
}

func TestSplittingAnSSHPaneReplaysTheWorkingDirectory(t *testing.T) {
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-ssh", Number: 1, Root: domainworkspace.NewPaneNode("pane-ssh"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-ssh",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, HostID: "host-1",
				Hostname: "example.com", Port: 22, Username: "root",
				Dir: "/srv/app",
			},
		}},
	}
	graph := newWorkspaceGraph(t)
	graph.hosts["host-1"] = domainvault.Host{
		ID: "host-1", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restored SSH Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-ssh")
		return ok && info.Surface != nil
	})

	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	ids := panesOf(t, graph.state, 0)
	graph.flushUntil(t, "the split SSH Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, ids[1])
		return ok && info.Surface != nil
	})

	graph.factory.mu.Lock()
	transport := graph.factory.transports[1]
	graph.factory.mu.Unlock()
	waitFor(t, "the split's replayed cd command", func() bool {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		return string(transport.input) == "cd -- '/srv/app'\n"
	})
}

func TestRestoreReopensANetworkDevicePaneFromTheHostRecord(t *testing.T) {
	// The restore descriptor carries no device mode; it is read from the Host
	// record, so editing the Host takes effect on the next restore.
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-switch", Number: 1, Root: domainworkspace.NewPaneNode("pane-switch"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-switch",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, HostID: "host-switch",
				Hostname: "switch.example.com", Port: 22, Username: "admin", Label: "switch",
			},
		}},
	}
	graph := newWorkspaceGraph(t)
	graph.hosts["host-switch"] = domainvault.Host{
		ID: "host-switch", Hostname: "switch.example.com", Port: 22,
		Username: "admin", Protocol: domainvault.ProtocolSSH,
		DeviceType: domainvault.DeviceTypeNetwork,
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restored network device Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-switch")
		return ok && !info.Pending
	})

	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindSSH || !spec.NetworkDevice {
		t.Fatalf("restored spec = %+v, want raw network-device execution", spec)
	}
}
