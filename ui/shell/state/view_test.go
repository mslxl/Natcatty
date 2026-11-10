package state

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	appview "github.com/mslxl/natcatty/application/view"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

type fakeViewSurface struct {
	closed bool
}

func (f *fakeViewSurface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "view-surface")
}

func (f *fakeViewSurface) Close() { f.closed = true }

type viewFixture struct {
	state    *State
	registry *appview.Registry
	surfaces []*fakeViewSurface
	factory  int
}

func newViewFixture(t *testing.T, policy domainplugin.InstancePolicy) *viewFixture {
	t.Helper()
	fixture := &viewFixture{registry: appview.NewRegistry()}
	t.Cleanup(fixture.registry.Close)
	_, err := fixture.registry.Register(appview.Owner{PluginID: "acme.view"}, domainplugin.ViewContribution{
		ID:         "counter",
		Title:      "Counter",
		Placements: []domainplugin.Placement{domainplugin.PlacementPane},
		Instances:  policy,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	fixture.state = NewState(WithViews(fixture.registry, func(*appview.Instance) (Surface, error) {
		fixture.factory++
		surface := &fakeViewSurface{}
		fixture.surfaces = append(fixture.surfaces, surface)
		return surface, nil
	}))
	return fixture
}

const counterViewID = "plugin.acme.view.counter"

// TestRefreshViewTitlesFollowsTheHostLocale proves a locale change relabels an
// open View tab without reopening its View Instance.
func TestRefreshViewTitlesFollowsTheHostLocale(t *testing.T) {
	registry := appview.NewRegistry()
	t.Cleanup(registry.Close)
	translations := map[string]string{"view_title": "Contador"}
	registry.SetLocalizer(func(_ appview.Owner, key, literal string) string {
		if translated, ok := translations[key]; ok {
			return translated
		}
		return literal
	})
	if _, err := registry.Register(appview.Owner{PluginID: "acme.view"}, domainplugin.ViewContribution{
		ID:         "counter",
		Title:      "Counter",
		TitleKey:   "view_title",
		Placements: []domainplugin.Placement{domainplugin.PlacementPane, domainplugin.PlacementWindow},
		Instances:  domainplugin.InstanceSingleton,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	factories := 0
	state := NewState(WithViews(registry, func(*appview.Instance) (Surface, error) {
		factories++
		return &fakeViewSurface{}, nil
	}))
	if _, err := state.OpenView(appview.OpenRequest{ViewID: counterViewID}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	if label, _, ok := state.TabLabel(0); !ok || label != "Contador" {
		t.Fatalf("initial tab label = %q, ok = %v", label, ok)
	}

	translations["view_title"] = "Compteur"
	state.RefreshViewTitles()
	if label, _, ok := state.TabLabel(0); !ok || label != "Compteur" {
		t.Fatalf("tab label after locale change = %q, ok = %v", label, ok)
	}
	if factories != 1 {
		t.Fatalf("the view surface was rebuilt %d times", factories)
	}

	// A detached window reads the same Tab label, so its title follows too.
	if !state.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	detachedIDs := state.DetachedTabIDs()
	if len(detachedIDs) != 1 {
		t.Fatalf("detached tabs = %v", detachedIDs)
	}
	translations["view_title"] = "Fenêtre"
	state.RefreshViewTitles()
	if info, ok := state.DetachedTabInfo(detachedIDs[0]); !ok || info.Label != "Fenêtre" {
		t.Fatalf("detached label = %q, ok = %v", info.Label, ok)
	}
	if factories != 1 {
		t.Fatalf("RefreshViewTitles rebuilt a detached surface: %d", factories)
	}

	// A closed View Instance is left alone rather than resurrected.
	instance, _, err := registry.Open(appview.OpenRequest{ViewID: counterViewID})
	if err != nil {
		t.Fatalf("Open existing: %v", err)
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("close instance: %v", err)
	}
	state.RefreshViewTitles()
	if info, ok := state.DetachedTabInfo(detachedIDs[0]); !ok || info.Label != "Fenêtre" {
		t.Fatalf("label after instance close = %q, ok = %v", info.Label, ok)
	}
	if factories != 1 {
		t.Fatalf("RefreshViewTitles rebuilt a surface: %d", factories)
	}
}

func TestOpenViewCreatesAWorkTabAndDisposesOnClose(t *testing.T) {
	fixture := newViewFixture(t, domainplugin.InstanceSingleton)
	tabID, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID})
	if err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	if fixture.state.TabCount() != 1 || fixture.state.ActiveTab() != 0 {
		t.Fatalf("tabs = %d active = %d", fixture.state.TabCount(), fixture.state.ActiveTab())
	}
	if got, _ := fixture.state.TabID(0); got != tabID {
		t.Fatalf("tab id = %q, want %q", got, tabID)
	}
	label, _, ok := fixture.state.TabLabel(0)
	if !ok || label != "Counter" {
		t.Fatalf("tab label = %q, ok = %v", label, ok)
	}

	paneID, ok := fixture.state.FocusedPaneID(0)
	if !ok {
		t.Fatal("the View Tab has no focused Pane")
	}
	info, ok := fixture.state.PaneInfoAt(0, paneID)
	if !ok || info.Kind != domainworkspace.PaneKindView {
		t.Fatalf("PaneInfo = %+v, ok = %v", info, ok)
	}
	if info.Label != "Counter" || info.Err != nil || info.Pending {
		t.Fatalf("View Pane info = %+v", info)
	}
	if info.Surface == nil || fixture.factory != 1 {
		t.Fatalf("surface = %v, factory calls = %d", info.Surface, fixture.factory)
	}
	surface := fixture.surfaces[0]

	payload := fixture.state.WorkspacePayload()
	if len(payload.Panes) != 1 ||
		payload.Panes[0].Kind != domainworkspace.PaneKindView ||
		payload.Panes[0].View == nil ||
		payload.Panes[0].View.ViewID != counterViewID {
		t.Fatalf("workspace payload = %+v", payload.Panes)
	}

	if !fixture.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab reported false")
	}
	if !surface.closed {
		t.Fatal("closing the Tab did not close the View surface")
	}
	if instances := fixture.registry.Instances(); len(instances) != 0 {
		t.Fatalf("closing the Tab left %d live instances", len(instances))
	}
	if fixture.state.TabCount() != 0 {
		t.Fatalf("tabs = %d after close", fixture.state.TabCount())
	}
}

func TestOpenViewSingletonFocusesTheExistingPane(t *testing.T) {
	fixture := newViewFixture(t, domainplugin.InstanceSingleton)
	first, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID})
	if err != nil {
		t.Fatalf("first OpenView: %v", err)
	}
	second, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID})
	if err != nil {
		t.Fatalf("second OpenView: %v", err)
	}
	if second != first {
		t.Fatalf("singleton opened a second Tab: %q then %q", first, second)
	}
	if fixture.state.TabCount() != 1 || fixture.factory != 1 {
		t.Fatalf("tabs = %d factory calls = %d", fixture.state.TabCount(), fixture.factory)
	}
	if instances := fixture.registry.Instances(); len(instances) != 1 {
		t.Fatalf("instances = %d", len(instances))
	}
}

