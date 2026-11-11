package kit

import (
	"image/color"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestToneForPercentAppliesTheSharedThresholds proves the gauge and bar tone
// boundaries are exact: above 85 is danger, above 60 warning, otherwise accent.
func TestToneForPercentAppliesTheSharedThresholds(t *testing.T) {
	tests := []struct {
		value float64
		want  GaugeTone
	}{
		{0, GaugeAccent},
		{60, GaugeAccent},
		{60.1, GaugeWarning},
		{85, GaugeWarning},
		{85.1, GaugeDanger},
		{100, GaugeDanger},
		{math.NaN(), GaugeAccent},
		{math.Inf(1), GaugeAccent},
	}
	for _, test := range tests {
		if got := ToneForPercent(test.value); got != test.want {
			t.Errorf("ToneForPercent(%v) = %v, want %v", test.value, got, test.want)
		}
	}
}

// TestNetworkGaugePercentUsesTheSyntheticLogScale proves the Network card's
// gauge maps bytes per second onto the original's 0–100 scale, and keeps an
// unknown rate unknown.
func TestNetworkGaugePercentUsesTheSyntheticLogScale(t *testing.T) {
	tests := []struct {
		value float64
		want  float64
	}{
		{math.NaN(), math.NaN()},
		{0, 0},
		{9, 14},
		{99, 28},
		{1 << 30, 100},
		{-5, 0},
	}
	for _, test := range tests {
		got := NetworkGaugePercent(test.value)
		switch {
		case math.IsNaN(test.want) && !math.IsNaN(got):
			t.Errorf("NetworkGaugePercent(%v) = %v, want unknown", test.value, got)
		case !math.IsNaN(test.want) && math.Abs(got-test.want) > 0.001:
			t.Errorf("NetworkGaugePercent(%v) = %v, want %v", test.value, got, test.want)
		}
	}
}

// TestResourceBarFillAndThresholdColors proves the bar's painted fill follows
// the percent: the fraction is visible, and 90, 70, and 50 percent use the
// Danger, Warning, and Accent colors, in both Interface Styles.
func TestResourceBarFillAndThresholdColors(t *testing.T) {
	t.Cleanup(func() { SetStyle(nil) })
	theme := ui.LightTheme()
	render := func(percent float64) (*ui.Tester, ui.Rect) {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).FillWidth().Padding(8).Children(func() {
				ResourceBar(c, percent, ResourceBarOptions{Height: 8, Label: "usage bar"})
			})
		}, 208, 40)
		tt.Frame()
		rect, ok := tt.Find("usage bar")
		if !ok {
			t.Fatalf("bar for %v drew no labeled element: %v", percent, tt.Texts())
		}
		return tt, rect
	}
	sample := func(percent, fraction float64) (bool, color.RGBA) {
		tt, rect := render(percent)
		image := tt.Image()
		x := int(rect.X + rect.W*float32(fraction))
		y := int(rect.Y + rect.H/2)
		for _, tone := range []GaugeTone{GaugeAccent, GaugeWarning, GaugeDanger} {
			if regionHasColor(image.RGBAAt, x, y, 1, GaugeColor(theme, tone)) {
				return true, image.RGBAAt(x, y)
			}
		}
		return false, image.RGBAAt(x, y)
	}

	for _, style := range []Style{Classic, MacOS} {
		SetStyle(style)
		for _, test := range []struct {
			percent float64
			tone    GaugeTone
		}{
			{90, GaugeDanger},
			{70, GaugeWarning},
			{50, GaugeAccent},
		} {
			tt, rect := render(test.percent)
			image := tt.Image()
			x := int(rect.X + rect.W*0.25)
			y := int(rect.Y + rect.H/2)
			want := GaugeColor(theme, test.tone)
			if !regionHasColor(image.RGBAAt, x, y, 1, want) {
				t.Errorf("%s: %v%% fill at (%d,%d) is not %v: %v",
					style.ID(), test.percent, x, y, want, regionColors(image.RGBAAt, x, y, 1))
			}
		}

		// A half-full bar is painted at a quarter but not at three quarters;
		// a full bar is painted there too. Unknown values leave only the track.
		if filled, _ := sample(100, 0.75); !filled {
			t.Errorf("%s: a 100%% bar is not filled at three quarters", style.ID())
		}
		if filled, _ := sample(50, 0.75); filled {
			t.Errorf("%s: a 50%% bar is filled past its halfway point", style.ID())
		}
		for _, percent := range []float64{0, math.NaN()} {
			if filled, _ := sample(percent, 0.25); filled {
				t.Errorf("%s: a %v bar is filled", style.ID(), percent)
			}
		}
	}
}

// TestResourceBarRendersKnownAndUnknownInBothStyles proves the bar draws at
// the requested height for 0, partial, full, and unknown percentages, with its
// label kept for assistive technology and tests, in Classic and macOS.
func TestResourceBarRendersKnownAndUnknownInBothStyles(t *testing.T) {
	t.Cleanup(func() { SetStyle(nil) })
	for _, style := range []Style{Classic, MacOS} {
		SetStyle(style)
		for _, percent := range []float64{math.NaN(), 0, 50, 100} {
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).FillWidth().Padding(8).Children(func() {
					ResourceBar(c, percent, ResourceBarOptions{Height: 8, Label: "disk usage bar"})
				})
			}, 200, 80)
			tt.Frame()
			rect, ok := tt.Find("disk usage bar")
			if !ok {
				t.Fatalf("%s: bar for %v drew no labeled element: %v",
					style.ID(), percent, tt.Texts())
			}
			if rect.H < 7 || rect.H > 9 {
				t.Fatalf("%s: bar for %v is %v high, want 8", style.ID(), percent, rect.H)
			}
		}
	}
}
