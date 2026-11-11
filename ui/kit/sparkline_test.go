package kit

import (
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
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

// TestSparklineUntimedLineRisesWithItsValues proves an untimed series uses the
// same up-from-the-bottom coordinate chart draws: the larger value of a rising
// series ends near the top of the strip, and the same values falling end near
// the bottom. An inverted conversion would swap the two bands.
func TestSparklineUntimedLineRisesWithItsValues(t *testing.T) {
	line := ui.RGB(10, 20, 200)
	for _, c := range []struct {
		name   string
		values []float64
		rise   bool
	}{
		{"rising", []float64{20, 80}, true},
		{"falling", []float64{80, 20}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tt, rect := sparklineTester(t, c.values, SparklineOptions{
				Height: 40, Max: 100, Color: line, NoInset: true,
				Label: "orientation " + c.name,
			})
			// With 20 and 80 over a max of 100, the endpoint lands at 0.8 or
			// 0.2 of the height, well inside the strip. Only the right half is
			// counted, where the later reading sits.
			right := ui.Rect{X: rect.X + rect.W/2, W: rect.W / 2}
			high := right
			high.Y, high.H = rect.Y+4, 8 // rows around chart.Y(rect, 0.8)
			low := right
			low.Y, low.H = rect.Y+rect.H-12, 8 // rows around chart.Y(rect, 0.2)
			image := tt.Image().RGBAAt
			highCount := sparklineColorCount(image, high, line)
			lowCount := sparklineColorCount(image, low, line)
			if c.rise {
				if highCount == 0 {
					t.Fatalf("rising series left the top band empty (low band has %d pixels)", lowCount)
				}
				if lowCount != 0 {
					t.Fatalf("rising series painted %d pixels in the bottom band, so the line is inverted", lowCount)
				}
				return
			}
			if lowCount == 0 {
				t.Fatalf("falling series left the bottom band empty (top band has %d pixels)", highCount)
			}
			if highCount != 0 {
				t.Fatalf("falling series painted %d pixels in the top band, so the line is inverted", highCount)
			}
		})
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

// TestSparklineSinglePointSitsAtItsValueInItsExplicitColor proves a one-sample
// sparkline draws its dot at chart.Y of the sample, not mirrored, and in the
// explicit Color rather than the Tone's. The sample is a fifth of the max, so
// the two vertical conventions put the dot 24 DIPs apart.
func TestSparklineSinglePointSitsAtItsValueInItsExplicitColor(t *testing.T) {
	line := ui.RGB(10, 20, 200)
	tt, rect := sparklineTester(t, []float64{20}, SparklineOptions{
		Height: 40, Max: 100, Color: line, NoInset: true,
		Label: "single point position",
	})
	image := tt.Image().RGBAAt
	// NoInset makes the padded rect the labeled rect, so the sample at 0.2 of
	// the max sits at rect.Y + (1-0.2)*rect.H.
	x := int(rect.X + rect.W/2)
	wantY := int(rect.Y + (1-0.2)*rect.H)
	mirroredY := int(rect.Y + 0.2*rect.H)
	if !regionHasColor(image, x, wantY, 2, line) {
		t.Fatalf("the single sample's dot is not at (%d,%d), a fifth up from the bottom: %v",
			x, wantY, regionColors(image, x, wantY, 2))
	}
	if regionHasColor(image, x, mirroredY, 2, line) {
		t.Fatalf("the single sample's dot is mirrored to (%d,%d), a fifth down from the top",
			x, mirroredY)
	}
	// A tone-colored dot would not match the explicit color at all, so the
	// exact-color lookup above already pins Color over Tone; this names it.
	if regionHasColor(image, x, wantY, 2, GaugeColor(ui.LightTheme(), GaugeAccent)) {
		t.Fatal("the single sample's dot is painted in the accent tone, not its explicit color")
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

// sparklineTester renders one sparkline and returns its tester and rect.
func sparklineTester(t *testing.T, values []float64, opts SparklineOptions) (*ui.Tester, ui.Rect) {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		Sparkline(c, values, opts)
	}, 200, 80)
	tt.Frame()
	rect, ok := tt.Find(opts.Label)
	if !ok || rect.W <= 0 || rect.H <= 0 {
		t.Fatalf("%q is not rendered: %v", opts.Label, tt.Texts())
	}
	return tt, rect
}

// sparklineInk reports whether any pixel within radius of x,y differs from bg.
func sparklineInk(at func(int, int) color.RGBA, x, y, radius int, bg color.RGBA) bool {
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if at(x+dx, y+dy) != bg {
				return true
			}
		}
	}
	return false
}

