package sysmanager

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestParseCapabilitiesReadsOnlyLineAnchoredMarkers(t *testing.T) {
	stdout := "noise __NC_HAS_ss=1\n" +
		"__NC_OS=Linux\n" +
		"__NC_HAS_ps=1\n" +
		"__NC_HAS_ss=1\r\n" +
		"__NC_HAS_systemctl=1\n"
	caps := parseCapabilities(stdout)
	if caps.OS != "linux" {
		t.Errorf("OS = %q, want linux", caps.OS)
	}
	if !caps.HasPs || !caps.HasSs || !caps.HasSystemctl {
		t.Errorf("capabilities = %+v, want ps, ss, systemctl", caps)
	}
	if caps.HasTop || caps.HasNetstat || caps.HasLsof {
		t.Errorf("capabilities invented tools: %+v", caps)
	}
	if ok, reason := caps.Available(TabPorts); !ok || reason != ReasonNone {
		t.Errorf("ports availability = %v, %q", ok, reason)
	}
}

func TestParseOverviewReadsLinuxAndDarwinFields(t *testing.T) {
	linux := "MODE:linux|HOST:web-1|OS:Debian 12|KERNEL:6.1|UPTIME:86400|" +
		"LOAD:0.50 0.40 0.30|CORES:8|CPURAW:1000 500|MEM:2048 8192|SWAP:128 1024|DISK:12.5 100.0"
	sample, err := parseOverview(linux)
	if err != nil {
		t.Fatalf("parse linux overview: %v", err)
	}
	overview := sample.overview
	if overview.Hostname != "web-1" || overview.OSName != "Debian 12" ||
		overview.KernelRelease != "6.1" || overview.UptimeSeconds != 86400 ||
		overview.Cores != 8 {
		t.Errorf("overview identity = %+v", overview)
	}
	if len(overview.LoadAverage) != 3 || overview.LoadAverage[0] != 0.5 {
		t.Errorf("load = %v", overview.LoadAverage)
	}
	if overview.MemUsedMB != 2048 || overview.MemTotalMB != 8192 ||
		overview.SwapUsedMB != 128 || overview.SwapTotalMB != 1024 ||
		overview.DiskUsedGB != 12.5 || overview.DiskTotalGB != 100 {
		t.Errorf("capacity fields = %+v", overview)
	}
	if cpu := sample.cpuPercent(overviewCounters{}); !math.IsNaN(cpu) {
		t.Errorf("first Linux CPU = %v, want NaN before a delta", cpu)
	}
	sample.cpuTotal = 1200
	sample.cpuIdle = 550
	if cpu := sample.cpuPercent(overviewCounters{mode: "linux", total: 1000, idle: 500}); cpu != 75 {
		t.Errorf("derived CPU = %v, want 75", cpu)
	}

	darwin := "MODE:darwin|HOST:mac|OS:macOS 14.0|KERNEL:23.0|UPTIME:3600|" +
		"LOAD:1.00 0.50 0.25|CORES:4|CPURAW:120|MEM:4096 16384|SWAP:0 0|DISK:20.0 494.4"
	sample, err = parseOverview(darwin)
	if err != nil {
		t.Fatalf("parse darwin overview: %v", err)
	}
	if cpu := sample.cpuPercent(overviewCounters{}); cpu != 30 {
		t.Errorf("darwin CPU = %v, want 30", cpu)
	}

	if _, err := parseOverview("MODE:unsupported|OS:FreeBSD\n"); err == nil {
		t.Error("an unsupported OS produced no typed error")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Reason != ReasonOS {
			t.Errorf("unsupported OS error = %v, want reason os", err)
		}
	}
	if _, err := parseOverview("nothing here\n"); err == nil {
		t.Error("an unrecognizable overview reply produced no typed error")
	}
}

