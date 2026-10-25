package shell

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	uimonitoring "github.com/mslxl/natcatty/ui/monitoring"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// ticket 44's regression fixture: the monitoring surface inside the real
// shell, with the 232-DIP sidebar taking space from the window. A standalone
// 720-DIP monitoring view has about 300 DIPs more width and hid the clipping
// this test catches.
type monitorLayoutHosts struct{ items []domainvault.Host }

func (h *monitorLayoutHosts) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), h.items...), nil
}

type monitorLayoutPrefs struct {
	mu       sync.Mutex
	settings domainpreferences.Settings
}

func (p *monitorLayoutPrefs) Settings() domainpreferences.Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings
}

func (p *monitorLayoutPrefs) Update(change func(*domainpreferences.Settings)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(&p.settings)
	p.settings.Monitor = domainpreferences.NormalizeMonitor(p.settings.Monitor)
	return nil
}

type monitorLayoutService struct {
	mu       sync.Mutex
	snapshot domainmonitor.Snapshot
}

func (s *monitorLayoutService) Start(string, []domainvault.Host, time.Duration) error { return nil }
func (s *monitorLayoutService) Stop(string)                                           {}
func (s *monitorLayoutService) StopAll()                                              {}
func (s *monitorLayoutService) Subscribe(func()) func()                               { return func() {} }
func (s *monitorLayoutService) Snapshot() domainmonitor.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.snapshot
	out.Hosts = make(map[string]domainmonitor.HostState, len(s.snapshot.Hosts))
	for id, host := range s.snapshot.Hosts {
		out.Hosts[id] = host
	}
	return out
}

func newMonitorLayoutTester(t *testing.T, width, height int) (*ui.Tester, *uimonitoring.State, *state.State) {
	t.Helper()
	now := time.Now()
	hosts := []domainvault.Host{
		{ID: model.HostID("h-live"), Label: "Alpha", Hostname: "alpha.example", Tags: []string{"web"}},
		{ID: model.HostID("h-unsupported"), Label: "Beta", Hostname: "beta.example"},
	}
	states := map[string]domainmonitor.HostState{
		"h-live": {
			HostID: "h-live", Status: domainmonitor.StatusLive, At: now,
			Sample: domainmonitor.Sample{
				At: now, CPU: 42, Memory: 33, Disk: 51,
				Rx: 1 << 20, Tx: 4096, Latency: 12, GPU: 35,
			},
			Points: []domainmonitor.Point{
				{At: now.Add(-4 * time.Second), CPU: 40, Memory: 31, Disk: 50, Rx: 1 << 19, Tx: 2048},
				{At: now, CPU: 42, Memory: 33, Disk: 51, Rx: 1 << 20, Tx: 4096},
			},
		},
		// A refused or unsupported Host has no Sample. Its zero metrics
		// must render as dashes, not as a healthy all-zero row.
		"h-unsupported": {
			HostID: "h-unsupported", Status: domainmonitor.StatusUnsupported,
			Reason: domainmonitor.ReasonOS, At: now,
		},
	}
	return newMonitorLayoutTesterFor(t, width, height, now, hosts, states)
}

// newMonitorLayoutTesterFor builds the shell fixture around the given Hosts
// and monitoring snapshot; a one-Host snapshot puts a degraded card first.
func newMonitorLayoutTesterFor(t *testing.T, width, height int, now time.Time,
	hosts []domainvault.Host, states map[string]domainmonitor.HostState) (*ui.Tester, *uimonitoring.State, *state.State) {
	t.Helper()
	hostIDs := make([]string, 0, len(hosts))
	for _, host := range hosts {
		hostIDs = append(hostIDs, string(host.ID))
	}
	prefs := &monitorLayoutPrefs{}
	prefs.settings.Monitor = domainpreferences.NormalizeMonitor(domainpreferences.Monitor{
		Panels: []domainpreferences.MonitorPanel{{
			ID: "ops", Name: "Operations", HostIDs: hostIDs,
			View: domainpreferences.MonitorViewList, Interval: 5,
			Range: 30, Sort: domainpreferences.MonitorSortManual,
		}},
	})
	service := &monitorLayoutService{snapshot: domainmonitor.Snapshot{
		TakenAt: now,
		Hosts:   states,
		Running: map[string]bool{"ops": true},
		StartedAt: map[string]time.Time{
			"ops": now.Add(-time.Minute),
		},
	}}
	monitorState := uimonitoring.NewState(service, &monitorLayoutHosts{items: hosts}, prefs, nil,
		uimonitoring.WithClock(func() time.Time { return now }))
	shellState := state.NewState(state.WithMonitor(
		uimonitoring.View(monitorState),
		monitorState.Activate,
		monitorState.Deactivate,
	))
	shellState.ShowMonitor()
	t.Cleanup(func() {
		monitorState.Close()
		shellState.Shutdown()
	})
	tester := ui.NewTester(View(shellState), width, height)
	tester.SetLocale("en")
	tester.Frame()
	return tester, monitorState, shellState
}

