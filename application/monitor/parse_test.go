package monitor

import (
	"math"
	"strings"
	"testing"
	"time"

	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
)

const twoGPUStatsOutput = "NC_LATENCY_MARK|CPURAW:1000 400|CORES:4|" +
	"PERCORERAW:500:200,500:200,500:200,500:200|MEMINFO:8000 2000 500 1000 1024 512|" +
	"GPU:42 1024 8192 NVIDIA GeForce RTX 4090,18 512 40960 NVIDIA A100-SXM4-40GB|" +
	"HOST:gpu-1|OS:Ubuntu 24.04|KERNEL:6.8.0|UPTIME:12345"

// TestParseStatsKeepsOneEntryPerGPU is the two-GPU fixture: each device keeps
// its own utilization and memory instead of one averaged triple.
func TestParseStatsKeepsOneEntryPerGPU(t *testing.T) {
	parsed, err := parseStats(twoGPUStatsOutput)
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	sample, _ := parsed.sample(samplerState{}, time.Unix(100, 0), time.Millisecond)
	if len(sample.GPUs) != 2 {
		t.Fatalf("gpus = %+v, want two devices", sample.GPUs)
	}
	first, second := sample.GPUs[0], sample.GPUs[1]
	if first.Utilization != 42 || first.MemUsedMB != 1024 || first.MemTotalMB != 8192 ||
		first.Name != "NVIDIA GeForce RTX 4090" {
		t.Errorf("first gpu = %+v", first)
	}
	if second.Utilization != 18 || second.MemUsedMB != 512 || second.MemTotalMB != 40960 ||
		second.Name != "NVIDIA A100-SXM4-40GB" {
		t.Errorf("second gpu = %+v", second)
	}
	if first.Utilization == second.Utilization && first.MemUsedMB == second.MemUsedMB {
		t.Error("the two GPUs were aggregated into one reading")
	}
}

// TestParseStatsSingleGPUMatchesLegacyReading proves the one-GPU fixture
// produces the same values the aggregate triple used to hold.
func TestParseStatsSingleGPUMatchesLegacyReading(t *testing.T) {
	parsed, err := parseStats(linuxStatsOutput)
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	sample, _ := parsed.sample(samplerState{}, time.Unix(100, 0), time.Millisecond)
	if len(sample.GPUs) != 1 {
		t.Fatalf("gpus = %+v, want one device", sample.GPUs)
	}
	gpu := sample.GPUs[0]
	if gpu.Utilization != 42 || gpu.MemUsedMB != 1024 || gpu.MemTotalMB != 8192 ||
		gpu.Name != "NVIDIA GeForce RTX 4090" {
		t.Errorf("gpu = %+v", gpu)
	}
	if got := domainmonitor.GPUUtilization(sample); got != 42 {
		t.Errorf("single-GPU trend value = %v, want 42", got)
	}
}

// TestParseStatsGPUKeepsUnreportedMetricsUnknown covers nvidia-smi's N/A
// memory columns; an unknown value must stay NaN, not become zero.
func TestParseStatsGPUKeepsUnreportedMetricsUnknown(t *testing.T) {
	output := "NC_LATENCY_MARK|GPU:7 N/A N/A NVIDIA A100|MEMINFO:100 10 0 0 0 0"
	parsed, err := parseStats(output)
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	sample, _ := parsed.sample(samplerState{}, time.Unix(100, 0), time.Millisecond)
	if len(sample.GPUs) != 1 {
		t.Fatalf("gpus = %+v", sample.GPUs)
	}
	gpu := sample.GPUs[0]
	if gpu.Utilization != 7 {
		t.Errorf("utilization = %v, want 7", gpu.Utilization)
	}
	if !math.IsNaN(gpu.MemUsedMB) || !math.IsNaN(gpu.MemTotalMB) {
		t.Errorf("memory = %v/%v, want NaN", gpu.MemUsedMB, gpu.MemTotalMB)
	}
}

// TestParseDiskIODerivesRatesPerDevice is the two-device fixture: each
// physical device keeps its own rate, derived from the remote uptime delta
// and 512-byte sectors, and the first reading reports no rate.
func TestParseDiskIODerivesRatesPerDevice(t *testing.T) {
	first, err := parseStats("NC_LATENCY_MARK|CPURAW:1000 400|" +
		"DISKIO:100.25;sda:100:200,nvme0n1:50:100,")
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	sample, baseline := first.sample(samplerState{}, time.Unix(100, 0), time.Millisecond)
	if len(sample.DiskIO) != 2 {
		t.Fatalf("first sample disk I/O = %+v, want two devices", sample.DiskIO)
	}
	for _, device := range sample.DiskIO {
		if !math.IsNaN(device.ReadBytesPerSecond) || !math.IsNaN(device.WriteBytesPerSecond) {
			t.Errorf("first sample %q = %+v, want unknown rates", device.Device, device)
		}
	}

	second, err := parseStats("NC_LATENCY_MARK|CPURAW:1100 420|" +
		"DISKIO:101.75;nvme0n1:80:140,sda:130:220,")
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	sample, _ = second.sample(baseline, time.Unix(101, 0), time.Millisecond)
	if len(sample.DiskIO) != 2 {
		t.Fatalf("second sample disk I/O = %+v, want two devices", sample.DiskIO)
	}
	// The devices are sorted by name; 30 and 40 sectors over 1.5 remote
	// seconds become 10240 and 13653 bytes per second.
	if got := sample.DiskIO[0]; got.Device != "nvme0n1" ||
		!closeEnough(got.ReadBytesPerSecond, 30*512/1.5) ||
		!closeEnough(got.WriteBytesPerSecond, 40*512/1.5) {
		t.Errorf("nvme0n1 = %+v", got)
	}
	if got := sample.DiskIO[1]; got.Device != "sda" ||
		!closeEnough(got.ReadBytesPerSecond, 30*512/1.5) ||
		!closeEnough(got.WriteBytesPerSecond, 20*512/1.5) {
		t.Errorf("sda = %+v", got)
	}
}

