package sysmanager

import (
	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// sidePadding is the inset between the side panel's edge and the System
// Tool's content. The tab strip measures against the width inside it.
const sidePadding float32 = 8

// sideAgeMinWidth is the narrowest panel that shows the Stats Sample age in
// the status row. Below it -- notably at the 280-DIP minimum -- the status,
// age, interval, and Refresh cannot share the single row without truncating
// one of them, so the age is omitted there; the normal 360-DIP panel shows it.
const sideAgeMinWidth float32 = 320

// ShowHost points the side panel's System tool at a Session's Host and starts
// sampling it, as the original's System tool follows the Session it sits in.
// A Host added to the Vault since the last load is found by reloading; a
// Host the Vault lacks shows nothing rather than another Host.
func (s *State) ShowHost(id string) {
	if s.active && id == s.sideHost {
		return
	}
	if s.hostID != id {
		s.resetHistory()
	}
	s.sideHost = id
	if !s.active {
		s.hostID = id
		s.Activate()
	} else {
		if _, ok := s.hostByID[id]; !ok {
			s.reload()
		}
		s.SetHost(id)
	}
	if s.hostID != id {
		s.hostID = ""
		if s.service != nil {
			s.service.Stop()
		}
	}
}

// Hide stops sampling once the side panel no longer draws the System tool.
func (s *State) Hide() {
	s.sideHost = ""
	if s.active {
		s.Deactivate()
	}
}

// SideView is the System tool of a Session's side panel: the System manager
// for the Session's own Host, so it has no Host picker. width is the panel's
// width in DIPs; the status, interval, and Refresh sit above the tabs, which
// measure the panel's inner width so they can drop their labels together or
// scroll at the narrowest panel.
func SideView(s *State, width float32) func(*ui.Context) {
	s.compact = true
	innerWidth := max(0, width-2*sidePadding)
	return func(c *ui.Context) {
		s.Flush()
		m := locales.In(c.Locale())
		t := c.Theme()

		ui.Column(c).Fill().MinWidth(0).Padding(sidePadding).Gap(surfaceGap).Children(func() {
			if _, ok := s.CurrentHost(); !ok || s.HostID() == "" {
				emptyState(c, m.SidePanelSystemNoHost(), t)
				return
			}
			state := s.TabState(s.Tab())
			// One row however narrow the panel: the status, the interval, and
			// Refresh as an icon. The tab layout and the Collapsed Tabs menu
			// follow the tabs in the strip below.
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
				text, color := statusText(m, state, t)
				dot := color
				if state.Status == appsysmanager.StatusLive {
					dot = t.Success
				}
				ui.Box(c).Size(7, 7).Radius(4).Background(dot).Shrink(0)
				ui.Text(c, text).FontSize(kit.M(c).Font.Footnote).TextColor(color).SingleLine().Grow(1).MinWidth(0)
				// The sample age stays beside any status, cached samples
				// included, but only where the row has room for it.
				if width >= sideAgeMinWidth {
					if age := overviewSampleAge(s, state, m); age != "" {
						ui.Text(c, age).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).
							SingleLine().Shrink(1).MinWidth(0)
					}
				}
				sideRefreshControls(c, s, m)
			})
			if problem := problemText(m, s.Problem()); problem != "" {
				ui.Text(c, problem).FontSize(kit.M(c).Font.Footnote).TextColor(t.Danger)
			}
			if notice := actionNotice(s, m); notice != "" {
				ui.Text(c, notice).FontSize(kit.M(c).Font.Footnote).TextColor(actionNoticeColor(s, t))
			}
			tabs(c, s, m, innerWidth)
			tabContent(c, s, m, t, width)
		})
		confirmation(c, s, m)
	}
}

// sideRefreshControls are the refresh interval and Refresh now, compact for
// the side panel.
func sideRefreshControls(c *ui.Context, s *State, m locales.Messages) {
	choices := make([]string, 0, len(domainpreferences.SysManagerIntervalChoices))
	for _, seconds := range domainpreferences.SysManagerIntervalChoices {
		choices = append(choices, m.SysManagerSeconds(seconds))
	}
	current := m.SysManagerSeconds(s.Interval())
	chosen := current
	ui.Select(c, &chosen, choices).Width(76).MinWidth(76).Label(m.SysManagerRefresh())
	if chosen != current {
		for index, label := range choices {
			if label == chosen {
				s.SetInterval(domainpreferences.SysManagerIntervalChoices[index])
				break
			}
		}
		c.Invalidate()
	}
	c.Key("sysmanager-side-refresh")
	if kit.IconButton(c, icons.RefreshCw, m.SysManagerRefreshNow()).Clicked() {
		s.Refresh()
		c.Invalidate()
	}
}
