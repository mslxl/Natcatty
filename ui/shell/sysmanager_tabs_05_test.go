package shell

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
)

// This file drives the Processes, Ports, and Services filters and action
// conditions through the real shell, plus the typed unavailable panels of
// every System Tab. Each case runs in both surfaces, both Interface Styles,
// and en and zh-CN, over a real Service whose Connector is an in-process fake.

const (
	otherTabsProcessesReply = `    1     0 root     Ss    0.0  0.1  1234  5678  1-02:03:04 /sbin/init
  100     1 root     Ss    0.0  0.1  1000  2000  00:10:00 parent-daemon
  101   100 root     R     1.0  0.2  1000  2000  00:01:00 child-runner
  102   100 app      S     0.5  0.1  1000  2000  00:02:00 child-sleeper
  103     1 app      R+    2.0  0.3  1000  2000  00:03:00 busy-worker`

	otherTabsPortsReply = `__NC_PORTS_BEGIN__
__NC_NETSTAT__
tcp 0 0 0.0.0.0:22 0.0.0.0:* LISTEN 123/sshd
udp 0 0 127.0.0.1:53 0.0.0.0:* 456/dnsmasq
tcp6 0 0 ::1.8443 *.* LISTEN
udp6 0 0 fe80::1%lo0.5353 *.*
__NC_PORTS_END__`

	otherTabsServicesReply = `__NC_SERVICES_BEGIN__
__NC_SYSTEM__
nginx.service loaded active running A high performance web server
● broken.service loaded failed failed Broken unit
db.service loaded inactive dead Database
boot.service loaded activating start Boot unit
__NC_USER__
agent.service loaded active running User agent
__NC_SERVICES_END__`

	otherTabsDockerDaemonReply = `__NC_DOCKER_BEGIN__
__NC_DOCKER_CONTAINERS__
__NC_DOCKER_ERROR_BEGIN__
Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?
__NC_DOCKER_ERROR_END__
__NC_DOCKER_IMAGES__
__NC_DOCKER_ERROR_BEGIN__
Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?
__NC_DOCKER_ERROR_END__
__NC_DOCKER_END__`

	otherTabsGPUEmptyReply = "__NC_GPU_BEGIN__\n__NC_GPU_END__"
)

// otherTabsConnector answers the capability probe and each tab's command from
// fixed fixtures. It records commands so a test can prove Retry ran another
// Docker sample.
type otherTabsConnector struct {
	mu sync.Mutex

	osName string
	tools  map[string]bool
	docker int
}

func newOtherTabsConnector() *otherTabsConnector {
	return &otherTabsConnector{
		osName: "Linux",
		tools: map[string]bool{
			"ps": true, "ss": true, "systemctl": true, "tmux": true,
		},
	}
}

func (c *otherTabsConnector) Open(context.Context, domainvault.Host) (appsysmanager.Connection, error) {
	return &otherTabsConnection{connector: c}, nil
}

func (c *otherTabsConnector) dockerRuns() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.docker
}

func (c *otherTabsConnector) probeOutput() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out strings.Builder
	fmt.Fprintf(&out, "__NC_OS=%s\n", c.osName)
	for _, tool := range []string{"ps", "top", "ss", "netstat", "lsof",
		"systemctl", "tmux", "docker", "nvidia-smi"} {
		if c.tools[tool] {
			fmt.Fprintf(&out, "__NC_HAS_%s=1\n", tool)
		}
	}
	return out.String()
}

type otherTabsConnection struct {
	connector *otherTabsConnector
}

func (c *otherTabsConnection) Exec(_ context.Context, command string) (appsysmanager.ExecResult, error) {
	connector := c.connector
	switch {
	case strings.Contains(command, "__NC_OS="):
		return appsysmanager.ExecResult{Stdout: connector.probeOutput()}, nil
	case strings.Contains(command, "ps -eo"):
		return appsysmanager.ExecResult{Stdout: otherTabsProcessesReply}, nil
	case strings.Contains(command, "__NC_PORTS_BEGIN__"):
		return appsysmanager.ExecResult{Stdout: otherTabsPortsReply}, nil
	case strings.Contains(command, "__NC_SERVICES_BEGIN__"):
		return appsysmanager.ExecResult{Stdout: otherTabsServicesReply}, nil
	case strings.Contains(command, "__NC_DOCKER_BEGIN__"):
		connector.mu.Lock()
		connector.docker++
		connector.mu.Unlock()
		return appsysmanager.ExecResult{Stdout: otherTabsDockerDaemonReply}, nil
	case strings.Contains(command, "__NC_GPU_BEGIN__"):
		return appsysmanager.ExecResult{Stdout: otherTabsGPUEmptyReply}, nil
	case strings.Contains(command, "__NC_TMUX_BEGIN__"):
		return appsysmanager.ExecResult{Stdout: "__NC_TMUX_BEGIN__\n__NC_TMUX_END__"}, nil
	}
	return appsysmanager.ExecResult{}, fmt.Errorf("unexpected command %q", command)
}

