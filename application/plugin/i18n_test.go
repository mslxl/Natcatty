package plugin_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// stubCatalogSource is the application layer's catalog port in tests. The TOML
// adapter is exercised by adapters/plugini18n; the application layer must not
// import it.
type stubCatalogSource struct {
	catalogs domainplugin.Catalogs
	err      error
	calls    int
}

func (s *stubCatalogSource) LoadCatalogs(string, string) (domainplugin.Catalogs, error) {
	s.calls++
	if s.err != nil {
		return domainplugin.Catalogs{}, s.err
	}
	return s.catalogs.Clone(), nil
}

func i18nCatalogs() domainplugin.Catalogs {
	return domainplugin.NewCatalogs("en", map[string]domainplugin.Catalog{
		"en": {
			"greet":        "Hello, {name}!",
			"files.one":    "{count} file",
			"files.other":  "{count} files",
			"only_default": "Default only",
		},
		"es": {
			"greet":       "Hola, {name}!",
			"files.one":   "{count} archivo",
			"files.other": "{count} archivos",
		},
	})
}

// i18nSession opens a second session for a distinct fiber so the fixture's
// original session keeps its UI handle table.
func i18nSession(t *testing.T, fixture *serviceFixture,
	catalogs domainplugin.Catalogs) (appplugin.PluginRef, domainplugin.FiberID, *appplugin.ServiceSession) {
	t.Helper()
	fiber := domainplugin.NewFiberID("installed", fixture.ref.ID, 2)
	session := fixture.host.Open(fixture.ref, fiber, nil, catalogs)
	if session == nil {
		t.Fatal("Open returned nil")
	}
	return fixture.ref, fiber, session
}

func decodeTranslation(t *testing.T, envelope serviceEnvelope) appplugin.MessageTranslation {
	t.Helper()
	var translation appplugin.MessageTranslation
	if err := json.Unmarshal(envelope.Data, &translation); err != nil {
		t.Fatalf("decode translation %s: %v", envelope.Data, err)
	}
	return translation
}

func TestI18nServiceIsPermissionFree(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	_, _, session := i18nSession(t, fixture, i18nCatalogs())

	envelope := dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodTranslate,
		`{"key":"greet","args":{"name":"Ada"}}`)
	if envelope.Code != appplugin.ServiceCodeOK {
		t.Fatalf("translate envelope = %+v", envelope)
	}
	translation := decodeTranslation(t, envelope)
	if translation.Text != "Hello, Ada!" || translation.Stage != domainplugin.StageExact ||
		translation.Missing || translation.Locale != "en" || translation.Generation != 1 {
		t.Fatalf("translation = %+v", translation)
	}

	// No permission was granted above: success proves the service is
	// permission-free rather than merely allowed.
	envelope = dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodCatalog, "")
	if envelope.Code != appplugin.ServiceCodeOK {
		t.Fatalf("permission-free catalog envelope = %+v", envelope)
	}
}

func TestI18nTranslateFallsBackInOrderAndCountsMisses(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	_, fiber, session := i18nSession(t, fixture, i18nCatalogs())

	if !fixture.host.SetLocale("es-MX") {
		t.Fatal("SetLocale(es-MX) reported no change")
	}

	tests := []struct {
		name  string
		body  string
		text  string
		stage domainplugin.ResolutionStage
	}{
		{"language only", `{"key":"greet","args":{"name":"Ana"}}`, "Hola, Ana!", domainplugin.StageLanguage},
		{"default locale", `{"key":"only_default"}`, "Default only", domainplugin.StageDefault},
		{"literal fallback", `{"key":"absent","literal":"Fallback"}`, "Fallback", domainplugin.StageLiteral},
		{"key fallback", `{"key":"absent"}`, "absent", domainplugin.StageKey},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			translation := decodeTranslation(t, dispatchService(t, session,
				appplugin.ServiceI18n, domainpluginabi.I18nMethodTranslate, test.body))
			if translation.Text != test.text || translation.Stage != test.stage ||
				!translation.Missing || translation.Locale != "es-MX" {
				t.Fatalf("translation = %+v", translation)
			}
		})
	}

	// Every non-exact lookup counts once per key; repeated lookups accumulate.
	_ = dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodTranslate, `{"key":"greet","args":{"name":"Ana"}}`)
	missing := fixture.host.MissingKeys(fiber)
	want := []appplugin.MissingKey{
		{Key: "absent", Count: 2},
		{Key: "greet", Count: 2},
		{Key: "only_default", Count: 1},
	}
	if len(missing) != len(want) {
		t.Fatalf("missing keys = %+v", missing)
	}
	for i, entry := range want {
		if missing[i] != entry {
			t.Fatalf("missing[%d] = %+v, want %+v", i, missing[i], entry)
		}
	}
}

