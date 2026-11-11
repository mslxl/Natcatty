package kit

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
)

// SparklineOptions configure a sparkline.
type SparklineOptions struct {
	// Height is the strip's height in DIPs. Zero uses a compact default.
	Height float32
	// Max is the value mapped to the top. Zero or a negative value scales to
	// the largest finite sample, so a caller with an unknown range can pass 0.
	Max float64
	// Tone colors the line and its fill. The zero value is GaugeAccent.
	Tone GaugeTone
	// Label names the sparkline for assistive technology and tests.
	Label string
	// Gridlines draws three dashed horizontal guides at the top, middle, and
	// bottom of the strip instead of the default solid baseline.
	Gridlines bool
	// EndDot marks the latest finite sample with a dot.
	EndDot bool
	// Times, with one entry per value, positions each sample by its timestamp
	// across the strip. Unknown values stay in the path as NaN breaks rather
	// than becoming false zero points. Without it samples are evenly spaced.
	Times []time.Time
	// Color overrides the color Tone would choose when its alpha is non-zero.
	Color ui.Color
	// NoInset draws the area and line to the strip's edges instead of 2 DIPs
	// inside them, so a zero value meets the baseline.
	NoInset bool
}

// SparklineScale is the value a sparkline maps to its top: an explicit
// positive max, otherwise the largest finite sample, and at least 1 so a flat
// zero line still has a frame.
func SparklineScale(values []float64, maxValue float64) float64 {
	if maxValue > 0 && !math.IsInf(maxValue, 0) && !math.IsNaN(maxValue) {
		return maxValue
	}
	largest := 0.0
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		largest = math.Max(largest, value)
	}
	if largest <= 0 {
		return 1
	}
	return largest
}

// SparklinePoints maps finite samples to normalized coordinates: x runs 0–1
// left to right and y runs 0–1 bottom to top. Non-finite samples are skipped,
// so an unknown reading never draws a false zero.
func SparklinePoints(values []float64, maxValue float64) [][2]float32 {
	scale := SparklineScale(values, maxValue)
	points := make([][2]float32, 0, len(values))
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		y := value / scale
		if y < 0 {
			y = 0
		}
		if y > 1 {
			y = 1
		}
		points = append(points, [2]float32{0, float32(y)})
	}
	if len(points) == 0 {
		return nil
	}
	if len(points) == 1 {
		points[0][0] = 0.5
		return points
	}
	for index := range points {
		points[index][0] = float32(index) / float32(len(points)-1)
	}
	return points
}

// sparklineTimedPoints maps timestamped samples onto the strip: x runs 0–1 by
// each sample's time between the first and last timestamp, and y runs 0–1 up
// from the bottom, as chart expects. A non-finite value becomes a NaN y so the
// chart breaks the line instead of drawing a false zero. It reports false when
// times do not align with values.
func sparklineTimedPoints(values []float64, times []time.Time, maxValue float64) (
	[]float32, []float32, bool) {
	if len(values) < 2 || len(values) != len(times) {
		return nil, nil, false
	}
	scale := SparklineScale(values, maxValue)
	start := times[0]
	span := times[len(times)-1].Sub(start)
	xs := make([]float32, len(values))
	ys := make([]float32, len(values))
	for index, value := range values {
		if span > 0 {
			xs[index] = float32(times[index].Sub(start)) / float32(span)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			ys[index] = float32(math.NaN())
			continue
		}
		y := value / scale
		if y < 0 {
			y = 0
		}
		if y > 1 {
			y = 1
		}
		ys[index] = float32(y)
	}
	return xs, ys, true
}

// Sparkline draws a metric's recent values as a line over a faint baseline.
// An empty history draws only the frame; a single value draws a dot. Both
// Interface Styles use this primitive.
func Sparkline(c *ui.Context, values []float64, opts SparklineOptions) ui.Element {
	t := c.Theme()
	height := opts.Height
	if height <= 0 {
		height = 32
	}
	color := opts.Color
	if color.A == 0 {
		color = GaugeColor(t, opts.Tone)
	}
	box := ui.Box(c).FillWidth().MinWidth(0).Height(height).Draw(func(p *ui.Painter, r ui.Rect) {
		if opts.Gridlines {
			grid := t.Border.Alpha(0.7)
			for line := 0; line <= 2; line++ {
				y := r.Y + r.H*float32(line)/2
				for x := r.X; x < r.X+r.W; x += 6 {
					p.Line(x, y, min(x+3, r.X+r.W), y, 1, grid)
				}
			}
		} else {
			baseline := r.Y + r.H - 1
			p.Line(r.X, baseline, r.X+r.W, baseline, 1, t.Border.Alpha(0.7))
		}
		if len(values) == 0 {
			return
		}
		inset := float32(2)
		if opts.NoInset {
			inset = 0
		}
		padded := r
		padded.Y += inset
		padded.H -= 2 * inset
		xs, ys, timed := sparklineTimedPoints(values, opts.Times, opts.Max)
		if !timed {
			points := SparklinePoints(values, opts.Max)
			if len(points) == 0 {
				return
			}
			xs = make([]float32, len(points))
			ys = make([]float32, len(points))
			for index, point := range points {
				xs[index] = point[0]
				// SparklinePoints already gives fractions up from the bottom,
				// as chart draws them; passing them through keeps the line
				// rising with its values.
				ys[index] = point[1]
			}
			if len(points) == 1 {
				x := padded.X + xs[0]*padded.W
				y := chart.Y(padded, ys[0])
				p.FillPath(new(ui.Path).Circle(x, y, 2.5), color)
				return
			}
		}
		p.FillPath(chart.Area(padded, xs, nil, ys, chart.CurveMonotone), color.Alpha(0.14))
		p.StrokePath(chart.Line(padded, xs, ys, chart.CurveMonotone), 1.5, color)
		if opts.EndDot {
			for index := len(ys) - 1; index >= 0; index-- {
				if math.IsNaN(float64(ys[index])) || math.IsInf(float64(ys[index]), 0) {
					continue
				}
				x := padded.X + xs[index]*padded.W
				y := chart.Y(padded, ys[index])
				p.FillPath(new(ui.Path).Circle(x, y, 3), color)
				break
			}
		}
	})
	if opts.Label != "" {
		box = box.Label(opts.Label)
	}
	return box
}
