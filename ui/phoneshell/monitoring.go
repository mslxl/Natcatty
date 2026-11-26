// Phone Monitoring.
//
// The Monitoring section is the phone's multi-Host monitoring surface and its
// own bottom-bar entry. It reuses application/monitoring — the same state,
// panel demand, sampling lifecycle, and snapshot the desktop UI renders — so a
// Host sampled here takes the same Connection Manager route and reports the
// same typed state. The phone keeps its panel in memory: focusing a Host from
// Host detail or adding one on this page never rewrites the saved desktop
// panel document.
//
// Sampling follows the page. The shell calls activate when Monitoring is the
// visible section and deactivate when it is not, on a tab switch, on
// navigation away, and when the app leaves the foreground.

package phoneshell

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"

	appmonitoring "github.com/mslxl/natcatty/application/monitoring"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// MonitoringService is the shared monitoring use-case seam. The application
// monitor service implements it; tests provide a fake.
type MonitoringService = appmonitoring.Service

// MonitoringProvider is the phone Session runtime's monitoring seam. The Shell
// takes the service from the Session opener when the composition root did not
// pass one explicitly, which keeps the phone root's wiring to one option.
type MonitoringProvider interface {
	MonitoringService() MonitoringService
}

// WithMonitoring gives the shell the shared monitoring runtime. Without it the
// Monitoring section states that monitoring is unavailable and Host detail
// hides its View Monitoring action; no other page is affected.
func WithMonitoring(service MonitoringService) Option {
	return func(s *Shell) { s.monitoringService = service }
}

// Phone monitoring cadences and trend windows. The cadences include 15 s even
// though the desktop toolbar does not: the phone's own page owns the choice,
// and the shared state samples at it without rewriting a saved panel.
var (
	phoneMonitorIntervals = []int{1, 5, 15}
	phoneMonitorWindows   = []int{5, 15, 30}
)

// monitoringPages is the Monitoring section's page state. The shared State
// owns panels, demand, snapshots, and status; these fields are view choices and
// the page's own sampling intent.
type monitoringPages struct {
	service MonitoringService
	state   *appmonitoring.State

	active bool
	// stopped is the user's Stop all choice. Entering the page resumes a panel
	// the user started, but never restarts one the user explicitly stopped.
	stopped bool
	// interval and window are the phone's own toolbar choices, so a cadence
	// the saved panel does not carry still works and is never persisted.
	interval int
	window   int
	charts   bool
	// picking shows the Add Host list instead of the metrics.
	picking bool
	// focus is the Host the user came for, highlighted in the list.
	focus string
	// problem is a local failure, such as a Host that cannot be added.
	problem string
}

// newMonitoringPages builds the page over the shared runtime. A nil service
// leaves the page in its unavailable state; the shell still opens.
func newMonitoringPages(service MonitoringService, hosts appmonitoring.Hosts) *monitoringPages {
	page := &monitoringPages{
		service:  service,
		interval: domainpreferences.DefaultMonitorInterval,
		window:   15,
	}
	if service == nil {
		return page
	}
	page.state = appmonitoring.NewEphemeral(service, hosts, nil,
		appmonitoring.WithRedrawRequest(invalidatePhoneWindows))
	page.state.SetSamplingInterval(page.interval)
	return page
}

// available reports whether this build wired the monitoring runtime.
func (m *monitoringPages) available() bool {
	return m != nil && m.state != nil
}

// reload resolves the Vault Hosts without starting sampling, so Host detail
// can focus a Host before the first Monitoring frame.
func (m *monitoringPages) reload() {
	if m.available() {
		m.state.Reload()
	}
}

// activate applies the page-is-visible rule. Returning to the page resumes a
// panel the user started; the first visit starts one once it has a Host.
func (m *monitoringPages) activate() {
	if !m.available() || m.active {
		return
	}
	m.active = true
	m.state.Activate()
	if m.stopped {
		return
	}
	if len(m.state.DisplayHosts()) > 0 && !m.state.Started() {
		m.state.Start()
	}
}

// deactivate pauses sampling but keeps the user's start intent, so activate
// resumes it.
func (m *monitoringPages) deactivate() {
	if !m.available() || !m.active {
		return
	}
	m.active = false
	m.state.Deactivate()
}

