package plugin_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type fixedManifests struct {
	manifest domainplugin.Manifest
	err      error
}

func (s fixedManifests) Load(string) (domainplugin.Manifest, error) {
	if s.err != nil {
		return domainplugin.Manifest{}, s.err
	}
	return s.manifest, nil
}

type fixedValidator struct {
	pkg  appplugin.Package
	err  error
	path string
}

func (v *fixedValidator) Validate(_ context.Context, path string) (appplugin.Package, error) {
	v.path = path
	if v.err != nil {
		return appplugin.Package{}, v.err
	}
	return v.pkg, nil
}

type recordingAudit struct {
	mu      sync.Mutex
	events  []domainplugin.AuditEvent
	closed  bool
	dropped uint64
}

func (a *recordingAudit) Dropped() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dropped
}

func (a *recordingAudit) Record(event domainplugin.AuditEvent) {
	a.mu.Lock()
	a.events = append(a.events, event)
	a.mu.Unlock()
}

func (a *recordingAudit) Close() error {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	return nil
}

func (a *recordingAudit) has(kind domainplugin.AuditEventType) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, event := range a.events {
		if event.Event == kind {
			return true
		}
	}
	return false
}

func (a *recordingAudit) count(kind domainplugin.AuditEventType) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	total := 0
	for _, event := range a.events {
		if event.Event == kind {
			total++
		}
	}
	return total
}

func (a *recordingAudit) last(kind domainplugin.AuditEventType) (domainplugin.AuditEvent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.events) - 1; i >= 0; i-- {
		if a.events[i].Event == kind {
			return a.events[i], true
		}
	}
	return domainplugin.AuditEvent{}, false
}

// fixtureModule is the module bytes the manager fixture writes to disk. The
// manifest hash matches because activation re-verifies installed modules.
var fixtureModule = []byte("module")

func fixtureModuleHash() string {
	sum := sha256.Sum256(fixtureModule)
	return hex.EncodeToString(sum[:])
}

func managerManifest(id string, required, optional []string,
	scopes map[string]domainplugin.Scope) domainplugin.Manifest {
	return domainplugin.Manifest{
		ManifestVersion: 1,
		ID:              id,
		Name:            "Acme Manager",
		Version:         "1.0.0",
		PluginAPI:       "^0.1",
		ABI:             1,
		DefaultLocale:   "en",
		Wasm: domainplugin.Wasm{
			Path: "plugin.wasm", SHA256: fixtureModuleHash(), MemoryMaxMiB: 64,
		},
		Permissions: domainplugin.Permissions{
			Required: required, Optional: optional, Scopes: scopes,
		},
		Contributions: domainplugin.Contributions{
			Commands: []domainplugin.Command{{ID: "run", Title: "Run"}},
		},
	}
}

// memoryStore is a test PackageStore. It keeps install records in memory but
// materializes package files and data directories on disk, because activation
// reads the module from the package directory.
type memoryStore struct {
	root    string
	records map[string]domainplugin.InstallRecord
	order   []string
}

func newMemoryStore(t *testing.T) *memoryStore {
	t.Helper()
	return &memoryStore{root: t.TempDir(), records: make(map[string]domainplugin.InstallRecord)}
}

func (s *memoryStore) Install(_ context.Context, pkg appplugin.Package,
	options appplugin.InstallOptions, now time.Time) (domainplugin.InstallRecord, error) {
	if _, exists := s.records[pkg.Manifest.ID]; exists {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s",
			appplugin.ErrAlreadyInstalled, pkg.Manifest.ID)
	}
	required, optional, err := pkg.Manifest.Permissions.Requests()
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if err := s.writeFiles(pkg); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if err := os.MkdirAll(s.DataDir(pkg.Manifest.ID), 0o755); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	record := domainplugin.InstallRecord{
		ID: pkg.Manifest.ID, Name: pkg.Manifest.Name, Version: pkg.Manifest.Version,
		Source: options.Source, TrustedSource: options.TrustedSource,
		PackageSHA256: pkg.SHA256, InstalledAt: now.UTC(),
		Required: required, Optional: optional,
		Dependencies: append([]domainplugin.Dependency(nil), pkg.Manifest.Dependencies...),
	}
	s.records[record.ID] = record
	s.order = append(s.order, record.ID)
	return record, nil
}

