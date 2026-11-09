package logs

import (
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// View renders Logs and History. The whole surface scrolls as one region, so
// at any window height the records stay reachable even after the title, tabs,
// and filters take their space. The offset lives in State, both to survive
// frames and so tests can drive real input and assert that it moved.
func View(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		s.Refresh()
		m := locales.In(c.Locale())
		t := c.Theme()

		ui.Scroll(c).Grow(1).FillHeight().MinWidth(0).TrackScroll(s.Scroll()).Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).Gap(8).Children(func() {
				kit.PageTitle(c, m.LogsTitle(), "")
				tabs(c, s, m, t)
				problems(c, s, m, t)
				filters(c, s, m, t)

				if s.Tab() == TabConnections {
					connectionRows(c, s, m, t)
				} else {
					historyRows(c, s, m, t)
				}
			})
		})
	}
}

// tabs renders the record-family selector and the selected family's clear-all
// action. At 720 DIPs the two tab labels and the clear button fit; no wrapping
// is attempted, so a longer translation would clip this strip. That clipping
// is a known, accepted limitation rather than a redesign target here: the
// records below stay reachable because the whole surface scrolls.
func tabs(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	clearLabel, clearConfirm := m.LogsClearConnections(), m.LogsClearConnectionsConfirm()
	if s.Tab() == TabHistory {
		clearLabel, clearConfirm = m.LogsClearHistory(), m.LogsClearConfirm()
	}
	// The record families switch with the same underline tabs as the System
	// manager's, and the clear-all action trails them as a toolbar action.
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		labels := []string{m.LogsTabConnections(s.TotalLogs()), m.LogsTabHistory(s.TotalEntries())}
		index := 0
		if s.Tab() == TabHistory {
			index = 1
		}
		chosen := index
		ui.Row(c).Grow(1).MinWidth(0).Children(func() { kit.UnderlineTabs(c, &chosen, labels, false) })
		if chosen != index {
			if chosen == 1 {
				s.SelectTab(TabHistory)
			} else {
				s.SelectTab(TabConnections)
			}
			c.Invalidate()
		}
		total := s.TotalLogs()
		if s.Tab() == TabHistory {
			total = s.TotalEntries()
		}
		// There is nothing to clear in an empty family.
		if total > 0 && kit.GhostButton(c, icons.Trash, clearLabel).Clicked() {
			s.RequestClear()
			c.Invalidate()
		}
	})
	if s.PendingClear() {
		kit.Notice(c, icons.Trash, t.Danger, clearConfirm, func() {
			if kit.GhostButton(c, "", m.LogsCancel()).Clicked() {
				s.CancelClear()
				c.Invalidate()
			}
			if kit.DangerButton(c, icons.Trash, m.LogsClearYes()).Clicked() {
				s.ConfirmClear()
				c.Invalidate()
			}
		})
	}
}

func problems(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	if text := problemText(m, s.ProblemTab(), s.Problem()); text != "" {
		ui.Text(c, text).TextColor(uitheme.DangerText(t)).FontSize(12)
	}
	if text := problemText(m, s.Tab(), s.OpenProblem()); text != "" {
		ui.Text(c, text).TextColor(uitheme.DangerText(t)).FontSize(12)
	}
	if s.DateProblem() {
		ui.Text(c, m.LogsProblemDate()).TextColor(uitheme.DangerText(t)).FontSize(12)
	}
	if count := s.Cleared(); count > 0 {
		if s.ClearedTab() == TabConnections {
			ui.Text(c, m.LogsConnectionsCleared(count)).FontSize(12).TextColor(t.TextMuted)
		} else {
			ui.Text(c, m.LogsHistoryCleared(count)).FontSize(12).TextColor(t.TextMuted)
		}
	}
}

