package shell

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/hoststats"
	appsession "github.com/mslxl/natcatty/application/session"
	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
	uisysmanager "github.com/mslxl/natcatty/ui/sysmanager"
)

// This file drives the Overview walking skeleton through the real shell: a
// real System manager State over a real Service over the shared Host Stats
// collector, whose Connector is a fake transport. It covers the five states in
// both surfaces, both Interface Styles, and en and zh-CN, and proves the one
// Stats command template reaches the transport.

const (
	overviewWidth  = 1280
	overviewHeight = 800
	// overviewSideWidth is the side panel's minimum width the tests pin.
	overviewSideWidth float32 = 280
	// overviewSampleAge is how far the surface clock sits past the collector's
	// data clock, so the status row's updated-at text is deterministic.
	overviewSampleAge = 10 * time.Second
)

var overviewBaseTime = time.Unix(1_700_000_000, 0)

const (
	overviewDarwin37Reply = "NC_LATENCY_MARK|MODE:darwin|CPU:37|CORES:8|" +
		"MEMINFO:16384 12288 0 0 1024 0|PROCS:|" +
		"DISKS:/:10.000000:40.000000:25:/dev/disk1|NET:en0:1000:2000|" +
		"HOST:shared|OS:macOS 14|KERNEL:23|UPTIME:100|LOAD:1 2 3"
	overviewDarwin55Reply = "NC_LATENCY_MARK|MODE:darwin|CPU:55|CORES:8|" +
		"MEMINFO:16384 12288 0 0 1024 0|PROCS:|" +
		"DISKS:/:10.000000:40.000000:25:/dev/disk1|NET:en0:1000:2000|" +
		"HOST:shared|OS:macOS 14|KERNEL:23|UPTIME:100|LOAD:1 2 3"
	overviewDiskReply = "DISKS:/:10.000000:40.000000:25:fs=ext4:/dev/sda1"
)

// overviewLinuxReply is the Stats Sample command's Linux answer with explicit
// counters, so two replies differ by a measurable delta.
func overviewLinuxReply(total, idle, coreTotal, coreIdle, rx, tx, uptime, read, write uint64) string {
	return fmt.Sprintf("NC_LATENCY_MARK|MODE:linux|CPURAW:%d %d|CORES:2|"+
		"PERCORERAW:cpu0:%d:%d,cpu1:%d:%d|MEMINFO:8000 1000 100 500 0 0|PROCS:|"+
		"NET:eth0:%d:%d|GPU:|DISKIO:%d;vda:%d:%d,|"+
		"HOST:shared|OS:Linux|KERNEL:6.1|UPTIME:%d|LOAD:0.1 0.2 0.3",
		total, idle, coreTotal, coreIdle, coreTotal, coreIdle, rx, tx,
		uptime, read, write, uptime)
}

// overviewTransport is the fake Host transport under the shared collector. It
// answers the one Stats Sample command template plus the Linux-only disk probe
// and records every command, so the tests can prove the surface produced no
// private sampler.
type overviewTransport struct {
	mu sync.Mutex

	statsOutputs []string
	diskOutputs  []string
	commands     []string
	opens        int
	closes       int
	statsCalls   int
	diskCalls    int
	openErr      error
	execErr      error
	block        <-chan struct{}
	single       bool
}

func newOverviewTransport(outputs ...string) *overviewTransport {
	return &overviewTransport{statsOutputs: outputs}
}

func (t *overviewTransport) Open(ctx context.Context, _ domainvault.Host) (hoststats.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.opens++
	err := t.openErr
	t.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &overviewConnection{transport: t}, nil
}

func (t *overviewTransport) setExecError(err error) {
	t.mu.Lock()
	t.execErr = err
	t.mu.Unlock()
}

func (t *overviewTransport) counts() (opens, stats, disks int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opens, t.statsCalls, t.diskCalls
}

func (t *overviewTransport) closeCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closes
}

func (t *overviewTransport) received() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.commands...)
}

type overviewConnection struct {
	transport *overviewTransport
}

func (c *overviewConnection) Exec(ctx context.Context, command string) (hoststats.ExecResult, error) {
	t := c.transport
	t.mu.Lock()
	t.commands = append(t.commands, command)
	var output string
	var err error
	switch command {
	case hoststats.StatsCommand():
		index := t.statsCalls
		t.statsCalls++
		if len(t.statsOutputs) > 0 {
			if index >= len(t.statsOutputs) {
				index = len(t.statsOutputs) - 1
			}
			output = t.statsOutputs[index]
		}
		err = t.execErr
	case hoststats.DiskCommand():
		index := t.diskCalls
		t.diskCalls++
		if len(t.diskOutputs) > 0 {
			if index >= len(t.diskOutputs) {
				index = len(t.diskOutputs) - 1
			}
			output = t.diskOutputs[index]
		}
	default:
		t.mu.Unlock()
		return hoststats.ExecResult{}, fmt.Errorf("unexpected command %q", command)
	}
	block := t.block
	t.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return hoststats.ExecResult{}, ctx.Err()
		}
	}
	if err != nil {
		return hoststats.ExecResult{}, err
	}
	return hoststats.ExecResult{Stdout: output}, nil
}

