package layout

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/titlebar"
)

// MainSlots fill a MainLayout: the main window's title bar, its sidebar,
// and the chosen Tab's body.
type MainSlots struct {
	// HomeTabs draws the fixed home and SFTP Tabs. Classic shows them at the
	// bar's start; the macOS style lists those pages in the sidebar instead.
	HomeTabs func()
	// Tabs draws the work Tab strip.
	Tabs func()
	// Flush marks a body that shares the bar's color, as a terminal does.
	Flush bool
	// Sidebar is the navigation column. Classic draws it beside the home
	// Tab's pages only, from the body's HomeLayout; the macOS style beside
	// every Tab.
	Sidebar SidebarSlots
	// Body draws the chosen Tab's surface: a HomeLayout or a WorkLayout.
	Body func()
}

// SidebarSlots fill the main window's sidebar.
type SidebarSlots struct {
	// Brand heads the column.
	Brand func()
	// Pages draws the home Tab's NavItems.
	Pages func()
	// Places draws the NavItems of pages Classic shows as Tabs instead, as
	// SFTP; may be nil.
	Places func()
	// Actions are the window's actions: Settings and the quick controls.
	Actions Actions
}

// Actions are the main window's own actions. Classic puts them at the
// title bar's end; the macOS style at the sidebar's foot.
type Actions struct {
	SettingsLabel string
	Settings      func()
	// Controls draws the quick controls' panel; nil shows no button.
	ControlsLabel string
	Controls      func()
	ControlsOpen  *bool
}

// MainLayout is the main window. Classic has one title bar across the
// window holding every Tab and the actions, and the sidebar only beside the
// home pages. The macOS style floats the sidebar the window's height beside
// every Tab, the window controls in its top corner and the actions at its
// foot, and keeps the bar beside it for the work Tabs alone.
func MainLayout(c *ui.Context, slots MainSlots) {
	if kit.CurrentStyle().Native() {
		nativeMain(c, slots)
		return
	}
	t := c.Theme()
	WindowFrame(c, FrameOptions{Opaque: true, Vibrant: true}, func() {
		left, right := titlebar.Insets(c, titlebar.Tabs)
		titleBar(c, slots.Flush, 12+left, 8+right, func() {
			slots.HomeTabs()
			ui.Box(c).Size(1, 16).Margin(0, 6).Background(t.Border).Shrink(0).AlignSelf(ui.Center)
			slots.Tabs()
			a := slots.Sidebar.Actions
			if a.Controls != nil {
				controlsButton(c, a, kit.IconBar, kit.PopoverPanel)
			}
			if kit.IconButtonWith(c, icons.Settings, a.SettingsLabel, kit.IconOptions{Role: kit.IconBar, Key: "bar-settings"}).Clicked() {
				a.Settings()
			}
		})
	}, slots.Body)
}

func nativeMain(c *ui.Context, slots MainSlots) {
	t := c.Theme()
	WindowFrame(c, FrameOptions{Opaque: true}, func() {}, func() {
		bodyRow(c, func() {
			floatingSidebar(c, SidebarWidth, CardGap, 2, func() {
				// The window controls sit in the panel's top corner, on the
				// title bar's center line; the room under them drags the
				// window.
				ui.Box(c).FillWidth().Height(titlebar.Height - 2*CardGap).Shrink(0).DragWindow()
				nativeSidebar(c, slots.Sidebar)
			})
			ui.Column(c).Grow(1).FillHeight().MinWidth(0).Background(t.Background).Children(func() {
				_, right := titlebar.Insets(c, titlebar.Tabs)
				titleBar(c, slots.Flush, 8, 8+right, slots.Tabs)
				slots.Body()
			})
		})
	})
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

// Sidebar is the home Tab's navigation column as Classic draws it: the
// brand, the pages in a list that scrolls when the window is too short for
// them, and Settings at the foot. The macOS style draws its column in
// MainLayout instead, so this draws nothing there.
func Sidebar(c *ui.Context, slots SidebarSlots) {
	if kit.CurrentStyle().Native() {
		return
	}
	slots.Brand()
	ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
		kit.NavList(c, kit.NavHome, slots.Pages)
	})
	settingsItem(c, slots.Actions)
}

// nativeSidebar is the macOS style's column: the brand, the pages and
// places, then Settings and the quick controls side by side at the foot.
func nativeSidebar(c *ui.Context, slots SidebarSlots) {
	slots.Brand()
	ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
		kit.NavList(c, kit.NavHome, func() {
			slots.Pages()
			if slots.Places != nil {
				slots.Places()
			}
		})
	})
	a := slots.Actions
	ui.Row(c).FillWidth().Shrink(0).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() { settingsItem(c, a) })
		if a.Controls != nil {
			controlsButton(c, a, kit.IconCard, kit.PopoverBeside)
		}
	})
}

// settingsItem opens Settings. Settings is a window rather than a page, so
// the item is never active.
func settingsItem(c *ui.Context, a Actions) {
	if kit.NavItem(c, kit.NavHome, "nav-settings", icons.Settings, a.SettingsLabel, false).Clicked() {
		a.Settings()
	}
}

// controlsButton toggles the quick controls' panel, which opens from it as
// role places it.
func controlsButton(c *ui.Context, a Actions, icon kit.IconRole, role kit.PopoverRole) {
	open := a.ControlsOpen
	button := kit.IconButtonWith(c, icons.SlidersHorizontal, a.ControlsLabel,
		kit.IconOptions{Role: icon, On: *open, Key: "bar-control-panel"})
	if button.Clicked() {
		*open = !*open
		c.Invalidate()
	}
	kit.Popover(c, role, button, open, a.Controls)
}