func TestParseProcessesReadsGNUAndBusyBoxShapes(t *testing.T) {
	stdout := strings.Join([]string{
		"",
		"    1     0 root     Ss    0.0  0.1  1234  5678  1-02:03:04 /sbin/init",
		" 1234     1 root     S     1.5  0.8  2048 16384  00:12:34 nginx: worker",
		"2345 root 1024 S sshd: /usr/sbin/sshd",
	}, "\n")
	processes, err := parseProcesses(stdout)
	if err != nil {
		t.Fatalf("parse processes: %v", err)
	}
	if len(processes) != 3 {
		t.Fatalf("processes = %d, want 3: %+v", len(processes), processes)
	}
	first := processes[0]
	if first.PID != 1 || first.PPID != 0 || first.User != "root" || first.Stat != "Ss" ||
		first.RSSKB != 1234 || first.VSZKB != 5678 || first.Elapsed != "1-02:03:04" ||
		first.Command != "/sbin/init" {
		t.Errorf("GNU row = %+v", first)
	}
	if processes[1].CPUPercent != 1.5 || processes[1].MemPercent != 0.8 {
		t.Errorf("GNU second row = %+v", processes[1])
	}
	if processes[2].PID != 2345 || processes[2].VSZKB != 1024 ||
		processes[2].Command != "sshd: /usr/sbin/sshd" {
		t.Errorf("BusyBox row = %+v", processes[2])
	}

	if _, err := parseProcesses("__NC_UNAVAILABLE=processes\n"); err == nil {
		t.Error("the unavailable marker produced no typed error")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Reason != ReasonMissingProcesses {
			t.Errorf("missing processes error = %v", err)
		}
	}
}

func TestParseBusyBoxTopSizeSuffix(t *testing.T) {
	processes, err := parseProcesses("42 1 root S 2.5m 3.0% 4.0% busybox-top-row")
	if err != nil {
		t.Fatalf("parse busybox top: %v", err)
	}
	if len(processes) != 1 || processes[0].VSZKB != 2560 ||
		processes[0].CPUPercent != 4.0 || processes[0].MemPercent != 3.0 {
		t.Fatalf("busybox top = %+v", processes)
	}
}

func TestParsePortsMergesSsNetstatAndLsof(t *testing.T) {
	stdout := strings.Join([]string{
		"__NC_PORTS_BEGIN__",
		"__NC_SS__",
		"Netid State Recv-Q Send-Q Local Address:Port Peer Address:Port Process",
		`tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=123,fd=3))`,
		`udp UNCONN 0 0 127.0.0.1:53 0.0.0.0:* users:(("dnsmasq",pid=456,fd=4))`,
		"__NC_NETSTAT__",
		"tcp 0 0 0.0.0.0:22 0.0.0.0:* LISTEN 123/sshd",
		"tcp 0 0 0.0.0.0:631 0.0.0.0:* -",
		"__NC_LSOF__",
		"COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME",
		"cupsd 789 root 6u IPv4 123 0t0 TCP *:631 (LISTEN)",
		"__NC_PORTS_END__",
	}, "\n")
	ports, err := parsePorts(stdout)
	if err != nil {
		t.Fatalf("parse ports: %v", err)
	}
	if len(ports) != 3 {
		t.Fatalf("ports = %d, want 3: %+v", len(ports), ports)
	}
	byPort := make(map[int]Port, len(ports))
	for _, port := range ports {
		byPort[port.Port] = port
	}
	if got := byPort[22]; got.PID != 123 || got.ProcessName != "sshd" || got.Protocol != "tcp" {
		t.Errorf("port 22 = %+v", got)
	}
	if got := byPort[53]; got.PID != 456 || got.Protocol != "udp" || got.Address != "127.0.0.1" {
		t.Errorf("port 53 = %+v", got)
	}
	if got := byPort[631]; got.PID != 789 || got.ProcessName != "cupsd" {
		t.Errorf("port 631 = %+v", got)
	}

	if _, err := parsePorts("__NC_UNAVAILABLE=ports\n"); err == nil {
		t.Error("the ports marker produced no typed error")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Reason != ReasonMissingPorts {
			t.Errorf("missing ports error = %v", err)
		}
	}
}

func TestParsePortsHandlesIPv6AndBSDNetstat(t *testing.T) {
	stdout := strings.Join([]string{
		"__NC_NETSTAT__",
		"tcp6 0 0 ::1.631 *.* LISTEN",
		"udp6 0 0 fe80::1%lo0.5353 *.*",
	}, "\n")
	ports, err := parsePorts(stdout)
	if err != nil {
		t.Fatalf("parse ports: %v", err)
	}
	if len(ports) != 2 {
		t.Fatalf("ports = %+v", ports)
	}
	if ports[0].Protocol != "tcp6" || ports[0].Port != 631 || ports[0].Address != "::1" {
		t.Errorf("bsd tcp6 = %+v", ports[0])
	}
	if ports[1].Protocol != "udp6" || ports[1].Port != 5353 || ports[1].Address != "fe80::1" {
		t.Errorf("bsd udp6 = %+v", ports[1])
	}
}