func TestI18nMissingKeyDiagnosticsStayBounded(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	_, fiber, session := i18nSession(t, fixture, i18nCatalogs())
	for i := 0; i < appplugin.MaxMissingKeyDiagnostics+20; i++ {
		_ = dispatchService(t, session, appplugin.ServiceI18n,
			domainpluginabi.I18nMethodTranslate, fmt.Sprintf(`{"key":"key_%03d"}`, i))
	}
	missing := fixture.host.MissingKeys(fiber)
	if len(missing) != appplugin.MaxMissingKeyDiagnostics {
		t.Fatalf("distinct missing keys = %d, want %d",
			len(missing), appplugin.MaxMissingKeyDiagnostics)
	}
}

func TestI18nPluralAndFormattingMethods(t *testing.T) {
	formatter := &recordingLocaleFormatter{}
	host := appplugin.NewHostServices(appplugin.ServiceDeps{
		Locale:          "es-MX",
		LocaleFormatter: formatter,
	})
	t.Cleanup(host.CloseAll)
	ref := appplugin.PluginRef{ID: "acme.i18n", Name: "I18n", Version: "1.0.0"}
	fiber := domainplugin.NewFiberID("installed", ref.ID, 1)
	session := host.Open(ref, fiber, nil, i18nCatalogs())
	if session == nil {
		t.Fatal("Open returned nil")
	}

	one := decodeTranslation(t, dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodTranslate, `{"key":"files","count":1}`))
	if one.Text != "1 archivo" || one.Stage != domainplugin.StageLanguage {
		t.Fatalf("plural one = %+v", one)
	}
	many := decodeTranslation(t, dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodTranslate, `{"key":"files","count":7}`))
	if many.Text != "7 archivos" {
		t.Fatalf("plural many = %+v", many)
	}

	plural := dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodPlural, `{"count":3}`)
	var category struct {
		Category string `json:"category"`
	}
	if err := json.Unmarshal(plural.Data, &category); err != nil || category.Category != domainplugin.PluralOther {
		t.Fatalf("plural category = %s, %v", plural.Data, err)
	}

	number := dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodNumber, `{"value":1234.5}`)
	if got := decodeText(t, number); got != "number:es-MX:1234.5" {
		t.Fatalf("number = %q", got)
	}
	date := dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodDate, `{"value":"2026-11-07T10:30:00Z"}`)
	if got := decodeText(t, date); got != "date:es-MX:2026-11-07T10:30:00Z" {
		t.Fatalf("date = %q", got)
	}
	relative := dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodRelativeTime,
		`{"value":"2026-11-07T08:30:00Z","now":"2026-11-07T10:30:00Z"}`)
	if got := decodeText(t, relative); got != "relative:es-MX:2h0m0s" {
		t.Fatalf("relative time = %q", got)
	}

	if !host.SetLocale("ru") {
		t.Fatal("SetLocale(ru) reported no change")
	}
	plural = dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodPlural, `{"count":3}`)
	if err := json.Unmarshal(plural.Data, &category); err != nil || category.Category != domainplugin.PluralFew {
		t.Fatalf("ru plural category = %s, %v", plural.Data, err)
	}
	number = dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodNumber, `{"value":2}`)
	if got := decodeText(t, number); got != "number:ru:2" || formatter.lastLocale() != "ru" {
		t.Fatalf("formatter after locale change = %q (locale %q)", got, formatter.lastLocale())
	}
}

func TestI18nCatalogSnapshotMergesFallbacks(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	_, _, session := i18nSession(t, fixture, i18nCatalogs())
	if !fixture.host.SetLocale("es-MX") {
		t.Fatal("SetLocale(es-MX) reported no change")
	}
	envelope := dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodCatalog, "")
	if envelope.Code != appplugin.ServiceCodeOK {
		t.Fatalf("catalog envelope = %+v", envelope)
	}
	var snapshot appplugin.CatalogSnapshot
	if err := json.Unmarshal(envelope.Data, &snapshot); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if snapshot.Locale != "es-MX" || snapshot.Direction != appplugin.UIDirectionLTR ||
		snapshot.DefaultLocale != "en" || snapshot.Generation != 2 {
		t.Fatalf("catalog snapshot = %+v", snapshot)
	}
	if snapshot.Messages["greet"] != "Hola, {name}!" ||
		snapshot.Messages["only_default"] != "Default only" {
		t.Fatalf("merged messages = %+v", snapshot.Messages)
	}

	locale := dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodLocale, "")
	var state struct {
		Locale     string                `json:"locale"`
		Direction  appplugin.UIDirection `json:"direction"`
		Generation uint64                `json:"generation"`
	}
	if err := json.Unmarshal(locale.Data, &state); err != nil ||
		state.Locale != "es-MX" || state.Direction != appplugin.UIDirectionLTR ||
		state.Generation != 2 {
		t.Fatalf("locale state = %s, %v", locale.Data, err)
	}
}

