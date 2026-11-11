package kit

import (
	"math"

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

// Sparkline draws a metric's recent values as a line over a faint baseline.
// An empty history draws only the frame; a single value draws a dot. Both
// Interface Styles use this primitive.
func Sparkline(c *ui.Context, values []float64, opts SparklineOptions) ui.Element {
	t := c.Theme()
	height := opts.Height
	if height <= 0 {
		height = 32
	}
	color := GaugeColor(t, opts.Tone)
	points := SparklinePoints(values, opts.Max)
	box := ui.Box(c).FillWidth().MinWidth(0).Height(height).Draw(func(p *ui.Painter, r ui.Rect) {
		baseline := r.Y + r.H - 1
		p.Line(r.X, baseline, r.X+r.W, baseline, 1, t.Border.Alpha(0.7))
		if len(points) == 0 {
			return
		}
		const inset float32 = 2
		xs := make([]float32, len(points))
		ys := make([]float32, len(points))
		for index, point := range points {
			xs[index] = point[0]
			ys[index] = 1 - point[1]
		}
		if len(points) == 1 {
			x := r.X + xs[0]*r.W
			y := r.Y + inset + ys[0]*(r.H-2*inset)
			p.FillPath(new(ui.Path).Circle(x, y, 2.5), color)
			return
		}
		padded := r
		padded.Y += inset
		padded.H -= 2 * inset
		p.FillPath(chart.Area(padded, xs, nil, ys, chart.CurveMonotone), color.Alpha(0.14))
		p.StrokePath(chart.Line(padded, xs, ys, chart.CurveMonotone), 1.5, color)
	})
	if opts.Label != "" {
		box = box.Label(opts.Label)
	}
	return box
}
