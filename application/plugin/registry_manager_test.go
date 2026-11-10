package plugin_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/fake"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// fakeRegistry serves the static registry layout from memory for manager
// tests. Package bodies are opaque to the manager besides their declared size
// and hash; the paired validator returns the parsed package for the staged
// body name.
type fakeRegistry struct {
	identity string
	index    domainplugin.RegistryIndex
	plugins  map[string]domainplugin.RegistryPlugin
	bodies   map[string][]byte
}

func (r *fakeRegistry) Identity() string { return r.identity }

func (r *fakeRegistry) Index(context.Context) (domainplugin.RegistryIndex, error) {
	return r.index, nil
}

func (r *fakeRegistry) Plugin(_ context.Context,
	id string) (domainplugin.RegistryPlugin, error) {
	plugin, ok := r.plugins[id]
	if !ok {
		return domainplugin.RegistryPlugin{}, fmt.Errorf("registry has no plugin %s", id)
	}
	return plugin, nil
}

func (r *fakeRegistry) FetchPackage(_ context.Context, id, version string) ([]byte, error) {
	body, ok := r.bodies[id+"@"+version]
	if !ok {
		return nil, fmt.Errorf("registry has no package %s %s", id, version)
	}
	return body, nil
}

// registryBodyValidator maps the staged registry package name back to the
// validated package the fake validator would have produced.
type registryBodyValidator struct {
	packages map[string]appplugin.Package
}

func (v *registryBodyValidator) Validate(_ context.Context,
	path string) (appplugin.Package, error) {
	name := strings.TrimSuffix(filepath.Base(path), ".natplug")
	pkg, ok := v.packages[name]
	if !ok {
		return appplugin.Package{}, fmt.Errorf("no fixture package for %s", name)
	}
	return pkg, nil
}

type registryVersionSpec struct {
	version      string
	channel      domainplugin.Channel
	required     []string
	optional     []string
	scopes       map[string]domainplugin.Scope
	dependencies []domainplugin.Dependency
	yanked       bool
	deprecated   string
	revoked      string
	module       []byte
	omitModule   bool
}

type registryPluginSpec struct {
	id          string
	name        string
	description string
	keywords    []string
	versions    []registryVersionSpec
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newFakeRegistry(t *testing.T, identity string,
	specs ...registryPluginSpec) (*fakeRegistry, *registryBodyValidator) {
	t.Helper()
	registry := &fakeRegistry{
		identity: identity,
		index:    domainplugin.RegistryIndex{Schema: 1, Name: "Fixture"},
		plugins:  make(map[string]domainplugin.RegistryPlugin),
		bodies:   make(map[string][]byte),
	}
	validator := &registryBodyValidator{packages: make(map[string]appplugin.Package)}
	for _, spec := range specs {
		plugin := domainplugin.RegistryPlugin{
			ID: spec.id, Name: spec.name, Description: spec.description,
		}
		registry.index.Plugins = append(registry.index.Plugins, domainplugin.RegistryPluginSummary{
			ID: spec.id, Name: spec.name, Description: spec.description,
			Keywords: append([]string(nil), spec.keywords...),
		})
		for _, version := range spec.versions {
			module := version.module
			if module == nil {
				module = fixtureModule
			}
			manifest := domainplugin.Manifest{
				ManifestVersion: 1, ID: spec.id, Name: spec.name, Version: version.version,
				PluginAPI: "^0.1", ABI: 1, DefaultLocale: "en",
				Wasm: domainplugin.Wasm{
					Path: "plugin.wasm", SHA256: fixtureModuleHash(), MemoryMaxMiB: 64,
				},
				Permissions: domainplugin.Permissions{
					Required: version.required, Optional: version.optional,
					Scopes: version.scopes,
				},
				Dependencies: version.dependencies,
				Contributions: domainplugin.Contributions{
					Commands: []domainplugin.Command{{ID: "run", Title: "Run"}},
				},
			}
			if version.omitModule {
				manifest.Wasm.SHA256 = ""
			}
			files := map[string][]byte{"plugin.toml": []byte("manifest")}
			if !version.omitModule {
				files["plugin.wasm"] = module
			}
			body := []byte("fixture-package:" + spec.id + ":" + version.version)
			key := spec.id + "@" + version.version
			registry.bodies[key] = body
			plugin.Versions = append(plugin.Versions, domainplugin.RegistryVersion{
				Version: version.version, Channel: version.channel,
				PluginAPI: "^0.1", ABI: 1,
				Package: "packages/" + spec.id + "/" + version.version + ".zip",
				SHA256:  sha256Hex(body), Size: int64(len(body)),
				Required:     append([]string(nil), version.required...),
				Optional:     append([]string(nil), version.optional...),
				Dependencies: append([]domainplugin.Dependency(nil), version.dependencies...),
				Yanked:       version.yanked,
				Deprecated:   version.deprecated,
				Revoked:      version.revoked,
			})
			validator.packages[spec.id+"-"+version.version] = appplugin.Package{
				Manifest: manifest, Files: files, SHA256: sha256Hex(body),
			}
		}
		registry.plugins[spec.id] = plugin
	}
	return registry, validator
}

func registryManager(t *testing.T, registry *fakeRegistry,
	validator *registryBodyValidator, manifest domainplugin.Manifest,
	audit appplugin.AuditSink) (*appplugin.Manager, *memoryStore) {
	t.Helper()
	store := newMemoryStore(t)
	manager := appplugin.NewManager(
		fake.New(), fixedManifests{manifest: manifest}, &registrar{}, validator, store,
		appplugin.WithRegistry(registry),
		appplugin.WithAudit(audit),
		appplugin.WithManagerClock(func() time.Time {
			return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
		}),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager, store
}

func enableAll(t *testing.T, manager *appplugin.Manager, id string) {
	t.Helper()
	record, ok, err := managerRecord(manager, id)
	if err != nil || !ok {
		t.Fatalf("record %s: ok %v, err %v", id, ok, err)
	}
	decisions := make([]domainplugin.PermissionDecision, 0, len(record.Required)+len(record.Optional))
	for _, request := range record.Required {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: request.Permission, Scope: request.Scope, Allow: true,
		})
	}
	for _, request := range record.Optional {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: request.Permission, Scope: request.Scope, Allow: true,
		})
	}
	if err := manager.Enable(context.Background(), id, decisions); err != nil {
		t.Fatalf("Enable %s: %v", id, err)
	}
}

