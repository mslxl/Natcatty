package monitor

import (
	"math"
	"strconv"
	"strings"
	"time"

	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
)

// Error is a typed monitor failure. Reason is a stable code the surface
// localizes; Cause is a developer diagnostic, never display text.
type Error struct {
	Reason Reason
	Cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	text := "monitor: " + string(e.Reason)
	if e.Cause != nil {
		text += ": " + e.Cause.Error()
	}
	return text
}

func (e *Error) Unwrap() error { return e.Cause }

func monitorError(reason Reason, cause error) error {
	return &Error{Reason: reason, Cause: cause}
}

type coreCounters struct {
	total int64
	idle  int64
}

type cpuCounters struct {
	total   int64
	idle    int64
	perCore []coreCounters
}

type netCounters struct {
	name string
	rx   uint64
	tx   uint64
}

// parsedStats is one command reply before deltas are applied.
type parsedStats struct {
	hostname      string
	osName        string
	kernelRelease string
	cores         int
	directCPU     float64
	cpu           cpuCounters
	memTotal      float64
	memFree       float64
	memBuffers    float64
	memCached     float64
	swapTotal     float64
	swapFree      float64
	disks         []domainmonitor.Disk
	network       []netCounters
	gpu           float64
	gpuName       string
	gpuMemUsed    float64
	gpuMemTotal   float64
	uptimeSeconds float64
	loadAverage   []float64
}

// samplerState is the per-Host delta baseline.
type samplerState struct {
	cpu     cpuCounters
	network map[string]netCounters
	at      time.Time
}

func newParsedStats() parsedStats {
	return parsedStats{
		directCPU:   math.NaN(),
		gpu:         math.NaN(),
		gpuMemUsed:  math.NaN(),
		gpuMemTotal: math.NaN(),
	}
}

// parseStats parses the pipe-separated labels both metric commands emit. The
// disk probe's reply may be appended so its DISKS label is parsed with the
// rest.
func parseStats(output string) (parsedStats, error) {
	parsed := newParsedStats()
	output = strings.TrimSpace(output)
	output = strings.TrimPrefix(output, latencyMarker)
	output = strings.TrimPrefix(output, "|")
	if strings.HasPrefix(output, "UNSUPPORTED_OS:") {
		return parsed, monitorError(ReasonOS, nil)
	}
	sawMetric := false
	for _, part := range strings.Split(output, "|") {
		switch {
		case strings.HasPrefix(part, "CPU:"):
			if value, ok := parseFloat(part[4:]); ok {
				parsed.directCPU = clamp(value, 0, 100)
				sawMetric = true
			}
		case strings.HasPrefix(part, "CPURAW:"):
			fields := strings.Fields(part[7:])
			if len(fields) >= 2 {
				total, okTotal := parseInt(fields[0])
				idle, okIdle := parseInt(fields[1])
				if okTotal && okIdle {
					parsed.cpu.total, parsed.cpu.idle = total, idle
					sawMetric = true
				}
			}
		case strings.HasPrefix(part, "PERCORERAW:"):
			parsed.cpu.perCore = parsePerCore(part[11:])
		case strings.HasPrefix(part, "CORES:"):
			if value, ok := parseInt(part[6:]); ok && value > 0 {
				parsed.cores = int(value)
			}
		case strings.HasPrefix(part, "MEMINFO:"):
			parseMemInfo(&parsed, part[8:])
			sawMetric = true
		case strings.HasPrefix(part, "NET:"):
			parsed.network = parseNetwork(part[4:])
		case strings.HasPrefix(part, "DISKS:"):
			parsed.disks = parseDisks(part[6:])
			sawMetric = true
		case strings.HasPrefix(part, "GPU:"):
			parseGPU(&parsed, part)
		case strings.HasPrefix(part, "HOST:"):
			parsed.hostname = strings.TrimSpace(part[5:])
		case strings.HasPrefix(part, "OS:"):
			parsed.osName = strings.TrimSpace(part[3:])
		case strings.HasPrefix(part, "KERNEL:"):
			parsed.kernelRelease = strings.TrimSpace(part[7:])
		case strings.HasPrefix(part, "UPTIME:"):
			if value, ok := parseFloat(part[7:]); ok && value >= 0 {
				parsed.uptimeSeconds = value
			}
		case strings.HasPrefix(part, "LOAD:"):
			parsed.loadAverage = parseLoad(part[5:])
		}
	}
	if !sawMetric {
		return parsed, monitorError(ReasonUnsupported,
			errText("no recognizable metrics in the reply"))
	}
	return parsed, nil
}

