package settings

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/ui/locales"
)

type fakePluginService struct {
	plugins         []appplugin.InstalledPlugin
	reviews         map[string]domainplugin.ConsentReview
	installKey      string
	installed       bool
	installErr      error
	installConfirm  appplugin.ConfirmedSourceChanges
	enableErr       error
	actionErr       error
	registryResults []appplugin.RegistrySearchResult
	registryErr     error
	preferences     domainplugin.RegistryPreferences
	preferencesErr  error

	channelKey string
	channel    domainplugin.Channel
	channelErr error

	autoUpdates bool
	autoErr     error

	registryInstallID      string
	registryInstallVersion string
	registryInstallConfirm appplugin.ConfirmedSourceChanges
	registryInstallKey     string
	registryInstallErr     error

	plan    appplugin.UpdatePlan
	planErr error
	planKey string

	appliedKey       string
	appliedPlan      appplugin.UpdatePlan
	appliedDecisions []domainplugin.PermissionDecision
	applyErr         error

	rollbackKey       string
	rollbackDecisions []domainplugin.PermissionDecision
	rollbackErr       error

	searchQuery string

	enabledKey       string
	enabled          []domainplugin.PermissionDecision
	disabledKey      string
	revoked          domainplugin.Permission
	uninstalledKey   string
	uninstalledPurge bool
	purgedKey        string

	requestedKey        string
	requestedPermission domainplugin.Permission
	requestedScope      domainplugin.Scope
	requestedAllow      bool
	requestedRemember   bool
	requestErr          error

	settingsView    appplugin.PluginSettingsView
	settingsErr     error
	settingsCalls   int
	savedKey        string
	savedSettings   map[string]any
	saveSettingsErr error
	resetKey        string

	diagnostics       appplugin.PluginDiagnostics
	diagnosticsErr    error
	diagnosticsKey    string
	diagnosticsReport appplugin.DiagnosticsReport
	restartKey        string
	restartErr        error
}

func (f *fakePluginService) Plugins() ([]appplugin.InstalledPlugin, error) {
	return append([]appplugin.InstalledPlugin(nil), f.plugins...), nil
}

func (f *fakePluginService) Review(key string) (domainplugin.ConsentReview, error) {
	review, ok := f.reviews[key]
	if !ok {
		return domainplugin.ConsentReview{}, errors.New("no review")
	}
	return review, nil
}

func (f *fakePluginService) InstallFromFile(
	confirmSourceChange appplugin.ConfirmedSourceChanges) (string, bool, error) {
	f.installConfirm = confirmSourceChange
	return f.installKey, f.installed, f.installErr
}

func (f *fakePluginService) SearchRegistry(query string) ([]appplugin.RegistrySearchResult, error) {
	f.searchQuery = query
	return append([]appplugin.RegistrySearchResult(nil), f.registryResults...), f.registryErr
}

func (f *fakePluginService) RegistryPreferences() (domainplugin.RegistryPreferences, error) {
	return f.preferences, f.preferencesErr
}

func (f *fakePluginService) SetChannel(key string, channel domainplugin.Channel) error {
	f.channelKey = key
	f.channel = channel
	if f.channelErr == nil {
		f.preferences.SetChannel(key, channel)
	}
	return f.channelErr
}

func (f *fakePluginService) SetAutomaticUpdates(enabled bool) error {
	f.autoUpdates = enabled
	if f.autoErr == nil {
		f.preferences.AutomaticUpdates = enabled
	}
	return f.autoErr
}

func (f *fakePluginService) InstallFromRegistry(id, version string,
	confirmSourceChange appplugin.ConfirmedSourceChanges) (string, bool, error) {
	f.registryInstallID = id
	f.registryInstallVersion = version
	f.registryInstallConfirm = confirmSourceChange
	if f.registryInstallErr != nil {
		return "", false, f.registryInstallErr
	}
	return f.registryInstallKey, true, nil
}

func (f *fakePluginService) PlanUpdate(key string) (appplugin.UpdatePlan, error) {
	f.planKey = key
	return f.plan, f.planErr
}

func (f *fakePluginService) ApplyUpdate(key string, plan appplugin.UpdatePlan,
	decisions []domainplugin.PermissionDecision) error {
	f.appliedKey = key
	f.appliedPlan = plan
	f.appliedDecisions = append([]domainplugin.PermissionDecision(nil), decisions...)
	return f.applyErr
}

func (f *fakePluginService) Rollback(key string,
	decisions []domainplugin.PermissionDecision) error {
	f.rollbackKey = key
	f.rollbackDecisions = append([]domainplugin.PermissionDecision(nil), decisions...)
	return f.rollbackErr
}

func (f *fakePluginService) Enable(key string, decisions []domainplugin.PermissionDecision) error {
	f.enabledKey = key
	f.enabled = append([]domainplugin.PermissionDecision(nil), decisions...)
	return f.enableErr
}

func (f *fakePluginService) Disable(key string) error {
	f.disabledKey = key
	return f.actionErr
}

func (f *fakePluginService) Revoke(key string, permission domainplugin.Permission) error {
	f.revoked = permission
	return f.actionErr
}

func (f *fakePluginService) RequestOptional(key string, permission domainplugin.Permission,
	scope domainplugin.Scope, allow, remember bool) error {
	f.requestedKey = key
	f.requestedPermission = permission
	f.requestedScope = scope
	f.requestedAllow = allow
	f.requestedRemember = remember
	return f.requestErr
}

func (f *fakePluginService) Uninstall(key string, purge bool) error {
	f.uninstalledKey = key
	f.uninstalledPurge = purge
	return f.actionErr
}

func (f *fakePluginService) PurgeData(key string) error {
	f.purgedKey = key
	return f.actionErr
}

func (f *fakePluginService) Settings(string) (appplugin.PluginSettingsView, error) {
	f.settingsCalls++
	return f.settingsView, f.settingsErr
}

func (f *fakePluginService) SaveSettings(key string, values map[string]any) error {
	f.savedKey = key
	f.savedSettings = make(map[string]any, len(values))
	for k, v := range values {
		f.savedSettings[k] = v
	}
	return f.saveSettingsErr
}

func (f *fakePluginService) ResetSettings(key string) error {
	f.resetKey = key
	return f.actionErr
}

func (f *fakePluginService) Diagnostics(key string) (appplugin.PluginDiagnostics, error) {
	f.diagnosticsKey = key
	return f.diagnostics, f.diagnosticsErr
}

func (f *fakePluginService) DiagnosticsReport() appplugin.DiagnosticsReport {
	return f.diagnosticsReport
}

func (f *fakePluginService) Restart(key string) error {
	f.restartKey = key
	return f.restartErr
}

