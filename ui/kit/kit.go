// Package kit is the interface's small component set: the buttons, cards,
// avatars, dialogs, and panels every surface draws with, so the app has one
// look. It reproduces the original's shadcn components on MyGo widgets and
// takes every color from the window's theme.
package kit

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
)

// Sizes shared by every surface.
//
// Deprecated: use M(c).Radius.Card, M(c).Radius.Control,
// M(c).Control.Large, and M(c).Icon.Regular, which follow the Interface
// Style.
const (
	RadiusCard    float32 = 10
	RadiusControl float32 = 6
	ControlHeight float32 = 32
	IconSize      float32 = 16
)

// Muted is a quiet fill for a secondary surface: a sidebar, a toolbar input,
// or a selected list row when the window has no focus.
func Muted(t *ui.Theme) ui.Color { return t.Background.Mix(t.Text, 0.05) }

// Subtle is the fill of a hovered ghost control.
func Subtle(t *ui.Theme) ui.Color { return t.Background.Mix(t.Text, 0.08) }

// Strong is the fill of a pressed ghost control or an active nav item.
func Strong(t *ui.Theme) ui.Color { return t.Background.Mix(t.Text, 0.12) }

// CardShadow is the soft shadow under a raised card.
func CardShadow(t *ui.Theme) ui.Color {
	if t.Dark {
		return ui.RGBA(0, 0, 0, 0.35)
	}
	return ui.RGBA(15, 23, 42, 0.08)
}

// Icon draws a named Lucide icon at the control icon size.
func Icon(c *ui.Context, name string) ui.Element {
	return ui.Icon(c, icons.Lucide(name)).Size(M(c).Icon.Regular, M(c).Icon.Regular)
}

// IconButton is a square ghost button showing only an icon. label names it
// for assistive technology and the tests, and shows as its tooltip.
func IconButton(c *ui.Context, name, label string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Label(label).Tooltip(label).
		Size(M(c).Control.Regular, M(c).Control.Regular).Radius(M(c).Radius.Control).Justify(ui.Center).AlignItems(ui.Center).
		TextColor(t.TextMuted).Shrink(0)
	switch {
	case button.Pressed():
		button.Background(Strong(t)).TextColor(t.Text)
	case button.Hovered():
		button.Background(Subtle(t)).TextColor(t.Text)
	}
	button.Children(func() { Icon(c, name) })
	return button
}

// GhostButton is a flat button with an icon and a label, as a toolbar's.
// An empty icon name shows the label alone.
func GhostButton(c *ui.Context, name, label string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Label(label).Height(M(c).Control.Large).Padding(0, 10).Gap(6).
		Radius(M(c).Radius.Control).AlignItems(ui.Center).TextColor(t.Text).Shrink(0)
	switch {
	case button.Pressed():
		button.Background(Strong(t))
	case button.Hovered():
		button.Background(Subtle(t))
	}
	button.Children(func() {
		if name != "" {
			Icon(c, name)
		}
		ui.Text(c, label).SingleLine()
	})
	return button
}

// SecondaryButton is a filled neutral button with an optional icon, the
// original's secondary variant.
func SecondaryButton(c *ui.Context, name, label string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Label(label).Height(M(c).Control.Large).Padding(0, 12).Gap(6).
		Radius(M(c).Radius.Control).AlignItems(ui.Center).TextColor(t.Text).
		Background(Muted(t)).Shrink(0)
	switch {
	case button.Pressed():
		button.Background(Strong(t))
	case button.Hovered():
		button.Background(Subtle(t))
	}
	button.Children(func() {
		if name != "" {
			Icon(c, name)
		}
		ui.Text(c, label).SingleLine()
	})
	return button
}

// PrimaryButton is the accent button with an optional icon.
func PrimaryButton(c *ui.Context, name, label string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Label(label).Height(M(c).Control.Large).Padding(0, 14).Gap(6).
		Radius(M(c).Radius.Control).AlignItems(ui.Center).TextColor(t.AccentText).
		Background(t.Accent).Shrink(0)
	switch {
	case button.Pressed():
		button.Background(t.AccentPressed)
	case button.Hovered():
		button.Background(t.AccentHover)
	}
	button.Children(func() {
		if name != "" {
			Icon(c, name)
		}
		ui.Text(c, label).SingleLine().FontWeight(500)
	})
	return button
}