func (c *otherTabsConnection) Close() error { return nil }

func otherTabsHarness(t *testing.T, surface overviewSurface, locale string,
	connector *otherTabsConnector) *overviewHarness {
	t.Helper()
	h := newOverviewHarnessWithConnector(t, surface, locale,
		newOverviewTransport(overviewDarwin37Reply), 60, overviewDefaultHost(), connector)
	overviewWaitState(t, h, "the live Overview", overviewLive)
	return h
}

func otherTabsWait(t *testing.T, h *overviewHarness, name string, condition func() bool) {
	t.Helper()
	overviewFrameUntil(t, h.tester, name, condition)
}

func otherTabsShows(tester *ui.Tester, label string) bool {
	rect, ok := tester.Find(label)
	return ok && rect.W > 0 && rect.H > 0
}

func otherTabsTabState(h *overviewHarness, tab appsysmanager.Tab) appsysmanager.TabState {
	return h.manager.TabState(tab)
}

// otherTabsSelect clicks a row and waits until the surface chose it.
func otherTabsSelect(t *testing.T, h *overviewHarness, name, row string, chosen func() bool) {
	t.Helper()
	if err := h.tester.Click(row); err != nil {
		t.Fatalf("%s: click %q: %v", name, row, err)
	}
	otherTabsWait(t, h, name+" selects "+row, chosen)
}

// TestSysManagerOtherTabFiltersEverywhere drives the new narrowing controls
// through the rendered surface in both surfaces, both Interface Styles, and en
// and zh-CN: running-only on Processes, PPID search, the TCP/UDP filter with
// selection, the service state filter with failed-first order, and the action
// conditions for each unit state.
func TestSysManagerOtherTabFiltersEverywhere(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				name := fmt.Sprintf("%s/%s/%s", surface, style.name, locale)
				t.Run(name, func(t *testing.T) {
					overviewUseStyle(t, style.style)
					m := locales.In(locale)
					h := otherTabsHarness(t, surface, locale, newOtherTabsConnector())

					otherTabsProcesses(t, h, m, name)
					otherTabsPorts(t, h, m, name)
					otherTabsServices(t, h, m, name)
				})
			}
		}
	}
}

func otherTabsProcesses(t *testing.T, h *overviewHarness, m locales.Messages, name string) {
	t.Helper()
	h.manager.SetTab(appsysmanager.TabProcesses)
	otherTabsWait(t, h, name+" live Processes", func() bool {
		state := otherTabsTabState(h, appsysmanager.TabProcesses)
		return state.Status == appsysmanager.StatusLive && len(state.Processes) == 5
	})

	all := []string{
		"PID 1 /sbin/init", "PID 100 parent-daemon", "PID 101 child-runner",
		"PID 102 child-sleeper", "PID 103 busy-worker",
	}
	for _, row := range all {
		overviewFind(t, h.tester, name, row)
	}

	if err := h.tester.Click(m.SysManagerRunningOnly()); err != nil {
		t.Fatalf("%s: click Running only: %v", name, err)
	}
	otherTabsWait(t, h, name+" running-only rows", func() bool {
		return h.manager.ProcessRunningOnly() &&
			otherTabsShows(h.tester, "PID 101 child-runner") &&
			otherTabsShows(h.tester, "PID 103 busy-worker") &&
			!otherTabsShows(h.tester, "PID 1 /sbin/init") &&
			!otherTabsShows(h.tester, "PID 100 parent-daemon") &&
			!otherTabsShows(h.tester, "PID 102 child-sleeper")
	})

	if err := h.tester.Click(m.SysManagerRunningOnly()); err != nil {
		t.Fatalf("%s: click Running only off: %v", name, err)
	}
	otherTabsWait(t, h, name+" full process list restored", func() bool {
		if h.manager.ProcessRunningOnly() {
			return false
		}
		for _, row := range all {
			if !otherTabsShows(h.tester, row) {
				return false
			}
		}
		return true
	})

	// The search field's placeholder names the field in the rendered surface;
	// clicking it focuses the input so typing drives the real filter path.
	overviewFind(t, h.tester, name, m.SysManagerSearchProcesses())
	if err := h.tester.Click(m.SysManagerSearchProcesses()); err != nil {
		t.Fatalf("%s: focus the process search: %v", name, err)
	}
	h.tester.Type("100")
	otherTabsWait(t, h, name+" PPID search", func() bool {
		return h.manager.Search() == "100" &&
			otherTabsShows(h.tester, "PID 100 parent-daemon") &&
			otherTabsShows(h.tester, "PID 101 child-runner") &&
			otherTabsShows(h.tester, "PID 102 child-sleeper") &&
			!otherTabsShows(h.tester, "PID 1 /sbin/init") &&
			!otherTabsShows(h.tester, "PID 103 busy-worker")
	})
}