func TestParseServicesMergesScopesAndSortsFailedFirst(t *testing.T) {
	stdout := strings.Join([]string{
		"__NC_SERVICES_BEGIN__",
		"__NC_SYSTEM__",
		"nginx.service loaded active running A high performance web server",
		"● broken.service loaded failed failed Broken unit",
		"db.service loaded inactive dead Database",
		"Legend: UNIT LOAD ACTIVE SUB DESCRIPTION",
		"3 loaded units listed.",
		"__NC_USER__",
		"user.service loaded active running User unit",
		"nginx.service loaded active running Duplicate user nginx",
		"__NC_SERVICES_END__",
	}, "\n")
	units, err := parseServices(stdout)
	if err != nil {
		t.Fatalf("parse services: %v", err)
	}
	if len(units) != 4 {
		t.Fatalf("units = %d, want 4: %+v", len(units), units)
	}
	if units[0].Name != "broken.service" || units[0].ActiveState != "failed" {
		t.Errorf("first unit = %+v, want the failed unit first", units[0])
	}
	byName := make(map[string]Unit, len(units))
	for _, unit := range units {
		byName[unit.Name] = unit
	}
	if got := byName["nginx.service"]; got.Scope != "system" || got.Description != "A high performance web server" {
		t.Errorf("nginx = %+v", got)
	}
	if got := byName["user.service"]; got.Scope != "user" {
		t.Errorf("user unit = %+v", got)
	}
	if _, err := parseServices("__NC_UNAVAILABLE=systemctl\n"); err == nil {
		t.Error("the systemctl marker produced no typed error")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Reason != ReasonMissingSystemctl {
			t.Errorf("missing systemctl error = %v", err)
		}
	}
}

func TestCommandsCoverCapabilitiesAndUnavailableMarkers(t *testing.T) {
	if !strings.Contains(probeCommand(), "command -v") ||
		!strings.Contains(probeCommand(), "systemctl") {
		t.Errorf("probe command = %q", probeCommand())
	}
	if !strings.Contains(processesCommand(), "ps -eo") ||
		!strings.Contains(processesCommand(), markerUnavailable) {
		t.Errorf("processes command = %q", processesCommand())
	}
	if !strings.Contains(portsCommand(), "__NC_SS__") ||
		!strings.Contains(portsCommand(), "__NC_NETSTAT__") ||
		!strings.Contains(portsCommand(), "__NC_LSOF__") {
		t.Errorf("ports command = %q", portsCommand())
	}
	if !strings.Contains(servicesCommand(), "systemctl --user") ||
		!strings.Contains(servicesCommand(), "list-units") {
		t.Errorf("services command = %q", servicesCommand())
	}
	if !strings.Contains(overviewCommand(), "MODE:darwin") ||
		!strings.Contains(overviewCommand(), "MODE:linux") {
		t.Errorf("overview command = %q", overviewCommand())
	}
}

func TestSignalAndServiceCommandsValidateTheirTargets(t *testing.T) {
	tests := []struct {
		pid    int
		signal string
		want   string
	}{
		{123, "TERM", "kill -15 123"},
		{123, "9", "kill -9 123"},
		{123, "hup", "kill -s HUP 123"},
		{123, "USR2", "kill -s USR2 123"},
	}
	for _, test := range tests {
		got, err := SignalCommand(test.pid, test.signal)
		if err != nil || got != test.want {
			t.Errorf("SignalCommand(%d, %q) = %q, %v; want %q", test.pid, test.signal, got, err, test.want)
		}
	}
	if _, err := SignalCommand(0, "TERM"); err == nil {
		t.Error("pid 0 was accepted")
	}
	if _, err := SignalCommand(12, "WINCH"); err == nil {
		t.Error("an unlisted signal was accepted")
	}

	if got, err := ServiceCommand("nginx.service", ServiceStart, ScopeSystem); err != nil ||
		got != "systemctl start 'nginx.service'" {
		t.Errorf("system start = %q, %v", got, err)
	}
	if got, err := ServiceCommand("app@1.service", ServiceRestart, ScopeUser); err != nil ||
		got != "systemctl --user restart 'app@1.service'" {
		t.Errorf("user restart = %q, %v", got, err)
	}
	for _, unit := range []string{"", "bad name", "nginx;rm -rf /", strings.Repeat("a", 257)} {
		if _, err := ServiceCommand(unit, ServiceStart, ScopeSystem); err == nil {
			t.Errorf("unit %q was accepted", unit)
		}
	}
	if _, err := ServiceCommand("nginx.service", "enable", ScopeSystem); err == nil {
		t.Error("an unlisted service action was accepted")
	}
}

