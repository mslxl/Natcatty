package terminal

import (
	"strings"
	"testing"
)

func TestDefaultKeywordHighlightRulesMatchTheOriginalCatalog(t *testing.T) {
	rules := DefaultKeywordHighlightRules()
	if len(rules) != 7 {
		t.Fatalf("rules = %d, want the original 7", len(rules))
	}
	want := []string{"error", "warning", "ok", "info", "debug", "ip-mac", "unix-path"}
	colors := map[string]string{
		"error": "#F87171", "warning": "#FBBF24", "ok": "#34D399",
		"info": "#3B82F6", "debug": "#A78BFA", "ip-mac": "#EC4899",
		"unix-path": "#A78BFA",
	}
	for index, rule := range rules {
		if rule.ID != want[index] {
			t.Fatalf("rule %d id = %q, want %q", index, rule.ID, want[index])
		}
		if !rule.Enabled || len(rule.Patterns) == 0 {
			t.Fatalf("default rule %q is not enabled with patterns", rule.ID)
		}
		if rule.Color != colors[rule.ID] {
			t.Fatalf("rule %q color = %q, want %q", rule.ID, rule.Color, colors[rule.ID])
		}
	}
}

func TestHighlighterMatchesTheConfiguredPatternsCaseInsensitively(t *testing.T) {
	highlighter := NewHighlighter(true, []KeywordHighlightRule{
		{ID: "error", Patterns: []string{`\berror\b`, `\bfatal\b`}, Color: "#F87171", Enabled: true},
		{ID: "ok", Patterns: []string{`\bok\b`}, Color: "#34D399", Enabled: true},
	})
	matches := highlighter.Matches("an ERROR and a fatal problem, but not an okay")
	if len(matches) != 2 {
		t.Fatalf("matches = %+v, want ERROR and fatal", matches)
	}
	if matches[0].Text("an ERROR and a fatal problem, but not an okay") != "ERROR" {
		t.Fatalf("first match = %+v", matches[0])
	}
	if matches[1].Text("an ERROR and a fatal problem, but not an okay") != "fatal" {
		t.Fatalf("second match = %+v", matches[1])
	}
	if matches[0].Color != "#F87171" || matches[1].Color != "#F87171" {
		t.Fatalf("colors = %q %q", matches[0].Color, matches[1].Color)
	}
}

func TestHighlighterKeepsTheEarliestThenHighestPriorityMatch(t *testing.T) {
	highlighter := NewHighlighter(true, []KeywordHighlightRule{
		{ID: "first", Patterns: []string{`error`}, Color: "#111111", Enabled: true},
		{ID: "second", Patterns: []string{`error`}, Color: "#222222", Enabled: true},
		{ID: "long", Patterns: []string{`error code`}, Color: "#333333", Enabled: true},
	})
	matches := highlighter.Matches("error code")
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want one non-overlapping match", matches)
	}
	// Same start: the earlier rule wins, matching the original priority order.
	if matches[0].Color != "#111111" || matches[0].RuleID != "first" {
		t.Fatalf("match = %+v, want the first rule", matches[0])
	}
}

func TestHighlighterAppliesSGRWithoutTouchingTerminalSequences(t *testing.T) {
	highlighter := NewHighlighter(true, []KeywordHighlightRule{
		{ID: "error", Patterns: []string{`ERROR`}, Color: "#F87171", Enabled: true},
	})
	input := "\x1b[32mERROR\x1b[0m plain ERROR\x07"
	got := string(highlighter.Apply([]byte(input)))
	want := "\x1b[32m\x1b[38;2;248;113;113mERROR\x1b[39m\x1b[0m plain \x1b[38;2;248;113;113mERROR\x1b[39m\x07"
	if got != want {
		t.Fatalf("apply = %q, want %q", got, want)
	}
	// The escape sequences themselves must stay untouched and in order.
	if strings.Count(got, "\x1b[") != 6 {
		t.Fatalf("apply changed the sequence count: %q", got)
	}
}

func TestHighlighterIgnoresDisabledRulesInvalidPatternsAndColors(t *testing.T) {
	highlighter := NewHighlighter(true, []KeywordHighlightRule{
		{ID: "off", Patterns: []string{`ERROR`}, Color: "#F87171", Enabled: false},
		{ID: "bad-pattern", Patterns: []string{`(`}, Color: "#F87171", Enabled: true},
		{ID: "bad-color", Patterns: []string{`ERROR`}, Color: "not a color", Enabled: true},
		{ID: "short", Patterns: []string{`WARN`}, Color: "#abc", Enabled: true},
	})
	if matches := highlighter.Matches("ERROR WARN"); len(matches) != 1 ||
		matches[0].RuleID != "short" || matches[0].Color != "#abc" {
		t.Fatalf("matches = %+v, want only the short-color WARN rule", matches)
	}
	off := NewHighlighter(false, []KeywordHighlightRule{
		{ID: "error", Patterns: []string{`ERROR`}, Color: "#F87171", Enabled: true},
	})
	if matches := off.Matches("ERROR"); len(matches) != 0 {
		t.Fatalf("disabled highlighter matched %+v", matches)
	}
	if got := string(off.Apply([]byte("ERROR"))); got != "ERROR" {
		t.Fatalf("disabled highlighter changed output to %q", got)
	}
}

func TestNormalizeKeywordHighlightRulesFallsBackToTheCatalog(t *testing.T) {
	if got := NormalizeKeywordHighlightRules(nil); len(got) != 7 {
		t.Fatalf("empty rules = %d, want the default catalog", len(got))
	}
	custom := []KeywordHighlightRule{
		{ID: "custom", Patterns: []string{`X`}, Color: "#123456", Enabled: true},
		{ID: "", Patterns: []string{`Y`}, Color: "#123456", Enabled: true},
		{ID: "bad", Patterns: []string{`Z`}, Color: "blue", Enabled: true},
		{ID: "empty", Patterns: nil, Color: "#123456", Enabled: true},
	}
	got := NormalizeKeywordHighlightRules(custom)
	if len(got) != 1 || got[0].ID != "custom" {
		t.Fatalf("normalized = %+v, want only the custom rule", got)
	}
}

func TestHighlighterLeavesIncompleteTrailingEscapeAlone(t *testing.T) {
	highlighter := NewHighlighter(true, []KeywordHighlightRule{
		{ID: "ok", Patterns: []string{`OK`}, Color: "#34D399", Enabled: true},
	})
	input := "OK\x1b[3"
	want := "\x1b[38;2;52;211;153mOK\x1b[39m\x1b[3"
	if got := string(highlighter.Apply([]byte(input))); got != want {
		t.Fatalf("apply = %q, want %q", got, want)
	}
}
