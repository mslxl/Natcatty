package quake

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// hostPickerWidth is the Host picker's panel width in DIPs. It is wide enough
// for a Host label beside its address but stays a flyout, not a sheet.
const hostPickerWidth float32 = 300

// hostPickerMaxHeight bounds the scrolling Host list so a large Vault does not
// grow the flyout past the Panel.
const hostPickerMaxHeight float32 = 320

// Bar draws the Quake Workspace's compact top bar: one entry per Tab, the add
// control that opens another local Shell, the Host picker, and switching and
// closing of the Tabs. The Dropdown Panel's frame owns the hide button beside
// it, outside the App Lock gate, so the Panel can still be hidden while the
// content is locked.
func Bar(s *State) func(*ui.Context) {
	// The bar owns its strip runtime for as long as this mounted bar lives. It
	// is separate from the body runtime, so a Tab switch changes only the
	// selected fill and the body while an open, close or reorder moves the
	// strip.
	return barWithStrip(s, &quakeStripMotion{})
}

// barWithStrip is Bar with the strip runtime supplied, so a test can read its
// run, generation and frame count. Bar owns one runtime per mounted bar.
func barWithStrip(s *State, strip *quakeStripMotion) func(*ui.Context) {
	return func(c *ui.Context) {
		// A Host connection may have finished since the last frame. Applying it
		// here keeps Tab mutation on the UI thread and lets this frame draw the
		// new Tab immediately.
		s.Flush()
		m := locales.In(c.Locale())
		setting := s.Motion()
		// The app marks the bar when the current route drew another bar (or no
		// frame at all). Nothing it might have shown was on screen, so this
		// frame warps the events it missed instead of sliding stale items.
		if s.takeBarUnrendered() {
			strip.skip()
		}
		strip.beginFrame(c, s)
		// Click actions are applied after the strip is built: closing a Tab
		// would shift the indexes of the entries still being drawn.
		selected, closed := -1, -1
		var pickerAnchor ui.Element
		added := kit.TabStrip(c, kit.TabPane, kit.TabStripOptions{
			Label:      m.AppName(),
			Add:        m.MenuNewTab(),
			Generation: strip.generation,
			Trailing: func() {
				pickerAnchor = kit.IconButtonWith(c, icons.Server, m.QuakeHostPicker(), kit.IconOptions{
					Role: kit.IconCard,
					Key:  "host-picker",
					On:   s.PickerOpen(),
				})
				if pickerAnchor.Clicked() {
					s.TogglePicker()
					c.Invalidate()
				}
			},
		}, func() {
			for _, tab := range s.Tabs() {
				title := tab.Label
				if title == "" {
					title = m.LocalSessionTab(tab.Number)
				}
				body, result := kit.Tab(c, kit.TabPane, kit.TabSpec{
					Key:    fmt.Sprintf("quake-tab-%d", tab.Number),
					Title:  title,
					Active: tab.Active,
					Close:  m.MenuCloseTab(),
				})
				// No Colors transition: the selected fill changes on this frame.
				// The strip's enter, exit and reorder are Position/Size only.
				body.Transition(strip.transition(setting))
				if result.Closed {
					closed = tab.Index
					continue
				}
				if result.Clicked {
					selected = tab.Index
				}
			}
		})
		switch {
		case closed >= 0:
			s.CloseTab(closed)
		case selected >= 0:
			s.SelectTab(selected)
		}
		if added {
			_ = s.AddLocalTab()
		}
		if pickerAnchor != (ui.Element{}) && s.PickerOpen() {
			hostPicker(c, s, pickerAnchor, m)
		}
	}
}

// hostPicker draws the Host chooser under the bar's Host button. It lists the
// local Shell entry first and the Vault Hosts after it, filtered by the
// search field that takes the keyboard as the picker opens. A selection is
// applied after the flyout is built, so opening a Tab never mutates the list
// it is drawn from.
func hostPicker(c *ui.Context, s *State, anchor ui.Element, m locales.Messages) {
	wasOpen := s.PickerOpen()
	hosts := filterHosts(s.Hosts(), s.Query())
	choseLocal := false
	chosenHost := -1
	kit.Popover(c, kit.PopoverPanel, anchor, &s.pickerOpen, func() {
		ui.Column(c).Width(hostPickerWidth).Gap(2).Padding(4).Children(func() {
			// The search field takes the keyboard when the picker opens, so
			// Escape reaches the flyout before the terminal consumes it as
			// input. It is also the picker's focusable element for MyGo's
			// popover focus order.
			if kit.SearchField(c, s.QueryPtr(), kit.SearchOptions{
				Role:        kit.SearchPicker,
				Placeholder: m.QuakeHostPicker(),
				AutoFocus:   true,
			}).Changed() {
				c.Invalidate()
			}
			ui.Text(c, m.QuakeHostsLabel()).FontSize(kit.M(c).Font.Caption2).
				FontWeight(600).TextColor(c.Theme().TextMuted).Padding(8, 8, 2, 8)
			if pickerRow(c, m.QuakeNewLocalShell(), "", "", true) {
				choseLocal = true
			}
			if len(hosts) == 0 {
				text := m.QuakeNoHosts()
				if strings.TrimSpace(s.Query()) != "" {
					text = m.QuakeNoMatchingHosts()
				}
				ui.Text(c, text).FontSize(kit.M(c).Font.Footnote).TextColor(c.Theme().TextMuted).
					Padding(20, 8).FillWidth().TextAlign(ui.Center)
				return
			}
			ui.Scroll(c).FillWidth().MaxHeight(hostPickerMaxHeight).Children(func() {
				ui.Column(c).FillWidth().Gap(1).Children(func() {
					for index, host := range hosts {
						if pickerRow(c, hostLabel(host), hostAddress(host), string(host.ID), false) {
							chosenHost = index
						}
					}
				})
			})
		})
	})
	switch {
	case choseLocal:
		_ = s.AddLocalTab()
	case chosenHost >= 0:
		_ = s.AddHostTab(hosts[chosenHost])
	}
	if wasOpen && !s.PickerOpen() {
		s.requestRedraw()
	}
}

