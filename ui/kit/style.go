package kit

import (
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

// Style is an Interface Style (ADR 0020): the sizes, the materials, and the
// recipes the components and the layout shells draw with. The color preset
// is the other axis and stays in the theme.
type Style interface {
	// ID is the style's stored name, as Appearance.InterfaceStyle's.
	ID() string
	Metrics() Metrics
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
)

// MaterialKind is how a Material fills a surface.
type MaterialKind int

const (
	// MaterialOpaque fills with the theme's color for the surface.
	MaterialOpaque MaterialKind = iota
	// MaterialGlass paints Paint, such as a glass.Glass.
	MaterialGlass
	// MaterialVibrant leaves the surface clear over the window's vibrancy.
	MaterialVibrant
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

func (classic) ID() string                    { return StyleClassic }
func (classic) Metrics() Metrics              { return classicMetrics }
func (classic) Material(SurfaceRole) Material { return Material{Kind: MaterialOpaque} }
func (classic) Native() bool                  { return false }

// macOS is the macOS 27 style. Until it has its own recipes it draws as
// Classic under its own ID.
type macOS struct{ classic }

func (macOS) ID() string { return StyleMacOS }

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
