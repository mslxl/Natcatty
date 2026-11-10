package plugin_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/fake"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

const firstPartyID = "acme.first-party"

func firstPartyManifest(id string) domainplugin.Manifest {
	return domainplugin.Manifest{
		ManifestVersion: 1,
		ID:              id,
		Name:            "First Party",
		Version:         "0.1.0",
		PluginAPI:       "^0.1",
		DefaultLocale:   "en",
		Permissions: domainplugin.Permissions{
			Required: []string{
				string(domainplugin.PermissionCommands),
				string(domainplugin.PermissionViews),
			},
			Optional: []string{string(domainplugin.PermissionUINative)},
		},
		Contributions: domainplugin.Contributions{
			Commands: []domainplugin.Command{{ID: "ping", Title: "Ping"}},
			Views: []domainplugin.ViewContribution{{
				ID:         "panel",
				Title:      "Panel",
				Placements: []domainplugin.Placement{domainplugin.PlacementPane},
			}},
		},
	}
}

func newFirstPartyManager(t *testing.T,
	opts ...appplugin.ManagerOption) (*appplugin.Manager, *viewRegistrar) {
	t.Helper()
	registrations := &viewRegistrar{registrar: &registrar{}}
	options := append([]appplugin.ManagerOption{
		appplugin.WithManagerClock(func() time.Time {
			return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
		}),
	}, opts...)
	manager := appplugin.NewManager(nil, nil, registrations, nil, nil, options...)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager, registrations
}

func findPlugin(t *testing.T, manager *appplugin.Manager, key string) appplugin.InstalledPlugin {
	t.Helper()
	list, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, plugin := range list {
		if plugin.Key == key || plugin.ID == key {
			return plugin
		}
	}
	t.Fatalf("plugin %s is not listed: %+v", key, list)
	return appplugin.InstalledPlugin{}
}

func builtinFiberState(t *testing.T, manager *appplugin.Manager,
	id string) (domainplugin.State, *appplugin.Fiber) {
	t.Helper()
	fiber, ok := manager.Container().Fiber(domainplugin.NewFiberID(appplugin.BuiltinGroup, id, 1))
	if !ok {
		t.Fatalf("built-in fiber %s is missing", id)
	}
	return fiber.State(), fiber
}

