package sysmanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// parseCapabilities reads the line-anchored probe markers. Line anchoring
// keeps a noisy shell that echoes the probe source from inventing commands.
func parseCapabilities(stdout string) Capabilities {
	text := stdout
	hasFlag := func(name string) bool {
		marker := markerHasPrefix + name + "=1"
		for _, line := range strings.Split(text, "\n") {
			if strings.TrimSpace(strings.TrimSuffix(line, "\r")) == marker {
				return true
			}
		}
		return false
	}
	caps := Capabilities{
		HasPs:        hasFlag("ps"),
		HasTop:       hasFlag("top"),
		HasSs:        hasFlag("ss"),
		HasNetstat:   hasFlag("netstat"),
		HasLsof:      hasFlag("lsof"),
		HasSystemctl: hasFlag("systemctl"),
		HasTmux:      hasFlag("tmux"),
		HasDocker:    hasFlag("docker"),
		HasPodman:    hasFlag("podman"),
		HasNerdctl:   hasFlag("nerdctl"),
		HasKubectl:   hasFlag("kubectl"),
		HasNvidiaSmi: hasFlag("nvidia-smi"),
		HasNpuSmi:    hasFlag("npu-smi"),
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, markerOS+"=") {
			caps.OS = normalizeOS(strings.TrimPrefix(line, markerOS+"="))
			break
		}
	}
	return caps
}

func normalizeOS(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(value, "linux"):
		return "linux"
	case strings.Contains(value, "darwin"):
		return "darwin"
	default:
		return "unknown"
	}
}

// overviewSample is one parsed overview reply plus the raw CPU counters the
// next reply derives a percentage from.
type overviewSample struct {
	overview Overview
	mode     string
	cpuTotal float64
	cpuIdle  float64
	perCore  []overviewCoreCounters
}

func (s overviewSample) counters() overviewCounters {
	return overviewCounters{
		mode:    s.mode,
		total:   s.cpuTotal,
		idle:    s.cpuIdle,
		perCore: append([]overviewCoreCounters(nil), s.perCore...),
	}
}

// overviewCounters is the previous CPU reading a service keeps per run.
type overviewCounters struct {
	mode    string
	total   float64
	idle    float64
	perCore []overviewCoreCounters
}

// overviewCoreCounters is one logical core's aggregate CPU time, in the same
// total/idle shape the aggregate counters use.
type overviewCoreCounters struct {
	total float64
	idle  float64
}

// perCorePercent derives each core's utilization from the counter delta. A
// reply without per-core counters leaves the list empty: the Host reports no
// per-core data, so the overview shows only the aggregate. Counters without a
// matching baseline still yield one entry per core, each NaN, so the rows
// appear as soon as the sampler sees them.
func (s overviewSample) perCorePercent(previous overviewCounters) []float64 {
	if len(s.perCore) == 0 {
		return nil
	}
	out := make([]float64, len(s.perCore))
	for index, core := range s.perCore {
		out[index] = math.NaN()
		if index >= len(previous.perCore) {
			continue
		}
		totalDelta := core.total - previous.perCore[index].total
		idleDelta := core.idle - previous.perCore[index].idle
		if totalDelta > 0 && idleDelta >= 0 {
			out[index] = clampPercent(100 * (totalDelta - idleDelta) / totalDelta)
		}
	}
	return out
}

