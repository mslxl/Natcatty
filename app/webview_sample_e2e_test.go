package app

// The webview sample e2e lives in the composition root because it needs the
// real archive validator, install store, consent review, manager, container,
// wazero runtime, app view factory, and shell state. Only the native page
// widget is faked: the sample's WASM guest, the per-plugin asset transport,
// the bridge verification, and the contribution allowlist are real.

import (
	"archive/zip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	adapterplugininstall "github.com/mslxl/natcatty/adapters/plugininstall"
	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	adapterpluginpackage "github.com/mslxl/natcatty/adapters/pluginpackage"
	adapterwasm "github.com/mslxl/natcatty/adapters/wasm"
	"github.com/mslxl/natcatty/application/command"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	appview "github.com/mslxl/natcatty/application/view"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/internal/testutil"
	"github.com/mslxl/natcatty/ui/shell/state"
)

const (
	webviewSampleID       = "acme.webview-view"
	webviewSampleWebView  = "plugin.acme.webview-view.web"
	webviewSampleEmbed    = "plugin.acme.webview-view.embed"
	webviewSampleOrigin   = "natcatty-plugin://acme.webview-view"
	webviewSampleGreeting = "Hello, Ada! Served by acme.webview-view.\n"
)

// sampleWebviewPage records the one page the fake port created. The test
// calls the application bridge with Identity, exactly as the real MyGo
// adapter passes the calling page.
type sampleWebviewPage struct {
	identity  any
	request   appplugin.WebviewOpenRequest
	mu        sync.Mutex
	locale    string
	direction appplugin.UIDirection
	destroys  int
}

func (p *sampleWebviewPage) Identity() any { return p.identity }
func (p *sampleWebviewPage) Native() any   { return nil }
func (p *sampleWebviewPage) LoadURL(string) error {
	return nil
}
func (p *sampleWebviewPage) Reload() {}
func (p *sampleWebviewPage) Destroy() {
	p.mu.Lock()
	p.destroys++
	p.mu.Unlock()
}
func (p *sampleWebviewPage) SetLocale(locale string, direction appplugin.UIDirection) {
	p.mu.Lock()
	p.locale, p.direction = locale, direction
	p.mu.Unlock()
}

type sampleWebviewPort struct {
	mu    sync.Mutex
	pages []*sampleWebviewPage
}

func (p *sampleWebviewPort) Open(_ context.Context, request appplugin.WebviewOpenRequest) (appplugin.WebviewPage, error) {
	page := &sampleWebviewPage{identity: new(int), request: request}
	p.mu.Lock()
	p.pages = append(p.pages, page)
	p.mu.Unlock()
	return page, nil
}

func (p *sampleWebviewPort) ClearStorage(string) error { return nil }

func (p *sampleWebviewPort) pageFor(t *testing.T, entry string, embedded bool) *sampleWebviewPage {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, page := range p.pages {
		if page.request.Entry == entry && page.request.Embedded == embedded {
			return page
		}
	}
	t.Fatalf("the fake port has no page for %s (embedded %v): %+v", entry, embedded, p.pages)
	return nil
}

type sampleWebviewTarget struct{}

type sampleFeedback struct {
	mu      sync.Mutex
	outputs []string
}

func (f *sampleFeedback) add(_ appplugin.PluginRef, output string) {
	f.mu.Lock()
	f.outputs = append(f.outputs, output)
	f.mu.Unlock()
}

func (f *sampleFeedback) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.outputs...)
}

type sampleRun struct {
	manager  *appplugin.Manager
	port     *sampleWebviewPort
	views    *appview.Registry
	feedback *sampleFeedback
	record   domainplugin.InstallRecord
}

func sampleRef(run *sampleRun) appplugin.PluginRef {
	return appplugin.PluginRef{ID: webviewSampleID, Source: run.record.Source}
}