func managerRecord(manager *appplugin.Manager, id string) (domainplugin.InstallRecord, bool, error) {
	return managerStoreRecord(manager, id)
}

// managerStoreRecord asks the manager's read model rather than peeking into
// the test store, so the assertions cover production paths.
func managerStoreRecord(manager *appplugin.Manager, id string) (domainplugin.InstallRecord, bool, error) {
	list, err := manager.List(context.Background())
	if err != nil {
		return domainplugin.InstallRecord{}, false, err
	}
	for _, plugin := range list {
		if plugin.ID != id || plugin.Development {
			continue
		}
		record := domainplugin.InstallRecord{
			ID: plugin.ID, Name: plugin.Name, Version: plugin.Version,
			Source: plugin.Source, Enabled: plugin.Enabled, Reason: plugin.Reason,
			PackageSHA256: plugin.PackageSHA256,
		}
		for _, status := range plugin.Permissions {
			request := domainplugin.PermissionRequest{
				Permission: status.Permission, Scope: status.Scope,
			}
			if status.Required {
				record.Required = append(record.Required, request)
			} else {
				record.Optional = append(record.Optional, request)
			}
			if status.Granted {
				record.Grants = append(record.Grants, domainplugin.PermissionGrant{
					Permission: status.Permission, Scope: status.Scope,
					Optional: !status.Required,
				})
			}
		}
		return record, true, nil
	}
	return domainplugin.InstallRecord{}, false, nil
}

func baseRegistryManifest(id string, required []string,
	scopes map[string]domainplugin.Scope) domainplugin.Manifest {
	return managerManifest(id, required, nil, scopes)
}

func TestRegistrySearchDefaultsToStableAndBetaIsOptIn(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Markdown notebook",
			keywords: []string{"writing"},
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.2.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.3.0-beta.1", channel: domainplugin.ChannelBeta, required: []string{"commands"}},
			},
		},
		registryPluginSpec{
			id: "acme.data", name: "Data Viewer", description: "Tabular data",
			versions: []registryVersionSpec{
				{version: "2.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
			},
		},
	)
	manager, _ := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), &recordingAudit{})
	ctx := context.Background()

	results, err := manager.SearchRegistry(ctx, "")
	if err != nil {
		t.Fatalf("SearchRegistry: %v", err)
	}
	if len(results) != 2 || results[0].ID != "acme.data" || results[1].ID != "acme.notes" {
		t.Fatalf("search results = %+v", results)
	}
	if results[1].LatestVersion != "1.2.0" || results[1].Channel != domainplugin.ChannelStable {
		t.Fatalf("stable selection = %+v, want 1.2.0 on stable", results[1])
	}
	results, err = manager.SearchRegistry(ctx, "WRITING")
	if err != nil || len(results) != 1 || results[0].ID != "acme.notes" {
		t.Fatalf("keyword search = %+v, %v", results, err)
	}
	if err := manager.SetChannel(ctx, "acme.notes", domainplugin.ChannelBeta); err != nil {
		t.Fatalf("SetChannel: %v", err)
	}
	results, err = manager.SearchRegistry(ctx, "notes")
	if err != nil || len(results) != 1 || results[0].LatestVersion != "1.3.0-beta.1" {
		t.Fatalf("beta search = %+v, %v", results, err)
	}
	preferences, err := manager.RegistryPreferences(ctx)
	if err != nil || preferences.AutomaticUpdates {
		t.Fatalf("preferences = %+v, %v; automatic updates must default off", preferences, err)
	}
}

