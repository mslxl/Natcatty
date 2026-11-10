package state

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	appview "github.com/mslxl/natcatty/application/view"
	appworkspace "github.com/mslxl/natcatty/application/workspace"
	"github.com/mslxl/natcatty/domain/model"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

var allPlacements = []domainplugin.Placement{
	domainplugin.PlacementPane,
	domainplugin.PlacementTab,
	domainplugin.PlacementPage,
	domainplugin.PlacementWindow,
	domainplugin.PlacementAside,
}

func newPlacementFixture(t *testing.T, policy domainplugin.InstancePolicy,
	placements ...domainplugin.Placement) *viewFixture {
	t.Helper()
	if len(placements) == 0 {
		placements = allPlacements
	}
	fixture := &viewFixture{registry: appview.NewRegistry()}
	t.Cleanup(fixture.registry.Close)
	if _, err := fixture.registry.Register(appview.Owner{PluginID: "acme.view"}, domainplugin.ViewContribution{
		ID:         "counter",
		Title:      "Counter",
		TitleKey:   "view_title",
		Placements: placements,
		Instances:  policy,
	}); err != nil {
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

func onlyInstance(t *testing.T, fixture *viewFixture) *appview.Instance {
	t.Helper()
	instances := fixture.registry.Instances()
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(instances))
	}
	return instances[0]
}

// TestViewTabUsesTheViewNamespace proves a View Tab cannot collide with a
// Session or editor Tab identity.
func TestViewTabUsesTheViewNamespace(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton)
	tabID, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID})
	if err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := onlyInstance(t, fixture)
	if want := model.TabID("view:" + instance.ID()); tabID != want {
		t.Fatalf("tab id = %q, want %q", tabID, want)
	}
	if !strings.HasPrefix(string(tabID), "view:") {
		t.Fatalf("tab id = %q, want the view: namespace", tabID)
	}
	if fixture.state.TabCount() != 1 || fixture.state.TabNumbers()[0] == 0 {
		t.Fatalf("tab count = %d numbers = %v", fixture.state.TabCount(), fixture.state.TabNumbers())
	}
}

// TestMoveViewPreservesTheInstanceAcrossEveryPlacement is the central ticket
// guarantee: the same instance, state, and surface move atomically between all
// five placements without a factory rebuild.
func TestMoveViewPreservesTheInstanceAcrossEveryPlacement(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton)
	if _, err := fixture.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, ContextKey: "global", State: []byte("logical"),
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := onlyInstance(t, fixture)
	surface := fixture.surfaces[0]

	for _, placement := range []domainplugin.Placement{
		domainplugin.PlacementPage,
		domainplugin.PlacementWindow,
		domainplugin.PlacementAside,
		domainplugin.PlacementTab,
		domainplugin.PlacementPane,
	} {
		if err := fixture.state.MoveView(instance.ID(), placement); err != nil {
			t.Fatalf("MoveView to %s: %v", placement, err)
		}
		if instance.Placement() != placement {
			t.Fatalf("placement = %s, want %s", instance.Placement(), placement)
		}
		if !fixture.state.viewPlaced(instance, placement) {
			t.Fatalf("no container shows the instance in %s", placement)
		}
		if instance.ID() != onlyInstance(t, fixture).ID() || string(instance.State()) != "logical" {
			t.Fatalf("move to %s recreated or reset the instance", placement)
		}
		if surface.closed || fixture.factory != 1 {
			t.Fatalf("move to %s closed or rebuilt the surface (closed=%v factories=%d)",
				placement, surface.closed, fixture.factory)
		}
		if fixture.state.PageVisible() != (placement == domainplugin.PlacementPage) {
			t.Fatalf("page visible = %v in %s", fixture.state.PageVisible(), placement)
		}
	}
}

// TestMoveViewIsAtomicOnRejection proves a denied target leaves the source
// container untouched.
func TestMoveViewIsAtomicOnRejection(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton,
		domainplugin.PlacementPane, domainplugin.PlacementPage)
	if _, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := onlyInstance(t, fixture)
	if err := fixture.state.MoveView(instance.ID(), domainplugin.PlacementWindow); !errors.Is(err, appview.ErrPlacementDenied) {
		t.Fatalf("MoveView = %v, want ErrPlacementDenied", err)
	}
	if instance.Placement() != domainplugin.PlacementPane || fixture.state.TabCount() != 1 ||
		fixture.factory != 1 || fixture.surfaces[0].closed {
		t.Fatalf("rejected move changed the source: placement=%s tabs=%d factories=%d",
			instance.Placement(), fixture.state.TabCount(), fixture.factory)
	}
}

// TestOpeningAPageMovesTheExistingSingleton covers the page singleton rule:
// opening an existing instance in the page relocates that same instance.
func TestOpeningAPageMovesTheExistingSingleton(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton)
	first, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID})
	if err != nil {
		t.Fatalf("OpenView pane: %v", err)
	}
	instance := onlyInstance(t, fixture)
	pageTab, err := fixture.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, Placement: domainplugin.PlacementPage,
	})
	if err != nil {
		t.Fatalf("OpenView page: %v", err)
	}
	if pageTab != "" {
		t.Fatalf("page open returned tab %q", pageTab)
	}
	if fixture.state.TabCount() != 0 || first == "" {
		t.Fatalf("tabs = %d after the instance moved to the page", fixture.state.TabCount())
	}
	if !fixture.state.PageVisible() || fixture.state.ShownPageViewID() != counterViewID {
		t.Fatalf("page visible = %v id = %q", fixture.state.PageVisible(), fixture.state.ShownPageViewID())
	}
	if info, ok := fixture.state.PageView(counterViewID); !ok || info.Surface == nil ||
		info.Title != "Counter" || info.Err != nil {
		t.Fatalf("page info = %+v ok=%v", info, ok)
	}
	if instance.Placement() != domainplugin.PlacementPage || fixture.factory != 1 {
		t.Fatalf("instance placement = %s factories = %d", instance.Placement(), fixture.factory)
	}

	// Opening the page again focuses it rather than creating a second one.
	if _, err := fixture.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, Placement: domainplugin.PlacementPage,
	}); err != nil {
		t.Fatalf("second page open: %v", err)
	}
	if len(fixture.registry.Instances()) != 1 || fixture.factory != 1 {
		t.Fatalf("page open created a duplicate: instances=%d factories=%d",
			len(fixture.registry.Instances()), fixture.factory)
	}
}

func TestPageRejectsMultiInstanceViews(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceMulti)
	if _, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := onlyInstance(t, fixture)
	if _, err := fixture.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, Placement: domainplugin.PlacementPage,
	}); !errors.Is(err, appview.ErrPageRequiresSingleton) {
		t.Fatalf("multi page open = %v", err)
	}
	if _, err := fixture.state.DuplicateView(instance.ID(), domainplugin.PlacementPage); !errors.Is(err, appview.ErrPageRequiresSingleton) {
		t.Fatalf("multi page duplicate = %v", err)
	}
}

// TestDuplicateIsMultiOnlyAndCopiesNoState covers the duplicate rule and the
// copy-to-window path.
func TestDuplicateIsMultiOnlyAndCopiesNoState(t *testing.T) {
	singleton := newPlacementFixture(t, domainplugin.InstanceSingleton)
	if _, err := singleton.state.OpenView(appview.OpenRequest{ViewID: counterViewID}); err != nil {
		t.Fatalf("singleton OpenView: %v", err)
	}
	source := onlyInstance(t, singleton)
	if _, err := singleton.state.DuplicateView(source.ID(), domainplugin.PlacementTab); !errors.Is(err, appview.ErrDuplicateSingleton) {
		t.Fatalf("singleton duplicate = %v", err)
	}
	if _, err := singleton.state.DuplicateView(source.ID(), domainplugin.PlacementWindow); !errors.Is(err, appview.ErrDuplicateSingleton) {
		t.Fatalf("singleton copy to window = %v", err)
	}
	// The generic Tab duplicate commands follow the same View rule.
	if err := singleton.state.DuplicateTab(0); !errors.Is(err, appview.ErrDuplicateSingleton) {
		t.Fatalf("DuplicateTab = %v", err)
	}
	if err := singleton.state.DuplicateTabToWindow(0); !errors.Is(err, appview.ErrDuplicateSingleton) {
		t.Fatalf("DuplicateTabToWindow = %v", err)
	}
	if singleton.factory != 1 || len(singleton.state.DetachedTabIDs()) != 0 {
		t.Fatalf("singleton duplicate created a surface or window")
	}

	fixture := newPlacementFixture(t, domainplugin.InstanceMulti)
	if _, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID}); err != nil {
		t.Fatalf("multi OpenView: %v", err)
	}
	source = onlyInstance(t, fixture)
	if err := source.SetState([]byte("source state")); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	copiedID, err := fixture.state.DuplicateView(source.ID(), domainplugin.PlacementTab)
	if err != nil {
		t.Fatalf("DuplicateView: %v", err)
	}
	if copiedID == source.ID() {
		t.Fatal("duplicate reused the source instance")
	}
	copied, ok := fixture.registry.Instance(copiedID)
	if !ok {
		t.Fatalf("duplicate %q is not live", copiedID)
	}
	if string(copied.State()) != "" || copied.Placement() != domainplugin.PlacementTab {
		t.Fatalf("duplicate = placement %s state %q", copied.Placement(), copied.State())
	}
	if source.Closed() || string(source.State()) != "source state" {
		t.Fatalf("duplicate changed the source")
	}
	if fixture.state.TabCount() != 2 || fixture.factory != 2 {
		t.Fatalf("tabs = %d factories = %d", fixture.state.TabCount(), fixture.factory)
	}

	windowCopy, err := fixture.state.DuplicateView(source.ID(), domainplugin.PlacementWindow)
	if err != nil {
		t.Fatalf("duplicate to window: %v", err)
	}
	if ids := fixture.state.DetachedTabIDs(); len(ids) != 1 ||
		ids[0] != model.TabID("view:"+windowCopy) {
		t.Fatalf("detached tabs = %v, want view:%s", ids, windowCopy)
	}
}

// TestDetachViewPreservesInstanceIdentity proves detach is a move, not a
// recreate.
func TestDetachViewPreservesInstanceIdentity(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton)
	if _, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := onlyInstance(t, fixture)
	surface := fixture.surfaces[0]
	if !fixture.state.DetachTab(0) {
		t.Fatal("DetachTab reported false for a View Tab")
	}
	if instance.Placement() != domainplugin.PlacementWindow {
		t.Fatalf("DetachTab left placement %s", instance.Placement())
	}
	if instance.Closed() || instance.Placement() != domainplugin.PlacementWindow ||
		fixture.state.TabCount() != 0 || len(fixture.state.DetachedTabIDs()) != 1 {
		t.Fatalf("detach lost the instance: closed=%v placement=%s tabs=%d detached=%v",
			instance.Closed(), instance.Placement(), fixture.state.TabCount(),
			fixture.state.DetachedTabIDs())
	}
	if surface.closed || fixture.factory != 1 {
		t.Fatalf("detach rebuilt or closed the surface (closed=%v factories=%d)",
			surface.closed, fixture.factory)
	}
	if err := fixture.state.MoveView(instance.ID(), domainplugin.PlacementPane); err != nil {
		t.Fatalf("MoveView back: %v", err)
	}
	if len(fixture.state.DetachedTabIDs()) != 0 || fixture.state.TabCount() != 1 ||
		fixture.surfaces[0].closed {
		t.Fatalf("docking back did not move the same surface")
	}
}