func (s *memoryStore) writeFiles(pkg appplugin.Package) error {
	dir := filepath.Join(s.root, "plugins", pkg.Manifest.ID, pkg.Manifest.Version)
	for name, data := range pkg.Files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (s *memoryStore) PromoteVersion(_ context.Context, pkg appplugin.Package,
	record domainplugin.InstallRecord, now time.Time) (domainplugin.InstallRecord, error) {
	existing, ok := s.records[pkg.Manifest.ID]
	if !ok {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s",
			appplugin.ErrNotInstalled, pkg.Manifest.ID)
	}
	required, optional, err := pkg.Manifest.Permissions.Requests()
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if err := s.writeFiles(pkg); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	record.ID = pkg.Manifest.ID
	record.Version = pkg.Manifest.Version
	record.Name = pkg.Manifest.Name
	record.PackageSHA256 = pkg.SHA256
	record.InstalledAt = now.UTC()
	record.Required = required
	record.Optional = optional
	record.Dependencies = append([]domainplugin.Dependency(nil), pkg.Manifest.Dependencies...)
	record.Previous = &domainplugin.PreviousInstall{
		Name: existing.Name, Version: existing.Version,
		PackageSHA256: existing.PackageSHA256,
		Required:      existing.Required, Optional: existing.Optional,
		Dependencies: existing.Dependencies,
		Grants:       existing.Grants, Remembered: existing.Remembered,
	}
	s.records[record.ID] = record
	return record, nil
}

func (s *memoryStore) RestorePrevious(_ context.Context, id string,
	now time.Time) (domainplugin.InstallRecord, error) {
	record, ok := s.records[id]
	if !ok {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s", appplugin.ErrNotInstalled, id)
	}
	previous := record.Previous
	if previous == nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s", appplugin.ErrNoPreviousVersion, id)
	}
	restored := record
	restored.Name = previous.Name
	restored.Version = previous.Version
	restored.PackageSHA256 = previous.PackageSHA256
	restored.InstalledAt = now.UTC()
	restored.Required = previous.Required
	restored.Optional = previous.Optional
	restored.Dependencies = previous.Dependencies
	restored.Grants = previous.Grants
	restored.Remembered = previous.Remembered
	restored.Previous = &domainplugin.PreviousInstall{
		Name: record.Name, Version: record.Version,
		PackageSHA256: record.PackageSHA256,
		Required:      record.Required, Optional: record.Optional,
		Dependencies: record.Dependencies,
		Grants:       record.Grants, Remembered: record.Remembered,
	}
	s.records[id] = restored
	return restored, nil
}

func (s *memoryStore) Record(_ context.Context, id string) (domainplugin.InstallRecord, bool, error) {
	record, ok := s.records[id]
	return record, ok, nil
}

func (s *memoryStore) Records(context.Context) ([]domainplugin.InstallRecord, error) {
	out := make([]domainplugin.InstallRecord, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.records[id])
	}
	return out, nil
}

func (s *memoryStore) SaveRecord(_ context.Context, record domainplugin.InstallRecord) error {
	if _, ok := s.records[record.ID]; !ok {
		return fmt.Errorf("%w: %s", appplugin.ErrNotInstalled, record.ID)
	}
	s.records[record.ID] = record
	return nil
}

