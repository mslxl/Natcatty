package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appworkspace "github.com/mslxl/natcatty/application/workspace"
	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// fakeETResumeStore is the slice of the ET secret store the shell uses: notes
// which resume keys were seeded and which the shell deleted.
type fakeETResumeStore struct {
	mu      sync.Mutex
	records map[string]bool
	deletes []string
	err     error
}

func newFakeETResumeStore() *fakeETResumeStore {
	return &fakeETResumeStore{records: make(map[string]bool)}
}

func (s *fakeETResumeStore) seed(resumeKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[resumeKey] = true
}

func (s *fakeETResumeStore) has(resumeKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records[resumeKey]
}

func (s *fakeETResumeStore) Delete(_ context.Context, resumeKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.deletes = append(s.deletes, resumeKey)
	delete(s.records, resumeKey)
	return nil
}

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
	etResume   *fakeETResumeStore
}

func newWorkspaceGraph(t *testing.T) *workspaceGraph {
	return newWorkspaceGraphWithOptions(t)
}

func newWorkspaceGraphWithOptions(t *testing.T, options ...Option) *workspaceGraph {
	t.Helper()
	graph := &workspaceGraph{
		factory:  &workspaceFactory{},
		repo:     &memoryWorkspaceRepository{payload: domainworkspace.EmptyPayload()},
		hosts:    map[model.HostID]domainvault.Host{},
		etResume: newFakeETResumeStore(),
	}
	graph.service = appsession.NewService(graph.factory)
	t.Cleanup(graph.service.CloseAll)
	base := []Option{
		WithSessions(graph.service, func(session *appsession.Session) (Surface, error) {
			graph.sessions = append(graph.sessions, session)
			surface := &fakeSurface{}
			graph.surfaces = append(graph.surfaces, surface)
			return surface, nil
		}),
		WithETResumeStore(graph.etResume),
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
	}
	graph.state = NewState(append(base, options...)...)
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

// TestCloseFocusedPaneWhileMagnifiedClearsTheZoom closes the Pane that fills
// its Tab. The zoom must end with the Pane, or MagnifiedPaneID keeps naming a
// removed Pane and the surviving sibling stays hidden.
func TestCloseFocusedPaneWhileMagnifiedClearsTheZoom(t *testing.T) {
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
	left, right := panesOf(t, graph.state, 0)[0], panesOf(t, graph.state, 0)[1]
	if !graph.state.SetMagnifiedPane(0, left) {
		t.Fatal("SetMagnifiedPane rejected the focused Pane")
	}
	if got, ok := graph.state.FocusedPaneID(0); !ok || got != left {
		t.Fatalf("focused Pane = %q, %v; want the magnified Pane", got, ok)
	}

	if !graph.state.CloseFocusedPane() {
		t.Fatal("CloseFocusedPane returned false")
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want the Tab to survive its second Pane", graph.state.TabCount())
	}
	if got := graph.state.MagnifiedPaneID(0); got != "" {
		t.Fatalf("magnified Pane = %q after closing the zoomed Pane, want the zoom cleared", got)
	}
	if got := panesOf(t, graph.state, 0); !equalPaneIDs(got, []model.PaneID{right}) {
		t.Fatalf("panes = %v, want the surviving sibling", got)
	}
	if got, ok := graph.state.FocusedPaneID(0); !ok || got != right {
		t.Fatalf("focused Pane = %q, %v; want the surviving sibling", got, ok)
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

// TestMovePaneKeepsTheMovedPanesSessionAndTabIdentity pins the acceptance
// contract: a drag rearranges the tree, never the Session behind the Pane.
func TestMovePaneKeepsTheMovedPanesSessionAndTabIdentity(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "both Pane Sessions", func() bool {
		for _, id := range panesOf(t, graph.state, 0) {
			info, ok := graph.state.PaneInfoAt(0, id)
			if !ok || info.Surface == nil {
				return false
			}
		}
		return len(panesOf(t, graph.state, 0)) == 2
	})
	tabID, _ := graph.state.TabID(0)
	panes := panesOf(t, graph.state, 0)
	left, right := panes[0], panes[1]

	moved := graph.state.tabs[0].panes[right]
	session, descriptor := moved.session, moved.descriptor
	if session == nil {
		t.Fatal("the moved Pane has no Session")
	}

	if !graph.state.MovePane(tabID, right, tabID, left, domainworkspace.SideLeft) {
		t.Fatal("MovePane returned false")
	}
	if got, _ := graph.state.TabID(0); got != tabID {
		t.Fatalf("Tab identity = %q, want %q", got, tabID)
	}
	after := graph.state.tabs[0].panes[right]
	if after == nil || after.session != session || after.descriptor != descriptor {
		t.Fatalf("the moved Pane lost its Session or descriptor: %+v", after)
	}
	if got, want := panesOf(t, graph.state, 0), []model.PaneID{right, left}; !equalPaneIDs(got, want) {
		t.Fatalf("panes = %v, want %v", got, want)
	}
}

// TestMovePaneRejectsADropWhoseTargetClosedMidDrag pins the stale-target
// guard: the source is pruned only after the target still exists.
func TestMovePaneRejectsADropWhoseTargetClosedMidDrag(t *testing.T) {
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
	left, right := panesOf(t, graph.state, 0)[0], panesOf(t, graph.state, 0)[1]

	// Close the right Pane mid-drag: it is the drop target the drag started
	// against but no longer exists when the drop lands.
	if !graph.state.FocusPaneID(0, right) || !graph.state.CloseFocusedPane() {
		t.Fatal("closing the drag target failed")
	}
	if got := panesOf(t, graph.state, 0); !equalPaneIDs(got, []model.PaneID{left}) {
		t.Fatalf("panes after close = %v, want only the source", got)
	}

	// The guard's decision is observable through the public drop-zone API:
	// a stale target must be rejected before the drop lands. Delete the state
	// containment check and CanMovePane reports true, failing here even though
	// the tree would later refuse to split around the missing Pane.
	if graph.state.CanMovePane(tabID, left, tabID, right, domainworkspace.SideRight) {
		t.Fatal("CanMovePane accepted a drop on a Pane that closed mid-drag")
	}
	if graph.state.MovePane(tabID, left, tabID, right, domainworkspace.SideRight) {
		t.Fatal("MovePane accepted a drop on a Pane that closed mid-drag")
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want the source Tab untouched", graph.state.TabCount())
	}
	if got := panesOf(t, graph.state, 0); !equalPaneIDs(got, []model.PaneID{left}) {
		t.Fatalf("panes = %v after a stale drop, want the source to survive", got)
	}
}

// TestMagnifiedPaneHidesItsMembersFromDrops pins the magnified guard: while a
// Pane fills its Tab, the hidden members neither accept a drop nor drag.
func TestMagnifiedPaneHidesItsMembersFromDrops(t *testing.T) {
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
	left, right := panesOf(t, graph.state, 0)[0], panesOf(t, graph.state, 0)[1]

	if !graph.state.SetMagnifiedPane(0, left) {
		t.Fatal("SetMagnifiedPane rejected a Pane in the Tab")
	}
	if got := graph.state.MagnifiedPaneID(0); got != left {
		t.Fatalf("magnified Pane = %q, want %q", got, left)
	}
	// Dropping onto the hidden right member is refused.
	if graph.state.MovePane(tabID, left, tabID, right, domainworkspace.SideRight) {
		t.Fatal("a hidden member accepted a drop while magnified")
	}
	// A hidden member cannot start a drag either.
	if graph.state.MovePane(tabID, right, tabID, left, domainworkspace.SideRight) {
		t.Fatal("a hidden member started a drag while magnified")
	}
	if got := panesOf(t, graph.state, 0); !equalPaneIDs(got, []model.PaneID{left, right}) {
		t.Fatalf("panes = %v after rejected drops, want the tree unchanged", got)
	}

	// The magnified Pane itself still accepts a drop from another Tab.
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	otherID, _ := graph.state.TabID(1)
	otherPane := panesOf(t, graph.state, 1)[0]
	if !graph.state.MovePane(otherID, otherPane, tabID, left, domainworkspace.SideRight) {
		t.Fatal("the magnified Pane refused a drop")
	}

	// Toggling restores the split and magnifies the focused Pane again.
	graph.state.SetMagnifiedPane(0, "")
	if !graph.state.FocusPaneID(0, left) {
		t.Fatal("focusing the left Pane failed")
	}
	if !graph.state.TogglePaneZoom() || graph.state.MagnifiedPaneID(0) != left {
		t.Fatal("TogglePaneZoom did not magnify the focused Pane")
	}
	if !graph.state.TogglePaneZoom() || graph.state.MagnifiedPaneID(0) != "" {
		t.Fatal("TogglePaneZoom did not restore the split")
	}
}

// TestDetachPaneClearsStaleMagnification pins the detach path: magnifying one
// Pane and detaching it must end the zoom. Otherwise MagnifiedPaneID points at
// a removed Pane, the Tab body renders nothing, and the remaining member is
// still hidden.
func TestDetachPaneClearsStaleMagnification(t *testing.T) {
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
	left, right := panesOf(t, graph.state, 0)[0], panesOf(t, graph.state, 0)[1]
	if !graph.state.SetMagnifiedPane(0, left) {
		t.Fatal("SetMagnifiedPane rejected a member Pane")
	}
	if !graph.state.DetachPane(0, left) {
		t.Fatal("DetachPane failed")
	}
	if got := graph.state.MagnifiedPaneID(0); got != "" {
		t.Fatalf("magnified Pane = %q after detaching the zoomed Pane, want the zoom cleared", got)
	}
	if got := panesOf(t, graph.state, 0); !equalPaneIDs(got, []model.PaneID{right}) {
		t.Fatalf("panes = %v, want the surviving sibling", got)
	}
	if got, ok := graph.state.FocusedPaneID(0); !ok || got != right {
		t.Fatalf("focused Pane = %q, %v; want the surviving sibling", got, ok)
	}
}

// TestDetachPaneKeepsAValidMagnification pins the other side of the same
// invariant: detaching a hidden member while another Pane is magnified leaves
// the zoom pointing at a Pane that is still in the tree.
func TestDetachPaneKeepsAValidMagnification(t *testing.T) {
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
	left, right := panesOf(t, graph.state, 0)[0], panesOf(t, graph.state, 0)[1]
	if !graph.state.SetMagnifiedPane(0, left) {
		t.Fatal("SetMagnifiedPane rejected a member Pane")
	}
	if !graph.state.DetachPane(0, right) {
		t.Fatal("DetachPane of the hidden member failed")
	}
	if got := graph.state.MagnifiedPaneID(0); got != left {
		t.Fatalf("magnified Pane = %q, want the still-present Pane %q", got, left)
	}
	if got := panesOf(t, graph.state, 0); !equalPaneIDs(got, []model.PaneID{left}) {
		t.Fatalf("panes = %v, want the zoomed Pane", got)
	}
}

// TestMovePaneAcrossTabsClearsTheStaleZoom pins the cross-Tab move path: the
// zoomed Pane leaves its Tab, so the zoom must end and the surviving sibling
// must become both visible and focused. Before the fix the stale check ran
// against the pre-removal root, the zoom survived, and the source Tab kept
// pointing at a Pane it no longer owned.
func TestMovePaneAcrossTabsClearsTheStaleZoom(t *testing.T) {
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
	sourceID, _ := graph.state.TabID(0)
	left, right := panesOf(t, graph.state, 0)[0], panesOf(t, graph.state, 0)[1]
	if !graph.state.SetMagnifiedPane(0, left) {
		t.Fatal("SetMagnifiedPane rejected a member Pane")
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	targetID, _ := graph.state.TabID(1)
	targetPane := panesOf(t, graph.state, 1)[0]

	if !graph.state.MovePane(sourceID, left, targetID, targetPane, domainworkspace.SideRight) {
		t.Fatal("MovePane across Tabs returned false")
	}
	if got := graph.state.MagnifiedPaneID(0); got != "" {
		t.Fatalf("magnified Pane = %q after moving the zoomed Pane out, want the zoom cleared", got)
	}
	if got := panesOf(t, graph.state, 0); !equalPaneIDs(got, []model.PaneID{right}) {
		t.Fatalf("source panes = %v, want the surviving sibling", got)
	}
	if got, ok := graph.state.FocusedPaneID(0); !ok || got != right {
		t.Fatalf("source focus = %q, %v; want the surviving sibling", got, ok)
	}
	if got := graph.state.ActiveTab(); got != 1 {
		t.Fatalf("active Tab = %d, want the drop target", got)
	}
	if got, ok := graph.state.FocusedPaneID(1); !ok || got != left {
		t.Fatalf("target focus = %q, %v; want the moved Pane", got, ok)
	}
}

// TestMovePaneToTabDropClearsTheStaleZoom pins the tab-strip drop path: it
// routes through MovePaneToTab rather than a Pane drop zone, so it needs the
// same stale-zoom invariant.
func TestMovePaneToTabDropClearsTheStaleZoom(t *testing.T) {
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
	sourceID, _ := graph.state.TabID(0)
	left, right := panesOf(t, graph.state, 0)[0], panesOf(t, graph.state, 0)[1]
	if !graph.state.SetMagnifiedPane(0, left) {
		t.Fatal("SetMagnifiedPane rejected a member Pane")
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	targetID, _ := graph.state.TabID(1)

	if !graph.state.MovePaneToTab(sourceID, left, targetID) {
		t.Fatal("MovePaneToTab returned false")
	}
	if got := graph.state.MagnifiedPaneID(0); got != "" {
		t.Fatalf("magnified Pane = %q after the tab-strip drop, want the zoom cleared", got)
	}
	if got := panesOf(t, graph.state, 0); !equalPaneIDs(got, []model.PaneID{right}) {
		t.Fatalf("source panes = %v, want the surviving sibling", got)
	}
	if got, ok := graph.state.FocusedPaneID(0); !ok || got != right {
		t.Fatalf("source focus = %q, %v; want the surviving sibling", got, ok)
	}
	if got := graph.state.ActiveTab(); got != 1 {
		t.Fatalf("active Tab = %d, want the Tab that received the drop", got)
	}
	got := panesOf(t, graph.state, 1)
	if len(got) != 2 {
		t.Fatalf("target panes = %v, want the moved Pane beside the target", got)
	}
	found := false
	for _, id := range got {
		if id == left {
			found = true
		}
	}
	if !found {
		t.Fatalf("target panes = %v, want the moved Pane %q", got, left)
	}
}

// TestMovePaneIntoAMagnifiedTabKeepsItsZoomFocused pins the receiving side: a
// drop into a zoomed Tab must not focus the hidden Pane it was inserted beside.
func TestMovePaneIntoAMagnifiedTabKeepsItsZoomFocused(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	sourceID, _ := graph.state.TabID(0)
	sourcePane := panesOf(t, graph.state, 0)[0]
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the target split", func() bool {
		return len(panesOf(t, graph.state, 1)) == 2
	})
	targetID, _ := graph.state.TabID(1)
	zoomed := panesOf(t, graph.state, 1)[0]
	if !graph.state.SetMagnifiedPane(1, zoomed) {
		t.Fatal("SetMagnifiedPane rejected a member Pane")
	}

	if !graph.state.MovePane(sourceID, sourcePane, targetID, zoomed, domainworkspace.SideRight) {
		t.Fatal("MovePane into the zoomed Tab returned false")
	}
	// The emptied source Tab is removed, so the target becomes Tab 0.
	if got := graph.state.TabCount(); got != 1 {
		t.Fatalf("tabs = %d after the source emptied, want 1", got)
	}
	if got := graph.state.MagnifiedPaneID(0); got != zoomed {
		t.Fatalf("magnified Pane = %q, want the zoomed target %q", got, zoomed)
	}
	if got, ok := graph.state.FocusedPaneID(0); !ok || got != zoomed {
		t.Fatalf("focused Pane = %q, %v; want the zoomed target", got, ok)
	}
}

func TestFocusStaysOnTheMagnifiedPane(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	left, right := panesOf(t, graph.state, 0)[0], panesOf(t, graph.state, 0)[1]
	if !graph.state.SetMagnifiedPane(0, left) {
		t.Fatal("SetMagnifiedPane rejected a member Pane")
	}
	if got, _ := graph.state.FocusedPaneID(0); got != left {
		t.Fatalf("focused Pane = %q after zooming, want the magnified Pane %q", got, left)
	}
	for _, direction := range []domainworkspace.FocusDirection{
		domainworkspace.FocusLeft, domainworkspace.FocusRight,
		domainworkspace.FocusUp, domainworkspace.FocusDown,
	} {
		if graph.state.FocusPane(direction) {
			t.Fatalf("FocusPane(%q) moved focus onto a hidden Pane", direction)
		}
	}
	if graph.state.FocusPaneID(0, right) {
		t.Fatal("FocusPaneID focused a hidden member")
	}
	if got, _ := graph.state.FocusedPaneID(0); got != left {
		t.Fatalf("focused Pane = %q, want the magnified Pane to keep focus", got)
	}
}

// TestSplitWhileMagnifiedRestoresTheSplit pins the split invariant: a split of
// the zoomed Pane reveals the split (so the new Pane is visible) and puts
// focus on the new Pane instead of leaving it hidden behind the zoom.
func TestSplitWhileMagnifiedRestoresTheSplit(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	left := panesOf(t, graph.state, 0)[0]
	if !graph.state.SetMagnifiedPane(0, left) {
		t.Fatal("SetMagnifiedPane rejected a member Pane")
	}
	if err := graph.state.SplitDown(); err != nil {
		t.Fatalf("SplitDown while magnified: %v", err)
	}
	if got := graph.state.MagnifiedPaneID(0); got != "" {
		t.Fatalf("magnified Pane = %q after splitting, want the split restored", got)
	}
	focused, ok := graph.state.FocusedPaneID(0)
	if !ok || focused == left {
		t.Fatalf("focused Pane = %q, %v; want the new Pane", focused, ok)
	}
	if got := len(panesOf(t, graph.state, 0)); got != 3 {
		t.Fatalf("panes = %d after the split, want 3", got)
	}
	if path := focusPathOf(t, graph.state); len(path) == 0 {
		t.Fatal("the split left no focus path")
	}
}

// TestMovePaneIntoAnEmptyTabAdoptsTheSourcePane pins empty-Workspace
// recovery: a Tab with no Panes takes the dropped Pane as its root.
func TestMovePaneIntoAnEmptyTabAdoptsTheSourcePane(t *testing.T) {
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
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	sourceID, _ := graph.state.TabID(0)
	targetID, _ := graph.state.TabID(1)
	left := panesOf(t, graph.state, 0)[0]

	// An emptied Tab is the recovery case: no Pane means no drop target.
	target := graph.state.tabs[1]
	target.panes = map[model.PaneID]*pane{}
	target.Root = nil
	target.FocusPath = nil

	if !graph.state.MovePaneToTab(sourceID, left, targetID) {
		t.Fatal("MovePaneToTab rejected an empty target Tab")
	}
	if got := panesOf(t, graph.state, 1); !equalPaneIDs(got, []model.PaneID{left}) {
		t.Fatalf("target panes = %v, want the adopted Pane", got)
	}
	if graph.state.TabCount() != 2 {
		t.Fatalf("tabs = %d, want the source Tab to keep its other Pane", graph.state.TabCount())
	}
}

func equalPaneIDs(got, want []model.PaneID) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
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

func TestRetryFailedPaneReopensTheSameDescriptor(t *testing.T) {
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
				Hostname: "127.0.0.1", Port: 2323, Label: "telnet-fake",
			},
		}},
	}
	graph := newWorkspaceGraph(t)
	graph.hosts["host-telnet"] = domainvault.Host{
		ID: "host-telnet", Hostname: "127.0.0.1", Port: 2323,
		Protocol: domainvault.ProtocolTelnet,
	}
	var attempts int
	graph.factory.fail = func(appsession.StartSpec) error {
		attempts++
		if attempts == 1 {
			return &appconnection.Error{Kind: appconnection.KindNetwork}
		}
		return nil
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the failed Telnet open", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-telnet")
		return ok && !info.Pending && info.Err != nil
	})
	tabID, ok := graph.state.TabID(0)
	if !ok {
		t.Fatal("the restored Tab has no identity")
	}

	if !graph.state.RetryPane(tabID, "pane-telnet") {
		t.Fatal("RetryPane refused the failed Pane")
	}
	if info, _ := graph.state.PaneInfoAt(0, "pane-telnet"); !info.Pending || info.Err != nil {
		t.Fatalf("Pane after RetryPane = %+v, want a pending reconnect", info)
	}
	if graph.state.RetryPane(tabID, "pane-telnet") {
		t.Fatal("a second RetryPane while pending returned true")
	}

	graph.flushUntil(t, "the retried surface", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-telnet")
		return ok && info.Surface != nil && info.Err == nil
	})
	graph.factory.mu.Lock()
	specs := append([]appsession.StartSpec(nil), graph.factory.specs...)
	graph.factory.mu.Unlock()
	if len(specs) != 2 {
		t.Fatalf("Session opens = %d, want the failed attempt and the retry", len(specs))
	}
	for index, spec := range specs {
		if spec.Kind != appsession.KindTelnet || spec.Telnet == nil ||
			spec.Telnet.Hostname != "127.0.0.1" || spec.Telnet.Port != 2323 {
			t.Fatalf("open %d spec = %+v, want the same Telnet endpoint", index, spec)
		}
	}
	if graph.state.RetryPane(tabID, "pane-telnet") {
		t.Fatal("RetryPane retried a live Pane")
	}
	if graph.state.RetryPane("no-such-tab", "pane-telnet") {
		t.Fatal("RetryPane accepted an unknown Tab")
	}
	if graph.state.RetryPane(tabID, "no-such-pane") {
		t.Fatal("RetryPane accepted an unknown Pane")
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

func TestRestoreReopensMoshAndETPanesFromTheSavedHost(t *testing.T) {
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-transports", Number: 1, Root: domainworkspace.NewPaneNode("pane-mosh"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-mosh",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionMosh, HostID: "host-mosh",
				Hostname: "stale.example", Port: 22, Username: "stale", Label: "roaming",
			},
		}},
	}
	graph := newWorkspaceGraph(t)
	graph.hosts["host-mosh"] = domainvault.Host{
		ID: "host-mosh", Hostname: "mosh.example.com", Port: 2222,
		Username: "alice", Protocol: domainvault.ProtocolMosh,
		MoshServerPath: "/usr/local/bin/mosh-server",
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restored Mosh Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-mosh")
		return ok && !info.Pending
	})
	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindMosh || spec.Mosh == nil ||
		spec.Mosh.Hostname != "mosh.example.com" || spec.Mosh.Port != 2222 ||
		spec.Mosh.Username != "alice" || spec.Mosh.ServerPath != "/usr/local/bin/mosh-server" {
		t.Fatalf("restored Mosh spec = %+v", spec)
	}

	payload = domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-transports", Number: 1, Root: domainworkspace.NewPaneNode("pane-et"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-et",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionET, HostID: "host-et",
				Hostname: "stale.example", Port: 22, Username: "stale", Label: "et",
				ResumeKey: "et-resume-retained",
			},
		}},
	}
	graph = newWorkspaceGraph(t)
	graph.hosts["host-et"] = domainvault.Host{
		ID: "host-et", Hostname: "et.example.com", Port: 2022,
		Username: "bob", Protocol: domainvault.ProtocolET, ETPort: 8737,
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restored ET Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-et")
		return ok && !info.Pending
	})
	spec = graph.factory.lastSpec()
	if spec.Kind != appsession.KindET || spec.ET == nil ||
		spec.ET.Hostname != "et.example.com" || spec.ET.Port != 2022 ||
		spec.ET.Username != "bob" || spec.ET.ETPort != 8737 {
		t.Fatalf("restored ET spec = %+v", spec)
	}
	if spec.ResumeKey != "et-resume-retained" {
		t.Fatalf("restored ET resume key = %q, want the descriptor's locator", spec.ResumeKey)
	}
}

