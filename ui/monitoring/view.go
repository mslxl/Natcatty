package monitoring

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

const (
	asideWidth float32 = 380
	cardWidth  float32 = 280
	cardHeight float32 = 392
)

// View returns the monitoring surface, laid out as the original's: a header
// choosing and running a panel, a toolbar shaping it, the Host cards or list,
// and the editor or a Host's detail beside them.
func View(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		s.Flush()
		m := locales.In(c.Locale())
		t := c.Theme()
		window, _ := c.Size()
		aside := s.Editing() || s.DetailID() != ""
		// Below this width the cards beside an open aside would be a sliver:
		// the aside takes the surface instead, as a sheet.
		narrow := window < 1000
		width := asideWidth
		if narrow {
			width = window
		}
		// compact is a surface too narrow for the header's labels on one
		// line: the shell's navigation and an open aside take their share.
		avail := window - 260
		if aside && !narrow {
			avail -= asideWidth
		}
		compact := avail < 640
		ui.Row(c).Fill().MinHeight(0).Children(func() {
			if aside && narrow {
				asidePanel(c, s, m, t, width)
				return
			}
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().Children(func() {
				header(c, s, m, compact)
				panel, ok := s.ActivePanel()
				if ok {
					toolbar(c, s, panel, m, compact)
				}
				if problem := problemText(m, s.Problem()); problem != "" && !s.Editing() {
					ui.Box(c).FillWidth().Padding(0, 20, 8, 20).Children(func() {
						kit.Notice(c, icons.TriangleAlert, t.Danger, problem, func() {
							if kit.IconButton(c, icons.X, m.MonitoringClose()).Clicked() {
								s.DismissProblem()
							}
						})
					})
				}
				ui.Column(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
					switch {
					case !ok:
						emptyPanels(c, s, m)
					case len(s.DisplayHosts()) == 0:
						kit.EmptyState(c, icons.Search, m.MonitoringNoMatches(), "", nil)
					case panel.View == domainpreferences.MonitorViewList:
						hostList(c, s, panel, m, t)
					default:
						hostCards(c, s, panel, m, t)
					}
				})
			})
			if aside {
				asidePanel(c, s, m, t, width)
			}
		})
		if pending, ok := s.PendingDelete(); ok {
			confirmed, cancelled := kit.Confirm(c, true, m.MonitoringDeletePanel(),
				m.MonitoringDeleteConfirm(pending.Name), m.MonitoringDeletePanel(), m.MonitoringCancel())
			switch {
			case confirmed:
				s.ConfirmDeletePanel()
			case cancelled:
				s.CancelDeletePanel()
			}
		}
	}
}

func asidePanel(c *ui.Context, s *State, m locales.Messages, t *ui.Theme, width float32) {
	if s.Editing() {
		editor(c, s, m, t, width)
		return
	}
	detail(c, s, m, t, width)
}

// header is the original's top row: no title, the panel picker with its edit
// and more actions, then Start or Stop and New panel.
func header(c *ui.Context, s *State, m locales.Messages, compact bool) {
	ui.Row(c).FillWidth().Height(56).Padding(0, 20).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
		panels := s.Panels()
		if len(panels) > 0 {
			names := make([]string, 0, len(panels))
			ids := make(map[string]string, len(panels))
			selected := ""
			active, _ := s.ActivePanel()
			for _, panel := range panels {
				names = append(names, panel.Name)
				ids[panel.Name] = panel.ID
				if panel.ID == active.ID {
					selected = panel.Name
				}
			}
			chosen := selected
			ui.Select(c, &chosen, names).Width(220).Shrink(1).MinWidth(110).Label(m.MonitoringChoosePanel())
			if chosen != selected {
				if id, ok := ids[chosen]; ok {
					s.SetPanel(id)
				}
			}
			if kit.IconButton(c, icons.Pencil, m.MonitoringEditPanel()).Clicked() {
				s.BeginEditPanel()
			}
			kit.IconButton(c, icons.Ellipsis, m.MonitoringPanelActions()).Menu(func(menu *ui.Menu) {
				if menu.Item(m.MonitoringDuplicatePanel()).Chosen() {
					s.DuplicatePanel(m.MonitoringCopyName)
				}
				if s.AnyStarted() && menu.Item(m.MonitoringStopAll()).Chosen() {
					s.StopAll()
				}
				menu.Separator()
				if menu.Item(m.MonitoringDeletePanel()).Chosen() {
					s.RequestDeletePanel()
				}
			})
		}
		ui.Spacer(c)
		if len(panels) > 0 {
			if s.Started() {
				if kit.SecondaryButton(c, icons.Square, m.MonitoringStop()).Clicked() {
					s.Stop()
				}
			} else if kit.PrimaryButton(c, icons.Play, m.MonitoringStart()).Clicked() {
				s.Start()
			}
		}
		// With no panels the empty state's own button creates the first one,
		// so the header does not offer a second.
		if len(panels) > 0 {
			newPanel := kit.IconButton
			if !compact {
				newPanel = kit.SecondaryButton
			}
			if newPanel(c, icons.Plus, m.MonitoringNewPanel()).Clicked() {
				s.BeginCreatePanel()
			}
		}
	})
}

