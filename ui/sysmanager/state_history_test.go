package sysmanager

import (
	"math"
	"testing"
	"time"

	"github.com/mslxl/natcatty/application/hoststats"
	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
)

func liveOverview(at time.Time, cpu float64) appsysmanager.TabState {
	return appsysmanager.TabState{
		Tab:    appsysmanager.TabOverview,
		Status: appsysmanager.StatusLive,
		At:     at,
		Stats: hoststats.Sample{
			SampledAt: at,
			CPU:       cpu,
		},
		Overview: appsysmanager.Overview{CPUPercent: cpu},
	}
}

// TestCPUHistorySurvivesOnlySystemTabSwitches proves the sparkline history is
// window-scoped UI state: switching to another System Tab and back keeps it,
// while a new surface starts from its own samples.
func TestCPUHistorySurvivesOnlySystemTabSwitches(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	service := newFakeService()
	service.Seed(appsysmanager.TabOverview, liveOverview(base, 25))
	state := newTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()
	if got := state.CPUHistory(); len(got) != 1 || got[0] != 25 {
		t.Fatalf("history after the first sample = %v, want [25]", got)
	}

	state.SetTab(appsysmanager.TabProcesses)
	state.Flush()
	if got := state.CPUHistory(); len(got) != 1 || got[0] != 25 {
		t.Fatalf("history after leaving the Overview = %v, want it unchanged", got)
	}

	service.Seed(appsysmanager.TabOverview, liveOverview(base.Add(time.Second), 40))
	state.SetTab(appsysmanager.TabOverview)
	state.Flush()
	if got := state.CPUHistory(); len(got) != 2 || got[0] != 25 || got[1] != 40 {
		t.Fatalf("history after returning to the Overview = %v, want [25 40]", got)
	}

	// The page and the System tool are separate State values. The page's
	// accumulated history must not appear in the tool's fresh card.
	tool := newTestState(service, newFakePreferences())
	tool.Activate()
	tool.Flush()
	if got := tool.CPUHistory(); len(got) != 1 || got[0] != 40 {
		t.Fatalf("another surface's history = %v, want only its own [40]", got)
	}
}

// TestCPUHistoryClearsWhenTheHostChanges proves one Host's samples never draw
// under another Host's card, and the new Host starts a fresh trend.
func TestCPUHistoryClearsWhenTheHostChanges(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	service := newFakeService()
	service.Seed(appsysmanager.TabOverview, liveOverview(base, 25))
	state := newTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()
	state.Flush()
	if got := state.CPUHistory(); len(got) != 1 || got[0] != 25 {
		t.Fatalf("history after repeated frames = %v, want one point", got)
	}

	service.Seed(appsysmanager.TabOverview, liveOverview(base.Add(2*time.Second), 60))
	state.SetHost("beta")
	if got := state.CPUHistory(); len(got) != 0 {
		t.Fatalf("history after the Host change = %v, want it cleared", got)
	}
	state.Flush()
	if got := state.CPUHistory(); len(got) != 1 || got[0] != 60 {
		t.Fatalf("history for the new Host = %v, want a fresh [60]", got)
	}
}

// TestCPUHistoryKeepsAtMostTwentyFourSamples proves the sparkline's bound.
func TestCPUHistoryKeepsAtMostTwentyFourSamples(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	service := newFakeService()
	state := newTestState(service, newFakePreferences())
	state.Activate()
	for index := 1; index <= 30; index++ {
		service.Seed(appsysmanager.TabOverview,
			liveOverview(base.Add(time.Duration(index)*time.Second), float64(index)))
		state.Flush()
	}
	history := state.CPUHistory()
	if len(history) != cpuHistoryLimit {
		t.Fatalf("history length = %d, want %d", len(history), cpuHistoryLimit)
	}
	if history[0] != 7 || history[len(history)-1] != 30 {
		t.Errorf("history = %v, want the newest 24 samples 7..30", history)
	}
}

// TestCPUHistoryIgnoresReusedOrOlderSamples proves a cached observation does
// not add a second point and an out-of-order observation does not move back.
func TestCPUHistoryIgnoresReusedOrOlderSamples(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	service := newFakeService()
	service.Seed(appsysmanager.TabOverview, liveOverview(base, 25))
	state := newTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	service.Seed(appsysmanager.TabOverview, liveOverview(base, 99))
	state.Flush()
	service.Seed(appsysmanager.TabOverview, liveOverview(base.Add(-time.Second), 1))
	state.Flush()
	if got := state.CPUHistory(); len(got) != 1 || got[0] != 25 {
		t.Fatalf("history after reused and older samples = %v, want [25]", got)
	}
}

