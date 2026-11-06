package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestTransportAreaOffersAndPersistsEveryChoice checks the global control the
// ticket asks for: the release default, the bundled client, and the in-process
// beta are all selectable; the beta is labelled as beta; and each choice is
// written with its provenance so a relaunch reads it back.
func TestTransportAreaOffersAndPersistsEveryChoice(t *testing.T) {
	prefs := newFakePreferences()
	prefs.settings = domain.Defaults()
	if got := prefs.settings.Transport.Implementation; got != transportmodel.CurrentDefault() {
		t.Fatalf("default implementation = %q, want the release default %q",
			got, transportmodel.CurrentDefault())
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
		m.SettingsTransportUseReleaseDefault(),
		m.SettingsTransportBundled(),
		m.SettingsTransportInProcess(),
	} {
		if _, ok := tester.Find(label); !ok {
			t.Fatalf("no %q radio: %v", label, tester.Texts())
		}
	}
	defaultLabel := m.SettingsTransportBundled()
	if transportmodel.CurrentDefault() == transportmodel.InProcess {
		defaultLabel = m.SettingsTransportInProcess()
	}
	if !tester.HasText(m.SettingsTransportEffectiveDefault(defaultLabel)) {
		t.Fatalf("the effective release default is not shown: %v", tester.Texts())
	}
	if !tester.HasText(m.SettingsTransportBetaHint(defaultLabel)) {
		t.Fatalf("the beta hint is missing: %v", tester.Texts())
	}

	if err := tester.Click(m.SettingsTransportInProcess()); err != nil {
		t.Fatalf("click in-process: %v", err)
	}
	tester.Frame()
	if prefs.settings.Transport.Implementation != transportmodel.InProcess ||
		!prefs.settings.Transport.ImplementationExplicit {
		t.Fatalf("in-process selection = %+v, want an explicit in_process",
			prefs.settings.Transport)
	}
	if !tester.HasText(m.SettingsTransportInProcess()) {
		t.Fatalf("the beta label disappeared: %v", tester.Texts())
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
	if prefs.settings.Transport.Implementation != transportmodel.Bundled ||
		!prefs.settings.Transport.ImplementationExplicit {
		t.Fatalf("bundled selection = %+v, want an explicit bundled", prefs.settings.Transport)
	}

	// Choosing the release default clears provenance even though it currently
	// resolves to the same bundled client.
	if err := tester.Click(m.SettingsTransportUseReleaseDefault()); err != nil {
		t.Fatalf("click release default: %v", err)
	}
	tester.Frame()
	if prefs.settings.Transport.Implementation != transportmodel.CurrentDefault() ||
		prefs.settings.Transport.ImplementationExplicit {
		t.Fatalf("release-default selection = %+v, want the non-explicit default",
			prefs.settings.Transport)
	}
	if !NewPreferencesState(prefs).IsDefault(domain.FieldTransportImplementation) {
		t.Fatal("the release-default choice does not look like the default")
	}
}

// TestTransportAreaRecordsAnExplicitBundledChoice covers the case the old UI
// could not express: selecting Bundled while it is already the effective
// default must still write provenance, so a later release default flip does
// not move that user.
func TestTransportAreaRecordsAnExplicitBundledChoice(t *testing.T) {
	prefs := newFakePreferences()
	prefs.settings = domain.Defaults()
	search := NewSearchState()
	search.SetArea(AreaTransport)
	tester := ui.NewTester(ViewWithSettings(Config{
		Preferences: NewPreferencesState(prefs),
		Search:      search,
	}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.SettingsTransportBundled()); err != nil {
		t.Fatalf("click bundled: %v", err)
	}
	tester.Frame()
	if !prefs.settings.Transport.ImplementationExplicit {
		t.Fatal("choosing Bundled while it is the default did not record provenance")
	}
}

// TestTransportAreaCallsOutUnknownSavedValues checks that a value written by a
// newer build stays in the document untouched and is reported, so the user can
// repair it without a silent fallback. Choosing the release default is the
// one-click cleanup path.
func TestTransportAreaCallsOutUnknownSavedValues(t *testing.T) {
	prefs := newFakePreferences()
	prefs.settings = domain.Defaults()
	prefs.settings.Transport.Implementation = transportmodel.Implementation("future_client")
	prefs.settings.Transport.ImplementationExplicit = true
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
	if err := tester.Click(m.SettingsTransportUseReleaseDefault()); err != nil {
		t.Fatalf("click release default: %v", err)
	}
	tester.Frame()
	if got := prefs.settings.Transport.Implementation; got != transportmodel.CurrentDefault() {
		t.Fatalf("release default did not repair the value: %q", got)
	}
	if prefs.settings.Transport.ImplementationExplicit {
		t.Fatal("repairing an unknown value kept the explicit-choice flag")
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
	if prefs.settings.Transport.Implementation != transportmodel.CurrentDefault() {
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
