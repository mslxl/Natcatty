package sysmanager

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/hoststats"
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

// probeCommand reports the remote OS and which commands exist. The core list
// stays one line so ticket 45's surface keeps its probe contract; the extended
// tools follow so a missing tmux, Docker, or accelerator collector is a typed
// capability rather than a failed command.
func probeCommand() string {
	return `printf "` + markerOS + `=%s\n" "$(uname -s 2>/dev/null || echo Unknown)"; ` +
		`for c in ps top ss netstat lsof systemctl; do ` +
		`if command -v "$c" >/dev/null 2>&1; then printf "` + markerHasPrefix + `%s=1\n" "$c"; fi; done; ` +
		`for c in tmux docker nvidia-smi npu-smi; do ` +
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

// overviewCommand returns the shared Host Stats Sample command. The
// monitoring surface sends the same template, so the Overview and the monitor
// read one command and one parser.
func overviewCommand() string { return hoststats.StatsCommand() }

// tmuxSessionsCommand lists sessions with the original's tab-separated
// fields, falling back to the plain list on an older tmux. A missing server
// is a normal empty list, so its diagnostic lines are allowed through and the
// parser drops them; only a missing tmux becomes a typed reason.
func tmuxSessionsCommand() string {
	// tmux does not expand \t inside -F, so shell printf turns the separators
	// into real tabs before tmux sees the format.
	return `printf "%s\n" "__NC_TMUX_BEGIN__"; ` +
		`if command -v tmux >/dev/null 2>&1; then ` +
		`_nc_format=$(printf '#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_created}'); ` +
		`tmux list-sessions -F "$_nc_format" 2>&1 ` +
		`|| tmux list-sessions 2>&1 || true; ` +
		`else printf "` + markerUnavailable + `=tmux\n"; fi; ` +
		`printf "%s\n" "__NC_TMUX_END__"`
}

// dockerListCommand lists containers and images as JSON lines. Each docker
// call's failure text is captured into its own marker section, so permission
// denied, a stopped daemon, and an ordinary error become distinct typed
// reasons instead of an empty live list.
func dockerListCommand() string {
	return `printf "%s\n" "__NC_DOCKER_BEGIN__"; ` +
		`if command -v docker >/dev/null 2>&1; then ` +
		`printf "%s\n" "__NC_DOCKER_CONTAINERS__"; ` +
		`_nc_out=$(docker ps -a --format '{{json .}}' 2>&1); _nc_status=$?; ` +
		`if [ "$_nc_status" -eq 0 ]; then printf "%s\n" "$_nc_out"; ` +
		`else printf "%s\n" "__NC_DOCKER_ERROR_BEGIN__"; printf "%s\n" "$_nc_out"; ` +
		`printf "%s\n" "__NC_DOCKER_ERROR_END__"; fi; ` +
		`printf "%s\n" "__NC_DOCKER_IMAGES__"; ` +
		`_nc_out=$(docker images --format '{{json .}}' 2>&1); _nc_status=$?; ` +
		`if [ "$_nc_status" -eq 0 ]; then printf "%s\n" "$_nc_out"; ` +
		`else printf "%s\n" "__NC_DOCKER_ERROR_BEGIN__"; printf "%s\n" "$_nc_out"; ` +
		`printf "%s\n" "__NC_DOCKER_ERROR_END__"; fi; ` +
		`else printf "` + markerUnavailable + `=docker\n"; fi; ` +
		`printf "%s\n" "__NC_DOCKER_END__"`
}

// gpuCommand queries every NVIDIA device and its driver version. A Host
// without the tool prints no device lines, which the parser reports as the
// normal empty state.
func gpuCommand() string {
	return `printf "%s\n" "__NC_GPU_BEGIN__"; ` +
		`if command -v nvidia-smi >/dev/null 2>&1; then ` +
		`printf "%s\n" "__NC_NVIDIA__"; ` +
		`nvidia-smi --query-gpu=index,uuid,name,utilization.gpu,memory.used,memory.total,` +
		`temperature.gpu,power.draw,power.limit,fan.speed,driver_version ` +
		`--format=csv,noheader,nounits 2>/dev/null || true; ` +
		`fi; ` +
		`printf "%s\n" "__NC_GPU_END__"`
}

func commandFor(tab Tab) string {
	switch tab {
	case TabProcesses:
		return processesCommand()
	case TabPorts:
		return portsCommand()
	case TabServices:
		return servicesCommand()
	case TabTmux:
		return tmuxSessionsCommand()
	case TabDocker:
		return dockerListCommand()
	case TabGPU:
		return gpuCommand()
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

// sanitizeTmuxSessionName accepts the original's length bound and rejects the
// characters tmux uses to separate a session target from a window and pane.
func sanitizeTmuxSessionName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > 64 {
		return "", &Error{Reason: ReasonInternal, Cause: fmt.Errorf("invalid session name: %q", name)}
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f || r == ':' || r == '.' {
			return "", &Error{Reason: ReasonInternal,
				Cause: fmt.Errorf("invalid session name: %q", name)}
		}
	}
	return trimmed, nil
}

// TmuxCreateCommand validates name and returns the detached new-session
// command the surface runs.
func TmuxCreateCommand(name string) (string, error) {
	session, err := sanitizeTmuxSessionName(name)
	if err != nil {
		return "", err
	}
	return "tmux new-session -d -s " + shellQuote(session), nil
}

// TmuxKillCommand validates name and returns the kill-session command.
func TmuxKillCommand(name string) (string, error) {
	session, err := sanitizeTmuxSessionName(name)
	if err != nil {
		return "", err
	}
	return "tmux kill-session -t " + shellQuote(session), nil
}

// TmuxAttachCommand returns the interactive command sent to a terminal Tab.
// The clear sequence and exec match the original so the attached client owns
// the terminal without a shell prompt above it.
func TmuxAttachCommand(name string) (string, error) {
	session, err := sanitizeTmuxSessionName(name)
	if err != nil {
		return "", err
	}
	return `printf '\033[H\033[2J\033[3J'; exec tmux attach -t ` + shellQuote(session), nil
}

// DockerAction is one confirmed container action.
type DockerAction string

const (
	DockerStart  DockerAction = "start"
	DockerStop   DockerAction = "stop"
	DockerRemove DockerAction = "remove"
)

// sanitizeDockerID accepts the original's container-id character set.
func sanitizeDockerID(id string) (string, error) {
	var builder strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			if builder.Len() == 64 {
				break
			}
		}
	}
	safe := builder.String()
	if safe == "" {
		return "", &Error{Reason: ReasonInternal, Cause: fmt.Errorf("invalid container id: %q", id)}
	}
	return safe, nil
}