// TestBuiltinUsesTheFiberTreeContributionsAndEffectStack proves a compiled-in
// plugin is not a parallel mechanism: it is loaded without a Runtime, appears
// in the same diagnostics tree, publishes through the same contribution
// registrar, and every contribution is removed by the same effect stack.
func TestBuiltinUsesTheFiberTreeContributionsAndEffectStack(t *testing.T) {
	var feedback []string
	manager, registrations := newFirstPartyManager(t,
		appplugin.WithAppVersion("2.4.1"),
		appplugin.WithManagerFeedback(func(_ appplugin.PluginRef, output string) {
			feedback = append(feedback, output)
		}),
	)
	var dispatched []string
	setup := func(_ context.Context, host *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
		if host.NativeUI() == nil {
			return nil, errors.New("the built-in host has no native UI session")
		}
		if host.Settings() == nil {
			return nil, errors.New("the built-in host has no settings map")
		}
		return appplugin.BuiltinFuncs{
			DispatchFunc: func(_ context.Context, command string) error {
				dispatched = append(dispatched, command)
				host.Log(appplugin.LogInfo, "first-party dispatch")
				host.Output([]byte("first-party output"))
				return nil
			},
		}, nil
	}
	err := manager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest(firstPartyID),
		Setup:    setup,
	})
	if err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}

	plugin := findPlugin(t, manager, "built-in/"+firstPartyID)
	if plugin.Source != domainplugin.SourceBuiltIn || plugin.Development {
		t.Fatalf("built-in source = %+v", plugin.Source)
	}
	if plugin.Version != "2.4.1" {
		t.Fatalf("built-in version = %q, want the app version", plugin.Version)
	}
	if plugin.Status != appplugin.StatusActive || plugin.State != domainplugin.StateActive {
		t.Fatalf("built-in status = %s state = %s", plugin.Status, plugin.State)
	}
	if len(plugin.Permissions) != 3 {
		t.Fatalf("permission rows = %+v", plugin.Permissions)
	}
	for _, row := range plugin.Permissions {
		if !row.Granted || row.Pending {
			t.Fatalf("built-in permission row is not visible as granted: %+v", row)
		}
	}

	report := manager.DiagnosticsReport()
	var builtinDiagnostics *appplugin.PluginDiagnostics
	for i := range report.Fibers {
		entry := &report.Fibers[i]
		if entry.PluginID == firstPartyID {
			builtinDiagnostics = entry
			break
		}
	}
	if builtinDiagnostics == nil {
		t.Fatal("built-in is absent from the diagnostics fiber tree")
	}
	if builtinDiagnostics.Group != appplugin.BuiltinGroup ||
		builtinDiagnostics.Source != domainplugin.SourceBuiltIn ||
		builtinDiagnostics.State != domainplugin.StateActive {
		t.Fatalf("built-in diagnostics = %+v", builtinDiagnostics)
	}
	if builtinDiagnostics.Effects != 2 {
		t.Fatalf("built-in effects = %d, want the command and view disposers",
			builtinDiagnostics.Effects)
	}
	if len(builtinDiagnostics.Grants) != 3 {
		t.Fatalf("built-in grants = %+v", builtinDiagnostics.Grants)
	}
	if len(registrations.registered()) != 1 {
		t.Fatalf("registered views = %+v", registrations.registered())
	}
	commandID := domainplugin.QualifiedCommandID(firstPartyID, "ping")
	handler, ok := registrations.commands()[commandID]
	if !ok {
		t.Fatalf("built-in command is not registered: %+v", registrations.commands())
	}
	if err := handler(context.Background()); err != nil {
		t.Fatalf("built-in command: %v", err)
	}
	if len(dispatched) != 1 || dispatched[0] != commandID {
		t.Fatalf("dispatched commands = %+v", dispatched)
	}
	if len(feedback) != 1 || feedback[0] != "first-party output" {
		t.Fatalf("feedback = %+v", feedback)
	}
	logs := manager.DiagnosticsReport().Fibers
	var liveLogs []string
	for _, entry := range logs {
		if entry.PluginID == firstPartyID {
			liveLogs = entry.Logs
		}
	}
	if len(liveLogs) == 0 ||
		!strings.Contains(strings.Join(liveLogs, "\n"), "first-party dispatch") {
		t.Fatalf("built-in logs = %+v", liveLogs)
	}

	// Disabling uses the same unload path: both disposers run and the fiber is
	// gone.
	if err := manager.Disable(context.Background(), firstPartyID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if registrations.count() != 0 || len(registrations.registered()) != 0 {
		t.Fatalf("contributions survived disable: %+v %+v",
			registrations.commands(), registrations.registered())
	}
	if registrations.registrar.disposed != 1 || registrations.disposed != 1 {
		t.Fatalf("disposers ran: command=%d view=%d",
			registrations.registrar.disposed, registrations.disposed)
	}
	if _, ok := manager.Container().Fiber(
		domainplugin.NewFiberID(appplugin.BuiltinGroup, firstPartyID, 1)); ok {
		t.Fatal("disabled built-in still has a fiber")
	}
	plugin = findPlugin(t, manager, "built-in/"+firstPartyID)
	if plugin.Enabled || plugin.Status != appplugin.StatusDisabled {
		t.Fatalf("disabled built-in summary = %+v", plugin)
	}

	// Enabling is direct: no consent review exists for shipped code, and the
	// same contributions come back.
	if err := manager.Enable(context.Background(), firstPartyID, nil); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	plugin = findPlugin(t, manager, "built-in/"+firstPartyID)
	if !plugin.Enabled || plugin.Status != appplugin.StatusActive {
		t.Fatalf("re-enabled built-in summary = %+v", plugin)
	}
	if registrations.count() != 1 || len(registrations.registered()) != 1 {
		t.Fatalf("contributions after enable: %+v %+v",
			registrations.commands(), registrations.registered())
	}
}

