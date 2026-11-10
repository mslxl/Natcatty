package settings

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/ui/locales"
)

// PluginService is the plugin-manager seam the Settings surface drives. The
// app supplies *application/plugin.Manager behind a small adapter that also
// owns the native file dialog and the plugin-key routing; tests supply a fake.
// Every method is called on the UI thread.
type PluginService interface {
	// Plugins returns the installed packages and development directories.
	Plugins() ([]appplugin.InstalledPlugin, error)
	// Review returns the install-source-labelled consent rows for an installed
	// package.
	Review(key string) (domainplugin.ConsentReview, error)
	// InstallFromFile asks the user for a local .natplug and installs it. The
	// installed flag is false when the user cancels the dialog.
	// confirmedSourceChanges names the plugin ids whose trust-on-first-use
	// source change the user explicitly approved.
	InstallFromFile(confirmedSourceChanges appplugin.ConfirmedSourceChanges) (key string, installed bool, err error)
	// SearchRegistry searches the static registry index on the client.
	SearchRegistry(query string) ([]appplugin.RegistrySearchResult, error)
	// RegistryPreferences returns the persisted channel and automatic-update
	// policy.
	RegistryPreferences() (domainplugin.RegistryPreferences, error)
	// SetChannel opts one plugin into beta releases or back to stable.
	SetChannel(key string, channel domainplugin.Channel) error
	// SetAutomaticUpdates records the automatic-update preference. It is off by
	// default.
	SetAutomaticUpdates(enabled bool) error
	// InstallFromRegistry installs one registry version disabled.
	InstallFromRegistry(id, version string,
		confirmedSourceChanges appplugin.ConfirmedSourceChanges) (key string, installed bool, err error)
	// PlanUpdate computes the update permission diff without mutating anything.
	PlanUpdate(key string) (appplugin.UpdatePlan, error)
	// ApplyUpdate applies a reviewed plan with the user's consent decisions.
	ApplyUpdate(key string, plan appplugin.UpdatePlan,
		decisions []domainplugin.PermissionDecision) error
	// Rollback restores the one retained previous version.
	Rollback(key string, decisions []domainplugin.PermissionDecision) error
	Enable(key string, decisions []domainplugin.PermissionDecision) error
	Disable(key string) error
	Revoke(key string, permission domainplugin.Permission) error
	// RequestOptional answers a first-use optional permission prompt for one
	// concrete scope. Remember authorizes the same scope without prompting
	// again.
	RequestOptional(key string, permission domainplugin.Permission,
		scope domainplugin.Scope, allow, remember bool) error
	Uninstall(key string, purge bool) error
	PurgeData(key string) error
	// Settings returns the plugin's declared settings page with effective
	// values and per-field validation errors.
	Settings(key string) (appplugin.PluginSettingsView, error)
	// SaveSettings validates and persists the full configuration.
	SaveSettings(key string, values map[string]any) error
	// ResetSettings restores the declared defaults.
	ResetSettings(key string) error
	// Diagnostics returns the first-party lifecycle and runtime snapshot for
	// one plugin. It is host-owned data, not a plugin service.
	Diagnostics(key string) (appplugin.PluginDiagnostics, error)
	// DiagnosticsReport returns the container-wide group/fiber tree and every
	// instance's diagnostics. It is host-owned data, not a plugin service.
	DiagnosticsReport() appplugin.DiagnosticsReport
	// Restart manually reloads a plugin after a fault exhausted its automatic
	// restart budget.
	Restart(key string) error
}

// PluginProblem is the last action outcome the surface shows. It is a stable
// enum, not an error string, so every locale renders the heading from the
// catalog; Detail carries the host's specific validation or activation text.
type PluginProblem int

const (
	PluginProblemNone PluginProblem = iota
	PluginProblemInstall
	PluginProblemEnable
	PluginProblemAction
)

// pluginConfirm is the destructive action awaiting confirmation. Uninstall and
// purge are separate states so purging data always has its own confirmation,
// even though the manager can purge as part of an uninstall.
type pluginConfirm int

const (
	pluginConfirmNone pluginConfirm = iota
	pluginConfirmUninstall
	pluginConfirmPurge
	pluginConfirmRollback
)

// pluginSourceChange is a pending trust-on-first-use source change that needs
// an explicit second action. local is true for a side-loaded package; the
// dialog is shown again because the service owns the native picker. id and
// version identify the install to repeat, while changed is the plugin id whose
// pin actually differs: a dependency install can change a different id than
// the package the user asked for.
type pluginSourceChange struct {
	local    bool
	id       string
	version  string
	changed  string
	previous string
	current  string
	// confirmed holds the ids the user approved on earlier rounds of the same
	// install. A root package and each dependency can have its own pin.
	confirmed appplugin.ConfirmedSourceChanges
}

// pluginRememberKey scopes a remember-for-scope checkbox to one plugin. A
// permission name alone is not an identity: two plugins declare the same
// permission with different scopes, so a choice made for one must never
// authorize the other.
type pluginRememberKey struct {
	plugin     string
	permission domainplugin.Permission
}

// PluginsState owns the Settings Plugins surface. All fields are mutated only
// on the UI thread; the manager itself is safe for concurrent use.
type PluginsState struct {
	service PluginService

	loaded   bool
	plugins  []appplugin.InstalledPlugin
	selected string

	review    *domainplugin.ConsentReview
	decisions map[domainplugin.Permission]bool
	remember  map[pluginRememberKey]bool
	confirm   pluginConfirm

	registryQuery   string
	registryResults []appplugin.RegistrySearchResult
	registryLoaded  bool
	preferences     domainplugin.RegistryPreferences
	sourceChange    *pluginSourceChange

	updatePlan      *appplugin.UpdatePlan
	updateDecisions map[domainplugin.Permission]bool

	settingsKey     string
	settingsView    *appplugin.PluginSettingsView
	settingsValues  map[string]any
	settingsProblem string

	diagnosticsKey     string
	diagnostics        *appplugin.PluginDiagnostics
	diagnosticsProblem string
	diagnosticsReport  *appplugin.DiagnosticsReport

	problem       PluginProblem
	problemDetail string
}

// NewPluginsState builds the plugin manager surface from the service.
func NewPluginsState(service PluginService) *PluginsState {
	return &PluginsState{
		service:         service,
		decisions:       make(map[domainplugin.Permission]bool),
		remember:        make(map[pluginRememberKey]bool),
		updateDecisions: make(map[domainplugin.Permission]bool),
	}
}

// Plugins returns the cached list, loading it on the first frame. Actions
// refresh the cache, so the surface does not stat the install layout every
// frame.
func (s *PluginsState) Plugins() []appplugin.InstalledPlugin {
	if s == nil {
		return nil
	}
	if !s.loaded {
		s.refresh()
	}
	return s.plugins
}

// Selected returns the selected plugin.
func (s *PluginsState) Selected() (appplugin.InstalledPlugin, bool) {
	if s == nil {
		return appplugin.InstalledPlugin{}, false
	}
	for _, plugin := range s.Plugins() {
		if plugin.Key == s.selected {
			return plugin, true
		}
	}
	return appplugin.InstalledPlugin{}, false
}

// Select focuses one plugin and clears any in-flight consent or confirmation.
// A remember-for-scope choice belongs to the previous selection and is dropped
// when the selection changes.
func (s *PluginsState) Select(key string) {
	if s == nil {
		return
	}
	if s.selected != key {
		s.remember = make(map[pluginRememberKey]bool)
		s.sourceChange = nil
		s.updatePlan = nil
		s.updateDecisions = make(map[domainplugin.Permission]bool)
		s.resetSettings()
		s.resetDiagnostics()
	}
	s.selected = key
	s.review = nil
	s.confirm = pluginConfirmNone
	s.problem = PluginProblemNone
	s.problemDetail = ""
}

func (s *PluginsState) resetSettings() {
	s.settingsKey = ""
	s.settingsView = nil
	s.settingsValues = nil
	s.settingsProblem = ""
}

func (s *PluginsState) resetDiagnostics() {
	s.diagnosticsKey = ""
	s.diagnostics = nil
	s.diagnosticsProblem = ""
	s.diagnosticsReport = nil
}

// DiagnosticsView returns the selected plugin's lifecycle and runtime
// diagnostics, loading them once per selection. The panel refreshes after a
// settings save or a restart.
func (s *PluginsState) DiagnosticsView() (appplugin.PluginDiagnostics, bool) {
	if s == nil || s.service == nil {
		return appplugin.PluginDiagnostics{}, false
	}
	key, ok := s.currentKey()
	if !ok {
		return appplugin.PluginDiagnostics{}, false
	}
	if s.diagnosticsKey != key {
		s.diagnosticsKey = key
		s.diagnostics = nil
		s.diagnosticsProblem = ""
		view, err := s.service.Diagnostics(key)
		if err != nil {
			s.diagnosticsProblem = err.Error()
			return appplugin.PluginDiagnostics{}, false
		}
		s.diagnostics = &view
	}
	if s.diagnostics == nil {
		return appplugin.PluginDiagnostics{}, false
	}
	return *s.diagnostics, true
}