// toolbar shapes the displayed panel: its view, a search, the refresh
// cadence, the trend window, and the order. Each change is saved with the
// panel, as in the original.
func toolbar(c *ui.Context, s *State, panel domainpreferences.MonitorPanel, m locales.Messages, compact bool) {
	selectWidth := float32(128)
	if compact {
		selectWidth = 112
	}
	// Two groups that wrap whole: the view and search, then the panel's
	// cadence, window, and order.
	ui.Row(c).FillWidth().Padding(0, 20, 12, 20).Gap(8).AlignItems(ui.Center).Wrap().Shrink(0).Children(func() {
		ui.Row(c).Grow(1).MinWidth(260).Gap(8).AlignItems(ui.Center).Children(func() {
			toolbarView(c, s, panel, m)
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
			toolbarChoices(c, s, panel, m, selectWidth)
		})
	})
}

func toolbarView(c *ui.Context, s *State, panel domainpreferences.MonitorPanel, m locales.Messages) {
	view := 1
	if panel.View == domainpreferences.MonitorViewList {
		view = 0
	}
	if kit.Segmented(c, kit.SegmentedText, &view, "", []kit.Segment{
		{Label: m.MonitoringViewList()}, {Label: m.MonitoringViewCharts()},
	}) {
		if view == 0 {
			s.UpdatePanelView(domainpreferences.MonitorViewList)
		} else {
			s.UpdatePanelView(domainpreferences.MonitorViewCharts)
		}
	}
	search := s.Search()
	ui.SearchField(c, &search).Placeholder(m.MonitoringSearch()).Grow(1).MinWidth(120).MaxWidth(420)
	s.SetSearch(search)
}

func toolbarChoices(c *ui.Context, s *State, panel domainpreferences.MonitorPanel, m locales.Messages, width float32) {
	choice(c, m.MonitoringInterval(), width, IntervalChoices(), panel.Interval, m.MonitoringSeconds, s.UpdatePanelInterval)
	choice(c, m.MonitoringRange(), width, RangeChoices, panel.Range, m.MonitoringMinutes, s.UpdatePanelRange)

	sorts := []string{m.MonitoringSortManual(), m.MonitoringSortName()}
	current := sorts[0]
	if panel.Sort == domainpreferences.MonitorSortName {
		current = sorts[1]
	}
	chosen := current
	ui.Select(c, &chosen, sorts).Width(width).Label(m.MonitoringSort())
	if chosen != current {
		if chosen == sorts[1] {
			s.UpdatePanelSort(domainpreferences.MonitorSortName)
		} else {
			s.UpdatePanelSort(domainpreferences.MonitorSortManual)
		}
	}
}

// choice is a select over numeric choices shown through format.
func choice(c *ui.Context, label string, width float32, choices []int, value int,
	format func(int) string, apply func(int)) {
	labels := make([]string, 0, len(choices))
	byLabel := make(map[string]int, len(choices))
	for _, option := range choices {
		text := format(option)
		labels = append(labels, text)
		byLabel[text] = option
	}
	current := format(value)
	chosen := current
	ui.Select(c, &chosen, labels).Width(width).Label(label)
	if chosen != current {
		if option, ok := byLabel[chosen]; ok {
			apply(option)
		}
	}
}

func emptyPanels(c *ui.Context, s *State, m locales.Messages) {
	ui.Column(c).Grow(1).FillWidth().Justify(ui.Center).Children(func() {
		kit.EmptyState(c, icons.Gauge, m.MonitoringEmptyTitle(), m.MonitoringEmptyBody(), func() {
			if kit.PrimaryButton(c, icons.Plus, m.MonitoringCreateFirst()).Clicked() {
				s.BeginCreatePanel()
			}
		})
	})
}

// hostCards is the charts view: one card per Host, as many per row as fit.
func hostCards(c *ui.Context, s *State, panel domainpreferences.MonitorPanel, m locales.Messages, t *ui.Theme) {
	hosts := s.DisplayHosts()
	now := s.Now()
	started := s.StartedAt()
	ui.GridView(c, &s.grid, len(hosts), cardWidth, cardHeight, func(index int) {
		if index < 0 || index >= len(hosts) {
			return
		}
		ui.Box(c).Fill().Padding(6).Children(func() {
			hostCard(c, s, m, t, hosts[index], panel, started, now)
		})
	}).Grow(1).FillWidth().Padding(0, 14, 14, 14)
}

