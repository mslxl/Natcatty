package sysmanager

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestParseCapabilitiesReadsTheExtendedTools(t *testing.T) {
	stdout := "__NC_OS=Linux\n__NC_HAS_ps=1\n__NC_HAS_tmux=1\n" +
		"__NC_HAS_docker=1\n__NC_HAS_nvidia-smi=1\n"
	caps := parseCapabilities(stdout)
	if !caps.HasTmux || !caps.HasDocker || !caps.HasNvidiaSmi {
		t.Fatalf("capabilities = %+v, want tmux, docker, and nvidia-smi", caps)
	}
	if caps.HasNpuSmi {
		t.Errorf("capabilities invented npu-smi: %+v", caps)
	}
	for _, tab := range []Tab{TabTmux, TabDocker, TabGPU} {
		if ok, reason := caps.Available(tab); !ok || reason != ReasonNone {
			t.Errorf("%s availability = %v, %q", tab, ok, reason)
		}
	}

	bare := parseCapabilities("__NC_OS=Linux\n")
	if ok, reason := bare.Available(TabTmux); ok || reason != ReasonMissingTmux {
		t.Errorf("tmux availability = %v, %q", ok, reason)
	}
	if ok, reason := bare.Available(TabDocker); ok || reason != ReasonMissingDocker {
		t.Errorf("docker availability = %v, %q", ok, reason)
	}
	// A Host without an accelerator tool is the normal empty GPU state, not
	// an unavailable tab.
	if ok, reason := bare.Available(TabGPU); !ok || reason != ReasonNone {
		t.Errorf("gpu availability = %v, %q", ok, reason)
	}
	if ok, reason := (Capabilities{OS: "freebsd"}).Available(TabGPU); ok || reason != ReasonOS {
		t.Errorf("gpu on an unsupported OS = %v, %q", ok, reason)
	}
}