func (s *memoryStore) Remove(_ context.Context, id string, purge bool) error {
	if _, ok := s.records[id]; !ok {
		return fmt.Errorf("%w: %s", appplugin.ErrNotInstalled, id)
	}
	if err := os.RemoveAll(filepath.Join(s.root, "plugins", id)); err != nil {
		return err
	}
	delete(s.records, id)
	for i, existing := range s.order {
		if existing == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	if purge {
		return os.RemoveAll(s.DataDir(id))
	}
	return nil
}

func (s *memoryStore) PurgeData(_ context.Context, id string) error {
	if _, ok := s.records[id]; !ok {
		return fmt.Errorf("%w: %s", appplugin.ErrNotInstalled, id)
	}
	return os.RemoveAll(s.DataDir(id))
}

func (s *memoryStore) PackageDir(_ context.Context, id string) (string, error) {
	record, ok := s.records[id]
	if !ok {
		return "", fmt.Errorf("%w: %s", appplugin.ErrNotInstalled, id)
	}
	return filepath.Join(s.root, "plugins", id, record.Version), nil
}

func (s *memoryStore) DataDir(id string) string {
	return filepath.Join(s.root, "plugin-data", id)
}

func managerFixture(t *testing.T, manifest domainplugin.Manifest,
	extraFiles map[string][]byte, runtime appplugin.Runtime,
	audit appplugin.AuditSink) (*appplugin.Manager, *memoryStore, *fixedValidator, *registrar) {
	t.Helper()
	files := map[string][]byte{
		"plugin.toml": []byte("manifest"),
		"plugin.wasm": fixtureModule,
	}
	for name, data := range extraFiles {
		files[name] = data
	}
	validator := &fixedValidator{pkg: appplugin.Package{
		Manifest: manifest, Files: files, SHA256: "package-hash",
	}}
	if runtime == nil {
		runtime = fake.New()
	}
	registrations := &registrar{}
	store := newMemoryStore(t)
	manager := appplugin.NewManager(runtime, fixedManifests{manifest: manifest},
		registrations, validator, store,
		appplugin.WithAudit(audit),
		appplugin.WithManagerClock(func() time.Time {
			return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
		}))
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager, store, validator, registrations
}

func installForManager(t *testing.T, manager *appplugin.Manager,
	validator *fixedValidator) domainplugin.InstallRecord {
	t.Helper()
	record, err := manager.InstallFile(context.Background(), "plugin.natplug")
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	if validator.path != "plugin.natplug" {
		t.Fatalf("validator path = %q", validator.path)
	}
	return record
}

func TestInstallAndEnableAreSeparateAndTheConsentReviewNamesTheSource(t *testing.T) {
	manifest := managerManifest("acme.manager",
		[]string{"commands", "network.dial"}, []string{"clipboard.write"},
		map[string]domainplugin.Scope{
			"network.dial": {Host: "example.com", Ports: []int{443}, Protocol: "tls", TLS: "verify"},
		})
	audit := &recordingAudit{}
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	record := installForManager(t, manager, validator)
	if record.Enabled {
		t.Fatal("install enabled the plugin")
	}
	review, err := manager.Review(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if review.Source != domainplugin.SourceLocalFile || len(review.Required) != 2 ||
		len(review.Optional) != 1 {
		t.Fatalf("review = %+v", review)
	}
	if review.Required[1].Scope.Host != "example.com" {
		t.Fatalf("consent scope = %+v", review.Required[1].Scope)
	}
	if !audit.has(domainplugin.AuditInstall) {
		t.Fatal("install was not audited")
	}
}

func TestDenyingARequiredPermissionLeavesThePluginInactiveWithAReason(t *testing.T) {
	manifest := managerManifest("acme.manager",
		[]string{"commands", "network.dial"}, nil,
		map[string]domainplugin.Scope{
			"network.dial": {Host: "example.com", Ports: []int{443}, Protocol: "tls", TLS: "verify"},
		})
	audit := &recordingAudit{}
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	record := installForManager(t, manager, validator)

	err := manager.Enable(context.Background(), record.ID, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionCommands, Allow: true},
		{Permission: domainplugin.PermissionNetworkDial, Allow: false},
	})
	var denied *appplugin.PermissionDeniedError
	if !errors.As(err, &denied) || denied.Permission != domainplugin.PermissionNetworkDial {
		t.Fatalf("Enable = %v, want PermissionDeniedError for network.dial", err)
	}
	list, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List = %+v", list)
	}
	if list[0].Enabled || list[0].Status != appplugin.StatusPending {
		t.Fatalf("plugin state = %+v", list[0])
	}
	if want := domainplugin.PermissionReason(domainplugin.ReasonPermissionDenied,
		domainplugin.PermissionNetworkDial); list[0].Reason != want {
		t.Fatalf("reason = %q, want %q", list[0].Reason, want)
	}
	if !audit.has(domainplugin.AuditDeny) {
		t.Fatal("the denial was not audited")
	}
	activation, ok := audit.last(domainplugin.AuditActivation)
	if !ok || activation.State != domainplugin.StatePending {
		t.Fatalf("activation event = %+v", activation)
	}
}

