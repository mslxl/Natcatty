package logs

import (
	"time"

	"github.com/egoist/mygo/ui"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/ui/locales"
)

// View renders Logs and History. The scroll offset is view state: the two
// tabs share one surface, so returning to a tab starts where the user left it
// rather than at a stale editor offset.
func View(s *State) func(*ui.Context) {
	var scroll ui.ScrollState
	return func(c *ui.Context) {
		s.Refresh()
		m := locales.In(c.Locale())
		t := c.Theme()

		ui.Column(c).Grow(1).FillHeight().MinWidth(0).Gap(8).Children(func() {
			ui.Text(c, m.LogsTitle()).FontSize(20).Bold()
			tabs(c, s, m, t)
			problems(c, s, m, t)
			filters(c, s, m, t)

			ui.Scroll(c).Grow(1).FillWidth().MinWidth(0).TrackScroll(&scroll).Children(func() {
				if s.Tab() == TabConnections {
					connectionRows(c, s, m, t)
				} else {
					historyRows(c, s, m, t)
				}
			})
		})
	}
}

func tabs(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		connections := m.LogsTabConnections(s.TotalLogs())
		if s.Tab() == TabConnections {
			if ui.PrimaryButton(c, connections).Clicked() {
				s.SelectTab(TabConnections)
			}
		} else if ui.Button(c, connections).Clicked() {
			s.SelectTab(TabConnections)
			c.Invalidate()
		}
		history := m.LogsTabHistory(s.TotalEntries())
		if s.Tab() == TabHistory {
			if ui.PrimaryButton(c, history).Clicked() {
				s.SelectTab(TabHistory)
			}
		} else if ui.Button(c, history).Clicked() {
			s.SelectTab(TabHistory)
			c.Invalidate()
		}
	})
	if s.Tab() == TabHistory {
		if ui.Button(c, m.LogsClearHistory()).Clicked() {
			s.RequestClear()
			c.Invalidate()
		}
	}
	if s.PendingClear() {
		ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
			ui.Text(c, m.LogsClearConfirm()).Grow(1).TextColor(t.Danger)
			if ui.Button(c, m.LogsCancel()).Clicked() {
				s.CancelClear()
				c.Invalidate()
			}
			if ui.Button(c, m.LogsClearYes()).Clicked() {
				s.ConfirmClear()
				c.Invalidate()
			}
		})
	}
}

func problems(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	if text := problemText(m, s.Problem()); text != "" {
		ui.Text(c, text).TextColor(t.Danger).FontSize(12)
	}
	if text := problemText(m, s.OpenProblem()); text != "" {
		ui.Text(c, text).TextColor(t.Danger).FontSize(12)
	}
	if s.DateProblem() {
		ui.Text(c, m.LogsProblemDate()).TextColor(t.Danger).FontSize(12)
	}
	if s.Cleared() > 0 {
		ui.Text(c, m.LogsHistoryCleared(s.Cleared())).FontSize(12).TextColor(t.TextMuted)
	}
}

