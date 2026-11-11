package kit

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// GaugeTone is the semantic color of a gauge or sparkline arc.
type GaugeTone int

const (
	// GaugeAccent is an ordinary value.
	GaugeAccent GaugeTone = iota
	// GaugeWarning marks a value above the warning threshold.
	GaugeWarning
	// GaugeDanger marks a value above the danger threshold.
	GaugeDanger
)

// GaugeOptions configure a radial gauge.
type GaugeOptions struct {
	// Size is the square's side in DIPs. Zero uses a compact default.
	Size float32
	// Tone colors the filled arc. The zero value is GaugeAccent.
	Tone GaugeTone
	// Label names the gauge for assistive technology and tests.
	Label string
}

// GaugeFraction maps a 0–100 value onto the arc's fraction. A non-finite
// value has no filled arc, reported as -1, so a caller can draw an unknown
// gauge instead of a zero one.
func GaugeFraction(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return -1
	}
	if value <= 0 {
		return 0
	}
	if value >= 100 {
		return 1
	}
	return value / 100
}

// ToneForPercent applies the shared resource thresholds: a known value above
// 85 percent is danger, above 60 warning, and anything else accent. An unknown
// value has no danger tone.
func ToneForPercent(percent float64) GaugeTone {
	switch {
	case math.IsNaN(percent) || math.IsInf(percent, 0):
		return GaugeAccent
	case percent > 85:
		return GaugeDanger
	case percent > 60:
		return GaugeWarning
	default:
		return GaugeAccent
	}
}

// NetworkGaugePercent maps a combined network rate in bytes per second onto
// the Network card's synthetic 0–100 gauge: min(100, log10(total + 1) × 14).
// An unknown rate stays unknown.
func NetworkGaugePercent(bytesPerSecond float64) float64 {
	if math.IsNaN(bytesPerSecond) || math.IsInf(bytesPerSecond, 0) {
		return math.NaN()
	}
	if bytesPerSecond <= 0 {
		return 0
	}
	return min(100, math.Log10(bytesPerSecond+1)*14)
}

// GaugeColor is the stroke color a gauge tone uses in the active theme.
func GaugeColor(t *ui.Theme, tone GaugeTone) ui.Color {
	switch tone {
	case GaugeWarning:
		return t.Warning
	case GaugeDanger:
		return t.Danger
	default:
		return t.Accent
	}
}

// Gauge draws a radial gauge: a full track ring and a filled arc from the top
// clockwise. It is NaN-safe: an unknown value leaves only the track. The
// macOS Interface Style uses a slightly thicker ring; both styles are the same
// primitive.
func Gauge(c *ui.Context, value float64, opts GaugeOptions) ui.Element {
	t := c.Theme()
	size := opts.Size
	if size <= 0 {
		size = 56
	}
	width := float32(3)
	if CurrentStyle().Native() {
		width = 4
	}
	track := t.Border.Alpha(0.75)
	fill := GaugeColor(t, opts.Tone)
	fraction := GaugeFraction(value)
	box := ui.Box(c).Size(size, size).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		radius := min(r.W, r.H)/2 - width/2
		if radius <= 0 {
			return
		}
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		p.StrokePath(new(ui.Path).Circle(cx, cy, radius), width, track)
		if fraction < 0 {
			return
		}
		if fraction >= 1 {
			p.StrokePath(new(ui.Path).Circle(cx, cy, radius), width, fill)
			return
		}
		if fraction <= 0 {
			return
		}
		// A polyline approximates the arc; two-degree steps stay round at the
		// sizes a card uses.
		steps := max(2, int(math.Ceil(fraction*180)))
		points := make([][2]float32, 0, steps+1)
		for step := 0; step <= steps; step++ {
			angle := -math.Pi/2 + fraction*2*math.Pi*float64(step)/float64(steps)
			points = append(points, [2]float32{
				cx + radius*float32(math.Cos(angle)),
				cy + radius*float32(math.Sin(angle)),
			})
		}
		p.StrokePolyline(points, width, fill)
	})
	if opts.Label != "" {
		box = box.Label(opts.Label)
	}
	return box
}
