package hoststats

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
)

// Parse reads the pipe-separated labels both metric commands emit. The disk
// probe's reply may be appended so its DISKS label is parsed with the rest.
func Parse(output string) (Parsed, error) {
	parsed := newParsed()
	text := strings.TrimSpace(output)
	text = strings.TrimPrefix(text, latencyMarker)
	text = strings.TrimPrefix(text, "|")
	if strings.HasPrefix(text, "UNSUPPORTED_OS:") {
		return parsed, &Error{Reason: ReasonOS}
	}
	sawMetric := false
	for _, part := range strings.Split(text, "|") {
		switch {
		case strings.HasPrefix(part, "MODE:"):
			parsed.mode = strings.ToLower(strings.TrimSpace(part[5:]))
		case strings.HasPrefix(part, "CPU:"):
			if value, ok := parseFloat(part[4:]); ok {
				parsed.directCPU = clamp(value, 0, 100)
				if parsed.mode == "" {
					parsed.mode = "darwin"
				}
				sawMetric = true
			}
		case strings.HasPrefix(part, "CPURAW:"):
			fields := strings.Fields(part[7:])
			if len(fields) >= 2 {
				total, okTotal := parseInt(fields[0])
				idle, okIdle := parseInt(fields[1])
				if okTotal && okIdle {
					parsed.cpu.total, parsed.cpu.idle = total, idle
					parsed.hasCPU = true
					if parsed.mode == "" {
						parsed.mode = "linux"
					}
					sawMetric = true
				}
			}
		case strings.HasPrefix(part, "PERCORERAW:"):
			parsed.perCore = parsePerCore(part[11:])
			if parsed.mode == "" && len(parsed.perCore) > 0 {
				parsed.mode = "linux"
			}
		case strings.HasPrefix(part, "CORES:"):
			if value, ok := parseInt(part[6:]); ok && value > 0 {
				parsed.cores = int(value)
			}
		case strings.HasPrefix(part, "MEMINFO:"):
			parseMemInfo(&parsed, part[8:])
			sawMetric = true
		case strings.HasPrefix(part, "PROCS:"):
			parsed.topProcesses = parseTopProcesses(part[6:])
			if len(parsed.topProcesses) > 0 {
				sawMetric = true
			}
		case strings.HasPrefix(part, "NET:"):
			parsed.network = parseNetwork(part[4:])
		case strings.HasPrefix(part, "DISKIO:"):
			if sample, ok := parseDiskIO(part[7:]); ok {
				parsed.diskIO = sample
				sawMetric = true
			}
		case strings.HasPrefix(part, "DISKS:"):
			parsed.disks = filterDisks(parseDisks(part[6:]))
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
	parsed.network = filterNetwork(parsed.mode, parsed.network)
	if !sawMetric {
		return parsed, &Error{Reason: ReasonUnsupported,
			Cause: errText("no recognizable metrics in the reply")}
	}
	return parsed, nil
}

func newParsed() Parsed {
	return Parsed{
		directCPU:     math.NaN(),
		memTotal:      math.NaN(),
		memFree:       math.NaN(),
		memBuffers:    math.NaN(),
		memCached:     math.NaN(),
		swapTotal:     math.NaN(),
		swapFree:      math.NaN(),
		gpu:           math.NaN(),
		gpuMemUsed:    math.NaN(),
		gpuMemTotal:   math.NaN(),
		uptimeSeconds: math.NaN(),
		perCore:       []namedCore{},
		disks:         []domainmonitor.Disk{},
		network:       []netCounters{},
		topProcesses:  []Process{},
	}
}

func newSample() Sample {
	return Sample{
		CPU:           math.NaN(),
		CPUPerCore:    []Core{},
		MemTotalMB:    math.NaN(),
		MemUsedMB:     math.NaN(),
		MemFreeMB:     math.NaN(),
		MemBuffersMB:  math.NaN(),
		MemCachedMB:   math.NaN(),
		SwapTotalMB:   math.NaN(),
		SwapUsedMB:    math.NaN(),
		Disks:         []domainmonitor.Disk{},
		DiskUsedGB:    math.NaN(),
		DiskTotalGB:   math.NaN(),
		DiskPercent:   math.NaN(),
		DiskReadBps:   math.NaN(),
		DiskWriteBps:  math.NaN(),
		NetworkRxBps:  math.NaN(),
		NetworkTxBps:  math.NaN(),
		Interfaces:    []Interface{},
		TopProcesses:  []Process{},
		GPU:           math.NaN(),
		GPUMemUsedMB:  math.NaN(),
		GPUMemTotalMB: math.NaN(),
		SampledAt:     time.Time{},
		LatencyMS:     0,
	}
}

// Sample applies the per-Host delta baseline and produces one Stats Sample.
// previous is the State returned by the prior call; its zero value yields
// unknown CPU, per-core, disk-I/O, and network rates.
func (p Parsed) Sample(previous State, sampledAt time.Time, latency time.Duration) (Sample, State) {
	sample := newSample()
	sample.SampledAt = sampledAt
	sample.LatencyMS = float64(latency) / float64(time.Millisecond)
	sample.Hostname = p.hostname
	sample.OSName = p.osName
	sample.KernelRelease = p.kernelRelease
	sample.UptimeSeconds = p.uptimeSeconds
	sample.LoadAverage = append([]float64{}, p.loadAverage...)
	sample.Cores = p.cores
	sample.GPU = p.gpu
	sample.GPUName = p.gpuName
	sample.GPUMemUsedMB = p.gpuMemUsed
	sample.GPUMemTotalMB = p.gpuMemTotal

	if valid(p.directCPU) {
		sample.CPU = p.directCPU
	} else if previous.hasCPU && p.hasCPU && previous.cpu.total > 0 && p.cpu.total > 0 {
		totalDelta := p.cpu.total - previous.cpu.total
		idleDelta := p.cpu.idle - previous.cpu.idle
		if totalDelta > 0 && idleDelta >= 0 {
			sample.CPU = clamp(100-float64(idleDelta)/float64(totalDelta)*100, 0, 100)
		}
	}
	sample.CPUPerCore = perCorePercent(previous.perCore, p.perCore)

	computeMemory(&sample, p)
	computeDisks(&sample, p)
	computeDiskIO(&sample, previous, p)
	computeNetwork(&sample, previous, p, sampledAt)
	sample.TopProcesses = append([]Process{}, p.topProcesses...)

	next := State{
		cpu:       p.cpu,
		hasCPU:    p.hasCPU,
		perCore:   make(map[string]coreCounters, len(p.perCore)),
		network:   make(map[string]netCounters, len(p.network)),
		diskIO:    p.diskIO,
		sampledAt: sampledAt,
	}
	for _, core := range p.perCore {
		next.perCore[core.name] = core.coreCounters
	}
	for _, counter := range p.network {
		next.network[counter.name] = counter
	}
	return sample, next
}

func computeMemory(sample *Sample, p Parsed) {
	sample.MemTotalMB = p.memTotal
	sample.MemFreeMB = p.memFree
	sample.MemBuffersMB = p.memBuffers
	sample.MemCachedMB = p.memCached
	sample.SwapTotalMB = p.swapTotal
	if valid(p.memTotal) && valid(p.memFree) && valid(p.memBuffers) && valid(p.memCached) {
		used := p.memTotal - p.memFree - p.memBuffers - p.memCached
		if used < 0 {
			used = 0
		}
		sample.MemUsedMB = used
	}
	if valid(p.swapTotal) && valid(p.swapFree) {
		used := p.swapTotal - p.swapFree
		if used < 0 {
			used = 0
		}
		sample.SwapUsedMB = used
	}
}

func computeDisks(sample *Sample, p Parsed) {
	sample.Disks = append([]domainmonitor.Disk{}, p.disks...)
	if len(p.disks) == 0 {
		return
	}
	used, total, percent := domainmonitor.AggregateDisks(p.disks)
	sample.DiskUsedGB, sample.DiskTotalGB, sample.DiskPercent = used, total, percent
}

func computeDiskIO(sample *Sample, previous State, p Parsed) {
	if p.mode != "linux" || p.diskIO == nil || previous.diskIO == nil {
		return
	}
	read, write, ok := diskIORates(p.diskIO, previous.diskIO)
	if !ok {
		return
	}
	sample.DiskReadBps, sample.DiskWriteBps = read, write
}

func computeNetwork(sample *Sample, previous State, p Parsed, sampledAt time.Time) {
	elapsed := 0.0
	if !previous.sampledAt.IsZero() {
		elapsed = sampledAt.Sub(previous.sampledAt).Seconds()
	}
	interfaces := make([]Interface, 0, len(p.network))
	totalRx, totalTx := 0.0, 0.0
	seen := false
	for _, counter := range p.network {
		iface := Interface{
			Name:    counter.name,
			RxBytes: counter.rx,
			TxBytes: counter.tx,
			RxBps:   math.NaN(),
			TxBps:   math.NaN(),
		}
		if elapsed > 0 {
			if before, ok := previous.network[counter.name]; ok {
				seen = true
				rxDelta := 0.0
				if counter.rx >= before.rx {
					rxDelta = float64(counter.rx - before.rx)
				}
				txDelta := 0.0
				if counter.tx >= before.tx {
					txDelta = float64(counter.tx - before.tx)
				}
				iface.RxBps = rxDelta / elapsed
				iface.TxBps = txDelta / elapsed
				totalRx += iface.RxBps
				totalTx += iface.TxBps
			}
		}
		interfaces = append(interfaces, iface)
	}
	sample.Interfaces = interfaces
	if seen {
		sample.NetworkRxBps, sample.NetworkTxBps = totalRx, totalTx
	}
}

func perCorePercent(previous map[string]coreCounters, current []namedCore) []Core {
	out := make([]Core, 0, len(current))
	if len(current) == 0 {
		return out
	}
	sameSet := len(previous) == len(current)
	if sameSet {
		for _, core := range current {
			if _, ok := previous[core.name]; !ok {
				sameSet = false
				break
			}
		}
	}
	for _, core := range current {
		percent := math.NaN()
		if sameSet {
			before := previous[core.name]
			totalDelta := core.total - before.total
			idleDelta := core.idle - before.idle
			if totalDelta > 0 && idleDelta >= 0 {
				percent = clamp(100-float64(idleDelta)/float64(totalDelta)*100, 0, 100)
			}
		}
		out = append(out, Core{Name: core.name, Percent: percent})
	}
	return out
}

func diskIORates(current, previous *diskIOSample) (read, write float64, ok bool) {
	if current == nil || previous == nil || len(current.devices) == 0 ||
		len(current.devices) != len(previous.devices) {
		return 0, 0, false
	}
	seconds := current.timestamp - previous.timestamp
	if seconds <= 0 {
		return 0, 0, false
	}
	for name, counters := range current.devices {
		before, exists := previous.devices[name]
		if !exists {
			return 0, 0, false
		}
		if counters.read < before.read || counters.write < before.write {
			return 0, 0, false
		}
		read += float64(counters.read-before.read) * 512
		write += float64(counters.write-before.write) * 512
	}
	return read / seconds, write / seconds, true
}

func parseMemInfo(parsed *Parsed, text string) {
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

func parsePerCore(text string) []namedCore {
	text = strings.TrimSpace(text)
	if text == "" {
		return []namedCore{}
	}
	cores := make([]namedCore, 0, 8)
	for _, entry := range strings.Split(text, ",") {
		fields := strings.Split(strings.TrimSpace(entry), ":")
		if len(fields) == 0 {
			return []namedCore{}
		}
		var (
			name      string
			totalText string
			idleText  string
		)
		switch len(fields) {
		case 3:
			name = strings.TrimSpace(fields[0])
			totalText, idleText = fields[1], fields[2]
		case 2:
			name = "cpu" + strconv.Itoa(len(cores))
			totalText, idleText = fields[0], fields[1]
		default:
			return []namedCore{}
		}
		total, okTotal := parseInt(totalText)
		idle, okIdle := parseInt(idleText)
		if !okTotal || !okIdle {
			return []namedCore{}
		}
		if name == "" {
			name = "cpu" + strconv.Itoa(len(cores))
		}
		cores = append(cores, namedCore{name: name, coreCounters: coreCounters{total: total, idle: idle}})
	}
	return cores
}

func parseNetwork(text string) []netCounters {
	text = strings.TrimSpace(text)
	if text == "" {
		return []netCounters{}
	}
	counters := make([]netCounters, 0, 4)
	for _, entry := range strings.Split(text, ",") {
		fields := strings.SplitN(strings.TrimSpace(entry), ":", 3)
		if len(fields) != 3 {
			continue
		}
		rx, okRx := parseUint(fields[1])
		tx, okTx := parseUint(fields[2])
		if !okRx || !okTx {
			continue
		}
		counters = append(counters, netCounters{name: strings.TrimSpace(fields[0]), rx: rx, tx: tx})
	}
	return counters
}

func filterNetwork(mode string, counters []netCounters) []netCounters {
	out := make([]netCounters, 0, len(counters))
	for _, counter := range counters {
		if !keepInterface(mode, counter.name) {
			continue
		}
		out = append(out, counter)
	}
	return out
}

func keepInterface(mode, name string) bool {
	switch mode {
	case "darwin":
		return !strings.HasPrefix(name, "lo")
	case "linux":
		if name == "lo" {
			return false
		}
	default:
		if name == "lo" || strings.HasPrefix(name, "lo") {
			return false
		}
	}
	if strings.HasPrefix(name, "veth") ||
		strings.HasPrefix(name, "docker") ||
		strings.HasPrefix(name, "br-") {
		return false
	}
	return true
}

func parseTopProcesses(text string) []Process {
	text = strings.TrimSpace(text)
	if text == "" {
		return []Process{}
	}
	processes := make([]Process, 0, 10)
	for _, entry := range strings.Split(text, ",") {
		fields := strings.SplitN(strings.TrimSpace(entry), ";", 3)
		if len(fields) < 3 {
			continue
		}
		pid, okPID := parseInt(fields[0])
		memPercent, okMem := parseFloat(fields[1])
		if !okPID || pid <= 0 || !okMem || memPercent < 0 {
			continue
		}
		processes = append(processes, Process{
			PID:        int(pid),
			MemPercent: memPercent,
			Command:    strings.TrimSpace(fields[2]),
		})
	}
	sort.SliceStable(processes, func(i, j int) bool {
		return processes[i].MemPercent > processes[j].MemPercent
	})
	if len(processes) > 10 {
		processes = processes[:10]
	}
	return processes
}

func parseDisks(text string) []domainmonitor.Disk {
	text = strings.TrimSpace(text)
	if text == "" {
		return []domainmonitor.Disk{}
	}
	disks := make([]domainmonitor.Disk, 0, 4)
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
			MountPoint:  strings.TrimSpace(fields[0]),
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

func filterDisks(disks []domainmonitor.Disk) []domainmonitor.Disk {
	kept := make([]domainmonitor.Disk, 0, len(disks))
	for _, disk := range disks {
		if keepDisk(disk) {
			kept = append(kept, disk)
		}
	}
	return kept
}

func keepDisk(disk domainmonitor.Disk) bool {
	mount := strings.TrimSpace(disk.MountPoint)
	if mount == "" {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(disk.CapacityKey))
	filesystemType := strings.ToLower(strings.TrimSpace(disk.FilesystemType))
	if mount != "/" {
		if underPseudoMount(mount) {
			return false
		}
		if strings.HasPrefix(key, "/dev/loop") {
			return false
		}
		if filesystemType == "" {
			filesystemType = key
		}
		if isOverlaySource(filesystemType) || isOverlaySource(key) ||
			filesystemType == "overlay" || filesystemType == "overlayfs" {
			return false
		}
		if pseudoFilesystems[filesystemType] {
			return false
		}
	}
	return !domainmonitor.IsRemoteOrFuseDisk(disk)
}

// underPseudoMount reports whether mount is one of the kernel's pseudo
// locations, matching a whole path component rather than a shared prefix.
func underPseudoMount(mount string) bool {
	for _, prefix := range []string{"/etc", "/proc", "/sys", "/dev", "/snap"} {
		if mount == prefix || strings.HasPrefix(mount, prefix+"/") {
			return true
		}
	}
	return false
}

// isOverlaySource reports an overlay mount source: the original skips
// non-root overlay mounts because their size belongs to the backing store.
func isOverlaySource(value string) bool {
	return strings.HasPrefix(value, "overlay:") || strings.HasPrefix(value, "overlayfs:") ||
		strings.HasPrefix(value, "overlay/") || strings.HasPrefix(value, "overlayfs/")
}

var pseudoFilesystems = map[string]bool{
	"tmpfs": true, "shm": true, "devtmpfs": true, "udev": true, "none": true,
	"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true, "devpts": true,
	"mqueue": true, "hugetlbfs": true, "debugfs": true, "tracefs": true,
	"securityfs": true, "pstore": true, "bpf": true, "fusectl": true,
	"configfs": true, "ramfs": true, "rpc_pipefs": true, "binfmt_misc": true,
	"efivarfs": true,
}

// parseDiskIO reads the physical block-device counters and kernel uptime the
// Linux stats command emits. dm/md/loop/zram layers are skipped: they would
// double-count the I/O their physical devices already report.
func parseDiskIO(text string) (*diskIOSample, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false
	}
	parts := strings.SplitN(text, ";", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return nil, false
	}
	timestamp, ok := parseFloat(parts[0])
	if !ok || timestamp < 0 {
		return nil, false
	}
	devices := make(map[string]sectorCounters, 8)
	for _, entry := range strings.Split(strings.TrimSuffix(strings.TrimSpace(parts[1]), ","), ",") {
		fields := strings.Split(entry, ":")
		if len(fields) != 3 {
			return nil, false
		}
		name := strings.TrimSpace(fields[0])
		if name == "" || skipBlockDevice(name) {
			continue
		}
		read, okRead := parseUint(fields[1])
		write, okWrite := parseUint(fields[2])
		if !okRead || !okWrite {
			return nil, false
		}
		if _, exists := devices[name]; exists {
			return nil, false
		}
		devices[name] = sectorCounters{read: read, write: write}
	}
	return &diskIOSample{timestamp: timestamp, devices: devices}, true
}

func skipBlockDevice(name string) bool {
	value := strings.ToLower(name)
	return strings.HasPrefix(value, "dm-") ||
		strings.HasPrefix(value, "md") ||
		strings.HasPrefix(value, "loop") ||
		strings.HasPrefix(value, "zram")
}

func parseGPU(parsed *Parsed, part string) {
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
	values := make([]float64, 0, 3)
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

func valid(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clamp(value, low, high float64) float64 {
	if !valid(value) {
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

// errText is a local error type so the package does not need fmt for one
// static diagnostic.
type errText string

func (e errText) Error() string { return string(e) }
