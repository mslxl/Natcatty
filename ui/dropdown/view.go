// Package dropdown draws the Dropdown Panel's own window chrome: a compact
// top bar with its hide button, then the routed content. The panel is a
// frameless native Panel with no window controls, so the content draws the
// one control it needs.
package dropdown

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// View puts the panel's frame around bar and content. bar draws the routed
// content's own top-bar items, such as the Quake tab strip, in the same row as
// the hide button; nil leaves the leading space empty. The caller supplies bar
// already behind the shared App Lock gate (see applock.Gate.WrapChrome), so
// locking hides routed labels while the frame and its hide button stay usable.
// The content slot also receives the gate, which draws the one lock screen.
func View(onHide func(), bar, content func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		t := c.Theme()
		m := locales.In(c.Locale())
		if kit.Vibrant(kit.CurrentStyle()) {
			kit.Fill(c, c.Root(), kit.SurfaceWindow)
		} else {
			c.Root().Background(t.Background)
		}
		ui.Column(c).Fill().Children(func() {
			ui.Row(c).FillWidth().Height(kit.M(c).Bar.Title).Shrink(0).
				Padding(0, kit.M(c).Space.XSmall, 0, kit.M(c).Space.Large).
				AlignItems(ui.Center).Children(func() {
				if bar != nil {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() { bar(c) })
				} else {
					ui.Column(c).Grow(1)
				}
				if kit.IconButtonWith(c, icons.X, m.DropdownHide(), kit.IconOptions{
					Role: kit.IconCard,
					Key:  "dropdown-hide",
				}).Clicked() && onHide != nil {
					onHide()
				}
			})
			ui.Column(c).FillWidth().Grow(1).MinWidth(0).MinHeight(0).Children(func() { content(c) })
		})
	}
}

// Placeholder is the neutral body shown until a later ticket routes the Quake
// or main-shell content in: no terminal, no shell, no state.
func Placeholder(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).FillWidth().Grow(1).MinWidth(0).MinHeight(0).
		Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icons.Lucide(icons.SquareTerminal)).Size(28, 28).TextColor(t.TextMuted.Alpha(0.6))
	})
}