func TestAllowingRequiredAndOptionalPermissionsActivatesAndGrantsScopes(t *testing.T) {
	manifest := managerManifest("acme.manager",
		[]string{"commands", "network.dial"}, []string{"clipboard.write"},
		map[string]domainplugin.Scope{
			"network.dial": {Host: "example.com", Ports: []int{443}, Protocol: "tls", TLS: "verify"},
		})
	audit := &recordingAudit{}
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	record := installForManager(t, manager, validator)

	if err := manager.Enable(context.Background(), record.ID, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionCommands, Allow: true},
		{Permission: domainplugin.PermissionNetworkDial, Allow: true},
		{Permission: domainplugin.PermissionClipboardWrite, Allow: true},
	}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	list, _ := manager.List(context.Background())
	if list[0].Status != appplugin.StatusActive || !list[0].Enabled {
		t.Fatalf("plugin = %+v", list[0])
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeNetwork,
		Host: "example.com", Ports: []int{443}, Protocol: "tls", TLS: "verify"}
	handle, err := manager.Authority().Issue(appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, record.ID), domainplugin.PermissionNetworkDial, scope)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := handle.Check(); err != nil {
		t.Fatalf("handle check: %v", err)
	}
	if !audit.has(domainplugin.AuditEnable) || !audit.has(domainplugin.AuditGrant) {
		t.Fatal("enable/grant were not audited")
	}
	activation, ok := audit.last(domainplugin.AuditActivation)
	if !ok || activation.State != domainplugin.StateActive {
		t.Fatalf("activation event = %+v", activation)
	}
}

func TestRevokingARequiredPermissionUnloadsAndInvalidatesHandles(t *testing.T) {
	manifest := managerManifest("acme.manager",
		[]string{"commands", "network.dial"}, nil,
		map[string]domainplugin.Scope{
			"network.dial": {Host: "example.com", Ports: []int{443}, Protocol: "tls", TLS: "verify"},
		})
	audit := &recordingAudit{}
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	record := installForManager(t, manager, validator)
	if err := manager.Enable(context.Background(), record.ID, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionCommands, Allow: true},
		{Permission: domainplugin.PermissionNetworkDial, Allow: true},
	}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeNetwork,
		Host: "example.com", Ports: []int{443}, Protocol: "tls", TLS: "verify"}
	handle, err := manager.Authority().Issue(appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, record.ID), domainplugin.PermissionNetworkDial, scope)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := manager.Revoke(context.Background(), record.ID,
		domainplugin.PermissionNetworkDial); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	var revoked *appplugin.CapabilityRevokedError
	if err := handle.Check(); !errors.As(err, &revoked) {
		t.Fatalf("handle after revoke = %v, want CapabilityRevokedError", err)
	}
	list, _ := manager.List(context.Background())
	if list[0].Enabled || list[0].Status != appplugin.StatusPending {
		t.Fatalf("required revoke did not unload: %+v", list[0])
	}
	if want := domainplugin.PermissionReason(domainplugin.ReasonPermissionRevoked,
		domainplugin.PermissionNetworkDial); list[0].Reason != want {
		t.Fatalf("reason = %q, want %q", list[0].Reason, want)
	}
	if !audit.has(domainplugin.AuditRevoke) {
		t.Fatal("revoke was not audited")
	}
}