func hostCard(c *ui.Context, s *State, m locales.Messages, t *ui.Theme,
	host domainvault.Host, panel domainpreferences.MonitorPanel, started, now time.Time) {
	id := string(host.ID)
	state := s.HostState(id)
	status := s.DisplayStatus(id)
	ready := sampleReady(state)
	sample := state.Sample
	points := chartPoints(state.Points, started, panel.Range, now)

	card := kit.Card(c, status == domainmonitor.StatusLive).Fill().Padding(16).Gap(12).Radius(12).Clip()
	if status != domainmonitor.StatusLive && status != StatusStale {
		card.Opacity(0.72)
	}
	if status == domainmonitor.StatusLive {
		card.Cursor(ui.CursorPointer)
		if card.Clicked() {
			s.OpenDetail(id)
		}
	}
	card.ContextMenu(func(menu *ui.Menu) {
		if menu.Item(m.MonitoringOpenTerminal()).Chosen() {
			s.OpenHost(id)
		}
	})
	card.Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			kit.HostAvatar(c, "", avatarKind(host.Protocol), s.HostLabel(host), 36)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, s.HostLabel(host)).FontWeight(600).SingleLine()
				ui.Text(c, host.Hostname).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).SingleLine()
			})
			if kit.IconButton(c, icons.SquareTerminal, m.MonitoringOpenTerminal()).Clicked() {
				s.OpenHost(id)
			}
		})
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			signalBars(c, t, unknownUnless(ready, sample.Latency))
			tagBadges(c, host.Tags)
			ui.Spacer(c)
			statusBadge(c, m, t, status, state.Reason)
		})
		ui.Row(c).FillWidth().Gap(12).Children(func() {
			metricBlock(c, t, m.MonitoringCpu(), unknownUnless(ready, sample.CPU), cpuSub(m, ready, sample),
				points, func(p domainmonitor.Point) float64 { return p.CPU }, 100, uitheme.SeriesCPU)
			metricBlock(c, t, m.MonitoringMemory(), unknownUnless(ready, sample.Memory), memorySub(ready, sample),
				points, func(p domainmonitor.Point) float64 { return p.Memory }, 100, uitheme.SeriesMemory)
		})
		partitions(c, m, t, ready, sample, 2)
		ui.Row(c).FillWidth().Gap(14).AlignItems(ui.Center).Children(func() {
			rate(c, t, icons.ArrowDown, uitheme.SeriesCPU, unknownUnless(ready, sample.Rx))
			rate(c, t, icons.ArrowUp, uitheme.SeriesUpload, unknownUnless(ready, sample.Tx))
		})
		ui.Spacer(c)
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Padding(8, 0, 0, 0).
			BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, host.Notes).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0)
			if ready {
				ui.Row(c).Gap(4).AlignItems(ui.Center).TextColor(t.TextMuted).Children(func() {
					kit.IconSized(c, icons.Clock, kit.M(c).Icon.Small)
					ui.Text(c, sample.At.Format("15:04:05")).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).NoWrap()
				})
			} else if reason := reasonText(m, state.Reason); reason != "" {
				ui.Text(c, reason).FontSize(kit.M(c).Font.Caption).TextColor(uitheme.DangerText(t)).NoWrap()
			} else if status != StatusStopped {
				ui.Text(c, m.MonitoringWaiting()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).NoWrap()
			}
		})
	})
}

// metricBlock is a card's half: the metric's name, its value in large type,
// a detail line, and its trend.
func metricBlock(c *ui.Context, t *ui.Theme, label string, value float64, sub string,
	points []domainmonitor.Point, pick func(domainmonitor.Point) float64, scale float64, color ui.Color) {
	ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(2).Children(func() {
		ui.Text(c, label).FontSize(kit.M(c).Font.Caption).FontWeight(500).TextColor(t.TextMuted).SingleLine()
		ui.Text(c, percentText(value)).FontSize(kit.M(c).Font.LargeTitle).FontWeight(600).
			TextColor(toneColor(t, domainmonitor.ToneForPercent(value), t.Text)).SingleLine()
		ui.Text(c, sub).FontSize(kit.M(c).Font.Caption2).TextColor(t.TextMuted).SingleLine()
		trend(c, points, pick, scale, color, 56).Margin(4, 0, 0, 0)
	})
}

// trend draws a metric's recent values over faint gridlines, ending in a dot
// at the latest reading. The kit's Sparkline owns the geometry; monitoring
// supplies its series color, the caller's scale, and the samples' times.
func trend(c *ui.Context, points []domainmonitor.Point,
	pick func(domainmonitor.Point) float64, scale float64, color ui.Color, height float32) ui.Element {
	opts := kit.SparklineOptions{
		Height:    height,
		Max:       scale,
		Gridlines: true,
		EndDot:    true,
		Color:     color,
		NoInset:   true,
	}
	if len(points) < 2 || scale <= 0 {
		return kit.Sparkline(c, nil, opts)
	}
	values := make([]float64, len(points))
	times := make([]time.Time, len(points))
	for index, point := range points {
		values[index] = pick(point)
		times[index] = point.At
	}
	opts.Times = times
	return kit.Sparkline(c, values, opts)
}

