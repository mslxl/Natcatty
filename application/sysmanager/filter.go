package sysmanager

import (
	"strconv"
	"strings"
)

// FilterProcesses narrows the Processes tab. runningOnly keeps rows whose
// state begins with R; query then matches the PID, PPID, user, state, or
// command. Rows keep their input order, and an empty query keeps every row.
func FilterProcesses(processes []Process, query string, runningOnly bool) []Process {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]Process, 0, len(processes))
	for _, process := range processes {
		if runningOnly && !ProcessRunning(process) {
			continue
		}
		if !processMatches(process, query) {
			continue
		}
		out = append(out, process)
	}
	return out
}

// ProcessRunning reports whether the process's state begins with R, ps's
// marker for a running process.
func ProcessRunning(process Process) bool {
	return strings.HasPrefix(strings.TrimSpace(process.Stat), "R")
}

func processMatches(process Process, query string) bool {
	if query == "" {
		return true
	}
	fields := []string{
		strconv.Itoa(process.PID),
		strconv.Itoa(process.PPID),
		process.User,
		process.Stat,
		process.Command,
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// PortFilter narrows the Ports tab to a socket protocol. The zero value and
// PortFilterAll keep every protocol.
type PortFilter string

const (
	PortFilterAll PortFilter = "all"
	PortFilterTCP PortFilter = "tcp"
	PortFilterUDP PortFilter = "udp"
)

// FilterPorts narrows the Ports tab to the chosen protocol, then to rows
// matching query across port, protocol, address, PID, and process name. Rows
// keep their input order.
func FilterPorts(ports []Port, query string, filter PortFilter) []Port {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]Port, 0, len(ports))
	for _, port := range ports {
		if !portFilterMatches(port, filter) {
			continue
		}
		if !portMatches(port, query) {
			continue
		}
		out = append(out, port)
	}
	return out
}

func portFilterMatches(port Port, filter PortFilter) bool {
	protocol := strings.ToLower(port.Protocol)
	switch filter {
	case PortFilterTCP:
		return strings.HasPrefix(protocol, "tcp")
	case PortFilterUDP:
		return strings.HasPrefix(protocol, "udp")
	default:
		return true
	}
}

func portMatches(port Port, query string) bool {
	if query == "" {
		return true
	}
	fields := []string{
		strconv.Itoa(port.Port),
		port.Protocol,
		port.Address,
		strconv.Itoa(port.PID),
		port.ProcessName,
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// ServiceFilter narrows the Services tab to a unit state. The zero value and
// ServiceFilterAll keep every state.
type ServiceFilter string

const (
	ServiceFilterAll      ServiceFilter = "all"
	ServiceFilterRunning  ServiceFilter = "running"
	ServiceFilterFailed   ServiceFilter = "failed"
	ServiceFilterInactive ServiceFilter = "inactive"
)

// FilterServices narrows the Services tab to the chosen state, then to units
// matching query across name, description, scope, and states. Rows keep their
// input order, so the parser's failed-first ordering survives.
func FilterServices(units []Unit, query string, filter ServiceFilter) []Unit {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]Unit, 0, len(units))
	for _, unit := range units {
		if !serviceFilterMatches(unit, filter) {
			continue
		}
		if !serviceMatches(unit, query) {
			continue
		}
		out = append(out, unit)
	}
	return out
}

func serviceFilterMatches(unit Unit, filter ServiceFilter) bool {
	switch filter {
	case ServiceFilterRunning:
		return strings.EqualFold(unit.ActiveState, "active")
	case ServiceFilterFailed:
		return strings.EqualFold(unit.ActiveState, "failed")
	case ServiceFilterInactive:
		return strings.EqualFold(unit.ActiveState, "inactive")
	default:
		return true
	}
}

func serviceMatches(unit Unit, query string) bool {
	if query == "" {
		return true
	}
	fields := []string{
		unit.Name,
		unit.Description,
		unit.Scope,
		unit.LoadState,
		unit.ActiveState,
		unit.SubState,
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// CanStart reports whether systemctl may start the unit: only an inactive or
// failed unit can be started.
func (u Unit) CanStart() bool {
	return strings.EqualFold(u.ActiveState, "inactive") || strings.EqualFold(u.ActiveState, "failed")
}

// CanStop reports whether systemctl may stop the unit: only an active unit can
// be stopped.
func (u Unit) CanStop() bool {
	return strings.EqualFold(u.ActiveState, "active")
}

// Actions returns the service actions the surface may offer for the unit, in
// display order. Start appears only when the unit can start, Stop only when it
// can stop, and Restart always appears.
func (u Unit) Actions() []ServiceAction {
	actions := make([]ServiceAction, 0, 2)
	if u.CanStart() {
		actions = append(actions, ServiceStart)
	}
	if u.CanStop() {
		actions = append(actions, ServiceStop)
	}
	return append(actions, ServiceRestart)
}