func TestRevokingAnOptionalPermissionLeavesThePluginActive(t *testing.T) {
	manifest := managerManifest("acme.manager", []string{"commands"},
		[]string{"clipboard.write"}, nil)
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, &recordingAudit{})
	record := installForManager(t, manager, validator)
	if err := manager.Enable(context.Background(), record.ID, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionCommands, Allow: true},
	}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := manager.RequestOptional(context.Background(), record.ID,
		domainplugin.PermissionClipboardWrite, domainplugin.Scope{}, true, false); err != nil {
		t.Fatalf("RequestOptional: %v", err)
	}
	handle, err := manager.Authority().Issue(appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, record.ID),
		domainplugin.PermissionClipboardWrite, domainplugin.Scope{})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := manager.Revoke(context.Background(), record.ID,
		domainplugin.PermissionClipboardWrite); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	var revoked *appplugin.CapabilityRevokedError
	if err := handle.Check(); !errors.As(err, &revoked) {
		t.Fatalf("optional handle after revoke = %v, want CapabilityRevokedError", err)
	}
	list, _ := manager.List(context.Background())
	if !list[0].Enabled || list[0].Status != appplugin.StatusActive {
		t.Fatalf("optional revoke changed the plugin state: %+v", list[0])
	}
}

func TestOptionalPermissionIsRememberedForTheSameScope(t *testing.T) {
	manifest := managerManifest("acme.manager", []string{"commands"},
		[]string{"files.read"},
		map[string]domainplugin.Scope{"files.read": {Path: "/tmp/notes"}})
	audit := &recordingAudit{}
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	record := installForManager(t, manager, validator)
	if err := manager.Enable(context.Background(), record.ID, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionCommands, Allow: true},
	}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"}
	if err := manager.RequestOptional(context.Background(), record.ID,
		domainplugin.PermissionFilesRead, scope, false, false); !errors.As(err, new(*appplugin.OptionalDeniedError)) {
		t.Fatalf("denied first use = %v, want OptionalDeniedError", err)
	}
	if err := manager.RequestOptional(context.Background(), record.ID,
		domainplugin.PermissionFilesRead, scope, true, true); err != nil {
		t.Fatalf("allowed first use: %v", err)
	}
	if err := manager.Revoke(context.Background(), record.ID,
		domainplugin.PermissionFilesRead); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// The remembered grant authorizes the same scope again without a prompt,
	// and that re-grant is audited like every other grant.
	grantsBefore := audit.count(domainplugin.AuditGrant)
	if err := manager.RequestOptional(context.Background(), record.ID,
		domainplugin.PermissionFilesRead, scope, false, false); err != nil {
		t.Fatalf("remembered first use = %v", err)
	}
	if err := manager.Authority().Check(appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, record.ID),
		domainplugin.PermissionFilesRead, scope); err != nil {
		t.Fatalf("remembered grant check: %v", err)
	}
	if got := audit.count(domainplugin.AuditGrant); got != grantsBefore+1 {
		t.Fatalf("remembered re-grant audit events = %d, want %d", got, grantsBefore+1)
	}
}

