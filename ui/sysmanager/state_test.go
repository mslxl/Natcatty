package sysmanager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeHosts struct {
	hosts []domainvault.Host
	err   error
}

func (f fakeHosts) List(context.Context) ([]domainvault.Host, error) {
	return f.hosts, f.err
}

type fakePreferences struct {
	mu       sync.Mutex
	settings domainpreferences.Settings
	err      error
	updates  int
}

func newFakePreferences() *fakePreferences {
	return &fakePreferences{settings: domainpreferences.Defaults()}
}

func (f *fakePreferences) Settings() domainpreferences.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings
}

func (f *fakePreferences) Update(change func(*domainpreferences.Settings)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	change(&f.settings)
	f.updates++
	return nil
}

type startCall struct {
	host     domainvault.Host
	tab      appsysmanager.Tab
	interval time.Duration
}

type fakeService struct {
	mu        sync.Mutex
	snapshot  appsysmanager.Snapshot
	listeners []func()
	starts    []startCall
	stops     int
	refreshes int

	signalCalls   int
	signalPID     int
	signalName    string
	serviceCalls  int
	serviceUnit   string
	serviceAction appsysmanager.ServiceAction
	serviceScope  appsysmanager.Scope

	blockSignal chan struct{}
	started     chan struct{}
	actionErr   error
}

func newFakeService() *fakeService {
	return &fakeService{
		snapshot: appsysmanager.Snapshot{
			HostID: "alpha",
			Active: appsysmanager.TabOverview,
			Tabs:   make(map[appsysmanager.Tab]appsysmanager.TabState),
		},
	}
}

func (f *fakeService) Seed(tab appsysmanager.Tab, state appsysmanager.TabState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot.Tabs[tab] = state
}

func (f *fakeService) Start(host domainvault.Host, tab appsysmanager.Tab, interval time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, startCall{host: host, tab: tab, interval: interval})
	f.snapshot.HostID = string(host.ID)
	f.snapshot.Active = tab
	return nil
}

func (f *fakeService) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
}

func (f *fakeService) Refresh() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
}

func (f *fakeService) Snapshot() appsysmanager.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := f.snapshot
	copied.Tabs = make(map[appsysmanager.Tab]appsysmanager.TabState, len(f.snapshot.Tabs))
	tabCopy := f.snapshot.Tabs
	for tab, state := range tabCopy {
		copied.Tabs[tab] = state
	}
	return copied
}

func (f *fakeService) Subscribe(fn func()) func() {
	f.mu.Lock()
	f.listeners = append(f.listeners, fn)
	f.mu.Unlock()
	return func() {}
}

func (f *fakeService) Signal(ctx context.Context, host domainvault.Host, pid int,
	signal string) (appsysmanager.ActionResult, error) {
	command, err := appsysmanager.SignalCommand(pid, signal)
	if err != nil {
		return appsysmanager.ActionResult{Kind: appsysmanager.ActionSignal}, err
	}
	f.mu.Lock()
	f.signalCalls++
	f.signalPID = pid
	f.signalName = signal
	block := f.blockSignal
	started := f.started
	actionErr := f.actionErr
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return appsysmanager.ActionResult{Kind: appsysmanager.ActionSignal, Command: command}, ctx.Err()
		}
	}
	if actionErr != nil {
		return appsysmanager.ActionResult{Kind: appsysmanager.ActionSignal, Command: command}, actionErr
	}
	if err := ctx.Err(); err != nil {
		return appsysmanager.ActionResult{Kind: appsysmanager.ActionSignal, Command: command}, err
	}
	return appsysmanager.ActionResult{Kind: appsysmanager.ActionSignal, Command: command,
		Stdout: "ok", HasExitCode: true}, nil
}

func (f *fakeService) ServiceAction(ctx context.Context, host domainvault.Host, unit string,
	action appsysmanager.ServiceAction, scope appsysmanager.Scope) (appsysmanager.ActionResult, error) {
	command, err := appsysmanager.ServiceCommand(unit, action, scope)
	if err != nil {
		return appsysmanager.ActionResult{Kind: appsysmanager.ActionServiceAction}, err
	}
	f.mu.Lock()
	f.serviceCalls++
	f.serviceUnit = unit
	f.serviceAction = action
	f.serviceScope = scope
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return appsysmanager.ActionResult{Kind: appsysmanager.ActionServiceAction, Command: command}, err
	}
	return appsysmanager.ActionResult{Kind: appsysmanager.ActionServiceAction, Command: command,
		Stdout: "ok", HasExitCode: true}, nil
}

func (f *fakeService) calls() (signals, services int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.signalCalls, f.serviceCalls
}

