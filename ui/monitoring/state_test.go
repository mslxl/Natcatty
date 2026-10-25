package monitoring

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appmonitor "github.com/mslxl/natcatty/application/monitor"
	"github.com/mslxl/natcatty/domain/model"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
)

type fakeHosts struct {
	items []domainvault.Host
	err   error
	calls int
}

func (f *fakeHosts) List(context.Context) ([]domainvault.Host, error) {
	f.calls++
	return append([]domainvault.Host(nil), f.items...), f.err
}

type fakePrefs struct {
	mu       sync.Mutex
	settings domainpreferences.Settings
	updates  int
	err      error
}

func newFakePrefs(monitor domainpreferences.Monitor) *fakePrefs {
	p := &fakePrefs{}
	p.settings.Monitor = domainpreferences.NormalizeMonitor(monitor)
	return p
}

func (p *fakePrefs) Settings() domainpreferences.Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings
}

func (p *fakePrefs) Update(change func(*domainpreferences.Settings)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	change(&p.settings)
	p.settings.Monitor = domainpreferences.NormalizeMonitor(p.settings.Monitor)
	p.updates++
	return nil
}

type fakeStart struct {
	panel    string
	hosts    []domainvault.Host
	interval time.Duration
}

type fakeService struct {
	mu       sync.Mutex
	starts   []fakeStart
	stops    []string
	stopped  int
	snapshot domainmonitor.Snapshot
	startErr error
	closed   bool
}

func newFakeService() *fakeService {
	return &fakeService{snapshot: domainmonitor.Snapshot{
		Hosts:     make(map[string]domainmonitor.HostState),
		Running:   make(map[string]bool),
		StartedAt: make(map[string]time.Time),
	}}
}

func (f *fakeService) Start(panel string, hosts []domainvault.Host, interval time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.starts = append(f.starts, fakeStart{
		panel: panel, hosts: append([]domainvault.Host(nil), hosts...), interval: interval,
	})
	f.snapshot.Running[panel] = true
	return nil
}

func (f *fakeService) Stop(panel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, panel)
	delete(f.snapshot.Running, panel)
}

func (f *fakeService) StopAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
	f.snapshot.Running = make(map[string]bool)
}

func (f *fakeService) Snapshot() domainmonitor.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.snapshot
	out.Hosts = make(map[string]domainmonitor.HostState, len(f.snapshot.Hosts))
	for id, state := range f.snapshot.Hosts {
		state.Points = domainmonitor.ClonePoints(state.Points)
		out.Hosts[id] = state
	}
	return out
}

func (f *fakeService) Subscribe(func()) func() { return func() {} }

func (f *fakeService) lastStart(t *testing.T) fakeStart {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) == 0 {
		t.Fatal("the service was never started")
	}
	return f.starts[len(f.starts)-1]
}

func testHost(id, label, hostname, group string, tags ...string) domainvault.Host {
	return domainvault.Host{
		ID:       model.HostID(id),
		Label:    label,
		Hostname: hostname,
		Group:    group,
		Tags:     tags,
	}
}

func liveSnapshot(now time.Time, hosts ...domainvault.Host) domainmonitor.Snapshot {
	snapshot := domainmonitor.Snapshot{
		TakenAt:   now,
		Hosts:     make(map[string]domainmonitor.HostState, len(hosts)),
		Running:   make(map[string]bool),
		StartedAt: make(map[string]time.Time),
	}
	for index, host := range hosts {
		sample := domainmonitor.Sample{
			At: now, CPU: 42 + float64(index), Memory: 33, Disk: 51,
			Rx: 1 << 20, Tx: 4096, Latency: 12, GPU: 35,
		}
		snapshot.Hosts[string(host.ID)] = domainmonitor.HostState{
			HostID: string(host.ID),
			Status: domainmonitor.StatusLive,
			At:     now,
			Sample: sample,
			Points: []domainmonitor.Point{
				{At: now.Add(-4 * time.Second), CPU: 40, Memory: 31, Disk: 50, Rx: 1 << 19, Tx: 2048},
				{At: now, CPU: sample.CPU, Memory: sample.Memory, Disk: sample.Disk, Rx: sample.Rx, Tx: sample.Tx},
			},
		}
	}
	return snapshot
}

