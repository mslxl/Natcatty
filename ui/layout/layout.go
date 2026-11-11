// Package layout holds the windows' page shells: where the title bar, the
// sidebar, the content, and the docked panels go. A view fills the shell's
// slots and keeps its own state; the shell decides the geometry, so an
// Interface Style can change it in one place.
package layout

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/titlebar"
)

// SidebarWidth is the fixed width of the home Tab's navigation column. Only
// the home Tab has one: SFTP and the terminal Tabs take the window's width.
const SidebarWidth float32 = 220

// ShellPadding is the distance from the body's edges to a surface's content:
// the gap around the content card plus the card's own padding.
const ShellPadding float32 = 16

// CardGap is the room between the window's edges and the content card.
// CardPadding insets a surface inside the card, so the two add up to
// ShellPadding. CardBorder is the width of the card's outline.
const (
	CardGap     float32 = 8
	CardPadding         = ShellPadding - CardGap
	CardBorder  float32 = 1
)

// FrameOptions configure a WindowFrame.
type FrameOptions struct {
	// Opaque paints the window's background under the frame. A clear frame
	// leaves the window's own background, as Settings does.
	Opaque bool
	// Vibrant lets a style whose window is vibrant show the window's
	// material through the frame instead, where the body paints nothing.
	Vibrant bool
}

// SidebarRadius rounds the macOS style's floating sidebar: concentric with
// the window's corners at its CardGap inset.
const SidebarRadius float32 = 18

// WindowFrame is a window's root: its title bar, then its body below.
func WindowFrame(c *ui.Context, o FrameOptions, bar, body func()) {
	frame := ui.Column(c).Fill()
	vibrant := o.Vibrant && kit.Vibrant(kit.CurrentStyle())
	switch {
	case vibrant:
		kit.Fill(c, c.Root(), kit.SurfaceWindow)
	case o.Opaque:
		frame.Background(c.Theme().Background)
	}
	frame.Children(func() {
		bar()
		body()
	})
}

// bodyRow is the row under the title bar every main window surface draws
// in. MinHeight(0) and MinWidth(0) keep it at the window's size when a
// surface's content is larger; it scrolls or clips inside instead.
func bodyRow(c *ui.Context, children func()) {
	ui.Row(c).FillWidth().Grow(1).MinHeight(0).MinWidth(0).Children(children)
}

// HomeSlots fill a HomeLayout.
type HomeSlots struct {
	// Sidebar draws the navigation column's entries; nil spans the card
	// across the window, as SFTP's.
	Sidebar func()
	// Notices draws the window's problems above the card; may be nil.
	Notices func()
	// Content draws the page inside the card.
	Content func()
}

// HomeContentWidth returns the width a page drawn by HomeLayout's Content
// slot has at the window's current width. A surface that must choose a
// compact form before layout runs, as an icon-only tab strip, measures
// against it.
func HomeContentWidth(c *ui.Context, hasSidebar bool) float32 {
	width, _ := c.Size()
	if width <= 0 {
		return 0
	}
	if hasSidebar {
		width -= SidebarWidth
	}
	if kit.CurrentStyle().Native() {
		return max(0, width-2*ShellPadding)
	}
	if hasSidebar {
		// The sidebar leaves the card's left inset to the row; the card
		// then takes its right inset, its outline, and its own padding.
		return max(0, width-CardGap-2*(CardPadding+CardBorder))
	}
	return max(0, width-2*(CardGap+CardPadding+CardBorder))
}

// HomeLayout is the main window's page body: the sidebar, and the raised,
// rounded card the page draws in. The macOS style floats the sidebar, inset
// on glass, beside the page drawn flush, with no card.
func HomeLayout(c *ui.Context, slots HomeSlots) {
	if kit.CurrentStyle().Native() {
		nativeHome(c, slots)
		return
	}
	bodyRow(c, func() {
		start := CardGap
		if slots.Sidebar != nil {
			// FillHeight, not Fill: the sidebar shares the row with the
			// card, and Fill would set its width to the whole window.
			ui.Column(c).Width(SidebarWidth).FillHeight().Shrink(0).Padding(4, 8, 8, 8).Gap(2).
				Children(slots.Sidebar)
			// The sidebar already leaves room at the card's left.
			start = 0
		}
		t := c.Theme()
		ui.Column(c).Grow(1).FillHeight().MinWidth(0).Padding(0, CardGap, CardGap, start).Gap(CardGap).Children(func() {
			if slots.Notices != nil {
				slots.Notices()
			}
			ui.Column(c).FillWidth().Grow(1).MinHeight(0).MinWidth(0).Padding(CardPadding).
				Background(t.Surface).Border(CardBorder, t.Border).Radius(kit.M(c).Radius.Card).
				Shadow(0, 1, 3, 0, kit.CardShadow(t)).Clip().Children(slots.Content)
		})
	})
}

