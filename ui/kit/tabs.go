package kit

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// TabRole picks a tab strip's recipe. The Underline strip, a surface's view
// switcher, is UnderlineTabs.
type TabRole int

const (
	// TabWork is the window's Tab bar: tabs with rounded tops on the bar's
	// floor, the chosen one opening into the surface below.
	TabWork TabRole = iota
	// TabPane is a pane's own Tabs, as the transfer panes': flat cells
	// divided by rules, the chosen one underlined.
	TabPane
)

// workTabRadius rounds a Work tab's top corners.
const workTabRadius float32 = 6

// TabStripOptions configure a TabStrip.
type TabStripOptions struct {
	// Label names the scrolling strip for assistive technology.
	Label string
	// Height is the strip's height; zero takes the role's bar height.
	Height float32
	// Add names the control that opens a new tab; empty leaves it out.
	Add string
}

// TabStrip lays out a row of tabs drawn by tabs, which scrolls sideways when
// they do not fit, and the add control after them. It reports whether the
// add control was clicked.
func TabStrip(c *ui.Context, role TabRole, o TabStripOptions, tabs func()) (added bool) {
	t := c.Theme()
	if role == TabPane && CurrentStyle().Native() {
		return nativePaneStrip(c, o, tabs)
	}
	if role == TabPane {
		height := o.Height
		if height == 0 {
			height = M(c).Bar.Tabs
		}
		Bar(c, BarTabs).Children(func() {
			strip := ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Height(height)
			if o.Label != "" {
				strip.Label(o.Label)
			}
			strip.Children(func() { ui.Row(c).Height(height).Children(tabs) })
			if o.Add == "" {
				return
			}
			add := ui.ButtonBase(c).Label(o.Add).Tooltip(o.Add).
				Width(height).FillHeight().Justify(ui.Center).AlignItems(ui.Center).Shrink(0).
				BorderWidth(0, 0, 0, 1).BorderColor(t.Border).TextColor(t.TextMuted)
			if add.Hovered() {
				add.Background(Subtle(t)).TextColor(t.Text)
			}
			add.Children(func() { IconSized(c, icons.Plus, M(c).Icon.Medium) })
			added = add.Clicked()
		})
		return added
	}
	height := o.Height
	if height == 0 {
		height = M(c).Bar.Title
	}
	align, addMargin := workTabAlign(), [4]float32{0, 0, 4, 4}
	if CurrentStyle().Native() {
		addMargin = [4]float32{0, 0, 0, 4}
	}
	ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Height(height).Label(o.Label).Children(func() {
		ui.Row(c).Height(height).AlignItems(align).Gap(workTabGap(c)).Children(func() {
			tabs()
			if o.Add != "" {
				added = IconButtonWith(c, icons.Plus, o.Add, IconOptions{Role: IconTab}).
					Margin(addMargin[0], addMargin[1], addMargin[2], addMargin[3]).Clicked()
			}
		})
	})
	return added
}

// workTabAlign is where Work tabs sit in their bar: Classic tabs on its
// floor, macOS capsules centered in it.
func workTabAlign() ui.Align {
	if CurrentStyle().Native() {
		return ui.Center
	}
	return ui.End
}

// workTabShape rounds a Work tab: the top corners in Classic, so the chosen
// tab opens into the surface below; a capsule in the macOS style.
func workTabShape(c *ui.Context, tab ui.Element) ui.Element {
	if CurrentStyle().Native() {
		return tab.Radius(M(c).Radius.Pill)
	}
	return tab.Radius(workTabRadius, workTabRadius, 0, 0)
}

// workTabGap is the room between Work tabs: Classic tabs touch, macOS
// capsules stand apart.
func workTabGap(c *ui.Context) float32 {
	if CurrentStyle().Native() {
		return M(c).Space.XSmall
	}
	return 0
}

// workTabFill fills a Work tab: the chosen one with the surface's color in
// Classic, a raised solid capsule in the macOS style, as a segmented
// control's chosen segment; a hovered one with a tint.
func workTabFill(c *ui.Context, tab ui.Element, active, hovered bool) {
	t := c.Theme()
	native := CurrentStyle().Native()
	switch {
	case active && native:
		raisedFill(c, tab)
	case active:
		tab.Background(t.Background)
	case hovered && native:
		tab.Background(t.Text.Alpha(0.06))
	case hovered:
		tab.Background(t.Background.Alpha(0.4))
	}
}