func pluginFixture() appplugin.InstalledPlugin {
	return appplugin.InstalledPlugin{
		ID:      "acme.notes",
		Key:     "local-file/acme.notes",
		Name:    "Acme Notes",
		Version: "1.0.0",
		Source:  domainplugin.SourceLocalFile,
		Status:  appplugin.StatusDisabled,
		Permissions: []appplugin.PermissionStatus{
			{
				Permission: domainplugin.PermissionCommands, Required: true, Granted: true,
			},
			{
				Permission: domainplugin.PermissionFilesRead, Required: true, Granted: true,
				Scope: domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"},
			},
		},
	}
}

func TestPluginsStateLoadsAndSelectsTheFirstPlugin(t *testing.T) {
	service := &fakePluginService{plugins: []appplugin.InstalledPlugin{pluginFixture()}}
	state := NewPluginsState(service)
	if got := len(state.Plugins()); got != 1 {
		t.Fatalf("plugins = %d, want 1", got)
	}
	selected, ok := state.Selected()
	if !ok || selected.Key != "local-file/acme.notes" {
		t.Fatalf("selected = %+v, %v", selected, ok)
	}
	// A second read is cached; the service is not called again.
	service.plugins = nil
	if got := len(state.Plugins()); got != 1 {
		t.Fatalf("plugins after cache = %d, want 1", got)
	}
}

func TestPluginsStateInstallSelectsTheNewPackage(t *testing.T) {
	service := &fakePluginService{installKey: "local-file/acme.notes", installed: true}
	state := NewPluginsState(service)
	state.Plugins()
	service.plugins = []appplugin.InstalledPlugin{pluginFixture()}
	state.Install()
	selected, ok := state.Selected()
	if !ok || selected.Key != "local-file/acme.notes" {
		t.Fatalf("selected after install = %+v, %v", selected, ok)
	}
	if state.Problem() != PluginProblemNone {
		t.Fatalf("problem = %v", state.Problem())
	}
	if service.uninstalledKey != "" || service.enabledKey != "" {
		t.Fatal("install ran an enable or uninstall step")
	}
}

func TestPluginsStateInstallCancelIsNotAnError(t *testing.T) {
	state := NewPluginsState(&fakePluginService{})
	state.Plugins()
	state.Install()
	if state.Problem() != PluginProblemNone {
		t.Fatalf("cancelled install reported %v", state.Problem())
	}
}

func TestPluginsStateConsentSubmitsExplicitDecisions(t *testing.T) {
	service := &fakePluginService{plugins: []appplugin.InstalledPlugin{pluginFixture()}}
	service.reviews = map[string]domainplugin.ConsentReview{
		"local-file/acme.notes": {
			ID: "acme.notes", Name: "Acme Notes", Version: "1.0.0",
			Source: domainplugin.SourceLocalFile,
			Required: []domainplugin.PermissionConsent{
				{Permission: domainplugin.PermissionCommands, Required: true},
				{
					Permission: domainplugin.PermissionFilesRead, Required: true,
					Scope: domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"},
				},
			},
			Optional: []domainplugin.PermissionConsent{
				{Permission: domainplugin.PermissionClipboardRead},
			},
		},
	}
	state := NewPluginsState(service)
	state.Plugins()
	state.BeginEnable()
	if _, ok := state.Review(); !ok {
		t.Fatal("consent review did not open")
	}
	if state.Decision(domainplugin.PermissionCommands) {
		t.Fatal("a permission was preselected")
	}
	state.SetDecision(domainplugin.PermissionCommands, true)
	state.SetDecision(domainplugin.PermissionFilesRead, true)
	state.ConfirmEnable()
	if _, ok := state.Review(); ok {
		t.Fatal("consent review stayed open after enable")
	}
	if service.enabledKey != "local-file/acme.notes" || len(service.enabled) != 3 {
		t.Fatalf("enable key = %q decisions = %+v", service.enabledKey, service.enabled)
	}
	for _, decision := range service.enabled {
		want := decision.Permission == domainplugin.PermissionCommands ||
			decision.Permission == domainplugin.PermissionFilesRead
		if decision.Allow != want {
			t.Fatalf("decision %+v allow = %v, want %v", decision, decision.Allow, want)
		}
		if decision.Permission == domainplugin.PermissionFilesRead &&
			decision.Scope.Path != "/tmp/notes" {
			t.Fatalf("files.read scope = %+v", decision.Scope)
		}
	}
}

func TestPluginsStateFirstUseOptionalUsesTheDeclaredScopeAndRemember(t *testing.T) {
	plugin := pluginFixture()
	plugin.Enabled = true
	plugin.Status = appplugin.StatusActive
	plugin.Permissions = append(plugin.Permissions, appplugin.PermissionStatus{
		Permission: domainplugin.PermissionFilesWrite,
		Scope:      domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"},
	})
	service := &fakePluginService{plugins: []appplugin.InstalledPlugin{plugin}}
	state := NewPluginsState(service)
	state.Plugins()

	state.SetRememberOptional(domainplugin.PermissionFilesWrite, true)
	state.RequestOptional(domainplugin.PermissionFilesWrite, true)
	if service.requestedKey != "local-file/acme.notes" ||
		service.requestedPermission != domainplugin.PermissionFilesWrite ||
		!service.requestedAllow || !service.requestedRemember {
		t.Fatalf("first-use request = key %q permission %q allow %v remember %v",
			service.requestedKey, service.requestedPermission,
			service.requestedAllow, service.requestedRemember)
	}
	if service.requestedScope.Path != "/tmp/notes" {
		t.Fatalf("first-use scope = %+v, want the declared scope", service.requestedScope)
	}

	state.SetRememberOptional(domainplugin.PermissionFilesWrite, false)
	state.RequestOptional(domainplugin.PermissionFilesWrite, false)
	if service.requestedAllow || service.requestedRemember {
		t.Fatalf("denied first use = allow %v remember %v",
			service.requestedAllow, service.requestedRemember)
	}
}