// TestRestoreGivesALegacyETPaneANewResumeKey: descriptors written before ET
// resume existed still get a locator, so the fresh session's credentials can
// be found after the next restart.
func TestRestoreGivesALegacyETPaneANewResumeKey(t *testing.T) {
	graph := newWorkspaceGraph(t)
	graph.hosts["host-et"] = domainvault.Host{
		ID: "host-et", Hostname: "et.example.com", Port: 2022,
		Username: "bob", Protocol: domainvault.ProtocolET, ETPort: 8737,
	}
	graph.repo.payload = domainworkspace.Payload{
		Version: domainworkspace.PayloadVersion, ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-et", Number: 1, Root: domainworkspace.NewPaneNode("pane-et"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-et",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionET, HostID: "host-et",
				Hostname: "et.example.com", Port: 2022, Username: "bob", Label: "et",
			},
		}},
	}
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restored ET Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-et")
		return ok && !info.Pending && info.Err == nil
	})
	spec := graph.factory.lastSpec()
	if spec.ResumeKey == "" {
		t.Fatal("a restored ET Pane without a locator did not get a resume key")
	}
	panes := graph.state.WorkspacePayload().Panes
	if len(panes) != 1 || panes[0].Session.ResumeKey != spec.ResumeKey {
		t.Fatalf("saved restored ET Pane = %+v, want resume key %q", panes, spec.ResumeKey)
	}
}

