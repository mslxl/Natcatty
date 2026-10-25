package monitoring

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"

	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
)

// View returns the monitoring surface. The surface is one flex column that
// takes the height its parent leaves, so the table, charts, and editors can
// scroll instead of running past the window at the 720x480 minimum.
func View(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		s.Flush()
		s.SyncColumnWidths()
		m := locales.In(c.Locale())
		t := c.Theme()
		ui.Column(c).FillWidth().Grow(1).MinHeight(0).Gap(6).Children(func() {
			if s.Editing() {
				editor(c, s, m, t)
				return
			}
			header(c, s, m, t)
			if s.ColumnsOpen() {
				columnsEditor(c, s, m, t)
				return
			}
			if problem := problemText(m, s.Problem()); problem != "" {
				ui.Text(c, problem).FontSize(12).TextColor(t.Danger)
			}
			panel, ok := s.ActivePanel()
			if !ok {
				emptyPanels(c, s, m, t)
				return
			}
			if panel.View == domainpreferences.MonitorViewCharts {
				charts(c, s, panel, m, t)
				return
			}
			hostTable(c, s, panel, m, t)
		})
	}
}

func header(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Wrap().PaddingX(4).Gap(8).Children(func() {
		ui.Text(c, m.MonitoringTitle()).FontSize(20).Bold().NoWrap()
		ui.Column(c).Grow(1)
		if text := summaryText(s, m); text != "" {
			ui.Text(c, text).FontSize(12).TextColor(t.TextMuted).NoWrap()
		}
	})

	ui.Row(c).FillWidth().Wrap().PaddingX(4).Gap(8).Children(func() {
		ui.SearchField(c, &s.search).Placeholder(m.MonitoringSearch()).Grow(1).MinWidth(140)
		view := 0
		if panel, ok := s.ActivePanel(); ok && panel.View == domainpreferences.MonitorViewCharts {
			view = 1
		}
		segments := ui.Segmented(c, &view, m.MonitoringViewList(), m.MonitoringViewCharts())
		if segments.Changed() {
			if view == 1 {
				s.UpdatePanelView(domainpreferences.MonitorViewCharts)
			} else {
				s.UpdatePanelView(domainpreferences.MonitorViewList)
			}
		}
		if ui.Button(c, m.MonitoringNewPanel()).Clicked() {
			s.BeginCreatePanel()
			c.Invalidate()
		}
	})

	if len(s.Panels()) == 0 {
		return
	}
	// Wrap the row rather than sizing it from the window: the surface is
	// about 432 DIPs wide inside the shell at the 720x480 minimum, so the
	// last controls move to a second line instead of being clipped.
	ui.Row(c).FillWidth().Wrap().PaddingX(4).Gap(8).Children(func() {
		names := make([]string, 0, len(s.Panels()))
		ids := make(map[string]string, len(s.Panels()))
		selectedName := ""
		for _, panel := range s.Panels() {
			names = append(names, panel.Name)
			ids[panel.Name] = panel.ID
			if panel.ID == s.activePanelID() {
				selectedName = panel.Name
			}
		}
		chosen := selectedName
		ui.Select(c, &chosen, names).Grow(1).MinWidth(120)
		if chosen != selectedName {
			if id, ok := ids[chosen]; ok {
				s.SetPanel(id)
				c.Invalidate()
			}
		}

		panel, ok := s.ActivePanel()
		if !ok {
			return
		}
		intervals := make([]string, 0, len(domainpreferences.MonitorIntervalChoices))
		for _, choice := range domainpreferences.MonitorIntervalChoices {
			intervals = append(intervals, strconv.Itoa(choice))
		}
		interval := strconv.Itoa(panel.Interval)
		ui.Select(c, &interval, intervals).Width(58).Label(m.MonitoringRefresh())
		if interval != strconv.Itoa(panel.Interval) {
			if seconds, err := strconv.Atoi(interval); err == nil {
				s.UpdatePanelInterval(seconds)
				c.Invalidate()
			}
		}

		sorts := []string{m.MonitoringSortManual(), m.MonitoringSortName()}
		order := sorts[0]
		if panel.Sort == domainpreferences.MonitorSortName {
			order = sorts[1]
		}
		chosenOrder := order
		ui.Select(c, &chosenOrder, sorts).Width(92).Label(m.MonitoringSort())
		if chosenOrder != order {
			if chosenOrder == sorts[1] {
				s.UpdatePanelSort(domainpreferences.MonitorSortName)
			} else {
				s.UpdatePanelSort(domainpreferences.MonitorSortManual)
			}
			c.Invalidate()
		}

		if ui.Button(c, m.MonitoringEditPanel()).Clicked() {
			s.BeginEditPanel()
			c.Invalidate()
		}
		// The header keeps its Columns label while the editor is open; the
		// editor's pinned Done is the primary way out, and either closes it.
		if ui.Button(c, m.MonitoringColumns()).Clicked() {
			s.ToggleColumns()
			c.Invalidate()
		}
	})
}