func TestParseTmuxSessionsReadsFormattedAndPlainLists(t *testing.T) {
	formatted := "__NC_TMUX_BEGIN__\n" +
		"work\t3\t1\t1700000000\n" +
		"idle\t1\t0\t1700000100\n" +
		"error connecting to /tmp/tmux-1000/default (No such file or directory)\n" +
		"__NC_TMUX_END__\n"
	sessions, err := parseTmuxSessions(formatted)
	if err != nil {
		t.Fatalf("parse formatted tmux list: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2: %+v", len(sessions), sessions)
	}
	if sessions[0].Name != "work" || sessions[0].Windows != 3 ||
		!sessions[0].Attached || sessions[0].Created != 1700000000 {
		t.Errorf("first session = %+v", sessions[0])
	}
	if sessions[1].Name != "idle" || sessions[1].Windows != 1 ||
		sessions[1].Attached || sessions[1].Created != 1700000100 {
		t.Errorf("second session = %+v", sessions[1])
	}

	// Some tmux builds print the format's tabs as a literal backslash-t.
	literal := "__NC_TMUX_BEGIN__\n" +
		`work\t2\t1\t1700000000` + "\n" +
		"__NC_TMUX_END__\n"
	sessions, err = parseTmuxSessions(literal)
	if err != nil {
		t.Fatalf("parse literal tab separators: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Name != "work" ||
		sessions[0].Windows != 2 || !sessions[0].Attached {
		t.Errorf("literal tab session = %+v", sessions)
	}

	plain := "no server running on /tmp/tmux-1000/default\n" +
		"dev: 2 windows (created Mon Oct  1 10:00:00 2025) (attached)\n" +
		"ops: 1 windows (created Mon Oct  1 11:00:00 2025)\n"
	sessions, err = parseTmuxSessions(plain)
	if err != nil {
		t.Fatalf("parse plain tmux list: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("plain sessions = %+v", sessions)
	}
	if sessions[0].Name != "dev" || sessions[0].Windows != 2 || !sessions[0].Attached {
		t.Errorf("plain attached session = %+v", sessions[0])
	}
	if sessions[1].Name != "ops" || sessions[1].Attached {
		t.Errorf("plain detached session = %+v", sessions[1])
	}

	sessions, err = parseTmuxSessions("no server running on /tmp/tmux-1000/default\n")
	if err != nil || len(sessions) != 0 {
		t.Errorf("a stopped server = %+v, %v; want the empty live state", sessions, err)
	}
	if _, err := parseTmuxSessions("__NC_UNAVAILABLE=tmux\n"); err == nil {
		t.Error("the tmux marker produced no typed error")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Reason != ReasonMissingTmux {
			t.Errorf("missing tmux error = %v", err)
		}
	}
}

const fakeDockerList = "__NC_DOCKER_BEGIN__\n" +
	"__NC_DOCKER_CONTAINERS__\n" +
	`{"ID":"3f2a1b","Names":"/web","Image":"nginx:1.27","State":"running","Status":"Up 2 hours","Ports":"0.0.0.0:80->80/tcp","CreatedAt":"2025-10-01 10:00:00 +0000 UTC"}` + "\n" +
	`{"ID":"9c8d7e","Names":"/db","Image":"postgres:16","State":"exited","Status":"Exited (0) 1 hour ago","Ports":"","CreatedAt":"2025-10-01 09:00:00 +0000 UTC"}` + "\n" +
	"__NC_DOCKER_IMAGES__\n" +
	`{"ID":"sha256:aa","Repository":"nginx","Tag":"1.27","Size":"192MB","CreatedAt":"2025-09-01 10:00:00 +0000 UTC","Digest":"sha256:bb"}` + "\n" +
	`{"ID":"sha256:cc","Repository":"<none>","Tag":"<none>","Size":"1.2GB","CreatedAt":"2025-08-01 10:00:00 +0000 UTC","Digest":"sha256:dd"}` + "\n" +
	"__NC_DOCKER_END__\n"

func TestParseDockerListReadsContainersAndImages(t *testing.T) {
	containers, images, err := parseDockerList(fakeDockerList)
	if err != nil {
		t.Fatalf("parse docker list: %v", err)
	}
	if len(containers) != 2 || len(images) != 2 {
		t.Fatalf("containers = %d, images = %d", len(containers), len(images))
	}
	web := containers[0]
	if web.ID != "3f2a1b" || web.Name != "web" || web.Image != "nginx:1.27" ||
		web.State != "running" || web.Ports != "0.0.0.0:80->80/tcp" {
		t.Errorf("web container = %+v", web)
	}
	if containers[1].Name != "db" || containers[1].State != "exited" {
		t.Errorf("db container = %+v", containers[1])
	}
	if images[0].ID != "sha256:aa" || images[0].Name() != "nginx:1.27" ||
		images[0].Size != "192MB" || images[0].Digest != "sha256:bb" {
		t.Errorf("nginx image = %+v", images[0])
	}
	if got := images[1].Name(); got != "sha256:cc" {
		t.Errorf("unnamed image display name = %q, want its id", got)
	}

	empty, _, err := parseDockerList("__NC_DOCKER_BEGIN__\n__NC_DOCKER_CONTAINERS__\n" +
		"__NC_DOCKER_IMAGES__\n__NC_DOCKER_END__\n")
	if err != nil || len(empty) != 0 {
		t.Errorf("empty docker list = %+v, %v", empty, err)
	}
}

func TestParseDockerListMapsFailuresToTypedReasons(t *testing.T) {
	tests := []struct {
		name   string
		detail string
		want   Reason
	}{
		{"permission",
			"permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock",
			ReasonPermission},
		{"daemon",
			"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
			ReasonDockerDaemon},
		{"missing", "docker: command not found", ReasonMissingDocker},
	}
	for _, test := range tests {
		stdout := "__NC_DOCKER_BEGIN__\n__NC_DOCKER_CONTAINERS__\n" +
			"__NC_DOCKER_ERROR_BEGIN__\n" + test.detail + "\n__NC_DOCKER_ERROR_END__\n" +
			"__NC_DOCKER_IMAGES__\n__NC_DOCKER_ERROR_BEGIN__\n" + test.detail +
			"\n__NC_DOCKER_ERROR_END__\n__NC_DOCKER_END__\n"
		_, _, err := parseDockerList(stdout)
		var typed *Error
		if !errors.As(err, &typed) || typed.Reason != test.want {
			t.Errorf("%s error = %v, want reason %q", test.name, err, test.want)
		}
	}

	_, _, err := parseDockerList("__NC_UNAVAILABLE=docker\n")
	var typed *Error
	if !errors.As(err, &typed) || typed.Reason != ReasonMissingDocker {
		t.Errorf("docker marker error = %v", err)
	}
}

func TestParseGPUReadsDevicesAndKeepsUnreportedMetricsNaN(t *testing.T) {
	stdout := "__NC_GPU_BEGIN__\n__NC_NVIDIA__\n" +
		"1, GPU-2222, NVIDIA RTX 4090, 87, 18234, 24564, 72, 310.5, 450.0, 65, 560.35.03\n" +
		"0, GPU-1111, NVIDIA A100, 0, 512, 40960, 41, 28.0, 300.0, [N/A], 560.35.03\n" +
		"__NC_GPU_END__\n"
	info, err := parseGPU(stdout)
	if err != nil {
		t.Fatalf("parse gpu: %v", err)
	}
	if len(info.Devices) != 2 {
		t.Fatalf("devices = %d, want 2: %+v", len(info.Devices), info.Devices)
	}
	if info.Devices[0].Index != 0 || info.Devices[0].Name != "NVIDIA A100" {
		t.Errorf("devices are not sorted by index: %+v", info.Devices)
	}
	first := info.Devices[0]
	if first.Vendor != "nvidia" || first.UUID != "GPU-1111" ||
		first.MemoryUsedMB != 512 || first.MemoryTotalMB != 40960 ||
		first.TemperatureC != 41 || first.PowerDrawW != 28 {
		t.Errorf("A100 device = %+v", first)
	}
	if !math.IsNaN(first.FanPercent) {
		t.Errorf("unreported fan = %v, want NaN", first.FanPercent)
	}
	if info.DriverVersion != "560.35.03" {
		t.Errorf("driver version = %q", info.DriverVersion)
	}
	second := info.Devices[1]
	if second.UtilizationPercent != 87 || second.MemoryUsedMB != 18234 ||
		second.PowerLimitW != 450 || second.FanPercent != 65 {
		t.Errorf("RTX device = %+v", second)
	}

	// A Host without nvidia-smi emits the empty sections; that is the normal
	// empty state, never an error.
	info, err = parseGPU("__NC_GPU_BEGIN__\n__NC_GPU_END__\n")
	if err != nil || len(info.Devices) != 0 || info.DriverVersion != "" {
		t.Errorf("empty gpu = %+v, %v", info, err)
	}
	if info.Devices == nil {
		t.Error("the empty gpu state must be an empty slice, not nil")
	}
}

func TestExtendedCommandsUseFixedTemplates(t *testing.T) {
	if !strings.Contains(probeCommand(), "tmux") ||
		!strings.Contains(probeCommand(), "docker") ||
		!strings.Contains(probeCommand(), "nvidia-smi") {
		t.Errorf("probe command = %q", probeCommand())
	}
	if !strings.Contains(tmuxSessionsCommand(), "list-sessions") ||
		!strings.Contains(tmuxSessionsCommand(), "#{session_name}") ||
		!strings.Contains(tmuxSessionsCommand(), markerUnavailable) {
		t.Errorf("tmux command = %q", tmuxSessionsCommand())
	}
	if !strings.Contains(tmuxSessionsCommand(), `_nc_format=$(printf '#{session_name}\t`) {
		t.Errorf("tmux format does not route its tabs through printf: %q",
			tmuxSessionsCommand())
	}
	if !strings.Contains(dockerListCommand(), "docker ps -a") ||
		!strings.Contains(dockerListCommand(), "docker images") ||
		!strings.Contains(dockerListCommand(), "__NC_DOCKER_ERROR_BEGIN__") {
		t.Errorf("docker command = %q", dockerListCommand())
	}
	if !strings.Contains(gpuCommand(), "nvidia-smi --query-gpu=") ||
		!strings.Contains(gpuCommand(), "driver_version") {
		t.Errorf("gpu command = %q", gpuCommand())
	}
	for tab, want := range map[Tab]string{
		TabTmux:   "__NC_TMUX_BEGIN__",
		TabDocker: "__NC_DOCKER_BEGIN__",
		TabGPU:    "__NC_GPU_BEGIN__",
	} {
		if got := commandFor(tab); !strings.Contains(got, want) {
			t.Errorf("commandFor(%s) = %q", tab, got)
		}
	}
}

func TestDefaultIntervalCoversExtendedTabs(t *testing.T) {
	if got := DefaultInterval(TabDocker); got != 5*time.Second {
		t.Errorf("docker interval = %s", got)
	}
	for _, tab := range []Tab{TabTmux, TabGPU} {
		if got := DefaultInterval(tab); got != 3*time.Second {
			t.Errorf("%s interval = %s", tab, got)
		}
	}
}

func TestTmuxCommandsValidateTheirSessionNames(t *testing.T) {
	create, err := TmuxCreateCommand("work")
	if err != nil || create != "tmux new-session -d -s 'work'" {
		t.Errorf("create = %q, %v", create, err)
	}
	kill, err := TmuxKillCommand("work")
	if err != nil || kill != "tmux kill-session -t 'work'" {
		t.Errorf("kill = %q, %v", kill, err)
	}
	attach, err := TmuxAttachCommand("work")
	if err != nil || !strings.Contains(attach, "exec tmux attach -t 'work'") {
		t.Errorf("attach = %q, %v", attach, err)
	}

	for _, bad := range []string{"", "   ", "a:b", "a.b", "a\nb", strings.Repeat("x", 65)} {
		if _, err := TmuxCreateCommand(bad); err == nil {
			t.Errorf("TmuxCreateCommand accepted %q", bad)
		}
		if _, err := TmuxKillCommand(bad); err == nil {
			t.Errorf("TmuxKillCommand accepted %q", bad)
		}
		if _, err := TmuxAttachCommand(bad); err == nil {
			t.Errorf("TmuxAttachCommand accepted %q", bad)
		}
	}
	quoted, err := TmuxKillCommand("it's")
	if err != nil || quoted != `tmux kill-session -t 'it'\''s'` {
		t.Errorf("quoted name = %q, %v", quoted, err)
	}
}

func TestDockerCommandsValidateTargetsAndBoundTheTail(t *testing.T) {
	tests := []struct {
		action DockerAction
		want   string
	}{
		{DockerStart, "docker start 3f2a1b"},
		{DockerStop, "docker stop 3f2a1b"},
		{DockerRemove, "docker rm -f 3f2a1b"},
	}
	for _, test := range tests {
		got, err := DockerActionCommand("3f2a1b", test.action)
		if err != nil || got != test.want {
			t.Errorf("DockerActionCommand(%s) = %q, %v", test.action, got, err)
		}
	}
	if _, err := DockerActionCommand("", DockerStart); err == nil {
		t.Error("an empty container id was accepted")
	}
	if _, err := DockerActionCommand("3f2a1b", "pause"); err == nil {
		t.Error("an unsupported container action was accepted")
	}
	if got, err := DockerInspectCommand("3f2a1b"); err != nil || got != "docker inspect 3f2a1b" {
		t.Errorf("inspect = %q, %v", got, err)
	}

	tailTests := []struct {
		in   int
		want string
	}{
		{0, "docker logs --tail 200 3f2a1b"},
		{-5, "docker logs --tail 200 3f2a1b"},
		{50, "docker logs --tail 50 3f2a1b"},
		{MaxDockerLogTail + 1, "docker logs --tail 1000 3f2a1b"},
	}
	for _, test := range tailTests {
		got, err := DockerLogsCommand("3f2a1b", test.in)
		if err != nil || got != test.want {
			t.Errorf("DockerLogsCommand(%d) = %q, %v; want %q", test.in, got, err, test.want)
		}
	}
}

func TestTmuxAndDockerActionsRunOnlyTheirConfirmedCommand(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		return ExecResult{Stdout: "ok", HasExitCode: true}, nil
	}
	service := NewService(connector)
	host := fakeHost("extended-actions")

	if _, err := service.TmuxCreate(context.Background(), host, "work"); err != nil {
		t.Fatalf("tmux create: %v", err)
	}
	if _, err := service.TmuxKill(context.Background(), host, "old"); err != nil {
		t.Fatalf("tmux kill: %v", err)
	}
	if _, err := service.DockerContainerAction(context.Background(), host, "3f2a1b", DockerStart); err != nil {
		t.Fatalf("docker start: %v", err)
	}
	if _, err := service.DockerContainerAction(context.Background(), host, "3f2a1b", DockerStop); err != nil {
		t.Fatalf("docker stop: %v", err)
	}
	if _, err := service.DockerContainerAction(context.Background(), host, "3f2a1b", DockerRemove); err != nil {
		t.Fatalf("docker remove: %v", err)
	}
	want := []string{
		"tmux new-session -d -s 'work'",
		"tmux kill-session -t 'old'",
		"docker start 3f2a1b",
		"docker stop 3f2a1b",
		"docker rm -f 3f2a1b",
	}
	commands := connector.commands("extended-actions")
	if len(commands) != len(want) {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, commands[i], want[i])
		}
	}

	// An invalid target or action never reaches the transport.
	before := len(connector.commands("extended-actions"))
	if _, err := service.TmuxKill(context.Background(), host, "bad:name"); err == nil {
		t.Error("an invalid session name was accepted")
	}
	if _, err := service.DockerContainerAction(context.Background(), host, "", DockerStop); err == nil {
		t.Error("an empty container id was accepted")
	}
	if _, err := service.DockerInspect(context.Background(), host, ""); err == nil {
		t.Error("an empty inspect id was accepted")
	}
	if _, err := service.DockerLogs(context.Background(), host, "", 10); err == nil {
		t.Error("an empty logs id was accepted")
	}
	if after := len(connector.commands("extended-actions")); after != before {
		t.Errorf("invalid input ran %d command(s)", after-before)
	}
}

