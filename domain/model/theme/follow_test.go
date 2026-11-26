package theme

import "testing"

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
