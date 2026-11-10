package app

// The counter View sample e2e lives in the composition root because it is the
// only layer allowed to import both MyGo (for the ui.Tester) and the adapters
// that host the real plugin Manager. It drives the committed WASM guest through
// the real archive validator, install store, consent review, container, wazero
// runtime, app plugin surface, and native View renderer.

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	adapterplugininstall "github.com/mslxl/natcatty/adapters/plugininstall"
	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	adapterpluginpackage "github.com/mslxl/natcatty/adapters/pluginpackage"
	adapterwasm "github.com/mslxl/natcatty/adapters/wasm"
	"github.com/mslxl/natcatty/application/command"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	appview "github.com/mslxl/natcatty/application/view"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/internal/testutil"
	"github.com/mslxl/natcatty/ui/shell/state"
)

const (
	counterSampleID        = "acme.counter-view"
	counterSampleViewID    = "plugin.acme.counter-view.counter"
	counterSampleViewNode  = 2
	counterSampleInputNode = 3
)

func counterViewArchive(t *testing.T) string {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "counter-view")
	manifest, err := os.ReadFile(filepath.Join(root, "plugin.toml"))
	if err != nil {
		t.Fatalf("read sample manifest: %v", err)
	}
	module, err := os.ReadFile(filepath.Join(root, "plugin.wasm"))
	if err != nil {
		t.Fatalf("read sample module: %v", err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "i18n", "en.toml"))
	if err != nil {
		t.Fatalf("read sample catalog: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), counterSampleID+".natplug")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	fixed := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"i18n/en.toml", catalog},
		{"plugin.toml", manifest},
		{"plugin.wasm", module},
	} {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetModTime(fixed)
		header.SetMode(0o644)
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create %s: %v", entry.name, err)
		}
		if _, err := part.Write(entry.data); err != nil {
			t.Fatalf("write %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}
	return archivePath
}

type counterFeedback struct {
	mu      sync.Mutex
	outputs []string
}

func (f *counterFeedback) add(_ appplugin.PluginRef, output string) {
	f.mu.Lock()
	f.outputs = append(f.outputs, output)
	f.mu.Unlock()
}

func (f *counterFeedback) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.outputs...)
}

type counterRun struct {
	manager  *appplugin.Manager
	surface  *pluginSurface
	commands *command.Registry
	feedback *counterFeedback
	views    *appview.Registry
	record   domainplugin.InstallRecord
}

// startCounterSample packages the committed sample and drives it through the
// real archive validator, install store, consent review, manager, container,
// and wazero runtime. grantNative decides whether the optional ui.native
// permission is consented to.
func startCounterSample(t *testing.T, grantNative bool) *counterRun {
	t.Helper()
	return startCounterSampleWithRegistry(t, grantNative, nil)
}

