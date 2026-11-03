package theme

import "testing"

func TestTerminalThemeForPresetFollowsTheOriginal(t *testing.T) {
	cases := []struct {
		dark   bool
		preset string
		want   string
		ok     bool
	}{
		{false, "snow", "ui-snow", true},
		{true, "midnight", "ui-midnight", true},
		{false, "catppuccin", "system-catppuccin-light", true},
		{true, "catppuccin", "system-catppuccin-dark", true},
		{false, "no-such-preset", "", false},
	}
	for _, c := range cases {
		got, ok := TerminalThemeForPreset(c.dark, c.preset)
		if got != c.want || ok != c.ok {
			t.Errorf("TerminalThemeForPreset(%v, %q) = %q, %v; want %q, %v", c.dark, c.preset, got, ok, c.want, c.ok)
		}
	}
}

// TestEveryPresetMatchesAScheme keeps the follow mode total: each built-in UI
// preset has a scheme, and that scheme maps back to it.
func TestEveryPresetMatchesAScheme(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, preset := range Presets(dark) {
			id, ok := TerminalThemeForPreset(dark, preset.ID)
			if !ok {
				t.Errorf("preset %q (dark=%v) has no terminal scheme", preset.ID, dark)
				continue
			}
			back, backDark, ok := PresetForTerminalTheme(id)
			if !ok || back != preset.ID || backDark != dark {
				t.Errorf("%q maps back to %q dark=%v ok=%v, want %q dark=%v", id, back, backDark, ok, preset.ID, dark)
			}
		}
	}
}

func TestFollowAppTerminalThemesExcludeOrdinarySchemes(t *testing.T) {
	if IsFollowAppTerminalTheme(DefaultTerminalTheme) {
		t.Fatalf("%q is not a follow-app scheme", DefaultTerminalTheme)
	}
	themes := FollowAppTerminalThemes()
	if len(themes) != 55*2+len(uiMatchTerminalThemeIDs) {
		t.Fatalf("follow-app schemes = %d", len(themes))
	}
}