// DiagnosticsTree returns the container-wide group/fiber tree. It is loaded
// once per selection and after a restart, like the per-plugin diagnostics.
func (s *PluginsState) DiagnosticsTree() (appplugin.DiagnosticsReport, bool) {
	if s == nil || s.service == nil {
		return appplugin.DiagnosticsReport{}, false
	}
	if s.diagnosticsReport == nil {
		report := s.service.DiagnosticsReport()
		s.diagnosticsReport = &report
	}
	return *s.diagnosticsReport, true
}

// DiagnosticsProblem returns the last diagnostics load failure.
func (s *PluginsState) DiagnosticsProblem() string {
	if s == nil {
		return ""
	}
	return s.diagnosticsProblem
}

// RestartPlugin manually reloads the selected plugin and refreshes every view.
func (s *PluginsState) RestartPlugin() {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	if err := s.service.Restart(key); err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		return
	}
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.settingsKey = ""
	s.resetDiagnostics()
	s.refresh()
}

// SettingsView returns the selected plugin's settings page, loading it once
// per selection. A plugin with no schema still returns an empty valid view.
func (s *PluginsState) SettingsView() (appplugin.PluginSettingsView, bool) {
	if s == nil || s.service == nil {
		return appplugin.PluginSettingsView{}, false
	}
	key, ok := s.currentKey()
	if !ok {
		return appplugin.PluginSettingsView{}, false
	}
	if s.settingsKey != key {
		s.settingsKey = key
		s.settingsView = nil
		s.settingsValues = nil
		s.settingsProblem = ""
		view, err := s.service.Settings(key)
		if err != nil {
			s.settingsProblem = err.Error()
			return appplugin.PluginSettingsView{}, false
		}
		s.settingsView = &view
		s.settingsValues = make(map[string]any, len(view.Fields))
		for _, field := range view.Fields {
			s.settingsValues[field.Key] = field.Value
		}
	}
	if s.settingsView == nil {
		return appplugin.PluginSettingsView{}, false
	}
	return *s.settingsView, true
}

// RefreshLocalizedText drops the cached settings page so its next frame
// re-reads the manifest labels and descriptions in the new host locale. The
// plugin and its settings are untouched; only the display cache is cleared.
func (s *PluginsState) RefreshLocalizedText() {
	if s == nil {
		return
	}
	s.settingsKey = ""
	s.settingsView = nil
	s.settingsValues = nil
}

// SettingsProblem returns a load or save failure for the settings page.
func (s *PluginsState) SettingsProblem() string {
	if s == nil {
		return ""
	}
	return s.settingsProblem
}

// SettingValue returns the current edited value for one field.
func (s *PluginsState) SettingValue(key string) any {
	if s == nil {
		return nil
	}
	if value, ok := s.settingsValues[key]; ok {
		return value
	}
	if s.settingsView != nil {
		for _, field := range s.settingsView.Fields {
			if field.Key == key {
				return field.Value
			}
		}
	}
	return nil
}

// SetSettingValue records one edited field. The full value set is saved, so
// untouched fields keep their stored values rather than reverting to defaults.
func (s *PluginsState) SetSettingValue(key string, value any) {
	if s == nil {
		return
	}
	if s.settingsValues == nil {
		s.settingsValues = make(map[string]any)
	}
	s.settingsValues[key] = value
	s.settingsProblem = ""
}

// SaveSettings submits the edited configuration. A field with no stored value
// and no default renders as nil; it is omitted rather than sent, because a nil
// value means "unset", not "invalid".
func (s *PluginsState) SaveSettings() {
	view, ok := s.SettingsView()
	if !ok {
		return
	}
	key, current := s.currentKey()
	if !current {
		return
	}
	values := make(map[string]any, len(view.Fields))
	for _, field := range view.Fields {
		value := s.SettingValue(field.Key)
		if value == nil {
			continue
		}
		values[field.Key] = value
	}
	if err := s.service.SaveSettings(key, values); err != nil {
		s.settingsProblem = err.Error()
		return
	}
	s.settingsProblem = ""
	s.reloadSettings()
	s.refresh()
}

// ResetSettings restores the declared defaults and reloads the page.
func (s *PluginsState) ResetSettings() {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	if err := s.service.ResetSettings(key); err != nil {
		s.settingsProblem = err.Error()
		return
	}
	s.settingsProblem = ""
	s.settingsKey = ""
	s.reloadSettings()
	s.refresh()
}

func (s *PluginsState) reloadSettings() {
	s.settingsKey = ""
	s.SettingsView()
}

// Review returns the consent screen currently open, if any.
func (s *PluginsState) Review() (domainplugin.ConsentReview, bool) {
	if s == nil || s.review == nil {
		return domainplugin.ConsentReview{}, false
	}
	return *s.review, true
}

// Decision reports whether the user allowed one consent row.
func (s *PluginsState) Decision(permission domainplugin.Permission) bool {
	if s == nil {
		return false
	}
	return s.decisions[permission]
}

// SetDecision records the user's answer for one consent row.
func (s *PluginsState) SetDecision(permission domainplugin.Permission, allow bool) {
	if s == nil {
		return
	}
	s.decisions[permission] = allow
}

// RememberOptional reports whether an optional permission's first-use or
// enable decision asks the host to remember the grant for its scope. The
// answer is scoped to the selected plugin.
func (s *PluginsState) RememberOptional(permission domainplugin.Permission) bool {
	if s == nil {
		return false
	}
	key, ok := s.currentKey()
	if !ok {
		return false
	}
	return s.remember[pluginRememberKey{plugin: key, permission: permission}]
}

// SetRememberOptional records the remember-for-scope checkbox for an optional
// permission. It is scoped to the selected plugin so a choice cannot leak
// across plugins that declare the same permission for a different scope.
func (s *PluginsState) SetRememberOptional(permission domainplugin.Permission, remember bool) {
	if s == nil {
		return
	}
	key, ok := s.currentKey()
	if !ok {
		return
	}
	s.remember[pluginRememberKey{plugin: key, permission: permission}] = remember
}

// RequestOptional answers the first-use prompt for an optional permission on
// the selected package. The declared scope shown in the permission row is the
// scope bound to the grant; Remember skips the prompt on the next use.
func (s *PluginsState) RequestOptional(permission domainplugin.Permission, allow bool) {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	plugin, ok := s.Selected()
	if !ok {
		return
	}
	var scope domainplugin.Scope
	found := false
	for _, status := range plugin.Permissions {
		if status.Permission == permission {
			scope = status.Scope
			found = true
			break
		}
	}
	if !found || pluginHasRequired(plugin, permission) {
		return
	}
	rememberKey := pluginRememberKey{plugin: key, permission: permission}
	remember := s.remember[rememberKey]
	if err := s.service.RequestOptional(key, permission, scope, allow, remember); err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		s.refresh()
		return
	}
	delete(s.remember, rememberKey)
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.refresh()
}

func pluginHasRequired(plugin appplugin.InstalledPlugin, permission domainplugin.Permission) bool {
	for _, status := range plugin.Permissions {
		if status.Permission == permission && status.Required {
			return true
		}
	}
	return false
}

// Confirm returns the destructive action awaiting confirmation.
func (s *PluginsState) Confirm() pluginConfirm {
	if s == nil {
		return pluginConfirmNone
	}
	return s.confirm
}

// Problem returns the last localized problem, or PluginProblemNone.
func (s *PluginsState) Problem() PluginProblem {
	if s == nil {
		return PluginProblemNone
	}
	return s.problem
}

// ProblemDetail returns the host's specific explanation for the last problem.
func (s *PluginsState) ProblemDetail() string {
	if s == nil {
		return ""
	}
	return s.problemDetail
}

func (s *PluginsState) refresh() {
	s.loaded = true
	if s.service == nil {
		return
	}
	plugins, err := s.service.Plugins()
	if err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		return
	}
	s.plugins = plugins
	keep := false
	for _, plugin := range plugins {
		if plugin.Key == s.selected {
			keep = true
			break
		}
	}
	if !keep {
		s.selected = ""
		s.review = nil
		s.confirm = pluginConfirmNone
		s.remember = make(map[pluginRememberKey]bool)
		s.resetSettings()
		if len(plugins) > 0 {
			s.selected = plugins[0].Key
		}
	}
}

func (s *PluginsState) currentKey() (string, bool) {
	plugin, ok := s.Selected()
	if !ok || plugin.Development {
		return "", false
	}
	return plugin.Key, true
}