// startCounterSampleWithRegistry installs version 1.0.0 from a fixture
// registry when one is supplied, so update and rollback tests use the same
// real manager, container, and view renderer as the install path.
func startCounterSampleWithRegistry(t *testing.T, grantNative bool,
	registry appplugin.Registry) *counterRun {
	t.Helper()
	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	views := appview.NewRegistry()
	t.Cleanup(views.Close)
	commands := command.NewRegistry()
	surface := newPluginSurface(commands)
	surface.SetViewRegistry(views)
	feedback := &counterFeedback{}
	options := []appplugin.ManagerOption{
		appplugin.WithManagerFeedback(func(ref appplugin.PluginRef, output string) {
			feedback.add(ref, output)
		}),
	}
	if registry != nil {
		options = append(options, appplugin.WithRegistry(registry))
	}
	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		surface,
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		options...,
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	var record domainplugin.InstallRecord
	var err error
	if registry != nil {
		record, err = manager.InstallFromRegistry(ctx, counterSampleID, "1.0.0", nil)
	} else {
		record, err = manager.InstallFile(ctx, counterViewArchive(t))
	}
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if record.ID != counterSampleID {
		t.Fatalf("install record = %+v", record)
	}
	if registry == nil && record.Source != domainplugin.SourceLocalFile {
		t.Fatalf("install record = %+v", record)
	}
	review, err := manager.Review(ctx, counterSampleID)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	required := make(map[domainplugin.Permission]bool)
	for _, row := range review.Required {
		required[row.Permission] = true
	}
	if !required[domainplugin.PermissionViews] || !required[domainplugin.PermissionCommands] {
		t.Fatalf("required consent rows = %+v", review.Required)
	}
	optional := make(map[domainplugin.Permission]bool)
	for _, row := range review.Optional {
		optional[row.Permission] = true
	}
	if !optional[domainplugin.PermissionUINative] || len(optional) != 1 {
		t.Fatalf("optional consent rows = %+v", review.Optional)
	}

	decisions := []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionViews, Allow: true},
		{Permission: domainplugin.PermissionCommands, Allow: true},
	}
	if grantNative {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: domainplugin.PermissionUINative, Allow: true,
		})
	}
	if err := manager.Enable(ctx, counterSampleID, decisions); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	list, err := manager.List(ctx)
	if err != nil || len(list) != 1 || list[0].Status != appplugin.StatusActive {
		t.Fatalf("List after Enable = %+v, %v", list, err)
	}
	return &counterRun{
		manager:  manager,
		surface:  surface,
		commands: commands,
		feedback: feedback,
		views:    views,
		record:   record,
	}
}

// report dispatches the sample's contributed report command through the app
// command registry and returns the guest's visible output.
func (r *counterRun) report(t *testing.T) string {
	t.Helper()
	before := len(r.feedback.snapshot())
	qualified := command.ID(domainplugin.QualifiedCommandID(counterSampleID, "report"))
	if err := r.commands.Dispatch(context.Background(), qualified); err != nil {
		t.Fatalf("dispatch report: %v", err)
	}
	outputs := r.feedback.snapshot()
	if len(outputs) != before+1 {
		t.Fatalf("outputs after report = %v", outputs)
	}
	return outputs[len(outputs)-1]
}

func requireCounterReportLines(t *testing.T, report string, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(report, line) {
			t.Fatalf("report is missing %q:\n%s", line, report)
		}
	}
}

// openCounterShell opens the sample View in a Pane through the real shell
// state and the app's real renderer factory.
func openCounterShell(t *testing.T, run *counterRun) *state.State {
	t.Helper()
	shell := state.NewState(state.WithViews(run.views,
		func(instance *appview.Instance) (state.Surface, error) {
			return newPluginViewSurface(instance, func() *appplugin.Manager {
				return run.manager
			}, pluginViewHost{
				Poll: pluginViewPoll(func() *appplugin.Manager { return run.manager }, instance.ViewID()),
			}, func() {})
		}))
	return shell
}