func (f *fakeService) lastStart() (startCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) == 0 {
		return startCall{}, false
	}
	return f.starts[len(f.starts)-1], true
}

func demoSnapshot() appsysmanager.Snapshot {
	return appsysmanager.Snapshot{
		HostID: "alpha",
		Active: appsysmanager.TabOverview,
		Tabs: map[appsysmanager.Tab]appsysmanager.TabState{
			appsysmanager.TabOverview: {
				Tab: appsysmanager.TabOverview, Status: appsysmanager.StatusLive,
				At: time.Now(),
				Overview: appsysmanager.Overview{
					Hostname: "alpha.test", OSName: "Debian 12", KernelRelease: "6.1",
					UptimeSeconds: 90061, LoadAverage: []float64{0.5, 0.4, 0.3},
					Cores: 4, CPUPercent: 25, MemUsedMB: 2048, MemTotalMB: 8192,
					SwapUsedMB: 128, SwapTotalMB: 1024, DiskUsedGB: 10, DiskTotalGB: 40,
				},
			},
			appsysmanager.TabProcesses: {
				Tab: appsysmanager.TabProcesses, Status: appsysmanager.StatusLive, At: time.Now(),
				Processes: []appsysmanager.Process{
					{PID: 1, User: "root", CPUPercent: 0.5, MemPercent: 0.1, Command: "init"},
					{PID: 42, User: "app", Stat: "T", CPUPercent: 12.5, MemPercent: 2.5, Command: "worker"},
				},
			},
			appsysmanager.TabPorts: {
				Tab: appsysmanager.TabPorts, Status: appsysmanager.StatusLive, At: time.Now(),
				Ports: []appsysmanager.Port{
					{Protocol: "tcp", Address: "0.0.0.0", Port: 22, PID: 123, ProcessName: "sshd"},
					{Protocol: "udp", Address: "127.0.0.1", Port: 53, PID: 0},
				},
			},
			appsysmanager.TabServices: {
				Tab: appsysmanager.TabServices, Status: appsysmanager.StatusLive, At: time.Now(),
				Services: []appsysmanager.Unit{
					{Name: "broken.service", Scope: "system", ActiveState: "failed"},
					{Name: "nginx.service", Scope: "system", ActiveState: "active", Description: "Web server"},
				},
			},
		},
	}
}

func testHosts() []domainvault.Host {
	return []domainvault.Host{
		{ID: model.HostID("alpha"), Label: "Alpha", Hostname: "alpha.test"},
		{ID: model.HostID("beta"), Label: "Beta", Hostname: "beta.test"},
	}
}

func newTestState(service *fakeService, prefs *fakePreferences) *State {
	return NewState(service, fakeHosts{hosts: testHosts()}, prefs)
}

func TestActivateSelectsTheFirstHostAndStartsTheDefaultCadence(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	state := newTestState(service, newFakePreferences())
	state.Activate()
	if _, ok := state.CurrentHost(); !ok || state.HostID() != "alpha" {
		t.Fatalf("current Host = %q", state.HostID())
	}
	start, ok := service.lastStart()
	if !ok || start.host.ID != "alpha" || start.tab != appsysmanager.TabOverview ||
		start.interval != 3*time.Second {
		t.Fatalf("start = %+v, %v", start, ok)
	}
	state.Flush()
	if got := state.TabState(appsysmanager.TabOverview).Overview.Hostname; got != "alpha.test" {
		t.Errorf("overview = %q", got)
	}
	if len(state.DisplayProcesses()) != 2 || state.DisplayProcesses()[0].PID != 42 {
		t.Errorf("default CPU order = %+v", state.DisplayProcesses())
	}
}

func TestTabSwitchStartsThatTabsCadenceAndSearchFilters(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	prefs := newFakePreferences()
	prefs.settings.SysManager.ServicesInterval = 10
	state := newTestState(service, prefs)
	state.Activate()

	state.SetTab(appsysmanager.TabServices)
	start, _ := service.lastStart()
	if start.tab != appsysmanager.TabServices || start.interval != 10*time.Second {
		t.Fatalf("services start = %+v", start)
	}
	state.Flush()
	if len(state.DisplayServices()) != 2 {
		t.Fatalf("services = %+v", state.DisplayServices())
	}
	state.SetSearch("nginx")
	if rows := state.DisplayServices(); len(rows) != 1 || rows[0].Name != "nginx.service" {
		t.Errorf("filtered services = %+v", rows)
	}
	state.SetSearch("")
	state.SetTab(appsysmanager.TabProcesses)
	state.Flush()
	if rows := state.DisplayProcesses(); len(rows) != 2 || rows[0].PID != 42 {
		t.Errorf("CPU descending = %+v", rows)
	}
	state.sort = ui.SortOrder{Column: "pid", Descending: false}
	state.Prepare()
	if rows := state.DisplayProcesses(); rows[0].PID != 1 || rows[1].PID != 42 {
		t.Errorf("PID ascending = %+v", rows)
	}
	state.sort = ui.SortOrder{Column: "user", Descending: false}
	state.Prepare()
	if rows := state.DisplayProcesses(); rows[0].User != "app" {
		t.Errorf("user ascending = %+v", rows)
	}
}

