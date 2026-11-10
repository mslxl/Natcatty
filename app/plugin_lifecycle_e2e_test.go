package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
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
	appsession "github.com/mslxl/natcatty/application/session"
	appview "github.com/mslxl/natcatty/application/view"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/internal/testutil"
)

// lifecycleSampleCases are every committed sample. They share one manager and
// store, so a teardown assertion must be scoped to the plugin under test while
// the other samples stay active.
var lifecycleSampleCases = []struct {
	directory string
	id        string
	provider  bool
	data      bool
}{
	{"counter-view", "acme.counter-view", false, false},
	{"data-provider", "acme.data", false, true},
	{"gallery-view", "acme.gallery-view", false, false},
	{"hello-command", "acme.hello", false, false},
	{"hello-command-c", "acme.hello-c", false, false},
	{"hello-command-tinygo", "acme.hello-tinygo", false, false},
	{"hello-command-zig", "acme.hello-zig", false, false},
	{"host-services", "acme.host-services", false, false},
	{"language-provider", "acme.language-provider", false, false},
	{"loopback-provider", "acme.loopback", true, false},
	{"mcp-tools", "acme.mcp-tools", false, false},
	{"service-consumer", "acme.service-consumer", false, false},
	{"service-provider", "acme.service-provider", false, false},
	{"webview-view", "acme.webview-view", false, false},
	{"workspace-services", "acme.workspace-services", false, false},
}

// lifecycleEntries walks the files a sample package ships: the manifest,
// module, i18n catalogs, view UI assets, and other manifest-referenced assets.
// Source, build scripts, and tests are not package content.
func lifecycleEntries(t *testing.T, directory string) map[string][]byte {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", directory)
	entries := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		switch {
		case relative == "plugin.toml" || relative == "plugin.wasm":
		case strings.HasPrefix(relative, "i18n/"),
			strings.HasPrefix(relative, "ui/"),
			strings.HasPrefix(relative, "assets/"):
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries[relative] = data
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", directory, err)
	}
	return entries
}

// lifecycleArchiveData builds a deterministic package. A non-empty version
// rewrites the top-level manifest version, which is how the update tests serve
// a newer release of the same sample.
func lifecycleArchiveData(t *testing.T, directory, version string) []byte {
	t.Helper()
	entries := lifecycleEntries(t, directory)
	if version != "" && version != "1.0.0" {
		manifest := string(entries["plugin.toml"])
		updated := strings.Replace(manifest, `version = "1.0.0"`,
			`version = "`+version+`"`, 1)
		if updated == manifest {
			t.Fatalf("%s: manifest has no 1.0.0 version to update", directory)
		}
		entries["plugin.toml"] = []byte(updated)
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
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
	return buffer.Bytes()
}

func lifecycleArchive(t *testing.T, directory string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), directory+".natplug")
	if err := os.WriteFile(archivePath, lifecycleArchiveData(t, directory, ""), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return archivePath
}

// lifecycleRegistry serves the committed samples at version 1.0.0 and
// 1.1.0. The newer package is byte-for-byte the same module with only the
// manifest version advanced, so update and rollback exercise the real install
// path without inventing a different plugin behavior.
type lifecycleRegistry struct {
	plugins map[string]domainplugin.RegistryPlugin
	bodies  map[string][]byte
}

func newLifecycleRegistry(t *testing.T, cases []struct {
	directory string
	id        string
	provider  bool
	data      bool
}) *lifecycleRegistry {
	t.Helper()
	registry := &lifecycleRegistry{
		plugins: make(map[string]domainplugin.RegistryPlugin),
		bodies:  make(map[string][]byte),
	}
	for _, testCase := range cases {
		directory := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", testCase.directory)
		manifest, err := (adapterpluginmanifest.Source{}).Load(directory)
		if err != nil {
			t.Fatalf("load %s manifest: %v", testCase.directory, err)
		}
		plugin := domainplugin.RegistryPlugin{ID: testCase.id, Name: manifest.Name}
		for _, version := range []string{"1.0.0", "1.1.0"} {
			body := lifecycleArchiveData(t, testCase.directory, version)
			sum := sha256.Sum256(body)
			plugin.Versions = append(plugin.Versions, domainplugin.RegistryVersion{
				Version: version, Channel: domainplugin.ChannelStable,
				PluginAPI: manifest.PluginAPI, ABI: manifest.ABI,
				Package: "packages/" + testCase.id + "/" + version + ".natplug",
				SHA256:  hex.EncodeToString(sum[:]), Size: int64(len(body)),
				Required: append([]string(nil), manifest.Permissions.Required...),
				Optional: append([]string(nil), manifest.Permissions.Optional...),
			})
			registry.bodies[testCase.id+"@"+version] = body
		}
		registry.plugins[testCase.id] = plugin
	}
	return registry
}

