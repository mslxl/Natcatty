package hoststatsintegration

import (
	"context"
	"fmt"
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

// collectorTransport is a fake Host Stats transport shared by the monitoring
// service and the System Tool in these tests. It records every command so the
// tests can prove both consumers read one collection.
type collectorTransport struct {
	mu sync.Mutex

	statsOutputs []string
	diskOutputs  []string
	commands     []string
	opens        int
	statsCalls   int
	diskCalls    int
}

func (t *collectorTransport) Open(ctx context.Context, _ domainvault.Host) (hoststats.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.opens++
	t.mu.Unlock()
	return &collectorConnection{transport: t}, nil
}

func (t *collectorTransport) counts() (opens, stats, disks int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opens, t.statsCalls, t.diskCalls
}

func (t *collectorTransport) received() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.commands...)
}

type collectorConnection struct {
	transport *collectorTransport
}

func (c *collectorConnection) Exec(ctx context.Context, command string) (hoststats.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return hoststats.ExecResult{}, err
	}
	t := c.transport
	t.mu.Lock()
	t.commands = append(t.commands, command)
	var output string
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
	t.mu.Unlock()
	return hoststats.ExecResult{Stdout: output}, nil
}

func (c *collectorConnection) Close() error { return nil }

func (c *collectorConnection) SingleChannel() bool { return false }

// collectorClock is the collector's data clock. The collector's scheduler
// stays on real time, so a test can move the sample time without waiting.
type collectorClock struct {
	mu sync.Mutex
	at time.Time
}

func newCollectorClock(at time.Time) *collectorClock { return &collectorClock{at: at} }

