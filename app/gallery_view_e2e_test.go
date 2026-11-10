package app

// The gallery View sample e2e lives in the composition root because it needs
// both MyGo (for the ui.Tester) and the real plugin adapters. It drives the
// committed C WASM guest through the archive validator, install store, consent
// review, container, wazero runtime, app plugin surface, and native View
// renderer, exercising the data grid, graph, plan graph, and canvas.

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/internal/testutil"
	"github.com/mslxl/natcatty/ui/pluginview"
	"github.com/mslxl/natcatty/ui/shell/state"
)

const (
	gallerySampleID       = "acme.gallery-view"
	gallerySampleViewID   = "plugin.acme.gallery-view.gallery"
	galleryGridNode       = 10
	galleryGraphNode      = 20
	galleryGraphLoadNode  = 21
	galleryGraphSaveNode  = 22
	galleryInitialGridRow = 6
	galleryVirtualNode    = 60
	galleryAnimateNode    = 80
	galleryRichParagraph  = "Rich text with <b>literal</b> markup and a trusted link"
)

func galleryViewArchive(t *testing.T) string {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "gallery-view")
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
	archivePath := filepath.Join(t.TempDir(), gallerySampleID+".natplug")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	fixed := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	entries := []struct {
		name string
		data []byte
	}{
		{"i18n/en.toml", catalog},
		{"plugin.toml", manifest},
		{"plugin.wasm", module},
	}
	for _, name := range []string{
		"assets/Go-Regular.ttf",
		"assets/Go-Regular.LICENSE",
		"assets/Go-Regular.PATENTS",
		"assets/logo.svg",
	} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read sample asset %s: %v", name, err)
		}
		entries = append(entries, struct {
			name string
			data []byte
		}{name, data})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, entry := range entries {
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

type galleryFeedback struct {
	mu      sync.Mutex
	outputs []string
}

func (f *galleryFeedback) add(_ appplugin.PluginRef, output string) {
	f.mu.Lock()
	f.outputs = append(f.outputs, output)
	f.mu.Unlock()
}

func (f *galleryFeedback) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.outputs...)
}

type galleryRun struct {
	manager  *appplugin.Manager
	surface  *pluginSurface
	commands *command.Registry
	feedback *galleryFeedback
	views    *appview.Registry
	record   domainplugin.InstallRecord
	ref      appplugin.PluginRef
}

// startGallerySample packages the committed sample and drives it through the
// real archive validator, install store, consent review, manager, container,
// and wazero runtime. grantNative decides whether the optional ui.native
// permission is consented to.
func startGallerySample(t *testing.T, grantNative bool) *galleryRun {
	t.Helper()
	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	views := appview.NewRegistry()
	t.Cleanup(views.Close)
	commands := command.NewRegistry()
	surface := newPluginSurface(commands)
	surface.SetViewRegistry(views)
	feedback := &galleryFeedback{}
	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		surface,
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithManagerFeedback(func(ref appplugin.PluginRef, output string) {
			feedback.add(ref, output)
		}),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	record, err := manager.InstallFile(ctx, galleryViewArchive(t))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	if record.ID != gallerySampleID || record.Source != domainplugin.SourceLocalFile {
		t.Fatalf("install record = %+v", record)
	}
	review, err := manager.Review(ctx, gallerySampleID)
	if err != nil {
		t.Fatalf("Review: %v", err)
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
	if err := manager.Enable(ctx, gallerySampleID, decisions); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	return &galleryRun{
		manager:  manager,
		surface:  surface,
		commands: commands,
		feedback: feedback,
		views:    views,
		record:   record,
		ref:      appplugin.PluginRef{ID: gallerySampleID, Source: record.Source},
	}
}

// report dispatches the sample's contributed report command through the app
// command registry and returns the guest's visible output.
func (r *galleryRun) report(t *testing.T) string {
	t.Helper()
	before := len(r.feedback.snapshot())
	qualified := command.ID(domainplugin.QualifiedCommandID(gallerySampleID, "report"))
	if err := r.commands.Dispatch(context.Background(), qualified); err != nil {
		t.Fatalf("dispatch report: %v", err)
	}
	outputs := r.feedback.snapshot()
	if len(outputs) != before+1 {
		t.Fatalf("outputs after report = %v", outputs)
	}
	return outputs[len(outputs)-1]
}

func requireGalleryReportLines(t *testing.T, report string, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(report, line) {
			t.Fatalf("report is missing %q:\n%s", line, report)
		}
	}
}