// TabSpec is one tab of a TabStrip.
type TabSpec struct {
	Key    string
	Title  string
	Active bool
	// Icon is the leading glyph; Lead draws the leading mark instead, such
	// as a Session's status dot.
	Icon string
	Lead func()
	// Fixed is a Work tab every window has, such as home: a button sized to
	// its label, with no controls.
	Fixed bool
	// Marker trails the title, such as an unsaved mark.
	Marker string
	// Close names the close control; empty leaves it out. A Work tab shows
	// it, and Menu's, only while active or hovered.
	Close string
	// MenuLabel and Menu are a Work tab's menu control. ContextMenu opens on
	// a right-click.
	MenuLabel   string
	Menu        func(*ui.Menu)
	ContextMenu func(*ui.Menu)
	// Reveal scrolls the tab into view while it is active.
	Reveal bool
}

// TabResult is what a tab reported this frame. Element is the tab itself,
// for the caller's drag and drop.
type TabResult struct {
	Element ui.Element
	Clicked bool
	Closed  bool
}

// Tab draws one tab of a TabStrip of role.
func Tab(c *ui.Context, role TabRole, spec TabSpec) TabResult {
	if role == TabPane && CurrentStyle().Native() {
		return nativePaneTab(c, spec)
	}
	if role == TabPane {
		return paneTab(c, spec)
	}
	if spec.Fixed {
		return fixedWorkTab(c, spec)
	}
	return workTab(c, spec)
}

func fixedWorkTab(c *ui.Context, spec TabSpec) TabResult {
	t := c.Theme()
	tab := ui.ButtonBase(c).Key(spec.Key).Label(spec.Title).Height(M(c).Control.Regular).Padding(0, 12).Gap(8).
		AlignItems(ui.Center).Shrink(0).TextColor(t.TextMuted)
	workTabShape(c, tab)
	workTabFill(c, tab, spec.Active, tab.Hovered())
	if spec.Active || tab.Hovered() {
		tab.TextColor(t.Text)
	}
	tab.Children(func() {
		IconSized(c, spec.Icon, M(c).Icon.Medium)
		ui.Text(c, spec.Title).FontSize(M(c).Font.Footnote).FontWeight(600).SingleLine()
	})
	return TabResult{Element: tab, Clicked: tab.Clicked()}
}

func workTab(c *ui.Context, spec TabSpec) TabResult {
	t := c.Theme()
	body := ui.Row(c).Height(M(c).Control.Regular).MinWidth(140).MaxWidth(240).Padding(0, 4, 0, 12).Gap(8).
		AlignItems(ui.Center).Tooltip(spec.Title)
	workTabShape(c, body)
	if spec.Key != "" {
		body.Key(spec.Key)
	}
	hovered := body.Hovered()
	workTabFill(c, body, spec.Active, hovered)
	result := TabResult{Element: body, Clicked: body.Clicked()}
	if spec.ContextMenu != nil {
		body.ContextMenu(spec.ContextMenu)
	}
	body.Children(func() {
		tabLead(c, spec, M(c).Icon.Medium, t.TextMuted)
		// SingleLine text can shrink to nothing in a flex row; give the title
		// a readable minimum so it is not squeezed out beside the controls.
		title := ui.Text(c, spec.Title).FontSize(M(c).Font.Footnote).FontWeight(600).SingleLine().MinWidth(72).Grow(1)
		if spec.Active || hovered {
			title.TextColor(t.Text)
		} else {
			title.TextColor(t.TextMuted)
		}
		if spec.Marker != "" {
			ui.Text(c, spec.Marker).FontSize(9).TextColor(t.Accent)
		}
		// An idle Tab keeps the controls' room, so showing them on hover
		// moves no Tab after it.
		if !spec.Active && !hovered {
			box, _, _ := iconRecipe(c, IconTab)
			for _, shown := range []bool{spec.Menu != nil, spec.Close != ""} {
				if shown {
					ui.Box(c).Size(box, box).Shrink(0)
				}
			}
			return
		}
		if spec.Menu != nil {
			IconButtonWith(c, icons.Ellipsis, spec.MenuLabel, IconOptions{Role: IconTab}).Menu(spec.Menu)
		}
		if spec.Close != "" {
			result.Closed = IconButtonWith(c, icons.X, spec.Close, IconOptions{Role: IconTab}).Clicked()
		}
	})
	if spec.Active && spec.Reveal {
		body.ScrollIntoView()
	}
	return result
}