// DangerButton is a destructive action's button.
func DangerButton(c *ui.Context, name, label string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Label(label).Height(M(c).Control.Large).Padding(0, 14).Gap(6).
		Radius(M(c).Radius.Control).AlignItems(ui.Center).TextColor(ui.RGB(255, 255, 255)).
		Background(t.Danger).Shrink(0)
	if button.Hovered() || button.Pressed() {
		button.Background(t.Danger.Mix(ui.RGB(0, 0, 0), 0.12))
	}
	button.Children(func() {
		if name != "" {
			Icon(c, name)
		}
		ui.Text(c, label).SingleLine().FontWeight(500)
	})
	return button
}

// Card is a raised, rounded surface. hoverable cards lift on hover, as the
// Vault's Host cards do. Build the card's content with Children.
func Card(c *ui.Context, hoverable bool) ui.Element {
	t := c.Theme()
	card := ui.Column(c).Background(t.Surface).Border(1, t.Border).Radius(M(c).Radius.Card)
	if hoverable && card.Hovered() {
		card.Border(1, t.Border.Mix(t.Text, 0.18)).
			Shadow(0, 4, 14, 0, CardShadow(t))
	} else {
		card.Shadow(0, 1, 2, 0, CardShadow(t))
	}
	return card
}

// SectionHeader is the small muted title above a group of cards, with an
// optional trailing text such as a count.
func SectionHeader(c *ui.Context, title, trailing string) {
	t := c.Theme()
	ui.Row(c).FillWidth().AlignItems(ui.Center).Padding(4, 2).Children(func() {
		ui.Text(c, title).FontSize(M(c).Font.Callout).FontWeight(600).TextColor(t.TextMuted).Grow(1).SingleLine()
		if trailing != "" {
			ui.Text(c, trailing).FontSize(M(c).Font.Footnote).TextColor(t.TextMuted).SingleLine()
		}
	})
}

// SectionBar is the header row of a list section: the SectionHeader title,
// and the section's actions, such as its create buttons, at the row's end.
// actions may be nil.
func SectionBar(c *ui.Context, title string, actions func()) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Wrap().Padding(10, 0, 6, 4).Children(func() {
		ui.Text(c, title).FontSize(M(c).Font.Callout).FontWeight(600).TextColor(t.TextMuted).
			Grow(1).MinWidth(0).SingleLine()
		if actions != nil {
			actions()
		}
	})
}

// UnderlineTabs is a surface's view switcher: text tabs with an accent bar
// under the chosen one. It sets *chosen when a tab is picked. reveal scrolls
// the chosen tab into view when the strip sits in a horizontal scroll.
func UnderlineTabs(c *ui.Context, chosen *int, labels []string, reveal bool) {
	t := c.Theme()
	parts := ui.TabsBase(c, chosen, len(labels))
	parts.List.Gap(t.Space(1)).Children(func() {
		for i, label := range labels {
			on := i == *chosen
			tab := parts.Tab(i).Padding(t.Space(2), t.Space(3)).FocusRing(false)
			tab.TextColor(t.TextMuted)
			if on {
				tab.TextColor(t.Text).FontWeight(600)
				if reveal {
					tab = tab.ScrollIntoView()
				}
			} else if tab.Hovered() {
				tab.TextColor(t.Text)
			}
			tab.DrawOver(func(p *ui.Painter, r ui.Rect) {
				if on {
					in, height := t.Space(1.5), t.Space(0.5)
					p.Fill(ui.Rect{X: r.X + in, Y: r.Y + r.H - height, W: r.W - 2*in, H: height},
						t.Accent, height/2)
				}
				if tab.FocusVisible() {
					p.FocusRing(r, [4]float32{t.Radius, t.Radius, t.Radius, t.Radius})
				}
			})
			tab.Children(func() { ui.Text(c, label).SingleLine() })
		}
	})
}

// PageTitle is a surface's heading with an optional description.
func PageTitle(c *ui.Context, title, description string) {
	t := c.Theme()
	ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
		ui.Text(c, title).FontSize(M(c).Font.Title).FontWeight(600).SingleLine()
		if description != "" {
			ui.Text(c, description).FontSize(M(c).Font.Footnote).TextColor(t.TextMuted)
		}
	})
}