func summaryText(s *State, m locales.Messages) string {
	panel, ok := s.ActivePanel()
	if !ok {
		return ""
	}
	live := 0
	for _, host := range s.DisplayHosts() {
		if s.HostState(string(host.ID)).Status == domainmonitor.StatusLive {
			live++
		}
	}
	text := m.MonitoringSummary(live, len(panel.HostIDs))
	if s.problem == ProblemLoad && s.loadErr != nil {
		text = m.MonitoringProblemLoad()
	}
	return text
}

func emptyPanels(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Gap(8).Children(func() {
		ui.Text(c, m.MonitoringNoPanelsTitle()).FontSize(16).Bold()
		ui.Text(c, m.MonitoringNoPanelsBody()).FontSize(12).TextColor(t.TextMuted)
		if ui.PrimaryButton(c, m.MonitoringNewPanel()).Clicked() {
			s.BeginCreatePanel()
			c.Invalidate()
		}
	})
}

func hostTable(c *ui.Context, s *State, panel domainpreferences.MonitorPanel, m locales.Messages, t *ui.Theme) {
	hosts := s.DisplayHosts()
	if len(panel.HostIDs) == 0 {
		ui.Text(c, m.MonitoringEmptyPanel()).FontSize(12).TextColor(t.TextMuted)
		return
	}
	if len(hosts) == 0 {
		ui.Text(c, m.MonitoringNoMatches()).FontSize(12).TextColor(t.TextMuted)
		return
	}
	keys := visibleColumns(s)
	columns := make([]ui.TableColumn, 0, len(keys))
	for _, key := range keys {
		columns = append(columns, ui.TableColumn{
			Title:    columnTitle(m, key),
			ID:       key,
			Width:    float32(s.Width(key)),
			MinWidth: 56,
			MaxWidth: 640,
		})
	}
	s.table.Columns.Widths = s.TableWidths()
	now := s.now()
	started := s.StartedAt()
	ui.Table(c, &s.table, columns, len(hosts), func(row, col int) {
		if row < 0 || row >= len(hosts) || col < 0 || col >= len(keys) {
			return
		}
		host := hosts[row]
		state := s.HostState(string(host.ID))
		cell(c, s, m, t, keys[col], host, state, panel, started, now)
	}).Grow(1).FillWidth()
}

