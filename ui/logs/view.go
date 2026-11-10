package logs

import (
	"fmt"
	"strconv"
	"strings"
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
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					kit.PageTitle(c, m.LogsTitle(), "")
					if s.AccountPanelAvailable() {
						if kit.SecondaryButton(c, icons.User, m.LogsAtuinAccount()).Clicked() {
							s.OpenAccountPanel()
							c.Invalidate()
						}
					}
				})
				tabs(c, s, m, t)
				problems(c, s, m, t)
				filters(c, s, m, t)
				if s.PendingPrune() {
					pruneDialog(c, s, m, t)
				}
				if s.PendingDeleteEverywhere() {
					// Delete everywhere is a separate, danger-styled action: the
					// confirmation names the limit that rows this device never
					// downloaded cannot be reached.
					confirmed, cancelled := kit.Confirm(c, true,
						m.LogsDeleteEverywhere(), m.LogsDeleteEverywhereConfirm(),
						m.LogsDeleteEverywhereYes(), m.LogsCancel())
					switch {
					case confirmed:
						s.ConfirmDeleteEverywhere()
						c.Invalidate()
					case cancelled:
						s.CancelDeleteEverywhere()
						c.Invalidate()
					}
				}

				if s.Tab() == TabConnections {
					connectionRows(c, s, m, t)
				} else {
					historyModeSelector(c, s, m)
					if s.HistoryMode() == HistoryModeStatistics {
						historyStatistics(c, s, m, t)
					} else {
						historyRows(c, s, m, t)
					}
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
	// manager's; the family's actions trail them as toolbar actions.
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
		// There is nothing to prune or clear in an empty family.
		if total > 0 {
			// Prune is a History-only action: Connection Logs keep their own
			// retention and are not part of Command History.
			if s.Tab() == TabHistory && kit.GhostButton(c, icons.Filter, m.LogsPrune()).Clicked() {
				s.RequestPrune()
				c.Invalidate()
			}
			if kit.GhostButton(c, icons.Trash, clearLabel).Clicked() {
				s.RequestClear()
				c.Invalidate()
			}
		}
	})
	if s.Tab() == TabHistory {
		// Delete everywhere is visibly separate from Clear this device and is
		// danger-styled: it also removes imported and synced rows and uploads a
		// tombstone for every row with a UUID identity.
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if kit.DangerButton(c, icons.Trash, m.LogsDeleteEverywhere()).Clicked() {
				s.RequestDeleteEverywhere()
				c.Invalidate()
			}
		})
	}
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
		ui.Text(c, text).TextColor(uitheme.DangerText(t)).FontSize(kit.M(c).Font.Footnote)
	}
	if text := problemText(m, s.Tab(), s.OpenProblem()); text != "" {
		ui.Text(c, text).TextColor(uitheme.DangerText(t)).FontSize(kit.M(c).Font.Footnote)
	}
	if s.DateProblem() {
		ui.Text(c, m.LogsProblemDate()).TextColor(uitheme.DangerText(t)).FontSize(kit.M(c).Font.Footnote)
	}
	if count := s.Cleared(); count > 0 {
		if s.ClearedTab() == TabConnections {
			ui.Text(c, m.LogsConnectionsCleared(count)).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
		} else {
			ui.Text(c, m.LogsHistoryCleared(count)).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
		}
	}
	if count := s.Pruned(); count > 0 {
		ui.Text(c, m.LogsPruned(count)).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	}
	if count := s.DeletedEverywhere(); count > 0 {
		ui.Text(c, m.LogsDeleteEverywhereDone(count)).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
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
	case ProblemPrune:
		return m.LogsProblemPrune()
	case ProblemInsert:
		return m.LogsProblemInsert()
	case ProblemInsertUnsafe:
		return m.LogsProblemInsertUnsafe()
	case ProblemDeleteEverywhere:
		return m.LogsProblemDeleteEverywhere()
	default:
		return ""
	}
}

