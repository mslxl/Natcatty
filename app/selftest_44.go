//go:build selftest

package app

// Ticket 44 self-test. Press F11, then 4, then 4.
//
// The check runs the real monitoring service and surface against an
// in-process fake Host transport:
//
//   - a panel is created through the production editor state, persisted to the
//     settings seam, and sampling starts;
//   - the Linux metric commands produce live CPU, memory, disk, network,
//     latency, and GPU values, and a second sample appends trend history;
//   - the list and chart surfaces render in English and zh-CN at 1100x720 and
//     720x480, and the captured frames are written beside the result file;
//   - a Host whose connection is refused for its host key becomes a typed
//     "login / host key not trusted" state, and no metric command runs on it;
//   - deactivation stops every sampler and releases every in-process
//     transport.
//
// Every wait observes service state with a hard deadline; there is no sleep.
//
// Result: %TEMP%\natcatty-selftest-44.json and
// %TEMP%\natcatty-selftest-44-shots\*.png.

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appmonitor "github.com/mslxl/natcatty/application/monitor"
	"github.com/mslxl/natcatty/domain/model"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	uimonitoring "github.com/mslxl/natcatty/ui/monitoring"
	"github.com/mslxl/natcatty/ui/selftest"
	uishell "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

const (
	selftest44Ticket   = 44
	selftest44Wait     = 8 * time.Second
	selftest44Panel    = "panel-selftest-44"
	selftest44Interval = 1
)

type selftest44 struct {
	prefs     *selftest44Prefs
	hosts     *selftest44Hosts
	connector *selftest44Connector
	service   *appmonitor.Service
	state     *uimonitoring.State

	configured      bool
	metricsDeadline time.Time
	trustStarted    bool
	trustDeadline   time.Time
	cleanupDeadline time.Time

	// prepared says this run has built its fake Hosts, transport, service, and
	// surface. The harness keeps one registered set of steps, so it is cleared
	// when a run finishes and the next F11,4,4 rebuilds them from
	// resetRunState.
	prepared bool
}

func (t *selftest44) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest44Ticket,
		Name:   "Host monitoring panels, metrics, and trust gating",
		Steps: []selftest.Step{
			{Name: "configure-panel", Run: t.configureStep},
			{Name: "collect-metrics", Run: t.metricsStep},
			{Name: "render-and-capture", Run: t.renderStep},
			{Name: "host-key-gate", Run: t.trustStep},
			{Name: "stop-and-release", Run: t.cleanupStep},
		},
	}
}

// resetRunState rebuilds the fake Hosts, transport, service, and surface and
// clears every per-run flag and deadline so pressing F11,4,4 again in the same
// process starts from the same conditions as the first run. The harness
// registers one set of steps, so without it the second run reuses
// configured=true, the first run's deadlines, and the service that its cleanup
// step closed. It follows the ticket 15 and ticket 45 convention; the fresh
// connector also resets the fake sample counters and the service starts with
// no trend history.
func (t *selftest44) resetRunState() {
	if t.state != nil {
		t.state.Close()
	}
	if t.service != nil {
		t.service.Close()
	}
	t.prefs = &selftest44Prefs{}
	t.prefs.settings.Monitor = domainpreferences.DefaultMonitor()
	t.hosts = &selftest44Hosts{items: []domainvault.Host{
		{
			ID: model.HostID("selftest-44-alpha"), Label: "Alpha",
			Hostname: "alpha.selftest", Group: "selftest", Tags: []string{"web"},
			Notes: "primary",
		},
		{
			ID: model.HostID("selftest-44-beta"), Label: "Beta",
			Hostname: "beta.selftest", Group: "selftest", Tags: []string{"db"},
		},
		{
			ID: model.HostID("selftest-44-denied"), Label: "Denied",
			Hostname: "denied.selftest", Group: "selftest",
		},
	}}
	t.connector = &selftest44Connector{
		connections: make(map[string]*selftest44Connection),
		opens:       make(map[string]int),
		denied:      map[string]bool{"selftest-44-denied": true},
	}
	t.service = appmonitor.NewService(t.connector)
	t.state = uimonitoring.NewState(t.service, t.hosts, t.prefs, nil)

	t.configured = false
	t.metricsDeadline = time.Time{}
	t.trustStarted = false
	t.trustDeadline = time.Time{}
	t.cleanupDeadline = time.Time{}
}

