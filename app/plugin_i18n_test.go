package app

import (
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// TestPluginLocaleFormatterUsesHostCLDRAndScript proves the app formatter, not
// a hand-rolled language list, answers plural categories and direction.
func TestPluginLocaleFormatterUsesHostCLDRAndScript(t *testing.T) {
	formatter := pluginLocaleFormatter{}

	plurals := []struct {
		locale string
		count  int
		want   string
	}{
		{"en", 1, "one"},
		{"en", 2, "other"},
		// CLDR operands use the absolute value.
		{"en", -1, "one"},
		{"ru", -2, "few"},
		{"ru", 5, "many"},
		{"zh-CN", 1, "other"},
		// Spanish has a CLDR "many" category for large magnitudes.
		{"es", 1000000, "many"},
	}
	for _, test := range plurals {
		if got := formatter.PluralCategory(test.locale, test.count); got != test.want {
			t.Fatalf("PluralCategory(%s, %d) = %q, want %q",
				test.locale, test.count, got, test.want)
		}
	}

	directions := []struct {
		locale string
		want   appplugin.UIDirection
	}{
		{"en", appplugin.UIDirectionLTR},
		{"ar", appplugin.UIDirectionRTL},
		{"he-IL", appplugin.UIDirectionRTL},
		// The script, not just the language, decides.
		{"az-Arab", appplugin.UIDirectionRTL},
	}
	for _, test := range directions {
		if got := formatter.Direction(test.locale); got != test.want {
			t.Fatalf("Direction(%s) = %q, want %q", test.locale, got, test.want)
		}
	}

	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if got := formatter.FormatRelativeTime("en", now.Add(-2*time.Hour), now); got != "2 hours ago" {
		t.Fatalf("relative time = %q", got)
	}
}
