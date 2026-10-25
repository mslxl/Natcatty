package sysmanager

import (
	"fmt"
	"strconv"
	"strings"
)

// The command templates mirror the original Electron system-manager bridge.
// They are fixed strings: markers separate a probe's output, unrelated shell
// noise falls outside the sections, and a Host without a command prints the
// typed marker the parser turns into a degraded state.

const (
	markerUnavailable = "__NC_UNAVAILABLE"
	markerOS          = "__NC_OS"
	markerHasPrefix   = "__NC_HAS_"
)

// probeCommand reports the remote OS and which commands exist.
func probeCommand() string {
	return `printf "` + markerOS + `=%s\n" "$(uname -s 2>/dev/null || echo Unknown)"; ` +
		`for c in ps top ss netstat lsof systemctl; do ` +
		`if command -v "$c" >/dev/null 2>&1; then printf "` + markerHasPrefix + `%s=1\n" "$c"; fi; done`
}

// processesCommand is the original's process list with a typed marker when
// neither ps nor top exists.
func processesCommand() string {
	return `if command -v ps >/dev/null 2>&1 || command -v top >/dev/null 2>&1; then ` +
		`ps -eo pid= -o ppid= -o user= -o stat= -o pcpu= -o pmem= -o rss= -o vsz= -o etime= -o args= 2>/dev/null ` +
		`|| top -b -n 1 2>/dev/null || ps ww 2>/dev/null || ps 2>/dev/null; ` +
		`else printf "` + markerUnavailable + `=processes\n"; fi`
}

// portsCommand runs every available collector, as the original did: macOS
// often lacks useful netstat PID columns and lsof fills the gap.
func portsCommand() string {
	return `printf "%s\n" "__NC_PORTS_BEGIN__"; ` +
		`if command -v ss >/dev/null 2>&1; then printf "%s\n" "__NC_SS__"; ` +
		`ss -H -tulnp 2>/dev/null || ss -tulnp 2>/dev/null || true; fi; ` +
		`if command -v netstat >/dev/null 2>&1; then printf "%s\n" "__NC_NETSTAT__"; ` +
		`if netstat -lntup 2>/dev/null; then :; ` +
		`elif netstat -lntp 2>/dev/null; then netstat -lnup 2>/dev/null || true; ` +
		`elif netstat -anv -p tcp >/dev/null 2>&1; then ` +
		`netstat -anv -p tcp 2>/dev/null | grep -i LISTEN || true; ` +
		`netstat -anv -p udp 2>/dev/null || true; ` +
		`else netstat -anp 2>/dev/null | grep -Ei 'LISTEN|^udp' || netstat -an 2>/dev/null | grep -Ei 'LISTEN|^udp' || true; fi; fi; ` +
		`if command -v lsof >/dev/null 2>&1; then printf "%s\n" "__NC_LSOF__"; ` +
		`lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null || true; ` +
		`lsof -nP -iUDP -sUDP:Idle 2>/dev/null || true; fi; ` +
		`printf "%s\n" "__NC_PORTS_END__"`
}

// servicesCommand lists system and user units. --plain needs systemd ~230,
// so an older host falls back to the column-format output.
func servicesCommand() string {
	return `printf "%s\n" "__NC_SERVICES_BEGIN__"; ` +
		`if command -v systemctl >/dev/null 2>&1; then printf "%s\n" "__NC_SYSTEM__"; ` +
		`systemctl list-units --type=service --all --no-pager --no-legend --plain 2>/dev/null ` +
		`|| systemctl list-units --type=service --all --no-pager --no-legend 2>/dev/null || true; ` +
		`printf "%s\n" "__NC_USER__"; ` +
		`systemctl --user list-units --type=service --all --no-pager --no-legend --plain 2>/dev/null ` +
		`|| systemctl --user list-units --type=service --all --no-pager --no-legend 2>/dev/null || true; ` +
		`else printf "` + markerUnavailable + `=systemctl\n"; fi; ` +
		`printf "%s\n" "__NC_SERVICES_END__"`
}

