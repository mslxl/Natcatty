package phoneshell

import (
	"context"
	"sync"
	"testing"
	"time"

	appapplock "github.com/mslxl/natcatty/application/applock"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
)

// fakeMonitoringService records the shared runtime's demand changes and
// publishes a snapshot the page can render. It stands in for
// application/monitor.Service in these behavior tests.
type fakeMonitoringService struct {
	mu        sync.Mutex
	starts    []fakeMonitorStart
	stops     []string
	stopAlls  int
	snapshot  domainmonitor.Snapshot
	listeners map[int]func()
	nextID    int
}

type fakeMonitorStart struct {
	panel    string
	hosts    []string
	interval time.Duration
}

func newFakeMonitoringService() *fakeMonitoringService {
	return &fakeMonitoringService{
		snapshot: domainmonitor.Snapshot{
			Hosts:     make(map[string]domainmonitor.HostState),
			Running:   make(map[string]bool),
			StartedAt: make(map[string]time.Time),
		},
		listeners: make(map[int]func()),
	}
}

func (s *fakeMonitoringService) Start(panelID string, hosts []domainvault.Host, interval time.Duration) error {
	s.mu.Lock()
	ids := make([]string, 0, len(hosts))
	for _, host := range hosts {
		id := string(host.ID)
		ids = append(ids, id)
		s.snapshot.Hosts[id] = liveMonitorState(id)
	}
	s.starts = append(s.starts, fakeMonitorStart{panel: panelID, hosts: ids, interval: interval})
	s.snapshot.Running[panelID] = true
	s.snapshot.StartedAt[panelID] = time.Now()
	notify := s.listeners
	s.mu.Unlock()
	for _, fn := range notify {
		fn()
	}
	return nil
}

func (s *fakeMonitoringService) Stop(panelID string) {
	s.mu.Lock()
	s.stops = append(s.stops, panelID)
	delete(s.snapshot.Running, panelID)
	delete(s.snapshot.StartedAt, panelID)
	notify := s.listeners
	s.mu.Unlock()
	for _, fn := range notify {
		fn()
	}
}

func (s *fakeMonitoringService) StopAll() {
	s.mu.Lock()
	s.stopAlls++
	s.snapshot.Running = make(map[string]bool)
	s.snapshot.StartedAt = make(map[string]time.Time)
	notify := s.listeners
	s.mu.Unlock()
	for _, fn := range notify {
		fn()
	}
}

func (s *fakeMonitoringService) Snapshot() domainmonitor.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := domainmonitor.Snapshot{
		TakenAt:   time.Now(),
		Hosts:     make(map[string]domainmonitor.HostState, len(s.snapshot.Hosts)),
		Running:   make(map[string]bool, len(s.snapshot.Running)),
		StartedAt: make(map[string]time.Time, len(s.snapshot.StartedAt)),
	}
	for id, state := range s.snapshot.Hosts {
		copied.Hosts[id] = state
	}
	for id, running := range s.snapshot.Running {
		copied.Running[id] = running
	}
	for id, at := range s.snapshot.StartedAt {
		copied.StartedAt[id] = at
	}
	return copied
}

func (s *fakeMonitoringService) Subscribe(fn func()) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	s.listeners[id] = fn
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

func (s *fakeMonitoringService) setHostState(id string, state domainmonitor.HostState) {
	s.mu.Lock()
	s.snapshot.Hosts[id] = state
	s.mu.Unlock()
}

func (s *fakeMonitoringService) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.starts)
}

func (s *fakeMonitoringService) lastStart(t *testing.T) fakeMonitorStart {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.starts) == 0 {
		t.Fatal("the runtime was never asked to start sampling")
	}
	return s.starts[len(s.starts)-1]
}

func (s *fakeMonitoringService) stopCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stops)
}

func (s *fakeMonitoringService) stopAllCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopAlls
}

func liveMonitorState(id string) domainmonitor.HostState {
	now := time.Now()
	return domainmonitor.HostState{
		HostID: id,
		Status: domainmonitor.StatusLive,
		At:     now,
		Sample: domainmonitor.Sample{
			At:         now,
			CPU:        12,
			MemUsedMB:  300,
			MemTotalMB: 1000,
			Rx:         2048,
			Tx:         1024,
			Latency:    20,
		},
		Points: []domainmonitor.Point{
			{At: now.Add(-2 * time.Minute), CPU: 10, Memory: 28, Disk: 40},
			{At: now.Add(-time.Minute), CPU: 12, Memory: 30, Disk: 40},
		},
	}
}