func TestOpenViewMultiPolicyCreatesOneInstancePerPane(t *testing.T) {
	fixture := newViewFixture(t, domainplugin.InstanceMulti)
	if _, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID}); err != nil {
		t.Fatalf("first OpenView: %v", err)
	}
	if _, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID}); err != nil {
		t.Fatalf("second OpenView: %v", err)
	}
	if fixture.state.TabCount() != 2 || fixture.factory != 2 {
		t.Fatalf("tabs = %d factory calls = %d", fixture.state.TabCount(), fixture.factory)
	}
	if instances := fixture.registry.Instances(); len(instances) != 2 {
		t.Fatalf("instances = %d", len(instances))
	}
}

func TestOpenViewRespectsDeclaredPlacement(t *testing.T) {
	fixture := newViewFixture(t, domainplugin.InstanceSingleton)
	_, err := fixture.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, Placement: domainplugin.PlacementWindow,
	})
	if !errors.Is(err, appview.ErrPlacementDenied) {
		t.Fatalf("OpenView = %v, want ErrPlacementDenied", err)
	}
	if fixture.state.TabCount() != 0 || fixture.factory != 0 {
		t.Fatalf("a denied placement created a Tab or surface")
	}
}

func TestOpenViewRequiresTheViewService(t *testing.T) {
	st := NewState()
	_, err := st.OpenView(appview.OpenRequest{ViewID: counterViewID})
	if !errors.Is(err, ErrNoViews) {
		t.Fatalf("OpenView = %v, want ErrNoViews", err)
	}
}

func TestOpenViewSurfaceFailureDoesNotLeakTheInstance(t *testing.T) {
	registry := appview.NewRegistry()
	t.Cleanup(registry.Close)
	if _, err := registry.Register(appview.Owner{PluginID: "acme.view"}, domainplugin.ViewContribution{
		ID:         "counter",
		Title:      "Counter",
		Placements: []domainplugin.Placement{domainplugin.PlacementPane},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	st := NewState(WithViews(registry, func(*appview.Instance) (Surface, error) {
		return nil, errors.New("no renderer")
	}))
	if _, err := st.OpenView(appview.OpenRequest{ViewID: counterViewID}); err == nil {
		t.Fatal("OpenView succeeded without a surface")
	}
	if instances := registry.Instances(); len(instances) != 0 {
		t.Fatalf("a failed open leaked %d instances", len(instances))
	}
	if st.TabCount() != 0 {
		t.Fatalf("a failed open left a Tab")
	}
}
