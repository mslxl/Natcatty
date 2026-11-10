package view_test

import (
	"errors"
	"testing"

	appview "github.com/mslxl/natcatty/application/view"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// TestTitleKeyResolvesAtDisplayTime proves a locale change updates an open
// View Instance's title without reopening the contribution.
func TestTitleKeyResolvesAtDisplayTime(t *testing.T) {
	registry := appview.NewRegistry()
	t.Cleanup(registry.Close)
	translations := map[string]string{"view_title": "Contador"}
	registry.SetLocalizer(func(_ appview.Owner, key, literal string) string {
		if translated, ok := translations[key]; ok {
			return translated
		}
		return literal
	})
	if _, err := registry.Register(owner(), domainplugin.ViewContribution{
		ID: "counter", Title: "Counter", TitleKey: "view_title",
		Placements: []domainplugin.Placement{domainplugin.PlacementPane},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	instance, created, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", ContextKey: "host-1",
	})
	if err != nil || !created {
		t.Fatalf("Open = %+v created=%v err=%v", instance, created, err)
	}
	if got := instance.Title(); got != "Contador" {
		t.Fatalf("title = %q", got)
	}
	contributions := registry.Contributions()
	if len(contributions) != 1 || contributions[0].TitleKey != "view_title" ||
		contributions[0].Title != "Counter" {
		t.Fatalf("contribution = %+v", contributions)
	}

	// The host locale changed: the same open instance now reads the new
	// translation, and deleting the translation falls back to the literal.
	translations["view_title"] = "Counter (fr)"
	if got := instance.Title(); got != "Counter (fr)" {
		t.Fatalf("title after locale change = %q", got)
	}
	delete(translations, "view_title")
	if got := instance.Title(); got != "Counter" {
		t.Fatalf("literal fallback title = %q", got)
	}
}

func TestViewTitleWithoutLocalizerUsesTheLiteral(t *testing.T) {
	registry := appview.NewRegistry()
	t.Cleanup(registry.Close)
	if _, err := registry.Register(owner(), domainplugin.ViewContribution{
		ID: "bare", Title: "Bare", TitleKey: "view_bare",
		Placements: []domainplugin.Placement{domainplugin.PlacementPane},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	instance, _, err := registry.Open(appview.OpenRequest{ViewID: "plugin.acme.counter.bare"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := instance.Title(); got != "Bare" {
		t.Fatalf("title = %q", got)
	}
}

func owner() appview.Owner {
	return appview.Owner{
		PluginID:   "acme.counter",
		PluginName: "Counter",
		Version:    "1.0.0",
		Source:     domainplugin.SourceLocalFile,
	}
}

func declaration(id string, policy domainplugin.InstancePolicy, placements ...domainplugin.Placement) domainplugin.ViewContribution {
	if len(placements) == 0 {
		placements = []domainplugin.Placement{domainplugin.PlacementPane}
	}
	return domainplugin.ViewContribution{
		ID: id, Title: "Counter", Placements: placements, Instances: policy,
	}
}

func register(t *testing.T, registry *appview.Registry, view domainplugin.ViewContribution) appview.Disposer {
	t.Helper()
	disposer, err := registry.Register(owner(), view)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return disposer
}

func TestRegisterAndLookup(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("counter", domainplugin.InstanceSingleton))
	register(t, registry, declaration("history", domainplugin.InstanceMulti))

	contributions := registry.Contributions()
	if len(contributions) != 2 {
		t.Fatalf("contributions = %+v", contributions)
	}
	first := contributions[0]
	if first.ID != "plugin.acme.counter.counter" || first.LocalID != "counter" ||
		first.Title != "Counter" || first.Owner.PluginID != "acme.counter" {
		t.Fatalf("contribution = %+v", first)
	}
	if first.Instances != domainplugin.InstanceSingleton || !first.Allows(domainplugin.PlacementPane) {
		t.Fatalf("policy/placements = %+v", first)
	}
	if _, ok := registry.Contribution("plugin.acme.counter.history"); !ok {
		t.Fatal("history contribution was not registered")
	}
	if _, err := registry.Register(owner(), declaration("counter", "")); !errors.Is(err, appview.ErrDuplicateView) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestPaneSingletonReturnsTheSameInstance(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("counter", domainplugin.InstanceSingleton))

	first, created, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", ContextKey: "host-1", Placement: domainplugin.PlacementPane,
	})
	if err != nil || !created {
		t.Fatalf("Open first = %+v created=%v err=%v", first, created, err)
	}
	second, created, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", ContextKey: "host-1", Placement: domainplugin.PlacementPane,
	})
	if err != nil || created {
		t.Fatalf("Open second = %+v created=%v err=%v", second, created, err)
	}
	if second.ID() != first.ID() {
		t.Fatalf("singleton ids = %q and %q", first.ID(), second.ID())
	}
	other, created, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", ContextKey: "host-2", Placement: domainplugin.PlacementPane,
	})
	if err != nil || !created || other.ID() == first.ID() {
		t.Fatalf("context instance = %+v created=%v err=%v", other, created, err)
	}
	if first.ContextKey() != "host-1" || first.Placement() != domainplugin.PlacementPane {
		t.Fatalf("instance = %+v", first)
	}
	if first.Title() != "Counter" || first.ViewID() != "plugin.acme.counter.counter" {
		t.Fatalf("instance identity = %q %q", first.Title(), first.ViewID())
	}
}