// TestRestoreRebuildsEveryPlacementAndUnknownViewsStayUnavailable covers the
// per-placement restore payload, bounded state, and the rule that a missing
// View never blocks the Workspace.
func TestRestoreRebuildsEveryPlacementAndUnknownViewsStayUnavailable(t *testing.T) {
	fixtureRegistry := appview.NewRegistry()
	t.Cleanup(fixtureRegistry.Close)
	if _, err := fixtureRegistry.Register(appview.Owner{PluginID: "acme.view"}, domainplugin.ViewContribution{
		ID:         "counter",
		Title:      "Counter",
		Placements: allPlacements,
		Instances:  domainplugin.InstanceSingleton,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	factory := 0
	var surfaces []*fakeViewSurface
	repo := &memoryWorkspaceRepository{payload: domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "view:vi-1", Number: 1, Title: "Counter Tab",
			Root: domainworkspace.NewPaneNode("pane-view"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-view",
			PaneDescriptor: domainworkspace.PaneDescriptor{
				Kind: domainworkspace.PaneKindView,
				View: &domainworkspace.ViewPaneDescriptor{
					ViewID: counterViewID, ContextKey: "tab", Placement: domainworkspace.ViewPlacementTab,
					State: []byte("tab-state"),
				},
			},
		}},
		Views: []domainworkspace.RestoredView{
			{Placement: domainworkspace.ViewPlacementPage, ViewID: counterViewID,
				ContextKey: "page", Title: "Counter Page", State: []byte("page-state")},
			{Placement: domainworkspace.ViewPlacementWindow, ViewID: counterViewID,
				ContextKey: "window", Title: "Counter Window"},
			{Placement: domainworkspace.ViewPlacementAside, ViewID: counterViewID,
				ContextKey: "host-1", Title: "Counter Aside"},
			{Placement: domainworkspace.ViewPlacementPage, ViewID: "plugin.missing.view",
				ContextKey: "global", Title: "Missing View",
				State: make([]byte, domainworkspace.MaxViewStateBytes+1)},
		},
	}}
	st := NewState(
		WithViews(fixtureRegistry, func(*appview.Instance) (Surface, error) {
			factory++
			surface := &fakeViewSurface{}
			surfaces = append(surfaces, surface)
			return surface, nil
		}),
		WithWorkspace(appworkspace.NewService(repo), nil),
	)
	st.Restore(context.Background())

	if st.TabCount() != 1 || factory != 4 {
		t.Fatalf("tabs = %d factories = %d, want one Tab and four surfaces", st.TabCount(), factory)
	}
	paneID, ok := st.FocusedPaneID(0)
	if !ok {
		t.Fatal("restored View Tab has no focused Pane")
	}
	pane, ok := st.PaneInfoAt(0, paneID)
	if !ok || pane.Kind != domainworkspace.PaneKindView || pane.Surface == nil || pane.Err != nil {
		t.Fatalf("restored View Pane = %+v ok=%v", pane, ok)
	}
	instances := fixtureRegistry.Instances()
	if len(instances) != 4 {
		t.Fatalf("instances = %d, want tab, page, window, and aside", len(instances))
	}
	states := make(map[string]string)
	for _, instance := range instances {
		states[instance.ContextKey()] = string(instance.State())
	}
	if states["tab"] != "tab-state" || states["page"] != "page-state" {
		t.Fatalf("restored instance state = %v", states)
	}

	page, ok := st.PageView(counterViewID)
	if !ok || page.Surface == nil || page.ContextKey != "page" || page.Err != nil {
		t.Fatalf("page = %+v ok=%v", page, ok)
	}
	if !st.ShowPage(counterViewID) || !st.PageVisible() {
		t.Fatal("restored page cannot be shown")
	}
	if info, ok := st.AsideView("host-1"); ok {
		t.Fatalf("aside is keyed by instance id, found %+v", info)
	}
	var aside AsideViewInfo
	for _, info := range st.AsideViews() {
		if info.ContextKey == "host-1" {
			aside = info
		}
	}
	if aside.Surface == nil || aside.ViewID != counterViewID || aside.Err != nil {
		t.Fatalf("aside = %+v; all = %+v", aside, st.AsideViews())
	}
	if ids := st.DetachedTabIDs(); len(ids) != 1 {
		t.Fatalf("restored windows = %v", ids)
	} else if info, ok := st.DetachedTabInfo(ids[0]); !ok || info.Label != "Counter Window" {
		t.Fatalf("restored window info = %+v ok=%v", info, ok)
	}

	missing, ok := st.PageView("plugin.missing.view")
	if !ok || missing.Err != ErrUnavailablePane || missing.Surface != nil {
		t.Fatalf("missing page = %+v ok=%v, want an explained unavailable surface", missing, ok)
	}
	if !st.ShowPage("plugin.missing.view") {
		t.Fatal("an unavailable page cannot be shown")
	}
	if got := st.WorkspacePayload(); len(got.Views) < 3 {
		t.Fatalf("payload views = %+v, want per-placement descriptors", got.Views)
	}
}

// TestHomeAndSideSurfacesAreContributionDriven proves the Home sidebar and the
// side panel derive their View entries from contributions, and that the side
// panel passes the target's explicit context.
func TestHomeAndSideSurfacesAreContributionDriven(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton,
		domainplugin.PlacementPane, domainplugin.PlacementPage, domainplugin.PlacementAside)
	pages := fixture.state.HomePages()
	if len(pages) != 1 || pages[0].ViewID != counterViewID || pages[0].Title != "Counter" {
		t.Fatalf("home pages = %+v", pages)
	}
	if !fixture.state.FilesAvailable() {
		t.Fatal("an Aside contribution did not make the side panel available")
	}
	tabID := model.TabID("tab-session")
	fixture.state.ToggleFiles(tabID, "host-42", "web")
	tools := fixture.state.SideTools(tabID)
	if len(tools) != 1 || tools[0] != viewToolID(counterViewID) {
		t.Fatalf("side tools = %v, want the View contribution", tools)
	}
	if view := fixture.state.FilesView(tabID, 320); view == nil {
		t.Fatal("the View tool has no renderer")
	}
	instance := onlyInstance(t, fixture)
	if instance.Placement() != domainplugin.PlacementAside || instance.ContextKey() != "host-42" {
		t.Fatalf("aside instance = placement %s context %q", instance.Placement(), instance.ContextKey())
	}
	tools = fixture.state.SideTools(tabID)
	if len(tools) != 1 || !strings.HasPrefix(string(tools[0]), "aside:") {
		t.Fatalf("side tools after opening = %v", tools)
	}
	if label := fixture.state.SideViewLabel(tools[0]); label != "Counter" {
		t.Fatalf("side label = %q", label)
	}
}

// TestPageMoveRollsBackWhenTheSourceCannotBeRelocated proves the registry and
// the shell stay in agreement when an existing instance cannot be found in a
// container: the rejected move leaves the recorded placement untouched.
func TestPageMoveRollsBackWhenTheSourceCannotBeRelocated(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton,
		domainplugin.PlacementPane, domainplugin.PlacementPage)
	instance, created, err := fixture.registry.Open(appview.OpenRequest{
		ViewID: counterViewID, Placement: domainplugin.PlacementPane,
	})
	if err != nil || !created {
		t.Fatalf("registry open = %+v created=%v err=%v", instance, created, err)
	}
	// The instance is live in the registry but no shell container shows it.
	if _, err := fixture.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, Placement: domainplugin.PlacementPage,
	}); !errors.Is(err, ErrViewNotPlaced) {
		t.Fatalf("page open = %v, want ErrViewNotPlaced", err)
	}
	if instance.Placement() != domainplugin.PlacementPane {
		t.Fatalf("placement after the failed move = %s", instance.Placement())
	}
	if _, ok := fixture.registry.PageInstance(counterViewID); ok {
		t.Fatal("the failed move left the instance in the page slot")
	}
	if fixture.state.TabCount() != 0 || fixture.factory != 0 {
		t.Fatalf("the failed move changed the shell: tabs=%d factories=%d",
			fixture.state.TabCount(), fixture.factory)
	}
}

// TestRestorePlacementsHonoursStoredOrder proves the per-placement Order in
// the restore payload decides the rebuilt container's order.
func TestRestorePlacementsHonoursStoredOrder(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton,
		domainplugin.PlacementWindow, domainplugin.PlacementAside)
	fixture.state.RestorePlacements([]domainworkspace.RestoredView{
		{Placement: domainworkspace.ViewPlacementWindow, ViewID: counterViewID,
			ContextKey: "second", Title: "Second", Order: 2},
		{Placement: domainworkspace.ViewPlacementWindow, ViewID: counterViewID,
			ContextKey: "first", Title: "First", Order: 1},
		{Placement: domainworkspace.ViewPlacementAside, ViewID: counterViewID,
			ContextKey: "aside-later", Title: "Aside Later", Order: 3},
		{Placement: domainworkspace.ViewPlacementAside, ViewID: counterViewID,
			ContextKey: "aside-first", Title: "Aside First", Order: 0},
	})
	ids := fixture.state.DetachedTabIDs()
	if len(ids) != 2 {
		t.Fatalf("restored windows = %v", ids)
	}
	if info, ok := fixture.state.DetachedTabInfo(ids[0]); !ok || info.Label != "First" {
		t.Fatalf("first window = %+v ok=%v", info, ok)
	}
	if info, ok := fixture.state.DetachedTabInfo(ids[1]); !ok || info.Label != "Second" {
		t.Fatalf("second window = %+v ok=%v", info, ok)
	}
	asides := fixture.state.AsideViews()
	if len(asides) != 2 || asides[0].ContextKey != "aside-first" ||
		asides[1].ContextKey != "aside-later" {
		t.Fatalf("restored asides = %+v", asides)
	}
}

// TestHomeAndSideLabelsFollowTheHostLocale proves contribution-driven titles
// resolve through the host localizer at display time.
func TestHomeAndSideLabelsFollowTheHostLocale(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton,
		domainplugin.PlacementPane, domainplugin.PlacementPage, domainplugin.PlacementAside)
	translations := map[string]string{"view_title": "Contador"}
	fixture.registry.SetLocalizer(func(_ appview.Owner, key, literal string) string {
		if translated, ok := translations[key]; ok {
			return translated
		}
		return literal
	})
	pages := fixture.state.HomePages()
	if len(pages) != 1 || pages[0].Title != "Contador" {
		t.Fatalf("home pages = %+v", pages)
	}
	if label := fixture.state.SideViewLabel(viewToolID(counterViewID)); label != "Contador" {
		t.Fatalf("side label = %q", label)
	}
	translations["view_title"] = "Compteur"
	if pages := fixture.state.HomePages(); pages[0].Title != "Compteur" {
		t.Fatalf("home title after locale change = %q", pages[0].Title)
	}
	if label := fixture.state.SideViewLabel(viewToolID(counterViewID)); label != "Compteur" {
		t.Fatalf("side label after locale change = %q", label)
	}
}