// cpuPercent derives Linux CPU utilization from the counter delta. macOS
// reports a direct ps percentage, divided by the logical core count.
func (s overviewSample) cpuPercent(previous overviewCounters) float64 {
	if s.mode == "darwin" {
		cores := s.overview.Cores
		if cores <= 0 {
			cores = 1
		}
		return clampPercent(s.cpuTotal / float64(cores))
	}
	if previous.mode != "linux" || previous.total <= 0 {
		return math.NaN()
	}
	totalDelta := s.cpuTotal - previous.total
	idleDelta := s.cpuIdle - previous.idle
	if totalDelta <= 0 {
		return math.NaN()
	}
	return clampPercent(100 * (totalDelta - idleDelta) / totalDelta)
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
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

// parseOverview reads the single labelled line both OS commands emit.
func parseOverview(stdout string) (overviewSample, error) {
	var sample overviewSample
	sample.overview.CPUPercent = math.NaN()
	line := firstMarkerLine(stdout, "MODE:")
	if line == "" {
		return sample, &Error{Reason: ReasonOS,
			Cause: errors.New("overview: no MODE label in the reply")}
	}
	labels := parseLabels(line)
	sample.mode = strings.ToLower(labels["MODE"])
	if sample.mode == "unsupported" {
		return sample, &Error{Reason: ReasonOS,
			Cause: fmt.Errorf("overview: unsupported operating system %q", labels["OS"])}
	}
	if sample.mode != "linux" && sample.mode != "darwin" {
		return sample, &Error{Reason: ReasonOS,
			Cause: fmt.Errorf("overview: unknown mode %q", sample.mode)}
	}
	sample.overview = Overview{
		Hostname:      labels["HOST"],
		OSName:        labels["OS"],
		KernelRelease: labels["KERNEL"],
		UptimeSeconds: parseFloat(labels["UPTIME"]),
		LoadAverage:   parseFloatList(labels["LOAD"], 3),
		Cores:         parseInt(labels["CORES"]),
		CPUPercent:    math.NaN(),
	}
	sample.overview.MemUsedMB, sample.overview.MemTotalMB = parsePair(labels["MEM"])
	sample.overview.SwapUsedMB, sample.overview.SwapTotalMB = parsePair(labels["SWAP"])
	sample.overview.DiskUsedGB, sample.overview.DiskTotalGB = parsePair(labels["DISK"])
	if raw := strings.Fields(labels["CPURAW"]); len(raw) >= 2 {
		sample.cpuTotal = parseFloat(raw[0])
		sample.cpuIdle = parseFloat(raw[1])
	} else if len(raw) == 1 {
		// macOS emits one direct percentage.
		sample.cpuTotal = parseFloat(raw[0])
	}
	sample.perCore = parseOverviewPerCore(labels["PERCORERAW"])
	return sample, nil
}

// parseOverviewPerCore reads "total:idle,total:idle,..." as the same counters
// the aggregate line carries. One malformed entry drops the whole list, so a
// truncated reply never looks like fewer cores than the Host has.
func parseOverviewPerCore(value string) []overviewCoreCounters {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	entries := strings.Split(value, ",")
	cores := make([]overviewCoreCounters, 0, len(entries))
	for _, entry := range entries {
		fields := strings.SplitN(strings.TrimSpace(entry), ":", 2)
		if len(fields) != 2 {
			return nil
		}
		total := parseFloat(fields[0])
		idle := parseFloat(fields[1])
		if math.IsNaN(total) || math.IsNaN(idle) || total < 0 || idle < 0 {
			return nil
		}
		cores = append(cores, overviewCoreCounters{total: total, idle: idle})
	}
	return cores
}

// parseLabels splits "LABEL:value|LABEL:value" without splitting inside a
// value: only the first colon of each field is the separator.
func parseLabels(line string) map[string]string {
	labels := make(map[string]string, 12)
	for _, field := range strings.Split(line, "|") {
		index := strings.IndexByte(field, ':')
		if index <= 0 {
			continue
		}
		labels[strings.ToUpper(strings.TrimSpace(field[:index]))] = strings.TrimSpace(field[index+1:])
	}
	return labels
}

func firstMarkerLine(stdout, prefix string) string {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func parseFloat(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return math.NaN()
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return math.NaN()
	}
	return parsed
}

func parseInt(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

func parseFloatList(value string, limit int) []float64 {
	fields := strings.Fields(value)
	out := make([]float64, 0, len(fields))
	for _, field := range fields {
		parsed := parseFloat(field)
		if math.IsNaN(parsed) {
			continue
		}
		out = append(out, parsed)
		if len(out) == limit {
			break
		}
	}
	return out
}

func parsePair(value string) (float64, float64) {
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return math.NaN(), math.NaN()
	}
	return parseFloat(fields[0]), parseFloat(fields[1])
}

var (
	gnuProcessPattern = regexp.MustCompile(
		`^(\d+)\s+(\d+)\s+(\S+)\s+(\S+)\s+([\d.]+)\s+([\d.]+)\s+(\d+)\s+(\d+)\s+(\S+)\s+(.+)$`)
	busyBoxTopPattern = regexp.MustCompile(
		`^(\d+)\s+(\d+)\s+(\S+)\s+(\S+)\s+(\d+(?:\.\d+)?[mgtpezy]?)\s+([\d.]+)%?\s+(?:\d+\s+)?([\d.]+)%?\s+(.+)$`)
	busyBoxProcessPattern = regexp.MustCompile(
		`^(\d+)\s+(\S+)\s+(\d+(?:\.\d+)?[mgtpezy]?)\s+(\S+)\s+(.+)$`)
	sizeMultipliers = map[byte]float64{
		'm': 1024, 'g': 1024 * 1024, 't': 1024 * 1024 * 1024,
		'p': 1024 * 1024 * 1024 * 1024, 'e': 1024 * 1024 * 1024 * 1024 * 1024,
		'z': 1024 * 1024 * 1024 * 1024 * 1024 * 1024,
		'y': 1024 * 1024 * 1024 * 1024 * 1024 * 1024 * 1024,
	}
)

// parseProcesses ports the original's three shapes: GNU ps -eo, BusyBox top,
// and BusyBox ps.
func parseProcesses(stdout string) ([]Process, error) {
	if hasUnavailable(stdout, "processes") {
		return nil, &Error{Reason: ReasonMissingProcesses,
			Cause: errors.New("processes: ps and top are unavailable")}
	}
	processes := make([]Process, 0, 64)
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" {
			continue
		}
		if match := gnuProcessPattern.FindStringSubmatch(trimmed); match != nil {
			processes = append(processes, Process{
				PID:        parseInt(match[1]),
				PPID:       parseInt(match[2]),
				User:       match[3],
				Stat:       match[4],
				CPUPercent: parseFloat(match[5]),
				MemPercent: parseFloat(match[6]),
				RSSKB:      int64(parseFloat(match[7])),
				VSZKB:      int64(parseFloat(match[8])),
				Elapsed:    match[9],
				Command:    match[10],
			})
			continue
		}
		if match := busyBoxTopPattern.FindStringSubmatch(trimmed); match != nil {
			processes = append(processes, Process{
				PID:        parseInt(match[1]),
				PPID:       parseInt(match[2]),
				User:       match[3],
				Stat:       match[4],
				CPUPercent: parseFloat(match[7]),
				MemPercent: parseFloat(match[6]),
				VSZKB:      parseSize(match[5]),
				Command:    match[8],
			})
			continue
		}
		if match := busyBoxProcessPattern.FindStringSubmatch(trimmed); match != nil {
			processes = append(processes, Process{
				PID:     parseInt(match[1]),
				User:    match[2],
				Stat:    match[4],
				VSZKB:   parseSize(match[3]),
				Command: match[5],
			})
		}
	}
	return processes, nil
}

// parseSize reads a BusyBox size field such as "2.5m" as KiB.
func parseSize(field string) int64 {
	field = strings.TrimSpace(field)
	if field == "" {
		return 0
	}
	number := field
	suffix := field[len(field)-1]
	if (suffix >= 'a' && suffix <= 'z') || (suffix >= 'A' && suffix <= 'Z') {
		number = field[:len(field)-1]
	}
	value := parseFloat(number)
	if math.IsNaN(value) {
		return 0
	}
	return int64(value * sizeMultiplier(field))
}

// sizeMultiplier reads the original's BusyBox size suffix.
func sizeMultiplier(field string) float64 {
	if field == "" {
		return 1
	}
	suffix := field[len(field)-1]
	if suffix >= 'A' && suffix <= 'Z' {
		suffix += 'a' - 'A'
	}
	if multiplier, ok := sizeMultipliers[suffix]; ok {
		return multiplier
	}
	return 1
}

// hasUnavailable reports the typed marker a command prints when its tool is
// missing.
func hasUnavailable(stdout, name string) bool {
	marker := markerUnavailable + "=" + name
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(strings.TrimSuffix(line, "\r")) == marker {
			return true
		}
	}
	return false
}

