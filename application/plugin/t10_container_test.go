package plugin_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/fake"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

type t10ManifestSource struct {
	mu        sync.Mutex
	manifests map[string]domainplugin.Manifest
}

// t10DirectoryName derives the directory base the test helper pluginDir uses
// for a manifest id (the part after the single dot).
func t10DirectoryName(id string) string {
	if index := strings.LastIndexByte(id, '.'); index >= 0 {
		return id[index+1:]
	}
	return id
}

func newT10ManifestSource(manifests ...domainplugin.Manifest) *t10ManifestSource {
	source := &t10ManifestSource{manifests: make(map[string]domainplugin.Manifest)}
	for _, manifest := range manifests {
		source.manifests[t10DirectoryName(manifest.ID)] = manifest
	}
	return source
}

func (s *t10ManifestSource) Load(directory string) (domainplugin.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest, ok := s.manifests[filepath.Base(directory)]
	if !ok {
		return domainplugin.Manifest{}, fmt.Errorf("no manifest for %s", directory)
	}
	return manifest, nil
}

func (s *t10ManifestSource) set(manifest domainplugin.Manifest) {
	s.mu.Lock()
	s.manifests[t10DirectoryName(manifest.ID)] = manifest
	s.mu.Unlock()
}

func t10BareManifest(id string) domainplugin.Manifest {
	manifest := validManifest(id)
	manifest.Contributions = domainplugin.Contributions{}
	manifest.Permissions = domainplugin.Permissions{}
	return manifest
}

type t10Runtime struct {
	mu    sync.Mutex
	specs []appplugin.ModuleSpec
	inner *fake.Runtime
}

func newT10Runtime() *t10Runtime {
	return &t10Runtime{inner: fake.New()}
}

func (r *t10Runtime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	return r.inner.Instantiate(ctx, spec, sink)
}

func (r *t10Runtime) Close(ctx context.Context) error { return r.inner.Close(ctx) }

func (r *t10Runtime) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.specs)
}

func TestGroupScopesIdentityDefaultsAndIsolateWithoutBeingAFiber(t *testing.T) {
	manifest := t10BareManifest("acme.groupcfg")
	manifest.Settings = domainplugin.SettingsSchema{
		Fields: []domainplugin.SettingsField{
			{Key: "mode", Type: domainplugin.SettingTypeString, Default: "manifest"},
			{Key: "level", Type: domainplugin.SettingTypeString, Default: "normal"},
			{Key: "only_group", Type: domainplugin.SettingTypeString},
		},
	}
	dir := pluginDir(t, "groupcfg", manifest)
	registrar := &registrar{}
	var inherited []appplugin.SettingsInheritance
	resolver := func(_ context.Context, manifest domainplugin.Manifest,
		values appplugin.SettingsInheritance) (map[string]any, error) {
		inherited = append(inherited, values)
		out := manifest.Settings.Defaults()
		for key, value := range values.Group {
			out[key] = value
		}
		for key, value := range values.Volatile {
			out[key] = value
		}
		return out, nil
	}
	container := appplugin.NewContainer(
		newT10Runtime(),
		newT10ManifestSource(manifest),
		registrar,
		appplugin.WithSettingsResolver(resolver),
	)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	if err := container.DefineGroup(domainplugin.Group{
		Name: "team",
		Defaults: map[string]map[string]any{
			"acme.groupcfg": {"mode": "parent"},
		},
	}); err != nil {
		t.Fatalf("DefineGroup(team): %v", err)
	}
	if err := container.DefineGroup(domainplugin.Group{
		Name: "team.child", Parent: "team",
		Defaults: map[string]map[string]any{
			"acme.groupcfg": {"mode": "child", "only_group": "from-group"},
		},
	}); err != nil {
		t.Fatalf("DefineGroup(child): %v", err)
	}
	if err := container.DefineGroup(domainplugin.Group{Name: "orphan", Parent: "missing"}); err == nil {
		t.Fatal("DefineGroup accepted an unknown parent")
	}

	fiber, err := container.LoadInstance(context.Background(), appplugin.LoadSpec{
		Group: "team.child", Directory: dir, Instance: 1,
		Development: true, Source: domainplugin.SourceDevelopment,
	})
	if err != nil {
		t.Fatalf("LoadInstance: %v", err)
	}
	if got := string(fiber.ID()); got != "team.child/acme.groupcfg#1" {
		t.Fatalf("identity = %q", got)
	}
	settings := fiber.Settings()
	// The container supplies the merged group inheritance; the manager's
	// resolver decides that a manifest default wins over it. Here the local
	// resolver intentionally lets the group win, proving the inheritance is
	// delivered with the nearest group shadowing its parent.
	if settings["mode"] != "child" {
		t.Fatalf("mode = %v, want the child group to shadow its parent", settings["mode"])
	}
	if settings["level"] != "normal" {
		t.Fatalf("level = %v", settings["level"])
	}
	if settings["only_group"] != "from-group" {
		t.Fatalf("only_group = %v, want the group default to fill an unset field", settings["only_group"])
	}
	if len(inherited) != 1 || inherited[0].Group["mode"] != "child" ||
		inherited[0].Group["only_group"] != "from-group" {
		t.Fatalf("resolver inheritance = %+v", inherited)
	}

	report := container.DiagnosticsReport()
	if len(report.Fibers) != 1 {
		t.Fatalf("fibers = %d, want one; a group must not be a fiber", len(report.Fibers))
	}
	byName := make(map[string]appplugin.GroupDiagnostics, len(report.Groups))
	for _, group := range report.Groups {
		byName[group.Name] = group
	}
	child, ok := byName["team.child"]
	if !ok || child.Parent != "team" || len(child.Fibers) != 1 ||
		child.Fibers[0] != fiber.ID() {
		t.Fatalf("child group diagnostics = %+v (groups %+v)", child, report.Groups)
	}
	parent := byName["team"]
	if len(parent.Children) != 1 || parent.Children[0] != "team.child" {
		t.Fatalf("group tree = %+v", parent)
	}
	if _, ok := byName[string(fiber.ID())]; ok {
		t.Fatal("a fiber identity was reported as a group")
	}
}