// partitions lists up to limit mounts with their usage bars; limit 0 lists
// them all.
func partitions(c *ui.Context, m locales.Messages, t *ui.Theme, ready bool, sample domainmonitor.Sample, limit int) {
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).TextColor(t.TextMuted).Children(func() {
			kit.IconSized(c, icons.HardDrive, kit.M(c).Icon.Small)
			ui.Text(c, m.MonitoringPartitions()).FontSize(kit.M(c).Font.Caption).FontWeight(500).TextColor(t.TextMuted)
		})
		disks := sample.Disks
		if !ready || len(disks) == 0 {
			resourceBar(c, t, "/", math.NaN(), "")
			return
		}
		if limit > 0 && len(disks) > limit {
			disks = disks[:limit]
		}
		for index, disk := range disks {
			capacity := m.MonitoringDiskCapacityUnknown()
			if disk.TotalGB > 0 {
				capacity = m.MonitoringDiskCapacity(gbText(disk.UsedGB), gbText(disk.TotalGB))
			}
			ui.Column(c).Key("disk-" + strconv.Itoa(index)).FillWidth().Children(func() {
				resourceBar(c, t, disk.MountPoint, disk.Percent, capacity)
			})
		}
	})
}

// resourceBar is a labelled 4-DIP usage bar, red past 85% and amber past 60%.
func resourceBar(c *ui.Context, t *ui.Theme, label string, percent float64, detail string) {
	ui.Column(c).FillWidth().Gap(3).Children(func() {
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, label).FontSize(kit.M(c).Font.Caption).SingleLine().Grow(1).MinWidth(0)
			ui.Text(c, percentText(percent)).FontSize(kit.M(c).Font.Caption2).TextColor(t.TextMuted).NoWrap()
		})
		fill := t.Accent
		switch {
		case !domainmonitor.Valid(percent):
		case percent > 85:
			fill = t.Danger
		case percent > 60:
			fill = uitheme.MonitorAttention
		}
		ui.Box(c).FillWidth().Height(4).Radius(2).Background(kit.Subtle(t)).Draw(func(p *ui.Painter, r ui.Rect) {
			if !domainmonitor.Valid(percent) {
				return
			}
			w := r.W * float32(math.Max(0, math.Min(100, percent))/100)
			p.Fill(ui.Rect{X: r.X, Y: r.Y, W: w, H: r.H}, fill, 2)
		})
		if detail != "" {
			ui.Text(c, detail).FontSize(kit.M(c).Font.Caption2).TextColor(t.TextMuted).SingleLine()
		}
	})
}

func rate(c *ui.Context, t *ui.Theme, icon string, color ui.Color, value float64) {
	ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
		kit.IconSized(c, icon, kit.M(c).Icon.Small).TextColor(color)
		ui.Text(c, rateText(value)).FontSize(kit.M(c).Font.Footnote).TextColor(t.Text).NoWrap()
	})
}

// signalBars shows latency as four rising bars, fewer lit as it grows.
func signalBars(c *ui.Context, t *ui.Theme, latency float64) {
	lit := 0
	switch {
	case !domainmonitor.Valid(latency):
	case latency < 80:
		lit = 4
	case latency < 150:
		lit = 3
	case latency < 300:
		lit = 2
	default:
		lit = 1
	}
	color := toneColor(t, domainmonitor.ToneForLatency(latency), uitheme.SeriesMemory)
	ui.Row(c).Gap(4).AlignItems(ui.Center).Tooltip(latencyText(latency)).Children(func() {
		ui.Row(c).Gap(2).AlignItems(ui.End).Height(13).Children(func() {
			for bar := 0; bar < 4; bar++ {
				fill := kit.Strong(t)
				if bar < lit {
					fill = color
				}
				ui.Box(c).Width(3).Height(float32(4 + 3*bar)).Radius(1).Background(fill)
			}
		})
		ui.Text(c, latencyText(latency)).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).NoWrap()
	})
}

func tagBadges(c *ui.Context, tags []string) {
	if len(tags) == 0 {
		return
	}
	kit.Pill(c, tags[0])
	if len(tags) > 1 {
		kit.Pill(c, "+"+strconv.Itoa(len(tags)-1))
	}
}

func statusBadge(c *ui.Context, m locales.Messages, t *ui.Theme, status domainmonitor.Status, reason domainmonitor.Reason) {
	text, color := statusText(m, t, status)
	tip := reasonText(m, reason)
	badge := kit.Badge(c, color, text)
	if tip != "" {
		badge.Tooltip(tip)
	}
}