func (c *overviewConnection) Close() error {
	c.transport.mu.Lock()
	c.transport.closes++
	c.transport.mu.Unlock()
	return nil
}

func (c *overviewConnection) SingleChannel() bool {
	c.transport.mu.Lock()
	defer c.transport.mu.Unlock()
	return c.transport.single
}

// overviewClock is the collector's data clock. Its scheduler stays on real
// time, so a test moves SampledAt and the state time without waiting.
type overviewClock struct {
	mu sync.Mutex
	at time.Time
}

func newOverviewClock(at time.Time) *overviewClock { return &overviewClock{at: at} }

func (c *overviewClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *overviewClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

type overviewPrefs struct {
	mu       sync.Mutex
	settings domainpreferences.Settings
}

func (p *overviewPrefs) Settings() domainpreferences.Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings
}

func (p *overviewPrefs) Update(change func(*domainpreferences.Settings)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(&p.settings)
	p.settings.SysManager = domainpreferences.NormalizeSysManager(p.settings.SysManager)
	return nil
}

type overviewHosts struct{ items []domainvault.Host }

func (h *overviewHosts) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), h.items...), nil
}

type overviewSurface int

const (
	overviewFullPage overviewSurface = iota
	overviewSidePanel
)

func (s overviewSurface) String() string {
	if s == overviewSidePanel {
		return "side-panel"
	}
	return "full-page"
}

type overviewHarness struct {
	tester    *ui.Tester
	manager   *uisysmanager.State
	transport *overviewTransport
	clock     *overviewClock
	shell     *state.State
	tab       model.TabID
}

// overviewDefaultHost is the Host the full page picks and the side panel's
// local Session maps to.
func overviewDefaultHost() domainvault.Host {
	return domainvault.Host{ID: model.HostID("h1"), Label: "Alpha", Hostname: "host-1"}
}

// newOverviewHarness builds the harness with the default polling interval and
// Host.
func newOverviewHarness(t *testing.T, surface overviewSurface, locale string,
	transport *overviewTransport, opts ...hoststats.CollectorOption) *overviewHarness {
	t.Helper()
	return newOverviewHarnessWith(t, surface, locale, transport, 60, overviewDefaultHost(), opts...)
}

// newOverviewHarnessWith builds the real shell around a real System manager
// service reading the shared collector. The service has no Connector of its
// own, so every command in the fake transport can only come from the shared
// collector. The surface clock sits a fixed distance past the data clock so
// the updated-at text is deterministic. interval is the Overview's polling
// interval in seconds.
func newOverviewHarnessWith(t *testing.T, surface overviewSurface, locale string,
	transport *overviewTransport, interval int, host domainvault.Host,
	opts ...hoststats.CollectorOption) *overviewHarness {
	t.Helper()
	return newOverviewHarnessWithConnector(t, surface, locale, transport, interval,
		host, nil, opts...)
}