// TestBuiltinPermissionsAreNotConsentGated proves the permission rows are
// visible and granted but the install consent flow is refused: the manifest is
// not reviewed and no decision is needed to enable the plugin.
func TestBuiltinPermissionsAreNotConsentGated(t *testing.T) {
	manager, _ := newFirstPartyManager(t)
	if err := manager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest(firstPartyID),
		Setup: func(context.Context, *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
			return appplugin.BuiltinFuncs{}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if _, err := manager.Review(context.Background(), firstPartyID); !errors.Is(err,
		appplugin.ErrBuiltinImmutable) {
		t.Fatalf("Review error = %v, want ErrBuiltinImmutable", err)
	}
	if err := manager.Revoke(context.Background(), firstPartyID,
		domainplugin.PermissionCommands); !errors.Is(err, appplugin.ErrBuiltinImmutable) {
		t.Fatalf("Revoke error = %v, want ErrBuiltinImmutable", err)
	}
	if err := manager.RequestOptional(context.Background(), firstPartyID,
		domainplugin.PermissionUINative, domainplugin.Scope{}, true, false); !errors.Is(err,
		appplugin.ErrBuiltinImmutable) {
		t.Fatalf("RequestOptional error = %v, want ErrBuiltinImmutable", err)
	}
	plugin := findPlugin(t, manager, "built-in/"+firstPartyID)
	if plugin.Status != appplugin.StatusActive {
		t.Fatalf("built-in without a consent decision = %+v", plugin)
	}
	for _, row := range plugin.Permissions {
		if !row.Granted {
			t.Fatalf("permission %s is not granted: %+v", row.Permission, row)
		}
	}
}

// TestBuiltinUsesPendingAndFailedSemantics covers the shared activation
// preconditions and failure recording. A missing injection leaves the fiber
// PENDING without calling Setup; a Setup error leaves it FAILED with a reason.
func TestBuiltinUsesPendingAndFailedSemantics(t *testing.T) {
	manager, _ := newFirstPartyManager(t)
	started := false
	pending := firstPartyManifest("acme.pending")
	pending.Inject = []domainplugin.Injection{{Service: "plugin.acme.missing.echo"}}
	err := manager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: pending,
		Setup: func(context.Context, *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
			started = true
			return appplugin.BuiltinFuncs{}, nil
		},
	})
	var missing *appplugin.MissingInjectionError
	if !errors.As(err, &missing) {
		t.Fatalf("pending registration error = %v, want MissingInjectionError", err)
	}
	if started {
		t.Fatal("Setup ran for a PENDING built-in")
	}
	state, fiber := builtinFiberState(t, manager, "acme.pending")
	if state != domainplugin.StatePending {
		t.Fatalf("pending fiber state = %s", state)
	}
	if got := fiber.MissingInjections(); len(got) != 1 || got[0] != "plugin.acme.missing.echo" {
		t.Fatalf("missing injections = %+v", got)
	}
	plugin := findPlugin(t, manager, "built-in/acme.pending")
	if plugin.Status != appplugin.StatusPending || !plugin.Enabled {
		t.Fatalf("pending summary = %+v", plugin)
	}

	failed := firstPartyManifest("acme.failed")
	err = manager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: failed,
		Setup: func(context.Context, *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
			return nil, errors.New("first-party boom")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "first-party boom") {
		t.Fatalf("failed registration error = %v", err)
	}
	state, fiber = builtinFiberState(t, manager, "acme.failed")
	if state != domainplugin.StateFailed || !strings.Contains(fiber.Reason(), "first-party boom") {
		t.Fatalf("failed fiber = %s %q", state, fiber.Reason())
	}
	plugin = findPlugin(t, manager, "built-in/acme.failed")
	if plugin.Status != appplugin.StatusFailed || !strings.Contains(plugin.Reason, "first-party boom") {
		t.Fatalf("failed summary = %+v", plugin)
	}
}

