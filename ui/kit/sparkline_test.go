package kit

import (
	"image/color"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSparklineScaleUsesAnExplicitMaxOrTheLargestFiniteSample(t *testing.T) {
	if got := SparklineScale([]float64{10, 50, 20}, 100); got != 100 {
		t.Errorf("explicit max = %v, want 100", got)
	}
	if got := SparklineScale([]float64{10, 50, 20}, 0); got != 50 {
		t.Errorf("auto max = %v, want 50", got)
	}
	if got := SparklineScale([]float64{math.NaN(), 4, 8}, 0); got != 8 {
		t.Errorf("auto max over finite samples = %v, want 8", got)
	}
	if got := SparklineScale(nil, 0); got != 1 {
		t.Errorf("empty auto max = %v, want 1", got)
	}
	if got := SparklineScale([]float64{0, 0}, 0); got != 1 {
		t.Errorf("flat zero auto max = %v, want 1", got)
	}
}

func TestSparklinePointsHandleEmptyOneAndTwentyFourSamples(t *testing.T) {
	if points := SparklinePoints(nil, 100); len(points) != 0 {
		t.Fatalf("empty history = %v, want no points", points)
	}
	if points := SparklinePoints([]float64{50}, 100); len(points) != 1 ||
		points[0][0] != 0.5 || points[0][1] != 0.5 {
		t.Fatalf("one sample = %v, want a centered half-height point", points)
	}
	values := make([]float64, 24)
	for index := range values {
		values[index] = float64(index * 4)
	}
	points := SparklinePoints(values, 0)
	if len(points) != 24 {
		t.Fatalf("24 samples = %d points, want 24", len(points))
	}
	if points[0][0] != 0 || points[23][0] != 1 {
		t.Errorf("24 samples span x %v..%v, want 0..1", points[0][0], points[23][0])
	}
	if points[0][1] != 0 || points[23][1] != 1 {
		t.Errorf("auto-scaled samples span y %v..%v, want 0..1", points[0][1], points[23][1])
	}
}

// TestSparklineSkipsUnknownValues proves an unknown reading never becomes a
// false zero point.
func TestSparklineSkipsUnknownValues(t *testing.T) {
	points := SparklinePoints([]float64{math.NaN(), 50, math.Inf(1), 100}, 100)
	if len(points) != 2 {
		t.Fatalf("finite samples = %d points, want 2: %v", len(points), points)
	}
	if points[0][0] != 0 || points[1][0] != 1 || points[1][1] != 1 {
		t.Fatalf("finite points = %v, want the two known samples spanning the strip", points)
	}
}

// TestSparklineSinglePointUsesItsToneColor proves the tone reaches the draw
// call: a one-sample line's dot is painted in the Accent, Warning, and Danger
// colors, in both Interface Styles.
func TestSparklineSinglePointUsesItsToneColor(t *testing.T) {
	t.Cleanup(func() { SetStyle(nil) })
	theme := ui.LightTheme()
	for _, style := range []Style{Classic, MacOS} {
		SetStyle(style)
		for _, tone := range []GaugeTone{GaugeAccent, GaugeWarning, GaugeDanger} {
			tt := ui.NewTester(func(c *ui.Context) {
				Sparkline(c, []float64{50}, SparklineOptions{
					Height: 24, Max: 100, Tone: tone, Label: "tone history",
				})
			}, 200, 80)
			tt.Frame()
			rect, ok := tt.Find("tone history")
			if !ok {
				t.Fatalf("%s: tone %v drew no labeled sparkline: %v", style.ID(), tone, tt.Texts())
			}
			image := tt.Image()
			x := int(rect.X + rect.W/2)
			y := int(rect.Y + 2 + (rect.H-4)/2)
			want := GaugeColor(theme, tone)
			if !regionHasColor(image.RGBAAt, x, y, 2, want) {
				t.Errorf("%s: tone %v dot at (%d,%d) is not %v: %v",
					style.ID(), tone, x, y, want, regionColors(image.RGBAAt, x, y, 2))
			}
		}
	}
}

// regionHasColor reports whether any pixel within radius of x,y has want.
func regionHasColor(at func(int, int) color.RGBA, x, y, radius int, want ui.Color) bool {
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			got := at(x+dx, y+dy)
			if got.R == want.R && got.G == want.G && got.B == want.B {
				return true
			}
		}
	}
	return false
}

// regionColors lists the distinct colors near x,y for a failure message.
func regionColors(at func(int, int) color.RGBA, x, y, radius int) []color.RGBA {
	seen := map[color.RGBA]bool{}
	var out []color.RGBA
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			got := at(x+dx, y+dy)
			if !seen[got] {
				seen[got] = true
				out = append(out, got)
			}
		}
	}
	return out
}

// TestSparklineRendersEmptyOneAndFullHistoryInBothStyles proves the primitive
// draws its frame at the requested height for every history shape in Classic
// and macOS.
func TestSparklineRendersEmptyOneAndFullHistoryInBothStyles(t *testing.T) {
	t.Cleanup(func() { SetStyle(nil) })
	values := make([]float64, 24)
	for index := range values {
		values[index] = float64(index)
	}
	for _, style := range []Style{Classic, MacOS} {
		SetStyle(style)
		for _, history := range [][]float64{nil, {50}, values} {
			tt := ui.NewTester(func(c *ui.Context) {
				Sparkline(c, history, SparklineOptions{
					Height: 24, Max: 100, Tone: GaugeWarning, Label: "CPU history",
				})
			}, 200, 80)
			tt.Frame()
			rect, ok := tt.Find("CPU history")
			if !ok {
				t.Fatalf("%s: %d samples drew no labeled sparkline: %v",
					style.ID(), len(history), tt.Texts())
			}
			if rect.H < 23 || rect.H > 25 {
				t.Fatalf("%s: %d samples drew height %v, want 24", style.ID(), len(history), rect.H)
			}
		}
	}
}
