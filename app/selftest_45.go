//go:build selftest

package app

// Ticket 45 self-test. Press F11, then 4, then 5.
//
// The check runs the real System manager service and surface against an
// in-process fake Host transport:
//
//   - Overview, Processes, Ports, and Services all sample through the
//     production command templates and parsers;
//   - a process signal and a systemd restart each need confirmation: the fake
//     records no command before ConfirmPending, and one command after it;
//   - changing the Processes refresh interval persists it through the settings
//     seam and restarts sampling;
//   - a Host without ss/netstat/lsof or systemctl becomes a typed
//     "unavailable" tab while its process tab keeps working;
//   - a Host whose host key is refused becomes login/host-key with zero
//     commands;
//   - every tab is rendered at 1100x720 and 720x480 in English and zh-CN,
//     with sixteen frames captured beside the result file.
//
// Every wait observes service or fake state with a hard deadline; there is no
// sleep.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-45.json and
// %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-45-shots\*.png.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	uisysmanager "github.com/mslxl/natcatty/ui/sysmanager"
)

const (
	selftest45Ticket = 45
	selftest45Wait   = 8 * time.Second
	selftest45Rich   = "selftest-45-alpha"
)

type selftest45 struct {
	prefs     *selftest45Prefs
	hosts     *selftest45Hosts
	connector *selftest45Connector
	service   *appsysmanager.Service
	state     *uisysmanager.State

	activated bool

	setupStage    int
	setupDeadline time.Time

	actionStage    int
	actionDeadline time.Time
	actionPID      int
	actionUnit     string
	killBefore     int
	systemBefore   int

	degradeStage    int
	degradeDeadline time.Time

	trustDeadline time.Time

	renderTab      int
	renderDeadline time.Time
	directory      string
	captured       int

	cleanupDeadline time.Time

	// prepared says this run has initialized its service and surface. The
	// harness keeps one registered set of steps, so it is cleared when a run
	// finishes and the next F11,4,5 rebuilds them from resetRunState.
	prepared bool
}

func (t *selftest45) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest45Ticket,
		Name:   "System manager tabs, confirmations, and typed degradation",
		Steps: []selftest.Step{
			{Name: "sample-tabs", Run: t.setupStep},
			{Name: "confirmation-gate", Run: t.actionStep},
			{Name: "degradation", Run: t.degradeStep},
			{Name: "host-key-gate", Run: t.trustStep},
			{Name: "render-and-capture", Run: t.renderStep},
			{Name: "stop-and-release", Run: t.cleanupStep},
		},
	}
}

// resetRunState rebuilds the service, surface, and per-run stages so pressing
// F11,4,5 again in the same process starts from the same conditions as the
// first run. The harness registers one set of steps, so without it the second
// run reuses the first run's stages and deadlines against a closed service and
// its confirmation-gate step times out. It follows the ticket 15 convention.
func (t *selftest45) resetRunState() {
	if t.state != nil {
		t.state.Close()
	}
	if t.service != nil {
		t.service.Close()
	}
	if t.directory != "" {
		_ = os.RemoveAll(t.directory)
		t.directory = ""
	}
	t.prefs = &selftest45Prefs{settings: domainpreferences.Defaults()}
	t.hosts = &selftest45Hosts{items: []domainvault.Host{
		{ID: model.HostID(selftest45Rich), Label: "Alpha", Hostname: "alpha.selftest",
			Group: "selftest"},
		{ID: model.HostID("selftest-45-minimal"), Label: "Minimal", Hostname: "minimal.selftest",
			Group: "selftest"},
		{ID: model.HostID("selftest-45-denied"), Label: "Denied", Hostname: "denied.selftest",
			Group: "selftest"},
	}}
	t.connector = &selftest45Connector{
		opens:     make(map[string]int),
		commands:  make(map[string][]string),
		denied:    map[string]bool{"selftest-45-denied": true},
		minimal:   map[string]bool{"selftest-45-minimal": true},
		overviews: make(map[string]int),
	}
	t.service = appsysmanager.NewService(t.connector)
	t.state = uisysmanager.NewState(t.service, t.hosts, t.prefs)

	t.activated = false
	t.setupStage = 0
	t.setupDeadline = time.Time{}
	t.actionStage = 0
	t.actionDeadline = time.Time{}
	t.actionPID = 0
	t.actionUnit = ""
	t.killBefore = 0
	t.systemBefore = 0
	t.degradeStage = 0
	t.degradeDeadline = time.Time{}
	t.trustDeadline = time.Time{}
	t.renderTab = 0
	t.renderDeadline = time.Time{}
	t.captured = 0
	t.cleanupDeadline = time.Time{}
}