func (r *lifecycleRegistry) Identity() string { return "lifecycle-fixture-registry" }

func (r *lifecycleRegistry) Index(context.Context) (domainplugin.RegistryIndex, error) {
	return domainplugin.RegistryIndex{Schema: 1, Name: "Lifecycle fixtures"}, nil
}

func (r *lifecycleRegistry) Plugin(_ context.Context,
	id string) (domainplugin.RegistryPlugin, error) {
	plugin, ok := r.plugins[id]
	if !ok {
		return domainplugin.RegistryPlugin{}, fmt.Errorf("registry has no plugin %s", id)
	}
	return plugin, nil
}

func (r *lifecycleRegistry) FetchPackage(_ context.Context, id, version string) ([]byte, error) {
	body, ok := r.bodies[id+"@"+version]
	if !ok {
		return nil, fmt.Errorf("registry has no package %s %s", id, version)
	}
	return body, nil
}

type lifecycleMCPTools struct {
	mu    sync.Mutex
	tools map[string]struct{}
}

func (r *lifecycleMCPTools) RegisterMCPTool(ref appplugin.PluginRef,
	tool domainplugin.MCPTool, _ appplugin.ToolCaller) (appplugin.Disposer, error) {
	key := ref.ID + "\x00" + tool.Name
	r.mu.Lock()
	if r.tools == nil {
		r.tools = make(map[string]struct{})
	}
	r.tools[key] = struct{}{}
	r.mu.Unlock()
	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			r.mu.Lock()
			delete(r.tools, key)
			r.mu.Unlock()
		})
		return nil
	}, nil
}

func (r *lifecycleMCPTools) count(pluginID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	prefix := pluginID + "\x00"
	for key := range r.tools {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
}

type lifecycleFeedback struct {
	mu      sync.Mutex
	outputs []string
}

func (f *lifecycleFeedback) add(_ appplugin.PluginRef, output string) {
	f.mu.Lock()
	f.outputs = append(f.outputs, output)
	f.mu.Unlock()
}

func (f *lifecycleFeedback) reset() {
	f.mu.Lock()
	f.outputs = nil
	f.mu.Unlock()
}

func (f *lifecycleFeedback) joined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.outputs, "")
}

type lifecycleHarness struct {
	manager      *appplugin.Manager
	commands     *command.Registry
	surface      *pluginSurface
	views        *appview.Registry
	webview      *sampleWebviewPort
	providerHost *appplugin.ProviderHost
	dataHost     *appplugin.DataProviderHost
	mcp          *lifecycleMCPTools
	feedback     *lifecycleFeedback
	archives     map[string]string
	refs         map[string]appplugin.PluginRef
}

func newLifecycleHarness(t *testing.T) *lifecycleHarness {
	t.Helper()
	return newLifecycleHarnessWithRegistry(t, nil)
}