// TestMonitorConfigurationIsReachableAtMinimumWindow renders the surface
// inside the real shell at the app's enforced 720x480 minimum. It fails
// against the layout that clipped Edit panel and Columns and put the editor's
// action row below the window.
func TestMonitorConfigurationIsReachableAtMinimumWindow(t *testing.T) {
	m := locales.In("en")
	tester, monitorState, _ := newMonitorLayoutTester(t, 720, 480)

	for _, label := range []string{
		m.MonitoringTitle(), m.MonitoringNewPanel(), m.MonitoringEditPanel(),
		m.MonitoringColumns(), m.MonitoringViewList(), m.MonitoringViewCharts(),
		"Operations", "5", m.MonitoringSortManual(),
	} {
		assertMonitorInside(t, tester, 720, 480, label)
	}

	// The typed state of the unsupported Host is visible without horizontal
	// scrolling: it sits beside the label and in the status column.
	assertMonitorInside(t, tester, 720, 480, m.MonitoringStatusUnsupported())
	assertMonitorInside(t, tester, 720, 480, m.MonitoringReasonOS())
	// A Host without a sample names every headline metric with a dash,
	// including the GPU slot in the Host detail line.
	assertMonitorInside(t, tester, 720, 480, "beta.example · "+m.MonitoringGpu()+" —")
	for _, zero := range []string{"0 ms", "0%", "0 B/s"} {
		if tester.HasText(zero) {
			t.Errorf("a Host with no sample still renders %q: %v", zero, tester.Texts())
		}
	}

	if err := tester.Click(m.MonitoringEditPanel()); err != nil {
		t.Fatalf("open the panel editor: %v", err)
	}
	tester.Frame()
	for _, label := range []string{
		m.MonitoringPanelFormEdit(), m.MonitoringName(), m.MonitoringDescription(),
		m.MonitoringSave(), m.MonitoringCancel(), m.MonitoringDeletePanel(),
	} {
		assertMonitorInside(t, tester, 720, 480, label)
	}
	// Hosts live in the scrolling part; at least the first one is reachable.
	if !tester.HasText("Alpha") {
		t.Fatalf("the editor lost the Host list: %v", tester.Texts())
	}
	// Scrolling the form body reaches the Host rows while the pinned actions
	// stay usable.
	tester.Scroll(float32(720)/2, 320, 0, 500)
	tester.Frame()
	assertMonitorInside(t, tester, 720, 480, "Alpha  alpha.example")
	for _, label := range []string{m.MonitoringSave(), m.MonitoringCancel(), m.MonitoringDeletePanel()} {
		assertMonitorInside(t, tester, 720, 480, label)
	}
	if err := tester.Click(m.MonitoringCancel()); err != nil {
		t.Fatalf("close the panel editor after scrolling: %v", err)
	}
	tester.Frame()

	if err := tester.Click(m.MonitoringColumns()); err != nil {
		t.Fatalf("open the columns editor: %v", err)
	}
	tester.Frame()
	done := assertMonitorInside(t, tester, 720, 480, m.MonitoringColumnsDone())
	if err := tester.Click(m.MonitoringColumnsDone()); err != nil {
		t.Fatalf("close the columns editor: %v", err)
	}
	tester.Frame()
	if tester.HasText(m.MonitoringColumnsHint()) {
		t.Fatal("the columns editor stayed open after Done")
	}
	if done.W <= 0 || done.H <= 0 {
		t.Fatalf("Done has no box: %+v", done)
	}

	// The columns editor scrolls to its lower rows while Done stays pinned.
	if err := tester.Click(m.MonitoringColumns()); err != nil {
		t.Fatalf("reopen the columns editor: %v", err)
	}
	tester.Frame()
	// Scroll inside the editor body, below its title row and above the
	// pinned Done row.
	tester.Scroll(float32(720)/2, 320, 0, 600)
	tester.Frame()
	terminal := assertMonitorInside(t, tester, 720, 480, m.MonitoringTerminal()+" · "+m.MonitoringAlwaysVisible())
	if terminal.Y+terminal.H > 480 {
		t.Fatalf("the lower column rows are clipped: %+v", terminal)
	}
	assertMonitorInside(t, tester, 720, 480, m.MonitoringColumnsDone())
	if err := tester.Click(m.MonitoringColumnsDone()); err != nil {
		t.Fatalf("close the columns editor after scrolling: %v", err)
	}
	tester.Frame()

	// Charts keep their content and action inside the card at the minimum.
	monitorState.UpdatePanelView(domainpreferences.MonitorViewCharts)
	tester.Frame()
	// The first card fits the viewport with its Open action; the unsupported
	// Host's card is one vertical scroll below in the single-column narrow
	// grid and must still show its reason and dashes when reached.
	assertMonitorInside(t, tester, 720, 480, m.MonitoringOpen())
	tester.Scroll(float32(720)/2, 400, 0, 600)
	tester.Frame()
	assertMonitorInside(t, tester, 720, 480, m.MonitoringReasonOS())
	assertMonitorInside(t, tester, 720, 480, "—")
	tester.Scroll(float32(720)/2, 400, 0, -600)
	tester.Frame()
	assertMonitorInside(t, tester, 720, 480, m.MonitoringOpen())
	for _, zero := range []string{"0 ms", "0%", "0 B/s"} {
		if tester.HasText(zero) {
			t.Errorf("a chart for a Host with no sample still renders %q: %v", zero, tester.Texts())
		}
	}
}

