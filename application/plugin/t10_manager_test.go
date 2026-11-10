package plugin_test

import (
	"context"
	"errors"
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

func TestGroupDefaultsFillUnsetConfigurationAndStoredValuesWin(t *testing.T) {
	fixture := newSettingsManagerFixture(t, true)
	if err := fixture.manager.Container().DefineGroup(domainplugin.Group{
		Name: "installed",
		Defaults: map[string]map[string]any{
			fixture.id: {"mode": "group", "optional_note": "from-group"},
		},
	}); err != nil {
		t.Fatalf("DefineGroup: %v", err)
	}
	fixture.config.Set(fixture.id, map[string]any{"mode": "slow"})
	if err := fixture.enable(t); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	fiber, ok := fixture.manager.Container().Fiber(
		domainplugin.NewFiberID("installed", fixture.id, 1))
	if !ok {
		t.Fatal("the enabled fiber is missing")
	}
	settings := fiber.Settings()
	if settings["mode"] != "slow" {
		t.Fatalf("mode = %v, want the stored value", settings["mode"])
	}
	if settings["optional_note"] != "from-group" {
		t.Fatalf("optional_note = %v, want the group default", settings["optional_note"])
	}
	if settings["retries"] != int64(3) {
		t.Fatalf("retries = %v, want the manifest default", settings["retries"])
	}
}

func TestReloadPreservesStoredConfigurationAndVolatileFieldsOnly(t *testing.T) {
	fixture := newSettingsManagerFixture(t, true)
	if err := fixture.enable(t); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	fiber, ok := fixture.manager.Container().Fiber(
		domainplugin.NewFiberID("installed", fixture.id, 1))
	if !ok {
		t.Fatal("the enabled fiber is missing")
	}
	if err := fixture.manager.SaveSettings(context.Background(), fixture.id, map[string]any{
		"mode": "fast", "debug": false,
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if fiber.Settings()["debug"] != false {
		t.Fatalf("live volatile debug = %v, want false", fiber.Settings()["debug"])
	}
	// A stored configuration change made outside the settings surface is
	// picked up by a reload; the volatile value is preserved, and no other
	// guest-visible state crosses the reload.
	fixture.config.Set(fixture.id, map[string]any{"mode": "slow"})
	if err := fixture.manager.Reload(context.Background(), fixture.id); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if fiber.State() != domainplugin.StateActive {
		t.Fatalf("state after reload = %s (%s)", fiber.State(), fiber.Reason())
	}
	settings := fiber.Settings()
	if settings["mode"] != "slow" {
		t.Fatalf("mode after reload = %v, want the stored value", settings["mode"])
	}
	if settings["debug"] != false {
		t.Fatalf("volatile debug after reload = %v, want it preserved", settings["debug"])
	}
	if settings["retries"] != int64(3) {
		t.Fatalf("retries after reload = %v, want the manifest default", settings["retries"])
	}
	diagnostics, ok := fixture.manager.PluginDiagnostics(fixture.id)
	if !ok {
		t.Fatal("diagnostics missing after reload")
	}
	if diagnostics.Generation != 1 || diagnostics.ManualRestarts != 1 {
		t.Fatalf("diagnostics after reload = %+v", diagnostics)
	}
}

func TestManagerDiagnosticsReportGrantsAndConfigurationErrors(t *testing.T) {
	fixture := newSettingsManagerFixture(t, false)
	if err := fixture.enable(t); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	report := fixture.manager.DiagnosticsReport()
	if len(report.Fibers) != 1 {
		t.Fatalf("diagnostics fibers = %+v", report.Fibers)
	}
	entry := report.Fibers[0]
	if entry.PluginID != fixture.id || entry.State != domainplugin.StateActive {
		t.Fatalf("diagnostics entry = %+v", entry)
	}
	if len(entry.Grants) != 1 ||
		entry.Grants[0].Permission != domainplugin.PermissionCommands {
		t.Fatalf("grants = %+v, want the consented commands grant", entry.Grants)
	}
	if entry.Effects != 1 || entry.MemoryBytes <= 0 || !hasTiming(entry.Timings, "init") {
		t.Fatalf("runtime diagnostics = %+v", entry)
	}
	groups := make(map[string]bool, len(report.Groups))
	for _, group := range report.Groups {
		groups[group.Name] = true
	}
	if !groups["installed"] {
		t.Fatalf("groups = %+v, want installed", report.Groups)
	}

	invalid := newSettingsManagerFixture(t, true)
	invalid.config.Set(invalid.id, map[string]any{
		"mode": "turbo", "retries": int64(3), "ratio": 0.5,
	})
	if err := invalid.enable(t); err == nil {
		t.Fatal("Enable accepted an invalid stored configuration")
	}
	invalidEntry, ok := invalid.manager.PluginDiagnostics(invalid.id)
	if !ok {
		t.Fatal("the PENDING fiber is missing from diagnostics")
	}
	if invalidEntry.State != domainplugin.StatePending {
		t.Fatalf("state = %s, want PENDING", invalidEntry.State)
	}
	if !strings.HasPrefix(invalidEntry.Reason, domainplugin.ReasonConfigInvalid) {
		t.Fatalf("reason = %q", invalidEntry.Reason)
	}
	if len(invalidEntry.ConfigErrors) == 0 ||
		invalidEntry.ConfigErrors[0].Field != "mode" {
		t.Fatalf("config errors = %+v", invalidEntry.ConfigErrors)
	}
	if invalidEntry.Effects != 0 {
		t.Fatalf("a PENDING plugin owns %d effects", invalidEntry.Effects)
	}
}

type t10MutableManifests struct {
	mu       sync.Mutex
	manifest domainplugin.Manifest
}

func (s *t10MutableManifests) Load(string) (domainplugin.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifest, nil
}

func (s *t10MutableManifests) set(manifest domainplugin.Manifest) {
	s.mu.Lock()
	s.manifest = manifest
	s.mu.Unlock()
}

func TestDevelopmentWatcherReloadsOncePerChange(t *testing.T) {
	manifest := managerManifest("acme.watch", []string{"commands"}, nil, nil)
	source := &t10MutableManifests{manifest: manifest}
	validator := &fixedValidator{pkg: appplugin.Package{
		Manifest: manifest,
		Files: map[string][]byte{
			"plugin.toml": []byte("manifest"),
			"plugin.wasm": fixtureModule,
		},
		SHA256: "package-hash",
	}}
	store := newMemoryStore(t)
	registrar := &registrar{}
	manager := appplugin.NewManager(fake.New(), source, registrar, validator, store,
		appplugin.WithAudit(&recordingAudit{}),
		appplugin.WithManagerClock(func() time.Time {
			return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
		}))
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte("manifest-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("module-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	dev, err := manager.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	watcher := manager.NewDevelopmentWatcher(time.Millisecond)
	changes, err := watcher.Check(context.Background())
	if err != nil || len(changes) != 0 {
		t.Fatalf("initial Check = %+v, %v; want only a baseline fingerprint", changes, err)
	}

	updated := manifest
	updated.Version = "1.0.1"
	source.set(updated)
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte("manifest-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err = watcher.Check(context.Background())
	if err != nil {
		t.Fatalf("Check after manifest change: %v", err)
	}
	if len(changes) != 1 || changes[0].ID != "acme.watch" {
		t.Fatalf("changes = %+v, want exactly one reload", changes)
	}
	diagnostics, ok := manager.PluginDiagnostics("acme.watch")
	if !ok {
		t.Fatal("development diagnostics missing")
	}
	if diagnostics.State != domainplugin.StateActive ||
		diagnostics.Generation != 1 || diagnostics.Restarts != 1 {
		t.Fatalf("diagnostics after manifest change = %+v", diagnostics)
	}
	if dev.Fiber.Manifest().Version != "1.0.1" {
		t.Fatalf("manifest version = %q, want the re-decoded manifest",
			dev.Fiber.Manifest().Version)
	}
	if registrar.count() != 1 {
		t.Fatalf("contributions after reload = %d, want the newly registered command", registrar.count())
	}
	if changes, err := watcher.Check(context.Background()); err != nil || len(changes) != 0 {
		t.Fatalf("unchanged Check = %+v, %v", changes, err)
	}
	if got := dev.Fiber.Manifest().Version; got != "1.0.1" {
		t.Fatalf("an unchanged directory was reloaded again (%s)", got)
	}

	invalid := updated
	invalid.ABI = 9
	source.set(invalid)
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("module-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err = watcher.Check(context.Background())
	if len(changes) != 1 {
		t.Fatalf("changes after module change = %+v", changes)
	}
	if err == nil {
		t.Fatal("an invalid reload reported no error")
	}
	if dev.Fiber.State() != domainplugin.StateFailed {
		t.Fatalf("state after invalid reload = %s (%s)", dev.Fiber.State(), dev.Fiber.Reason())
	}
	diagnostics, _ = manager.PluginDiagnostics("acme.watch")
	if diagnostics.Generation != 2 || diagnostics.Restarts != 2 {
		t.Fatalf("a failed reload was retried: %+v", diagnostics)
	}
	if registrar.count() != 0 {
		t.Fatalf("a failed reload left %d effects", registrar.count())
	}
	if changes, err := watcher.Check(context.Background()); err != nil || len(changes) != 0 {
		t.Fatalf("Check after a failed reload retried: %+v, %v", changes, err)
	}
}

// t10PathManifests serves manifests by directory base. One manager can then
// hold an installed package and a development directory that share a plugin
// id while each directory keeps its own manifest bytes.
type t10PathManifests struct {
	mu        sync.Mutex
	manifests map[string]domainplugin.Manifest
}

func newT10PathManifests(manifests map[string]domainplugin.Manifest) *t10PathManifests {
	return &t10PathManifests{manifests: manifests}
}

func (s *t10PathManifests) Load(directory string) (domainplugin.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest, ok := s.manifests[filepath.Base(directory)]
	if !ok {
		return domainplugin.Manifest{}, errors.New("no manifest for " + directory)
	}
	return manifest, nil
}

func (s *t10PathManifests) set(directory string, manifest domainplugin.Manifest) {
	s.mu.Lock()
	s.manifests[filepath.Base(directory)] = manifest
	s.mu.Unlock()
}

// t10FailOnceRuntime fails its first instantiation and delegates afterwards.
// It makes the installed package FAILED without taking the development
// directory down with it.
type t10FailOnceRuntime struct {
	mu     sync.Mutex
	failed bool
	inner  appplugin.Runtime
}

func (r *t10FailOnceRuntime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	r.mu.Lock()
	fail := !r.failed
	r.failed = true
	r.mu.Unlock()
	if fail {
		return nil, errors.New("t10: installed module failed to instantiate")
	}
	return r.inner.Instantiate(ctx, spec, sink)
}

func (r *t10FailOnceRuntime) Close(ctx context.Context) error { return r.inner.Close(ctx) }

// TestDevelopmentWatcherTargetsTheDevelopmentFiberWhenAnInstalledCopySharesItsID
// covers the coexistence the container explicitly supports: an installed
// plugin and a development directory with the same plugin id. A change to the
// development directory must reload the development fiber exactly once,
// whether the installed fiber is ACTIVE, PENDING, or FAILED.
func TestDevelopmentWatcherTargetsTheDevelopmentFiberWhenAnInstalledCopySharesItsID(t *testing.T) {
	tests := []struct {
		name          string
		pending       bool
		failed        bool
		installedWant domainplugin.State
	}{
		{name: "active", installedWant: domainplugin.StateActive},
		{name: "pending", pending: true, installedWant: domainplugin.StatePending},
		{name: "failed", failed: true, installedWant: domainplugin.StateFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const id = "acme.shared"
			installedManifest := t10BareManifest(id)
			installedManifest.Wasm.SHA256 = fixtureModuleHash()
			if test.pending {
				installedManifest.Settings = domainplugin.SettingsSchema{
					Version: domainplugin.CurrentSettingsSchemaVersion,
					Fields: []domainplugin.SettingsField{{
						Key: "required_note", Type: domainplugin.SettingTypeString, Required: true,
					}},
				}
			}
			devManifest := t10BareManifest(id)
			devManifest.Wasm.SHA256 = fixtureModuleHash()

			devDir := filepath.Join(t.TempDir(), "devcopy")
			if err := os.MkdirAll(devDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(devDir, "plugin.toml"), []byte("manifest-v1"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(devDir, "plugin.wasm"), fixtureModule, 0o644); err != nil {
				t.Fatal(err)
			}

			var runtime appplugin.Runtime = fake.New()
			if test.failed {
				runtime = &t10FailOnceRuntime{inner: fake.New()}
			}
			source := newT10PathManifests(map[string]domainplugin.Manifest{
				"1.0.0":   installedManifest,
				"devcopy": devManifest,
			})
			validator := &fixedValidator{pkg: appplugin.Package{
				Manifest: installedManifest,
				Files: map[string][]byte{
					"plugin.toml": []byte("manifest"),
					"plugin.wasm": fixtureModule,
				},
				SHA256: "package-hash",
			}}
			manager := appplugin.NewManager(runtime, source, &registrar{}, validator,
				newMemoryStore(t), appplugin.WithAudit(&recordingAudit{}))
			t.Cleanup(func() { _ = manager.Close(context.Background()) })

			record, err := manager.InstallFile(context.Background(), "plugin.natplug")
			if err != nil {
				t.Fatalf("InstallFile: %v", err)
			}
			enableErr := manager.Enable(context.Background(), record.ID, nil)
			if !test.pending && !test.failed && enableErr != nil {
				t.Fatalf("Enable: %v", enableErr)
			}
			if (test.pending || test.failed) && enableErr == nil {
				t.Fatal("Enable reported success for a fiber that cannot activate")
			}
			installed, ok := manager.Container().Fiber(
				domainplugin.NewFiberID("installed", id, 1))
			if !ok {
				t.Fatal("the installed fiber is missing")
			}
			if got := installed.State(); got != test.installedWant {
				t.Fatalf("installed state = %s (%s), want %s",
					got, installed.Reason(), test.installedWant)
			}
			installedBefore, ok := manager.Container().PluginDiagnostics(installed.ID())
			if !ok {
				t.Fatal("installed diagnostics are missing")
			}

			dev, err := manager.LoadDevelopment(context.Background(), devDir)
			if err != nil {
				t.Fatalf("LoadDevelopment: %v", err)
			}
			if dev.Fiber.State() != domainplugin.StateActive {
				t.Fatalf("development state = %s (%s)", dev.Fiber.State(), dev.Fiber.Reason())
			}

			watcher := manager.NewDevelopmentWatcher(time.Millisecond)
			baseline, err := watcher.Check(context.Background())
			if err != nil || len(baseline) != 0 {
				t.Fatalf("baseline Check = %+v, %v", baseline, err)
			}

			updated := devManifest
			updated.Version = "1.0.1"
			source.set(devDir, updated)
			if err := os.WriteFile(filepath.Join(devDir, "plugin.wasm"),
				[]byte("module-v2"), 0o644); err != nil {
				t.Fatal(err)
			}
			changes, err := watcher.Check(context.Background())
			if err != nil {
				t.Fatalf("Check after a development change: %v", err)
			}
			if len(changes) != 1 || changes[0].ID != id {
				t.Fatalf("changes = %+v, want exactly the development reload", changes)
			}

			devEntry, ok := manager.Container().PluginDiagnostics(dev.Fiber.ID())
			if !ok {
				t.Fatal("development diagnostics are missing")
			}
			if devEntry.Generation != 1 || devEntry.Restarts != 1 {
				t.Fatalf("development fiber was not reloaded once: %+v", devEntry)
			}
			if dev.Fiber.Manifest().Version != "1.0.1" {
				t.Fatalf("development manifest = %q, want the re-decoded manifest",
					dev.Fiber.Manifest().Version)
			}

			installedAfter, ok := manager.Container().PluginDiagnostics(installed.ID())
			if !ok {
				t.Fatal("installed diagnostics disappeared")
			}
			if installedAfter.Generation != installedBefore.Generation ||
				installedAfter.Restarts != installedBefore.Restarts ||
				installedAfter.State != installedBefore.State {
				t.Fatalf("the installed fiber changed: before %+v, after %+v",
					installedBefore, installedAfter)
			}
			if installedAfter.Generation != 0 || installedAfter.Restarts != 0 {
				t.Fatalf("the installed fiber was reloaded: %+v", installedAfter)
			}

			if changes, err := watcher.Check(context.Background()); err != nil || len(changes) != 0 {
				t.Fatalf("an unchanged directory triggered another reload: %+v, %v", changes, err)
			}
			devEntry, _ = manager.Container().PluginDiagnostics(dev.Fiber.ID())
			if devEntry.Generation != 1 || devEntry.Restarts != 1 {
				t.Fatalf("a change caused more than one reload: %+v", devEntry)
			}
		})
	}
}

// t10DiskManifests behaves like the production manifest adapter: Load reads
// plugin.toml from disk again on every call. A test registers the manifest a
// given file body declares, so an edited plugin.toml is observed by the next
// reload exactly as it would be in the app.
type t10DiskManifests struct {
	mu     sync.Mutex
	byBody map[string]domainplugin.Manifest
}

func (s *t10DiskManifests) Load(directory string) (domainplugin.Manifest, error) {
	data, err := os.ReadFile(filepath.Join(directory, appplugin.ManifestFileName))
	if err != nil {
		return domainplugin.Manifest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest, ok := s.byBody[string(data)]
	if !ok {
		return domainplugin.Manifest{}, errors.New("t10: unrecognized plugin.toml body")
	}
	return manifest, nil
}

func t10WriteFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDevelopmentWatcherReloadsOnceWhenTheModulePathChanges covers criterion 5
// when the edited manifest also changes wasm.path: the reload replaces the
// fiber's manifest, so the watcher must baseline the post-reload module path
// or the same edit is reloaded a second time.
func TestDevelopmentWatcherReloadsOnceWhenTheModulePathChanges(t *testing.T) {
	const id = "acme.pathchange"
	first := managerManifest(id, nil, nil, nil)
	second := first
	second.Version = "1.0.1"
	second.Wasm.Path = "plugin-next.wasm"
	source := &t10DiskManifests{byBody: map[string]domainplugin.Manifest{
		"manifest-v1": first,
		"manifest-v2": second,
	}}
	manager := appplugin.NewManager(fake.New(), source, &registrar{},
		&fixedValidator{pkg: appplugin.Package{Manifest: first}}, newMemoryStore(t),
		appplugin.WithAudit(&recordingAudit{}))
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	dir := t.TempDir()
	t10WriteFile(t, dir, "plugin.toml", []byte("manifest-v1"))
	t10WriteFile(t, dir, "plugin.wasm", []byte("module-v1"))
	// The new module path already exists, so the reload succeeds.
	t10WriteFile(t, dir, "plugin-next.wasm", []byte("module-v2"))

	dev, err := manager.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	if dev.Fiber.State() != domainplugin.StateActive {
		t.Fatalf("development state = %s (%s)", dev.Fiber.State(), dev.Fiber.Reason())
	}
	watcher := manager.NewDevelopmentWatcher(time.Millisecond)
	if changes, err := watcher.Check(context.Background()); err != nil || len(changes) != 0 {
		t.Fatalf("baseline Check = %+v, %v", changes, err)
	}

	// One development change: the manifest now names a different module.
	t10WriteFile(t, dir, "plugin.toml", []byte("manifest-v2"))
	changes, err := watcher.Check(context.Background())
	if err != nil {
		t.Fatalf("Check after the module path change: %v", err)
	}
	if len(changes) != 1 || changes[0].ID != id {
		t.Fatalf("changes = %+v, want exactly one reload", changes)
	}
	if got := dev.Fiber.Manifest().Wasm.Path; got != "plugin-next.wasm" {
		t.Fatalf("module path after reload = %q, want the re-decoded manifest", got)
	}
	diagnostics, ok := manager.PluginDiagnostics(id)
	if !ok {
		t.Fatal("development diagnostics missing")
	}
	if diagnostics.State != domainplugin.StateActive ||
		diagnostics.Generation != 1 || diagnostics.Restarts != 1 {
		t.Fatalf("diagnostics after the module path change = %+v", diagnostics)
	}

	// The reloaded manifest, including its module path, is the baseline now:
	// the same edit must not cause a second restart.
	if changes, err := watcher.Check(context.Background()); err != nil || len(changes) != 0 {
		t.Fatalf("an unchanged directory was reloaded again: %+v, %v", changes, err)
	}
	diagnostics, _ = manager.PluginDiagnostics(id)
	if diagnostics.Generation != 1 || diagnostics.Restarts != 1 {
		t.Fatalf("one development change caused more than one restart: %+v", diagnostics)
	}
}

// TestDevelopmentWatcherDoesNotRetryAFailedReloadUntilTheFilesChange covers the
// second half of criterion 5: a reload that fails because the new module path
// is missing must be recorded as the baseline, not retried on every Check, and
// a later file change must still trigger exactly one attempt.
func TestDevelopmentWatcherDoesNotRetryAFailedReloadUntilTheFilesChange(t *testing.T) {
	const id = "acme.missingmodule"
	first := managerManifest(id, nil, nil, nil)
	second := first
	second.Version = "1.0.1"
	second.Wasm.Path = "plugin-missing.wasm"
	source := &t10DiskManifests{byBody: map[string]domainplugin.Manifest{
		"manifest-v1": first,
		"manifest-v2": second,
	}}
	manager := appplugin.NewManager(fake.New(), source, &registrar{},
		&fixedValidator{pkg: appplugin.Package{Manifest: first}}, newMemoryStore(t),
		appplugin.WithAudit(&recordingAudit{}))
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	dir := t.TempDir()
	t10WriteFile(t, dir, "plugin.toml", []byte("manifest-v1"))
	t10WriteFile(t, dir, "plugin.wasm", []byte("module-v1"))

	dev, err := manager.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	if dev.Fiber.State() != domainplugin.StateActive {
		t.Fatalf("development state = %s (%s)", dev.Fiber.State(), dev.Fiber.Reason())
	}
	watcher := manager.NewDevelopmentWatcher(time.Millisecond)
	if changes, err := watcher.Check(context.Background()); err != nil || len(changes) != 0 {
		t.Fatalf("baseline Check = %+v, %v", changes, err)
	}

	// The manifest now names a module that does not exist: one FAILED attempt.
	t10WriteFile(t, dir, "plugin.toml", []byte("manifest-v2"))
	changes, err := watcher.Check(context.Background())
	if len(changes) != 1 || changes[0].ID != id {
		t.Fatalf("changes = %+v, want exactly one reload attempt", changes)
	}
	if err == nil {
		t.Fatal("a reload from a missing module path reported no error")
	}
	if got := dev.Fiber.State(); got != domainplugin.StateFailed {
		t.Fatalf("state after the failed reload = %s (%s)", got, dev.Fiber.Reason())
	}
	diagnostics, ok := manager.PluginDiagnostics(id)
	if !ok {
		t.Fatal("development diagnostics missing")
	}
	if diagnostics.Generation != 1 || diagnostics.Restarts != 1 {
		t.Fatalf("diagnostics after the failed reload = %+v", diagnostics)
	}

	// Unchanged files: the failure is not retried.
	changes, err = watcher.Check(context.Background())
	if err != nil || len(changes) != 0 {
		t.Fatalf("a failed reload was retried without a file change: %+v, %v", changes, err)
	}
	diagnostics, _ = manager.PluginDiagnostics(id)
	if diagnostics.Generation != 1 || diagnostics.Restarts != 1 {
		t.Fatalf("a failed reload was retried: %+v", diagnostics)
	}

	// Creating the missing module is a real file change: exactly one attempt,
	// this time successful.
	t10WriteFile(t, dir, "plugin-missing.wasm", []byte("module-v2"))
	changes, err = watcher.Check(context.Background())
	if err != nil {
		t.Fatalf("Check after the module appeared: %v", err)
	}
	if len(changes) != 1 || changes[0].ID != id {
		t.Fatalf("changes = %+v, want exactly one retry after the file change", changes)
	}
	if got := dev.Fiber.State(); got != domainplugin.StateActive {
		t.Fatalf("state after the retry = %s (%s)", got, dev.Fiber.Reason())
	}
	diagnostics, _ = manager.PluginDiagnostics(id)
	if diagnostics.Generation != 2 || diagnostics.Restarts != 2 {
		t.Fatalf("diagnostics after the retry = %+v", diagnostics)
	}

	if changes, err := watcher.Check(context.Background()); err != nil || len(changes) != 0 {
		t.Fatalf("an unchanged directory was reloaded again: %+v, %v", changes, err)
	}
}