// TestRestoreETPaneFallsBackWhenLegacyServerRefusesReattach: a pair saved by
// an older build is unusable on a pre-v7 server. Restoring the Pane must clear
// the stale secret and explicitly start one fresh session, with a visible
// notice, instead of leaving the Pane permanently failed and retrying the same
// key forever. The fake factory models the adapter's typed outcome: the first
// Open with a saved pair returns ErrReattachUnsupported; the pair is gone on
// the second Open.
func TestRestoreETPaneFallsBackWhenLegacyServerRefusesReattach(t *testing.T) {
	const resumeKey = "et-resume-legacy"
	graph := newWorkspaceGraph(t)
	graph.hosts["host-et"] = domainvault.Host{
		ID: "host-et", Hostname: "et.example.com", Port: 2022,
		Username: "bob", Protocol: domainvault.ProtocolET, ETPort: 8737,
	}
	graph.etResume.seed(resumeKey)
	graph.factory.fail = func(spec appsession.StartSpec) error {
		if spec.Kind == appsession.KindET && spec.ResumeKey == resumeKey && graph.etResume.has(resumeKey) {
			return fmt.Errorf("%w: server does not support session reattach; upgrade etserver",
				etmodel.ErrReattachUnsupported)
		}
		return nil
	}
	graph.repo.payload = domainworkspace.Payload{
		Version: domainworkspace.PayloadVersion, ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-et", Number: 1, Root: domainworkspace.NewPaneNode("pane-et"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-et",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionET, HostID: "host-et",
				Hostname: "et.example.com", Port: 2022, Username: "bob", Label: "et",
				ResumeKey: resumeKey,
			},
		}},
	}
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the fresh ET Pane after the legacy fallback", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-et")
		return ok && !info.Pending && info.Err == nil && info.Surface != nil
	})
	if got := graph.factory.specCount(); got != 2 {
		t.Fatalf("ET opens = %d, want the refused reattach followed by one fresh session", got)
	}
	if graph.etResume.has(resumeKey) {
		t.Fatal("the stale pair survived the fallback")
	}
	if len(graph.etResume.deletes) != 1 || graph.etResume.deletes[0] != resumeKey {
		t.Fatalf("secret deletes = %v, want exactly %q", graph.etResume.deletes, resumeKey)
	}
	snapshot, ok := graph.state.ActiveSnapshot()
	if !ok || snapshot.Notice == nil || snapshot.Notice.Code != appsession.NoticeETResumeFallback {
		t.Fatalf("fallback notice = %+v, %v; want the explicit ET resume fallback notice", snapshot.Notice, ok)
	}
}

