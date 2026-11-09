package logs

import (
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// sideLimit bounds the rows the side panel draws; the search reaches the
// rest.
const sideLimit = 300

// HistoryPanel is the command History tool of a Session's side panel, as the
// original's: the commands run on this Session's Host, or on every Host,
// newest first, each ready to run again or paste into the terminal. It reads
// the records the History surface lists and deletes through it.
type HistoryPanel struct {
	state *State
	// send types a command into the focused Session; run submits it.
	send func(command string, run bool) bool

	search string
	all    bool
	failed bool
}

// NewHistoryPanel returns the side panel's History over the History surface's
// state. send types into the focused Session.
func NewHistoryPanel(state *State, send func(command string, run bool) bool) *HistoryPanel {
	return &HistoryPanel{state: state, send: send}
}

// View renders the History for the Session's Host, or for this computer when
// host is empty.
func (p *HistoryPanel) View(host model.HostID) func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()
		p.state.Refresh()
		ui.Column(c).Fill().MinWidth(0).Children(func() {
			if kit.SidePanelHeader(c, &p.search, m.SidePanelSearchHistory(), nil) {
				c.Invalidate()
			}
			entries := p.entries(host)
			ui.Row(c).FillWidth().Gap(8).Padding(0, 8, 4, 8).AlignItems(ui.Center).Shrink(0).Children(func() {
				scope := 0
				if p.all {
					scope = 1
				}
				if kit.Segmented(c, kit.SegmentedText, &scope, "", []kit.Segment{
					{Label: m.SidePanelHistoryThisHost()}, {Label: m.SidePanelHistoryAllHosts()},
				}) {
					p.all = scope == 1
					c.Invalidate()
				}
				ui.Column(c).Grow(1)
				ui.Text(c, m.SidePanelHistoryCount(len(entries))).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
			})
			if p.state.Problem() == ProblemLoad {
				ui.Box(c).FillWidth().Padding(0, 8, 4, 8).Children(func() {
					kit.Notice(c, icons.CircleX, t.Danger, m.LogsProblemLoad(), nil)
				})
			}
			if p.failed {
				ui.Box(c).FillWidth().Padding(0, 8, 4, 8).Children(func() {
					kit.Notice(c, icons.TriangleAlert, t.Danger, m.SidePanelHistorySendFailed(), func() {
						if kit.IconButton(c, icons.X, m.SidePanelDismiss()).Clicked() {
							p.failed = false
						}
					})
				})
			}
			if len(entries) == 0 {
				text := m.SidePanelHistoryEmpty()
				if strings.TrimSpace(p.search) != "" {
					text = m.SidePanelNoMatches()
				}
				kit.EmptyState(c, icons.History, text, "", nil)
				return
			}
			if len(entries) > sideLimit {
				entries = entries[:sideLimit]
			}
			ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Padding(0, 8, 8, 8).Gap(2).Children(func() {
					for _, entry := range entries {
						p.row(c, m, t, entry)
					}
				})
			})
		})
	}
}

// entries are the scope's commands matching the search, newest first.
func (p *HistoryPanel) entries(host model.HostID) []domainhistory.Entry {
	query := strings.ToLower(strings.TrimSpace(p.search))
	var out []domainhistory.Entry
	for _, entry := range p.state.entries {
		if !p.all {
			if host == "" && !entry.IsLocal() || host != "" && entry.HostID != host {
				continue
			}
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.Command), query) {
			continue
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RecordedAt.After(out[j].RecordedAt) })
	return out
}

func (p *HistoryPanel) row(c *ui.Context, m locales.Messages, t *ui.Theme, entry domainhistory.Entry) {
	c.Key("side-history-" + entry.ID)
	// Run and Paste stay on every row, so they need no hover to be found;
	// a double click pastes, and Delete is in the row's context menu.
	row := ui.Row(c).FillWidth().Padding(4, 4, 4, 8).Gap(2).Radius(kit.M(c).Radius.Control).AlignItems(ui.Center)
	if row.Hovered() {
		row.Background(kit.Subtle(t))
	}
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, strings.TrimSpace(entry.Command)).FontSize(kit.M(c).Font.Footnote).SingleLine()
			meta := relativeTime(m, entry.RecordedAt)
			if p.all {
				meta = historyHostLabel(m, entry) + " · " + meta
			}
			ui.Text(c, meta).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
		})
		if kit.IconButton(c, icons.CornerDownLeft, m.SidePanelHistoryPaste()).Clicked() {
			p.failed = !p.send(entry.Command, false)
			c.Invalidate()
		}
		if kit.IconButton(c, icons.Play, m.SidePanelHistoryRun()).Clicked() {
			p.failed = !p.send(entry.Command, true)
			c.Invalidate()
		}
	})
	row.ContextMenu(func(menu *ui.Menu) {
		if menu.Item(m.SidePanelHistoryRun()).Chosen() {
			p.failed = !p.send(entry.Command, true)
		}
		if menu.Item(m.SidePanelHistoryPaste()).Chosen() {
			p.failed = !p.send(entry.Command, false)
		}
		menu.Separator()
		if menu.Item(m.LogsDelete()).Chosen() {
			p.state.DeleteEntry(entry.ID)
		}
	})
	if row.DoubleClicked() {
		p.failed = !p.send(entry.Command, false)
		c.Invalidate()
	}
}

// relativeTime is how long ago a command ran, in the coarsest unit that says
// it, or its date once it is a week old.
func relativeTime(m locales.Messages, at time.Time) string {
	age := time.Since(at)
	switch {
	case age < time.Minute:
		return m.SidePanelJustNow()
	case age < time.Hour:
		return m.SidePanelMinutesAgo(int(age / time.Minute))
	case age < 24*time.Hour:
		return m.SidePanelHoursAgo(int(age / time.Hour))
	case age < 7*24*time.Hour:
		return m.SidePanelDaysAgo(int(age / (24 * time.Hour)))
	}
	return at.Local().Format("2006-01-02")
}
