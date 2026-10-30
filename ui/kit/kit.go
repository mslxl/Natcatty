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
const (
	// RadiusCard rounds cards, panels, and dialogs.
	RadiusCard float32 = 10
	// RadiusControl rounds buttons, inputs, and list rows.
	RadiusControl float32 = 6
	// ControlHeight is the height of a toolbar button or input.
	ControlHeight float32 = 32
	// IconSize is the size of an icon inside a control.
	IconSize float32 = 16
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
	return ui.Icon(c, icons.Lucide(name)).Size(IconSize, IconSize)
}

// IconButton is a square ghost button showing only an icon. label names it
// for assistive technology and the tests, and shows as its tooltip.
func IconButton(c *ui.Context, name, label string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Label(label).Tooltip(label).
		Size(28, 28).Radius(RadiusControl).Justify(ui.Center).AlignItems(ui.Center).
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
	button := ui.ButtonBase(c).Label(label).Height(ControlHeight).Padding(0, 10).Gap(6).
		Radius(RadiusControl).AlignItems(ui.Center).TextColor(t.Text).Shrink(0)
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
	button := ui.ButtonBase(c).Label(label).Height(ControlHeight).Padding(0, 12).Gap(6).
		Radius(RadiusControl).AlignItems(ui.Center).TextColor(t.Text).
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
	button := ui.ButtonBase(c).Label(label).Height(ControlHeight).Padding(0, 14).Gap(6).
		Radius(RadiusControl).AlignItems(ui.Center).TextColor(t.AccentText).
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
	button := ui.ButtonBase(c).Label(label).Height(ControlHeight).Padding(0, 14).Gap(6).
		Radius(RadiusControl).AlignItems(ui.Center).TextColor(ui.RGB(255, 255, 255)).
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
	card := ui.Column(c).Background(t.Surface).Border(1, t.Border).Radius(RadiusCard)
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
		ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(t.TextMuted).Grow(1).SingleLine()
		if trailing != "" {
			ui.Text(c, trailing).FontSize(12).TextColor(t.TextMuted).SingleLine()
		}
	})
}

// PageTitle is a surface's heading with an optional description.
func PageTitle(c *ui.Context, title, description string) {
	t := c.Theme()
	ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
		ui.Text(c, title).FontSize(18).FontWeight(600).SingleLine()
		if description != "" {
			ui.Text(c, description).FontSize(12).TextColor(t.TextMuted)
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
			ui.Text(c, description).FontSize(13).TextColor(t.TextMuted).MaxWidth(420)
		}
		if action != nil {
			ui.Row(c).Gap(8).Margin(8, 0, 0, 0).Children(action)
		}
	})
}

// Pill is a small rounded label, as a tag or a count.
func Pill(c *ui.Context, text string) ui.Element {
	t := c.Theme()
	return ui.Box(c).Padding(1, 7).Radius(999).Background(Muted(t)).Border(1, t.Border).
		Children(func() { ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).SingleLine() })
}

// StatusDot is a small filled circle in color.
func StatusDot(c *ui.Context, color ui.Color) ui.Element {
	return ui.Box(c).Size(8, 8).Radius(4).Background(color).Shrink(0)
}

// Notice is an inline message row: an icon, text, and optional actions.
// tone is the color of the icon and the left edge.
func Notice(c *ui.Context, name string, tone ui.Color, text string, actions func()) ui.Element {
	t := c.Theme()
	return ui.Row(c).FillWidth().Gap(10).Padding(10, 12).Radius(RadiusControl).
		Background(tone.Alpha(0.08)).Border(1, tone.Alpha(0.35)).AlignItems(ui.Center).
		Children(func() {
			ui.Icon(c, icons.Lucide(name)).Size(IconSize, IconSize).TextColor(tone)
			ui.Text(c, text).FontSize(13).Grow(1).MinWidth(0).TextColor(t.Text)
			if actions != nil {
				actions()
			}
		})
}