// overviewCommand reports the original's summary fields. Linux reads /proc
// and df; macOS uses sysctl, ps, vm_stat, and df. Both emit one labelled line
// so one parser serves them.
func overviewCommand() string {
	return `ostype=$(uname -s 2>/dev/null || echo Unknown); ` +
		`if [ "$ostype" = "Darwin" ]; then ` + darwinOverview + `; ` +
		`elif [ "$ostype" = "Linux" ]; then ` + linuxOverview + `; ` +
		`else printf "MODE:unsupported|OS:%s\n" "$ostype"; fi`
}

const linuxOverview = `host=$(hostname 2>/dev/null || uname -n 2>/dev/null); ` +
	`osname=$(awk -F= '/^PRETTY_NAME=/{gsub(/"/,"",$2);print $2;exit}' /etc/os-release 2>/dev/null); ` +
	`[ -n "$osname" ] || osname=$(uname -s 2>/dev/null); ` +
	`kernel=$(uname -r 2>/dev/null); ` +
	`uptime=$(awk '{printf "%.0f",$1}' /proc/uptime 2>/dev/null); ` +
	`load=$(awk '{print $1" "$2" "$3}' /proc/loadavg 2>/dev/null); ` +
	`cores=$(nproc 2>/dev/null || grep -c '^processor' /proc/cpuinfo 2>/dev/null); ` +
	`cpuraw=$(awk '/^cpu /{t=0;for(i=2;i<=NF;i++)t+=$i;printf "%d %d",t,$5}' /proc/stat 2>/dev/null); ` +
	`mem=$(awk '/^MemTotal:/{t=$2}/^MemFree:/{f=$2}/^Buffers:/{b=$2}/^Cached:/{c=$2}/^SReclaimable:/{s=$2}END{if(t>0)printf "%d %d",(t-f-b-c-s)/1024,t/1024}' /proc/meminfo 2>/dev/null); ` +
	`swap=$(awk '/^SwapTotal:/{t=$2}/^SwapFree:/{f=$2}END{if(t>0)printf "%d %d",(t-f)/1024,t/1024}' /proc/meminfo 2>/dev/null); ` +
	`disk=$(df -Pk / 2>/dev/null | awk 'NR==2{printf "%.1f %.1f",$3/1048576,$2/1048576}'); ` +
	`printf "MODE:linux|HOST:%s|OS:%s|KERNEL:%s|UPTIME:%s|LOAD:%s|CORES:%s|CPURAW:%s|MEM:%s|SWAP:%s|DISK:%s\n" ` +
	`"$host" "$osname" "$kernel" "$uptime" "$load" "$cores" "$cpuraw" "$mem" "$swap" "$disk"`

const darwinOverview = `host=$(hostname 2>/dev/null || uname -n 2>/dev/null); ` +
	`osname=$(printf "%s %s" "$(sw_vers -productName 2>/dev/null)" "$(sw_vers -productVersion 2>/dev/null)" | sed 's/[[:space:]]*$//'); ` +
	`kernel=$(uname -r 2>/dev/null); ` +
	`bootsec=$(sysctl -n kern.boottime 2>/dev/null | awk -F'[ ,]+' '{for(i=1;i<=NF;i++){if($i=="sec"){print $(i+2);exit}}}'); ` +
	`nowsec=$(date +%s 2>/dev/null || echo "0"); ` +
	`uptime=$(awk -v n="$nowsec" -v b="$bootsec" 'BEGIN{if(n>0&&b>0)printf "%.0f",n-b}'); ` +
	`load=$(sysctl -n vm.loadavg 2>/dev/null | tr -d '{}'); ` +
	`cores=$(sysctl -n hw.logicalcpu 2>/dev/null || echo "1"); ` +
	`cpuraw=$(ps -A -o %cpu= 2>/dev/null | awk '{s+=$1} END{printf "%.0f",s}'); ` +
	`pagesize=$(sysctl -n hw.pagesize 2>/dev/null || echo "4096"); ` +
	`memsize=$(sysctl -n hw.memsize 2>/dev/null || echo "0"); ` +
	`mem=$(vm_stat 2>/dev/null | awk -v ps="$pagesize" -v total="$memsize" '/^Pages free:/{gsub(/[^0-9]/,"",$NF);free=$NF+0} /^Pages speculative:/{gsub(/[^0-9]/,"",$NF);spec=$NF+0} /^Pages inactive:/{gsub(/[^0-9]/,"",$NF);inact=$NF+0} /^Pages purgeable:/{gsub(/[^0-9]/,"",$NF);purg=$NF+0} END{used=int((total-(free+spec+inact+purg)*ps)/1048576); t=int(total/1048576); if(total>0)printf "%d %d",used,t}'); ` +
	`swapraw=$(sysctl vm.swapusage 2>/dev/null); ` +
	`swaptotal=$(echo "$swapraw" | awk '{for(i=1;i<=NF;i++){if($i=="total"&&$(i+1)=="="){v=$(i+2);m=1;if(v~/G/)m=1024;gsub(/[MmGg]/,"",v);st=v*m}};printf "%.0f",st+0}'); ` +
	`swapused=$(echo "$swapraw" | awk '{for(i=1;i<=NF;i++){if($i=="used"&&$(i+1)=="="){v=$(i+2);m=1;if(v~/G/)m=1024;gsub(/[MmGg]/,"",v);su=v*m}};printf "%.0f",su+0}'); ` +
	`swap="$swapused $swaptotal"; ` +
	`disk=$(df -kP / 2>/dev/null | awk 'NR==2{printf "%.1f %.1f",$3/1048576,$2/1048576}'); ` +
	`printf "MODE:darwin|HOST:%s|OS:%s|KERNEL:%s|UPTIME:%s|LOAD:%s|CORES:%s|CPURAW:%s|MEM:%s|SWAP:%s|DISK:%s\n" ` +
	`"$host" "$osname" "$kernel" "$uptime" "$load" "$cores" "$cpuraw" "$mem" "$swap" "$disk"`