// EmptyState is the centered placeholder of an empty list: an icon in a
// tile, a title, a description, and an optional action built by action.
func EmptyState(c *ui.Context, name, title, description string, action func()) ui.Element {
	t := c.Theme()
	return ui.Column(c).FillWidth().Padding(40, 16).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(48, 48).Radius(12).Background(Muted(t)).Center().TextColor(t.TextMuted).
			Children(func() { ui.Icon(c, icons.Lucide(name)).Size(22, 22) })
		ui.Text(c, title).FontWeight(600).Margin(6, 0, 0, 0)
		if description != "" {
			ui.Text(c, description).FontSize(M(c).Font.Callout).TextColor(t.TextMuted).MaxWidth(420).TextAlign(ui.Center)
		}
		if action != nil {
			ui.Row(c).Gap(8).Margin(8, 0, 0, 0).Children(action)
		}
	})
}

// EmptyHint is the one-line placeholder of an empty section inside a longer
// list, inset to line up with its SectionBar title. A surface that is empty
// as a whole uses EmptyState instead.
func EmptyHint(c *ui.Context, text string) ui.Element {
	t := c.Theme()
	return ui.Text(c, text).FontSize(M(c).Font.Footnote).TextColor(t.TextMuted).Padding(0, 4, 4, 4)
}

// ListRow is one item of a list section: a bordered row in the shape of the
// Vault's Host cards that shades on hover. Build its content with Children;
// its actions go last, as IconButtons.
func ListRow(c *ui.Context) ui.Element {
	t := c.Theme()
	row := ui.Row(c).FillWidth().MinWidth(0).Gap(8).Padding(8, 8, 8, 12).Margin(0, 0, 6, 0).
		Radius(M(c).Radius.Card).Border(1, t.Border).Background(t.Surface).AlignItems(ui.Center)
	if row.Hovered() {
		row.Background(Muted(t)).Border(1, t.Border.Mix(t.Text, 0.15))
	}
	return row
}

// ConfirmBar asks inline to confirm a destructive action: a danger Notice
// with text, an optional detail under it, and cancel and confirm buttons. It
// reports which button was clicked.
func ConfirmBar(c *ui.Context, text string, detail func(), cancel, confirm string) (cancelled, confirmed bool) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(10).Padding(10, 12).Radius(M(c).Radius.Control).Wrap().
		Background(t.Danger.Alpha(0.08)).Border(1, t.Danger.Alpha(0.35)).AlignItems(ui.Center).
		Children(func() {
			ui.Icon(c, icons.Lucide(icons.TriangleAlert)).Size(M(c).Icon.Regular, M(c).Icon.Regular).TextColor(t.Danger)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, text).FontSize(M(c).Font.Callout).TextColor(t.Text)
				if detail != nil {
					detail()
				}
			})
			ui.Row(c).Gap(8).Children(func() {
				cancelled = GhostButton(c, "", cancel).Clicked()
				confirmed = DangerButton(c, "", confirm).Clicked()
			})
		})
	return cancelled, confirmed
}

// Pill is a small rounded label, as a tag or a count.
func Pill(c *ui.Context, text string) ui.Element {
	t := c.Theme()
	return ui.Box(c).Padding(1, 7).Radius(M(c).Radius.Pill).Background(Muted(t)).Border(1, t.Border).
		Children(func() { ui.Text(c, text).FontSize(M(c).Font.Caption).TextColor(t.TextMuted).SingleLine() })
}

// StatusDot is a small filled circle in color.
func StatusDot(c *ui.Context, color ui.Color) ui.Element {
	return ui.Box(c).Size(8, 8).Radius(4).Background(color).Shrink(0)
}

// Notice is an inline message row: an icon, text, and optional actions.
// tone is the color of the icon and the left edge.
func Notice(c *ui.Context, name string, tone ui.Color, text string, actions func()) ui.Element {
	t := c.Theme()
	return ui.Row(c).FillWidth().Gap(10).Padding(10, 12).Radius(M(c).Radius.Control).
		Background(tone.Alpha(0.08)).Border(1, tone.Alpha(0.35)).AlignItems(ui.Center).
		Children(func() {
			ui.Icon(c, icons.Lucide(name)).Size(M(c).Icon.Regular, M(c).Icon.Regular).TextColor(tone)
			ui.Text(c, text).FontSize(M(c).Font.Callout).Grow(1).MinWidth(0).TextColor(t.Text)
			if actions != nil {
				actions()
			}
		})
}
