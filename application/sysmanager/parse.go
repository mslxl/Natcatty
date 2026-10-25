package sysmanager

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
}

func (s overviewSample) counters() overviewCounters {
	return overviewCounters{mode: s.mode, total: s.cpuTotal, idle: s.cpuIdle}
}

// overviewCounters is the previous CPU reading a service keeps per run.
type overviewCounters struct {
	mode  string
	total float64
	idle  float64
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
	return sample, nil
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
