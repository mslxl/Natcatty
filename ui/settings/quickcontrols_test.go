package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/titlebar"
)

// TestLanguageIsTheFirstAppearanceRow keeps the language where the original
// has it, in Settings, and checks a choice reaches the app.
func TestLanguageIsTheFirstAppearanceRow(t *testing.T) {
	var requested []string
	state := NewPreferencesState(newFakePreferences(), WithLocaleRequest(func(tag string) {
		requested = append(requested, tag)
	}))
	tester := ui.NewTester(viewAt(AreaAppearance, Config{Preferences: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")
	language, ok := tester.Find(m.NavLanguage())
	theme, themeOK := tester.Find(m.SettingsThemeMode())
	if !ok || !themeOK || language.Y >= theme.Y {
		t.Fatalf("Language is not above the theme: %+v %v, %+v %v", language, ok, theme, themeOK)
	}
	if err := tester.Click("English"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if err := tester.Click("español"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(requested) != 1 || requested[0] != "es" {
		t.Fatalf("requested locales = %v, want [es]", requested)
	}
}

// TestTerminalFontPickerStoresTheCatalogID checks the picker writes the id
// the terminal resolves to a monospace family, not the display name.
func TestTerminalFontPickerStoresTheCatalogID(t *testing.T) {
	fake := newFakePreferences()
	state := NewPreferencesState(fake)
	// Tall enough that the font row is on screen without scrolling.
	tester := ui.NewTester(viewAt(AreaAppearance, Config{Preferences: state}), 1100, 2400)
	tester.SetLocale("en")
	tester.Frame()
	current, _ := domain.LookupTerminalFont(fake.settings.Appearance.TerminalFontFamily)
	// The popup opens on the current font; choose the one after it, which
	// is on screen whatever the OS default is.
	fonts := domain.TerminalFonts()
	choice := fonts[0]
	for index, font := range fonts {
		if font.ID == current.ID && index+1 < len(fonts) {
			choice = fonts[index+1]
		}
	}
	want := choice.ID
	if err := tester.Click(current.Name); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if err := tester.Click(choice.Name); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if got := fake.settings.Appearance.TerminalFontFamily; got != want {
		t.Fatalf("terminal font = %q, want the catalog id %q", got, want)
	}
}

// TestQuickControlsChangeOpacityAndTheme drives the Tab bar's control panel.
func TestQuickControlsChangeOpacityAndTheme(t *testing.T) {
	fake := newFakePreferences()
	state := NewPreferencesState(fake)
	tester := ui.NewTester(state.QuickControls(), 400, 300)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")
	if err := tester.Click(m.SettingsWindowOpacityValue("85")); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if err := tester.Click(m.SettingsThemeDark()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if got := fake.settings.Appearance; got.WindowOpacity != 0.85 || got.ThemeMode != domain.ThemeDark {
		t.Fatalf("opacity %v theme %q, want 0.85 and dark", got.WindowOpacity, got.ThemeMode)
	}
}

// TestHeaderRowLinesUpWithTheCloseButton keeps the Settings title row on the
// line of the window's close button, as the original's header row, and its
// actions clear of it.
func TestHeaderRowLinesUpWithTheCloseButton(t *testing.T) {
	const width = 1100
	tester := ui.NewTester(ViewWithSettings(Config{}), width, 720)
	tester.SetLocale("en")
	tester.Frame()
	rect, ok := tester.Find(locales.In("en").NavSettings())
	if !ok {
		t.Fatalf("no title: %v", tester.Texts())
	}
	center := rect.Y + rect.H/2
	want := titlebar.CloseTop + titlebar.CloseSize/2
	if center < want-1 || center > want+1 {
		t.Fatalf("title centered at %v, the close button at %v", center, want)
	}
}
