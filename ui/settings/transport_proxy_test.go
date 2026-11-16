package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestSettingsDefaultProxyRoundTripsThePreference proves the app-wide default
// proxy is reachable from the Settings UI, persisted through the preferences
// seam, and offered again by a freshly loaded state.
func TestSettingsDefaultProxyRoundTripsThePreference(t *testing.T) {
	prefs := newFakePreferences()
	choices := []ProxyProfileChoice{
		{ID: "proxy-office", Label: "office-proxy"},
		{ID: "proxy-home", Label: "home-proxy"},
	}
	profiles := func() []ProxyProfileChoice { return choices }
	state := NewPreferencesState(prefs, WithProxyProfiles(profiles))
	tester := ui.NewTester(viewAt(AreaTransport, Config{Preferences: state}), 1100, 1600)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	for _, text := range []string{
		m.SettingsDefaultProxy(), m.SettingsDefaultProxyHint(),
		m.SettingsDefaultProxyOff(), "office-proxy", "home-proxy",
	} {
		if !tester.HasText(text) {
			t.Fatalf("the Transport area does not show %q; texts %q", text, tester.Texts())
		}
	}

	if err := tester.Click("office-proxy"); err != nil {
		t.Fatalf("click office-proxy: %v", err)
	}
	tester.Frame()
	if got := prefs.settings.Proxy.DefaultProfileID; got != "proxy-office" {
		t.Fatalf("persisted DefaultProfileID = %q, want proxy-office", got)
	}
	if got := state.Settings().DefaultProxyProfileID(); got != "proxy-office" {
		t.Fatalf("state DefaultProxyProfileID = %q, want proxy-office", got)
	}

	// A new state over the same persisted document, as a restart builds.
	reloaded := NewPreferencesState(prefs, WithProxyProfiles(profiles))
	if got := reloaded.Settings().DefaultProxyProfileID(); got != "proxy-office" {
		t.Fatalf("reloaded DefaultProxyProfileID = %q, want proxy-office", got)
	}
	reloadedTester := ui.NewTester(viewAt(AreaTransport, Config{Preferences: reloaded}), 1100, 1600)
	reloadedTester.SetLocale("en")
	reloadedTester.Frame()
	if !reloadedTester.HasText("office-proxy") {
		t.Fatalf("reloaded Settings lost the saved profile list: %q", reloadedTester.Texts())
	}

	if err := tester.Click(m.SettingsDefaultProxyOff()); err != nil {
		t.Fatalf("click %s: %v", m.SettingsDefaultProxyOff(), err)
	}
	tester.Frame()
	if got := prefs.settings.Proxy.DefaultProfileID; got != "" {
		t.Fatalf("DefaultProfileID after Off = %q, want empty", got)
	}
}

// TestResettingTheTransportAreaClearsTheDefaultProxy keeps the new row honest
// with the area's reset control.
func TestResettingTheTransportAreaClearsTheDefaultProxy(t *testing.T) {
	prefs := newFakePreferences()
	prefs.settings.Proxy.DefaultProfileID = "proxy-office"
	state := NewPreferencesState(prefs)

	if !state.ResetGroup(domain.GroupTransport) {
		t.Fatalf("ResetGroup failed")
	}
	if got := prefs.settings.Proxy.DefaultProfileID; got != "" {
		t.Fatalf("DefaultProfileID after reset = %q, want empty", got)
	}
}
