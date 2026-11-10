package kit

import (
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// IconRole is where an icon button sits, which decides its size.
type IconRole int

const (
	// IconToolbar is a toolbar's or a list row's action, the default.
	IconToolbar IconRole = iota
	// IconCard is an action inside a card, a tool strip, or the Tab bar.
	IconCard
	// IconPane is an action in a Pane's chrome.
	IconPane
	// IconTab is an action inside a Tab or a Pane header.
	IconTab
	// IconBar is an action in the window's title bar: an IconCard in
	// Classic, a glass capsule in the macOS style.
	IconBar
)

// IconOptions configure IconButtonWith.
type IconOptions struct {
	Role IconRole
	// On fills the button as pressed: a toggle whose panel is open.
	On bool
	// Selected marks the chosen one of a strip of tools: tinted with the
	// accent in Classic, its glyph in the accent in the macOS style.
	Selected bool
	// Key keeps the button the same element when a control before it comes
	// and goes.
	Key string
}

// iconRecipe is the shape of an icon button in a role: its square, its
// glyph, and its corner radius.
func iconRecipe(c *ui.Context, role IconRole) (box, glyph, radius float32) {
	metrics := M(c)
	switch role {
	case IconCard, IconBar:
		return metrics.Control.Regular, 15, metrics.Radius.Control
	case IconPane:
		return metrics.Control.Small, metrics.Icon.Medium, 5
	case IconTab:
		return metrics.Control.Mini, 13, metrics.Radius.Small
	default:
		return metrics.Control.Regular, metrics.Icon.Regular, metrics.Radius.Control
	}
}

// IconButtonWith is a square ghost button showing only an icon, sized for
// its role. label names it and shows as its tooltip. Hover and press shade
// it with the text color, so it reads the same over any bar's fill.
func IconButtonWith(c *ui.Context, name, label string, options IconOptions) ui.Element {
	t := c.Theme()
	box, glyph, radius := iconRecipe(c, options.Role)
	button := ui.ButtonBase(c)
	if options.Key != "" {
		button.Key(options.Key)
	}
	button.Label(label).Tooltip(label).Size(box, box).Radius(radius).
		Justify(ui.Center).AlignItems(ui.Center).TextColor(t.TextMuted).Shrink(0)
	if options.Role == IconBar && CurrentStyle().Native() {
		// A capsule of interactive glass; an open toggle is tinted. With
		// more contrast asked for, it is an opaque, outlined capsule.
		button.Radius(M(c).Radius.Pill).TextColor(t.Text)
		switch {
		case c.Preferences().HighContrast:
			fill := t.Surface
			if options.On || button.Pressed() {
				fill = Strong(t)
			}
			button.Background(fill).Border(1, t.Border)
		case options.On:
			button.Material(glass.Glass{Interactive: true, Tint: t.Text.Alpha(0.12)})
		default:
			button.Material(glass.Glass{Interactive: true})
		}
		button.Children(func() { IconSized(c, name, glyph) })
		return button
	}
	switch {
	case options.Selected && CurrentStyle().Native():
		button.Background(t.Text.Alpha(0.08)).TextColor(t.Accent)
	case options.Selected:
		button.Background(t.Accent.Alpha(0.24)).TextColor(t.Text)
	case options.Role == IconBar && options.On:
		// An open title bar toggle is shaded a little lighter than a press.
		button.Background(t.Text.Alpha(0.1))
		if button.Hovered() || button.Pressed() {
			button.TextColor(t.Text)
		}
	case options.On || button.Pressed():
		button.Background(t.Text.Alpha(0.12)).TextColor(t.Text)
	case button.Hovered():
		button.Background(t.Text.Alpha(0.08)).TextColor(t.Text)
	}
	button.Children(func() { IconSized(c, name, glyph) })
	return button
}

// BarRole is what a horizontal bar is for, which decides its height, the
// side of its divider, and its fill.
type BarRole int

const (
	// BarTabs is a surface's own tab strip.
	BarTabs BarRole = iota
	// BarPane is a split Pane's header.
	BarPane
	// BarListHeader is a list's column header.
	BarListHeader
	// BarFilter is a filter row under a toolbar.
	BarFilter
	// BarFooter is a list's status row, divided from the list above it.
	BarFooter
	// BarStrip is a side panel's tool strip.
	BarStrip
)

// Bar is a fixed-height row for role, its items centered across it, with a
// divider between it and the content. Set its padding and gap, and build its
// items with Children.
func Bar(c *ui.Context, role BarRole) ui.Element {
	t := c.Theme()
	heights := M(c).Bar
	height, fill, below := heights.Tabs, Muted(t), true
	switch role {
	case BarPane:
		height, fill = heights.Pane, t.Background
	case BarListHeader:
		height = heights.ListHeader
	case BarFilter:
		height, fill = heights.Filter, ui.Transparent
	case BarFooter:
		height, below = heights.Footer, false
	case BarStrip:
		height, fill = heights.Strip, ui.Transparent
	}
	if CurrentStyle().Native() {
		// A macOS bar is drawn on its pane's own fill, set off by the rule;
		// a strip of rounded tabs needs none.
		fill = ui.Transparent
		if role == BarTabs {
			return ui.Row(c).FillWidth().Height(height).AlignItems(ui.Center).Shrink(0)
		}
	}
	bar := ui.Row(c).FillWidth().Height(height).AlignItems(ui.Center).Shrink(0).BorderColor(t.Border)
	if fill != ui.Transparent {
		bar.Background(fill)
	}
	if below {
		bar.BorderWidth(0, 0, 1, 0)
	} else {
		bar.BorderWidth(1, 0, 0, 0)
	}
	return bar
}

// ChromeFill is the fill of a surface's own toolbar or status strip: a
// muted band in Classic, none in the macOS style, whose strips sit on their
// pane's fill.
func ChromeFill(t *ui.Theme) ui.Color {
	if CurrentStyle().Native() {
		return ui.Transparent
	}
	return Muted(t)
}

// ChromeRule is the color of a rule inside a surface's toolbar: the
// divider in Classic, none in the macOS style, whose toolbars run as one.
func ChromeRule(t *ui.Theme) ui.Color {
	if CurrentStyle().Native() {
		return ui.Transparent
	}
	return t.Border
}

// PaneGap is the room between the panes of a SplitPane pair: Classic panes
// share a rule, macOS cards stand apart.
func PaneGap(c *ui.Context) float32 {
	if CurrentStyle().Native() {
		return M(c).Space.Medium
	}
	return 0
}

// SplitPaneOptions describe one pane of a pair SplitPane styles.
type SplitPaneOptions struct {
	// First is the leading (or top) pane, which draws Classic's rule.
	First bool
	// Across lays the pair side by side rather than stacked.
	Across bool
	// Focused marks the pane the keyboard acts on while the focus is a
	// choice between the two.
	Focused bool
}

// SplitPane styles one of two panes that share a surface, as the SFTP Tab's
// file panes: Classic divides them with a rule and marks the focused one
// with an accent bar along its top; the macOS style draws each as its own
// rounded card, the focused one outlined in the accent.
func SplitPane(c *ui.Context, pane ui.Element, o SplitPaneOptions) ui.Element {
	t := c.Theme()
	if CurrentStyle().Native() {
		pane.Radius(M(c).Radius.Card).Background(Raised(t)).Border(1, t.Border).
			Shadow(0, 1, 3, 0, CardShadow(t))
		if o.Focused {
			pane.BorderColor(t.Accent.Alpha(0.6))
		}
		return pane
	}
	if o.First {
		if o.Across {
			pane.BorderWidth(0, 1, 0, 0)
		} else {
			pane.BorderWidth(0, 0, 1, 0)
		}
		pane.BorderColor(t.Border)
	}
	if o.Focused {
		pane.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: 2}, t.Accent, 0)
		})
	}
	return pane
}

