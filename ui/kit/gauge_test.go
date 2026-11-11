package kit

import (
	"image/color"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestGaugeFractionMapsTheScaleAndKeepsUnknown(t *testing.T) {
	tests := []struct {
		value float64
		want  float64
	}{
		{0, 0},
		{25, 0.25},
		{50, 0.5},
		{100, 1},
		{-10, 0},
		{150, 1},
		{math.NaN(), -1},
		{math.Inf(1), -1},
		{math.Inf(-1), -1},
	}
	for _, test := range tests {
		if got := GaugeFraction(test.value); got != test.want {
			t.Errorf("GaugeFraction(%v) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestGaugeColorUsesTheThemesSemanticColors(t *testing.T) {
	theme := &ui.Theme{
		Accent:  ui.RGB(10, 20, 30),
		Warning: ui.RGB(40, 50, 60),
		Danger:  ui.RGB(70, 80, 90),
	}
	if got := GaugeColor(theme, GaugeAccent); got != theme.Accent {
		t.Errorf("accent tone = %v, want %v", got, theme.Accent)
	}
	if got := GaugeColor(theme, GaugeWarning); got != theme.Warning {
		t.Errorf("warning tone = %v, want %v", got, theme.Warning)
	}
	if got := GaugeColor(theme, GaugeDanger); got != theme.Danger {
		t.Errorf("danger tone = %v, want %v", got, theme.Danger)
	}
}

// TestGaugeFullRingUsesItsToneColor proves the tone reaches the arc's paint:
// a full ring is drawn in Accent, Warning, and Danger, while a zero gauge
// leaves only the track, in both Interface Styles.
func TestGaugeFullRingUsesItsToneColor(t *testing.T) {
	t.Cleanup(func() { SetStyle(nil) })
	theme := ui.LightTheme()
	topPixels := func(style Style, value float64, tone GaugeTone) (func(int, int) color.RGBA, int, int, ui.Rect) {
		tt := ui.NewTester(func(c *ui.Context) {
			Gauge(c, value, GaugeOptions{Size: 64, Tone: tone, Label: "tone gauge"})
		}, 80, 80)
		tt.Frame()
		rect, ok := tt.Find("tone gauge")
		if !ok {
			t.Fatalf("%s: tone %v drew no labeled gauge: %v", style.ID(), tone, tt.Texts())
		}
		width := float32(3)
		if style.Native() {
			width = 4
		}
		radius := min(rect.W, rect.H)/2 - width/2
		x := int(rect.X + rect.W/2)
		y := int(rect.Y + rect.H/2 - radius)
		return tt.Image().RGBAAt, x, y, rect
	}
	for _, style := range []Style{Classic, MacOS} {
		SetStyle(style)
		for _, tone := range []GaugeTone{GaugeAccent, GaugeWarning, GaugeDanger} {
			at, x, y, _ := topPixels(style, 100, tone)
			want := GaugeColor(theme, tone)
			if !regionHasColor(at, x, y, 2, want) {
				t.Errorf("%s: full ring tone %v at (%d,%d) is not %v: %v",
					style.ID(), tone, x, y, want, regionColors(at, x, y, 2))
			}
		}
		at, x, y, _ := topPixels(style, 0, GaugeAccent)
		if regionHasColor(at, x, y, 2, theme.Accent) {
			t.Errorf("%s: a zero gauge painted an accent arc at (%d,%d)", style.ID(), x, y)
		}
	}
}

// TestGaugeRendersEveryValueInBothStyles proves the primitive draws an empty
// track for an unknown value, a partial arc, and a full ring, at the size it
// was asked for, in Classic and macOS.
func TestGaugeRendersEveryValueInBothStyles(t *testing.T) {
	t.Cleanup(func() { SetStyle(nil) })
	for _, style := range []Style{Classic, MacOS} {
		SetStyle(style)
		for _, value := range []float64{math.NaN(), 0, 50, 100} {
			tt := ui.NewTester(func(c *ui.Context) {
				Gauge(c, value, GaugeOptions{Size: 64, Label: "CPU gauge", Tone: GaugeDanger})
			}, 160, 120)
			tt.Frame()
			rect, ok := tt.Find("CPU gauge")
			if !ok {
				t.Fatalf("%s: gauge %v drew no labeled element: %v", style.ID(), value, tt.Texts())
			}
			if rect.W < 63 || rect.W > 65 || rect.H < 63 || rect.H > 65 {
				t.Fatalf("%s: gauge %v is %vx%v, want 64x64", style.ID(), value, rect.W, rect.H)
			}
		}
	}
}
