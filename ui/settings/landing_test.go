package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestStartupLandingOffersEverySurface drives the Open on launch select: the
// four choices are offered and the two new ones store their own values.
func TestStartupLandingOffersEverySurface(t *testing.T) {
	fake := newFakePreferences()
	state := NewPreferencesState(fake)
	tester := ui.NewTester(viewAt(AreaLayout, Config{Preferences: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	scrollToText(t, tester, m.SettingsStartupLanding(), 1100, 720)
	if err := tester.Click(m.SettingsStartupVault()); err != nil {
		t.Fatalf("open the startup landing select: %v", err)
	}
	tester.Frame()
	for _, want := range []string{
		m.SettingsStartupVault(), m.SettingsStartupLast(),
		m.SettingsStartupTerminal(), m.SettingsStartupMonitoring(),
	} {
		if !tester.HasText(want) {
			t.Fatalf("startup landing option %q is missing: %v", want, tester.Texts())
		}
	}

	if err := tester.Click(m.SettingsStartupMonitoring()); err != nil {
		t.Fatalf("choose Monitoring: %v", err)
	}
	tester.Frame()
	if fake.settings.Layout.StartupLanding != domain.StartupMonitoring {
		t.Fatalf("startup landing = %q, want %q",
			fake.settings.Layout.StartupLanding, domain.StartupMonitoring)
	}

	// The new local terminal choice maps back to its own value, not to the
	// legacy "last session" one.
	if err := tester.Click(m.SettingsStartupMonitoring()); err != nil {
		t.Fatalf("reopen the startup landing select: %v", err)
	}
	tester.Frame()
	if err := tester.Click(m.SettingsStartupTerminal()); err != nil {
		t.Fatalf("choose New local terminal: %v", err)
	}
	tester.Frame()
	if fake.settings.Layout.StartupLanding != domain.StartupTerminal {
		t.Fatalf("startup landing = %q, want %q",
			fake.settings.Layout.StartupLanding, domain.StartupTerminal)
	}
}