func newLifecycleHarnessWithRegistry(t *testing.T,
	registry appplugin.Registry) *lifecycleHarness {
	t.Helper()
	store := adapterplugininstall.Open(t.TempDir())
	commands := command.NewRegistry()
	views := appview.NewRegistry()
	t.Cleanup(views.Close)
	surface := newPluginSurface(commands)
	surface.SetViewRegistry(views)
	webview := &sampleWebviewPort{}
	providerHost := appplugin.NewProviderHost(appplugin.ProviderHostOptions{})
	t.Cleanup(providerHost.Close)
	dataHost := appplugin.NewDataProviderHost(appplugin.DataHostOptions{
		Profiles: appplugin.NewMemoryDataProfileStore(),
		Secrets:  appplugin.NewMemoryDataProfileSecrets(),
	})
	t.Cleanup(dataHost.Close)
	mcp := &lifecycleMCPTools{}
	feedback := &lifecycleFeedback{}
	options := []appplugin.ManagerOption{
		appplugin.WithWebviewPort(webview),
		appplugin.WithAuthBroker(&appplugin.MemoryAuthBroker{}),
		appplugin.WithMCPToolRegistrar(mcp),
		appplugin.WithManagerConnectionProviders(providerHost,
			&connectionProviderRegistrar{registry: appsession.NewProviderRegistry()}),
		appplugin.WithManagerDataProviders(dataHost),
		appplugin.WithManagerFeedback(feedback.add),
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
	return &lifecycleHarness{
		manager: manager, commands: commands, surface: surface, views: views,
		webview: webview, providerHost: providerHost, dataHost: dataHost, mcp: mcp,
		feedback: feedback, archives: make(map[string]string),
		refs: make(map[string]appplugin.PluginRef),
	}
}

// archive builds a sample package once. Reinstalling the same sample must use
// the same file path; a different source path is an explicit install-source
// change that the manager rightly asks the user to confirm.
func (h *lifecycleHarness) archive(t *testing.T, directory string) string {
	t.Helper()
	if path, ok := h.archives[directory]; ok {
		return path
	}
	path := lifecycleArchive(t, directory)
	h.archives[directory] = path
	return path
}

func (h *lifecycleHarness) install(t *testing.T, testCase struct {
	directory string
	id        string
	provider  bool
	data      bool
}) {
	t.Helper()
	record, err := h.manager.InstallFile(context.Background(),
		h.archive(t, testCase.directory))
	if err != nil {
		t.Fatalf("InstallFile(%s): %v", testCase.directory, err)
	}
	if record.ID != testCase.id {
		t.Fatalf("installed %s as %s", testCase.directory, record.ID)
	}
	h.remember(record)
}

// remember stores the exact reference of an installed plugin. Fiber lookup
// matches ID, Source, and Development, so a reference built from only the ID
// would never find the fiber and could hide a leaked UI session.
func (h *lifecycleHarness) remember(record domainplugin.InstallRecord) {
	h.refs[record.ID] = appplugin.PluginRef{
		ID: record.ID, Name: record.Name, Version: record.Version,
		Source: record.Source,
	}
}

func (h *lifecycleHarness) installFromRegistry(t *testing.T, id, version string) {
	t.Helper()
	record, err := h.manager.InstallFromRegistry(context.Background(), id, version, nil)
	if err != nil {
		t.Fatalf("InstallFromRegistry(%s): %v", id, err)
	}
	if record.ID != id {
		t.Fatalf("installed %s as %s", id, record.ID)
	}
	h.remember(record)
}

func (h *lifecycleHarness) enable(t *testing.T, id string) {
	t.Helper()
	view, err := h.manager.Review(context.Background(), id)
	if err != nil {
		t.Fatalf("Review(%s): %v", id, err)
	}
	decisions := make([]domainplugin.PermissionDecision, 0, len(view.Required)+len(view.Optional))
	for _, row := range view.Required {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: row.Permission, Scope: row.Scope, Allow: true,
		})
	}
	for _, row := range view.Optional {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: row.Permission, Scope: row.Scope, Allow: true,
		})
	}
	if err := h.manager.Enable(context.Background(), id, decisions); err != nil {
		t.Fatalf("Enable(%s): %v", id, err)
	}
}