func (t *selftest44) configureStep(*ui.Context) selftest.Outcome {
	if !t.prepared {
		t.resetRunState()
		t.prepared = true
	}
	if !t.configured {
		t.state.Activate()
		if len(t.state.Panels()) != 0 {
			return selftest.Failed(errors.New("the fake settings store already had a panel"))
		}
		t.state.BeginCreatePanel()
		form := t.state.Form()
		form.ID = selftest44Panel
		form.Name = "Self-test panel"
		form.Description = "in-process monitoring proof"
		form.Interval = "1"
		form.Range = "5"
		form.View = domainpreferences.MonitorViewList
		form.Sort = domainpreferences.MonitorSortName
		t.state.ToggleFormHost("selftest-44-alpha")
		t.state.ToggleFormHost("selftest-44-beta")
		t.state.SavePanel()
		if t.state.Editing() {
			return selftest.Failed(fmt.Errorf("the panel editor rejected the valid form: problem %q",
				t.state.Problem()))
		}
		stored := t.prefs.Settings().Monitor
		if len(stored.Panels) != 1 || stored.Panels[0].ID != selftest44Panel ||
			len(stored.Panels[0].HostIDs) != 2 {
			return selftest.Failed(fmt.Errorf("persisted monitor configuration = %+v", stored))
		}
		if !t.state.Running() {
			return selftest.Failed(errors.New("the saved panel did not start sampling"))
		}
		t.configured = true
	}
	return selftest.Passed(
		"the editor saved a two-Host panel to the settings seam and started sampling",
		"uimonitoring.State + domain/preferences.Monitor")
}

func (t *selftest44) metricsStep(*ui.Context) selftest.Outcome {
	if t.metricsDeadline.IsZero() {
		t.metricsDeadline = time.Now().Add(selftest44Wait)
	}
	return t.metrics(t.metricsDeadline)
}

func (t *selftest44) metrics(deadline time.Time) selftest.Outcome {
	t.state.Flush()
	snapshot := t.state.Snapshot()
	for _, id := range []string{"selftest-44-alpha", "selftest-44-beta"} {
		hostState, ok := snapshot.Hosts[id]
		if !ok || hostState.Status != domainmonitor.StatusLive || len(hostState.Points) < 3 {
			return pending(deadline, "three live metric samples with trend history")
		}
		sample := hostState.Sample
		if !domainmonitor.Valid(sample.CPU) || !domainmonitor.Valid(sample.Memory) ||
			!domainmonitor.Valid(sample.Disk) || !domainmonitor.Valid(sample.Rx) ||
			!domainmonitor.Valid(sample.Tx) || !domainmonitor.Valid(sample.Latency) ||
			!domainmonitor.Valid(sample.GPU) {
			return pending(deadline, "every metric to become valid")
		}
	}
	alpha := snapshot.Hosts["selftest-44-alpha"]
	beta := snapshot.Hosts["selftest-44-beta"]
	if alpha.Sample.Hostname != "fake-selftest-44-alpha" ||
		beta.Sample.Hostname != "fake-selftest-44-beta" {
		return selftest.Failed(fmt.Errorf("reported hostnames = %q and %q",
			alpha.Sample.Hostname, beta.Sample.Hostname))
	}
	if alpha.Sample.CPU < 70 || alpha.Sample.CPU > 90 {
		return selftest.Failed(fmt.Errorf("derived CPU = %.1f%%, want the fake's 80%%", alpha.Sample.CPU))
	}
	return selftest.Passed(fmt.Sprintf(
		"two Hosts are live with CPU %.0f%%, memory %.0f%%, disk %.0f%%, RX %.0f B/s, latency %.1f ms, GPU %.0f%%, and %d trend points",
		alpha.Sample.CPU, alpha.Sample.Memory, alpha.Sample.Disk,
		alpha.Sample.Rx, alpha.Sample.Latency, alpha.Sample.GPU,
		len(alpha.Points)),
		"application/monitor.Service fake SSH command runner")
}