// filters is the record list's toolbar: the search, the Host and status or
// source pickers, and the date range on one row that wraps when narrow. Each
// control carries its field name as its label and placeholder.
func filters(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		// A plain text input rather than a SearchField: the global-search
		// shortcut has to focus the input itself, and SearchField's wrapper
		// hides the input from FocusBind.
		if ui.TextInput(c, s.Search()).Label(m.LogsSearch()).Placeholder(m.LogsSearch()).
			FocusBind(&s.focusRequest, focusHistorySearch).Grow(1).MinWidth(200).Changed() {
			s.SetSearch(*s.Search())
			c.Invalidate()
		}

		hostChoices := []selectChoice{{Value: "", Label: m.LogsHostAll()}}
		for _, option := range s.HostOptions() {
			if option.Key == "" {
				continue
			}
			hostChoices = append(hostChoices, selectChoice{
				Value: option.Key, Label: hostOptionLabel(m, option),
			})
		}
		selectField(c, m.LogsHostFilter(), s.HostKey(), hostChoices, 170,
			func(value string) { s.SetHostKey(value) })

		if s.Tab() == TabConnections {
			statusChoices := []selectChoice{
				{Value: "", Label: m.LogsStatusAll()},
				{Value: string(domainhistory.ConnectionConnected), Label: m.LogsStatusConnected()},
				{Value: string(domainhistory.ConnectionFailed), Label: m.LogsStatusFailed()},
			}
			selectField(c, m.LogsStatusFilter(), string(s.Status()), statusChoices, 140,
				func(value string) { s.SetStatus(domainhistory.ConnectionStatus(value)) })
		} else {
			selectField(c, m.LogsSourceFilter(), s.SourceFilter(),
				facetChoices(m.LogsSourceAll(), s.SourceOptions(), func(value string) string {
					return sourceLabel(m, value)
				}), 140, func(value string) { s.SetSourceFilter(value) })
			selectField(c, m.LogsSessionFilter(), s.SessionFilter(),
				facetChoices(m.LogsSessionAll(), s.SessionOptions(), identityLabel), 200,
				func(value string) { s.SetSessionFilter(value) })
			selectField(c, m.LogsCwdFilter(), s.CwdFilter(),
				facetChoices(m.LogsCwdAll(), s.CwdOptions(), identityLabel), 220,
				func(value string) { s.SetCwdFilter(value) })
			selectField(c, m.LogsShellFilter(), s.ShellFilter(),
				facetChoices(m.LogsShellAll(), s.ShellOptions(), identityLabel), 160,
				func(value string) { s.SetShellFilter(value) })
			selectField(c, m.LogsExitFilter(), string(s.ExitFilter()),
				exitChoices(m, s.ExitOptions()), 160,
				func(value string) { s.SetExitFilter(domainhistory.ExitFilter(value)) })
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
		kit.Popover(c, kit.PopoverPicker, anchor, &picker.open, func() {
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

// pruneDialog is the filter-based prune preview. It edits its own draft
// filters seeded from the History surface, so a cancelled or malformed prune
// never changes what the list below is showing. Only the matching rows are
// removed, with the optional limit trimming the oldest matches first.
func pruneDialog(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	draft := s.PruneFilter()
	ui.Column(c).FillWidth().Padding(10).Gap(6).Background(t.Surface).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.LogsPruneTitle()).Bold().Grow(1)
			if ui.Button(c, m.LogsCancel()).Clicked() {
				s.CancelPrune()
				c.Invalidate()
			}
		})
		ui.Text(c, m.LogsPruneHint()).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
		ui.Field(c, m.LogsSearch(), func() {
			if ui.TextInput(c, &draft.Query).FillWidth().Changed() {
				c.Invalidate()
			}
		})

		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
			hostChoices := []selectChoice{{Value: "", Label: m.LogsHostAll()}}
			for _, option := range s.HostOptions() {
				if option.Key == "" {
					continue
				}
				hostChoices = append(hostChoices, selectChoice{
					Value: option.Key, Label: hostOptionLabel(m, option),
				})
			}
			selectField(c, m.LogsHostFilter(), draft.Host, hostChoices, 200,
				func(value string) { draft.Host = value })
			selectField(c, m.LogsSourceFilter(), draft.Source,
				facetChoices(m.LogsSourceAll(), s.SourceOptions(), func(value string) string {
					return sourceLabel(m, value)
				}), 190, func(value string) { draft.Source = value })
		})
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
			selectField(c, m.LogsSessionFilter(), draft.Session,
				facetChoices(m.LogsSessionAll(), s.SessionOptions(), identityLabel), 240,
				func(value string) { draft.Session = value })
			selectField(c, m.LogsCwdFilter(), draft.CWD,
				facetChoices(m.LogsCwdAll(), s.CwdOptions(), identityLabel), 250,
				func(value string) { draft.CWD = value })
		})
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
			selectField(c, m.LogsShellFilter(), draft.Shell,
				facetChoices(m.LogsShellAll(), s.ShellOptions(), identityLabel), 170,
				func(value string) { draft.Shell = value })
			selectField(c, m.LogsExitFilter(), string(draft.Exit),
				exitChoices(m, s.ExitOptions()), 180,
				func(value string) { draft.Exit = domainhistory.ExitFilter(value) })
		})
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Field(c, m.LogsFrom(), func() {
				if ui.TextInput(c, &draft.From).Width(130).Changed() {
					c.Invalidate()
				}
			}).Width(150)
			ui.Field(c, m.LogsTo(), func() {
				if ui.TextInput(c, &draft.To).Width(130).Changed() {
					c.Invalidate()
				}
			}).Width(150)
			ui.Field(c, m.LogsPruneLimit(), func() {
				if ui.TextInput(c, s.PruneLimit()).Width(120).Changed() {
					c.Invalidate()
				}
			}).Width(150)
		})

		ui.Text(c, m.LogsPrunePreview(s.PrunePreview())).FontSize(kit.M(c).Font.Footnote)
		if s.PruneDateProblem() {
			ui.Text(c, m.LogsProblemDate()).FontSize(kit.M(c).Font.Footnote).
				TextColor(uitheme.DangerText(t))
		}
		if s.PruneLimitProblem() {
			ui.Text(c, m.LogsPruneLimitProblem()).FontSize(kit.M(c).Font.Footnote).
				TextColor(uitheme.DangerText(t))
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if ui.PrimaryButton(c, m.LogsPruneApply()).Clicked() {
				s.ConfirmPrune()
				c.Invalidate()
			}
			if ui.Button(c, m.LogsCancel()).Clicked() {
				s.CancelPrune()
				c.Invalidate()
			}
		})
	})
}