// focusHost adds a Host to the in-memory panel and marks it for the page.
// It never writes the saved configuration.
//
// It runs before Select makes the page visible, so a Host created after the
// page's last load — the Host editor's save refreshes only the Hosts page — is
// not yet in the State's Host index. Resolve the live Vault list first;
// Reload also re-reads the ephemeral panel, whose in-memory AddHost changes
// live in the source it reads, so earlier focuses survive.
func (m *monitoringPages) focusHost(host domainvault.Host) {
	if !m.available() {
		return
	}
	m.reload()
	m.focus = string(host.ID)
	m.stopped = false
	if !m.state.AddHost(string(host.ID)) {
		m.problem = "This Host cannot be monitored."
		return
	}
	m.problem = ""
	if m.active && !m.state.Started() {
		m.state.Start()
	}
}

// addHost adds one Host from the picker.
func (m *monitoringPages) addHost(id string) {
	if !m.available() {
		return
	}
	m.stopped = false
	if !m.state.AddHost(id) {
		m.problem = "This Host cannot be monitored."
		return
	}
	m.problem = ""
	if m.active && !m.state.Started() {
		m.state.Start()
	}
}

// stopAll ends sampling and records that the user stopped it. A later visit
// does not restart it until the user adds a Host or starts again.
func (m *monitoringPages) stopAll() {
	if !m.available() {
		return
	}
	m.state.StopAll()
	m.stopped = true
}

// setInterval changes the sampling cadence and resamples a running panel.
func (m *monitoringPages) setInterval(seconds int) {
	if !m.available() {
		return
	}
	m.interval = seconds
	m.state.SetSamplingInterval(seconds)
	if m.active && !m.stopped && !m.state.Started() && len(m.state.DisplayHosts()) > 0 {
		m.state.Start()
	}
}

// setWindow changes the trend window the chart view shows.
func (m *monitoringPages) setWindow(minutes int) { m.window = minutes }

// close stops sampling and releases the runtime. The shared service owns every
// sampler goroutine, so it is closed when the shell ends.
func (m *monitoringPages) close() {
	if !m.available() {
		return
	}
	m.state.Close()
	if closer, ok := m.service.(interface{ Close() }); ok {
		closer.Close()
	}
}

// drawRoot is the Monitoring section's only page.
func (m *monitoringPages) drawRoot(c *ui.Context) {
	if !m.available() {
		ui.Column(c).Fill().Padding(16).Children(func() {
			kit.EmptyHint(c, "Monitoring is not available in this build.")
		})
		return
	}
	m.state.Flush()
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(14).Children(func() {
			if m.picking {
				m.drawPicker(c)
				return
			}
			m.drawToolbar(c)
			if m.problem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, m.problem, nil)
			}
			hosts := m.focusFirst(m.state.DisplayHosts())
			if len(hosts) == 0 {
				kit.EmptyHint(c, "No Hosts are monitored. Add a Host to begin.")
				return
			}
			for _, host := range hosts {
				m.drawHost(c, host)
			}
		})
	})
}

// focusFirst moves the Host chosen in Host detail to the top of the page, so
// the metrics the user asked for are the first thing shown. It does not change
// the panel's saved order.
func (m *monitoringPages) focusFirst(hosts []domainvault.Host) []domainvault.Host {
	if m.focus == "" {
		return hosts
	}
	for index, host := range hosts {
		if string(host.ID) != m.focus {
			continue
		}
		out := make([]domainvault.Host, 0, len(hosts))
		out = append(out, host)
		out = append(out, hosts[:index]...)
		out = append(out, hosts[index+1:]...)
		return out
	}
	return hosts
}

