package app

import (
	"archive/zip"
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

	adapterplugininstall "github.com/mslxl/natcatty/adapters/plugininstall"
	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	adapterpluginpackage "github.com/mslxl/natcatty/adapters/pluginpackage"
	adapterwasm "github.com/mslxl/natcatty/adapters/wasm"
	"github.com/mslxl/natcatty/application/command"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/fake"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/internal/testutil"
	"github.com/mslxl/natcatty/ui/settings"
)

// consentAudit records manager audit events in memory for the consent test.
type consentAudit struct {
	mu     sync.Mutex
	events []domainplugin.AuditEvent
}

func (a *consentAudit) Record(event domainplugin.AuditEvent) {
	a.mu.Lock()
	a.events = append(a.events, event)
	a.mu.Unlock()
}

func (a *consentAudit) Close() error { return nil }

func (a *consentAudit) count(kind domainplugin.AuditEventType) int {
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

func (a *consentAudit) hasPermission(kind domainplugin.AuditEventType,
	permission domainplugin.Permission, scope domainplugin.Scope) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, event := range a.events {
		if event.Event == kind && event.Permission == permission &&
			event.Scope != nil && event.Scope.Equal(scope) {
			return true
		}
	}
	return false
}

// consentArchive packages the walking-skeleton module with a manifest whose
// optional files.read permission is first-use only.
func consentArchive(t *testing.T) string {
	t.Helper()
	module, err := os.ReadFile(filepath.Join(testutil.ModuleRoot(t),
		"adapters", "wasm", "testdata", "devplugin", "plugin.wasm"))
	if err != nil {
		t.Fatalf("read fixture module: %v", err)
	}
	sum := sha256.Sum256(module)
	manifest := fmt.Sprintf(`manifest_version = 1
id = "acme.consent"
name = "Acme Consent"
version = "1.0.0"
plugin_api = "^0.1"
abi = 1
default_locale = "en"

[wasm]
path = "plugin.wasm"
sha256 = "%s"
memory_max_mib = 64

[permissions]
required = ["commands"]
optional = ["files.read"]

[permissions.scopes."files.read"]
path = "/tmp/consent-notes"
`, hex.EncodeToString(sum[:]))

	path := filepath.Join(t.TempDir(), "acme.consent.natplug")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	fixed := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"i18n/en.toml", []byte("plugin_name = \"Consent\"\n")},
		{"plugin.toml", []byte(manifest)},
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
	return path
}

// TestFirstUseOptionalConsentThroughTheProductionService composes the real
// appPluginService and settings.PluginsState over a real application Manager,
// real archive validator, real install store, and real audit sink. The first
// use of the optional files.read permission is denied, then granted with
// remember-for-scope, revoked, and re-authorized from the remembered scope; a
// different scope still prompts and development keys are rejected.
func TestFirstUseOptionalConsentThroughTheProductionService(t *testing.T) {
	ctx := context.Background()
	audit := &consentAudit{}
	store := adapterplugininstall.Open(t.TempDir())
	manager := appplugin.NewManager(
		fake.New(),
		adapterpluginmanifest.Source{},
		newPluginSurface(command.NewRegistry()),
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithAudit(audit),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	record, err := manager.InstallFile(ctx, consentArchive(t))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	key := string(record.Source) + "/" + record.ID

	// The production adapter and the production Settings state, not a fake.
	service := newAppPluginService(manager, func() string { return "en" })
	state := settings.NewPluginsState(service)
	state.Select(key)
	if selected, ok := state.Selected(); !ok || selected.Key != key {
		t.Fatalf("selected plugin = %+v, %v", selected, ok)
	}

	// Enable with the required permission only. The optional files.read row
	// stays ungranted so the first-use prompt is reachable.
	state.BeginEnable()
	if _, ok := state.Review(); !ok {
		t.Fatal("the consent review did not open through the production service")
	}
	state.SetDecision(domainplugin.PermissionCommands, true)
	state.SetDecision(domainplugin.PermissionFilesRead, false)
	state.ConfirmEnable()
	if state.Problem() != settings.PluginProblemNone {
		t.Fatalf("enable problem = %v: %s", state.Problem(), state.ProblemDetail())
	}
	selected, ok := state.Selected()
	if !ok || !selected.Enabled || selected.Status != appplugin.StatusActive {
		t.Fatalf("enabled plugin = %+v", selected)
	}
	for _, status := range selected.Permissions {
		if status.Permission == domainplugin.PermissionFilesRead && status.Granted {
			t.Fatal("the optional permission was granted at enable time")
		}
	}

	declared := domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/consent-notes"}

	// Deny the first use. The production service routes the answer to the
	// manager, which audits the denial.
	denials := audit.count(domainplugin.AuditDeny)
	state.RequestOptional(domainplugin.PermissionFilesRead, false)
	if state.Problem() != settings.PluginProblemAction {
		t.Fatalf("denial problem = %v, want the action problem", state.Problem())
	}
	if got := audit.count(domainplugin.AuditDeny); got != denials+1 {
		t.Fatalf("deny audit events = %d, want %d", got, denials+1)
	}

	// Grant with remember-for-scope and prove the manager persisted both the
	// grant and its remembered scope.
	state.SetRememberOptional(domainplugin.PermissionFilesRead, true)
	state.RequestOptional(domainplugin.PermissionFilesRead, true)
	if state.Problem() != settings.PluginProblemNone {
		t.Fatalf("remembered grant problem = %v: %s", state.Problem(), state.ProblemDetail())
	}
	stored, ok, err := store.Record(ctx, record.ID)
	if err != nil || !ok {
		t.Fatalf("record after grant: ok=%v err=%v", ok, err)
	}
	grant, granted := stored.Grant(domainplugin.PermissionFilesRead)
	if !granted || !grant.Optional || !grant.Scope.Equal(declared) {
		t.Fatalf("persisted grant = %+v, granted=%v, want the declared scope", grant, granted)
	}
	if !stored.WasRemembered(domainplugin.PermissionFilesRead, declared) {
		t.Fatal("the manager did not persist the remembered scope")
	}
	if err := manager.Authority().Check(
		appplugin.SubjectForInstalled(record.Source, record.ID),
		domainplugin.PermissionFilesRead, declared); err != nil {
		t.Fatalf("authority check after UI grant: %v", err)
	}
	selected, _ = state.Selected()
	if !permissionGranted(selected, domainplugin.PermissionFilesRead) {
		t.Fatalf("the refreshed Settings row did not show the grant: %+v", selected.Permissions)
	}

	// Revoke through the production surface, leaving the remembered choice.
	state.Revoke(domainplugin.PermissionFilesRead)
	stored, _, _ = store.Record(ctx, record.ID)
	if _, granted := stored.Grant(domainplugin.PermissionFilesRead); granted {
		t.Fatal("revoke left the optional grant in the record")
	}
	// A first-use request for the remembered declared scope is authorized
	// without a prompt, even though the caller answered "do not allow".
	state.RequestOptional(domainplugin.PermissionFilesRead, false)
	if state.Problem() != settings.PluginProblemNone {
		t.Fatalf("remembered scope prompted again: %v: %s", state.Problem(), state.ProblemDetail())
	}
	stored, _, _ = store.Record(ctx, record.ID)
	if grant, granted := stored.Grant(domainplugin.PermissionFilesRead); !granted || !grant.Scope.Equal(declared) {
		t.Fatalf("remembered re-grant = %+v, granted=%v", grant, granted)
	}
	if !audit.hasPermission(domainplugin.AuditGrant,
		domainplugin.PermissionFilesRead, declared) {
		t.Fatal("the remembered re-grant was not audited with the declared scope")
	}

	// The remembered grant applies only to its declared scope. A different
	// scope must still prompt and be denied.
	other := domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/other-notes"}
	err = service.RequestOptional(key, domainplugin.PermissionFilesRead, other, false, false)
	var denied *appplugin.OptionalDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("different scope = %v, want OptionalDeniedError", err)
	}
	if !audit.hasPermission(domainplugin.AuditDeny,
		domainplugin.PermissionFilesRead, other) {
		t.Fatal("the different-scope denial was not audited with that scope")
	}

	// Development directories are not install records; the production adapter
	// rejects the key before it can reach the manager's optional-consent path.
	if err := service.RequestOptional("development/acme.dev",
		domainplugin.PermissionFilesRead, declared, true, true); err == nil ||
		!strings.Contains(err.Error(), "development directory") {
		t.Fatalf("development key = %v, want a development-directory rejection", err)
	}
	if _, err := service.Review("development/acme.dev"); err == nil ||
		!strings.Contains(err.Error(), "development directory") {
		t.Fatalf("development review = %v, want a development-directory rejection", err)
	}
}

