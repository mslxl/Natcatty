package sysmanager

import (
	"slices"
	"testing"
)

// TestFilterProcessesRunningOnlyUsesTheStatePrefix proves the Processes tab's
// running filter keeps only rows whose state begins with R and leaves the
// order untouched. Off is the default and keeps every row.
func TestFilterProcessesRunningOnlyUsesTheStatePrefix(t *testing.T) {
	processes := []Process{
		{PID: 1, PPID: 0, User: "root", Stat: "Ss", Command: "init"},
		{PID: 2, PPID: 1, User: "root", Stat: "R", Command: "worker"},
		{PID: 3, PPID: 1, User: "app", Stat: "Rsl", Command: "runner"},
		{PID: 4, PPID: 1, User: "app", Stat: "S", Command: "sleeper"},
		{PID: 5, PPID: 1, User: "app", Stat: "D", Command: "waiter"},
	}

	all := FilterProcesses(processes, "", false)
	if got := pids(all); !slices.Equal(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("running-only off returned PIDs %v, want every row in order", got)
	}

	running := FilterProcesses(processes, "", true)
	if got := pids(running); !slices.Equal(got, []int{2, 3}) {
		t.Fatalf("running-only on returned PIDs %v, want the R-prefixed rows", got)
	}

	for _, test := range []struct {
		stat string
		want bool
	}{
		{"R", true},
		{"Rsl", true},
		{"R+", true},
		{"S", false},
		{"Ss", false},
		{"D", false},
		{"", false},
	} {
		if got := ProcessRunning(Process{Stat: test.stat}); got != test.want {
			t.Errorf("ProcessRunning(stat %q) = %v, want %v", test.stat, got, test.want)
		}
	}
}

// TestFilterProcessesSearchMatchesPPIDAndExistingFields proves a PPID query
// returns a parent's children while the existing PID, user, state, and
// command matches still work, and that the running filter combines with it.
func TestFilterProcessesSearchMatchesPPIDAndExistingFields(t *testing.T) {
	processes := []Process{
		{PID: 100, PPID: 1, User: "root", Stat: "Ss", Command: "parent-daemon"},
		{PID: 101, PPID: 100, User: "root", Stat: "R", Command: "child-runner"},
		{PID: 102, PPID: 100, User: "app", Stat: "S", Command: "child-sleeper"},
		{PID: 200, PPID: 1, User: "db", Stat: "S", Command: "database"},
	}
	tests := []struct {
		name        string
		query       string
		runningOnly bool
		want        []int
	}{
		{"empty query", "", false, []int{100, 101, 102, 200}},
		{"parent PID finds itself and its children", "100", false, []int{100, 101, 102}},
		{"PID still matches", "101", false, []int{101}},
		{"user still matches", "app", false, []int{102}},
		{"state still matches", "Ss", false, []int{100}},
		{"command still matches", "child-runner", false, []int{101}},
		{"PPID plus running-only", "100", true, []int{101}},
		{"no match", "nothing-here", false, nil},
	}
	for _, test := range tests {
		got := pids(FilterProcesses(processes, test.query, test.runningOnly))
		if !slices.Equal(got, test.want) {
			t.Errorf("%s: FilterProcesses(query %q, runningOnly %v) = %v, want %v",
				test.name, test.query, test.runningOnly, got, test.want)
		}
	}
}

func pids(processes []Process) []int {
	out := make([]int, 0, len(processes))
	for _, process := range processes {
		out = append(out, process.PID)
	}
	return out
}

// TestFilterPortsByProtocolKeepsSearchAndOrder proves All keeps every socket,
// TCP and UDP narrow by the normalized protocol (IPv6 included), and the
// existing search still narrows within the chosen protocol.
func TestFilterPortsByProtocolKeepsSearchAndOrder(t *testing.T) {
	ports := []Port{
		{Protocol: "tcp", Address: "0.0.0.0", Port: 22, PID: 1, ProcessName: "sshd"},
		{Protocol: "udp", Address: "0.0.0.0", Port: 53, PID: 2, ProcessName: "dnsmasq"},
		{Protocol: "tcp6", Address: "::", Port: 443, PID: 3, ProcessName: "nginx"},
		{Protocol: "udp6", Address: "::", Port: 5353, PID: 7, ProcessName: "avahi"},
		{Protocol: "unknown", Address: "0.0.0.0", Port: 999, PID: 0},
	}
	portsOf := func(rows []Port) []int {
		out := make([]int, 0, len(rows))
		for _, port := range rows {
			out = append(out, port.Port)
		}
		return out
	}

	for _, test := range []struct {
		name   string
		query  string
		filter PortFilter
		want   []int
	}{
		{"default All", "", "", []int{22, 53, 443, 5353, 999}},
		{"explicit All", "", PortFilterAll, []int{22, 53, 443, 5353, 999}},
		{"TCP includes IPv6", "", PortFilterTCP, []int{22, 443}},
		{"UDP includes IPv6", "", PortFilterUDP, []int{53, 5353}},
		{"search keeps working", "53", PortFilterAll, []int{53, 5353}},
		{"search within TCP", "443", PortFilterTCP, []int{443}},
		{"search excludes other protocols", "53", PortFilterTCP, nil},
		{"process name search", "nginx", PortFilterAll, []int{443}},
		{"PID search", "7", PortFilterAll, []int{5353}},
	} {
		got := portsOf(FilterPorts(ports, test.query, test.filter))
		if !slices.Equal(got, test.want) {
			t.Errorf("%s: FilterPorts(query %q, filter %q) = %v, want %v",
				test.name, test.query, test.filter, got, test.want)
		}
	}
}