// TestPluginsStateRememberDoesNotLeakAcrossPlugins is the regression for a
// remember map keyed only by permission: a remember-for-scope choice made for
// one plugin must not authorize another plugin that declares the same
// permission for a different scope.
func TestPluginsStateRememberDoesNotLeakAcrossPlugins(t *testing.T) {
	first := pluginFixture()
	first.Enabled = true
	first.Status = appplugin.StatusActive
	first.Permissions = append(first.Permissions, appplugin.PermissionStatus{
		Permission: domainplugin.PermissionFilesWrite,
		Scope:      domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"},
	})
	second := appplugin.InstalledPlugin{
		ID:      "acme.other",
		Key:     "local-file/acme.other",
		Name:    "Acme Other",
		Version: "1.0.0",
		Source:  domainplugin.SourceLocalFile,
		Status:  appplugin.StatusActive,
		Enabled: true,
		Permissions: []appplugin.PermissionStatus{{
			Permission: domainplugin.PermissionFilesWrite,
			Scope:      domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/other"},
		}},
	}
	service := &fakePluginService{plugins: []appplugin.InstalledPlugin{first, second}}
	state := NewPluginsState(service)
	state.Plugins()

	state.SetRememberOptional(domainplugin.PermissionFilesWrite, true)
	if !state.RememberOptional(domainplugin.PermissionFilesWrite) {
		t.Fatal("remember choice was not recorded for the selected plugin")
	}

	state.Select(second.Key)
	if state.RememberOptional(domainplugin.PermissionFilesWrite) {
		t.Fatal("remember choice leaked to a different plugin after selection changed")
	}
	state.RequestOptional(domainplugin.PermissionFilesWrite, true)
	if service.requestedKey != second.Key {
		t.Fatalf("request key = %q, want %q", service.requestedKey, second.Key)
	}
	if service.requestedRemember {
		t.Fatal("the second plugin received the first plugin's remember choice")
	}
	if service.requestedScope.Path != "/tmp/other" {
		t.Fatalf("request scope = %+v, want the second plugin's declared scope", service.requestedScope)
	}

	// Returning to the first plugin must not resurrect the stale choice.
	state.Select(first.Key)
	if state.RememberOptional(domainplugin.PermissionFilesWrite) {
		t.Fatal("a remember choice survived a selection round trip")
	}
}

func TestPluginsStateConsentRememberSetsTheOptionalDecision(t *testing.T) {
	service := &fakePluginService{plugins: []appplugin.InstalledPlugin{pluginFixture()}}
	service.reviews = map[string]domainplugin.ConsentReview{
		"local-file/acme.notes": {
			ID: "acme.notes", Name: "Acme Notes",
			Source: domainplugin.SourceLocalFile,
			Required: []domainplugin.PermissionConsent{
				{Permission: domainplugin.PermissionCommands, Required: true},
			},
			Optional: []domainplugin.PermissionConsent{
				{
					Permission: domainplugin.PermissionFilesWrite,
					Scope:      domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"},
				},
			},
		},
	}
	state := NewPluginsState(service)
	state.Plugins()
	state.BeginEnable()
	state.SetDecision(domainplugin.PermissionCommands, true)
	state.SetDecision(domainplugin.PermissionFilesWrite, true)
	state.SetRememberOptional(domainplugin.PermissionFilesWrite, true)
	state.ConfirmEnable()

	var optional *domainplugin.PermissionDecision
	for i := range service.enabled {
		if service.enabled[i].Permission == domainplugin.PermissionFilesWrite {
			optional = &service.enabled[i]
		}
	}
	if optional == nil || !optional.Allow || !optional.Remember {
		t.Fatalf("optional enable decision = %+v, want allow and remember", optional)
	}
}

func TestPluginsStateDeniedRequiredPermissionShowsTheReason(t *testing.T) {
	service := &fakePluginService{plugins: []appplugin.InstalledPlugin{pluginFixture()}}
	service.reviews = map[string]domainplugin.ConsentReview{
		"local-file/acme.notes": {
			ID: "acme.notes", Name: "Acme Notes",
			Source: domainplugin.SourceLocalFile,
			Required: []domainplugin.PermissionConsent{
				{Permission: domainplugin.PermissionCommands, Required: true},
			},
		},
	}
	state := NewPluginsState(service)
	state.Plugins()
	state.BeginEnable()
	state.ConfirmEnable()
	if len(service.enabled) != 1 || service.enabled[0].Allow {
		t.Fatalf("denied decision = %+v", service.enabled)
	}
	// The manager leaves the reason on the refreshed record.
	service.plugins[0].Status = appplugin.StatusPending
	service.plugins[0].Reason = "required permission denied: commands"
	state.refresh()
	selected, _ := state.Selected()
	if !strings.Contains(selected.Reason, "required permission denied") {
		t.Fatalf("reason = %q", selected.Reason)
	}
}

func TestPluginsStateUninstallAndPurgeUseSeparateConfirmations(t *testing.T) {
	service := &fakePluginService{plugins: []appplugin.InstalledPlugin{pluginFixture()}}
	state := NewPluginsState(service)
	state.Plugins()

	state.AskUninstall()
	if state.Confirm() != pluginConfirmUninstall {
		t.Fatalf("confirm = %v, want uninstall", state.Confirm())
	}
	state.ConfirmUninstall()
	if service.uninstalledKey != "local-file/acme.notes" || service.uninstalledPurge {
		t.Fatalf("uninstall key = %q purge = %v", service.uninstalledKey, service.uninstalledPurge)
	}
	if service.purgedKey != "" {
		t.Fatal("uninstall purged data without the separate purge confirmation")
	}

	service.plugins = []appplugin.InstalledPlugin{pluginFixture()}
	state.refresh()
	state.AskPurge()
	if state.Confirm() != pluginConfirmPurge {
		t.Fatalf("confirm = %v, want purge", state.Confirm())
	}
	state.ConfirmPurge()
	if service.purgedKey != "local-file/acme.notes" {
		t.Fatalf("purged key = %q", service.purgedKey)
	}
	if service.uninstalledKey != "local-file/acme.notes" || service.uninstalledPurge {
		t.Fatal("purge changed the uninstall outcome")
	}
}

func TestPluginsStateRevokeTargetsTheSelectedGrant(t *testing.T) {
	service := &fakePluginService{plugins: []appplugin.InstalledPlugin{pluginFixture()}}
	state := NewPluginsState(service)
	state.Plugins()
	state.Revoke(domainplugin.PermissionFilesRead)
	if service.revoked != domainplugin.PermissionFilesRead {
		t.Fatalf("revoked = %q", service.revoked)
	}
}

