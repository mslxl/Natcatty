package hoststats

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

const fullLinuxReply = "NC_LATENCY_MARK|MODE:linux|CPURAW:1000 400|CORES:2|" +
	"PERCORERAW:cpu0:500:200,cpu1:500:200|" +
	"MEMINFO:8000 2000 500 1000 1024 512|" +
	"PROCS:42;5.0;nginx,7;1.0;sshd|" +
	"NET:eth0:1000:2000,eth1:500:700,lo:10:20,veth0:1:2,docker0:3:4,br-ab:5:6|" +
	"GPU:42 1024 8192 NVIDIA GeForce RTX 4090|" +
	"DISKIO:100.0;sda:100:200,nvme0n1:10:20,dm-0:1:1,md0:1:1,loop0:1:1,zram0:1:1|" +
	"HOST:web-1|OS:Ubuntu 24.04|KERNEL:6.8.0|UPTIME:12345|" +
	"LOAD:0.50 0.40 0.30|" +
	"DISKS:/:10.000000:40.000000:25:fs=ext4:/dev/sda1," +
	"/mnt/nfs:1.000000:10.000000:10:fs=nfs:server:/export," +
	"/snap/core:1.000000:2.000000:50:fs=squashfs:/dev/loop3," +
	"/proc:0.000000:1.000000:0:fs=proc:proc"

const firstLinuxReply = fullLinuxReply

const secondLinuxReply = "NC_LATENCY_MARK|MODE:linux|CPURAW:1200 440|CORES:2|" +
	"PERCORERAW:cpu1:600:220,cpu0:600:220|" +
	"MEMINFO:8000 2000 500 1000 0 0|" +
	"NET:eth0:2000:2500,eth1:1000:1000|" +
	"DISKIO:110.0;sda:300:500,nvme0n1:10:20|" +
	"HOST:web-1|DISKS:/:10.000000:40.000000:25:fs=ext4:/dev/sda1"

const fullMacReply = "NC_LATENCY_MARK|MODE:darwin|CPU:37|CORES:8|" +
	"MEMINFO:16384 4096 0 6144 2048 1024|" +
	"PROCS:99;9.5;WindowServer,12;2.0;loginwindow|" +
	"DISKS:/:20.000000:100.000000:20:apfs:/dev/disk3," +
	"/Volumes/Data:80.000000:100.000000:80:apfs:/dev/disk3," +
	"/Volumes/External:5.000000:50.000000:10:fs=exfat:/dev/disk4s1|" +
	"NET:en0:10:20,lo0:1:2,utun0:3:4|" +
	"DISKIO:100.0;disk0:100:200|" +
	"HOST:mac|OS:macOS 15|KERNEL:24.0|UPTIME:99|LOAD:1 2 3"

func mustParse(t *testing.T, output string) Parsed {
	t.Helper()
	parsed, err := Parse(output)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return parsed
}

func mustSample(t *testing.T, parsed Parsed, previous State, at time.Time) (Sample, State) {
	t.Helper()
	return parsed.Sample(previous, at, 20*time.Millisecond)
}

func assertNaN(t *testing.T, name string, value float64) {
	t.Helper()
	if !math.IsNaN(value) {
		t.Errorf("%s = %v, want NaN", name, value)
	}
}

func assertClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestParseFullLinuxReplyProducesOneStatsSample(t *testing.T) {
	at := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	sample, state := mustSample(t, mustParse(t, fullLinuxReply), State{}, at)

	if !sample.SampledAt.Equal(at) {
		t.Errorf("SampledAt = %v, want %v", sample.SampledAt, at)
	}
	if sample.LatencyMS != 20 {
		t.Errorf("LatencyMS = %v, want 20", sample.LatencyMS)
	}
	assertNaN(t, "first CPU", sample.CPU)
	if sample.Cores != 2 {
		t.Errorf("Cores = %d, want 2", sample.Cores)
	}
	if len(sample.CPUPerCore) != 2 ||
		sample.CPUPerCore[0].Name != "cpu0" || sample.CPUPerCore[1].Name != "cpu1" {
		t.Fatalf("CPUPerCore = %+v, want cpu0 and cpu1", sample.CPUPerCore)
	}
	assertNaN(t, "first per-core cpu0", sample.CPUPerCore[0].Percent)
	assertNaN(t, "first per-core cpu1", sample.CPUPerCore[1].Percent)

	assertClose(t, "MemTotalMB", sample.MemTotalMB, 8000)
	assertClose(t, "MemUsedMB", sample.MemUsedMB, 4500)
	assertClose(t, "MemFreeMB", sample.MemFreeMB, 2000)
	assertClose(t, "MemBuffersMB", sample.MemBuffersMB, 500)
	assertClose(t, "MemCachedMB", sample.MemCachedMB, 1000)
	assertClose(t, "SwapTotalMB", sample.SwapTotalMB, 1024)
	assertClose(t, "SwapUsedMB", sample.SwapUsedMB, 512)

	if len(sample.Disks) != 1 || sample.Disks[0].MountPoint != "/" {
		t.Fatalf("Disks = %+v, want only the local root mount", sample.Disks)
	}
	assertClose(t, "DiskUsedGB", sample.DiskUsedGB, 10)
	assertClose(t, "DiskTotalGB", sample.DiskTotalGB, 40)
	assertClose(t, "DiskPercent", sample.DiskPercent, 25)

	assertNaN(t, "first disk read", sample.DiskReadBps)
	assertNaN(t, "first disk write", sample.DiskWriteBps)
	if len(sample.Interfaces) != 2 ||
		sample.Interfaces[0].Name != "eth0" || sample.Interfaces[1].Name != "eth1" {
		t.Fatalf("Interfaces = %+v, want eth0 and eth1", sample.Interfaces)
	}
	if sample.Interfaces[0].RxBytes != 1000 || sample.Interfaces[0].TxBytes != 2000 {
		t.Errorf("eth0 counters = %+v", sample.Interfaces[0])
	}
	assertNaN(t, "first eth0 RX rate", sample.Interfaces[0].RxBps)
	assertNaN(t, "first aggregate RX", sample.NetworkRxBps)
	assertNaN(t, "first aggregate TX", sample.NetworkTxBps)

	if len(sample.TopProcesses) != 2 ||
		sample.TopProcesses[0].PID != 42 || sample.TopProcesses[0].Command != "nginx" ||
		sample.TopProcesses[0].MemPercent != 5 {
		t.Errorf("TopProcesses = %+v, want nginx first", sample.TopProcesses)
	}
	if sample.GPU != 42 || sample.GPUMemUsedMB != 1024 || sample.GPUMemTotalMB != 8192 ||
		sample.GPUName != "NVIDIA GeForce RTX 4090" {
		t.Errorf("GPU = %+v", sample)
	}
	if sample.Hostname != "web-1" || sample.OSName != "Ubuntu 24.04" ||
		sample.KernelRelease != "6.8.0" || sample.UptimeSeconds != 12345 {
		t.Errorf("identity = %+v", sample)
	}
	if len(sample.LoadAverage) != 3 || sample.LoadAverage[0] != 0.5 {
		t.Errorf("LoadAverage = %v", sample.LoadAverage)
	}
	if !state.hasCPU || state.perCore["cpu0"].total != 500 ||
		state.network["eth0"].rx != 1000 || state.diskIO == nil {
		t.Errorf("next State did not retain the counters: %+v", state)
	}
}

func TestParseMacOSReplyKeepsDirectCPUAndAPFSDedup(t *testing.T) {
	at := time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC)
	sample, _ := mustSample(t, mustParse(t, fullMacReply), State{}, at)

	assertClose(t, "macOS CPU", sample.CPU, 37)
	if sample.Cores != 8 {
		t.Errorf("Cores = %d, want 8", sample.Cores)
	}
	if len(sample.CPUPerCore) != 0 {
		t.Errorf("CPUPerCore = %+v, want no per-core data on macOS", sample.CPUPerCore)
	}
	assertClose(t, "MemUsedMB", sample.MemUsedMB, 16384-4096-0-6144)
	if len(sample.Disks) != 3 {
		t.Fatalf("Disks = %+v, want three kept mounts", sample.Disks)
	}
	if len(sample.Interfaces) != 2 ||
		sample.Interfaces[0].Name != "en0" || sample.Interfaces[1].Name != "utun0" {
		t.Fatalf("Interfaces = %+v, want loopback excluded", sample.Interfaces)
	}
	// disk3 backs both / and /Volumes/Data; its capacity counts once.
	assertClose(t, "DiskUsedGB", sample.DiskUsedGB, 85)
	assertClose(t, "DiskTotalGB", sample.DiskTotalGB, 150)
	assertClose(t, "DiskPercent", sample.DiskPercent, 85.0/150*100)
	assertNaN(t, "macOS disk read", sample.DiskReadBps)
	assertNaN(t, "macOS disk write", sample.DiskWriteBps)
	if len(sample.TopProcesses) != 2 || sample.TopProcesses[0].PID != 99 {
		t.Errorf("TopProcesses = %+v", sample.TopProcesses)
	}
}

