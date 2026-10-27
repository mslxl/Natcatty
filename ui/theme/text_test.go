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