func cell(c *ui.Context, s *State, m locales.Messages, t *ui.Theme, key string,
	host domainvault.Host, state domainmonitor.HostState,
	panel domainpreferences.MonitorPanel, started, now time.Time) {
	ready := sampleReady(state)
	sample := state.Sample
	switch key {
	case "host":
		ui.Column(c).Gap(1).Children(func() {
			// The typed state sits beside the label so it never scrolls out
			// of view with the optional Status column, and the reason gets
			// a line of its own: a refused or unsupported Host must not look
			// like an idle one.
			ui.Row(c).FillWidth().Gap(6).Children(func() {
				ui.Text(c, s.HostLabel(host)).Bold().SingleLine().Grow(1).MinWidth(0)
				text, color := statusText(m, state, panel, now, t)
				ui.Text(c, text).FontSize(10).TextColor(color).NoWrap()
			})
			details := host.Hostname
			if details == "" {
				details = string(host.ID)
			}
			// A Host without a sample still names the GPU slot with a dash;
			// a sampled Host without a GPU (an invalid value) omits it as
			// before.
			if !ready || domainmonitor.Valid(sample.GPU) {
				details += " · " + m.MonitoringGpu() + " " + percentText(unknownUnless(ready, sample.GPU))
			}
			ui.Text(c, details).FontSize(11).TextColor(t.TextMuted).SingleLine()
			if reason := reasonText(m, state.Reason); reason != "" {
				ui.Text(c, reason).FontSize(10).TextColor(t.TextMuted).SingleLine()
			}
		})
	case "latency":
		latency := unknownUnless(ready, sample.Latency)
		ui.Text(c, latencyText(latency)).FontSize(12).
			TextColor(toneColor(t, domainmonitor.ToneForLatency(latency)))
	case "tags":
		ui.Text(c, strings.Join(host.Tags, ", ")).FontSize(12).TextColor(t.TextMuted).SingleLine()
	case "notes":
		ui.Text(c, host.Notes).FontSize(12).TextColor(t.TextMuted).SingleLine()
	case "cpu":
		percentCell(c, m.MonitoringCpu(), unknownUnless(ready, sample.CPU), t)
	case "memory":
		percentCell(c, m.MonitoringMemory(), unknownUnless(ready, sample.Memory), t)
	case "disks":
		percentCell(c, m.MonitoringDisk(), unknownUnless(ready, sample.Disk), t)
	case "network":
		rx, tx := unknownUnless(ready, sample.Rx), unknownUnless(ready, sample.Tx)
		ui.Column(c).Gap(0).Children(func() {
			ui.Text(c, "↓ "+rateText(rx)).FontSize(11).TextColor(t.Text).SingleLine()
			ui.Text(c, "↑ "+rateText(tx)).FontSize(11).TextColor(t.TextMuted).SingleLine()
		})
	case "updated":
		ui.Text(c, ageText(m, state, now)).FontSize(11).TextColor(t.TextMuted)
	case "status":
		ui.Column(c).Gap(1).Children(func() {
			text, color := statusText(m, state, panel, now, t)
			ui.Text(c, text).FontSize(11).TextColor(color).SingleLine()
			if reason := reasonText(m, state.Reason); reason != "" {
				ui.Text(c, reason).FontSize(10).TextColor(t.TextMuted).SingleLine()
			}
		})
	case "terminal":
		if ui.Button(c, m.MonitoringOpen()).Key("monitor-open-" + string(host.ID)).Clicked() {
			s.OpenHost(string(host.ID))
			c.Invalidate()
		}
	}
}

// sampleReady reports whether the Host has completed a measurement. A zero
// Sample is what the surface sees for a queued, refused, unsupported, capped,
// or not-yet-sampled Host; its zero metrics are absence, not healthy values.
func sampleReady(state domainmonitor.HostState) bool { return !state.Sample.At.IsZero() }

// unknownUnless hides the fake zero of an absent Sample behind NaN, which
// every metric formatter paints as a dash.
func unknownUnless(ready bool, value float64) float64 {
	if !ready {
		return math.NaN()
	}
	return value
}

func percentCell(c *ui.Context, label string, value float64, t *ui.Theme) {
	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, percentText(value)).FontSize(11).TextColor(toneColor(t, domainmonitor.ToneForPercent(value)))
		if domainmonitor.Valid(value) {
			ui.Progress(c, value/100).Grow(1)
		}
	})
}