func TestDockerInspectAndLogsReturnBoundedDocuments(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		switch {
		case strings.HasPrefix(command, "docker inspect"):
			return ExecResult{Stdout: `{"Id":"3f2a1b","State":{"Running":true}}`}, nil
		case strings.HasPrefix(command, "docker logs"):
			return ExecResult{Stdout: "stdout line\n", Stderr: "stderr line\n"}, nil
		default:
			return ExecResult{HasExitCode: true}, nil
		}
	}
	service := NewService(connector)
	host := fakeHost("documents")

	document, err := service.DockerInspect(context.Background(), host, "3f2a1b")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if document.Kind != DocumentInspect || document.ContainerID != "3f2a1b" ||
		document.Truncated {
		t.Errorf("inspect document = %+v", document)
	}
	if !strings.Contains(document.Text, "\n  \"Id\": \"3f2a1b\"") {
		t.Errorf("inspect text was not indented: %q", document.Text)
	}

	logs, err := service.DockerLogs(context.Background(), host, "3f2a1b", 50)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if logs.Kind != DocumentLogs || !strings.Contains(logs.Text, "stdout line") ||
		!strings.Contains(logs.Text, "stderr line") {
		t.Errorf("logs document = %+v", logs)
	}

	// A document larger than the budget is truncated on a rune boundary and
	// flagged for the surface.
	huge := strings.Repeat("界", maxDockerDocumentBytes)
	connector.exec = func(_ domainvault.Host, _ string) (ExecResult, error) {
		return ExecResult{Stdout: huge}, nil
	}
	document, err = service.DockerLogs(context.Background(), host, "3f2a1b", 1000)
	if err != nil {
		t.Fatalf("huge logs: %v", err)
	}
	if !document.Truncated || !strings.HasSuffix(document.Text, "…") {
		t.Errorf("huge document was not truncated: truncated=%v suffix=%q",
			document.Truncated, tailOf(document.Text, 8))
	}
	if len(document.Text) > maxDockerDocumentBytes+len("\n…") {
		t.Errorf("truncated document is %d bytes", len(document.Text))
	}
}