// newOverviewHarnessWithConnector is newOverviewHarnessWith with a Connector
// for the non-Overview tabs, whose own sampling loop needs one when a test
// switches away and back. The Overview still reads only the shared collector.
func newOverviewHarnessWithConnector(t *testing.T, surface overviewSurface, locale string,
	transport *overviewTransport, interval int, host domainvault.Host,
	connector appsysmanager.Connector, opts ...hoststats.CollectorOption) *overviewHarness {
	t.Helper()
	clock := newOverviewClock(overviewBaseTime)
	uiNow := clock.Now().Add(overviewSampleAge)

	collectorOpts := []hoststats.CollectorOption{hoststats.WithClock(clock.Now), hoststats.WithCacheTTL(0)}
	collectorOpts = append(collectorOpts, opts...)
	collector := hoststats.NewCollector(transport, collectorOpts...)
	system := appsysmanager.NewService(connector,
		appsysmanager.WithStatsCollector(collector),
		appsysmanager.WithClock(clock.Now),
	)
	settings := domainpreferences.Defaults()
	settings.SysManager.OverviewInterval = interval
	manager := uisysmanager.NewState(system,
		&overviewHosts{items: []domainvault.Host{host}},
		&overviewPrefs{settings: settings},
		uisysmanager.WithClock(func() time.Time { return uiNow }),
	)
	t.Cleanup(func() {
		manager.Close()
		system.Close()
		collector.Close()
	})

	var tester *ui.Tester
	var shellState *state.State
	var tab model.TabID
	switch surface {
	case overviewFullPage:
		shellState = state.NewState(state.WithSystemManager(
			uisysmanager.View(manager), manager.Activate, manager.Deactivate))
		shellState.ShowSystemManager()
		t.Cleanup(shellState.Shutdown)
		tester = ui.NewTester(View(shellState), overviewWidth, overviewHeight)
	case overviewSidePanel:
		sessions := appsession.NewService(multiFactory{})
		t.Cleanup(sessions.CloseAll)
		shellState = state.NewState(
			state.WithSessions(sessions, func(session *appsession.Session) (state.Surface, error) {
				return sessionui.New(session)
			}),
			state.WithFilesPanel(state.FilesPanel{View: func(float32) func(*ui.Context) {
				return func(c *ui.Context) { ui.Text(c, "files-panel") }
			}}),
			state.WithSideTools(
				state.SideTool{ID: state.SideSystem,
					View: func(target state.SideTarget, width float32) func(*ui.Context) {
						host := string(target.Host)
						if host == "" {
							host = "h1" // the local Session's panel samples this computer
						}
						manager.ShowHost(host)
						return uisysmanager.SideView(manager, width)
					},
					Hide: manager.Hide},
				state.SideTool{ID: state.SideNotes,
					View: func(state.SideTarget, float32) func(*ui.Context) {
						return func(c *ui.Context) { ui.Text(c, "notes-tool") }
					}},
			),
		)
		graph := &workspaceViewGraph{state: shellState}
		if err := shellState.NewTab(); err != nil {
			t.Fatalf("open a Session Tab: %v", err)
		}
		graph.waitForSurfaces(t, 1)
		if !shellState.ToggleActiveFiles() {
			t.Fatal("the side panel did not open")
		}
		tab, _ = shellState.TabID(shellState.ActiveTab())
		shellState.SetSideTool(tab, state.SideSystem)
		*shellState.FilesWidth(overviewWidth) = overviewSideWidth
		t.Cleanup(shellState.Shutdown)
		tester = ui.NewTester(View(shellState), overviewWidth, overviewHeight)
	default:
		t.Fatalf("unknown overview surface %d", surface)
	}
	tester.SetLocale(locale)
	return &overviewHarness{tester: tester, manager: manager, transport: transport,
		clock: clock, shell: shellState, tab: tab}
}

// overviewFrameUntil drives frames until condition holds. The interval is a
// poll cadence, not a synchronization assumption: every iteration renders a
// frame and checks a real condition, and the loop has a hard deadline.
func overviewFrameUntil(t *testing.T, tester *ui.Tester, what string, condition func() bool) {
	t.Helper()
	const timeout = 5 * time.Second
	deadline := time.Now().Add(timeout)
	for {
		tester.Frame()
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s; texts: %v", timeout, what, tester.Texts())
		}
		time.Sleep(time.Millisecond)
	}
}

func overviewTabState(manager *uisysmanager.State) appsysmanager.TabState {
	return manager.TabState(appsysmanager.TabOverview)
}

func overviewWaitState(t *testing.T, h *overviewHarness, what string,
	condition func(appsysmanager.TabState) bool) appsysmanager.TabState {
	t.Helper()
	overviewFrameUntil(t, h.tester, what, func() bool {
		return condition(overviewTabState(h.manager))
	})
	return overviewTabState(h.manager)
}

func overviewLive(state appsysmanager.TabState) bool {
	return state.Status == appsysmanager.StatusLive && !state.Stats.SampledAt.IsZero()
}

// overviewFind returns the rendered rect of text and fails when it is absent.
func overviewFind(t *testing.T, tester *ui.Tester, name, text string) ui.Rect {
	t.Helper()
	rect, ok := tester.Find(text)
	if !ok || rect.W <= 0 || rect.H <= 0 {
		t.Fatalf("%s: %q is not rendered: %v", name, text, tester.Texts())
	}
	return rect
}

// overviewMissing fails when text is rendered as an exact text or label.
func overviewMissing(t *testing.T, tester *ui.Tester, name, text string) {
	t.Helper()
	if rect, ok := tester.Find(text); ok {
		t.Fatalf("%s: %q is rendered at %+v, want it absent: %v", name, text, rect, tester.Texts())
	}
}

