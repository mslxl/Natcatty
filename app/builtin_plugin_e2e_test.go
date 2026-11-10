package app

// The first-party sample e2e lives in the composition root because it drives
// the real app command registry, plugin surface, View registry, shell state,
// and native View renderer. The sample is a compiled-in Go package: it has no
// archive, no consent review, and no WASM runtime, but it uses the same
// manifest, contributions, fiber lifecycle, effect stack, and bounded unload
// as an installed plugin.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	adapterplugininstall "github.com/mslxl/natcatty/adapters/plugininstall"
	"github.com/mslxl/natcatty/application/command"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	appview "github.com/mslxl/natcatty/application/view"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/plugins/sample"
	"github.com/mslxl/natcatty/ui/shell/state"
)

func TestBuiltinSampleCommandAndViewEndToEnd(t *testing.T) {
	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	views := appview.NewRegistry()
	t.Cleanup(views.Close)
	commands := command.NewRegistry()
	surface := newPluginSurface(commands)
	surface.SetViewRegistry(views)
	feedback := &counterFeedback{}
	manager := appplugin.NewManager(nil, nil, surface, nil, store,
		appplugin.WithAppVersion("9.8.7"),
		appplugin.WithBuiltinStateStore(store),
		appplugin.WithManagerFeedback(feedback.add),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	if err := manager.RegisterBuiltin(ctx, appplugin.Builtin{
		Manifest: sample.Manifest,
		Setup:    sample.Setup,
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	list, err := manager.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("plugin list = %+v", list)
	}
	plugin := list[0]
	if plugin.ID != sample.Manifest.ID || plugin.Key != "built-in/"+sample.Manifest.ID {
		t.Fatalf("built-in identity = %+v", plugin)
	}
	if plugin.Source != domainplugin.SourceBuiltIn ||
		plugin.Version != "9.8.7" ||
		plugin.Status != appplugin.StatusActive {
		t.Fatalf("built-in summary = %+v", plugin)
	}
	if len(plugin.Permissions) != 3 {
		t.Fatalf("built-in permission rows = %+v", plugin.Permissions)
	}
	for _, row := range plugin.Permissions {
		if !row.Granted || row.Pending {
			t.Fatalf("built-in permission is not shown as granted: %+v", row)
		}
	}
	if _, err := manager.Review(ctx, sample.Manifest.ID); !errors.Is(
		err, appplugin.ErrBuiltinImmutable) {
		t.Fatalf("Review error = %v, want ErrBuiltinImmutable", err)
	}

	qualifiedCommand := command.ID(
		domainplugin.QualifiedCommandID(sample.Manifest.ID, "increment"))
	if !commands.Has(qualifiedCommand) {
		t.Fatalf("sample command is missing: %v", commands.IDs())
	}
	if err := commands.Dispatch(ctx, qualifiedCommand); err != nil {
		t.Fatalf("dispatch increment: %v", err)
	}
	outputs := feedback.snapshot()
	if len(outputs) != 1 || outputs[0] != "sample Count: 1" {
		t.Fatalf("command feedback = %+v", outputs)
	}

	viewID := domainplugin.QualifiedViewID(sample.Manifest.ID, "counter")
	contributions := views.Contributions()
	if len(contributions) != 1 || contributions[0].ID != viewID ||
		contributions[0].Title != "Sample Counter" ||
		!contributions[0].Allows(domainplugin.PlacementPane) {
		t.Fatalf("view contributions = %+v", contributions)
	}

	shell := state.NewState(state.WithViews(views,
		func(instance *appview.Instance) (state.Surface, error) {
			return newPluginViewSurface(instance, func() *appplugin.Manager {
				return manager
			}, pluginViewHost{
				Poll: pluginViewPoll(func() *appplugin.Manager { return manager },
					instance.ViewID()),
			}, func() {})
		}))
	if _, err := shell.OpenView(appview.OpenRequest{
		ViewID: viewID, Placement: domainplugin.PlacementPane,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	paneID, ok := shell.FocusedPaneID(0)
	if !ok {
		t.Fatal("the View Tab has no focused Pane")
	}
	info, ok := shell.PaneInfoAt(0, paneID)
	if !ok || info.Kind != domainworkspace.PaneKindView {
		t.Fatalf("View Pane = %+v, ok = %v", info, ok)
	}
	if info.Surface == nil {
		t.Fatal("the View Pane has no surface")
	}
	tt := ui.NewTester(func(c *ui.Context) {
		info.Surface.(state.FocusedSurface).ViewFocused(c, true)
	}, 640, 480)
	tt.Frame()
	if !tt.HasText("Count: 1") || !tt.HasText("Increment") {
		t.Fatalf("initial view texts = %v", tt.Texts())
	}

	// A click is queued by the renderer, the next frame drives the sample's
	// Poll, and the sample commits the updated count through the same UISession
	// the renderer reads.
	if err := tt.Click("Increment"); err != nil {
		t.Fatalf("Click Increment: %v", err)
	}
	tt.Frame()
	if !tt.HasText("Count: 2") {
		t.Fatalf("after click texts = %v", tt.Texts())
	}
	if outputs := feedback.snapshot(); len(outputs) != 2 ||
		outputs[1] != "sample Count: 2" {
		t.Fatalf("click feedback = %+v", outputs)
	}
	if !shell.CloseTab(0) {
		t.Fatal("CloseTab reported false")
	}

	// Disable unloads through the same bounded path: the command and View
	// contributions disappear, the session closes, and the disabled state is
	// persisted by the real install store.
	if err := manager.Disable(ctx, sample.Manifest.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if commands.Has(qualifiedCommand) {
		t.Fatal("the sample command survived disable")
	}
	if contributions := views.Contributions(); len(contributions) != 0 {
		t.Fatalf("view contributions after disable = %+v", contributions)
	}
	enabled, known, err := store.BuiltinEnabled(ctx, sample.Manifest.ID)
	if err != nil || !known || enabled {
		t.Fatalf("persisted built-in state = %v %v %v", enabled, known, err)
	}
	plugin = findListed(t, manager, sample.Manifest.ID)
	if plugin.Status != appplugin.StatusDisabled || plugin.Enabled {
		t.Fatalf("disabled built-in summary = %+v", plugin)
	}

	// Enable is direct: it re-grants the shipped permissions, re-registers both
	// contributions, and a fresh instance starts from zero.
	if err := manager.Enable(ctx, sample.Manifest.ID, nil); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	plugin = findListed(t, manager, sample.Manifest.ID)
	if plugin.Status != appplugin.StatusActive || !plugin.Enabled {
		t.Fatalf("re-enabled built-in summary = %+v", plugin)
	}
	if !commands.Has(qualifiedCommand) || len(views.Contributions()) != 1 {
		t.Fatalf("contributions after enable: command=%v views=%+v",
			commands.Has(qualifiedCommand), views.Contributions())
	}
	if err := commands.Dispatch(ctx, qualifiedCommand); err != nil {
		t.Fatalf("dispatch after enable: %v", err)
	}
	outputs = feedback.snapshot()
	if len(outputs) != 3 || outputs[2] != "sample Count: 1" {
		t.Fatalf("feedback after enable = %+v", outputs)
	}

	// App quit follows the same bounded lifecycle: Close unloads the fiber,
	// disposes its effects, and returns well inside the global quit budget.
	start := time.Now()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Close took %v, want the bounded quit window", elapsed)
	}
	if commands.Has(qualifiedCommand) {
		t.Fatal("the sample command survived app quit")
	}
}

func findListed(t *testing.T, manager *appplugin.Manager,
	id string) appplugin.InstalledPlugin {
	t.Helper()
	list, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, plugin := range list {
		if plugin.ID == id {
			return plugin
		}
	}
	t.Fatalf("plugin %s is not listed: %+v", id, list)
	return appplugin.InstalledPlugin{}
}

func TestBuiltinSampleUnloadRunsItsEffects(t *testing.T) {
	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	var logs []string
	commands := command.NewRegistry()
	surface := newPluginSurface(commands)
	views := appview.NewRegistry()
	t.Cleanup(views.Close)
	surface.SetViewRegistry(views)
	manager := appplugin.NewManager(nil, nil, surface, nil, store,
		appplugin.WithManagerLogf(func(_ appplugin.PluginRef, _ appplugin.LogLevel,
			message string) {
			logs = append(logs, message)
		}),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.RegisterBuiltin(ctx, appplugin.Builtin{
		Manifest: sample.Manifest, Setup: sample.Setup,
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if err := manager.Disable(ctx, sample.Manifest.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "sample shutdown") ||
		!strings.Contains(joined, "sample disposed") {
		t.Fatalf("unload logs = %q", logs)
	}
}