func TestStateLoadsPanelsSearchSortAndPersistsColumns(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	prefs := newFakePrefs(domainpreferences.Monitor{
		Panels: []domainpreferences.MonitorPanel{{
			ID: "ops", Name: "Operations", HostIDs: []string{"h2", "h1"},
			View: domainpreferences.MonitorViewList, Interval: 10,
			Range: 15, Sort: domainpreferences.MonitorSortManual,
		}},
	})
	hosts := &fakeHosts{items: []domainvault.Host{
		testHost("h1", "Alpha", "alpha.example", "prod", "web"),
		testHost("h2", "Beta", "beta.example", "dev", "db"),
	}}
	service := newFakeService()
	state := NewState(service, hosts, prefs, nil, WithClock(func() time.Time { return now }))
	defer state.Close()
	state.Activate()

	if hosts.calls != 1 {
		t.Fatalf("Host list calls = %d, want 1", hosts.calls)
	}
	order := state.DisplayHosts()
	if len(order) != 2 || string(order[0].ID) != "h2" || string(order[1].ID) != "h1" {
		t.Fatalf("manual order = %v, want [h2 h1]", hostIDs(order))
	}
	start := service.lastStart(t)
	if start.panel != "ops" || start.interval != 10*time.Second || len(start.hosts) != 2 {
		t.Fatalf("start = %+v, want panel ops at 10s with two Hosts", start)
	}
	if !state.Running() {
		t.Fatal("the active panel is not running after Activate")
	}

	state.SetSearch("db")
	if got := hostIDs(state.DisplayHosts()); len(got) != 1 || got[0] != "h2" {
		t.Fatalf("search db = %v, want [h2]", got)
	}
	state.SetSearch("alpha")
	if got := hostIDs(state.DisplayHosts()); len(got) != 1 || got[0] != "h1" {
		t.Fatalf("search alpha = %v, want [h1]", got)
	}
	state.SetSearch("")

	state.UpdatePanelSort(domainpreferences.MonitorSortName)
	if got := hostIDs(state.DisplayHosts()); len(got) != 2 || got[0] != "h1" || got[1] != "h2" {
		t.Fatalf("name order = %v, want [h1 h2]", got)
	}
	state.ToggleColumn("cpu")
	state.AdjustColumnWidth("host", 20)

	stored := prefs.Settings().Monitor
	if len(stored.Panels) != 1 || stored.Panels[0].Sort != domainpreferences.MonitorSortName {
		t.Fatalf("persisted panel = %+v, want name sort", stored.Panels)
	}
	if len(stored.HiddenColumns) != 1 || stored.HiddenColumns[0] != "cpu" {
		t.Fatalf("persisted hidden columns = %v, want [cpu]", stored.HiddenColumns)
	}
	if width := stored.ColumnWidths["host"]; width != 240 {
		t.Fatalf("persisted host width = %d, want 240", width)
	}

	reloaded := NewState(service, hosts, prefs, nil, WithClock(func() time.Time { return now }))
	defer reloaded.Close()
	reloaded.Activate()
	if !reloaded.Hidden("cpu") || reloaded.Width("host") != 240 {
		t.Fatalf("reloaded columns: hidden(cpu)=%v host=%d", reloaded.Hidden("cpu"), reloaded.Width("host"))
	}
	if panel, ok := reloaded.ActivePanel(); !ok || panel.Sort != domainpreferences.MonitorSortName {
		t.Fatalf("reloaded panel = %+v, want name sort", panel)
	}
	if got := hostIDs(reloaded.DisplayHosts()); len(got) != 2 || got[0] != "h1" {
		t.Fatalf("reloaded order = %v, want [h1 h2]", got)
	}
}