func tailOf(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[len(text)-n:]
}

func TestDockerDocumentsCancelWithTheirContext(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, _ string) (ExecResult, error) {
		return ExecResult{Stdout: "ok"}, nil
	}
	service := NewService(connector)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.DockerInspect(ctx, fakeHost("cancelled"), "3f2a1b"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled inspect error = %v", err)
	}
	if commands := connector.commands("cancelled"); len(commands) != 0 {
		t.Errorf("a cancelled document ran %v", commands)
	}
}

func TestServiceSamplesExtendedTabs(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		switch {
		case command == probeCommand():
			return ExecResult{Stdout: fakeProbeAll +
				"__NC_HAS_tmux=1\n__NC_HAS_docker=1\n__NC_HAS_nvidia-smi=1\n"}, nil
		case strings.Contains(command, "__NC_TMUX_BEGIN__"):
			return ExecResult{Stdout: "__NC_TMUX_BEGIN__\nwork\t2\t1\t1700000000\n__NC_TMUX_END__\n"}, nil
		case strings.Contains(command, "__NC_DOCKER_BEGIN__"):
			return ExecResult{Stdout: fakeDockerList}, nil
		case strings.Contains(command, "__NC_GPU_BEGIN__"):
			return ExecResult{Stdout: "__NC_GPU_BEGIN__\n__NC_NVIDIA__\n" +
				"0, GPU-1111, NVIDIA A100, 12, 512, 40960, 41, 28.0, 300.0, 30, 560.35.03\n" +
				"__NC_GPU_END__\n"}, nil
		default:
			return ExecResult{HasExitCode: true}, nil
		}
	}
	service := NewService(connector)
	defer service.Close()
	host := fakeHost("extended")

	for _, tab := range []Tab{TabTmux, TabDocker, TabGPU} {
		if err := service.Start(host, tab, 20*time.Millisecond); err != nil {
			t.Fatalf("start %s: %v", tab, err)
		}
		waitStatus(t, service, tab, StatusLive)
		state := service.Snapshot().Tabs[tab]
		switch tab {
		case TabTmux:
			if len(state.Sessions) != 1 || state.Sessions[0].Name != "work" ||
				state.Sessions[0].Windows != 2 || !state.Sessions[0].Attached {
				t.Errorf("tmux state = %+v", state.Sessions)
			}
		case TabDocker:
			if len(state.Containers) != 2 || len(state.Images) != 2 {
				t.Errorf("docker state = %+v", state)
			}
		case TabGPU:
			if len(state.GPU.Devices) != 1 ||
				state.GPU.Devices[0].Name != "NVIDIA A100" ||
				state.GPU.DriverVersion != "560.35.03" {
				t.Errorf("gpu state = %+v", state.GPU)
			}
		}
	}
}