func problemText(m locales.Messages, problem Problem) string {
	switch problem {
	case ProblemLoad:
		return m.LogsProblemLoad()
	case ProblemDelete:
		return m.LogsProblemDelete()
	case ProblemClear:
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

func filters(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	ui.Field(c, m.LogsSearch(), func() {
		if ui.TextInput(c, s.Search()).FillWidth().Changed() {
			s.SetSearch(*s.Search())
			c.Invalidate()
		}
	})

	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
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
		ui.Field(c, m.LogsHostFilter(), func() {
			if ui.Select(c, &hostLabel, hostLabels).Width(180).Changed() {
				for index, label := range hostLabels {
					if label == hostLabel {
						s.SetHostKey(hostOptions[index].Key)
						break
					}
				}
				c.Invalidate()
			}
		}).Width(190)

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
			ui.Field(c, m.LogsStatusFilter(), func() {
				if ui.Select(c, &statusLabel, statusLabels).Width(150).Changed() {
					for index, label := range statusLabels {
						if label == statusLabel {
							s.SetStatus(statusValues[index])
							break
						}
					}
					c.Invalidate()
				}
			}).Width(160)
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
			ui.Field(c, m.LogsSourceFilter(), func() {
				if ui.Select(c, &sourceLabel, sourceLabels).Width(150).Changed() {
					for index, label := range sourceLabels {
						if label == sourceLabel {
							s.SetSourceFilter(sourceValues[index])
							break
						}
					}
					c.Invalidate()
				}
			}).Width(160)
		}
	})

	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
		from := s.From()
		ui.Field(c, m.LogsFrom(), func() {
			if ui.TextInput(c, from).Width(130).Changed() {
				s.SetDateRange(*from, *s.To())
				c.Invalidate()
			}
		}).Width(150)
		to := s.To()
		ui.Field(c, m.LogsTo(), func() {
			if ui.TextInput(c, to).Width(130).Changed() {
				s.SetDateRange(*s.From(), *to)
				c.Invalidate()
			}
		}).Width(150)
	})
}

func hostOptionLabel(m locales.Messages, option HostOption) string {
	switch option.Key {
	case "":
		return m.LogsHostAll()
	case domainhistory.LocalHostKey:
		return m.LogsHostLocal()
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
			ui.Row(c).FillWidth().Gap(8).Padding(6, 8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, connectionTitle(m, log)).Bold()
					ui.Text(c, m.LogsConnectionMeta(statusLabel(m, log.Status), log.Kind)).
						FontSize(12).TextColor(t.TextMuted)
					if address := connectionAddress(log); address != "" {
						ui.Text(c, address).FontSize(12).TextColor(t.TextMuted)
					}
					if log.Error != "" {
						ui.Text(c, log.Error).FontSize(12).TextColor(t.Danger)
					}
					ui.Text(c, timestamp(log.StartedAt)).FontSize(11).TextColor(t.TextMuted)
				})
				if ui.Button(c, m.LogsOpenTerminal()).Clicked() {
					s.OpenTerminal(log.SessionID, log.HostID)
					c.Invalidate()
				}
				if ui.Button(c, m.LogsDelete()).Clicked() {
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
			ui.Row(c).FillWidth().Gap(8).Padding(6, 8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, entry.Command).Bold()
					ui.Text(c, m.LogsHistoryMeta(historyHostLabel(m, entry),
						sourceLabel(m, entry.Source), timestamp(entry.RecordedAt))).
						FontSize(12).TextColor(t.TextMuted)
				})
				if ui.Button(c, m.LogsOpenTerminal()).Clicked() {
					s.OpenTerminal(entry.SessionID, entry.HostID)
					c.Invalidate()
				}
				if ui.Button(c, m.LogsDelete()).Clicked() {
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
		if ui.Button(c, m.LogsShowMore()).Clicked() {
			s.ShowMore()
			c.Invalidate()
		}
	}
}

func empty(c *ui.Context, total int, emptyText string, m locales.Messages, t *ui.Theme) {
	if total == 0 {
		ui.Text(c, emptyText).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, m.LogsNoMatches()).TextColor(t.TextMuted)
}

func connectionTitle(m locales.Messages, log domainhistory.ConnectionLog) string {
	// A local attempt has no Host, and the stored Label is the app's English
	// default; the localized local-terminal name is the useful title.
	if log.HostID == "" {
		return m.LogsLocalTerminal()
	}
	if log.HostLabel != "" {
		return log.HostLabel
	}
	if log.Hostname != "" {
		return log.Hostname
	}
	return m.LogsLocalTerminal()
}

func connectionAddress(log domainhistory.ConnectionLog) string {
	if log.Username != "" && log.Hostname != "" {
		return log.Username + "@" + log.Hostname
	}
	return log.Hostname
}

func historyHostLabel(m locales.Messages, entry domainhistory.Entry) string {
	if entry.HostID == "" {
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
