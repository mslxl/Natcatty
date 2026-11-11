// Package hoststatsintegration tests the one Stats Sample seam shared by the
// monitoring surface and the System Tool. It lives outside the application
// contexts so it can drive both consumers without either importing the other.
package hoststatsintegration

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/application/hoststats"
	"github.com/mslxl/natcatty/application/monitor"
	"github.com/mslxl/natcatty/application/sysmanager"
	"github.com/mslxl/natcatty/domain/model"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// sharedStatsTransport is one Host transport observed by both consumers. Its
// log proves which exact template each one sent; its reply is the same canned
// Stats Sample for both.
type sharedStatsTransport struct {
	mu       sync.Mutex
	commands []string
	exec     func(string) (string, error)
}

func (t *sharedStatsTransport) run(command string) (string, error) {
	t.mu.Lock()
	t.commands = append(t.commands, command)
	t.mu.Unlock()
	return t.exec(command)
}

func (t *sharedStatsTransport) commandsSnapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.commands...)
}

// monitorConnector adapts the shared transport to the monitoring port.
type monitorConnector struct{ transport *sharedStatsTransport }

func (c monitorConnector) Open(context.Context, domainvault.Host) (monitor.Connection, error) {
	return monitorConnection{transport: c.transport}, nil
}

type monitorConnection struct{ transport *sharedStatsTransport }

func (c monitorConnection) Exec(_ context.Context, command string) (monitor.ExecResult, error) {
	stdout, err := c.transport.run(command)
	return monitor.ExecResult{Stdout: stdout}, err
}

func (c monitorConnection) Close() error { return nil }

// systemConnector adapts the same transport to the System Tool port.
type systemConnector struct{ transport *sharedStatsTransport }

func (c systemConnector) Open(context.Context, domainvault.Host) (sysmanager.Connection, error) {
	return systemConnection{transport: c.transport}, nil
}

type systemConnection struct{ transport *sharedStatsTransport }

func (c systemConnection) Exec(_ context.Context, command string) (sysmanager.ExecResult, error) {
	stdout, err := c.transport.run(command)
	return sysmanager.ExecResult{Stdout: stdout}, err
}

func (c systemConnection) Close() error { return nil }