func TestSetLocaleUpdatesSessionsAndNativeViewsWithoutReload(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	_, _, session := i18nSession(t, fixture, i18nCatalogs())
	ui := fixture.session.NativeUI()
	if env := ui.Environment(); env.Locale != "en" || env.Direction != appplugin.UIDirectionLTR {
		t.Fatalf("initial environment = %+v", env)
	}
	// A session-wide listener receives locale_changed, like a native view's
	// renderer poll loop.
	if _, err := ui.Listen(0, domainnativeui.EventLocaleChanged); err != nil {
		t.Fatalf("Listen(locale_changed): %v", err)
	}

	if !fixture.host.SetLocale("ar") {
		t.Fatal("SetLocale(ar) reported no change")
	}
	if fixture.host.Locale() != "ar" || fixture.host.CatalogGeneration() != 2 {
		t.Fatalf("host locale = %q generation = %d",
			fixture.host.Locale(), fixture.host.CatalogGeneration())
	}
	if env := ui.Environment(); env.Locale != "ar" || env.Direction != appplugin.UIDirectionRTL {
		t.Fatalf("environment after locale change = %+v", env)
	}
	events := ui.Poll(0)
	if len(events) != 1 || events[0].Kind != domainnativeui.EventLocaleChanged {
		t.Fatalf("native events = %+v", events)
	}

	// The same session keeps serving translations in the new locale; nothing
	// reopened it, and the generation tells a catalog cache to refresh.
	translation := decodeTranslation(t, dispatchService(t, session, appplugin.ServiceI18n,
		domainpluginabi.I18nMethodTranslate, `{"key":"greet","args":{"name":"Ada"}}`))
	if translation.Text != "Hello, Ada!" || translation.Stage != domainplugin.StageDefault ||
		translation.Locale != "ar" || translation.Generation != 2 {
		t.Fatalf("translation after locale change = %+v", translation)
	}

	if fixture.host.SetLocale("ar") {
		t.Fatal("SetLocale repeated the same locale")
	}
	if fixture.host.CatalogGeneration() != 2 {
		t.Fatalf("generation advanced without a locale change: %d",
			fixture.host.CatalogGeneration())
	}
	if pending := ui.PendingEvents(); pending != 0 {
		t.Fatalf("repeated locale change queued %d events", pending)
	}
}

func decodeText(t *testing.T, envelope serviceEnvelope) string {
	t.Helper()
	if envelope.Code != appplugin.ServiceCodeOK {
		t.Fatalf("service envelope = %+v", envelope)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(envelope.Data, &payload); err != nil {
		t.Fatalf("decode text: %v", err)
	}
	return payload.Text
}

// recordingLocaleFormatter reports the locale it received so a test can prove
// the host, not the guest, chooses the formatting locale.
type recordingLocaleFormatter struct {
	last string
}

func (f *recordingLocaleFormatter) record(locale string) {
	f.last = locale
}

func (f *recordingLocaleFormatter) lastLocale() string { return f.last }

func (f *recordingLocaleFormatter) FormatNumber(locale string, value float64) string {
	f.record(locale)
	return fmt.Sprintf("number:%s:%s", locale, trimFloat(value))
}

func (f *recordingLocaleFormatter) FormatDate(locale string, value time.Time) string {
	f.record(locale)
	return fmt.Sprintf("date:%s:%s", locale, value.UTC().Format(time.RFC3339))
}

func (f *recordingLocaleFormatter) FormatRelativeTime(locale string, value, now time.Time) string {
	f.record(locale)
	return fmt.Sprintf("relative:%s:%s", locale, now.Sub(value))
}

func (f *recordingLocaleFormatter) PluralCategory(locale string, count int) string {
	f.record(locale)
	return domainplugin.PluralCategory(locale, count)
}

func (f *recordingLocaleFormatter) Direction(string) appplugin.UIDirection {
	return appplugin.UIDirectionLTR
}

func trimFloat(value float64) string {
	return fmt.Sprintf("%g", value)
}