func overviewInside(outer, inner ui.Rect) bool {
	const epsilon float32 = 1
	return inner.X >= outer.X-epsilon && inner.Y >= outer.Y-epsilon &&
		inner.X+inner.W <= outer.X+outer.W+epsilon &&
		inner.Y+inner.H <= outer.Y+outer.H+epsilon
}

// overviewSurfaceBounds is where the surface may draw: the home content
// column for the full page, and the 280-DIP side panel for the System Tool.
func overviewSurfaceBounds(width int, surface overviewSurface) (left, right float32) {
	if surface == overviewSidePanel {
		return 0, overviewSideWidth
	}
	return sysManagerContentBounds(width)
}

func overviewUseStyle(t *testing.T, style kit.Style) {
	t.Helper()
	kit.SetStyle(style)
	t.Cleanup(func() { kit.SetStyle(nil) })
}

var (
	overviewStyles = []struct {
		name  string
		style kit.Style
	}{
		{"classic", kit.Classic},
		{"macos", kit.MacOS},
	}
	overviewLocales  = []string{"en", "zh-CN"}
	overviewSurfaces = []overviewSurface{overviewFullPage, overviewSidePanel}
)

// TestSysManagerOverviewLiveCPUCardEverywhere proves the live CPU card renders
// its percent, core count, gauge, and sparkline inside its surface, for both
// surfaces, both Interface Styles, and en and zh-CN, from one Stats Sample
// command and no private sampler.
func TestSysManagerOverviewLiveCPUCardEverywhere(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				name := fmt.Sprintf("%s/%s/%s", surface, style.name, locale)
				t.Run(name, func(t *testing.T) {
					overviewUseStyle(t, style.style)
					transport := newOverviewTransport(overviewDarwin37Reply)
					h := newOverviewHarness(t, surface, locale, transport)
					state := overviewWaitState(t, h, "the live sample", overviewLive)
					if state.Stats.CPU != 37 || state.Stats.Cores != 8 {
						t.Fatalf("%s: sample = %v%% on %d cores, want 37%% on 8",
							name, state.Stats.CPU, state.Stats.Cores)
					}

					m := locales.In(locale)
					card := overviewFind(t, h.tester, name, m.SysManagerOverviewCPU()+" card")
					left, right := overviewSurfaceBounds(overviewWidth, surface)
					if card.X < left-0.5 || card.X+card.W > right+0.5 {
						t.Fatalf("%s: the CPU card %.0f..%.0f is outside the surface %.0f..%.0f: %+v",
							name, card.X, card.X+card.W, left, right, card)
					}
					for _, text := range []string{
						"37%",
						m.SysManagerOverviewCores(8),
						m.SysManagerOverviewCPU() + " gauge",
						m.SysManagerOverviewCPU() + " history",
					} {
						rect := overviewFind(t, h.tester, name, text)
						if !overviewInside(card, rect) {
							t.Fatalf("%s: %q at %+v is outside the CPU card %+v",
								name, text, rect, card)
						}
					}
					if surface == overviewFullPage {
						// The status row's age comes from SampledAt, not from the
						// frame or state-change time.
						overviewFind(t, h.tester, name, m.SysManagerUpdatedSeconds(int(overviewSampleAge/time.Second)))
						// The full page keeps its Host picker, interval select,
						// and Refresh action.
						overviewFind(t, h.tester, name, m.SysManagerHost())
						overviewFind(t, h.tester, name, m.SysManagerRefresh())
						overviewFind(t, h.tester, name, m.SysManagerRefreshNow())
					}

					commands := transport.received()
					if len(commands) != 1 || commands[0] != hoststats.StatsCommand() {
						t.Fatalf("%s: the transport received %d commands, want one Stats Sample command",
							name, len(commands))
					}
					opens, statsCalls, diskCalls := transport.counts()
					if opens != 1 || statsCalls != 1 || diskCalls != 0 {
						t.Fatalf("%s: transport saw %d opens, %d stats, %d disk; want one Darwin collection",
							name, opens, statsCalls, diskCalls)
					}
				})
			}
		}
	}
}

// overviewBlockingCase is one no-card state: what the fake transport answers,
// the state the surface must reach, and what the rendered surface must show.
type overviewBlockingCase struct {
	name string
	// host replaces the default Host when set, as for a protocol that cannot
	// run a stats command.
	host    domainvault.Host
	prepare func(*overviewTransport)
	reached func(appsysmanager.TabState) bool
	verify  func(t *testing.T, h *overviewHarness, m locales.Messages, name string)
}