func sharedHost(id string) domainvault.Host {
	return domainvault.Host{ID: model.HostID(id), Label: id, Hostname: id + ".test"}
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

// TestBothConsumersSendOneStatsCommandAndProduceOneSample proves the
// monitoring surface and the System Tool run the same command template and
// derive the same Stats Sample from one canned reply. A fixed clock removes
// collection-time differences so every field, including SampledAt, compares
// directly.
func TestBothConsumersSendOneStatsCommandAndProduceOneSample(t *testing.T) {
	const canned = "NC_LATENCY_MARK|MODE:darwin|CPU:37|CORES:8|" +
		"MEMINFO:16384 12288 0 0 1024 0|PROCS:42;5.0;worker,7;1.0;svc|" +
		"DISKS:/:20.000000:100.000000:20:apfs:disk3s1|NET:en0:1000:2000|" +
		"HOST:mac|OS:macOS 15|KERNEL:24.0|UPTIME:99|LOAD:1 2 3"
	fixed := time.Unix(1_700_000_000, 0)
	transport := &sharedStatsTransport{exec: func(command string) (string, error) {
		if strings.Contains(command, "__NC_HAS_") {
			return "__NC_OS=Darwin\n", nil
		}
		return canned, nil
	}}
	systemTool := sysmanager.NewService(systemConnector{transport},
		sysmanager.WithClock(func() time.Time { return fixed }))
	defer systemTool.Close()
	pane := monitor.NewService(monitorConnector{transport},
		monitor.WithClock(func() time.Time { return fixed }))
	defer pane.Close()

	host := sharedHost("shared")
	if err := systemTool.Start(host, sysmanager.TabOverview, time.Hour); err != nil {
		t.Fatalf("start the System Tool overview: %v", err)
	}
	if err := pane.Start("panel-a", []domainvault.Host{host}, time.Hour); err != nil {
		t.Fatalf("start the monitoring pane: %v", err)
	}
	waitFor(t, 5*time.Second, "the System Tool overview to go live", func() bool {
		return systemTool.Snapshot().Tabs[sysmanager.TabOverview].Status == sysmanager.StatusLive
	})
	waitFor(t, 5*time.Second, "the monitoring Host to go live", func() bool {
		return pane.Snapshot().Hosts["shared"].Status == domainmonitor.StatusLive
	})

	stats := systemTool.Snapshot().Tabs[sysmanager.TabOverview].Stats
	monSample := pane.Snapshot().Hosts["shared"].Sample

	// The same literal template reached the Host once per consumer. A
	// consumer that had kept its own command would leave only one match.
	statsCommandSends := 0
	for _, command := range transport.commandsSnapshot() {
		if command == hoststats.StatsCommand() {
			statsCommandSends++
		}
	}
	if statsCommandSends != 2 {
		t.Fatalf("the shared stats command ran %d times, want once per consumer: %d commands",
			statsCommandSends, len(transport.commandsSnapshot()))
	}

	// The canned reply yields the same Stats Sample through the monitoring
	// projection and through the System Tool's stored sample.
	if !stats.SampledAt.Equal(fixed) {
		t.Errorf("SampledAt = %v, want the collection clock %v", stats.SampledAt, fixed)
	}
	if !sameFloat(stats.CPU, monSample.CPU) || stats.Cores != monSample.CPUCores {
		t.Errorf("cpu = %v/%d vs monitor %v/%d", stats.CPU, stats.Cores,
			monSample.CPU, monSample.CPUCores)
	}
	if len(monSample.CPUPerCore) != len(stats.CPUPerCore) {
		t.Errorf("per-core = %v vs %v", monSample.CPUPerCore, stats.CPUPerCore)
	} else {
		for index, core := range stats.CPUPerCore {
			if monSample.CPUPerCore[index] != core.Percent {
				t.Errorf("core %d = %v vs %v", index, monSample.CPUPerCore[index], core.Percent)
			}
		}
	}
	if !sameFloat(stats.MemUsedMB, monSample.MemUsedMB) ||
		!sameFloat(stats.MemTotalMB, monSample.MemTotalMB) ||
		!sameFloat(stats.SwapUsedMB, monSample.SwapUsedMB) ||
		!sameFloat(stats.SwapTotalMB, monSample.SwapTotalMB) {
		t.Errorf("memory = %+v vs monitor %+v", stats, monSample)
	}
	if !sameFloat(stats.DiskUsedGB, monSample.DiskUsedGB) ||
		!sameFloat(stats.DiskTotalGB, monSample.DiskTotalGB) ||
		!sameFloat(stats.DiskPercent, monSample.Disk) ||
		len(stats.Disks) != len(monSample.Disks) {
		t.Errorf("disk = %+v vs monitor %+v", stats, monSample)
	} else {
		for index, disk := range stats.Disks {
			if disk != monSample.Disks[index] {
				t.Errorf("disk %d = %+v vs %+v", index, disk, monSample.Disks[index])
			}
		}
	}
	if !sameFloat(stats.NetworkRxBps, monSample.Rx) ||
		!sameFloat(stats.NetworkTxBps, monSample.Tx) {
		t.Errorf("network rates = %v/%v vs monitor %v/%v",
			stats.NetworkRxBps, stats.NetworkTxBps, monSample.Rx, monSample.Tx)
	}
	if stats.LatencyMS != monSample.Latency {
		t.Errorf("latency = %v vs monitor %v", stats.LatencyMS, monSample.Latency)
	}
	if !sameFloat(stats.GPU, monSample.GPU) || stats.GPUName != monSample.GPUName ||
		!sameFloat(stats.GPUMemUsedMB, monSample.GPUMemUsedMB) ||
		!sameFloat(stats.GPUMemTotalMB, monSample.GPUMemTotalMB) {
		t.Errorf("gpu = %+v vs monitor %+v", stats, monSample)
	}
	if stats.Hostname != monSample.Hostname || stats.OSName != monSample.OSName ||
		stats.KernelRelease != monSample.KernelRelease ||
		stats.UptimeSeconds != monSample.UptimeSeconds {
		t.Errorf("identity = %+v vs monitor %+v", stats, monSample)
	}
	if len(stats.LoadAverage) != len(monSample.LoadAverage) {
		t.Errorf("load = %v vs %v", stats.LoadAverage, monSample.LoadAverage)
	} else {
		for index, value := range stats.LoadAverage {
			if value != monSample.LoadAverage[index] {
				t.Errorf("load %d = %v vs %v", index, value, monSample.LoadAverage[index])
			}
		}
	}
	if len(stats.TopProcesses) != 2 || stats.TopProcesses[0].PID != 42 ||
		stats.TopProcesses[0].Command != "worker" {
		t.Errorf("top processes = %+v", stats.TopProcesses)
	}

	// The stored sample is exactly the one the shared parser derives from
	// the canned reply, so neither consumer rewrites a field.
	parsed, err := hoststats.Parse(canned)
	if err != nil {
		t.Fatalf("parse the canned reply: %v", err)
	}
	expected, _ := parsed.Sample(hoststats.State{}, fixed, 0)
	if !sameFloat(stats.CPU, expected.CPU) ||
		!sameFloat(stats.MemUsedMB, expected.MemUsedMB) ||
		!sameFloat(stats.DiskPercent, expected.DiskPercent) ||
		stats.Hostname != expected.Hostname ||
		!stats.SampledAt.Equal(expected.SampledAt) {
		t.Errorf("System Tool sample = %+v, want the shared parser's %+v", stats, expected)
	}
}

// sameFloat compares two metrics, treating two unknowns as equal.
func sameFloat(a, b float64) bool {
	return (math.IsNaN(a) && math.IsNaN(b)) || a == b
}
