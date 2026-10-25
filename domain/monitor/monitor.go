// Package monitor owns Host metric snapshots, their derived values, the
// history each Host keeps, and the thresholds the UI paints them with. It is
// pure: samples arrive from application/monitor, and the surface renders the
// snapshot without knowing how a metric was collected.
package monitor

import (
	"math"
	"time"
)

// MaxHosts is the original's monitor cap: one session samples at most this
// many distinct Hosts.
const MaxHosts = 256

// HistoryRetention is how far back a Host's trend may reach.
const HistoryRetention = 30 * time.Minute

// MaxPoints bounds one Host's history independently of the interval.
const MaxPoints = 1800

// Status is a Host's sampling state. It never carries display text; the
// surface localizes each value.
type Status string

const (
	// StatusQueued is a Host whose sampling has not started.
	StatusQueued Status = "queued"
	// StatusConnecting is a Host whose SSH transport is being established or
	// whose host key is awaiting a trust decision.
	StatusConnecting Status = "connecting"
	// StatusLive is a Host whose last sample succeeded.
	StatusLive Status = "live"
	// StatusLogin is a Host whose credentials or host key were refused. The
	// user must act before sampling can continue.
	StatusLogin Status = "login"
	// StatusError is a collection failure that may recover on retry.
	StatusError Status = "error"
	// StatusUnsupported is a Host whose operating system or transport cannot
	// report metrics.
	StatusUnsupported Status = "unsupported"
	// StatusDisconnected is a Host whose transport dropped.
	StatusDisconnected Status = "disconnected"
	// StatusUnavailable is a Host that has failed repeatedly and is no longer
	// sampled. It is the typed degraded state a surface shows instead of an
	// error dialog.
	StatusUnavailable Status = "unavailable"
	// StatusLimit is a Host beyond the MaxHosts cap.
	StatusLimit Status = "limit"
)

// Reason classifies why a Host is not live. The surface localizes it; it is
// never user-visible text.
type Reason string

const (
	ReasonNone        Reason = ""
	ReasonHostKey     Reason = "host_key"
	ReasonAuth        Reason = "auth"
	ReasonNetwork     Reason = "network"
	ReasonTimeout     Reason = "timeout"
	ReasonUnsupported Reason = "unsupported"
	ReasonCancelled   Reason = "cancelled"
	ReasonInternal    Reason = "internal"
	ReasonOS          Reason = "os"
)

// Sample is one Host measurement. A NaN metric is unknown, matching the
// original's null for a first reading or an unsupported sensor.
type Sample struct {
	// At is when the measurement was taken.
	At time.Time
	// CPU is overall utilization in percent, 0-100.
	CPU float64
	// CPUPerCore is per-core utilization; it may be empty.
	CPUPerCore []float64
	// CPUCores is the logical core count when reported.
	CPUCores int
	// Memory is used memory in percent, excluding buffers and cache.
	Memory float64
	// MemUsedMB and MemTotalMB are the absolute memory figures.
	MemUsedMB  float64
	MemTotalMB float64
	// SwapUsedMB and SwapTotalMB are the swap figures.
	SwapUsedMB  float64
	SwapTotalMB float64
	// Disk is the aggregate local capacity in percent. Disks carries each
	// mount.
	Disk        float64
	DiskUsedGB  float64
	DiskTotalGB float64
	Disks       []Disk
	// Network rates are bytes per second across physical interfaces. They are
	// NaN until a second reading establishes a delta.
	Rx float64
	Tx float64
	// Latency is the metric command's round trip in milliseconds.
	Latency float64
	// GPU is NVIDIA utilization in percent, NaN when unavailable.
	GPU           float64
	GPUName       string
	GPUMemUsedMB  float64
	GPUMemTotalMB float64
	// Host identity reported by the Host itself.
	Hostname      string
	OSName        string
	KernelRelease string
	UptimeSeconds float64
	LoadAverage   []float64
}

// Disk is one mounted filesystem's usage.
type Disk struct {
	MountPoint string
	UsedGB     float64
	TotalGB    float64
	Percent    float64
	// CapacityKey groups mounts backed by the same capacity, so a bind mount
	// is not counted twice. FilesystemType marks network and FUSE capacity.
	CapacityKey    string
	FilesystemType string
}

// Point is one history entry for trends. Unknown values are NaN so a chart
// breaks the line instead of drawing through a gap.
type Point struct {
	At      time.Time
	CPU     float64
	Memory  float64
	Disk    float64
	Rx      float64
	Tx      float64
	GPU     float64
	Latency float64
}