func problemText(m locales.Messages, tab Tab, problem Problem) string {
	switch problem {
	case ProblemLoad:
		return m.LogsProblemLoad()
	case ProblemDelete:
		return m.LogsProblemDelete()
	case ProblemClear:
		if tab == TabConnections {
			return m.LogsProblemClearConnections()
		}
		return m.LogsProblemClear()
	case ProblemOpen:
		return m.LogsProblemOpen()
	case ProblemHostMissing:
		return m.LogsProblemHostMissing()
	case ProblemDate:
		return m.LogsProblemDate()
	default:
		return ""
	}
}

// filters is the record list's toolbar: the search, the Host and status or
// source pickers, and the date range on one row that wraps when narrow. Each
// control carries its field name as its label and placeholder.
func filters(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		if ui.SearchField(c, s.Search()).Label(m.LogsSearch()).Placeholder(m.LogsSearch()).
			Grow(1).MinWidth(200).Changed() {
			s.SetSearch(*s.Search())
			c.Invalidate()
		}

		hostOptions := s.HostOptions()
		hostLabels := make([]string, len(hostOptions))
		for index, option := range hostOptions {
			hostLabels[index] = hostOptionLabel(m, option)
		}
		hostLabel := hostLabels[0]
		for index, option := range hostOptions {
			if option.Key == s.HostKey() {
				hostLabel = hostLabels[index]
				break
			}
		}
		if ui.Select(c, &hostLabel, hostLabels).Label(m.LogsHostFilter()).Tooltip(m.LogsHostFilter()).
			Width(170).Changed() {
			for index, label := range hostLabels {
				if label == hostLabel {
					s.SetHostKey(hostOptions[index].Key)
					break
				}
			}
			c.Invalidate()
		}

		if s.Tab() == TabConnections {
			statusLabels := []string{m.LogsStatusAll(), m.LogsStatusConnected(), m.LogsStatusFailed()}
			statusValues := []domainhistory.ConnectionStatus{
				"", domainhistory.ConnectionConnected, domainhistory.ConnectionFailed,
			}
			statusLabel := statusLabels[0]
			for index, value := range statusValues {
				if value == s.Status() {
					statusLabel = statusLabels[index]
					break
				}
			}
			if ui.Select(c, &statusLabel, statusLabels).Label(m.LogsStatusFilter()).
				Tooltip(m.LogsStatusFilter()).Width(140).Changed() {
				for index, label := range statusLabels {
					if label == statusLabel {
						s.SetStatus(statusValues[index])
						break
					}
				}
				c.Invalidate()
			}
		} else {
			sourceLabels := []string{m.LogsSourceAll(), m.LogsSourceSession(), m.LogsSourceShell()}
			sourceValues := []string{
				"", domainhistory.SourceSession, domainhistory.SourceShellHistory,
			}
			sourceLabel := sourceLabels[0]
			for index, value := range sourceValues {
				if value == s.SourceFilter() {
					sourceLabel = sourceLabels[index]
					break
				}
			}
			if ui.Select(c, &sourceLabel, sourceLabels).Label(m.LogsSourceFilter()).
				Tooltip(m.LogsSourceFilter()).Width(140).Changed() {
				for index, label := range sourceLabels {
					if label == sourceLabel {
						s.SetSourceFilter(sourceValues[index])
						break
					}
				}
				c.Invalidate()
			}
		}

		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			c.Key("logs-from")
			dateField(c, m, m.LogsFrom(), *s.From(), &s.pickers[0], func(day string) { s.SetDateRange(day, *s.To()) })
			ui.Text(c, "–").TextColor(t.TextMuted)
			c.Key("logs-to")
			dateField(c, m, m.LogsTo(), *s.To(), &s.pickers[1], func(day string) { s.SetDateRange(*s.From(), day) })
		})
	})
}

// datePicker is the calendar of an unset date filter end: whether it is open
// and the month it shows, which starts at today's.
type datePicker struct {
	open  bool
	draft time.Time
}