// TestBuiltinFailedSetupStillDisposesItsEffects covers a Setup that registers
// effects and returns a partial instance before failing. The failed-activation
// cleanup must release those effects in reverse order and shut the instance
// down, exactly like a failed contribution registration.
func TestBuiltinFailedSetupStillDisposesItsEffects(t *testing.T) {
	manager, _ := newFirstPartyManager(t)
	var order []string
	setup := func(_ context.Context, host *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
		if err := host.Dispose(func(context.Context) error {
			order = append(order, "first effect")
			return nil
		}); err != nil {
			return nil, err
		}
		if err := host.Dispose(func(context.Context) error {
			order = append(order, "second effect")
			return nil
		}); err != nil {
			return nil, err
		}
		return appplugin.BuiltinFuncs{
			ShutdownFunc: func(context.Context) error {
				order = append(order, "shutdown")
				return nil
			},
		}, errors.New("setup boom")
	}
	err := manager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest("acme.setup-failed"),
		Setup:    setup,
	})
	if err == nil || !strings.Contains(err.Error(), "setup boom") {
		t.Fatalf("failed registration error = %v", err)
	}
	if want := []string{"second effect", "first effect", "shutdown"}; !equalStrings(order, want) {
		t.Fatalf("failed-setup cleanup order = %v, want %v", order, want)
	}
	state, _ := builtinFiberState(t, manager, "acme.setup-failed")
	if state != domainplugin.StateFailed {
		t.Fatalf("failed fiber state = %s", state)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestBuiltinFailureIsIsolatedFromThirdPartyFiber registers a built-in whose
// Setup panics while a real installed fiber is ACTIVE. The panic becomes a
// contained trap, the built-in is FAILED, and the third-party command still
// dispatches.
func TestBuiltinFailureIsIsolatedFromThirdPartyFiber(t *testing.T) {
	thirdParty := managerManifest("acme.third-party", nil, nil, nil)
	manager, _, validator, registrations := managerFixture(t, thirdParty, nil, nil,
		appplugin.NopAudit{})
	installForManager(t, manager, validator)
	if err := manager.Enable(context.Background(), thirdParty.ID, nil); err != nil {
		t.Fatalf("Enable third-party: %v", err)
	}
	err := manager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest(firstPartyID),
		Setup: func(context.Context, *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
			panic("first-party exploded")
		},
	})
	var trap *appplugin.TrapError
	if !errors.As(err, &trap) {
		t.Fatalf("built-in panic error = %v, want TrapError", err)
	}
	state, _ := builtinFiberState(t, manager, firstPartyID)
	if state != domainplugin.StateFailed {
		t.Fatalf("built-in fiber state = %s", state)
	}
	fiber, ok := manager.Container().Fiber(
		domainplugin.NewFiberID("installed", thirdParty.ID, 1))
	if !ok || fiber.State() != domainplugin.StateActive {
		t.Fatalf("third-party fiber = %v, %v", fiber, ok)
	}
	handler, ok := registrations.commands()[domainplugin.QualifiedCommandID(thirdParty.ID, "run")]
	if !ok {
		t.Fatalf("third-party command is missing: %+v", registrations.commands())
	}
	if err := handler(context.Background()); err != nil {
		t.Fatalf("third-party command after built-in failure: %v", err)
	}
}

