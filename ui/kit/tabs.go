package kit

import (
	"strconv"

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
	// AddKey keeps the add control the same element while the tabs before it
	// come and go. Without it its positional identity changes with the tab
	// count, so a layout warp that rests MyGo's records in place still loses
	// the keyboard focus the control held.
	AddKey string
	// FocusAdd gives the add control the keyboard focus as it is built, for
	// a strip whose row had to remount and is restoring focus a warp took
	// with it.
	FocusAdd bool
	// AddFocused, if set, receives whether the add control holds the keyboard
	// focus after this build, so the caller can restore it across a remount.
	AddFocused *bool
	// Trailing draws one control after the Tab strip and the add control, as
	// the Quake bar's Host picker. It draws a shrink-wrapped control; the
	// strip takes the remaining width.
	Trailing func()
	// Scroll, if set, keeps the strip's horizontal scrolling there. MaxX is
	// the last built frame's overflow, so a view reading it before a build
	// knows whether a reveal's scroll and a position transition would fight.
	Scroll *ui.ScrollState
	// Boxes, if set, receives the strip's previous-frame boxes before this
	// frame's build: the scrolling viewport and the row of tabs and add
	// control inside it. A view predicts from them whether the layout this
	// frame will overflow, which a reveal must not animate into.
	Boxes *TabStripBoxes
	// Generation renames the row that holds the items. A view that must
	// cancel its items' in-flight transitions and exit copies at once, such
	// as a batch Tab close, raises it: the row becomes a new parent, so MyGo
	// drops every record under the old one without window-wide
	// DisableAnimations. Zero keeps the row's positional identity.
	Generation int
}

// TabStripBoxes are a TabStrip's previous-frame boxes, in window DIPs.
type TabStripBoxes struct {
	// Viewport is the scrolling strip's visible box.
	Viewport ui.Rect
	// Content is the row of tabs and the add control inside it. Its width
	// is the strip's content width, whether or not it is scrolled.
	Content ui.Rect
}

// WorkTabMinWidth and WorkTabMaxWidth bound a Work Tab's laid-out width,
// whatever its title. A strip that predicts whether an opening Tab's reveal
// will overflow uses both: the minimum for a lower bound and the maximum for
// an upper one (ticket 20).
const (
	WorkTabMinWidth float32 = 140
	WorkTabMaxWidth float32 = 240
)

// WorkTabGap is the row's gap between Work Tabs.
func WorkTabGap(c *ui.Context) float32 { return workTabGap(c) }

// The Work Tab row's fixed room, which WorkTabWidthBound charges on top of the
// measured title and marker. workTab draws
//
//	Row.Padding(0, 4, 0, 12).Gap(8) { StatusDot, title, marker?, menu, close }
//
// so the bound adds the horizontal padding, the 8-DIP status-dot lead, one
// 8-DIP gap between every pair of the row's children (three without a marker,
// four with one), and the two reserved Control.Mini controls. The title's own
// MinWidth(72) is charged even for an empty or one-glyph title. The marker,
// when the row draws one, adds its gap and measured glyphs. workTabWidthSafety
// covers float rounding and a font metric a style may round differently; the
// measured values match the laid-out widths exactly in both styles today.
const (
	workTabWidthPadding    float32 = 12 + 4
	workTabWidthLead       float32 = 8
	workTabWidthGap        float32 = 8
	workTabTitleMinWidth   float32 = 72
	workTabReservedActions float32 = 2
	workTabWidthSafety     float32 = 4
)

