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
// inside the real shell at the app's enforced 720x480 minimum: the header's
// controls, the editor and its Host picker, and both views must stay inside
// the window.
func TestMonitorConfigurationIsReachableAtMinimumWindow(t *testing.T) {
	m := locales.In("en")
	tester, monitorState, _ := newMonitorLayoutTester(t, 720, 480)

	for _, label := range []string{
		m.MonitoringNewPanel(), m.MonitoringEditPanel(), m.MonitoringPanelActions(),
		m.MonitoringStart(), m.MonitoringViewList(), m.MonitoringViewCharts(),
		m.MonitoringSeconds(5), m.MonitoringSortManual(),
	} {
		assertMonitorInside(t, tester, 720, 480, label)
	}
	// Opening the surface never connects: the Hosts wait for Start.
	if !tester.HasText(m.MonitoringStatusStopped()) {
		t.Fatalf("an unstarted panel does not say so: %v", tester.Texts())
	}
	if err := tester.Click(m.MonitoringStart()); err != nil {
		t.Fatalf("start monitoring: %v", err)
	}
	tester.Frame()
	assertMonitorInside(t, tester, 720, 480, m.MonitoringStop())
	assertMonitorInside(t, tester, 720, 480, "Beta")
	// A Host without a sample names its metrics with dashes, never zeros.
	for _, zero := range []string{"0 ms", "0.0%", "0.0 KiB/s"} {
		if tester.HasText(zero) {
			t.Errorf("a Host with no sample still renders %q: %v", zero, tester.Texts())
		}
	}

	if err := tester.Click(m.MonitoringEditPanel()); err != nil {
		t.Fatalf("open the panel editor: %v", err)
	}
	tester.Frame()
	for _, label := range []string{
		m.MonitoringName(), m.MonitoringDescription(), m.MonitoringSelectedCount(2),
		m.MonitoringSave(), m.MonitoringCancel(), "Alpha",
	} {
		assertMonitorInside(t, tester, 720, 480, label)
	}
	if err := tester.Click(m.MonitoringSelectedCount(2)); err != nil {
		t.Fatalf("open the Host picker: %v", err)
	}
	tester.Frame()
	assertMonitorInside(t, tester, 720, 480, m.MonitoringDone())
	assertMonitorInside(t, tester, 720, 480, "beta.example")
	if err := tester.Click(m.MonitoringDone()); err != nil {
		t.Fatalf("close the Host picker: %v", err)
	}
	tester.Frame()
	if err := tester.Click(m.MonitoringCancel()); err != nil {
		t.Fatalf("close the panel editor: %v", err)
	}
	tester.Frame()
	if tester.HasText(m.MonitoringName()) {
		t.Fatal("the editor stayed open after Cancel")
	}

	// The first card fits the viewport with its terminal action; the
	// unsupported Host's card is one scroll below in the single-column grid
	// and says why it has no readings.
	monitorState.UpdatePanelView(domainpreferences.MonitorViewCharts)
	tester.Frame()
	assertMonitorInside(t, tester, 720, 480, m.MonitoringOpenTerminal())
	tester.Scroll(float32(720)/2, 400, 0, 600)
	tester.Frame()
	assertMonitorInside(t, tester, 720, 480, m.MonitoringReasonOS())
	if !tester.HasText("--") {
		t.Errorf("the unsupported Host's card shows no dashes: %v", tester.Texts())
	}
	for _, zero := range []string{"0 ms", "0.0%", "0.0 KiB/s"} {
		if tester.HasText(zero) {
			t.Errorf("a chart for a Host with no sample still renders %q: %v", zero, tester.Texts())
		}
	}
}

// TestMonitorChartWithoutSampleFitsItsActionAtMinimumWindow covers a
// degraded Host's card: no trend to plot, a reason, and its terminal action
// inside the window at 720x480.
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
	monitorState.Start()
	tester.Frame()
	for _, text := range []string{
		m.MonitoringStatusUnsupported(), "--", m.MonitoringCpu(), m.MonitoringOpenTerminal(),
	} {
		assertMonitorInside(t, tester, 720, 480, text)
	}
	// The card's footer, with the reason, is one scroll down at the minimum.
	tester.Scroll(float32(720)/2, 400, 0, 600)
	tester.Frame()
	assertMonitorInside(t, tester, 720, 480, m.MonitoringReasonOS())
}

// TestMonitorChartsKeepMetricsWholeAtDefaultWindow proves a live card's
// readings show on single lines inside the window at 1100x720.
func TestMonitorChartsKeepMetricsWholeAtDefaultWindow(t *testing.T) {
	m := locales.In("en")
	tester, monitorState, _ := newMonitorLayoutTester(t, 1100, 720)
	monitorState.Start()
	if err := tester.Click(m.MonitoringViewCharts()); err != nil {
		t.Fatalf("open charts: %v", err)
	}
	tester.Frame()
	for _, line := range []string{"42.0%", "33.0%", "12 ms", "1.0 MiB/s", "4.0 KiB/s"} {
		rect, ok := tester.Find(line)
		if !ok {
			t.Fatalf("chart metric %q is hidden: %v", line, tester.Texts())
		}
		if rect.X < sidebarWidth || rect.X+rect.W > 1100 || rect.Y < 0 || rect.Y+rect.H > 720 {
			t.Fatalf("chart metric %q is clipped: %+v", line, rect)
		}
		if rect.H > 30 {
			t.Fatalf("chart metric %q wrapped onto %.1f DIPs: %+v", line, rect.H, rect)
		}
	}
	assertMonitorInside(t, tester, 1100, 720, m.MonitoringOpenTerminal())
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