func TestPluginsSettingsPageLoadsSavesAndResets(t *testing.T) {
	service := &fakePluginService{
		plugins: []appplugin.InstalledPlugin{pluginFixture()},
		settingsView: appplugin.PluginSettingsView{
			PluginID: "acme.notes",
			Valid:    true,
			Fields: []appplugin.SettingsFieldView{
				{Key: "mode", Type: domainplugin.SettingTypeString, Value: "fast", Default: "fast",
					Required: true, Options: []string{"fast", "slow"}},
				{Key: "retries", Type: domainplugin.SettingTypeInt, Value: int64(3), Default: int64(3)},
				{Key: "debug", Type: domainplugin.SettingTypeBool, Value: true, Default: true,
					Volatile: true},
			},
		},
	}
	state := NewPluginsState(service)
	state.Select("local-file/acme.notes")
	view, ok := state.SettingsView()
	if !ok || !view.Valid || len(view.Fields) != 3 {
		t.Fatalf("SettingsView = %+v, %v", view, ok)
	}
	if got := state.SettingValue("mode"); got != "fast" {
		t.Fatalf("initial mode = %v", got)
	}

	// Editing one field keeps every other effective value; save submits the
	// complete set so untouched fields are not reset to their defaults.
	state.SetSettingValue("mode", "slow")
	state.SetSettingValue("retries", int64(5))
	state.SaveSettings()
	if service.savedKey != "local-file/acme.notes" {
		t.Fatalf("saved key = %q", service.savedKey)
	}
	if service.savedSettings["mode"] != "slow" ||
		service.savedSettings["retries"] != int64(5) ||
		service.savedSettings["debug"] != true {
		t.Fatalf("saved settings = %+v", service.savedSettings)
	}
	if problem := state.SettingsProblem(); problem != "" {
		t.Fatalf("save problem = %q", problem)
	}

	// A rejected save surfaces the manager's validation message and keeps
	// the edited page in place.
	service.saveSettingsErr = &domainplugin.SettingsValidationError{}
	state.SetSettingValue("mode", "turbo")
	state.SaveSettings()
	if problem := state.SettingsProblem(); problem == "" {
		t.Fatal("rejected save did not surface a problem")
	}
	service.saveSettingsErr = nil

	state.ResetSettings()
	if service.resetKey != "local-file/acme.notes" {
		t.Fatalf("reset key = %q", service.resetKey)
	}
	if problem := state.SettingsProblem(); problem != "" {
		t.Fatalf("reset problem = %q", problem)
	}
}

// TestPluginsSettingsSaveOmitsUnsetOptionalField covers a declared optional
// field with no stored value and no default: the view renders it as nil, and
// submitting nil must not turn into a validation failure.
func TestPluginsSettingsSaveOmitsUnsetOptionalField(t *testing.T) {
	service := &fakePluginService{
		plugins: []appplugin.InstalledPlugin{pluginFixture()},
		settingsView: appplugin.PluginSettingsView{
			PluginID: "acme.notes",
			Valid:    true,
			Fields: []appplugin.SettingsFieldView{
				{Key: "mode", Type: domainplugin.SettingTypeString, Value: "fast", Default: "fast"},
				{Key: "optional_note", Type: domainplugin.SettingTypeString},
			},
		},
	}
	state := NewPluginsState(service)
	state.Select("local-file/acme.notes")
	if _, ok := state.SettingsView(); !ok {
		t.Fatal("SettingsView did not load")
	}
	if value := state.SettingValue("optional_note"); value != nil {
		t.Fatalf("unset optional field = %#v, want nil", value)
	}
	state.SetSettingValue("mode", "slow")
	state.SaveSettings()
	if service.savedSettings["mode"] != "slow" {
		t.Fatalf("saved mode = %#v", service.savedSettings["mode"])
	}
	if _, ok := service.savedSettings["optional_note"]; ok {
		t.Fatalf("nil optional field was submitted: %#v", service.savedSettings)
	}
	if problem := state.SettingsProblem(); problem != "" {
		t.Fatalf("save problem = %q", problem)
	}
}

func TestPluginsSettingsPageReportsALoadFailure(t *testing.T) {
	service := &fakePluginService{
		plugins:     []appplugin.InstalledPlugin{pluginFixture()},
		settingsErr: errors.New("settings unavailable"),
	}
	state := NewPluginsState(service)
	state.Select("local-file/acme.notes")
	if _, ok := state.SettingsView(); ok {
		t.Fatal("SettingsView succeeded after a load failure")
	}
	if problem := state.SettingsProblem(); !strings.Contains(problem, "settings unavailable") {
		t.Fatalf("problem = %q", problem)
	}
}

// TestRefreshLocalizedTextReloadsTheSettingsPage proves a host locale change
// re-reads labels instead of leaving the cached page in the old language.
func TestRefreshLocalizedTextReloadsTheSettingsPage(t *testing.T) {
	service := &fakePluginService{
		plugins: []appplugin.InstalledPlugin{pluginFixture()},
		settingsView: appplugin.PluginSettingsView{
			PluginID: "acme.notes",
			Valid:    true,
			Fields: []appplugin.SettingsFieldView{{
				Key: "mode", Type: domainplugin.SettingTypeString,
				Label: "Mode", Description: "Execution mode", Value: "fast",
			}},
		},
	}
	state := NewPluginsState(service)
	state.Select("local-file/acme.notes")
	view, ok := state.SettingsView()
	if !ok || view.Fields[0].Label != "Mode" {
		t.Fatalf("initial view = %+v, ok = %v", view, ok)
	}
	if service.settingsCalls != 1 {
		t.Fatalf("settings calls = %d, want 1", service.settingsCalls)
	}

	view.Fields[0].Label = "Modo"
	view.Fields[0].Description = "Modo de ejecución"
	service.settingsView = view
	state.RefreshLocalizedText()
	view, ok = state.SettingsView()
	if !ok || view.Fields[0].Label != "Modo" ||
		view.Fields[0].Description != "Modo de ejecución" {
		t.Fatalf("view after locale change = %+v, ok = %v", view, ok)
	}
	if service.settingsCalls != 2 {
		t.Fatalf("settings calls after locale change = %d, want 2", service.settingsCalls)
	}
}

func TestPluginReasonTextLocalizesStableCodes(t *testing.T) {
	m := locales.In("en")
	denied := domainplugin.PermissionReason(
		domainplugin.ReasonPermissionDenied, domainplugin.PermissionNetworkDial)
	if got, want := pluginReasonText(denied, m),
		m.SettingsPluginsReasonPermissionDenied("network.dial"); got != want {
		t.Fatalf("denied reason = %q, want %q", got, want)
	}
	revoked := domainplugin.PermissionReason(
		domainplugin.ReasonPermissionRevoked, domainplugin.PermissionCommands)
	if got, want := pluginReasonText(revoked, m),
		m.SettingsPluginsReasonPermissionRevoked("commands"); got != want {
		t.Fatalf("revoked reason = %q, want %q", got, want)
	}
	if got, want := pluginReasonText("instantiate module: trap", m),
		m.SettingsPluginsReason("instantiate module: trap"); got != want {
		t.Fatalf("operational reason = %q, want %q", got, want)
	}
}

func TestPluginScopeTextRendersStructuredFields(t *testing.T) {
	scope := domainplugin.Scope{
		Kind: domainplugin.ScopeNetwork, Host: "example.com", Ports: []int{443},
		Protocol: "tls", TLS: "verify",
	}
	text := pluginScopeText(scope, locales.In("en"))
	for _, want := range []string{"host=example.com", "ports=443", "protocol=tls", "tls=verify"} {
		if !strings.Contains(text, want) {
			t.Fatalf("scope text %q lacks %q", text, want)
		}
	}
}