// WorkTabWidthBound returns an upper bound of a Work Tab body's laid-out
// width for the title and marker in the active Interface Style. It measures
// the text the row draws instead of reading a previous frame's layout, so a
// strip can decide from the state it is about to draw whether an open will
// overflow: a rename, a dirty marker or a style change is part of the bound
// on the frame it happens, and a resize cannot leave it describing the old
// layout (ticket 20). The result is clamped to the Work Tab's own
// WorkTabMinWidth/WorkTabMaxWidth, so it is never below what workTab lays out
// and never above the widest a Tab can be.
//
// The bound describes the row the shell draws: a status-dot Lead, a Menu and a
// Close control, which workTab reserves the room of even while idle.
func WorkTabWidthBound(c *ui.Context, title, marker string) float32 {
	titleWidth, _ := c.MeasureText(0, ui.Span{Text: title, Size: M(c).Font.Footnote, Weight: 600})
	if titleWidth < workTabTitleMinWidth {
		titleWidth = workTabTitleMinWidth
	}
	bound := workTabWidthPadding + workTabWidthLead + workTabReservedActions*M(c).Control.Mini +
		3*workTabWidthGap + titleWidth + workTabWidthSafety
	if marker != "" {
		markerWidth, _ := c.MeasureText(0, ui.Span{Text: marker, Size: 9})
		bound += workTabWidthGap + markerWidth
	}
	if bound < WorkTabMinWidth {
		return WorkTabMinWidth
	}
	if bound > WorkTabMaxWidth {
		return WorkTabMaxWidth
	}
	return bound
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
			if o.Scroll != nil {
				strip.TrackScroll(o.Scroll)
			}
			if o.Label != "" {
				strip.Label(o.Label)
			}
			strip.Children(func() {
				row := ui.Row(c).Height(height)
				if o.Generation > 0 {
					row.Key(tabStripRowKey(o.Generation))
				}
				if o.Boxes != nil {
					o.Boxes.Viewport = strip.Bounds()
					o.Boxes.Content = row.Bounds()
				}
				row.Children(tabs)
			})
			if o.Add == "" {
				if o.Trailing != nil {
					o.Trailing()
				}
				return
			}
			if o.AddKey != "" {
				c = c.Key(o.AddKey)
			}
			add := ui.ButtonBase(c).Label(o.Add).Tooltip(o.Add).
				Width(height).FillHeight().Justify(ui.Center).AlignItems(ui.Center).Shrink(0).
				BorderWidth(0, 0, 0, 1).BorderColor(t.Border).TextColor(t.TextMuted)
			if add.Hovered() {
				add.Background(Subtle(t)).TextColor(t.Text)
			}
			add.Children(func() { IconSized(c, icons.Plus, M(c).Icon.Medium) })
			if o.FocusAdd {
				add.AutoFocus()
			}
			added = add.Clicked()
			if o.AddFocused != nil {
				*o.AddFocused = add.Focused()
			}
			if o.Trailing != nil {
				o.Trailing()
			}
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
	scroll := ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Height(height).Label(o.Label)
	if o.Scroll != nil {
		scroll.TrackScroll(o.Scroll)
	}
	if o.Boxes != nil {
		o.Boxes.Viewport = scroll.Bounds()
	}
	scroll.Children(func() {
		row := ui.Row(c).Height(height).AlignItems(align).Gap(workTabGap(c))
		if o.Generation > 0 {
			row.Key(tabStripRowKey(o.Generation))
		}
		if o.Boxes != nil {
			o.Boxes.Content = row.Bounds()
		}
		row.Children(func() {
			tabs()
			if o.Add != "" {
				add := IconButtonWith(c, icons.Plus, o.Add, IconOptions{Role: IconTab, Key: o.AddKey}).
					Margin(addMargin[0], addMargin[1], addMargin[2], addMargin[3])
				if o.FocusAdd {
					add.AutoFocus()
				}
				added = add.Clicked()
				if o.AddFocused != nil {
					*o.AddFocused = add.Focused()
				}
			}
		})
	})
	return added
}

// tabStripRowKey names the row of a strip whose view raised its generation,
// so a warp can make every item under it a fresh element.
func tabStripRowKey(generation int) string {
	return "tab-strip-row:" + strconv.Itoa(generation)
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

// TabControlFocus names a control of a Tab that can hold the keyboard focus.
// A caller that remounts its row after a warp (a new element key) loses the
// focus MyGo keeps by element ID; it names the control the user had focused
// so the rebuilt Tab gives that same control the focus back.
type TabControlFocus int

const (
	// TabFocusNone gives no control the focus.
	TabFocusNone TabControlFocus = iota
	// TabFocusMenu gives the menu control the focus.
	TabFocusMenu
	// TabFocusClose gives the close control the focus.
	TabFocusClose
)

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
	// Focus names a control that takes the keyboard focus as the tab is
	// built, used by a row that remounted its items to restore equivalent
	// focus. It only takes effect while the control is built, which is while
	// the tab is active or hovered.
	Focus TabControlFocus
	// Reveal scrolls the tab into view while it is active.
	Reveal bool
}