func (t *selftest45) setupStep(*ui.Context) selftest.Outcome {
	if !t.prepared {
		t.resetRunState()
		t.prepared = true
	}
	if !t.activated {
		t.activated = true
		t.state.Activate()
		t.setupDeadline = time.Now().Add(selftest45Wait)
		return selftest.Pending
	}
	for t.setupStage < len(appsysmanager.Tabs) {
		tab := appsysmanager.Tabs[t.setupStage]
		if t.state.Tab() != tab {
			t.state.SetTab(tab)
			t.setupDeadline = time.Now().Add(selftest45Wait)
			return selftest.Pending
		}
		t.state.Flush()
		state := t.state.TabState(tab)
		if !selftest45TabReady(t.state, tab, state) {
			return pending(t.setupDeadline, string(tab)+" sampling to become live")
		}
		t.setupStage++
	}
	t.state.Flush()
	overview := t.state.TabState(appsysmanager.TabOverview).Overview
	if overview.Hostname != "selftest-45-alpha" || overview.UptimeSeconds != 90061 ||
		overview.Cores != 8 || len(overview.LoadAverage) != 3 {
		return selftest.Failed(fmt.Errorf("overview = %+v", overview))
	}
	if rows := t.state.DisplayProcesses(); len(rows) != 3 || rows[0].Command == "" {
		return selftest.Failed(fmt.Errorf("processes = %+v", rows))
	}
	if rows := t.state.DisplayPorts(); len(rows) != 3 {
		return selftest.Failed(fmt.Errorf("ports = %+v", rows))
	}
	if rows := t.state.DisplayServices(); len(rows) != 4 || rows[0].ActiveState != "failed" {
		return selftest.Failed(fmt.Errorf("services = %+v", rows))
	}

	// Change the Processes cadence through the production surface and read it
	// back from the settings seam.
	t.state.SetTab(appsysmanager.TabProcesses)
	t.state.SetInterval(10)
	if got := t.prefs.Settings().SysManager.ProcessesInterval; got != 10 {
		return selftest.Failed(fmt.Errorf("persisted Processes interval = %d, want 10", got))
	}
	if got := t.state.Interval(); got != 10 {
		return selftest.Failed(fmt.Errorf("active interval = %d, want 10", got))
	}
	return selftest.Passed(
		fmt.Sprintf("four tabs are live (CPU %.0f%%, %d processes, %d listeners, %d units) and the Processes interval persisted as 10s",
			overview.CPUPercent, len(t.state.DisplayProcesses()),
			len(t.state.DisplayPorts()), len(t.state.DisplayServices())),
		"application/sysmanager.Service fake exec + domain/preferences.SysManager")
}

func selftest45TabReady(state *uisysmanager.State, tab appsysmanager.Tab,
	tabState appsysmanager.TabState) bool {
	if tabState.Status != appsysmanager.StatusLive {
		return false
	}
	switch tab {
	case appsysmanager.TabOverview:
		return tabState.Overview.Hostname != "" && appsysmanager.Valid(tabState.Overview.CPUPercent)
	case appsysmanager.TabProcesses:
		return len(tabState.Processes) > 0
	case appsysmanager.TabPorts:
		return len(tabState.Ports) > 0
	case appsysmanager.TabServices:
		return len(tabState.Services) > 0
	}
	return false
}