// fakeConnector is an in-process Host transport. Its exec function sees the
// exact command, so tests prove which production template ran.
type fakeConnector struct {
	mu     sync.Mutex
	opens  map[string]int
	execs  map[string][]string
	closed int
	open   func(domainvault.Host) error
	exec   func(domainvault.Host, string) (ExecResult, error)
}

func newFakeConnector() *fakeConnector {
	return &fakeConnector{
		opens: make(map[string]int),
		execs: make(map[string][]string),
	}
}

func (c *fakeConnector) Open(ctx context.Context, host domainvault.Host) (Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := string(host.ID)
	c.mu.Lock()
	c.opens[id]++
	open := c.open
	c.mu.Unlock()
	if open != nil {
		if err := open(host); err != nil {
			return nil, err
		}
	}
	return &fakeConnection{connector: c, host: host}, nil
}

func (c *fakeConnector) commands(id string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.execs[id]...)
}

func (c *fakeConnector) openCount(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens[id]
}

type fakeConnection struct {
	connector *fakeConnector
	host      domainvault.Host
}

func (c *fakeConnection) Exec(ctx context.Context, command string) (ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecResult{}, err
	}
	id := string(c.host.ID)
	c.connector.mu.Lock()
	c.connector.execs[id] = append(c.connector.execs[id], command)
	exec := c.connector.exec
	c.connector.mu.Unlock()
	if exec == nil {
		return ExecResult{HasExitCode: true}, nil
	}
	return exec(c.host, command)
}

func (c *fakeConnection) Close() error {
	c.connector.mu.Lock()
	c.connector.closed++
	c.connector.mu.Unlock()
	return nil
}

func fakeHost(id string) domainvault.Host {
	return domainvault.Host{ID: model.HostID(id), Label: id, Hostname: id + ".test"}
}

const (
	fakeProbeAll = "__NC_OS=Linux\n__NC_HAS_ps=1\n__NC_HAS_top=1\n__NC_HAS_ss=1\n" +
		"__NC_HAS_netstat=1\n__NC_HAS_lsof=1\n__NC_HAS_systemctl=1\n"
	fakeOverview = "MODE:linux|HOST:fake-host|OS:Debian 12|KERNEL:6.1|UPTIME:7200|" +
		"LOAD:0.10 0.20 0.30|CORES:4|CPURAW:%d %d|MEM:2048 8192|SWAP:0 1024|DISK:10.0 40.0"
	fakeProcesses = "  1     0 root Ss 0.0 0.1 1234 5678 1-00:00:00 /sbin/init\n" +
		" 42     1 app  R  5.0 1.2 2048 4096 00:01:00 worker"
	fakePorts = "__NC_PORTS_BEGIN__\n__NC_SS__\n" +
		`tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=123,fd=3))` +
		"\n__NC_PORTS_END__\n"
	fakeServices = "__NC_SERVICES_BEGIN__\n__NC_SYSTEM__\n" +
		"nginx.service loaded active running Web server\n__NC_SERVICES_END__\n"
)

// sampleCounter counts overview samples across the sampler goroutine and the
// test goroutine. The fake shells run on the service's goroutines, so a plain
// int read by waitFor's closure is a data race under -race; the atomic gives
// both sides one synchronization point without changing the product.
type sampleCounter struct{ calls atomic.Int64 }

func (c *sampleCounter) sampled() int { return int(c.calls.Load()) }
func (c *sampleCounter) next() int    { return int(c.calls.Add(1)) }

func fakeExec(overviewCall *sampleCounter) func(domainvault.Host, string) (ExecResult, error) {
	return func(_ domainvault.Host, command string) (ExecResult, error) {
		switch {
		case command == probeCommand():
			return ExecResult{Stdout: fakeProbeAll}, nil
		case strings.Contains(command, "PRETTY_NAME"):
			return ExecResult{Stdout: sprintfOverview(overviewCall.next())}, nil
		case strings.Contains(command, "ps -eo"):
			return ExecResult{Stdout: fakeProcesses}, nil
		case strings.Contains(command, "__NC_PORTS_BEGIN__"):
			return ExecResult{Stdout: fakePorts}, nil
		case strings.Contains(command, "__NC_SERVICES_BEGIN__"):
			return ExecResult{Stdout: fakeServices}, nil
		default:
			return ExecResult{Stdout: "", HasExitCode: true}, nil
		}
	}
}