func (h *lifecycleHarness) contributions(t *testing.T, id string) (registered, mapped, menus, keys int) {
	t.Helper()
	prefix := "plugin." + id + "."
	for _, commandID := range h.commands.IDs() {
		if strings.HasPrefix(string(commandID), prefix) {
			registered++
		}
	}
	h.surface.mu.Lock()
	for _, entry := range h.surface.commands {
		if entry.ref.ID == id {
			mapped++
		}
	}
	for _, entry := range h.surface.menus {
		if entry.ref.ID == id {
			menus++
		}
	}
	for _, entry := range h.surface.keys {
		if strings.HasPrefix(string(entry.command), prefix) {
			keys++
		}
	}
	h.surface.mu.Unlock()
	return registered, mapped, menus, keys
}

func (h *lifecycleHarness) viewCount(id string) int {
	count := 0
	for _, contribution := range h.views.Contributions() {
		if contribution.Owner.PluginID == id {
			count++
		}
	}
	return count
}

func (h *lifecycleHarness) serviceCount(id string) int {
	count := 0
	for _, row := range h.manager.Container().Services() {
		if row.PluginID == id {
			count++
		}
	}
	return count
}

func (h *lifecycleHarness) providerFiberCount(t *testing.T, id string) int {
	t.Helper()
	count := 0
	for _, fiber := range h.manager.Container().Fibers() {
		if fiber.Ref().ID == id {
			count++
		}
	}
	return count
}

// assertQuiescent proves no contribution, handle, service, provider claim, or
// fiber for id survives a lifecycle operation. Other samples are untouched.
func (h *lifecycleHarness) assertQuiescent(t *testing.T, testCase struct {
	directory string
	id        string
	provider  bool
	data      bool
}) {
	t.Helper()
	id := testCase.id
	if fibers := h.providerFiberCount(t, id); fibers != 0 {
		t.Fatalf("%s: %d fibers survived", id, fibers)
	}
	registered, mapped, menus, keys := h.contributions(t, id)
	if registered != 0 || mapped != 0 || menus != 0 || keys != 0 {
		t.Fatalf("%s: contributions survived registry=%d surface=%d menus=%d keys=%d",
			id, registered, mapped, menus, keys)
	}
	if views := h.viewCount(id); views != 0 {
		t.Fatalf("%s: %d view contributions survived", id, views)
	}
	for _, instance := range h.views.Instances() {
		if instance.Owner().PluginID == id {
			t.Fatalf("%s: view instance %q survived", id, instance.ID())
		}
	}
	if services := h.serviceCount(id); services != 0 {
		t.Fatalf("%s: %d services survived", id, services)
	}
	if tools := h.mcp.count(id); tools != 0 {
		t.Fatalf("%s: %d MCP tools survived", id, tools)
	}
	ref, known := h.refs[id]
	if !known {
		t.Fatalf("%s: no installed reference was recorded", id)
	}
	if _, ok := h.manager.Container().NativeUI(ref); ok {
		t.Fatalf("%s: the Native UI session survived", id)
	}
	if testCase.provider && h.providerHost.Claimed(sampleProviderProtocol) {
		t.Fatalf("%s: the connection provider claim survived", id)
	}
	if testCase.data {
		if _, ok := h.dataHost.Lookup(dataSampleProviderID); ok {
			t.Fatalf("%s: the data provider survived", id)
		}
	}
}

type lifecycleContributions struct {
	registered int
	mapped     int
	menus      int
	keys       int
	views      int
	services   int
	tools      int
}

func (h *lifecycleHarness) snapshot(t *testing.T, id string) lifecycleContributions {
	t.Helper()
	registered, mapped, menus, keys := h.contributions(t, id)
	return lifecycleContributions{
		registered: registered, mapped: mapped, menus: menus, keys: keys,
		views: h.viewCount(id), services: h.serviceCount(id), tools: h.mcp.count(id),
	}
}

func (h *lifecycleHarness) assertSnapshot(t *testing.T, id string,
	want lifecycleContributions) {
	t.Helper()
	if got := h.snapshot(t, id); got != want {
		t.Fatalf("%s: contributions after lifecycle change = %+v, want %+v", id, got, want)
	}
}

