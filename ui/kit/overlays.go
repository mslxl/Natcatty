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
// the autocomplete popup: the surface fill inside a rule of border.
func PopoverSurface(c *ui.Context, el ui.Element, border ui.Color) ui.Element {
	if CurrentStyle().Native() {
		return overlaySurface(c, el, ui.Color{}, 0).Clip()
	}
	return el.Background(c.Theme().Surface).Border(1, border).Clip()
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