// TestEnableRememberOptionalPersistsTheGrantForTheDeclaredScope covers the
// production consent path: PermissionDecision.Remember is set by the enable
// consent UI, the manager stores it, and a later first-use request for the same
// scope is authorized without another prompt.
func TestEnableRememberOptionalPersistsTheGrantForTheDeclaredScope(t *testing.T) {
	manifest := managerManifest("acme.manager", []string{"commands"},
		[]string{"files.read"},
		map[string]domainplugin.Scope{"files.read": {Path: "/tmp/notes"}})
	audit := &recordingAudit{}
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	record := installForManager(t, manager, validator)
	if err := manager.Enable(context.Background(), record.ID,
		[]domainplugin.PermissionDecision{
			{Permission: domainplugin.PermissionCommands, Allow: true},
			{Permission: domainplugin.PermissionFilesRead, Allow: true, Remember: true},
		}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"}
	if err := manager.Revoke(context.Background(), record.ID,
		domainplugin.PermissionFilesRead); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	grantsBefore := audit.count(domainplugin.AuditGrant)
	if err := manager.RequestOptional(context.Background(), record.ID,
		domainplugin.PermissionFilesRead, scope, false, false); err != nil {
		t.Fatalf("remembered first use: %v", err)
	}
	if err := manager.Authority().Check(
		appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, record.ID),
		domainplugin.PermissionFilesRead, scope); err != nil {
		t.Fatalf("remembered grant check: %v", err)
	}
	if got := audit.count(domainplugin.AuditGrant); got != grantsBefore+1 {
		t.Fatalf("remembered re-grant audit events = %d, want %d", got, grantsBefore+1)
	}
}

func TestUninstallKeepsDataAndPurgeRemovesIt(t *testing.T) {
	manifest := managerManifest("acme.manager", []string{"commands"}, nil, nil)
	audit := &recordingAudit{}
	manager, store, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	record := installForManager(t, manager, validator)
	dataDir := store.DataDir(record.ID)
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), record.ID, false); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "config.json")); err != nil {
		t.Fatalf("uninstall removed data: %v", err)
	}
	if !audit.has(domainplugin.AuditUninstall) {
		t.Fatal("uninstall was not audited")
	}

	// A fresh install's data can be purged on its own, with a separate audit
	// event and without touching the package.
	if _, err := store.Install(context.Background(), validator.pkg,
		appplugin.InstallOptions{Source: domainplugin.SourceLocalFile}, time.Now()); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.PurgeData(context.Background(), record.ID); err != nil {
		t.Fatalf("PurgeData: %v", err)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purge left the data directory: %v", err)
	}
	if _, _, err := manager.Detail(context.Background(), record.ID); err != nil {
		t.Fatalf("Detail after purge: %v", err)
	}
	if !audit.has(domainplugin.AuditPurge) {
		t.Fatal("purge was not audited")
	}
}

func TestActivationFailureIsVisibleAndAudited(t *testing.T) {
	manifest := managerManifest("acme.broken", []string{"commands"}, nil, nil)
	audit := &recordingAudit{}
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	// The package record claims a module, but the package directory has no
	// module file, so activation fails structurally.
	validator.pkg.Files = map[string][]byte{"plugin.toml": []byte("manifest")}
	record := installForManager(t, manager, validator)
	if err := manager.Enable(context.Background(), record.ID,
		[]domainplugin.PermissionDecision{{Permission: domainplugin.PermissionCommands, Allow: true}}); err == nil {
		t.Fatal("Enable succeeded for a package without a module")
	}
	list, _ := manager.List(context.Background())
	if list[0].Status != appplugin.StatusFailed || !strings.Contains(list[0].Reason, "plugin.wasm") {
		t.Fatalf("failed plugin = %+v", list[0])
	}
	activation, ok := audit.last(domainplugin.AuditActivation)
	if !ok || activation.State != domainplugin.StateFailed {
		t.Fatalf("activation event = %+v", activation)
	}
}

type trapRuntime struct {
	inner *fake.Runtime
}

func (r trapRuntime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	if spec.Name == "acme.crash" {
		spec.Name = fake.Trap
	}
	return r.inner.Instantiate(ctx, spec, sink)
}

func (r trapRuntime) Close(ctx context.Context) error { return r.inner.Close(ctx) }