// webviewSampleArchive packages the committed sample, including its UI
// assets. The validator, installer, and container read the real files.
func webviewSampleArchive(t *testing.T) string {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "webview-view")
	entries := make(map[string][]byte)
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read sample %s: %v", name, err)
		}
		return data
	}
	entries["plugin.toml"] = read("plugin.toml")
	entries["plugin.wasm"] = read("plugin.wasm")
	entries["i18n/en.toml"] = read("i18n/en.toml")
	uiFiles, err := os.ReadDir(filepath.Join(root, "ui"))
	if err != nil {
		t.Fatalf("read sample ui directory: %v", err)
	}
	for _, file := range uiFiles {
		if file.IsDir() {
			continue
		}
		name := "ui/" + file.Name()
		entries[name] = read(name)
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	archivePath := filepath.Join(t.TempDir(), webviewSampleID+".natplug")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	fixed := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(fixed)
		header.SetMode(0o644)
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := part.Write(entries[name]); err != nil {
			t.Fatalf("write %s: %v", name, err)
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

func startWebviewSample(t *testing.T, grantNative bool) *sampleRun {
	t.Helper()
	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	views := appview.NewRegistry()
	t.Cleanup(views.Close)
	surface := newPluginSurface(command.NewRegistry())
	surface.SetViewRegistry(views)
	port := &sampleWebviewPort{}
	feedback := &sampleFeedback{}
	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		surface,
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithLocale("en"),
		appplugin.WithWebviewPort(port),
		appplugin.WithManagerFeedback(func(ref appplugin.PluginRef, output string) {
			feedback.add(ref, output)
		}),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	record, err := manager.InstallFile(ctx, webviewSampleArchive(t))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	review, err := manager.Review(ctx, webviewSampleID)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	required := make(map[domainplugin.Permission]bool)
	for _, row := range review.Required {
		required[row.Permission] = true
	}
	for _, permission := range []domainplugin.Permission{
		domainplugin.PermissionViews,
		domainplugin.PermissionCommands,
		domainplugin.PermissionUIWebview,
	} {
		if !required[permission] {
			t.Fatalf("required consent rows missing %s: %+v", permission, review.Required)
		}
	}
	decisions := []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionViews, Allow: true},
		{Permission: domainplugin.PermissionCommands, Allow: true},
		{Permission: domainplugin.PermissionUIWebview, Allow: true},
	}
	if grantNative {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: domainplugin.PermissionUINative, Allow: true,
		})
	}
	if err := manager.Enable(ctx, webviewSampleID, decisions); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	return &sampleRun{manager: manager, port: port, views: views, feedback: feedback, record: record}
}

// openWebviewSampleShell opens the sample's declared views through the real
// shell state and the app's real surface factory. The native widget is faked
// by the port, so no window is needed.
func openWebviewSampleShell(t *testing.T, run *sampleRun, viewIDs ...string) *state.State {
	t.Helper()
	shell := state.NewState(state.WithViews(run.views,
		func(instance *appview.Instance) (state.Surface, error) {
			return newPluginViewSurface(instance, func() *appplugin.Manager {
				return run.manager
			}, pluginViewHost{
				Window: func() any { return sampleWebviewTarget{} },
				Poll: pluginViewPoll(func() *appplugin.Manager {
					return run.manager
				}, instance.ViewID()),
			}, func() {})
		}))
	for _, viewID := range viewIDs {
		if _, err := shell.OpenView(appview.OpenRequest{
			ViewID: viewID, Placement: domainplugin.PlacementPane,
		}); err != nil {
			t.Fatalf("OpenView(%s): %v", viewID, err)
		}
	}
	return shell
}

func sampleSurfaces(t *testing.T, run *sampleRun) []*appplugin.WebviewSurface {
	t.Helper()
	webviews, ok := run.manager.Container().Webviews(sampleRef(run))
	if !ok {
		t.Fatal("the sample has no live webview session")
	}
	return webviews.Surfaces()
}

func findSampleSurface(t *testing.T, run *sampleRun, entry string, embedded bool) *appplugin.WebviewSurface {
	t.Helper()
	for _, surface := range sampleSurfaces(t, run) {
		if surface.Entry() == entry && surface.Embedded() == embedded {
			return surface
		}
	}
	t.Fatalf("no surface for %s (embedded %v)", entry, embedded)
	return nil
}

// callSampleBridge invokes the real bridge and drives the real guest poll,
// which is where the sample answers the request. The poll runs only after the
// queued event is observable, so the test waits on a real condition.
func callSampleBridge(t *testing.T, run *sampleRun, surface *appplugin.WebviewSurface,
	page *sampleWebviewPage, method, payload string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		payload string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		answer, err := surface.Bridge(ctx, page.Identity(), surface.Origin(), method, payload)
		done <- result{payload: answer, err: err}
	}()
	session, ok := run.manager.Container().NativeUI(sampleRef(run))
	if !ok {
		t.Fatal("the sample has no live UI session")
	}
	deadline := time.Now().Add(2 * time.Second)
	for session.PendingEvents() == 0 {
		select {
		case answer := <-done:
			t.Fatalf("bridge %s returned before the poll: %v", method, answer.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the bridge request never reached the guest event queue")
		}
		time.Sleep(time.Millisecond)
	}
	if err := run.manager.Container().PollUI(ctx, sampleRef(run)); err != nil {
		t.Fatalf("PollUI: %v", err)
	}
	select {
	case answer := <-done:
		if answer.err != nil {
			t.Fatalf("bridge %s: %v", method, answer.err)
		}
		return answer.payload
	case <-ctx.Done():
		t.Fatalf("bridge %s did not answer; feedback=%v", method, run.feedback.snapshot())
		return ""
	}
}