// galleryReportValue reads the integer after key= in the gallery report.
func galleryReportValue(t *testing.T, report, key string) int {
	t.Helper()
	marker := key + "="
	start := strings.Index(report, marker)
	if start < 0 {
		t.Fatalf("report has no %s:\n%s", key, report)
	}
	rest := report[start+len(marker):]
	end := 0
	for end < len(rest) && ((rest[end] >= '0' && rest[end] <= '9') || (end == 0 && rest[end] == '-')) {
		end++
	}
	if end == 0 {
		t.Fatalf("report %s has no integer value:\n%s", key, report)
	}
	value, err := strconv.Atoi(rest[:end])
	if err != nil {
		t.Fatalf("report %s value: %v", key, err)
	}
	return value
}

// openGalleryShell opens the sample View in a Pane through the real shell
// state and the app's real renderer factory.
func openGalleryShell(t *testing.T, run *galleryRun) *state.State {
	t.Helper()
	return state.NewState(state.WithViews(run.views,
		func(instance *appview.Instance) (state.Surface, error) {
			return newPluginViewSurface(instance, func() *appplugin.Manager {
				return run.manager
			}, pluginViewHost{
				Poll: pluginViewPoll(func() *appplugin.Manager { return run.manager }, instance.ViewID()),
			}, func() {})
		}))
}

