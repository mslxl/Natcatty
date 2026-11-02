// Package titlebar draws the window controls of the app's frameless windows,
// as the original's: each window's own top bar drags it, and its controls sit
// over the bar's right end in the bar's colors.
package titlebar

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/locales"
)

// Height is the height of a window's title bar, as the original's h-9 Tab
// bar and terminal popup title bar.
const Height float32 = 36

// ButtonWidth is the width of one control of the main window, as the
// original's .window-control-btn, and Room the width the three take at the
// right end of the Tab bar.
const (
	ButtonWidth float32 = 40
	Room                = 3 * ButtonWidth
)

// PopupButtonWidth is the width of one control of a detached terminal window,
// as the original popup's w-11 buttons, and PopupRoom the width the three
// take at the right end of its title bar.
const (
	PopupButtonWidth float32 = 44
	PopupRoom                = 3 * PopupButtonWidth
)

// The Settings window has only a close button, as the original's: a 32-square
// at the end of its header row, CloseInset from the window's right edge and
// CloseTop from its top. DialogRoom is what the header row keeps clear of it.
const (
	CloseSize  float32 = 32
	CloseInset float32 = 16
	CloseTop   float32 = 20
	DialogRoom         = CloseSize + 8
)

// closeHover is the main window's close hover fill, the original's red-500.
var closeHover = ui.RGB(239, 68, 68)

// Style picks the controls a window shows.
type Style int

const (
	// Tabs is the main window's minimize, maximize, and close at the end of
	// its Tab bar.
	Tabs Style = iota
	// Popup is a detached terminal window's controls, in its scheme's colors.
	Popup
	// Dialog is the Settings window's lone close button.
	Dialog
)

// Window is the native window the controls act on.
type Window interface {
	Minimize()
	ToggleMaximize()
	IsMaximized() bool
	Close()
}

// Frame lays content over the whole window and the window's controls over
// it. window is resolved per frame, because the content is built before the
// native window exists; a nil window draws no controls.
func Frame(style Style, window func() Window, content func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			content(c)
			if window == nil {
				return
			}
			if w := window(); w != nil {
				Controls(c, style, w)
			}
		})
	}
}

// Controls draws a window's controls at the top-right corner of their parent,
// above its other children.
func Controls(c *ui.Context, style Style, w Window) {
	m := locales.In(c.Locale())
	if style == Dialog {
		ui.Row(c).Key("window-controls").Absolute().Top(CloseTop).Right(CloseInset).Children(func() {
			if dialogClose(c, m.WindowClose()).Clicked() {
				w.Close()
			}
		})
		return
	}
	button := tabsControl
	if style == Popup {
		button = popupControl
	}
	ui.Row(c).Key("window-controls").Absolute().Top(0).Right(0).Height(Height).Children(func() {
		if button(c, "window-minimize", icons.Minus, m.WindowMinimize(), false).Clicked() {
			w.Minimize()
		}
		maximize, glyph := m.WindowMaximize(), icons.Square
		if w.IsMaximized() {
			maximize, glyph = m.WindowRestore(), icons.Copy
		}
		if button(c, "window-maximize", glyph, maximize, false).Clicked() {
			w.ToggleMaximize()
		}
		if button(c, "window-close", icons.X, m.WindowClose(), true).Clicked() {
			w.Close()
		}
	})
}

// tabsControl is one of the main window's controls: muted, a faint fill on
// hover, and red under the close glyph.
func tabsControl(c *ui.Context, key, icon, label string, danger bool) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Key(key).Label(label).Tooltip(label).Size(ButtonWidth, Height).
		Justify(ui.Center).AlignItems(ui.Center).TextColor(t.TextMuted)
	switch {
	case danger && (button.Hovered() || button.Pressed()):
		button.Background(closeHover).TextColor(ui.Hex("#ffffff"))
	case button.Hovered() || button.Pressed():
		button.Background(t.Text.Alpha(0.1))
	}
	// The original's Minus and X are 16, Square and Copy 14.
	size := float32(16)
	if icon == icons.Square || icon == icons.Copy {
		size = 14
	}
	button.Children(func() { ui.Icon(c, icons.Lucide(icon)).Size(size, size) })
	return button
}

// popupControl is one of a detached terminal window's controls: the scheme's
// foreground with a faint fill on hover, and a close glyph that is slightly
// dimmed until hovered rather than red.
func popupControl(c *ui.Context, key, icon, label string, closing bool) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Key(key).Label(label).Tooltip(label).Size(PopupButtonWidth, Height).
		Justify(ui.Center).AlignItems(ui.Center).TextColor(t.Text)
	hovered := button.Hovered() || button.Pressed()
	if hovered {
		button.Background(t.Text.Alpha(0.1))
	}
	// The original's Minus is 15, Square 13, Copy 14, and X 16.
	size := map[string]float32{icons.Minus: 15, icons.Square: 13, icons.Copy: 14, icons.X: 16}[icon]
	button.Children(func() {
		glyph := ui.Icon(c, icons.Lucide(icon)).Size(size, size)
		if closing && !hovered {
			glyph.Opacity(0.8)
		}
	})
	return button
}

// dialogClose is the Settings window's close button: a muted glyph on a
// rounded square that turns the destructive color on hover.
func dialogClose(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Key("window-close").Label(label).Size(CloseSize, CloseSize).Radius(6).
		Justify(ui.Center).AlignItems(ui.Center).TextColor(t.TextMuted)
	if button.Hovered() || button.Pressed() {
		button.Background(t.Danger.Alpha(0.2)).TextColor(t.Danger)
	}
	button.Children(func() { ui.Icon(c, icons.Lucide(icons.X)).Size(16, 16) })
	return button
}

// DragStripe is the original's always-on drag stripe: a thin band along the
// top of its parent that drags the window even where Tabs fill the bar. It
// sits above the parent's other children.
func DragStripe(c *ui.Context) {
	ui.Box(c).Key("drag-stripe").Absolute().Top(0).Left(0).Right(0).Height(4).DragWindow()
}