func TestRegistryInstallVerifiesSizeAndHashAndPinsTrustOnFirstUse(t *testing.T) {
	registry, validator := newFakeRegistry(t, "https://plugins.example",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{{
				version: "1.0.0", channel: domainplugin.ChannelStable,
				required: []string{"commands"},
			}},
		},
	)
	manager, _ := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), &recordingAudit{})
	ctx := context.Background()

	record, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil)
	if err != nil {
		t.Fatalf("InstallFromRegistry: %v", err)
	}
	if record.Source != domainplugin.SourceRegistry || record.Version != "1.0.0" ||
		record.Enabled || record.TrustedSource != "https://plugins.example" {
		t.Fatalf("record = %+v", record)
	}
	metadata := registry.plugins["acme.notes"].Versions[0]
	if record.PackageSHA256 != metadata.SHA256 {
		t.Fatalf("record hash = %s, want %s", record.PackageSHA256, metadata.SHA256)
	}
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); !errors.Is(err,
		appplugin.ErrAlreadyInstalled) {
		t.Fatalf("second install = %v, want ErrAlreadyInstalled", err)
	}
	if err := manager.Uninstall(ctx, "acme.notes", false); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	// A body whose bytes do not match the declared hash is rejected before
	// the validator or store sees it. The tampered body keeps the declared
	// size so the hash check, not the size check, is what rejects it.
	tampered := []byte("fixture-package:acme.notes:1.0.0")
	tampered[len(tampered)-1] = '1'
	registry.bodies["acme.notes@1.0.0"] = tampered
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err == nil ||
		!strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered package error = %v", err)
	}
	// Restore the body, then declare the wrong size.
	registry.bodies["acme.notes@1.0.0"] = []byte("fixture-package:acme.notes:1.0.0")
	plugin := registry.plugins["acme.notes"]
	plugin.Versions[0].Size++
	registry.plugins["acme.notes"] = plugin
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err == nil ||
		!strings.Contains(err.Error(), "bytes") {
		t.Fatalf("size mismatch error = %v", err)
	}
}

func TestLocalInstallRecordsTrustAndRejectsASourceChangeWithoutConfirmation(t *testing.T) {
	manifest := managerManifest("acme.local", []string{"commands"}, nil, nil)
	store := newMemoryStore(t)
	manager := appplugin.NewManager(fake.New(), fixedManifests{manifest: manifest},
		&registrar{}, &fixedValidator{pkg: appplugin.Package{
			Manifest: manifest,
			Files:    map[string][]byte{"plugin.toml": []byte("manifest"), "plugin.wasm": fixtureModule},
			SHA256:   "package-hash",
		}}, store)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	ctx := context.Background()
	if _, err := manager.InstallFile(ctx, "first.natplug"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := manager.Uninstall(ctx, "acme.local", false); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	_, err := manager.InstallFile(ctx, "second.natplug")
	var changed *appplugin.SourceChangedError
	if !errors.As(err, &changed) || changed.ID != "acme.local" {
		t.Fatalf("source change error = %v, want SourceChangedError", err)
	}
	if _, err := manager.InstallFileConfirm(ctx, "second.natplug", appplugin.ConfirmedSourceChanges{"acme.local": true}); err != nil {
		t.Fatalf("confirmed install: %v", err)
	}
}

func TestRegistryInstallRejectsATamperedRegistryOnReinstall(t *testing.T) {
	registry, validator := newFakeRegistry(t, "registry-one",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{{
				version: "1.0.0", channel: domainplugin.ChannelStable,
				required: []string{"commands"},
			}},
		},
	)
	manager, _ := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), &recordingAudit{})
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := manager.Uninstall(ctx, "acme.notes", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	registry.identity = "registry-two"
	_, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil)
	var changed *appplugin.SourceChangedError
	if !errors.As(err, &changed) || changed.PinnedSource != "registry-one" ||
		changed.NewSource != "registry-two" {
		t.Fatalf("source change error = %v", err)
	}
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", appplugin.ConfirmedSourceChanges{"acme.notes": true}); err != nil {
		t.Fatalf("confirmed source change: %v", err)
	}
}

