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
	dark := appearance.ThemeMode == domainpreferences.ThemeDark
	if appearance.ThemeMode == domainpreferences.ThemeSystem {
		dark = systemDark
	}
	preset := domaintheme.ResolvePreset(dark, appearance.LightTheme, appearance.DarkTheme)
	return uitheme.UI(dark, preset, uitheme.Options{
		CustomAccent: appearance.AccentMode == domainpreferences.AccentCustom,
		AccentHSL:    appearance.AccentColor,
		Font:         appearance.UIFont,
	})
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