// renderStep renders the surface inside the real shell, with the 232-DIP
// sidebar taking space, at both supported sizes and in both languages. A
// standalone monitoring view at 720 DIPs has about 300 DIPs more width and
// cannot see the clipping the fixed layout had at 720x480.
func (t *selftest44) renderStep(*ui.Context) selftest.Outcome {
	if !t.state.Running() {
		return selftest.Failed(errors.New("the panel stopped before the surface could be rendered"))
	}
	directory := filepath.Join(selftestResultDir, "natcatty-selftest-44-shots")
	// The directory is shared across runs and capture names have changed
	// before; clear it so an old frame cannot masquerade as this run's.
	if err := os.RemoveAll(directory); err != nil {
		return selftest.Failed(fmt.Errorf("clear the screenshot directory: %w", err))
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return selftest.Failed(fmt.Errorf("create the screenshot directory: %w", err))
	}
	variants := []struct {
		width, height int
		locale        string
	}{
		{1100, 720, "en"},
		{1100, 720, "zh-CN"},
		{720, 480, "en"},
		{720, 480, "zh-CN"},
	}
	captured := 0
	for _, variant := range variants {
		tester, shellState := t.shellTester(variant.width, variant.height, variant.locale)
		m := locales.In(variant.locale)
		prefix := fmt.Sprintf("monitor-%dx%d-%s", variant.width, variant.height, variant.locale)

		// List: every header control is reachable inside the content column,
		// and the typed live state is visible.
		t.state.UpdatePanelView(domainpreferences.MonitorViewList)
		tester.Frame()
		for _, text := range []string{
			m.MonitoringTitle(), m.MonitoringNewPanel(), m.MonitoringEditPanel(),
			m.MonitoringColumns(), "Alpha", m.MonitoringStatusLive(),
		} {
			if !insideShell(tester, text, variant.width, variant.height) {
				shellState.Shutdown()
				return selftest.Failed(fmt.Errorf("%s list: %q is missing or clipped: %v",
					prefix, text, tester.Texts()))
			}
		}
		if err := capture(tester, filepath.Join(directory, prefix+"-list.png")); err != nil {
			shellState.Shutdown()
			return selftest.Failed(err)
		}
		captured++

		// Panel editor: Save, Cancel, and Delete stay pinned inside the
		// window while the fields and Host list scroll.
		t.state.BeginEditPanel()
		tester.Frame()
		for _, text := range []string{
			m.MonitoringPanelFormEdit(), m.MonitoringName(), m.MonitoringDescription(),
			m.MonitoringSave(), m.MonitoringCancel(), m.MonitoringDeletePanel(),
		} {
			if !insideShell(tester, text, variant.width, variant.height) {
				t.state.CancelEdit()
				shellState.Shutdown()
				return selftest.Failed(fmt.Errorf("%s editor: %q is missing or clipped: %v",
					prefix, text, tester.Texts()))
			}
		}
		if !tester.HasText("Alpha") {
			t.state.CancelEdit()
			shellState.Shutdown()
			return selftest.Failed(fmt.Errorf("%s editor: the Host list is missing: %v",
				prefix, tester.Texts()))
		}
		if err := capture(tester, filepath.Join(directory, prefix+"-editor.png")); err != nil {
			t.state.CancelEdit()
			shellState.Shutdown()
			return selftest.Failed(err)
		}
		captured++
		t.state.CancelEdit()
		tester.Frame()

		// Columns editor: Done is inside the window. At 720x480 the lower
		// rows are reached by scrolling the editor body, not the window.
		t.state.ToggleColumns()
		tester.Frame()
		if !insideShell(tester, m.MonitoringColumnsDone(), variant.width, variant.height) {
			t.state.ToggleColumns()
			shellState.Shutdown()
			return selftest.Failed(fmt.Errorf("%s columns: Done is missing or clipped: %v",
				prefix, tester.Texts()))
		}
		if err := capture(tester, filepath.Join(directory, prefix+"-columns.png")); err != nil {
			t.state.ToggleColumns()
			shellState.Shutdown()
			return selftest.Failed(err)
		}
		captured++
		if variant.height < 600 {
			// Scroll the editor body, below its title and above Done.
			tester.Scroll(float32(variant.width)/2, float32(variant.height)*0.65, 0, 600)
			tester.Frame()
			if !insideShell(tester, m.MonitoringTerminal()+" · "+m.MonitoringAlwaysVisible(),
				variant.width, variant.height) {
				t.state.ToggleColumns()
				shellState.Shutdown()
				return selftest.Failed(fmt.Errorf(
					"%s columns: the lower rows are unreachable: %v", prefix, tester.Texts()))
			}
			if !insideShell(tester, m.MonitoringColumnsDone(), variant.width, variant.height) {
				t.state.ToggleColumns()
				shellState.Shutdown()
				return selftest.Failed(fmt.Errorf(
					"%s columns: Done scrolled out of the card", prefix))
			}
		}
		t.state.ToggleColumns()
		tester.Frame()

		// Charts: each card keeps its metric row and Open action inside the
		// card; at 720x480 the first card fits without a clipped action.
		t.state.UpdatePanelView(domainpreferences.MonitorViewCharts)
		tester.Frame()
		for _, text := range []string{m.MonitoringCpu(), m.MonitoringOpen()} {
			if !insideShell(tester, text, variant.width, variant.height) {
				shellState.Shutdown()
				return selftest.Failed(fmt.Errorf("%s charts: %q is missing or clipped: %v",
					prefix, text, tester.Texts()))
			}
		}
		if err := capture(tester, filepath.Join(directory, prefix+"-charts.png")); err != nil {
			shellState.Shutdown()
			return selftest.Failed(err)
		}
		captured++
		shellState.Shutdown()
	}
	t.state.UpdatePanelView(domainpreferences.MonitorViewList)
	return selftest.Passed(fmt.Sprintf(
		"list, panel editor, columns editor, and charts rendered inside the real shell with no clipped control at 720x480 and 1100x720 in en and zh-CN; %d frames captured",
		captured),
		"ui/shell.View + uimonitoring.View + ui.Tester")
}