// TestSideSelectionStaysOnTheOpenedAside proves opening a contribution lazily
// keeps the side panel on that View instead of falling back to the first tool.
func TestSideSelectionStaysOnTheOpenedAside(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton,
		domainplugin.PlacementPane, domainplugin.PlacementAside)
	tabID, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID})
	if err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	fixture.state.ToggleFiles(tabID, "host-7", "web")
	tool := viewToolID(counterViewID)
	fixture.state.SetSideTool(tabID, tool)
	if got := fixture.state.SideTool(tabID); got != tool {
		t.Fatalf("selected tool = %q, want %q", got, tool)
	}
	if view := fixture.state.FilesView(tabID, 320); view == nil {
		t.Fatal("the View tool has no renderer")
	}
	tools := fixture.state.SideTools(tabID)
	if len(tools) != 1 || !strings.HasPrefix(string(tools[0]), "aside:") {
		t.Fatalf("side tools after opening = %v", tools)
	}
	if got := fixture.state.SideTool(tabID); got != tools[0] {
		t.Fatalf("selection flipped to %q, want %q", got, tools[0])
	}
	if fixture.state.SideViewUnavailable(tools[0]) {
		t.Fatalf("the opened aside %q is not renderable", tools[0])
	}
}

// TestMissingAsideIsReportedUnavailable proves a restored side slot whose
// contribution is gone is explained rather than drawn as a raw id.
func TestMissingAsideIsReportedUnavailable(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton,
		domainplugin.PlacementPane, domainplugin.PlacementAside)
	fixture.state.RestorePlacements([]domainworkspace.RestoredView{{
		Placement:  domainworkspace.ViewPlacementAside,
		ViewID:     "plugin.missing.view",
		ContextKey: "host-9",
		Title:      "Missing View",
	}})
	tabID, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID})
	if err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	fixture.state.ToggleFiles(tabID, "host-9", "web")
	var missing SideToolID
	for _, id := range fixture.state.SideTools(tabID) {
		if strings.HasPrefix(string(id), "aside:") {
			missing = id
		}
	}
	if missing == "" {
		t.Fatalf("restored aside is missing from %v", fixture.state.SideTools(tabID))
	}
	if !fixture.state.SideViewUnavailable(missing) {
		t.Fatalf("missing aside %q is not reported unavailable", missing)
	}
	if label := fixture.state.SideViewLabel(missing); label != "Missing View" {
		t.Fatalf("missing aside label = %q", label)
	}
	// The aside belongs to host-9; another Host's panel must not offer it.
	fixture.state.ToggleFiles(tabID, "host-7", "other")
	for _, id := range fixture.state.SideTools(tabID) {
		if strings.HasPrefix(string(id), "aside:") {
			t.Fatalf("host-9 aside leaked into the host-7 panel: %q", id)
		}
	}
}

// mixedPlacementFixture pairs the Session service with the View registry so a
// Tab can hold View Panes beside terminal Panes.
type mixedPlacementFixture struct {
	state     *State
	registry  *appview.Registry
	transport *workspaceFactory
	service   *appsession.Service
	surfaces  []*fakeViewSurface
	factory   int
}

func newMixedPlacementFixture(t *testing.T, policy domainplugin.InstancePolicy,
	placements ...domainplugin.Placement) *mixedPlacementFixture {
	t.Helper()
	if len(placements) == 0 {
		placements = allPlacements
	}
	fixture := &mixedPlacementFixture{
		registry:  appview.NewRegistry(),
		transport: &workspaceFactory{},
	}
	t.Cleanup(fixture.registry.Close)
	if _, err := fixture.registry.Register(appview.Owner{PluginID: "acme.view"}, domainplugin.ViewContribution{
		ID:         "counter",
		Title:      "Counter",
		Placements: placements,
		Instances:  policy,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	fixture.service = appsession.NewService(fixture.transport)
	t.Cleanup(fixture.service.CloseAll)
	fixture.state = NewState(
		WithSessions(fixture.service, func(*appsession.Session) (Surface, error) {
			return &fakeSurface{}, nil
		}),
		WithViews(fixture.registry, func(*appview.Instance) (Surface, error) {
			fixture.factory++
			surface := &fakeViewSurface{}
			fixture.surfaces = append(fixture.surfaces, surface)
			return surface, nil
		}),
	)
	return fixture
}

func (f *mixedPlacementFixture) onlyInstance(t *testing.T) *appview.Instance {
	t.Helper()
	instances := f.registry.Instances()
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(instances))
	}
	return instances[0]
}

func (f *mixedPlacementFixture) instanceWithContext(t *testing.T, contextKey string) *appview.Instance {
	t.Helper()
	for _, instance := range f.registry.Instances() {
		if instance.ContextKey() == contextKey {
			return instance
		}
	}
	t.Fatalf("no live View instance for context %q", contextKey)
	return nil
}

// openMixedTab builds one Session Tab with two Panes: the original Session and
// a View Pane moved in from its own View Tab.
func (f *mixedPlacementFixture) openMixedTab(t *testing.T,
	contextKey string) (model.TabID, *appview.Instance, *fakeViewSurface, model.PaneID) {
	t.Helper()
	if err := f.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	tabID, _ := f.state.TabID(0)
	sessionPane := panesOf(t, f.state, 0)[0]
	if _, err := f.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, ContextKey: contextKey,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := f.instanceWithContext(t, contextKey)
	viewTabID, _ := f.state.TabID(1)
	viewPane := panesOf(t, f.state, 1)[0]
	if !f.state.MovePane(viewTabID, viewPane, tabID, sessionPane, domainworkspace.SideRight) {
		t.Fatalf("MovePane into the Session Tab returned false")
	}
	if f.state.TabCount() != 1 {
		t.Fatalf("tabs = %d after the View joined the Session Tab", f.state.TabCount())
	}
	return tabID, instance, f.surfaces[0], sessionPane
}

func tabIndexByID(t *testing.T, state *State, id model.TabID) int {
	t.Helper()
	for index := 0; index < state.TabCount(); index++ {
		if got, _ := state.TabID(index); got == id {
			return index
		}
	}
	t.Fatalf("tab %q is missing", id)
	return -1
}

func viewPaneInfoAt(t *testing.T, state *State, tabIndex int) PaneInfo {
	t.Helper()
	for _, paneID := range panesOf(t, state, tabIndex) {
		if info, ok := state.PaneInfoAt(tabIndex, paneID); ok && info.ViewInstanceID != "" {
			return info
		}
	}
	t.Fatalf("tab %d has no live View Pane", tabIndex)
	return PaneInfo{}
}

func detachedViewPaneInfo(t *testing.T, state *State, tabID model.TabID) PaneInfo {
	t.Helper()
	tab, ok := state.DetachedTabInfo(tabID)
	if !ok {
		t.Fatalf("detached tab %q is missing", tabID)
	}
	for _, paneID := range tab.Panes {
		if info, ok := state.DetachedPaneInfoAt(tabID, paneID); ok && info.ViewInstanceID != "" {
			return info
		}
	}
	t.Fatalf("detached tab %q has no live View Pane", tabID)
	return PaneInfo{}
}

// TestViewMovesOutOfAMixedTabPreserveTheInstanceAndSiblingSession walks a View
// through every placement after it was dragged into a Session split. The same
// instance and surface survive, and the sibling terminal Pane stays live.
func TestViewMovesOutOfAMixedTabPreserveTheInstanceAndSiblingSession(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	tabID, instance, surface, sessionPane := fixture.openMixedTab(t, "global")
	if err := instance.SetState([]byte("logical")); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if instance.Placement() != domainplugin.PlacementPane {
		t.Fatalf("placement in the split = %s, want pane", instance.Placement())
	}

	for _, placement := range []domainplugin.Placement{
		domainplugin.PlacementTab,
		domainplugin.PlacementWindow,
		domainplugin.PlacementPage,
		domainplugin.PlacementAside,
		domainplugin.PlacementPane,
		domainplugin.PlacementWindow,
		domainplugin.PlacementPane,
	} {
		if err := fixture.state.MoveView(instance.ID(), placement); err != nil {
			t.Fatalf("MoveView to %s: %v", placement, err)
		}
		if instance.Placement() != placement {
			t.Fatalf("placement = %s, want %s", instance.Placement(), placement)
		}
		if !fixture.state.viewPlaced(instance, placement) {
			t.Fatalf("no container shows the instance in %s", placement)
		}
		if live := fixture.registry.Instances(); len(live) != 1 || live[0] != instance {
			t.Fatalf("move to %s recreated the instance: %+v", placement, live)
		}
		if fixture.factory != 1 || surface.closed {
			t.Fatalf("move to %s rebuilt the surface (factory=%d closed=%v)",
				placement, fixture.factory, surface.closed)
		}
		if string(instance.State()) != "logical" {
			t.Fatalf("move to %s reset the logical state: %q", placement, instance.State())
		}

		index := tabIndexByID(t, fixture.state, tabID)
		if panes := panesOf(t, fixture.state, index); len(panes) != 1 {
			t.Fatalf("sibling Session Tab has %d Panes in %s, want 1", len(panes), placement)
		}
		info, ok := fixture.state.PaneInfoAt(index, sessionPane)
		if !ok || info.Kind != domainworkspace.PaneKindSession || info.Surface == nil || info.Err != nil {
			t.Fatalf("sibling Session Pane in %s = %+v ok=%v", placement, info, ok)
		}
		wantTabs := 1
		if placement == domainplugin.PlacementTab || placement == domainplugin.PlacementPane {
			wantTabs = 2
		}
		if fixture.state.TabCount() != wantTabs {
			t.Fatalf("tabs in %s = %d, want %d", placement, fixture.state.TabCount(), wantTabs)
		}
	}
}