func TestMultipleInstancesOfOneManifestHaveDistinctIdentities(t *testing.T) {
	manifest := t10BareManifest("acme.multi")
	dir := pluginDir(t, "multi", manifest)
	container := appplugin.NewContainer(newT10Runtime(),
		newT10ManifestSource(manifest), &registrar{})
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	first, err := container.LoadInstance(context.Background(), appplugin.LoadSpec{
		Group: "team", Directory: dir, Development: true,
		Source: domainplugin.SourceDevelopment,
	})
	if err != nil {
		t.Fatalf("first instance: %v", err)
	}
	second, err := container.LoadInstance(context.Background(), appplugin.LoadSpec{
		Group: "team", Directory: dir, Development: true,
		Source: domainplugin.SourceDevelopment,
	})
	if err != nil {
		t.Fatalf("second instance: %v", err)
	}
	third, err := container.LoadInstance(context.Background(), appplugin.LoadSpec{
		Group: "team", Directory: dir, Instance: 3,
		Development: true, Source: domainplugin.SourceDevelopment,
	})
	if err != nil {
		t.Fatalf("third instance: %v", err)
	}
	if first.ID() != domainplugin.NewFiberID("team", "acme.multi", 1) ||
		second.ID() != domainplugin.NewFiberID("team", "acme.multi", 2) ||
		third.ID() != domainplugin.NewFiberID("team", "acme.multi", 3) {
		t.Fatalf("identities = %q %q %q", first.ID(), second.ID(), third.ID())
	}
	if first == second || first.ID() == second.ID() {
		t.Fatal("instances share an identity")
	}
	for _, fiber := range []*appplugin.Fiber{first, second, third} {
		if fiber.State() != domainplugin.StateActive {
			t.Fatalf("%s state = %s (%s)", fiber.ID(), fiber.State(), fiber.Reason())
		}
	}
	if _, err := container.LoadInstance(context.Background(), appplugin.LoadSpec{
		Group: "team", Directory: dir, Instance: 3,
		Development: true, Source: domainplugin.SourceDevelopment,
	}); err == nil {
		t.Fatal("an explicit instance number replaced a live instance")
	}
	if err := container.Unload(context.Background(), second.ID()); err != nil {
		t.Fatalf("Unload(second): %v", err)
	}
	if second.State() != domainplugin.StateDisposed {
		t.Fatalf("second state = %s", second.State())
	}
	if first.State() != domainplugin.StateActive || third.State() != domainplugin.StateActive {
		t.Fatalf("unloading one instance disturbed its siblings: %s %s",
			first.State(), third.State())
	}
}