// Install asks for a local package and selects it on success. The package is
// installed disabled; enabling stays a separate consent step. A trust-on-first-
// use source change is surfaced as a pending confirmation instead of success.
func (s *PluginsState) Install() {
	if s == nil || s.service == nil {
		return
	}
	s.installLocal(nil)
}

func (s *PluginsState) installLocal(confirmed appplugin.ConfirmedSourceChanges) {
	s.sourceChange = nil
	key, installed, err := s.service.InstallFromFile(confirmed)
	if err != nil {
		s.failInstall(err, func(changed *appplugin.SourceChangedError) *pluginSourceChange {
			return &pluginSourceChange{
				local: true, id: changed.ID, changed: changed.ID,
				previous: changed.PinnedSource, current: changed.NewSource,
				confirmed: cloneConfirmedSourceChanges(confirmed),
			}
		})
		return
	}
	if !installed {
		return
	}
	s.installed(key)
}

// installed clears the install flow and refreshes the list.
func (s *PluginsState) installed(key string) {
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.review = nil
	s.confirm = pluginConfirmNone
	s.sourceChange = nil
	s.selected = key
	s.registryLoaded = false
	s.refresh()
}

func (s *PluginsState) failInstall(err error,
	change func(*appplugin.SourceChangedError) *pluginSourceChange) {
	var typed *appplugin.SourceChangedError
	if errors.As(err, &typed) {
		s.sourceChange = change(typed)
		s.problem = PluginProblemInstall
		s.problemDetail = err.Error()
		return
	}
	s.problem = PluginProblemInstall
	s.problemDetail = err.Error()
}

// RegistryQuery returns the current client-side search text.
func (s *PluginsState) RegistryQuery() string {
	if s == nil {
		return ""
	}
	return s.registryQuery
}

// SetRegistryQuery records the search text without running it.
func (s *PluginsState) SetRegistryQuery(query string) {
	if s == nil {
		return
	}
	s.registryQuery = query
}

// SearchRegistry runs the client-side search and reloads the registry policy.
func (s *PluginsState) SearchRegistry() {
	if s == nil || s.service == nil {
		return
	}
	s.registryLoaded = false
	s.loadRegistry()
}

// RegistryResults returns the cached registry search, loading on first use.
func (s *PluginsState) RegistryResults() []appplugin.RegistrySearchResult {
	if s == nil {
		return nil
	}
	if !s.registryLoaded {
		s.loadRegistry()
	}
	return s.registryResults
}

func (s *PluginsState) loadRegistry() {
	s.registryLoaded = true
	if s.service == nil {
		return
	}
	preferences, err := s.service.RegistryPreferences()
	if err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		return
	}
	s.preferences = preferences
	results, err := s.service.SearchRegistry(s.registryQuery)
	if err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		s.registryResults = nil
		return
	}
	s.registryResults = results
}

// RegistryChannel returns the selected release channel for one plugin.
func (s *PluginsState) RegistryChannel(id string) domainplugin.Channel {
	if s == nil {
		return domainplugin.ChannelStable
	}
	return s.preferences.ChannelFor(id)
}

// SetRegistryChannel opts one plugin into beta or back to stable and reloads
// the search.
func (s *PluginsState) SetRegistryChannel(id string,
	channel domainplugin.Channel) {
	if s == nil || s.service == nil {
		return
	}
	if err := s.service.SetChannel(id, channel); err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		return
	}
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.registryLoaded = false
	s.loadRegistry()
}

// AutomaticUpdates reports the persisted policy; off by default.
func (s *PluginsState) AutomaticUpdates() bool {
	if s == nil {
		return false
	}
	if !s.registryLoaded {
		s.loadRegistry()
	}
	return s.preferences.AutomaticUpdates
}

// SetAutomaticUpdates records the automatic-update preference.
func (s *PluginsState) SetAutomaticUpdates(enabled bool) {
	if s == nil || s.service == nil {
		return
	}
	if err := s.service.SetAutomaticUpdates(enabled); err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		return
	}
	s.preferences.AutomaticUpdates = enabled
	s.problem = PluginProblemNone
	s.problemDetail = ""
}

// InstallRegistry installs the latest selected registry version disabled. A
// source change is returned as a pending confirmation.
func (s *PluginsState) InstallRegistry(id, version string) {
	if s == nil || s.service == nil {
		return
	}
	s.installRegistry(id, version, nil)
}

func (s *PluginsState) installRegistry(id, version string,
	confirmed appplugin.ConfirmedSourceChanges) {
	s.sourceChange = nil
	key, installed, err := s.service.InstallFromRegistry(id, version, confirmed)
	if err != nil {
		s.failInstall(err, func(changed *appplugin.SourceChangedError) *pluginSourceChange {
			return &pluginSourceChange{
				id: id, version: version, changed: changed.ID,
				previous: changed.PinnedSource, current: changed.NewSource,
				confirmed: cloneConfirmedSourceChanges(confirmed),
			}
		})
		return
	}
	if !installed {
		return
	}
	s.installed(key)
}

// SourceChange returns the pending trust-on-first-use confirmation, if any.
func (s *PluginsState) SourceChange() (pluginSourceChange, bool) {
	if s == nil || s.sourceChange == nil {
		return pluginSourceChange{}, false
	}
	return *s.sourceChange, true
}

// ConfirmSourceChange repeats the install with the user's explicit source
// change confirmation.
func (s *PluginsState) ConfirmSourceChange() {
	if s == nil || s.sourceChange == nil {
		return
	}
	pending := *s.sourceChange
	confirmed := cloneConfirmedSourceChanges(pending.confirmed)
	confirmed[pending.changed] = true
	if pending.local {
		s.installLocal(confirmed)
		return
	}
	s.installRegistry(pending.id, pending.version, confirmed)
}

// cloneConfirmedSourceChanges returns an independent copy so a later round
// never mutates the set an earlier round passed to the service.
func cloneConfirmedSourceChanges(source appplugin.ConfirmedSourceChanges) appplugin.ConfirmedSourceChanges {
	out := make(appplugin.ConfirmedSourceChanges, len(source)+1)
	for id, ok := range source {
		if ok {
			out[id] = true
		}
	}
	return out
}

// CancelSourceChange dismisses the pending source-change confirmation.
func (s *PluginsState) CancelSourceChange() {
	if s == nil {
		return
	}
	s.sourceChange = nil
	s.problem = PluginProblemNone
	s.problemDetail = ""
}

// BeginUpdate plans an update and opens the permission-diff review. The plan
// is read-only; nothing is swapped until ConfirmUpdate.
func (s *PluginsState) BeginUpdate() {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	plan, err := s.service.PlanUpdate(key)
	if err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		s.updatePlan = nil
		return
	}
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.updatePlan = &plan
	s.updateDecisions = make(map[domainplugin.Permission]bool)
	s.confirm = pluginConfirmNone
}

// UpdatePlan returns the open update review, if any.
func (s *PluginsState) UpdatePlan() (appplugin.UpdatePlan, bool) {
	if s == nil || s.updatePlan == nil {
		return appplugin.UpdatePlan{}, false
	}
	return *s.updatePlan, true
}

// UpdateDecision reports whether the user allowed one newly required
// permission on the update review.
func (s *PluginsState) UpdateDecision(permission domainplugin.Permission) bool {
	if s == nil {
		return false
	}
	return s.updateDecisions[permission]
}

// SetUpdateDecision records one update consent row.
func (s *PluginsState) SetUpdateDecision(permission domainplugin.Permission, allow bool) {
	if s == nil {
		return
	}
	s.updateDecisions[permission] = allow
}

// ConfirmUpdate applies the reviewed plan. The click confirms exactly the
// source changes the review rendered, per plugin id; the manager refuses any
// change that is not in the set, including a dependency pin that moved after
// planning.
func (s *PluginsState) ConfirmUpdate() {
	key, ok := s.currentKey()
	if !ok || s.service == nil || s.updatePlan == nil {
		return
	}
	plan := *s.updatePlan
	plan.ConfirmedSourceChanges = make(appplugin.ConfirmedSourceChanges, len(plan.SourceChanges))
	for _, change := range plan.SourceChanges {
		plan.ConfirmedSourceChanges[change.ID] = true
	}
	if len(plan.SourceChanges) == 0 && plan.SourceChanged {
		// Compatibility for a caller-built plan that only carries the root
		// summary; the review rendered that root row.
		plan.ConfirmedSourceChanges[plan.ID] = true
	}
	var decisions []domainplugin.PermissionDecision
	for _, request := range plan.RequiresConsent {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: request.Permission, Scope: request.Scope,
			Allow: s.UpdateDecision(request.Permission),
		})
	}
	for _, request := range plan.Diff.AddedOptional {
		if s.UpdateDecision(request.Permission) {
			decisions = append(decisions, domainplugin.PermissionDecision{
				Permission: request.Permission, Scope: request.Scope, Allow: true,
			})
		}
	}
	if err := s.service.ApplyUpdate(key, plan, decisions); err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		s.refresh()
		return
	}
	s.updatePlan = nil
	s.updateDecisions = make(map[domainplugin.Permission]bool)
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.registryLoaded = false
	s.refresh()
}

