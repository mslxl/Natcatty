package theme

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

// TestDangerAndWarningTextClearTheBodyContrastTarget keeps every appearance
// preset's body text readable on the two fills the UI draws text on. Ticket 61
// applies these helpers to raw danger and warning text across the surfaces.
func TestDangerAndWarningTextClearTheBodyContrastTarget(t *testing.T) {
	// Pin the ticket 53 values: the shared light red and Midnight's first
	// dark ladder red, so delegating the sync helpers cannot drift.
	snow, ok := domaintheme.LookupPreset(false, "snow")
	if !ok {
		t.Fatal("the Snow preset is missing")
	}
	if got := DangerText(UI(false, snow, Options{})); got != ui.Hex("#b51616") {
		t.Fatalf("light danger text = %+v, want #b51616", got)
	}
	midnight, ok := domaintheme.LookupPreset(true, "midnight")
	if !ok {
		t.Fatal("the Midnight preset is missing")
	}
	if got := DangerText(UI(true, midnight, Options{})); got != ui.Hex("#ff6b6b") {
		t.Fatalf("Midnight danger text = %+v, want #ff6b6b", got)
	}
	if got := WarningText(UI(false, snow, Options{})); got != ui.Hex("#9a5c00") {
		t.Fatalf("light warning text = %+v, want #9a5c00", got)
	}
	midnightTheme := UI(true, midnight, Options{})
	if got := WarningText(midnightTheme); got != midnightTheme.Warning {
		t.Fatalf("Midnight warning text = %+v, want the theme warning token %+v", got, midnightTheme.Warning)
	}

	for _, test := range []struct {
		name    string
		dark    bool
		presets []domaintheme.Preset
	}{
		{"light", false, domaintheme.LightPresets()},
		{"dark", true, domaintheme.DarkPresets()},
	} {
		for _, preset := range test.presets {
			uiTheme := UI(test.dark, preset, Options{})
			if ratio := contrast(DangerText(uiTheme), uiTheme.Background); ratio < MinimumTextContrast {
				t.Errorf("%s preset %s: danger text on the background = %.2f:1", test.name, preset.ID, ratio)
			}
			if ratio := contrast(DangerText(uiTheme), uiTheme.Surface); ratio < MinimumTextContrast {
				t.Errorf("%s preset %s: danger text on the surface = %.2f:1", test.name, preset.ID, ratio)
			}
			if ratio := contrast(WarningText(uiTheme), uiTheme.Background); ratio < MinimumTextContrast {
				t.Errorf("%s preset %s: warning text on the background = %.2f:1", test.name, preset.ID, ratio)
			}
			if ratio := contrast(WarningText(uiTheme), uiTheme.Surface); ratio < MinimumTextContrast {
				t.Errorf("%s preset %s: warning text on the surface = %.2f:1", test.name, preset.ID, ratio)
			}
		}
	}
}

// TestAccentBodyTextClearsTheBodyContrastTarget keeps accent-colored body text
// (the developer self-test overlay title and PASS label) readable on every
// appearance preset. The Snow accent is #0088ff, which measures 3.52:1 on
// white; AccentBodyText darkens it until it clears the target.
func TestAccentBodyTextClearsTheBodyContrastTarget(t *testing.T) {
	snow, ok := domaintheme.LookupPreset(false, "snow")
	if !ok {
		t.Fatal("the Snow preset is missing")
	}
	snowTheme := UI(false, snow, Options{})
	if ratio := contrast(snowTheme.Accent, snowTheme.Surface); ratio >= MinimumTextContrast {
		t.Fatalf("the Snow accent now clears the target by itself (%.2f:1); the helper's premise changed", ratio)
	}
	if ratio := contrast(AccentBodyText(snowTheme), snowTheme.Surface); ratio < MinimumTextContrast {
		t.Errorf("Snow accent body text on the surface = %.2f:1", ratio)
	}

	for _, test := range []struct {
		name    string
		dark    bool
		presets []domaintheme.Preset
	}{
		{"light", false, domaintheme.LightPresets()},
		{"dark", true, domaintheme.DarkPresets()},
	} {
		for _, preset := range test.presets {
			uiTheme := UI(test.dark, preset, Options{})
			text := AccentBodyText(uiTheme)
			if ratio := contrast(text, uiTheme.Background); ratio < MinimumTextContrast {
				t.Errorf("%s preset %s: accent body text on the background = %.2f:1", test.name, preset.ID, ratio)
			}
			if ratio := contrast(text, uiTheme.Surface); ratio < MinimumTextContrast {
				t.Errorf("%s preset %s: accent body text on the surface = %.2f:1", test.name, preset.ID, ratio)
			}
		}
	}
}

// TestPlaceholderTextClearsTheBodyContrastTarget pins the real placeholder
// color. Ticket 59's acceptance reported "Search the Vault" as #6786b4 at
// 3.72:1, but that value is a subpixel-antialiasing fringe: MyGo paints the
// placeholder with the theme's TextMuted, #676f7e on the Snow preset, which
// measures 5.05:1 on the white input surface (4.62:1 on the Snow background).
// Sampling the solid glyph pixels confirms the foreground; the fringe pixels
// must not be used as the text color.
func TestPlaceholderTextClearsTheBodyContrastTarget(t *testing.T) {
	snow, ok := domaintheme.LookupPreset(false, "snow")
	if !ok {
		t.Fatal("the Snow preset is missing")
	}
	uiTheme := UI(false, snow, Options{})
	if got, want := uiTheme.TextMuted, ui.Hex("#676f7e"); got != want {
		t.Fatalf("Snow placeholder color = %+v, want %+v", got, want)
	}
	for _, fill := range []struct {
		name  string
		color ui.Color
	}{
		{"surface", uiTheme.Surface},
		{"background", uiTheme.Background},
	} {
		if ratio := contrast(uiTheme.TextMuted, fill.color); ratio < MinimumTextContrast {
			t.Errorf("placeholder on the %s = %.2f:1", fill.name, ratio)
		}
	}
}
