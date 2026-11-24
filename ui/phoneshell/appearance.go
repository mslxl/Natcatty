package phoneshell

// The phone terminal reads the shared Appearance document: the same font,
// cursor and color scheme the desktop terminal uses. The settings page edits
// that document through the shared services, and the phone root asks the live
// screen to restyle itself when it changes, as the desktop root does.

import (
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/terminal"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// TerminalAppearance is the visual configuration the phone hands a terminal
// when it is built and again when the Appearance settings change.
type TerminalAppearance struct {
	Font   terminal.Font
	Theme  *terminal.Theme
	Cursor terminal.CursorStyle
	Blink  bool
	// Keybinds are the external-keyboard bindings MyGo honors in this
	// terminal. They are captured when the terminal is built, as on the
	// desktop; a live terminal keeps the table it was created with. The
	// Appearance document contributes none, so this stays nil unless the
	// composition root installs the shared keybinding service.
	Keybinds []terminal.Keybind
}

// TerminalAppearanceFromAppearance maps the shared Appearance document onto
// the terminal's options. systemDark is the phone's current system appearance,
// consulted only while FollowAppTerminalTheme is on.
func TerminalAppearanceFromAppearance(appearance domainpreferences.Appearance,
	systemDark bool) TerminalAppearance {

	return TerminalAppearance{
		Font: terminal.Font{
			Family: domainpreferences.TerminalFontFamily(appearance.TerminalFontFamily),
			Size:   float32(appearance.TerminalFontSize),
		},
		Theme:  phoneTerminalTheme(appearance, systemDark),
		Cursor: phoneCursor(appearance.TerminalCursorStyle),
		Blink:  !appearance.TerminalCursorSteady,
	}
}

// DefaultTerminalAppearance is what a shell without a settings service uses:
// the shared default document, not a phone-only scheme.
func DefaultTerminalAppearance() TerminalAppearance {
	return TerminalAppearanceFromAppearance(domainpreferences.Defaults().Appearance, false)
}

// phoneTerminalTheme resolves the scheme the terminal draws with. While the
// terminal follows the app, the scheme matched to the active UI preset wins;
// otherwise, or for a preset with no match, the stored scheme does. An unknown
// stored id falls back to the original's default, as the desktop does.
func phoneTerminalTheme(appearance domainpreferences.Appearance,
	systemDark bool) *terminal.Theme {

	id := appearance.TerminalTheme
	if appearance.FollowAppTerminalTheme {
		dark := appearance.ThemeMode == domainpreferences.ThemeDark
		if appearance.ThemeMode == domainpreferences.ThemeSystem {
			dark = systemDark
		}
		preset := domaintheme.ResolvePreset(dark, appearance.LightTheme, appearance.DarkTheme)
		if match, ok := domaintheme.TerminalThemeForPreset(dark, preset.ID); ok {
			id = match
		}
	}
	scheme, ok := domaintheme.LookupTerminalTheme(id)
	if !ok {
		scheme, _ = domaintheme.LookupTerminalTheme(domaintheme.DefaultTerminalTheme)
	}
	return uitheme.Terminal(scheme)
}

func phoneCursor(style domainpreferences.CursorStyle) terminal.CursorStyle {
	switch style {
	case domainpreferences.CursorBar:
		return terminal.CursorBar
	case domainpreferences.CursorUnderline:
		return terminal.CursorUnderline
	default:
		return terminal.CursorBlock
	}
}
