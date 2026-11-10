package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo"

	adapterpluginregistry "github.com/mslxl/natcatty/adapters/pluginregistry"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/settings"
)

// appPluginService adapts the plugin manager to the Settings surface and owns
// the native file dialog. The manager speaks plugin ids for mutating actions;
// the Settings list uses keys (source/id), so the adapter routes them back.
type appPluginService struct {
	manager *appplugin.Manager
	locale  func() string
}

var _ settings.PluginService = (*appPluginService)(nil)

func newAppPluginService(manager *appplugin.Manager, locale func() string) *appPluginService {
	return &appPluginService{manager: manager, locale: locale}
}

// openPluginRegistry returns the static registry configured by
// NATCATTY_PLUGIN_REGISTRY: an http(s) base URL or a local directory with the
// same index/plugins/packages layout. An unset value leaves registry features
// unavailable while local install and every other plugin feature keep working.
func openPluginRegistry() appplugin.Registry {
	value := strings.TrimSpace(os.Getenv("NATCATTY_PLUGIN_REGISTRY"))
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return adapterpluginregistry.OpenHTTP(value)
	}
	return adapterpluginregistry.OpenDirectory(value)
}

func (s *appPluginService) Plugins() ([]appplugin.InstalledPlugin, error) {
	return s.manager.List(context.Background())
}

// installedPluginID maps a manager key to the installed-record id. A
// development directory is not an install record and cannot be enabled,
// revoked, uninstalled, or purged through this surface.
func installedPluginID(key string) (string, bool) {
	if strings.HasPrefix(key, string(domainplugin.SourceDevelopment)+"/") {
		return "", false
	}
	for _, source := range []domainplugin.Source{
		domainplugin.SourceLocalFile, domainplugin.SourceRegistry, domainplugin.SourceBuiltIn,
	} {
		prefix := string(source) + "/"
		if strings.HasPrefix(key, prefix) {
			return strings.TrimPrefix(key, prefix), true
		}
	}
	return key, key != ""
}

func (s *appPluginService) recordID(key string) (string, error) {
	id, ok := installedPluginID(key)
	if !ok {
		return "", fmt.Errorf("plugin: development directory %s is not installed", key)
	}
	return id, nil
}

func (s *appPluginService) Review(key string) (domainplugin.ConsentReview, error) {
	id, err := s.recordID(key)
	if err != nil {
		return domainplugin.ConsentReview{}, err
	}
	return s.manager.Review(context.Background(), id)
}

func (s *appPluginService) Enable(key string, decisions []domainplugin.PermissionDecision) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	return s.manager.Enable(context.Background(), id, decisions)
}

func (s *appPluginService) Disable(key string) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	return s.manager.Disable(context.Background(), id)
}

func (s *appPluginService) Revoke(key string, permission domainplugin.Permission) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	return s.manager.Revoke(context.Background(), id, permission)
}

func (s *appPluginService) RequestOptional(key string, permission domainplugin.Permission,
	scope domainplugin.Scope, allow, remember bool) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	return s.manager.RequestOptional(context.Background(), id, permission, scope, allow, remember)
}

func (s *appPluginService) Uninstall(key string, purge bool) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	return s.manager.Uninstall(context.Background(), id, purge)
}

func (s *appPluginService) PurgeData(key string) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	return s.manager.PurgeData(context.Background(), id)
}

func (s *appPluginService) Settings(key string) (appplugin.PluginSettingsView, error) {
	id, err := s.recordID(key)
	if err != nil {
		return appplugin.PluginSettingsView{}, err
	}
	return s.manager.Settings(context.Background(), id)
}

func (s *appPluginService) SaveSettings(key string, values map[string]any) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	return s.manager.SaveSettings(context.Background(), id, values)
}

func (s *appPluginService) ResetSettings(key string) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	return s.manager.ResetSettings(context.Background(), id)
}

// Diagnostics returns the host-owned lifecycle snapshot for an installed or
// development plugin, routed by the same key the list uses.
func (s *appPluginService) Diagnostics(key string) (appplugin.PluginDiagnostics, error) {
	diagnostics, ok := s.manager.PluginDiagnostics(key)
	if !ok {
		return appplugin.PluginDiagnostics{}, fmt.Errorf("plugin: %s is not loaded", key)
	}
	return diagnostics, nil
}

