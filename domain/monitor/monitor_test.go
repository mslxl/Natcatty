package monitor

import (
	"math"
	"testing"
	"time"
)

func TestPercentThresholdsMirrorResourceBar(t *testing.T) {
	tests := []struct {
		value float64
		want  Tone
	}{
		{0, ToneNormal}, {60, ToneNormal}, {60.1, ToneWarning},
		{85, ToneWarning}, {85.1, ToneDanger}, {100, ToneDanger},
		{math.NaN(), ToneUnknown},
	}
	for _, test := range tests {
		if got := ToneForPercent(test.value); got != test.want {
			t.Errorf("ToneForPercent(%v) = %v, want %v", test.value, got, test.want)
		}
	}
	if got := ToneForPercent(50); got != ToneNormal {
		t.Errorf("ToneForPercent(50) = %v", got)
	}
}

func TestLatencyThresholdsMirrorSignalBars(t *testing.T) {
	tests := []struct {
		value float64
		want  Tone
	}{
		{0, ToneNormal}, {100, ToneNormal}, {100.1, ToneWarning},
		{300, ToneWarning}, {300.1, ToneDanger}, {-1, ToneUnknown},
		{math.NaN(), ToneUnknown},
	}
	for _, test := range tests {
		if got := ToneForLatency(test.value); got != test.want {
			t.Errorf("ToneForLatency(%v) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestMemoryPercentUsesUsedOverTotal(t *testing.T) {
	sample := Sample{MemUsedMB: 2048, MemTotalMB: 8192}
	if got := MemoryPercent(sample); math.Abs(got-25) > 0.001 {
		t.Fatalf("MemoryPercent = %v, want 25", got)
	}
	if got := MemoryPercent(Sample{MemUsedMB: 1}); !math.IsNaN(got) {
		t.Fatalf("MemoryPercent without a total = %v, want NaN", got)
	}
	if got := PercentOf(150, 100); got != 100 {
		t.Fatalf("PercentOf clamps = %v, want 100", got)
	}
}

func TestIsStaleUsesTheOriginalFreshnessWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	live := HostState{Status: StatusLive, Sample: Sample{At: now.Add(-6 * time.Second)}}
	if !IsStale(live, time.Second, now) {
		t.Error("a six-second-old sample at a one-second interval is not stale")
	}
	live.Sample.At = now.Add(-4 * time.Second)
	if IsStale(live, time.Second, now) {
		t.Error("a four-second-old sample is stale, want the five-second floor")
	}
	live.Status = StatusQueued
	if IsStale(live, time.Second, now) {
		t.Error("a non-live Host is stale")
	}
	long := HostState{Status: StatusLive, Sample: Sample{At: now.Add(-26 * time.Second)}}
	if !IsStale(long, 10*time.Second, now) {
		t.Error("a sample older than 2.5 intervals is not stale")
	}
}

func TestAppendPointKeepsTheRetentionWindowAndCap(t *testing.T) {
	now := time.Unix(100, 0)
	points := []Point{{At: now.Add(-HistoryRetention - time.Minute)}}
	points = AppendPoint(points, Sample{At: now, CPU: 42})
	if len(points) != 1 || points[0].CPU != 42 {
		t.Fatalf("points = %+v, want only the fresh sample", points)
	}
	points = AppendPoint(points, Sample{At: now.Add(time.Second),
		CPU: math.NaN(), Memory: math.NaN(), Disk: math.NaN(),
		Rx: math.NaN(), Tx: math.NaN(), GPU: math.NaN()})
	if len(points) != 1 {
		t.Fatalf("an all-unknown sample was appended: %+v", points)
	}
	for index := 0; index < MaxPoints+10; index++ {
		points = AppendPoint(points, Sample{At: now.Add(time.Duration(index) * time.Second), CPU: 1})
	}
	if len(points) != MaxPoints {
		t.Fatalf("history length = %d, want %d", len(points), MaxPoints)
	}
}

func TestAggregateDisksCountsPhysicalCapacityOnceAndSkipsNetwork(t *testing.T) {
	disks := []Disk{
		{MountPoint: "/", UsedGB: 20, TotalGB: 100, CapacityKey: "/dev/sda1", FilesystemType: "ext4"},
		{MountPoint: "/boot", UsedGB: 1, TotalGB: 100, CapacityKey: "/dev/sda1", FilesystemType: "ext4"},
		{MountPoint: "/data", UsedGB: 50, TotalGB: 200, CapacityKey: "/dev/sdb1", FilesystemType: "xfs"},
		{MountPoint: "/mnt/nfs", UsedGB: 900, TotalGB: 1000, CapacityKey: "host:/export", FilesystemType: "nfs4"},
		{MountPoint: "/mnt/cloud", UsedGB: 10, TotalGB: 10, CapacityKey: "rclone:remote", FilesystemType: "fuse.rclone"},
	}
	used, total, percent := AggregateDisks(disks)
	if used != 70 || total != 300 {
		t.Fatalf("aggregate = %v/%v, want 70/300", used, total)
	}
	if math.Abs(percent-70.0/3) > 0.001 {
		t.Fatalf("percent = %v", percent)
	}
}

func TestAggregateDisksSkipsFuseSourcesEvenWhenTypeIsMissing(t *testing.T) {
	disks := []Disk{
		{MountPoint: "/mnt/a", UsedGB: 10, TotalGB: 10, CapacityKey: "//server/share"},
		{MountPoint: "/mnt/b", UsedGB: 10, TotalGB: 10, CapacityKey: "sshfs"},
		{MountPoint: "/mnt/c", UsedGB: 10, TotalGB: 10, CapacityKey: "[fe80::1]:/export"},
	}
	if _, total, _ := AggregateDisks(disks); total != 0 {
		t.Fatalf("network mounts contributed %v GB", total)
	}
}