func commandFor(tab Tab) string {
	switch tab {
	case TabProcesses:
		return processesCommand()
	case TabPorts:
		return portsCommand()
	case TabServices:
		return servicesCommand()
	default:
		return overviewCommand()
	}
}

// allowedSignals is the original's set. Numeric equivalents are normalized
// to their names before the command is built.
var allowedSignals = map[string]string{
	"TERM": "TERM", "KILL": "KILL", "STOP": "STOP", "CONT": "CONT",
	"HUP": "HUP", "INT": "INT", "USR1": "USR1", "USR2": "USR2",
	"1": "HUP", "2": "INT", "9": "KILL", "15": "TERM", "18": "CONT", "19": "STOP",
}

// SignalCommand validates pid and signal and returns the exact command the
// confirmation names and the service runs.
func SignalCommand(pid int, signal string) (string, error) {
	if pid <= 0 {
		return "", &Error{Reason: ReasonInternal, Cause: fmt.Errorf("invalid pid: %d", pid)}
	}
	name, ok := allowedSignals[strings.ToUpper(strings.TrimSpace(signal))]
	if !ok {
		return "", &Error{Reason: ReasonInternal,
			Cause: fmt.Errorf("invalid signal: %q", signal)}
	}
	if name == "KILL" {
		return "kill -9 " + strconv.Itoa(pid), nil
	}
	if name == "TERM" {
		return "kill -15 " + strconv.Itoa(pid), nil
	}
	return "kill -s " + name + " " + strconv.Itoa(pid), nil
}

// sanitizeUnitName accepts the original's systemd unit character set.
func sanitizeUnitName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > 256 {
		return "", &Error{Reason: ReasonInternal, Cause: fmt.Errorf("invalid unit name: %q", name)}
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == ':', r == '.', r == '_', r == '@', r == '-':
		default:
			return "", &Error{Reason: ReasonInternal,
				Cause: fmt.Errorf("invalid unit name: %q", name)}
		}
	}
	return trimmed, nil
}

// ServiceCommand validates a unit action and returns the exact command the
// confirmation names and the service runs.
func ServiceCommand(unit string, action ServiceAction, scope Scope) (string, error) {
	name, err := sanitizeUnitName(unit)
	if err != nil {
		return "", err
	}
	switch action {
	case ServiceStart, ServiceStop, ServiceRestart:
	default:
		return "", &Error{Reason: ReasonInternal,
			Cause: fmt.Errorf("invalid service action: %q", action)}
	}
	scopeFlag := ""
	if scope == ScopeUser {
		scopeFlag = "--user "
	}
	return "systemctl " + scopeFlag + string(action) + " " + shellQuote(name), nil
}

// shellQuote single-quotes a value for the remote POSIX shell.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
