package terminal

import "testing"

func TestPowerUxDefaultsMatchTheOriginal(t *testing.T) {
	settings := Defaults()
	if !settings.AutocompleteEnabled || settings.AutocompleteGhostText ||
		!settings.AutocompletePopupMenu || settings.AutocompleteDebounceMs != 100 ||
		settings.AutocompleteMinChars != 1 || settings.AutocompleteMaxSuggestions != 50 ||
		settings.AutocompleteHistoryScope != AutocompleteHistoryHost {
		t.Fatalf("autocomplete defaults = %+v", settings)
	}
	if !settings.KeywordHighlightEnabled || len(settings.KeywordHighlightRules) != 7 {
		t.Fatalf("keyword highlight defaults = enabled %t, rules %d",
			settings.KeywordHighlightEnabled, len(settings.KeywordHighlightRules))
	}
	if !settings.ShowSessionStats {
		t.Fatal("session statistics are hidden by default")
	}
}

func TestNormalizeClampsAutocompleteAndFallsBackForUnknownValues(t *testing.T) {
	settings := Normalize(Settings{
		AutocompleteMinChars:       0,
		AutocompleteMaxSuggestions: 100000,
		AutocompleteDebounceMs:     -5,
		AutocompleteHistoryScope:   AutocompleteHistoryScope("everywhere"),
		KeywordHighlightRules: []KeywordHighlightRule{
			{ID: "custom", Patterns: []string{`boom`}, Color: "#123456", Enabled: true},
		},
	})
	if settings.AutocompleteMinChars != 1 {
		t.Fatalf("min chars = %d, want 1", settings.AutocompleteMinChars)
	}
	if settings.AutocompleteMaxSuggestions != 200 {
		t.Fatalf("max suggestions = %d, want 200", settings.AutocompleteMaxSuggestions)
	}
	if settings.AutocompleteDebounceMs != 100 {
		t.Fatalf("debounce = %d, want the original 100", settings.AutocompleteDebounceMs)
	}
	if settings.AutocompleteHistoryScope != AutocompleteHistoryHost {
		t.Fatalf("history scope = %q, want host", settings.AutocompleteHistoryScope)
	}
	if len(settings.KeywordHighlightRules) != 1 ||
		settings.KeywordHighlightRules[0].ID != "custom" {
		t.Fatalf("rules = %+v, want the custom rule", settings.KeywordHighlightRules)
	}
}

func TestNormalizeKeepsAutocompleteAndStatisticsValues(t *testing.T) {
	input := Settings{
		AutocompleteEnabled:        false,
		AutocompleteGhostText:      true,
		AutocompletePopupMenu:      false,
		AutocompleteDebounceMs:     250,
		AutocompleteMinChars:       2,
		AutocompleteMaxSuggestions: 20,
		AutocompleteHistoryScope:   AutocompleteHistoryGlobal,
		KeywordHighlightEnabled:    false,
		ShowSessionStats:           false,
	}
	got := Normalize(input)
	if got.AutocompleteEnabled != input.AutocompleteEnabled ||
		got.AutocompleteGhostText != input.AutocompleteGhostText ||
		got.AutocompletePopupMenu != input.AutocompletePopupMenu ||
		got.AutocompleteDebounceMs != input.AutocompleteDebounceMs ||
		got.AutocompleteMinChars != input.AutocompleteMinChars ||
		got.AutocompleteMaxSuggestions != input.AutocompleteMaxSuggestions ||
		got.AutocompleteHistoryScope != input.AutocompleteHistoryScope ||
		got.KeywordHighlightEnabled != input.KeywordHighlightEnabled ||
		got.ShowSessionStats != input.ShowSessionStats {
		t.Fatalf("normalize changed a valid value: %+v", got)
	}
}

func TestAutocompleteHistoryScopeValid(t *testing.T) {
	for _, scope := range []AutocompleteHistoryScope{AutocompleteHistoryHost, AutocompleteHistoryGlobal} {
		if !scope.Valid() {
			t.Fatalf("%q is not valid", scope)
		}
	}
	if AutocompleteHistoryScope("all").Valid() {
		t.Fatal("an unknown history scope is valid")
	}
}

func TestAutocompleteValuesAreBounded(t *testing.T) {
	if got := NormalizeAutocompleteMinChars(-4); got != 1 {
		t.Fatalf("min chars(-4) = %d, want 1", got)
	}
	if got := NormalizeAutocompleteMinChars(500); got != 100 {
		t.Fatalf("min chars(500) = %d, want 100", got)
	}
	if got := NormalizeAutocompleteMaxSuggestions(0); got != 50 {
		t.Fatalf("max suggestions(0) = %d, want 50", got)
	}
	if got := NormalizeAutocompleteMaxSuggestions(999); got != 200 {
		t.Fatalf("max suggestions(999) = %d, want 200", got)
	}
	if got := NormalizeAutocompleteDebounce(-1); got != 100 {
		t.Fatalf("debounce(-1) = %d, want 100", got)
	}
	if got := NormalizeAutocompleteDebounce(50); got != 50 {
		t.Fatalf("debounce(50) = %d, want 50", got)
	}
}