// CancelUpdate closes the update review without applying it.
func (s *PluginsState) CancelUpdate() {
	if s == nil {
		return
	}
	s.updatePlan = nil
	s.updateDecisions = make(map[domainplugin.Permission]bool)
}

// AskRollback opens the rollback confirmation for the selected package.
func (s *PluginsState) AskRollback() {
	if s == nil {
		return
	}
	if _, ok := s.currentKey(); !ok || s.service == nil {
		return
	}
	plugin, ok := s.Selected()
	if !ok || !plugin.HasPrevious {
		return
	}
	s.confirm = pluginConfirmRollback
	s.review = nil
	s.updatePlan = nil
}

// ConfirmRollback restores the retained previous version. A required
// permission the user has not granted keeps it inactive until consent.
func (s *PluginsState) ConfirmRollback() {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	s.confirm = pluginConfirmNone
	if err := s.service.Rollback(key, nil); err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		s.refresh()
		return
	}
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.updatePlan = nil
	s.registryLoaded = false
	s.refresh()
}

// BeginEnable opens the consent screen for the selected package.
func (s *PluginsState) BeginEnable() {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	review, err := s.service.Review(key)
	if err != nil {
		s.problem = PluginProblemEnable
		s.problemDetail = err.Error()
		return
	}
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.review = &review
	s.decisions = make(map[domainplugin.Permission]bool)
	s.remember = make(map[pluginRememberKey]bool)
	s.confirm = pluginConfirmNone
}

// EnableBuiltin activates a compiled-in first-party plugin directly. Its code
// ships with the app, so there is no install-source consent screen and no
// permission decision to submit; the manager grants the declared permissions
// as part of activation.
func (s *PluginsState) EnableBuiltin() {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	if err := s.service.Enable(key, nil); err != nil {
		s.problem = PluginProblemEnable
		s.problemDetail = err.Error()
		s.refresh()
		return
	}
	s.review = nil
	s.decisions = make(map[domainplugin.Permission]bool)
	s.remember = make(map[pluginRememberKey]bool)
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.refresh()
}

// CancelReview closes the consent screen without enabling.
func (s *PluginsState) CancelReview() {
	if s == nil {
		return
	}
	s.review = nil
	s.decisions = make(map[domainplugin.Permission]bool)
	s.remember = make(map[pluginRememberKey]bool)
}

// ConfirmEnable submits the reviewed decisions. A denied required permission
// leaves the plugin inactive with the manager's readable reason, which the
// detail shows after the refresh.
func (s *PluginsState) ConfirmEnable() {
	if s == nil || s.review == nil {
		return
	}
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	decisions := make([]domainplugin.PermissionDecision, 0,
		len(s.review.Required)+len(s.review.Optional))
	for _, row := range s.review.Required {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: row.Permission, Scope: row.Scope, Allow: s.Decision(row.Permission),
		})
	}
	for _, row := range s.review.Optional {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: row.Permission, Scope: row.Scope,
			Allow: s.Decision(row.Permission), Remember: s.RememberOptional(row.Permission),
		})
	}
	if err := s.service.Enable(key, decisions); err != nil {
		s.problem = PluginProblemEnable
		s.problemDetail = err.Error()
		s.refresh()
		return
	}
	s.review = nil
	s.decisions = make(map[domainplugin.Permission]bool)
	s.remember = make(map[pluginRememberKey]bool)
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.refresh()
}

// Disable unloads the selected package. Its grants are cleared, so enabling it
// again asks for consent.
func (s *PluginsState) Disable() {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	if err := s.service.Disable(key); err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		s.refresh()
		return
	}
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.confirm = pluginConfirmNone
	s.refresh()
}

// Revoke removes one grant immediately. Revoking a required permission leaves
// the plugin disabled; an optional one leaves it active with typed failures.
func (s *PluginsState) Revoke(permission domainplugin.Permission) {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	if err := s.service.Revoke(key, permission); err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		s.refresh()
		return
	}
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.refresh()
}

// AskUninstall opens the uninstall confirmation. Data is kept; purging is a
// separate action with its own confirmation.
func (s *PluginsState) AskUninstall() {
	if s == nil {
		return
	}
	if _, ok := s.currentKey(); !ok {
		return
	}
	s.confirm = pluginConfirmUninstall
	s.review = nil
}

// AskPurge opens the separate purge-data confirmation. The package stays
// installed while its data directory is deleted.
func (s *PluginsState) AskPurge() {
	if s == nil {
		return
	}
	if _, ok := s.currentKey(); !ok {
		return
	}
	s.confirm = pluginConfirmPurge
	s.review = nil
}

// CancelConfirm closes the open confirmation.
func (s *PluginsState) CancelConfirm() {
	if s == nil {
		return
	}
	s.confirm = pluginConfirmNone
}

// ConfirmUninstall removes the package and keeps its data.
func (s *PluginsState) ConfirmUninstall() {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	s.confirm = pluginConfirmNone
	if err := s.service.Uninstall(key, false); err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		s.refresh()
		return
	}
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.selected = ""
	s.refresh()
}

// ConfirmPurge deletes the selected package's data and keeps it installed.
func (s *PluginsState) ConfirmPurge() {
	key, ok := s.currentKey()
	if !ok || s.service == nil {
		return
	}
	s.confirm = pluginConfirmNone
	if err := s.service.PurgeData(key); err != nil {
		s.problem = PluginProblemAction
		s.problemDetail = err.Error()
		s.refresh()
		return
	}
	s.problem = PluginProblemNone
	s.problemDetail = ""
	s.refresh()
}

// pluginSourceText localizes the install source. An unknown value is shown
// verbatim rather than mislabelled.
func pluginSourceText(source domainplugin.Source, m locales.Messages) string {
	switch source {
	case domainplugin.SourceLocalFile:
		return m.SettingsPluginsSourceLocalFile()
	case domainplugin.SourceRegistry:
		return m.SettingsPluginsSourceRegistry()
	case domainplugin.SourceDevelopment:
		return m.SettingsPluginsSourceDevelopment()
	case domainplugin.SourceBuiltIn:
		return m.SettingsPluginsSourceBuiltIn()
	default:
		return string(source)
	}
}

// pluginStatusText localizes the manager's status enum.
func pluginStatusText(status appplugin.PluginStatus, m locales.Messages) string {
	switch status {
	case appplugin.StatusDisabled:
		return m.SettingsPluginsStatusDisabled()
	case appplugin.StatusActive:
		return m.SettingsPluginsStatusActive()
	case appplugin.StatusFailed:
		return m.SettingsPluginsStatusFailed()
	case appplugin.StatusPending:
		return m.SettingsPluginsStatusPending()
	case appplugin.StatusDevelopment:
		return m.SettingsPluginsStatusDevelopment()
	default:
		return string(status)
	}
}

