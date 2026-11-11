package sysmanager

import (
	"math"
	"testing"

	"github.com/mslxl/natcatty/application/hoststats"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
)

// TestOverviewFromSampleCarriesEverySharedMetric proves the Overview contract
// surfaces every field the shared Stats Sample already carries: per-core CPU,
// memory detail, disk I/O, interfaces, top processes, and the aggregate disk
// figures rather than one mount's.
func TestOverviewFromSampleCarriesEverySharedMetric(t *testing.T) {
	sample := hoststats.Sample{
		Hostname:      "web-1",
		OSName:        "Debian 12",
		KernelRelease: "6.1",
		UptimeSeconds: 86400,
		LoadAverage:   []float64{0.5, 0.4, 0.3},
		Cores:         2,
		CPU:           42,
		CPUPerCore: []hoststats.Core{
			{Name: "cpu0", Percent: 10},
			{Name: "cpu1", Percent: 74},
		},
		MemUsedMB:    2048,
		MemTotalMB:   8192,
		MemFreeMB:    4096,
		MemBuffersMB: 128,
		MemCachedMB:  1920,
		SwapUsedMB:   128,
		SwapTotalMB:  1024,
		Disks: []domainmonitor.Disk{
			{MountPoint: "/", UsedGB: 10, TotalGB: 40, Percent: 25, CapacityKey: "/dev/a"},
			{MountPoint: "/data", UsedGB: 50, TotalGB: 80, Percent: 62.5, CapacityKey: "/dev/b"},
		},
		DiskUsedGB:   60,
		DiskTotalGB:  120,
		DiskPercent:  50,
		DiskReadBps:  1536,
		DiskWriteBps: 2048,
		NetworkRxBps: 3072,
		NetworkTxBps: 512,
		Interfaces: []hoststats.Interface{
			{Name: "eth0", RxBytes: 1 << 20, TxBytes: 2 << 20, RxBps: 1024, TxBps: 2048},
		},
		TopProcesses: []hoststats.Process{
			{PID: 42, MemPercent: 3.5, Command: "worker"},
		},
	}

	overview := overviewFromSample(sample)

	if overview.Hostname != "web-1" || overview.OSName != "Debian 12" ||
		overview.KernelRelease != "6.1" || overview.UptimeSeconds != 86400 ||
		overview.Cores != 2 || overview.CPUPercent != 42 {
		t.Errorf("identity and CPU = %+v", overview)
	}
	if len(overview.CPUPerCore) != 2 ||
		overview.CPUPerCore[0] != (hoststats.Core{Name: "cpu0", Percent: 10}) ||
		overview.CPUPerCore[1] != (hoststats.Core{Name: "cpu1", Percent: 74}) {
		t.Errorf("per-core CPU = %+v", overview.CPUPerCore)
	}
	if overview.MemUsedMB != 2048 || overview.MemTotalMB != 8192 ||
		overview.MemFreeMB != 4096 || overview.MemBuffersMB != 128 ||
		overview.MemCachedMB != 1920 {
		t.Errorf("memory detail = %+v", overview)
	}
	if overview.SwapUsedMB != 128 || overview.SwapTotalMB != 1024 {
		t.Errorf("swap = %+v", overview)
	}
	if overview.DiskUsedGB != 60 || overview.DiskTotalGB != 120 ||
		overview.DiskPercent != 50 {
		t.Errorf("disk aggregate = %v/%v %v%%, want the aggregate 60/120 50%%",
			overview.DiskUsedGB, overview.DiskTotalGB, overview.DiskPercent)
	}
	if len(overview.Disks) != 2 || overview.Disks[1].MountPoint != "/data" {
		t.Errorf("disk list = %+v", overview.Disks)
	}
	if overview.DiskReadBps != 1536 || overview.DiskWriteBps != 2048 {
		t.Errorf("disk I/O = %v/%v", overview.DiskReadBps, overview.DiskWriteBps)
	}
	if overview.NetworkRxBps != 3072 || overview.NetworkTxBps != 512 {
		t.Errorf("network rates = %v/%v", overview.NetworkRxBps, overview.NetworkTxBps)
	}
	if len(overview.Interfaces) != 1 || overview.Interfaces[0].Name != "eth0" ||
		overview.Interfaces[0].RxBytes != 1<<20 {
		t.Errorf("interfaces = %+v", overview.Interfaces)
	}
	if len(overview.TopProcesses) != 1 || overview.TopProcesses[0].PID != 42 ||
		overview.TopProcesses[0].MemPercent != 3.5 ||
		overview.TopProcesses[0].Command != "worker" {
		t.Errorf("top processes = %+v", overview.TopProcesses)
	}

	// The projection owns its slices; a later sample changing its own list must
	// not rewrite a rendered Overview.
	sample.CPUPerCore[0].Percent = 99
	sample.Disks[0].MountPoint = "/elsewhere"
	sample.Interfaces[0].Name = "changed"
	sample.TopProcesses[0].Command = "changed"
	if overview.CPUPerCore[0].Percent != 10 || overview.Disks[0].MountPoint != "/" ||
		overview.Interfaces[0].Name != "eth0" ||
		overview.TopProcesses[0].Command != "worker" {
		t.Errorf("the Overview aliases the sample's slices: %+v", overview)
	}
}

// TestOverviewDiskAggregateFallsBackToRootMount proves the card's aggregate
// comes from the shared deduplication and only falls back to the root mount
// when the aggregate cannot be formed.
func TestOverviewDiskAggregateFallsBackToRootMount(t *testing.T) {
	root := domainmonitor.Disk{
		MountPoint: "/", UsedGB: 10, TotalGB: 40, Percent: 25, CapacityKey: "/dev/a",
	}
	fallback := overviewFromSample(hoststats.Sample{
		DiskUsedGB:  math.NaN(),
		DiskTotalGB: math.NaN(),
		DiskPercent: math.NaN(),
		Disks:       []domainmonitor.Disk{root},
	})
	if fallback.DiskUsedGB != 10 || fallback.DiskTotalGB != 40 ||
		fallback.DiskPercent != 25 {
		t.Errorf("fallback disk = %v/%v %v%%, want the root mount 10/40 25%%",
			fallback.DiskUsedGB, fallback.DiskTotalGB, fallback.DiskPercent)
	}

	none := overviewFromSample(hoststats.Sample{
		DiskUsedGB:  math.NaN(),
		DiskTotalGB: math.NaN(),
		DiskPercent: math.NaN(),
	})
	if !math.IsNaN(none.DiskUsedGB) || !math.IsNaN(none.DiskTotalGB) ||
		!math.IsNaN(none.DiskPercent) {
		t.Errorf("disk without any mount = %+v, want unknown values", none)
	}
}