func TestConfirmationGatesEveryDestructiveAction(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	state := newTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	if err := state.RequestSignal(42, "KILL"); err != nil {
		t.Fatalf("request signal: %v", err)
	}
	pending, ok := state.Pending()
	if !ok || pending.Command != "kill -9 42" || pending.PID != 42 || pending.Signal != "KILL" {
		t.Fatalf("pending = %+v, %v", pending, ok)
	}
	if signals, services := service.calls(); signals != 0 || services != 0 {
		t.Fatalf("a command ran before confirmation: signals=%d services=%d", signals, services)
	}

	state.ConfirmPending()
	waitFor(t, 2*time.Second, "the confirmed signal", func() bool {
		signals, _ := service.calls()
		if signals > 0 {
			state.Flush()
		}
		return signals == 1 && !state.ActionBusy()
	})
	if service.signalPID != 42 || service.signalName != "KILL" {
		t.Errorf("signal target = %d/%s", service.signalPID, service.signalName)
	}
	if _, ok := state.Pending(); ok {
		t.Error("the confirmation stayed open after confirming")
	}
	if result, ok := state.ActionResult(); !ok || result.Command != "kill -9 42" {
		t.Errorf("action result = %+v, %v", result, ok)
	}
	if service.refreshes == 0 {
		t.Error("a successful action did not request a fresh sample")
	}

	if err := state.RequestPortTerminate(appsysmanager.Port{Port: 22, PID: 0}); err == nil {
		t.Error("a port without an owner produced a confirmation")
	}
	port := appsysmanager.Port{Protocol: "tcp", Address: "0.0.0.0", Port: 22, PID: 123}
	if err := state.RequestPortTerminate(port); err != nil {
		t.Fatalf("request port terminate: %v", err)
	}
	pending, ok = state.Pending()
	if !ok || pending.Command != "kill -15 123" || pending.PID != 123 ||
		pending.Signal != "TERM" || pending.Origin != OriginPort ||
		pending.Address != "0.0.0.0" || pending.Port != 22 || pending.Protocol != "tcp" {
		t.Fatalf("port pending = %+v, %v", pending, ok)
	}
	if signals, _ := service.calls(); signals != 1 {
		t.Fatalf("a port command ran before confirmation: signals=%d", signals)
	}
	state.CancelPending()
	state.Flush()
	if signals, _ := service.calls(); signals != 1 {
		t.Fatalf("cancelling the port action ran a command: signals=%d", signals)
	}

	for _, test := range []struct {
		action  appsysmanager.ServiceAction
		command string
	}{
		{appsysmanager.ServiceStart, "systemctl start 'nginx.service'"},
		{appsysmanager.ServiceStop, "systemctl stop 'nginx.service'"},
		{appsysmanager.ServiceRestart, "systemctl restart 'nginx.service'"},
	} {
		if err := state.RequestServiceAction("nginx.service", test.action,
			appsysmanager.ScopeSystem); err != nil {
			t.Fatalf("request service %s: %v", test.action, err)
		}
		pending, ok = state.Pending()
		if !ok || pending.Command != test.command || pending.Unit != "nginx.service" ||
			pending.Action != test.action || pending.Scope != appsysmanager.ScopeSystem {
			t.Fatalf("service %s pending = %+v, %v", test.action, pending, ok)
		}
		if _, services := service.calls(); services != 0 {
			t.Fatalf("service %s ran before confirmation: services=%d", test.action, services)
		}
		state.CancelPending()
		state.Flush()
	}
	if _, ok := state.Pending(); ok {
		t.Error("cancel left a pending confirmation")
	}
	if signals, services := service.calls(); signals != 1 || services != 0 {
		t.Fatalf("cancelled confirmations ran commands: signals=%d services=%d", signals, services)
	}

	state.SetTab(appsysmanager.TabPorts)
	state.Flush()
	if result, ok := state.ActionResult(); ok {
		t.Errorf("a finished action notice survived a tab change: %+v", result)
	}
}