func sprintfOverview(call int) string {
	total := 1000 + call*100
	idle := 500 + call*50
	return strings.Replace(
		strings.Replace(fakeOverview, "%d", intString(total), 1),
		"%d", intString(idle), 1)
}

func intString(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 6)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func TestServiceSamplesEveryTabAndHonorsTheInterval(t *testing.T) {
	connector := newFakeConnector()
	overviewCalls := &sampleCounter{}
	connector.exec = fakeExec(overviewCalls)
	service := NewService(connector)
	defer service.Close()

	host := fakeHost("alpha")
	if err := service.Start(host, TabOverview, 20*time.Millisecond); err != nil {
		t.Fatalf("start overview: %v", err)
	}
	waitStatus(t, service, TabOverview, StatusLive)
	// The fake counts its command before the sampler parses and publishes the
	// result, so the counter is not the condition this test needs: the product
	// state is. A Linux CPU percentage needs a second counter reading, so wait
	// until the published snapshot carries a real value, not the first NaN.
	waitFor(t, 3*time.Second, "the second overview sample to be published", func() bool {
		return Valid(service.Snapshot().Tabs[TabOverview].Overview.CPUPercent)
	})
	state := service.Snapshot().Tabs[TabOverview]
	if state.Overview.Hostname != "fake-host" || state.Overview.UptimeSeconds != 7200 ||
		len(state.Overview.LoadAverage) != 3 || state.Overview.Cores != 4 {
		t.Errorf("overview = %+v", state.Overview)
	}
	if !Valid(state.Overview.CPUPercent) {
		t.Errorf("CPU stayed unknown after two samples: %+v", state.Overview)
	}

	for _, tab := range []Tab{TabProcesses, TabPorts, TabServices} {
		if err := service.Start(host, tab, 20*time.Millisecond); err != nil {
			t.Fatalf("start %s: %v", tab, err)
		}
		waitStatus(t, service, tab, StatusLive)
		state := service.Snapshot().Tabs[tab]
		switch tab {
		case TabProcesses:
			if len(state.Processes) != 2 || state.Processes[1].PID != 42 ||
				state.Processes[1].Command != "worker" {
				t.Errorf("process rows = %+v", state.Processes)
			}
		case TabPorts:
			if len(state.Ports) != 1 || state.Ports[0].Port != 22 ||
				state.Ports[0].PID != 123 {
				t.Errorf("port rows = %+v", state.Ports)
			}
		case TabServices:
			if len(state.Services) != 1 || state.Services[0].Name != "nginx.service" ||
				state.Services[0].ActiveState != "active" {
				t.Errorf("service rows = %+v", state.Services)
			}
		}
	}

	// A five-second cadence cannot produce a second command inside this
	// observation window; switching to 20 ms can, proving Start applies the
	// new interval instead of keeping the old one.
	if err := service.Start(host, TabProcesses, 5*time.Second); err != nil {
		t.Fatalf("start slow interval: %v", err)
	}
	before := len(connector.commands("alpha"))
	waitFor(t, 300*time.Millisecond, "the slow cadence to stay quiet", func() bool {
		return len(connector.commands("alpha")) <= before+1
	})
	if err := service.Start(host, TabProcesses, 20*time.Millisecond); err != nil {
		t.Fatalf("start fast interval: %v", err)
	}
	waitFor(t, 2*time.Second, "repeated fast samples", func() bool {
		return len(connector.commands("alpha")) >= before+3
	})
}