// sparklineColorCount counts the pixels of a rect drawn in exactly want.
func sparklineColorCount(at func(int, int) color.RGBA, r ui.Rect, want ui.Color) int {
	count := 0
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			got := at(x, y)
			if got.R == want.R && got.G == want.G && got.B == want.B {
				count++
			}
		}
	}
	return count
}

// TestSparklineEndDotMarksTheLatestReading proves EndDot adds a disk around the
// final sample of an untimed series at chart.Y of that sample: the last value
// is a fifth of the max, so an inverted dot would sit near the top instead of
// near the bottom.
func TestSparklineEndDotMarksTheLatestReading(t *testing.T) {
	line := ui.RGB(10, 20, 200)
	values := []float64{100, 20}
	plainOpts := SparklineOptions{Height: 40, Max: 100, Color: line, NoInset: true,
		Label: "end-dot plain"}
	plain, plainRect := sparklineTester(t, values, plainOpts)
	dottedOpts := plainOpts
	dottedOpts.EndDot = true
	dottedOpts.Label = "end-dot marked"
	dotted, rect := sparklineTester(t, values, dottedOpts)

	plainPixels := sparklineColorCount(plain.Image().RGBAAt, plainRect, line)
	dottedPixels := sparklineColorCount(dotted.Image().RGBAAt, rect, line)
	if dottedPixels <= plainPixels {
		t.Fatalf("EndDot drew %d line-colored pixels, want more than the line's %d",
			dottedPixels, plainPixels)
	}
	// The dot's center is the line's endpoint: the last sample at x 1 and
	// chart.Y(0.2), a fifth up from the bottom. Two pixels below and left of
	// the center lie inside the disk but off the 1.5-DIP line.
	x := int(rect.X+rect.W) - 2
	y := int(chart.Y(rect, 0.2)) + 2
	if regionHasColor(plain.Image().RGBAAt, x, y, 0, line) {
		t.Fatalf("the plain line already paints the dot's area at (%d,%d)", x, y)
	}
	if !regionHasColor(dotted.Image().RGBAAt, x, y, 1, line) {
		t.Fatalf("the end dot is not drawn at (%d,%d): %v",
			x, y, regionColors(dotted.Image().RGBAAt, x, y, 1))
	}
	// The dot is not mirrored: a top-down fraction would put it at 0.2 of the
	// height, chart.Y of the complementary 0.8.
	if regionHasColor(dotted.Image().RGBAAt, x, int(chart.Y(rect, 0.8)), 1, line) {
		t.Fatalf("the end dot is mirrored to the top at (%d,%d)", x, int(chart.Y(rect, 0.8)))
	}
}

// TestSparklineTimesPositionSamples proves a timed sparkline places samples by
// their timestamps: a peak at one tenth of the span sits at one tenth of the
// width, while the same samples without times sit evenly spaced at the middle.
func TestSparklineTimesPositionSamples(t *testing.T) {
	line := ui.RGB(10, 20, 200)
	start := time.Unix(1_700_000_000, 0)
	values := []float64{0, 100, 0}
	times := []time.Time{start, start.Add(time.Second), start.Add(10 * time.Second)}

	timed := SparklineOptions{Height: 40, Max: 100, Color: line, Label: "timed"}
	timed.Times = times
	timedTester, timedRect := sparklineTester(t, values, timed)
	timedImage := timedTester.Image()
	// The strip is empty above the curve at its middle, so that pixel is the
	// background every other pixel is compared with.
	peakY := int(timedRect.Y + 2) // the 2-DIP inset puts the maximum here.
	bg := timedImage.RGBAAt(int(timedRect.X+timedRect.W/2), peakY)
	early := int(timedRect.X + timedRect.W*0.1)
	if !sparklineInk(timedImage.RGBAAt, early, peakY, 2, bg) {
		t.Fatalf("the timed peak is not at one tenth of the width (%d,%d)", early, peakY)
	}
	if sparklineInk(timedImage.RGBAAt,
		int(timedRect.X+timedRect.W/2), peakY, 1, bg) {
		t.Fatal("the timed peak is drawn at the middle, so times did not position it")
	}

	even := SparklineOptions{Height: 40, Max: 100, Color: line, Label: "even"}
	evenTester, evenRect := sparklineTester(t, values, even)
	evenImage := evenTester.Image()
	evenBG := evenImage.RGBAAt(int(evenRect.X+evenRect.W/2), int(evenRect.Y+2))
	// Without times the three samples stay evenly spaced, so the middle
	// sample keeps its historical place at the middle of the strip.
	if !sparklineInk(evenImage.RGBAAt,
		int(evenRect.X+evenRect.W/2), int(evenRect.Y+evenRect.H-2), 2, evenBG) {
		t.Fatal("without times the middle sample is not at the middle, so default spacing changed")
	}
}