func TestParsePartialReplyUsesNaNAndEmptyLists(t *testing.T) {
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	sample, _ := mustSample(t,
		mustParse(t, "MODE:linux|HOST:web|DISKS:/:1.000000:2.000000:50:fs=ext4:/dev/sda1"),
		State{}, at)

	assertNaN(t, "CPU", sample.CPU)
	assertNaN(t, "memory total", sample.MemTotalMB)
	assertNaN(t, "memory used", sample.MemUsedMB)
	assertNaN(t, "swap total", sample.SwapTotalMB)
	assertNaN(t, "swap used", sample.SwapUsedMB)
	assertNaN(t, "disk read", sample.DiskReadBps)
	assertNaN(t, "disk write", sample.DiskWriteBps)
	assertNaN(t, "network RX", sample.NetworkRxBps)
	assertNaN(t, "network TX", sample.NetworkTxBps)
	assertNaN(t, "GPU", sample.GPU)
	assertNaN(t, "GPU memory used", sample.GPUMemUsedMB)
	assertNaN(t, "uptime", sample.UptimeSeconds)
	if sample.Cores != 0 {
		t.Errorf("Cores = %d, want 0", sample.Cores)
	}
	if sample.CPUPerCore == nil || len(sample.CPUPerCore) != 0 {
		t.Errorf("CPUPerCore = %#v, want an empty slice", sample.CPUPerCore)
	}
	if sample.Interfaces == nil || len(sample.Interfaces) != 0 {
		t.Errorf("Interfaces = %#v, want an empty slice", sample.Interfaces)
	}
	if sample.TopProcesses == nil || len(sample.TopProcesses) != 0 {
		t.Errorf("TopProcesses = %#v, want an empty slice", sample.TopProcesses)
	}
	if sample.LoadAverage == nil || len(sample.LoadAverage) != 0 {
		t.Errorf("LoadAverage = %#v, want an empty slice", sample.LoadAverage)
	}
	if sample.Disks == nil || len(sample.Disks) != 1 || sample.Disks[0].MountPoint != "/" {
		t.Errorf("Disks = %#v, want the one parsed mount", sample.Disks)
	}

	withoutDisks, _ := mustSample(t, mustParse(t, "MODE:linux|CPURAW:1000 400"), State{}, at)
	if withoutDisks.Disks == nil || len(withoutDisks.Disks) != 0 {
		t.Errorf("Disks = %#v, want an empty slice when DISKS is absent", withoutDisks.Disks)
	}
	if sample.Hostname != "web" {
		t.Errorf("Hostname = %q, want web", sample.Hostname)
	}
	if !sample.SampledAt.Equal(at) {
		t.Errorf("SampledAt = %v, want %v", sample.SampledAt, at)
	}
}