func nativeHome(c *ui.Context, slots HomeSlots) {
	t := c.Theme()
	bodyRow(c, func() {
		if slots.Sidebar != nil {
			floatingSidebar(c, SidebarWidth, 0, 2, slots.Sidebar)
		}
		// The page fills the rest of the body, and its notices sit above
		// it on its own background.
		ui.Column(c).Grow(1).FillHeight().MinWidth(0).Background(t.Background).Children(func() {
			if slots.Notices != nil {
				ui.Column(c).FillWidth().Padding(CardGap, CardGap, 0, CardGap).Gap(CardGap).Children(slots.Notices)
			}
			ui.Column(c).FillWidth().Grow(1).MinHeight(0).MinWidth(0).Padding(ShellPadding).Children(slots.Content)
		})
	})
}

// floatingSidebar is the macOS style's sidebar: a panel inset by CardGap
// from the window's edges below top, width wide with the inset, lifted off
// the window by a hairline and a soft shadow.
func floatingSidebar(c *ui.Context, width, top, gap float32, children func()) {
	t := c.Theme()
	ui.Column(c).Width(width).FillHeight().Shrink(0).Padding(top, 0, CardGap, CardGap).Children(func() {
		panel := ui.Column(c).Fill().MinHeight(0).Padding(8).Gap(gap).Radius(SidebarRadius).Clip().
			Border(1, t.Border).Shadow(0, 1, 4, 0, ui.RGB(0, 0, 0).Alpha(0.06)).Background(kit.Raised(t))
		panel.Children(children)
	})
}

// insetPanel is the macOS style's docked panel: a raised, rounded card inset
// from the window's edges, as the home sidebar floats, and set off from the
// terminal beside it.
func insetPanel(c *ui.Context, dock Dock, panel func()) func() {
	return func() {
		t := c.Theme()
		outer := ui.Column(c).Fill().MinWidth(0).MinHeight(0)
		top := float32(0)
		if dock == DockBottom {
			top = CardGap
		}
		outer.Padding(top, CardGap, CardGap, CardGap)
		outer.Children(func() {
			ui.Column(c).Fill().MinWidth(0).MinHeight(0).Radius(kit.M(c).Radius.Card).Clip().
				Border(1, t.Border).Shadow(0, 1, 4, 0, kit.CardShadow(t)).Background(kit.Raised(t)).
				Children(panel)
		})
	}
}

// Dock is the edge a WorkLayout's panel docks at.
type Dock int

const (
	DockLeft Dock = iota
	DockRight
	DockBottom
)

// WorkSlots fill a WorkLayout.
type WorkSlots struct {
	// Top draws above the panes: notices and the input panels; may be nil.
	Top func()
	// Panes draws the terminal's split tree; nil draws nothing below Top.
	Panes func()
	// Panel draws the docked side panel; nil gives the panes the room.
	Panel func()
	Dock  Dock
	// Width is the panel's width when docked at a side, and Ratio the panes'
	// share in percent when docked at the bottom. The divider writes them.
	Width, Ratio *float32
}

// WorkLayout is a terminal Tab's body: the panes reach the window's edges,
// as the original's, with the side panel docked beside or below them, flush
// with the terminal: the split's own one-DIP line is the only separator.
func WorkLayout(c *ui.Context, slots WorkSlots) {
	bodyRow(c, func() {
		// The body paints its own background, so a vibrant window shows its
		// material only beside the home pages, never around a terminal.
		ui.Column(c).Grow(1).FillHeight().MinWidth(0).Background(c.Theme().Background).Children(func() {
			if slots.Top != nil {
				slots.Top()
			}
			if slots.Panes == nil {
				return
			}
			if slots.Panel != nil && kit.CurrentStyle().Native() {
				slots.Panel = insetPanel(c, slots.Dock, slots.Panel)
			}
			panes := func() { ui.Column(c).Fill().MinWidth(0).Children(slots.Panes) }
			if slots.Panel == nil {
				ui.Column(c).FillWidth().Grow(1).MinHeight(0).Children(panes)
				return
			}
			switch slots.Dock {
			case DockBottom:
				ui.SplitVerticalPercent(c, slots.Ratio, panes, slots.Panel).FillWidth().Grow(1).MinHeight(0)
			case DockRight:
				// Split puts its first child, the panel and its width, at
				// the leading edge; a right-to-left box makes that the right
				// one, and each side turns back to left to right inside.
				ui.Row(c).Direction(ui.RightToLeft).FillWidth().Grow(1).MinHeight(0).Children(func() {
					ui.Split(c, slots.Width, func() {
						ui.Column(c).Direction(ui.LeftToRight).Fill().MinWidth(0).Children(slots.Panel)
					}, func() {
						ui.Column(c).Direction(ui.LeftToRight).Fill().MinWidth(0).Children(panes)
					}).Fill()
				})
			default:
				ui.Split(c, slots.Width, slots.Panel, panes).FillWidth().Grow(1).MinHeight(0)
			}
		})
	})
}

