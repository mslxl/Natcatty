package shell

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	appview "github.com/mslxl/natcatty/application/view"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/shell/state"
)

type shellViewSurface struct{}

func (shellViewSurface) View(c *ui.Context) ui.Element { return ui.Text(c, "view-surface") }
func (shellViewSurface) Close()                        {}

type viewShellGraph struct {
	state    *state.State
	registry *appview.Registry
}

func newViewShellGraph(t *testing.T) *viewShellGraph {
	t.Helper()
	registry := appview.NewRegistry()
	t.Cleanup(registry.Close)
	if _, err := registry.Register(appview.Owner{PluginID: "acme.shell"}, domainplugin.ViewContribution{
		ID:    "counter",
		Title: "Counter",
		Placements: []domainplugin.Placement{
			domainplugin.PlacementPane,
			domainplugin.PlacementTab,
			domainplugin.PlacementPage,
			domainplugin.PlacementWindow,
			domainplugin.PlacementAside,
		},
		Instances: domainplugin.InstanceSingleton,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	st := state.NewState(state.WithViews(registry, func(*appview.Instance) (state.Surface, error) {
		return shellViewSurface{}, nil
	}))
	return &viewShellGraph{state: st, registry: registry}
}

// newMixedViewShellGraph adds a Session service to the View shell graph, so a
// Session Tab and a View Tab can be rendered in one split.
func newMixedViewShellGraph(t *testing.T) *viewShellGraph {
	t.Helper()
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	registry := appview.NewRegistry()
	t.Cleanup(registry.Close)
	if _, err := registry.Register(appview.Owner{PluginID: "acme.shell"}, domainplugin.ViewContribution{
		ID:    "counter",
		Title: "Counter",
		Placements: []domainplugin.Placement{
			domainplugin.PlacementPane,
			domainplugin.PlacementTab,
			domainplugin.PlacementPage,
			domainplugin.PlacementWindow,
			domainplugin.PlacementAside,
		},
		Instances: domainplugin.InstanceSingleton,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	st := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithViews(registry, func(*appview.Instance) (state.Surface, error) {
			return shellViewSurface{}, nil
		}),
	)
	return &viewShellGraph{state: st, registry: registry}
}

const shellCounterViewID = "plugin.acme.shell.counter"

// TestShellViewPaneMenuFollowsASplitDrag proves the real Pane menu after a
// drag between a standalone View Tab and a Session split: the View Pane offers
// "Move to Tab" and no longer treats "Move to Pane" as current, and moving back
// out inverts the menu.
func TestShellViewPaneMenuFollowsASplitDrag(t *testing.T) {
	graph := newMixedViewShellGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	sessionTab, ok := graph.state.TabID(0)
	if !ok {
		t.Fatal("the Session Tab has no id")
	}
	sessionRoot, _, ok := graph.state.TabTree(0)
	if !ok {
		t.Fatal("the Session Tab has no tree")
	}
	sessionPane := domainworkspace.CollectPaneIDs(sessionRoot)[0]
	if _, err := graph.state.OpenView(appview.OpenRequest{
		ViewID: shellCounterViewID, Placement: domainplugin.PlacementTab,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	viewTab, ok := graph.state.TabID(1)
	if !ok {
		t.Fatal("the View Tab has no id")
	}
	viewRoot, _, ok := graph.state.TabTree(1)
	if !ok {
		t.Fatal("the View Tab has no tree")
	}
	viewPane := domainworkspace.CollectPaneIDs(viewRoot)[0]
	if !graph.state.MovePane(viewTab, viewPane, sessionTab, sessionPane, domainworkspace.SideLeft) {
		t.Fatal("MovePane into the Session split returned false")
	}
	instances := graph.registry.Instances()
	if len(instances) != 1 || instances[0].Placement() != domainplugin.PlacementPane {
		t.Fatalf("dragged View instance = %+v, want one Pane placement", instances)
	}

	// The moved Pane is first in the split, so its header owns the first menu.
	tt.Frame()
	if err := tt.Click("⋮"); err != nil {
		t.Fatalf("open the View Pane menu: %v", err)
	}
	menu := tt.Menu()
	if !slices.Contains(menu, "Move to Tab") {
		t.Fatalf("View Pane menu = %v, want Move to Tab", menu)
	}
	if slices.Contains(menu, "Move to Pane") {
		t.Fatalf("View Pane menu = %v, Move to Pane must not be offered as current", menu)
	}
	if err := tt.ChooseMenuItem("Move to Tab"); err != nil {
		t.Fatalf("choose Move to Tab: %v", err)
	}
	tt.Frame()

	if graph.state.TabCount() != 2 {
		t.Fatalf("tabs after moving back out = %d, want 2", graph.state.TabCount())
	}
	if instances[0].Placement() != domainplugin.PlacementTab {
		t.Fatalf("placement after moving back out = %s, want tab", instances[0].Placement())
	}
	if err := tt.Click("⋮"); err != nil {
		t.Fatalf("open the standalone View Pane menu: %v", err)
	}
	menu = tt.Menu()
	if !slices.Contains(menu, "Move to Pane") {
		t.Fatalf("standalone View Pane menu = %v, want Move to Pane", menu)
	}
	if slices.Contains(menu, "Move to Tab") {
		t.Fatalf("standalone View Pane menu = %v, Move to Tab must not be offered as current", menu)
	}
}

// TestShellViewPaneMenuMovesToAPage proves the View Pane menu is the view
// menu, offers the declared placements, and that choosing one moves the same
// instance into the contribution-driven Home page.
func TestShellViewPaneMenuMovesToAPage(t *testing.T) {
	graph := newViewShellGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if _, err := graph.state.OpenView(appview.OpenRequest{ViewID: shellCounterViewID}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	tt.Frame()
	if countText(tt.Texts(), "view-surface") != 1 {
		t.Fatalf("view surface texts = %q", tt.Texts())
	}

	if err := tt.Click("⋮"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Move to Page"); err != nil {
		t.Fatalf("Choose Move to Page: %v", err)
	}
	tt.Frame()
	if graph.state.TabCount() != 0 {
		t.Fatalf("tabs = %d after moving to the page", graph.state.TabCount())
	}
	if !graph.state.PageVisible() {
		t.Fatal("the page is not visible")
	}
	if countText(tt.Texts(), "view-surface") != 1 {
		t.Fatalf("page surface texts = %q", tt.Texts())
	}
	// The Home sidebar lists the contribution-driven page.
	if !tt.HasText("Counter") {
		t.Fatalf("the Home sidebar does not list the View page: %q", tt.Texts())
	}
}

// TestShellViewPaneMenuDisablesSessionCommands proves Split Right and Split
// Down stay visible but disabled in a View Pane. A closed menu must not have
// performed the action.
func TestShellViewPaneMenuKeepsSplitDisabled(t *testing.T) {
	graph := newViewShellGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if _, err := graph.state.OpenView(appview.OpenRequest{ViewID: shellCounterViewID}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	tt.Frame()
	if err := tt.Click("⋮"); err != nil {
		t.Fatal(err)
	}
	_ = tt.ChooseMenuItem("Split Right")
	tt.Frame()
	if graph.state.TabCount() != 1 {
		t.Fatalf("a disabled Split changed the layout: tabs=%d", graph.state.TabCount())
	}
	if root, _, ok := graph.state.TabTree(0); !ok || !root.IsPane() {
		t.Fatalf("a disabled Split changed the tree: %+v", root)
	}
}

// TestShellMultiInstancePaneMenuOmitsThePage proves a multi-instance View is
// never offered a Page move the registry would reject.
func TestShellMultiInstancePaneMenuOmitsThePage(t *testing.T) {
	registry := appview.NewRegistry()
	t.Cleanup(registry.Close)
	if _, err := registry.Register(appview.Owner{PluginID: "acme.shell"}, domainplugin.ViewContribution{
		ID:    "gallery",
		Title: "Gallery",
		Placements: []domainplugin.Placement{
			domainplugin.PlacementPane,
			domainplugin.PlacementPage,
		},
		Instances: domainplugin.InstanceMulti,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	st := state.NewState(state.WithViews(registry, func(*appview.Instance) (state.Surface, error) {
		return shellViewSurface{}, nil
	}))
	tt := ui.NewTester(View(st), 900, 600)
	if _, err := st.OpenView(appview.OpenRequest{ViewID: "plugin.acme.shell.gallery"}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	tt.Frame()
	if err := tt.Click("⋮"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Move to Page"); err == nil {
		t.Fatal("a multi-instance View offered the singleton Page move")
	}
	tt.Frame()
	if st.PageVisible() {
		t.Fatal("a multi-instance View entered the page")
	}
}

// TestShellSidePanelRendersAnAsideContribution proves the side panel is
// contribution-driven and passes the tab's explicit Host context to the View.
func TestShellSidePanelRendersAnAsideContribution(t *testing.T) {
	service := appsession.NewService(&fakeFactory{transport: newFakeTransport()})
	t.Cleanup(service.CloseAll)
	registry := appview.NewRegistry()
	t.Cleanup(registry.Close)
	if _, err := registry.Register(appview.Owner{PluginID: "acme.shell"}, domainplugin.ViewContribution{
		ID:         "side",
		Title:      "Side View",
		Placements: []domainplugin.Placement{domainplugin.PlacementPane, domainplugin.PlacementAside},
		Instances:  domainplugin.InstanceSingleton,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	st := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithViews(registry, func(*appview.Instance) (state.Surface, error) {
			return shellViewSurface{}, nil
		}),
	)
	if err := st.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	st.SelectTab(0)
	tabID, ok := st.TabID(0)
	if !ok {
		t.Fatal("the new Tab has no id")
	}
	st.ToggleFiles(tabID, "host-1", "web")
	tt := ui.NewTester(View(st), 900, 600)
	tt.SetLocale("en")
	tt.Frame()
	if countText(tt.Texts(), "view-surface") != 1 {
		t.Fatalf("the side panel did not render the View: %v", tt.Texts())
	}
	instances := registry.Instances()
	if len(instances) != 1 || instances[0].ContextKey() != "host-1" ||
		instances[0].Placement() != domainplugin.PlacementAside {
		t.Fatalf("side View instance = %+v", instances)
	}
}

// TestShellHomeSidebarListsPageContributions proves Home navigation is
// contribution-driven even before the page is open.
func TestShellHomeSidebarListsPageContributions(t *testing.T) {
	graph := newViewShellGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	graph.state.ShowHome()
	tt.Frame()
	item, ok := tt.Find("Counter")
	if !ok {
		t.Fatalf("the Home sidebar is missing the View page: %q", tt.Texts())
	}
	tt.ClickAt(item.X+item.W/2, item.Y+item.H/2)
	tt.Frame()
	if !graph.state.PageVisible() {
		t.Fatal("clicking the View page did not open it")
	}
	if countText(tt.Texts(), "view-surface") != 1 {
		t.Fatalf("page surface texts = %q", tt.Texts())
	}
}