// selectChoice is one option of a filter selector: the stored filter value
// and the localized label shown to the user.
type selectChoice struct {
	Value string
	Label string
}

// selectField renders a labelled select over choices and reports the chosen
// filter value. The select is located by its label, which facets keep unique.
func selectField(c *ui.Context, label, selected string, choices []selectChoice,
	width float32, choose func(string)) {
	labels := make([]string, len(choices))
	current := ""
	for index, choice := range choices {
		labels[index] = choice.Label
		if choice.Value == selected {
			current = choice.Label
		}
	}
	if current == "" && len(choices) > 0 {
		current = labels[0]
	}
	if ui.Select(c, &current, labels).Label(label).Tooltip(label).Width(width).Changed() {
		for index, text := range labels {
			if text == current {
				choose(choices[index].Value)
				break
			}
		}
		c.Invalidate()
	}
}

// facetChoices prepends the All value to one facet's values. The label
// function localizes a value; identityLabel shows the value itself.
func facetChoices(allLabel string, options []FacetValue, label func(string) string) []selectChoice {
	choices := make([]selectChoice, 0, len(options)+1)
	choices = append(choices, selectChoice{Value: "", Label: allLabel})
	for _, option := range options {
		choices = append(choices, selectChoice{Value: option.Value, Label: label(option.Value)})
	}
	return choices
}

func identityLabel(value string) string { return value }