func TestATrapIsAuditedAsACrash(t *testing.T) {
	manifest := managerManifest("acme.crash", []string{"commands"}, nil, nil)
	audit := &recordingAudit{}
	manager, _, validator, registrations := managerFixture(t, manifest, nil,
		trapRuntime{inner: fake.New()}, audit)
	record := installForManager(t, manager, validator)
	if err := manager.Enable(context.Background(), record.ID,
		[]domainplugin.PermissionDecision{{Permission: domainplugin.PermissionCommands, Allow: true}}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	handlers := registrations.commands()
	run, ok := handlers["plugin.acme.crash.run"]
	if !ok {
		t.Fatalf("commands = %+v", handlers)
	}
	if err := run(context.Background()); err == nil {
		t.Fatal("the trap fixture succeeded")
	}
	if !audit.has(domainplugin.AuditCrash) {
		t.Fatal("the trap was not audited as a crash")
	}
	activation, ok := audit.last(domainplugin.AuditActivation)
	if !ok || activation.State != domainplugin.StateFailed {
		t.Fatalf("activation event after the trap = %+v, want FAILED", activation)
	}
	if !strings.Contains(activation.Reason, "trapped") {
		t.Fatalf("activation reason = %q, want the trap failure", activation.Reason)
	}
}

func TestManagerReportsDroppedAuditEvents(t *testing.T) {
	manifest := managerManifest("acme.audit", []string{"commands"}, nil, nil)
	audit := &recordingAudit{dropped: 7}
	manager, _, _, _ := managerFixture(t, manifest, nil, nil, audit)
	if got := manager.AuditDropped(); got != 7 {
		t.Fatalf("AuditDropped = %d, want the sink's 7", got)
	}
}

func TestListWithoutAPackageStoreReturnsAnError(t *testing.T) {
	manifest := managerManifest("acme.nostore", []string{"commands"}, nil, nil)
	manager := appplugin.NewManager(fake.New(), fixedManifests{manifest: manifest},
		&registrar{}, nil, nil)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if _, err := manager.List(context.Background()); err == nil {
		t.Fatal("List succeeded without a package store")
	}
}

func TestRestoreReactivatesEnabledPlugins(t *testing.T) {
	manifest := managerManifest("acme.manager", []string{"commands"}, nil, nil)
	audit := &recordingAudit{}
	manager, store, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	record := installForManager(t, manager, validator)
	if err := manager.Enable(context.Background(), record.ID,
		[]domainplugin.PermissionDecision{{Permission: domainplugin.PermissionCommands, Allow: true}}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	restored := appplugin.NewManager(fake.New(), fixedManifests{manifest: manifest},
		&registrar{}, validator, store, appplugin.WithAudit(audit))
	t.Cleanup(func() { _ = restored.Close(context.Background()) })
	if err := restored.Restore(context.Background()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	list, _ := restored.List(context.Background())
	if len(list) != 1 || list[0].Status != appplugin.StatusActive {
		t.Fatalf("restored list = %+v", list)
	}
}

func TestDevelopmentDirectoryIsAPathGrantAndNeverAnInstalledPackage(t *testing.T) {
	manifest := managerManifest("acme.dev", []string{"commands"}, nil, nil)
	audit := &recordingAudit{}
	manager, store, _, _ := managerFixture(t, manifest, nil, nil, audit)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("module"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LoadDevelopment(context.Background(), dir); err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	list, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List = %+v", list)
	}
	if !list[0].Development || list[0].Source != domainplugin.SourceDevelopment {
		t.Fatalf("development entry = %+v", list[0])
	}
	if list[0].Key != "development/acme.dev" {
		t.Fatalf("development key = %q", list[0].Key)
	}
	records, err := store.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("development directory entered the install store: %+v", records)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: filepath.ToSlash(canonical)}
	if err := manager.Authority().Check(appplugin.SubjectForDevelopment(manifest.ID),
		domainplugin.PermissionFilesRead, scope); err != nil {
		t.Fatalf("development path grant check: %v", err)
	}
	if !audit.has(domainplugin.AuditDevelopment) {
		t.Fatal("development load was not audited")
	}
}

// TestAuthorityIsIsolatedBetweenInstalledAndDevelopmentCopies is the
// regression for authority keyed only by plugin id: an installed package and a
// development directory sharing an id must not share grants, and disabling or
// unloading one must not revoke the other's authority.
func TestAuthorityIsIsolatedBetweenInstalledAndDevelopmentCopies(t *testing.T) {
	manifest := managerManifest("acme.shared", []string{"commands"}, nil, nil)
	audit := &recordingAudit{}
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	record := installForManager(t, manager, validator)
	if err := manager.Enable(context.Background(), record.ID,
		[]domainplugin.PermissionDecision{{Permission: domainplugin.PermissionCommands, Allow: true}}); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	installed := appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, record.ID)
	development := appplugin.SubjectForDevelopment(manifest.ID)
	if err := manager.Authority().Check(installed,
		domainplugin.PermissionCommands, domainplugin.Scope{}); err != nil {
		t.Fatalf("installed grant check: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("module"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LoadDevelopment(context.Background(), dir); err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	devScope := domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: filepath.ToSlash(canonical)}
	if err := manager.Authority().Check(development,
		domainplugin.PermissionFilesRead, devScope); err != nil {
		t.Fatalf("development path grant check: %v", err)
	}
	if err := manager.Authority().Check(development,
		domainplugin.PermissionCommands, domainplugin.Scope{}); !errors.Is(err, appplugin.ErrPermissionNotGranted) {
		t.Fatalf("development copy inherited the installed grant: %v", err)
	}
	if err := manager.Authority().Check(installed,
		domainplugin.PermissionFilesRead, devScope); !errors.Is(err, appplugin.ErrPermissionNotGranted) {
		t.Fatalf("installed package inherited the development path grant: %v", err)
	}

	// Unloading the development copy leaves the installed authority intact.
	if err := manager.UnloadDevelopment(context.Background(), manifest.ID); err != nil {
		t.Fatalf("UnloadDevelopment: %v", err)
	}
	if err := manager.Authority().Check(installed,
		domainplugin.PermissionCommands, domainplugin.Scope{}); err != nil {
		t.Fatalf("installed grant after development unload: %v", err)
	}

	// Reload it, then disable the installed package. The development fiber
	// stays ACTIVE and keeps its path grant.
	dev, err := manager.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("reload development: %v", err)
	}
	if err := manager.Disable(context.Background(), record.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if err := manager.Authority().Check(installed,
		domainplugin.PermissionCommands, domainplugin.Scope{}); !errors.Is(err, appplugin.ErrPermissionNotGranted) {
		t.Fatalf("disable did not revoke the installed grant: %v", err)
	}
	if err := manager.Authority().Check(development,
		domainplugin.PermissionFilesRead, devScope); err != nil {
		t.Fatalf("disable revoked the development grant: %v", err)
	}
	if state := dev.Fiber.State(); state != domainplugin.StateActive {
		t.Fatalf("development fiber state = %s, want ACTIVE", state)
	}
	if !audit.has(domainplugin.AuditDisable) {
		t.Fatal("disable was not audited")
	}
}

func TestInvalidDecisionsAreRejectedBeforeThePluginChangesState(t *testing.T) {
	manifest := managerManifest("acme.manager", []string{"commands"}, nil, nil)
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, &recordingAudit{})
	record := installForManager(t, manager, validator)
	err := manager.Enable(context.Background(), record.ID, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionNetworkDial, Allow: true},
	})
	var invalid *appplugin.InvalidRequestError
	if !errors.As(err, &invalid) {
		t.Fatalf("Enable = %v, want InvalidRequestError", err)
	}
	list, _ := manager.List(context.Background())
	if list[0].Enabled {
		t.Fatalf("invalid decisions enabled the plugin: %+v", list[0])
	}
}
