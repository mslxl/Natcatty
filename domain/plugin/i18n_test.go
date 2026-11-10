package plugin

import (
	"strings"
	"testing"
)

func testCatalogs() Catalogs {
	return NewCatalogs("en", map[string]Catalog{
		"en": {
			"greet":        "Hello, {name}!",
			"files.one":    "{count} file",
			"files.other":  "{count} files",
			"files.many":   "{count} files (many)",
			"only_default": "Default only",
			"escaped":      "{{literal}} {name}",
		},
		"es": {
			"greet":       "Hola, {name}!",
			"files.one":   "{count} archivo",
			"files.other": "{count} archivos",
		},
		"pt": {
			"greet": "Olá, {name}!",
		},
	})
}

func TestCatalogFallbackChain(t *testing.T) {
	catalogs := testCatalogs()
	tests := []struct {
		name    string
		locale  string
		key     string
		literal string
		want    string
		stage   ResolutionStage
		missing bool
	}{
		{"exact tag", "es", "greet", "", "Hola, {name}!", StageExact, false},
		{"language only", "es-MX", "greet", "", "Hola, {name}!", StageLanguage, true},
		{"region falls through to default", "es-MX", "only_default", "", "Default only", StageDefault, true},
		{"literal fallback", "es-MX", "unknown", "Fallback", "Fallback", StageLiteral, true},
		{"key fallback", "es-MX", "unknown", "", "unknown", StageKey, true},
		{"default locale exact", "en", "only_default", "", "Default only", StageExact, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := catalogs.Resolve(test.locale, test.key, test.literal)
			if got.Text != test.want || got.Stage != test.stage || got.Missing != test.missing {
				t.Fatalf("Resolve = %+v, want text %q stage %q missing %v",
					got, test.want, test.stage, test.missing)
			}
		})
	}
}

func TestResolvePluralUsesSiblingThenBaseKey(t *testing.T) {
	catalogs := testCatalogs()
	got := catalogs.ResolvePlural("es", "files", "no translation", "other")
	if got.Text != "{count} archivos" || got.Stage != StageExact {
		t.Fatalf("plural other = %+v", got)
	}
	got = catalogs.ResolvePlural("es-MX", "files", "", "one")
	if got.Text != "{count} archivo" || got.Stage != StageLanguage {
		t.Fatalf("plural one via language = %+v", got)
	}
	// A locale whose sibling is untranslated falls back to the base key.
	base := NewCatalogs("en", map[string]Catalog{
		"en": {"files": "some files"},
	})
	got = base.ResolvePlural("en", "files", "", "one")
	if got.Text != "some files" || got.Stage != StageExact {
		t.Fatalf("plural base fallback = %+v", got)
	}
}

func TestEffectiveMergesDefaultLanguageAndExact(t *testing.T) {
	catalogs := testCatalogs()
	effective := catalogs.Effective("es-MX")
	if effective["only_default"] != "Default only" {
		t.Fatalf("default key = %q", effective["only_default"])
	}
	if effective["greet"] != "Hola, {name}!" {
		t.Fatalf("language key = %q", effective["greet"])
	}
	// Mutating the returned map never touches the package.
	effective["greet"] = "changed"
	if again := catalogs.Effective("es-MX"); again["greet"] != "Hola, {name}!" {
		t.Fatal("Effective returned a shared map")
	}
}

func TestRenderMessagePlaceholdersAndEscapes(t *testing.T) {
	tests := []struct {
		template string
		args     map[string]string
		want     string
	}{
		{"Hello, {name}!", map[string]string{"name": "Ada"}, "Hello, Ada!"},
		{"{{literal}} {name}", map[string]string{"name": "Ada"}, "{literal} Ada"},
		{"{unknown} stays", nil, "{unknown} stays"},
		{"{", nil, "{"},
		{"a } b", nil, "a } b"},
		{"{bad name}", map[string]string{"bad name": "x"}, "{bad name}"},
	}
	for _, test := range tests {
		if got := RenderMessage(test.template, test.args); got != test.want {
			t.Fatalf("RenderMessage(%q) = %q, want %q", test.template, got, test.want)
		}
	}
}

func TestPluralCategoryCLDRRules(t *testing.T) {
	tests := []struct {
		locale string
		count  int
		want   string
	}{
		{"en", 1, PluralOne},
		{"en", 2, PluralOther},
		{"en", -1, PluralOne},
		{"ru", 1, PluralOne},
		{"ru", 2, PluralFew},
		{"ru", 5, PluralMany},
		{"ru", 11, PluralMany},
		{"ru", 21, PluralOne},
		{"uk", 4, PluralFew},
		{"zh-CN", 1, PluralOther},
		{"ja", 2, PluralOther},
		{"es", 1, PluralOne},
		{"es", 0, PluralOther},
	}
	for _, test := range tests {
		if got := PluralCategory(test.locale, test.count); got != test.want {
			t.Fatalf("PluralCategory(%s, %d) = %q, want %q", test.locale, test.count, got, test.want)
		}
	}
}