func (c *collectorClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *collectorClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

const (
	collectorDarwinReply = "NC_LATENCY_MARK|MODE:darwin|CPU:37|CORES:8|" +
		"MEMINFO:16384 12288 0 0 1024 0|PROCS:|" +
		"DISKS:/:10.000000:40.000000:25:/dev/disk1|NET:en0:1000:2000|" +
		"HOST:shared|OS:macOS 14|KERNEL:23|UPTIME:100|LOAD:1 2 3"
	collectorLinuxReply = "NC_LATENCY_MARK|MODE:linux|CPURAW:1000 500|CORES:2|" +
		"PERCORERAW:cpu0:500:250,cpu1:500:250|MEMINFO:8000 1000 100 500 0 0|PROCS:|" +
		"NET:eth0:1000:2000|GPU:|DISKIO:100;vda:100:100,|" +
		"HOST:shared|OS:Linux|KERNEL:6.1|UPTIME:100|LOAD:0.1 0.2 0.3"
	collectorDiskReply = "DISKS:/:10.000000:40.000000:25:fs=ext4:/dev/sda1"
)

func collectorLinuxCounters(total, idle, coreTotal, coreIdle, rx, tx, uptime, read, write uint64) string {
	return fmt.Sprintf("NC_LATENCY_MARK|MODE:linux|CPURAW:%d %d|CORES:2|"+
		"PERCORERAW:cpu0:%d:%d,cpu1:%d:%d|MEMINFO:8000 1000 100 500 0 0|PROCS:|"+
		"NET:eth0:%d:%d|GPU:|DISKIO:%d;vda:%d:%d,|"+
		"HOST:shared|OS:Linux|KERNEL:6.1|UPTIME:%d|LOAD:0.1 0.2 0.3",
		total, idle, coreTotal, coreIdle, coreTotal, coreIdle, rx, tx,
		uptime, read, write, uptime)
}

func collectorHost(id string) domainvault.Host {
	return domainvault.Host{ID: model.HostID(id), Label: id, Hostname: id}
}

// TestOneSharedCollectionServesMonitoringAndSystemTool proves the two
// surfaces inside the four-second cache window read the same Stats Sample from
// one collection. The services have no Connector of their own, so a private
// sampler could not succeed: every command in the transport is the shared
// collector's.
func TestOneSharedCollectionServesMonitoringAndSystemTool(t *testing.T) {
	transport := &collectorTransport{
		statsOutputs: []string{collectorLinuxReply},
		diskOutputs:  []string{collectorDiskReply},
	}
	// The scheduling clock is frozen, so the System Tool always joins inside
	// the four-second window no matter how long the test itself takes.
	clock := newCollectorClock(time.Unix(1_700_000_000, 0))
	collector := hoststats.NewCollector(transport,
		hoststats.WithScheduleClock(clock.Now),
		hoststats.WithCacheTTL(4*time.Second),
	)
	t.Cleanup(collector.Close)

	monitoring := monitor.NewService(nil, monitor.WithStatsCollector(collector))
	t.Cleanup(monitoring.Close)
	system := sysmanager.NewService(nil, sysmanager.WithStatsCollector(collector))
	t.Cleanup(system.Close)

	host := collectorHost("shared")
	if err := monitoring.Start("overview", []domainvault.Host{host}, time.Hour); err != nil {
		t.Fatalf("start monitoring: %v", err)
	}
	waitFor(t, 5*time.Second, "the monitoring surface's first sample", func() bool {
		state, ok := monitoring.Snapshot().Hosts["shared"]
		return ok && state.Status == domainmonitor.StatusLive && !state.Sample.At.IsZero()
	})
	monitored := monitoring.Snapshot().Hosts["shared"]

	if err := system.Start(host, sysmanager.TabOverview, time.Hour); err != nil {
		t.Fatalf("start system overview: %v", err)
	}
	waitFor(t, 5*time.Second, "the System Tool's cached sample", func() bool {
		state, ok := system.Snapshot().Tabs[sysmanager.TabOverview]
		return ok && state.Status == sysmanager.StatusLive && !state.Stats.SampledAt.IsZero()
	})
	overview := system.Snapshot().Tabs[sysmanager.TabOverview]

	if !overview.Stats.SampledAt.Equal(monitored.Sample.At) {
		t.Fatalf("SampledAt = %v in the System Tool, %v in monitoring; want one sample",
			overview.Stats.SampledAt, monitored.Sample.At)
	}
	if monitored.Sample.CPUCores != 2 || overview.Stats.Cores != 2 {
		t.Errorf("cores = %d monitoring, %d System Tool; want 2 from the shared reply",
			monitored.Sample.CPUCores, overview.Stats.Cores)
	}
	if monitored.Sample.MemTotalMB != 8000 || overview.Overview.MemTotalMB != 8000 {
		t.Errorf("memory total = %v monitoring, %v System Tool; want the shared reply's 8000",
			monitored.Sample.MemTotalMB, overview.Overview.MemTotalMB)
	}

	opens, statsCalls, diskCalls := transport.counts()
	if opens != 1 || statsCalls != 1 || diskCalls != 1 {
		t.Errorf("transport saw %d opens, %d stats, %d disk; want one shared collection",
			opens, statsCalls, diskCalls)
	}
	for _, command := range transport.received() {
		switch command {
		case hoststats.StatsCommand(), hoststats.DiskCommand():
		default:
			t.Errorf("unexpected command produced by a private sampler: %.80q", command)
		}
	}
}

// TestRetainedCountersSurviveMonitorStopAndSystemToolStart proves the collector
// keeps one per-Host delta baseline across surfaces: monitoring's first sample
// establishes the counters, then closing monitoring and opening the System Tool
// produces a live delta instead of a second unknown first sample.
func TestRetainedCountersSurviveMonitorStopAndSystemToolStart(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	clock := newCollectorClock(start)
	transport := &collectorTransport{
		statsOutputs: []string{
			collectorLinuxCounters(1000, 500, 1000, 500, 1000, 2000, 100, 100, 100),
			collectorLinuxCounters(2000, 1000, 2000, 1000, 3000, 4000, 110, 300, 200),
		},
		diskOutputs: []string{collectorDiskReply, collectorDiskReply},
	}
	collector := hoststats.NewCollector(transport,
		hoststats.WithClock(clock.Now),
		hoststats.WithCacheTTL(0),
	)
	t.Cleanup(collector.Close)

	monitoring := monitor.NewService(nil, monitor.WithStatsCollector(collector))
	t.Cleanup(monitoring.Close)
	system := sysmanager.NewService(nil, sysmanager.WithStatsCollector(collector))
	t.Cleanup(system.Close)

	host := collectorHost("switch")
	if err := monitoring.Start("overview", []domainvault.Host{host}, time.Hour); err != nil {
		t.Fatalf("start monitoring: %v", err)
	}
	waitFor(t, 5*time.Second, "the monitoring baseline", func() bool {
		state, ok := monitoring.Snapshot().Hosts["switch"]
		return ok && state.Status == domainmonitor.StatusLive && state.Sample.At.Equal(start)
	})
	monitoring.Stop("overview")

	clock.Advance(10 * time.Second)
	secondAt := start.Add(10 * time.Second)
	if err := system.Start(host, sysmanager.TabOverview, time.Hour); err != nil {
		t.Fatalf("start system overview: %v", err)
	}
	system.Refresh()
	waitFor(t, 5*time.Second, "the second sample through the retained baseline", func() bool {
		state, ok := system.Snapshot().Tabs[sysmanager.TabOverview]
		return ok && state.Status == sysmanager.StatusLive && state.Stats.SampledAt.Equal(secondAt)
	})
	overview := system.Snapshot().Tabs[sysmanager.TabOverview]

	if overview.Stats.CPU != 50 || overview.Overview.CPUPercent != 50 {
		t.Errorf("CPU = %v across surfaces, want the 50%% delta from the retained counters",
			overview.Stats.CPU)
	}
	if len(overview.Stats.CPUPerCore) != 2 {
		t.Fatalf("per-core samples = %d, want 2", len(overview.Stats.CPUPerCore))
	}
	for _, core := range overview.Stats.CPUPerCore {
		if core.Percent != 50 {
			t.Errorf("%s = %v%%, want 50%% from the retained per-core baseline", core.Name, core.Percent)
		}
	}
	if overview.Stats.DiskReadBps != 10240 || overview.Stats.DiskWriteBps != 5120 {
		t.Errorf("disk I/O = %v/%v Bps, want 10240/5120 from the retained counters",
			overview.Stats.DiskReadBps, overview.Stats.DiskWriteBps)
	}
	if overview.Stats.NetworkRxBps != 200 || overview.Stats.NetworkTxBps != 200 {
		t.Errorf("network = %v/%v Bps, want 200/200 from the retained counters",
			overview.Stats.NetworkRxBps, overview.Stats.NetworkTxBps)
	}
	if _, statsCalls, diskCalls := transport.counts(); statsCalls != 2 || diskCalls != 2 {
		t.Errorf("collections = %d stats and %d disk; want one baseline and one switched-surface sample",
			statsCalls, diskCalls)
	}
}