// TestT14ReattachUpdatesViewPlacement proves a reattached View records the
// work-Tab placement of its resulting container instead of staying "window".
func TestT14ReattachUpdatesViewPlacement(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton)
	if _, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := onlyInstance(t, fixture)
	surface := fixture.surfaces[0]
	if !fixture.state.DetachTab(0) {
		t.Fatal("DetachTab reported false for a View Tab")
	}
	if instance.Placement() != domainplugin.PlacementWindow ||
		!fixture.state.viewPlaced(instance, domainplugin.PlacementWindow) {
		t.Fatalf("detached placement = %s", instance.Placement())
	}
	detached := fixture.state.DetachedTabIDs()
	if len(detached) != 1 {
		t.Fatalf("detached tabs = %v", detached)
	}

	if !fixture.state.ReattachDetachedTab(detached[0]) {
		t.Fatal("ReattachDetachedTab reported false")
	}
	if instance.Placement() != domainplugin.PlacementTab {
		t.Fatalf("reattached placement = %s, want tab", instance.Placement())
	}
	if !fixture.state.viewPlaced(instance, domainplugin.PlacementTab) {
		t.Fatal("no main-window container shows the reattached instance")
	}
	tabID, ok := fixture.state.TabID(0)
	if !ok || tabID != model.TabID("view:"+instance.ID()) {
		t.Fatalf("reattached tab id = %q, want view:%s", tabID, instance.ID())
	}
	if fixture.state.TabCount() != 1 || len(fixture.state.DetachedTabIDs()) != 0 ||
		fixture.factory != 1 || surface.closed {
		t.Fatalf("reattach changed the instance: tabs=%d detached=%v factory=%d closed=%v",
			fixture.state.TabCount(), fixture.state.DetachedTabIDs(), fixture.factory, surface.closed)
	}
	if live := fixture.registry.Instances(); len(live) != 1 || live[0] != instance {
		t.Fatalf("reattach recreated the instance: %+v", live)
	}

	payload := fixture.state.WorkspacePayload()
	found := false
	for _, pane := range payload.Panes {
		if pane.View != nil && pane.View.ViewID == counterViewID {
			found = true
			if pane.View.Placement != domainworkspace.ViewPlacementTab {
				t.Fatalf("saved View placement = %s, want tab", pane.View.Placement)
			}
		}
	}
	if !found {
		t.Fatalf("saved payload has no View Pane: %+v", payload.Panes)
	}
}

// TestT14MixedTabDetachUpdatesPlacement covers detaching a Tab that holds both
// a Session and a View: the View records Window, comes back as Pane, and the
// whole move stays atomic when Window is not a declared placement.
func TestT14MixedTabDetachUpdatesPlacement(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	tabID, instance, surface, sessionPane := fixture.openMixedTab(t, "global")

	if !fixture.state.DetachTab(0) {
		t.Fatal("DetachTab reported false for a mixed Tab")
	}
	if instance.Placement() != domainplugin.PlacementWindow ||
		!fixture.state.viewPlaced(instance, domainplugin.PlacementWindow) {
		t.Fatalf("mixed detach left placement %s", instance.Placement())
	}
	detached := fixture.state.DetachedTabIDs()
	if len(detached) != 1 || detached[0] != tabID {
		t.Fatalf("detached tabs = %v, want %q", detached, tabID)
	}
	if fixture.state.TabCount() != 0 {
		t.Fatalf("tabs = %d after the mixed Tab detached", fixture.state.TabCount())
	}
	for _, view := range fixture.state.WorkspacePayload().Views {
		if view.ViewID == counterViewID && view.Placement != domainworkspace.ViewPlacementWindow {
			t.Fatalf("saved detached View placement = %s, want window", view.Placement)
		}
	}

	if !fixture.state.ReattachDetachedTab(tabID) {
		t.Fatal("ReattachDetachedTab reported false")
	}
	if instance.Placement() != domainplugin.PlacementPane ||
		!fixture.state.viewPlaced(instance, domainplugin.PlacementPane) {
		t.Fatalf("mixed reattach left placement %s", instance.Placement())
	}
	if fixture.state.TabCount() != 1 || len(fixture.state.DetachedTabIDs()) != 0 {
		t.Fatalf("reattach left tabs=%d detached=%v",
			fixture.state.TabCount(), fixture.state.DetachedTabIDs())
	}
	index := tabIndexByID(t, fixture.state, tabID)
	if panes := panesOf(t, fixture.state, index); len(panes) != 2 {
		t.Fatalf("reattached mixed Tab has %d Panes, want 2", len(panes))
	}
	if info, ok := fixture.state.PaneInfoAt(index, sessionPane); !ok || info.Surface == nil || info.Err != nil {
		t.Fatalf("sibling Session Pane after reattach = %+v ok=%v", info, ok)
	}
	if fixture.factory != 1 || surface.closed {
		t.Fatalf("detach/reattach rebuilt the surface (factory=%d closed=%v)",
			fixture.factory, surface.closed)
	}
	if live := fixture.registry.Instances(); len(live) != 1 || live[0] != instance {
		t.Fatalf("detach/reattach recreated the instance: %+v", live)
	}
	payload := fixture.state.WorkspacePayload()
	found := false
	for _, pane := range payload.Panes {
		if pane.View != nil && pane.View.ViewID == counterViewID {
			found = true
			if pane.View.Placement != domainworkspace.ViewPlacementPane {
				t.Fatalf("saved View placement after reattach = %s, want pane", pane.View.Placement)
			}
		}
	}
	if !found {
		t.Fatalf("saved payload has no View Pane: %+v", payload.Panes)
	}

	// A View that does not declare Window refuses the detach before the Tab or
	// its recorded placement changes.
	restricted := newMixedPlacementFixture(t, domainplugin.InstanceSingleton,
		domainplugin.PlacementPane)
	_, restrictedInstance, _, _ := restricted.openMixedTab(t, "global")
	if restricted.state.DetachTab(0) {
		t.Fatal("DetachTab moved a View that does not declare Window")
	}
	if restricted.state.TabCount() != 1 || len(restricted.state.DetachedTabIDs()) != 0 {
		t.Fatalf("rejected detach changed the layout: tabs=%d detached=%v",
			restricted.state.TabCount(), restricted.state.DetachedTabIDs())
	}
	if restrictedInstance.Placement() != domainplugin.PlacementPane {
		t.Fatalf("rejected detach changed placement to %s", restrictedInstance.Placement())
	}
}

// TestT14RestoredViewTabIDsFollowLiveInstancesAndStayUnique proves restore
// discards stale stored View tab ids, names every View Tab after the instance
// this run opened, and resolves a stored collision deterministically.
func TestT14RestoredViewTabIDsFollowLiveInstancesAndStayUnique(t *testing.T) {
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
	factory := 0
	viewPane := func(contextKey string) domainworkspace.PaneDescriptor {
		return domainworkspace.PaneDescriptor{
			Kind: domainworkspace.PaneKindView,
			View: &domainworkspace.ViewPaneDescriptor{
				ViewID:     counterViewID,
				ContextKey: contextKey,
				Placement:  domainworkspace.ViewPlacementTab,
			},
		}
	}
	repo := &memoryWorkspaceRepository{payload: domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{
			{ID: "view:vi-7", Number: 1, Root: domainworkspace.NewPaneNode("pane-one")},
			{ID: "view:vi-8", Number: 2, Root: domainworkspace.NewPaneNode("pane-two")},
			{ID: "view:vi-1", Number: 3, Root: domainworkspace.NewPaneNode("pane-session")},
		},
		Panes: []domainworkspace.RestoredPane{
			{ID: "pane-one", PaneDescriptor: viewPane("one")},
			{ID: "pane-two", PaneDescriptor: viewPane("two")},
			{ID: "pane-session", PaneDescriptor: domainworkspace.NewSessionPaneDescriptor(
				domainworkspace.SessionDescriptor{Kind: domainworkspace.SessionLocal})},
		},
		Views: []domainworkspace.RestoredView{{
			Placement: domainworkspace.ViewPlacementWindow, ViewID: counterViewID,
			ContextKey: "window", Title: "Counter Window",
		}},
	}}
	st := NewState(
		WithViews(registry, func(*appview.Instance) (Surface, error) {
			factory++
			return &fakeViewSurface{}, nil
		}),
		WithWorkspace(appworkspace.NewService(repo), nil),
	)
	st.Restore(context.Background())

	if st.TabCount() != 3 {
		t.Fatalf("tabs = %d, want the two View Tabs and the Session Tab", st.TabCount())
	}
	seen := make(map[model.TabID]bool, st.TabCount())
	for index := 0; index < st.TabCount(); index++ {
		id, ok := st.TabID(index)
		if !ok {
			t.Fatalf("tab %d has no id", index)
		}
		if seen[id] {
			t.Fatalf("duplicate restored tab id %q", id)
		}
		seen[id] = true
	}
	for index, want := range []string{"vi-1", "vi-2"} {
		info, ok := st.TabInfo(index)
		if !ok || info.ViewInstanceID != want {
			t.Fatalf("tab %d View instance = %q ok=%v, want %s",
				index, info.ViewInstanceID, ok, want)
		}
		if id, _ := st.TabID(index); id != model.TabID("view:"+want) {
			t.Fatalf("tab %d id = %q, want view:%s", index, id, want)
		}
	}
	// The Session Tab stored view:vi-1, which a fresh live View now owns. A
	// Session-only Tab cannot keep the view: namespace, so it gets a fresh
	// runtime id instead of an ambiguous or stale View id.
	if id, _ := st.TabID(2); id == "" || strings.HasPrefix(string(id), "view:") {
		t.Fatalf("session-only restored tab id = %q, want a runtime id", id)
	}
	first, _ := st.TabID(0)
	second, _ := st.TabID(1)
	if !st.MoveTab(first, second, true) {
		t.Fatal("MoveTab could not resolve the restored View Tab ids")
	}

	detached := st.DetachedTabIDs()
	if len(detached) != 1 {
		t.Fatalf("restored windows = %v", detached)
	}
	var window *appview.Instance
	for _, instance := range registry.Instances() {
		if instance.ContextKey() == "window" {
			window = instance
		}
	}
	if window == nil {
		t.Fatal("the restored window instance is missing")
	}
	if want := model.TabID("view:" + window.ID()); detached[0] != want {
		t.Fatalf("restored window id = %q, want %q", detached[0], want)
	}
	if factory != 3 {
		t.Fatalf("view surfaces = %d, want one per restored instance", factory)
	}
}

