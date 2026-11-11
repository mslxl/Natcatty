package kit

import (
	"github.com/egoist/mygo/ui"
)

// ResourceBarOptions configure a horizontal resource bar.
type ResourceBarOptions struct {
	// Height is the bar's height in DIPs. Zero uses a compact default.
	Height float32
	// Label names the bar for assistive technology and tests.
	Label string
}

// ResourceBar draws a percentage as a horizontal fill and applies the shared
// thresholds: above 85 percent danger, above 60 warning, otherwise accent. An
// unknown percentage leaves only the track, so it never reads as zero. Both
// Interface Styles use this primitive.
func ResourceBar(c *ui.Context, percent float64, opts ResourceBarOptions) ui.Element {
	t := c.Theme()
	height := opts.Height
	if height <= 0 {
		height = 6
	}
	track := t.Border.Alpha(0.75)
	fill := GaugeColor(t, ToneForPercent(percent))
	fraction := GaugeFraction(percent)
	box := ui.Box(c).FillWidth().MinWidth(0).Height(height).Shrink(0).
		Draw(func(p *ui.Painter, r ui.Rect) {
			radius := height / 2
			p.Fill(r, track, radius)
			if fraction <= 0 {
				return
			}
			filled := r
			filled.W = r.W * float32(fraction)
			p.Fill(filled, fill, radius)
		})
	if opts.Label != "" {
		box = box.Label(opts.Label)
	}
	return box
}