func visibleColumns(s *State) []string {
	out := make([]string, 0, len(domainpreferences.MonitorColumnKeys))
	for _, key := range domainpreferences.MonitorColumnKeys {
		if key == "host" || key == "terminal" || !s.Hidden(key) {
			out = append(out, key)
		}
	}
	return out
}

func columnTitle(m locales.Messages, key string) string {
	switch key {
	case "host":
		return m.MonitoringHost()
	case "latency":
		return m.MonitoringLatency()
	case "tags":
		return m.MonitoringTags()
	case "notes":
		return m.MonitoringNotes()
	case "cpu":
		return m.MonitoringCpu()
	case "memory":
		return m.MonitoringMemory()
	case "disks":
		return m.MonitoringDisk()
	case "network":
		return m.MonitoringNetwork()
	case "updated":
		return m.MonitoringUpdated()
	case "status":
		return m.MonitoringStatus()
	case "terminal":
		return m.MonitoringTerminal()
	default:
		return key
	}
}

// columnsEditor takes the surface height the header leaves. Its rows scroll
// and its Done row stays pinned inside the card, so at 720x480 the editor can
// always be closed and the lower rows (Network, Updated, Status, Terminal)
// are reachable.
func columnsEditor(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().MinHeight(0).Padding(10).Gap(6).
		Background(t.Surface).Radius(t.Radius).Children(func() {
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			ui.Text(c, m.MonitoringColumns()).FontSize(14).Bold().NoWrap()
			ui.Column(c).Grow(1).MinWidth(0)
			ui.Text(c, m.MonitoringColumnsHint()).FontSize(11).TextColor(t.TextMuted).Wrap()
		})
		ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				for _, key := range domainpreferences.MonitorColumnKeys {
					if key == "host" || key == "terminal" {
						ui.Text(c, columnTitle(m, key)+" · "+m.MonitoringAlwaysVisible()).
							FontSize(11).TextColor(t.TextMuted).NoWrap()
						continue
					}
					ui.Row(c).FillWidth().Gap(8).Children(func() {
						visible := !s.Hidden(key)
						if ui.Checkbox(c, &visible, columnTitle(m, key)).Changed() {
							s.ToggleColumn(key)
							c.Invalidate()
						}
						ui.Column(c).Grow(1).MinWidth(0)
						ui.Text(c, strconv.Itoa(s.Width(key))).FontSize(11).
							TextColor(t.TextMuted).NoWrap()
						if ui.Button(c, "−").Key("monitor-narrow-" + key).Width(28).Clicked() {
							s.AdjustColumnWidth(key, -20)
							c.Invalidate()
						}
						if ui.Button(c, "+").Key("monitor-widen-" + key).Width(28).Clicked() {
							s.AdjustColumnWidth(key, 20)
							c.Invalidate()
						}
					})
				}
			})
		})
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.Column(c).Grow(1)
			if ui.PrimaryButton(c, m.MonitoringColumnsDone()).Clicked() {
				s.ToggleColumns()
				c.Invalidate()
			}
		})
	})
}

func charts(c *ui.Context, s *State, panel domainpreferences.MonitorPanel, m locales.Messages, t *ui.Theme) {
	hosts := s.DisplayHosts()
	if len(panel.HostIDs) == 0 {
		ui.Text(c, m.MonitoringEmptyPanel()).FontSize(12).TextColor(t.TextMuted)
		return
	}
	if len(hosts) == 0 {
		ui.Text(c, m.MonitoringNoMatches()).FontSize(12).TextColor(t.TextMuted)
		return
	}
	now := s.now()
	started := s.StartedAt()
	// A wider minimum keeps one card per row in the shell's 432-DIP content
	// column at 720x480, so a title is not truncated and the bottom metric
	// row and its Open action stay on one line. Two cards share a row from
	// about 840 DIPs up, as at 1100x720.
	ui.GridView(c, &s.grid, len(hosts), 300, 300, func(index int) {
		if index < 0 || index >= len(hosts) {
			return
		}
		host := hosts[index]
		chartCard(c, s, m, t, host, panel, started, now)
	}).Grow(1).FillWidth()
}