// TestT14DuplicateOfAMixedTabFollowsTheViewRule proves the generic Tab
// duplicate commands apply the View rules to a Tab holding a View Pane:
// singletons refuse, and multi-instance Views are duplicated without state.
func TestT14DuplicateOfAMixedTabFollowsTheViewRule(t *testing.T) {
	singleton := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	singleton.openMixedTab(t, "global")
	if singleton.state.TabCount() != 1 {
		t.Fatalf("mixed singleton fixture tabs = %d", singleton.state.TabCount())
	}
	if err := singleton.state.DuplicateTab(0); !errors.Is(err, appview.ErrDuplicateSingleton) {
		t.Fatalf("DuplicateTab = %v, want ErrDuplicateSingleton", err)
	}
	if err := singleton.state.DuplicateTabToWindow(0); !errors.Is(err, appview.ErrDuplicateSingleton) {
		t.Fatalf("DuplicateTabToWindow = %v, want ErrDuplicateSingleton", err)
	}
	if singleton.state.TabCount() != 1 || len(singleton.state.DetachedTabIDs()) != 0 ||
		singleton.factory != 1 {
		t.Fatalf("refused duplicate changed the layout: tabs=%d detached=%v factory=%d",
			singleton.state.TabCount(), singleton.state.DetachedTabIDs(), singleton.factory)
	}

	multi := newMixedPlacementFixture(t, domainplugin.InstanceMulti)
	_, source, sourceSurface, _ := multi.openMixedTab(t, "global")
	if err := source.SetState([]byte("source state")); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := multi.state.DuplicateTab(0); err != nil {
		t.Fatalf("DuplicateTab: %v", err)
	}
	if multi.state.TabCount() != 2 {
		t.Fatalf("tabs after duplicate = %d, want 2", multi.state.TabCount())
	}
	copiedInfo := viewPaneInfoAt(t, multi.state, 1)
	if copiedInfo.ViewInstanceID == source.ID() {
		t.Fatal("the duplicated mixed Tab reused the source instance")
	}
	copied, ok := multi.registry.Instance(copiedInfo.ViewInstanceID)
	if !ok {
		t.Fatalf("duplicate %q is not live", copiedInfo.ViewInstanceID)
	}
	if copied.ViewID() != source.ViewID() || copied.ContextKey() != source.ContextKey() {
		t.Fatalf("duplicate = view %q context %q, want %q/%q",
			copied.ViewID(), copied.ContextKey(), source.ViewID(), source.ContextKey())
	}
	if string(copied.State()) != "" || copied.Placement() != domainplugin.PlacementPane {
		t.Fatalf("duplicate = state %q placement %s, want empty/pane",
			copied.State(), copied.Placement())
	}
	if copiedInfo.Err != nil || copiedInfo.Surface == nil ||
		copiedInfo.Kind != domainworkspace.PaneKindView {
		t.Fatalf("duplicated View Pane = %+v, want a live surface", copiedInfo)
	}
	if source.Closed() || string(source.State()) != "source state" {
		t.Fatal("duplicate changed the source instance")
	}
	if multi.factory != 2 || sourceSurface.closed {
		t.Fatalf("duplicate = factory %d source closed %v", multi.factory, sourceSurface.closed)
	}

	if err := multi.state.DuplicateTabToWindow(0); err != nil {
		t.Fatalf("DuplicateTabToWindow: %v", err)
	}
	detached := multi.state.DetachedTabIDs()
	if len(detached) != 1 {
		t.Fatalf("detached tabs = %v", detached)
	}
	windowInfo := detachedViewPaneInfo(t, multi.state, detached[0])
	windowCopy, ok := multi.registry.Instance(windowInfo.ViewInstanceID)
	if !ok {
		t.Fatalf("window duplicate %q is not live", windowInfo.ViewInstanceID)
	}
	if windowCopy.ID() == source.ID() || windowCopy.ID() == copied.ID() {
		t.Fatal("the window copy reused an existing instance")
	}
	if string(windowCopy.State()) != "" ||
		windowCopy.Placement() != domainplugin.PlacementWindow {
		t.Fatalf("window copy = state %q placement %s, want empty/window",
			windowCopy.State(), windowCopy.Placement())
	}
	if windowInfo.Err != nil || windowInfo.Surface == nil {
		t.Fatalf("window copy Pane = %+v, want a live surface", windowInfo)
	}
	if multi.factory != 3 {
		t.Fatalf("view surfaces = %d, want three", multi.factory)
	}
}

// TestAsideContextMatchesTheDocumentedTarget locks the explicit context the
// side panel passes: Host id, local:<label>, or the app-wide fallback.
func TestAsideContextMatchesTheDocumentedTarget(t *testing.T) {
	cases := []struct {
		target SideTarget
		want   string
	}{
		{SideTarget{Host: "host-1", Label: "web"}, "host-1"},
		{SideTarget{Label: "web"}, "local:web"},
		{SideTarget{}, "global"},
	}
	for _, test := range cases {
		if got := asideContext(test.target); got != test.want {
			t.Fatalf("asideContext(%+v) = %q, want %q", test.target, got, test.want)
		}
	}
}

// TestT14PaneDragUpdatesTheViewPlacement covers the direct drag between a
// standalone View Tab and a Session split in both directions: the registry, the
// Pane menu hint, and the saved descriptor follow the container instead of
// keeping the placement the View was opened with.
func TestT14PaneDragUpdatesTheViewPlacement(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	if err := fixture.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	sessionTab, _ := fixture.state.TabID(0)
	sessionPane := panesOf(t, fixture.state, 0)[0]
	if _, err := fixture.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, Placement: domainplugin.PlacementTab,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := fixture.onlyInstance(t)
	viewTab, _ := fixture.state.TabID(1)
	viewPane := panesOf(t, fixture.state, 1)[0]
	if want := model.TabID("view:" + instance.ID()); viewTab != want {
		t.Fatalf("View Tab id = %q, want %q", viewTab, want)
	}
	if instance.Placement() != domainplugin.PlacementTab {
		t.Fatalf("opened placement = %s, want tab", instance.Placement())
	}

	if !fixture.state.MovePane(viewTab, viewPane, sessionTab, sessionPane, domainworkspace.SideLeft) {
		t.Fatal("MovePane into the Session split returned false")
	}
	if fixture.state.TabCount() != 1 {
		t.Fatalf("tabs = %d after the View joined the split", fixture.state.TabCount())
	}
	if instance.Placement() != domainplugin.PlacementPane {
		t.Fatalf("placement after the drag into the split = %s, want pane", instance.Placement())
	}
	if info := viewPaneInfoAt(t, fixture.state, 0); info.ViewPlacement != domainplugin.PlacementPane {
		t.Fatalf("Pane menu hint = %s, want pane", info.ViewPlacement)
	}
	payload := fixture.state.WorkspacePayload()
	found := false
	for _, pane := range payload.Panes {
		if pane.View == nil || pane.View.ViewID != counterViewID {
			continue
		}
		found = true
		if pane.View.Placement != domainworkspace.ViewPlacementPane {
			t.Fatalf("saved placement after the drag = %s, want pane", pane.View.Placement)
		}
	}
	if !found {
		t.Fatalf("saved payload lost the View Pane: %+v", payload.Panes)
	}

	// Moving the View back out is a real move now: the registry no longer
	// claims it is already a Tab, so the same instance gets its own View Tab.
	if err := fixture.state.MoveView(instance.ID(), domainplugin.PlacementTab); err != nil {
		t.Fatalf("MoveView back to a Tab: %v", err)
	}
	if fixture.state.TabCount() != 2 {
		t.Fatalf("tabs after moving the View back out = %d, want 2", fixture.state.TabCount())
	}
	if instance.Placement() != domainplugin.PlacementTab {
		t.Fatalf("placement after moving back out = %s, want tab", instance.Placement())
	}
	backIndex := tabIndexByID(t, fixture.state, model.TabID("view:"+instance.ID()))
	if info := viewPaneInfoAt(t, fixture.state, backIndex); info.ViewPlacement != domainplugin.PlacementTab {
		t.Fatalf("Pane menu hint after moving back out = %s, want tab", info.ViewPlacement)
	}
	for _, pane := range fixture.state.WorkspacePayload().Panes {
		if pane.View == nil || pane.View.ViewID != counterViewID {
			continue
		}
		if pane.View.Placement != domainworkspace.ViewPlacementTab {
			t.Fatalf("saved placement after moving back out = %s, want tab", pane.View.Placement)
		}
	}
	if fixture.factory != 1 || fixture.surfaces[0].closed {
		t.Fatalf("the drags rebuilt the surface (factory=%d closed=%v)",
			fixture.factory, fixture.surfaces[0].closed)
	}
}

// TestT14PaneDropOnAViewTabRecordsPane covers the other drag direction: a
// Session Pane dragged into a standalone View Tab changes that View's container
// from Tab to Pane even though the moved Pane is not a View.
func TestT14PaneDropOnAViewTabRecordsPane(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	if err := fixture.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	sessionTab, _ := fixture.state.TabID(0)
	sessionPane := panesOf(t, fixture.state, 0)[0]
	if _, err := fixture.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, Placement: domainplugin.PlacementTab,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := fixture.onlyInstance(t)
	viewTab, _ := fixture.state.TabID(1)
	viewPane := panesOf(t, fixture.state, 1)[0]

	if !fixture.state.MovePane(sessionTab, sessionPane, viewTab, viewPane, domainworkspace.SideRight) {
		t.Fatal("MovePane onto the View Tab returned false")
	}
	if fixture.state.TabCount() != 1 {
		t.Fatalf("tabs = %d after the Session joined the View Tab", fixture.state.TabCount())
	}
	if instance.Placement() != domainplugin.PlacementPane {
		t.Fatalf("placement = %s, want pane", instance.Placement())
	}
	if got, _ := fixture.state.TabID(0); got != viewTab {
		t.Fatalf("target Tab id = %q, want the View Tab id %q", got, viewTab)
	}
	if info := viewPaneInfoAt(t, fixture.state, 0); info.ViewPlacement != domainplugin.PlacementPane {
		t.Fatalf("Pane menu hint = %s, want pane", info.ViewPlacement)
	}
}