func newMonitoringFixture(t *testing.T, service *fakeMonitoringService) *phoneFixture {
	t.Helper()
	fixture := newPhoneFixtureWith(t, phoneHosts(), &fakeOpener{}, WithMonitoring(service))
	t.Cleanup(fixture.shell.Close)
	return fixture
}

// monitoringOpener is the phone Session runtime's provider seam: the shell
// must take the monitoring service from the opener when the composition root
// passes no explicit option.
type monitoringOpener struct {
	*fakeOpener
	service *fakeMonitoringService
}

func (o *monitoringOpener) MonitoringService() MonitoringService { return o.service }

// TestShellTakesMonitoringFromTheSessionOpener covers the one-option phone
// root wiring: the Session opener supplies monitoring when no option does.
func TestShellTakesMonitoringFromTheSessionOpener(t *testing.T) {
	service := newFakeMonitoringService()
	shell := New(
		WithHosts(&fakeHostStore{hosts: []domainvault.Host{{
			ID: "prod", Label: "prod box", Hostname: "prod.example.com", Port: 22,
			Protocol: domainvault.ProtocolSSH,
		}}}),
		WithSessionOpener(&monitoringOpener{fakeOpener: &fakeOpener{}, service: service}),
	)
	defer shell.Close()
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !shell.monitoring.available() {
		t.Fatal("the shell did not take the monitoring service from the Session opener")
	}
}

func selectSection(f *phoneFixture, section Section) {
	f.shell.Select(section)
	f.tester.Frame()
}

// TestMonitoringIsTheThirdTabOfFive checks the bottom-bar entry the tab bar is
// built from: the five sections keep the spec's order and the third entry is
// Monitoring.
func TestMonitoringIsTheThirdTabOfFive(t *testing.T) {
	shell := New()
	want := []string{"Sessions", "Hosts", "Monitoring", "Vault", "Settings"}
	if got := int(sectionCount); got != len(want) {
		t.Fatalf("sectionCount = %d, want %d", got, len(want))
	}
	for index, title := range want {
		if got := shell.Info(Section(index)).Title; got != title {
			t.Errorf("section %d title = %q, want %q", index, got, title)
		}
	}
}

