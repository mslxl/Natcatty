package settings

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

// newAppearanceFixture builds the real Appearance form over the fake
// preferences service and a fake terminal catalog, with the import request
// counted instead of opening a native dialog.
func newAppearanceFixture(t *testing.T) (*fakePreferences, *fakeThemeCatalog, *ThemesState, *int) {
	t.Helper()
	prefs := newFakePreferences()
	catalog := &fakeThemeCatalog{
		themes: []domaintheme.TerminalTheme{
			{ID: domaintheme.DefaultTerminalTheme, Name: "Netcatty Dark"},
			{ID: "paper", Name: "Paper Light"},
		},
		imported: domaintheme.TerminalTheme{ID: "custom-mine", Name: "Mine", Custom: true},
	}
	imports := 0
	catalogState := NewThemesState(catalog,
		WithThemeImportRequest(func() { imports++ }),
		WithThemeImported(func(theme domaintheme.TerminalTheme) bool {
			return prefs.Update(func(next *domain.Settings) {
				next.Appearance.TerminalTheme = theme.ID
			}) == nil
		}))
	return prefs, catalog, catalogState, &imports
}

// TestAppearanceAreaRendersAndDrivesTheCatalog renders the real Appearance
// area at both supported window sizes in English and Simplified Chinese,
// proves every field and group label is laid out inside the window, and drives
// the accent, opacity, icon, and terminal-scheme controls through the view.
func TestAppearanceAreaRendersAndDrivesTheCatalog(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range [][2]int{{1100, 720}, {720, 480}} {
			prefs, fakeCatalog, catalogState, imports := newAppearanceFixture(t)
			state := NewPreferencesState(prefs)
			tester := ui.NewTester(viewAt(AreaAppearance, Config{
				Preferences: state, Themes: catalogState,
			}), size[0], size[1])
			tester.SetLocale(locale)
			tester.Frame()

			m := locales.In(locale)
			for _, text := range []string{
				m.SettingsThemeMode(), m.SettingsLightTheme(), m.SettingsDarkTheme(),
			} {
				rect, ok := tester.Find(text)
				if !ok {
					t.Fatalf("%s %dx%d: %q missing: %v", locale, size[0], size[1], text, tester.Texts())
				}
				if rect.X < -0.5 || rect.W <= 0 || rect.X+rect.W > float32(size[0])+0.5 {
					t.Fatalf("%s %dx%d: %q is clipped horizontally: %+v",
						locale, size[0], size[1], text, rect)
				}
			}
			// The two built-in preset trigger names are the original's data
			// names, so they are not translated.
			if !tester.HasText("Snow") || !tester.HasText("Midnight") {
				t.Fatalf("%s %dx%d: preset triggers do not show their themes: %v",
					locale, size[0], size[1], tester.Texts())
			}

			// Accent swatches select custom accent mode.
			scrollToText(t, tester, m.SettingsAccentAmber(), size[0], size[1])
			checkFieldLabels(t, tester, locale, size[0], size[1],
				m.SettingsAccentMode(), m.SettingsAccentPresets())
			if err := tester.Click(m.SettingsAccentAmber()); err != nil {
				t.Fatalf("%s %dx%d: accent swatch: %v", locale, size[0], size[1], err)
			}
			tester.Frame()
			if prefs.settings.Appearance.AccentMode != domain.AccentCustom ||
				prefs.settings.Appearance.AccentColor != "38 92% 50%" {
				t.Fatalf("%s %dx%d: accent = %+v",
					locale, size[0], size[1], prefs.settings.Appearance)
			}

			// The 70% quick choice writes the same value the slider stores.
			scrollToText(t, tester, m.SettingsWindowOpacityValue("70"), size[0], size[1])
			checkFieldLabels(t, tester, locale, size[0], size[1], m.SettingsWindowOpacity())
			if err := tester.Click(m.SettingsWindowOpacityValue("70")); err != nil {
				t.Fatalf("%s %dx%d: opacity choice: %v", locale, size[0], size[1], err)
			}
			tester.Frame()
			if math.Abs(prefs.settings.Appearance.WindowOpacity-0.7) > 0.0001 {
				t.Fatalf("%s %dx%d: opacity = %v",
					locale, size[0], size[1], prefs.settings.Appearance.WindowOpacity)
			}

			scrollToText(t, tester, m.SettingsAppIconRainbow(), size[0], size[1])
			checkFieldLabels(t, tester, locale, size[0], size[1], m.SettingsAppIcon())
			if err := tester.Click(m.SettingsAppIconRainbow()); err != nil {
				t.Fatalf("%s %dx%d: icon choice: %v", locale, size[0], size[1], err)
			}
			tester.Frame()
			if prefs.settings.Appearance.AppIcon != "rainbow" {
				t.Fatalf("%s %dx%d: icon = %q", locale, size[0], size[1],
					prefs.settings.Appearance.AppIcon)
			}

			scrollToText(t, tester, m.SettingsTerminalThemeImport(), size[0], size[1])
			checkFieldLabels(t, tester, locale, size[0], size[1], m.SettingsTerminalTheme())
			if err := tester.Click(m.SettingsTerminalThemeImport()); err != nil {
				t.Fatalf("%s %dx%d: import button: %v", locale, size[0], size[1], err)
			}
			tester.Frame()
			if *imports != 1 {
				t.Fatalf("%s %dx%d: import requests = %d", locale, size[0], size[1], *imports)
			}

			// A successful import selects its scheme and reports it.
			if !catalogState.Import("Mine.itermcolors", []byte("x"), nil) {
				t.Fatalf("%s %dx%d: import failed: %q", locale, size[0], size[1], catalogState.Problem())
			}
			tester.Frame()
			if prefs.settings.Appearance.TerminalTheme != "custom-mine" {
				t.Fatalf("%s %dx%d: imported scheme was not selected: %q",
					locale, size[0], size[1], prefs.settings.Appearance.TerminalTheme)
			}
			if !tester.HasText(m.SettingsTerminalThemeImported("Mine")) {
				t.Fatalf("%s %dx%d: import result is not shown: %v",
					locale, size[0], size[1], tester.Texts())
			}
			if !tester.HasText(m.SettingsThemeCount(3)) {
				t.Fatalf("%s %dx%d: theme count is not shown: %v",
					locale, size[0], size[1], tester.Texts())
			}

			// A rejected file reports the validation failure and changes
			// neither the catalog nor the selection.
			fakeCatalog.err = domaintheme.ErrInvalidITermcolors
			before := prefs.settings.Appearance.TerminalTheme
			if catalogState.Import("bad.itermcolors", []byte("<plist/>"), nil) {
				t.Fatalf("%s %dx%d: an invalid file was accepted", locale, size[0], size[1])
			}
			tester.Frame()
			if !tester.HasText(m.SettingsTerminalThemeImportInvalid()) {
				t.Fatalf("%s %dx%d: invalid-import message is missing: %v",
					locale, size[0], size[1], tester.Texts())
			}
			if prefs.settings.Appearance.TerminalTheme != before {
				t.Fatalf("%s %dx%d: a failed import changed the selection",
					locale, size[0], size[1])
			}
		}
	}
}