// TestT14PaneDragPlacementSurvivesRestore proves the drag records the Pane
// placement in the saved payload and that a restored split keeps the View in
// the Pane placement rather than reverting to the opened Tab placement.
func TestT14PaneDragPlacementSurvivesRestore(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	if err := fixture.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	sessionTab, _ := fixture.state.TabID(0)
	sessionPane := panesOf(t, fixture.state, 0)[0]
	if _, err := fixture.state.OpenView(appview.OpenRequest{
		ViewID: counterViewID, Placement: domainplugin.PlacementTab,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := fixture.onlyInstance(t)
	viewTab, _ := fixture.state.TabID(1)
	viewPane := panesOf(t, fixture.state, 1)[0]
	if !fixture.state.MovePane(viewTab, viewPane, sessionTab, sessionPane, domainworkspace.SideLeft) {
		t.Fatal("MovePane into the Session split returned false")
	}
	payload := domainworkspace.SanitizePayload(fixture.state.WorkspacePayload())

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
	factory := 0
	restored := NewState(
		WithViews(registry, func(*appview.Instance) (Surface, error) {
			factory++
			return &fakeViewSurface{}, nil
		}),
		WithWorkspace(appworkspace.NewService(&memoryWorkspaceRepository{payload: payload}), nil),
	)
	restored.Restore(context.Background())
	if restored.TabCount() != 1 || factory != 1 {
		t.Fatalf("restored tabs = %d factories = %d, want one mixed Tab", restored.TabCount(), factory)
	}
	info := viewPaneInfoAt(t, restored, 0)
	if info.ViewPlacement != domainplugin.PlacementPane {
		t.Fatalf("restored Pane menu hint = %s, want pane", info.ViewPlacement)
	}
	instances := registry.Instances()
	if len(instances) != 1 || instances[0].Placement() != domainplugin.PlacementPane {
		t.Fatalf("restored instance = %+v, want one Pane placement", instances)
	}
	if instances[0].ID() != instance.ID() || string(instances[0].State()) != string(instance.State()) {
		t.Fatalf("restore did not preserve the View instance identity/state")
	}
}

// TestT14RuntimeViewTabIDsStayUnique reproduces the runtime View Tab id
// collision: two Tab-placed Views are mixed into one Tab, then the View that
// names that Tab leaves. The leftover Tab is re-identified after the View it
// still shows before the moved instance's new Tab takes the canonical id, so
// save/restore keeps both View Tabs and MoveTab stays unambiguous.
func TestT14RuntimeViewTabIDsStayUnique(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	if err := fixture.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	for _, contextKey := range []string{"one", "two"} {
		if _, err := fixture.state.OpenView(appview.OpenRequest{
			ViewID: counterViewID, ContextKey: contextKey, Placement: domainplugin.PlacementTab,
		}); err != nil {
			t.Fatalf("OpenView %q: %v", contextKey, err)
		}
	}
	one := fixture.instanceWithContext(t, "one")
	two := fixture.instanceWithContext(t, "two")
	oneTab := model.TabID("view:" + one.ID())
	twoTab := model.TabID("view:" + two.ID())
	if got, _ := fixture.state.TabID(1); got != oneTab {
		t.Fatalf("first View Tab id = %q, want %q", got, oneTab)
	}
	if got, _ := fixture.state.TabID(2); got != twoTab {
		t.Fatalf("second View Tab id = %q, want %q", got, twoTab)
	}
	onePane := panesOf(t, fixture.state, 1)[0]
	twoPane := panesOf(t, fixture.state, 2)[0]

	if !fixture.state.MovePane(twoTab, twoPane, oneTab, onePane, domainworkspace.SideRight) {
		t.Fatal("MovePane into the other View Tab returned false")
	}
	if fixture.state.TabCount() != 2 {
		t.Fatalf("tabs = %d after the View Panes mixed", fixture.state.TabCount())
	}
	if one.Placement() != domainplugin.PlacementPane || two.Placement() != domainplugin.PlacementPane {
		t.Fatalf("mixed placements = %s/%s, want pane/pane", one.Placement(), two.Placement())
	}
	if got, _ := fixture.state.TabID(1); got != oneTab {
		t.Fatalf("mixed Tab id = %q, want %q", got, oneTab)
	}

	// The owning View leaves for its own View Tab. The leftover mixed Tab must
	// be renamed after the View it still shows, before the new Tab claims
	// view:<one instance>.
	if err := fixture.state.MoveView(one.ID(), domainplugin.PlacementTab); err != nil {
		t.Fatalf("MoveView out of the mixed Tab: %v", err)
	}
	if one.Placement() != domainplugin.PlacementTab || two.Placement() != domainplugin.PlacementTab {
		t.Fatalf("placements after the split = %s/%s, want tab/tab",
			one.Placement(), two.Placement())
	}
	if fixture.state.TabCount() != 3 {
		t.Fatalf("tabs = %d, want the Session Tab and two View Tabs", fixture.state.TabCount())
	}
	seen := make(map[model.TabID]bool, fixture.state.TabCount())
	for index := 0; index < fixture.state.TabCount(); index++ {
		id, ok := fixture.state.TabID(index)
		if !ok || id == "" {
			t.Fatalf("tab %d has no id", index)
		}
		if seen[id] {
			t.Fatalf("duplicate runtime tab id %q in %v", id, fixture.state.TabNumbers())
		}
		seen[id] = true
	}
	if got, _ := fixture.state.TabID(1); got != twoTab {
		t.Fatalf("leftover Tab id = %q, want %q", got, twoTab)
	}
	if got, _ := fixture.state.TabID(2); got != oneTab {
		t.Fatalf("moved View Tab id = %q, want %q", got, oneTab)
	}
	for _, want := range []*appview.Instance{one, two} {
		index := tabIndexByID(t, fixture.state, model.TabID("view:"+want.ID()))
		info, ok := fixture.state.TabInfo(index)
		if !ok || info.ViewInstanceID != want.ID() {
			t.Fatalf("tab %d shows View %q, want %q", index, info.ViewInstanceID, want.ID())
		}
	}
	// MoveTab can resolve both ids without landing on the same Tab.
	if !fixture.state.MoveTab(oneTab, twoTab, true) {
		t.Fatal("MoveTab could not resolve the runtime View Tab ids")
	}
	if got, _ := fixture.state.TabID(2); got != oneTab {
		t.Fatalf("tab order after MoveTab = %v, want the moved View Tab last", fixture.state.TabNumbers())
	}

	payload := domainworkspace.SanitizePayload(fixture.state.WorkspacePayload())
	if len(payload.Tabs) != 3 {
		t.Fatalf("saved tabs = %d, want both View Tabs and the Session Tab", len(payload.Tabs))
	}
	placements := make(map[string]domainworkspace.ViewPlacement)
	for _, pane := range payload.Panes {
		if pane.View != nil && pane.View.ViewID == counterViewID {
			placements[pane.View.ContextKey] = pane.View.Placement
		}
	}
	if len(placements) != 2 ||
		placements["one"] != domainworkspace.ViewPlacementTab ||
		placements["two"] != domainworkspace.ViewPlacementTab {
		t.Fatalf("saved View placements = %v, want both View Tabs", placements)
	}

	// A restart rebuilds both View Tabs; neither is dropped as a duplicate.
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
	restored := NewState(
		WithViews(registry, func(*appview.Instance) (Surface, error) {
			return &fakeViewSurface{}, nil
		}),
		WithWorkspace(appworkspace.NewService(&memoryWorkspaceRepository{payload: payload}), nil),
	)
	restored.Restore(context.Background())
	if restored.TabCount() != 3 {
		t.Fatalf("restored tabs = %d, want both View Tabs and the Session Tab", restored.TabCount())
	}
	viewTabs := 0
	for index := 0; index < restored.TabCount(); index++ {
		info, ok := restored.TabInfo(index)
		if !ok || info.ViewInstanceID == "" {
			continue
		}
		viewTabs++
		if id, _ := restored.TabID(index); id != model.TabID("view:"+info.ViewInstanceID) {
			t.Fatalf("restored View Tab id = %q, want view:%s", id, info.ViewInstanceID)
		}
	}
	if viewTabs != 2 {
		t.Fatalf("restored View Tabs = %d, want 2", viewTabs)
	}
}

// TestViewPanesCannotUseSessionCommands proves View Panes stay out of splits
// and broadcast input.
func TestViewPanesCannotUseSessionCommands(t *testing.T) {
	fixture := newPlacementFixture(t, domainplugin.InstanceSingleton)
	service := appsession.NewService(&workspaceFactory{})
	t.Cleanup(service.CloseAll)
	fixture.state = NewState(
		WithSessions(service, func(*appsession.Session) (Surface, error) {
			return &fakeSurface{}, nil
		}),
		WithViews(fixture.registry, func(*appview.Instance) (Surface, error) {
			fixture.factory++
			surface := &fakeViewSurface{}
			fixture.surfaces = append(fixture.surfaces, surface)
			return surface, nil
		}),
	)
	if _, err := fixture.state.OpenView(appview.OpenRequest{ViewID: counterViewID}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	if err := fixture.state.SplitRight(); err == nil {
		t.Fatal("a View Pane accepted Split Right")
	}
	if id, ok := fixture.state.FocusedSessionID(); ok || id != "" {
		t.Fatalf("View Pane exposed Session %q", id)
	}
	if sessions := fixture.state.BroadcastSessions(); len(sessions) != 0 {
		t.Fatalf("View Pane joined broadcast: %+v", sessions)
	}
	if targets := fixture.state.BroadcastTargets(); len(targets) != 0 {
		t.Fatalf("View Pane became a broadcast target: %v", targets)
	}
	if ids, broadcast := fixture.state.ComposeTargets(); len(ids) != 0 || broadcast {
		t.Fatalf("compose targets = %v broadcast=%v", ids, broadcast)
	}
}

// TestT14DetachedMixedTabReidentifiesTheViewItStillShows reproduces the stale
// canonical id in a detached window: two View Tabs are mixed, detached, and one
// View leaves. The leftover detached Tab must be named after the View it still
// shows before the moved instance's new Tab claims view:<instanceId>, so neither
// Tab is misnamed and MoveTab/ReattachDetachedTab stay unambiguous.
func TestT14DetachedMixedTabReidentifiesTheViewItStillShows(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	for _, contextKey := range []string{"first", "second"} {
		if _, err := fixture.state.OpenView(appview.OpenRequest{
			ViewID: counterViewID, ContextKey: contextKey, Placement: domainplugin.PlacementTab,
		}); err != nil {
			t.Fatalf("OpenView %q: %v", contextKey, err)
		}
	}
	first := fixture.instanceWithContext(t, "first")
	second := fixture.instanceWithContext(t, "second")
	firstTab := model.TabID("view:" + first.ID())
	secondTab := model.TabID("view:" + second.ID())
	firstIndex := tabIndexByID(t, fixture.state, firstTab)
	secondIndex := tabIndexByID(t, fixture.state, secondTab)
	firstPane := panesOf(t, fixture.state, firstIndex)[0]
	secondPane := panesOf(t, fixture.state, secondIndex)[0]
	if !fixture.state.MovePane(secondTab, secondPane, firstTab, firstPane, domainworkspace.SideRight) {
		t.Fatal("MovePane into the other View Tab returned false")
	}
	if got, _ := fixture.state.TabID(0); got != firstTab {
		t.Fatalf("mixed Tab id = %q, want %q", got, firstTab)
	}
	if !fixture.state.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	detached := fixture.state.DetachedTabIDs()
	if len(detached) != 1 || detached[0] != firstTab {
		t.Fatalf("detached tabs = %v, want %q", detached, firstTab)
	}

	if err := fixture.state.MoveView(first.ID(), domainplugin.PlacementTab); err != nil {
		t.Fatalf("MoveView out of the detached mixed Tab: %v", err)
	}
	detached = fixture.state.DetachedTabIDs()
	if len(detached) != 1 || detached[0] != secondTab {
		t.Fatalf("leftover detached tab id = %v, want %q", detached, secondTab)
	}
	if fixture.state.DetachedTabExists(firstTab) {
		t.Fatalf("detached window kept the stale id %q", firstTab)
	}
	if info, ok := fixture.state.DetachedTabInfo(secondTab); !ok || len(info.Panes) != 1 {
		t.Fatalf("leftover detached tab = %+v ok=%v, want only the second View", info, ok)
	}
	if second.Placement() != domainplugin.PlacementWindow {
		t.Fatalf("leftover View placement = %s, want window", second.Placement())
	}
	if fixture.state.TabCount() != 1 {
		t.Fatalf("main tabs = %d, want the moved View Tab", fixture.state.TabCount())
	}
	movedTab, _ := fixture.state.TabID(0)
	if movedTab != firstTab {
		t.Fatalf("moved View Tab id = %q, want %q (a stale detached id forced a suffix)", movedTab, firstTab)
	}
	if info, ok := fixture.state.TabInfo(0); !ok || info.ViewInstanceID != first.ID() {
		t.Fatalf("main Tab shows %q, want %q", info.ViewInstanceID, first.ID())
	}

	// Both ids now point at the View they name, so reattach and reorder address
	// the right Tab.
	if !fixture.state.ReattachDetachedTab(secondTab) {
		t.Fatalf("ReattachDetachedTab(%q) returned false", secondTab)
	}
	if !fixture.state.MoveTab(secondTab, firstTab, true) {
		t.Fatal("MoveTab could not resolve the reattached Tab ids")
	}
	if got, _ := fixture.state.TabID(0); got != firstTab {
		t.Fatalf("tab 0 = %q after moving %q last, want %q", got, secondTab, firstTab)
	}
	if got, _ := fixture.state.TabID(1); got != secondTab {
		t.Fatalf("tab 1 = %q, want %q", got, secondTab)
	}
}

// TestT14CloseFocusedPaneRenamesTheLeftoverViewTab covers closing a View that
// names a mixed Tab: the remaining View becomes the whole Tab, so it records the
// Tab placement and takes the canonical id instead of keeping view:<closed>.
func TestT14CloseFocusedPaneRenamesTheLeftoverViewTab(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	for _, contextKey := range []string{"first", "second"} {
		if _, err := fixture.state.OpenView(appview.OpenRequest{
			ViewID: counterViewID, ContextKey: contextKey, Placement: domainplugin.PlacementTab,
		}); err != nil {
			t.Fatalf("OpenView %q: %v", contextKey, err)
		}
	}
	first := fixture.instanceWithContext(t, "first")
	second := fixture.instanceWithContext(t, "second")
	firstTab := model.TabID("view:" + first.ID())
	secondTab := model.TabID("view:" + second.ID())
	firstPane := panesOf(t, fixture.state, tabIndexByID(t, fixture.state, firstTab))[0]
	secondPane := panesOf(t, fixture.state, tabIndexByID(t, fixture.state, secondTab))[0]
	if !fixture.state.MovePane(secondTab, secondPane, firstTab, firstPane, domainworkspace.SideRight) {
		t.Fatal("MovePane into the other View Tab returned false")
	}

	if !fixture.state.FocusPaneID(0, firstPane) {
		t.Fatal("FocusPaneID returned false")
	}
	if !fixture.state.CloseFocusedPane() {
		t.Fatal("CloseFocusedPane returned false")
	}
	if live := fixture.registry.Instances(); len(live) != 1 || live[0] != second {
		t.Fatalf("live instances = %d, want only the second View", len(live))
	}
	if fixture.state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want the leftover View Tab", fixture.state.TabCount())
	}
	if got, _ := fixture.state.TabID(0); got != secondTab {
		t.Fatalf("leftover Tab id = %q, want %q", got, secondTab)
	}
	if second.Placement() != domainplugin.PlacementTab {
		t.Fatalf("leftover placement = %s, want tab", second.Placement())
	}
	if info := viewPaneInfoAt(t, fixture.state, 0); info.ViewPlacement != domainplugin.PlacementTab {
		t.Fatalf("Pane menu placement = %s, want tab", info.ViewPlacement)
	}
	assertSavedViewPlacement(t, fixture.state, second.ViewID(), second.ContextKey(), domainworkspace.ViewPlacementTab)
}

// TestT14CloseFocusedPaneIdentifiesTheViewLeftInASessionTab covers closing the
// Session Pane of a Tab that a View was dragged into: the View is alone now, so
// the Tab takes view:<instanceId> instead of keeping the Session's runtime id.
func TestT14CloseFocusedPaneIdentifiesTheViewLeftInASessionTab(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	_, instance, _, sessionPane := fixture.openMixedTab(t, "global")
	if got, _ := fixture.state.TabID(0); strings.HasPrefix(string(got), "view:") {
		t.Fatalf("mixed Session Tab id = %q, want a runtime id before the Session closes", got)
	}
	if !fixture.state.FocusPaneID(0, sessionPane) {
		t.Fatal("FocusPaneID returned false")
	}
	if !fixture.state.CloseFocusedPane() {
		t.Fatal("CloseFocusedPane returned false")
	}
	want := model.TabID("view:" + instance.ID())
	if got, _ := fixture.state.TabID(0); got != want {
		t.Fatalf("leftover Tab id = %q, want %q", got, want)
	}
	if instance.Placement() != domainplugin.PlacementTab {
		t.Fatalf("leftover placement = %s, want tab", instance.Placement())
	}
	if info := viewPaneInfoAt(t, fixture.state, 0); info.ViewPlacement != domainplugin.PlacementTab {
		t.Fatalf("Pane menu placement = %s, want tab", info.ViewPlacement)
	}
	assertSavedViewPlacement(t, fixture.state, instance.ViewID(), instance.ContextKey(), domainworkspace.ViewPlacementTab)
}

// TestT14DetachPaneIdentifiesTheViewLeftInASessionTab covers detaching the
// Session Pane from a mixed Tab: the View left behind is the whole work Tab, so
// it records Tab and takes the canonical id.
func TestT14DetachPaneIdentifiesTheViewLeftInASessionTab(t *testing.T) {
	fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
	_, instance, _, sessionPane := fixture.openMixedTab(t, "global")
	if !fixture.state.DetachPane(0, sessionPane) {
		t.Fatal("DetachPane returned false")
	}
	if fixture.state.TabCount() != 1 {
		t.Fatalf("main tabs = %d, want the leftover View Tab", fixture.state.TabCount())
	}
	want := model.TabID("view:" + instance.ID())
	if got, _ := fixture.state.TabID(0); got != want {
		t.Fatalf("leftover Tab id = %q, want %q", got, want)
	}
	if instance.Placement() != domainplugin.PlacementTab {
		t.Fatalf("leftover placement = %s, want tab", instance.Placement())
	}
	if info := viewPaneInfoAt(t, fixture.state, 0); info.ViewPlacement != domainplugin.PlacementTab {
		t.Fatalf("Pane menu placement = %s, want tab", info.ViewPlacement)
	}
	assertSavedViewPlacement(t, fixture.state, instance.ViewID(), instance.ContextKey(), domainworkspace.ViewPlacementTab)
	if detached := fixture.state.DetachedTabIDs(); len(detached) != 1 {
		t.Fatalf("detached tabs = %v, want the moved Session Pane", detached)
	}
}

// TestT14TmuxDetachIdentifiesTheViewLeftInASessionTab reproduces the background
// removal path: a tmux detach closes the Session Pane of a mixed Tab, leaving
// the View alone. The Tab must record Tab and take view:<instanceId> without an
// explicit user close.
func TestT14TmuxDetachIdentifiesTheViewLeftInASessionTab(t *testing.T) {
	factory := &endingFactory{end: appsession.End{
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
	if err := st.OpenHost(domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH, AutoAttachTmux: true,
	}); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the Host Tab", func() bool {
		st.Flush()
		return st.TabCount() == 1 && st.ActiveSurface() != nil
	})
	hostTab, _ := st.TabID(0)
	hostPane := panesOf(t, st, 0)[0]
	if _, err := st.OpenView(appview.OpenRequest{
		ViewID: counterViewID, ContextKey: "global", Placement: domainplugin.PlacementTab,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instance := registry.Instances()[0]
	viewTab, _ := st.TabID(1)
	viewPane := panesOf(t, st, 1)[0]
	if !st.MovePane(viewTab, viewPane, hostTab, hostPane, domainworkspace.SideRight) {
		t.Fatal("MovePane into the Host Tab returned false")
	}
	if got, _ := st.TabID(0); got != hostTab || strings.HasPrefix(string(got), "view:") {
		t.Fatalf("mixed Host Tab id = %q, want the Session Tab id %q", got, hostTab)
	}
	if instance.Placement() != domainplugin.PlacementPane {
		t.Fatalf("placement in the split = %s, want pane", instance.Placement())
	}

	if err := factory.last().Close(); err != nil {
		t.Fatalf("close the detached transport: %v", err)
	}
	waitFor(t, "the detached Session Pane to close", func() bool {
		st.Flush()
		root, _, ok := st.TabTree(0)
		return ok && len(domainworkspace.CollectPaneIDs(root)) == 1
	})
	want := model.TabID("view:" + instance.ID())
	if got, _ := st.TabID(0); got != want {
		t.Fatalf("leftover Tab id = %q, want %q", got, want)
	}
	if instance.Placement() != domainplugin.PlacementTab {
		t.Fatalf("leftover placement = %s, want tab", instance.Placement())
	}
	if info := viewPaneInfoAt(t, st, 0); info.ViewPlacement != domainplugin.PlacementTab {
		t.Fatalf("Pane menu placement = %s, want tab", info.ViewPlacement)
	}
	assertSavedViewPlacement(t, st, instance.ViewID(), instance.ContextKey(), domainworkspace.ViewPlacementTab)
}

// assertSavedViewPlacement checks the restore descriptor of a work-Tab View.
func assertSavedViewPlacement(t *testing.T, st *State, viewID, contextKey string,
	want domainworkspace.ViewPlacement) {
	t.Helper()
	for _, pane := range st.WorkspacePayload().Panes {
		if pane.View == nil || pane.View.ViewID != viewID || pane.View.ContextKey != contextKey {
			continue
		}
		if pane.View.Placement != want {
			t.Fatalf("saved View placement = %s, want %s", pane.View.Placement, want)
		}
		return
	}
	t.Fatalf("saved payload has no View %q with context %q", viewID, contextKey)
}

// TestT14ViewTabIdentityInvariantSurvivesRandomMutations exercises the invariants
// through deterministic random sequences of mixed drags, closes, detaches, and
// moves. After every operation it checks the identity rules; at the end it
// save/restores the Workspace and checks the same rules plus placement and
// context preservation.
func TestT14ViewTabIdentityInvariantSurvivesRandomMutations(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			fixture := newMixedPlacementFixture(t, domainplugin.InstanceSingleton)
			rng := rand.New(rand.NewSource(seed))
			nextContext := 0
			for step := 0; step < 120; step++ {
				applyRandomViewMutation(t, fixture, rng, &nextContext)
				assertViewTabIdentityInvariant(t, fixture.state, fixture.registry)
			}
			assertWorkspaceRoundTrip(t, fixture)
		})
	}
}

