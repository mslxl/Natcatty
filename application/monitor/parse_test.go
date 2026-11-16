package monitor

import (
	"math"
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