// exitChoices localizes the fixed exit-status vocabulary.
func exitChoices(m locales.Messages, options []FacetValue) []selectChoice {
	choices := []selectChoice{{Value: "", Label: m.LogsExitAll()}}
	for _, option := range options {
		label := m.LogsExitUnknown()
		switch domainhistory.ExitFilter(option.Value) {
		case domainhistory.ExitSuccess:
			label = m.LogsExitSuccess()
		case domainhistory.ExitFailure:
			label = m.LogsExitFailure()
		}
		choices = append(choices, selectChoice{Value: option.Value, Label: label})
	}
	return choices
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
		if strings.HasPrefix(option.Key, domainhistory.OriginHostKeyPrefix) {
			return m.LogsHostOrigin(option.Label)
		}
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
						ui.Text(c, meta).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
						// A WireGuard row also keeps the handshake time and the
						// counters recorded for that event, so the Logs surface
						// shows the same tunnel detail as the Profile row.
						if log.NetworkProviderKind == string(domainvault.NetworkKindWireGuard) {
							ui.Text(c, uiconnection.WireGuardDetail(m,
								!log.NetworkLastHandshake.IsZero(), log.NetworkLastHandshake,
								log.NetworkRxBytes, log.NetworkTxBytes)).
								FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
						}
					} else {
						status := statusLabel(m, log.Status)
						meta := m.LogsConnectionMeta(status, log.Kind)
						if log.IsUnsaved() {
							meta = m.LogsConnectionMetaUnsaved(status, log.Kind)
						}
						ui.Text(c, meta).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
					}
					if address := connectionAddress(log); address != "" {
						ui.Text(c, address).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
					}
					if log.Error != "" {
						ui.Text(c, log.Error).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
					}
					ui.Text(c, timestamp(log.StartedAt)).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
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
					ui.Text(c, historyMeta(m, entry)).
						FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).SingleLine()
					// Provenance says where the row came from; the exit and
					// tier badges label how much the capture observed.
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						kit.Pill(c, provenanceLabel(m, entry))
						exitBadge(c, m, t, entry)
						if entry.CaptureTier != 0 {
							kit.Pill(c, m.LogsHistoryTierBadge(entry.CaptureTier))
						}
					})
				})
				if kit.SecondaryButton(c, icons.Info, m.LogsHistoryDetails()).Clicked() {
					s.SelectEntry(entry.ID)
					c.Invalidate()
				}
				// Copy and Insert are explicit row actions. Insert writes the
				// command into the focused Session's prompt and never submits
				// it; nothing is sent automatically.
				if kit.SecondaryButton(c, icons.Copy, m.LogsCopy()).Clicked() {
					c.WriteClipboard(entry.Command)
				}
				if kit.SecondaryButton(c, icons.CornerDownLeft, m.LogsInsert()).Clicked() {
					s.InsertEntry(entry.Command)
					c.Invalidate()
				}
				if kit.SecondaryButton(c, icons.SquareTerminal, m.LogsOpenTerminal()).Clicked() {
					s.OpenTerminal(entry.SessionID, entry.HostID)
					c.Invalidate()
				}
				// While the row awaits confirmation its Delete action is
				// replaced by the confirmation strip below, so the only
				// Delete button in the strip is the confirming one.
				if s.PendingDeleteEntry() != entry.ID &&
					kit.IconButton(c, icons.Trash, m.LogsDelete()).Clicked() {
					s.RequestDeleteEntry(entry.ID)
					c.Invalidate()
				}
			})
			if s.PendingDeleteEntry() == entry.ID {
				kit.Notice(c, icons.Trash, t.Danger, m.LogsDeleteConfirm(), func() {
					if kit.GhostButton(c, "", m.LogsCancel()).Clicked() {
						s.CancelDeleteEntry()
						c.Invalidate()
					}
					if kit.DangerButton(c, icons.Trash, m.LogsDelete()).Clicked() {
						s.ConfirmDeleteEntry()
						c.Invalidate()
					}
				})
			}
			if s.EntrySelected(entry.ID) {
				historyDetail(c, s, entry, m, t)
			}
		}
	}
	showMore(c, s, m)
}

// historyMeta is the compact row summary: Host or origin, cwd, duration, and
// start time. A field the Session never observed is labelled unknown.
func historyMeta(m locales.Messages, entry domainhistory.Entry) string {
	unknown := m.LogsHistoryUnknown()
	cwd := entry.CWD
	if cwd == "" {
		cwd = unknown
	}
	duration := unknown
	if !entry.FinishedAt.IsZero() && !entry.RecordedAt.IsZero() {
		duration = formatDuration(entry.Duration)
	}
	return m.LogsHistoryRowMeta(historyHostLabel(m, entry), cwd, duration,
		timestamp(entry.RecordedAt))
}

// provenanceLabel says whether a row came from this device, the shell-history
// import, an Atuin import, or another device through sync.
func provenanceLabel(m locales.Messages, entry domainhistory.Entry) string {
	switch entry.Source {
	case domainhistory.SourceShellHistory:
		return m.LogsProvenanceShellImport()
	case domainhistory.SourceAtuinImport:
		return m.LogsProvenanceAtuinImport()
	case domainhistory.SourceAtuinSync:
		return m.LogsProvenanceAtuinSync()
	default:
		return m.LogsProvenanceThisDevice()
	}
}