// TestFilterServicesByStateKeepsFailedFirstOrder proves each state choice
// narrows the units, All keeps the parser's failed-first order, and the
// existing search still narrows the list.
func TestFilterServicesByStateKeepsFailedFirstOrder(t *testing.T) {
	units := []Unit{
		{Name: "broken.service", Scope: "system", ActiveState: "failed", SubState: "failed"},
		{Name: "boot.service", Scope: "system", ActiveState: "activating", SubState: "start"},
		{Name: "idle.service", Scope: "user", ActiveState: "inactive", SubState: "dead"},
		{Name: "nginx.service", Scope: "system", ActiveState: "active", SubState: "running"},
		{Name: "agent.service", Scope: "user", ActiveState: "active", SubState: "running"},
	}
	names := func(rows []Unit) []string {
		out := make([]string, 0, len(rows))
		for _, unit := range rows {
			out = append(out, unit.Name)
		}
		return out
	}

	for _, test := range []struct {
		name   string
		query  string
		filter ServiceFilter
		want   []string
	}{
		{"default All", "", "", []string{"broken.service", "boot.service", "idle.service", "nginx.service", "agent.service"}},
		{"explicit All", "", ServiceFilterAll, []string{"broken.service", "boot.service", "idle.service", "nginx.service", "agent.service"}},
		{"Running means active", "", ServiceFilterRunning, []string{"nginx.service", "agent.service"}},
		{"Failed", "", ServiceFilterFailed, []string{"broken.service"}},
		{"Inactive", "", ServiceFilterInactive, []string{"idle.service"}},
		{"search still narrows", "nginx", ServiceFilterAll, []string{"nginx.service"}},
		{"search within a state", "agent", ServiceFilterRunning, []string{"agent.service"}},
		{"search within another state", "agent", ServiceFilterFailed, nil},
		{"scope search", "user", ServiceFilterAll, []string{"idle.service", "agent.service"}},
	} {
		got := names(FilterServices(units, test.query, test.filter))
		if !slices.Equal(got, test.want) {
			t.Errorf("%s: FilterServices(query %q, filter %q) = %v, want %v",
				test.name, test.query, test.filter, got, test.want)
		}
	}
}

// TestServiceActionsMatchTheActiveState proves Start is offered only for
// inactive or failed units, Stop only for active units, and Restart always.
func TestServiceActionsMatchTheActiveState(t *testing.T) {
	for _, test := range []struct {
		state       string
		canStart    bool
		canStop     bool
		wantActions []ServiceAction
	}{
		{"inactive", true, false, []ServiceAction{ServiceStart, ServiceRestart}},
		{"failed", true, false, []ServiceAction{ServiceStart, ServiceRestart}},
		{"active", false, true, []ServiceAction{ServiceStop, ServiceRestart}},
		{"activating", false, false, []ServiceAction{ServiceRestart}},
		{"deactivating", false, false, []ServiceAction{ServiceRestart}},
		{"reloading", false, false, []ServiceAction{ServiceRestart}},
		{"Active", false, true, []ServiceAction{ServiceStop, ServiceRestart}},
		{"", false, false, []ServiceAction{ServiceRestart}},
	} {
		unit := Unit{Name: "unit.service", ActiveState: test.state}
		if got := unit.CanStart(); got != test.canStart {
			t.Errorf("state %q: CanStart = %v, want %v", test.state, got, test.canStart)
		}
		if got := unit.CanStop(); got != test.canStop {
			t.Errorf("state %q: CanStop = %v, want %v", test.state, got, test.canStop)
		}
		if got := unit.Actions(); !slices.Equal(got, test.wantActions) {
			t.Errorf("state %q: Actions = %v, want %v", test.state, got, test.wantActions)
		}
	}
}