func TestUpdateShowsPermissionDiffAndNewRequiredPermissionStaysInactive(t *testing.T) {
	scopes := map[string]domainplugin.Scope{
		"network.dial": {Host: "example.com", Ports: []int{443}, Protocol: "tls", TLS: "verify"},
	}
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable,
					required: []string{"commands", "network.dial"}, scopes: scopes},
			},
		},
	)
	audit := &recordingAudit{}
	manager, _ := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), audit)
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	enableAll(t, manager, "acme.notes")

	plan, err := manager.PlanUpdate(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if plan.From != "1.0.0" || plan.To != "1.1.0" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Diff.AddedRequired) != 1 ||
		plan.Diff.AddedRequired[0].Permission != domainplugin.PermissionNetworkDial {
		t.Fatalf("permission diff = %+v", plan.Diff)
	}
	if len(plan.RequiresConsent) != 1 {
		t.Fatalf("requiring consent = %+v", plan.RequiresConsent)
	}

	updated, err := manager.ApplyUpdate(ctx, plan, nil)
	if err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if updated.Version != "1.1.0" || updated.Enabled {
		t.Fatalf("updated record = %+v, want 1.1.0 inactive", updated)
	}
	if !strings.Contains(updated.Reason, "network.dial") {
		t.Fatalf("reason = %q, want the ungranted required permission", updated.Reason)
	}
	list, _ := manager.List(ctx)
	if list[0].Status != appplugin.StatusPending {
		t.Fatalf("status = %s, want pending", list[0].Status)
	}
	if !audit.has(domainplugin.AuditUpdate) {
		t.Fatal("update was not audited")
	}
	if !audit.has(domainplugin.AuditDeny) {
		t.Fatal("a denied required permission on update was not audited")
	}
	if _, err := manager.PlanUpdate(ctx, "acme.notes"); !errors.Is(err,
		appplugin.ErrNoUpdateAvailable) {
		t.Fatalf("second PlanUpdate = %v, want ErrNoUpdateAvailable", err)
	}
	if _, err := manager.ApplyUpdate(ctx, plan, nil); !errors.Is(err,
		appplugin.ErrStaleUpdatePlan) {
		t.Fatalf("stale ApplyUpdate = %v, want ErrStaleUpdatePlan", err)
	}

	// Grant the new required permission on the next attempted update.
	record, _, _ := managerRecord(manager, "acme.notes")
	if err := manager.Enable(ctx, "acme.notes", []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionCommands, Allow: true},
		{Permission: domainplugin.PermissionNetworkDial, Allow: true, Scope: scopes["network.dial"]},
	}); err != nil {
		t.Fatalf("Enable after update: %v", err)
	}
	if record.Enabled {
		t.Fatal("the inactive updated record reported enabled")
	}
	list, _ = manager.List(ctx)
	if !list[0].Enabled || list[0].Status != appplugin.StatusActive {
		t.Fatalf("plugin after consent = %+v", list[0])
	}
}

func TestUpdateRevokesRemovedPermissionsAutomatically(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable,
					required: []string{"commands"}, optional: []string{"clipboard.read"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
			},
		},
	)
	audit := &recordingAudit{}
	manager, _ := registryManager(t, registry, validator,
		managerManifest("acme.notes", []string{"commands"}, []string{"clipboard.read"}, nil), audit)
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	enableAll(t, manager, "acme.notes")
	if record, _, _ := managerRecord(manager, "acme.notes"); len(record.Grants) != 2 {
		t.Fatalf("grants before update = %+v", record.Grants)
	}

	plan, err := manager.PlanUpdate(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	updated, err := manager.ApplyUpdate(ctx, plan, nil)
	if err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if !updated.Enabled {
		t.Fatal("an update with no new required permission disabled the plugin")
	}
	if len(updated.Grants) != 1 || updated.Grants[0].Permission != domainplugin.PermissionCommands {
		t.Fatalf("grants after update = %+v, want only commands", updated.Grants)
	}
	if !audit.has(domainplugin.AuditRevoke) {
		t.Fatal("removed permission revocation was not audited")
	}
}

func TestUpdateTreatsAnOptionalPermissionBecomingRequiredAsFreshConsent(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable,
					required: []string{"commands"}, optional: []string{"clipboard.read"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable,
					required: []string{"commands", "clipboard.read"}},
			},
		},
	)
	manager, _ := registryManager(t, registry, validator,
		managerManifest("acme.notes", []string{"commands"}, []string{"clipboard.read"}, nil),
		&recordingAudit{})
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	enableAll(t, manager, "acme.notes")

	plan, err := manager.PlanUpdate(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if len(plan.RequiresConsent) != 1 ||
		plan.RequiresConsent[0].Permission != domainplugin.PermissionClipboardRead {
		t.Fatalf("requiring consent = %+v", plan.RequiresConsent)
	}
	updated, err := manager.ApplyUpdate(ctx, plan, nil)
	if err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if updated.Enabled {
		t.Fatal("an optional permission that became required kept the plugin active")
	}
	if !strings.Contains(updated.Reason, string(domainplugin.PermissionClipboardRead)) {
		t.Fatalf("reason = %q, want the ungranted required permission", updated.Reason)
	}
	if _, ok := updated.Grant(domainplugin.PermissionClipboardRead); ok {
		t.Fatalf("grants = %+v, want the old optional grant revoked", updated.Grants)
	}
	if _, ok := updated.Grant(domainplugin.PermissionCommands); !ok {
		t.Fatalf("grants = %+v, want the unchanged required grant kept", updated.Grants)
	}
}

func TestUpdateRevokesARequiredPermissionThatBecomesOptional(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable,
					required: []string{"commands", "clipboard.write"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable,
					required: []string{"commands"}, optional: []string{"clipboard.write"}},
			},
		},
	)
	manager, _ := registryManager(t, registry, validator,
		managerManifest("acme.notes", []string{"commands", "clipboard.write"}, nil, nil),
		&recordingAudit{})
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	enableAll(t, manager, "acme.notes")

	plan, err := manager.PlanUpdate(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if len(plan.RequiresConsent) != 0 {
		t.Fatalf("requiring consent = %+v, want no new required permission", plan.RequiresConsent)
	}
	updated, err := manager.ApplyUpdate(ctx, plan, nil)
	if err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if !updated.Enabled {
		t.Fatal("an update with no new required permission disabled the plugin")
	}
	if _, ok := updated.Grant(domainplugin.PermissionClipboardWrite); ok {
		t.Fatalf("grants = %+v, want the old required grant revoked", updated.Grants)
	}
	if _, ok := updated.Grant(domainplugin.PermissionCommands); !ok {
		t.Fatalf("grants = %+v, want the unchanged required grant kept", updated.Grants)
	}
}