func TestGalleryViewSampleExercisesEveryDataWidget(t *testing.T) {
	run := startGallerySample(t, true)
	contributions := run.views.Contributions()
	if len(contributions) != 1 {
		t.Fatalf("registered views = %+v", contributions)
	}
	contribution := contributions[0]
	if contribution.ID != gallerySampleViewID || contribution.LocalID != "gallery" ||
		!contribution.Allows(domainplugin.PlacementPane) ||
		contribution.Instances != domainplugin.InstanceSingleton {
		t.Fatalf("view contribution = %+v", contribution)
	}

	shell := openGalleryShell(t, run)
	if _, err := shell.OpenView(appview.OpenRequest{
		ViewID: gallerySampleViewID, Placement: domainplugin.PlacementPane,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
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
	}, 760, 1400)
	tt.Frame()
	tt.Frame()
	tt.Frame()
	for _, want := range []string{
		"Widget gallery",
		"Inventory", "Name", "Size", "Kind", "asset_0.bin",
		"Load", "Save", "Transform",
		"Seq Scan", "Index Scan", "actual rows 42",
		"Hash Aggregate", "spilled to disk", "12.5",
		"Sketch", "canvas label",
		galleryRichParagraph, "feed row 0", "Animate",
	} {
		if !tt.HasText(want) {
			t.Fatalf("missing gallery text %q in %v\nreport:\n%s", want, tt.Texts(), run.report(t))
		}
	}
	// The packaged SVG resolves from the installed package; the alt
	// fallback must not show.
	if tt.HasText("Image fallback") {
		t.Fatalf("the packaged image fell back to its alt text: %v", tt.Texts())
	}
	// The app's resolver also reaches images drawn inside a canvas. The logo's
	// blue field must paint where the fallback would only show an outline.
	canvasBox, ok := tt.Find("Sketch")
	if !ok {
		t.Fatalf("the canvas has no labeled box: %v", tt.Texts())
	}
	screen := tt.Image()
	blue := 0
	for y := max(0, int(canvasBox.Y)); y < min(screen.Bounds().Dy(), int(canvasBox.Y+canvasBox.H)); y++ {
		for x := max(0, int(canvasBox.X)); x < min(screen.Bounds().Dx(), int(canvasBox.X+canvasBox.W)); x++ {
			if pixel := screen.RGBAAt(x, y); pixel.B > 150 && pixel.R < 120 && pixel.G < 180 {
				blue++
			}
		}
	}
	if blue < 100 {
		t.Fatalf("canvas packaged image painted %d blue pixels in %v", blue, canvasBox)
	}

	// The grid declares 50,000 rows but only patches the sparse window the
	// host asks for; the retained tree stays far below the declared count.
	report := run.report(t)
	requireGalleryReportLines(t, report,
		"ui.apply status=ok", "ui.listen status=ok listeners=16",
		"handles.grid=1 handles.graph=1 handles.plan=1 handles.canvas=1",
		"invalid.prop=invalid", "invalid.asset=invalid",
		"invalid.handle=stale-handle", "revision.intact=1",
	)
	if got := galleryReportValue(t, report, "virtual.item_count"); got != 250000 {
		t.Fatalf("virtual.item_count = %d, want 250000", got)
	}
	if got := galleryReportValue(t, report, "virtual.requests"); got < 1 {
		t.Fatalf("virtual.requests = %d, want at least one visible window", got)
	}
	if got := galleryReportValue(t, report, "virtual.rows"); got < 3 || got > 64 {
		t.Fatalf("virtual.rows = %d, want only the pulled window", got)
	}
	if got := galleryReportValue(t, report, "grid.row_count"); got != 50000 {
		t.Fatalf("grid.row_count = %d, want 50000", got)
	}
	if got := galleryReportValue(t, report, "grid.requests"); got < 1 {
		t.Fatalf("grid.requests = %d, want at least one on-demand page", got)
	}
	if got := galleryReportValue(t, report, "grid.rows"); got < galleryInitialGridRow || got > 200 {
		t.Fatalf("grid.rows = %d, want a sparse window between %d and 200", got, galleryInitialGridRow)
	}
	uiSession, ok := run.manager.Container().NativeUI(run.ref)
	if !ok {
		t.Fatal("the sample has no live UI session")
	}
	if got := uiSession.Tree().Len(); got < 20 || got > 500 {
		t.Fatalf("retained tree has %d nodes; large row counts must not cross in one patch", got)
	}

	// Grid rows select like a table, and no cell materializes more nodes.
	if err := tt.Click("asset_1.bin"); err != nil {
		t.Fatalf("click grid row: %v", err)
	}
	tt.Frame()
	report = run.report(t)
	gridSelectionsBeforeSort := galleryReportValue(t, report, "grid.selection")
	if gridSelectionsBeforeSort < 1 {
		t.Fatalf("grid.selection = %d, want a selection event", gridSelectionsBeforeSort)
	}

	// A sortable header reports its sort change with the column and order,
	// and must not fabricate a row selection.
	if err := tt.Click("Name"); err != nil {
		t.Fatalf("click sort header: %v", err)
	}
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "grid.sort"); got < 1 {
		t.Fatalf("grid.sort = %d, want a sort change event", got)
	}
	if got := galleryReportValue(t, report, "grid.selection"); got != gridSelectionsBeforeSort {
		t.Fatalf("grid.selection after sorting = %d, want %d (no fabricated selection)", got, gridSelectionsBeforeSort)
	}
	if got := galleryReportValue(t, report, "grid.sort.index"); got != 0 {
		t.Fatalf("grid.sort.index = %d, want 0 (Name)", got)
	}
	if got := galleryReportValue(t, report, "grid.sort.direction"); got != 0 {
		t.Fatalf("grid.sort.direction = %d, want 0 (ascending)", got)
	}

	// The graph reports selection and activation with the graph node and the
	// chosen node, never with a raw child id.
	if err := tt.Click("Load"); err != nil {
		t.Fatalf("click graph node: %v", err)
	}
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "graph.selection.index"); got != 0 {
		t.Fatalf("graph.selection.index = %d, want 0 (Load)", got)
	}
	if got := galleryReportValue(t, report, "graph.selection.related"); got != 0 {
		t.Fatalf("graph.selection.related = %d, want the Load node", got)
	}
	if err := tt.Click("Save"); err != nil {
		t.Fatalf("first graph activation: %v", err)
	}
	if err := tt.Click("Save"); err != nil {
		t.Fatalf("second graph activation: %v", err)
	}
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "graph.activation"); got < 1 {
		t.Fatalf("graph.activation = %d, want an activation event", got)
	}
	if got := galleryReportValue(t, report, "graph.selection.index"); got != 1 {
		t.Fatalf("graph.selection.index after activation = %d, want 1 (Save)", got)
	}

	// Plan interaction stays attributed to the plan_graph node.
	if err := tt.Click("Index Scan"); err != nil {
		t.Fatalf("click plan node: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Index Scan"); err != nil {
		t.Fatalf("first plan activation: %v", err)
	}
	if err := tt.Click("Index Scan"); err != nil {
		t.Fatalf("second plan activation: %v", err)
	}
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "plan.selection.index"); got != 1 {
		t.Fatalf("plan.selection.index = %d, want 1 (Index Scan)", got)
	}
	if got := galleryReportValue(t, report, "plan.activation"); got < 1 {
		t.Fatalf("plan.activation = %d, want an activation event", got)
	}
	if got := galleryReportValue(t, report, "plan.escaped"); got != 0 {
		t.Fatalf("plan.escaped = %d, want plan interaction contained to its node", got)
	}

	// Rich text selects host-side, and the selected text crosses the ABI as
	// plain text; the link opens through the host browser.
	richRect, ok := tt.Find(galleryRichParagraph)
	if !ok {
		t.Fatalf("the rich paragraph has no box: %v", tt.Texts())
	}
	tt.Press(richRect.X+2, richRect.Y+6)
	tt.Move(richRect.X+richRect.W-2, richRect.Y+richRect.H-6)
	tt.Release(richRect.X+richRect.W-2, richRect.Y+richRect.H-6)
	tt.Frame()
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "rich.selection"); got < 1 {
		t.Fatalf("rich.selection = %d, want a selection event", got)
	}
	if !strings.Contains(report, "rich.selection.text=") || !strings.Contains(report, "<b>literal</b>") {
		t.Fatalf("rich selection text is missing from the report:\n%s", report)
	}
	if err := tt.Click("trusted link"); err != nil {
		t.Fatalf("click rich link: %v", err)
	}
	opened := tt.OpenedURLs()
	if len(opened) != 1 || opened[0] != "https://example.com/gallery" {
		t.Fatalf("opened URLs = %v, want the inline link", opened)
	}

	// The virtual list pulls only the visible window and reports absolute
	// item indices for clicks and selection.
	if err := tt.Click("feed row 1"); err != nil {
		t.Fatalf("click virtual row: %v", err)
	}
	tt.Frame()
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "virtual.click"); got < 1 {
		t.Fatalf("virtual.click = %d, want a row click", got)
	}
	if got := galleryReportValue(t, report, "virtual.click.index"); got != 1 {
		t.Fatalf("virtual.click.index = %d, want 1", got)
	}
	if got := galleryReportValue(t, report, "virtual.selection.index"); got != 1 {
		t.Fatalf("virtual.selection.index = %d, want 1", got)
	}
	virtualRequestsBeforeScroll := galleryReportValue(t, report, "virtual.requests")
	feedRect, ok := tt.Find("feed row 0")
	if !ok {
		t.Fatalf("the virtual list has no visible row: %v", tt.Texts())
	}
	tt.Scroll(feedRect.X+feedRect.W/2, feedRect.Y+feedRect.H/2, 0, 2500)
	tt.Frame()
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "virtual.requests"); got <= virtualRequestsBeforeScroll {
		t.Fatalf("virtual.requests = %d after scrolling, want more than %d", got, virtualRequestsBeforeScroll)
	}
	if got := galleryReportValue(t, report, "virtual.rows"); got > 64 {
		t.Fatalf("virtual.rows = %d after scrolling; the whole list must never cross at once", got)
	}

	// The guest patches a width; the host drives the transition and reports
	// animation_end once it settles.
	if err := tt.Click("Animate"); err != nil {
		t.Fatalf("click Animate: %v", err)
	}
	tt.Frame()
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "animation.changes"); got != 1 {
		t.Fatalf("animation.changes = %d, want the host transition patch", got)
	}
	if got := galleryReportValue(t, report, "animation.ends"); got != 0 {
		t.Fatalf("animation.ends = %d before the transition ran", got)
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "animation.ends"); got != 1 {
		t.Fatalf("animation.ends = %d, want one host-driven completion", got)
	}
	if got := galleryReportValue(t, report, "animation.wide"); got != 1 {
		t.Fatalf("animation.wide = %d, want the settled wide bar", got)
	}

	// Canvas pointer coordinates are relative to the canvas's top-left,
	// never the window or a guest transform.
	canvasRect, ok := tt.Find("Sketch")
	if !ok {
		t.Fatalf("the canvas has no labeled box: %v", tt.Texts())
	}
	tt.ClickAt(canvasRect.X+30, canvasRect.Y+20)
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "canvas.click.x"); got != 30 {
		t.Fatalf("canvas.click.x = %d, want 30", got)
	}
	if got := galleryReportValue(t, report, "canvas.click.y"); got != 20 {
		t.Fatalf("canvas.click.y = %d, want 20", got)
	}
	tt.Press(canvasRect.X+10, canvasRect.Y+40)
	tt.Move(canvasRect.X+25, canvasRect.Y+45)
	tt.Frame()
	tt.Release(canvasRect.X+25, canvasRect.Y+45)
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "canvas.drag"); got < 1 {
		t.Fatalf("canvas.drag = %d, want a drag event", got)
	}
	if got := galleryReportValue(t, report, "canvas.drag.x"); got != 25 {
		t.Fatalf("canvas.drag.x = %d, want 25", got)
	}
	if got := galleryReportValue(t, report, "canvas.drag.y"); got != 45 {
		t.Fatalf("canvas.drag.y = %d, want 45", got)
	}

	// A wheel over the grid is attributed to the data_grid, and the new
	// window is paged in through another item_request.
	gridRect, ok := tt.Find("asset_3.bin")
	if !ok {
		t.Fatalf("the grid has no visible row: %v", tt.Texts())
	}
	tt.Scroll(gridRect.X+gridRect.W/2, gridRect.Y+gridRect.H/2, 0, 4000)
	tt.Frame()
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "events.scroll"); got < 1 {
		t.Fatalf("events.scroll = %d, want a grid scroll event", got)
	}
	if got := galleryReportValue(t, report, "events.scroll.dy"); got != 4000 {
		t.Fatalf("events.scroll.dy = %d, want 4000", got)
	}
	if got := galleryReportValue(t, report, "grid.requests"); got < 2 {
		t.Fatalf("grid.requests = %d, want another page after scrolling", got)
	}
	if got := galleryReportValue(t, report, "grid.rows"); got > 200 {
		t.Fatalf("grid.rows = %d after scrolling; the whole set must never cross at once", got)
	}

	// Closing the last Pane releases every node and listener handle.
	if !shell.CloseTab(0) {
		t.Fatal("CloseTab reported false")
	}
	if tree := uiSession.Tree(); tree == nil || tree.Len() != 0 || uiSession.Revision() != 0 {
		t.Fatalf("after close tree = %+v", tree)
	}
	if listeners := uiSession.Listeners(); len(listeners) != 0 {
		t.Fatalf("after close listeners = %+v", listeners)
	}
	if inst := run.views.Instances(); len(inst) != 0 {
		t.Fatalf("after close instances = %d", len(inst))
	}
}