// --- Listening ports -------------------------------------------------------

// Port parsing is a port of the original portOps parser. It merges ss,
// netstat, and lsof rows so a socket with a known PID drops its anonymous
// placeholder and SO_REUSEPORT listeners stay distinct.

func normalizeProtocol(raw string) string {
	text := strings.ToLower(strings.TrimSpace(raw))
	switch text {
	case "tcp", "tcp4":
		return "tcp"
	case "tcp6", "tcp46":
		return "tcp6"
	case "udp", "udp4":
		return "udp"
	case "udp6", "udp46":
		return "udp6"
	default:
		return "unknown"
	}
}

func parseListenAddress(raw string) (string, int, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", 0, false
	}
	// BSD netstat: "*.22", "127.0.0.1.53", "::1.631".
	if dotted := regexp.MustCompile(`^(.*)\.(\d+)$`).FindStringSubmatch(text); dotted != nil {
		port := parseInt(dotted[2])
		if port >= 0 && port <= 65535 {
			address := dotted[1]
			if address == "" {
				address = "*"
			}
			if index := strings.IndexByte(address, '%'); index >= 0 {
				address = address[:index]
			}
			if address == "*" || address == "0.0.0.0" || address == "::" {
				address = "*"
			}
			return address, port, true
		}
	}
	lastColon := strings.LastIndexByte(text, ':')
	if lastColon <= 0 {
		return "", 0, false
	}
	portText := text[lastColon+1:]
	if !isDigits(portText) {
		return "", 0, false
	}
	port := parseInt(portText)
	if port < 0 || port > 65535 {
		return "", 0, false
	}
	address := text[:lastColon]
	if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
		address = address[1 : len(address)-1]
	}
	if index := strings.IndexByte(address, '%'); index >= 0 {
		address = address[:index]
	}
	if address == "*" || address == "0.0.0.0" || address == "::" {
		address = "*"
	}
	return address, port, true
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseSsProcesses(info string) []struct {
	name string
	pid  int
} {
	entries := []struct {
		name string
		pid  int
	}{}
	pattern := regexp.MustCompile(`\("([^"]+)",pid=(\d+)`)
	for _, match := range pattern.FindAllStringSubmatch(info, -1) {
		entries = append(entries, struct {
			name string
			pid  int
		}{name: match[1], pid: parseInt(match[2])})
	}
	if len(entries) == 0 {
		entries = append(entries, struct {
			name string
			pid  int
		}{})
	}
	return entries
}

type portCollector struct {
	entries []Port
	byKey   map[string]int
}

func newPortCollector() *portCollector {
	return &portCollector{byKey: make(map[string]int)}
}

func portKey(protocol, address string, port, pid int) string {
	pidKey := "-"
	if pid > 0 {
		pidKey = strconv.Itoa(pid)
	}
	return normalizeProtocol(protocol) + "|" + address + "|" + strconv.Itoa(port) + "|" + pidKey
}

func (c *portCollector) push(protocol, address string, port, pid int, processName string) {
	if port < 0 || port > 65535 {
		return
	}
	protocol = normalizeProtocol(protocol)
	if address == "" {
		address = "*"
	}
	if pid < 0 {
		pid = 0
	}
	if pid > 0 {
		anonymous := portKey(protocol, address, port, 0)
		if index, ok := c.byKey[anonymous]; ok {
			index = c.byKey[anonymous]
			c.entries = append(c.entries[:index], c.entries[index+1:]...)
			delete(c.byKey, anonymous)
			c.reindex()
		}
	} else {
		for _, existing := range c.entries {
			if existing.Protocol == protocol && existing.Address == address &&
				existing.Port == port && existing.PID > 0 {
				return
			}
		}
	}
	key := portKey(protocol, address, port, pid)
	if index, ok := c.byKey[key]; ok {
		if c.entries[index].ProcessName == "" && processName != "" {
			c.entries[index].ProcessName = processName
		}
		return
	}
	c.byKey[key] = len(c.entries)
	c.entries = append(c.entries, Port{
		Protocol: protocol, Address: address, Port: port, PID: pid, ProcessName: processName,
	})
}

func (c *portCollector) reindex() {
	c.byKey = make(map[string]int, len(c.entries))
	for index, entry := range c.entries {
		c.byKey[portKey(entry.Protocol, entry.Address, entry.Port, entry.PID)] = index
	}
}

func isWildcardPeer(peer string) bool {
	switch strings.TrimSpace(peer) {
	case "*.*", "*:*", "0.0.0.0:*", ":::*", "[::]:*", "*.":
		return true
	default:
		return false
	}
}

func parseSsOutput(stdout string, collector *portCollector) {
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" || regexp.MustCompile(`(?i)^Netid\b`).MatchString(trimmed) ||
			regexp.MustCompile(`(?i)^State\b`).MatchString(trimmed) {
			continue
		}
		parts := strings.Fields(trimmed)
		if len(parts) < 5 {
			continue
		}
		protocol := parts[0]
		if !regexp.MustCompile(`(?i)^(tcp|udp)`).MatchString(protocol) {
			continue
		}
		state := ""
		localAddress := ""
		peerAddress := ""
		processField := ""
		if len(parts) >= 6 && (strings.Contains(parts[4], ":") || strings.Contains(parts[4], ".")) {
			state = parts[1]
			localAddress = parts[4]
			peerAddress = parts[5]
			processField = strings.Join(parts[6:], " ")
		} else {
			localAddress = parts[3]
			peerAddress = parts[4]
			processField = strings.Join(parts[5:], " ")
		}
		isUDP := strings.HasPrefix(strings.ToLower(protocol), "udp")
		if !isUDP && state != "" && !strings.EqualFold(state, "LISTEN") {
			continue
		}
		if isUDP && state != "" && !strings.EqualFold(state, "UNCONN") &&
			!strings.EqualFold(state, "IDLE") && !strings.EqualFold(state, "LISTEN") {
			continue
		}
		if isUDP && peerAddress != "" && !isWildcardPeer(peerAddress) {
			continue
		}
		address, port, ok := parseListenAddress(localAddress)
		if !ok {
			continue
		}
		for _, process := range parseSsProcesses(processField) {
			collector.push(protocol, address, port, process.pid, process.name)
		}
	}
}

