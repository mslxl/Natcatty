package kit

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
)

// OverflowItem is one row of an OverflowMenu: Key is reported when it is
// chosen, Label is drawn beside Icon.
type OverflowItem struct {
	Key, Label, Icon string
}

// OverflowMenuOptions configure an OverflowMenu.
type OverflowMenuOptions struct {
	// Label names the trigger and the menu for assistive technology, as
	// "More tabs"; it is also the trigger's tooltip.
	Label string
	// Icon is the trigger's Lucide glyph; the ellipsis when empty.
	Icon string
	// Title heads the menu in a small muted label, as "Collapsed tabs".
	Title string
	// Width is the menu's width; 180 DIPs when zero.
	Width float32
}

// OverflowMenu draws a square trigger that opens an overflow menu of items
// in order, each an icon and its label. It draws no trigger and closes
// *open when there are no items, so an empty overflow affordance never
// shows. Choosing a row calls choose with its key and closes the menu; a
// click on the trigger toggles it, and a click outside or Escape closes it.
func OverflowMenu(c *ui.Context, items []OverflowItem, open *bool, o OverflowMenuOptions, choose func(key string)) {
	if open == nil {
		return
	}
	if len(items) == 0 {
		*open = false
		return
	}
	name := o.Icon
	if name == "" {
		name = icons.Ellipsis
	}
	trigger := IconButtonWith(c, name, o.Label, IconOptions{Role: IconCard, On: *open})
	if trigger.Clicked() {
		*open = !*open
		c.Invalidate()
	}

	width := o.Width
	if width <= 0 {
		width = 180
	}
	Popover(c, PopoverPanel, trigger, open, func() {
		menu := ui.Column(c).Width(width).Padding(4).Gap(2)
		if o.Label != "" {
			menu.Label(o.Label)
		}
		menu.Children(func() {
			if o.Title != "" {
				ui.Text(c, o.Title).FontSize(M(c).Font.Caption).FontWeight(600).
					TextColor(c.Theme().TextMuted).SingleLine().
					Padding(4, 4, 2, 4+PopoverInset(c))
			}
			for _, item := range items {
				row := ui.ButtonBase(c).FillWidth().Height(M(c).Control.Regular).
					Padding(0, 8).Gap(8).AlignItems(ui.Center).
					Label(item.Label).Shrink(0)
				colors := PopoverRow(c, row, false)
				row.TextColor(colors.Text)
				if row.Clicked() {
					*open = false
					if choose != nil {
						choose(item.Key)
					}
					c.Invalidate()
				}
				row.Children(func() {
					IconSized(c, item.Icon, M(c).Icon.Regular)
					ui.Text(c, item.Label).FontSize(M(c).Font.Footnote).SingleLine()
				})
			}
		})
	})
}