// pickerRow draws one clickable Host picker entry. A local entry has no
// address and carries a terminal glyph; a saved Host shows its avatar, label,
// and address.
func pickerRow(c *ui.Context, title, detail, seed string, local bool) bool {
	t := c.Theme()
	row := ui.Row(c).FillWidth().Height(42).Padding(0, 8).Gap(10).AlignItems(ui.Center).
		Radius(kit.M(c).Radius.Control).Cursor(ui.CursorPointer).Label(title)
	if row.Hovered() {
		row.Background(kit.Subtle(t))
	}
	row.Children(func() {
		if local {
			ui.Column(c).Size(28, 28).Radius(kit.M(c).Radius.Control).Background(t.Accent.Alpha(0.12)).Shrink(0).
				Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.Lucide(icons.SquareTerminal)).Size(15, 15).TextColor(t.Accent)
			})
		} else {
			kit.HostAvatar(c, "", kit.AvatarServer, seed, 28)
		}
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, title).FontSize(kit.M(c).Font.Callout).FontWeight(500).SingleLine().
				MinWidth(0).TextColor(t.Text)
			if detail != "" {
				ui.Text(c, detail).FontSize(kit.M(c).Font.Caption).SingleLine().
					MinWidth(0).TextColor(t.TextMuted)
			}
		})
	})
	return row.Clicked()
}

// hostLabel is the picker's and Tab strip's name for a Host: its label, or its
// address when the label is empty.
func hostLabel(host domainvault.Host) string {
	if label := strings.TrimSpace(host.Label); label != "" {
		return label
	}
	return strings.TrimSpace(host.Hostname)
}

// hostAddress is the picker's secondary line: the endpoint a Host connects to.
func hostAddress(host domainvault.Host) string {
	if strings.TrimSpace(host.Hostname) == "" {
		return ""
	}
	port := host.Port
	if host.Username != "" {
		return fmt.Sprintf("%s@%s:%d", host.Username, host.Hostname, port)
	}
	return fmt.Sprintf("%s:%d", host.Hostname, port)
}

// filterHosts narrows the picker's list to the Hosts matching query by label,
// address, or username. An empty query keeps the Vault order.
func filterHosts(hosts []domainvault.Host, query string) []domainvault.Host {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return hosts
	}
	matched := make([]domainvault.Host, 0, len(hosts))
	for _, host := range hosts {
		text := strings.ToLower(strings.Join([]string{host.Label, host.Hostname, host.Username}, "\n"))
		if strings.Contains(text, query) {
			matched = append(matched, host)
		}
	}
	return matched
}

// View draws the active Quake Tab's terminal in the Panel's remaining body.
// The active Surface is rendered every frame through FocusedSurface so the
// keyboard follows a Tab switch after MyGo pruned the previous subtree. A
// failed first show explains itself instead of leaving an empty Panel.
func View(s *State) func(*ui.Context) {
	// The body runtime belongs to this mounted view. It consumes the same
	// event sequence as the bar independently: a strip-only event never moves
	// the body, and a switch changes only the selection and the body.
	return viewWithBody(s, &quakeBodyMotion{})
}

// viewWithBody is View with the body runtime supplied, so a test can read its
// phase, durations and frame count. View owns one runtime per mounted view.
func viewWithBody(s *State, body *quakeBodyMotion) func(*ui.Context) {
	return func(c *ui.Context) {
		// Apply completed Host opens before drawing the active Tab. The bar also
		// flushes; the second call in one frame is a no-op.
		s.Flush()
		// The app marks the body when another route (or a hidden Panel) drew
		// none of it. This first frame back warps the gap's events; a later
		// visible event still animates normally.
		if s.takeViewUnrendered() {
			body.skip()
		}
		body.beginFrame(c, s)
		if _, ok := s.Active(); !ok && body.phase == quakeBodyIdle {
			// No Tab and no closing body to draw: keep the Panel's empty state
			// instead of an empty body container.
			if s.Problem() != nil {
				ui.Column(c).Fill().Justify(ui.Center).Children(func() {
					kit.EmptyState(c, icons.TriangleAlert, locales.In(c.Locale()).PaneOpenFailed(), "", nil)
				})
			}
			return
		}
		body.view(c, s)
	}
}