func TestRollbackRestoresTheRetainedVersionAndIsReversible(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
			},
		},
	)
	manager, _ := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), &recordingAudit{})
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	enableAll(t, manager, "acme.notes")
	plan, err := manager.PlanUpdate(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if _, err := manager.ApplyUpdate(ctx, plan, nil); err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	restored, err := manager.Rollback(ctx, "acme.notes", nil)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if restored.Version != "1.0.0" || !restored.Enabled {
		t.Fatalf("restored = %+v", restored)
	}
	if restored.Previous == nil || restored.Previous.Version != "1.1.0" {
		t.Fatalf("after rollback previous = %+v", restored.Previous)
	}
	again, err := manager.Rollback(ctx, "acme.notes", nil)
	if err != nil {
		t.Fatalf("second Rollback: %v", err)
	}
	if again.Version != "1.1.0" || !again.Enabled {
		t.Fatalf("second rollback = %+v", again)
	}
}

func TestUpdateActivationFailureRestoresThePreviousVersion(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable,
					required: []string{"commands"}, omitModule: true},
			},
		},
	)
	audit := &recordingAudit{}
	manager, store := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), audit)
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	enableAll(t, manager, "acme.notes")
	plan, err := manager.PlanUpdate(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if _, err := manager.ApplyUpdate(ctx, plan, nil); err == nil {
		t.Fatal("ApplyUpdate succeeded for a package without a module")
	}
	stored, ok, err := store.Record(ctx, "acme.notes")
	if err != nil || !ok {
		t.Fatalf("Record: ok %v, err %v", ok, err)
	}
	if stored.Version != "1.0.0" {
		t.Fatalf("record after failed activation = %s, want the restored 1.0.0", stored.Version)
	}
	if !stored.Enabled {
		t.Fatal("the restored previous version is not enabled")
	}
	if !audit.has(domainplugin.AuditRollback) {
		t.Fatal("automatic restore was not audited as a rollback")
	}
}

func TestDependenciesResolveHighestSatisfyingVersionAndBlockUninstall(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.core", name: "Core", description: "Core library",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.5.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "2.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
			},
		},
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{{
				version: "1.0.0", channel: domainplugin.ChannelStable,
				required:     []string{"commands"},
				dependencies: []domainplugin.Dependency{{ID: "acme.core", Version: "^1.0.0"}},
			}},
		},
	)
	manager, store := registryManager(t, registry, validator,
		managerManifest("acme.notes", []string{"commands"}, nil, nil), &recordingAudit{})
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("InstallFromRegistry: %v", err)
	}
	core, ok, err := store.Record(ctx, "acme.core")
	if err != nil || !ok {
		t.Fatalf("core record: ok %v, err %v", ok, err)
	}
	if core.Version != "1.5.0" || core.Enabled {
		t.Fatalf("resolved dependency = %+v, want 1.5.0 installed disabled", core)
	}
	enableAll(t, manager, "acme.notes")
	err = manager.Uninstall(ctx, "acme.core", false)
	var dependents *appplugin.DependentsError
	if !errors.As(err, &dependents) || len(dependents.Dependents) != 1 ||
		dependents.Dependents[0] != "acme.notes" {
		t.Fatalf("uninstall blocked error = %v", err)
	}
	if err := manager.Disable(ctx, "acme.notes"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if err := manager.Uninstall(ctx, "acme.core", false); err != nil {
		t.Fatalf("uninstall after disabling the dependent: %v", err)
	}
}

func TestDependencyCyclesAreRejected(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.a", name: "A", description: "A",
			versions: []registryVersionSpec{{
				version: "1.0.0", channel: domainplugin.ChannelStable,
				required:     []string{"commands"},
				dependencies: []domainplugin.Dependency{{ID: "acme.b", Version: "^1.0.0"}},
			}},
		},
		registryPluginSpec{
			id: "acme.b", name: "B", description: "B",
			versions: []registryVersionSpec{{
				version: "1.0.0", channel: domainplugin.ChannelStable,
				required:     []string{"commands"},
				dependencies: []domainplugin.Dependency{{ID: "acme.a", Version: "^1.0.0"}},
			}},
		},
	)
	manager, _ := registryManager(t, registry, validator,
		managerManifest("acme.a", []string{"commands"}, nil, nil), &recordingAudit{})
	_, err := manager.InstallFromRegistry(context.Background(), "acme.a", "1.0.0", nil)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle install error = %v", err)
	}
}