func paneTab(c *ui.Context, spec TabSpec) TabResult {
	t := c.Theme()
	el := ui.Row(c).Height(M(c).Bar.Tabs).MinWidth(100).MaxWidth(180).Padding(0, 6, 0, 12).Gap(6).
		AlignItems(ui.Center).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).
		Cursor(ui.CursorPointer).Label(spec.Title)
	if spec.Key != "" {
		el.Key(spec.Key)
	}
	text, glyph := t.TextMuted, t.TextMuted
	switch {
	case spec.Active:
		el.Background(t.Background)
		text, glyph = t.Text, t.Accent
	case el.Hovered():
		el.Background(Subtle(t))
		text = t.Text
	}
	result := TabResult{Element: el}
	el.Children(func() {
		tabLead(c, spec, M(c).Icon.Small, glyph)
		ui.Text(c, spec.Title).FontSize(M(c).Font.Footnote).FontWeight(500).SingleLine().MinWidth(0).Grow(1).TextColor(text)
		if spec.Close != "" {
			closer := ui.ButtonBase(c).Label(spec.Close).Tooltip(spec.Close).
				Size(18, 18).Radius(M(c).Radius.Small).Justify(ui.Center).AlignItems(ui.Center).Shrink(0).
				TextColor(t.TextMuted)
			if closer.Hovered() {
				closer.Background(Strong(t)).TextColor(uitheme.DangerText(t))
			}
			closer.Children(func() { IconSized(c, icons.X, M(c).Icon.Small) })
			result.Closed = closer.Clicked()
		}
		if spec.Active {
			ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(2).Background(t.Accent)
		}
	})
	result.Clicked = el.Clicked()
	if spec.ContextMenu != nil {
		el.ContextMenu(spec.ContextMenu)
	}
	if spec.Active && spec.Reveal {
		el.ScrollIntoView()
	}
	return result
}

// nativePaneStrip is a pane's Tabs in the macOS style: rounded cells apart
// on the pane's own fill, above a hairline.
func nativePaneStrip(c *ui.Context, o TabStripOptions, tabs func()) (added bool) {
	height := o.Height
	if height == 0 {
		height = M(c).Bar.Tabs
	}
	Bar(c, BarTabs).Padding(0, 4).Gap(2).Children(func() {
		strip := ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Height(height)
		if o.Label != "" {
			strip.Label(o.Label)
		}
		strip.Children(func() {
			ui.Row(c).Height(height).Gap(M(c).Space.Hairline).AlignItems(ui.Center).Children(tabs)
		})
		if o.Add != "" {
			added = IconButtonWith(c, icons.Plus, o.Add, IconOptions{Role: IconCard}).Clicked()
		}
	})
	return added
}

// nativePaneTab is one of a nativePaneStrip's Tabs: the chosen one shaded,
// as a sidebar row is.
func nativePaneTab(c *ui.Context, spec TabSpec) TabResult {
	t := c.Theme()
	el := ui.Row(c).Height(M(c).Bar.Tabs-8).MinWidth(100).MaxWidth(180).Padding(0, 4, 0, 10).Gap(6).
		Radius(M(c).Radius.Control).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(spec.Title)
	if spec.Key != "" {
		el.Key(spec.Key)
	}
	text, glyph := t.TextMuted, t.TextMuted
	switch {
	case spec.Active:
		el.Background(t.Text.Alpha(0.08))
		text, glyph = t.Text, t.Accent
	case el.Hovered():
		el.Background(t.Text.Alpha(0.04))
		text = t.Text
	}
	result := TabResult{Element: el}
	el.Children(func() {
		tabLead(c, spec, M(c).Icon.Small, glyph)
		ui.Text(c, spec.Title).FontSize(M(c).Font.Footnote).FontWeight(500).SingleLine().MinWidth(0).Grow(1).TextColor(text)
		if spec.Close != "" {
			closer := ui.ButtonBase(c).Label(spec.Close).Tooltip(spec.Close).
				Size(18, 18).Radius(M(c).Radius.Pill).Justify(ui.Center).AlignItems(ui.Center).Shrink(0).
				TextColor(t.TextMuted)
			if closer.Hovered() {
				closer.Background(t.Text.Alpha(0.1)).TextColor(t.Text)
			}
			closer.Children(func() { IconSized(c, icons.X, M(c).Icon.Small) })
			result.Closed = closer.Clicked()
		}
	})
	result.Clicked = el.Clicked()
	if spec.ContextMenu != nil {
		el.ContextMenu(spec.ContextMenu)
	}
	if spec.Active && spec.Reveal {
		el.ScrollIntoView()
	}
	return result
}

func tabLead(c *ui.Context, spec TabSpec, size float32, color ui.Color) {
	switch {
	case spec.Lead != nil:
		spec.Lead()
	case spec.Icon != "":
		IconSized(c, spec.Icon, size).TextColor(color).Shrink(0)
	}
}