func TestInjectionsAreIsolateScopedAndWakeDependents(t *testing.T) {
	provider := t10BareManifest("acme.prov")
	provider.Provides = []domainplugin.Provision{{Service: "plugin.acme.prov.search"}}
	consumer := t10BareManifest("acme.cons")
	consumer.Inject = []domainplugin.Injection{{Service: "plugin.acme.prov.search"}}
	providerDir := pluginDir(t, "prov", provider)
	consumerDir := pluginDir(t, "cons", consumer)

	runtime := newT10Runtime()
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{})
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	for _, group := range []domainplugin.Group{
		{Name: "app"},
		{Name: "app.left", Parent: "app"},
		{Name: "app.right", Parent: "app"},
	} {
		if err := container.DefineGroup(group); err != nil {
			t.Fatalf("DefineGroup(%s): %v", group.Name, err)
		}
	}

	load := func(group, dir string) *appplugin.Fiber {
		t.Helper()
		fiber, err := container.LoadInstance(context.Background(), appplugin.LoadSpec{
			Group: group, Directory: dir, Instance: 1,
			Development: true, Source: domainplugin.SourceDevelopment,
		})
		if fiber == nil {
			t.Fatalf("load %s in %s returned no fiber: %v", filepath.Base(dir), group, err)
		}
		return fiber
	}

	rootConsumer := load("app", consumerDir)
	leftConsumer := load("app.left", consumerDir)
	rightConsumer := load("app.right", consumerDir)
	for _, fiber := range []*appplugin.Fiber{rootConsumer, leftConsumer, rightConsumer} {
		if fiber.State() != domainplugin.StatePending {
			t.Fatalf("%s state = %s, want PENDING", fiber.ID(), fiber.State())
		}
		if fiber.Reason() != domainplugin.InjectionReason([]string{"plugin.acme.prov.search"}) {
			t.Fatalf("%s reason = %q", fiber.ID(), fiber.Reason())
		}
		if len(fiber.MissingInjections()) != 1 {
			t.Fatalf("%s missing injections = %v", fiber.ID(), fiber.MissingInjections())
		}
	}
	if runtime.count() != 0 {
		t.Fatalf("a PENDING fiber instantiated %d modules, want none", runtime.count())
	}

	leftProvider := load("app.left", providerDir)
	if leftConsumer.State() != domainplugin.StateActive {
		t.Fatalf("same-group consumer state = %s (%s)", leftConsumer.State(), leftConsumer.Reason())
	}
	if rootConsumer.State() != domainplugin.StatePending {
		t.Fatal("a parent group saw a child group's provider")
	}
	if rightConsumer.State() != domainplugin.StatePending {
		t.Fatal("a sibling group saw another child's provider")
	}

	rightProvider := load("app.right", providerDir)
	if rightConsumer.State() != domainplugin.StateActive {
		t.Fatalf("right consumer state = %s (%s)", rightConsumer.State(), rightConsumer.Reason())
	}
	if rootConsumer.State() != domainplugin.StatePending {
		t.Fatal("a parent group saw a child group's provider")
	}

	rootProvider := load("app", providerDir)
	if rootConsumer.State() != domainplugin.StateActive {
		t.Fatalf("consumer state = %s (%s), want ACTIVE from the ancestor provider",
			rootConsumer.State(), rootConsumer.Reason())
	}
	if len(container.Services()) != 3 {
		t.Fatalf("published services = %+v", container.Services())
	}

	if err := container.Unload(context.Background(), rootProvider.ID()); err != nil {
		t.Fatalf("Unload(root provider): %v", err)
	}
	if rootConsumer.State() != domainplugin.StatePending {
		t.Fatalf("consumer state after provider unload = %s, want PENDING", rootConsumer.State())
	}
	if len(rootConsumer.MissingInjections()) != 1 {
		t.Fatalf("missing injections after provider unload = %v", rootConsumer.MissingInjections())
	}
	if leftConsumer.State() != domainplugin.StateActive ||
		rightConsumer.State() != domainplugin.StateActive {
		t.Fatalf("child providers were disturbed: %s %s",
			leftConsumer.State(), rightConsumer.State())
	}
	_ = leftProvider
	_ = rightProvider
}

func TestInvalidConfigurationIsPendingWithoutAnInstanceAndRetryActivates(t *testing.T) {
	manifest := t10BareManifest("acme.config")
	manifest.Settings = domainplugin.SettingsSchema{
		Fields: []domainplugin.SettingsField{
			{Key: "mode", Type: domainplugin.SettingTypeString, Required: true},
		},
	}
	dir := pluginDir(t, "config", manifest)
	runtime := newT10Runtime()
	valid := false
	resolver := func(_ context.Context, manifest domainplugin.Manifest,
		_ appplugin.SettingsInheritance) (map[string]any, error) {
		if !valid {
			return nil, &domainplugin.SettingsValidationError{Errors: []domainplugin.SettingsError{
				{Field: "mode", Problem: "a value is required"},
			}}
		}
		return map[string]any{"mode": "fast"}, nil
	}
	container := appplugin.NewContainer(runtime, newT10ManifestSource(manifest),
		&registrar{}, appplugin.WithSettingsResolver(resolver))
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	var validation *domainplugin.SettingsValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("LoadDevelopment error = %v, want SettingsValidationError", err)
	}
	if fiber.State() != domainplugin.StatePending {
		t.Fatalf("state = %s, want PENDING", fiber.State())
	}
	if !strings.HasPrefix(fiber.Reason(), domainplugin.ReasonConfigInvalid) {
		t.Fatalf("reason = %q, want config-invalid", fiber.Reason())
	}
	if len(fiber.ConfigErrors()) != 1 || fiber.ConfigErrors()[0].Field != "mode" {
		t.Fatalf("config errors = %+v", fiber.ConfigErrors())
	}
	if runtime.count() != 0 {
		t.Fatal("an invalid configuration instantiated the module")
	}
	if fiber.EffectCount() != 0 {
		t.Fatalf("effects = %d", fiber.EffectCount())
	}

	valid = true
	retried, err := container.Retry(context.Background(), fiber.ID())
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if retried.State() != domainplugin.StateActive {
		t.Fatalf("state after retry = %s (%s)", retried.State(), retried.Reason())
	}
	if runtime.count() != 1 {
		t.Fatalf("instantiations = %d, want one after retry", runtime.count())
	}
}

