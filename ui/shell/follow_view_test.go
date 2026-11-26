package shell

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// followSurface is the fake terminal the follow renderer mounts. It counts
// the frames it is drawn and takes, and records the chrome theme it saw, so a
// test can prove a background Tab is not rendered and the live scheme reaches
// the follow view.
type followSurface struct {
	text     string
	views    int
	focuses  int
	closes   int
	browser  bool
	chromeBg ui.Color
}

func (s *followSurface) View(c *ui.Context) ui.Element {
	s.views++
	s.chromeBg = c.Theme().Background
	return ui.Text(c, s.text).Fill()
}

func (s *followSurface) ViewFocused(c *ui.Context, focused bool) ui.Element {
	if focused {
		s.focuses++
	}
	return s.View(c)
}

func (s *followSurface) Close() { s.closes++ }

func (s *followSurface) BrowserOpen() bool { return s.browser }

type followGraph struct {
	state    *state.State
	surfaces []*followSurface
}

func newFollowGraph(t *testing.T) *followGraph {
	t.Helper()
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	graph := &followGraph{}
	graph.state = state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			surface := &followSurface{text: fmt.Sprintf("follow-terminal-%d", len(graph.surfaces)+1)}
			graph.surfaces = append(graph.surfaces, surface)
			return surface, nil
		}),
	)
	return graph
}

func (g *followGraph) waitForSurfaces(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g.state.Flush()
		live := 0
		for index := 0; index < g.state.TabCount(); index++ {
			tab, ok := g.state.ReadOnlyTab(index)
			if !ok {
				continue
			}
			root, _, ok := tab.Tree()
			if !ok {
				continue
			}
			for _, id := range domainworkspace.CollectPaneIDs(root) {
				if info, ok := tab.PaneInfo(id); ok && info.Surface != nil {
					live++
				}
			}
		}
		if live == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d follow Pane surfaces", count)
}

// followFrame renders the mini Tab strip above the follow body, as the
// Dropdown Panel's frame lays them out.
func followFrame(s *state.State, openMain func()) func(*ui.Context) {
	bar := FollowBar(s)
	body := FollowView(s, WithFollowOpenMain(openMain))
	return func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			bar(c)
			ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).Children(func() { body(c) })
		})
	}
}