func TestMultiInstanceCreatesPerOpen(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("gallery", domainplugin.InstanceMulti))
	request := appview.OpenRequest{
		ViewID: "plugin.acme.counter.gallery", ContextKey: "same", Placement: domainplugin.PlacementPane,
	}
	first, created, err := registry.Open(request)
	if err != nil || !created {
		t.Fatalf("first = %+v %v", first, err)
	}
	second, created, err := registry.Open(request)
	if err != nil || !created {
		t.Fatalf("second = %+v %v", second, err)
	}
	if first.ID() == second.ID() {
		t.Fatalf("multi instances share id %q", first.ID())
	}
}

func TestOpenRejectsUnknownAndUndeclaredPlacements(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("counter", "",
		domainplugin.PlacementPane, domainplugin.PlacementWindow))

	if _, _, err := registry.Open(appview.OpenRequest{ViewID: "missing"}); !errors.Is(err, appview.ErrUnknownView) {
		t.Fatalf("unknown view error = %v", err)
	}
	if _, _, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", Placement: domainplugin.PlacementPage,
	}); !errors.Is(err, appview.ErrPlacementDenied) {
		t.Fatalf("undeclared placement error = %v", err)
	}
	if _, _, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", Placement: "shell",
	}); !errors.Is(err, appview.ErrPlacementDenied) {
		t.Fatalf("unknown placement error = %v", err)
	}
	// Every declared placement is served now.
	instance, created, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", Placement: domainplugin.PlacementWindow,
	})
	if err != nil || !created || instance.Placement() != domainplugin.PlacementWindow {
		t.Fatalf("window placement = %+v created=%v err=%v", instance, created, err)
	}
}

// TestMovePreservesTheInstanceAcrossEveryPlacement proves a user move reuses
// the same identity, context, and state in every declared container.
func TestMovePreservesTheInstanceAcrossEveryPlacement(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("counter", domainplugin.InstanceSingleton,
		domainplugin.PlacementPane, domainplugin.PlacementTab, domainplugin.PlacementPage,
		domainplugin.PlacementWindow, domainplugin.PlacementAside))
	instance, _, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", ContextKey: "global", State: []byte("state"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, placement := range []domainplugin.Placement{
		domainplugin.PlacementTab, domainplugin.PlacementPage,
		domainplugin.PlacementWindow, domainplugin.PlacementAside,
		domainplugin.PlacementPane,
	} {
		moved, err := registry.Move(instance.ID(), placement)
		if err != nil {
			t.Fatalf("Move to %s: %v", placement, err)
		}
		if moved != instance || moved.Placement() != placement {
			t.Fatalf("Move to %s returned %+v", placement, moved)
		}
		if moved.ID() != instance.ID() || moved.ContextKey() != "global" ||
			string(moved.State()) != "state" {
			t.Fatalf("move to %s lost identity or state: %+v", placement, moved)
		}
	}

	// An undeclared target is atomic: the source is unchanged.
	if _, err := registry.Move(instance.ID(), "shell"); !errors.Is(err, appview.ErrPlacementDenied) {
		t.Fatalf("unknown target error = %v", err)
	}
	if instance.Placement() != domainplugin.PlacementPane {
		t.Fatalf("rejected move changed placement to %s", instance.Placement())
	}
}

