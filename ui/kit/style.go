package kit

import (
	"sync/atomic"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// Style is an Interface Style (ADR 0020): the sizes, the materials, and the
// recipes the components and the layout shells draw with. The color preset
// is the other axis and stays in the theme.
type Style interface {
	// ID is the style's stored name, as Appearance.InterfaceStyle's.
	ID() string
	Metrics() Metrics
	// Theme derives the style's widget theme from the preset's. It returns
	// a copy and never changes base.
	Theme(base *ui.Theme) *ui.Theme
	// Material is what fills a surface of role.
	Material(role SurfaceRole) Material
	// Native reports whether the style follows the platform's own idiom,
	// as the macOS style does; recipes that differ by style branch on it.
	Native() bool
}

// SurfaceRole names a surface a style fills.
type SurfaceRole int

const (
	// SurfaceWindow is a window's root.
	SurfaceWindow SurfaceRole = iota
	// SurfaceSidebar is a navigation column.
	SurfaceSidebar
	// SurfaceContent is the pane a page draws in.
	SurfaceContent
	// SurfaceBar is a title or tool bar.
	SurfaceBar
	// SurfaceOverlay is a popover, palette, or dialog.
	SurfaceOverlay
	// SurfaceScrollEdge lies over content scrolling under a bar, where the
	// macOS style fades the content out.
	SurfaceScrollEdge
)

// MaterialKind is how a Material fills a surface.
type MaterialKind int

const (
	// MaterialOpaque fills with the theme's color for the surface.
	MaterialOpaque MaterialKind = iota
	// MaterialGlass paints Paint, such as a glass.Glass.
	MaterialGlass
	// MaterialVibrant leaves the surface clear over the window's vibrancy,
	// or fills it as MaterialOpaque where the window shows none.
	MaterialVibrant
	// MaterialClear draws nothing.
	MaterialClear
)

// Material is a surface's fill. Paint is set for MaterialGlass.
type Material struct {
	Kind  MaterialKind
	Paint ui.Material
}

// Style IDs, as Appearance.InterfaceStyle stores them.
const (
	StyleClassic = "classic"
	StyleMacOS   = "macos"
)

// classic is today's look: the original's sizes on opaque surfaces.
type classic struct{}

func (classic) ID() string                     { return StyleClassic }
func (classic) Metrics() Metrics               { return classicMetrics }
func (classic) Theme(base *ui.Theme) *ui.Theme { return base }
func (classic) Native() bool                   { return false }

func (classic) Material(role SurfaceRole) Material {
	if role == SurfaceScrollEdge {
		return Material{Kind: MaterialClear}
	}
	return Material{Kind: MaterialOpaque}
}

// macOS is the macOS 27 style: the window's vibrancy behind a floating glass
// sidebar, Liquid Glass bars and overlays, and a hierarchy drawn with fills
// more than with lines.
type macOS struct{}

func (macOS) ID() string       { return StyleMacOS }
func (macOS) Metrics() Metrics { return macOSMetrics }
func (macOS) Native() bool     { return true }

func (macOS) Material(role SurfaceRole) Material {
	switch role {
	case SurfaceWindow:
		return Material{Kind: MaterialVibrant}
	case SurfaceSidebar, SurfaceBar, SurfaceOverlay:
		return Material{Kind: MaterialGlass, Paint: glass.Glass{Style: glass.Regular}}
	case SurfaceScrollEdge:
		return Material{Kind: MaterialGlass, Paint: glass.ScrollEdge{}}
	}
	return Material{Kind: MaterialOpaque}
}

// Theme keeps the preset's colors and fades its borders toward the
// background, so that a surface over a translucent material reads by its
// fill; the body text is macOS's 13 DIPs.
func (macOS) Theme(base *ui.Theme) *ui.Theme {
	t := *base
	fade := float32(0.5)
	if t.Dark {
		// A dark border is already close to the background.
		fade = 0.35
	}
	t.Border = base.Border.Mix(base.Background, fade)
	t.Radius = macOSMetrics.Radius.Control
	t.FontSize = macOSMetrics.Font.Body
	return &t
}

// Classic and MacOS are the two Interface Styles.
var (
	Classic Style = classic{}
	MacOS   Style = macOS{}
)

// StyleFor is the style stored as id, or Classic for an unknown one.
func StyleFor(id string) Style {
	if id == StyleMacOS {
		return MacOS
	}
	return Classic
}

// current holds the active style. The app sets it at the start of every
// window's frame; every window shares one, since the style is per device.
var current atomic.Value

// SetStyle makes s the active style; nil restores Classic.
func SetStyle(s Style) {
	if s == nil {
		s = Classic
	}
	current.Store(&s)
}

// CurrentStyle is the active style, Classic until one is set.
func CurrentStyle() Style {
	if s, ok := current.Load().(*Style); ok {
		return *s
	}
	return Classic
}

// Fill fills el as the active style fills a surface of role: the theme's
// color for it, the style's glass, or clear over the window's vibrancy. A
// vibrant surface in a window that shows no vibrancy is filled as an opaque
// one, and so is every surface while the desktop asks for more contrast,
// which on macOS stands in for Reduce Transparency too.
func Fill(c *ui.Context, el ui.Element, role SurfaceRole) ui.Element {
	t := c.Theme()
	m := CurrentStyle().Material(role)
	if c.Preferences().HighContrast && (m.Kind == MaterialGlass || m.Kind == MaterialVibrant) {
		m = Material{Kind: MaterialOpaque}
	}
	switch m.Kind {
	case MaterialGlass:
		return el.Material(m.Paint)
	case MaterialVibrant:
		if c.Vibrancy() {
			// Explicitly clear: the root paints the background otherwise.
			return el.Background(ui.Transparent)
		}
	case MaterialClear:
		return el
	}
	switch role {
	case SurfaceContent, SurfaceSidebar, SurfaceOverlay:
		return el.Background(t.Surface)
	case SurfaceScrollEdge:
		return el
	}
	return el.Background(t.Background)
}

// StyleTheme is style's theme from the preset's base, as a window draws it:
// while the desktop asks for more contrast it keeps the preset's borders.
func StyleTheme(c *ui.Context, style Style, base *ui.Theme) *ui.Theme {
	t := style.Theme(base)
	if t != base && c.Preferences().HighContrast {
		t.Border = base.Border
	}
	return t
}

// Vibrant reports whether s shows the window's vibrancy anywhere, so the
// window should have one.
func Vibrant(s Style) bool {
	return s.Material(SurfaceWindow).Kind == MaterialVibrant
}