func TestStateCreatesPanelValidatesAndOpensTerminal(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	prefs := newFakePrefs(domainpreferences.DefaultMonitor())
	hosts := &fakeHosts{items: []domainvault.Host{
		testHost("h1", "Alpha", "alpha.example", "prod"),
		testHost("h2", "Beta", "beta.example", "dev"),
	}}
	service := newFakeService()
	var opened []string
	state := NewState(service, hosts, prefs, func(host domainvault.Host) error {
		opened = append(opened, string(host.ID))
		return nil
	}, WithClock(func() time.Time { return now }))
	defer state.Close()
	state.Activate()

	state.BeginCreatePanel()
	state.Form().Name = "   "
	state.SavePanel()
	if state.Problem() != ProblemNameRequired {
		t.Fatalf("empty name problem = %q, want name_required", state.Problem())
	}
	if !state.Editing() {
		t.Fatal("the editor closed after an invalid save")
	}

	form := state.Form()
	form.ID = "panel-lab"
	form.Name = "Lab"
	form.Description = "  staging  "
	form.Interval = "10"
	form.Range = "15"
	form.View = domainpreferences.MonitorViewCharts
	form.Sort = domainpreferences.MonitorSortName
	state.ToggleFormHost("h1")
	state.ToggleFormHost("h2")
	state.MoveFormHost("h2", -1)
	state.SavePanel()

	if state.Editing() {
		t.Fatal("the editor stayed open after a valid save")
	}
	panel, ok := state.ActivePanel()
	if !ok {
		t.Fatal("the new panel is not active")
	}
	if panel.Name != "Lab" || panel.Description != "staging" ||
		panel.Interval != 10 || panel.Range != 15 ||
		panel.View != domainpreferences.MonitorViewCharts ||
		panel.Sort != domainpreferences.MonitorSortName {
		t.Fatalf("saved panel = %+v", panel)
	}
	if len(panel.HostIDs) != 2 || panel.HostIDs[0] != "h2" || panel.HostIDs[1] != "h1" {
		t.Fatalf("saved Host order = %v, want [h2 h1]", panel.HostIDs)
	}
	start := service.lastStart(t)
	if start.panel != "panel-lab" || start.interval != 10*time.Second {
		t.Fatalf("start = %+v, want panel-lab at 10s", start)
	}
	if len(prefs.Settings().Monitor.Panels) != 1 {
		t.Fatalf("persisted panels = %+v", prefs.Settings().Monitor.Panels)
	}

	state.BeginCreatePanel()
	state.Form().ID = "panel-other"
	state.Form().Name = "lab"
	state.SavePanel()
	if state.Problem() != ProblemDuplicateName {
		t.Fatalf("duplicate name problem = %q, want duplicate_name", state.Problem())
	}
	state.CancelEdit()

	state.OpenHost("h1")
	if len(opened) != 1 || opened[0] != "h1" {
		t.Fatalf("opened = %v, want [h1]", opened)
	}
	state.OpenHost("missing")
	if state.Problem() != ProblemOpenTerminal {
		t.Fatalf("missing Host problem = %q, want open_terminal", state.Problem())
	}
}

func TestStateReportsTooManyHostsAndStopsOnDeactivate(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	prefs := newFakePrefs(domainpreferences.Monitor{
		Panels: []domainpreferences.MonitorPanel{{
			ID: "ops", Name: "Ops", HostIDs: []string{"h1"},
			View: domainpreferences.MonitorViewList, Interval: 5,
			Range: 30, Sort: domainpreferences.MonitorSortManual,
		}},
	})
	service := newFakeService()
	service.startErr = appmonitor.ErrTooManyHosts
	state := NewState(service, &fakeHosts{items: []domainvault.Host{
		testHost("h1", "Alpha", "alpha.example", ""),
	}}, prefs, nil, WithClock(func() time.Time { return now }))
	defer state.Close()
	state.Activate()
	if state.Problem() != ProblemTooManyHosts {
		t.Fatalf("problem = %q, want too_many_hosts", state.Problem())
	}
	if state.Running() {
		t.Fatal("a panel whose start failed reports running")
	}

	service.startErr = nil
	state.Activate()
	if !state.Running() {
		t.Fatal("the panel did not start after the cap cleared")
	}
	state.Deactivate()
	if state.Running() {
		t.Fatal("Deactivate left the panel running")
	}
	service.mu.Lock()
	stops := append([]string(nil), service.stops...)
	service.mu.Unlock()
	if len(stops) == 0 || stops[len(stops)-1] != "ops" {
		t.Fatalf("stops = %v, want ops", stops)
	}
}

