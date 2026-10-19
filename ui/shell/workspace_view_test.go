package shell

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// multiFactory gives each Session its own transport so split views can show
// several live Panes in one test.
type multiFactory struct{}

func (multiFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return newFakeTransport(), nil
}

type workspaceViewGraph struct {
	state *state.State
}

func newWorkspaceViewGraph(t *testing.T) *workspaceViewGraph {
	t.Helper()
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	return &workspaceViewGraph{state: state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
	)}
}

func (g *workspaceViewGraph) waitForSurfaces(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g.state.Flush()
		live := 0
		for index := 0; index < g.state.TabCount(); index++ {
			root, _, ok := g.state.TabTree(index)
			if !ok {
				continue
			}
			for _, id := range domainworkspace.CollectPaneIDs(root) {
				if info, ok := g.state.PaneInfoAt(index, id); ok && info.Surface != nil {
					live++
				}
			}
		}
		if live == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d Pane surfaces", count)
}

func TestShellRendersRecursiveSplitPanes(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 2)
	tt.Frame()

	if got := countText(tt.Texts(), "terminal-surface"); got != 2 {
		t.Fatalf("terminal surfaces = %d, want 2; texts %q", got, tt.Texts())
	}
	if tt.HasText("Split Right") {
		t.Fatal("split menu items are visible before opening a menu")
	}
}

func TestShellPaneMenuSplitsAndCloses(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if err := tt.Click("⋮"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Split Right"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	graph.waitForSurfaces(t, 2)
	tt.Frame()
	if got := countText(tt.Texts(), "terminal-surface"); got != 2 {
		t.Fatalf("terminal surfaces = %d after the split, want 2", got)
	}

	if err := tt.Click("⋮"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Close Pane"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	root, _, _ := graph.state.TabTree(0)
	if root == nil || !root.IsPane() {
		t.Fatalf("root after Close Pane = %+v, want the surviving Pane", root)
	}
	if got := countText(tt.Texts(), "terminal-surface"); got != 1 {
		t.Fatalf("terminal surfaces = %d after Close Pane, want 1", got)
	}
}

func TestShellTabMenuDuplicatesWithCopyTitle(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if err := tt.Click("⋯"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Duplicate Tab"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if graph.state.TabCount() != 2 {
		t.Fatalf("tabs = %d after Duplicate Tab, want 2", graph.state.TabCount())
	}
	want := locales.In("en").TabCopyTitle("Local Session 1", 1)
	if !tt.HasText(want) {
		t.Fatalf("shell does not show %q, texts %q", want, tt.Texts())
	}
}

func TestShellTabMenuClosesOtherTabs(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	graph.state.SelectTab(0)
	tt.Frame()

	if err := tt.Click("⋯"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Close Other Tabs"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d after Close Other Tabs, want 1", graph.state.TabCount())
	}
	if !tt.HasText("Local Session 1") {
		t.Fatalf("the kept Tab is not shown, texts %q", tt.Texts())
	}
}

func TestShellDragReordersTabs(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	tt.Frame()

	third, ok := tt.Find("Local Session 3")
	if !ok {
		t.Fatalf("Local Session 3 is not visible, texts %q", tt.Texts())
	}
	first, ok := tt.Find("Local Session 1")
	if !ok {
		t.Fatalf("Local Session 1 is not visible, texts %q", tt.Texts())
	}
	tt.Press(third.X+third.W/2, third.Y+third.H/2)
	tt.Move(first.X+first.W/2, first.Y+first.H/2)
	tt.Frame()
	tt.Release(first.X+first.W/2, first.Y+first.H/2)
	tt.Frame()

	if got := graph.state.TabNumbers(); len(got) != 3 || got[0] != 3 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("tab numbers after the drag = %v, want [3 1 2]", got)
	}
}

func TestShellShowsTheTabActivityMarker(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.state.SetTabDirty(0, true)
	tt.Frame()

	if !tt.HasText(locales.In("en").TabDirtyMarker()) {
		t.Fatalf("shell does not show the activity marker, texts %q", tt.Texts())
	}
}

func countText(texts []string, want string) int {
	count := 0
	for _, text := range texts {
		if text == want {
			count++
		}
	}
	return count
}

func TestShellDragMovesAPaneOntoAnotherTab(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 2)
	tt.Frame()

	handle, ok := tt.Find("::")
	if !ok {
		t.Fatalf("Pane drag handle is not visible, texts %q", tt.Texts())
	}
	first, ok := tt.Find("Local Session 1")
	if !ok {
		t.Fatalf("Local Session 1 is not visible, texts %q", tt.Texts())
	}
	tt.Press(handle.X+handle.W/2, handle.Y+handle.H/2)
	tt.Move(first.X+first.W/2, first.Y+first.H/2)
	tt.Frame()
	tt.Release(first.X+first.W/2, first.Y+first.H/2)
	tt.Frame()

	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want the emptied source Tab removed", graph.state.TabCount())
	}
	root, _, ok := graph.state.TabTree(0)
	if !ok || len(domainworkspace.CollectPaneIDs(root)) != 2 {
		t.Fatalf("target Tab panes = %+v, want two", root)
	}
}

func TestShellKeepsWorkTabsVisibleOverTheVault(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.state.ShowVault()
	tt.Frame()

	tab, ok := tt.Find("Local Session 1")
	if !ok {
		t.Fatalf("the work Tab is hidden on the Vault, texts %q", tt.Texts())
	}
	tt.ClickAt(tab.X+tab.W/2, tab.Y+tab.H/2)
	tt.Frame()
	if graph.state.VaultVisible() {
		t.Fatal("clicking the Tab did not return to the Session")
	}
}
