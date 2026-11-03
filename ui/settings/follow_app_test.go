package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestFollowAppHidesTheSchemePicker keeps the original's Terminal theme
// section: while the terminal follows the app only the toggle shows, and
// turning it off brings the scheme picker back.
func TestFollowAppHidesTheSchemePicker(t *testing.T) {
	m := locales.In("en")
	fake := newFakePreferences()
	prefs := NewPreferencesState(fake)
	tester := ui.NewTester(ViewWithSettings(Config{Preferences: prefs}), 1100, 2400)
	tester.SetLocale("en")
	tester.Frame()

	if !tester.HasText(m.SettingsFollowAppTerminalTheme()) {
		t.Fatalf("no follow-app toggle: %v", tester.Texts())
	}
	if tester.HasText(m.SettingsTerminalTheme()) {
		t.Fatal("the scheme picker shows while the terminal follows the app")
	}
	if !prefs.Update(func(s *domain.Settings) { s.Appearance.FollowAppTerminalTheme = false }) {
		t.Fatal("the update was refused")
	}
	tester.Frame()
	if !tester.HasText(m.SettingsTerminalTheme()) {
		t.Fatalf("the scheme picker did not come back: %v", tester.Texts())
	}
}

// TestSideThemePanelFollowsTheApp keeps the original's side panel: following,
// it lists the window themes' schemes and a pick switches the window theme.
func TestSideThemePanelFollowsTheApp(t *testing.T) {
	fake := newFakePreferences()
	panel := NewThemePanel(NewThemesState(nil), fake)
	tester := ui.NewTester(panel.View(), 320, 4000)
	tester.SetLocale("en")
	tester.Frame()

	netcatty, _ := domaintheme.LookupTerminalTheme(domaintheme.DefaultTerminalTheme)
	if tester.HasText(netcatty.Name) {
		t.Fatalf("%q is offered while the terminal follows the app", netcatty.Name)
	}
	pick, _ := domaintheme.LookupTerminalTheme("system-catppuccin-dark")
	if err := tester.Click(pick.Name); err != nil {
		t.Fatalf("%v: %v", err, tester.Texts())
	}
	tester.Frame()
	appearance := fake.Settings().Appearance
	if appearance.ThemeMode != domain.ThemeDark || appearance.DarkTheme != "catppuccin" {
		t.Fatalf("the pick set mode=%q dark=%q", appearance.ThemeMode, appearance.DarkTheme)
	}
	if appearance.TerminalTheme != domaintheme.DefaultTerminalTheme {
		t.Fatalf("the pick changed the terminal's own scheme to %q", appearance.TerminalTheme)
	}
}