func TestConfirmedServiceActionRunsOffTheUIThread(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	state := newTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	if err := state.RequestServiceAction("nginx.service", appsysmanager.ServiceRestart,
		appsysmanager.ScopeSystem); err != nil {
		t.Fatalf("request: %v", err)
	}
	pending, _ := state.Pending()
	if pending.Command != "systemctl restart 'nginx.service'" ||
		pending.Unit != "nginx.service" {
		t.Fatalf("pending = %+v", pending)
	}
	state.ConfirmPending()
	waitFor(t, 2*time.Second, "the confirmed restart", func() bool {
		_, services := service.calls()
		if services > 0 {
			state.Flush()
		}
		return services == 1 && !state.ActionBusy()
	})
	if service.serviceUnit != "nginx.service" ||
		service.serviceAction != appsysmanager.ServiceRestart ||
		service.serviceScope != appsysmanager.ScopeSystem {
		t.Errorf("service call = %+v", service)
	}
	if state.ActionError() != nil {
		t.Errorf("action error = %v", state.ActionError())
	}
}

func TestCancelStopsARunningAction(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	service.blockSignal = make(chan struct{})
	service.started = make(chan struct{}, 1)
	defer close(service.blockSignal)
	state := newTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	if err := state.RequestSignal(42, "TERM"); err != nil {
		t.Fatalf("request: %v", err)
	}
	state.ConfirmPending()
	select {
	case <-service.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the action goroutine never started")
	}
	state.CancelPending()
	waitFor(t, 2*time.Second, "the cancelled action outcome", func() bool {
		state.Flush()
		return !state.ActionBusy() && state.ActionDone()
	})
	if !errors.Is(state.ActionError(), context.Canceled) {
		t.Errorf("cancel error = %v, want context.Canceled", state.ActionError())
	}
}

func TestSetIntervalPersistsPerTabAndRestartsSampling(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	prefs := newFakePreferences()
	state := newTestState(service, prefs)
	state.Activate()
	state.SetTab(appsysmanager.TabProcesses)
	state.Flush()

	state.SetInterval(10)
	if prefs.updates != 1 {
		t.Errorf("preference updates = %d, want 1", prefs.updates)
	}
	settings := prefs.Settings()
	if settings.SysManager.ProcessesInterval != 10 {
		t.Errorf("persisted processes interval = %d", settings.SysManager.ProcessesInterval)
	}
	if settings.SysManager.OverviewInterval != 3 || settings.SysManager.ServicesInterval != 5 {
		t.Errorf("unrelated intervals changed: %+v", settings.SysManager)
	}
	start, _ := service.lastStart()
	if start.tab != appsysmanager.TabProcesses || start.interval != 10*time.Second {
		t.Errorf("restarted at %+v", start)
	}

	// A fresh surface reads the persisted cadence.
	reloaded := newTestState(service, prefs)
	reloaded.Activate()
	reloaded.SetTab(appsysmanager.TabProcesses)
	start, _ = service.lastStart()
	if start.interval != 10*time.Second {
		t.Errorf("reloaded interval = %s", start.interval)
	}
}

func TestSetIntervalSaveFailureIsReportedAndDoesNotApply(t *testing.T) {
	service := newFakeService()
	prefs := newFakePreferences()
	prefs.err = errors.New("disk full")
	state := newTestState(service, prefs)
	state.Activate()
	state.SetTab(appsysmanager.TabProcesses)
	state.SetInterval(10)
	if state.Problem() != ProblemSave {
		t.Errorf("problem = %q", state.Problem())
	}
	if state.Interval() != 3 {
		t.Errorf("interval applied after a failed save: %d", state.Interval())
	}
}

func TestHostSwitchClearsSelectionAndRetargets(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	state := newTestState(service, newFakePreferences())
	state.Activate()
	state.SetTab(appsysmanager.TabProcesses)
	state.Flush()
	state.selected = 1

	state.SetHost("beta")
	start, _ := service.lastStart()
	if start.host.ID != "beta" || start.tab != appsysmanager.TabProcesses {
		t.Errorf("retarget = %+v", start)
	}
	if state.selected != -1 {
		t.Errorf("selection survived the Host switch: %d", state.selected)
	}
}

func TestDeactivateStopsSamplingAndDiscardsConfirmation(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	state := newTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()
	if err := state.RequestSignal(42, "KILL"); err != nil {
		t.Fatalf("request: %v", err)
	}
	state.Deactivate()
	if service.stops == 0 {
		t.Error("deactivate did not stop sampling")
	}
	if _, ok := state.Pending(); ok {
		t.Error("deactivate left a pending confirmation")
	}
	before, _ := service.calls()
	if before != 0 {
		t.Errorf("a hidden surface ran %d commands", before)
	}
}

// waitFor polls a real condition up to a hard deadline. The 5 ms cadence is
// not a synchronization assumption.
func waitFor(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