// TestGalleryViewSampleSkipsOverBudgetFramesAndCoalescesEvents drives the real
// WASM guest through a renderer with a 1ns frame budget, so every guest poll
// overruns and the following frame is skipped. Events queued in between stay
// in the coalescing queue (two scrolls become one, two graph selections become
// the later one) and reach the guest on the frame after the skip.
func TestGalleryViewSampleSkipsOverBudgetFramesAndCoalescesEvents(t *testing.T) {
	run := startGallerySample(t, true)
	uiSession, ok := run.manager.Container().NativeUI(run.ref)
	if !ok {
		t.Fatal("the sample has no live UI session")
	}
	surface := pluginview.New(uiSession,
		pluginview.WithFrameBudget(time.Nanosecond),
		pluginview.WithPoll(func() {
			if err := run.manager.Container().PollUI(context.Background(), run.ref); err != nil &&
				!errors.Is(err, appplugin.ErrCallInFlight) {
				t.Errorf("PollUI: %v", err)
			}
		}),
	)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 760, 1000)

	// Let the initial resize and the grid's first paging rounds settle. The
	// loop waits on an observable condition (an empty host queue after a
	// rendered frame) and has a hard frame bound.
	settled := false
	for i := 0; i < 64; i++ {
		tt.Frame()
		if uiSession.PendingEvents() == 0 {
			settled = true
			break
		}
	}
	if !settled {
		t.Fatalf("the gallery view did not settle: pending = %d", uiSession.PendingEvents())
	}
	// Flush a skip armed by the last poll, then start counting.
	tt.Frame()
	tt.Frame()

	report := run.report(t)
	baselinePolls := galleryReportValue(t, report, "poll.calls")
	if baselinePolls < 1 {
		t.Fatalf("poll.calls = %d, want the initial guest polls", baselinePolls)
	}

	loadHandle, ok := uiSession.NodeHandle(galleryGraphLoadNode)
	if !ok {
		t.Fatal("the Load node has no handle")
	}
	saveHandle, ok := uiSession.NodeHandle(galleryGraphSaveNode)
	if !ok {
		t.Fatal("the Save node has no handle")
	}
	// Two coalescing scrolls on the same node plus two coalescing selections
	// on the same graph node queued before one frame.
	if !uiSession.EnqueueNode(galleryGridNode, domainnativeui.Event{Kind: domainnativeui.EventScroll, DY: -10}) {
		t.Fatal("the grid does not accept scroll events")
	}
	if !uiSession.EnqueueNode(galleryGridNode, domainnativeui.Event{Kind: domainnativeui.EventScroll, DY: -20}) {
		t.Fatal("the grid does not accept the second scroll event")
	}
	if !uiSession.EnqueueNode(galleryGraphNode, domainnativeui.Event{
		Kind: domainnativeui.EventSelection, Index: 0, Related: domainnativeui.NodeRef(loadHandle),
	}) {
		t.Fatal("the graph does not accept selection events")
	}
	if !uiSession.EnqueueNode(galleryGraphNode, domainnativeui.Event{
		Kind: domainnativeui.EventSelection, Index: 1, Related: domainnativeui.NodeRef(saveHandle),
	}) {
		t.Fatal("the graph does not accept the second selection event")
	}

	// The over-budget poll delivers the coalesced work and arms the skip.
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "poll.calls"); got != baselinePolls+1 {
		t.Fatalf("poll.calls after the over-budget frame = %d, want %d", got, baselinePolls+1)
	}
	if got := galleryReportValue(t, report, "events.scroll"); got != 1 {
		t.Fatalf("events.scroll = %d, want two scrolls coalesced into one", got)
	}
	if got := galleryReportValue(t, report, "events.scroll.dy"); got != -20 {
		t.Fatalf("events.scroll.dy = %d, want the later scroll (-20)", got)
	}
	if got := galleryReportValue(t, report, "graph.selection"); got != 1 {
		t.Fatalf("graph.selection = %d, want two selections coalesced into one", got)
	}
	if got := galleryReportValue(t, report, "graph.selection.index"); got != 1 {
		t.Fatalf("graph.selection.index = %d, want the later selection (1)", got)
	}
	if got := galleryReportValue(t, report, "graph.selection.related"); got != 1 {
		t.Fatalf("graph.selection.related = %d, want the later related node (Save)", got)
	}

	// A new event arriving while the skip is armed is not delivered.
	if !uiSession.EnqueueNode(galleryGridNode, domainnativeui.Event{Kind: domainnativeui.EventScroll, DY: -30}) {
		t.Fatal("the grid does not accept the deferred scroll event")
	}
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "poll.calls"); got != baselinePolls+1 {
		t.Fatalf("the over-budget frame was not skipped: poll.calls = %d, want %d", got, baselinePolls+1)
	}
	if got := galleryReportValue(t, report, "events.scroll"); got != 1 {
		t.Fatalf("the deferred scroll was delivered during the skipped frame: events.scroll = %d", got)
	}

	// The frame after the skip polls and delivers the deferred event.
	tt.Frame()
	report = run.report(t)
	if got := galleryReportValue(t, report, "poll.calls"); got != baselinePolls+2 {
		t.Fatalf("poll.calls after the skipped frame = %d, want %d", got, baselinePolls+2)
	}
	if got := galleryReportValue(t, report, "events.scroll"); got != 2 {
		t.Fatalf("events.scroll after the deferred frame = %d, want 2", got)
	}
	if got := galleryReportValue(t, report, "events.scroll.dy"); got != -30 {
		t.Fatalf("events.scroll.dy = %d, want the deferred scroll (-30)", got)
	}
}