func requireSampleAsset(t *testing.T, run *sampleRun, path, wantType, wantText string) {
	t.Helper()
	asset, err := run.manager.Container().ServeWebview(webviewSampleID, webviewSampleOrigin, path)
	if err != nil {
		t.Fatalf("ServeWebview(%s): %v", path, err)
	}
	if asset.ContentType != wantType {
		t.Fatalf("%s content type = %q, want %q", path, asset.ContentType, wantType)
	}
	if asset.Headers["Content-Security-Policy"] != appplugin.WebviewCSP {
		t.Fatalf("%s CSP = %q", path, asset.Headers["Content-Security-Policy"])
	}
	if asset.Headers["X-Content-Type-Options"] != "nosniff" {
		t.Fatalf("%s nosniff = %q", path, asset.Headers["X-Content-Type-Options"])
	}
	if asset.Headers["Set-Cookie"] != "" {
		t.Fatalf("%s set a cookie", path)
	}
	if len(asset.Body) == 0 || !strings.Contains(string(asset.Body), wantText) {
		t.Fatalf("%s body does not contain %q:\n%s", path, wantText, asset.Body)
	}
}

func TestWebviewSampleServesBothSurfacesThroughTheVerifiedBridge(t *testing.T) {
	run := startWebviewSample(t, true)
	openWebviewSampleShell(t, run, webviewSampleWebView, webviewSampleEmbed)

	// The standalone web View and the embedded node both opened a page, and
	// both use the one per-plugin origin.
	surfaces := sampleSurfaces(t, run)
	if len(surfaces) != 2 {
		t.Fatalf("surfaces = %+v", surfaces)
	}
	for _, surface := range surfaces {
		if surface.Origin() != webviewSampleOrigin {
			t.Fatalf("%s origin = %q", surface.Entry(), surface.Origin())
		}
	}
	webSurface := findSampleSurface(t, run, "ui/index.html", false)
	embedSurface := findSampleSurface(t, run, "ui/embed.html", true)
	if webSurface.URL() != webviewSampleOrigin+"/ui/index.html" {
		t.Fatalf("web URL = %q", webSurface.URL())
	}

	// The transport serves only packaged UI files with the strict policy and
	// static types. Anything outside ui/ is not reachable from the origin.
	requireSampleAsset(t, run, "/ui/index.html", "text/html; charset=utf-8",
		"Sandboxed web View")
	requireSampleAsset(t, run, "/ui/embed.html", "text/html; charset=utf-8",
		"Embedded webview node")
	requireSampleAsset(t, run, "/ui/app.js", "text/javascript; charset=utf-8",
		domainplugin.WebviewBridgePath)
	requireSampleAsset(t, run, "/ui/style.css", "text/css; charset=utf-8", "CanvasText")
	for _, path := range []string{"/ui/index.html", "/ui/embed.html"} {
		asset, err := run.manager.Container().ServeWebview(webviewSampleID, webviewSampleOrigin, path)
		if err != nil {
			t.Fatalf("ServeWebview(%s): %v", path, err)
		}
		if !strings.Contains(string(asset.Body), "connect-src 'self'") {
			t.Fatalf("%s does not allow the same-origin bridge in its CSP meta", path)
		}
	}
	for _, path := range []string{"/ui/app.js", "/ui/embed.js"} {
		asset, err := run.manager.Container().ServeWebview(webviewSampleID, webviewSampleOrigin, path)
		if err != nil {
			t.Fatalf("ServeWebview(%s): %v", path, err)
		}
		if strings.Contains(string(asset.Body), "window.mygo") {
			t.Fatalf("%s relies on MyGo's injected page bridge", path)
		}
		if !strings.Contains(string(asset.Body), domainplugin.WebviewBridgePath) {
			t.Fatalf("%s does not call the same-origin bridge", path)
		}
	}
	if _, err := run.manager.Container().ServeWebview(webviewSampleID,
		webviewSampleOrigin, "/plugin.wasm"); !errors.Is(err, appplugin.ErrWebviewAssetPath) {
		t.Fatalf("serving outside ui/ = %v", err)
	}
	if _, err := run.manager.Container().ServeWebview(webviewSampleID,
		"natcatty-plugin://acme.other", "/ui/index.html"); !errors.Is(err, appplugin.ErrWebviewCrossOrigin) {
		t.Fatalf("foreign origin = %v", err)
	}
	if _, err := run.manager.Container().ServeWebview("acme.other",
		"natcatty-plugin://acme.other", "/ui/index.html"); !errors.Is(err, appplugin.ErrWebviewUnavailable) {
		t.Fatalf("other plugin origin = %v", err)
	}

	// The real guest answers the standalone page through the real bridge and
	// the real poll export.
	webPage := run.port.pageFor(t, "ui/index.html", false)
	if answer := callSampleBridge(t, run, webSurface, webPage, "greet", "Ada"); answer != webviewSampleGreeting {
		t.Fatalf("greet answer = %q", answer)
	}
	report := callSampleBridge(t, run, webSurface, webPage, "report", "")
	for _, want := range []string{"tree=ready", "handled=1", "denied=0"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report is missing %q:\n%s", want, report)
		}
	}

	// The embedded node's page uses the same allowlist and backend.
	embedPage := run.port.pageFor(t, "ui/embed.html", true)
	if answer := callSampleBridge(t, run, embedSurface, embedPage, "greet", "embedded page"); answer !=
		"Hello, embedded page! Served by acme.webview-view.\n" {
		t.Fatalf("embedded greet answer = %q", answer)
	}

	// The page is not a principal: a method outside the fixed allowlist, a
	// caller that is not the page, and an origin the page left are all denied
	// before any event is queued.
	ctx := context.Background()
	if _, err := webSurface.Bridge(ctx, webPage.Identity(), webSurface.Origin(), "delete", ""); !errors.Is(err, appplugin.ErrWebviewMethod) {
		t.Fatalf("unlisted method = %v", err)
	}
	if _, err := webSurface.Bridge(ctx, new(int), webSurface.Origin(), "greet", ""); !errors.Is(err, appplugin.ErrWebviewPage) {
		t.Fatalf("foreign caller = %v", err)
	}
	if _, err := webSurface.Bridge(ctx, webPage.Identity(), "natcatty-plugin://acme.other", "greet", ""); !errors.Is(err, appplugin.ErrWebviewOrigin) {
		t.Fatalf("left origin = %v", err)
	}

	// Host locale and direction reach every live page.
	if !run.manager.Services().SetLocale("ar") {
		t.Fatal("SetLocale(ar) reported no change")
	}
	for _, page := range []*sampleWebviewPage{webPage, embedPage} {
		page.mu.Lock()
		locale, direction := page.locale, page.direction
		page.mu.Unlock()
		if locale != "ar" || direction != appplugin.UIDirectionRTL {
			t.Fatalf("page locale = %q, %q", locale, direction)
		}
	}
}

