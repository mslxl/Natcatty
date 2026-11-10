package plugin_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/fake"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

type manifestSource struct {
	manifests map[string]domainplugin.Manifest
	err       error
}

func (s manifestSource) Load(directory string) (domainplugin.Manifest, error) {
	if s.err != nil {
		return domainplugin.Manifest{}, s.err
	}
	manifest, ok := s.manifests[filepath.Base(directory)]
	if !ok {
		return domainplugin.Manifest{}, fmt.Errorf("no manifest for %s", directory)
	}
	return manifest, nil
}

type registration struct {
	kind string
	id   string
	run  appplugin.Handler
	ref  appplugin.PluginRef
}

type registrar struct {
	mu          sync.Mutex
	regist      []registration
	failMenu    bool
	disposed    int
	disposeHook func(registration)
}

func (r *registrar) add(item registration) (appplugin.Disposer, error) {
	r.mu.Lock()
	r.regist = append(r.regist, item)
	r.mu.Unlock()
	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			r.mu.Lock()
			for i := range r.regist {
				if r.regist[i].kind == item.kind && r.regist[i].id == item.id {
					r.regist = append(r.regist[:i], r.regist[i+1:]...)
					break
				}
			}
			r.disposed++
			hook := r.disposeHook
			r.mu.Unlock()
			if hook != nil {
				hook(item)
			}
		})
		return nil
	}, nil
}

func (r *registrar) RegisterCommand(ref appplugin.PluginRef, command domainplugin.Command,
	run appplugin.Handler) (appplugin.Disposer, error) {
	return r.add(registration{
		kind: "command",
		id:   domainplugin.QualifiedCommandID(ref.ID, command.ID),
		run:  run,
		ref:  ref,
	})
}

func (r *registrar) RegisterMenu(ref appplugin.PluginRef,
	menu domainplugin.Menu) (appplugin.Disposer, error) {
	r.mu.Lock()
	fail := r.failMenu
	r.mu.Unlock()
	if fail {
		return nil, errors.New("menu registration refused")
	}
	return r.add(registration{
		kind: "menu",
		id:   domainplugin.QualifiedCommandID(ref.ID, menu.Command),
		ref:  ref,
	})
}

func (r *registrar) RegisterKeybinding(ref appplugin.PluginRef,
	binding domainplugin.Keybinding) (appplugin.Disposer, error) {
	return r.add(registration{
		kind: "keybinding",
		id:   domainplugin.QualifiedCommandID(ref.ID, binding.Command),
		ref:  ref,
	})
}

// viewRegistrar is a registrar that also serves View Contributions.
type viewRegistrar struct {
	*registrar
	mu       sync.Mutex
	views    []domainplugin.ViewContribution
	refs     []appplugin.PluginRef
	disposed int
}

func (r *viewRegistrar) RegisterView(ref appplugin.PluginRef,
	declaration domainplugin.ViewContribution) (appplugin.Disposer, error) {
	r.mu.Lock()
	r.views = append(r.views, declaration)
	r.refs = append(r.refs, ref)
	r.mu.Unlock()
	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			r.mu.Lock()
			for i := range r.views {
				if r.views[i].ID == declaration.ID && r.refs[i].ID == ref.ID {
					r.views = append(r.views[:i], r.views[i+1:]...)
					r.refs = append(r.refs[:i], r.refs[i+1:]...)
					break
				}
			}
			r.disposed++
			r.mu.Unlock()
		})
		return nil
	}, nil
}

func (r *viewRegistrar) registered() []domainplugin.ViewContribution {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domainplugin.ViewContribution(nil), r.views...)
}

func (r *registrar) commands() map[string]appplugin.Handler {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]appplugin.Handler)
	for _, item := range r.regist {
		if item.kind == "command" {
			out[item.id] = item.run
		}
	}
	return out
}

func (r *registrar) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.regist)
}