func (t *selftest45) actionStep(*ui.Context) selftest.Outcome {
	if t.actionStage == 0 {
		t.state.SetTab(appsysmanager.TabProcesses)
		t.state.Flush()
		rows := t.state.DisplayProcesses()
		if len(rows) == 0 {
			t.actionDeadline = time.Now().Add(selftest45Wait)
			return pending(t.actionDeadline, "a process row for the signal action")
		}
		t.state.SelectRow(0)
		t.actionPID = rows[0].PID
		t.killBefore = t.connector.countPrefix(selftest45Rich, "kill ")
		if err := t.state.RequestSignal(t.actionPID, "KILL"); err != nil {
			return selftest.Failed(fmt.Errorf("request signal: %w", err))
		}
		pendingAction, ok := t.state.Pending()
		wantCommand := "kill -9 " + strconv.Itoa(t.actionPID)
		if !ok || pendingAction.Command != wantCommand {
			return selftest.Failed(fmt.Errorf("pending signal = %+v, want %q", pendingAction, wantCommand))
		}
		if got := t.connector.countPrefix(selftest45Rich, "kill "); got != t.killBefore {
			return selftest.Failed(fmt.Errorf("a kill command ran before confirmation: %d -> %d",
				t.killBefore, got))
		}
		t.state.ConfirmPending()
		t.actionDeadline = time.Now().Add(selftest45Wait)
		t.actionStage = 1
		return selftest.Pending
	}

	t.state.Flush()
	if t.actionStage == 1 {
		if !t.state.ActionDone() {
			return pending(t.actionDeadline, "the confirmed signal to finish")
		}
		if t.state.ActionError() != nil {
			return selftest.Failed(fmt.Errorf("confirmed signal failed: %w", t.state.ActionError()))
		}
		want := "kill -9 " + strconv.Itoa(t.actionPID)
		if got := t.connector.countPrefix(selftest45Rich, "kill "); got != t.killBefore+1 {
			return selftest.Failed(fmt.Errorf("kill commands = %d, want %d", got, t.killBefore+1))
		}
		if result, ok := t.state.ActionResult(); !ok || result.Command != want {
			return selftest.Failed(fmt.Errorf("signal result = %+v", result))
		}

		t.state.SetTab(appsysmanager.TabServices)
		t.actionDeadline = time.Now().Add(selftest45Wait)
		t.actionStage = 2
		return selftest.Pending
	}
	if t.actionStage == 2 {
		rows := t.state.DisplayServices()
		if len(rows) == 0 {
			return pending(t.actionDeadline, "a service row for the restart action")
		}
		t.state.SelectRow(0)
		t.actionUnit = rows[0].Name
		t.systemBefore = t.connector.countPrefix(selftest45Rich, "systemctl ")
		if err := t.state.RequestServiceAction(t.actionUnit, appsysmanager.ServiceRestart,
			appsysmanager.Scope(rows[0].Scope)); err != nil {
			return selftest.Failed(fmt.Errorf("request restart: %w", err))
		}
		if _, ok := t.state.Pending(); !ok {
			return selftest.Failed(errors.New("the restart did not ask for confirmation"))
		}
		t.state.CancelPending()
		if got := t.connector.countPrefix(selftest45Rich, "systemctl "); got != t.systemBefore {
			return selftest.Failed(fmt.Errorf("cancel ran a systemctl command: %d -> %d",
				t.systemBefore, got))
		}
		if err := t.state.RequestServiceAction(t.actionUnit, appsysmanager.ServiceRestart,
			appsysmanager.Scope(rows[0].Scope)); err != nil {
			return selftest.Failed(fmt.Errorf("request restart again: %w", err))
		}
		t.state.ConfirmPending()
		t.actionDeadline = time.Now().Add(selftest45Wait)
		t.actionStage = 3
		return selftest.Pending
	}
	if !t.state.ActionDone() {
		return pending(t.actionDeadline, "the confirmed restart to finish")
	}
	if t.state.ActionError() != nil {
		return selftest.Failed(fmt.Errorf("confirmed restart failed: %w", t.state.ActionError()))
	}
	if got := t.connector.countPrefix(selftest45Rich, "systemctl "); got != t.systemBefore+1 {
		return selftest.Failed(fmt.Errorf("systemctl commands = %d, want %d",
			got, t.systemBefore+1))
	}
	return selftest.Passed(fmt.Sprintf(
		"kill -9 %d and systemctl restart %q each ran only after ConfirmPending; CancelPending ran nothing",
		t.actionPID, t.actionUnit),
		"ui/sysmanager.State confirmation gate + application/sysmanager actions")
}