func otherTabsPorts(t *testing.T, h *overviewHarness, m locales.Messages, name string) {
	t.Helper()
	h.manager.SetTab(appsysmanager.TabPorts)
	otherTabsWait(t, h, name+" live Ports", func() bool {
		state := otherTabsTabState(h, appsysmanager.TabPorts)
		return state.Status == appsysmanager.StatusLive && len(state.Ports) == 4
	})

	all := []string{"tcp *:22", "udp 127.0.0.1:53", "tcp6 ::1:8443", "udp6 fe80::1:5353"}
	for _, row := range all {
		overviewFind(t, h.tester, name, row)
	}

	if err := h.tester.Click(m.SysManagerFilterTCP()); err != nil {
		t.Fatalf("%s: click TCP: %v", name, err)
	}
	otherTabsWait(t, h, name+" TCP rows", func() bool {
		return h.manager.PortFilter() == appsysmanager.PortFilterTCP &&
			otherTabsShows(h.tester, "tcp *:22") &&
			otherTabsShows(h.tester, "tcp6 ::1:8443") &&
			!otherTabsShows(h.tester, "udp 127.0.0.1:53") &&
			!otherTabsShows(h.tester, "udp6 fe80::1:5353")
	})

	// Selection keeps working inside the narrowed list.
	otherTabsSelect(t, h, name+" TCP selection", "tcp6 ::1:8443", func() bool {
		port, ok := h.manager.SelectedPort()
		return ok && port.Port == 8443 && port.Protocol == "tcp6"
	})

	if err := h.tester.Click(m.SysManagerFilterUDP()); err != nil {
		t.Fatalf("%s: click UDP: %v", name, err)
	}
	otherTabsWait(t, h, name+" UDP rows", func() bool {
		return h.manager.PortFilter() == appsysmanager.PortFilterUDP &&
			otherTabsShows(h.tester, "udp 127.0.0.1:53") &&
			otherTabsShows(h.tester, "udp6 fe80::1:5353") &&
			!otherTabsShows(h.tester, "tcp *:22") &&
			!otherTabsShows(h.tester, "tcp6 ::1:8443")
	})

	if err := h.tester.Click(m.SysManagerFilterAll()); err != nil {
		t.Fatalf("%s: click All ports: %v", name, err)
	}
	otherTabsWait(t, h, name+" all ports restored", func() bool {
		if h.manager.PortFilter() != appsysmanager.PortFilterAll {
			return false
		}
		for _, row := range all {
			if !otherTabsShows(h.tester, row) {
				return false
			}
		}
		return true
	})
}