func validManifest(id string) domainplugin.Manifest {
	return domainplugin.Manifest{
		ManifestVersion: 1,
		ID:              id,
		Name:            "Hello",
		Version:         "1.0.0",
		PluginAPI:       "^0.1",
		ABI:             1,
		DefaultLocale:   "en",
		Wasm: domainplugin.Wasm{
			Path:         "plugin.wasm",
			SHA256:       strings.Repeat("a", 64),
			MemoryMaxMiB: 64,
		},
		Contributions: domainplugin.Contributions{
			Commands: []domainplugin.Command{
				{ID: "greet", Title: "Say Hello", Keywords: []string{"hello"}},
			},
			Menus:       []domainplugin.Menu{{Command: "greet", Label: "Say Hello"}},
			Keybindings: []domainplugin.Keybinding{{Command: "greet", Accelerator: "CmdOrCtrl+Shift+G"}},
		},
	}
}

func pluginDir(t *testing.T, name string, manifest domainplugin.Manifest) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newTestContainer(t *testing.T, manifest domainplugin.Manifest, dir string,
	registrar appplugin.ContributionRegistrar, opts ...appplugin.Option) *appplugin.Container {
	t.Helper()
	options := append([]appplugin.Option{
		appplugin.WithFeedback(func(appplugin.PluginRef, string) {}),
	}, opts...)
	return appplugin.NewContainer(fake.New(),
		manifestSource{manifests: map[string]domainplugin.Manifest{filepath.Base(dir): manifest}},
		registrar, options...)
}

func TestLoadDevelopmentReachesActiveAndRegistersContributions(t *testing.T) {
	manifest := validManifest("acme.hello")
	dir := pluginDir(t, "hello", manifest)
	registrar := &registrar{}
	var outputs []string
	container := newTestContainer(t, manifest, dir, registrar,
		appplugin.WithFeedback(func(_ appplugin.PluginRef, text string) {
			outputs = append(outputs, text)
		}))
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	if fiber.State() != domainplugin.StateActive {
		t.Fatalf("state = %s, want ACTIVE (%s)", fiber.State(), fiber.Reason())
	}
	if !fiber.Development() {
		t.Fatal("a development-directory plugin is not labelled development")
	}
	if got := string(fiber.ID()); got != "dev/acme.hello#1" {
		t.Fatalf("fiber id = %q", got)
	}
	if registrar.count() != 3 {
		t.Fatalf("registrations = %d, want command, menu, and keybinding", registrar.count())
	}
	commands := registrar.commands()
	run, ok := commands["plugin.acme.hello.greet"]
	if !ok {
		t.Fatalf("commands = %v, want plugin.acme.hello.greet", commands)
	}
	if err := run(context.Background()); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(outputs) != 1 || outputs[0] != "hello from plugin" {
		t.Fatalf("visible output = %v", outputs)
	}
	if logs := fiber.Logs(); !contains(logs, "hello init") {
		t.Fatalf("logs = %v, want init log", logs)
	}
}

// TestLoadDevelopmentUsesTheCatalogSource proves activation loads the package
// catalogs and surfaces a catalog error instead of activating a fiber.
func TestLoadDevelopmentUsesTheCatalogSource(t *testing.T) {
	manifest := validManifest("acme.i18n")
	dir := pluginDir(t, "i18n", manifest)
	source := &stubCatalogSource{catalogs: i18nCatalogs()}
	container := newTestContainer(t, manifest, dir, &registrar{},
		appplugin.WithCatalogSource(source))
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("catalog source calls = %d, want 1", source.calls)
	}
	resolution := fiber.Catalogs().Resolve("en", "greet", "")
	if resolution.Text != "Hello, {name}!" || resolution.Stage != domainplugin.StageExact {
		t.Fatalf("loaded catalog resolution = %+v", resolution)
	}
	if err := container.Unload(context.Background(), fiber.ID()); err != nil {
		t.Fatalf("Unload: %v", err)
	}

	withoutCatalog := validManifest("acme.nocatalog")
	bare := pluginDir(t, "nocatalog", withoutCatalog)
	failing := &stubCatalogSource{err: &domainplugin.CatalogError{
		Path: "i18n", Problem: `default locale "en" has no catalog`,
	}}
	noCatalogContainer := newTestContainer(t, withoutCatalog, bare, &registrar{},
		appplugin.WithCatalogSource(failing))
	t.Cleanup(func() { _ = noCatalogContainer.Close(context.Background()) })
	if _, err := noCatalogContainer.LoadDevelopment(context.Background(), bare); err == nil ||
		!strings.Contains(err.Error(), `default locale "en" has no catalog`) {
		t.Fatalf("missing catalog load error = %v", err)
	}
}