func (t *selftest45) degradeStep(*ui.Context) selftest.Outcome {
	if t.degradeStage == 0 {
		t.state.SetHost("selftest-45-minimal")
		t.state.SetTab(appsysmanager.TabPorts)
		t.degradeDeadline = time.Now().Add(selftest45Wait)
		t.degradeStage = 1
		return selftest.Pending
	}
	t.state.Flush()
	switch t.degradeStage {
	case 1:
		state := t.state.TabState(appsysmanager.TabPorts)
		if state.Status != appsysmanager.StatusUnavailable ||
			state.Reason != appsysmanager.ReasonMissingPorts {
			return pending(t.degradeDeadline, "the ports tab to degrade to missing_ports")
		}
		t.state.SetTab(appsysmanager.TabProcesses)
		t.degradeDeadline = time.Now().Add(selftest45Wait)
		t.degradeStage = 2
		return selftest.Pending
	case 2:
		state := t.state.TabState(appsysmanager.TabProcesses)
		if state.Status != appsysmanager.StatusLive || len(state.Processes) == 0 {
			return pending(t.degradeDeadline, "the process tab to keep working")
		}
		t.state.SetTab(appsysmanager.TabServices)
		t.degradeDeadline = time.Now().Add(selftest45Wait)
		t.degradeStage = 3
		return selftest.Pending
	case 3:
		state := t.state.TabState(appsysmanager.TabServices)
		if state.Status != appsysmanager.StatusUnavailable ||
			state.Reason != appsysmanager.ReasonMissingSystemctl {
			return pending(t.degradeDeadline, "the services tab to degrade to missing_systemctl")
		}
		t.state.SetHost(selftest45Rich)
		t.state.SetTab(appsysmanager.TabProcesses)
		t.degradeDeadline = time.Now().Add(selftest45Wait)
		t.degradeStage = 4
		return selftest.Pending
	default:
		state := t.state.TabState(appsysmanager.TabProcesses)
		if state.Status != appsysmanager.StatusLive || len(state.Processes) == 0 {
			return pending(t.degradeDeadline, "the rich Host to recover")
		}
		return selftest.Passed(
			"a Host without ss/netstat/lsof and systemctl showed typed missing-port and missing-systemctl states while its process tab stayed live",
			"application/sysmanager.Capabilities.Available + ui/sysmanager reason text")
	}
}

func (t *selftest45) trustStep(*ui.Context) selftest.Outcome {
	if t.trustDeadline.IsZero() {
		t.state.SetHost("selftest-45-denied")
		t.state.SetTab(appsysmanager.TabOverview)
		t.trustDeadline = time.Now().Add(selftest45Wait)
		return selftest.Pending
	}
	t.state.Flush()
	state := t.state.TabState(appsysmanager.TabOverview)
	if state.Status != appsysmanager.StatusLogin || state.Reason != appsysmanager.ReasonHostKey {
		return pending(t.trustDeadline, "the refused Host key to become login/host_key")
	}
	if opens := t.connector.openCount("selftest-45-denied"); opens != 1 {
		return selftest.Failed(fmt.Errorf("the refused Host opened %d leases, want 1", opens))
	}
	if execs := t.connector.execCount("selftest-45-denied"); execs != 0 {
		return selftest.Failed(fmt.Errorf("the untrusted Host ran %d commands", execs))
	}
	t.state.SetHost(selftest45Rich)
	t.state.SetTab(appsysmanager.TabProcesses)
	return selftest.Passed(
		"a refused host key became login/host-key with zero commands; switching back resumed the rich Host",
		"application/sysmanager.Connector trust decision + typed Reason")
}

var selftest45Labels = map[appsysmanager.Tab]struct{ en, zh string }{
	appsysmanager.TabOverview:  {"Hostname", "主机名"},
	appsysmanager.TabProcesses: {"Command", "命令"},
	appsysmanager.TabPorts:     {"Protocol", "协议"},
	appsysmanager.TabServices:  {"Unit", "单元"},
}