// shellTester renders the monitoring surface in the production shell. The
// monitor's activate hook is nil: sampling already runs from the configure
// step, and this step only inspects the layout.
func (t *selftest44) shellTester(width, height int, locale string) (*ui.Tester, *shellstate.State) {
	shellState := shellstate.NewState(shellstate.WithMonitor(
		uimonitoring.View(t.state), nil, nil))
	shellState.ShowMonitor()
	tester := ui.NewTester(uishell.View(shellState), width, height)
	tester.SetLocale(locale)
	tester.Frame()
	return tester, shellState
}

func (t *selftest44) trustStep(*ui.Context) selftest.Outcome {
	if !t.trustStarted {
		t.trustStarted = true
		t.trustDeadline = time.Now().Add(selftest44Wait)
		t.state.BeginEditPanel()
		t.state.ToggleFormHost("selftest-44-denied")
		t.state.SavePanel()
	}
	for {
		t.state.Flush()
		hostState, ok := t.state.Snapshot().Hosts["selftest-44-denied"]
		if ok && hostState.Status == domainmonitor.StatusLogin &&
			hostState.Reason == domainmonitor.ReasonHostKey {
			break
		}
		if time.Now().After(t.trustDeadline) {
			return selftest.Failed(fmt.Errorf(
				"the refused Host never became a typed login state: %+v", hostState))
		}
		// The harness asks again on its next frame; the deadline is a real
		// condition, not a fixed delay.
		return selftest.Pending
	}
	if opens := t.connector.openCount("selftest-44-denied"); opens != 1 {
		return selftest.Failed(fmt.Errorf("the refused Host was opened %d times, want 1", opens))
	}
	if execs := t.connector.execCount("selftest-44-denied"); execs != 0 {
		return selftest.Failed(fmt.Errorf(
			"a metric command ran on an untrusted Host: %d commands", execs))
	}
	return selftest.Passed(
		"a refused host key became login/host-key with zero metric commands, and the other Hosts kept sampling",
		"application/monitor.Connector host-key decision + typed Reason")
}

func (t *selftest44) cleanupStep(*ui.Context) selftest.Outcome {
	if t.cleanupDeadline.IsZero() {
		t.cleanupDeadline = time.Now().Add(selftest44Wait)
	}
	t.state.Deactivate()
	if !t.connector.allClosed() {
		return pending(t.cleanupDeadline, "the sampler transports to close")
	}
	t.state.Close()
	t.service.Close()
	if t.state.Running() {
		return selftest.Failed(errors.New("the panel is still running after close"))
	}
	// The run is complete; the next F11,4,4 rebuilds its service and surface.
	t.prepared = false
	return selftest.Passed(
		"deactivation stopped sampling and every in-process Host transport closed",
		"application/monitor.Service.Close")
}

func inside(tester *ui.Tester, text string, width, height int) bool {
	rect, ok := tester.Find(text)
	if !ok || rect.W <= 0 || rect.H <= 0 {
		return false
	}
	return rect.X >= 0 && rect.Y >= 0 &&
		rect.X+rect.W <= float32(width) && rect.Y+rect.H <= float32(height)
}

// insideShell is inside plus the shell's 232-DIP sidebar: a control must sit
// in the content column, not over the navigation.
func insideShell(tester *ui.Tester, text string, width, height int) bool {
	rect, ok := tester.Find(text)
	if !ok || rect.W <= 0 || rect.H <= 0 {
		return false
	}
	const sidebar = 232
	return rect.X >= sidebar && rect.Y >= 0 &&
		rect.X+rect.W <= float32(width) && rect.Y+rect.H <= float32(height)
}