func parseNetstatOutput(stdout string, collector *portCollector) {
	pattern := regexp.MustCompile(
		`(?i)^(tcp46|udp46|tcp[46]?|udp[46]?)\s+\d+\s+\d+\s+(\S+)\s+(\S+)(?:\s+(.*))?$`)
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" || regexp.MustCompile(`(?i)^Proto\b`).MatchString(trimmed) ||
			regexp.MustCompile(`(?i)^Active\b`).MatchString(trimmed) {
			continue
		}
		match := pattern.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		protocol := match[1]
		local := match[2]
		peer := match[3]
		rest := strings.TrimSpace(match[4])
		isUDP := strings.HasPrefix(strings.ToLower(protocol), "udp")
		state := ""
		pidField := ""
		if rest != "" {
			restParts := strings.Fields(rest)
			if strings.EqualFold(restParts[0], "LISTEN") {
				state = "LISTEN"
				pidField = strings.Join(restParts[1:], " ")
			} else if isUDP && regexp.MustCompile(`^\d+/`).MatchString(restParts[0]) {
				pidField = rest
			} else if !isUDP {
				continue
			}
		}
		if !isUDP {
			if !strings.EqualFold(state, "LISTEN") {
				continue
			}
		} else if !isWildcardPeer(peer) {
			continue
		}
		address, port, ok := parseListenAddress(local)
		if !ok {
			continue
		}
		pid := 0
		processName := ""
		if pidMatch := regexp.MustCompile(`^(\d+)/(.+)$`).FindStringSubmatch(pidField); pidMatch != nil {
			pid = parseInt(pidMatch[1])
			processName = pidMatch[2]
		}
		collector.push(protocol, address, port, pid, processName)
	}
}

func parseLsofOutput(stdout string, collector *portCollector) {
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" || regexp.MustCompile(`(?i)^COMMAND\b`).MatchString(trimmed) {
			continue
		}
		parts := strings.Fields(trimmed)
		if len(parts) < 9 {
			continue
		}
		processName := parts[0]
		pid := parseInt(parts[1])
		typeField := strings.ToUpper(parts[4])
		nodeField := strings.ToUpper(parts[7])
		nameField := strings.Join(parts[8:], " ")
		if strings.Contains(nameField, "->") {
			continue
		}
		isUDPNode := nodeField == "UDP" || regexp.MustCompile(`\bUDP\b`).MatchString(nameField)
		isTCPNode := nodeField == "TCP" || regexp.MustCompile(`\bTCP\b`).MatchString(nameField) ||
			regexp.MustCompile(`(?i)\(LISTEN\)`).MatchString(nameField)
		if !isUDPNode && !isTCPNode {
			continue
		}
		if isTCPNode && !regexp.MustCompile(`(?i)\(LISTEN\)`).MatchString(nameField) {
			continue
		}
		cleaned := regexp.MustCompile(`(?i)^(TCP|UDP)\s+`).ReplaceAllString(nameField, "")
		cleaned = regexp.MustCompile(`(?i)\s+\((LISTEN|UDP)\)\s*$`).ReplaceAllString(cleaned, "")
		cleaned = strings.TrimSpace(cleaned)
		address, port, ok := parseListenAddress(cleaned)
		if !ok {
			continue
		}
		protocol := "tcp"
		if isUDPNode {
			protocol = "udp"
		}
		if strings.Contains(typeField, "IPV6") || strings.Contains(address, ":") {
			if isUDPNode {
				protocol = "udp6"
			} else {
				protocol = "tcp6"
			}
		}
		collector.push(protocol, address, port, pid, processName)
	}
}

// parsePorts extracts each collector's marker section and merges the rows.
func parsePorts(stdout string) ([]Port, error) {
	text := stdout
	if hasUnavailable(text, "ports") || hasUnavailable(text, "ss") {
		return nil, &Error{Reason: ReasonMissingPorts,
			Cause: errors.New("ports: no listening-port tool is available")}
	}
	collector := newPortCollector()
	if strings.Contains(text, "__NC_SS__") {
		parseSsOutput(extractSection(text, "__NC_SS__"), collector)
	}
	if strings.Contains(text, "__NC_NETSTAT__") {
		parseNetstatOutput(extractSection(text, "__NC_NETSTAT__"), collector)
	}
	if strings.Contains(text, "__NC_LSOF__") {
		parseLsofOutput(extractSection(text, "__NC_LSOF__"), collector)
	}
	if len(collector.entries) == 0 {
		// Bare output without markers (a host whose shell echoed nothing)
		// still gets one attempt at each shape.
		parseSsOutput(text, collector)
		if len(collector.entries) == 0 {
			parseNetstatOutput(text, collector)
		}
		if len(collector.entries) == 0 {
			parseLsofOutput(text, collector)
		}
	}
	sort.SliceStable(collector.entries, func(i, j int) bool {
		if collector.entries[i].Port != collector.entries[j].Port {
			return collector.entries[i].Port < collector.entries[j].Port
		}
		return collector.entries[i].Protocol < collector.entries[j].Protocol
	})
	return collector.entries, nil
}

func extractSection(stdout, beginMarker string) string {
	begin := strings.Index(stdout, beginMarker)
	if begin < 0 {
		return ""
	}
	after := stdout[begin+len(beginMarker):]
	endPattern := regexp.MustCompile(`\n__NC_(SS|NETSTAT|LSOF|PORTS_END)__`)
	if location := endPattern.FindStringIndex(after); location != nil {
		return after[:location[0]]
	}
	return after
}

// --- systemd services ------------------------------------------------------

var (
	unitLinePattern = regexp.MustCompile(`^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)(?:\s+(.*))?$`)
	unitBullet      = regexp.MustCompile(`^●\s*`)
)