func parseMemInfo(parsed *parsedStats, text string) {
	fields := strings.Fields(text)
	if len(fields) < 4 {
		return
	}
	values := make([]float64, len(fields))
	for index, field := range fields {
		value, ok := parseFloat(field)
		if !ok {
			return
		}
		values[index] = value
	}
	parsed.memTotal = values[0]
	parsed.memFree = values[1]
	parsed.memBuffers = values[2]
	parsed.memCached = values[3]
	if len(values) >= 6 {
		parsed.swapTotal = values[4]
		parsed.swapFree = values[5]
	}
}

func parsePerCore(text string) []coreCounters {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var cores []coreCounters
	for _, entry := range strings.Split(text, ",") {
		fields := strings.SplitN(strings.TrimSpace(entry), ":", 2)
		if len(fields) != 2 {
			return nil
		}
		total, okTotal := parseInt(fields[0])
		idle, okIdle := parseInt(fields[1])
		if !okTotal || !okIdle {
			return nil
		}
		cores = append(cores, coreCounters{total: total, idle: idle})
	}
	return cores
}

func parseNetwork(text string) []netCounters {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var counters []netCounters
	for _, entry := range strings.Split(text, ",") {
		fields := strings.SplitN(entry, ":", 3)
		if len(fields) != 3 {
			continue
		}
		rx, okRx := parseUint(fields[1])
		tx, okTx := parseUint(fields[2])
		if !okRx || !okTx {
			continue
		}
		counters = append(counters, netCounters{name: fields[0], rx: rx, tx: tx})
	}
	return counters
}

func parseDisks(text string) []domainmonitor.Disk {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var disks []domainmonitor.Disk
	for _, entry := range strings.Split(text, ",") {
		fields := strings.SplitN(entry, ":", 5)
		if len(fields) < 4 {
			continue
		}
		used, okUsed := parseFloat(fields[1])
		total, okTotal := parseFloat(fields[2])
		if !okUsed || !okTotal || total <= 0 {
			continue
		}
		percent, okPercent := parseFloat(fields[3])
		if !okPercent {
			percent = used / total * 100
		}
		metadata := ""
		if len(fields) == 5 {
			metadata = strings.TrimSpace(fields[4])
		}
		disk := domainmonitor.Disk{
			MountPoint:  fields[0],
			UsedGB:      used,
			TotalGB:     total,
			Percent:     clamp(percent, 0, 100),
			CapacityKey: metadata,
		}
		if strings.HasPrefix(metadata, "fs=") {
			separator := strings.Index(metadata, ":")
			if separator > 3 {
				disk.FilesystemType = strings.TrimSpace(metadata[3:separator])
				disk.CapacityKey = strings.TrimSpace(metadata[separator+1:])
			}
		}
		disks = append(disks, disk)
	}
	return disks
}

func parseGPU(parsed *parsedStats, part string) {
	// The label may arrive as "GPU:..." or after another label without its
	// own delimiter, as in the original parser.
	index := strings.Index(part, "GPU:")
	if index < 0 {
		return
	}
	fields := strings.Fields(part[index+4:])
	if len(fields) == 0 {
		return
	}
	if value, ok := parseFloat(fields[0]); ok {
		parsed.gpu = clamp(value, 0, 100)
	}
	if len(fields) > 1 {
		if value, ok := parseFloat(fields[1]); ok {
			parsed.gpuMemUsed = value
		}
	}
	if len(fields) > 2 {
		if value, ok := parseFloat(fields[2]); ok {
			parsed.gpuMemTotal = value
		}
	}
	if len(fields) > 3 {
		parsed.gpuName = strings.Join(fields[3:], " ")
	}
}

func parseLoad(text string) []float64 {
	var values []float64
	for _, field := range strings.Fields(text) {
		value, ok := parseFloat(field)
		if ok && value >= 0 {
			values = append(values, value)
		}
		if len(values) == 3 {
			break
		}
	}
	return values
}