func capture(tester *ui.Tester, path string) error {
	image := tester.Image()
	if image == nil {
		return errors.New("the renderer produced no image")
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create screenshot %s: %w", path, err)
	}
	if err := png.Encode(file, image); err != nil {
		_ = file.Close()
		return fmt.Errorf("write screenshot %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close screenshot %s: %w", path, err)
	}
	return nil
}

// selftest44Prefs is an in-memory settings seam. It normalizes exactly as the
// real store does, so the check observes the production persistence shape.
type selftest44Prefs struct {
	mu       sync.Mutex
	settings domainpreferences.Settings
}

func (p *selftest44Prefs) Settings() domainpreferences.Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings
}

func (p *selftest44Prefs) Update(change func(*domainpreferences.Settings)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(&p.settings)
	p.settings.Monitor = domainpreferences.NormalizeMonitor(p.settings.Monitor)
	return nil
}

type selftest44Hosts struct {
	mu    sync.Mutex
	items []domainvault.Host
}

func (h *selftest44Hosts) List(context.Context) ([]domainvault.Host, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]domainvault.Host(nil), h.items...), nil
}

// selftest44Connector refuses one Host before opening any command channel and
// opens a healthy fake transport for the others.
type selftest44Connector struct {
	mu          sync.Mutex
	connections map[string]*selftest44Connection
	opens       map[string]int
	denied      map[string]bool
}

func (c *selftest44Connector) Open(ctx context.Context, host domainvault.Host) (appmonitor.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := string(host.ID)
	c.mu.Lock()
	c.opens[id]++
	if c.denied[id] {
		c.mu.Unlock()
		return nil, &appmonitor.Error{
			Reason: appmonitor.ReasonHostKey,
			Cause:  appconnection.ErrHostKeyRejected,
		}
	}
	connection := &selftest44Connection{id: id}
	c.connections[id] = connection
	c.mu.Unlock()
	return connection, nil
}

func (c *selftest44Connector) openCount(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens[id]
}

func (c *selftest44Connector) execCount(id string) int {
	c.mu.Lock()
	connection := c.connections[id]
	c.mu.Unlock()
	if connection == nil {
		return 0
	}
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.calls
}

func (c *selftest44Connector) allClosed() bool {
	c.mu.Lock()
	connections := make([]*selftest44Connection, 0, len(c.connections))
	for _, connection := range c.connections {
		connections = append(connections, connection)
	}
	c.mu.Unlock()
	for _, connection := range connections {
		connection.mu.Lock()
		closed := connection.closed
		connection.mu.Unlock()
		if !closed {
			return false
		}
	}
	return true
}

// selftest44Connection answers the production stats and disk commands with
// canned Linux replies, increasing its counters each sample so the service
// derives CPU and network deltas.
type selftest44Connection struct {
	mu      sync.Mutex
	id      string
	samples int
	calls   int
	closed  bool
}

func (m *selftest44Connection) Exec(ctx context.Context, command string) (appmonitor.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return appmonitor.ExecResult{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if command == "" {
		return appmonitor.ExecResult{}, errors.New("empty metric command")
	}
	if !strings.Contains(command, "CPURAW:") {
		return appmonitor.ExecResult{
			Stdout: "DISKS:/:50:100:50:fs=ext4:/dev/sda1",
		}, nil
	}
	index := m.samples
	m.samples++
	total := 1000 + index*100
	idle := 100 + index*30
	if index > 1 {
		// Slow the idle counter after the second sample so the trend line
		// visibly rises instead of staying flat.
		idle -= 10 * (index - 1)
	}
	rx := 1000 + index*1300 + index*index*300
	tx := 500 + index*800 + index*index*200
	free := 1024 - index*128
	return appmonitor.ExecResult{Stdout: fmt.Sprintf(
		"CPURAW:%d %d|CORES:4|PERCORERAW:250:%d,250:%d,250:%d,250:%d|"+
			"MEMINFO:8192 %d 512 2048 0 0|NET:eth0:%d:%d|"+
			"GPU:35 512 2048 NVIDIA|HOST:fake-%s|OS:Natcatty Selftest|"+
			"KERNEL:6.0-selftest|UPTIME:100|LOAD:0.1 0.2 0.3",
		total, idle,
		25+index*5, 25+index*5, 25+index*5, 25+index*5,
		free, rx, tx, m.id,
	)}, nil
}

func (m *selftest44Connection) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}
