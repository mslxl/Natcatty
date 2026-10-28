package theme

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

// MinimumTextContrast is the WCAG AA contrast target for normal-size body
// text.
const MinimumTextContrast = 4.5

// DangerText returns a danger color readable as body text on the theme's
// background and surface. The theme's Danger token is tuned for borders and
// filled buttons; as small text on a light fill it measures about 4.47:1, and
// on the default dark fill about 3.6:1, both below the body-text target. Light
// mode starts from a darker red; dark mode starts from a light red and
// brightens only as far as the preset's fills demand.
func DangerText(t *ui.Theme) ui.Color {
	return readableText(t, []ui.Color{
		ui.Hex("#b51616"), ui.Hex("#8a1010"), ui.Hex("#6e0c0c"),
		ui.Hex("#500808"), ui.Hex("#000000"),
	}, []ui.Color{
		ui.Hex("#ff6b6b"), ui.Hex("#ff9999"), ui.Hex("#ffb3b3"),
		ui.Hex("#ffd1d1"), ui.Hex("#ffffff"),
	})
}

// WarningText returns a warning color readable as body text on the theme's
// background and surface. The light warning token is tuned for icons and
// borders and falls below the body-text target on light fills; dark mode keeps
// the bright token when it clears the target.
func WarningText(t *ui.Theme) ui.Color {
	light := []ui.Color{
		ui.Hex("#9a5c00"), ui.Hex("#7a4800"), ui.Hex("#5c3600"),
		ui.Hex("#3d2400"), ui.Hex("#000000"),
	}
	dark := []ui.Color{
		t.Warning, ui.Hex("#ffb84d"), ui.Hex("#ffd166"),
		ui.Hex("#ffe0a3"), ui.Hex("#fff3d6"),
	}
	return readableText(t, light, dark)
}

// AccentBodyText returns an accent color readable as body text on the theme's
// background and surface. The Accent token is tuned for fills, focus rings,
// and primary buttons; as small text on a light fill it measures below the
// body-text target (the Snow accent is about 3.5:1 on white). Moving the
// accent toward black in a light theme, or white in a dark theme, keeps its
// hue while reaching the target.
func AccentBodyText(t *ui.Theme) ui.Color {
	candidate := t.Accent
	target := ui.Hex("#000000")
	if t.Dark {
		target = ui.Hex("#ffffff")
	}
	for step := 0; step < 20; step++ {
		if contrast(candidate, t.Background) >= MinimumTextContrast &&
			contrast(candidate, t.Surface) >= MinimumTextContrast {
			return candidate
		}
		candidate = mix(candidate, target, 0.15)
	}
	return candidate
}

// readableText picks the first candidate that clears the target against both
// fills text is drawn on, so one helper covers a plain panel and a card row.
// The last candidate is the fallback for a pathological theme.
func readableText(t *ui.Theme, light, dark []ui.Color) ui.Color {
	candidates := light
	if t.Dark {
		candidates = dark
	}
	for _, candidate := range candidates {
		if contrast(candidate, t.Background) >= MinimumTextContrast &&
			contrast(candidate, t.Surface) >= MinimumTextContrast {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

// contrast returns the WCAG contrast ratio of two opaque colors through the
// domain's single luminance implementation.
func contrast(a, b ui.Color) float64 {
	la, okA := domaintheme.HexLuminance(colorHex(a))
	lb, okB := domaintheme.HexLuminance(colorHex(b))
	if !okA || !okB {
		return 0
	}
	return domaintheme.ContrastRatio(la, lb)
}

func colorHex(c ui.Color) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}
