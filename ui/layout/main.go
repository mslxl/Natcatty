package layout

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/titlebar"
)

// MainSlots fill a MainLayout: the main window's title bar, the home
// pages' sidebar, and the chosen Tab's body.
type MainSlots struct {
	// HomeTabs draws the fixed home and SFTP Tabs at the bar's start.
	HomeTabs func()
	// Tabs draws the work Tab strip.
	Tabs func()
	// Flush marks a body that shares the bar's color, as a terminal does.
	Flush bool
	// Body draws the chosen Tab's surface: a HomeLayout or a WorkLayout.
	Body func()
}

// SidebarSlots fill the home pages' sidebar.
type SidebarSlots struct {
	// Brand heads the column.
	Brand func()
	// Pages draws the home Tab's NavItems.
	Pages func()
	// SettingsLabel names the Settings item at the foot; Settings opens it.
	SettingsLabel string
	Settings      func()
}

// MainLayout is the main window: one title bar across it holding the home
// and SFTP Tabs and the work Tabs, over the chosen Tab's body. Both styles
// share it; each draws the bar and its Tabs its own way.
func MainLayout(c *ui.Context, slots MainSlots) {
	t := c.Theme()
	WindowFrame(c, FrameOptions{Opaque: true, Vibrant: true}, func() {
		left, right := titlebar.Insets(c, titlebar.Tabs)
		titleBar(c, slots.Flush, 12+left, 8+right, func() {
			slots.HomeTabs()
			ui.Box(c).Size(1, 16).Margin(0, 6).Background(t.Border).Shrink(0).AlignSelf(ui.Center)
			slots.Tabs()
		})
	}, slots.Body)
}

// titleBar is the bar the work Tabs share with the window's title: its
// empty room drags the window. It keeps its height when the body needs
// room: without Shrink(0) a Column under height pressure shrinks it first.
func titleBar(c *ui.Context, flush bool, start, end float32, children func()) {
	bar := ui.Row(c).FillWidth().Height(titlebar.Height).Shrink(0).Padding(0, end, 0, start).Gap(2)
	kit.TitleBar(c, bar, flush).DragWindow().Children(func() {
		titlebar.DragStripe(c)
		children()
	})
}

// Sidebar is the home Tab's navigation column: the brand, the pages in a
// list that scrolls when the window is too short for them, and Settings at
// the foot. Settings is a window rather than a page, so its item is never
// active.
func Sidebar(c *ui.Context, slots SidebarSlots) {
	slots.Brand()
	ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
		kit.NavList(c, kit.NavHome, slots.Pages)
	})
	if kit.NavItem(c, kit.NavHome, "nav-settings", icons.Settings, slots.SettingsLabel, false).Clicked() {
		slots.Settings()
	}
}