// TestFollowViewRendersOnlyTheActiveTabWithAMiniStrip proves the follow
// renderer draws exactly the active Tab's terminal, not every background Tab,
// and the mini strip selects a Tab without changing Tab identities.
func TestFollowViewRendersOnlyTheActiveTabWithAMiniStrip(t *testing.T) {
	graph := newFollowGraph(t)
	for index := 0; index < 2; index++ {
		if err := graph.state.NewTab(); err != nil {
			t.Fatalf("NewTab %d: %v", index, err)
		}
	}
	graph.waitForSurfaces(t, 2)
	// Distinct titles make the two mini Tab entries individually clickable.
	for index, title := range []string{"alpha", "beta"} {
		pane, _ := graph.state.FocusedPaneID(index)
		graph.state.RenamePane(index, pane, title)
	}

	tester := ui.NewTester(followFrame(graph.state, nil), 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	if got := countText(tester.Texts(), "follow-terminal-2"); got != 1 {
		t.Fatalf("active terminal renders = %d, want 1; texts %q", got, tester.Texts())
	}
	if got := countText(tester.Texts(), "follow-terminal-1"); got != 0 {
		t.Fatalf("background Tab rendered %d times, want 0; texts %q", got, tester.Texts())
	}
	if graph.surfaces[0].views != 0 {
		t.Fatalf("background surface was drawn %d times", graph.surfaces[0].views)
	}
	for _, title := range []string{"alpha", "beta"} {
		if !tester.HasText(title) {
			t.Fatalf("the mini Tab strip does not show %q: %v", title, tester.Texts())
		}
	}

	tabIDs := []string{tabID(t, graph.state, 0), tabID(t, graph.state, 1)}
	surfaces := []*followSurface{graph.surfaces[0], graph.surfaces[1]}
	root1, _, _ := graph.state.TabTree(0)
	root2, _, _ := graph.state.TabTree(1)
	// The previously rendered Pane is unmounted by the switch and the newly
	// active one asks for focus again on the next frame.
	activeFocuses := graph.surfaces[0].focuses

	if err := tester.Click("alpha"); err != nil {
		t.Fatalf("clicking the first mini Tab: %v", err)
	}
	tester.Frame()

	if active := graph.state.ActiveTab(); active != 0 {
		t.Fatalf("active Tab = %d, want the selected first Tab", active)
	}
	if graph.surfaces[0].focuses <= activeFocuses {
		t.Fatal("the newly active terminal did not take the keyboard after the switch")
	}
	inactiveViews := graph.surfaces[1].views
	tester.Frame()
	if graph.surfaces[1].views != inactiveViews {
		t.Fatal("the switched-away Tab's terminal kept rendering")
	}
	if tabID(t, graph.state, 0) != tabIDs[0] || tabID(t, graph.state, 1) != tabIDs[1] {
		t.Fatal("selecting a mini Tab changed the Workspace Tab order")
	}
	after1, _, _ := graph.state.TabTree(0)
	after2, _, _ := graph.state.TabTree(1)
	if after1 != root1 || after2 != root2 {
		t.Fatal("selecting a mini Tab replaced a split tree")
	}
	if graph.state.TabCount() != 2 || len(graph.state.DetachedTabIDs()) != 0 {
		t.Fatalf("tabs = %d, detached = %v; selecting must not detach",
			graph.state.TabCount(), graph.state.DetachedTabIDs())
	}
	info, ok := graph.state.PaneInfoAt(0, paneID(t, graph.state, 0))
	if !ok || info.Surface != surfaces[0] {
		t.Fatal("selecting a mini Tab replaced the first Tab's Session surface")
	}
	if got := countText(tester.Texts(), "follow-terminal-1"); got != 1 {
		t.Fatalf("after selection the first terminal renders %d times", got)
	}
}

// TestFollowViewRendersASplitTreeAndRequestsFocusEveryFrame covers a split
// Tab: both Panes render, and the mounted follow view asks for the keyboard
// again on every frame because MyGo prunes the previous element state.
func TestFollowViewRendersASplitTreeAndRequestsFocusEveryFrame(t *testing.T) {
	graph := newFollowGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 2)

	tester := ui.NewTester(followFrame(graph.state, nil), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	tester.Frame()

	for _, text := range []string{"follow-terminal-1", "follow-terminal-2"} {
		if got := countText(tester.Texts(), text); got != 1 {
			t.Fatalf("%s renders %d times, want one Pane each; texts %q", text, got, tester.Texts())
		}
	}
	focused := 0
	for _, surface := range graph.surfaces {
		focused += surface.focuses
	}
	if focused < 2 {
		t.Fatalf("the follow view requested terminal focus %d times over two frames", focused)
	}
}

// TestFollowViewFlushesQueuedSessions pins the per-frame shell work: the
// follow renderer applies queued Session outcomes even when the main window is
// not rendering the shell.
func TestFollowViewFlushesQueuedSessions(t *testing.T) {
	graph := newFollowGraph(t)
	tester := ui.NewTester(FollowView(graph.state), 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	if err := graph.state.OpenHost(domainvault.Host{Label: "prod"}); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for graph.state.TabCount() == 0 && time.Now().Before(deadline) {
		tester.Frame()
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("the follow renderer did not flush the queued Host open: %d tabs", graph.state.TabCount())
	}
}

// TestFollowViewFinishesItsOwnSideFrame proves the follow renderer, not the
// main renderer, tells a side tool it left the screen, and does so once.
func TestFollowViewFinishesItsOwnSideFrame(t *testing.T) {
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	hides := 0
	shellState := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithSideTools(state.SideTool{
			ID: state.SideHistory,
			View: func(state.SideTarget, float32) func(*ui.Context) {
				return func(c *ui.Context) { ui.Text(c, "side-probe") }
			},
			Hide: func() { hides++ },
		}),
	)
	if err := shellState.NewTab(); err != nil {
		t.Fatal(err)
	}
	(&workspaceViewGraph{state: shellState}).waitForSurfaces(t, 1)
	tabID, _ := shellState.TabID(0)
	shellState.ToggleFiles(tabID, "", "")

	tester := ui.NewTester(FollowView(shellState), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	if !tester.HasText("side-probe") {
		t.Fatalf("the follow view did not draw the side tool: %v", tester.Texts())
	}

	shellState.CloseFiles(tabID)
	tester.Frame()
	if hides != 0 {
		t.Fatalf("the closing drawer stopped the side tool before it left: %d", hides)
	}
	if !tester.HasText("side-probe") {
		t.Fatalf("the closing drawer did not paint the side tool: %v", tester.Texts())
	}
	// The drawer keeps the tool alive until its run ends; then this renderer,
	// not the main window's, stops it exactly once.
	tester.Advance(2 * time.Second)
	tester.Frame()
	if hides != 1 {
		t.Fatalf("the follow renderer hid the side tool %d times after the drawer, want 1", hides)
	}
	tester.Frame()
	if hides != 1 {
		t.Fatalf("the follow renderer hid the side tool again: %d", hides)
	}
}

// TestFollowViewShowsEmptyStatesForNonTerminalsAndBrowsers proves the two
// placeholders: the active Tab is not a terminal, or it has a browser panel
// open that cannot be mounted in another window. Both offer the main window
// instead of silently changing the Workspace.
func TestFollowViewShowsEmptyStatesForNonTerminalsAndBrowsers(t *testing.T) {
	m := locales.In("en")
	t.Run("non-terminal", func(t *testing.T) {
		graph := newFollowGraph(t)
		opened := 0
		tester := ui.NewTester(FollowView(graph.state, WithFollowOpenMain(func() { opened++ })), 900, 600)
		tester.SetLocale("en")
		tester.Frame()
		if !tester.HasText(m.FollowEmptyTitle()) || !tester.HasText(m.FollowOpenMain()) {
			t.Fatalf("the non-terminal empty state is missing: %v", tester.Texts())
		}
		if err := tester.Click(m.FollowOpenMain()); err != nil {
			t.Fatalf("clicking Open main window: %v", err)
		}
		if opened != 1 {
			t.Fatalf("Open main window fired %d times", opened)
		}
	})

	t.Run("browser", func(t *testing.T) {
		graph := newFollowGraph(t)
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		graph.waitForSurfaces(t, 1)
		graph.surfaces[0].browser = true
		opened := 0
		tester := ui.NewTester(FollowView(graph.state, WithFollowOpenMain(func() { opened++ })), 900, 600)
		tester.SetLocale("en")
		tester.Frame()
		if !tester.HasText(m.FollowBrowserTitle()) || !tester.HasText(m.FollowOpenMain()) {
			t.Fatalf("the browser placeholder is missing: %v", tester.Texts())
		}
		if tester.HasText("follow-terminal-1") {
			t.Fatalf("the browser Tab mounted its terminal in the follow view: %v", tester.Texts())
		}
		if err := tester.Click(m.FollowOpenMain()); err != nil {
			t.Fatalf("clicking Open main window: %v", err)
		}
		if opened != 1 {
			t.Fatalf("Open main window fired %d times", opened)
		}
	})
}

// TestFollowViewUsesTheLiveShellTheme proves the follow renderer takes the
// terminal scheme's chrome, and a scheme change reaches the next frame
// without remounting anything.
func TestFollowViewUsesTheLiveShellTheme(t *testing.T) {
	graph := newFollowGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 1)

	first := domaintheme.TerminalTheme{
		Dark:   true,
		Colors: domaintheme.TerminalColors{Background: "#101820", Foreground: "#e0e0e0"},
	}
	second := domaintheme.TerminalTheme{
		Dark:   true,
		Colors: domaintheme.TerminalColors{Background: "#204060", Foreground: "#f0f0f0"},
	}
	graph.state.SetTerminalTheme(first)

	var base *ui.Theme
	tester := ui.NewTester(func(c *ui.Context) {
		if base == nil {
			base = c.Theme()
		}
		FollowView(graph.state)(c)
	}, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	want := uitheme.Chrome(base, first).Background
	if got := graph.surfaces[0].chromeBg; got != want {
		t.Fatalf("follow chrome = %v, want the first scheme's %v", got, want)
	}

	graph.state.SetTerminalTheme(second)
	tester.Frame()
	want = uitheme.Chrome(base, second).Background
	if got := graph.surfaces[0].chromeBg; got != want {
		t.Fatalf("follow chrome after the live change = %v, want %v", got, want)
	}
}

// tabID and paneID are small lookup helpers shared by the follow tests.
func tabID(t *testing.T, s *state.State, index int) string {
	t.Helper()
	id, ok := s.TabID(index)
	if !ok {
		t.Fatalf("no Tab at %d", index)
	}
	return string(id)
}

func paneID(t *testing.T, s *state.State, index int) model.PaneID {
	t.Helper()
	id, ok := s.FocusedPaneID(index)
	if !ok {
		t.Fatalf("no focused Pane at %d", index)
	}
	return id
}