// hostList is the list view: a header of the visible columns, whose context
// menu chooses them and whose edges resize them, over one row per Host.
func hostList(c *ui.Context, s *State, panel domainpreferences.MonitorPanel, m locales.Messages, t *ui.Theme) {
	hosts := s.DisplayHosts()
	keys := visibleColumns(s)
	now := s.Now()
	started := s.StartedAt()
	var total float32 = 24
	for _, key := range keys {
		total += s.ColumnWidth(key)
	}
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.ScrollHorizontal(c).FillWidth().Children(func() {
			ui.Column(c).FillWidth().MinWidth(total+40).Padding(0, 20, 20, 20).Gap(6).Children(func() {
				ui.Row(c).FillWidth().Height(32).Padding(0, 12).AlignItems(ui.Center).
					ContextMenu(func(menu *ui.Menu) {
						menu.Item(m.MonitoringVisibleColumns()).Disabled(true)
						menu.Separator()
						for _, key := range ColumnKeys {
							if key == "host" || key == "terminal" {
								continue
							}
							if menu.Item(columnTitle(m, key)).Checked(!s.Hidden(key)).Chosen() {
								s.SetColumnVisible(key, s.Hidden(key))
							}
						}
					}).
					Children(func() {
						for _, key := range keys {
							columnHeader(c, s, m, t, key)
						}
					})
				for _, host := range hosts {
					id := string(host.ID)
					state := s.HostState(id)
					status := s.DisplayStatus(id)
					row := ui.Row(c).Key("host-"+id).FillWidth().MinHeight(52).Padding(8, 12).
						AlignItems(ui.Center).Radius(kit.M(c).Radius.Card).Background(t.Surface).Border(1, t.Border)
					if row.Hovered() {
						row.Border(1, t.Border.Mix(t.Text, 0.18))
					}
					if status == domainmonitor.StatusLive && row.Clicked() {
						s.OpenDetail(id)
					}
					row.ContextMenu(func(menu *ui.Menu) {
						if menu.Item(m.MonitoringOpenTerminal()).Chosen() {
							s.OpenHost(id)
						}
					})
					row.Children(func() {
						for _, key := range keys {
							cellBox(c, s, key).Children(func() {
								cell(c, s, m, t, key, host, state, status, panel, started, now)
							})
						}
					})
				}
			})
		})
	})
}

func cellBox(c *ui.Context, s *State, key string) ui.Element {
	box := ui.Row(c).Width(s.ColumnWidth(key)).MinWidth(0).Shrink(0).Padding(0, 6).AlignItems(ui.Center).Clip()
	if key == "host" {
		box.Grow(1)
	}
	return box
}

func columnHeader(c *ui.Context, s *State, m locales.Messages, t *ui.Theme, key string) {
	cellBox(c, s, key).Padding(0, 0, 0, 6).Children(func() {
		ui.Text(c, columnTitle(m, key)).FontSize(kit.M(c).Font.Caption).FontWeight(600).TextColor(t.TextMuted).
			SingleLine().Grow(1).MinWidth(0)
		if key == "terminal" {
			return
		}
		edge := ui.Box(c).Width(6).FillHeight().Height(20).Cursor(ui.CursorResizeColumn)
		if edge.Hovered() || edge.Dragging() {
			edge.Background(t.Accent.Alpha(0.35)).Radius(2)
		}
		if dx, _, ok := edge.Dragged(); ok {
			s.DragColumn(key, dx)
		}
		if _, _, ended := edge.DragEnded(); ended {
			s.EndColumnDrag(key)
		}
		if edge.DoubleClicked() {
			s.ResetColumnWidth(key)
		}
	})
}

func cell(c *ui.Context, s *State, m locales.Messages, t *ui.Theme, key string,
	host domainvault.Host, state domainmonitor.HostState, status domainmonitor.Status,
	panel domainpreferences.MonitorPanel, started, now time.Time) {
	ready := sampleReady(state)
	sample := state.Sample
	switch key {
	case "host":
		kit.HostAvatar(c, "", avatarKind(host.Protocol), s.HostLabel(host), 28)
		ui.Column(c).Grow(1).MinWidth(0).Gap(0).Margin(0, 0, 0, 8).Children(func() {
			// The state's dot stays beside the name when the Status column
			// is scrolled away or hidden.
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, s.HostLabel(host)).FontWeight(600).SingleLine().MinWidth(0)
				text, color := statusText(m, t, status)
				kit.StatusDot(c, color).Size(6, 6).Tooltip(text)
			})
			ui.Text(c, host.Hostname).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
		})
	case "latency":
		signalBars(c, t, unknownUnless(ready, sample.Latency))
	case "tags":
		ui.Row(c).Gap(4).Clip().Children(func() { tagBadges(c, host.Tags) })
	case "notes":
		ui.Text(c, host.Notes).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).SingleLine()
	case "cpu":
		listMetric(c, t, unknownUnless(ready, sample.CPU), state.Points, started, panel, now,
			func(p domainmonitor.Point) float64 { return p.CPU }, uitheme.SeriesCPU)
	case "memory":
		listMetric(c, t, unknownUnless(ready, sample.Memory), state.Points, started, panel, now,
			func(p domainmonitor.Point) float64 { return p.Memory }, uitheme.SeriesMemory)
	case "disks":
		percent := math.NaN()
		if ready && len(sample.Disks) > 0 {
			percent = sample.Disks[0].Percent
		} else if ready {
			percent = sample.Disk
		}
		ui.Column(c).FillWidth().Children(func() { resourceBar(c, t, diskLabel(sample, ready), percent, "") })
	case "network":
		ui.Column(c).Gap(2).Children(func() {
			rate(c, t, icons.ArrowDown, uitheme.SeriesCPU, unknownUnless(ready, sample.Rx))
			rate(c, t, icons.ArrowUp, uitheme.SeriesUpload, unknownUnless(ready, sample.Tx))
		})
	case "updated":
		text := "--"
		if ready {
			text = sample.At.Format("15:04:05")
		}
		ui.Text(c, text).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).NoWrap()
	case "status":
		statusBadge(c, m, t, status, state.Reason)
	case "terminal":
		if kit.IconButton(c, icons.SquareTerminal, m.MonitoringOpenTerminal()).Clicked() {
			s.OpenHost(string(host.ID))
		}
	}
}