func TestYankedVersionsAreHiddenButKeepRunningAndWarningsSurface(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable,
					required: []string{"commands"}, yanked: true},
				{version: "1.2.0", channel: domainplugin.ChannelStable,
					required: []string{"commands"}, deprecated: "use 2.0"},
				{version: "1.3.0", channel: domainplugin.ChannelStable,
					required: []string{"commands"}, deprecated: "use 2.0",
					revoked: "security review"},
			},
		},
	)
	manager, _ := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), &recordingAudit{})
	ctx := context.Background()
	results, err := manager.SearchRegistry(ctx, "notes")
	if err != nil || len(results) != 1 {
		t.Fatalf("search = %+v, %v", results, err)
	}
	if results[0].LatestVersion != "1.3.0" {
		t.Fatalf("latest = %s, want the non-yanked 1.3.0", results[0].LatestVersion)
	}
	kinds := map[domainplugin.ReleaseWarningKind]domainplugin.ReleaseWarning{}
	for _, warning := range results[0].Warnings {
		kinds[warning.Kind] = warning
	}
	if _, ok := kinds[domainplugin.ReleaseWarningDeprecated]; !ok {
		t.Fatalf("deprecated warning missing: %+v", results[0].Warnings)
	}
	revoked, ok := kinds[domainplugin.ReleaseWarningRevoked]
	if !ok || !revoked.Strong {
		t.Fatalf("revoked warning is not strong: %+v", results[0].Warnings)
	}
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.1.0", nil); err == nil {
		t.Fatal("a yanked version was installed explicitly")
	} else {
		var yanked *appplugin.YankedVersionError
		if !errors.As(err, &yanked) {
			t.Fatalf("yanked install error = %v", err)
		}
	}
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install non-yanked: %v", err)
	}
	enableAll(t, manager, "acme.notes")
	// Mark the running version yanked. It stays installed and active; it is
	// only hidden from new selection.
	plugin := registry.plugins["acme.notes"]
	plugin.Versions[0].Yanked = true
	registry.plugins["acme.notes"] = plugin
	list, err := manager.List(ctx)
	if err != nil || !list[0].Enabled || list[0].Status != appplugin.StatusActive {
		t.Fatalf("running yanked plugin = %+v, %v", list, err)
	}
}

func TestAutomaticUpdatesAreOffByDefaultAndSkipConsentUpdates(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.2.0", channel: domainplugin.ChannelStable,
					required: []string{"commands", "network.dial"},
					scopes: map[string]domainplugin.Scope{
						"network.dial": {Host: "example.com", Ports: []int{443},
							Protocol: "tls", TLS: "verify"},
					}},
			},
		},
	)
	manager, _ := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), &recordingAudit{})
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	enableAll(t, manager, "acme.notes")
	if _, err := manager.AutoUpdate(ctx); !errors.Is(err,
		appplugin.ErrAutomaticUpdatesDisabled) {
		t.Fatalf("AutoUpdate with defaults = %v, want ErrAutomaticUpdatesDisabled", err)
	}
	if err := manager.SetAutomaticUpdates(ctx, true); err != nil {
		t.Fatalf("SetAutomaticUpdates: %v", err)
	}
	// 1.2.0 requires a new permission, so automatic update must skip it.
	result, err := manager.AutoUpdate(ctx)
	if err != nil {
		t.Fatalf("AutoUpdate: %v", err)
	}
	if len(result.Updated) != 0 || len(result.Skipped) != 1 {
		t.Fatalf("automatic update result = %+v, want the consent update skipped", result)
	}
	stored, ok, _ := managerRecord(manager, "acme.notes")
	if !ok || stored.Version != "1.0.0" {
		t.Fatalf("version after skipped auto-update = %+v", stored)
	}
	// Remove the consent update so the next pass applies the safe one.
	plugin := registry.plugins["acme.notes"]
	plugin.Versions = plugin.Versions[:2]
	registry.plugins["acme.notes"] = plugin
	result, err = manager.AutoUpdate(ctx)
	if err != nil {
		t.Fatalf("AutoUpdate without consent change: %v", err)
	}
	if len(result.Updated) != 1 || result.Updated[0] != "acme.notes" {
		t.Fatalf("automatic update result = %+v", result)
	}
	stored, _, _ = managerRecord(manager, "acme.notes")
	if stored.Version != "1.1.0" || !stored.Enabled {
		t.Fatalf("after automatic update = %+v", stored)
	}
}

func TestPlanUpdateRequiresSourceChangeConfirmation(t *testing.T) {
	registry, validator := newFakeRegistry(t, "registry-one",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
			},
		},
	)
	manager, _ := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), &recordingAudit{})
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	registry.identity = "registry-two"
	plan, err := manager.PlanUpdate(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if !plan.SourceChanged || plan.PinnedSource != "registry-one" ||
		plan.RegistrySource != "registry-two" {
		t.Fatalf("source change plan = %+v", plan)
	}
	_, err = manager.ApplyUpdate(ctx, plan, nil)
	var changed *appplugin.SourceChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("unconfirmed update = %v, want SourceChangedError", err)
	}
	if len(plan.SourceChanges) != 1 || plan.SourceChanges[0].ID != "acme.notes" {
		t.Fatalf("source change rows = %+v", plan.SourceChanges)
	}
	plan.ConfirmedSourceChanges = appplugin.ConfirmedSourceChanges{"acme.notes": true}
	updated, err := manager.ApplyUpdate(ctx, plan, nil)
	if err != nil {
		t.Fatalf("confirmed source update: %v", err)
	}
	if updated.Version != "1.1.0" || updated.TrustedSource != "registry-two" {
		t.Fatalf("updated record = %+v", updated)
	}
}