// SettingsSlots fill a SettingsLayout.
type SettingsSlots struct {
	// Title heads the window's title bar.
	Title string
	// SidebarWidth is the nav column's width; Sidebar draws its entries.
	SidebarWidth float32
	Sidebar      func()
	// Content draws the chosen area's pane.
	Content func()
}

// SettingsLayout is the Settings window: a title bar that drags the window
// and leaves the close button its end, then the nav column beside the
// content pane on a faint muted fill.
func SettingsLayout(c *ui.Context, slots SettingsSlots) {
	if kit.CurrentStyle().Native() {
		nativeSettings(c, slots)
		return
	}
	t := c.Theme()
	WindowFrame(c, FrameOptions{}, func() {
		left, right := titlebar.Insets(c, titlebar.Dialog)
		ui.Row(c).Key("title-bar").FillWidth().Shrink(0).Padding(titlebar.CloseTop, 16+right, 8, 16+left).
			AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
			DragWindow().Children(func() {
			ui.Row(c).Height(titlebar.CloseSize).AlignItems(ui.Center).Children(func() {
				ui.Text(c, slots.Title).FontSize(kit.M(c).Font.Title).FontWeight(600)
			})
		})
	}, func() {
		ui.Row(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Width(slots.SidebarWidth).Shrink(0).FillHeight().MinHeight(0).Padding(12).Gap(8).
				BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(slots.Sidebar)
			ui.Column(c).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Background(t.TextMuted.Alpha(0.04)).
				Children(slots.Content)
		})
	})
}

// nativeSettings is the macOS style's Settings window, as System Settings:
// the floating sidebar runs the window's height under the window controls,
// with the search at its top, and the pane beside it has the title bar.
func nativeSettings(c *ui.Context, slots SettingsSlots) {
	t := c.Theme()
	WindowFrame(c, FrameOptions{Vibrant: true}, func() {}, func() {
		ui.Row(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			// The panel's inner padding is 4 DIPs less than Classic's column,
			// which the inset at its left makes up for.
			floatingSidebar(c, slots.SidebarWidth, CardGap, 8, func() {
				// The window controls sit in the panel's top corner, centered
				// on the title bar's close row; the room down to its foot,
				// above the search, drags the window.
				room := titlebar.CloseTop + titlebar.CloseSize - 2*CardGap
				ui.Box(c).FillWidth().Height(room).Shrink(0).DragWindow()
				slots.Sidebar()
			})
			ui.Column(c).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Background(t.Background).Children(func() {
				_, right := titlebar.Insets(c, titlebar.Dialog)
				ui.Row(c).Key("title-bar").FillWidth().Shrink(0).Padding(titlebar.CloseTop, 16+right, 0, 24).
					AlignItems(ui.Center).DragWindow().Children(func() {
					ui.Row(c).Height(titlebar.CloseSize).AlignItems(ui.Center).Children(func() {
						ui.Text(c, slots.Title).FontSize(kit.M(c).Font.Headline).FontWeight(600)
					})
				})
				slots.Content()
			})
		})
	})
}

// DetachedLayout is a detached Tab's window: as the original's terminal
// popup, a title bar on the terminal's own background with its title
// dimmed and actions at its end, which drags the window, then body.
func DetachedLayout(c *ui.Context, title string, actions, body func()) {
	t := c.Theme()
	WindowFrame(c, FrameOptions{Opaque: true}, func() {
		left, right := titlebar.Insets(c, titlebar.Popup)
		ui.Row(c).FillWidth().Height(titlebar.Height).Shrink(0).Padding(0, 4+right, 0, 16+left).Gap(8).
			AlignItems(ui.Center).Background(t.Background).DragWindow().Children(func() {
			ui.Text(c, title).FontSize(kit.M(c).Font.Footnote).FontWeight(500).TextColor(t.Text.Alpha(0.7)).
				SingleLine().MinWidth(0).Shrink(1)
			ui.Column(c).Grow(1)
			if actions != nil {
				actions()
			}
		})
	}, body)
}

// LockLayout is the lock screen: content centered in a window that, being
// frameless, drags as a whole.
func LockLayout(c *ui.Context, content func()) {
	// The macOS style shows the window's material behind the lock.
	if kit.Vibrant(kit.CurrentStyle()) {
		kit.Fill(c, c.Root(), kit.SurfaceWindow)
	}
	ui.Column(c).Fill().DragWindow().Children(func() {
		ui.Column(c).FillWidth().Grow(1).MinHeight(0).Padding(48).Gap(12).Justify(ui.Center).AlignItems(ui.Center).
			Children(content)
	})
}