// TestFailedETPaneCloseClearsSavedCredentials: a Pane whose Open never
// produced a Session has no transport that could clear its pair. Closing that
// Pane explicitly must clear it rather than orphan it in the secret store.
func TestFailedETPaneCloseClearsSavedCredentials(t *testing.T) {
	const resumeKey = "et-resume-failed"
	graph := newWorkspaceGraph(t)
	graph.hosts["host-et"] = domainvault.Host{
		ID: "host-et", Hostname: "et.example.com", Port: 2022,
		Username: "bob", Protocol: domainvault.ProtocolET, ETPort: 8737,
	}
	graph.etResume.seed(resumeKey)
	graph.factory.fail = func(spec appsession.StartSpec) error {
		if spec.Kind == appsession.KindET {
			return errors.New("network unreachable")
		}
		return nil
	}
	graph.repo.payload = domainworkspace.Payload{
		Version: domainworkspace.PayloadVersion, ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-et", Number: 1, Root: domainworkspace.NewPaneNode("pane-et"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-et",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionET, HostID: "host-et",
				Hostname: "et.example.com", Port: 2022, Username: "bob", Label: "et",
				ResumeKey: resumeKey,
			},
		}},
	}
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the failed ET Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-et")
		return ok && !info.Pending && info.Err != nil
	})
	if !graph.etResume.has(resumeKey) {
		t.Fatal("a failed Open cleared the pair before the user closed the Pane")
	}
	if !graph.state.CloseTab(0) {
		t.Fatal("CloseTab returned false")
	}
	if graph.etResume.has(resumeKey) {
		t.Fatal("closing a failed ET Pane left its saved pair orphaned")
	}
	if len(graph.etResume.deletes) != 1 || graph.etResume.deletes[0] != resumeKey {
		t.Fatalf("secret deletes = %v, want exactly %q", graph.etResume.deletes, resumeKey)
	}
}

// TestETSplitGetsItsOwnResumeKey keeps two Panes' secret entries independent:
// closing one must not clear the other's saved session.
func TestETSplitGetsItsOwnResumeKey(t *testing.T) {
	graph := newWorkspaceGraph(t)
	graph.hosts["host-et"] = domainvault.Host{
		ID: "host-et", Hostname: "et.example.com", Port: 2022,
		Username: "bob", Protocol: domainvault.ProtocolET, ETPort: 8737,
	}
	if err := graph.state.OpenHost(graph.hosts["host-et"]); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	graph.flushUntil(t, "the first ET Pane", func() bool {
		return graph.state.TabCount() == 1 && graph.factory.specCount() == 1 &&
			graph.state.ActiveSurface() != nil
	})
	first := graph.factory.lastSpec()
	if first.ResumeKey == "" {
		t.Fatal("the first ET Pane has no resume key")
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	graph.flushUntil(t, "the split ET Pane", func() bool {
		return graph.factory.specCount() == 2 && len(graph.surfaces) == 2
	})
	second := graph.factory.lastSpec()
	if second.Kind != appsession.KindET || second.ResumeKey == "" {
		t.Fatalf("split ET spec = %+v, want a resume key", second)
	}
	if second.ResumeKey == first.ResumeKey {
		t.Fatalf("split ET Pane reused resume key %q", first.ResumeKey)
	}
}

// TestETDuplicateGetsItsOwnResumeKey: a duplicated Tab is a new client, so it
// must not share the source's saved session and fight over ET's single slot.
func TestETDuplicateGetsItsOwnResumeKey(t *testing.T) {
	graph := newWorkspaceGraph(t)
	graph.hosts["host-et"] = domainvault.Host{
		ID: "host-et", Hostname: "et.example.com", Port: 2022,
		Username: "bob", Protocol: domainvault.ProtocolET, ETPort: 8737,
	}
	if err := graph.state.OpenHost(graph.hosts["host-et"]); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	graph.flushUntil(t, "the first ET Pane", func() bool {
		return graph.state.TabCount() == 1 && graph.factory.specCount() == 1 &&
			graph.state.ActiveSurface() != nil
	})
	first := graph.factory.lastSpec()
	if first.ResumeKey == "" {
		t.Fatal("the first ET Pane has no resume key")
	}
	if err := graph.state.DuplicateTab(0); err != nil {
		t.Fatalf("DuplicateTab: %v", err)
	}
	graph.flushUntil(t, "the duplicated ET Pane", func() bool {
		return graph.state.TabCount() == 2 && graph.factory.specCount() == 2
	})
	second := graph.factory.lastSpec()
	if second.Kind != appsession.KindET || second.ResumeKey == "" {
		t.Fatalf("duplicated ET spec = %+v, want a resume key", second)
	}
	if second.ResumeKey == first.ResumeKey {
		t.Fatalf("duplicated ET Pane reused resume key %q", first.ResumeKey)
	}
	panes := graph.state.WorkspacePayload().Panes
	keys := make(map[string]bool, len(panes))
	for _, pane := range panes {
		if pane.Session.Kind != domainworkspace.SessionET {
			continue
		}
		if pane.Session.ResumeKey == "" || keys[pane.Session.ResumeKey] {
			t.Fatalf("workspace ET resume keys are not distinct: %+v", panes)
		}
		keys[pane.Session.ResumeKey] = true
	}
	if len(keys) != 2 {
		t.Fatalf("workspace holds %d distinct ET resume keys, want 2: %+v", len(keys), panes)
	}
}

func (f *workspaceFactory) transportAt(index int) *fakeTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.transports) {
		return nil
	}
	return f.transports[index]
}