func TestMoveRejectsAnUndeclaredPlacementAtomically(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("counter", domainplugin.InstanceSingleton,
		domainplugin.PlacementPane))
	instance, _, err := registry.Open(appview.OpenRequest{ViewID: "plugin.acme.counter.counter"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Move(instance.ID(), domainplugin.PlacementWindow); !errors.Is(err, appview.ErrPlacementDenied) {
		t.Fatalf("move error = %v", err)
	}
	if instance.Placement() != domainplugin.PlacementPane {
		t.Fatalf("placement after rejected move = %s", instance.Placement())
	}
}

// TestPageIsSingletonPerView covers both halves of the page rule: opening an
// existing page view moves the same instance, and a second context cannot
// steal the slot.
func TestPageIsSingletonPerView(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("counter", domainplugin.InstanceSingleton,
		domainplugin.PlacementPane, domainplugin.PlacementPage))
	first, created, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", ContextKey: "one",
	})
	if err != nil || !created {
		t.Fatalf("first open = %+v created=%v err=%v", first, created, err)
	}
	moved, created, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", ContextKey: "one",
		Placement: domainplugin.PlacementPage,
	})
	if err != nil || created || moved.ID() != first.ID() ||
		moved.Placement() != domainplugin.PlacementPage {
		t.Fatalf("page open = %+v created=%v err=%v", moved, created, err)
	}
	if page, ok := registry.PageInstance("plugin.acme.counter.counter"); !ok || page.ID() != first.ID() {
		t.Fatalf("page instance = %+v, ok=%v", page, ok)
	}

	other, created, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", ContextKey: "two",
	})
	if err != nil || !created {
		t.Fatalf("second context = %+v created=%v err=%v", other, created, err)
	}
	if _, err := registry.Move(other.ID(), domainplugin.PlacementPage); !errors.Is(err, appview.ErrPageOccupied) {
		t.Fatalf("occupied page move = %v", err)
	}
	if other.Placement() != domainplugin.PlacementPane {
		t.Fatalf("occupied page move changed the source to %s", other.Placement())
	}
	// Once the slot empties, another instance may enter it.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Move(other.ID(), domainplugin.PlacementPage); err != nil {
		t.Fatalf("move into the freed page: %v", err)
	}
}

func TestMultiInstanceCannotUsePageNorBeDuplicatedIntoIt(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("gallery", domainplugin.InstanceMulti,
		domainplugin.PlacementPane, domainplugin.PlacementPage, domainplugin.PlacementWindow))
	if _, _, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.gallery", Placement: domainplugin.PlacementPage,
	}); !errors.Is(err, appview.ErrPageRequiresSingleton) {
		t.Fatalf("multi page open = %v", err)
	}
	instance, _, err := registry.Open(appview.OpenRequest{ViewID: "plugin.acme.counter.gallery"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Move(instance.ID(), domainplugin.PlacementPage); !errors.Is(err, appview.ErrPageRequiresSingleton) {
		t.Fatalf("multi page move = %v", err)
	}
	if _, err := registry.Duplicate(instance.ID(), domainplugin.PlacementPage); !errors.Is(err, appview.ErrPageRequiresSingleton) {
		t.Fatalf("multi page duplicate = %v", err)
	}
}

func TestDuplicateIsMultiOnlyAndCopiesNoState(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("counter", domainplugin.InstanceSingleton))
	singleton, _, err := registry.Open(appview.OpenRequest{ViewID: "plugin.acme.counter.counter"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Duplicate(singleton.ID(), ""); !errors.Is(err, appview.ErrDuplicateSingleton) {
		t.Fatalf("singleton duplicate = %v", err)
	}

	register(t, registry, declaration("gallery", domainplugin.InstanceMulti,
		domainplugin.PlacementPane, domainplugin.PlacementWindow))
	source, _, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.gallery", ContextKey: "same", State: []byte("not copied"),
	})
	if err != nil {
		t.Fatal(err)
	}
	copyOf, err := registry.Duplicate(source.ID(), domainplugin.PlacementWindow)
	if err != nil {
		t.Fatalf("Duplicate: %v", err)
	}
	if copyOf.ID() == source.ID() || copyOf.ViewID() != source.ViewID() ||
		copyOf.ContextKey() != source.ContextKey() ||
		copyOf.Placement() != domainplugin.PlacementWindow {
		t.Fatalf("duplicate = %+v, source = %+v", copyOf, source)
	}
	if len(copyOf.State()) != 0 {
		t.Fatalf("duplicate copied state: %q", copyOf.State())
	}
	if copyOf.Descriptor().Placement != domainworkspace.ViewPlacementWindow {
		t.Fatalf("duplicate descriptor = %+v", copyOf.Descriptor())
	}
}