// Badge is a small status capsule: a dot and a label in tone, over a tint of
// it.
func Badge(c *ui.Context, tone ui.Color, text string) ui.Element {
	return ui.Row(c).Gap(5).Padding(2, 8).Radius(M(c).Radius.Pill).AlignItems(ui.Center).
		Background(tone.Alpha(0.12)).Shrink(0).Children(func() {
		StatusDot(c, tone).Size(6, 6)
		ui.Text(c, text).FontSize(M(c).Font.Caption).FontWeight(500).TextColor(tone).NoWrap()
	})
}

// TitleBar styles the window's title bar, which the work Tabs share: it
// lines its children up as the work Tabs and fills it. In Classic it is a
// shade off the surface below; in the macOS style it is clear over the
// window's material, or the surface's own color when flush with it, as over
// a terminal.
func TitleBar(c *ui.Context, bar ui.Element, flush bool) ui.Element {
	t := c.Theme()
	bar.AlignItems(workTabAlign())
	if !CurrentStyle().Native() {
		return bar.Background(Muted(t))
	}
	if flush {
		return bar.Background(t.Background)
	}
	return bar
}

// PaneFrame frames a terminal Pane of a split Tab. Classic outlines each
// Pane, the focused one in the accent; the macOS style draws the Panes flush,
// divided by the split's own line, and marks the focused one with an accent
// rule along its top.
func PaneFrame(c *ui.Context, pane ui.Element, focused bool) ui.Element {
	t := c.Theme()
	if !CurrentStyle().Native() {
		if focused {
			return pane.Border(1, t.Accent.Alpha(0.7))
		}
		return pane.Border(1, t.Border)
	}
	if focused {
		return pane.BorderWidth(2, 0, 0, 0).BorderColor(t.Accent)
	}
	return pane.BorderWidth(2, 0, 0, 0).BorderColor(ui.Transparent)
}