func TestServiceDegradesExtendedTabsToTypedUnavailability(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		switch {
		case command == probeCommand():
			return ExecResult{Stdout: "__NC_OS=Linux\n__NC_HAS_ps=1\n"}, nil
		case strings.Contains(command, "PRETTY_NAME"):
			return ExecResult{Stdout: sprintfOverview(1)}, nil
		default:
			return ExecResult{Stdout: "", HasExitCode: true}, nil
		}
	}
	service := NewService(connector)
	defer service.Close()
	host := fakeHost("bare-tools")

	for _, test := range []struct {
		tab    Tab
		reason Reason
	}{
		{TabTmux, ReasonMissingTmux},
		{TabDocker, ReasonMissingDocker},
	} {
		if err := service.Start(host, test.tab, 20*time.Millisecond); err != nil {
			t.Fatalf("start %s: %v", test.tab, err)
		}
		waitStatus(t, service, test.tab, StatusUnavailable)
		state := service.Snapshot().Tabs[test.tab]
		if state.Reason != test.reason {
			t.Errorf("%s reason = %q, want %q", test.tab, state.Reason, test.reason)
		}
	}

	// GPU without a tool is live and empty, not unavailable.
	if err := service.Start(host, TabGPU, 20*time.Millisecond); err != nil {
		t.Fatalf("start gpu: %v", err)
	}
	waitStatus(t, service, TabGPU, StatusLive)
	if devices := service.Snapshot().Tabs[TabGPU].GPU.Devices; len(devices) != 0 {
		t.Errorf("gpu devices = %+v, want empty", devices)
	}

	// The core tabs keep working on the same Host.
	if err := service.Start(host, TabOverview, 20*time.Millisecond); err != nil {
		t.Fatalf("start overview: %v", err)
	}
	waitStatus(t, service, TabOverview, StatusLive)
}