func overviewBlockingCases() []overviewBlockingCase {
	return []overviewBlockingCase{
		{
			name: "loading",
			prepare: func(transport *overviewTransport) {
				transport.block = make(chan struct{})
			},
			reached: func(state appsysmanager.TabState) bool {
				return state.Status == appsysmanager.StatusConnecting
			},
			verify: func(t *testing.T, h *overviewHarness, m locales.Messages, name string) {
				overviewFind(t, h.tester, name, m.SysManagerOverviewLoading())
				overviewMissing(t, h.tester, name, m.SysManagerOverviewCPU()+" card")
				overviewMissing(t, h.tester, name, "0%")
				overviewMissing(t, h.tester, name, m.SysManagerRetry())
			},
		},
		{
			name: "empty",
			prepare: func(transport *overviewTransport) {
				transport.statsOutputs = []string{"nothing here"}
			},
			reached: func(state appsysmanager.TabState) bool {
				return state.Status == appsysmanager.StatusEmpty
			},
			verify: func(t *testing.T, h *overviewHarness, m locales.Messages, name string) {
				overviewFind(t, h.tester, name, m.SysManagerStatusEmpty())
				overviewFind(t, h.tester, name, m.SysManagerReasonNoData())
				overviewFind(t, h.tester, name, m.SysManagerRetry())
				overviewMissing(t, h.tester, name, m.SysManagerOverviewCPU()+" card")
				overviewMissing(t, h.tester, name, "0%")
			},
		},
		{
			name: "blocking-error",
			prepare: func(transport *overviewTransport) {
				transport.openErr = &hoststats.Error{
					Reason: hoststats.ReasonNetwork,
					Cause:  errors.New("dial tcp: connection refused"),
				}
			},
			reached: func(state appsysmanager.TabState) bool {
				return state.Status == appsysmanager.StatusError
			},
			verify: func(t *testing.T, h *overviewHarness, m locales.Messages, name string) {
				overviewFind(t, h.tester, name, m.SysManagerStatusError())
				overviewFind(t, h.tester, name, m.SysManagerReasonNetwork())
				overviewFind(t, h.tester, name, m.SysManagerRetry())
				overviewMissing(t, h.tester, name, m.SysManagerOverviewCPU()+" card")
				overviewMissing(t, h.tester, name, "0%")
			},
		},
		{
			name: "unsupported-os",
			prepare: func(transport *overviewTransport) {
				transport.statsOutputs = []string{"NC_LATENCY_MARK|UNSUPPORTED_OS:FreeBSD"}
			},
			reached: func(state appsysmanager.TabState) bool {
				return state.Status == appsysmanager.StatusUnsupported
			},
			verify: func(t *testing.T, h *overviewHarness, m locales.Messages, name string) {
				overviewFind(t, h.tester, name, m.SysManagerStatusUnsupported())
				overviewFind(t, h.tester, name, m.SysManagerReasonOS())
				overviewMissing(t, h.tester, name, m.SysManagerRetry())
				overviewMissing(t, h.tester, name, m.SysManagerOverviewCPU()+" card")
			},
		},
		{
			name: "telnet",
			host: domainvault.Host{
				ID: model.HostID("h1"), Label: "Alpha", Hostname: "host-1",
				Protocol: domainvault.ProtocolTelnet,
			},
			reached: func(state appsysmanager.TabState) bool {
				return state.Status == appsysmanager.StatusUnavailable
			},
			verify: func(t *testing.T, h *overviewHarness, m locales.Messages, name string) {
				overviewFind(t, h.tester, name, m.SysManagerStatusUnavailable())
				overviewFind(t, h.tester, name, m.SysManagerReasonNoExec())
				overviewMissing(t, h.tester, name, m.SysManagerRetry())
				overviewMissing(t, h.tester, name, m.SysManagerOverviewCPU()+" card")
				if opens, _, _ := h.transport.counts(); opens != 0 {
					t.Fatalf("%s: a Telnet Host opened %d transports, want none", name, opens)
				}
				if commands := h.transport.received(); len(commands) != 0 {
					t.Fatalf("%s: a Telnet Host ran %d exec commands, want none: %q",
						name, len(commands), commands)
				}
			},
		},
		{
			name: "single-channel",
			prepare: func(transport *overviewTransport) {
				transport.single = true
			},
			reached: func(state appsysmanager.TabState) bool {
				return state.Status == appsysmanager.StatusUnavailable
			},
			verify: func(t *testing.T, h *overviewHarness, m locales.Messages, name string) {
				overviewFind(t, h.tester, name, m.SysManagerStatusUnavailable())
				overviewFind(t, h.tester, name, m.SysManagerReasonNoExec())
				overviewMissing(t, h.tester, name, m.SysManagerRetry())
				overviewMissing(t, h.tester, name, m.SysManagerOverviewCPU()+" card")
				if commands := h.transport.received(); len(commands) != 0 {
					t.Fatalf("%s: a single-channel Host ran %d exec commands, want none: %q",
						name, len(commands), commands)
				}
			},
		},
	}
}