// TestMonitoringSectionHonoursAppLock covers the shared gate: a closed App
// Lock hides the Monitoring page as it hides every other section.
func TestMonitoringSectionHonoursAppLock(t *testing.T) {
	service := newFakeMonitoringService()
	fixture, _, _ := newLockedPhoneFixtureWith(t, &fakeOpener{}, 1, WithMonitoring(service))
	selectSection(fixture, Monitoring)
	m := locales.In("en")
	if !fixture.tester.HasText(m.AppLockUnlock()) {
		t.Fatalf("Monitoring did not show the lock page; texts: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText("No Hosts are monitored") {
		t.Fatalf("Monitoring drew behind the lock; texts: %q", fixture.tester.Texts())
	}
	if got := service.startCount(); got != 0 {
		t.Fatalf("sampling started behind the lock: %d starts", got)
	}
}

// TestMonitoringStopsWhenAppLockCloses covers a page hidden by the lock: an
// idle or manual lock releases sampling, and unlocking resumes on the next
// frame.
func TestMonitoringStopsWhenAppLockCloses(t *testing.T) {
	service := newFakeMonitoringService()
	fixture, lockService, _ := newLockedPhoneFixtureWith(t, &fakeOpener{}, 30, WithMonitoring(service))
	unlockPhone(t, fixture.tester, locales.In("en"), "correct horse")
	selectSection(fixture, Monitoring)

	if err := fixture.tester.Click("Add Host"); err != nil {
		t.Fatalf("open the Host picker: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Add prod box"); err != nil {
		t.Fatalf("add a Host: %v", err)
	}
	fixture.tester.Frame()
	if got := service.startCount(); got != 1 {
		t.Fatalf("starts before locking = %d, want 1", got)
	}
	if err := lockService.Lock(appapplock.ReasonManual); err != nil {
		t.Fatalf("lock App Lock: %v", err)
	}
	fixture.tester.Frame()
	if got := service.stopCount(); got != 1 {
		t.Fatalf("stops after the lock closed = %d, want 1", got)
	}
}

// TestMonitoringSamplesOnlyWhileVisible drives the lifecycle: Host detail
// opens the page and focuses the Host, leaving the section or backgrounding
// pauses, and returning resumes.
func TestMonitoringSamplesOnlyWhileVisible(t *testing.T) {
	service := newFakeMonitoringService()
	fixture := newMonitoringFixture(t, service)

	// Host detail's View Monitoring opens the same page with the Host focused.
	fixture.await(t, "the Hosts list", func() bool { return fixture.tester.HasText("prod box") })
	if err := fixture.tester.Click("Details for prod box"); err != nil {
		t.Fatalf("open Host detail: %v", err)
	}
	fixture.settle()
	selectSection(fixture, Hosts)
	if err := fixture.tester.Click("View Monitoring for prod box"); err != nil {
		t.Fatalf("open Monitoring from Host detail: %v", err)
	}
	fixture.tester.Frame()
	if got := fixture.shell.Selected(); got != Monitoring {
		t.Fatalf("selected section = %v, want Monitoring", got)
	}
	if got := service.startCount(); got != 1 {
		t.Fatalf("starts after opening Monitoring = %d, want 1", got)
	}
	if start := service.lastStart(t); len(start.hosts) != 1 || start.hosts[0] != "prod" {
		t.Fatalf("started Hosts = %v, want [prod]", start.hosts)
	}

	// The hidden page does not sample: leaving stops the demand, and further
	// host frames do not start it again.
	selectSection(fixture, Hosts)
	if got := service.stopCount(); got != 1 {
		t.Fatalf("stops after leaving Monitoring = %d, want 1", got)
	}
	fixture.tester.Frame()
	fixture.tester.Frame()
	if got := service.startCount(); got != 1 {
		t.Fatalf("starts while Monitoring is hidden = %d, want 1", got)
	}

	// Returning resumes the same panel.
	selectSection(fixture, Monitoring)
	if got := service.startCount(); got != 2 {
		t.Fatalf("starts after returning = %d, want 2", got)
	}

	// Backgrounding pauses; foregrounding resumes.
	fixture.shell.Background()
	if got := service.stopCount(); got != 2 {
		t.Fatalf("stops after background = %d, want 2", got)
	}
	fixture.shell.Foreground()
	if got := service.startCount(); got != 3 {
		t.Fatalf("starts after foreground = %d, want 3", got)
	}
}

// TestMonitoringAddHostAndStopAll covers the page's own Host selection and the
// Stop all action, including that a stopped page stays stopped when revisited.
func TestMonitoringAddHostAndStopAll(t *testing.T) {
	service := newFakeMonitoringService()
	fixture := newMonitoringFixture(t, service)
	selectSection(fixture, Monitoring)

	if err := fixture.tester.Click("Add Host"); err != nil {
		t.Fatalf("open the Host picker: %v", err)
	}
	fixture.tester.Frame()
	// A Telnet Host has no metric channel and is never offered.
	if fixture.tester.HasText("Add console") {
		t.Fatalf("the picker offered a Telnet Host; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Add prod box"); err != nil {
		t.Fatalf("add a Host: %v", err)
	}
	fixture.tester.Frame()
	if start := service.lastStart(t); len(start.hosts) != 1 || start.hosts[0] != "prod" {
		t.Fatalf("started Hosts = %v, want [prod]", start.hosts)
	}
	if !fixture.tester.HasText("prod box") {
		t.Fatalf("the added Host is not shown; texts: %q", fixture.tester.Texts())
	}

	if err := fixture.tester.Click("Stop all"); err != nil {
		t.Fatalf("Stop all: %v", err)
	}
	fixture.tester.Frame()
	if got := service.stopAllCount(); got != 1 {
		t.Fatalf("StopAll calls = %d, want 1", got)
	}
	if fixture.tester.HasText("Stop all") {
		t.Fatalf("Stop all still shows while nothing samples; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("Start") {
		t.Fatalf("no way to resume after Stop all; texts: %q", fixture.tester.Texts())
	}

	// A deliberate Stop all is not undone by leaving and returning.
	selectSection(fixture, Hosts)
	selectSection(fixture, Monitoring)
	if got := service.startCount(); got != 1 {
		t.Fatalf("starts after returning to a stopped page = %d, want 1", got)
	}
}

// TestMonitoringIntervalWindowAndViews covers the toolbar: cadence changes
// resample at the new interval, the trend window reaches the chart, and the
// list and chart presentations both render.
func TestMonitoringIntervalWindowAndViews(t *testing.T) {
	service := newFakeMonitoringService()
	fixture := newMonitoringFixture(t, service)
	selectSection(fixture, Monitoring)
	if err := fixture.tester.Click("Add Host"); err != nil {
		t.Fatalf("open the Host picker: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Add prod box"); err != nil {
		t.Fatalf("add a Host: %v", err)
	}
	fixture.tester.Frame()

	if got := service.lastStart(t).interval; got != 5*time.Second {
		t.Fatalf("initial interval = %v, want 5s", got)
	}
	if err := fixture.tester.Click("15 s"); err != nil {
		t.Fatalf("choose a 15 s interval: %v", err)
	}
	fixture.tester.Frame()
	if got := service.lastStart(t).interval; got != 15*time.Second {
		t.Fatalf("interval after choosing 15 s = %v, want 15s", got)
	}
	if err := fixture.tester.Click("1 s"); err != nil {
		t.Fatalf("choose a 1 s interval: %v", err)
	}
	fixture.tester.Frame()
	if got := service.lastStart(t).interval; got != time.Second {
		t.Fatalf("interval after choosing 1 s = %v, want 1s", got)
	}

	// List is the default; it shows current values.
	if !fixture.tester.HasText("CPU 12% · Mem 30% · Disk —") {
		t.Fatalf("list view lacks current metrics; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Charts"); err != nil {
		t.Fatalf("switch to Charts: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("Last 15 min") {
		t.Fatalf("chart view lacks its default window; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("30 min"); err != nil {
		t.Fatalf("choose a 30 min window: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("Last 30 min") {
		t.Fatalf("chart view did not adopt the window; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("List"); err != nil {
		t.Fatalf("switch back to List: %v", err)
	}
	fixture.tester.Frame()
	if fixture.tester.HasText("Last 30 min") {
		t.Fatalf("list view still shows the chart window; texts: %q", fixture.tester.Texts())
	}
}

// TestMonitoringFocusesAHostCreatedAfterTheLastLoad reproduces the verifier's
// flow: the shell loads, the Host editor saves a new SSH Host (which refreshes
// only the Hosts page), and the new Host's detail then opens Monitoring. The
// shared State's Host cache is stale at that point, so focusing the Host must
// resolve the live Vault list first instead of reporting it unmonitorable.
func TestMonitoringFocusesAHostCreatedAfterTheLastLoad(t *testing.T) {
	service := newFakeMonitoringService()
	fixture := newMonitoringFixture(t, service)
	fixture.await(t, "the Hosts list", func() bool { return fixture.tester.HasText("prod box") })

	// The Host editor's save path: the Vault store gains the record, then the
	// Hosts page re-reads its own list. Monitoring is not reloaded here, which
	// is exactly the stale-cache window the detail action used to hit.
	fixture.store.hosts = append(fixture.store.hosts, domainvault.Host{
		ID: "staging", Label: "staging box", Hostname: "staging.example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH,
	})
	if err := fixture.shell.hosts.load(context.Background()); err != nil {
		t.Fatalf("reload the Hosts page after saving a Host: %v", err)
	}
	fixture.tester.Frame()

	if err := fixture.tester.Click("Details for staging box"); err != nil {
		t.Fatalf("open the new Host's detail: %v", err)
	}
	fixture.settle()
	if err := fixture.tester.Click("View Monitoring for staging box"); err != nil {
		t.Fatalf("open Monitoring from the new Host's detail: %v", err)
	}
	fixture.tester.Frame()

	if fixture.tester.HasText("This Host cannot be monitored.") {
		t.Fatalf("Monitoring rejected a Host created after its last load; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("staging box") {
		t.Fatalf("the new Host is not shown as monitored; texts: %q", fixture.tester.Texts())
	}
	if start := service.lastStart(t); len(start.hosts) != 1 || start.hosts[0] != "staging" {
		t.Fatalf("started Hosts = %v, want [staging]", start.hosts)
	}
}

// TestMonitoringShowsTypedHostFailure covers the per-Host error state: an
// error sample is shown with its reason instead of stale metrics.
func TestMonitoringShowsTypedHostFailure(t *testing.T) {
	service := newFakeMonitoringService()
	fixture := newMonitoringFixture(t, service)
	selectSection(fixture, Monitoring)
	if err := fixture.tester.Click("Add Host"); err != nil {
		t.Fatalf("open the Host picker: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Add prod box"); err != nil {
		t.Fatalf("add a Host: %v", err)
	}
	fixture.tester.Frame()

	service.setHostState("prod", domainmonitor.HostState{
		HostID: "prod",
		Status: domainmonitor.StatusError,
		Reason: domainmonitor.ReasonNetwork,
		At:     time.Now(),
	})
	fixture.tester.Frame()
	if !fixture.tester.HasText("Error (network)") {
		t.Fatalf("the Host failure is not shown; texts: %q", fixture.tester.Texts())
	}
}