func TestFirstLinuxSampleIsUnknownAndSecondDerivesEveryDelta(t *testing.T) {
	start := time.Unix(100, 0)
	first, err := Parse(firstLinuxReply)
	if err != nil {
		t.Fatal(err)
	}
	firstSample, previous := first.Sample(State{}, start, 10*time.Millisecond)
	// The first Linux sample has no earlier counters, so every derived
	// metric is unknown rather than a fabricated zero.
	assertNaN(t, "first CPU", firstSample.CPU)
	if len(firstSample.CPUPerCore) != 2 {
		t.Fatalf("first CPUPerCore = %+v", firstSample.CPUPerCore)
	}
	assertNaN(t, "first per-core cpu0", firstSample.CPUPerCore[0].Percent)
	assertNaN(t, "first per-core cpu1", firstSample.CPUPerCore[1].Percent)
	assertNaN(t, "first disk read", firstSample.DiskReadBps)
	assertNaN(t, "first disk write", firstSample.DiskWriteBps)
	assertNaN(t, "first network RX", firstSample.NetworkRxBps)
	assertNaN(t, "first network TX", firstSample.NetworkTxBps)

	second, err := Parse(secondLinuxReply)
	if err != nil {
		t.Fatal(err)
	}
	sample, _ := second.Sample(previous, start.Add(10*time.Second), 15*time.Millisecond)
	assertClose(t, "CPU", sample.CPU, 80)
	if len(sample.CPUPerCore) != 2 ||
		sample.CPUPerCore[0].Name != "cpu1" || sample.CPUPerCore[1].Name != "cpu0" {
		t.Fatalf("CPUPerCore = %+v, want the reply's cpu1,cpu0 order", sample.CPUPerCore)
	}
	assertClose(t, "per-core cpu1", sample.CPUPerCore[0].Percent, 80)
	assertClose(t, "per-core cpu0", sample.CPUPerCore[1].Percent, 80)
	assertClose(t, "disk read", sample.DiskReadBps, 200.0*512/10)
	assertClose(t, "disk write", sample.DiskWriteBps, 300.0*512/10)
	if len(sample.Interfaces) != 2 {
		t.Fatalf("Interfaces = %+v", sample.Interfaces)
	}
	assertClose(t, "eth0 RX", sample.Interfaces[0].RxBps, 100)
	assertClose(t, "eth0 TX", sample.Interfaces[0].TxBps, 50)
	assertClose(t, "eth1 RX", sample.Interfaces[1].RxBps, 50)
	assertClose(t, "eth1 TX", sample.Interfaces[1].TxBps, 30)
	assertClose(t, "aggregate RX", sample.NetworkRxBps, 150)
	assertClose(t, "aggregate TX", sample.NetworkTxBps, 80)
	if sample.LatencyMS != 15 {
		t.Errorf("LatencyMS = %v, want 15", sample.LatencyMS)
	}
}

func TestPerCoreDeltaAlignsByNameAndResetsOnSetChange(t *testing.T) {
	first := mustParse(t, "MODE:linux|CPURAW:1000 400|PERCORERAW:cpu0:500:200,cpu1:500:200")
	_, previous := first.Sample(State{}, time.Unix(100, 0), 0)

	changed := mustParse(t, "MODE:linux|CPURAW:1200 440|PERCORERAW:cpu0:600:220,cpu2:600:200")
	sample, next := changed.Sample(previous, time.Unix(110, 0), 0)
	if len(sample.CPUPerCore) != 2 {
		t.Fatalf("CPUPerCore = %+v", sample.CPUPerCore)
	}
	if sample.CPUPerCore[0].Name != "cpu0" || sample.CPUPerCore[1].Name != "cpu2" {
		t.Errorf("CPUPerCore names = %+v", sample.CPUPerCore)
	}
	assertNaN(t, "cpu0 after set change", sample.CPUPerCore[0].Percent)
	assertNaN(t, "cpu2 after set change", sample.CPUPerCore[1].Percent)

	stable := mustParse(t, "MODE:linux|CPURAW:1400 480|PERCORERAW:cpu0:700:250,cpu2:700:220")
	nextSample, _ := stable.Sample(next, time.Unix(120, 0), 0)
	assertClose(t, "cpu0 after stable set", nextSample.CPUPerCore[0].Percent, 70)
	assertClose(t, "cpu2 after stable set", nextSample.CPUPerCore[1].Percent, 80)
}

func TestPerCoreDeltaAlignsReorderedCoresByName(t *testing.T) {
	first := mustParse(t, "MODE:linux|CPURAW:1000 400|PERCORERAW:cpu0:500:200,cpu1:700:100")
	_, previous := first.Sample(State{}, time.Unix(100, 0), 0)

	reordered := mustParse(t, "MODE:linux|CPURAW:1200 440|PERCORERAW:cpu1:900:150,cpu0:600:250")
	sample, _ := reordered.Sample(previous, time.Unix(110, 0), 0)
	if len(sample.CPUPerCore) != 2 ||
		sample.CPUPerCore[0].Name != "cpu1" || sample.CPUPerCore[1].Name != "cpu0" {
		t.Fatalf("CPUPerCore = %+v, want the reply's cpu1,cpu0 order", sample.CPUPerCore)
	}
	// cpu1: +200 total, +50 idle -> 75% busy. cpu0: +100 total, +50 idle ->
	// 50% busy. A positional implementation would pair cpu1 with the old cpu0
	// counters and cpu0 with the old cpu1 counters, yielding unknown for both.
	assertClose(t, "reordered cpu1", sample.CPUPerCore[0].Percent, 75)
	assertClose(t, "reordered cpu0", sample.CPUPerCore[1].Percent, 50)
}