// pluginScopeText renders a structured scope as stable field=value parts. The
// field names are part of the host model, not user content, so they stay
// unlocalized; a zero scope is the localized no-resource-scope label.
func pluginScopeText(scope domainplugin.Scope, m locales.Messages) string {
	if scope.IsZero() {
		return m.SettingsPluginsScopeNone()
	}
	var parts []string
	add := func(name, value string) {
		if value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	add("host", scope.Host)
	add("session", scope.Session)
	add("field", scope.Field)
	add("event", scope.Event)
	add("path", scope.Path)
	add("key", scope.Key)
	add("provider", scope.Provider)
	add("access", scope.Access)
	if len(scope.Ports) > 0 {
		ports := make([]string, 0, len(scope.Ports))
		for _, port := range scope.Ports {
			ports = append(ports, strconv.Itoa(port))
		}
		add("ports", strings.Join(ports, ","))
	}
	add("protocol", scope.Protocol)
	add("tls", scope.TLS)
	add("service", scope.Service)
	add("version", scope.Version)
	return strings.Join(parts, " ")
}

// pluginReasonText localizes the stable denial and revocation codes and falls
// back to the operational activation error for anything else.
func pluginReasonText(reason string, m locales.Messages) string {
	if code, permission, ok := domainplugin.ParsePermissionReason(reason); ok {
		switch code {
		case domainplugin.ReasonPermissionDenied:
			return m.SettingsPluginsReasonPermissionDenied(string(permission))
		case domainplugin.ReasonPermissionRevoked:
			return m.SettingsPluginsReasonPermissionRevoked(string(permission))
		}
	}
	return m.SettingsPluginsReason(reason)
}

func pluginProblemText(problem PluginProblem, m locales.Messages) string {
	switch problem {
	case PluginProblemInstall:
		return m.SettingsPluginsProblemInstall()
	case PluginProblemEnable:
		return m.SettingsPluginsProblemEnable()
	case PluginProblemAction:
		return m.SettingsPluginsProblemAction()
	default:
		return ""
	}
}

// pluginsArea renders the installed packages, their consent state, and the
// actions for the selected entry.
func pluginsArea(c *ui.Context, state *PluginsState, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil || state.service == nil {
			ui.Text(c, m.SettingsPluginsEmpty()).FontSize(12).TextColor(t.TextMuted)
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, m.SettingsPluginsInstallFromFile()).Clicked() {
				state.Install()
				c.Invalidate()
			}
			ui.Text(c, m.SettingsPluginsDescription()).FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		})
		if problem := pluginProblemText(state.Problem(), m); problem != "" {
			ui.Text(c, problem).FontSize(12).TextColor(t.Danger)
			if detail := strings.TrimSpace(state.ProblemDetail()); detail != "" {
				ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).MinWidth(0)
			}
		}
		pluginSourceChangePanel(c, state, m, t)
		pluginRegistry(c, state, m, t)

		plugins := state.Plugins()
		if len(plugins) == 0 {
			ui.Text(c, m.SettingsPluginsEmpty()).FontSize(12).TextColor(t.TextMuted)
			return
		}
		for _, plugin := range plugins {
			plugin := plugin
			row := ui.ButtonBase(c).Key("settings-plugin-"+plugin.Key).Label(plugin.Key).
				FillWidth().MinWidth(0).Padding(8, 12).Gap(8).Radius(6).Justify(ui.Start).
				AlignItems(ui.Center)
			switch {
			case plugin.Key == state.selected:
				row.Background(t.Accent.Alpha(0.15))
			case row.Hovered() || row.Pressed():
				row.Background(t.Text.Alpha(0.05))
			}
			row.Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, plugin.Name).FontSize(14).FontWeight(500).MinWidth(0)
						if plugin.Source == domainplugin.SourceBuiltIn {
							ui.Badge(c, m.SettingsPluginsSourceBuiltIn())
						}
					})
					ui.Text(c, m.SettingsPluginsVersion(plugin.Version)+" · "+
						pluginSourceText(plugin.Source, m)+" · "+
						pluginStatusText(plugin.Status, m)).
						FontSize(11).TextColor(t.TextMuted).MinWidth(0)
				})
			})
			if row.Clicked() {
				state.Select(plugin.Key)
				c.Invalidate()
			}
		}

		selected, ok := state.Selected()
		if !ok {
			ui.Text(c, m.SettingsPluginsSelectHint()).FontSize(11).TextColor(t.TextMuted)
			return
		}
		pluginDetail(c, state, selected, m, t)
	})
}

// pluginRegistry renders the static registry browser: a client-side search
// field, the auto-update preference, and one row per result with its selected
// channel and installed state.
func pluginRegistry(c *ui.Context, state *PluginsState, m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Padding(12).Radius(6).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Text(c, m.SettingsPluginsRegistryTitle()).FontSize(13).FontWeight(600)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			query := state.RegistryQuery()
			if ui.TextInput(c, &query).Width(280).
				Placeholder(m.SettingsPluginsRegistrySearch()).Changed() {
				state.SetRegistryQuery(query)
				state.SearchRegistry()
				c.Invalidate()
			}
			automatic := state.AutomaticUpdates()
			if ui.Checkbox(c, &automatic, m.SettingsPluginsAutomaticUpdates()).Changed() {
				state.SetAutomaticUpdates(automatic)
				c.Invalidate()
			}
		})
		results := state.RegistryResults()
		if len(results) == 0 {
			ui.Text(c, m.SettingsPluginsRegistryEmpty()).FontSize(11).TextColor(t.TextMuted).MinWidth(0)
			return
		}
		for _, result := range results {
			result := result
			ui.Column(c).FillWidth().MinWidth(0).Gap(2).Padding(8).Radius(4).
				Background(t.Background).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, result.Name).FontSize(13).FontWeight(500).MinWidth(0)
						version := result.LatestVersion
						if version == "" {
							version = "-"
						}
						ui.Text(c, result.ID+" · "+m.SettingsPluginsVersion(version)+" · "+
							pluginChannelText(state.RegistryChannel(result.ID), m)).
							FontSize(11).TextColor(t.TextMuted).MinWidth(0)
					})
					beta := state.RegistryChannel(result.ID) == domainplugin.ChannelBeta
					if ui.Checkbox(c, &beta, m.SettingsPluginsUseBeta()).Changed() {
						channel := domainplugin.ChannelStable
						if beta {
							channel = domainplugin.ChannelBeta
						}
						state.SetRegistryChannel(result.ID, channel)
						c.Invalidate()
					}
					if result.Installed {
						ui.Text(c, m.SettingsPluginsRegistryInstalled(result.InstalledVersion)).
							FontSize(11).TextColor(t.TextMuted)
					} else if ui.PrimaryButton(c, m.SettingsPluginsRegistryInstall()).Clicked() {
						state.InstallRegistry(result.ID, result.LatestVersion)
						c.Invalidate()
					}
				})
				if result.Description != "" {
					ui.Text(c, result.Description).FontSize(11).TextColor(t.TextMuted).MinWidth(0)
				}
				pluginWarnings(c, result.Warnings, m, t)
			})
		}
	})
}

func pluginChannelText(channel domainplugin.Channel, m locales.Messages) string {
	if channel == domainplugin.ChannelBeta {
		return m.SettingsPluginsChannelBeta()
	}
	return m.SettingsPluginsChannelStable()
}

// pluginWarnings renders release notices. A yanked or revoked version warns
// without ever uninstalling or disabling the running plugin.
func pluginWarnings(c *ui.Context, warnings []domainplugin.ReleaseWarning,
	m locales.Messages, t *ui.Theme) {
	for _, warning := range warnings {
		color := t.Warning
		if warning.Kind == domainplugin.ReleaseWarningYanked ||
			warning.Kind == domainplugin.ReleaseWarningRevoked {
			color = t.Danger
		}
		ui.Text(c, pluginWarningText(warning, m)).FontSize(11).TextColor(color).MinWidth(0)
	}
}

func pluginWarningText(warning domainplugin.ReleaseWarning, m locales.Messages) string {
	switch warning.Kind {
	case domainplugin.ReleaseWarningYanked:
		return m.SettingsPluginsWarningYanked()
	case domainplugin.ReleaseWarningDeprecated:
		return m.SettingsPluginsWarningDeprecated(warning.Detail)
	case domainplugin.ReleaseWarningRevoked:
		return m.SettingsPluginsWarningRevoked(warning.Detail)
	default:
		return string(warning.Kind)
	}
}

// pluginSourceChangePanel is the explicit trust-on-first-use confirmation for
// a plugin id whose install source changed.
func pluginSourceChangePanel(c *ui.Context, state *PluginsState,
	m locales.Messages, t *ui.Theme) {
	pending, ok := state.SourceChange()
	if !ok {
		return
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Padding(12).Radius(6).
		Background(t.Surface).Border(1, t.Danger).Children(func() {
		ui.Text(c, m.SettingsPluginsSourceChangeTitle(pending.changed)).FontSize(14).FontWeight(600)
		ui.Text(c, m.SettingsPluginsSourceChangeBody(pending.previous, pending.current)).
			FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, m.SettingsPluginsConfirmSourceChange()).Clicked() {
				state.ConfirmSourceChange()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsPluginsCancel()).Clicked() {
				state.CancelSourceChange()
				c.Invalidate()
			}
		})
	})
}

// pluginUpdateReview shows the permission diff and requires an explicit
// decision for each newly required permission before applying.
func pluginUpdateReview(c *ui.Context, state *PluginsState, m locales.Messages, t *ui.Theme) {
	plan, ok := state.UpdatePlan()
	if !ok {
		return
	}
	name := plan.ID
	if selected, ok := state.Selected(); ok {
		name = selected.Name
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Padding(12).Radius(6).
		Background(t.Surface).Border(1, t.Warning).Children(func() {
		ui.Text(c, m.SettingsPluginsUpdateTitle(name, plan.To)).
			FontSize(14).FontWeight(600)
		ui.Text(c, m.SettingsPluginsUpdateBody()).FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		changes := plan.SourceChanges
		if len(changes) == 0 && plan.SourceChanged {
			changes = []appplugin.SourceChange{{
				ID: plan.ID, PinnedSource: plan.PinnedSource, NewSource: plan.RegistrySource,
			}}
		}
		for _, change := range changes {
			change := change
			ui.Text(c, m.SettingsPluginsSourceChangeTitle(change.ID)).
				FontSize(11).FontWeight(600).TextColor(t.Danger).MinWidth(0)
			ui.Text(c, m.SettingsPluginsSourceChangeBody(change.PinnedSource, change.NewSource)).
				FontSize(11).TextColor(t.Danger).MinWidth(0)
		}
		pluginWarnings(c, plan.Warnings, m, t)
		ui.Text(c, m.SettingsPluginsPermissionChanges()).FontSize(12).FontWeight(600)
		pluginUpdateDiffRows(c, state, plan, m, t)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, m.SettingsPluginsConfirmUpdate()).Clicked() {
				state.ConfirmUpdate()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsPluginsCancel()).Clicked() {
				state.CancelUpdate()
				c.Invalidate()
			}
		})
	})
}

