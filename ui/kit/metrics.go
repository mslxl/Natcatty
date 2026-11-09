package kit

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
)

// Metrics are the sizes every surface draws with: the type scale, corner
// radii, control and bar heights, icon sizes, and gaps. An Interface Style
// supplies them, so a surface names a role (Font.Footnote, Bar.Toolbar) and
// never writes the number.
type Metrics struct {
	Font    FontScale
	Radius  Radii
	Control ControlHeights
	Bar     BarHeights
	Icon    IconSizes
	Space   Spacing
}

// FontScale is the type scale, from the smallest label to a page heading.
type FontScale struct {
	// Caption2 is the smallest label, as a badge's or a chart axis's.
	Caption2 float32
	// Caption is secondary text in a dense row: a count, a path, a hint.
	Caption float32
	// Footnote is a row's detail line and a section's label.
	Footnote float32
	// Callout is the text of a notice or a compact form.
	Callout float32
	// Body is the default text size.
	Body float32
	// Title3 heads a dialog.
	Title3 float32
	// Title heads a page.
	Title float32
	// LargeTitle is a hero heading, as an empty window's or a stat's.
	LargeTitle float32
}

// Radii round corners, from a hairline indicator to a capsule.
type Radii struct {
	// Small rounds an indicator, a swatch, or a small chip.
	Small float32
	// Control rounds buttons, inputs, and list rows.
	Control float32
	// Card rounds cards, panels, and popovers.
	Card float32
	// Overlay rounds dialogs and palettes.
	Overlay float32
	// Pill makes a capsule.
	Pill float32
}

// ControlHeights are the heights of buttons and rows, smallest first.
type ControlHeights struct {
	// Mini is an inline icon button inside a row or a tab.
	Mini float32
	// Small is an icon button in a pane's chrome.
	Small float32
	// Compact is a dense row or a small button.
	Compact float32
	// Regular is a ghost icon button or a list row.
	Regular float32
	// Large is a toolbar button or an input.
	Large float32
}

// BarHeights are the heights of the horizontal bars surfaces stack.
type BarHeights struct {
	// Title is the window's title bar, which the work Tabs share. The macOS
	// traffic lights are centered on it, so every style keeps it.
	Title float32
	// Tabs is a surface's own tab strip, as the SFTP panes'.
	Tabs float32
	// Toolbar is a surface's action bar.
	Toolbar float32
	// Pane is a split Pane's header.
	Pane float32
	// ListHeader is a list's column header.
	ListHeader float32
	// Filter is a filter row under a toolbar.
	Filter float32
	// Footer is a list's status row.
	Footer float32
	// Strip is a side panel's tool strip.
	Strip float32
	// Page is a page's title header.
	Page float32
}

// IconSizes are the sizes of icons inside controls.
type IconSizes struct {
	Small   float32
	Medium  float32
	Regular float32
}

// Spacing are the gaps between and inside elements.
type Spacing struct {
	Hairline float32
	XSmall   float32
	Small    float32
	Medium   float32
	Large    float32
	XLarge   float32
}

// classicMetrics are the original's sizes.
var classicMetrics = Metrics{
	Font: FontScale{Caption2: 10, Caption: 11, Footnote: 12, Callout: 13, Body: 14,
		Title3: 16, Title: 18, LargeTitle: 20},
	Radius:  Radii{Small: 4, Control: 6, Card: 10, Overlay: 12, Pill: 999},
	Control: ControlHeights{Mini: 20, Small: 22, Compact: 24, Regular: 28, Large: 32},
	Bar: BarHeights{Title: 36, Tabs: 32, Toolbar: 30, Pane: 30, ListHeader: 28, Filter: 34,
		Footer: 30, Strip: 36, Page: 56},
	Icon:  IconSizes{Small: 12, Medium: 14, Regular: 16},
	Space: Spacing{Hairline: 2, XSmall: 4, Small: 6, Medium: 8, Large: 12, XLarge: 16},
}

// M returns the metrics of the active Interface Style.
func M(*ui.Context) Metrics { return classicMetrics }

// IconSized draws a named Lucide icon size DIPs square, one of
// Metrics.Icon's sizes.
func IconSized(c *ui.Context, name string, size float32) ui.Element {
	return ui.Icon(c, icons.Lucide(name)).Size(size, size)
}
