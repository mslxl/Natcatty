package phoneshell

import (
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// NewSettingsService adapts the shared preferences and terminal-settings
// services to the phone Settings pages. Both services persist through the
// same versioned JSON documents the desktop uses, so a portable change is
// written to the shared schema that Vault sync carries, and a device-local
// change such as interface style or app icon stays in this phone's file.
//
// A nil service falls back to the domain defaults, which keeps a partially
// wired shell usable in tests and in a build where a settings file failed to
// open.
func NewSettingsService(preferences *apppreferences.Service, terminal *appterminalsettings.Service) SettingsService {
	return settingsService{preferences: preferences, terminal: terminal}
}

type settingsService struct {
	preferences *apppreferences.Service
	terminal    *appterminalsettings.Service
}

func (s settingsService) Preferences() domainpreferences.Settings {
	if s.preferences == nil {
		return domainpreferences.Defaults()
	}
	return s.preferences.Settings()
}

func (s settingsService) UpdatePreferences(change func(*domainpreferences.Settings)) error {
	if s.preferences == nil {
		return nil
	}
	return s.preferences.Update(change)
}

func (s settingsService) Terminal() domainterminal.Settings {
	if s.terminal == nil {
		return domainterminal.Defaults()
	}
	return s.terminal.Settings()
}

func (s settingsService) UpdateTerminal(change func(*domainterminal.Settings)) error {
	if s.terminal == nil {
		return nil
	}
	return s.terminal.Update(change)
}
