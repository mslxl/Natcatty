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
}

func (t *selftest44) check() selftest.Check {
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

func (t *selftest44) configureStep(*ui.Context) selftest.Outcome {
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

func (t *selftest44) renderStep(*ui.Context) selftest.Outcome {
	if !t.state.Running() {
		return selftest.Failed(errors.New("the panel stopped before the surface could be rendered"))
	}
	t.state.UpdatePanelView(domainpreferences.MonitorViewList)
	directory := filepath.Join(selftestResultDir, "natcatty-selftest-44-shots")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return selftest.Failed(fmt.Errorf("create the screenshot directory: %w", err))
	}
	en := locales.In("en")
	zh := locales.In("zh-CN")

	wide := ui.NewTester(uimonitoring.View(t.state), 1100, 720)
	wide.SetLocale("en")
	wide.Frame()
	if !inside(wide, en.MonitoringTitle(), 1100, 720) {
		return selftest.Failed(fmt.Errorf("the 1100x720 en title is clipped: %v", wide.Texts()))
	}
	for _, text := range []string{"Alpha", "beta.selftest", "80%", en.MonitoringStatusLive()} {
		if !wide.HasText(text) {
			return selftest.Failed(fmt.Errorf("the 1100x720 en list does not show %q: %v",
				text, wide.Texts()))
		}
	}
	if err := capture(wide, filepath.Join(directory, "monitor-1100x720-en.png")); err != nil {
		return selftest.Failed(err)
	}

	wide.SetLocale("zh-CN")
	wide.Frame()
	if !inside(wide, zh.MonitoringTitle(), 1100, 720) {
		return selftest.Failed(fmt.Errorf("the 1100x720 zh-CN title is clipped: %v", wide.Texts()))
	}
	for _, text := range []string{zh.MonitoringMemory(), zh.MonitoringOpen(), "Alpha"} {
		if !wide.HasText(text) {
			return selftest.Failed(fmt.Errorf("the 1100x720 zh-CN list does not show %q: %v",
				text, wide.Texts()))
		}
	}
	if err := capture(wide, filepath.Join(directory, "monitor-1100x720-zh-CN.png")); err != nil {
		return selftest.Failed(err)
	}

	t.state.UpdatePanelView(domainpreferences.MonitorViewCharts)
	narrow := ui.NewTester(uimonitoring.View(t.state), 720, 480)
	narrow.SetLocale("en")
	narrow.Frame()
	if !inside(narrow, en.MonitoringTitle(), 720, 480) {
		return selftest.Failed(fmt.Errorf("the 720x480 en title is clipped: %v", narrow.Texts()))
	}
	for _, text := range []string{"Alpha", "Beta", en.MonitoringCpu()} {
		if !narrow.HasText(text) {
			return selftest.Failed(fmt.Errorf("the 720x480 en charts do not show %q: %v",
				text, narrow.Texts()))
		}
	}
	if err := capture(narrow, filepath.Join(directory, "monitor-720x480-en.png")); err != nil {
		return selftest.Failed(err)
	}

	narrow.SetLocale("zh-CN")
	narrow.Frame()
	if !inside(narrow, zh.MonitoringTitle(), 720, 480) {
		return selftest.Failed(fmt.Errorf("the 720x480 zh-CN title is clipped: %v", narrow.Texts()))
	}
	for _, text := range []string{zh.MonitoringNetwork(), zh.MonitoringLatency(), "Alpha"} {
		if !narrow.HasText(text) {
			return selftest.Failed(fmt.Errorf("the 720x480 zh-CN charts do not show %q: %v",
				text, narrow.Texts()))
		}
	}
	if err := capture(narrow, filepath.Join(directory, "monitor-720x480-zh-CN.png")); err != nil {
		return selftest.Failed(err)
	}

	return selftest.Passed(
		"list and chart views rendered without clipping at 1100x720 en/zh-CN and 720x480 en/zh-CN; four frames captured",
		"uimonitoring.View + ui.Tester")
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