// sendTransportOutput delivers one chunk to a live Session without blocking
// forever if the Session already closed the transport.
func sendTransportOutput(t *testing.T, transport *fakeTransport, text string) {
	t.Helper()
	select {
	case transport.output <- []byte(text):
	case <-transport.closed:
		t.Fatalf("the transport closed before %q could be delivered", text)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out delivering %q", text)
	}
}

// TestRestoreResolvesASavedLocalHostsStartupCommand is the workspace half of
// the Local Host fix: the descriptor names a saved Host, so the restored
// Session keeps that association, runs its startup command once after the
// shell's prompt, and never repeats it on later output.
func TestRestoreResolvesASavedLocalHostsStartupCommand(t *testing.T) {
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-local", Number: 1, Root: domainworkspace.NewPaneNode("pane-local"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-local",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionLocal, Label: "local-host",
				Number: 1, HostID: "host-local",
			},
		}},
	}
	graph := newWorkspaceGraph(t)
	graph.hosts["host-local"] = domainvault.Host{
		ID: "host-local", Label: "local-host", Protocol: domainvault.ProtocolLocal,
		StartupCommand: "echo restored",
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restored local Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-local")
		return ok && !info.Pending && info.Surface != nil
	})

	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindLocal || spec.HostID != "host-local" {
		t.Fatalf("restored spec = %+v, want the saved Local Host association", spec)
	}
	if spec.StartupCommand != "echo restored" {
		t.Fatalf("restored StartupCommand = %q, want the Host's command", spec.StartupCommand)
	}

	transport := graph.factory.transportAt(0)
	sendTransportOutput(t, transport, "$ ")
	waitTransportInput(t, transport, "echo restored\r")

	before := graph.sessions[0].Snapshot().LastActivity
	sendTransportOutput(t, transport, "later output")
	waitFor(t, "the later output", func() bool {
		return graph.sessions[0].Snapshot().LastActivity.After(before)
	})
	if got := strings.Count(transport.inputString(), "echo restored\r"); got != 1 {
		t.Fatalf("the startup command ran %d times on the restored connection", got)
	}
}

// TestRestoredSSHPaneReplaysItsStartupCommandOncePerReconnect proves the
// restore path keeps the Host's startup command and that each reconnect
// replays it once, not once per output burst.
func TestRestoredSSHPaneReplaysItsStartupCommandOncePerReconnect(t *testing.T) {
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-ssh", Number: 1, Root: domainworkspace.NewPaneNode("pane-ssh"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-ssh",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, Label: "prod",
				HostID: "host-ssh", Hostname: "prod.example.com", Port: 22,
				Username: "root",
			},
		}},
	}
	graph := newWorkspaceGraph(t)
	graph.hosts["host-ssh"] = domainvault.Host{
		ID: "host-ssh", Label: "prod", Hostname: "prod.example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
		StartupCommand: "echo restored",
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restored SSH Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-ssh")
		return ok && !info.Pending && info.Surface != nil
	})

	first := graph.factory.transportAt(0)
	sendTransportOutput(t, first, "$ ")
	waitTransportInput(t, first, "echo restored\r")

	before := graph.sessions[0].Snapshot().LastActivity
	sendTransportOutput(t, first, "more output\r\n")
	waitFor(t, "the output after restore", func() bool {
		return graph.sessions[0].Snapshot().LastActivity.After(before)
	})
	if got := strings.Count(first.inputString(), "echo restored\r"); got != 1 {
		t.Fatalf("the startup command ran %d times on the restored connection", got)
	}

	if !graph.sessions[0].ReconnectNow() {
		t.Fatal("ReconnectNow was refused")
	}
	waitFor(t, "the reconnected transport", func() bool { return graph.factory.specCount() == 2 })
	second := graph.factory.transportAt(1)
	if got := second.inputString(); got != "" {
		t.Fatalf("the startup command ran before the reconnected shell spoke: %q", got)
	}
	sendTransportOutput(t, second, "$ ")
	waitTransportInput(t, second, "echo restored\r")
	before = graph.sessions[0].Snapshot().LastActivity
	sendTransportOutput(t, second, "after reconnect\r\n")
	waitFor(t, "the output after reconnect", func() bool {
		return graph.sessions[0].Snapshot().LastActivity.After(before)
	})
	if got := strings.Count(second.inputString(), "echo restored\r"); got != 1 {
		t.Fatalf("the startup command replayed %d times after reconnect", got)
	}
}

// TestPendingTerminalPaneKeepsTheToolbarBeforeItsSessionOpens proves the
// terminal-toolbar gate follows the Pane's kind rather than whether its
// Session exists yet. A Pane opened by a split is pending with a nil Session
// until the next Flush applies the background open, and it must keep the
// toolbar during that window.
func TestPendingTerminalPaneKeepsTheToolbarBeforeItsSessionOpens(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	id, ok := graph.state.FocusedPaneID(0)
	if !ok {
		t.Fatal("split Tab has no focused Pane")
	}
	// The open runs on its own goroutine and is applied only by Flush, so the
	// new Pane is still pending with no Session here.
	value := graph.state.tabs[0].panes[id]
	if value == nil || value.session != nil || !value.pending {
		t.Fatalf("split Pane = %+v, want a pending Pane without a Session", value)
	}
	info, ok := graph.state.PaneInfoAt(0, id)
	if !ok || !info.Pending || !info.ByteStream {
		t.Fatalf("PaneInfo = %+v, %v; want a pending Pane with a byte stream", info, ok)
	}
}

// TestVNCPaneNeverShowsTheTerminalToolbar proves a pixel Pane stays out of the
// terminal-toolbar gate even while it is connecting and has no Session. A fix
// that merely defaults a nil Session to "terminal" would regress this.
func TestVNCPaneNeverShowsTheTerminalToolbar(t *testing.T) {
	graph := newWorkspaceGraph(t)
	tab := graph.state.newTab(TabOptions{Descriptor: domainworkspace.SessionDescriptor{
		Kind:     domainworkspace.SessionVNC,
		HostID:   "vnc-desk",
		Hostname: "desk.local",
		Port:     5901,
		Label:    "desk",
	}})
	graph.state.tabs = append(graph.state.tabs, tab)
	id := tab.paneIDs()[0]
	// Model the connect window: the Pane is visible but its Session is nil.
	tab.panes[id].pending = true

	info, ok := graph.state.PaneInfoAt(0, id)
	if !ok || !info.Pending {
		t.Fatalf("PaneInfo = %+v, %v; want a pending Pane", info, ok)
	}
	if info.ByteStream {
		t.Fatal("VNC Pane reported a byte stream; the terminal toolbar must stay hidden")
	}
}