// parseServices merges the system and user sections, keeps a system unit on
// a name collision, and lists failed units first as the original did.
func parseServices(stdout string) ([]Unit, error) {
	if hasUnavailable(stdout, "systemctl") {
		return nil, &Error{Reason: ReasonMissingSystemctl,
			Cause: errors.New("services: systemctl is unavailable")}
	}
	systemPart := extractBetween(stdout, "__NC_SYSTEM__", "__NC_USER__", "__NC_SERVICES_END__")
	userPart := extractBetween(stdout, "__NC_USER__", "__NC_SERVICES_END__")
	system := parseSystemctlUnits(systemPart, "system")
	user := parseSystemctlUnits(userPart, "user")
	seen := make(map[string]bool, len(system))
	merged := make([]Unit, 0, len(system)+len(user))
	for _, unit := range system {
		seen[unit.Name] = true
		merged = append(merged, unit)
	}
	for _, unit := range user {
		if seen[unit.Name] {
			continue
		}
		seen[unit.Name] = true
		merged = append(merged, unit)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if (merged[i].ActiveState == "failed") != (merged[j].ActiveState == "failed") {
			return merged[i].ActiveState == "failed"
		}
		return merged[i].Name < merged[j].Name
	})
	return merged, nil
}

func parseSystemctlUnits(stdout, scope string) []Unit {
	units := make([]Unit, 0, 32)
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" {
			continue
		}
		trimmed = unitBullet.ReplaceAllString(trimmed, "")
		if trimmed == "" || regexp.MustCompile(`(?i)^UNIT\b`).MatchString(trimmed) ||
			regexp.MustCompile(`(?i)^Legend:`).MatchString(trimmed) ||
			regexp.MustCompile(`(?i)^\d+ loaded units listed`).MatchString(trimmed) ||
			regexp.MustCompile(`(?i)^To show all`).MatchString(trimmed) {
			continue
		}
		match := unitLinePattern.FindStringSubmatch(trimmed)
		if match == nil || !strings.Contains(match[1], ".") {
			continue
		}
		units = append(units, Unit{
			Name:        match[1],
			LoadState:   normalizeLoadState(match[2]),
			ActiveState: normalizeActiveState(match[3]),
			SubState:    match[4],
			Description: strings.TrimSpace(match[5]),
			Scope:       scope,
		})
	}
	return units
}

func extractBetween(stdout, startMarker string, endMarkers ...string) string {
	begin := strings.Index(stdout, startMarker)
	if begin < 0 {
		return ""
	}
	after := stdout[begin+len(startMarker):]
	end := -1
	for _, marker := range endMarkers {
		if index := strings.Index(after, marker); index >= 0 && (end < 0 || index < end) {
			end = index
		}
	}
	if end >= 0 {
		return after[:end]
	}
	return after
}

func normalizeActiveState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "active", "inactive", "failed", "activating", "deactivating", "reloading":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "unknown"
	}
}

func normalizeLoadState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "loaded", "not-found", "bad-setting", "error", "masked":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "unknown"
	}
}

// --- tmux sessions ---------------------------------------------------------

var (
	tmuxPlainPattern = regexp.MustCompile(`^([^:]+):\s*(\d+)\s+windows?\b`)
	tmuxAttachedText = regexp.MustCompile(`(?i)\battached\b`)
	// tmux diagnostics must never become session rows: a stale socket prints
	// "error connecting to /tmp/tmux-0/default", and a stopped server prints
	// "no server running on /tmp/tmux-0/default".
	tmuxDiagnosticPattern = regexp.MustCompile(
		`(?i)^(error connecting to|no server running|no current client|can't find|` +
			`lost server|server exited|failed to connect|protocol version mismatch|` +
			`open terminal failed|invalid option|usage:|unknown command)`)
)

// parseTmuxSessions reads the tab-separated -F reply and the plain list
// fallback. A stopped tmux server is an empty list, not a failure.
func parseTmuxSessions(stdout string) ([]TmuxSession, error) {
	if hasUnavailable(stdout, "tmux") {
		return nil, &Error{Reason: ReasonMissingTmux,
			Cause: errors.New("tmux: the command is unavailable")}
	}
	sessions := make([]TmuxSession, 0, 8)
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "__NC_") ||
			tmuxDiagnosticPattern.MatchString(trimmed) {
			continue
		}
		fields := strings.Split(trimmed, "\t")
		if len(fields) < 4 {
			// A tmux that did not expand the format's tabs prints them
			// literally; the original did the same on some builds.
			fields = strings.Split(trimmed, `\t`)
		}
		if len(fields) >= 4 && !strings.Contains(fields[0], ":") {
			name := strings.TrimSpace(fields[0])
			if name == "" {
				continue
			}
			created, _ := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
			sessions = append(sessions, TmuxSession{
				Name:     name,
				Windows:  parseInt(fields[1]),
				Attached: strings.TrimSpace(fields[2]) == "1",
				Created:  created,
			})
			continue
		}
		if match := tmuxPlainPattern.FindStringSubmatch(trimmed); match != nil {
			sessions = append(sessions, TmuxSession{
				Name:     strings.TrimSpace(match[1]),
				Windows:  parseInt(match[2]),
				Attached: tmuxAttachedText.MatchString(trimmed),
			})
		}
	}
	return sessions, nil
}

// --- Docker containers and images ------------------------------------------

type dockerContainerJSON struct {
	ID        string `json:"ID"`
	IDLower   string `json:"Id"`
	Names     string `json:"Names"`
	Name      string `json:"Name"`
	Image     string `json:"Image"`
	Status    string `json:"Status"`
	State     string `json:"State"`
	Ports     string `json:"Ports"`
	CreatedAt string `json:"CreatedAt"`
	Created   string `json:"Created"`
}

type dockerImageJSON struct {
	ID           string `json:"ID"`
	IDLower      string `json:"Id"`
	Repository   string `json:"Repository"`
	Tag          string `json:"Tag"`
	Size         string `json:"Size"`
	CreatedAt    string `json:"CreatedAt"`
	CreatedSince string `json:"CreatedSince"`
	Digest       string `json:"Digest"`
}

