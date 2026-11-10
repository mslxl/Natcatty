package plugin

import (
	"testing"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// TestDefaultLocaleFormatterHasNoDisplayStrings keeps ADR-0009's rule for
// domain and application code: the headless fallback emits a signed duration,
// not English prose. The app supplies the localized formatter.
func TestDefaultLocaleFormatterHasNoDisplayStrings(t *testing.T) {
	formatter := defaultLocaleFormatter{}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if got := formatter.FormatRelativeTime("en", now.Add(-2*time.Hour), now); got != "-2h0m0s" {
		t.Fatalf("past relative time = %q", got)
	}
	if got := formatter.FormatRelativeTime("en", now.Add(3*time.Hour), now); got != "3h0m0s" {
		t.Fatalf("future relative time = %q", got)
	}
	if got := formatter.FormatNumber("de", 1234.5); got != "1234.5" {
		t.Fatalf("number = %q", got)
	}
	if got := formatter.FormatDate("de", now); got != "2026-11-07" {
		t.Fatalf("date = %q", got)
	}
}

func internalCatalogs() domainplugin.Catalogs {
	return domainplugin.NewCatalogs("en", map[string]domainplugin.Catalog{
		"en": {"greet": "Hello, {name}!", "panel_title": "System", "panel_action": "Refresh"},
		"es": {"greet": "Hola, {name}!"},
	})
}

func TestResolveLocalizedAppliesTheHostLocaleAndCountsMisses(t *testing.T) {
	host := NewHostServices(ServiceDeps{Locale: "es-MX"})
	t.Cleanup(host.CloseAll)
	manager := &Manager{services: host}
	fiber := domainplugin.FiberID("installed:acme.i18n:1")

	if got := manager.resolveLocalized(internalCatalogs(), fiber, "greet", "Hi"); got != "Hola, {name}!" {
		t.Fatalf("localized greet = %q", got)
	}
	if got := manager.resolveLocalized(internalCatalogs(), fiber, "panel_title", "System"); got != "System" {
		t.Fatalf("default fallback = %q", got)
	}
	if got := manager.resolveLocalized(internalCatalogs(), fiber, "absent", "Literal"); got != "Literal" {
		t.Fatalf("literal fallback = %q", got)
	}
	if got := manager.resolveLocalized(internalCatalogs(), fiber, "absent", ""); got != "absent" {
		t.Fatalf("key fallback = %q", got)
	}
	if got := manager.resolveLocalized(internalCatalogs(), fiber, "", "literal"); got != "literal" {
		t.Fatalf("empty key = %q", got)
	}

	// greet resolved through the es family catalog, not the exact es-MX tag,
	// so it is a fallback too.
	missing := host.MissingKeys(fiber)
	if len(missing) != 3 ||
		missing[0].Key != "absent" || missing[0].Count != 2 ||
		missing[1].Key != "greet" || missing[1].Count != 1 ||
		missing[2].Key != "panel_title" || missing[2].Count != 1 {
		t.Fatalf("missing keys = %+v", missing)
	}
	if got := manager.Localize(PluginRef{ID: "acme.missing"}, "greet", "Hi"); got != "Hi" {
		t.Fatalf("Localize without a live fiber = %q", got)
	}
	if got := manager.Localize(PluginRef{ID: "acme.missing"}, "greet", ""); got != "greet" {
		t.Fatalf("Localize without a literal = %q", got)
	}
}

func TestLocalizedSectionResolvesTitlesAndActionLabels(t *testing.T) {
	panel := NewSystemPanel(PanelDeps{})
	t.Cleanup(panel.Close)
	panel.SetLocalize(func(_ PluginRef, key, literal string) string {
		switch key {
		case "panel_title":
			return "Sistema"
		case "panel_action":
			return "Actualizar"
		default:
			return literal
		}
	})
	section := domainplugin.PanelSection{
		ID: "status", Title: "System", TitleKey: "panel_title",
		Actions: []domainplugin.PanelAction{
			{ID: "refresh", Label: "Refresh", LabelKey: "panel_action"},
			{ID: "plain", Label: "Plain"},
		},
	}
	localized := panel.localizedSection(PluginRef{ID: "acme.i18n"}, section)
	if localized.Title != "Sistema" {
		t.Fatalf("section title = %q", localized.Title)
	}
	if localized.Actions[0].Label != "Actualizar" || localized.Actions[1].Label != "Plain" {
		t.Fatalf("action labels = %+v", localized.Actions)
	}
	if section.Title != "System" || section.Actions[0].Label != "Refresh" {
		t.Fatalf("localization mutated the manifest: %+v", section)
	}

	panel.SetLocalize(nil)
	if got := panel.localizedSection(PluginRef{ID: "acme.i18n"}, section).Title; got != "System" {
		t.Fatalf("title without a localizer = %q", got)
	}
}