func listMetric(c *ui.Context, t *ui.Theme, value float64, points []domainmonitor.Point,
	started time.Time, panel domainpreferences.MonitorPanel, now time.Time,
	pick func(domainmonitor.Point) float64, color ui.Color) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, percentText(value)).FontSize(kit.M(c).Font.Footnote).FontWeight(500).Width(46).NoWrap().
			TextColor(toneColor(t, domainmonitor.ToneForPercent(value), t.Text))
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			trend(c, chartPoints(points, started, panel.Range, now), pick, 100, color, 24)
		})
	})
}

func diskLabel(sample domainmonitor.Sample, ready bool) string {
	if ready && len(sample.Disks) > 0 && sample.Disks[0].MountPoint != "" {
		return sample.Disks[0].MountPoint
	}
	return "/"
}

// detail is a live Host's side panel: every reading of its last sample.
func detail(c *ui.Context, s *State, m locales.Messages, t *ui.Theme, width float32) {
	id := s.DetailID()
	host, ok := s.Host(id)
	if !ok {
		return
	}
	state := s.HostState(id)
	sample := state.Sample
	ready := sampleReady(state)
	panel, _ := s.ActivePanel()
	points := chartPoints(state.Points, s.StartedAt(), panel.Range, s.Now())
	kit.Panel(c, width, s.HostLabel(host), host.Hostname, m.MonitoringClose(), nil, s.CloseDetail, func() {
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			signalBars(c, t, unknownUnless(ready, sample.Latency))
			ui.Spacer(c)
			statusBadge(c, m, t, s.DisplayStatus(id), state.Reason)
		})
		ui.Row(c).FillWidth().Gap(12).Children(func() {
			metricBlock(c, t, m.MonitoringCpu(), unknownUnless(ready, sample.CPU), cpuSub(m, ready, sample),
				points, func(p domainmonitor.Point) float64 { return p.CPU }, 100, uitheme.SeriesCPU)
			metricBlock(c, t, m.MonitoringMemory(), unknownUnless(ready, sample.Memory), memorySub(ready, sample),
				points, func(p domainmonitor.Point) float64 { return p.Memory }, 100, uitheme.SeriesMemory)
		})
		if ready && domainmonitor.Valid(sample.GPU) {
			kit.FormGroup(c, m.MonitoringGpu(), func() {
				if sample.GPUName != "" {
					ui.Text(c, sample.GPUName).FontSize(kit.M(c).Font.Footnote).SingleLine()
				}
				resourceBar(c, t, m.MonitoringGpuUtil(), sample.GPU, "")
				if sample.GPUMemTotalMB > 0 {
					resourceBar(c, t, m.MonitoringGpuMemory(), sample.GPUMemUsedMB/sample.GPUMemTotalMB*100,
						mbText(sample.GPUMemUsedMB)+" / "+mbText(sample.GPUMemTotalMB))
				}
			})
		}
		kit.FormGroup(c, m.MonitoringNetwork(), func() {
			infoRow(c, t, m.MonitoringDownload(), rateText(unknownUnless(ready, sample.Rx)))
			infoRow(c, t, m.MonitoringUpload(), rateText(unknownUnless(ready, sample.Tx)))
		})
		kit.FormGroup(c, "", func() { partitions(c, m, t, ready, sample, 0) })
		kit.FormGroup(c, m.MonitoringSystem(), func() {
			infoRow(c, t, m.MonitoringHost(), orDash(sample.Hostname))
			infoRow(c, t, "OS", orDash(sample.OSName))
			infoRow(c, t, "Kernel", orDash(sample.KernelRelease))
			infoRow(c, t, m.MonitoringUpdated(), sample.At.Format("15:04:05"))
		})
		if started := s.StartedAt(); !started.IsZero() {
			ui.Text(c, m.MonitoringObserved(int(s.Now().Sub(started)/time.Minute))).
				FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		}
	}, func() {
		if kit.PrimaryButton(c, icons.SquareTerminal, m.MonitoringOpenTerminal()).Clicked() {
			s.OpenHost(id)
		}
	})
}