func (t *selftest45) renderStep(*ui.Context) selftest.Outcome {
	if t.directory == "" {
		t.directory = filepath.Join(selftestResultDir, "natcatty-selftest-45-shots")
		if err := os.MkdirAll(t.directory, 0o755); err != nil {
			return selftest.Failed(fmt.Errorf("create the screenshot directory: %w", err))
		}
	}
	if t.renderTab >= len(appsysmanager.Tabs) {
		entries, err := os.ReadDir(t.directory)
		if err != nil {
			return selftest.Failed(fmt.Errorf("read the screenshot directory: %w", err))
		}
		if len(entries) != 16 {
			return selftest.Failed(fmt.Errorf("captured %d screenshots, want 16", len(entries)))
		}
		return selftest.Passed(
			"Overview, Processes, Ports, and Services rendered at 1100x720 and 720x480 in en and zh-CN; sixteen frames captured",
			"uisysmanager.View + ui.Tester")
	}
	tab := appsysmanager.Tabs[t.renderTab]
	if t.state.Tab() != tab {
		t.state.SetTab(tab)
		t.renderDeadline = time.Now().Add(selftest45Wait)
		return selftest.Pending
	}
	t.state.Flush()
	state := t.state.TabState(tab)
	if !selftest45TabReady(t.state, tab, state) {
		if t.renderDeadline.IsZero() {
			t.renderDeadline = time.Now().Add(selftest45Wait)
		}
		return pending(t.renderDeadline, string(tab)+" to become live for capture")
	}
	if tab != appsysmanager.TabOverview {
		// A frame with no choice lets the Table forget the previous tab's
		// row identity before the first row is chosen here.
		warm := ui.NewTester(uisysmanager.View(t.state), 1100, 720)
		warm.Frame()
		t.state.SelectRow(0)
	}
	en := locales.In("en")
	zh := locales.In("zh-CN")
	for _, size := range []struct {
		width, height int
	}{{1100, 720}, {720, 480}} {
		for _, locale := range []string{"en", "zh-CN"} {
			tester := ui.NewTester(uisysmanager.View(t.state), size.width, size.height)
			tester.SetLocale(locale)
			tester.Frame()
			m := en
			if locale == "zh-CN" {
				m = zh
			}
			if !inside(tester, m.SysManagerTitle(), size.width, size.height) {
				return selftest.Failed(fmt.Errorf("%s %dx%d title is clipped: %v",
					locale, size.width, size.height, tester.Texts()))
			}
			label := selftest45Labels[tab]
			want := label.en
			if locale == "zh-CN" {
				want = label.zh
			}
			if !tester.HasText(want) {
				return selftest.Failed(fmt.Errorf("%s %dx%d %s tab does not show %q: %v",
					locale, size.width, size.height, tab, want, tester.Texts()))
			}
			if tab != appsysmanager.TabOverview {
				action := m.SysManagerTerminate()
				if tab == appsysmanager.TabServices {
					action = m.SysManagerRestart()
				}
				if !tester.HasText(action) {
					return selftest.Failed(fmt.Errorf("%s %dx%d %s action bar does not show %q: %v",
						locale, size.width, size.height, tab, action, tester.Texts()))
				}
			}
			name := fmt.Sprintf("sysmanager-%s-%dx%d-%s.png", tab, size.width, size.height, locale)
			if err := capture(tester, filepath.Join(t.directory, name)); err != nil {
				return selftest.Failed(err)
			}
			t.captured++
		}
	}
	t.renderTab++
	return selftest.Pending
}

func (t *selftest45) cleanupStep(*ui.Context) selftest.Outcome {
	if t.cleanupDeadline.IsZero() {
		t.cleanupDeadline = time.Now().Add(selftest45Wait)
		t.state.Deactivate()
		t.state.Close()
		t.service.Close()
	}
	t.service.Wait()
	if !t.connector.allClosed() {
		return pending(t.cleanupDeadline, "every leased transport to close")
	}
	// The run is complete; the next F11,4,5 rebuilds its service and surface.
	t.prepared = false
	return selftest.Passed(fmt.Sprintf(
		"deactivation and close released every sampler and action lease (%d leases); %d screenshots captured",
		t.connector.openCount(selftest45Rich), t.captured),
		"application/sysmanager.Service.Close + ui/sysmanager.State.Close")
}

type selftest45Prefs struct {
	mu       sync.Mutex
	settings domainpreferences.Settings
}

func (p *selftest45Prefs) Settings() domainpreferences.Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings
}

func (p *selftest45Prefs) Update(change func(*domainpreferences.Settings)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(&p.settings)
	p.settings.Monitor = domainpreferences.NormalizeMonitor(p.settings.Monitor)
	p.settings.SysManager = domainpreferences.NormalizeSysManager(p.settings.SysManager)
	return nil
}

type selftest45Hosts struct {
	mu    sync.Mutex
	items []domainvault.Host
}

func (h *selftest45Hosts) List(context.Context) ([]domainvault.Host, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]domainvault.Host(nil), h.items...), nil
}

type selftest45Connector struct {
	mu         sync.Mutex
	opens      map[string]int
	openLeases int
	closed     int
	commands   map[string][]string
	denied     map[string]bool
	minimal    map[string]bool
	overviews  map[string]int
}

func (c *selftest45Connector) Open(ctx context.Context, host domainvault.Host) (appsysmanager.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := string(host.ID)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens[id]++
	if c.denied[id] {
		return nil, &appsysmanager.Error{Reason: appsysmanager.ReasonHostKey,
			Cause: errors.New("host key rejected by the self-test")}
	}
	c.openLeases++
	return &selftest45Connection{connector: c, id: id}, nil
}