// TestActivationRejectsATamperedInstalledModule installs a valid package
// through the real validator and store, replaces the installed module on disk,
// and proves activation fails closed with the typed integrity error.
func TestActivationRejectsATamperedInstalledModule(t *testing.T) {
	ctx := context.Background()
	audit := &consentAudit{}
	store := adapterplugininstall.Open(t.TempDir())
	manager := appplugin.NewManager(
		fake.New(),
		adapterpluginmanifest.Source{},
		newPluginSurface(command.NewRegistry()),
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithAudit(audit),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	record, err := manager.InstallFile(ctx, consentArchive(t))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	dir, err := store.PackageDir(ctx, record.ID)
	if err != nil {
		t.Fatalf("PackageDir: %v", err)
	}
	manifest, err := (adapterpluginmanifest.Source{}).Load(dir)
	if err != nil {
		t.Fatalf("load installed manifest: %v", err)
	}
	modulePath := filepath.Join(dir, filepath.FromSlash(manifest.Wasm.Path))
	if err := os.Chmod(modulePath, 0o644); err != nil {
		t.Fatalf("make the installed module writable: %v", err)
	}
	if err := os.WriteFile(modulePath, []byte("tampered module"), 0o644); err != nil {
		t.Fatalf("tamper with the installed module: %v", err)
	}

	err = manager.Enable(ctx, record.ID, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionCommands, Allow: true},
	})
	var integrity *appplugin.ModuleIntegrityError
	if !errors.As(err, &integrity) {
		t.Fatalf("Enable after tampering = %v, want ModuleIntegrityError", err)
	}
	if integrity.Path != manifest.Wasm.Path {
		t.Fatalf("integrity path = %q, want %q", integrity.Path, manifest.Wasm.Path)
	}

	list, listErr := manager.List(ctx)
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(list) != 1 || list[0].Status != appplugin.StatusFailed ||
		!strings.Contains(list[0].Reason, "sha256") {
		t.Fatalf("tampered plugin = %+v, want FAILED with a sha256 reason", list)
	}
	audit.mu.Lock()
	failedActivations := 0
	for _, event := range audit.events {
		if event.Event == domainplugin.AuditActivation && event.State == domainplugin.StateFailed {
			failedActivations++
		}
	}
	audit.mu.Unlock()
	if failedActivations != 1 {
		t.Fatalf("FAILED activation audit events = %d, want 1", failedActivations)
	}
}

func permissionGranted(plugin appplugin.InstalledPlugin,
	permission domainplugin.Permission) bool {
	for _, status := range plugin.Permissions {
		if status.Permission == permission {
			return status.Granted && !status.Pending
		}
	}
	return false
}