// TestSysManagerOverviewNoCardStatesEverywhere proves loading, an empty
// answer, a blocking error, an unsupported operating system, and a
// single-channel Host each render their typed state, and that Retry appears
// only where retrying can change the answer.
func TestSysManagerOverviewNoCardStatesEverywhere(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				for _, test := range overviewBlockingCases() {
					name := fmt.Sprintf("%s/%s/%s/%s", surface, style.name, locale, test.name)
					t.Run(name, func(t *testing.T) {
						overviewUseStyle(t, style.style)
						transport := newOverviewTransport()
						if test.prepare != nil {
							test.prepare(transport)
						}
						host := test.host
						if host.ID == "" {
							host = overviewDefaultHost()
						}
						h := newOverviewHarnessWith(t, surface, locale, transport, 60, host)
						overviewWaitState(t, h, test.name, test.reached)
						test.verify(t, h, locales.In(locale), name)
					})
				}
			}
		}
	}
}

// TestSysManagerOverviewCachedCardsSurviveAFailedRefreshEverywhere proves a
// failure after a live sample keeps the cards visible with an inline reason and
// Retry, and that Retry runs a new Stats Sample and returns the card to live.
func TestSysManagerOverviewCachedCardsSurviveAFailedRefreshEverywhere(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				name := fmt.Sprintf("%s/%s/%s", surface, style.name, locale)
				t.Run(name, func(t *testing.T) {
					overviewUseStyle(t, style.style)
					transport := newOverviewTransport(overviewDarwin37Reply, overviewDarwin55Reply)
					h := newOverviewHarness(t, surface, locale, transport)
					first := overviewWaitState(t, h, "the live sample", overviewLive)
					if first.Stats.SampledAt.IsZero() {
						t.Fatalf("%s: the live sample has no SampledAt", name)
					}

					transport.setExecError(&hoststats.Error{
						Reason: hoststats.ReasonNetwork,
						Cause:  errors.New("connection reset"),
					})
					h.clock.Advance(5 * time.Second)
					h.manager.Refresh()
					failedAt := first.Stats.SampledAt.Add(5 * time.Second)
					failed := overviewWaitState(t, h, "the failed refresh", func(state appsysmanager.TabState) bool {
						return state.Status == appsysmanager.StatusError
					})
					if !failed.Stats.SampledAt.Equal(first.Stats.SampledAt) {
						t.Fatalf("%s: SampledAt = %v after the failure, want the cached %v",
							name, failed.Stats.SampledAt, first.Stats.SampledAt)
					}
					if !failed.At.Equal(failedAt) {
						t.Fatalf("%s: the state time = %v, want the failure's %v",
							name, failed.At, failedAt)
					}

					m := locales.In(locale)
					card := overviewFind(t, h.tester, name, m.SysManagerOverviewCPU()+" card")
					overviewFind(t, h.tester, name, "37%")
					overviewFind(t, h.tester, name, m.SysManagerReasonNetwork())
					retry := overviewFind(t, h.tester, name, m.SysManagerRetry())
					if retry.Y+retry.H > card.Y+0.5 {
						t.Fatalf("%s: Retry at %+v is not above the cards %+v", name, retry, card)
					}

					_, beforeStats, _ := transport.counts()
					transport.setExecError(nil)
					h.clock.Advance(5 * time.Second)
					retriedAt := first.Stats.SampledAt.Add(10 * time.Second)
					if err := h.tester.Click(m.SysManagerRetry()); err != nil {
						t.Fatalf("%s: click Retry: %v", name, err)
					}
					refreshed := overviewWaitState(t, h, "the retried live sample", func(state appsysmanager.TabState) bool {
						return overviewLive(state) && state.Stats.SampledAt.Equal(retriedAt)
					})
					if refreshed.Stats.CPU != 55 {
						t.Fatalf("%s: CPU after Retry = %v, want the new sample's 55",
							name, refreshed.Stats.CPU)
					}
					overviewFind(t, h.tester, name, "55%")
					overviewMissing(t, h.tester, name, m.SysManagerReasonNetwork())
					overviewMissing(t, h.tester, name, m.SysManagerRetry())
					if _, afterStats, _ := transport.counts(); afterStats != beforeStats+1 {
						t.Fatalf("%s: Retry ran %d Stats Sample commands, want one new (%d -> %d)",
							name, afterStats, beforeStats, afterStats)
					}
				})
			}
		}
	}
}