func otherTabsServices(t *testing.T, h *overviewHarness, m locales.Messages, name string) {
	t.Helper()
	h.manager.SetTab(appsysmanager.TabServices)
	otherTabsWait(t, h, name+" live Services", func() bool {
		state := otherTabsTabState(h, appsysmanager.TabServices)
		return state.Status == appsysmanager.StatusLive && len(state.Services) == 5
	})

	all := []string{"broken.service", "agent.service", "boot.service", "db.service", "nginx.service"}
	for _, row := range all {
		overviewFind(t, h.tester, name, row)
	}
	// The parser merges system and user scopes and keeps the failed unit first.
	failed := overviewFind(t, h.tester, name, "broken.service")
	for _, row := range all[1:] {
		rect := overviewFind(t, h.tester, name, row)
		if rect.Y <= failed.Y {
			t.Fatalf("%s: %q is at %.0f, not below the failed unit at %.0f",
				name, row, rect.Y, failed.Y)
		}
	}

	if err := h.tester.Click(m.SysManagerFilterRunning()); err != nil {
		t.Fatalf("%s: click Running services: %v", name, err)
	}
	otherTabsWait(t, h, name+" running services", func() bool {
		return h.manager.ServiceFilter() == appsysmanager.ServiceFilterRunning &&
			otherTabsShows(h.tester, "nginx.service") &&
			otherTabsShows(h.tester, "agent.service") &&
			!otherTabsShows(h.tester, "broken.service") &&
			!otherTabsShows(h.tester, "db.service") &&
			!otherTabsShows(h.tester, "boot.service")
	})

	if err := h.tester.Click(m.SysManagerFilterFailed()); err != nil {
		t.Fatalf("%s: click Failed services: %v", name, err)
	}
	otherTabsWait(t, h, name+" failed services", func() bool {
		return h.manager.ServiceFilter() == appsysmanager.ServiceFilterFailed &&
			otherTabsShows(h.tester, "broken.service") &&
			!otherTabsShows(h.tester, "nginx.service") &&
			!otherTabsShows(h.tester, "db.service")
	})

	if err := h.tester.Click(m.SysManagerFilterInactive()); err != nil {
		t.Fatalf("%s: click Inactive services: %v", name, err)
	}
	otherTabsWait(t, h, name+" inactive services", func() bool {
		return h.manager.ServiceFilter() == appsysmanager.ServiceFilterInactive &&
			otherTabsShows(h.tester, "db.service") &&
			!otherTabsShows(h.tester, "broken.service") &&
			!otherTabsShows(h.tester, "nginx.service")
	})

	if err := h.tester.Click(m.SysManagerFilterAll()); err != nil {
		t.Fatalf("%s: click All services: %v", name, err)
	}
	otherTabsWait(t, h, name+" all services restored", func() bool {
		if h.manager.ServiceFilter() != appsysmanager.ServiceFilterAll {
			return false
		}
		for _, row := range all {
			if !otherTabsShows(h.tester, row) {
				return false
			}
		}
		return true
	})

	// Start is offered only for inactive or failed units, Stop only for active
	// units, and Restart always; the rendered bar follows the selected unit.
	cases := []struct {
		unit        string
		start, stop bool
	}{
		{"broken.service", true, false},
		{"db.service", true, false},
		{"nginx.service", false, true},
		{"agent.service", false, true},
		{"boot.service", false, false},
	}
	for _, test := range cases {
		otherTabsSelect(t, h, name+" action for "+test.unit, test.unit, func() bool {
			unit, ok := h.manager.SelectedService()
			return ok && unit.Name == test.unit
		})
		overviewFrameUntil(t, h.tester, name+" actions for "+test.unit, func() bool {
			_, hasStart := h.tester.Find(m.SysManagerStart())
			_, hasStop := h.tester.Find(m.SysManagerStop())
			_, hasRestart := h.tester.Find(m.SysManagerRestart())
			return hasStart == test.start && hasStop == test.stop && hasRestart
		})
	}
}

// TestSysManagerUnavailableTabsEverywhere proves every System Tab keeps its
// typed unavailable panel, reason, and place in the strip when the Host
// cannot run its tool, with no Retry where retrying cannot help. GPU keeps its
// unsupported-OS reason and shows no invented empty state.
func TestSysManagerUnavailableTabsEverywhere(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				name := fmt.Sprintf("%s/%s/%s", surface, style.name, locale)
				t.Run(name, func(t *testing.T) {
					overviewUseStyle(t, style.style)
					connector := newOtherTabsConnector()
					connector.osName = "FreeBSD"
					connector.tools = map[string]bool{}
					h := otherTabsHarness(t, surface, locale, connector)
					m := locales.In(locale)

					cases := []struct {
						tab        appsysmanager.Tab
						status     appsysmanager.Status
						reason     appsysmanager.Reason
						statusText string
						reasonText string
						label      string
					}{
						{appsysmanager.TabProcesses, appsysmanager.StatusUnavailable,
							appsysmanager.ReasonMissingProcesses, m.SysManagerStatusUnavailable(),
							m.SysManagerReasonMissingProcesses(), m.SysManagerTabProcesses()},
						{appsysmanager.TabPorts, appsysmanager.StatusUnavailable,
							appsysmanager.ReasonMissingPorts, m.SysManagerStatusUnavailable(),
							m.SysManagerReasonMissingPorts(), m.SysManagerTabPorts()},
						{appsysmanager.TabServices, appsysmanager.StatusUnavailable,
							appsysmanager.ReasonMissingSystemctl, m.SysManagerStatusUnavailable(),
							m.SysManagerReasonMissingSystemctl(), m.SysManagerTabServices()},
						{appsysmanager.TabTmux, appsysmanager.StatusUnavailable,
							appsysmanager.ReasonMissingTmux, m.SysManagerStatusUnavailable(),
							m.SysManagerReasonMissingTmux(), m.SysManagerTabTmux()},
						{appsysmanager.TabDocker, appsysmanager.StatusUnavailable,
							appsysmanager.ReasonMissingDocker, m.SysManagerStatusUnavailable(),
							m.SysManagerReasonMissingDocker(), m.SysManagerTabDocker()},
						{appsysmanager.TabGPU, appsysmanager.StatusUnsupported,
							appsysmanager.ReasonOS, m.SysManagerStatusUnsupported(),
							m.SysManagerReasonOS(), m.SysManagerTabGPU()},
					}
					for _, test := range cases {
						caseName := fmt.Sprintf("%s %v", name, test.tab)
						h.manager.SetTab(test.tab)
						otherTabsWait(t, h, caseName+" unavailable", func() bool {
							state := otherTabsTabState(h, test.tab)
							return state.Status == test.status && state.Reason == test.reason
						})
						overviewFind(t, h.tester, caseName+" tab", test.label)
						overviewFind(t, h.tester, caseName+" status", test.statusText)
						overviewFind(t, h.tester, caseName+" reason", test.reasonText)
						overviewMissing(t, h.tester, caseName+" retry", m.SysManagerRetry())
						if test.tab == appsysmanager.TabGPU {
							overviewMissing(t, h.tester, name+" GPU empty", m.SysManagerGPUEmpty())
						}
					}
				})
			}
		}
	}
}