// TestKeyboardGrabFollowsTheLiveVisiblePane pins the accelerator gate: a live,
// visible Pane that took the keyboard reports true, while a home page, SFTP, a
// hibernated Surface, an opening Pane, and a failed Pane all report false. The
// Surface keeps the user's preference throughout, so returning to the live Pane
// restores the grab. Detached windows read their own predicate and are not
// affected by which surface the main window shows.
func TestKeyboardGrabFollowsTheLiveVisiblePane(t *testing.T) {
	graph := newWorkspaceGraph(t)
	// A VNC Pane with a live Session and a viewer Surface that implements both
	// the grab and the hibernation seam. The fake transport cannot provide the
	// RFB display a real VNC Open requires, so the Session is local while the
	// Pane's descriptor stays a pixel kind; the accelerator gate reads only the
	// Surface and the Session's liveness.
	session, err := graph.service.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindLocal, Label: "desk",
	})
	if err != nil {
		t.Fatalf("open a live Session: %v", err)
	}
	surface := &fakeSurface{keyboardGrab: true}
	tab := graph.state.newTab(TabOptions{Descriptor: domainworkspace.SessionDescriptor{
		Kind: domainworkspace.SessionVNC, Label: "desk", HostID: "vnc-desk",
		Hostname: "desk.local", Port: 5901,
	}})
	tab.attachOnlySession(session, surface)
	graph.state.tabs = append(graph.state.tabs, tab)
	graph.state.SelectTab(0)

	if !graph.state.KeyboardGrab() {
		t.Fatal("a live visible grabbing Pane did not report the grab")
	}

	for _, page := range []struct {
		name string
		show func()
	}{
		{name: "Vault", show: graph.state.ShowVault},
		{name: "Logs", show: graph.state.ShowLogs},
		{name: "Monitor", show: graph.state.ShowMonitor},
		{name: "System Manager", show: graph.state.ShowSystemManager},
		{name: "SFTP", show: graph.state.ShowSFTP},
	} {
		page.show()
		if graph.state.KeyboardGrab() {
			t.Fatalf("%s: the hidden work Tab still grabbed the keyboard", page.name)
		}
		// Returning to the work Tab restores the grab; the Surface was never
		// asked to forget the user's preference.
		graph.state.SelectTab(0)
		if !graph.state.KeyboardGrab() {
			t.Fatalf("%s: the visible work Tab did not restore the grab", page.name)
		}
	}

	surface.hibernated = true
	if graph.state.KeyboardGrab() {
		t.Fatal("a hibernated Surface grabbed the keyboard")
	}
	surface.hibernated = false
	if !graph.state.KeyboardGrab() {
		t.Fatal("resuming the Surface did not restore the grab")
	}

	paneID, _ := graph.state.FocusedPaneID(0)
	pane := graph.state.tabs[0].panes[paneID]
	pane.pending = true
	if graph.state.KeyboardGrab() {
		t.Fatal("an opening Pane grabbed the keyboard")
	}
	pane.pending = false
	pane.err = errors.New("connection failed")
	if graph.state.KeyboardGrab() {
		t.Fatal("a failed Pane grabbed the keyboard")
	}
	pane.err = nil

	// A detached window asks its own predicate, so showing the Vault in the
	// main window does not release the detached Tab's guest input.
	if !graph.state.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	detached := graph.state.DetachedTabIDs()[0]
	graph.state.ShowVault()
	if graph.state.KeyboardGrab() {
		t.Fatal("the main window grabbed with no work Tab visible")
	}
	if !graph.state.DetachedKeyboardGrab(detached) {
		t.Fatal("the detached window lost its grab when the main window showed the Vault")
	}
	if !surface.keyboardGrab {
		t.Fatal("the grab preference was cleared instead of gated")
	}
}

// TestSelectTabReleasesTheOutgoingPanesHeldInput is the lifecycle seam the
// hidden-VNC-Tab risk exposed: a Tab that stops being built receives no MyGo
// key-up, so the shell must ask its Surface to release guest input as it
// switches away.
func TestSelectTabReleasesTheOutgoingPanesHeldInput(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "both Pane surfaces", func() bool {
		return graph.state.TabCount() == 2 && len(graph.surfaces) == 2
	})
	// Opening the second Tab already released the first Tab's input. Start the
	// switch assertion from a clean count.
	for _, surface := range graph.surfaces {
		surface.releases = 0
	}

	outgoing, ok := graph.state.ActiveSurface().(*fakeSurface)
	if !ok || outgoing == nil {
		t.Fatal("the active Tab has no fake surface")
	}
	graph.state.SelectTab(0)
	if outgoing.releases != 1 {
		t.Fatalf("outgoing surface releases = %d, want 1", outgoing.releases)
	}
	incoming, ok := graph.state.ActiveSurface().(*fakeSurface)
	if !ok || incoming == nil {
		t.Fatal("the selected Tab has no fake surface")
	}
	if incoming.releases != 0 {
		t.Fatalf("incoming surface releases = %d, want 0", incoming.releases)
	}
}

// TestOpeningATabReleasesTheOutgoingPanesInput covers the other main way a
// different Tab becomes visible: opening one, whether from Ctrl+T or a Host,
// must not leave the previously visible Pane holding a key or pointer button.
func TestOpeningATabReleasesTheOutgoingPanesInput(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the first Pane surface", func() bool {
		return graph.state.TabCount() == 1 && len(graph.surfaces) == 1
	})
	outgoing, ok := graph.state.ActiveSurface().(*fakeSurface)
	if !ok || outgoing == nil {
		t.Fatal("the first Tab has no fake surface")
	}

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if outgoing.releases != 1 {
		t.Fatalf("outgoing surface releases = %d, want 1", outgoing.releases)
	}
}

// TestDetachAndCloseReleaseHeldInput covers the other two ways a Surface stops
// rendering: detaching its Tab and closing it.
func TestDetachAndCloseReleaseHeldInput(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the Pane surface", func() bool {
		return graph.state.TabCount() == 1 && len(graph.surfaces) == 1
	})
	detached, _ := graph.state.ActiveSurface().(*fakeSurface)
	if !graph.state.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	if detached == nil || detached.releases != 1 {
		t.Fatalf("detached surface releases = %v, want 1", detached)
	}

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the second Pane surface", func() bool {
		return len(graph.surfaces) == 2
	})
	closed, _ := graph.state.ActiveSurface().(*fakeSurface)
	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	if closed == nil || closed.releases != 1 {
		t.Fatalf("closed surface releases = %v, want 1", closed)
	}
	if closed.closed != true {
		t.Fatal("the closed Pane's surface was not closed")
	}
}

// resetSurfaceReleases starts a release assertion from a clean count.
func resetSurfaceReleases(graph *workspaceGraph) {
	for _, surface := range graph.surfaces {
		surface.releases = 0
	}
}

// fakeSurfaceAt returns the fake Surface of one Pane, failing the test when
// the Pane has not opened yet.
func fakeSurfaceAt(t *testing.T, state *State, tabIndex int, id model.PaneID) *fakeSurface {
	t.Helper()
	info, ok := state.PaneInfoAt(tabIndex, id)
	if !ok || info.Surface == nil {
		t.Fatalf("Pane %q at Tab %d has no Surface", id, tabIndex)
	}
	surface, ok := info.Surface.(*fakeSurface)
	if !ok {
		t.Fatalf("Pane %q surface is %T, want *fakeSurface", id, info.Surface)
	}
	return surface
}