func TestCounterViewSampleRendersUpdatesAndReleasesHandles(t *testing.T) {
	run := startCounterSample(t, true)
	contributions := run.views.Contributions()
	if len(contributions) != 1 {
		t.Fatalf("registered views = %+v", contributions)
	}
	contribution := contributions[0]
	if contribution.ID != counterSampleViewID || contribution.LocalID != "counter" ||
		!contribution.Allows(domainplugin.PlacementPane) ||
		contribution.Instances != domainplugin.InstanceSingleton {
		t.Fatalf("view contribution = %+v", contribution)
	}

	shell := openCounterShell(t, run)
	tabID, err := shell.OpenView(appview.OpenRequest{
		ViewID: counterSampleViewID, Placement: domainplugin.PlacementPane,
	})
	if err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	if tabID == "" || shell.TabCount() != 1 {
		t.Fatalf("tab = %q count = %d", tabID, shell.TabCount())
	}
	paneID, ok := shell.FocusedPaneID(0)
	if !ok {
		t.Fatal("the View Tab has no focused Pane")
	}
	info, ok := shell.PaneInfoAt(0, paneID)
	if !ok || info.Kind != domainworkspace.PaneKindView || info.Surface == nil {
		t.Fatalf("View Pane = %+v, ok = %v", info, ok)
	}
	focused := true
	tt := ui.NewTester(func(c *ui.Context) {
		info.Surface.(state.FocusedSurface).ViewFocused(c, focused)
	}, 640, 480)
	tt.Frame()
	if !tt.HasText("Count: 0") || !tt.HasText("Hello, stranger!") {
		t.Fatalf("initial view texts = %v", tt.Texts())
	}

	// Typing reaches the plugin as an input event. The renderer queues it;
	// the next frame drives natcatty_poll, the guest commits a set_text
	// patch, and the same frame reads the new snapshot.
	if err := tt.Click("Name"); err != nil {
		t.Fatalf("Click input: %v", err)
	}
	tt.Type("Ada")
	tt.Frame()
	if !tt.HasText("Hello, Ada!") {
		t.Fatalf("after typing texts = %v", tt.Texts())
	}

	// Clicking the button increments the counter through another patch.
	for i := 1; i <= 2; i++ {
		if err := tt.Click("Increment"); err != nil {
			t.Fatalf("Click Increment: %v", err)
		}
		tt.Frame()
		if want := fmt.Sprintf("Count: %d", i); !tt.HasText(want) {
			t.Fatalf("after click %d texts = %v", i, tt.Texts())
		}
	}

	uiSession, ok := run.manager.Container().NativeUI(appplugin.PluginRef{
		ID: counterSampleID, Source: run.record.Source,
	})
	if !ok {
		t.Fatal("the sample has no live UI session")
	}
	if uiSession.Revision() < 4 {
		t.Fatalf("revision = %d, want the initial batch plus both patches", uiSession.Revision())
	}
	report := run.report(t)
	requireCounterReportLines(t, report,
		"ui.apply status=ok", "ui.listen status=ok",
		"counter=2", "name=Ada",
		"handles.count=1 handles.input=1 handles.button=1",
	)

	// Losing the Pane's keyboard focus is what selecting another Tab does.
	// The retained state and the typed text survive; regaining focus
	// restores the node focus and further input keeps editing the same text.
	// Click the input first: the focus toggle restores the node that had the
	// focus last, and clicking the button moved it to the button.
	if err := tt.Click("Name"); err != nil {
		t.Fatalf("Click input: %v", err)
	}
	tt.Frame()
	focused = false
	tt.Frame()
	focused = true
	tt.Frame()
	tt.Frame()
	if !tt.HasText("Count: 2") || !tt.HasText("Hello, Ada!") {
		t.Fatalf("after refocus texts = %v", tt.Texts())
	}
	if active, _ := tt.TextInputActive(); !active {
		t.Fatalf("the input did not regain text focus after refocus")
	}
	tt.Type(" Lovelace")
	tt.Frame()
	if !tt.HasText("Hello, Ada Lovelace!") {
		t.Fatalf("after refocus typing texts = %v", tt.Texts())
	}

	// Closing the last Pane closes the surface, releases every node and
	// listener handle, and disposes the instance. The plugin stays active.
	if !shell.CloseTab(0) {
		t.Fatal("CloseTab reported false")
	}
	if tree := uiSession.Tree(); tree == nil || tree.Len() != 0 || uiSession.Revision() != 0 {
		t.Fatalf("after close tree = %+v", tree)
	}
	if listeners := uiSession.Listeners(); len(listeners) != 0 {
		t.Fatalf("after close listeners = %+v", listeners)
	}
	if _, live := uiSession.NodeHandle(counterSampleViewNode); live {
		t.Fatal("after close the count node still has a handle")
	}
	if instances := run.views.Instances(); len(instances) != 0 {
		t.Fatalf("after close instances = %d", len(instances))
	}
	fiber, ok := run.manager.Container().FiberByRef(appplugin.PluginRef{
		ID: counterSampleID, Source: run.record.Source,
	})
	if !ok || fiber == nil {
		t.Fatal("closing the view unloaded the plugin")
	}
}