// TestParseDiskIOResetAndTopologyChangeStayUnknown keeps a reset counter or a
// device that just appeared from looking idle.
func TestParseDiskIOResetAndTopologyChangeStayUnknown(t *testing.T) {
	first, err := parseStats("NC_LATENCY_MARK|MEMINFO:100 10 0 0 0 0|DISKIO:100;sda:100:200,")
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	_, baseline := first.sample(samplerState{}, time.Unix(100, 0), 0)

	reset, err := parseStats("NC_LATENCY_MARK|MEMINFO:100 10 0 0 0 0|DISKIO:101;sda:99:210,")
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	sample, _ := reset.sample(baseline, time.Unix(101, 0), 0)
	if got := sample.DiskIO[0]; !math.IsNaN(got.ReadBytesPerSecond) || !math.IsNaN(got.WriteBytesPerSecond) {
		t.Errorf("reset counters = %+v, want unknown rates", got)
	}

	added, err := parseStats("NC_LATENCY_MARK|MEMINFO:100 10 0 0 0 0|DISKIO:102;sda:130:220,sdb:1:2,")
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	sample, _ = added.sample(baseline, time.Unix(102, 0), 0)
	if got := sample.DiskIO[0]; got.Device != "sda" || !domainmonitor.Valid(got.ReadBytesPerSecond) {
		t.Errorf("existing device = %+v, want a valid rate", got)
	}
	if got := sample.DiskIO[1]; got.Device != "sdb" ||
		!math.IsNaN(got.ReadBytesPerSecond) || !math.IsNaN(got.WriteBytesPerSecond) {
		t.Errorf("new device = %+v, want unknown rates", got)
	}
}

// TestParseDiskIORejectsMalformedReplies proves a partial reply never looks
// like a valid sample, and a platform without counters keeps the plain
// disk-usage view.
func TestParseDiskIORejectsMalformedReplies(t *testing.T) {
	for _, text := range []string{
		"DISKIO:100", "DISKIO:NaN;sda:1:2,", "DISKIO:100;sda:-1:2,",
		"DISKIO:100;sda:1:NaN,", "DISKIO:100;sda:1:2:3,", "DISKIO:100;:1:2,",
	} {
		parsed, err := parseStats("NC_LATENCY_MARK|MEMINFO:100 10 0 0 0 0|" + text)
		if err != nil {
			t.Fatalf("parseStats(%q): %v", text, err)
		}
		if parsed.diskIO != nil {
			t.Errorf("%q parsed as %+v, want no counters", text, parsed.diskIO)
		}
	}
	macOS, err := parseStats("NC_LATENCY_MARK|CPU:12|DISKS:/:10:50:20:/")
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	sample, _ := macOS.sample(samplerState{}, time.Unix(100, 0), 0)
	if len(sample.DiskIO) != 0 {
		t.Errorf("non-Linux disk I/O = %+v, want none", sample.DiskIO)
	}
	if sample.Disk != 20 {
		t.Errorf("non-Linux disk usage = %v, want the reported 20", sample.Disk)
	}
}

// TestParseStatsAcceptsADiskIOOnlyReply proves counters alone count as a
// recognizable metric, so a probe reply without a usage line is not discarded.
func TestParseStatsAcceptsADiskIOOnlyReply(t *testing.T) {
	parsed, err := parseStats("DISKIO:100.5;sda:10:20,")
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	if parsed.diskIO == nil {
		t.Fatal("a disk-I/O-only reply was discarded")
	}
	if got := parsed.diskIO.devices["sda"]; got.read != 10 || got.write != 20 {
		t.Errorf("sda counters = %+v, want 10/20", got)
	}
}

// TestLinuxDiskCommandSamplesDiskstats proves the separate probe reads the
// Linux counters rather than extending every platform's stats request.
func TestLinuxDiskCommandSamplesDiskstats(t *testing.T) {
	command := diskCommand()
	for _, want := range []string{"/proc/diskstats", "/sys/block", "$d/device", "/proc/uptime", "$6", "$10", "DISKIO:"} {
		if !strings.Contains(command, want) {
			t.Errorf("disk probe is missing %q", want)
		}
	}
}

func closeEnough(got, want float64) bool {
	return math.Abs(got-want) < 1e-6
}

// TestParseStatsWithoutGPUKeepsEmptyState covers a machine with no GPU.
func TestParseStatsWithoutGPUKeepsEmptyState(t *testing.T) {
	parsed, err := parseStats("NC_LATENCY_MARK|MEMINFO:100 10 0 0 0 0|CPU:12")
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	sample, _ := parsed.sample(samplerState{}, time.Unix(100, 0), time.Millisecond)
	if len(sample.GPUs) != 0 {
		t.Errorf("gpus = %+v, want none", sample.GPUs)
	}
	if got := domainmonitor.GPUUtilization(sample); !math.IsNaN(got) {
		t.Errorf("trend value = %v, want NaN", got)
	}
}