func TestPluginWebviewSchemeServesOnlySandboxAssets(t *testing.T) {
	run := startWebviewSample(t, false)
	handler := &pluginWebviewScheme{manager: func() *appplugin.Manager { return run.manager }}
	serve := func(method, target, origin string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, target, nil)
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		request.Header.Set("Cookie", "session=host-browser")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	response := serve(http.MethodGet, webviewSampleOrigin+"/ui/index.html", webviewSampleOrigin)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d", response.Code)
	}
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options"} {
		if response.Header().Get(header) == "" {
			t.Fatalf("GET is missing %s", header)
		}
	}
	for _, header := range []string{"Access-Control-Allow-Origin", "Set-Cookie"} {
		if response.Header().Get(header) != "" {
			t.Fatalf("GET leaked %s", header)
		}
	}
	if !strings.Contains(response.Body.String(), "Sandboxed web View") {
		t.Fatalf("GET body:\n%s", response.Body.String())
	}

	if response := serve(http.MethodHead, webviewSampleOrigin+"/ui/app.js", webviewSampleOrigin); response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("HEAD = %d, %d body bytes", response.Code, response.Body.Len())
	}
	if response := serve(http.MethodPost, webviewSampleOrigin+"/ui/index.html", webviewSampleOrigin); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", response.Code)
	}
	if response := serve(http.MethodGet, webviewSampleOrigin+"/ui/index.html", "natcatty-plugin://acme.other"); response.Code != http.StatusForbidden {
		t.Fatalf("foreign origin status = %d", response.Code)
	}
	if response := serve(http.MethodGet, webviewSampleOrigin+"/plugin.wasm", webviewSampleOrigin); response.Code != http.StatusNotFound {
		t.Fatalf("outside ui/ status = %d", response.Code)
	}
}

func TestWebviewSamplePageIsIndependentOfTheNativeGrant(t *testing.T) {
	run := startWebviewSample(t, false)
	openWebviewSampleShell(t, run, webviewSampleWebView)

	webSurface := findSampleSurface(t, run, "ui/index.html", false)
	webPage := run.port.pageFor(t, "ui/index.html", false)
	if answer := callSampleBridge(t, run, webSurface, webPage, "greet", "Ada"); answer != webviewSampleGreeting {
		t.Fatalf("greet answer = %q", answer)
	}
	report := callSampleBridge(t, run, webSurface, webPage, "report", "")
	if !strings.Contains(report, "tree=denied") {
		t.Fatalf("without ui.native the tree must stay uncommitted:\n%s", report)
	}
}