// applyRandomViewMutation performs one valid-or-refused layout operation. The
// caller checks the invariants after every call, so a refused operation is fine.
func applyRandomViewMutation(t *testing.T, fixture *mixedPlacementFixture,
	rng *rand.Rand, nextContext *int) {
	t.Helper()
	st := fixture.state
	openView := func(placement domainplugin.Placement) {
		*nextContext++
		if _, err := st.OpenView(appview.OpenRequest{
			ViewID: counterViewID, ContextKey: fmt.Sprintf("ctx-%d", *nextContext),
			Placement: placement,
		}); err != nil {
			t.Fatalf("OpenView: %v", err)
		}
	}
	switch rng.Intn(12) {
	case 0, 1:
		placement := domainplugin.PlacementPane
		if rng.Intn(2) == 0 {
			placement = domainplugin.PlacementTab
		}
		openView(placement)
	case 2:
		if st.TabCount() < 6 {
			if err := st.NewTab(); err != nil {
				t.Fatalf("NewTab: %v", err)
			}
		}
	case 3, 4:
		moveRandomPane(t, st, rng)
	case 5:
		moveRandomPaneToTab(t, st, rng)
	case 6:
		if st.TabCount() > 0 {
			index := rng.Intn(st.TabCount())
			panes := panesOf(t, st, index)
			if st.FocusPaneID(index, panes[rng.Intn(len(panes))]) {
				st.CloseFocusedPane()
			}
		}
	case 7:
		if st.TabCount() > 0 {
			index := rng.Intn(st.TabCount())
			panes := panesOf(t, st, index)
			st.DetachPane(index, panes[rng.Intn(len(panes))])
		}
	case 8:
		if st.TabCount() > 0 {
			st.DetachTab(rng.Intn(st.TabCount()))
		}
	case 9:
		if ids := st.DetachedTabIDs(); len(ids) > 0 {
			st.ReattachDetachedTab(ids[rng.Intn(len(ids))])
		}
	case 10:
		if st.TabCount() > 1 {
			source, target := randomTabPair(st, rng)
			st.MoveTab(source, target, rng.Intn(2) == 0)
		}
	case 11:
		if instances := sortedInstances(fixture.registry); len(instances) > 0 {
			instance := instances[rng.Intn(len(instances))]
			placement := allPlacements[rng.Intn(len(allPlacements))]
			_ = st.MoveView(instance.ID(), placement)
		}
	}
	if st.TabCount() > 8 {
		st.CloseTab(rng.Intn(st.TabCount()))
	}
	if ids := st.DetachedTabIDs(); len(ids) > 4 {
		st.CloseDetachedTab(ids[0])
	}
	if st.TabCount() == 0 && len(st.DetachedTabIDs()) == 0 {
		openView(domainplugin.PlacementTab)
	}
}