// TestMonitorChartWithoutSampleFitsItsActionAtMinimumWindow covers the card
// that previously clipped its Open button: a degraded Host adds a reason line
// but has no trend to plot, and its action must still fit at 720x480.
func TestMonitorChartWithoutSampleFitsItsActionAtMinimumWindow(t *testing.T) {
	m := locales.In("en")
	now := time.Now()
	hosts := []domainvault.Host{{ID: model.HostID("h-unsupported"), Label: "Beta", Hostname: "beta.example"}}
	states := map[string]domainmonitor.HostState{
		"h-unsupported": {
			HostID: "h-unsupported", Status: domainmonitor.StatusUnsupported,
			Reason: domainmonitor.ReasonOS, At: now,
		},
	}
	tester, monitorState, _ := newMonitorLayoutTesterFor(t, 720, 480, now, hosts, states)
	monitorState.UpdatePanelView(domainpreferences.MonitorViewCharts)
	tester.Frame()
	for _, text := range []string{
		m.MonitoringReasonOS(), "—", m.MonitoringCpu(), m.MonitoringGpu() + " —",
		m.MonitoringOpen(),
	} {
		assertMonitorInside(t, tester, 720, 480, text)
	}
}

// TestMonitorChartsKeepMetricsWholeAtDefaultWindow proves the bottom metric
// row is not split into a label line and a value line at 1100x720.
func TestMonitorChartsKeepMetricsWholeAtDefaultWindow(t *testing.T) {
	m := locales.In("en")
	tester, _, _ := newMonitorLayoutTester(t, 1100, 720)
	if err := tester.Click(m.MonitoringViewCharts()); err != nil {
		t.Fatalf("open charts: %v", err)
	}
	tester.Frame()
	for _, line := range []string{
		m.MonitoringDisk() + " 51%", m.MonitoringLatency() + " 12 ms", m.MonitoringGpu() + " 35%",
	} {
		rect, ok := tester.Find(line)
		if !ok {
			t.Fatalf("chart metric %q was split or hidden: %v", line, tester.Texts())
		}
		if rect.X < sidebarWidth || rect.X+rect.W > 1100 || rect.Y < 0 || rect.Y+rect.H > 720 {
			t.Fatalf("chart metric %q is clipped: %+v", line, rect)
		}
		if rect.H > 22 {
			t.Fatalf("chart metric %q wrapped onto %.1f DIPs: %+v", line, rect.H, rect)
		}
	}
	assertMonitorInside(t, tester, 1100, 720, m.MonitoringOpen())
}

func assertMonitorInside(t *testing.T, tester *ui.Tester, width, height int, text string) ui.Rect {
	t.Helper()
	rect, ok := tester.Find(text)
	if !ok {
		t.Fatalf("%dx%d does not show %q inside the shell: %v", width, height, text, tester.Texts())
	}
	const epsilon = 0.5
	if rect.X < sidebarWidth-epsilon || rect.Y < -epsilon ||
		rect.X+rect.W > float32(width)+epsilon || rect.Y+rect.H > float32(height)+epsilon {
		t.Fatalf("%q is clipped at %dx%d: box %+v (content starts at %.0f)",
			text, width, height, rect, sidebarWidth)
	}
	if strings.TrimSpace(text) == "" || rect.W <= 0 || rect.H <= 0 {
		t.Fatalf("%q has no visible box: %+v", text, rect)
	}
	return rect
}