func TestCounterViewSampleReportsDeniedNativeUI(t *testing.T) {
	run := startCounterSample(t, false)
	report := run.report(t)
	requireCounterReportLines(t, report,
		"ui.apply status=denied revision=0",
		"ui.listen status=not-run",
		"counter=0",
		"handles.count=0 handles.input=0 handles.button=0",
	)
	uiSession, ok := run.manager.Container().NativeUI(appplugin.PluginRef{
		ID: counterSampleID, Source: run.record.Source,
	})
	if !ok {
		t.Fatal("the sample has no live UI session")
	}
	if tree := uiSession.Tree(); tree == nil || tree.Len() != 0 {
		t.Fatalf("denied ui.apply changed the tree: %+v", tree)
	}

	// The View Contribution itself is still registered, because views was a
	// required permission. Opening it shows an empty view, not an error.
	shell := openCounterShell(t, run)
	if _, err := shell.OpenView(appview.OpenRequest{ViewID: counterSampleViewID}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	paneID, _ := shell.FocusedPaneID(0)
	info, _ := shell.PaneInfoAt(0, paneID)
	if info.Surface == nil {
		t.Fatal("the denied View Pane has no surface")
	}
	tt := ui.NewTester(func(c *ui.Context) {
		info.Surface.(state.FocusedSurface).ViewFocused(c, true)
	}, 480, 320)
	tt.Frame()
	if tt.HasText("Count") {
		t.Fatalf("denied native UI rendered the guest tree: %v", tt.Texts())
	}
	shell.CloseTab(0)
}

// counterViewSurface returns the live surface for one placement. The move
// tests use it to prove every container binds the same renderer object.
func counterViewSurface(t *testing.T, shell *state.State,
	placement domainplugin.Placement) state.Surface {
	t.Helper()
	switch placement {
	case domainplugin.PlacementPage:
		info, ok := shell.PageView(counterSampleViewID)
		if !ok {
			t.Fatalf("no page for %s", counterSampleViewID)
		}
		return info.Surface
	case domainplugin.PlacementWindow:
		ids := shell.DetachedTabIDs()
		if len(ids) != 1 {
			t.Fatalf("detached windows = %v", ids)
		}
		info, ok := shell.DetachedTabInfo(ids[0])
		if !ok || len(info.Panes) != 1 {
			t.Fatalf("detached info = %+v ok=%v", info, ok)
		}
		pane, ok := shell.DetachedPaneInfoAt(ids[0], info.Panes[0])
		if !ok {
			t.Fatalf("detached pane %q missing", info.Panes[0])
		}
		return pane.Surface
	case domainplugin.PlacementAside:
		asides := shell.AsideViews()
		if len(asides) != 1 {
			t.Fatalf("asides = %+v", asides)
		}
		return asides[0].Surface
	default:
		paneID, ok := shell.FocusedPaneID(0)
		if !ok {
			t.Fatal("the View Tab has no focused Pane")
		}
		info, ok := shell.PaneInfoAt(0, paneID)
		if !ok {
			t.Fatalf("pane %q missing", paneID)
		}
		return info.Surface
	}
}

// TestCounterViewMovesAcrossEveryPlacementWithoutRecreation drives the real
// WASM guest, UISession, and native renderer while the same instance and
// surface move through all five placements. The typed guest state must survive
// every move, which proves nothing was recreated.
func TestCounterViewMovesAcrossEveryPlacementWithoutRecreation(t *testing.T) {
	run := startCounterSample(t, true)
	shell := openCounterShell(t, run)
	if _, err := shell.OpenView(appview.OpenRequest{
		ViewID: counterSampleViewID, Placement: domainplugin.PlacementPane,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	instances := run.views.Instances()
	if len(instances) != 1 {
		t.Fatalf("instances = %d", len(instances))
	}
	instance := instances[0]
	surface := counterViewSurface(t, shell, domainplugin.PlacementPane)
	if surface == nil {
		t.Fatal("the initial View Pane has no surface")
	}

	tt := ui.NewTester(func(c *ui.Context) {
		surface.(state.FocusedSurface).ViewFocused(c, true)
	}, 640, 480)
	tt.Frame()
	if err := tt.Click("Name"); err != nil {
		t.Fatalf("Click input: %v", err)
	}
	tt.Type("Ada")
	tt.Frame()
	if !tt.HasText("Hello, Ada!") {
		t.Fatalf("after typing texts = %v", tt.Texts())
	}

	for _, placement := range []domainplugin.Placement{
		domainplugin.PlacementTab,
		domainplugin.PlacementPage,
		domainplugin.PlacementWindow,
		domainplugin.PlacementAside,
		domainplugin.PlacementPane,
	} {
		if err := shell.MoveView(instance.ID(), placement); err != nil {
			t.Fatalf("MoveView to %s: %v", placement, err)
		}
		if instance.Placement() != placement {
			t.Fatalf("placement = %s, want %s", instance.Placement(), placement)
		}
		if live := run.views.Instances(); len(live) != 1 || live[0].ID() != instance.ID() {
			t.Fatalf("move to %s recreated the instance: %+v", placement, live)
		}
		moved := counterViewSurface(t, shell, placement)
		if moved != surface {
			t.Fatalf("move to %s built a new surface", placement)
		}
		tt.Frame()
		if !tt.HasText("Hello, Ada!") {
			t.Fatalf("after moving to %s texts = %v", placement, tt.Texts())
		}
	}

	// The plugin still owns exactly one set of handles, and closing the last
	// surface resets the one per-fiber UISession.
	uiSession, ok := run.manager.Container().NativeUI(appplugin.PluginRef{
		ID: counterSampleID, Source: run.record.Source,
	})
	if !ok {
		t.Fatal("the sample has no live UI session")
	}
	if shell.TabCount() != 1 || !shell.CloseTab(0) {
		t.Fatalf("close final pane: tabs=%d", shell.TabCount())
	}
	if tree := uiSession.Tree(); tree == nil || tree.Len() != 0 {
		t.Fatalf("closing the last View did not reset the UISession: %+v", tree)
	}
	if live := run.views.Instances(); len(live) != 0 {
		t.Fatalf("closing the last View left %d instances", len(live))
	}
}

// TestDisablingCounterViewReleasesTheOpenSession closes the live wiring while a
// View Pane and its UISession are open. Disabling must dispose the View
// contribution, close the session and every node/listener handle, remove the
// command, and unload the fiber; re-enabling must rebuild all of it without a
// stale registration left behind.
// TestCounterViewSessionClosesAcrossEveryLifecycleOperation keeps the counter
// View open while updating, rolling back, revoking a required permission, and
// uninstalling. Every operation must close the UISession and every node and
// listener handle, and leave no View instance or registered session behind.
func TestCounterViewSessionClosesAcrossEveryLifecycleOperation(t *testing.T) {
	ctx := context.Background()
	registry := newLifecycleRegistry(t, []struct {
		directory string
		id        string
		provider  bool
		data      bool
	}{{directory: "counter-view", id: counterSampleID}})
	run := startCounterSampleWithRegistry(t, true, registry)
	ref := appplugin.PluginRef{ID: counterSampleID, Source: run.record.Source}

	openSession := func(t *testing.T, stage string) *appplugin.UISession {
		t.Helper()
		shell := openCounterShell(t, run)
		if _, err := shell.OpenView(appview.OpenRequest{
			ViewID: counterSampleViewID, Placement: domainplugin.PlacementPane,
		}); err != nil {
			t.Fatalf("%s: OpenView: %v", stage, err)
		}
		focused, ok := shell.FocusedPaneID(0)
		if !ok {
			t.Fatalf("%s: the View Tab has no focused Pane", stage)
		}
		pane, ok := shell.PaneInfoAt(0, focused)
		if !ok || pane.Surface == nil {
			t.Fatalf("%s: View Pane = %+v, ok = %v", stage, pane, ok)
		}
		tt := ui.NewTester(func(c *ui.Context) {
			pane.Surface.(state.FocusedSurface).ViewFocused(c, true)
		}, 640, 480)
		tt.Frame()
		if !tt.HasText("Count: 0") {
			t.Fatalf("%s: the open view has no rendered tree: %v", stage, tt.Texts())
		}
		session, ok := run.manager.Container().NativeUI(ref)
		if !ok {
			t.Fatalf("%s: the sample has no live UI session", stage)
		}
		if _, live := session.NodeHandle(counterSampleViewNode); !live {
			t.Fatalf("%s: the rendered view has no node handle", stage)
		}
		if len(session.Listeners()) == 0 {
			t.Fatalf("%s: the rendered view has no listener handles", stage)
		}
		return session
	}

	assertClosed := func(t *testing.T, stage string, session *appplugin.UISession,
		pluginActive bool) {
		t.Helper()
		if !session.Closed() {
			t.Fatalf("%s left the UISession open", stage)
		}
		if _, live := session.NodeHandle(counterSampleViewNode); live {
			t.Fatalf("%s left a node handle", stage)
		}
		if listeners := session.Listeners(); len(listeners) != 0 {
			t.Fatalf("%s left listeners: %+v", stage, listeners)
		}
		// Update and rollback keep the plugin active, so activation may install
		// a fresh session. The old one must never be that registered session.
		if current, ok := run.manager.Container().NativeUI(ref); ok {
			if current == session {
				t.Fatalf("%s left the old UISession registered", stage)
			}
			if !pluginActive {
				t.Fatalf("%s left a UISession registered while inactive", stage)
			}
		}
		if instances := run.views.Instances(); len(instances) != 0 {
			t.Fatalf("%s left %d view instances", stage, len(instances))
		}
	}

	enable := func(t *testing.T) {
		t.Helper()
		review, err := run.manager.Review(ctx, counterSampleID)
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		decisions := make([]domainplugin.PermissionDecision, 0,
			len(review.Required)+len(review.Optional))
		for _, row := range review.Required {
			decisions = append(decisions, domainplugin.PermissionDecision{
				Permission: row.Permission, Scope: row.Scope, Allow: true,
			})
		}
		for _, row := range review.Optional {
			decisions = append(decisions, domainplugin.PermissionDecision{
				Permission: row.Permission, Scope: row.Scope, Allow: true,
			})
		}
		if err := run.manager.Enable(ctx, counterSampleID, decisions); err != nil {
			t.Fatalf("Enable: %v", err)
		}
	}

	session := openSession(t, "before update")
	plan, err := run.manager.PlanUpdate(ctx, counterSampleID)
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if _, err := run.manager.ApplyUpdate(ctx, plan, nil); err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	assertClosed(t, "update", session, true)

	session = openSession(t, "before rollback")
	if _, err := run.manager.Rollback(ctx, counterSampleID, nil); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	assertClosed(t, "rollback", session, true)

	session = openSession(t, "before revoke")
	detail, ok, err := run.manager.Detail(ctx, counterSampleID)
	if err != nil || !ok {
		t.Fatalf("Detail: ok=%v err=%v", ok, err)
	}
	var required domainplugin.Permission
	for _, status := range detail.Permissions {
		if status.Required {
			required = status.Permission
			break
		}
	}
	if required == "" {
		t.Fatal("the sample declares no required permission to revoke")
	}
	if err := run.manager.Revoke(ctx, counterSampleID, required); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	assertClosed(t, "revoke", session, false)

	enable(t)
	session = openSession(t, "before uninstall")
	if err := run.manager.Uninstall(ctx, counterSampleID, true); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	assertClosed(t, "uninstall", session, false)
}

func TestDisablingCounterViewReleasesTheOpenSession(t *testing.T) {
	ctx := context.Background()
	run := startCounterSample(t, true)
	shell := openCounterShell(t, run)
	paneID, err := shell.OpenView(appview.OpenRequest{
		ViewID: counterSampleViewID, Placement: domainplugin.PlacementPane,
	})
	if err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	if paneID == "" {
		t.Fatal("OpenView returned no tab")
	}
	focused, ok := shell.FocusedPaneID(0)
	if !ok {
		t.Fatal("the View Tab has no focused Pane")
	}
	pane, ok := shell.PaneInfoAt(0, focused)
	if !ok || pane.Surface == nil {
		t.Fatalf("View Pane = %+v, ok = %v", pane, ok)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		pane.Surface.(state.FocusedSurface).ViewFocused(c, true)
	}, 640, 480)
	tt.Frame()
	if !tt.HasText("Count: 0") {
		t.Fatalf("the open view has no rendered tree: %v", tt.Texts())
	}
	uiSession, ok := run.manager.Container().NativeUI(appplugin.PluginRef{
		ID: counterSampleID, Source: run.record.Source,
	})
	if !ok {
		t.Fatal("the sample has no live UI session")
	}
	if _, live := uiSession.NodeHandle(counterSampleViewNode); !live {
		t.Fatal("the rendered view has no node handle")
	}
	if len(uiSession.Listeners()) == 0 {
		t.Fatal("the rendered view has no listener handles")
	}

	if err := run.manager.Disable(ctx, counterSampleID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if !uiSession.Closed() {
		t.Fatal("disabling the plugin left the UISession open")
	}
	if _, live := uiSession.NodeHandle(counterSampleViewNode); live {
		t.Fatal("disabling the plugin left a node handle")
	}
	if listeners := uiSession.Listeners(); len(listeners) != 0 {
		t.Fatalf("disabling the plugin left listeners: %+v", listeners)
	}
	if _, ok := run.manager.Container().NativeUI(appplugin.PluginRef{
		ID: counterSampleID, Source: run.record.Source,
	}); ok {
		t.Fatal("disabling the plugin left its UISession registered")
	}
	if contributions := run.views.Contributions(); len(contributions) != 0 {
		t.Fatalf("disabling the plugin left view contributions: %+v", contributions)
	}
	if instances := run.views.Instances(); len(instances) != 0 {
		t.Fatalf("disabling the plugin left %d view instances", len(instances))
	}
	if _, ok := run.manager.Container().FiberByRef(appplugin.PluginRef{
		ID: counterSampleID, Source: run.record.Source,
	}); ok {
		t.Fatal("disabling the plugin left its fiber")
	}
	qualified := command.ID(domainplugin.QualifiedCommandID(counterSampleID, "report"))
	if err := run.commands.Dispatch(ctx, qualified); err == nil {
		t.Fatal("disabling the plugin left its command registered")
	}

	review, err := run.manager.Review(ctx, counterSampleID)
	if err != nil {
		t.Fatalf("Review after disable: %v", err)
	}
	decisions := make([]domainplugin.PermissionDecision, 0, len(review.Required)+len(review.Optional))
	for _, row := range review.Required {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: row.Permission, Scope: row.Scope, Allow: true,
		})
	}
	for _, row := range review.Optional {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: row.Permission, Scope: row.Scope, Allow: true,
		})
	}
	if err := run.manager.Enable(ctx, counterSampleID, decisions); err != nil {
		t.Fatalf("Enable after disable: %v", err)
	}
	if contributions := run.views.Contributions(); len(contributions) != 1 {
		t.Fatalf("re-enabling rebuilt %d view contributions, want 1", len(contributions))
	}
	report := run.report(t)
	requireCounterReportLines(t, report, "ui.apply status=ok", "handles.count=1")
}