// DockerActionCommand validates the target and action and returns the exact
// command the confirmation names and the service runs.
func DockerActionCommand(id string, action DockerAction) (string, error) {
	safe, err := sanitizeDockerID(id)
	if err != nil {
		return "", err
	}
	switch action {
	case DockerStart:
		return "docker start " + safe, nil
	case DockerStop:
		return "docker stop " + safe, nil
	case DockerRemove:
		// The original's remove is a forced removal; a stopped or running
		// container leaves the same way.
		return "docker rm -f " + safe, nil
	default:
		return "", &Error{Reason: ReasonInternal,
			Cause: fmt.Errorf("invalid container action: %q", action)}
	}
}

// DockerInspectCommand returns the inspect command for id.
func DockerInspectCommand(id string) (string, error) {
	safe, err := sanitizeDockerID(id)
	if err != nil {
		return "", err
	}
	return "docker inspect " + safe, nil
}

// MaxDockerLogTail is the largest bounded log tail the surface may request.
const MaxDockerLogTail = 1000

// DefaultDockerLogTail matches the original's `--tail 200`.
const DefaultDockerLogTail = 200

// DockerLogsCommand returns a bounded, non-following logs command.
func DockerLogsCommand(id string, tail int) (string, error) {
	safe, err := sanitizeDockerID(id)
	if err != nil {
		return "", err
	}
	if tail <= 0 {
		tail = DefaultDockerLogTail
	}
	if tail > MaxDockerLogTail {
		tail = MaxDockerLogTail
	}
	return "docker logs --tail " + strconv.Itoa(tail) + " " + safe, nil
}