func chartCard(c *ui.Context, s *State, m locales.Messages, t *ui.Theme,
	host domainvault.Host, panel domainpreferences.MonitorPanel, started, now time.Time) {
	state := s.HostState(string(host.ID))
	ready := sampleReady(state)
	sample := state.Sample
	cpu := unknownUnless(ready, sample.CPU)
	memory := unknownUnless(ready, sample.Memory)
	disk := unknownUnless(ready, sample.Disk)
	rx, tx := unknownUnless(ready, sample.Rx), unknownUnless(ready, sample.Tx)
	latency := unknownUnless(ready, sample.Latency)
	gpu := unknownUnless(ready, sample.GPU)
	points := chartPoints(state.Points, started, panel.Range, now)
	ui.Column(c).Fill().Padding(10).Gap(4).Background(t.Surface).Radius(t.Radius).Children(func() {
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(0).Children(func() {
				ui.Text(c, s.HostLabel(host)).Bold().SingleLine()
				ui.Text(c, host.Hostname).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
			text, color := statusText(m, state, panel, now, t)
			ui.Text(c, text).FontSize(11).TextColor(color).NoWrap()
		})
		if reason := reasonText(m, state.Reason); reason != "" {
			ui.Text(c, reason).FontSize(10).TextColor(t.TextMuted).SingleLine()
		}
		metricTrend(c, m.MonitoringCpu(), percentText(cpu),
			points, func(p domainmonitor.Point) float64 { return p.CPU }, 100,
			t.Accent, toneColor(t, domainmonitor.ToneForPercent(cpu)))
		metricTrend(c, m.MonitoringMemory(), percentText(memory),
			points, func(p domainmonitor.Point) float64 { return p.Memory }, 100,
			t.Success, toneColor(t, domainmonitor.ToneForPercent(memory)))
		metricTrend(c, m.MonitoringDisk(), percentText(disk),
			points, func(p domainmonitor.Point) float64 { return p.Disk }, 100,
			t.Accent, toneColor(t, domainmonitor.ToneForPercent(disk)))
		maxRate := 1024.0
		for _, p := range points {
			if domainmonitor.Valid(p.Rx) && p.Rx > maxRate {
				maxRate = p.Rx
			}
			if domainmonitor.Valid(p.Tx) && p.Tx > maxRate {
				maxRate = p.Tx
			}
		}
		networkValue := "↓ " + rateText(rx) + "  ↑ " + rateText(tx)
		metricTrend(c, m.MonitoringNetwork(), networkValue, points,
			func(p domainmonitor.Point) float64 { return p.Rx }, maxRate, t.Warning, t.Text)
		// Each metric is one unwrapping text, and the row wraps between them
		// as whole units. The wider card keeps the English row on one line;
		// a narrower one moves the whole group rather than splitting a label
		// from its value.
		ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.MonitoringDisk()+" "+percentText(disk)).
				FontSize(11).NoWrap().
				TextColor(toneColor(t, domainmonitor.ToneForPercent(disk)))
			ui.Text(c, m.MonitoringLatency()+" "+latencyText(latency)).
				FontSize(11).NoWrap().
				TextColor(toneColor(t, domainmonitor.ToneForLatency(latency)))
			// Keep the GPU slot visible with a dash until the first sample;
			// a sampled Host without a GPU omits the slot as before.
			if !ready || domainmonitor.Valid(gpu) {
				ui.Text(c, m.MonitoringGpu()+" "+percentText(gpu)).
					FontSize(11).NoWrap().
					TextColor(toneColor(t, domainmonitor.ToneForPercent(gpu)))
			}
			ui.Column(c).Grow(1).MinWidth(0)
			if ui.Button(c, m.MonitoringOpen()).Key("monitor-chart-open-" + string(host.ID)).Clicked() {
				s.OpenHost(string(host.ID))
				c.Invalidate()
			}
		})
	})
}