func TestDiskListFilteringAndLoopRoot(t *testing.T) {
	output := "MODE:linux|DISKS:" +
		"/:10.000000:40.000000:25:fs=ext4:/dev/sda1," +
		"/proc:0.000000:1.000000:0:fs=proc:proc," +
		"/dev/shm:0.000000:1.000000:0:fs=tmpfs:tmpfs," +
		"/snap/core:1.000000:2.000000:50:fs=squashfs:/dev/loop3," +
		"/mnt/loop:1.000000:2.000000:50:fs=ext4:/dev/loop0," +
		"/etcetera:1.000000:2.000000:50:fs=ext4:/dev/sda3," +
		"/procdata:1.000000:2.000000:50:fs=ext4:/dev/sda4," +
		"/mnt/nfs:1.000000:10.000000:10:fs=nfs:server:/export," +
		"/mnt/remote:1.000000:10.000000:10:server:/export," +
		"/mnt/fuse:1.000000:10.000000:10:fs=fuse.sshfs:user@host:/x," +
		"/mnt/overlay:1.000000:10.000000:10:overlay:overlay," +
		"/mnt/ovl:1.000000:10.000000:10:fs=overlay:overlay," +
		"/data:20.000000:100.000000:20:fs=ext4:/dev/sda2"
	sample, _ := mustSample(t, mustParse(t, output), State{}, time.Unix(0, 0))
	if len(sample.Disks) != 4 ||
		sample.Disks[0].MountPoint != "/" || sample.Disks[1].MountPoint != "/etcetera" ||
		sample.Disks[2].MountPoint != "/procdata" || sample.Disks[3].MountPoint != "/data" {
		t.Fatalf("Disks = %+v, want root, /etcetera, /procdata, and /data", sample.Disks)
	}

	loopRoot := mustParse(t, "MODE:linux|DISKS:/:1.000000:2.000000:50:fs=ext4:/dev/loop0")
	rootSample, _ := mustSample(t, loopRoot, State{}, time.Unix(0, 0))
	if len(rootSample.Disks) != 1 || rootSample.Disks[0].MountPoint != "/" {
		t.Errorf("loop-backed root was dropped: %+v", rootSample.Disks)
	}

	overlayRoot := mustParse(t, "MODE:linux|DISKS:/:1.000000:2.000000:50:fs=overlay:overlay")
	overlaySample, _ := mustSample(t, overlayRoot, State{}, time.Unix(0, 0))
	if len(overlaySample.Disks) != 1 || overlaySample.Disks[0].MountPoint != "/" {
		t.Errorf("overlay-backed root was dropped: %+v", overlaySample.Disks)
	}
}