func pluginUpdateDiffRows(c *ui.Context, state *PluginsState,
	plan appplugin.UpdatePlan, m locales.Messages, t *ui.Theme) {
	rows := 0
	for _, request := range append(append([]domainplugin.PermissionRequest(nil),
		plan.Diff.AddedRequired...), plan.Diff.AddedOptional...) {
		rows++
		request := request
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SettingsPluginsDiffAdded()+" "+string(request.Permission)).
				FontSize(11).TextColor(t.TextMuted)
			ui.Text(c, m.SettingsPluginsScope(pluginScopeText(request.Scope, m))).
				FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		})
	}
	for _, request := range append(append([]domainplugin.PermissionRequest(nil),
		plan.Diff.RemovedRequired...), plan.Diff.RemovedOptional...) {
		rows++
		ui.Text(c, m.SettingsPluginsDiffRemoved()+" "+string(request.Permission)).
			FontSize(11).TextColor(t.TextMuted).MinWidth(0)
	}
	for _, change := range plan.Diff.ScopeChanged {
		rows++
		change := change
		ui.Text(c, m.SettingsPluginsDiffScope(string(change.Permission),
			pluginScopeText(change.Before, m), pluginScopeText(change.After, m))).
			FontSize(11).TextColor(t.Warning).MinWidth(0)
	}
	if rows == 0 {
		ui.Text(c, m.SettingsPluginsNoPermissionChanges()).FontSize(11).TextColor(t.TextMuted)
	}
	for _, request := range plan.RequiresConsent {
		request := request
		allow := state.UpdateDecision(request.Permission)
		label := m.SettingsPluginsRequired() + ": " + string(request.Permission)
		if ui.Checkbox(c, &allow, label).Changed() {
			state.SetUpdateDecision(request.Permission, allow)
			c.Invalidate()
		}
		ui.Text(c, m.SettingsPluginsScope(pluginScopeText(request.Scope, m))).
			FontSize(11).TextColor(t.TextMuted).MinWidth(0)
	}
	for _, request := range plan.Diff.AddedOptional {
		request := request
		allow := state.UpdateDecision(request.Permission)
		label := m.SettingsPluginsOptional() + ": " + string(request.Permission)
		if ui.Checkbox(c, &allow, label).Changed() {
			state.SetUpdateDecision(request.Permission, allow)
			c.Invalidate()
		}
	}
}

func pluginRollbackConfirm(c *ui.Context, state *PluginsState,
	plugin appplugin.InstalledPlugin, m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Padding(12).Radius(6).
		Background(t.Surface).Border(1, t.Warning).Children(func() {
		ui.Text(c, m.SettingsPluginsRollbackTitle(plugin.Name)).FontSize(14).FontWeight(600)
		ui.Text(c, m.SettingsPluginsRollbackBody()).FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, m.SettingsPluginsConfirmRollback()).Clicked() {
				state.ConfirmRollback()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsPluginsCancel()).Clicked() {
				state.CancelConfirm()
				c.Invalidate()
			}
		})
	})
}

func pluginDetail(c *ui.Context, state *PluginsState, plugin appplugin.InstalledPlugin,
	m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Padding(12).Radius(6).
		Background(t.Background).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, plugin.Name).FontSize(16).FontWeight(600)
			if plugin.Source == domainplugin.SourceBuiltIn {
				ui.Badge(c, m.SettingsPluginsSourceBuiltIn())
			}
		})
		ui.Text(c, m.SettingsPluginsVersion(plugin.Version)).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, m.SettingsPluginsSource(pluginSourceText(plugin.Source, m))).
			FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, pluginStatusText(plugin.Status, m)).FontSize(12).Bold()
		if plugin.Development {
			ui.Text(c, m.SettingsPluginsDevelopmentNote()).FontSize(11).TextColor(t.TextMuted)
		}
		if reason := strings.TrimSpace(plugin.Reason); reason != "" {
			ui.Text(c, pluginReasonText(reason, m)).FontSize(11).TextColor(t.Danger).MinWidth(0)
		}
		pluginWarnings(c, plugin.Warnings, m, t)
		pluginPermissions(c, state, plugin, m, t)
		pluginSettings(c, state, m, t)
		pluginDiagnosticsTree(c, state, m, t)
		pluginDiagnostics(c, state, m, t)
		// A compiled-in plugin is never installed, updated, rolled back,
		// uninstalled, or purged at runtime. It still enables and disables like
		// any other plugin, without a consent review.
		if plugin.Source == domainplugin.SourceBuiltIn {
			pluginActions(c, state, plugin, m, t)
			return
		}
		if plugin.Development {
			return
		}
		switch state.Confirm() {
		case pluginConfirmUninstall:
			pluginUninstallConfirm(c, state, plugin, m, t)
		case pluginConfirmPurge:
			pluginPurgeConfirm(c, state, plugin, m, t)
		case pluginConfirmRollback:
			pluginRollbackConfirm(c, state, plugin, m, t)
		default:
			if _, reviewing := state.Review(); reviewing {
				pluginConsent(c, state, m, t)
				return
			}
			if _, planning := state.UpdatePlan(); planning {
				pluginUpdateReview(c, state, m, t)
				return
			}
			pluginActions(c, state, plugin, m, t)
		}
	})
}

func pluginPermissions(c *ui.Context, state *PluginsState, plugin appplugin.InstalledPlugin,
	m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SettingsPluginsPermissions()).FontSize(13).FontWeight(600)
	if len(plugin.Permissions) == 0 {
		ui.Text(c, m.SettingsPluginsNoPermissions()).FontSize(11).TextColor(t.TextMuted)
		return
	}
	for _, status := range plugin.Permissions {
		status := status
		granted := status.Granted && !status.Pending
		ui.Column(c).FillWidth().MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, string(status.Permission)).FontSize(12).FontWeight(500).MinWidth(0)
				kind := m.SettingsPluginsOptional()
				if status.Required {
					kind = m.SettingsPluginsRequired()
				}
				ui.Text(c, kind).FontSize(11).TextColor(t.TextMuted)
				if status.Pending {
					ui.Text(c, m.SettingsPluginsScopePending()).FontSize(11).TextColor(t.TextMuted)
				} else if granted {
					ui.Text(c, m.SettingsPluginsGranted()).FontSize(11).TextColor(t.TextMuted)
				}
				if granted && plugin.Enabled && !plugin.Development &&
					plugin.Source != domainplugin.SourceBuiltIn {
					if ui.Button(c, m.SettingsPluginsRevoke()).Clicked() {
						state.Revoke(status.Permission)
						c.Invalidate()
					}
				}
			})
			ui.Text(c, m.SettingsPluginsScope(pluginScopeText(status.Scope, m))).
				FontSize(11).TextColor(t.TextMuted).MinWidth(0)
			if status.Scope.Kind == domainplugin.ScopeFiles && status.Scope.Path != "" {
				ui.Text(c, m.SettingsPluginsScopeSubtree()).
					FontSize(11).TextColor(t.TextMuted).MinWidth(0)
			}
			// An optional permission the user did not allow at enable time is
			// the first-use prompt: the plugin may ask for it while running,
			// and the user can allow or deny it here, optionally remembering
			// the choice for the declared scope.
			if !status.Required && !granted && plugin.Enabled && !plugin.Development {
				pluginFirstUse(c, state, status, m, t)
			}
		})
	}
}