func metricTrend(c *ui.Context, label, value string,
	points []domainmonitor.Point, pick func(domainmonitor.Point) float64,
	scale float64, color, valueColor ui.Color) {
	ui.Row(c).FillWidth().Gap(4).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(c.Theme().TextMuted)
		ui.Column(c).Grow(1)
		ui.Text(c, value).FontSize(11).TextColor(valueColor)
	})
	sparkline(c, points, pick, scale, color)
}

func sparkline(c *ui.Context, points []domainmonitor.Point, pick func(domainmonitor.Point) float64,
	scale float64, color ui.Color) {
	// A card with no sample has nothing to plot. Reserving an empty plot strip
	// for each metric pushed the card's action below the 720x480 fold, so
	// absent or single-point trends take no vertical space at all.
	if len(points) < 2 || scale <= 0 {
		return
	}
	chart.Plot(c, chart.Axes{}, func(area ui.Element) {
		area.Draw(func(p *ui.Painter, r ui.Rect) {
			xs := make([]float32, len(points))
			ys := make([]float32, len(points))
			start := points[0].At
			span := points[len(points)-1].At.Sub(start)
			for index, point := range points {
				if span > 0 {
					xs[index] = float32(point.At.Sub(start)) / float32(span)
				} else {
					xs[index] = 0
				}
				value := pick(point)
				if !domainmonitor.Valid(value) {
					ys[index] = float32(math.NaN())
					continue
				}
				y := value / scale
				if y < 0 {
					y = 0
				}
				if y > 1 {
					y = 1
				}
				ys[index] = float32(y)
			}
			p.FillPath(chart.Area(r, xs, nil, ys, chart.CurveLinear), color.Alpha(0.12))
			p.StrokePath(chart.Line(r, xs, ys, chart.CurveLinear), 1.5, color)
		})
	}).Height(18).FillWidth()
}

func chartPoints(points []domainmonitor.Point, started time.Time, minutes int, now time.Time) []domainmonitor.Point {
	points = domainmonitor.PointsSince(points, started)
	if minutes <= 0 {
		return points
	}
	cutoff := now.Add(-time.Duration(minutes) * time.Minute)
	first := 0
	for first < len(points) && points[first].At.Before(cutoff) {
		first++
	}
	return points[first:]
}