func TestServiceDegradesDockerDaemonFailuresInsideTheTab(t *testing.T) {
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		switch {
		case command == probeCommand():
			return ExecResult{Stdout: fakeProbeAll + "__NC_HAS_docker=1\n"}, nil
		case strings.Contains(command, "__NC_DOCKER_BEGIN__"):
			return ExecResult{Stdout: "__NC_DOCKER_BEGIN__\n__NC_DOCKER_CONTAINERS__\n" +
				"__NC_DOCKER_ERROR_BEGIN__\nCannot connect to the Docker daemon at " +
				"unix:///var/run/docker.sock. Is the docker daemon running?\n" +
				"__NC_DOCKER_ERROR_END__\n__NC_DOCKER_IMAGES__\n__NC_DOCKER_END__\n"}, nil
		case strings.Contains(command, "PRETTY_NAME"):
			return ExecResult{Stdout: sprintfOverview(1)}, nil
		default:
			return ExecResult{HasExitCode: true}, nil
		}
	}
	service := NewService(connector)
	defer service.Close()
	host := fakeHost("daemon-down")

	if err := service.Start(host, TabDocker, 20*time.Millisecond); err != nil {
		t.Fatalf("start docker: %v", err)
	}
	waitStatus(t, service, TabDocker, StatusUnavailable)
	if reason := service.Snapshot().Tabs[TabDocker].Reason; reason != ReasonDockerDaemon {
		t.Errorf("docker reason = %q, want docker_daemon", reason)
	}

	if err := service.Start(host, TabOverview, 20*time.Millisecond); err != nil {
		t.Fatalf("start overview: %v", err)
	}
	waitStatus(t, service, TabOverview, StatusLive)
	if reason := service.Snapshot().Tabs[TabOverview].Reason; reason != ReasonNone {
		t.Errorf("overview reason = %q, want none", reason)
	}
}