func TestDiskIORatesUsePhysicalDevicesAndMoveToUnknownOnReset(t *testing.T) {
	first := mustParse(t, "MODE:linux|DISKIO:100.0;sda:100:200,nvme0n1:10:20,dm-0:1:1,md0:1:1,loop0:1:1,zram0:1:1")
	firstSample, previous := first.Sample(State{}, time.Unix(100, 0), 0)
	assertNaN(t, "first disk read", firstSample.DiskReadBps)
	assertNaN(t, "first disk write", firstSample.DiskWriteBps)

	second := mustParse(t, "MODE:linux|DISKIO:110.0;sda:300:500,nvme0n1:10:20")
	sample, next := second.Sample(previous, time.Unix(110, 0), 0)
	assertClose(t, "disk read", sample.DiskReadBps, (200+0)*512.0/10)
	assertClose(t, "disk write", sample.DiskWriteBps, (300+0)*512.0/10)

	regressed := mustParse(t, "MODE:linux|DISKIO:120.0;sda:200:400,nvme0n1:10:20")
	regressedSample, _ := regressed.Sample(next, time.Unix(120, 0), 0)
	assertNaN(t, "regressed disk read", regressedSample.DiskReadBps)
	assertNaN(t, "regressed disk write", regressedSample.DiskWriteBps)

	changedSet := mustParse(t, "MODE:linux|DISKIO:120.0;sda:300:500")
	changedSample, _ := changedSet.Sample(next, time.Unix(120, 0), 0)
	assertNaN(t, "changed-set disk read", changedSample.DiskReadBps)
	assertNaN(t, "changed-set disk write", changedSample.DiskWriteBps)

	onlyVirtual := mustParse(t, "MODE:linux|DISKIO:100.0;dm-0:1:1,loop0:1:1")
	_, virtualPrevious := onlyVirtual.Sample(State{}, time.Unix(100, 0), 0)
	onlyVirtual2 := mustParse(t, "MODE:linux|DISKIO:110.0;dm-0:9:9,loop0:9:9")
	virtualSample, _ := onlyVirtual2.Sample(virtualPrevious, time.Unix(110, 0), 0)
	assertNaN(t, "virtual-only disk read", virtualSample.DiskReadBps)
	assertNaN(t, "virtual-only disk write", virtualSample.DiskWriteBps)

	mac := mustParse(t, "MODE:darwin|CPU:10|DISKIO:100.0;disk0:100:200")
	_, macPrevious := mac.Sample(State{}, time.Unix(100, 0), 0)
	mac2 := mustParse(t, "MODE:darwin|CPU:10|DISKIO:110.0;disk0:300:500")
	macSample, _ := mac2.Sample(macPrevious, time.Unix(110, 0), 0)
	assertNaN(t, "macOS disk read", macSample.DiskReadBps)
	assertNaN(t, "macOS disk write", macSample.DiskWriteBps)
}

func TestNetworkFilteringAndCounterResets(t *testing.T) {
	linux := mustParse(t, "MODE:linux|CPURAW:10 5|"+
		"NET:lo:1:1,veth0:1:1,docker0:1:1,br-ab:1:1,eth0:100:200")
	first, previous := linux.Sample(State{}, time.Unix(100, 0), 0)
	if len(first.Interfaces) != 1 || first.Interfaces[0].Name != "eth0" {
		t.Fatalf("Linux Interfaces = %+v, want only eth0", first.Interfaces)
	}

	second := mustParse(t, "MODE:linux|CPURAW:20 10|"+
		"NET:lo:2:2,veth0:2:2,docker0:2:2,br-ab:2:2,eth0:50:300")
	reset, _ := second.Sample(previous, time.Unix(110, 0), 0)
	if len(reset.Interfaces) != 1 {
		t.Fatalf("Linux Interfaces = %+v", reset.Interfaces)
	}
	assertClose(t, "reset RX rate", reset.Interfaces[0].RxBps, 0)
	assertClose(t, "TX rate", reset.Interfaces[0].TxBps, 10)
	assertClose(t, "aggregate RX", reset.NetworkRxBps, 0)
	assertClose(t, "aggregate TX", reset.NetworkTxBps, 10)

	mac := mustParse(t, "MODE:darwin|CPU:10|NET:lo0:1:1,lo1:1:1,en0:10:20")
	macSample, _ := mac.Sample(State{}, time.Unix(100, 0), 0)
	if len(macSample.Interfaces) != 1 || macSample.Interfaces[0].Name != "en0" {
		t.Fatalf("macOS Interfaces = %+v, want only en0", macSample.Interfaces)
	}
}