// TestSysManagerDockerDaemonRetryAndGPUEmptyEverywhere proves Retry appears
// where it can help (a stopped Docker daemon) and runs another sample, while a
// Host without an accelerator tool keeps the GPU tab live with its existing
// empty state and no Retry.
func TestSysManagerDockerDaemonRetryAndGPUEmptyEverywhere(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				name := fmt.Sprintf("%s/%s/%s", surface, style.name, locale)
				t.Run(name, func(t *testing.T) {
					overviewUseStyle(t, style.style)
					connector := newOtherTabsConnector()
					connector.tools["docker"] = true
					h := otherTabsHarness(t, surface, locale, connector)
					m := locales.In(locale)

					h.manager.SetTab(appsysmanager.TabDocker)
					otherTabsWait(t, h, name+" Docker daemon reason", func() bool {
						state := otherTabsTabState(h, appsysmanager.TabDocker)
						return state.Status == appsysmanager.StatusUnavailable &&
							state.Reason == appsysmanager.ReasonDockerDaemon
					})
					overviewFind(t, h.tester, name, m.SysManagerReasonDockerDaemon())
					overviewFind(t, h.tester, name, m.SysManagerRetry())
					before := connector.dockerRuns()
					if err := h.tester.Click(m.SysManagerRetry()); err != nil {
						t.Fatalf("%s: click Retry: %v", name, err)
					}
					otherTabsWait(t, h, name+" Docker retry", func() bool {
						return connector.dockerRuns() > before
					})

					h.manager.SetTab(appsysmanager.TabGPU)
					otherTabsWait(t, h, name+" live empty GPU", func() bool {
						state := otherTabsTabState(h, appsysmanager.TabGPU)
						return state.Status == appsysmanager.StatusLive &&
							len(state.GPU.Devices) == 0
					})
					overviewFind(t, h.tester, name, m.SysManagerGPUEmpty())
					overviewMissing(t, h.tester, name+" GPU retry", m.SysManagerRetry())
				})
			}
		}
	}
}

// TestSysManagerProcessPPIDColumnFitsTheMinimumWindow proves the new PPID
// header, and the Command header it shares the row with, still fit the content
// column at the minimum window in both Interface Styles and both locales.
func TestSysManagerProcessPPIDColumnFitsTheMinimumWindow(t *testing.T) {
	for _, style := range overviewStyles {
		for _, locale := range overviewLocales {
			name := fmt.Sprintf("%s/%s", style.name, locale)
			t.Run(name, func(t *testing.T) {
				overviewUseStyle(t, style.style)
				tester, _ := newSysManagerLayoutTester(t, sysManagerMinimumWidth,
					sysManagerMinimumHeight, locale, appsysmanager.TabProcesses, 4)
				m := locales.In(locale)
				left, right := sysManagerContentBounds(sysManagerMinimumWidth)
				for _, header := range []string{
					m.SysManagerPid(), m.SysManagerPpid(), m.SysManagerCommand(),
				} {
					rect, ok := tester.Find(header)
					if !ok || rect.W <= 0 {
						t.Fatalf("%s: the %q header is not rendered: %v",
							name, header, tester.Texts())
					}
					if rect.X < left-0.5 || rect.X+rect.W > right+0.5 {
						t.Fatalf("%s: the %q header %.0f..%.0f crosses the content column %.0f..%.0f",
							name, header, rect.X, rect.X+rect.W, left, right)
					}
				}
			})
		}
	}
}