// parseContainerList reads the container and image JSON-line sections from
// the selected engine. The command captures each failure into a marker
// section, so a missing daemon or socket permission produces a typed reason
// rather than an empty list.
func parseContainerList(stdout string, runtime ContainerRuntime) ([]Container, []Image, error) {
	bin := runtime.Binary()
	if hasUnavailable(stdout, bin) {
		reason := ReasonMissingContainerRuntime
		if runtime == RuntimeDocker {
			reason = ReasonMissingDocker
		}
		return nil, nil, &Error{Reason: reason,
			Cause: errors.New(bin + ": the command is unavailable")}
	}
	if detail := dockerErrorText(stdout); detail != "" {
		return nil, nil, containerFailure(detail, runtime)
	}
	containerText := extractBetween(stdout, "__NC_DOCKER_CONTAINERS__",
		"__NC_DOCKER_IMAGES__", "__NC_DOCKER_END__")
	imageText := extractBetween(stdout, "__NC_DOCKER_IMAGES__", "__NC_DOCKER_END__")

	containers := make([]Container, 0, 16)
	for _, line := range strings.Split(containerText, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
			continue
		}
		var row dockerContainerJSON
		if err := json.Unmarshal([]byte(trimmed), &row); err != nil {
			continue
		}
		id := row.ID
		if id == "" {
			id = row.IDLower
		}
		name := row.Names
		if name == "" {
			name = row.Name
		}
		name = strings.TrimPrefix(strings.TrimSpace(name), "/")
		created := row.CreatedAt
		if created == "" {
			created = row.Created
		}
		containers = append(containers, Container{
			ID:        id,
			Name:      name,
			Image:     row.Image,
			State:     row.State,
			Status:    row.Status,
			Ports:     row.Ports,
			CreatedAt: created,
		})
	}

	images := make([]Image, 0, 16)
	for _, line := range strings.Split(imageText, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
			continue
		}
		var row dockerImageJSON
		if err := json.Unmarshal([]byte(trimmed), &row); err != nil {
			continue
		}
		id := row.ID
		if id == "" {
			id = row.IDLower
		}
		created := row.CreatedAt
		if created == "" {
			created = row.CreatedSince
		}
		images = append(images, Image{
			ID:         id,
			Repository: row.Repository,
			Tag:        row.Tag,
			Size:       row.Size,
			CreatedAt:  created,
			Digest:     row.Digest,
		})
	}
	return containers, images, nil
}

// parseDockerList is the Docker-only form kept for callers and tests that
// predate the runtime switcher.
func parseDockerList(stdout string) ([]Container, []Image, error) {
	return parseContainerList(stdout, RuntimeDocker)
}

// dockerErrorText joins every captured docker failure section.
func dockerErrorText(stdout string) string {
	var (
		builder strings.Builder
		inside  bool
	)
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch trimmed {
		case "__NC_DOCKER_ERROR_BEGIN__":
			inside = true
			continue
		case "__NC_DOCKER_ERROR_END__":
			inside = false
			continue
		}
		if inside && trimmed != "" {
			builder.WriteString(trimmed)
			builder.WriteByte('\n')
		}
	}
	return strings.TrimSpace(builder.String())
}

// containerFailure maps a runtime's stderr text onto the surface's typed
// reasons. Docker keeps its historical daemon reason; Podman and nerdctl use
// the shared container-daemon reason.
func containerFailure(detail string, runtime ContainerRuntime) error {
	if runtime == RuntimeDocker {
		return dockerFailure(detail)
	}
	lower := strings.ToLower(detail)
	reason := ReasonInternal
	switch {
	case strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "operation not permitted"):
		reason = ReasonPermission
	case isContainerDaemon(detail),
		strings.Contains(lower, "podman daemon"),
		strings.Contains(lower, "cannot connect"):
		reason = ReasonContainerDaemon
	case strings.Contains(lower, "command not found"),
		strings.Contains(lower, "no such file or directory"):
		reason = ReasonMissingContainerRuntime
	}
	return &Error{Reason: reason, Cause: errors.New(detail)}
}

// isContainerDaemon reports stderr text from an unreachable podman or nerdctl
// daemon. The same symptoms appear in a command's captured output and on the
// closed-connection path, so both failure mappers share one list.
func isContainerDaemon(detail string) bool {
	lower := strings.ToLower(detail)
	for _, symptom := range []string{
		"podman.sock",
		"cannot connect to podman",
		"containerd.sock",
		"rootless containerd",
	} {
		if strings.Contains(lower, symptom) {
			return true
		}
	}
	return false
}

// dockerFailure maps Docker's stderr text onto the surface's typed reasons.
func dockerFailure(detail string) error {
	lower := strings.ToLower(detail)
	reason := ReasonInternal
	switch {
	case strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "operation not permitted"):
		reason = ReasonPermission
	case strings.Contains(lower, "cannot connect to the docker daemon"),
		strings.Contains(lower, "is the docker daemon running"),
		strings.Contains(lower, "error during connect"):
		reason = ReasonDockerDaemon
	case strings.Contains(lower, "command not found"),
		strings.Contains(lower, "no such file or directory"):
		reason = ReasonMissingDocker
	}
	return &Error{Reason: reason, Cause: errors.New(detail)}
}

// --- Kubernetes namespaces and pods ---------------------------------------

type kubeMetadataJSON struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	CreationTimestamp string `json:"creationTimestamp"`
	DeletionTimestamp string `json:"deletionTimestamp"`
}

type kubeNamespaceListJSON struct {
	Items []struct {
		Metadata kubeMetadataJSON `json:"metadata"`
	} `json:"items"`
}

type kubeContainerSpecJSON struct {
	Name string `json:"name"`
}

type kubeStateJSON struct {
	Waiting *struct {
		Reason string `json:"reason"`
	} `json:"waiting"`
	Terminated *struct {
		Reason string `json:"reason"`
	} `json:"terminated"`
	Running *struct{} `json:"running"`
}

type kubeContainerStatusJSON struct {
	Name         string        `json:"name"`
	RestartCount int           `json:"restartCount"`
	State        kubeStateJSON `json:"state"`
}