// HostState is everything the surface knows about one Host at one instant.
type HostState struct {
	HostID string
	Status Status
	Reason Reason
	// Detail is a developer diagnostic, never shown untranslated.
	Detail string
	// At is when Status was last updated.
	At time.Time
	// Sample is the latest measurement. Its At may differ from At when a
	// sample and a status change did not coincide.
	Sample Sample
	// Points is the bounded local history in time order.
	Points []Point
}

// Snapshot is the application service's publication to the surface.
type Snapshot struct {
	TakenAt time.Time
	// Hosts is keyed by Host ID.
	Hosts map[string]HostState
	// Running reports which panel ids are sampling.
	Running map[string]bool
	// StartedAt records when each running panel began sampling, so a panel
	// only charts points from its current run.
	StartedAt map[string]time.Time
}

// Valid reports whether a metric has a usable value.
func Valid(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// MemoryPercent derives used memory in percent from the absolute figures.
func MemoryPercent(sample Sample) float64 {
	if sample.MemTotalMB <= 0 || !Valid(sample.MemUsedMB) {
		return math.NaN()
	}
	percent := sample.MemUsedMB / sample.MemTotalMB * 100
	return clampPercent(percent)
}

// PercentOf returns part/total as a percentage, or NaN.
func PercentOf(part, total float64) float64 {
	if total <= 0 || !Valid(part) || !Valid(total) {
		return math.NaN()
	}
	return clampPercent(part / total * 100)
}

func clampPercent(value float64) float64 {
	if !Valid(value) {
		return math.NaN()
	}
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// Tone is how a thresholded metric is painted.
type Tone int

const (
	// ToneNormal is the primary color.
	ToneNormal Tone = iota
	// ToneWarning is amber, above 60 percent.
	ToneWarning
	// ToneDanger is red, above 85 percent or above 300 ms.
	ToneDanger
	// ToneUnknown paints no value.
	ToneUnknown
)

// ToneForPercent mirrors the original ResourceBar: above 85 is danger, above
// 60 is warning, and an unknown value has no tone.
func ToneForPercent(value float64) Tone {
	if !Valid(value) {
		return ToneUnknown
	}
	if value > 85 {
		return ToneDanger
	}
	if value > 60 {
		return ToneWarning
	}
	return ToneNormal
}

// ToneForLatency mirrors the original signal bars: up to 100 ms is good, up
// to 300 ms is warning, and above it is danger.
func ToneForLatency(value float64) Tone {
	if !Valid(value) || value < 0 {
		return ToneUnknown
	}
	if value <= 100 {
		return ToneNormal
	}
	if value <= 300 {
		return ToneWarning
	}
	return ToneDanger
}

// IsStale reports a live sample older than the freshness window: the larger
// of five seconds and 2.5 sampling intervals, as in the original.
func IsStale(state HostState, interval time.Duration, now time.Time) bool {
	if state.Status != StatusLive {
		return false
	}
	freshness := interval * 5 / 2
	if freshness < 5*time.Second {
		freshness = 5 * time.Second
	}
	return now.Sub(state.Sample.At) > freshness
}

// AppendPoint appends sample to history, dropping entries older than
// retention and keeping at most MaxPoints.
func AppendPoint(points []Point, sample Sample) []Point {
	if !Valid(sample.CPU) && !Valid(sample.Memory) && !Valid(sample.Disk) &&
		!Valid(sample.Rx) && !Valid(sample.Tx) && !Valid(sample.GPU) {
		return points
	}
	cutoff := sample.At.Add(-HistoryRetention)
	points = append(points, Point{
		At:      sample.At,
		CPU:     sample.CPU,
		Memory:  sample.Memory,
		Disk:    sample.Disk,
		Rx:      sample.Rx,
		Tx:      sample.Tx,
		GPU:     sample.GPU,
		Latency: sample.Latency,
	})
	first := 0
	for first < len(points) && points[first].At.Before(cutoff) {
		first++
	}
	points = points[first:]
	if len(points) > MaxPoints {
		points = points[len(points)-MaxPoints:]
	}
	return points
}

// ClonePoints returns a copy so a snapshot cannot be mutated by a reader.
func ClonePoints(points []Point) []Point {
	return append([]Point(nil), points...)
}

// PointsSince returns the points newer than startedAt, or all of them when
// the panel has no start time.
func PointsSince(points []Point, startedAt time.Time) []Point {
	if startedAt.IsZero() {
		return ClonePoints(points)
	}
	start := 0
	for start < len(points) && !points[start].At.After(startedAt) {
		start++
	}
	return ClonePoints(points[start:])
}