// checkFieldLabels proves each label is laid out and inside the window after
// its section has been scrolled into view.
func checkFieldLabels(t *testing.T, tester *ui.Tester,
	locale string, width, height int, labels ...string) {
	t.Helper()
	for _, text := range labels {
		rect, ok := tester.Find(text)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			t.Fatalf("%s %dx%d: %q is not laid out: %+v (present=%v): %v",
				locale, width, height, text, rect, ok, tester.Texts())
		}
		if rect.X < -0.5 || rect.X+rect.W > float32(width)+0.5 ||
			rect.Y < -0.5 || rect.Y+rect.H > float32(height)+0.5 {
			t.Fatalf("%s %dx%d: %q is clipped: %+v", locale, width, height, text, rect)
		}
	}
}

// scrollToText wheels the Settings content down until text is fully inside
// the window. Each iteration renders a frame, so the loop is bounded by real
// work rather than a wall-clock delay.
func scrollToText(t *testing.T, tester *ui.Tester, text string, width, height int) {
	t.Helper()
	for step := 0; step < 200; step++ {
		if rect, ok := tester.Find(text); ok && rect.W > 0 && rect.H > 0 &&
			rect.X >= -0.5 && rect.Y >= -0.5 &&
			rect.X+rect.W <= float32(width)+0.5 && rect.Y+rect.H <= float32(height)+0.5 {
			return
		}
		tester.Scroll(float32(width)/2, float32(height)/2, 0, 40)
		tester.Frame()
	}
	t.Fatalf("%q never scrolled into the %dx%d window", text, width, height)
}

// TestAppearanceAreaFilterNarrowsTheTerminalCatalog drives the picker's
// filter without depending on the popup's pointer geometry.
func TestAppearanceAreaFilterNarrowsTheTerminalCatalog(t *testing.T) {
	prefs, _, catalogState, _ := newAppearanceFixture(t)
	state := NewPreferencesState(prefs)
	tester := ui.NewTester(viewAt(AreaAppearance, Config{
		Preferences: state, Themes: catalogState,
	}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	*catalogState.Filter() = "paper"
	filtered := catalogState.Filtered()
	if len(filtered) != 1 || filtered[0].ID != "paper" {
		t.Fatalf("filtered = %+v", filtered)
	}
	*catalogState.Filter() = "missing"
	if got := catalogState.Filtered(); len(got) != 0 {
		t.Fatalf("missing filter = %+v", got)
	}
	*catalogState.Filter() = ""
	if got := len(catalogState.Filtered()); got != 2 {
		t.Fatalf("cleared filter = %d themes", got)
	}
}