func infoRow(c *ui.Context, t *ui.Theme, label, value string) {
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, label).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).NoWrap()
		ui.Spacer(c)
		ui.Text(c, value).FontSize(kit.M(c).Font.Footnote).SingleLine().MinWidth(0)
	})
}

// editor is the panel editor beside the cards, as the original's sheet: the
// panel's name and description, its Hosts in their saved order, and its
// default view. Choosing Hosts replaces the form with a searchable picker.
func editor(c *ui.Context, s *State, m locales.Messages, t *ui.Theme, width float32) {
	form := s.Form()
	title := m.MonitoringEditPanel()
	if s.Creating() {
		title = m.MonitoringNewPanel()
	}
	if s.Picking() {
		picker(c, s, m, t, width)
		return
	}
	kit.Panel(c, width, title, "", m.MonitoringClose(), nil, s.CancelEdit, func() {
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, m.MonitoringName()).FontSize(kit.M(c).Font.Footnote).FontWeight(500)
			ui.TextInput(c, &form.Name).FillWidth()
		})
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, m.MonitoringDescription()).FontSize(kit.M(c).Font.Footnote).FontWeight(500)
			ui.TextInput(c, &form.Description).FillWidth()
		})
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.MonitoringPanelHosts()).FontSize(kit.M(c).Font.Footnote).FontWeight(500).Grow(1)
				if kit.SecondaryButton(c, icons.Plus, m.MonitoringSelectedCount(len(form.HostIDs))).Clicked() {
					s.BeginPickHosts()
				}
			})
			ui.Text(c, m.MonitoringSelectionHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
			ids := append([]string(nil), form.HostIDs...)
			for index, hostID := range ids {
				host, known := s.Host(hostID)
				label := hostID
				if known {
					label = s.HostLabel(host)
				}
				first := index == 0
				ui.Row(c).Key("selected-"+hostID).FillWidth().Gap(8).Padding(6, 8).Radius(kit.M(c).Radius.Control).
					Background(kit.Muted(t)).AlignItems(ui.Center).Children(func() {
					kit.HostAvatar(c, "", avatarKind(host.Protocol), label, 24)
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, label).FontSize(kit.M(c).Font.Footnote).FontWeight(500).SingleLine()
						if known && host.Hostname != "" {
							ui.Text(c, host.Hostname).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
						}
					})
					if !first && kit.IconButton(c, icons.ArrowUp, m.MonitoringMoveUp()).Clicked() {
						s.MoveFormHost(hostID, -1)
					}
					if kit.IconButton(c, icons.X, m.MonitoringRemoveHost()).Clicked() {
						s.RemoveFormHost(hostID)
					}
				})
			}
		})
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, m.MonitoringDefaultView()).FontSize(kit.M(c).Font.Footnote).FontWeight(500)
			view := 1
			if form.View == domainpreferences.MonitorViewList {
				view = 0
			}
			if kit.Segmented(c, kit.SegmentedText, &view, "", []kit.Segment{
				{Label: m.MonitoringViewList()}, {Label: m.MonitoringViewCharts()},
			}) {
				form.View = domainpreferences.MonitorViewCharts
				if view == 0 {
					form.View = domainpreferences.MonitorViewList
				}
			}
		})
		ui.Text(c, m.MonitoringSaveHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		if problem := problemText(m, s.Problem()); problem != "" {
			ui.Text(c, problem).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
		}
	}, func() {
		if kit.SecondaryButton(c, "", m.MonitoringCancel()).Clicked() {
			s.CancelEdit()
		}
		if kit.PrimaryButton(c, "", m.MonitoringSave()).Clicked() {
			s.SavePanel()
		}
	})
}