func TestCatalogValidateEnforcesTheContract(t *testing.T) {
	valid := testCatalogs()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid catalogs: %v", err)
	}

	missingDefault := NewCatalogs("fr", map[string]Catalog{
		"en": {"greet": "Hello"},
	})
	if err := missingDefault.Validate(); err == nil ||
		!strings.Contains(err.Error(), "default locale \"fr\" has no catalog") {
		t.Fatalf("missing default = %v", err)
	}

	badTag := NewCatalogs("en", map[string]Catalog{
		"en":        {"greet": "Hello"},
		"not a tag": {"greet": "Hi"},
	})
	if err := badTag.Validate(); err == nil || !strings.Contains(err.Error(), "BCP 47") {
		t.Fatalf("bad tag = %v", err)
	}

	caseVariant := NewCatalogs("en", map[string]Catalog{
		"en": {"greet": "Hello"},
		"EN": {"greet": "Hello"},
	})
	if err := caseVariant.Validate(); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("case-variant locales = %v", err)
	}

	badKey := NewCatalogs("en", map[string]Catalog{
		"en": {"Bad Key": "x"},
	})
	if err := badKey.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("bad key = %v", err)
	}

	tooLarge := NewCatalogs("en", map[string]Catalog{
		"en": {"big": strings.Repeat("x", MaxMessageBytes+1)},
	})
	if err := tooLarge.Validate(); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("oversized message = %v", err)
	}

	manyLocales := make(map[string]Catalog, MaxLocales+1)
	for i := 0; i <= MaxLocales; i++ {
		manyLocales["en"] = map[string]string{"greet": "Hello"}
		if i > 0 {
			manyLocales[localeName(i)] = map[string]string{"greet": "Hello"}
		}
	}
	if err := NewCatalogs("en", manyLocales).Validate(); err == nil ||
		!strings.Contains(err.Error(), "locale limit") {
		t.Fatalf("locale limit = %v", err)
	}
}

// localeName builds a distinct two-letter tag: aa, ab, ... zz.
func localeName(index int) string {
	first := byte('a' + index/26%26)
	second := byte('a' + index%26)
	return string([]byte{first, second})
}

func TestManifestValidatesMessageKeys(t *testing.T) {
	manifest := func(command Command) Manifest {
		return Manifest{
			ManifestVersion: 1,
			ID:              "acme.i18n",
			Name:            "I18n",
			Version:         "1.0.0",
			PluginAPI:       "^0.1",
			ABI:             1,
			DefaultLocale:   "en",
			Wasm: Wasm{
				Path: "plugin.wasm", SHA256: strings.Repeat("a", 64),
			},
			Contributions: Contributions{Commands: []Command{command}},
		}
	}
	if err := manifest(Command{ID: "greet", TitleKey: "command_greet"}).Validate(); err != nil {
		t.Fatalf("title-only key rejected: %v", err)
	}
	if err := manifest(Command{ID: "greet", Title: "Hi", TitleKey: "Bad Key"}).Validate(); err == nil {
		t.Fatal("bad title_key accepted")
	}
	if err := manifest(Command{ID: "greet"}).Validate(); err == nil {
		t.Fatal("command without title or title_key accepted")
	}
}

func TestReferencedMessageKeysFollowDeclarationOrder(t *testing.T) {
	manifest := Manifest{
		Contributions: Contributions{
			Commands: []Command{{ID: "a", TitleKey: "command_a"}},
			Menus:    []Menu{{Command: "a", LabelKey: "menu_a"}},
			Views:    []ViewContribution{{ID: "v", TitleKey: "view_v", Placements: []Placement{PlacementPane}}},
			SystemPanel: []PanelSection{{
				ID: "s", TitleKey: "panel_s",
				Actions: []PanelAction{{ID: "act", LabelKey: "panel_action"}},
			}},
		},
		Settings: SettingsSchema{Fields: []SettingsField{{
			Key: "name", Type: SettingTypeString,
			LabelKey: "field_label", DescriptionKey: "field_description",
		}}},
	}
	refs := manifest.ReferencedMessageKeys()
	want := []string{"command_a", "menu_a", "view_v", "field_label", "field_description", "panel_s", "panel_action"}
	if len(refs) != len(want) {
		t.Fatalf("refs = %+v", refs)
	}
	for i, key := range want {
		if refs[i].Key != key || refs[i].Field == "" {
			t.Fatalf("ref %d = %+v, want %q", i, refs[i], key)
		}
	}
}