// metricOverview is one live Stats Sample carrying all four card metrics.
func metricOverview(at time.Time, cpu, memUsed, memTotal, diskPercent, rx, tx float64) appsysmanager.TabState {
	return appsysmanager.TabState{
		Tab:    appsysmanager.TabOverview,
		Status: appsysmanager.StatusLive,
		At:     at,
		Stats: hoststats.Sample{
			SampledAt:    at,
			CPU:          cpu,
			MemUsedMB:    memUsed,
			MemTotalMB:   memTotal,
			DiskPercent:  diskPercent,
			NetworkRxBps: rx,
			NetworkTxBps: tx,
		},
		Overview: appsysmanager.Overview{
			CPUPercent: cpu, MemUsedMB: memUsed, MemTotalMB: memTotal,
			DiskPercent: diskPercent, NetworkRxBps: rx, NetworkTxBps: tx,
		},
	}
}

// TestEveryMetricHistoryRecordsItsOwnSamples proves each card keeps its own
// 24-sample trend: memory keeps the used/total percent, disk keeps the
// aggregate percent, and network keeps RX+TX bytes per second. An unknown
// metric is skipped without holding back the others.
func TestEveryMetricHistoryRecordsItsOwnSamples(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	service := newFakeService()
	service.Seed(appsysmanager.TabOverview, metricOverview(base, 25, 2048, 8192, 50, 100, 200))
	state := newTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()
	if got := state.CPUHistory(); len(got) != 1 || got[0] != 25 {
		t.Fatalf("CPU history = %v, want [25]", got)
	}
	if got := state.MemoryHistory(); len(got) != 1 || got[0] != 25 {
		t.Fatalf("memory history = %v, want [25]", got)
	}
	if got := state.DiskHistory(); len(got) != 1 || got[0] != 50 {
		t.Fatalf("disk history = %v, want [50]", got)
	}
	if got := state.NetworkHistory(); len(got) != 1 || got[0] != 300 {
		t.Fatalf("network history = %v, want [300]", got)
	}

	// Leaving and returning to the Overview is a System Tab switch, not a new
	// surface: every metric keeps its trend.
	state.SetTab(appsysmanager.TabProcesses)
	state.Flush()
	service.Seed(appsysmanager.TabOverview, metricOverview(
		base.Add(time.Second), 40, 4096, 8192, 70, math.NaN(), 300))
	state.SetTab(appsysmanager.TabOverview)
	state.Flush()
	if got := state.CPUHistory(); len(got) != 2 || got[1] != 40 {
		t.Fatalf("CPU history after the tab switch = %v, want a second point", got)
	}
	if got := state.MemoryHistory(); len(got) != 2 || got[1] != 50 {
		t.Fatalf("memory history after the tab switch = %v, want [25 50]", got)
	}
	if got := state.DiskHistory(); len(got) != 2 || got[1] != 70 {
		t.Fatalf("disk history after the tab switch = %v, want [50 70]", got)
	}
	if got := state.NetworkHistory(); len(got) != 1 || got[0] != 300 {
		t.Fatalf("network history with one unknown rate = %v, want it unchanged", got)
	}

	// A Host change clears every metric, not only CPU.
	service.Seed(appsysmanager.TabOverview, metricOverview(
		base.Add(2*time.Second), 10, 1024, 8192, 10, 10, 10))
	state.SetHost("beta")
	if got := state.MemoryHistory(); len(got) != 0 {
		t.Fatalf("memory history after the Host change = %v, want it cleared", got)
	}
	if got := state.DiskHistory(); len(got) != 0 {
		t.Fatalf("disk history after the Host change = %v, want it cleared", got)
	}
	if got := state.NetworkHistory(); len(got) != 0 {
		t.Fatalf("network history after the Host change = %v, want it cleared", got)
	}
	state.Flush()
	if got := state.NetworkHistory(); len(got) != 1 || got[0] != 20 {
		t.Fatalf("network history for the new Host = %v, want a fresh [20]", got)
	}
}

// TestMetricHistoriesStayBounded proves every card's sparkline keeps at most
// 24 samples.
func TestMetricHistoriesStayBounded(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	service := newFakeService()
	state := newTestState(service, newFakePreferences())
	state.Activate()
	for index := 1; index <= 30; index++ {
		service.Seed(appsysmanager.TabOverview, metricOverview(
			base.Add(time.Duration(index)*time.Second),
			float64(index), float64(index), 100, float64(index), float64(index), 0))
		state.Flush()
	}
	for name, history := range map[string][]float64{
		"CPU":     state.CPUHistory(),
		"memory":  state.MemoryHistory(),
		"disk":    state.DiskHistory(),
		"network": state.NetworkHistory(),
	} {
		if len(history) != cpuHistoryLimit {
			t.Errorf("%s history length = %d, want %d", name, len(history), cpuHistoryLimit)
		}
		if len(history) > 0 && history[len(history)-1] != 30 {
			t.Errorf("%s history ends at %v, want 30", name, history[len(history)-1])
		}
	}
}

// TestCPUHistorySkipsUnknownReadings proves the sparkline never invents a zero
// for an unknown CPU, such as Linux's first sample.
func TestCPUHistorySkipsUnknownReadings(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	service := newFakeService()
	service.Seed(appsysmanager.TabOverview, liveOverview(base, math.NaN()))
	state := newTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()
	if got := state.CPUHistory(); len(got) != 0 {
		t.Fatalf("history for an unknown CPU = %v, want none", got)
	}
}