// picker chooses the panel's Hosts from every group of the Vault.
func picker(c *ui.Context, s *State, m locales.Messages, t *ui.Theme, width float32) {
	form := s.Form()
	kit.Panel(c, width, m.MonitoringChooseHosts(), m.MonitoringSelectedCount(len(form.HostIDs)),
		m.MonitoringClose(), nil, s.EndPickHosts, func() {
			ui.SearchField(c, &s.pickerSearch).Placeholder(m.MonitoringSearch()).FillWidth()
			hosts := s.PickerHosts()
			if len(hosts) == 0 {
				ui.Text(c, m.MonitoringNoHostsAvailable()).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
				return
			}
			ui.Column(c).FillWidth().Gap(2).Children(func() {
				for _, host := range hosts {
					id := string(host.ID)
					selected := s.FormHostSelected(id)
					row := ui.Row(c).Key("pick-"+id).FillWidth().Gap(10).Padding(6, 8).
						Radius(kit.M(c).Radius.Control).AlignItems(ui.Center)
					if row.Hovered() {
						row.Background(kit.Subtle(t))
					}
					row.Children(func() {
						if ui.Checkbox(c, &selected, "").Changed() {
							s.ToggleFormHost(id)
						}
						kit.HostAvatar(c, "", avatarKind(host.Protocol), s.HostLabel(host), 24)
						ui.Column(c).Grow(1).MinWidth(0).Children(func() {
							ui.Text(c, s.HostLabel(host)).FontSize(kit.M(c).Font.Footnote).FontWeight(500).SingleLine()
							detail := host.Hostname
							if host.Group != "" {
								detail = host.Group + " · " + detail
							}
							ui.Text(c, detail).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
						})
					})
					if row.Clicked() {
						s.ToggleFormHost(id)
					}
				}
			})
		}, func() {
			if kit.PrimaryButton(c, "", m.MonitoringDone()).Clicked() {
				s.EndPickHosts()
			}
		})
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

func cpuSub(m locales.Messages, ready bool, sample domainmonitor.Sample) string {
	if !ready || sample.CPUCores <= 0 {
		return "--"
	}
	return m.MonitoringCores(sample.CPUCores)
}

func memorySub(ready bool, sample domainmonitor.Sample) string {
	if !ready || sample.MemTotalMB <= 0 {
		return "--"
	}
	return mbText(sample.MemUsedMB) + " / " + mbText(sample.MemTotalMB)
}

func visibleColumns(s *State) []string {
	out := make([]string, 0, len(ColumnKeys))
	for _, key := range ColumnKeys {
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
		return m.MonitoringPartitions()
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

func avatarKind(protocol domainvault.Protocol) kit.AvatarKind {
	switch protocol {
	case domainvault.ProtocolLocal:
		return kit.AvatarLocal
	case domainvault.ProtocolTelnet:
		return kit.AvatarTelnet
	}
	return kit.AvatarServer
}

func statusText(m locales.Messages, t *ui.Theme, status domainmonitor.Status) (string, ui.Color) {
	switch status {
	case StatusStopped:
		return m.MonitoringStatusStopped(), t.TextMuted
	case StatusStale:
		return m.MonitoringStale(), uitheme.MonitorAttention
	case domainmonitor.StatusLive:
		return m.MonitoringStatusLive(), uitheme.SeriesMemory
	case domainmonitor.StatusConnecting:
		return m.MonitoringStatusConnecting(), uitheme.SeriesCPU
	case domainmonitor.StatusQueued:
		return m.MonitoringStatusQueued(), t.TextMuted
	case domainmonitor.StatusLogin:
		return m.MonitoringStatusLogin(), uitheme.DangerText(t)
	case domainmonitor.StatusError:
		return m.MonitoringStatusError(), uitheme.DangerText(t)
	case domainmonitor.StatusUnsupported:
		return m.MonitoringStatusUnsupported(), t.TextMuted
	case domainmonitor.StatusDisconnected:
		return m.MonitoringStatusDisconnected(), uitheme.MonitorAttention
	case domainmonitor.StatusUnavailable:
		return m.MonitoringStatusUnavailable(), uitheme.DangerText(t)
	case domainmonitor.StatusLimit:
		return m.MonitoringStatusLimit(), uitheme.MonitorAttention
	default:
		return m.MonitoringStatusQueued(), t.TextMuted
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

// toneColor colors a value by its tone, using normal for a healthy one.
func toneColor(t *ui.Theme, tone domainmonitor.Tone, normal ui.Color) ui.Color {
	switch tone {
	case domainmonitor.ToneDanger:
		return uitheme.DangerText(t)
	case domainmonitor.ToneWarning:
		return uitheme.WarningText(t)
	case domainmonitor.ToneUnknown:
		return t.TextMuted
	default:
		return normal
	}
}

func percentText(value float64) string {
	if !domainmonitor.Valid(value) {
		return "--"
	}
	return fmt.Sprintf("%.1f%%", value)
}

func latencyText(value float64) string {
	if !domainmonitor.Valid(value) {
		return "--"
	}
	return fmt.Sprintf("%.0f ms", value)
}

func rateText(value float64) string {
	if !domainmonitor.Valid(value) {
		return "--"
	}
	switch {
	case value >= 1<<30:
		return fmt.Sprintf("%.1f GiB/s", value/(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.1f MiB/s", value/(1<<20))
	default:
		return fmt.Sprintf("%.1f KiB/s", value/(1<<10))
	}
}

func mbText(mb float64) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GiB", mb/1024)
	}
	return fmt.Sprintf("%.0f MiB", mb)
}

func gbText(gb float64) string {
	if gb >= 1024 {
		return fmt.Sprintf("%.1f TiB", gb/1024)
	}
	return fmt.Sprintf("%.1f GiB", gb)
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "--"
	}
	return value
}

func problemText(m locales.Messages, problem Problem) string {
	switch problem {
	case ProblemNameRequired:
		return m.MonitoringProblemNameRequired()
	case ProblemHostRequired:
		return m.MonitoringProblemHostRequired()
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