func (h *lifecycleHarness) assertActive(t *testing.T, testCase struct {
	directory string
	id        string
	provider  bool
	data      bool
}) {
	t.Helper()
	id := testCase.id
	if fibers := h.providerFiberCount(t, id); fibers != 1 {
		t.Fatalf("%s: active fibers = %d, want 1", id, fibers)
	}
	if testCase.provider && !h.providerHost.Claimed(sampleProviderProtocol) {
		t.Fatalf("%s: did not claim its connection protocol", id)
	}
	if testCase.data {
		if _, ok := h.dataHost.Lookup(dataSampleProviderID); !ok {
			t.Fatalf("%s: did not register its data provider", id)
		}
	}
}

// TestEverySampleTearsDownAcrossRepeatedLifecycleCycles installs every
// committed sample together, then disables, revokes a required permission,
// and uninstalls each one in turn. Every operation must leave no command,
// menu, keybinding, view, service, MCP tool, Native UI handle, provider claim,
// data provider, or fiber behind, including after repeated cycles.
func TestEverySampleTearsDownAcrossRepeatedLifecycleCycles(t *testing.T) {
	ctx := context.Background()
	harness := newLifecycleHarness(t)
	for _, testCase := range lifecycleSampleCases {
		harness.install(t, testCase)
		harness.enable(t, testCase.id)
	}
	for _, testCase := range lifecycleSampleCases {
		harness.assertActive(t, testCase)
	}

	for _, testCase := range lifecycleSampleCases {
		t.Run(testCase.directory, func(t *testing.T) {
			for cycle := 0; cycle < 2; cycle++ {
				if err := harness.manager.Disable(ctx, testCase.id); err != nil {
					t.Fatalf("Disable cycle %d: %v", cycle, err)
				}
				harness.assertQuiescent(t, testCase)
				harness.enable(t, testCase.id)
				harness.assertActive(t, testCase)
			}

			detail, ok, err := harness.manager.Detail(ctx, testCase.id)
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
				t.Fatalf("%s declares no required permission to revoke", testCase.id)
			}
			if err := harness.manager.Revoke(ctx, testCase.id, required); err != nil {
				t.Fatalf("Revoke: %v", err)
			}
			harness.assertQuiescent(t, testCase)
			harness.enable(t, testCase.id)
			harness.assertActive(t, testCase)

			if err := harness.manager.Uninstall(ctx, testCase.id, true); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			harness.assertQuiescent(t, testCase)
			if _, ok, err := harness.manager.Detail(ctx, testCase.id); err != nil || ok {
				t.Fatalf("uninstall left a record: ok=%v err=%v", ok, err)
			}
			if _, err := harness.manager.InstallFile(ctx,
				harness.archive(t, testCase.directory)); err != nil {
				t.Fatalf("reinstall: %v", err)
			}
			harness.enable(t, testCase.id)
			harness.assertActive(t, testCase)
		})
	}
}

// TestSampleCommandMenuRunsThroughInstallAndConsent installs the committed
// command sample, reviews and grants its declared permissions, and invokes the
// command from the contributed menu item. Disabling the sample must remove the
// menu and palette command together, and re-enabling must restore both.
func TestSampleCommandMenuRunsThroughInstallAndConsent(t *testing.T) {
	ctx := context.Background()
	harness := newLifecycleHarness(t)
	sample := struct {
		directory string
		id        string
		provider  bool
		data      bool
	}{directory: "hello-command", id: "acme.hello"}

	harness.install(t, sample)
	review, err := harness.manager.Review(ctx, sample.id)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	declared := false
	for _, row := range review.Required {
		if row.Permission == domainplugin.PermissionMenus {
			declared = true
		}
	}
	if !declared {
		t.Fatalf("review did not ask consent for the menu permission: %+v", review.Required)
	}
	harness.enable(t, sample.id)

	registered, mapped, menus, keys := harness.contributions(t, sample.id)
	if registered != 1 || mapped != 1 || menus != 1 || keys != 0 {
		t.Fatalf("contributions = registered=%d mapped=%d menus=%d keys=%d",
			registered, mapped, menus, keys)
	}
	harness.feedback.reset()
	invoked := false
	for _, item := range harness.surface.MenuItems() {
		if item == nil || item.Label != "Say Hello" {
			continue
		}
		if item.ID != "plugin-menu-1" {
			t.Fatalf("menu item id = %q", item.ID)
		}
		item.Click(item, nil)
		invoked = true
		break
	}
	if !invoked {
		t.Fatal("the contributed menu item is missing")
	}
	if got := harness.feedback.joined(); !strings.HasPrefix(got, "hello from plugin now=") {
		t.Fatalf("menu command output = %q", got)
	}

	if err := harness.manager.Disable(ctx, sample.id); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	harness.assertQuiescent(t, sample)
	if items := harness.surface.MenuItems(); len(items) != 0 {
		t.Fatalf("menu items after Disable = %d", len(items))
	}
	if ids := harness.commands.IDs(); len(ids) != 0 {
		t.Fatalf("registered commands after Disable = %v", ids)
	}

	harness.enable(t, sample.id)
	harness.assertActive(t, sample)
	if registered, mapped, menus, _ := harness.contributions(t, sample.id); registered != 1 ||
		mapped != 1 || menus != 1 {
		t.Fatalf("contributions after re-enable = registered=%d mapped=%d menus=%d",
			registered, mapped, menus)
	}
	if err := harness.manager.Uninstall(ctx, sample.id, true); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	harness.assertQuiescent(t, sample)
}