// DiagnosticsReport returns the container-wide group/fiber tree for the
// settings diagnostics view.
func (s *appPluginService) DiagnosticsReport() appplugin.DiagnosticsReport {
	return s.manager.DiagnosticsReport()
}

// Restart manually reloads one plugin, granting a fresh automatic-restart
// budget after a runtime fault.
func (s *appPluginService) Restart(key string) error {
	return s.manager.Restart(context.Background(), key)
}

// InstallFromFile asks for one .natplug archive and installs it disabled. A
// cancelled dialog is not an error.
func (s *appPluginService) InstallFromFile(
	confirmed appplugin.ConfirmedSourceChanges) (string, bool, error) {
	path, err := s.pickPackage()
	if err != nil {
		return "", false, err
	}
	if path == "" {
		return "", false, nil
	}
	record, err := s.manager.InstallFileConfirm(context.Background(), path, confirmed)
	if err != nil {
		return "", false, err
	}
	return string(record.Source) + "/" + record.ID, true, nil
}

// SearchRegistry searches the static index on the client and enriches the
// rows with installed state.
func (s *appPluginService) SearchRegistry(query string) ([]appplugin.RegistrySearchResult, error) {
	return s.manager.SearchRegistry(context.Background(), query)
}

func (s *appPluginService) RegistryPreferences() (domainplugin.RegistryPreferences, error) {
	return s.manager.RegistryPreferences(context.Background())
}

func (s *appPluginService) SetChannel(key string, channel domainplugin.Channel) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	return s.manager.SetChannel(context.Background(), id, channel)
}

func (s *appPluginService) SetAutomaticUpdates(enabled bool) error {
	return s.manager.SetAutomaticUpdates(context.Background(), enabled)
}

// InstallFromRegistry installs a registry package disabled and returns its
// manager key.
func (s *appPluginService) InstallFromRegistry(id, version string,
	confirmed appplugin.ConfirmedSourceChanges) (string, bool, error) {
	record, err := s.manager.InstallFromRegistry(context.Background(), id, version,
		confirmed)
	if err != nil {
		return "", false, err
	}
	return string(record.Source) + "/" + record.ID, true, nil
}

func (s *appPluginService) PlanUpdate(key string) (appplugin.UpdatePlan, error) {
	id, err := s.recordID(key)
	if err != nil {
		return appplugin.UpdatePlan{}, err
	}
	return s.manager.PlanUpdate(context.Background(), id)
}

func (s *appPluginService) ApplyUpdate(key string, plan appplugin.UpdatePlan,
	decisions []domainplugin.PermissionDecision) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	plan.ID = id
	_, err = s.manager.ApplyUpdate(context.Background(), plan, decisions)
	return err
}

func (s *appPluginService) Rollback(key string,
	decisions []domainplugin.PermissionDecision) error {
	id, err := s.recordID(key)
	if err != nil {
		return err
	}
	_, err = s.manager.Rollback(context.Background(), id, decisions)
	return err
}

// runAutomaticPluginUpdates performs one automatic-update pass after startup
// when the persisted preference is on. It runs off the UI thread and the pass
// itself skips every update that would add consent or change a source.
func runAutomaticPluginUpdates(manager *appplugin.Manager) {
	result, err := manager.AutoUpdate(context.Background())
	if errors.Is(err, appplugin.ErrAutomaticUpdatesDisabled) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "natcatty: automatic plugin updates: %v\n", err)
	}
	if len(result.Updated) > 0 {
		fmt.Fprintf(os.Stderr, "natcatty: automatically updated plugins: %s\n",
			strings.Join(result.Updated, ", "))
	}
}

func (s *appPluginService) pickPackage() (string, error) {
	m := locales.In("en")
	if s.locale != nil {
		if tag := s.locale(); tag != "" {
			m = locales.In(tag)
		}
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title: m.SettingsPluginsInstallFromFile(),
		Filters: []mygo.FileFilter{
			{Name: m.SettingsPluginsInstallFromFile(), Extensions: []string{"natplug"}},
		},
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	return paths[0], nil
}
