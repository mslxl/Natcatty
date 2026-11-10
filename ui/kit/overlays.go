package kit

import (
	"github.com/egoist/mygo/ui"

	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// Chord is a key with its modifiers.
type Chord struct {
	Mods ui.Modifiers
	Key  ui.Key
}

// PaletteOptions configure a Palette. Width and Height are the panel's;
// Top is its distance below the window's top edge.
type PaletteOptions struct {
	Width, Height, Top float32
	Padding, Gap       float32
	// OnBackdrop runs when a click lands outside the panel. It runs after
	// the build, so it changes the state itself. Nil ignores such clicks.
	OnBackdrop func()
	// Close lists chords besides Escape that close the palette, as the one
	// that opened it: the backdrop holds the window's shortcuts back.
	Close []Chord
}

// Palette floats a top-anchored panel over the dimmed window, as the Quick
// Switcher, the Transfer center, and the editor. The backdrop traps the
// focus and holds the window's shortcuts. It reports whether Escape or one
// of o.Close asked to close the palette this frame.
func Palette(c *ui.Context, o PaletteOptions, content func()) (closed bool) {
	t := c.Theme()
	ui.Overlay(c, func() {
		back := ui.Column(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			AlignItems(ui.Center).Justify(ui.Start).Modal().
			Background(uitheme.Scrim)
		// The panel swallows its own clicks below, so a click inside it never
		// reaches the backdrop's handler.
		if o.OnBackdrop != nil {
			back.OnClick(o.OnBackdrop)
		}
		if back.OverlayShortcut(0, ui.KeyEscape) {
			closed = true
		}
		for _, chord := range o.Close {
			if back.OverlayShortcut(chord.Mods, chord.Key) {
				closed = true
			}
		}
		back.Children(func() {
			panel := ui.Column(c).Role(ui.RoleDialog).
				Width(o.Width).Height(o.Height).
				Margin(o.Top, 0, 0, 0).Padding(o.Padding).Gap(o.Gap).
				Shadow(0, 10, 30, 0, uitheme.OverlayShadow).Clip()
			overlaySurface(c, panel, t.Background, t.Radius+2)
			panel.OnClick(func() {})
			panel.Children(content)
		})
	})
	return closed
}

// PopoverRole picks a Popover's recipe.
type PopoverRole int

const (
	// PopoverPanel is a card of controls under its button's end, as the
	// quick controls.
	PopoverPanel PopoverRole = iota
	// PopoverPicker is the toolkit's popover, for a picker such as the
	// calendar.
	PopoverPicker
)

// Popover shows content beside anchor while *open is true. A click outside
// and Escape set *open to false.
func Popover(c *ui.Context, role PopoverRole, anchor ui.Element, open *bool, content func()) {
	if role == PopoverPicker {
		ui.Popover(c, anchor, open, content)
		return
	}
	t := c.Theme()
	ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		panel.AttachTo(anchor, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(6, 0, 0, 0).
			Shadow(0, 8, 24, 0, CardShadow(t)).Clip()
		overlaySurface(c, panel, t.Surface, M(c).Radius.Card)
		content()
	})
}

// PopoverSurface paints el, a floating list the caller places itself, as
// the autocomplete popup: the surface fill inside a rule of border. The
// macOS style rounds it as a menu rather than as a sheet, so a short list
// keeps its text clear of the corners, and keeps it solid: glass refracting
// a terminal's text under a list this small only muddies it.
func PopoverSurface(c *ui.Context, el ui.Element, border ui.Color) ui.Element {
	t := c.Theme()
	if CurrentStyle().Native() {
		return el.Background(t.Surface).Border(1, t.Border).Radius(M(c).Radius.Control).
			Shadow(0, 4, 12, 0, CardShadow(t)).Clip()
	}
	return el.Background(t.Surface).Border(1, border).Clip()
}

// RowColors are the colors of a row's text, its secondary labels, and its
// marker, as PopoverRow paints them.
type RowColors struct {
	Text, Muted, Marker ui.Color
}

// PopoverRow paints a row of a PopoverSurface list and returns the colors
// to draw its contents in. Classic shades the selected row; the macOS style
// fills it with the accent, rounded, as a menu's highlighted item.
func PopoverRow(c *ui.Context, row ui.Element, selected bool) RowColors {
	t := c.Theme()
	colors := RowColors{Text: t.Text, Muted: t.TextMuted, Marker: t.TextMuted}
	native := CurrentStyle().Native()
	if native {
		row.Radius(M(c).Radius.Small)
	}
	switch {
	case selected && native:
		row.Background(t.Accent)
		return RowColors{Text: t.AccentText, Muted: t.AccentText.Alpha(0.8), Marker: t.AccentText}
	case selected:
		row.Background(t.SurfaceHover)
		colors.Marker = t.Accent
	case native && row.Hovered():
		row.Background(t.Text.Alpha(0.06))
	}
	return colors
}

// PopoverInset is the room a PopoverSurface list's header keeps at its
// sides, level with its rows' text: none in Classic, a row's padding in the
// macOS style.
func PopoverInset(c *ui.Context) float32 {
	if CurrentStyle().Native() {
		return 4
	}
	return 0
}

// overlaySurface fills a floating panel: in Classic with fill inside a rule
// of the border, rounded by radius; in the macOS style on the style's glass,
// with no rule, rounded as an overlay.
func overlaySurface(c *ui.Context, el ui.Element, fill ui.Color, radius float32) ui.Element {
	if CurrentStyle().Native() {
		return Fill(c, el.Radius(M(c).Radius.Overlay), SurfaceOverlay)
	}
	return el.Background(fill).Border(1, c.Theme().Border).Radius(radius)
}