// TabResult is what a tab reported this frame.
type TabResult struct {
	Clicked bool
	Closed  bool
	// MenuFocused and CloseFocused report that this build's menu or close
	// control holds the keyboard focus, so a caller that remounts its row
	// can give the same control the focus back (TabSpec.Focus).
	MenuFocused  bool
	CloseFocused bool
}

// Tab draws one tab of a TabStrip of role. It returns the tab's element, for
// the caller's drag and drop within this build pass.
func Tab(c *ui.Context, role TabRole, spec TabSpec) (ui.Element, TabResult) {
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

func fixedWorkTab(c *ui.Context, spec TabSpec) (ui.Element, TabResult) {
	t := c.Theme()
	tab := ui.ButtonBase(c.Key(spec.Key)).Label(spec.Title).Height(M(c).Control.Regular).Padding(0, 12).Gap(8).
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
	return tab, TabResult{Clicked: tab.Clicked()}
}

func workTab(c *ui.Context, spec TabSpec) (ui.Element, TabResult) {
	t := c.Theme()
	body := ui.Row(c).Height(M(c).Control.Regular).MinWidth(WorkTabMinWidth).MaxWidth(WorkTabMaxWidth).Padding(0, 4, 0, 12).Gap(8).
		AlignItems(ui.Center).Tooltip(spec.Title)
	workTabShape(c, body)
	if spec.Key != "" {
		body.Key(spec.Key)
	}
	hovered := body.Hovered()
	workTabFill(c, body, spec.Active, hovered)
	result := TabResult{Clicked: body.Clicked()}
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
			menu := IconButtonWith(c, icons.Ellipsis, spec.MenuLabel, IconOptions{Role: IconTab})
			if spec.Focus == TabFocusMenu {
				menu.AutoFocus()
			}
			result.MenuFocused = menu.Focused()
			menu.Menu(spec.Menu)
		}
		if spec.Close != "" {
			closer := IconButtonWith(c, icons.X, spec.Close, IconOptions{Role: IconTab})
			if spec.Focus == TabFocusClose {
				closer.AutoFocus()
			}
			result.CloseFocused = closer.Focused()
			result.Closed = closer.Clicked()
		}
	})
	if spec.Active && spec.Reveal {
		body.ScrollIntoView()
	}
	return body, result
}

func paneTab(c *ui.Context, spec TabSpec) (ui.Element, TabResult) {
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
	result := TabResult{}
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
	return el, result
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
		if o.Scroll != nil {
			strip.TrackScroll(o.Scroll)
		}
		if o.Label != "" {
			strip.Label(o.Label)
		}
		strip.Children(func() {
			row := ui.Row(c).Height(height).Gap(M(c).Space.Hairline).AlignItems(ui.Center)
			if o.Generation > 0 {
				row.Key(tabStripRowKey(o.Generation))
			}
			if o.Boxes != nil {
				o.Boxes.Viewport = strip.Bounds()
				o.Boxes.Content = row.Bounds()
			}
			row.Children(tabs)
		})
		if o.Add != "" {
			add := IconButtonWith(c, icons.Plus, o.Add, IconOptions{Role: IconCard, Key: o.AddKey})
			if o.FocusAdd {
				add.AutoFocus()
			}
			added = add.Clicked()
			if o.AddFocused != nil {
				*o.AddFocused = add.Focused()
			}
		}
		if o.Trailing != nil {
			o.Trailing()
		}
	})
	return added
}

// nativePaneTab is one of a nativePaneStrip's Tabs: the chosen one shaded,
// as a sidebar row is.
func nativePaneTab(c *ui.Context, spec TabSpec) (ui.Element, TabResult) {
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
	result := TabResult{}
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
	return el, result
}

func tabLead(c *ui.Context, spec TabSpec, size float32, color ui.Color) {
	switch {
	case spec.Lead != nil:
		spec.Lead()
	case spec.Icon != "":
		IconSized(c, spec.Icon, size).TextColor(color).Shrink(0)
	}
}