// editor is a bounded surface: its fields, Host list, and problem text scroll
// in the space left above the pinned action rows. At 720x480 Save, Cancel, and
// Delete therefore stay inside the window even though the form as a whole is
// taller than the content column.
func editor(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	form := s.Form()
	title := m.MonitoringPanelFormCreate()
	if _, ok := s.findPanel(form.ID); ok {
		title = m.MonitoringPanelFormEdit()
	}
	ui.Text(c, title).FontSize(20).Bold().NoWrap()

	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Form(c, func() {
				ui.Field(c, m.MonitoringName(), func() {
					ui.TextInput(c, &form.Name).FillWidth()
				})
				ui.Field(c, m.MonitoringDescription(), func() {
					ui.TextInput(c, &form.Description).FillWidth()
				})
				ui.Field(c, m.MonitoringPanelFormView(), func() {
					ui.RadioGroup(c, func() {
						ui.Radio(c, &form.View, domainpreferences.MonitorViewList, m.MonitoringViewList())
						ui.Radio(c, &form.View, domainpreferences.MonitorViewCharts, m.MonitoringViewCharts())
					})
				})
				ui.Field(c, m.MonitoringPanelFormInterval(), func() {
					intervals := make([]string, 0, len(domainpreferences.MonitorIntervalChoices))
					for _, choice := range domainpreferences.MonitorIntervalChoices {
						intervals = append(intervals, strconv.Itoa(choice))
					}
					ui.Select(c, &form.Interval, intervals).Width(80)
				})
				ui.Field(c, m.MonitoringPanelFormRange(), func() {
					ranges := make([]string, 0, len(domainpreferences.MonitorRangeChoices))
					for _, choice := range domainpreferences.MonitorRangeChoices {
						ranges = append(ranges, strconv.Itoa(choice))
					}
					ui.Select(c, &form.Range, ranges).Width(80)
				})
				ui.Field(c, m.MonitoringPanelFormSort(), func() {
					ui.RadioGroup(c, func() {
						ui.Radio(c, &form.Sort, domainpreferences.MonitorSortManual, m.MonitoringSortManual())
						ui.Radio(c, &form.Sort, domainpreferences.MonitorSortName, m.MonitoringSortName())
					})
				})
				ui.Field(c, m.MonitoringPanelHosts(), func() {
					ui.Text(c, m.MonitoringSelectedCount(len(form.HostIDs))).FontSize(11).TextColor(t.TextMuted)
				})
			}).FillWidth()

			ui.Text(c, m.MonitoringPanelFormHostsHint()).FontSize(11).TextColor(t.TextMuted).Wrap()
			hosts := s.Hosts()
			if len(hosts) == 0 {
				ui.Text(c, m.MonitoringNoHostsAvailable()).FontSize(12).TextColor(t.TextMuted)
			} else {
				for _, host := range hosts {
					id := string(host.ID)
					selected := s.FormHostSelected(id)
					ui.Row(c).FillWidth().Gap(6).Children(func() {
						if ui.Checkbox(c, &selected, s.HostLabel(host)+"  "+host.Hostname).Changed() {
							s.ToggleFormHost(id)
							c.Invalidate()
						}
						ui.Column(c).Grow(1).MinWidth(0)
						if ui.Button(c, "↑").Key("monitor-up-" + id).Width(28).Clicked() {
							s.MoveFormHost(id, -1)
							c.Invalidate()
						}
						if ui.Button(c, "↓").Key("monitor-down-" + id).Width(28).Clicked() {
							s.MoveFormHost(id, 1)
							c.Invalidate()
						}
					})
				}
			}
			if problem := problemText(m, s.Problem()); problem != "" {
				ui.Text(c, problem).FontSize(12).TextColor(t.Danger).Wrap()
			}
		})
	})

	if s.pendingDelete {
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			ui.Text(c, m.MonitoringDeleteConfirm(form.Name)).Grow(1).MinWidth(120).TextColor(t.Danger).Wrap()
			if ui.Button(c, m.MonitoringCancel()).Clicked() {
				s.CancelDeletePanel()
				c.Invalidate()
			}
			if ui.Button(c, m.MonitoringDeletePanel()).Clicked() {
				s.ConfirmDeletePanel()
				c.Invalidate()
			}
		})
	}
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.MonitoringSave()).Clicked() {
			s.SavePanel()
			c.Invalidate()
		}
		if ui.Button(c, m.MonitoringCancel()).Clicked() {
			s.CancelEdit()
			c.Invalidate()
		}
		if _, ok := s.findPanel(form.ID); ok && !s.pendingDelete {
			if ui.Button(c, m.MonitoringDeletePanel()).Clicked() {
				s.RequestDeletePanel()
				c.Invalidate()
			}
		}
	})
}

func (s *State) activePanelID() string { return s.activeID }