// drawToolbar is the interval, window, view, and action row.
func (m *monitoringPages) drawToolbar(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		for _, seconds := range phoneMonitorIntervals {
			label := fmt.Sprintf("%d s", seconds)
			var clicked bool
			if seconds == m.interval {
				clicked = kit.PrimaryButton(c, icons.Clock, label).Clicked()
			} else {
				clicked = kit.SecondaryButton(c, icons.Clock, label).Clicked()
			}
			if clicked && seconds != m.interval {
				m.setInterval(seconds)
			}
		}
	})
	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		for _, minutes := range phoneMonitorWindows {
			label := fmt.Sprintf("%d min", minutes)
			var clicked bool
			if minutes == m.window {
				clicked = kit.PrimaryButton(c, icons.Clock, label).Clicked()
			} else {
				clicked = kit.SecondaryButton(c, icons.Clock, label).Clicked()
			}
			if clicked && minutes != m.window {
				m.setWindow(minutes)
			}
		}
	})
	ui.Row(c).FillWidth().Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		listClicked, chartsClicked := false, false
		if m.charts {
			chartsClicked = kit.PrimaryButton(c, icons.Gauge, "Charts").Clicked()
			listClicked = kit.SecondaryButton(c, icons.Clock, "List").Clicked()
		} else {
			listClicked = kit.PrimaryButton(c, icons.Gauge, "List").Clicked()
			chartsClicked = kit.SecondaryButton(c, icons.Clock, "Charts").Clicked()
		}
		if chartsClicked {
			m.charts = true
		}
		if listClicked {
			m.charts = false
		}
		if kit.GhostButton(c, icons.Plus, "Add Host").Clicked() {
			m.picking = true
			m.problem = ""
		}
		if m.state.AnyStarted() {
			if kit.GhostButton(c, icons.Square, "Stop all").Clicked() {
				m.stopAll()
			}
		} else if len(m.state.DisplayHosts()) > 0 {
			if kit.GhostButton(c, icons.Play, "Start").Clicked() {
				m.stopped = false
				m.state.Start()
			}
		}
	})
}

// drawPicker lists the Vault Hosts that can be sampled and are not already in
// the panel.
func (m *monitoringPages) drawPicker(c *ui.Context) {
	if kit.GhostButton(c, icons.ChevronLeft, "Done").Clicked() {
		m.picking = false
		return
	}
	if m.problem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, m.problem, nil)
	}
	count := 0
	for _, host := range m.state.Hosts() {
		if !host.SupportsMonitoring() || m.state.InPanel(string(host.ID)) {
			continue
		}
		count++
		host := host
		row := kit.ListRow(c).Label("Add " + hostName(host))
		row.Children(func() {
			ui.Row(c).Grow(1).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.Lucide(icons.Server)).TextColor(c.Theme().Accent)
				ui.Text(c, hostName(host)).FontWeight(600)
			})
			ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
		})
		if row.Clicked() {
			m.addHost(string(host.ID))
			m.picking = false
		}
	}
	if count == 0 {
		kit.EmptyHint(c, "Every remote Host is already monitored.")
	}
}

// drawHost renders one Host as a list row or a chart card.
func (m *monitoringPages) drawHost(c *ui.Context, host domainvault.Host) {
	id := string(host.ID)
	state := m.state.HostState(id)
	status := m.state.DisplayStatus(id)
	focused := m.focus == id
	if m.charts {
		m.drawChartCard(c, host, state, status, focused)
		return
	}
	m.drawListRow(c, host, state, status, focused)
}

// drawListRow is the compact view: each Host's state and latest values.
func (m *monitoringPages) drawListRow(c *ui.Context, host domainvault.Host,
	state domainmonitor.HostState, status domainmonitor.Status, focused bool) {
	t := c.Theme()
	target := idPage("monitor-host-", string(host.ID))
	box := ui.Column(c.Key(target)).FillWidth().Padding(12, 14).Gap(4).
		Background(t.Surface).Radius(12)
	if focused {
		box = box.Border(1, t.Accent)
	}
	box.Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, hostName(host)).FontWeight(600).SingleLine()
			ui.Spacer(c)
			ui.Text(c, m.statusText(state, status)).FontSize(12).
				TextColor(m.statusColor(t, state, status, t.TextMuted)).SingleLine()
		})
		if status == domainmonitor.StatusLive {
			sample := state.Sample
			ui.Text(c, fmt.Sprintf("CPU %s · Mem %s · Disk %s",
				percentText(sample.CPU), percentText(domainmonitor.MemoryPercent(sample)),
				diskPercentText(sample))).FontSize(12).TextColor(t.TextMuted).SingleLine()
			ui.Text(c, fmt.Sprintf("↓ %s ↑ %s · %s",
				rateText(sample.Rx), rateText(sample.Tx), latencyText(sample.Latency))).
				FontSize(12).TextColor(t.TextMuted).SingleLine()
		}
	})
}