// dateField is one end of the date range. A set end is the calendar's
// DateInput, in the user's regional format, with a button that clears it. An
// open end is a button naming it that opens a calendar; DateInput has no
// empty value, so the end is set only once a day is chosen.
func dateField(c *ui.Context, m locales.Messages, label, text string, picker *datePicker,
	set func(day string)) {
	day, ok := parseDay(text)
	if !ok || day.IsZero() {
		anchor := kit.SecondaryButton(c, icons.Calendar, label)
		if anchor.Clicked() {
			picker.open = !picker.open
			picker.draft = time.Now()
			c.Invalidate()
		}
		ui.Popover(c, anchor, &picker.open, func() {
			shown := picker.draft
			if ui.Calendar(c, &picker.draft).Label(label).AutoFocus().Changed() {
				// Calendar reports the months its buttons and Page keys move
				// through too: only a day in the month shown chooses it.
				if picker.draft.Year() == shown.Year() && picker.draft.Month() == shown.Month() {
					set(picker.draft.Format(dayLayout))
					picker.open = false
				}
				c.Invalidate()
			}
		})
		return
	}
	ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
		if ui.DateInput(c, &day).Label(label).Tooltip(label).Changed() {
			set(day.Format(dayLayout))
			c.Invalidate()
		}
		if kit.IconButton(c, icons.X, m.LogsDateClear()).Clicked() {
			set("")
			c.Invalidate()
		}
	})
}

func hostOptionLabel(m locales.Messages, option HostOption) string {
	switch option.Key {
	case "":
		return m.LogsHostAll()
	case domainhistory.LocalHostKey:
		return m.LogsHostLocal()
	case domainhistory.UnsavedHostKey:
		return m.LogsHostUnsaved()
	default:
		if option.Label != "" {
			return option.Label
		}
		return option.Key
	}
}

func connectionRows(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	rows := s.VisibleLogs()
	if len(rows) == 0 {
		empty(c, s.TotalLogs(), m.LogsEmptyConnections(), m, t)
	} else {
		for _, log := range rows {
			log := log
			kit.ListRow(c).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, connectionTitle(m, log)).Bold()
					if log.IsProvider() {
						// A provider row is a tunnel lifecycle event, not a
						// Session: report its phase instead of connected/failed.
						meta := m.LogsProviderMeta(
							uiconnection.ProviderPhaseText(m,
								appconnection.ProviderPhase(log.NetworkProviderPhase)),
							log.NetworkProviderKind)
						ui.Text(c, meta).FontSize(12).TextColor(t.TextMuted)
						// A WireGuard row also keeps the handshake time and the
						// counters recorded for that event, so the Logs surface
						// shows the same tunnel detail as the Profile row.
						if log.NetworkProviderKind == string(domainvault.NetworkKindWireGuard) {
							ui.Text(c, uiconnection.WireGuardDetail(m,
								!log.NetworkLastHandshake.IsZero(), log.NetworkLastHandshake,
								log.NetworkRxBytes, log.NetworkTxBytes)).
								FontSize(12).TextColor(t.TextMuted)
						}
					} else {
						status := statusLabel(m, log.Status)
						meta := m.LogsConnectionMeta(status, log.Kind)
						if log.IsUnsaved() {
							meta = m.LogsConnectionMetaUnsaved(status, log.Kind)
						}
						ui.Text(c, meta).FontSize(12).TextColor(t.TextMuted)
					}
					if address := connectionAddress(log); address != "" {
						ui.Text(c, address).FontSize(12).TextColor(t.TextMuted)
					}
					if log.Error != "" {
						ui.Text(c, log.Error).FontSize(12).TextColor(uitheme.DangerText(t))
					}
					ui.Text(c, timestamp(log.StartedAt)).FontSize(11).TextColor(t.TextMuted)
				})
				// A provider row has no terminal Session to reopen. Delete stays
				// available for every row.
				if !log.IsProvider() {
					if kit.SecondaryButton(c, icons.SquareTerminal, m.LogsOpenTerminal()).Clicked() {
						s.OpenTerminal(log.SessionID, log.HostID)
						c.Invalidate()
					}
				}
				if kit.IconButton(c, icons.Trash, m.LogsDelete()).Clicked() {
					s.DeleteLog(log.ID)
					c.Invalidate()
				}
			})
		}
	}
	showMore(c, s, m)
}