// TestEverySampleUpdatesAndRollsBackWithoutStaleState installs every sample
// from a fixture registry, applies a newer version, then rolls back to the
// retained one. The registered commands, menus, keybindings, views, services,
// MCP tools, provider claims, and data providers must be identical before and
// after each version change, which fails if an old contribution, handle, or
// service survives the reload.
func TestEverySampleUpdatesAndRollsBackWithoutStaleState(t *testing.T) {
	ctx := context.Background()
	registry := newLifecycleRegistry(t, lifecycleSampleCases)
	harness := newLifecycleHarnessWithRegistry(t, registry)
	before := make(map[string]lifecycleContributions, len(lifecycleSampleCases))
	for _, testCase := range lifecycleSampleCases {
		harness.installFromRegistry(t, testCase.id, "1.0.0")
		harness.enable(t, testCase.id)
		harness.assertActive(t, testCase)
		before[testCase.id] = harness.snapshot(t, testCase.id)
	}

	for _, testCase := range lifecycleSampleCases {
		t.Run(testCase.directory, func(t *testing.T) {
			plan, err := harness.manager.PlanUpdate(ctx, testCase.id)
			if err != nil {
				t.Fatalf("PlanUpdate: %v", err)
			}
			if plan.From != "1.0.0" || plan.To != "1.1.0" {
				t.Fatalf("plan = %+v, want 1.0.0 -> 1.1.0", plan)
			}
			updated, err := harness.manager.ApplyUpdate(ctx, plan, nil)
			if err != nil {
				t.Fatalf("ApplyUpdate: %v", err)
			}
			if updated.Version != "1.1.0" || !updated.Enabled {
				t.Fatalf("updated record = %+v, want active 1.1.0", updated)
			}
			harness.assertActive(t, testCase)
			harness.assertSnapshot(t, testCase.id, before[testCase.id])
			detail, ok, err := harness.manager.Detail(ctx, testCase.id)
			if err != nil || !ok || !detail.HasPrevious {
				t.Fatalf("after update Detail = %+v, ok=%v err=%v", detail, ok, err)
			}
			if _, err := harness.manager.PlanUpdate(ctx, testCase.id); err == nil {
				t.Fatal("PlanUpdate found a second update after applying 1.1.0")
			}

			restored, err := harness.manager.Rollback(ctx, testCase.id, nil)
			if err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if restored.Version != "1.0.0" || !restored.Enabled {
				t.Fatalf("rolled back record = %+v, want active 1.0.0", restored)
			}
			harness.assertActive(t, testCase)
			harness.assertSnapshot(t, testCase.id, before[testCase.id])

			if err := harness.manager.Uninstall(ctx, testCase.id, true); err != nil {
				t.Fatalf("Uninstall after rollback: %v", err)
			}
			harness.assertQuiescent(t, testCase)
		})
	}
}
