package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestTransportAreaOffersAndPersistsBothChoices checks the global control the
// ticket asks for: the bundled and in-process clients are both selectable,
// the latter labelled as not available yet, and the choice is written through
// the preferences service so a relaunch reads it back.
func TestTransportAreaOffersAndPersistsBothChoices(t *testing.T) {
	prefs := newFakePreferences()
	prefs.settings = domain.Defaults()
	if got := prefs.settings.Transport.Implementation; got != transportmodel.Bundled {
		t.Fatalf("default implementation = %q, want bundled", got)
	}
	search := NewSearchState()
	search.SetArea(AreaTransport)
	tester := ui.NewTester(ViewWithSettings(Config{
		Preferences: NewPreferencesState(prefs),
		Search:      search,
	}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	for _, label := range []string{m.SettingsTransportBundled(), m.SettingsTransportInProcess()} {
		if _, ok := tester.Find(label); !ok {
			t.Fatalf("no %q radio: %v", label, tester.Texts())
		}
	}

	if err := tester.Click(m.SettingsTransportInProcess()); err != nil {
		t.Fatalf("click in-process: %v", err)
	}
	tester.Frame()
	if prefs.settings.Transport.Implementation != transportmodel.InProcess {
		t.Fatalf("in-process selection was not saved: %q", prefs.settings.Transport.Implementation)
	}
	if !tester.HasText(m.SettingsTransportInProcess()) {
		t.Fatalf("the not-available-yet label disappeared: %v", tester.Texts())
	}

	// A relaunch builds a fresh PreferencesState over the same persisted
	// document; the selection must still be there.
	reloaded := NewPreferencesState(prefs)
	if got := reloaded.Settings().Transport.Implementation; got != transportmodel.InProcess {
		t.Fatalf("implementation after reload = %q, want in_process", got)
	}
	if reloaded.IsDefault(domain.FieldTransportImplementation) {
		t.Fatal("the non-default in-process choice looks like the default")
	}

	if err := tester.Click(m.SettingsTransportBundled()); err != nil {
		t.Fatalf("click bundled: %v", err)
	}
	tester.Frame()
	if prefs.settings.Transport.Implementation != transportmodel.Bundled {
		t.Fatalf("bundled selection was not saved: %q", prefs.settings.Transport.Implementation)
	}
}

// TestTransportAreaCallsOutUnknownSavedValues checks that a value written by a
// newer build stays in the document untouched and is reported, so the user can
// repair it without a silent fallback.
func TestTransportAreaCallsOutUnknownSavedValues(t *testing.T) {
	prefs := newFakePreferences()
	prefs.settings = domain.Defaults()
	prefs.settings.Transport.Implementation = transportmodel.Implementation("future_client")
	search := NewSearchState()
	search.SetArea(AreaTransport)
	tester := ui.NewTester(ViewWithSettings(Config{
		Preferences: NewPreferencesState(prefs),
		Search:      search,
	}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if !tester.HasText(m.SettingsTransportUnknown()) {
		t.Fatalf("the unknown saved value was not called out: %v", tester.Texts())
	}
	if err := tester.Click(m.SettingsTransportBundled()); err != nil {
		t.Fatalf("click bundled: %v", err)
	}
	tester.Frame()
	if prefs.settings.Transport.Implementation != transportmodel.Bundled {
		t.Fatalf("bundled selection did not repair the value: %q", prefs.settings.Transport.Implementation)
	}
	if tester.HasText(m.SettingsTransportUnknown()) {
		t.Fatalf("the warning survived repairing the value: %v", tester.Texts())
	}
}

// TestTransportAreaOffersAndPersistsPredictionModes checks the user-visible
// local-echo control: stock's adaptive default plus the always and never
// overrides, written through the preferences service and read back on a fresh
// state.
func TestTransportAreaOffersAndPersistsPredictionModes(t *testing.T) {
	prefs := newFakePreferences()
	prefs.settings = domain.Defaults()
	if got := prefs.settings.Transport.MoshPredictionDisplay; got != moshmodel.PredictionAdaptive {
		t.Fatalf("default prediction display = %q, want adaptive", got)
	}
	search := NewSearchState()
	search.SetArea(AreaTransport)
	tester := ui.NewTester(ViewWithSettings(Config{
		Preferences: NewPreferencesState(prefs),
		Search:      search,
	}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	for _, label := range []string{
		m.SettingsMoshPredictionAdaptive(),
		m.SettingsMoshPredictionAlways(),
		m.SettingsMoshPredictionNever(),
	} {
		if _, ok := tester.Find(label); !ok {
			t.Fatalf("no %q radio: %v", label, tester.Texts())
		}
	}
	if !tester.HasText(m.SettingsMoshPredictionHint()) {
		t.Fatalf("prediction hint missing: %v", tester.Texts())
	}

	if err := tester.Click(m.SettingsMoshPredictionAlways()); err != nil {
		t.Fatalf("click always: %v", err)
	}
	tester.Frame()
	if prefs.settings.Transport.MoshPredictionDisplay != moshmodel.PredictionAlways {
		t.Fatalf("always selection was not saved: %q", prefs.settings.Transport.MoshPredictionDisplay)
	}
	if prefs.settings.Transport.Implementation != transportmodel.Bundled {
		t.Fatalf("prediction change altered the transport choice: %q",
			prefs.settings.Transport.Implementation)
	}

	reloaded := NewPreferencesState(prefs)
	if got := reloaded.Settings().Transport.MoshPredictionDisplay; got != moshmodel.PredictionAlways {
		t.Fatalf("prediction after reload = %q, want always", got)
	}
	if reloaded.IsDefault(domain.FieldMoshPredictionDisplay) {
		t.Fatal("the non-default always choice looks like the default")
	}

	if err := tester.Click(m.SettingsMoshPredictionNever()); err != nil {
		t.Fatalf("click never: %v", err)
	}
	tester.Frame()
	if prefs.settings.Transport.MoshPredictionDisplay != moshmodel.PredictionNever {
		t.Fatalf("never selection was not saved: %q", prefs.settings.Transport.MoshPredictionDisplay)
	}

	if err := tester.Click(m.SettingsMoshPredictionAdaptive()); err != nil {
		t.Fatalf("click adaptive: %v", err)
	}
	tester.Frame()
	if prefs.settings.Transport.MoshPredictionDisplay != moshmodel.PredictionAdaptive {
		t.Fatalf("adaptive selection was not saved: %q", prefs.settings.Transport.MoshPredictionDisplay)
	}
	if !NewPreferencesState(prefs).IsDefault(domain.FieldMoshPredictionDisplay) {
		t.Fatal("adaptive choice does not look like the default")
	}
}

func TestPredictionDisplayIsSearchable(t *testing.T) {
	hits := FilterHits(SearchIndex(locales.In("en")), "local echo")
	for _, hit := range hits {
		if hit.Area == AreaTransport && hit.Field == domain.FieldMoshPredictionDisplay {
			return
		}
	}
	t.Fatalf("searching local echo did not reach the Mosh prediction setting: %+v", hits)
}