func TestServiceStartClearsSamplesWhenTheHostChanges(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(host domainvault.Host, command string) (ExecResult, error) {
		switch {
		case command == probeCommand():
			return ExecResult{Stdout: fakeProbeAll}, nil
		case strings.Contains(command, "ps -eo"):
			if string(host.ID) == "beta" {
				return ExecResult{Stdout: "  7     1 beta S 0.0 0.1 100 200 00:00:05 beta-only"}, nil
			}
			return ExecResult{Stdout: fakeProcesses}, nil
		default:
			return ExecResult{}, nil
		}
	}
	service := NewService(connector)
	defer service.Close()

	if err := service.Start(fakeHost("alpha"), TabProcesses, 20*time.Millisecond); err != nil {
		t.Fatalf("start alpha: %v", err)
	}
	waitStatus(t, service, TabProcesses, StatusLive)
	if rows := service.Snapshot().Tabs[TabProcesses].Processes; len(rows) != 2 {
		t.Fatalf("alpha rows = %d, want 2", len(rows))
	}

	if err := service.Start(fakeHost("beta"), TabProcesses, 20*time.Millisecond); err != nil {
		t.Fatalf("start beta: %v", err)
	}
	snapshot := service.Snapshot()
	if len(snapshot.Tabs) != 1 {
		t.Fatalf("tabs after Host switch = %d, want only the new demand", len(snapshot.Tabs))
	}
	if rows := snapshot.Tabs[TabProcesses].Processes; len(rows) != 0 {
		t.Fatalf("alpha rows survived the Host switch: %+v", rows)
	}
	waitStatus(t, service, TabProcesses, StatusLive)
	rows := service.Snapshot().Tabs[TabProcesses].Processes
	if len(rows) != 1 || rows[0].Command != "beta-only" {
		t.Fatalf("beta rows = %+v, want only beta's process", rows)
	}
}

func TestServiceRefreshRequestsAnImmediateSample(t *testing.T) {
	connector := newFakeConnector()
	overviewCalls := &sampleCounter{}
	connector.exec = fakeExec(overviewCalls)
	service := NewService(connector)
	defer service.Close()

	if err := service.Start(fakeHost("refresh"), TabProcesses, 5*time.Second); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitStatus(t, service, TabProcesses, StatusLive)
	before := len(connector.commands("refresh"))
	service.Refresh()
	waitFor(t, 2*time.Second, "a refreshed sample", func() bool {
		return len(connector.commands("refresh")) > before
	})
}

func TestServiceDegradesWhenRemoteCommandsAreMissing(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		if command == probeCommand() {
			return ExecResult{Stdout: "__NC_OS=Linux\n"}, nil
		}
		if strings.Contains(command, "PRETTY_NAME") {
			return ExecResult{Stdout: sprintfOverview(1)}, nil
		}
		return ExecResult{}, nil
	}
	service := NewService(connector)
	defer service.Close()
	host := fakeHost("minimal")

	for _, test := range []struct {
		tab    Tab
		reason Reason
	}{
		{TabProcesses, ReasonMissingProcesses},
		{TabPorts, ReasonMissingPorts},
		{TabServices, ReasonMissingSystemctl},
	} {
		if err := service.Start(host, test.tab, 20*time.Millisecond); err != nil {
			t.Fatalf("start %s: %v", test.tab, err)
		}
		waitStatus(t, service, test.tab, StatusUnavailable)
		state := service.Snapshot().Tabs[test.tab]
		if state.Reason != test.reason {
			t.Errorf("%s reason = %q, want %q", test.tab, state.Reason, test.reason)
		}
	}

	// The other tabs keep working: overview still samples on the same Host.
	if err := service.Start(host, TabOverview, 20*time.Millisecond); err != nil {
		t.Fatalf("start overview: %v", err)
	}
	waitStatus(t, service, TabOverview, StatusLive)
}

func TestServiceDoesNotExecBeforeTheHostKeyIsTrusted(t *testing.T) {
	connector := newFakeConnector()
	connector.open = func(host domainvault.Host) error {
		if string(host.ID) == "denied" {
			return &Error{Reason: ReasonHostKey, Cause: errors.New("host key rejected")}
		}
		return nil
	}
	overviewCalls := &sampleCounter{}
	connector.exec = fakeExec(overviewCalls)
	service := NewService(connector)
	defer service.Close()

	if err := service.Start(fakeHost("denied"), TabOverview, 20*time.Millisecond); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitStatus(t, service, TabOverview, StatusLogin)
	if got := service.Snapshot().Tabs[TabOverview].Reason; got != ReasonHostKey {
		t.Errorf("reason = %q, want host_key", got)
	}
	if commands := connector.commands("denied"); len(commands) != 0 {
		t.Errorf("commands ran on an untrusted Host: %v", commands)
	}
	if opens := connector.openCount("denied"); opens != 1 {
		t.Errorf("opens = %d, want 1", opens)
	}
}