// drawChartCard is the chart view: the latest value and its trend over the
// chosen window.
func (m *monitoringPages) drawChartCard(c *ui.Context, host domainvault.Host,
	state domainmonitor.HostState, status domainmonitor.Status, focused bool) {
	t := c.Theme()
	card := ui.Column(c).FillWidth().Padding(14).Gap(8).
		Background(t.Surface).Radius(12)
	if focused {
		card = card.Border(1, t.Accent)
	}
	card.Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, hostName(host)).FontWeight(600).SingleLine()
			ui.Spacer(c)
			ui.Text(c, m.statusText(state, status)).FontSize(12).
				TextColor(m.statusColor(t, state, status, t.TextMuted)).SingleLine()
		})
		ui.Text(c, fmt.Sprintf("Last %d min", m.window)).FontSize(11).
			TextColor(t.TextMuted)
		points := m.windowPoints(state)
		sample := state.Sample
		m.chartMetric(c, "CPU", percentText(sample.CPU), points,
			func(p domainmonitor.Point) float64 { return p.CPU }, 100, t.Accent)
		m.chartMetric(c, "Memory", percentText(domainmonitor.MemoryPercent(sample)), points,
			func(p domainmonitor.Point) float64 { return p.Memory }, 100, t.Warning)
		m.chartMetric(c, "Disk", diskPercentText(sample), points,
			func(p domainmonitor.Point) float64 { return p.Disk }, 100, t.TextMuted)
		m.chartMetric(c, "Network ↓", rateText(sample.Rx), points,
			func(p domainmonitor.Point) float64 { return p.Rx }, rateScale(points,
				func(p domainmonitor.Point) float64 { return p.Rx }), t.Accent)
	})
}

func (m *monitoringPages) chartMetric(c *ui.Context, label, value string,
	points []domainmonitor.Point, pick func(domainmonitor.Point) float64,
	scale float64, color ui.Color) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(70)
		ui.Text(c, value).FontSize(12).FontWeight(600).Width(70)
		sparkline(c, t, points, pick, scale, color)
	})
}

// sparkline draws a metric's recent points in a small fixed-height box.
func sparkline(c *ui.Context, t *ui.Theme, points []domainmonitor.Point,
	pick func(domainmonitor.Point) float64, scale float64, color ui.Color) {
	ui.Box(c).Grow(1).Height(26).Draw(func(p *ui.Painter, r ui.Rect) {
		if len(points) < 2 || scale <= 0 {
			p.Line(r.X, r.Y+r.H/2, r.X+r.W, r.Y+r.H/2, 1, t.Border.Alpha(0.7))
			return
		}
		xs := make([]float32, len(points))
		ys := make([]float32, len(points))
		start := points[0].At
		span := points[len(points)-1].At.Sub(start)
		for index, point := range points {
			if span > 0 {
				xs[index] = float32(point.At.Sub(start)) / float32(span)
			}
			value := pick(point)
			if !domainmonitor.Valid(value) {
				ys[index] = float32(math.NaN())
				continue
			}
			ys[index] = float32(math.Max(0, math.Min(1, value/scale)))
		}
		p.StrokePath(chart.Line(r, xs, ys, chart.CurveMonotone), 1.5, color)
	})
}

// windowPoints trims a Host's history to the current run and trend window.
func (m *monitoringPages) windowPoints(state domainmonitor.HostState) []domainmonitor.Point {
	started := m.state.StartedAt()
	if cutoff := time.Now().Add(-time.Duration(m.window) * time.Minute); cutoff.After(started) {
		started = cutoff
	}
	return domainmonitor.PointsSince(state.Points, started)
}