func historyRows(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	rows := s.VisibleEntries()
	if len(rows) == 0 {
		empty(c, s.TotalEntries(), m.LogsEmptyHistory(), m, t)
	} else {
		for _, entry := range rows {
			entry := entry
			kit.ListRow(c).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, entry.Command).Bold()
					ui.Text(c, m.LogsHistoryMeta(historyHostLabel(m, entry),
						sourceLabel(m, entry.Source), timestamp(entry.RecordedAt))).
						FontSize(12).TextColor(t.TextMuted)
				})
				if kit.SecondaryButton(c, icons.SquareTerminal, m.LogsOpenTerminal()).Clicked() {
					s.OpenTerminal(entry.SessionID, entry.HostID)
					c.Invalidate()
				}
				if kit.IconButton(c, icons.Trash, m.LogsDelete()).Clicked() {
					s.DeleteEntry(entry.ID)
					c.Invalidate()
				}
			})
		}
	}
	showMore(c, s, m)
}

func showMore(c *ui.Context, s *State, m locales.Messages) {
	if s.HasMore() {
		if kit.SecondaryButton(c, "", m.LogsShowMore()).Clicked() {
			s.ShowMore()
			c.Invalidate()
		}
	}
}

func empty(c *ui.Context, total int, emptyText string, m locales.Messages, t *ui.Theme) {
	if total == 0 {
		kit.EmptyState(c, icons.Activity, emptyText, "", nil)
		return
	}
	kit.EmptyState(c, icons.Search, m.LogsNoMatches(), "", nil)
}

func connectionTitle(m locales.Messages, log domainhistory.ConnectionLog) string {
	// A Network Provider row names the Profile whose tunnel changed state.
	if log.IsProvider() {
		if log.NetworkProfileLabel != "" {
			return log.NetworkProfileLabel
		}
		if log.HostLabel != "" {
			return log.HostLabel
		}
		return log.NetworkProfileID
	}
	// A local attempt has no Host and stores no label; the localized
	// local-terminal name is the useful title. An unsaved Host also has no
	// Host identity, but it is not local: its recorded typed label (or
	// address) names the target.
	if log.IsLocal() {
		return m.LogsLocalTerminal()
	}
	if log.HostLabel != "" {
		return log.HostLabel
	}
	if log.Hostname != "" {
		return log.Hostname
	}
	if log.HostID != "" {
		return string(log.HostID)
	}
	return m.LogsHostUnsaved()
}

func connectionAddress(log domainhistory.ConnectionLog) string {
	if log.IsProvider() {
		return log.NetworkAddress
	}
	if log.Username != "" && log.Hostname != "" {
		return log.Username + "@" + log.Hostname
	}
	return log.Hostname
}

func historyHostLabel(m locales.Messages, entry domainhistory.Entry) string {
	if entry.IsUnsaved() {
		if entry.HostLabel != "" {
			return entry.HostLabel
		}
		return m.LogsHostUnsaved()
	}
	if entry.IsLocal() {
		return m.LogsHostLocal()
	}
	if entry.HostLabel != "" {
		return entry.HostLabel
	}
	return string(entry.HostID)
}

func statusLabel(m locales.Messages, status domainhistory.ConnectionStatus) string {
	if status == domainhistory.ConnectionFailed {
		return m.LogsStatusFailed()
	}
	return m.LogsStatusConnected()
}

func sourceLabel(m locales.Messages, source string) string {
	if source == domainhistory.SourceShellHistory {
		return m.LogsSourceShell()
	}
	return m.LogsSourceSession()
}

// timestamp formats a record time as data, not as a translatable sentence:
// the digits and separators are locale-neutral and the caller's own clock is
// what makes them meaningful.
func timestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Local().Format("2006-01-02 15:04:05")
}