// TestSysManagerOverviewUnknownCPUAndDeltaEverywhere proves a Linux first
// sample draws an unknown CPU as a dash with no sparkline point, and a second
// sample after Refresh turns it into a live percent and a history point.
func TestSysManagerOverviewUnknownCPUAndDeltaEverywhere(t *testing.T) {
	firstReply := overviewLinuxReply(1000, 500, 1000, 500, 1000, 2000, 100, 100, 100)
	secondReply := overviewLinuxReply(2000, 1000, 2000, 1000, 3000, 4000, 110, 300, 200)
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				name := fmt.Sprintf("%s/%s/%s", surface, style.name, locale)
				t.Run(name, func(t *testing.T) {
					overviewUseStyle(t, style.style)
					transport := newOverviewTransport(firstReply, secondReply)
					transport.diskOutputs = []string{overviewDiskReply, overviewDiskReply}
					h := newOverviewHarness(t, surface, locale, transport)
					first := overviewWaitState(t, h, "the first Linux sample", func(state appsysmanager.TabState) bool {
						return overviewLive(state) && state.Stats.SampledAt.Equal(overviewBaseTime)
					})
					if !math.IsNaN(first.Stats.CPU) {
						t.Fatalf("%s: the first Linux CPU = %v, want unknown", name, first.Stats.CPU)
					}

					m := locales.In(locale)
					card := overviewFind(t, h.tester, name, m.SysManagerOverviewCPU()+" card")
					dash := overviewFind(t, h.tester, name, m.SysManagerUnknown())
					if !overviewInside(card, dash) {
						t.Fatalf("%s: the unknown CPU dash %+v is outside the card %+v",
							name, dash, card)
					}
					overviewMissing(t, h.tester, name, "0%")
					overviewFind(t, h.tester, name, m.SysManagerOverviewCores(2))
					overviewFind(t, h.tester, name, m.SysManagerOverviewCPU()+" gauge")
					overviewFind(t, h.tester, name, m.SysManagerOverviewCPU()+" history")
					if history := h.manager.CPUHistory(); len(history) != 0 {
						t.Fatalf("%s: history for an unknown CPU = %v, want none", name, history)
					}
					commands := transport.received()
					if len(commands) != 2 ||
						commands[0] != hoststats.StatsCommand() ||
						commands[1] != hoststats.DiskCommand() {
						t.Fatalf("%s: a Linux collection ran %d commands, want the Stats Sample then the disk probe: %q",
							name, len(commands), commands)
					}

					h.clock.Advance(10 * time.Second)
					secondAt := overviewBaseTime.Add(10 * time.Second)
					h.manager.Refresh()
					second := overviewWaitState(t, h, "the second Linux sample", func(state appsysmanager.TabState) bool {
						return overviewLive(state) && state.Stats.SampledAt.Equal(secondAt)
					})
					if second.Stats.CPU != 50 {
						t.Fatalf("%s: the second Linux CPU = %v, want the 50%% delta",
							name, second.Stats.CPU)
					}
					overviewFind(t, h.tester, name, "50%")
					history := h.manager.CPUHistory()
					if len(history) != 1 || history[0] != 50 {
						t.Fatalf("%s: history after the delta = %v, want [50]", name, history)
					}
					if _, statsCalls, diskCalls := transport.counts(); statsCalls != 2 || diskCalls != 2 {
						t.Fatalf("%s: collections = %d stats and %d disk, want two of each",
							name, statsCalls, diskCalls)
					}
				})
			}
		}
	}
}

// TestSysManagerOverviewStatusAgeComesFromSampledAt proves a manual refresh
// inside the cache window republishes the cached sample: the state time moves,
// SampledAt does not, and the status row's age follows SampledAt.
func TestSysManagerOverviewStatusAgeComesFromSampledAt(t *testing.T) {
	for _, style := range overviewStyles {
		for _, locale := range overviewLocales {
			name := fmt.Sprintf("full-page/%s/%s", style.name, locale)
			t.Run(name, func(t *testing.T) {
				overviewUseStyle(t, style.style)
				transport := newOverviewTransport(overviewDarwin37Reply)
				h := newOverviewHarness(t, overviewFullPage, locale, transport,
					hoststats.WithCacheTTL(time.Hour))
				first := overviewWaitState(t, h, "the live sample", overviewLive)
				m := locales.In(locale)
				overviewFind(t, h.tester, name,
					m.SysManagerUpdatedSeconds(int(overviewSampleAge/time.Second)))

				h.clock.Advance(time.Second)
				refreshedAt := first.Stats.SampledAt.Add(time.Second)
				h.manager.Refresh()
				overviewWaitState(t, h, "the cached re-publication", func(state appsysmanager.TabState) bool {
					return overviewLive(state) &&
						state.At.Equal(refreshedAt) &&
						state.Stats.SampledAt.Equal(first.Stats.SampledAt)
				})
				// The state time is one second newer, but the age still reads the
				// sample time, so it stays at the fixed surface offset.
				overviewFind(t, h.tester, name,
					m.SysManagerUpdatedSeconds(int(overviewSampleAge/time.Second)))
				overviewMissing(t, h.tester, name,
					m.SysManagerUpdatedSeconds(int(overviewSampleAge/time.Second)-1))
				if _, statsCalls, _ := transport.counts(); statsCalls != 1 {
					t.Fatalf("%s: a refresh inside the cache window ran %d Stats Sample commands, want the cached one",
						name, statsCalls)
				}
			})
		}
	}
}