// exitBadge colors a known success green and a known failure red. An unknown
// exit status keeps a muted badge instead of implying success.
func exitBadge(c *ui.Context, m locales.Messages, t *ui.Theme, entry domainhistory.Entry) {
	text := m.LogsHistoryExitUnknown()
	color := t.TextMuted
	if entry.HasExitCode {
		text = m.LogsHistoryExitBadge(entry.ExitCode)
		color = t.Success
		if entry.ExitCode != 0 {
			color = uitheme.DangerText(t)
		}
	}
	badge := ui.Row(c).Gap(5).Padding(1, 7).Radius(kit.M(c).Radius.Pill).AlignItems(ui.Center).
		Background(color.Alpha(0.12)).Shrink(0)
	badge.Children(func() {
		ui.Text(c, text).FontSize(kit.M(c).Font.Caption).FontWeight(500).TextColor(color).NoWrap()
	})
}

// historyDetail renders the full command, provenance badges, and every field
// captured for one row. It stays inline under the selected row. Missing values
// explain what the capture could not observe rather than showing an empty
// string that looks like a real value.
func historyDetail(c *ui.Context, s *State, entry domainhistory.Entry, m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().Padding(10).Gap(6).Background(t.Surface).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.LogsHistoryDetailTitle()).Bold().Grow(1)
			if ui.Button(c, m.LogsHistoryDetailClose()).Clicked() {
				s.CloseEntryDetail()
				c.Invalidate()
			}
		})
		ui.Text(c, entry.Command).FontSize(kit.M(c).Font.Callout).Wrap()
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			kit.Pill(c, provenanceLabel(m, entry))
			exitBadge(c, m, t, entry)
			if entry.CaptureTier != 0 {
				kit.Pill(c, m.LogsHistoryTierBadge(entry.CaptureTier))
			}
		})

		detailField(c, m, m.LogsHistoryFieldHost(), historyHostLabel(m, entry))
		detailField(c, m, m.LogsHistoryFieldHostID(), detailValue(m, string(entry.HostID)))
		detailField(c, m, m.LogsHistoryFieldHostLabel(), detailValue(m, entry.HostLabel))
		detailField(c, m, m.LogsHistoryFieldUnsaved(),
			map[bool]string{true: m.LogsHistoryYes(), false: m.LogsHistoryNo()}[entry.UnsavedHost])
		detailField(c, m, m.LogsHistoryFieldScope(), detailValue(m, entry.Scope))
		detailField(c, m, m.LogsHistoryFieldOrigin(), detailValue(m, entry.Origin))
		detailField(c, m, m.LogsHistoryFieldID(), detailValue(m, entry.ID))
		detailField(c, m, m.LogsHistoryFieldSession(), detailValue(m, string(entry.SessionID)))
		detailField(c, m, m.LogsHistoryFieldSessionUUID(), detailValue(m, entry.SessionUUID))
		detailField(c, m, m.LogsHistoryFieldCwd(), detailCwd(m, entry))
		detailField(c, m, m.LogsHistoryFieldShell(), detailShell(m, entry))
		detailField(c, m, m.LogsHistoryFieldStart(), detailTime(m, entry.RecordedAt))
		detailField(c, m, m.LogsHistoryFieldFinish(), detailFinish(m, entry))
		detailField(c, m, m.LogsHistoryFieldDuration(), detailDuration(m, entry))
		detailField(c, m, m.LogsHistoryFieldExit(), detailExit(m, entry))
		detailField(c, m, m.LogsHistoryFieldSource(), sourceLabel(m, entry.Source))
		detailField(c, m, m.LogsHistoryFieldTier(), detailTier(m, entry))
		detailField(c, m, m.LogsHistoryFieldAuthor(), detailValue(m, entry.Author))
		detailField(c, m, m.LogsHistoryFieldAuthorKind(), detailValue(m, entry.AuthorKind))
		detailField(c, m, m.LogsHistoryFieldIntent(), detailValue(m, entry.Intent))
		historyMissingFields(c, m, entry, t)
	})
}

// historyMissingFields explains each captured field the Session never
// observed. Naming the field and the capture tier that could not observe it
// keeps a missing cwd distinguishable from an empty one, which is the point of
// the partial-field label.
func historyMissingFields(c *ui.Context, m locales.Messages, entry domainhistory.Entry, t *ui.Theme) {
	reason := historyMissingReason(m, entry)
	missing := []string{}
	if entry.CWD == "" {
		missing = append(missing, m.LogsHistoryFieldCwd())
	}
	if !entry.HasExitCode {
		missing = append(missing, m.LogsHistoryFieldExit())
	}
	if entry.FinishedAt.IsZero() || entry.RecordedAt.IsZero() {
		missing = append(missing, m.LogsHistoryFieldDuration())
	}
	for _, field := range missing {
		ui.Text(c, m.LogsHistoryMissingField(field, reason)).
			FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).Wrap()
	}
}