// TestBuiltinInstallUpdateAndUninstallAreRefused covers every runtime action
// that could replace shipped code or its consent state.
func TestBuiltinInstallUpdateAndUninstallAreRefused(t *testing.T) {
	native := managerManifest("acme.native", nil, nil, nil)
	manager, _, validator, _ := managerFixture(t, native, nil, nil, appplugin.NopAudit{})
	if err := manager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest(firstPartyID),
		Setup: func(context.Context, *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
			return appplugin.BuiltinFuncs{}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	validator.pkg = appplugin.Package{Manifest: firstPartyManifest(firstPartyID)}
	checks := []struct {
		name string
		err  error
	}{
		{"InstallFile", func() error {
			_, err := manager.InstallFile(context.Background(), "builtin.natplug")
			return err
		}()},
		{"InstallFromRegistry", func() error {
			_, err := manager.InstallFromRegistry(
				context.Background(), firstPartyID, "1.0.0", nil)
			return err
		}()},
		{"PlanUpdate", func() error {
			_, err := manager.PlanUpdate(context.Background(), firstPartyID)
			return err
		}()},
		{"ApplyUpdate", func() error {
			_, err := manager.ApplyUpdate(context.Background(),
				appplugin.UpdatePlan{ID: firstPartyID}, nil)
			return err
		}()},
		{"Rollback", func() error {
			_, err := manager.Rollback(context.Background(), firstPartyID, nil)
			return err
		}()},
		{"Uninstall", manager.Uninstall(context.Background(), firstPartyID, true)},
		{"PurgeData", manager.PurgeData(context.Background(), firstPartyID)},
	}
	for _, check := range checks {
		if !errors.Is(check.err, appplugin.ErrBuiltinImmutable) {
			t.Fatalf("%s error = %v, want ErrBuiltinImmutable", check.name, check.err)
		}
	}
}

// TestBuiltinDependencyIsSatisfiedWithoutARegistryInstall proves a built-in
// occupies its id for dependency resolution: a package that depends on it
// installs without fetching the built-in, and an unsatisfied range fails
// before anything is written.
func TestBuiltinDependencyIsSatisfiedWithoutARegistryInstall(t *testing.T) {
	id := "acme.consumer"
	registry, validator := newFakeRegistry(t, "fixture.registry", registryPluginSpec{
		id: id, name: "Consumer",
		versions: []registryVersionSpec{{
			version: "1.0.0", channel: domainplugin.ChannelStable,
			dependencies: []domainplugin.Dependency{{
				ID: firstPartyID, Version: "^2.0",
			}},
		}},
	})
	store := newMemoryStore(t)
	manager := appplugin.NewManager(fake.New(), nil,
		&registrar{}, validator, store,
		appplugin.WithRegistry(registry),
		appplugin.WithAppVersion("2.3.0"))
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest(firstPartyID),
		Setup: func(context.Context, *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
			return appplugin.BuiltinFuncs{}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	record, err := manager.InstallFromRegistry(context.Background(), id, "1.0.0", nil)
	if err != nil {
		t.Fatalf("InstallFromRegistry: %v", err)
	}
	if record.ID != id {
		t.Fatalf("installed record = %+v", record)
	}
	if _, installed, err := store.Record(context.Background(), firstPartyID); err != nil || installed {
		t.Fatalf("built-in was installed into the package store: %v %v", installed, err)
	}

	strict := "acme.strict"
	strictRegistry, strictValidator := newFakeRegistry(t, "fixture.registry",
		registryPluginSpec{
			id: strict, name: "Strict",
			versions: []registryVersionSpec{{
				version: "1.0.0", channel: domainplugin.ChannelStable,
				dependencies: []domainplugin.Dependency{{
					ID: firstPartyID, Version: "^3.0",
				}},
			}},
		})
	strictStore := newMemoryStore(t)
	strictManager := appplugin.NewManager(fake.New(), nil, &registrar{},
		strictValidator, strictStore, appplugin.WithRegistry(strictRegistry),
		appplugin.WithAppVersion("2.3.0"))
	t.Cleanup(func() { _ = strictManager.Close(context.Background()) })
	if err := strictManager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest(firstPartyID),
		Setup: func(context.Context, *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
			return appplugin.BuiltinFuncs{}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterBuiltin strict: %v", err)
	}
	if _, err := strictManager.InstallFromRegistry(
		context.Background(), strict, "1.0.0", nil); err == nil {
		t.Fatal("an unsatisfied built-in dependency range was accepted")
	}
	if _, installed, _ := strictStore.Record(context.Background(), strict); installed {
		t.Fatal("a failed dependency check wrote the package")
	}
}

// TestBuiltinUnloadAndQuitAreBounded proves a built-in that ignores context
// cancellation cannot pin Disable or Manager.Close. Both return at the host
// deadline; the shutdown goroutines are released during cleanup.
func TestBuiltinUnloadAndQuitAreBounded(t *testing.T) {
	var mu sync.Mutex
	var releases []chan struct{}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, release := range releases {
			close(release)
		}
	})
	setup := func(context.Context, *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
		release := make(chan struct{})
		mu.Lock()
		releases = append(releases, release)
		mu.Unlock()
		return appplugin.BuiltinFuncs{
			ShutdownFunc: func(context.Context) error {
				<-release
				return nil
			},
		}, nil
	}
	manager, _ := newFirstPartyManager(t,
		appplugin.WithManagerUnloadTimeout(40*time.Millisecond),
		appplugin.WithManagerGlobalUnloadBudget(90*time.Millisecond),
	)
	if err := manager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest(firstPartyID),
		Setup:    setup,
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}

	start := time.Now()
	err := manager.Disable(context.Background(), firstPartyID)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Disable error = %v, want the unload deadline", err)
	}
	if elapsed < 20*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("Disable took %v, want the bounded unload window", elapsed)
	}

	if err := manager.Enable(context.Background(), firstPartyID, nil); err != nil {
		t.Fatalf("re-Enable: %v", err)
	}
	start = time.Now()
	err = manager.Close(context.Background())
	elapsed = time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want the global quit budget", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Close took %v, want the bounded quit window", elapsed)
	}
}