func statusText(m locales.Messages, state domainmonitor.HostState,
	panel domainpreferences.MonitorPanel, now time.Time, t *ui.Theme) (string, ui.Color) {
	if domainmonitor.IsStale(state, time.Duration(panel.Interval)*time.Second, now) {
		return m.MonitoringStale(), t.Warning
	}
	switch state.Status {
	case domainmonitor.StatusLive:
		return m.MonitoringStatusLive(), t.Text
	case domainmonitor.StatusConnecting:
		return m.MonitoringStatusConnecting(), t.TextMuted
	case domainmonitor.StatusQueued:
		return m.MonitoringStatusQueued(), t.TextMuted
	case domainmonitor.StatusLogin:
		return m.MonitoringStatusLogin(), t.Danger
	case domainmonitor.StatusError:
		return m.MonitoringStatusError(), t.Danger
	case domainmonitor.StatusUnsupported:
		return m.MonitoringStatusUnsupported(), t.TextMuted
	case domainmonitor.StatusDisconnected:
		return m.MonitoringStatusDisconnected(), t.Warning
	case domainmonitor.StatusUnavailable:
		return m.MonitoringStatusUnavailable(), t.Danger
	case domainmonitor.StatusLimit:
		return m.MonitoringStatusLimit(), t.Warning
	default:
		return m.MonitoringUnknown(), t.TextMuted
	}
}

func reasonText(m locales.Messages, reason domainmonitor.Reason) string {
	switch reason {
	case domainmonitor.ReasonHostKey:
		return m.MonitoringReasonHostKey()
	case domainmonitor.ReasonAuth:
		return m.MonitoringReasonAuth()
	case domainmonitor.ReasonNetwork:
		return m.MonitoringReasonNetwork()
	case domainmonitor.ReasonTimeout:
		return m.MonitoringReasonTimeout()
	case domainmonitor.ReasonUnsupported:
		return m.MonitoringReasonUnsupported()
	case domainmonitor.ReasonCancelled:
		return m.MonitoringReasonCancelled()
	case domainmonitor.ReasonInternal:
		return m.MonitoringReasonInternal()
	case domainmonitor.ReasonOS:
		return m.MonitoringReasonOS()
	default:
		return ""
	}
}

func toneColor(t *ui.Theme, tone domainmonitor.Tone) ui.Color {
	switch tone {
	case domainmonitor.ToneDanger:
		return t.Danger
	case domainmonitor.ToneWarning:
		return t.Warning
	case domainmonitor.ToneUnknown:
		return t.TextMuted
	default:
		return t.Text
	}
}

func percentText(value float64) string {
	if !domainmonitor.Valid(value) {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", value)
}

func latencyText(value float64) string {
	if !domainmonitor.Valid(value) {
		return "—"
	}
	return fmt.Sprintf("%.0f ms", value)
}

func rateText(value float64) string {
	if !domainmonitor.Valid(value) {
		return "—"
	}
	switch {
	case value >= 1<<30:
		return fmt.Sprintf("%.1f GB/s", value/(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", value/(1<<20))
	case value >= 1<<10:
		return fmt.Sprintf("%.1f KB/s", value/(1<<10))
	default:
		return fmt.Sprintf("%.0f B/s", value)
	}
}

func ageText(m locales.Messages, state domainmonitor.HostState, now time.Time) string {
	at := state.Sample.At
	if at.IsZero() {
		at = state.At
	}
	if at.IsZero() {
		return "—"
	}
	age := now.Sub(at)
	if age < 2*time.Second {
		return m.MonitoringUpdatedNow()
	}
	if age < time.Minute {
		return m.MonitoringUpdatedSeconds(int(age / time.Second))
	}
	return m.MonitoringUpdatedMinutes(int(age / time.Minute))
}

func problemText(m locales.Messages, problem Problem) string {
	switch problem {
	case ProblemNameRequired:
		return m.MonitoringProblemNameRequired()
	case ProblemDuplicateName:
		return m.MonitoringProblemDuplicateName()
	case ProblemSave:
		return m.MonitoringProblemSave()
	case ProblemLoad:
		return m.MonitoringProblemLoad()
	case ProblemTooManyHosts:
		return m.MonitoringProblemTooManyHosts()
	case ProblemNoPanel:
		return m.MonitoringProblemNoPanel()
	case ProblemOpenTerminal:
		return m.MonitoringProblemOpenTerminal()
	case ProblemInternal:
		return m.MonitoringProblemInternal()
	default:
		return ""
	}
}