type kubePodJSON struct {
	Metadata kubeMetadataJSON `json:"metadata"`
	Spec     struct {
		Containers          []kubeContainerSpecJSON `json:"containers"`
		InitContainers      []kubeContainerSpecJSON `json:"initContainers"`
		EphemeralContainers []kubeContainerSpecJSON `json:"ephemeralContainers"`
	} `json:"spec"`
	Status struct {
		Phase                      string                    `json:"phase"`
		Reason                     string                    `json:"reason"`
		ContainerStatuses          []kubeContainerStatusJSON `json:"containerStatuses"`
		InitContainerStatuses      []kubeContainerStatusJSON `json:"initContainerStatuses"`
		EphemeralContainerStatuses []kubeContainerStatusJSON `json:"ephemeralContainerStatuses"`
	} `json:"status"`
}

type kubePodListJSON struct {
	Items []kubePodJSON `json:"items"`
}

// parseKubernetesList reads the namespace and pod JSON sections. A forbidden
// namespace list is tolerated because a restricted kubeconfig often permits
// pods in its own namespace but not a cluster-wide namespace list; any other
// failure is typed. now anchors the pod age the surface shows.
func parseKubernetesList(stdout string, now time.Time) (Kubernetes, error) {
	inventory := Kubernetes{Namespaces: make([]string, 0, 16), Pods: make([]Pod, 0, 32)}
	if hasUnavailable(stdout, "kubectl") {
		return inventory, &Error{Reason: ReasonMissingKubectl,
			Cause: errors.New("kubectl: the command is unavailable")}
	}

	namespacesText := extractBetween(stdout, markerKubernetesNamespaces,
		markerKubernetesNamespacesEnd)
	namespacesBody, namespacesErr := splitKubernetesSection(namespacesText)
	if namespacesErr != "" {
		if !isKubectlForbidden(namespacesErr) {
			return inventory, kubernetesFailure(namespacesErr)
		}
	} else if strings.TrimSpace(namespacesBody) != "" {
		var list kubeNamespaceListJSON
		if err := decodeJSONAfterBanner(namespacesBody, &list, "items"); err != nil {
			return inventory, &Error{Reason: ReasonInternal,
				Cause: fmt.Errorf("kubectl: parse namespaces: %w", err)}
		}
		for _, item := range list.Items {
			if name := strings.TrimSpace(item.Metadata.Name); name != "" {
				inventory.Namespaces = append(inventory.Namespaces, name)
			}
		}
	}

	podsText := extractBetween(stdout, markerKubernetesPods, markerKubernetesPodsEnd)
	podsBody, podsErr := splitKubernetesSection(podsText)
	if podsErr != "" {
		return inventory, kubernetesFailure(podsErr)
	}
	if strings.TrimSpace(podsBody) != "" {
		var list kubePodListJSON
		if err := decodeJSONAfterBanner(podsBody, &list, "items"); err != nil {
			return inventory, &Error{Reason: ReasonInternal,
				Cause: fmt.Errorf("kubectl: parse pods: %w", err)}
		}
		for _, item := range list.Items {
			name := strings.TrimSpace(item.Metadata.Name)
			if name == "" {
				continue
			}
			inventory.Pods = append(inventory.Pods, kubePod(item, now))
		}
	}

	// An empty pod list cannot tell which namespace the kubeconfig used, so
	// leave it empty rather than claiming "default": the surface then lets the
	// kubeconfig's choice stand instead of displaying a namespace that was
	// never queried.
	for _, pod := range inventory.Pods {
		if pod.Namespace != "" {
			inventory.Namespace = pod.Namespace
			break
		}
	}
	return inventory, nil
}

// splitKubernetesSection separates a section's JSON from the failure text the
// command captured after its error marker.
func splitKubernetesSection(text string) (body, failure string) {
	if index := strings.Index(text, markerKubernetesError); index >= 0 {
		return "", strings.TrimSpace(text[index+len(markerKubernetesError):])
	}
	return text, ""
}

// decodeJSONAfterBanner skips a kubectl banner such as a bracketed warning
// before the JSON document. Trying each opening brace keeps an occasional
// nested brace from ending the search at the first one. A warning that happens
// to be JSON-shaped must not shadow the real document, so the body is decoded
// as a stream of successive JSON values and only the value carrying the
// payload's required top-level key is accepted. The warning may precede or
// follow the list: neither a successful decode nor extra trailing data makes it
// the answer. When no value carries the key, the reply is a parse failure
// rather than a silently empty inventory.
func decodeJSONAfterBanner(text string, target any, requiredKey string) error {
	trimmed := strings.TrimSpace(text)
	for offset := 0; offset < len(trimmed); offset++ {
		if trimmed[offset] != '{' && trimmed[offset] != '[' {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(trimmed[offset:]))
		for {
			var candidate json.RawMessage
			if err := decoder.Decode(&candidate); err != nil {
				// End of the decodable stream: text after the last value is a
				// banner or a malformed tail, not another document.
				break
			}
			if requiredKey != "" && !jsonObjectHasKey(candidate, requiredKey) {
				continue
			}
			return json.Unmarshal(candidate, target)
		}
	}
	if requiredKey != "" {
		return fmt.Errorf("no JSON document with top-level key %q", requiredKey)
	}
	return errors.New("no JSON document")
}

// jsonObjectHasKey reports whether raw is a JSON object with key at its top
// level. The Kubernetes list payloads both use "items"; a warning object that
// merely parses as JSON does not.
func jsonObjectHasKey(raw []byte, key string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return false
	}
	_, ok := object[key]
	return ok
}