// historyMissingReason names what the capture path could not observe. The
// reason is per row, not per field: a row shares one capture tier. A row that
// came from an import or another device has no live capture path, so it gets a
// neutral reason instead of blaming a shell that never ran here.
func historyMissingReason(m locales.Messages, entry domainhistory.Entry) string {
	if entry.Source != "" && entry.Source != domainhistory.SourceSession {
		return m.LogsHistoryMissingReasonImported()
	}
	switch entry.CaptureTier {
	case domainhistory.CaptureTierHook:
		return m.LogsHistoryMissingReasonHook()
	case domainhistory.CaptureTierNativeOSC:
		return m.LogsHistoryMissingReasonNative()
	case domainhistory.CaptureTierFallback:
		return m.LogsHistoryMissingReasonTyped()
	default:
		return m.LogsHistoryMissingReasonUnknown()
	}
}

// detailField renders one labelled capture field as one localized line, so the
// label and its value stay together when the row wraps.
func detailField(c *ui.Context, m locales.Messages, label, value string) {
	ui.Text(c, m.LogsHistoryDetailField(label, value)).FontSize(kit.M(c).Font.Footnote).Wrap()
}

func detailValue(m locales.Messages, value string) string {
	if value == "" {
		return m.LogsHistoryUnknown()
	}
	return value
}

func detailTime(m locales.Messages, value time.Time) string {
	if value.IsZero() {
		return m.LogsHistoryUnknown()
	}
	return timestamp(value)
}

func detailCwd(m locales.Messages, entry domainhistory.Entry) string {
	if entry.CWD == "" {
		return m.LogsHistoryMissingCwd()
	}
	return entry.CWD
}

func detailShell(m locales.Messages, entry domainhistory.Entry) string {
	if entry.Shell == "" {
		return m.LogsHistoryMissingShell()
	}
	return entry.Shell
}

func detailFinish(m locales.Messages, entry domainhistory.Entry) string {
	if entry.FinishedAt.IsZero() {
		return m.LogsHistoryMissingFinish()
	}
	return timestamp(entry.FinishedAt)
}

// detailDuration reports the captured duration, or explains that the shell
// never reported a finish. A zero duration with a known finish is valid.
func detailDuration(m locales.Messages, entry domainhistory.Entry) string {
	if entry.FinishedAt.IsZero() || entry.RecordedAt.IsZero() {
		return m.LogsHistoryMissingDuration()
	}
	return formatDuration(entry.Duration)
}

// detailExit reports the captured exit code, or explains that the shell never
// reported one. A known zero is shown as 0.
func detailExit(m locales.Messages, entry domainhistory.Entry) string {
	if !entry.HasExitCode {
		return m.LogsHistoryMissingExit()
	}
	return strconv.Itoa(entry.ExitCode)
}

func detailTier(m locales.Messages, entry domainhistory.Entry) string {
	if entry.CaptureTier == 0 {
		return m.LogsHistoryMissingTier()
	}
	return strconv.Itoa(entry.CaptureTier)
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
	if entry.HostID != "" {
		return string(entry.HostID)
	}
	if entry.Origin != "" {
		return entry.Origin
	}
	return m.LogsHistoryUnknown()
}

func statusLabel(m locales.Messages, status domainhistory.ConnectionStatus) string {
	if status == domainhistory.ConnectionFailed {
		return m.LogsStatusFailed()
	}
	return m.LogsStatusConnected()
}

func sourceLabel(m locales.Messages, source string) string {
	switch source {
	case domainhistory.SourceShellHistory:
		return m.LogsSourceShell()
	case domainhistory.SourceAtuinImport:
		return m.LogsSourceAtuinImport()
	case domainhistory.SourceAtuinSync:
		return m.LogsSourceAtuinSync()
	default:
		return m.LogsSourceSession()
	}
}

// formatDuration renders an execution duration compactly. It clamps a
// negative value instead of showing a nonsensical one.
func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	if duration < time.Second {
		return fmt.Sprintf("%dms", duration.Milliseconds())
	}
	if duration < time.Minute {
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	seconds := int(duration / time.Second)
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
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