// statusText localizes the typed state for the phone. An error carries its
// reason so a user can tell a network failure from a refused credential.
func (m *monitoringPages) statusText(state domainmonitor.HostState, status domainmonitor.Status) string {
	switch status {
	case domainmonitor.StatusLive:
		return "Live"
	case domainmonitor.StatusQueued:
		return "Queued"
	case domainmonitor.StatusConnecting:
		return "Connecting"
	case domainmonitor.StatusLogin:
		return "Sign-in required"
	case domainmonitor.StatusError, domainmonitor.StatusUnavailable, domainmonitor.StatusDisconnected:
		if state.Reason != domainmonitor.ReasonNone {
			return "Error (" + string(state.Reason) + ")"
		}
		return "Error"
	case domainmonitor.StatusUnsupported:
		return "Unsupported"
	case domainmonitor.StatusLimit:
		return "Limit reached"
	case appmonitoring.StatusStopped:
		return "Paused"
	case appmonitoring.StatusStale:
		return "Stale"
	}
	return string(status)
}

func (m *monitoringPages) statusColor(t *ui.Theme, state domainmonitor.HostState,
	status domainmonitor.Status, fallback ui.Color) ui.Color {
	switch status {
	case domainmonitor.StatusLive:
		return t.Success
	case domainmonitor.StatusError, domainmonitor.StatusUnavailable,
		domainmonitor.StatusDisconnected, domainmonitor.StatusLogin:
		return t.Danger
	case domainmonitor.StatusConnecting, domainmonitor.StatusQueued,
		appmonitoring.StatusStale:
		return t.Warning
	case domainmonitor.StatusUnsupported, domainmonitor.StatusLimit:
		return t.TextMuted
	}
	return fallback
}

func percentText(value float64) string {
	if !domainmonitor.Valid(value) {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", value)
}

func diskPercentText(sample domainmonitor.Sample) string {
	_, _, percent := domainmonitor.AggregateDisks(sample.Disks)
	return percentText(percent)
}

func rateText(value float64) string {
	if !domainmonitor.Valid(value) || value < 0 {
		return "—"
	}
	switch {
	case value >= 1024*1024:
		return fmt.Sprintf("%.1f MB/s", value/(1024*1024))
	case value >= 1024:
		return fmt.Sprintf("%.0f KB/s", value/1024)
	default:
		return fmt.Sprintf("%.0f B/s", value)
	}
}

func latencyText(value float64) string {
	if !domainmonitor.Valid(value) || value < 0 {
		return "latency —"
	}
	return fmt.Sprintf("%.0f ms", value)
}

func rateScale(points []domainmonitor.Point, pick func(domainmonitor.Point) float64) float64 {
	scale := 0.0
	for _, point := range points {
		value := pick(point)
		if domainmonitor.Valid(value) && value > scale {
			scale = value
		}
	}
	if scale <= 0 {
		return 1
	}
	return scale
}

// idPage keeps element identities unique per Host without exposing the ID as
// visible text.
func idPage(prefix, id string) string { return prefix + id }

// drawHostDetail draws the Hosts/Vault Host detail page and, when monitoring
// is available for this Host, a View Monitoring action below it. The action
// switches to the Monitoring section and focuses this Host without touching a
// saved panel.
func (s *Shell) drawHostDetail(c *ui.Context, id string) {
	host, ok := s.hosts.host(id)
	if !ok || s.monitoring == nil || !s.monitoring.available() ||
		!host.SupportsMonitoring() {
		s.hosts.drawDetail(c, id)
		return
	}
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			s.hosts.drawDetail(c, id)
		})
		ui.Box(c).FillWidth().Padding(8, 16, 16, 16).Children(func() {
			row := kit.ListRow(c).Label("View Monitoring for " + hostName(host))
			row.Children(func() {
				ui.Row(c).Grow(1).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icons.Lucide(icons.Gauge)).TextColor(c.Theme().Accent)
					ui.Text(c, "View Monitoring").FontWeight(600)
				})
				ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
			})
			if row.Clicked() {
				s.OpenMonitoringHost(host)
			}
		})
	})
}

// OpenMonitoringHost opens the Monitoring section and focuses host. The Host
// is added to the page's in-memory panel, so it reports metrics without being
// pre-added to any saved panel.
func (s *Shell) OpenMonitoringHost(host domainvault.Host) {
	if s.monitoring == nil {
		return
	}
	s.monitoring.focusHost(host)
	s.Select(Monitoring)
}