func kubePod(pod kubePodJSON, now time.Time) Pod {
	names := make([]string, 0, len(pod.Spec.Containers))
	specs := make([]kubeContainerSpecJSON, 0,
		len(pod.Spec.Containers)+len(pod.Spec.InitContainers)+len(pod.Spec.EphemeralContainers))
	specs = append(specs, pod.Spec.Containers...)
	specs = append(specs, pod.Spec.InitContainers...)
	specs = append(specs, pod.Spec.EphemeralContainers...)
	for _, container := range specs {
		name := strings.TrimSpace(container.Name)
		if name == "" || containsString(names, name) {
			continue
		}
		names = append(names, name)
	}
	running := make([]string, 0, len(names))
	statuses := make([]kubeContainerStatusJSON, 0,
		len(pod.Status.ContainerStatuses)+len(pod.Status.InitContainerStatuses)+
			len(pod.Status.EphemeralContainerStatuses))
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	statuses = append(statuses, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.EphemeralContainerStatuses...)
	restarts := 0
	for _, status := range statuses {
		restarts += status.RestartCount
		if status.State.Running != nil && status.Name != "" &&
			!containsString(running, status.Name) {
			running = append(running, status.Name)
		}
	}
	// The declared order is the terminal's natural order; a running status
	// the spec omitted still gets a place after every declared container.
	ordered := make([]string, 0, len(running))
	for _, name := range names {
		if containsString(running, name) {
			ordered = append(ordered, name)
		}
	}
	for _, name := range running {
		if !containsString(ordered, name) {
			ordered = append(ordered, name)
		}
	}
	return Pod{
		Name:              strings.TrimSpace(pod.Metadata.Name),
		Namespace:         strings.TrimSpace(pod.Metadata.Namespace),
		Status:            kubePodStatus(pod),
		Restarts:          restarts,
		Age:               kubePodAge(pod.Metadata.CreationTimestamp, now),
		Containers:        names,
		RunningContainers: ordered,
	}
}

func kubePodStatus(pod kubePodJSON) string {
	if strings.TrimSpace(pod.Metadata.DeletionTimestamp) != "" {
		return "Terminating"
	}
	statuses := make([]kubeContainerStatusJSON, 0,
		len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
	statuses = append(statuses, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	for _, status := range statuses {
		if status.State.Waiting != nil && status.State.Waiting.Reason != "" {
			return status.State.Waiting.Reason
		}
	}
	if reason := strings.TrimSpace(pod.Status.Reason); reason != "" {
		return reason
	}
	for _, status := range statuses {
		if status.State.Terminated != nil &&
			status.State.Terminated.Reason != "" &&
			status.State.Terminated.Reason != "Completed" {
			return status.State.Terminated.Reason
		}
	}
	phase := strings.TrimSpace(pod.Status.Phase)
	if phase == "Succeeded" {
		return "Completed"
	}
	if phase == "" {
		return "Unknown"
	}
	return phase
}

// kubePodAge formats a creation timestamp the way kubectl's AGE column does.
func kubePodAge(created string, now time.Time) string {
	timestamp, err := time.Parse(time.RFC3339, strings.TrimSpace(created))
	if err != nil {
		return ""
	}
	seconds := int(now.Sub(timestamp) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	switch {
	case seconds < 60:
		return strconv.Itoa(seconds) + "s"
	case seconds < 3600:
		return strconv.Itoa(seconds/60) + "m"
	case seconds < 86400:
		return strconv.Itoa(seconds/3600) + "h"
	default:
		return strconv.Itoa(seconds/86400) + "d"
	}
}

// isKubectlForbidden reports a refusal the namespace selector can survive.
func isKubectlForbidden(detail string) bool {
	return strings.Contains(strings.ToLower(detail), "forbidden")
}

// kubernetesFailure maps kubectl's stderr text onto the surface's typed
// reasons.
func kubernetesFailure(detail string) error {
	lower := strings.ToLower(detail)
	reason := ReasonInternal
	switch {
	case isKubectlForbidden(detail):
		reason = ReasonKubernetesForbidden
	case strings.Contains(lower, "unable to connect to the server"),
		strings.Contains(lower, "couldn't get current server api group list"),
		strings.Contains(lower, "could not get current server api group list"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "dial tcp"),
		strings.Contains(lower, "i/o timeout"),
		strings.Contains(lower, "no such host"),
		strings.Contains(lower, "network is unreachable"),
		strings.Contains(lower, "context deadline exceeded"),
		strings.Contains(lower, "tls handshake timeout"):
		reason = ReasonClusterUnreachable
	case strings.Contains(lower, "command not found"):
		reason = ReasonMissingKubectl
	}
	return &Error{Reason: reason, Cause: errors.New(detail)}
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// --- Accelerators ----------------------------------------------------------

// parseGPU reads the nvidia-smi CSV section. An absent tool or driver leaves
// the section empty and becomes the normal empty GPU state, never an error.
func parseGPU(stdout string) (GPUInfo, error) {
	info := GPUInfo{Devices: make([]GPUDevice, 0, 4)}
	section := extractBetween(stdout, "__NC_NVIDIA__", "__NC_GPU_END__")
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "__NC_") {
			continue
		}
		fields := strings.Split(trimmed, ",")
		if len(fields) < 3 {
			continue
		}
		index, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil || index < 0 {
			continue
		}
		device := GPUDevice{
			Vendor:             "nvidia",
			Index:              index,
			UUID:               csvField(fields, 1),
			Name:               csvField(fields, 2),
			UtilizationPercent: parseFloat(csvField(fields, 3)),
			MemoryUsedMB:       parseFloat(csvField(fields, 4)),
			MemoryTotalMB:      parseFloat(csvField(fields, 5)),
			TemperatureC:       parseFloat(csvField(fields, 6)),
			PowerDrawW:         parseFloat(csvField(fields, 7)),
			PowerLimitW:        parseFloat(csvField(fields, 8)),
			FanPercent:         parseFloat(csvField(fields, 9)),
			DriverVersion:      csvField(fields, 10),
		}
		if !metricReported(device.DriverVersion) {
			device.DriverVersion = ""
		} else if info.DriverVersion == "" {
			info.DriverVersion = device.DriverVersion
		}
		info.Devices = append(info.Devices, device)
	}
	sort.SliceStable(info.Devices, func(i, j int) bool {
		return info.Devices[i].Index < info.Devices[j].Index
	})
	return info, nil
}

func csvField(fields []string, index int) string {
	if index < 0 || index >= len(fields) {
		return ""
	}
	return strings.TrimSpace(fields[index])
}

// metricReported reports whether a driver text field carries a value rather
// than nvidia-smi's placeholders.
func metricReported(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "-", "n/a", "[n/a]", "not supported", "unknown":
		return false
	default:
		return true
	}
}
