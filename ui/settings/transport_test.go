package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

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