func TestUngrantedRequiredPermissionIsPendingWithoutAnInstance(t *testing.T) {
	manifest := t10BareManifest("acme.perm")
	manifest.Permissions.Required = []string{"commands"}
	dir := pluginDir(t, "perm", manifest)
	runtime := newT10Runtime()
	allowed := false
	checker := func(ref appplugin.PluginRef,
		request domainplugin.PermissionRequest) error {
		if !allowed {
			return fmt.Errorf("%w: %s", appplugin.ErrPermissionNotGranted, request.Permission)
		}
		return nil
	}
	container := appplugin.NewContainer(runtime, newT10ManifestSource(manifest),
		&registrar{}, appplugin.WithRequiredPermissions(checker))
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if !errors.Is(err, appplugin.ErrPermissionNotGranted) {
		t.Fatalf("LoadDevelopment error = %v, want ErrPermissionNotGranted", err)
	}
	if fiber.State() != domainplugin.StatePending {
		t.Fatalf("state = %s, want PENDING", fiber.State())
	}
	wantReason := domainplugin.PermissionReason(domainplugin.ReasonPermissionDenied,
		domainplugin.PermissionCommands)
	if fiber.Reason() != wantReason {
		t.Fatalf("reason = %q, want %q", fiber.Reason(), wantReason)
	}
	if runtime.count() != 0 {
		t.Fatal("an ungranted required permission instantiated the module")
	}

	allowed = true
	retried, err := container.Retry(context.Background(), fiber.ID())
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if retried.State() != domainplugin.StateActive {
		t.Fatalf("state after retry = %s (%s)", retried.State(), retried.Reason())
	}
	if runtime.count() != 1 {
		t.Fatalf("instantiations = %d", runtime.count())
	}
}

type t10StatusRuntime struct {
	inner *fake.Runtime
}

func (r *t10StatusRuntime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	instance, err := r.inner.Instantiate(ctx, spec, sink)
	if err != nil {
		return nil, err
	}
	return &t10StatusInstance{Instance: instance}, nil
}

func (r *t10StatusRuntime) Close(ctx context.Context) error { return r.inner.Close(ctx) }

type t10StatusInstance struct {
	appplugin.Instance
}

func (i *t10StatusInstance) Dispatch(context.Context, string) error {
	return &appplugin.GuestStatusError{Operation: "dispatch", Status: 3}
}