// pluginSettings renders the manifest-declared settings page. It shows the
// effective values, defaults, volatile markers, and per-field validation
// errors, and saves only through the manager's schema validation.
func pluginSettings(c *ui.Context, state *PluginsState, m locales.Messages, t *ui.Theme) {
	view, ok := state.SettingsView()
	if !ok {
		if problem := strings.TrimSpace(state.SettingsProblem()); problem != "" {
			ui.Text(c, m.SettingsPluginsSettingsTitle()).FontSize(13).FontWeight(600)
			ui.Text(c, problem).FontSize(11).TextColor(t.Danger).MinWidth(0)
		}
		return
	}
	if len(view.Fields) == 0 && view.Valid {
		return
	}
	ui.Text(c, m.SettingsPluginsSettingsTitle()).FontSize(13).FontWeight(600)
	if !view.Valid {
		ui.Text(c, m.SettingsPluginsSettingsInvalid()).FontSize(11).TextColor(t.Danger).MinWidth(0)
	}
	for _, field := range view.Fields {
		pluginSettingRow(c, state, field, m, t)
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.PrimaryButton(c, m.SettingsPluginsSettingsSave()).Clicked() {
			state.SaveSettings()
			c.Invalidate()
		}
		if ui.Button(c, m.SettingsPluginsSettingsReset()).Clicked() {
			state.ResetSettings()
			c.Invalidate()
		}
	})
	if problem := strings.TrimSpace(state.SettingsProblem()); problem != "" {
		ui.Text(c, m.SettingsPluginsSettingsProblem()).FontSize(11).TextColor(t.Danger)
		ui.Text(c, problem).FontSize(11).TextColor(t.TextMuted).MinWidth(0)
	}
}

// pluginDiagnosticsTree renders the group/fiber tree from the container-wide
// diagnostics report. Each group appears once at its nesting depth, including
// groups with no instances, followed by the instances loaded directly in it.
// The tree is built from the flat registry so a group with several instances
// cannot appear more than once.
func pluginDiagnosticsTree(c *ui.Context, state *PluginsState,
	m locales.Messages, t *ui.Theme) {
	report, ok := state.DiagnosticsTree()
	if !ok || (len(report.Groups) == 0 && len(report.Fibers) == 0) {
		return
	}
	fibers := make(map[string][]appplugin.PluginDiagnostics, len(report.Groups))
	for _, fiber := range report.Fibers {
		fibers[fiber.Group] = append(fibers[fiber.Group], fiber)
	}
	known := make(map[string]bool, len(report.Groups)+len(report.Fibers))
	for _, group := range report.Groups {
		known[group.Name] = true
	}
	groups := append([]appplugin.GroupDiagnostics(nil), report.Groups...)
	children := make(map[string][]string, len(report.Groups))
	roots := make([]string, 0, len(report.Groups))
	for _, group := range report.Groups {
		if group.Parent == "" || !known[group.Parent] {
			roots = append(roots, group.Name)
			continue
		}
		children[group.Parent] = append(children[group.Parent], group.Name)
	}
	// Defensive: a fiber whose group is missing from the registry still gets a
	// node instead of silently vanishing from the view.
	for _, fiber := range report.Fibers {
		if known[fiber.Group] {
			continue
		}
		known[fiber.Group] = true
		roots = append(roots, fiber.Group)
		groups = append(groups, appplugin.GroupDiagnostics{Name: fiber.Group})
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(2).Padding(8).Radius(4).
		Background(t.Surface).Children(func() {
		ui.Text(c, m.SettingsPluginsDiagnosticsFiberTree()).FontSize(12).FontWeight(600)
		seen := make(map[string]bool, len(groups))
		var render func(name string, depth int)
		render = func(name string, depth int) {
			if seen[name] {
				return
			}
			seen[name] = true
			prefix := strings.Repeat("• ", depth)
			ui.Text(c, prefix+name).FontSize(11).FontWeight(600).MinWidth(0)
			for _, fiber := range fibers[name] {
				ui.Text(c, prefix+"• "+string(fiber.ID)+" "+string(fiber.State)).
					FontSize(10).TextColor(t.TextMuted).MinWidth(0)
			}
			for _, child := range children[name] {
				render(child, depth+1)
			}
		}
		for _, root := range roots {
			render(root, 0)
		}
		// A cycle or orphan cannot hide a registered group from the view.
		for _, group := range groups {
			render(group.Name, 0)
		}
	})
}

// pluginDiagnostics renders the host-owned lifecycle and runtime snapshot:
// state, missing injections, configuration errors, grants, effects, memory,
// call timings, fault history, and the bounded log ring. It never asks the
// guest for any of this data.
func pluginDiagnostics(c *ui.Context, state *PluginsState, m locales.Messages, t *ui.Theme) {
	diagnostics, ok := state.DiagnosticsView()
	if !ok {
		if problem := strings.TrimSpace(state.DiagnosticsProblem()); problem != "" {
			ui.Text(c, m.SettingsPluginsDiagnosticsTitle()).FontSize(13).FontWeight(600)
			ui.Text(c, problem).FontSize(11).TextColor(t.Danger).MinWidth(0)
		}
		return
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(3).Padding(8).Radius(4).
		Background(t.Surface).Children(func() {
		ui.Text(c, m.SettingsPluginsDiagnosticsTitle()).FontSize(13).FontWeight(600)
		ui.Text(c, m.SettingsPluginsDiagnosticsLifecycle(string(diagnostics.State))).
			FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		if diagnostics.Reason != "" {
			ui.Text(c, m.SettingsPluginsDiagnosticsReason(diagnostics.Reason)).
				FontSize(11).TextColor(t.Danger).MinWidth(0)
		}
		if len(diagnostics.MissingInjections) > 0 {
			ui.Text(c, m.SettingsPluginsDiagnosticsMissing(
				strings.Join(diagnostics.MissingInjections, ", "))).
				FontSize(11).TextColor(t.Warning).MinWidth(0)
		}
		if len(diagnostics.ConfigErrors) > 0 {
			errors := make([]string, 0, len(diagnostics.ConfigErrors))
			for _, item := range diagnostics.ConfigErrors {
				errors = append(errors, item.Error())
			}
			ui.Text(c, m.SettingsPluginsDiagnosticsConfig(strings.Join(errors, "; "))).
				FontSize(11).TextColor(t.Danger).MinWidth(0)
		}
		if len(diagnostics.Grants) == 0 {
			ui.Text(c, m.SettingsPluginsDiagnosticsNoGrants()).
				FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		} else {
			grants := make([]string, 0, len(diagnostics.Grants))
			for _, grant := range diagnostics.Grants {
				text := string(grant.Permission)
				if !grant.Scope.IsZero() {
					text += " (" + pluginScopeText(grant.Scope, m) + ")"
				}
				grants = append(grants, text)
			}
			ui.Text(c, m.SettingsPluginsDiagnosticsGrants(strings.Join(grants, ", "))).
				FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		}
		ui.Text(c, m.SettingsPluginsDiagnosticsEffects(strconv.Itoa(diagnostics.Effects))).
			FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		ui.Text(c, m.SettingsPluginsDiagnosticsMemory(
			diagnosticBytes(diagnostics.MemoryBytes), diagnosticBytes(diagnostics.MemoryMax))).
			FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		ui.Text(c, m.SettingsPluginsDiagnosticsRestarts(
			strconv.Itoa(diagnostics.ManualRestarts),
			strconv.Itoa(diagnostics.AutomaticRestarts))).
			FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		if len(diagnostics.Timings) > 0 {
			ui.Text(c, m.SettingsPluginsDiagnosticsTimings()).FontSize(12).FontWeight(600)
			for _, timing := range diagnostics.Timings {
				ui.Text(c, m.SettingsPluginsDiagnosticsTiming(timing.Operation,
					fmt.Sprint(timing.Calls), timing.Last.String(), timing.Max.String())).
					FontSize(11).TextColor(t.TextMuted).MinWidth(0)
			}
		}
		if len(diagnostics.Faults) == 0 {
			ui.Text(c, m.SettingsPluginsDiagnosticsFaults()).FontSize(12).FontWeight(600)
			ui.Text(c, m.SettingsPluginsDiagnosticsNoFaults()).
				FontSize(11).TextColor(t.TextMuted)
		} else {
			ui.Text(c, m.SettingsPluginsDiagnosticsFaults()).FontSize(12).FontWeight(600)
			for _, fault := range diagnostics.Faults {
				ui.Text(c, m.SettingsPluginsDiagnosticsFault(fault.Time.Format("15:04:05"),
					fault.Operation, fault.Reason)).
					FontSize(11).TextColor(t.Danger).MinWidth(0)
			}
		}
		ui.Text(c, m.SettingsPluginsDiagnosticsLogs()).FontSize(12).FontWeight(600)
		if len(diagnostics.Logs) == 0 {
			ui.Text(c, m.SettingsPluginsDiagnosticsNoLogs()).
				FontSize(11).TextColor(t.TextMuted)
		} else {
			// The host ring is bounded at 200 lines; the panel shows the most
			// recent slice so a chatty plugin cannot push the actions away.
			logs := diagnostics.Logs
			if len(logs) > 20 {
				logs = logs[len(logs)-20:]
			}
			for _, line := range logs {
				ui.Text(c, line).FontSize(10).TextColor(t.TextMuted).MinWidth(0)
			}
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, m.SettingsPluginsDiagnosticsRestart()).Clicked() {
				state.RestartPlugin()
				c.Invalidate()
			}
		})
	})
}

