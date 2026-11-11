package hoststats

import (
	"time"

	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
)

// Reason is a typed parser failure. The consumer translates it to its own
// surface reason; it is never display text.
type Reason string

const (
	// ReasonNone is the zero reason.
	ReasonNone Reason = ""
	// ReasonOS is an operating system the stats command cannot inspect.
	ReasonOS Reason = "os"
	// ReasonUnsupported is a reply with no recognizable Stats Sample.
	ReasonUnsupported Reason = "unsupported"
)

// Error is a typed parser failure. Cause is a developer diagnostic.
type Error struct {
	Reason Reason
	Cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	text := "hoststats: " + string(e.Reason)
	if e.Cause != nil {
		text += ": " + e.Cause.Error()
	}
	return text
}

func (e *Error) Unwrap() error { return e.Cause }

// Core is one processor's utilization. Name is the kernel's cpuN name, so a
// sample aligns cores by name rather than by position.
type Core struct {
	Name    string
	Percent float64
}

// Interface is one network interface's cumulative counters and derived rates.
// A rate is NaN until two readings establish a delta.
type Interface struct {
	Name    string
	RxBytes uint64
	TxBytes uint64
	RxBps   float64
	TxBps   float64
}

// Process is one row of the stats command's top-process list.
type Process struct {
	PID        int
	MemPercent float64
	Command    string
}

// Sample is one Host Stats Sample. Every unknown float is NaN and every
// missing list is an empty slice; a caller must never read an unknown as zero.
type Sample struct {
	// SampledAt is when the stats command's reply completed. It is the sample's
	// own collection time and is distinct from any UI state-change time.
	SampledAt time.Time
	// LatencyMS is the stats command's measured round trip.
	LatencyMS float64

	Hostname      string
	OSName        string
	KernelRelease string
	UptimeSeconds float64
	LoadAverage   []float64
	Cores         int

	CPU        float64
	CPUPerCore []Core

	MemTotalMB   float64
	MemUsedMB    float64
	MemFreeMB    float64
	MemBuffersMB float64
	MemCachedMB  float64
	SwapTotalMB  float64
	SwapUsedMB   float64

	Disks       []domainmonitor.Disk
	DiskUsedGB  float64
	DiskTotalGB float64
	DiskPercent float64

	DiskReadBps  float64
	DiskWriteBps float64

	NetworkRxBps float64
	NetworkTxBps float64
	Interfaces   []Interface

	TopProcesses []Process

	GPU           float64
	GPUName       string
	GPUMemUsedMB  float64
	GPUMemTotalMB float64
}

// State is the per-Host delta baseline between two Stats Samples. Consumers
// keep one State per Host and pass it back into Sample.
type State struct {
	cpu       cpuCounters
	hasCPU    bool
	perCore   map[string]coreCounters
	network   map[string]netCounters
	diskIO    *diskIOSample
	sampledAt time.Time
}

// Parsed is one command reply before deltas are applied.
type Parsed struct {
	mode          string
	hostname      string
	osName        string
	kernelRelease string
	cores         int
	directCPU     float64
	cpu           cpuCounters
	hasCPU        bool
	perCore       []namedCore
	memTotal      float64
	memFree       float64
	memBuffers    float64
	memCached     float64
	swapTotal     float64
	swapFree      float64
	disks         []domainmonitor.Disk
	network       []netCounters
	diskIO        *diskIOSample
	topProcesses  []Process
	gpu           float64
	gpuName       string
	gpuMemUsed    float64
	gpuMemTotal   float64
	uptimeSeconds float64
	loadAverage   []float64
}

type coreCounters struct {
	total int64
	idle  int64
}

type cpuCounters struct {
	total int64
	idle  int64
}

type namedCore struct {
	name string
	coreCounters
}

type netCounters struct {
	name string
	rx   uint64
	tx   uint64
}

type diskIOSample struct {
	timestamp float64
	devices   map[string]sectorCounters
}

type sectorCounters struct {
	read  uint64
	write uint64
}