func TestCallLevelErrorsLeaveTheLifecycleUnchanged(t *testing.T) {
	manifest := validManifest("acme.status")
	dir := pluginDir(t, "status", manifest)
	registrar := &registrar{}
	container := appplugin.NewContainer(&t10StatusRuntime{inner: fake.New()},
		newT10ManifestSource(manifest), registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	run := registrar.commands()["plugin.acme.status.greet"]
	if run == nil {
		t.Fatal("the command was not registered")
	}
	var status *appplugin.GuestStatusError
	if err := run(context.Background()); !errors.As(err, &status) {
		t.Fatalf("call error = %v, want GuestStatusError", err)
	}
	if fiber.State() != domainplugin.StateActive {
		t.Fatalf("state = %s, want ACTIVE after a call-level error", fiber.State())
	}
	if _, ok := registrar.commands()["plugin.acme.status.greet"]; !ok {
		t.Fatal("a call-level error removed the command")
	}
	diagnostics, ok := container.PluginDiagnostics(fiber.ID())
	if !ok {
		t.Fatal("diagnostics missing")
	}
	if diagnostics.State != domainplugin.StateActive || len(diagnostics.Faults) != 0 {
		t.Fatalf("diagnostics after call-level error = %+v", diagnostics)
	}
}

func TestEffectsAreDisposedInReverseOrder(t *testing.T) {
	manifest := validManifest("acme.order")
	manifest.Contributions = domainplugin.Contributions{
		Commands: []domainplugin.Command{
			{ID: "first", Title: "First"},
			{ID: "second", Title: "Second"},
		},
	}
	dir := pluginDir(t, "order", manifest)
	registrar := &registrar{}
	var mu sync.Mutex
	var order []string
	registrar.disposeHook = func(item registration) {
		mu.Lock()
		order = append(order, item.id)
		mu.Unlock()
	}
	container := appplugin.NewContainer(newT10Runtime(),
		newT10ManifestSource(manifest), registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	if err := container.Unload(context.Background(), fiber.ID()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"plugin.acme.order.second", "plugin.acme.order.first"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("disposal order = %v, want %v", order, want)
	}
}

type t10HangingRegistrar struct {
	release chan struct{}
}

func (r *t10HangingRegistrar) RegisterCommand(appplugin.PluginRef, domainplugin.Command,
	appplugin.Handler) (appplugin.Disposer, error) {
	return func(context.Context) error {
		<-r.release
		return nil
	}, nil
}

func (r *t10HangingRegistrar) RegisterMenu(appplugin.PluginRef,
	domainplugin.Menu) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (r *t10HangingRegistrar) RegisterKeybinding(appplugin.PluginRef,
	domainplugin.Keybinding) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func TestUnloadReturnsWithinTheFiberUnloadBudget(t *testing.T) {
	manifest := validManifest("acme.stuck")
	manifest.Contributions = domainplugin.Contributions{
		Commands: []domainplugin.Command{{ID: "run", Title: "Run"}},
	}
	manifest.Contributions.Menus = nil
	manifest.Contributions.Keybindings = nil
	dir := pluginDir(t, "stuck", manifest)
	registrar := &t10HangingRegistrar{release: make(chan struct{})}
	container := appplugin.NewContainer(newT10Runtime(),
		newT10ManifestSource(manifest), registrar,
		appplugin.WithUnloadTimeout(40*time.Millisecond))
	t.Cleanup(func() {
		close(registrar.release)
		_ = container.Close(context.Background())
	})

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	start := time.Now()
	err = container.Unload(context.Background(), fiber.ID())
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("Unload took %s, above the 40ms budget", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Unload error = %v, want the deadline", err)
	}
	if fiber.State() != domainplugin.StateDisposed {
		t.Fatalf("state = %s, want DISPOSED", fiber.State())
	}
}

type t10BlockingShutdownRuntime struct {
	release chan struct{}
	inner   *fake.Runtime
}

func (r *t10BlockingShutdownRuntime) Instantiate(ctx context.Context,
	spec appplugin.ModuleSpec, sink appplugin.HostSink) (appplugin.Instance, error) {
	instance, err := r.inner.Instantiate(ctx, spec, sink)
	if err != nil {
		return nil, err
	}
	return &t10BlockingShutdownInstance{Instance: instance, release: r.release}, nil
}

func (r *t10BlockingShutdownRuntime) Close(ctx context.Context) error {
	return r.inner.Close(ctx)
}

type t10BlockingShutdownInstance struct {
	appplugin.Instance
	release <-chan struct{}
}

func (i *t10BlockingShutdownInstance) Shutdown(context.Context) error {
	<-i.release
	return nil
}

func TestContainerCloseCompletesWithinTheGlobalUnloadBudget(t *testing.T) {
	manifest := validManifest("acme.quit")
	dir := pluginDir(t, "quit", manifest)
	release := make(chan struct{})
	container := appplugin.NewContainer(
		&t10BlockingShutdownRuntime{release: release, inner: fake.New()},
		newT10ManifestSource(manifest), &registrar{},
		appplugin.WithGlobalUnloadBudget(50*time.Millisecond))
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	if _, err := container.LoadDevelopment(context.Background(), dir); err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	start := time.Now()
	err := container.Close(context.Background())
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("Close took %s, above the 50ms global budget", elapsed)
	}
	if err == nil {
		t.Fatal("Close reported success although the guest ignored shutdown")
	}
}

func TestDiagnosticsReportLifecycleEffectsMemoryTimingsFaultsAndLogs(t *testing.T) {
	spinManifest := validManifest("acme.spin")
	spinDir := pluginDir(t, "spin", spinManifest)
	goodManifest := validManifest("acme.good")
	goodDir := pluginDir(t, "good", goodManifest)
	registrar := &registrar{}
	container := appplugin.NewContainer(
		&fixtureMapRuntime{fixtures: map[string]string{"acme.spin": fake.Trap}},
		newT10ManifestSource(spinManifest, goodManifest), registrar)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	good, err := container.LoadDevelopment(context.Background(), goodDir)
	if err != nil {
		t.Fatalf("LoadDevelopment(good): %v", err)
	}
	spin, err := container.LoadDevelopment(context.Background(), spinDir)
	if err != nil {
		t.Fatalf("LoadDevelopment(spin): %v", err)
	}
	if err := registrar.commands()["plugin.acme.good.greet"](context.Background()); err != nil {
		t.Fatalf("good command: %v", err)
	}
	var trapped *appplugin.TrapError
	if err := registrar.commands()["plugin.acme.spin.greet"](context.Background()); !errors.As(err, &trapped) {
		t.Fatalf("spin command = %v, want TrapError", err)
	}
	if good.State() != domainplugin.StateActive {
		t.Fatalf("the failing sibling changed the good fiber: %s", good.State())
	}

	report := container.DiagnosticsReport()
	if len(report.Fibers) != 2 {
		t.Fatalf("fibers = %+v", report.Fibers)
	}
	byID := make(map[domainplugin.FiberID]appplugin.PluginDiagnostics)
	for _, entry := range report.Fibers {
		byID[entry.ID] = entry
	}
	goodEntry := byID[good.ID()]
	if goodEntry.State != domainplugin.StateActive || goodEntry.Effects != 3 {
		t.Fatalf("good diagnostics = %+v", goodEntry)
	}
	if goodEntry.MemoryBytes <= 0 {
		t.Fatalf("good memory = %d, want the runtime report", goodEntry.MemoryBytes)
	}
	if len(goodEntry.Faults) != 0 {
		t.Fatalf("good faults = %+v", goodEntry.Faults)
	}
	if !hasTiming(goodEntry.Timings, "init") || !hasTiming(goodEntry.Timings, "dispatch") {
		t.Fatalf("good timings = %+v", goodEntry.Timings)
	}
	if !logContains(goodEntry.Logs, "dispatch") {
		t.Fatalf("good logs = %v", goodEntry.Logs)
	}
	spinEntry := byID[spin.ID()]
	if spinEntry.State != domainplugin.StateFailed {
		t.Fatalf("spin state = %s", spinEntry.State)
	}
	if len(spinEntry.Faults) != 1 || !strings.Contains(spinEntry.Faults[0].Reason, "trapped") {
		t.Fatalf("spin faults = %+v", spinEntry.Faults)
	}
	if spinEntry.Effects != 0 {
		t.Fatalf("spin effects after recycle = %d", spinEntry.Effects)
	}
	if spinEntry.MemoryBytes <= 0 || spinEntry.MemoryMax <= 0 {
		t.Fatalf("spin memory = %d/%d", spinEntry.MemoryBytes, spinEntry.MemoryMax)
	}
	if !logContains(spinEntry.Logs, "init") {
		t.Fatalf("spin logs = %v", spinEntry.Logs)
	}
	var devGroup *appplugin.GroupDiagnostics
	for i := range report.Groups {
		if report.Groups[i].Name == "dev" {
			devGroup = &report.Groups[i]
		}
	}
	if devGroup == nil || len(devGroup.Fibers) != 2 {
		t.Fatalf("dev group diagnostics = %+v", report.Groups)
	}
}

func hasTiming(timings []appplugin.CallTiming, operation string) bool {
	for _, timing := range timings {
		if timing.Operation == operation && timing.Calls > 0 {
			return true
		}
	}
	return false
}

func logContains(logs []string, text string) bool {
	for _, line := range logs {
		if strings.Contains(line, text) {
			return true
		}
	}
	return false
}

type t10Timer struct {
	delay time.Duration
	run   func()
}

func TestRuntimeFaultGetsOneAutomaticRestartThenManualRestart(t *testing.T) {
	manifest := validManifest("acme.retry")
	dir := pluginDir(t, "retry", manifest)
	registrar := &registrar{}
	var mu sync.Mutex
	var timers []t10Timer
	schedule := func(delay time.Duration, run func()) func() {
		mu.Lock()
		timers = append(timers, t10Timer{delay: delay, run: run})
		mu.Unlock()
		return func() {}
	}
	container := appplugin.NewContainer(
		&fixtureMapRuntime{fixtures: map[string]string{"acme.retry": fake.Trap}},
		newT10ManifestSource(manifest), registrar,
		appplugin.WithRestartBackoff(time.Millisecond),
		appplugin.WithSchedule(schedule))
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	run := func() {
		t.Helper()
		command := registrar.commands()["plugin.acme.retry.greet"]
		if command == nil {
			t.Fatal("the command was not registered")
		}
		var trapped *appplugin.TrapError
		if err := command(context.Background()); !errors.As(err, &trapped) {
			t.Fatalf("command = %v, want TrapError", err)
		}
	}
	run()
	if fiber.State() != domainplugin.StateFailed {
		t.Fatalf("state = %s, want FAILED", fiber.State())
	}
	mu.Lock()
	if len(timers) != 1 {
		mu.Unlock()
		t.Fatalf("scheduled restarts = %d, want one", len(timers))
	}
	if timers[0].delay < appplugin.MinRestartBackoff {
		mu.Unlock()
		t.Fatalf("restart backoff = %s, below the %s minimum",
			timers[0].delay, appplugin.MinRestartBackoff)
	}
	first := timers[0]
	mu.Unlock()
	diagnostics, _ := container.PluginDiagnostics(fiber.ID())
	if !diagnostics.RestartPending || diagnostics.AutomaticRestarts != 1 ||
		len(diagnostics.Faults) != 1 {
		t.Fatalf("diagnostics before restart = %+v", diagnostics)
	}

	first.run()
	if fiber.State() != domainplugin.StateActive {
		t.Fatalf("state after automatic restart = %s (%s)", fiber.State(), fiber.Reason())
	}
	diagnostics, _ = container.PluginDiagnostics(fiber.ID())
	if diagnostics.Generation != 1 || diagnostics.AutomaticRestarts != 1 {
		t.Fatalf("diagnostics after automatic restart = %+v", diagnostics)
	}

	run()
	if fiber.State() != domainplugin.StateFailed {
		t.Fatalf("second fault state = %s", fiber.State())
	}
	mu.Lock()
	scheduledAfterSecond := len(timers)
	mu.Unlock()
	if scheduledAfterSecond != 1 {
		t.Fatalf("a second automatic restart was scheduled (%d timers)", scheduledAfterSecond)
	}
	diagnostics, _ = container.PluginDiagnostics(fiber.ID())
	if len(diagnostics.Faults) != 2 || diagnostics.RestartPending {
		t.Fatalf("diagnostics after second fault = %+v", diagnostics)
	}

	if _, err := container.Restart(context.Background(), fiber.ID()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if fiber.State() != domainplugin.StateActive {
		t.Fatalf("state after manual restart = %s (%s)", fiber.State(), fiber.Reason())
	}
	diagnostics, _ = container.PluginDiagnostics(fiber.ID())
	if diagnostics.ManualRestarts != 1 || diagnostics.AutomaticRestarts != 0 {
		t.Fatalf("diagnostics after manual restart = %+v", diagnostics)
	}

	run()
	mu.Lock()
	scheduledAfterManual := len(timers)
	mu.Unlock()
	if scheduledAfterManual != 2 {
		t.Fatalf("manual restart did not renew the automatic budget (%d timers)", scheduledAfterManual)
	}
}

func TestLifecycleStatesHaveTheirSpecifiedMeanings(t *testing.T) {
	for _, state := range []domainplugin.State{
		domainplugin.StatePending, domainplugin.StateLoading, domainplugin.StateActive,
		domainplugin.StateUnloading, domainplugin.StateDisposed, domainplugin.StateFailed,
	} {
		if !state.Valid() {
			t.Fatalf("%s is not a valid lifecycle state", state)
		}
	}
	manifest := validManifest("acme.life")
	dir := pluginDir(t, "life", manifest)
	var events []domainplugin.State
	container := appplugin.NewContainer(newT10Runtime(),
		newT10ManifestSource(manifest), &registrar{},
		appplugin.WithLifecycle(func(_ appplugin.PluginRef, state domainplugin.State, _ string) {
			events = append(events, state)
		}))
	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	if err := container.Unload(context.Background(), fiber.ID()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	want := []domainplugin.State{
		domainplugin.StateLoading, domainplugin.StateActive,
		domainplugin.StateUnloading, domainplugin.StateDisposed,
	}
	if len(events) != len(want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("lifecycle events = %v, want %v", events, want)
		}
	}
}

// TestDiagnosticsGroupTreeListsEachGroupOnceAndKeepsEmptyGroups proves the
// report is a tree, not a per-fiber child list: a group with several instances
// appears once as a child, and a child with no fibers of its own is still in
// its parent's Children.
func TestDiagnosticsGroupTreeListsEachGroupOnceAndKeepsEmptyGroups(t *testing.T) {
	manifest := t10BareManifest("acme.tree")
	dir := pluginDir(t, "tree", manifest)
	container := appplugin.NewContainer(newT10Runtime(),
		newT10ManifestSource(manifest), &registrar{})
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	for _, group := range []domainplugin.Group{
		{Name: "team"},
		{Name: "team.child", Parent: "team"},
		{Name: "team.child.empty", Parent: "team.child"},
		{Name: "team.empty", Parent: "team"},
		{Name: "solo"},
	} {
		if err := container.DefineGroup(group); err != nil {
			t.Fatalf("DefineGroup(%s): %v", group.Name, err)
		}
	}
	first, err := container.LoadInstance(context.Background(), appplugin.LoadSpec{
		Group: "team.child", Directory: dir, Instance: 1,
		Development: true, Source: domainplugin.SourceDevelopment,
	})
	if err != nil {
		t.Fatalf("first instance: %v", err)
	}
	second, err := container.LoadInstance(context.Background(), appplugin.LoadSpec{
		Group: "team.child", Directory: dir, Instance: 2,
		Development: true, Source: domainplugin.SourceDevelopment,
	})
	if err != nil {
		t.Fatalf("second instance: %v", err)
	}

	report := container.DiagnosticsReport()
	byName := make(map[string]appplugin.GroupDiagnostics, len(report.Groups))
	appearances := make(map[string]int, len(report.Groups))
	for _, group := range report.Groups {
		byName[group.Name] = group
		appearances[group.Name]++
	}
	for _, name := range []string{
		"team", "team.child", "team.child.empty", "team.empty", "solo",
	} {
		if appearances[name] != 1 {
			t.Fatalf("group %q appears %d times in %+v", name, appearances[name], report.Groups)
		}
	}
	if len(report.Groups) != 5 {
		t.Fatalf("groups = %+v, want the five defined groups", report.Groups)
	}

	child := byName["team.child"]
	if len(child.Fibers) != 2 || child.Fibers[0] != first.ID() || child.Fibers[1] != second.ID() {
		t.Fatalf("team.child fibers = %+v, want both instances", child.Fibers)
	}
	if len(byName["team"].Children) != 2 ||
		byName["team"].Children[0] != "team.child" ||
		byName["team"].Children[1] != "team.empty" {
		t.Fatalf("team children = %+v, want each child exactly once", byName["team"].Children)
	}
	if len(child.Children) != 1 || child.Children[0] != "team.child.empty" {
		t.Fatalf("team.child children = %+v, want the empty child group", child.Children)
	}
	if len(byName["team.empty"].Fibers) != 0 {
		t.Fatalf("team.empty fibers = %+v, want none", byName["team.empty"].Fibers)
	}
	if len(byName["team.child.empty"].Fibers) != 0 {
		t.Fatalf("team.child.empty fibers = %+v, want none", byName["team.child.empty"].Fibers)
	}
	if byName["solo"].Parent != "" || len(byName["solo"].Children) != 0 {
		t.Fatalf("solo group = %+v, want a root with no children", byName["solo"])
	}
	for _, group := range report.Groups {
		if group.Parent == "" {
			continue
		}
		parent := byName[group.Parent]
		count := 0
		for _, name := range parent.Children {
			if name == group.Name {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("group %q appears %d times under %q: %+v",
				group.Name, count, group.Parent, parent.Children)
		}
	}
}

// t10ReloadFailureFixture loads an ACTIVE provider and consumer pair whose
// required injection is satisfied. The manifest source stays mutable so a
// test can break the provider manifest before a development reload.
type t10ReloadFailureFixture struct {
	container *appplugin.Container
	source    *t10ManifestSource
	provider  *appplugin.Fiber
	consumer  *appplugin.Fiber
	manifest  domainplugin.Manifest
}

func newT10ReloadFailureFixture(t *testing.T) *t10ReloadFailureFixture {
	t.Helper()
	const service = "plugin.acme.prov.search"
	provider := t10BareManifest("acme.prov")
	provider.Provides = []domainplugin.Provision{{Service: service}}
	consumer := t10BareManifest("acme.cons")
	consumer.Inject = []domainplugin.Injection{{Service: service}}
	providerDir := pluginDir(t, "prov", provider)
	consumerDir := pluginDir(t, "cons", consumer)

	source := newT10ManifestSource(provider, consumer)
	container := appplugin.NewContainer(newT10Runtime(), source, &registrar{})
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	providerFiber, err := container.LoadDevelopment(context.Background(), providerDir)
	if err != nil {
		t.Fatalf("LoadDevelopment(provider): %v", err)
	}
	consumerFiber, err := container.LoadDevelopment(context.Background(), consumerDir)
	if err != nil {
		t.Fatalf("LoadDevelopment(consumer): %v", err)
	}
	if state := providerFiber.State(); state != domainplugin.StateActive {
		t.Fatalf("provider state = %s (%s)", state, providerFiber.Reason())
	}
	if state := consumerFiber.State(); state != domainplugin.StateActive {
		t.Fatalf("consumer state = %s (%s)", state, consumerFiber.Reason())
	}
	return &t10ReloadFailureFixture{
		container: container,
		source:    source,
		provider:  providerFiber,
		consumer:  consumerFiber,
		manifest:  provider,
	}
}

// assertSuspendedConsumer checks that a provider reload that ended FAILED
// suspended the consumer with the typed missing-injection reason, and that the
// same result is visible through the diagnostics view.
func (f *t10ReloadFailureFixture) assertSuspendedConsumer(t *testing.T) {
	t.Helper()
	const service = "plugin.acme.prov.search"
	if state := f.provider.State(); state != domainplugin.StateFailed {
		t.Fatalf("provider state = %s (%s), want FAILED", state, f.provider.Reason())
	}
	if state := f.consumer.State(); state != domainplugin.StatePending {
		t.Fatalf("consumer state after the failed provider reload = %s (%s), want PENDING",
			state, f.consumer.Reason())
	}
	if reason, want := f.consumer.Reason(), domainplugin.InjectionReason([]string{service}); reason != want {
		t.Fatalf("consumer reason = %q, want %q", reason, want)
	}
	missing := f.consumer.MissingInjections()
	if len(missing) != 1 || missing[0] != service {
		t.Fatalf("consumer missing injections = %v, want [%s]", missing, service)
	}
	diagnostics, ok := f.container.PluginDiagnostics(f.consumer.ID())
	if !ok {
		t.Fatal("consumer diagnostics missing")
	}
	if diagnostics.State != domainplugin.StatePending ||
		len(diagnostics.MissingInjections) != 1 ||
		diagnostics.MissingInjections[0] != service ||
		diagnostics.Reason != domainplugin.InjectionReason([]string{service}) {
		t.Fatalf("consumer diagnostics after the failed provider reload = %+v", diagnostics)
	}
}

func (f *t10ReloadFailureFixture) assertRecovered(t *testing.T) {
	t.Helper()
	if state := f.provider.State(); state != domainplugin.StateActive {
		t.Fatalf("provider state after recovery = %s (%s)", state, f.provider.Reason())
	}
	if state := f.consumer.State(); state != domainplugin.StateActive {
		t.Fatalf("consumer state after recovery = %s (%s)", state, f.consumer.Reason())
	}
}

// TestDevelopmentReloadWithInvalidABILeavesDependentsPending covers criterion
// 2 on reload's manifest-refresh failure path: a provider that was ACTIVE and
// ends FAILED must suspend its dependents even though activation never runs.
func TestDevelopmentReloadWithInvalidABILeavesDependentsPending(t *testing.T) {
	fixture := newT10ReloadFailureFixture(t)
	invalid := fixture.manifest
	invalid.ABI = 2
	fixture.source.set(invalid)

	if _, err := fixture.container.DevelopmentChanged(context.Background(),
		fixture.provider.ID()); err == nil {
		t.Fatal("a development reload with an invalid ABI reported no error")
	}
	fixture.assertSuspendedConsumer(t)

	// An unsupported ABI is rejected before the fiber adopts the new
	// manifest, so a manual restart recovers with the last valid one.
	if _, err := fixture.container.Restart(context.Background(), fixture.provider.ID()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	fixture.assertRecovered(t)
}

// TestDevelopmentReloadWithMissingModuleLeavesDependentsPending covers reload's
// activation failure path: the refreshed manifest is adopted, module loading
// fails, and the provider's dependents must still be suspended.
func TestDevelopmentReloadWithMissingModuleLeavesDependentsPending(t *testing.T) {
	fixture := newT10ReloadFailureFixture(t)
	missing := fixture.manifest
	missing.Wasm.Path = "missing.wasm"
	fixture.source.set(missing)

	if _, err := fixture.container.DevelopmentChanged(context.Background(),
		fixture.provider.ID()); err == nil {
		t.Fatal("a development reload from a missing module path reported no error")
	}
	fixture.assertSuspendedConsumer(t)

	// The failed reload adopted the new manifest, so recovery is another
	// development reload after the module is present again.
	fixture.source.set(fixture.manifest)
	if _, err := fixture.container.DevelopmentChanged(context.Background(),
		fixture.provider.ID()); err != nil {
		t.Fatalf("DevelopmentChanged after restoring the module: %v", err)
	}
	fixture.assertRecovered(t)
}