// TestSelectTabReleasesEveryPaneInASplitTab is the non-focused-pane case: MyGo
// hit-tests pointer input, so a split member that never held focus can still
// have a button down when the Tab is hidden. Hiding the Tab must release every
// member, not only tab.focusedPane().
func TestSelectTabReleasesEveryPaneInASplitTab(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "both split surfaces", func() bool {
		return len(panesOf(t, graph.state, 0)) == 2 && len(graph.surfaces) == 2
	})
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the second Tab surface", func() bool {
		return len(graph.surfaces) == 3
	})
	graph.state.SelectTab(0)
	resetSurfaceReleases(graph)

	ids := panesOf(t, graph.state, 0)
	if !graph.state.FocusPaneID(0, ids[0]) {
		t.Fatal("FocusPaneID rejected the left Pane")
	}
	outgoing := []*fakeSurface{
		fakeSurfaceAt(t, graph.state, 0, ids[0]),
		fakeSurfaceAt(t, graph.state, 0, ids[1]),
	}
	incoming := fakeSurfaceAt(t, graph.state, 1, panesOf(t, graph.state, 1)[0])

	graph.state.SelectTab(1)
	for index, surface := range outgoing {
		if surface.releases != 1 {
			t.Fatalf("outgoing Pane %d releases = %d, want 1", index, surface.releases)
		}
	}
	if incoming.releases != 0 {
		t.Fatalf("incoming Pane releases = %d, want 0", incoming.releases)
	}
}

// TestDetachTabReleasesEveryPaneInASplitTab mirrors the hidden-Tab case for
// detach: the whole subtree leaves this window, so every member releases.
func TestDetachTabReleasesEveryPaneInASplitTab(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "both split surfaces", func() bool {
		return len(panesOf(t, graph.state, 0)) == 2 && len(graph.surfaces) == 2
	})
	ids := panesOf(t, graph.state, 0)
	surfaces := []*fakeSurface{
		fakeSurfaceAt(t, graph.state, 0, ids[0]),
		fakeSurfaceAt(t, graph.state, 0, ids[1]),
	}
	resetSurfaceReleases(graph)

	if !graph.state.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	for index, surface := range surfaces {
		if surface.releases != 1 {
			t.Fatalf("detached Pane %d releases = %d, want 1", index, surface.releases)
		}
	}
}

// TestMagnifyReleasesEveryHiddenPane proves zoom is not a focused-Pane-only
// transition: every other member of the split stops rendering, and the Pane
// that stays visible is left alone. Un-zooming hides nothing, so it must not
// release the Panes it just restored.
func TestMagnifyReleasesEveryHiddenPane(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the first split", func() bool {
		return len(panesOf(t, graph.state, 0)) == 2 && len(graph.surfaces) == 2
	})
	// Split one member again so more than one Pane is hidden by the zoom.
	first := panesOf(t, graph.state, 0)[0]
	if !graph.state.FocusPaneID(0, first) {
		t.Fatal("FocusPaneID rejected the first Pane")
	}
	if err := graph.state.SplitDown(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the second split", func() bool {
		return len(panesOf(t, graph.state, 0)) == 3 && len(graph.surfaces) == 3
	})

	ids := panesOf(t, graph.state, 0)
	surfaces := map[model.PaneID]*fakeSurface{}
	for _, id := range ids {
		surfaces[id] = fakeSurfaceAt(t, graph.state, 0, id)
	}
	if !graph.state.FocusPaneID(0, first) {
		t.Fatal("FocusPaneID rejected the first Pane")
	}
	resetSurfaceReleases(graph)

	if !graph.state.SetMagnifiedPane(0, first) {
		t.Fatal("SetMagnifiedPane rejected the first Pane")
	}
	if got := surfaces[first].releases; got != 0 {
		t.Fatalf("visible magnified Pane releases = %d, want 0", got)
	}
	for _, id := range ids {
		if id == first {
			continue
		}
		if got := surfaces[id].releases; got != 1 {
			t.Fatalf("hidden Pane %q releases = %d, want 1", id, got)
		}
	}

	// The magnified Pane is focused, so TogglePaneZoom restores the split.
	if !graph.state.TogglePaneZoom() || graph.state.MagnifiedPaneID(0) != "" {
		t.Fatal("TogglePaneZoom did not restore the split")
	}
	if got := surfaces[first].releases; got != 0 {
		t.Fatalf("visible Pane releases = %d after un-zoom, want 0", got)
	}
	for _, id := range ids {
		if id == first {
			continue
		}
		if got := surfaces[id].releases; got != 1 {
			t.Fatalf("un-zoom released Pane %q again: %d, want 1", id, got)
		}
	}

	// TogglePaneZoom's zoom path releases the newly hidden members too.
	if !graph.state.TogglePaneZoom() || graph.state.MagnifiedPaneID(0) != first {
		t.Fatal("TogglePaneZoom did not magnify the focused Pane")
	}
	if got := surfaces[first].releases; got != 0 {
		t.Fatalf("magnified Pane releases = %d after TogglePaneZoom, want 0", got)
	}
	for _, id := range ids {
		if id == first {
			continue
		}
		if got := surfaces[id].releases; got != 2 {
			t.Fatalf("hidden Pane %q releases = %d after TogglePaneZoom, want 2", id, got)
		}
	}
}

// TestMovePaneIntoAMagnifiedTabReleasesTheMovedPane covers the cross-Tab drop
// whose destination shows only its zoomed Pane: the inserted Pane never
// renders there, so it releases both any held key and any pointer button.
func TestMovePaneIntoAMagnifiedTabReleasesTheMovedPane(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the source Pane surface", func() bool {
		return len(graph.surfaces) == 1
	})
	sourceTab, _ := graph.state.TabID(0)
	sourcePane := panesOf(t, graph.state, 0)[0]
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the target split", func() bool {
		return len(graph.surfaces) == 3 && len(panesOf(t, graph.state, 1)) == 2
	})
	targetTab, _ := graph.state.TabID(1)
	targetPanes := panesOf(t, graph.state, 1)
	zoomed := targetPanes[0]
	if !graph.state.SetMagnifiedPane(1, zoomed) {
		t.Fatal("SetMagnifiedPane rejected the target Pane")
	}
	movedSurface := fakeSurfaceAt(t, graph.state, 0, sourcePane)
	zoomedSurface := fakeSurfaceAt(t, graph.state, 1, zoomed)
	resetSurfaceReleases(graph)

	if !graph.state.MovePane(sourceTab, sourcePane, targetTab, zoomed, domainworkspace.SideRight) {
		t.Fatal("MovePane into the magnified Tab returned false")
	}
	if got := movedSurface.releases; got != 1 {
		t.Fatalf("moved Pane releases = %d, want 1 after landing hidden", got)
	}
	if got := zoomedSurface.releases; got != 0 {
		t.Fatalf("visible zoomed Pane releases = %d, want 0", got)
	}
}

// TestMovePaneIntoANonMagnifiedTabKeepsTheMovedPanesInput is the other side of
// the drop: a non-magnified destination makes the moved Pane visible, so
// releasing it would be spurious.
func TestMovePaneIntoANonMagnifiedTabKeepsTheMovedPanesInput(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the source Pane surface", func() bool {
		return len(graph.surfaces) == 1
	})
	sourceTab, _ := graph.state.TabID(0)
	sourcePane := panesOf(t, graph.state, 0)[0]
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the target Pane surface", func() bool {
		return len(graph.surfaces) == 2
	})
	targetTab, _ := graph.state.TabID(1)
	targetPane := panesOf(t, graph.state, 1)[0]
	movedSurface := fakeSurfaceAt(t, graph.state, 0, sourcePane)
	targetSurface := fakeSurfaceAt(t, graph.state, 1, targetPane)
	resetSurfaceReleases(graph)

	if !graph.state.MovePane(sourceTab, sourcePane, targetTab, targetPane, domainworkspace.SideRight) {
		t.Fatal("MovePane into the plain Tab returned false")
	}
	if got := movedSurface.releases; got != 0 {
		t.Fatalf("moved Pane releases = %d, want 0 while it stays visible", got)
	}
	if got := targetSurface.releases; got != 0 {
		t.Fatalf("destination Pane releases = %d, want 0 while it stays visible", got)
	}
}

