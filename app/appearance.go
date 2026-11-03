package app

import (
	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// resolveUITheme maps the Appearance document onto MyGo's widget theme.
// systemDark is the desktop's current appearance, used only in System mode.
func resolveUITheme(appearance domainpreferences.Appearance, systemDark bool) *ui.Theme {
	dark, preset := activePreset(appearance, systemDark)
	return uitheme.UI(dark, preset, uitheme.Options{
		CustomAccent: appearance.AccentMode == domainpreferences.AccentCustom,
		AccentHSL:    appearance.AccentColor,
		Font:         appearance.UIFont,
	})
}

// activePreset returns the appearance and UI preset the window shows.
func activePreset(appearance domainpreferences.Appearance, systemDark bool) (bool, domaintheme.Preset) {
	dark := appearance.ThemeMode == domainpreferences.ThemeDark
	if appearance.ThemeMode == domainpreferences.ThemeSystem {
		dark = systemDark
	}
	return dark, domaintheme.ResolvePreset(dark, appearance.LightTheme, appearance.DarkTheme)
}

// resolveAppTerminalTheme returns the scheme terminals draw with. While the
// terminal follows the app it is the scheme matched to the active UI preset,
// so a light window has a light terminal, as the original's; otherwise, or
// for a preset with no match, it is the stored scheme.
func resolveAppTerminalTheme(appearance domainpreferences.Appearance, systemDark bool,
	find func(string) (domaintheme.TerminalTheme, bool)) domaintheme.TerminalTheme {
	if appearance.FollowAppTerminalTheme {
		dark, preset := activePreset(appearance, systemDark)
		if id, ok := domaintheme.TerminalThemeForPreset(dark, preset.ID); ok {
			if theme, ok := domaintheme.LookupTerminalTheme(id); ok {
				return theme
			}
		}
	}
	return resolveTerminalTheme(appearance.TerminalTheme, find)
}

// resolveTerminalTheme returns the stored scheme, or the original's Netcatty
// Dark when the ID is unknown (for example after a custom theme file was
// removed by hand).
func resolveTerminalTheme(id string,
	find func(string) (domaintheme.TerminalTheme, bool)) domaintheme.TerminalTheme {
	if find != nil {
		if theme, ok := find(id); ok {
			return theme
		}
	}
	fallback, _ := domaintheme.LookupTerminalTheme(domaintheme.DefaultTerminalTheme)
	return fallback
}