// TestBuiltinDisabledStatePersists uses the manager's state store to prove a
// disabled built-in stays disabled across registrations (an app restart) and
// is not activated until the user enables it.
func TestBuiltinDisabledStatePersists(t *testing.T) {
	state := appplugin.NewMemoryBuiltinStateStore()
	first, _ := newFirstPartyManager(t, appplugin.WithBuiltinStateStore(state))
	setup := func(context.Context, *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
		return appplugin.BuiltinFuncs{}, nil
	}
	if err := first.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest(firstPartyID), Setup: setup,
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if err := first.Disable(context.Background(), firstPartyID); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	second, registrations := newFirstPartyManager(t,
		appplugin.WithBuiltinStateStore(state))
	if err := second.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest(firstPartyID), Setup: setup,
	}); err != nil {
		t.Fatalf("RegisterBuiltin after restart: %v", err)
	}
	plugin := findPlugin(t, second, "built-in/"+firstPartyID)
	if plugin.Enabled || plugin.Status != appplugin.StatusDisabled {
		t.Fatalf("restarted built-in summary = %+v", plugin)
	}
	if registrations.count() != 0 {
		t.Fatalf("a disabled built-in registered contributions: %+v", registrations.commands())
	}
	if err := second.Enable(context.Background(), firstPartyID, nil); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if findPlugin(t, second, "built-in/"+firstPartyID).Status != appplugin.StatusActive {
		t.Fatal("enabled built-in is not active")
	}
}

type failingBuiltinStateStore struct{ err error }

func (s failingBuiltinStateStore) BuiltinEnabled(context.Context, string) (bool, bool, error) {
	return false, false, s.err
}

func (s failingBuiltinStateStore) SaveBuiltinEnabled(context.Context, string, bool) error {
	return s.err
}

// TestBuiltinStateReadFailureRollsBackRegistration proves a broken state store
// fails closed: no half-registered PENDING entry is left behind, so a retry
// cannot silently flip a user-disabled plugin back on.
func TestBuiltinStateReadFailureRollsBackRegistration(t *testing.T) {
	manager := appplugin.NewManager(nil, nil, &viewRegistrar{registrar: &registrar{}},
		nil, newMemoryStore(t), appplugin.WithBuiltinStateStore(
			failingBuiltinStateStore{err: errors.New("state unavailable")}))
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	err := manager.RegisterBuiltin(context.Background(), appplugin.Builtin{
		Manifest: firstPartyManifest(firstPartyID),
		Setup: func(context.Context, *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
			return appplugin.BuiltinFuncs{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "state unavailable") {
		t.Fatalf("RegisterBuiltin error = %v", err)
	}
	if fiber, ok := manager.Container().Fiber(
		domainplugin.NewFiberID(appplugin.BuiltinGroup, firstPartyID, 1)); ok {
		t.Fatalf("failed registration left a fiber behind: %s", fiber.State())
	}
	list, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("failed registration left a summary behind: %+v", list)
	}
	if err := manager.Enable(context.Background(), firstPartyID, nil); !errors.Is(
		err, appplugin.ErrNotInstalled) {
		t.Fatalf("Enable after rollback = %v, want ErrNotInstalled", err)
	}
}