// sample applies the delta baseline and produces the surface's Sample.
func (p parsedStats) sample(previous samplerState, at time.Time, latency time.Duration) (domainmonitor.Sample, samplerState) {
	sample := domainmonitor.Sample{
		At:            at,
		CPU:           math.NaN(),
		Memory:        math.NaN(),
		Disk:          math.NaN(),
		Rx:            math.NaN(),
		Tx:            math.NaN(),
		Latency:       latency.Seconds() * 1000,
		GPU:           p.gpu,
		GPUName:       p.gpuName,
		GPUMemUsedMB:  p.gpuMemUsed,
		GPUMemTotalMB: p.gpuMemTotal,
		Hostname:      p.hostname,
		OSName:        p.osName,
		KernelRelease: p.kernelRelease,
		UptimeSeconds: p.uptimeSeconds,
		LoadAverage:   p.loadAverage,
		CPUCores:      p.cores,
	}
	if domainmonitor.Valid(p.directCPU) {
		sample.CPU = p.directCPU
	} else if previous.cpu.total > 0 && p.cpu.total > 0 {
		totalDelta := p.cpu.total - previous.cpu.total
		idleDelta := p.cpu.idle - previous.cpu.idle
		if totalDelta > 0 && idleDelta >= 0 {
			sample.CPU = clamp(100-float64(idleDelta)/float64(totalDelta)*100, 0, 100)
		}
	}
	sample.CPUPerCore = perCorePercent(previous.cpu.perCore, p.cpu.perCore)

	memUsed := p.memTotal - p.memFree - p.memBuffers - p.memCached
	if memUsed < 0 {
		memUsed = 0
	}
	sample.MemTotalMB = p.memTotal
	sample.MemUsedMB = memUsed
	sample.Memory = domainmonitor.MemoryPercent(sample)
	sample.SwapTotalMB = p.swapTotal
	sample.SwapUsedMB = p.swapTotal - p.swapFree
	if sample.SwapUsedMB < 0 {
		sample.SwapUsedMB = 0
	}

	sample.Disks = p.disks
	if len(p.disks) > 0 {
		used, total, percent := domainmonitor.AggregateDisks(p.disks)
		sample.DiskUsedGB, sample.DiskTotalGB, sample.Disk = used, total, percent
	}

	elapsed := 0.0
	if !previous.at.IsZero() {
		elapsed = at.Sub(previous.at).Seconds()
	}
	rx, tx := networkRates(p.network, previous.network, elapsed)
	if domainmonitor.Valid(rx) {
		sample.Rx, sample.Tx = rx, tx
	}

	next := samplerState{
		cpu:     p.cpu,
		network: make(map[string]netCounters, len(p.network)),
		at:      at,
	}
	for _, counter := range p.network {
		next.network[counter.name] = counter
	}
	return sample, next
}

func perCorePercent(previous, current []coreCounters) []float64 {
	if len(current) == 0 {
		return nil
	}
	out := make([]float64, len(current))
	for index, core := range current {
		out[index] = 0
		if index >= len(previous) {
			continue
		}
		totalDelta := core.total - previous[index].total
		idleDelta := core.idle - previous[index].idle
		if totalDelta > 0 && idleDelta >= 0 {
			out[index] = clamp(100-float64(idleDelta)/float64(totalDelta)*100, 0, 100)
		}
	}
	return out
}

func networkRates(current []netCounters, previous map[string]netCounters, elapsed float64) (rx, tx float64) {
	if elapsed <= 0 || len(previous) == 0 || len(current) == 0 {
		return math.NaN(), math.NaN()
	}
	rx, tx = 0, 0
	seen := false
	for _, counter := range current {
		before, ok := previous[counter.name]
		if !ok {
			continue
		}
		seen = true
		if counter.rx >= before.rx {
			rx += float64(counter.rx-before.rx) / elapsed
		}
		if counter.tx >= before.tx {
			tx += float64(counter.tx-before.tx) / elapsed
		}
	}
	if !seen {
		return math.NaN(), math.NaN()
	}
	return rx, tx
}

func clamp(value, low, high float64) float64 {
	if math.IsNaN(value) {
		return math.NaN()
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func parseFloat(text string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func parseUint(text string) (uint64, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(text), 10, 64)
	return value, err == nil
}

func parseInt(text string) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	return value, err == nil
}

// errText is a local error type so the package does not need fmt for two
// static diagnostics.
type errText string

func (e errText) Error() string { return string(e) }