// sortedInstances gives the randomized test a deterministic instance order;
// Registry.Instances walks a map.
func sortedInstances(registry *appview.Registry) []*appview.Instance {
	instances := registry.Instances()
	sort.Slice(instances, func(i, j int) bool { return instances[i].ID() < instances[j].ID() })
	return instances
}

func randomTabPair(st *State, rng *rand.Rand) (model.TabID, model.TabID) {
	first := rng.Intn(st.TabCount())
	second := rng.Intn(st.TabCount())
	if first == second {
		second = (second + 1) % st.TabCount()
	}
	source, _ := st.TabID(first)
	target, _ := st.TabID(second)
	return source, target
}

func moveRandomPane(t *testing.T, st *State, rng *rand.Rand) {
	t.Helper()
	if st.TabCount() < 2 {
		return
	}
	sourceID, targetID := randomTabPair(st, rng)
	source := tabIndexByID(t, st, sourceID)
	target := tabIndexByID(t, st, targetID)
	sourcePanes := panesOf(t, st, source)
	targetPanes := panesOf(t, st, target)
	if len(sourcePanes) == 0 || len(targetPanes) == 0 {
		return
	}
	side := domainworkspace.SideRight
	if rng.Intn(2) == 0 {
		side = domainworkspace.SideLeft
	}
	st.MovePane(sourceID, sourcePanes[rng.Intn(len(sourcePanes))],
		targetID, targetPanes[rng.Intn(len(targetPanes))], side)
}

func moveRandomPaneToTab(t *testing.T, st *State, rng *rand.Rand) {
	t.Helper()
	if st.TabCount() < 2 {
		return
	}
	sourceID, targetID := randomTabPair(st, rng)
	sourcePanes := panesOf(t, st, tabIndexByID(t, st, sourceID))
	if len(sourcePanes) == 0 {
		return
	}
	st.MovePaneToTab(sourceID, sourcePanes[rng.Intn(len(sourcePanes))], targetID)
}

// assertViewTabIdentityInvariant checks the AC3 rules after one mutation:
// unique ids across main and detached Tabs, canonical ids for single-View work
// Tabs, no view: id on a Session-only Tab, a view: id that names a View the Tab
// shows, one container per live View with a matching placement kind, and
// descriptor/registry agreement.
func assertViewTabIdentityInvariant(t *testing.T, st *State, registry *appview.Registry) {
	t.Helper()
	seen := make(map[model.TabID]string, len(st.tabs)+len(st.detached))
	for index, tab := range st.tabs {
		assertTabViewIdentity(t, tab, fmt.Sprintf("main tab %d", index), seen)
	}
	for index, tab := range st.detached {
		assertTabViewIdentity(t, tab, fmt.Sprintf("detached tab %d", index), seen)
	}

	for _, instance := range registry.Instances() {
		containers := 0
		kind := ""
		for _, tab := range st.tabs {
			if _, _, ok := tabShowingView(tab, instance.ID()); ok {
				containers++
				kind = "work tab"
			}
		}
		for _, tab := range st.detached {
			if _, _, ok := tabShowingView(tab, instance.ID()); ok {
				containers++
				kind = "detached tab"
			}
		}
		for _, entry := range st.pages {
			if entry != nil && entry.instance == instance {
				containers++
				kind = "page"
			}
		}
		for _, entry := range st.asides {
			if entry != nil && entry.instance == instance {
				containers++
				kind = "aside"
			}
		}
		if containers != 1 {
			t.Fatalf("View %s (context %q, placement %s) is in %d containers, want 1",
				instance.ID(), instance.ContextKey(), instance.Placement(), containers)
		}
		switch kind {
		case "work tab":
			if placement := instance.Placement(); placement != domainplugin.PlacementPane &&
				placement != domainplugin.PlacementTab {
				t.Fatalf("View %s in a work Tab records %s, want pane or tab",
					instance.ID(), placement)
			}
		case "detached tab":
			if instance.Placement() != domainplugin.PlacementWindow {
				t.Fatalf("View %s in a detached Tab records %s, want window",
					instance.ID(), instance.Placement())
			}
		case "page":
			if instance.Placement() != domainplugin.PlacementPage {
				t.Fatalf("View %s in a Page records %s, want page",
					instance.ID(), instance.Placement())
			}
		case "aside":
			if instance.Placement() != domainplugin.PlacementAside {
				t.Fatalf("View %s in an aside records %s, want aside",
					instance.ID(), instance.Placement())
			}
		}
	}
	for viewID, entry := range st.pages {
		if entry == nil || entry.instance == nil || entry.instance.Closed() {
			continue
		}
		if entry.descriptor.Placement != workspacePlacement(entry.instance.Placement()) {
			t.Fatalf("page %q descriptor = %s, registry = %s", viewID,
				entry.descriptor.Placement, entry.instance.Placement())
		}
	}
	for instanceID, entry := range st.asides {
		if entry == nil || entry.instance == nil || entry.instance.Closed() {
			continue
		}
		if entry.descriptor.Placement != workspacePlacement(entry.instance.Placement()) {
			t.Fatalf("aside %q descriptor = %s, registry = %s", instanceID,
				entry.descriptor.Placement, entry.instance.Placement())
		}
	}
}

func assertTabViewIdentity(t *testing.T, tab *Tab, where string, seen map[model.TabID]string) {
	t.Helper()
	if tab.ID == "" {
		t.Fatalf("%s has an empty id", where)
	}
	if previous, ok := seen[tab.ID]; ok {
		t.Fatalf("duplicate tab id %q in %s and %s", tab.ID, previous, where)
	}
	seen[tab.ID] = where
	instances := tabViewInstances(tab)
	if single := onlyViewInstance(tab); single != nil {
		if want := viewTabID(single); tab.ID != want {
			t.Fatalf("%s shows only View %s but id = %q, want %q",
				where, single.ID(), tab.ID, want)
		}
	}
	if len(instances) == 0 && strings.HasPrefix(string(tab.ID), "view:") {
		t.Fatalf("%s has no live View but keeps the view: id %q", where, tab.ID)
	}
	if strings.HasPrefix(string(tab.ID), "view:") && !tabIDNamesView(tab.ID, instances) {
		shown := make([]string, 0, len(instances))
		for _, instance := range instances {
			shown = append(shown, instance.ID())
		}
		panes := make([]string, 0, len(tab.panes))
		for paneID, value := range tab.panes {
			instanceID := ""
			if content := value.viewContent(); content != nil && content.instance != nil {
				instanceID = content.instance.ID()
			}
			panes = append(panes, fmt.Sprintf("%s=%q", paneID, instanceID))
		}
		t.Fatalf("%s id %q names no View it shows (live instances %v, panes %v)",
			where, tab.ID, shown, panes)
	}
	for _, paneID := range tab.paneIDs() {
		value := tab.panes[paneID]
		if value == nil {
			continue
		}
		content := value.viewContent()
		if content == nil || content.instance == nil || content.instance.Closed() {
			continue
		}
		want := workspacePlacement(content.instance.Placement())
		if content.descriptor.Placement != want {
			t.Fatalf("%s View %s descriptor = %s, registry = %s",
				where, content.instance.ID(), content.descriptor.Placement, want)
		}
	}
}

// assertWorkspaceRoundTrip checks the payload keeps every main Tab and every
// live View, then restores it and checks identity, placement, and context.
func assertWorkspaceRoundTrip(t *testing.T, fixture *mixedPlacementFixture) {
	t.Helper()
	st := fixture.state
	payload := st.WorkspacePayload()
	if len(payload.Tabs) != st.TabCount() {
		t.Fatalf("payload tabs = %d, want %d", len(payload.Tabs), st.TabCount())
	}
	for index := 0; index < st.TabCount(); index++ {
		id, _ := st.TabID(index)
		if payload.Tabs[index].ID != id {
			t.Fatalf("payload tab %d id = %q, want %q", index, payload.Tabs[index].ID, id)
		}
	}
	want := make(map[string]int)
	for _, instance := range fixture.registry.Instances() {
		want[viewPlacementKey(instance.ViewID(), instance.ContextKey(), instance.Placement())]++
	}
	got := make(map[string]int)
	for _, pane := range payload.Panes {
		if pane.View == nil {
			continue
		}
		got[viewPlacementKey(pane.View.ViewID, pane.View.ContextKey, pluginPlacement(pane.View.Placement))]++
	}
	for _, view := range payload.Views {
		got[viewPlacementKey(view.ViewID, view.ContextKey, pluginPlacement(view.Placement))]++
	}
	if len(got) != len(want) {
		t.Fatalf("saved View descriptors = %v, want %v", got, want)
	}
	for key, count := range want {
		if got[key] != count {
			t.Fatalf("saved View descriptors = %v, want %v", got, want)
		}
	}

	clean := domainworkspace.SanitizePayload(payload)
	if len(clean.Tabs) != len(payload.Tabs) {
		t.Fatalf("sanitize dropped tabs: %d -> %d", len(payload.Tabs), len(clean.Tabs))
	}
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
	restored := NewState(
		WithViews(registry, func(*appview.Instance) (Surface, error) {
			return &fakeViewSurface{}, nil
		}),
		WithWorkspace(appworkspace.NewService(&memoryWorkspaceRepository{payload: clean}), nil),
	)
	restored.Restore(context.Background())
	if restored.TabCount() != len(clean.Tabs) {
		t.Fatalf("restored tabs = %d, want %d", restored.TabCount(), len(clean.Tabs))
	}
	assertViewTabIdentityInvariant(t, restored, registry)
	restoredViews := make(map[string]int)
	for _, instance := range registry.Instances() {
		restoredViews[viewPlacementKey(instance.ViewID(), instance.ContextKey(), instance.Placement())]++
	}
	if len(restoredViews) != len(want) {
		t.Fatalf("restored Views = %v, want %v", restoredViews, want)
	}
	for key, count := range want {
		if restoredViews[key] != count {
			t.Fatalf("restored Views = %v, want %v", restoredViews, want)
		}
	}
}

func viewPlacementKey(viewID, contextKey string, placement domainplugin.Placement) string {
	return viewID + "\x00" + contextKey + "\x00" + string(placement)
}
