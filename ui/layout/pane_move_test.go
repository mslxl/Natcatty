package layout_test

// The layout package owns a window's page shells (title bar, sidebar, content,
// and docked panels); the split tree itself lives in ui/shell/state, as ADR
// 0006 requires. The ticket's "ui/layout state test" is therefore an external
// test in this package that drives the production pane-move API and asserts
// the resulting layout, without introducing a lower-level view -> state
// import in non-test code.

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

// layoutTransport is a local Session transport that stays open until closed.
type layoutTransport struct {
	mu     sync.Mutex
	closed chan struct{}
}

func newLayoutTransport() *layoutTransport {
	return &layoutTransport{closed: make(chan struct{})}
}

func (t *layoutTransport) Read([]byte) (int, error) {
	<-t.closed
	return 0, io.EOF
}

func (t *layoutTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *layoutTransport) Resize(int, int) error       { return nil }

func (t *layoutTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}

type layoutFactory struct{}

func (layoutFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return newLayoutTransport(), nil
}

type layoutSurface struct{}

func (layoutSurface) View(*ui.Context) ui.Element { return ui.Element{} }
func (layoutSurface) Close()                      {}

func newLayoutState(t *testing.T) *shellstate.State {
	t.Helper()
	service := appsession.NewService(layoutFactory{})
	t.Cleanup(service.CloseAll)
	return shellstate.NewState(shellstate.WithSessions(service,
		func(*appsession.Session) (shellstate.Surface, error) {
			return layoutSurface{}, nil
		}))
}

func layoutPaneIDs(t *testing.T, state *shellstate.State, index int) []model.PaneID {
	t.Helper()
	root, _, ok := state.TabTree(index)
	if !ok {
		t.Fatalf("Tab %d does not exist", index)
	}
	return domainworkspace.CollectPaneIDs(root)
}

// TestPaneMoveRearrangesTheLayout pins the acceptance bullet from the layout
// package: a pane move is a pure layout rearrangement. The moved Pane keeps
// its identity, appears in the requested slot, and the source Tab survives
// with its remaining members when the move crosses Tabs.
func TestPaneMoveRearrangesTheLayout(t *testing.T) {
	state := newLayoutState(t)
	if err := state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	tabID, _ := state.TabID(0)
	before := layoutPaneIDs(t, state, 0)
	if len(before) != 2 {
		t.Fatalf("panes = %v, want 2", before)
	}
	left, right := before[0], before[1]

	// Move the right Pane before the left one inside the same Tab.
	if !state.MovePane(tabID, right, tabID, left, domainworkspace.SideLeft) {
		t.Fatal("within-Tab MovePane returned false")
	}
	root, _, _ := state.TabTree(0)
	if root == nil || root.First == nil || !root.First.IsPane() || root.First.PaneID != right {
		t.Fatalf("root.First = %+v, want the moved Pane in the first slot", root)
	}
	if got := layoutPaneIDs(t, state, 0); len(got) != 2 || got[0] != right || got[1] != left {
		t.Fatalf("panes = %v after the move, want [%q %q]", got, right, left)
	}

	// Move the Pane across Tabs: the target grows and the moved identity is
	// preserved, while the source keeps its own Pane.
	if err := state.NewTab(); err != nil {
		t.Fatal(err)
	}
	secondID, _ := state.TabID(1)
	secondPane := layoutPaneIDs(t, state, 1)[0]
	if !state.MovePane(tabID, right, secondID, secondPane, domainworkspace.SideRight) {
		t.Fatal("cross-Tab MovePane returned false")
	}
	if got := layoutPaneIDs(t, state, 0); len(got) != 1 || got[0] != left {
		t.Fatalf("source panes = %v, want the remaining Pane", got)
	}
	if got := layoutPaneIDs(t, state, 1); len(got) != 2 || got[1] != right {
		t.Fatalf("target panes = %v, want the moved Pane beside the target", got)
	}
}

// TestPaneMoveRejectsAStaleTargetFromTheLayout pins the drop guard through the
// same public API the drop zones use: once the target Pane has left the Tab,
// the move is refused and the layout is unchanged.
func TestPaneMoveRejectsAStaleTargetFromTheLayout(t *testing.T) {
	state := newLayoutState(t)
	if err := state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	tabID, _ := state.TabID(0)
	left, right := layoutPaneIDs(t, state, 0)[0], layoutPaneIDs(t, state, 0)[1]

	if !state.FocusPaneID(0, right) || !state.CloseFocusedPane() {
		t.Fatal("closing the drop target failed")
	}
	if state.CanMovePane(tabID, left, tabID, right, domainworkspace.SideRight) {
		t.Fatal("the drop-zone guard accepted a Pane that closed mid-drag")
	}
	if state.MovePane(tabID, left, tabID, right, domainworkspace.SideRight) {
		t.Fatal("MovePane accepted a Pane that closed mid-drag")
	}
	if got := layoutPaneIDs(t, state, 0); len(got) != 1 || got[0] != left {
		t.Fatalf("panes = %v, want the source layout unchanged", got)
	}
}