// TestMovingTheMagnifiedPaneOutReleasesItOnly pins the source side: moving the
// zoomed Pane away clears the source's zoom, so the surviving member becomes
// the visible one and must not be released, while the moved Pane lands hidden
// in the magnified destination and does release.
func TestMovingTheMagnifiedPaneOutReleasesItOnly(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the source split", func() bool {
		return len(graph.surfaces) == 2 && len(panesOf(t, graph.state, 0)) == 2
	})
	sourceTab, _ := graph.state.TabID(0)
	left, right := panesOf(t, graph.state, 0)[0], panesOf(t, graph.state, 0)[1]
	if !graph.state.FocusPaneID(0, left) || !graph.state.SetMagnifiedPane(0, left) {
		t.Fatal("could not magnify the source Pane")
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the target split", func() bool {
		return len(graph.surfaces) == 4 && len(panesOf(t, graph.state, 1)) == 2
	})
	targetTab, _ := graph.state.TabID(1)
	zoomed := panesOf(t, graph.state, 1)[0]
	if !graph.state.SetMagnifiedPane(1, zoomed) {
		t.Fatal("could not magnify the target Pane")
	}
	leftSurface := fakeSurfaceAt(t, graph.state, 0, left)
	rightSurface := fakeSurfaceAt(t, graph.state, 0, right)
	zoomedSurface := fakeSurfaceAt(t, graph.state, 1, zoomed)
	resetSurfaceReleases(graph)

	if !graph.state.MovePane(sourceTab, left, targetTab, zoomed, domainworkspace.SideRight) {
		t.Fatal("MovePane across Tabs returned false")
	}
	if got := leftSurface.releases; got != 1 {
		t.Fatalf("moved magnified Pane releases = %d, want 1", got)
	}
	if got := rightSurface.releases; got != 0 {
		t.Fatalf("surviving source Pane releases = %d, want 0", got)
	}
	if got := zoomedSurface.releases; got != 0 {
		t.Fatalf("visible destination Pane releases = %d, want 0", got)
	}
	if got := graph.state.MagnifiedPaneID(0); got != "" {
		t.Fatalf("source magnification = %q after the move, want cleared", got)
	}
	if got, ok := graph.state.FocusedPaneID(0); !ok || got != right {
		t.Fatalf("source focus = %q, %v; want the surviving Pane", got, ok)
	}
}

// TestRestoreReopensAVNCPaneFromTheSavedHost proves a restart restores an open
// VNC Tab through the ordinary descriptor path and reconnects it to its Host.
func TestRestoreReopensAVNCPaneFromTheSavedHost(t *testing.T) {
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-vnc", Number: 1, Root: domainworkspace.NewPaneNode("pane-vnc"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-vnc",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionVNC, HostID: "vnc-desk",
				Hostname: "desk.local", Port: 5901, Label: "desk",
			},
		}},
	}
	graph := newWorkspaceGraph(t)
	graph.hosts["vnc-desk"] = domainvault.Host{
		ID: "vnc-desk", Hostname: "desk.local", Port: 5901,
		Protocol: domainvault.ProtocolVNC,
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the restored VNC Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-vnc")
		return ok && !info.Pending
	})

	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindVNC || spec.VNC == nil ||
		spec.VNC.Hostname != "desk.local" || spec.VNC.Port != 5901 {
		t.Fatalf("restored spec = %+v, want the VNC endpoint", spec)
	}
	if spec.Reconnect == nil {
		t.Fatal("a restored VNC Pane has no automatic reconnect policy")
	}
	if info, ok := graph.state.PaneInfoAt(0, "pane-vnc"); !ok || info.ByteStream {
		t.Fatalf("restored VNC Pane = %+v, %v; want a pixel Pane", info, ok)
	}
}

// TestReconnectPaneReopensOnlyAStoppedPane proves a manual reconnect replaces
// a stopped Session and its Surface, while a still-running Pane is left alone
// so the action cannot abandon a transport that is still recovering.
func TestReconnectPaneReopensOnlyAStoppedPane(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.flushUntil(t, "the first Pane surface", func() bool {
		return graph.state.ActiveSurface() != nil && len(graph.surfaces) == 1
	})
	tabID, ok := graph.state.TabID(0)
	if !ok {
		t.Fatal("the Tab has no ID")
	}
	paneID, ok := graph.state.FocusedPaneID(0)
	if !ok {
		t.Fatal("the Tab has no focused Pane")
	}

	if graph.state.ReconnectPane(tabID, paneID) {
		t.Fatal("a live Pane was reopened")
	}
	if graph.factory.specCount() != 1 {
		t.Fatalf("Session opens after a live reconnect = %d, want 1", graph.factory.specCount())
	}

	first := graph.sessions[0]
	if err := first.Close(); err != nil {
		t.Fatalf("stop the first Session: %v", err)
	}
	graph.flushUntil(t, "the stopped Session", func() bool {
		return first.Snapshot().Status != appsession.StatusRunning
	})

	if !graph.state.ReconnectPane(tabID, paneID) {
		t.Fatal("a stopped Pane was not reopened")
	}
	graph.flushUntil(t, "the replacement Pane", func() bool {
		info, ok := graph.state.PaneInfoAt(0, paneID)
		return ok && !info.Pending && info.Surface != nil && len(graph.surfaces) == 2
	})
	if !graph.surfaces[0].closed {
		t.Fatal("the stopped Pane's Surface was not closed")
	}
	if graph.factory.specCount() != 2 {
		t.Fatalf("Session opens = %d, want one replacement dial", graph.factory.specCount())
	}
	info, _ := graph.state.PaneInfoAt(0, paneID)
	if info.Surface == Surface(graph.surfaces[0]) {
		t.Fatal("the Pane still renders the stopped Session's Surface")
	}
}

// TestSessionBellMarksTheFocusedMainPaneOnce proves a bell on the focused main
// Pane still produces a visible marker (the ordinary activity path suppresses
// it), and that the Session's activity marker and the viewer's bell marker are
// one cue, not two.
func TestSessionBellMarksTheFocusedMainPaneOnce(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	id := graph.sessions[0].Snapshot().ID

	graph.state.NoteSessionActivity(id)
	graph.state.NoteSessionBell(id)
	graph.state.Flush()
	info, _ := graph.state.TabInfo(0)
	if !info.Dirty {
		t.Fatal("a focused Pane's bell left no visible marker")
	}

	// A second bell from the same Session is idempotent: the marker is a
	// state, so the user sees one cue.
	graph.state.NoteSessionBell(id)
	graph.state.Flush()
	// Re-query the Tab: the earlier TabInfo is a snapshot from before the
	// second bell, so asserting on it would not observe the idempotent path.
	if refreshed, _ := graph.state.TabInfo(0); !refreshed.Dirty {
		t.Fatal("the bell marker disappeared")
	}
}

// TestSessionBellMarksADetachedWindow proves a bell in a detached Tab sets the
// header marker (the main tab strip cannot show it) and that focusing the
// window acknowledges it.
func TestSessionBellMarksADetachedWindow(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	id := graph.sessions[0].Snapshot().ID
	if !graph.state.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	tabID := graph.state.DetachedTabIDs()[0]

	graph.state.NoteSessionBell(id)
	graph.state.Flush()
	info, ok := graph.state.DetachedTabInfo(tabID)
	if !ok || !info.Bell {
		t.Fatalf("detached bell = %+v, want the header marker", info)
	}

	// Repeating the bell keeps one marker, and the window's focus clears it.
	graph.state.NoteSessionBell(id)
	graph.state.Flush()
	if info, _ := graph.state.DetachedTabInfo(tabID); !info.Bell {
		t.Fatal("a repeated bell cleared or duplicated the marker")
	}
	if !graph.state.ClearDetachedBell(tabID) {
		t.Fatal("ClearDetachedBell reported nothing to clear")
	}
	if info, _ := graph.state.DetachedTabInfo(tabID); info.Bell {
		t.Fatal("the detached bell marker stayed after acknowledgement")
	}
}