func TestMacOSNetworkRatesDeriveAcrossSamples(t *testing.T) {
	first := mustParse(t, "MODE:darwin|CPU:10|NET:en0:1000:2000,utun0:10:20")
	firstSample, previous := first.Sample(State{}, time.Unix(100, 0), 0)
	if len(firstSample.Interfaces) != 2 {
		t.Fatalf("first Interfaces = %+v", firstSample.Interfaces)
	}
	assertNaN(t, "first en0 RX rate", firstSample.Interfaces[0].RxBps)
	assertNaN(t, "first en0 TX rate", firstSample.Interfaces[0].TxBps)
	assertNaN(t, "first aggregate RX", firstSample.NetworkRxBps)
	assertNaN(t, "first aggregate TX", firstSample.NetworkTxBps)

	second := mustParse(t, "MODE:darwin|CPU:10|NET:en0:3000:5000,utun0:30:80")
	sample, _ := second.Sample(previous, time.Unix(110, 0), 0)
	if len(sample.Interfaces) != 2 ||
		sample.Interfaces[0].Name != "en0" || sample.Interfaces[1].Name != "utun0" {
		t.Fatalf("Interfaces = %+v, want en0 and utun0", sample.Interfaces)
	}
	assertClose(t, "en0 RX", sample.Interfaces[0].RxBps, 200)
	assertClose(t, "en0 TX", sample.Interfaces[0].TxBps, 300)
	assertClose(t, "utun0 RX", sample.Interfaces[1].RxBps, 2)
	assertClose(t, "utun0 TX", sample.Interfaces[1].TxBps, 6)
	assertClose(t, "aggregate RX", sample.NetworkRxBps, 202)
	assertClose(t, "aggregate TX", sample.NetworkTxBps, 306)
}

func TestTopProcessesKeepTenByMemoryPercent(t *testing.T) {
	var entries []string
	for pid := 1; pid <= 12; pid++ {
		entries = append(entries, strings.Join([]string{
			itoa(pid), floatString(float64(pid)), "command-" + itoa(pid),
		}, ";"))
	}
	entries = append(entries, "bad;2.0;ignored", "13;nope;ignored")
	sample, _ := mustSample(t, mustParse(t,
		"MODE:linux|PROCS:"+strings.Join(entries, ",")), State{}, time.Unix(0, 0))
	if len(sample.TopProcesses) != 10 {
		t.Fatalf("TopProcesses = %d, want 10", len(sample.TopProcesses))
	}
	for index := 0; index < 10; index++ {
		wantPID := 12 - index
		if sample.TopProcesses[index].PID != wantPID {
			t.Errorf("TopProcesses[%d] = %+v, want PID %d", index, sample.TopProcesses[index], wantPID)
		}
	}

	withSemicolon := mustParse(t, "MODE:linux|PROCS:5;3.0;comm;with;semicolons")
	semicolonSample, _ := mustSample(t, withSemicolon, State{}, time.Unix(0, 0))
	if len(semicolonSample.TopProcesses) != 1 ||
		semicolonSample.TopProcesses[0].Command != "comm;with;semicolons" {
		t.Errorf("command with semicolons = %+v", semicolonSample.TopProcesses)
	}
}

func TestParseReportsTypedReasonsRatherThanAnEmptySample(t *testing.T) {
	_, err := Parse("NC_LATENCY_MARK|UNSUPPORTED_OS:FreeBSD")
	var typed *Error
	if !errors.As(err, &typed) || typed.Reason != ReasonOS {
		t.Fatalf("unsupported OS error = %v, want ReasonOS", err)
	}
	_, err = Parse("NC_LATENCY_MARK|MODE:linux|HOST:web-1|UPTIME:1")
	if !errors.As(err, &typed) || typed.Reason != ReasonUnsupported {
		t.Fatalf("empty metrics error = %v, want ReasonUnsupported", err)
	}
}

func TestPerCoreLegacyPositionalEntriesStillAlign(t *testing.T) {
	first := mustParse(t, "MODE:linux|CPURAW:1000 400|PERCORERAW:500:200,500:200")
	_, previous := first.Sample(State{}, time.Unix(100, 0), 0)
	second := mustParse(t, "MODE:linux|CPURAW:1200 440|PERCORERAW:600:220,600:220")
	sample, _ := second.Sample(previous, time.Unix(110, 0), 0)
	if len(sample.CPUPerCore) != 2 ||
		sample.CPUPerCore[0].Name != "cpu0" || sample.CPUPerCore[1].Name != "cpu1" {
		t.Fatalf("CPUPerCore = %+v", sample.CPUPerCore)
	}
	assertClose(t, "cpu0", sample.CPUPerCore[0].Percent, 80)
	assertClose(t, "cpu1", sample.CPUPerCore[1].Percent, 80)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func floatString(value float64) string {
	if value == math.Trunc(value) {
		return itoa(int(value)) + ".0"
	}
	return "0"
}