func TestViewRendersListChartsEditorAndNarrowWindow(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	hosts := []domainvault.Host{
		testHost("h1", "Alpha", "alpha.example", "prod", "web"),
		testHost("h2", "Beta", "beta.example", "dev", "db"),
	}
	prefs := newFakePrefs(domainpreferences.Monitor{
		Panels: []domainpreferences.MonitorPanel{{
			ID: "ops", Name: "Operations", HostIDs: []string{"h1", "h2"},
			View: domainpreferences.MonitorViewList, Interval: 5,
			Range: 30, Sort: domainpreferences.MonitorSortManual,
		}},
	})
	service := newFakeService()
	service.snapshot = liveSnapshot(now, hosts...)
	service.snapshot.Running["ops"] = true
	service.snapshot.StartedAt["ops"] = now.Add(-time.Minute)

	state := NewState(service, &fakeHosts{items: hosts}, prefs, nil,
		WithClock(func() time.Time { return now }))
	defer state.Close()
	state.Activate()

	m := locales.In("en")
	tester := ui.NewTester(View(state), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	for _, want := range []string{
		m.MonitoringTitle(), "Alpha", "alpha.example", m.MonitoringCpu(),
		m.MonitoringMemory(), m.MonitoringStatusLive(), m.MonitoringOpen(),
		"42%", "12 ms",
	} {
		if !tester.HasText(want) {
			t.Errorf("list view does not show %q; texts: %v", want, tester.Texts())
		}
	}

	// Every header control stays inside the window at both supported sizes
	// and in both writing systems; a Grow child must not push the last
	// button past the right edge.
	localesToCheck := []struct {
		locale  string
		columns string
		title   string
	}{
		{"en", m.MonitoringColumns(), m.MonitoringTitle()},
		{"zh-CN", locales.In("zh-CN").MonitoringColumns(), locales.In("zh-CN").MonitoringTitle()},
	}
	for _, size := range []struct{ width, height int }{{1100, 720}, {720, 480}} {
		for _, check := range localesToCheck {
			tester.SetSize(size.width, size.height)
			tester.SetLocale(check.locale)
			tester.Frame()
			for _, label := range []string{
				check.title,
				locales.In(check.locale).MonitoringNewPanel(),
				locales.In(check.locale).MonitoringEditPanel(),
				check.columns,
			} {
				rect, ok := tester.Find(label)
				if !ok || rect.W <= 0 || rect.H <= 0 {
					t.Fatalf("%dx%d %s: header control %q is missing: %v",
						size.width, size.height, check.locale, label, tester.Texts())
				}
				if rect.X < 0 || rect.Y < 0 ||
					rect.X+rect.W > float32(size.width) ||
					rect.Y+rect.H > float32(size.height) {
					t.Fatalf("%dx%d %s: header control %q is clipped: %+v",
						size.width, size.height, check.locale, label, rect)
				}
			}
		}
	}
	tester.SetSize(1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	state.UpdatePanelView(domainpreferences.MonitorViewCharts)
	tester.Frame()
	for _, want := range []string{"Alpha", "Beta", m.MonitoringCpu(), m.MonitoringNetwork()} {
		if !tester.HasText(want) {
			t.Errorf("charts view does not show %q", want)
		}
	}

	state.BeginCreatePanel()
	state.Form().Name = "Staging"
	tester.Frame()
	if !tester.HasText(m.MonitoringPanelFormCreate()) || !tester.HasText(m.MonitoringSave()) {
		t.Errorf("editor missing; texts: %v", tester.Texts())
	}
	if !tester.HasText("Beta") {
		t.Error("the editor does not list the Vault Hosts")
	}
	state.CancelEdit()

	tester.SetSize(720, 480)
	tester.Frame()
	if !tester.HasText(m.MonitoringTitle()) {
		t.Error("the narrow window lost the surface title")
	}

	tester.SetLocale("zh-CN")
	state.UpdatePanelView(domainpreferences.MonitorViewList)
	tester.Frame()
	if !tester.HasText(locales.In("zh-CN").MonitoringTitle()) {
		t.Errorf("zh-CN title missing; texts: %v", tester.Texts())
	}
}

func hostIDs(hosts []domainvault.Host) []string {
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, string(host.ID))
	}
	return out
}