func TestRollbackHonoursConsentForNewlyRequiredPermissions(t *testing.T) {
	scopes := map[string]domainplugin.Scope{
		"network.dial": {Host: "example.com", Ports: []int{443}, Protocol: "tls", TLS: "verify"},
	}
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable,
					required: []string{"commands", "network.dial"}, scopes: scopes},
				{version: "1.1.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
			},
		},
	)
	audit := &recordingAudit{}
	manager, _ := registryManager(t, registry, validator,
		managerManifest("acme.notes", []string{"commands", "network.dial"}, nil, scopes),
		audit)
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	enableAll(t, manager, "acme.notes")
	plan, err := manager.PlanUpdate(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	updated, err := manager.ApplyUpdate(ctx, plan, nil)
	if err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if updated.Version != "1.1.0" || !updated.Enabled {
		t.Fatalf("updated = %+v", updated)
	}
	// The rollback target requires network.dial, which the update revoked.
	restored, err := manager.Rollback(ctx, "acme.notes", nil)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if restored.Version != "1.0.0" || restored.Enabled {
		t.Fatalf("rollback without consent = %+v, want 1.0.0 inactive", restored)
	}
	if !strings.Contains(restored.Reason, "network.dial") {
		t.Fatalf("rollback reason = %q", restored.Reason)
	}
	if !audit.has(domainplugin.AuditDeny) {
		t.Fatal("a denied required permission on rollback was not audited")
	}
	// Consent to the restored permission and enable it.
	if err := manager.Enable(ctx, "acme.notes", []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionCommands, Allow: true},
		{Permission: domainplugin.PermissionNetworkDial, Allow: true, Scope: scopes["network.dial"]},
	}); err != nil {
		t.Fatalf("Enable after rollback: %v", err)
	}
	list, _ := manager.List(ctx)
	if !list[0].Enabled || list[0].Status != appplugin.StatusActive {
		t.Fatalf("plugin after rollback consent = %+v", list[0])
	}
}

// TestDependencySourceChangeNeedsItsOwnConfirmation is the regression for a
// root confirmation silently authorizing a dependency: a pin that survived
// uninstall must be named and confirmed separately before the update retries.
func TestDependencySourceChangeNeedsItsOwnConfirmation(t *testing.T) {
	registry, validator := newFakeRegistry(t, "registry-one",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable,
					required:     []string{"commands"},
					dependencies: []domainplugin.Dependency{{ID: "acme.core", Version: "^1.0.0"}}},
			},
		},
		registryPluginSpec{
			id: "acme.core", name: "Core", description: "Core library",
			versions: []registryVersionSpec{{
				version: "1.0.0", channel: domainplugin.ChannelStable,
				required: []string{"commands"},
			}},
		},
	)
	manager, store := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), &recordingAudit{})
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install notes: %v", err)
	}
	// Install core from a local file, then uninstall it. The trust pin is kept
	// on purpose: a later registry install of the same id is a source change.
	localPath := filepath.Join(t.TempDir(), "acme.core-1.0.0.natplug")
	if _, err := manager.InstallFile(ctx, localPath); err != nil {
		t.Fatalf("local install core: %v", err)
	}
	if err := manager.Uninstall(ctx, "acme.core", false); err != nil {
		t.Fatalf("uninstall core: %v", err)
	}

	plan, err := manager.PlanUpdate(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if plan.To != "1.1.0" {
		t.Fatalf("plan target = %s, want 1.1.0", plan.To)
	}
	var dependencyChange *appplugin.SourceChange
	for i := range plan.SourceChanges {
		if plan.SourceChanges[i].ID == "acme.core" {
			dependencyChange = &plan.SourceChanges[i]
		}
	}
	if dependencyChange == nil {
		t.Fatalf("plan source changes = %+v, want acme.core", plan.SourceChanges)
	}
	if dependencyChange.PinnedSource != localPath ||
		dependencyChange.NewSource != "registry-one" {
		t.Fatalf("dependency source change = %+v", *dependencyChange)
	}

	_, err = manager.ApplyUpdate(ctx, plan, nil)
	var changed *appplugin.SourceChangedError
	if !errors.As(err, &changed) || changed.ID != "acme.core" {
		t.Fatalf("unconfirmed dependency update = %v, want SourceChangedError for acme.core", err)
	}
	if _, installed, err := store.Record(ctx, "acme.core"); err != nil || installed {
		t.Fatalf("unconfirmed dependency record = installed %v, err %v", installed, err)
	}
	if stored, ok, _ := store.Record(ctx, "acme.notes"); !ok || stored.Version != "1.0.0" {
		t.Fatalf("notes was mutated by a refused update: %+v", stored)
	}

	plan.ConfirmedSourceChanges = appplugin.ConfirmedSourceChanges{"acme.core": true}
	updated, err := manager.ApplyUpdate(ctx, plan, nil)
	if err != nil {
		t.Fatalf("confirmed dependency source update: %v", err)
	}
	if updated.Version != "1.1.0" {
		t.Fatalf("updated notes = %+v", updated)
	}
	core, ok, err := store.Record(ctx, "acme.core")
	if err != nil || !ok || core.Version != "1.0.0" ||
		core.TrustedSource != "registry-one" {
		t.Fatalf("dependency after confirmed update = %+v, ok %v, err %v", core, ok, err)
	}
}