func TestServiceCloseCancelsSampling(t *testing.T) {
	connector := newFakeConnector()
	overviewCalls := &sampleCounter{}
	connector.exec = fakeExec(overviewCalls)
	service := NewService(connector)

	if err := service.Start(fakeHost("stop"), TabOverview, 10*time.Millisecond); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 2*time.Second, "at least two samples", func() bool {
		return overviewCalls.sampled() >= 2
	})
	service.Close()
	service.Wait()
	after := len(connector.commands("stop"))
	// Observe a bounded window with a deadline; a closed sampler must not
	// add a command in it.
	waitFor(t, 150*time.Millisecond, "no command after Close", func() bool {
		return len(connector.commands("stop")) == after
	})
	if len(connector.commands("stop")) != after {
		t.Errorf("commands grew after Close: %d -> %d", after, len(connector.commands("stop")))
	}
}

func TestSignalRunsOnlyTheConfirmedCommand(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		return ExecResult{Stdout: "ok", HasExitCode: true}, nil
	}
	service := NewService(connector)
	host := fakeHost("action")

	result, err := service.Signal(context.Background(), host, 4321, "KILL")
	if err != nil {
		t.Fatalf("signal: %v", err)
	}
	if result.Command != "kill -9 4321" {
		t.Errorf("result command = %q", result.Command)
	}
	commands := connector.commands("action")
	if len(commands) != 1 || commands[0] != "kill -9 4321" {
		t.Errorf("executed commands = %v", commands)
	}
	if opens := connector.openCount("action"); opens != 1 {
		t.Errorf("opens = %d, want one trusted lease", opens)
	}

	if _, err := service.Signal(context.Background(), host, 0, "KILL"); err == nil {
		t.Error("an invalid pid reached the transport")
	}
	if commands := connector.commands("action"); len(commands) != 1 {
		t.Errorf("an invalid signal ran a command: %v", commands)
	}
}

func TestServiceActionReportsPermissionAndRetriesWithSudo(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		if command == "systemctl restart 'nginx.service'" {
			return ExecResult{Stderr: "Failed to restart: Permission denied", ExitCode: 1,
				HasExitCode: true}, nil
		}
		return ExecResult{Stdout: "ok", HasExitCode: true}, nil
	}
	service := NewService(connector)
	host := fakeHost("sudo")

	result, err := service.ServiceAction(context.Background(), host, "nginx.service",
		ServiceRestart, ScopeSystem)
	if err != nil {
		t.Fatalf("service action: %v", err)
	}
	if result.Command != "sudo -n systemctl restart 'nginx.service'" {
		t.Errorf("result command = %q", result.Command)
	}
	commands := connector.commands("sudo")
	if len(commands) != 2 ||
		commands[0] != "systemctl restart 'nginx.service'" ||
		commands[1] != "sudo -n systemctl restart 'nginx.service'" {
		t.Errorf("commands = %v", commands)
	}

	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		return ExecResult{Stderr: "permission denied", ExitCode: 1, HasExitCode: true}, nil
	}
	_, err = service.ServiceAction(context.Background(), fakeHost("noperm"), "db.service",
		ServiceStop, ScopeSystem)
	var typed *Error
	if !errors.As(err, &typed) || typed.Reason != ReasonPermission {
		t.Fatalf("permission error = %v", err)
	}
}

func TestServiceActionRejectsInvalidInputsBeforeOpening(t *testing.T) {
	connector := newFakeConnector()
	service := NewService(connector)
	host := fakeHost("invalid")
	if _, err := service.ServiceAction(context.Background(), host, "bad name",
		ServiceStart, ScopeSystem); err == nil {
		t.Fatal("an invalid unit was accepted")
	}
	if _, err := service.ServiceAction(context.Background(), host, "ok.service",
		"enable", ScopeSystem); err == nil {
		t.Fatal("an invalid action was accepted")
	}
	if opens := connector.openCount("invalid"); opens != 0 {
		t.Errorf("opens = %d, want 0", opens)
	}
}

func waitStatus(t *testing.T, service *Service, tab Tab, status Status) {
	t.Helper()
	waitFor(t, 3*time.Second, "tab "+string(tab)+" to become "+string(status), func() bool {
		state, ok := service.Snapshot().Tabs[tab]
		return ok && state.Status == status
	})
}

// waitFor polls a real condition until it holds or the deadline passes. The
// poll cadence is not a synchronization assumption.
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