func viewManifest(id string) domainplugin.Manifest {
	manifest := validManifest(id)
	manifest.Permissions.Required = []string{string(domainplugin.PermissionViews)}
	manifest.Contributions.Views = []domainplugin.ViewContribution{{
		ID:         "counter",
		Title:      "Counter",
		Placements: []domainplugin.Placement{domainplugin.PlacementPane},
		Instances:  domainplugin.InstanceSingleton,
	}}
	return manifest
}

func TestViewContributionsRegisterAndDisposeOnUnload(t *testing.T) {
	manifest := viewManifest("acme.view")
	dir := pluginDir(t, "view", manifest)
	registrar := &viewRegistrar{registrar: &registrar{}}
	container := newTestContainer(t, manifest, dir, registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	views := registrar.registered()
	if len(views) != 1 {
		t.Fatalf("registered views = %+v", views)
	}
	if views[0].ID != "counter" || views[0].Title != "Counter" ||
		views[0].Instances != domainplugin.InstanceSingleton ||
		len(views[0].Placements) != 1 || views[0].Placements[0] != domainplugin.PlacementPane {
		t.Fatalf("view declaration = %+v", views[0])
	}
	if len(registrar.refs) != 1 || registrar.refs[0].ID != "acme.view" {
		t.Fatalf("owner refs = %+v", registrar.refs)
	}
	// The contribution registrations still happen alongside the view.
	if registrar.count() != 3 {
		t.Fatalf("registrations = %d, want command, menu, and keybinding", registrar.count())
	}

	if err := container.Unload(context.Background(), fiber.ID()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	registrar.mu.Lock()
	disposed := registrar.disposed
	registrar.mu.Unlock()
	if disposed != 1 {
		t.Fatalf("view disposers run = %d, want 1", disposed)
	}
}

func TestViewContributionsAreSkippedWithoutAViewSurface(t *testing.T) {
	manifest := viewManifest("acme.headless")
	dir := pluginDir(t, "headless", manifest)
	registrar := &registrar{}
	container := newTestContainer(t, manifest, dir, registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	if _, err := container.LoadDevelopment(context.Background(), dir); err != nil {
		t.Fatalf("LoadDevelopment without a view surface: %v", err)
	}
	if registrar.count() != 3 {
		t.Fatalf("registrations = %d, want the three non-view contributions", registrar.count())
	}
}

func TestMissingModuleFailsTheFiber(t *testing.T) {
	manifest := validManifest("acme.missing")
	dir := filepath.Join(t.TempDir(), "missing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	registrar := &registrar{}
	container := newTestContainer(t, manifest, dir, registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err == nil {
		t.Fatal("LoadDevelopment succeeded without a module")
	}
	if fiber == nil || fiber.State() != domainplugin.StateFailed {
		t.Fatalf("fiber = %+v, want FAILED", fiber)
	}
	if !strings.Contains(fiber.Reason(), "plugin.wasm") {
		t.Fatalf("reason = %q, want the module path", fiber.Reason())
	}
	if registrar.count() != 0 {
		t.Fatalf("failed load registered %d effects", registrar.count())
	}
}

func TestABIMismatchFailsWithReadableReason(t *testing.T) {
	manifest := validManifest("acme.badabi")
	dir := pluginDir(t, "badabi", manifest)
	registrar := &registrar{}
	container := appplugin.NewContainer(
		&fixtureRuntime{name: fake.BadABI},
		manifestSource{manifests: map[string]domainplugin.Manifest{"badabi": manifest}},
		registrar)
	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err == nil {
		t.Fatal("LoadDevelopment succeeded with an ABI mismatch")
	}
	if fiber == nil || fiber.State() != domainplugin.StateFailed {
		t.Fatalf("fiber = %+v, want FAILED", fiber)
	}
	if !strings.Contains(fiber.Reason(), "ABI 2") {
		t.Fatalf("reason = %q, want the ABI version", fiber.Reason())
	}
}

func TestInitFailureFailsTheFiber(t *testing.T) {
	manifest := validManifest("acme.initfail")
	dir := pluginDir(t, "initfail", manifest)
	registrar := &registrar{}
	container := appplugin.NewContainer(
		&fixtureRuntime{name: fake.InitFailure},
		manifestSource{manifests: map[string]domainplugin.Manifest{"initfail": manifest}},
		registrar)
	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err == nil {
		t.Fatal("LoadDevelopment succeeded after a failed init")
	}
	if fiber == nil || fiber.State() != domainplugin.StateFailed {
		t.Fatalf("fiber = %+v, want FAILED", fiber)
	}
	if !strings.Contains(fiber.Reason(), "init failed") {
		t.Fatalf("reason = %q", fiber.Reason())
	}
}

func TestRegistrationFailureRollsBackAndFails(t *testing.T) {
	manifest := validManifest("acme.rollback")
	dir := pluginDir(t, "rollback", manifest)
	registrar := &registrar{failMenu: true}
	container := newTestContainer(t, manifest, dir, registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err == nil {
		t.Fatal("LoadDevelopment succeeded although menu registration failed")
	}
	if fiber == nil || fiber.State() != domainplugin.StateFailed {
		t.Fatalf("fiber = %+v, want FAILED", fiber)
	}
	if registrar.count() != 0 {
		t.Fatalf("partial registration left %d effects", registrar.count())
	}
	if registrar.disposed == 0 {
		t.Fatal("the command registered before the failure was not disposed")
	}
}

func TestUnloadDisposesEveryContributionAndRejectsStaleCalls(t *testing.T) {
	manifest := validManifest("acme.unload")
	dir := pluginDir(t, "unload", manifest)
	registrar := &registrar{}
	container := newTestContainer(t, manifest, dir, registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	commands := registrar.commands()
	run := commands["plugin.acme.unload.greet"]
	if run == nil {
		t.Fatal("plugin command was not registered")
	}
	if err := container.Unload(context.Background(), fiber.ID()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if fiber.State() != domainplugin.StateDisposed {
		t.Fatalf("state = %s, want DISPOSED", fiber.State())
	}
	if registrar.count() != 0 {
		t.Fatalf("registrations after unload = %d", registrar.count())
	}
	if err := run(context.Background()); !errors.Is(err, appplugin.ErrNotActive) {
		t.Fatalf("stale invocation error = %v, want ErrNotActive", err)
	}
	if _, ok := container.Fiber(fiber.ID()); ok {
		t.Fatal("the disposed fiber is still listed")
	}
	if err := container.Unload(context.Background(), fiber.ID()); err == nil {
		t.Fatal("second unload succeeded, want unknown fiber")
	}
}

func TestManifestValidationFailureLeavesFailedFiber(t *testing.T) {
	manifest := validManifest("acme.invalid")
	manifest.ABI = 9
	dir := pluginDir(t, "invalid", manifest)
	registrar := &registrar{}
	container := newTestContainer(t, manifest, dir, registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err == nil {
		t.Fatal("LoadDevelopment accepted an unsupported ABI")
	}
	if fiber == nil || fiber.State() != domainplugin.StateFailed {
		t.Fatalf("fiber = %+v, want FAILED", fiber)
	}
	if !strings.Contains(fiber.Reason(), "abi") {
		t.Fatalf("reason = %q", fiber.Reason())
	}
}

func TestCloseUnloadsEveryFiber(t *testing.T) {
	manifest := validManifest("acme.close")
	dir := pluginDir(t, "close", manifest)
	registrar := &registrar{}
	container := newTestContainer(t, manifest, dir, registrar)
	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	if err := container.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fiber.State() != domainplugin.StateDisposed {
		t.Fatalf("state = %s, want DISPOSED", fiber.State())
	}
	if registrar.count() != 0 {
		t.Fatalf("registrations after Close = %d", registrar.count())
	}
}

func TestTrapRecyclesOnlyTheFailedFiber(t *testing.T) {
	spinManifest := validManifest("acme.spin")
	spinDir := pluginDir(t, "spin", spinManifest)
	goodManifest := validManifest("acme.good")
	goodDir := pluginDir(t, "good", goodManifest)

	var failedEvents int
	var failedReason string
	registrar := &registrar{}
	container := appplugin.NewContainer(
		&fixtureMapRuntime{fixtures: map[string]string{"acme.spin": fake.Trap}},
		manifestSource{manifests: map[string]domainplugin.Manifest{
			"spin": spinManifest,
			"good": goodManifest,
		}},
		registrar,
		appplugin.WithLifecycle(func(_ appplugin.PluginRef, state domainplugin.State, reason string) {
			if state == domainplugin.StateFailed {
				failedEvents++
				failedReason = reason
			}
		}))
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	spinFiber, err := container.LoadDevelopment(context.Background(), spinDir)
	if err != nil {
		t.Fatalf("LoadDevelopment(spin): %v", err)
	}
	goodFiber, err := container.LoadDevelopment(context.Background(), goodDir)
	if err != nil {
		t.Fatalf("LoadDevelopment(good): %v", err)
	}
	if registrar.count() != 6 {
		t.Fatalf("registrations = %d, want 6", registrar.count())
	}

	spinRun := registrar.commands()["plugin.acme.spin.greet"]
	if spinRun == nil {
		t.Fatal("the trapping plugin has no command")
	}
	var trapped *appplugin.TrapError
	if err := spinRun(context.Background()); !errors.As(err, &trapped) {
		t.Fatalf("trapping command = %v, want TrapError", err)
	}
	if spinFiber.State() != domainplugin.StateFailed {
		t.Fatalf("spin state = %s, want FAILED", spinFiber.State())
	}
	if !strings.Contains(spinFiber.Reason(), "trapped") {
		t.Fatalf("spin reason = %q", spinFiber.Reason())
	}
	if failedEvents != 1 || !strings.Contains(failedReason, "trapped") {
		t.Fatalf("lifecycle FAILED notifications = %d (%q), want the trap reason", failedEvents, failedReason)
	}
	if _, ok := container.Fiber(spinFiber.ID()); !ok {
		t.Fatal("the failed fiber was dropped instead of being retained for diagnostics")
	}
	if err := spinRun(context.Background()); !errors.Is(err, appplugin.ErrNotActive) {
		t.Fatalf("stale invocation = %v, want ErrNotActive", err)
	}
	if _, ok := registrar.commands()["plugin.acme.spin.greet"]; ok {
		t.Fatal("the trapping plugin's command survived recycling")
	}

	// The sibling plugin is untouched and still runs.
	if goodFiber.State() != domainplugin.StateActive {
		t.Fatalf("good state = %s, want ACTIVE (%s)", goodFiber.State(), goodFiber.Reason())
	}
	goodRun := registrar.commands()["plugin.acme.good.greet"]
	if goodRun == nil {
		t.Fatal("the sibling command was removed")
	}
	if err := goodRun(context.Background()); err != nil {
		t.Fatalf("sibling invocation: %v", err)
	}
	if registrar.count() != 3 {
		t.Fatalf("registrations after recycling = %d, want the sibling's 3", registrar.count())
	}
}

func TestEffectRegistrationQuotaFailsTheLoad(t *testing.T) {
	manifest := validManifest("acme.many")
	commands := make([]domainplugin.Command, appplugin.MaxEffectRegistrations+1)
	for i := range commands {
		commands[i] = domainplugin.Command{
			ID:    fmt.Sprintf("cmd%04d", i),
			Title: "A command",
		}
	}
	manifest.Contributions = domainplugin.Contributions{Commands: commands}
	dir := pluginDir(t, "many", manifest)
	registrar := &registrar{}
	container := newTestContainer(t, manifest, dir, registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	var limit *appplugin.LimitError
	if !errors.As(err, &limit) {
		t.Fatalf("LoadDevelopment error = %v, want LimitError", err)
	}
	if limit.Resource != "effect registrations" {
		t.Fatalf("limit = %+v, want the effect quota", limit)
	}
	if fiber == nil || fiber.State() != domainplugin.StateFailed {
		t.Fatalf("fiber = %+v, want FAILED", fiber)
	}
	if registrar.count() != 0 {
		t.Fatalf("partial registration left %d effects", registrar.count())
	}
	if registrar.disposed != appplugin.MaxEffectRegistrations+1 {
		t.Fatalf("disposed = %d, want every registration rolled back", registrar.disposed)
	}
}

func TestLogRateQuotaDropsInsteadOfGrowing(t *testing.T) {
	manifest := validManifest("acme.logs")
	dir := pluginDir(t, "logs", manifest)
	registrar := &registrar{}
	now := time.Unix(1_000, 0)
	container := newTestContainer(t, manifest, dir, registrar,
		appplugin.WithClock(func() time.Time { return now }))
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	run := registrar.commands()["plugin.acme.logs.greet"]
	if run == nil {
		t.Fatal("the plugin command was not registered")
	}
	// Init logs once, so 199 of the 200 lines in this window are left.
	for i := 0; i < appplugin.MaxLogsPerSecond+50; i++ {
		if err := run(context.Background()); err != nil {
			t.Fatalf("invocation %d: %v", i, err)
		}
	}
	// One init line plus 250 dispatch lines against a 200-line window.
	wantDropped := 51
	if fiber.DroppedLogs() != wantDropped {
		t.Fatalf("dropped logs = %d, want %d", fiber.DroppedLogs(), wantDropped)
	}
	if len(fiber.Logs()) > appplugin.MaxLogLines {
		t.Fatalf("log ring = %d lines, above the %d cap", len(fiber.Logs()), appplugin.MaxLogLines)
	}

	now = now.Add(time.Second)
	if err := run(context.Background()); err != nil {
		t.Fatalf("invocation after the window: %v", err)
	}
	if fiber.DroppedLogs() != wantDropped {
		t.Fatalf("dropped logs after the window = %d, want %d", fiber.DroppedLogs(), wantDropped)
	}
}

func TestContainerPassesTheEffectiveMemoryMaximum(t *testing.T) {
	manifest := validManifest("acme.memory")
	manifest.Wasm.MemoryMaxMiB = 0
	dir := pluginDir(t, "memory", manifest)
	registrar := &registrar{}
	runtime := newRecordingRuntime()
	container := appplugin.NewContainer(runtime,
		manifestSource{manifests: map[string]domainplugin.Manifest{"memory": manifest}},
		registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	if _, err := container.LoadDevelopment(context.Background(), dir); err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	if got, want := runtime.lastMemoryMax(), int64(domainplugin.DefaultMemoryMaxMiB)<<20; got != want {
		t.Fatalf("memory maximum = %d, want the %d byte default", got, want)
	}

	explicit := validManifest("acme.explicit")
	explicit.Wasm.MemoryMaxMiB = 32
	explicitDir := pluginDir(t, "explicit", explicit)
	container2 := appplugin.NewContainer(runtime,
		manifestSource{manifests: map[string]domainplugin.Manifest{"explicit": explicit}},
		registrar)
	t.Cleanup(func() { _ = container2.Close(context.Background()) })
	if _, err := container2.LoadDevelopment(context.Background(), explicitDir); err != nil {
		t.Fatalf("LoadDevelopment(explicit): %v", err)
	}
	if got, want := runtime.lastMemoryMax(), int64(32)<<20; got != want {
		t.Fatalf("explicit memory maximum = %d, want %d", got, want)
	}
}

type fixtureMapRuntime struct {
	fixtures map[string]string
}

func (r *fixtureMapRuntime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	if name, ok := r.fixtures[spec.Name]; ok {
		spec.Name = name
	}
	return fake.New().Instantiate(ctx, spec, sink)
}

func (r *fixtureMapRuntime) Close(context.Context) error { return nil }

type recordingRuntime struct {
	mu    sync.Mutex
	specs []appplugin.ModuleSpec
	inner *fake.Runtime
}

func newRecordingRuntime() *recordingRuntime {
	return &recordingRuntime{inner: fake.New()}
}

func (r *recordingRuntime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	return r.inner.Instantiate(ctx, spec, sink)
}

func (r *recordingRuntime) Close(ctx context.Context) error { return r.inner.Close(ctx) }

func (r *recordingRuntime) lastMemoryMax() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.specs) == 0 {
		return 0
	}
	return r.specs[len(r.specs)-1].MemoryMaxBytes
}

type fixtureRuntime struct {
	name string
}

func (r *fixtureRuntime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	spec.Name = r.name
	return fake.New().Instantiate(ctx, spec, sink)
}

func (r *fixtureRuntime) Close(context.Context) error { return nil }

func contains(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}