// diagnosticBytes renders a memory value for the diagnostics panel.
func diagnosticBytes(bytes int64) string {
	if bytes <= 0 {
		return "0 B"
	}
	const mib = 1 << 20
	if bytes >= mib {
		return fmt.Sprintf("%.1f MiB", float64(bytes)/mib)
	}
	return fmt.Sprintf("%.1f KiB", float64(bytes)/1024)
}

func pluginSettingRow(c *ui.Context, state *PluginsState,
	field appplugin.SettingsFieldView, m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(2).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			label := field.Label
			if label == "" {
				label = field.Key
			}
			ui.Text(c, label).FontSize(12).FontWeight(500).MinWidth(0)
			if field.Required {
				ui.Text(c, m.SettingsPluginsRequired()).FontSize(11).TextColor(t.TextMuted)
			}
			if field.Volatile {
				ui.Text(c, m.SettingsPluginsSettingsVolatile()).FontSize(11).TextColor(t.TextMuted)
			}
		})
		if field.Description != "" {
			ui.Text(c, field.Description).FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		}
		switch field.Type {
		case domainplugin.SettingTypeBool:
			value, _ := state.SettingValue(field.Key).(bool)
			if ui.Checkbox(c, &value, "").Changed() {
				state.SetSettingValue(field.Key, value)
				c.Invalidate()
			}
		case domainplugin.SettingTypeString:
			value, _ := state.SettingValue(field.Key).(string)
			if len(field.Options) > 0 {
				if ui.Select(c, &value, field.Options).Changed() {
					state.SetSettingValue(field.Key, value)
					c.Invalidate()
				}
			} else if ui.TextInput(c, &value).Width(280).Changed() {
				state.SetSettingValue(field.Key, value)
				c.Invalidate()
			}
		case domainplugin.SettingTypeInt, domainplugin.SettingTypeFloat:
			value := settingNumber(state.SettingValue(field.Key))
			low, high, step := -1e12, 1e12, 0.01
			if field.Min != nil {
				low = *field.Min
			}
			if field.Max != nil {
				high = *field.Max
			}
			if field.Type == domainplugin.SettingTypeInt {
				step = 1
			}
			if ui.NumberInput(c, &value, low, high, step).Width(140).Changed() {
				if field.Type == domainplugin.SettingTypeInt {
					state.SetSettingValue(field.Key, int64(math.Round(value)))
				} else {
					state.SetSettingValue(field.Key, value)
				}
				c.Invalidate()
			}
		}
		if field.Default != nil {
			ui.Text(c, m.SettingsPluginsSettingsDefault(fmt.Sprint(field.Default))).
				FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		}
		if field.Error != "" {
			ui.Text(c, field.Error).FontSize(11).TextColor(t.Danger).MinWidth(0)
		}
	})
}

func settingNumber(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case float32:
		return float64(number)
	case int64:
		return float64(number)
	case int:
		return float64(number)
	default:
		return 0
	}
}

func pluginFirstUse(c *ui.Context, state *PluginsState,
	status appplugin.PermissionStatus, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SettingsPluginsFirstUseBody()).FontSize(11).TextColor(t.TextMuted).MinWidth(0)
	if !status.Scope.IsZero() {
		remember := state.RememberOptional(status.Permission)
		if ui.Checkbox(c, &remember, m.SettingsPluginsRememberScope()).Changed() {
			state.SetRememberOptional(status.Permission, remember)
			c.Invalidate()
		}
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.PrimaryButton(c, m.SettingsPluginsGrant()).Clicked() {
			state.RequestOptional(status.Permission, true)
			c.Invalidate()
		}
		if ui.Button(c, m.SettingsPluginsDeny()).Clicked() {
			state.RequestOptional(status.Permission, false)
			c.Invalidate()
		}
	})
}

func pluginActions(c *ui.Context, state *PluginsState, plugin appplugin.InstalledPlugin,
	m locales.Messages, t *ui.Theme) {
	builtin := plugin.Source == domainplugin.SourceBuiltIn
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if plugin.Enabled {
			if ui.Button(c, m.SettingsPluginsDisable()).Clicked() {
				state.Disable()
				c.Invalidate()
			}
		} else if builtin {
			if ui.PrimaryButton(c, m.SettingsPluginsEnable()).Clicked() {
				state.EnableBuiltin()
				c.Invalidate()
			}
		} else {
			if ui.PrimaryButton(c, m.SettingsPluginsEnable()).Clicked() {
				state.BeginEnable()
				c.Invalidate()
			}
		}
		if builtin {
			return
		}
		if plugin.Source == domainplugin.SourceRegistry && !plugin.Development {
			if ui.Button(c, m.SettingsPluginsUpdate()).Clicked() {
				state.BeginUpdate()
				c.Invalidate()
			}
		}
		if plugin.HasPrevious && !plugin.Development {
			if ui.Button(c, m.SettingsPluginsRollback()).Clicked() {
				state.AskRollback()
				c.Invalidate()
			}
		}
		if ui.Button(c, m.SettingsPluginsUninstall()).Clicked() {
			state.AskUninstall()
			c.Invalidate()
		}
		if ui.Button(c, m.SettingsPluginsPurgeData()).Clicked() {
			state.AskPurge()
			c.Invalidate()
		}
	})
}

// pluginConsent renders required and optional rows. Nothing is preselected:
// the user must grant each permission explicitly before the plugin can
// activate.
func pluginConsent(c *ui.Context, state *PluginsState, m locales.Messages, t *ui.Theme) {
	review, ok := state.Review()
	if !ok {
		return
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Padding(12).Radius(6).
		Background(t.Surface).Border(1, t.Warning).Children(func() {
		ui.Text(c, m.SettingsPluginsConsentTitle(review.Name)).FontSize(14).FontWeight(600)
		ui.Text(c, m.SettingsPluginsConsentBody()).FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, m.SettingsPluginsSource(pluginSourceText(review.Source, m))).
			FontSize(11).TextColor(t.TextMuted)
		pluginConsentRows(c, state, review.Required, m, t)
		pluginConsentRows(c, state, review.Optional, m, t)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, m.SettingsPluginsConfirmEnable()).Clicked() {
				state.ConfirmEnable()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsPluginsCancel()).Clicked() {
				state.CancelReview()
				c.Invalidate()
			}
		})
	})
}

func pluginConsentRows(c *ui.Context, state *PluginsState, rows []domainplugin.PermissionConsent,
	m locales.Messages, t *ui.Theme) {
	for _, row := range rows {
		row := row
		allow := state.Decision(row.Permission)
		kind := m.SettingsPluginsOptional()
		if row.Required {
			kind = m.SettingsPluginsRequired()
		}
		label := string(row.Permission) + " (" + kind + ")"
		if ui.Checkbox(c, &allow, label).Changed() {
			state.SetDecision(row.Permission, allow)
			c.Invalidate()
		}
		if !row.Required && !row.Scope.IsZero() {
			remember := state.RememberOptional(row.Permission)
			if ui.Checkbox(c, &remember, m.SettingsPluginsRememberScope()).Changed() {
				state.SetRememberOptional(row.Permission, remember)
				c.Invalidate()
			}
		}
		ui.Text(c, m.SettingsPluginsScope(pluginScopeText(row.Scope, m))).
			FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		if row.Scope.Kind == domainplugin.ScopeFiles && row.Scope.Path != "" {
			ui.Text(c, m.SettingsPluginsScopeSubtree()).
				FontSize(11).TextColor(t.TextMuted).MinWidth(0)
		}
	}
}

func pluginUninstallConfirm(c *ui.Context, state *PluginsState, plugin appplugin.InstalledPlugin,
	m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Padding(12).Radius(6).
		Background(t.Surface).Border(1, t.Danger).Children(func() {
		ui.Text(c, m.SettingsPluginsUninstallTitle(plugin.Name)).FontSize(14).FontWeight(600)
		ui.Text(c, m.SettingsPluginsUninstallBody()).FontSize(11).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, m.SettingsPluginsConfirmUninstall()).Clicked() {
				state.ConfirmUninstall()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsPluginsCancel()).Clicked() {
				state.CancelConfirm()
				c.Invalidate()
			}
		})
	})
}

func pluginPurgeConfirm(c *ui.Context, state *PluginsState, plugin appplugin.InstalledPlugin,
	m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(8).Padding(12).Radius(6).
		Background(t.Surface).Border(1, t.Danger).Children(func() {
		ui.Text(c, m.SettingsPluginsPurgeTitle(plugin.Name)).FontSize(14).FontWeight(600)
		ui.Text(c, m.SettingsPluginsPurgeBody()).FontSize(11).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, m.SettingsPluginsConfirmPurgeData()).Clicked() {
				state.ConfirmPurge()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsPluginsCancel()).Clicked() {
				state.CancelConfirm()
				c.Invalidate()
			}
		})
	})
}