func (c *selftest45Connector) openCount(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens[id]
}

func (c *selftest45Connector) execCount(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.commands[id])
}

func (c *selftest45Connector) countPrefix(id, prefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, command := range c.commands[id] {
		if strings.HasPrefix(command, prefix) {
			count++
		}
	}
	return count
}

func (c *selftest45Connector) allClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed == c.openLeases
}

func (c *selftest45Connector) leaseClosed() {
	c.mu.Lock()
	c.closed++
	c.mu.Unlock()
}

type selftest45Connection struct {
	connector *selftest45Connector
	id        string
	closed    bool
}

func (c *selftest45Connection) Exec(ctx context.Context, command string) (appsysmanager.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return appsysmanager.ExecResult{}, err
	}
	connector := c.connector
	connector.mu.Lock()
	connector.commands[c.id] = append(connector.commands[c.id], command)
	minimal := connector.minimal[c.id]
	index := connector.overviews[c.id]
	if strings.Contains(command, "PRETTY_NAME") {
		connector.overviews[c.id] = index + 1
	}
	connector.mu.Unlock()
	if strings.Contains(command, "for c in ps top ss netstat lsof systemctl") {
		if minimal {
			return appsysmanager.ExecResult{Stdout: "__NC_OS=Linux\n__NC_HAS_ps=1\n"}, nil
		}
		return appsysmanager.ExecResult{Stdout: "__NC_OS=Linux\n__NC_HAS_ps=1\n" +
			"__NC_HAS_top=1\n__NC_HAS_ss=1\n__NC_HAS_netstat=1\n__NC_HAS_lsof=1\n" +
			"__NC_HAS_systemctl=1\n"}, nil
	}
	switch {
	case strings.Contains(command, "PRETTY_NAME"):
		return appsysmanager.ExecResult{Stdout: selftest45Overview(index)}, nil
	case strings.Contains(command, "ps -eo"):
		return appsysmanager.ExecResult{Stdout: selftest45Processes}, nil
	case strings.Contains(command, "__NC_PORTS_BEGIN__"):
		return appsysmanager.ExecResult{Stdout: selftest45Ports}, nil
	case strings.Contains(command, "__NC_SERVICES_BEGIN__"):
		return appsysmanager.ExecResult{Stdout: selftest45Services}, nil
	case strings.HasPrefix(command, "kill "), strings.HasPrefix(command, "systemctl "),
		strings.HasPrefix(command, "sudo -n systemctl "):
		return appsysmanager.ExecResult{Stdout: "ok", HasExitCode: true}, nil
	default:
		return appsysmanager.ExecResult{}, nil
	}
}

func (c *selftest45Connection) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	c.connector.leaseClosed()
	return nil
}

const (
	selftest45Processes = "  1     0 root Ss 0.0 0.1 1024 2048 2-03:04:05 /sbin/init\n" +
		" 42     1 app  Sl 12.5 2.5 4096 8192 00:12:34 /usr/bin/worker --serve\n" +
		"777     1 batch T  0.0 0.2  512 1024 00:01:00 paused-job"
	selftest45Ports = "__NC_PORTS_BEGIN__\n__NC_SS__\n" +
		"tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:((\"sshd\",pid=123,fd=3))\n" +
		"tcp LISTEN 0 128 0.0.0.0:80 0.0.0.0:* users:((\"nginx\",pid=789,fd=6))\n" +
		"udp UNCONN 0 0 127.0.0.1:53 0.0.0.0:* users:((\"dnsmasq\",pid=456,fd=4))\n" +
		"__NC_PORTS_END__\n"
	selftest45Services = "__NC_SERVICES_BEGIN__\n__NC_SYSTEM__\n" +
		"● broken.service loaded failed failed Broken worker\n" +
		"nginx.service loaded active running A high performance web server\n" +
		"db.service loaded inactive dead Database\n" +
		"__NC_USER__\n" +
		"user.service loaded active running User unit\n" +
		"__NC_SERVICES_END__\n"
)

func selftest45Overview(index int) string {
	total := 1000 + index*100
	idle := 500 + index*25
	return fmt.Sprintf("MODE:linux|HOST:selftest-45-alpha|OS:Debian 12|KERNEL:6.1.0-selftest|"+
		"UPTIME:90061|LOAD:0.52 0.48 0.41|CORES:8|CPURAW:%d %d|MEM:3072 8192|"+
		"SWAP:128 1024|DISK:25.0 100.0", total, idle)
}