func TestInstanceStateIsBoundedAndCopied(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("counter", ""))
	instance, _, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", ContextKey: "ctx",
		State: []byte("seed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	state := instance.State()
	state[0] = 'X'
	if got := string(instance.State()); got != "seed" {
		t.Fatalf("state aliased caller buffer: %q", got)
	}
	if err := instance.SetState(make([]byte, domainworkspace.MaxViewStateBytes+1)); !errors.Is(err, appview.ErrStateTooLarge) {
		t.Fatalf("oversized state error = %v", err)
	}
	if err := instance.SetState(make([]byte, domainworkspace.MaxViewStateBytes)); err != nil {
		t.Fatalf("max-size state: %v", err)
	}
	descriptor := instance.Descriptor()
	if descriptor.ViewID != "plugin.acme.counter.counter" || descriptor.ContextKey != "ctx" ||
		len(descriptor.State) != domainworkspace.MaxViewStateBytes {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	if _, _, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter", ContextKey: "ctx2",
		State: make([]byte, domainworkspace.MaxViewStateBytes+1),
	}); !errors.Is(err, appview.ErrStateTooLarge) {
		t.Fatalf("oversized open state error = %v", err)
	}
}

func TestCloseAndUnregisterDisposeInstances(t *testing.T) {
	registry := appview.NewRegistry()
	disposer := register(t, registry, declaration("counter", ""))
	instance, _, err := registry.Open(appview.OpenRequest{ViewID: "plugin.acme.counter.counter"})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !instance.Closed() {
		t.Fatal("closed instance reports open")
	}
	if _, ok := registry.Instance(instance.ID()); ok {
		t.Fatal("closed instance is still registered")
	}
	if err := instance.SetState([]byte("x")); !errors.Is(err, appview.ErrInstanceClosed) {
		t.Fatalf("SetState after close = %v", err)
	}

	reopened, _, err := registry.Open(appview.OpenRequest{ViewID: "plugin.acme.counter.counter"})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID() == instance.ID() {
		t.Fatal("reopened instance reused a disposed id")
	}
	disposer()
	disposer() // idempotent
	if !reopened.Closed() {
		t.Fatal("unregister did not close the live instance")
	}
	if _, ok := registry.Contribution("plugin.acme.counter.counter"); ok {
		t.Fatal("unregister kept the contribution")
	}
	if _, _, err := registry.Open(appview.OpenRequest{
		ViewID: "plugin.acme.counter.counter",
	}); !errors.Is(err, appview.ErrUnknownView) {
		t.Fatalf("open after unregister = %v", err)
	}
}

func TestRegistryCloseRejectsLaterOpens(t *testing.T) {
	registry := appview.NewRegistry()
	register(t, registry, declaration("counter", ""))
	instance, _, err := registry.Open(appview.OpenRequest{ViewID: "plugin.acme.counter.counter"})
	if err != nil {
		t.Fatal(err)
	}
	registry.Close()
	if !instance.Closed() {
		t.Fatal("registry close did not close the instance")
	}
	if _, err := registry.Register(owner(), declaration("later", "")); !errors.Is(err, appview.ErrClosed) {
		t.Fatalf("register after close = %v", err)
	}
	if _, _, err := registry.Open(appview.OpenRequest{ViewID: "x"}); !errors.Is(err, appview.ErrClosed) {
		t.Fatalf("open after close = %v", err)
	}
}