// TestSparklineTimedEndDotSitsOnTheLatestSample proves a timed EndDot uses the
// same up-the-strip coordinates as the timed line: a falling series ends in a
// dot at the bottom, not mirrored at the top.
func TestSparklineTimedEndDotSitsOnTheLatestSample(t *testing.T) {
	line := ui.RGB(10, 20, 200)
	start := time.Unix(1_700_000_000, 0)
	opts := SparklineOptions{Height: 40, Max: 100, Color: line, EndDot: true,
		NoInset: true, Gridlines: true, Label: "timed end dot",
		Times: []time.Time{start, start.Add(10 * time.Second)}}
	dotted, rect := sparklineTester(t, []float64{100, 0}, opts)
	x := int(rect.X+rect.W) - 2
	bottom := int(rect.Y+rect.H) - 2
	if !regionHasColor(dotted.Image().RGBAAt, x, bottom, 1, line) {
		t.Fatalf("the timed end dot is not on the latest sample (%d,%d): %v",
			x, bottom, regionColors(dotted.Image().RGBAAt, x, bottom, 1))
	}
	if regionHasColor(dotted.Image().RGBAAt, x, int(rect.Y+2), 1, line) {
		t.Fatal("the timed end dot is mirrored to the top")
	}
	// The line alone reaches the same bottom corner, so the dot must add a
	// disk of its own: the same series without EndDot has fewer line-colored
	// pixels in the strip.
	plainOpts := opts
	plainOpts.EndDot = false
	plainOpts.Label = "timed line only"
	plain, plainRect := sparklineTester(t, []float64{100, 0}, plainOpts)
	plainPixels := sparklineColorCount(plain.Image().RGBAAt, plainRect, line)
	dottedPixels := sparklineColorCount(dotted.Image().RGBAAt, rect, line)
	if dottedPixels <= plainPixels {
		t.Fatalf("EndDot drew %d line-colored pixels, want more than the line's %d",
			dottedPixels, plainPixels)
	}
}

// TestSparklineGridlinesSitAtTheTopAndMiddle proves the gridline option draws
// dashed guides at the top and middle of the strip in place of the default
// baseline-only frame.
func TestSparklineGridlinesSitAtTheTopAndMiddle(t *testing.T) {
	gridTester, rect := sparklineTester(t, nil,
		SparklineOptions{Height: 40, Gridlines: true, Label: "grid"})
	grid := gridTester.Image()
	bg := grid.RGBAAt(int(rect.X+rect.W/2), int(rect.Y+2))
	if !sparklineInk(grid.RGBAAt, int(rect.X+1), int(rect.Y), 0, bg) {
		t.Fatal("the gridline at the top is not drawn")
	}
	if !sparklineInk(grid.RGBAAt, int(rect.X+1), int(rect.Y+rect.H/2), 0, bg) {
		t.Fatal("the gridline at the middle is not drawn")
	}

	plainTester, plainRect := sparklineTester(t, nil,
		SparklineOptions{Height: 40, Label: "plain frame"})
	if sparklineInk(plainTester.Image().RGBAAt, int(plainRect.X+1), int(plainRect.Y), 0, bg) {
		t.Fatal("the default frame drew a line at the top")
	}
}

// TestSparklineNoInsetReachesTheStripEdges proves NoInset extends the area to
// the strip's bottom row, while the default 2-DIP inset leaves it clear.
func TestSparklineNoInsetReachesTheStripEdges(t *testing.T) {
	line := ui.RGB(10, 20, 200)
	insetOpts := SparklineOptions{Height: 40, Gridlines: true, Color: line,
		Label: "inset"}
	inset, insetRect := sparklineTester(t, []float64{50, 50}, insetOpts)
	fullOpts := insetOpts
	fullOpts.NoInset = true
	fullOpts.Label = "no inset"
	full, rect := sparklineTester(t, []float64{50, 50}, fullOpts)
	// The window's background just below the strip, not a pixel inside it: a
	// filled area tints the strip's own pixels all the way down.
	bg := inset.Image().RGBAAt(int(insetRect.X+insetRect.W/2), int(insetRect.Y+insetRect.H)+1)
	if sparklineInk(inset.Image().RGBAAt,
		int(insetRect.X+insetRect.W/2), int(insetRect.Y+insetRect.H)-1, 0, bg) {
		t.Fatal("an inset sparkline painted the strip's bottom row")
	}
	if !sparklineInk(full.Image().RGBAAt,
		int(rect.X+rect.W/2), int(rect.Y+rect.H)-1, 0, bg) {
		t.Fatal("a no-inset sparkline did not reach the strip's bottom row")
	}
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