// TestUpdateRefusesAVersionThatBreaksAnEnabledDependent is the regression for
// an update leaving an enabled dependent with an unsatisfied range.
func TestUpdateRefusesAVersionThatBreaksAnEnabledDependent(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.core", name: "Core", description: "Core library",
			versions: []registryVersionSpec{
				{version: "1.5.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "2.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
			},
		},
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{{
				version: "1.0.0", channel: domainplugin.ChannelStable,
				required:     []string{"commands"},
				dependencies: []domainplugin.Dependency{{ID: "acme.core", Version: "^1.0.0"}},
			}},
		},
	)
	manager, store := registryManager(t, registry, validator,
		managerManifest("acme.notes", []string{"commands"}, nil, nil), &recordingAudit{})
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install notes: %v", err)
	}
	// Only the dependent is enabled; the dependency it constrains stays
	// installed but disabled.
	enableAll(t, manager, "acme.notes")

	plan, err := manager.PlanUpdate(ctx, "acme.core")
	if err != nil {
		t.Fatalf("PlanUpdate core: %v", err)
	}
	if plan.To != "2.0.0" {
		t.Fatalf("core plan target = %s, want 2.0.0", plan.To)
	}
	_, err = manager.ApplyUpdate(ctx, plan, nil)
	var blocked *appplugin.DependentsConstraintError
	if !errors.As(err, &blocked) {
		t.Fatalf("update core = %v, want DependentsConstraintError", err)
	}
	if blocked.ID != "acme.core" || blocked.Version != "2.0.0" {
		t.Fatalf("blocked update = %+v", blocked)
	}
	if len(blocked.Dependents) != 1 || blocked.Dependents[0].ID != "acme.notes" ||
		blocked.Dependents[0].Range != "^1.0.0" {
		t.Fatalf("blocked dependents = %+v", blocked.Dependents)
	}
	core, ok, err := store.Record(ctx, "acme.core")
	if err != nil || !ok || core.Version != "1.5.0" {
		t.Fatalf("core after refused update = %+v, ok %v, err %v", core, ok, err)
	}

	if err := manager.Disable(ctx, "acme.notes"); err != nil {
		t.Fatalf("Disable notes: %v", err)
	}
	updated, err := manager.ApplyUpdate(ctx, plan, nil)
	if err != nil {
		t.Fatalf("update core after disabling the dependent: %v", err)
	}
	if updated.Version != "2.0.0" {
		t.Fatalf("core after allowed update = %+v", updated)
	}
}

// TestRollbackRejectsInvalidDecisionsBeforeUnloading is the regression for a
// rollback that moved the durable record and unloaded the plugin before
// discovering an invalid consent decision.
func TestRollbackRejectsInvalidDecisionsBeforeUnloading(t *testing.T) {
	registry, validator := newFakeRegistry(t, "fixture-registry",
		registryPluginSpec{
			id: "acme.notes", name: "Acme Notes", description: "Notebook",
			versions: []registryVersionSpec{
				{version: "1.0.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
				{version: "1.1.0", channel: domainplugin.ChannelStable, required: []string{"commands"}},
			},
		},
	)
	manager, store := registryManager(t, registry, validator,
		baseRegistryManifest("acme.notes", []string{"commands"}, nil), &recordingAudit{})
	ctx := context.Background()
	if _, err := manager.InstallFromRegistry(ctx, "acme.notes", "1.0.0", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	enableAll(t, manager, "acme.notes")
	plan, err := manager.PlanUpdate(ctx, "acme.notes")
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if _, err := manager.ApplyUpdate(ctx, plan, nil); err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	list, err := manager.List(ctx)
	if err != nil || !list[0].Enabled || list[0].Status != appplugin.StatusActive {
		t.Fatalf("plugin before rollback = %+v, %v", list, err)
	}

	_, err = manager.Rollback(ctx, "acme.notes", []domainplugin.PermissionDecision{{
		Permission: domainplugin.PermissionNetworkDial, Allow: true,
	}})
	var invalid *appplugin.InvalidRequestError
	if !errors.As(err, &invalid) {
		t.Fatalf("Rollback with an invalid decision = %v, want InvalidRequestError", err)
	}
	stored, ok, err := store.Record(ctx, "acme.notes")
	if err != nil || !ok {
		t.Fatalf("record after refused rollback: ok %v, err %v", ok, err)
	}
	if stored.Version != "1.1.0" || !stored.Enabled {
		t.Fatalf("record after refused rollback = %+v, want the unchanged 1.1.0 active", stored)
	}
	list, err = manager.List(ctx)
	if err != nil || !list[0].Enabled || list[0].Status != appplugin.StatusActive {
		t.Fatalf("activation after refused rollback = %+v, %v", list, err)
	}

	// The rejected rollback left the retained version and the flow intact.
	restored, err := manager.Rollback(ctx, "acme.notes", nil)
	if err != nil {
		t.Fatalf("Rollback after the refused attempt: %v", err)
	}
	if restored.Version != "1.0.0" || !restored.Enabled {
		t.Fatalf("restored = %+v", restored)
	}
}
