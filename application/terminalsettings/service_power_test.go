package terminalsettings

import (
	"testing"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

func TestPowerUxSettersPersist(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	steps := []struct {
		name   string
		apply  func() error
		assert func(domainterminal.Settings) bool
	}{
		{"autocomplete off", func() error { return service.SetAutocompleteEnabled(false) },
			func(s domainterminal.Settings) bool { return !s.AutocompleteEnabled }},
		{"ghost text", func() error { return service.SetAutocompleteGhostText(true) },
			func(s domainterminal.Settings) bool { return s.AutocompleteGhostText }},
		{"popup off", func() error { return service.SetAutocompletePopupMenu(false) },
			func(s domainterminal.Settings) bool { return !s.AutocompletePopupMenu }},
		{"debounce", func() error { return service.SetAutocompleteDebounceMs(250) },
			func(s domainterminal.Settings) bool { return s.AutocompleteDebounceMs == 250 }},
		{"min chars", func() error { return service.SetAutocompleteMinChars(3) },
			func(s domainterminal.Settings) bool { return s.AutocompleteMinChars == 3 }},
		{"max suggestions", func() error { return service.SetAutocompleteMaxSuggestions(12) },
			func(s domainterminal.Settings) bool { return s.AutocompleteMaxSuggestions == 12 }},
		{"global scope", func() error { return service.SetAutocompleteHistoryScope(domainterminal.AutocompleteHistoryGlobal) },
			func(s domainterminal.Settings) bool {
				return s.AutocompleteHistoryScope == domainterminal.AutocompleteHistoryGlobal
			}},
		{"highlight off", func() error { return service.SetKeywordHighlightEnabled(false) },
			func(s domainterminal.Settings) bool { return !s.KeywordHighlightEnabled }},
	}
	for _, step := range steps {
		if err := step.apply(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if !step.assert(service.Settings()) {
			t.Fatalf("%s did not persist: %+v", step.name, service.Settings())
		}
		if !step.assert(store.settings) {
			t.Fatalf("%s did not reach the store: %+v", step.name, store.settings)
		}
	}
}

func TestAutocompleteBoundsAreClampedByTheService(t *testing.T) {
	service := NewService(&fakeStore{})
	if err := service.SetAutocompleteMinChars(0); err != nil {
		t.Fatal(err)
	}
	if got := service.Settings().AutocompleteMinChars; got != domainterminal.DefaultAutocompleteMinChars {
		t.Fatalf("min chars = %d", got)
	}
	if err := service.SetAutocompleteMaxSuggestions(10000); err != nil {
		t.Fatal(err)
	}
	if got := service.Settings().AutocompleteMaxSuggestions; got != domainterminal.MaxAutocompleteMaxSuggestions {
		t.Fatalf("max suggestions = %d", got)
	}
	if err := service.SetAutocompleteDebounceMs(-1); err != nil {
		t.Fatal(err)
	}
	if got := service.Settings().AutocompleteDebounceMs; got != domainterminal.DefaultAutocompleteDebounceMs {
		t.Fatalf("debounce = %d", got)
	}
}

func TestInvalidHistoryScopeIsIgnored(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	if err := service.SetAutocompleteHistoryScope(domainterminal.AutocompleteHistoryScope("bogus")); err != nil {
		t.Fatal(err)
	}
	if store.saves != 0 {
		t.Fatalf("invalid scope reached the store")
	}
	if got := service.Settings().AutocompleteHistoryScope; got != domainterminal.AutocompleteHistoryHost {
		t.Fatalf("scope = %q", got)
	}
}

func TestSetKeywordHighlightRulesReplacesAndNormalizes(t *testing.T) {
	service := NewService(&fakeStore{})
	rules := []domainterminal.KeywordHighlightRule{
		{ID: "custom", Patterns: []string{`boom`}, Color: "#123456", Enabled: true},
	}
	if err := service.SetKeywordHighlightRules(rules); err != nil {
		t.Fatal(err)
	}
	got := service.Settings().KeywordHighlightRules
	if len(got) != 1 || got[0].ID != "custom" {
		t.Fatalf("rules = %+v", got)
	}
	if err := service.SetKeywordHighlightRules(nil); err != nil {
		t.Fatal(err)
	}
	if got := service.Settings().KeywordHighlightRules; len(got) != 7 {
		t.Fatalf("cleared rules = %+v, want the default catalog", got)
	}
}
