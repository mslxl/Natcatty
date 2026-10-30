package kit

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
)

// Dialog shows a centered dialog over the dimmed window while *open is true.
// body builds its content and footer, when not nil, its right-aligned
// buttons. Escape and a click outside set *open to false; Dismissed on the
// returned panel reports it.
func Dialog(c *ui.Context, open *bool, width float32, title, description string, body, footer func()) ui.Element {
	t := c.Theme()
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Width(width).MaxWidthPercent(92).MaxHeightPercent(90).Radius(12).
			Background(t.Surface).Border(1, t.Border).
			Shadow(0, 16, 48, 0, ui.RGBA(0, 0, 0, 0.28)).Clip()
		ui.Column(c).FillWidth().Padding(20, 20, 0, 20).Gap(4).Children(func() {
			ui.Text(c, title).FontSize(16).FontWeight(600)
			if description != "" {
				ui.Text(c, description).FontSize(13).TextColor(t.TextMuted)
			}
		})
		if body != nil {
			ui.Scroll(c).FillWidth().Shrink(1).Children(func() {
				ui.Column(c).FillWidth().Padding(16, 20).Gap(12).Children(body)
			})
		}
		if footer != nil {
			ui.Row(c).FillWidth().Padding(12, 20, 16, 20).Gap(8).Justify(ui.End).Children(footer)
		} else {
			ui.Box(c).Height(16)
		}
	})
}

// Confirm asks to confirm a destructive action while open is true. It
// reports which button was pressed this frame; Escape or a click outside
// count as cancel.
func Confirm(c *ui.Context, open bool, title, message, confirmLabel, cancelLabel string) (confirmed, cancelled bool) {
	if !open {
		return false, false
	}
	shown := true
	panel := Dialog(c, &shown, 420, title, message, nil, func() {
		if SecondaryButton(c, "", cancelLabel).Clicked() {
			cancelled = true
		}
		if DangerButton(c, icons.Trash, confirmLabel).AutoFocus().Clicked() {
			confirmed = true
		}
	})
	if !shown || panel.Dismissed() {
		cancelled = true
	}
	return confirmed, cancelled
}

// Panel is the side panel an editor opens in, beside the list it edits, as
// the original's Host details panel: a header with a title, an optional
// subtitle, and a close button; a scrolling body; and a footer of actions.
// closeLabel names the close button. It returns the panel's column.
func Panel(c *ui.Context, width float32, title, subtitle, closeLabel string,
	scroll *ui.ScrollState, onClose func(), body, footer func()) ui.Element {
	t := c.Theme()
	panel := ui.Column(c).Width(width).MinWidth(0).Shrink(1).FillHeight().
		Background(t.Surface).BorderWidth(0, 0, 0, 1).BorderColor(t.Border)
	panel.Children(func() {
		ui.Row(c).FillWidth().Height(52).Padding(0, 12, 0, 18).Gap(8).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Shrink(0).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, title).FontWeight(600).SingleLine()
				if subtitle != "" {
					ui.Text(c, subtitle).FontSize(12).TextColor(t.TextMuted).SingleLine()
				}
			})
			if onClose != nil && IconButton(c, icons.X, closeLabel).Clicked() {
				onClose()
			}
		})
		area := ui.Scroll(c).FillWidth().Grow(1).MinHeight(0)
		if scroll != nil {
			area.TrackScroll(scroll)
		}
		area.Children(func() {
			ui.Column(c).FillWidth().Padding(16, 18).Gap(14).Children(body)
		})
		if footer != nil {
			ui.Row(c).FillWidth().Padding(12, 18).Gap(8).Justify(ui.End).AlignItems(ui.Center).
				BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Shrink(0).Children(footer)
		}
	})
	return panel
}

// FormSection is a titled group of fields inside a panel or page, drawn as
// the original's bordered field groups.
func FormSection(c *ui.Context, title string, body func()) ui.Element {
	t := c.Theme()
	return ui.Column(c).FillWidth().Gap(10).Children(func() {
		if title != "" {
			ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(t.TextMuted)
		}
		ui.Column(c).FillWidth().Gap(12).Padding(14).Radius(RadiusCard).
			Background(t.Background).Border(1, t.Border).Children(body)
	})
}