// TestSysManagerOverviewSidePanelPollsOnlyWhileVisible proves the System Tool
// samples only while it is the side panel's chosen tool: the collector runs a
// second Stats Sample on its own interval, choosing Notes stops the loop and
// closes the Host connection, and choosing System again resumes with a fresh
// sample.
func TestSysManagerOverviewSidePanelPollsOnlyWhileVisible(t *testing.T) {
	for _, style := range overviewStyles {
		t.Run(style.name, func(t *testing.T) {
			overviewUseStyle(t, style.style)
			transport := newOverviewTransport(overviewDarwin37Reply, overviewDarwin55Reply)
			h := newOverviewHarnessWith(t, overviewSidePanel, "en", transport, 1, overviewDefaultHost())
			first := overviewWaitState(t, h, "the live sample", overviewLive)
			if first.Stats.CPU != 37 {
				t.Fatalf("the first sample CPU = %v, want 37", first.Stats.CPU)
			}

			// The panel is visible, so the collector's own one-second interval
			// produces another Stats Sample. This is a real condition, not a
			// wait on elapsed time.
			overviewFrameUntil(t, h.tester, "the collector's next visible poll", func() bool {
				_, stats, _ := transport.counts()
				return stats >= 2
			})

			m := locales.In("en")
			if err := h.tester.Click(m.SidePanelNotes()); err != nil {
				t.Fatalf("choose the Notes tool: %v", err)
			}
			// Finishing the frame hides the System Tool, which stops its
			// collection loop and closes the Host connection. Nothing can run on
			// that loop afterwards.
			overviewFrameUntil(t, h.tester, "the hidden tool's connection to close", func() bool {
				return transport.closeCount() >= 1
			})
			_, hiddenStats, _ := transport.counts()

			// The new sample arrives only after the tool is drawn again.
			h.clock.Advance(10 * time.Second)
			resumedAt := overviewBaseTime.Add(10 * time.Second)
			if err := h.tester.Click(m.SidePanelSystem()); err != nil {
				t.Fatalf("choose the System tool: %v", err)
			}
			resumed := overviewWaitState(t, h, "the resumed live sample", func(state appsysmanager.TabState) bool {
				return overviewLive(state) && state.Stats.SampledAt.Equal(resumedAt)
			})
			if resumed.Stats.CPU != 55 {
				t.Fatalf("the resumed CPU = %v, want the new sample's 55", resumed.Stats.CPU)
			}
			if _, afterStats, _ := transport.counts(); afterStats <= hiddenStats {
				t.Fatalf("Stats Sample commands = %d before the tool was shown again and %d after, want a new one",
					hiddenStats, afterStats)
			}

			// Closing the whole panel hides the System Tool the same way.
			closedBefore := transport.closeCount()
			h.shell.CloseFiles(h.tab)
			overviewFrameUntil(t, h.tester, "the closed panel's connection to close", func() bool {
				return transport.closeCount() > closedBefore
			})
			_, closedStats, _ := transport.counts()

			// Reopening the panel shows the chosen System Tool again and
			// resumes sampling with a fresh state.
			h.clock.Advance(10 * time.Second)
			reopenedAt := overviewBaseTime.Add(20 * time.Second)
			if !h.shell.ToggleActiveFiles() {
				t.Fatal("the side panel did not reopen")
			}
			reopened := overviewWaitState(t, h, "the reopened live sample", func(state appsysmanager.TabState) bool {
				return overviewLive(state) && state.Stats.SampledAt.Equal(reopenedAt)
			})
			if reopened.Stats.CPU != 55 {
				t.Fatalf("the reopened CPU = %v, want the new sample's 55", reopened.Stats.CPU)
			}
			if _, afterStats, _ := transport.counts(); afterStats <= closedStats {
				t.Fatalf("Stats Sample commands = %d before the panel was reopened and %d after, want a new one",
					closedStats, afterStats)
			}
		})
	}
}