func TestPluginsAreaShowsTheFirstUseOptionalPrompt(t *testing.T) {
	plugin := pluginFixture()
	plugin.Enabled = true
	plugin.Status = appplugin.StatusActive
	plugin.Permissions = append(plugin.Permissions, appplugin.PermissionStatus{
		Permission: domainplugin.PermissionFilesWrite,
		Scope:      domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"},
	})
	state := NewPluginsState(&fakePluginService{plugins: []appplugin.InstalledPlugin{plugin}})
	tester := ui.NewTester(viewAt(AreaPlugins, Config{Plugins: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")
	for _, want := range []string{
		m.SettingsPluginsFirstUseBody(),
		m.SettingsPluginsRememberScope(),
		m.SettingsPluginsGrant(),
		m.SettingsPluginsDeny(),
	} {
		if !tester.HasText(want) {
			t.Fatalf("first-use prompt lacks %q; texts %q", want, tester.Texts())
		}
	}
}

func TestPluginsAreaShowsSourceStatusPermissionsAndDevelopmentLabel(t *testing.T) {
	development := appplugin.InstalledPlugin{
		ID:          "dev.sample",
		Key:         "development/dev.sample",
		Name:        "Dev Sample",
		Version:     "0.1.0",
		Source:      domainplugin.SourceDevelopment,
		Development: true,
		Status:      appplugin.StatusDevelopment,
		Enabled:     true,
		Permissions: []appplugin.PermissionStatus{
			{
				Permission: domainplugin.PermissionFilesRead, Required: true, Granted: true,
				Scope: domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/dev"},
			},
		},
	}
	service := &fakePluginService{plugins: []appplugin.InstalledPlugin{pluginFixture(), development}}
	state := NewPluginsState(service)
	tester := ui.NewTester(viewAt(AreaPlugins, Config{Plugins: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")
	for _, want := range []string{
		"Acme Notes",
		m.SettingsPluginsSourceLocalFile(),
		m.SettingsPluginsStatusDisabled(),
		m.SettingsPluginsPermissions(),
		"files.read",
		m.SettingsPluginsGranted(),
		"path=/tmp/notes",
		m.SettingsPluginsScopeSubtree(),
		m.SettingsPluginsUninstall(),
		m.SettingsPluginsPurgeData(),
	} {
		if !tester.HasText(want) {
			t.Fatalf("plugins area lacks %q; texts %q", want, tester.Texts())
		}
	}
	state.Select("development/dev.sample")
	tester.Frame()
	for _, want := range []string{"Dev Sample", m.SettingsPluginsDevelopmentNote()} {
		if !tester.HasText(want) {
			t.Fatalf("development detail lacks %q; texts %q", want, tester.Texts())
		}
	}
	if tester.HasText(m.SettingsPluginsUninstall()) {
		t.Fatal("a development directory offers uninstall")
	}
}

func TestPluginsStateSearchesTheRegistryAndOptsIntoBeta(t *testing.T) {
	service := &fakePluginService{
		registryResults: []appplugin.RegistrySearchResult{{
			ID: "acme.notes", Name: "Acme Notes", LatestVersion: "1.2.0",
			Channel: domainplugin.ChannelStable,
		}},
	}
	state := NewPluginsState(service)
	results := state.RegistryResults()
	if len(results) != 1 || results[0].ID != "acme.notes" {
		t.Fatalf("registry results = %+v", results)
	}
	if state.AutomaticUpdates() {
		t.Fatal("automatic updates defaulted on")
	}
	state.SetRegistryQuery("notes")
	state.SearchRegistry()
	if service.searchQuery != "notes" {
		t.Fatalf("search query = %q, want notes", service.searchQuery)
	}
	state.SetRegistryChannel("acme.notes", domainplugin.ChannelBeta)
	if service.channelKey != "acme.notes" || service.channel != domainplugin.ChannelBeta {
		t.Fatalf("channel request = %q %q", service.channelKey, service.channel)
	}
	if got := state.RegistryChannel("acme.notes"); got != domainplugin.ChannelBeta {
		t.Fatalf("selected channel = %q, want beta", got)
	}
	state.SetAutomaticUpdates(true)
	if !state.AutomaticUpdates() || !service.autoUpdates {
		t.Fatal("automatic updates preference was not recorded")
	}
}

func TestPluginsStateInstallsFromRegistryAndConfirmsASourceChange(t *testing.T) {
	service := &fakePluginService{
		registryResults: []appplugin.RegistrySearchResult{{
			ID: "acme.notes", Name: "Acme Notes", LatestVersion: "1.0.0",
		}},
		registryInstallKey: "registry/acme.notes",
		plugins: []appplugin.InstalledPlugin{{
			ID: "acme.notes", Key: "registry/acme.notes", Name: "Acme Notes",
			Source: domainplugin.SourceRegistry,
		}},
	}
	state := NewPluginsState(service)
	state.Plugins()
	state.InstallRegistry("acme.notes", "1.0.0")
	if service.registryInstallID != "acme.notes" || service.registryInstallVersion != "1.0.0" ||
		len(service.registryInstallConfirm) != 0 {
		t.Fatalf("registry install request = %+v, confirm %v",
			service.registryInstallID, service.registryInstallConfirm)
	}
	if selected, ok := state.Selected(); !ok || selected.Key != "registry/acme.notes" {
		t.Fatalf("selected after registry install = %+v, %v", selected, ok)
	}

	service = &fakePluginService{
		registryInstallErr: &appplugin.SourceChangedError{
			ID: "acme.notes", PinnedSource: "registry-one", NewSource: "registry-two",
		},
	}
	state = NewPluginsState(service)
	state.InstallRegistry("acme.notes", "1.0.0")
	pending, ok := state.SourceChange()
	if !ok || pending.previous != "registry-one" || pending.current != "registry-two" {
		t.Fatalf("pending source change = %+v, %v", pending, ok)
	}
	service.registryInstallErr = nil
	service.registryInstallKey = "registry/acme.notes"
	service.plugins = []appplugin.InstalledPlugin{{
		ID: "acme.notes", Key: "registry/acme.notes", Name: "Acme Notes",
		Source: domainplugin.SourceRegistry,
	}}
	state.ConfirmSourceChange()
	if !service.registryInstallConfirm.Contains("acme.notes") {
		t.Fatal("source change confirmation was not passed to the install")
	}
	if selected, ok := state.Selected(); !ok || selected.Key != "registry/acme.notes" {
		t.Fatalf("selected after confirmed install = %+v, %v", selected, ok)
	}
}

func TestPluginsStateLocalInstallRequiresASecondConfirmationForASourceChange(t *testing.T) {
	service := &fakePluginService{
		installErr: &appplugin.SourceChangedError{
			ID: "acme.notes", PinnedSource: "/old.natplug", NewSource: "/new.natplug",
		},
	}
	state := NewPluginsState(service)
	state.Install()
	pending, ok := state.SourceChange()
	if !ok || !pending.local {
		t.Fatalf("pending local source change = %+v, %v", pending, ok)
	}
	service.installErr = nil
	service.installKey = "local-file/acme.notes"
	service.installed = true
	service.plugins = []appplugin.InstalledPlugin{{
		ID: "acme.notes", Key: "local-file/acme.notes", Name: "Acme Notes",
		Source: domainplugin.SourceLocalFile,
	}}
	state.ConfirmSourceChange()
	if !service.installConfirm.Contains("acme.notes") {
		t.Fatal("the local confirm action did not confirm the changed plugin id")
	}
	if selected, ok := state.Selected(); !ok || selected.Key != "local-file/acme.notes" {
		t.Fatalf("selected after local install = %+v, %v", selected, ok)
	}
}

func TestPluginsStateUpdateReviewPassesConsentDecisions(t *testing.T) {
	plugin := pluginFixture()
	plugin.Source = domainplugin.SourceRegistry
	plugin.Key = "registry/acme.notes"
	plugin.HasPrevious = true
	request := domainplugin.PermissionRequest{
		Permission: domainplugin.PermissionNetworkDial,
		Scope: domainplugin.Scope{Host: "example.com", Ports: []int{443},
			Protocol: "tls", TLS: "verify"},
	}
	service := &fakePluginService{
		plugins: []appplugin.InstalledPlugin{plugin},
		plan: appplugin.UpdatePlan{
			ID: "acme.notes", From: "1.0.0", To: "1.1.0",
			Diff:            domainplugin.PermissionDiff{AddedRequired: []domainplugin.PermissionRequest{request}},
			RequiresConsent: []domainplugin.PermissionRequest{request},
			SourceChanged:   true, PinnedSource: "registry-one", RegistrySource: "registry-two",
			SourceChanges: []appplugin.SourceChange{{
				ID: "acme.notes", PinnedSource: "registry-one", NewSource: "registry-two",
			}},
		},
	}
	state := NewPluginsState(service)
	state.Plugins()
	state.BeginUpdate()
	if service.planKey != "registry/acme.notes" {
		t.Fatalf("plan key = %q", service.planKey)
	}
	if _, ok := state.UpdatePlan(); !ok {
		t.Fatal("the update review did not open")
	}
	state.ConfirmUpdate()
	if len(service.appliedDecisions) != 1 || service.appliedDecisions[0].Allow {
		t.Fatalf("update decisions = %+v, want an explicit deny", service.appliedDecisions)
	}
	if !service.appliedPlan.ConfirmedSourceChanges.Contains("acme.notes") {
		t.Fatal("the confirmed update did not carry the source-change confirmation")
	}
	if _, ok := state.UpdatePlan(); ok {
		t.Fatal("the update review stayed open after applying")
	}

	state.BeginUpdate()
	state.SetUpdateDecision(domainplugin.PermissionNetworkDial, true)
	state.ConfirmUpdate()
	if len(service.appliedDecisions) != 1 || !service.appliedDecisions[0].Allow {
		t.Fatalf("update decisions = %+v, want an allow", service.appliedDecisions)
	}
	if service.appliedDecisions[0].Scope.Host != "example.com" {
		t.Fatalf("update decision scope = %+v", service.appliedDecisions[0].Scope)
	}
}

func TestPluginsStateRollbackRequiresConfirmation(t *testing.T) {
	plugin := pluginFixture()
	plugin.Source = domainplugin.SourceRegistry
	plugin.Key = "registry/acme.notes"
	plugin.HasPrevious = true
	service := &fakePluginService{plugins: []appplugin.InstalledPlugin{plugin}}
	state := NewPluginsState(service)
	state.Plugins()
	state.AskRollback()
	if state.Confirm() != pluginConfirmRollback {
		t.Fatalf("confirm state = %v, want rollback", state.Confirm())
	}
	state.ConfirmRollback()
	if service.rollbackKey != "registry/acme.notes" {
		t.Fatalf("rollback key = %q", service.rollbackKey)
	}
	if state.Confirm() != pluginConfirmNone {
		t.Fatal("the rollback confirmation stayed open")
	}
}

// TestPluginsStateNamesADependencySourceChange is the regression for a pending
// install source change labelled with the root id instead of the id whose pin
// actually differs, and for confirming that root id implicitly.
func TestPluginsStateNamesADependencySourceChange(t *testing.T) {
	service := &fakePluginService{
		registryInstallErr: &appplugin.SourceChangedError{
			ID: "acme.core", PinnedSource: "/old/core.natplug", NewSource: "registry-two",
		},
	}
	state := NewPluginsState(service)
	state.InstallRegistry("acme.notes", "1.1.0")
	pending, ok := state.SourceChange()
	if !ok || pending.changed != "acme.core" {
		t.Fatalf("pending source change = %+v, want acme.core", pending)
	}
	if pending.id != "acme.notes" || pending.version != "1.1.0" {
		t.Fatalf("pending retry target = %q %q", pending.id, pending.version)
	}
	service.registryInstallErr = nil
	service.registryInstallKey = "registry/acme.notes"
	service.plugins = []appplugin.InstalledPlugin{{
		ID: "acme.notes", Key: "registry/acme.notes", Name: "Acme Notes",
		Source: domainplugin.SourceRegistry,
	}}
	state.ConfirmSourceChange()
	if !service.registryInstallConfirm.Contains("acme.core") {
		t.Fatalf("confirmed ids = %+v, want acme.core", service.registryInstallConfirm)
	}
	if service.registryInstallConfirm.Contains("acme.notes") {
		t.Fatalf("confirmed ids = %+v, the root id was not the changed id",
			service.registryInstallConfirm)
	}
	if service.registryInstallID != "acme.notes" || service.registryInstallVersion != "1.1.0" {
		t.Fatalf("retry target = %q %q", service.registryInstallID, service.registryInstallVersion)
	}
}

// TestPluginsStateUpdateConfirmsOnlyShownSourceChanges guards per-plugin
// update confirmations: a dependency row shown in the review is confirmed,
// while unrelated ids are never confirmed implicitly.
func TestPluginsStateUpdateConfirmsOnlyShownSourceChanges(t *testing.T) {
	plugin := pluginFixture()
	plugin.Source = domainplugin.SourceRegistry
	plugin.Key = "registry/acme.notes"
	service := &fakePluginService{
		plugins: []appplugin.InstalledPlugin{plugin},
		plan: appplugin.UpdatePlan{
			ID: "acme.notes", From: "1.0.0", To: "1.1.0",
			SourceChanges: []appplugin.SourceChange{{
				ID: "acme.core", PinnedSource: "/old/core.natplug", NewSource: "registry-two",
			}},
		},
	}
	state := NewPluginsState(service)
	state.Plugins()
	state.BeginUpdate()
	state.ConfirmUpdate()
	if !service.appliedPlan.ConfirmedSourceChanges.Contains("acme.core") {
		t.Fatalf("confirmed ids = %+v, want acme.core", service.appliedPlan.ConfirmedSourceChanges)
	}
	if service.appliedPlan.ConfirmedSourceChanges.Contains("acme.notes") {
		t.Fatalf("confirmed ids = %+v, an unseen id was confirmed",
			service.appliedPlan.ConfirmedSourceChanges)
	}
}

// TestPluginsStateDiagnosticsAndRestart drives the host-owned diagnostics
// view: the panel loads by selection, reports the lifecycle snapshot, and the
// restart action targets the selected plugin.
func TestPluginsStateDiagnosticsAndRestart(t *testing.T) {
	plugin := pluginFixture()
	service := &fakePluginService{
		plugins: []appplugin.InstalledPlugin{plugin},
		diagnostics: appplugin.PluginDiagnostics{
			ID:       domainplugin.NewFiberID("installed", plugin.ID, 1),
			PluginID: plugin.ID, State: domainplugin.StateFailed,
			Reason: "trapped", Effects: 2, MemoryBytes: 1024, MemoryMax: 64 << 20,
			MissingInjections: []string{"plugin.acme.other.search"},
			ConfigErrors:      []domainplugin.SettingsError{{Field: "mode", Problem: "bad"}},
			Faults:            []appplugin.FaultRecord{{Operation: "dispatch", Reason: "trap"}},
			Logs:              []string{"INFO: hello"},
		},
	}
	state := NewPluginsState(service)
	state.Plugins()
	state.Select(plugin.Key)
	view, ok := state.DiagnosticsView()
	if !ok {
		t.Fatalf("DiagnosticsView returned no view (%s)", state.DiagnosticsProblem())
	}
	if service.diagnosticsKey != plugin.Key || view.PluginID != plugin.ID {
		t.Fatalf("diagnostics key = %q, view = %+v", service.diagnosticsKey, view)
	}
	if view.State != domainplugin.StateFailed || len(view.Faults) != 1 {
		t.Fatalf("diagnostics view = %+v", view)
	}
	state.RestartPlugin()
	if service.restartKey != plugin.Key {
		t.Fatalf("restart key = %q, want %q", service.restartKey, plugin.Key)
	}
}

// TestPluginsAreaRendersTheDiagnosticsPanel proves the settings surface shows
// the lifecycle state and the manual restart action without asking a guest.
func TestPluginsAreaRendersTheDiagnosticsPanel(t *testing.T) {
	plugin := pluginFixture()
	plugin.Status = appplugin.StatusFailed
	plugin.State = domainplugin.StateFailed
	plugin.Reason = "trapped"
	service := &fakePluginService{
		plugins: []appplugin.InstalledPlugin{plugin},
		diagnostics: appplugin.PluginDiagnostics{
			ID:       domainplugin.NewFiberID("installed", plugin.ID, 1),
			PluginID: plugin.ID, State: domainplugin.StateFailed,
			Reason: "trapped", Effects: 1, MemoryBytes: 1024, MemoryMax: 64 << 20,
			Logs: []string{"INFO: hello"},
		},
	}
	state := NewPluginsState(service)
	state.Plugins()
	state.Select(plugin.Key)
	tester := ui.NewTester(viewAt(AreaPlugins, Config{Plugins: state}), 1100, 900)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")
	for _, want := range []string{
		m.SettingsPluginsDiagnosticsTitle(),
		m.SettingsPluginsDiagnosticsLifecycle(string(domainplugin.StateFailed)),
		m.SettingsPluginsDiagnosticsRestart(),
		m.SettingsPluginsDiagnosticsLogs(),
	} {
		if !tester.HasText(want) {
			t.Fatalf("diagnostics panel lacks %q; texts %q", want, tester.Texts())
		}
	}
}

// TestPluginsAreaRendersTheDiagnosticsFiberTree proves the diagnostics view
// shows the group/fiber tree: every group once, nested under its parent, and
// every instance once.
func TestPluginsAreaRendersTheDiagnosticsFiberTree(t *testing.T) {
	plugin := pluginFixture()
	first := domainplugin.NewFiberID("installed", plugin.ID, 1)
	second := domainplugin.NewFiberID("installed", plugin.ID, 2)
	nested := domainplugin.NewFiberID("installed.child", "acme.core", 1)
	service := &fakePluginService{
		plugins: []appplugin.InstalledPlugin{plugin},
		diagnostics: appplugin.PluginDiagnostics{
			ID: first, PluginID: plugin.ID, Group: "installed",
			State: domainplugin.StateActive,
		},
		diagnosticsReport: appplugin.DiagnosticsReport{
			Groups: []appplugin.GroupDiagnostics{
				{Name: "installed", Children: []string{"installed.child", "installed.empty"}},
				{Name: "installed.child", Parent: "installed",
					Fibers: []domainplugin.FiberID{first, second, nested}},
				{Name: "installed.empty", Parent: "installed"},
			},
			Fibers: []appplugin.PluginDiagnostics{
				{ID: first, Group: "installed.child", PluginID: plugin.ID,
					Instance: 1, State: domainplugin.StateActive},
				{ID: second, Group: "installed.child", PluginID: plugin.ID,
					Instance: 2, State: domainplugin.StatePending},
				{ID: nested, Group: "installed.child", PluginID: "acme.core",
					Instance: 1, State: domainplugin.StateActive},
			},
		},
	}
	state := NewPluginsState(service)
	state.Plugins()
	state.Select(plugin.Key)
	tester := ui.NewTester(viewAt(AreaPlugins, Config{Plugins: state}), 1100, 1500)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")
	if !tester.HasText(m.SettingsPluginsDiagnosticsFiberTree()) {
		t.Fatalf("diagnostics view lacks the fiber tree; texts %q", tester.Texts())
	}
	for _, group := range []string{
		"installed", "• installed.child", "• installed.empty",
	} {
		count := 0
		for _, text := range tester.Texts() {
			if text == group {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("group line %q appears %d times; texts %q", group, count, tester.Texts())
		}
	}
	for _, fiber := range []domainplugin.FiberID{first, second, nested} {
		prefix := "• • " + string(fiber) + " "
		count := 0
		for _, text := range tester.Texts() {
			if strings.HasPrefix(text, prefix) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("fiber %q appears %d times; texts %q", fiber, count, tester.Texts())
		}
	}
}

// TestPluginsAreaNamesADependencySourceChangeInTheUpdateReview verifies the
// review renders the changed dependency id, not just the root plugin name.
func TestPluginsAreaNamesADependencySourceChangeInTheUpdateReview(t *testing.T) {
	plugin := pluginFixture()
	plugin.Source = domainplugin.SourceRegistry
	plugin.Key = "registry/acme.notes"
	service := &fakePluginService{
		plugins: []appplugin.InstalledPlugin{plugin},
		plan: appplugin.UpdatePlan{
			ID: "acme.notes", From: "1.0.0", To: "1.1.0",
			SourceChanges: []appplugin.SourceChange{{
				ID: "acme.core", PinnedSource: "/old/core.natplug", NewSource: "registry-two",
			}},
		},
	}
	state := NewPluginsState(service)
	state.Plugins()
	state.BeginUpdate()
	tester := ui.NewTester(viewAt(AreaPlugins, Config{Plugins: state}), 1100, 900)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")
	if !tester.HasText(m.SettingsPluginsSourceChangeTitle("acme.core")) {
		t.Fatalf("update review lacks the dependency name; texts %q", tester.Texts())
	}
	if tester.HasText(m.SettingsPluginsSourceChangeTitle("acme.notes")) {
		t.Fatalf("update review labelled the root as the source change; texts %q", tester.Texts())
	}
}

// hasExactPluginText reports whether want is one complete label. HasText is a
// case-sensitive substring check, so "Disable" would match "Disabled".
func hasExactPluginText(texts []string, want string) bool {
	for _, text := range texts {
		if text == want {
			return true
		}
	}
	return false
}

// builtinPluginFixture is one compiled-in plugin summary. Its declared
// permissions are visible and already granted; the terminal actions an
// installed package offers do not apply.
func builtinPluginFixture(enabled bool) appplugin.InstalledPlugin {
	status := appplugin.StatusDisabled
	if enabled {
		status = appplugin.StatusActive
	}
	return appplugin.InstalledPlugin{
		ID:      "natcatty.sample",
		Key:     "built-in/natcatty.sample",
		Name:    "Sample Built-in",
		Version: "9.8.7",
		Source:  domainplugin.SourceBuiltIn,
		Status:  status,
		Enabled: enabled,
		State:   domainplugin.StatePending,
		Permissions: []appplugin.PermissionStatus{
			{Permission: domainplugin.PermissionCommands, Required: true, Granted: true},
			{Permission: domainplugin.PermissionViews, Required: true, Granted: true},
			{Permission: domainplugin.PermissionUINative, Granted: true},
		},
	}
}

// TestPluginsAreaRendersABuiltInAndEnablesItWithoutConsent proves the settings
// surface labels shipped code, shows its permissions as granted, offers only
// Enable/Disable, and calls the service directly instead of opening a consent
// screen.
func TestPluginsAreaRendersABuiltInAndEnablesItWithoutConsent(t *testing.T) {
	plugin := builtinPluginFixture(false)
	service := &fakePluginService{
		plugins:      []appplugin.InstalledPlugin{plugin},
		settingsView: appplugin.PluginSettingsView{Valid: true},
	}
	state := NewPluginsState(service)
	state.Plugins()
	tester := ui.NewTester(viewAt(AreaPlugins, Config{Plugins: state}), 1100, 1000)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	badges := 0
	for _, text := range tester.Texts() {
		if text == m.SettingsPluginsSourceBuiltIn() {
			badges++
		}
	}
	if badges < 2 {
		t.Fatalf("built-in badge appears %d times; texts %q", badges, tester.Texts())
	}
	for _, want := range []string{
		"Sample Built-in", m.SettingsPluginsEnable(),
		string(domainplugin.PermissionCommands), string(domainplugin.PermissionUINative),
		m.SettingsPluginsGranted(),
	} {
		if !tester.HasText(want) {
			t.Fatalf("built-in detail lacks %q; texts %q", want, tester.Texts())
		}
	}
	for _, unwanted := range []string{
		m.SettingsPluginsUninstall(), m.SettingsPluginsPurgeData(),
		m.SettingsPluginsUpdate(), m.SettingsPluginsRollback(),
		m.SettingsPluginsRevoke(), m.SettingsPluginsDisable(),
	} {
		if hasExactPluginText(tester.Texts(), unwanted) {
			t.Fatalf("built-in detail offers %q; texts %q", unwanted, tester.Texts())
		}
	}
	if err := tester.Click(m.SettingsPluginsEnable()); err != nil {
		t.Fatalf("Click Enable: %v", err)
	}
	if service.enabledKey != plugin.Key {
		t.Fatalf("enabled key = %q, want %q", service.enabledKey, plugin.Key)
	}
	if len(service.enabled) != 0 {
		t.Fatalf("built-in enable submitted consent decisions: %+v", service.enabled)
	}
	if _, reviewing := state.Review(); reviewing {
		t.Fatal("enabling a built-in opened a consent review")
	}
	if state.Problem() != PluginProblemNone {
		t.Fatalf("enable reported a problem: %d %q", state.Problem(), state.ProblemDetail())
	}
}

// TestPluginsAreaOffersABuiltInOnlyDisableWhenActive proves the runtime action
// set is the same for both built-in states: shipped code can be turned off but
// never uninstalled, updated, rolled back, purged, or revoked.
func TestPluginsAreaOffersABuiltInOnlyDisableWhenActive(t *testing.T) {
	plugin := builtinPluginFixture(true)
	plugin.State = domainplugin.StateActive
	service := &fakePluginService{
		plugins:      []appplugin.InstalledPlugin{plugin},
		settingsView: appplugin.PluginSettingsView{Valid: true},
	}
	state := NewPluginsState(service)
	state.Plugins()
	tester := ui.NewTester(viewAt(AreaPlugins, Config{Plugins: state}), 1100, 1200)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if !tester.HasText(m.SettingsPluginsDisable()) {
		t.Fatalf("active built-in has no Disable action; texts %q", tester.Texts())
	}
	for _, unwanted := range []string{
		m.SettingsPluginsEnable(), m.SettingsPluginsUninstall(),
		m.SettingsPluginsPurgeData(), m.SettingsPluginsUpdate(),
		m.SettingsPluginsRollback(), m.SettingsPluginsRevoke(),
	} {
		if hasExactPluginText(tester.Texts(), unwanted) {
			t.Fatalf("active built-in offers %q; texts %q", unwanted, tester.Texts())
		}
	}
	if err := tester.Click(m.SettingsPluginsDisable()); err != nil {
		t.Fatalf("Click Disable: %v", err)
	}
	if service.disabledKey != plugin.Key {
		t.Fatalf("disabled key = %q, want %q", service.disabledKey, plugin.Key)
	}
}
