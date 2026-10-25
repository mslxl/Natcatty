//go:build selftest

package app

// Ticket 46 self-test. Press F11, then 4, then 6.
//
// The check runs the real System manager service and surface against an
// in-process fake Host transport:
//
//   - tmux sessions, Docker containers and images, and the NVIDIA device list
//     all sample through the production command templates and parsers;
//   - creating a tmux session runs without confirmation; killing one and
//     starting, stopping, or force-removing a container each need
//     confirmation: the fake records no command before ConfirmPending, and
//     one command after it;
//   - attaching a session delivers the production attach command to a
//     recorded terminal opener;
//   - inspect and a bounded log tail load into the document panel;
//   - a Host without tmux or Docker shows typed unavailable states inside
//     those tabs while its GPU tab stays live and empty;
//   - hiding and reordering tabs persists through the settings seam and is
//     restored by a fresh surface;
//   - tmux, Docker containers, Docker images, and GPU are rendered at
//     1100x720 and 720x480 in English and zh-CN, with sixteen frames captured
//     beside the result file.
//
// Every wait observes service or fake state with a hard deadline; there is no
// sleep.
//
// Result: %TEMP%\natcatty-selftest-46.json and
// %TEMP%\natcatty-selftest-46-shots\*.png.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	uisysmanager "github.com/mslxl/natcatty/ui/sysmanager"
)

const (
	selftest46Ticket  = 46
	selftest46Wait    = 8 * time.Second
	selftest46Rich    = "selftest-46-alpha"
	selftest46Minimal = "selftest-46-minimal"
)

type selftest46 struct {
	prefs     *selftest45Prefs
	hosts     *selftest45Hosts
	connector *selftest46Connector
	service   *appsysmanager.Service
	state     *uisysmanager.State

	activated bool
	attached  *selftest46Attach

	sampleStage    int
	sampleDeadline time.Time

	tmuxStage    int
	tmuxDeadline time.Time
	killBefore   int

	dockerStage    int
	dockerDeadline time.Time
	stopBefore     int

	degradeStage    int
	degradeDeadline time.Time

	layoutStage    int
	layoutDeadline time.Time

	renderTab      int
	renderDeadline time.Time
	directory      string
	captured       int

	cleanupDeadline time.Time
}

type selftest46Attach struct {
	host    string
	command string
	calls   int
	err     error
}

func (t *selftest46) check() selftest.Check {
	t.prefs = &selftest45Prefs{settings: domainpreferences.Defaults()}
	t.hosts = &selftest45Hosts{items: []domainvault.Host{
		{ID: model.HostID(selftest46Rich), Label: "Extended", Hostname: "extended.selftest",
			Group: "selftest"},
		{ID: model.HostID(selftest46Minimal), Label: "Minimal", Hostname: "minimal.selftest",
			Group: "selftest"},
	}}
	t.connector = &selftest46Connector{
		commands: make(map[string][]string),
		minimal:  map[string]bool{selftest46Minimal: true},
	}
	t.service = appsysmanager.NewService(t.connector)
	t.attached = &selftest46Attach{}
	t.state = uisysmanager.NewState(t.service, t.hosts, t.prefs,
		uisysmanager.WithTerminalOpener(func(host domainvault.Host, command string) error {
			t.attached.calls++
			t.attached.host = string(host.ID)
			t.attached.command = command
			return t.attached.err
		}))
	return selftest.Check{
		Ticket: selftest46Ticket,
		Name:   "System manager tmux, Docker, GPU, and tab layout",
		Steps: []selftest.Step{
			{Name: "sample-extended-tabs", Run: t.sampleStep},
			{Name: "tmux-actions", Run: t.tmuxStep},
			{Name: "docker-actions", Run: t.dockerStep},
			{Name: "typed-degradation", Run: t.degradeStep},
			{Name: "tab-layout-persistence", Run: t.layoutStep},
			{Name: "render-and-capture", Run: t.renderStep},
			{Name: "stop-and-release", Run: t.cleanupStep},
		},
	}
}

func (t *selftest46) sampleStep(*ui.Context) selftest.Outcome {
	if !t.activated {
		t.activated = true
		t.state.Activate()
		t.sampleDeadline = time.Now().Add(selftest46Wait)
		return selftest.Pending
	}
	t.state.Flush()
	switch t.sampleStage {
	case 0:
		t.state.SetTab(appsysmanager.TabTmux)
		state := t.state.TabState(appsysmanager.TabTmux)
		if state.Status != appsysmanager.StatusLive || len(state.Sessions) != 2 {
			return pending(t.sampleDeadline, "the tmux tab to show two sessions")
		}
		t.sampleStage = 1
		return selftest.Pending
	case 1:
		t.state.SetTab(appsysmanager.TabDocker)
		state := t.state.TabState(appsysmanager.TabDocker)
		if state.Status != appsysmanager.StatusLive || len(state.Containers) != 2 ||
			len(state.Images) != 2 {
			return pending(t.sampleDeadline, "the Docker tab to show two containers and images")
		}
		t.sampleStage = 2
		return selftest.Pending
	case 2:
		t.state.SetTab(appsysmanager.TabGPU)
		state := t.state.TabState(appsysmanager.TabGPU)
		if state.Status != appsysmanager.StatusLive || len(state.GPU.Devices) != 2 ||
			state.GPU.DriverVersion != "560.35.03" {
			return pending(t.sampleDeadline, "the GPU tab to show both devices")
		}
	}
	t.state.SetTab(appsysmanager.TabTmux)
	sessions := t.state.DisplaySessions()
	if len(sessions) != 2 || sessions[0].Name != "idle" || sessions[1].Name != "work" ||
		sessions[1].Windows != 3 || !sessions[1].Attached || sessions[1].Created != 1700000000 {
		return selftest.Failed(fmt.Errorf("tmux sessions = %+v", sessions))
	}
	images := t.state.DisplayImages()
	if len(images) != 2 || images[0].Name() != "nginx:1.27" ||
		images[1].Name() != "sha256:cc" {
		return selftest.Failed(fmt.Errorf("docker images = %+v", images))
	}
	return selftest.Passed(fmt.Sprintf(
		"tmux showed %d sessions, Docker showed %d containers and %d images, and nvidia-smi reported %d devices on driver %s",
		len(sessions), 2, len(images),
		len(t.state.TabState(appsysmanager.TabGPU).GPU.Devices),
		t.state.TabState(appsysmanager.TabGPU).GPU.DriverVersion),
		"application/sysmanager parsers + ui/sysmanager State")
}

func (t *selftest46) tmuxStep(*ui.Context) selftest.Outcome {
	t.state.Flush()
	switch t.tmuxStage {
	case 0:
		if t.tmuxDeadline.IsZero() {
			t.tmuxDeadline = time.Now().Add(selftest46Wait)
		}
		t.state.SetTab(appsysmanager.TabTmux)
		rows := t.state.DisplaySessions()
		if len(rows) == 0 {
			return pending(t.tmuxDeadline, "a tmux row for the kill action")
		}
		index := -1
		for position, session := range rows {
			if session.Name == "work" {
				index = position
			}
		}
		if index < 0 {
			return selftest.Failed(fmt.Errorf("the work session is missing: %+v", rows))
		}
		t.state.SelectRow(index)
		t.killBefore = t.connector.countPrefix(selftest46Rich, "tmux kill-session")
		if err := t.state.RequestTmuxKill("work"); err != nil {
			return selftest.Failed(fmt.Errorf("request tmux kill: %w", err))
		}
		if pendingAction, ok := t.state.Pending(); !ok ||
			pendingAction.Command != "tmux kill-session -t 'work'" {
			return selftest.Failed(fmt.Errorf("tmux kill pending = %+v", pendingAction))
		}
		if got := t.connector.countPrefix(selftest46Rich, "tmux kill-session"); got != t.killBefore {
			return selftest.Failed(fmt.Errorf("a tmux kill ran before confirmation: %d -> %d",
				t.killBefore, got))
		}
		t.state.CancelPending()
		if got := t.connector.countPrefix(selftest46Rich, "tmux kill-session"); got != t.killBefore {
			return selftest.Failed(fmt.Errorf("cancelling the tmux kill ran a command: %d", got))
		}
		if err := t.state.RequestTmuxKill("work"); err != nil {
			return selftest.Failed(fmt.Errorf("request tmux kill again: %w", err))
		}
		t.state.ConfirmPending()
		t.tmuxDeadline = time.Now().Add(selftest46Wait)
		t.tmuxStage = 1
		return selftest.Pending
	case 1:
		if !t.state.ActionDone() {
			return pending(t.tmuxDeadline, "the confirmed tmux kill to finish")
		}
		if t.state.ActionError() != nil {
			return selftest.Failed(fmt.Errorf("tmux kill failed: %w", t.state.ActionError()))
		}
		if got := t.connector.countPrefix(selftest46Rich, "tmux kill-session"); got != t.killBefore+1 {
			return selftest.Failed(fmt.Errorf("tmux kill commands = %d, want %d",
				got, t.killBefore+1))
		}
		creates := t.connector.countPrefix(selftest46Rich, "tmux new-session")
		t.state.SetTmuxName("selftest")
		if err := t.state.CreateSession(); err != nil {
			return selftest.Failed(fmt.Errorf("create tmux session: %w", err))
		}
		if _, ok := t.state.Pending(); ok {
			return selftest.Failed(errors.New("creating a session asked for confirmation"))
		}
		if got := t.connector.countPrefix(selftest46Rich, "tmux new-session"); got != creates {
			return selftest.Failed(fmt.Errorf("the created session ran before its action goroutine: %d", got))
		}
		t.tmuxDeadline = time.Now().Add(selftest46Wait)
		t.tmuxStage = 2
		return selftest.Pending
	case 2:
		if !t.state.ActionDone() {
			return pending(t.tmuxDeadline, "the created tmux session to finish")
		}
		if t.state.ActionError() != nil {
			return selftest.Failed(fmt.Errorf("tmux create failed: %w", t.state.ActionError()))
		}
		if result, ok := t.state.ActionResult(); !ok ||
			result.Command != "tmux new-session -d -s 'selftest'" {
			return selftest.Failed(fmt.Errorf("tmux create result = %+v", result))
		}
		if t.connector.countPrefix(selftest46Rich, "tmux new-session") != 1 {
			return selftest.Failed(errors.New("the created session did not run exactly once"))
		}
		if t.state.TmuxName() != "" {
			return selftest.Failed(fmt.Errorf("the created name stayed in the field: %q", t.state.TmuxName()))
		}
		if err := t.state.AttachSession("work"); err != nil {
			return selftest.Failed(fmt.Errorf("attach tmux session: %w", err))
		}
		if t.attached.calls != 1 || t.attached.host != selftest46Rich ||
			!strings.Contains(t.attached.command, "exec tmux attach -t 'work'") {
			return selftest.Failed(fmt.Errorf("attach opener = %+v", t.attached))
		}
		t.tmuxStage = 3
	}
	return selftest.Passed(fmt.Sprintf(
		"tmux kill-session ran only after ConfirmPending (%d command), the cancelled confirmation ran nothing, new-session -d ran without confirmation, and attach delivered %q to the terminal opener",
		t.killBefore+1, t.attached.command),
		"ui/sysmanager confirmation gate + application/sysmanager tmux actions")
}

func (t *selftest46) dockerStep(*ui.Context) selftest.Outcome {
	t.state.Flush()
	switch t.dockerStage {
	case 0:
		if t.dockerDeadline.IsZero() {
			t.dockerDeadline = time.Now().Add(selftest46Wait)
		}
		t.state.SetTab(appsysmanager.TabDocker)
		t.state.SetDockerView(0)
		rows := t.state.DisplayContainers()
		index := -1
		for position, container := range rows {
			if container.ID == "3f2a1b" {
				index = position
			}
		}
		if index < 0 {
			return pending(t.dockerDeadline, "the web container to appear")
		}
		t.state.SelectRow(index)
		container, ok := t.state.SelectedContainer()
		if !ok {
			return selftest.Failed(errors.New("the web container could not be selected"))
		}
		t.stopBefore = t.connector.countPrefix(selftest46Rich, "docker stop ")
		if err := t.state.RequestDockerAction(container, appsysmanager.DockerStop); err != nil {
			return selftest.Failed(fmt.Errorf("request docker stop: %w", err))
		}
		if pendingAction, ok := t.state.Pending(); !ok ||
			pendingAction.Command != "docker stop 3f2a1b" {
			return selftest.Failed(fmt.Errorf("docker stop pending = %+v", pendingAction))
		}
		if got := t.connector.countPrefix(selftest46Rich, "docker stop "); got != t.stopBefore {
			return selftest.Failed(fmt.Errorf("a docker stop ran before confirmation: %d", got))
		}
		t.state.CancelPending()
		if got := t.connector.countPrefix(selftest46Rich, "docker stop "); got != t.stopBefore {
			return selftest.Failed(fmt.Errorf("cancelling the docker stop ran a command: %d", got))
		}
		if err := t.state.RequestDockerAction(container, appsysmanager.DockerStop); err != nil {
			return selftest.Failed(fmt.Errorf("request docker stop again: %w", err))
		}
		t.state.ConfirmPending()
		t.dockerDeadline = time.Now().Add(selftest46Wait)
		t.dockerStage = 1
		return selftest.Pending
	case 1:
		if !t.state.ActionDone() {
			return pending(t.dockerDeadline, "the confirmed docker stop to finish")
		}
		if t.state.ActionError() != nil {
			return selftest.Failed(fmt.Errorf("docker stop failed: %w", t.state.ActionError()))
		}
		if got := t.connector.countPrefix(selftest46Rich, "docker stop "); got != t.stopBefore+1 {
			return selftest.Failed(fmt.Errorf("docker stop commands = %d, want %d",
				got, t.stopBefore+1))
		}
		container := appsysmanager.Container{ID: "3f2a1b", Name: "web"}
		if err := t.state.RequestDockerAction(container, appsysmanager.DockerRemove); err != nil {
			return selftest.Failed(fmt.Errorf("request docker remove: %w", err))
		}
		if _, ok := t.state.Pending(); !ok {
			return selftest.Failed(errors.New("docker remove asked for no confirmation"))
		}
		t.state.ConfirmPending()
		t.dockerDeadline = time.Now().Add(selftest46Wait)
		t.dockerStage = 2
		return selftest.Pending
	case 2:
		if !t.state.ActionDone() {
			return pending(t.dockerDeadline, "the confirmed docker remove to finish")
		}
		if t.state.ActionError() != nil {
			return selftest.Failed(fmt.Errorf("docker remove failed: %w", t.state.ActionError()))
		}
		if got := t.connector.countPrefix(selftest46Rich, "docker rm -f "); got != 1 {
			return selftest.Failed(fmt.Errorf("docker remove commands = %d, want 1", got))
		}
		container := appsysmanager.Container{ID: "3f2a1b", Name: "web"}
		if err := t.state.InspectContainer(container); err != nil {
			return selftest.Failed(fmt.Errorf("inspect container: %w", err))
		}
		t.dockerDeadline = time.Now().Add(selftest46Wait)
		t.dockerStage = 3
		return selftest.Pending
	case 3:
		if t.state.DocumentBusy() {
			return pending(t.dockerDeadline, "the inspect document")
		}
		document, open := t.state.Document()
		if !open || document.Kind != appsysmanager.DocumentInspect ||
			!strings.Contains(document.Text, "\"Id\": \"3f2a1b\"") {
			return selftest.Failed(fmt.Errorf("inspect document = %+v, %v", document, open))
		}
		if t.state.DocumentError() != nil {
			return selftest.Failed(fmt.Errorf("inspect error: %w", t.state.DocumentError()))
		}
		if err := t.state.ContainerLogs(appsysmanager.Container{ID: "3f2a1b", Name: "web"}); err != nil {
			return selftest.Failed(fmt.Errorf("container logs: %w", err))
		}
		t.dockerDeadline = time.Now().Add(selftest46Wait)
		t.dockerStage = 4
		return selftest.Pending
	case 4:
		if t.state.DocumentBusy() {
			return pending(t.dockerDeadline, "the logs document")
		}
		document, open := t.state.Document()
		if !open || document.Kind != appsysmanager.DocumentLogs ||
			!strings.Contains(document.Text, "listening on 0.0.0.0:80") {
			return selftest.Failed(fmt.Errorf("logs document = %+v, %v", document, open))
		}
		if !t.connector.hasCommand(selftest46Rich, "docker logs --tail 200 3f2a1b") {
			return selftest.Failed(errors.New("the bounded log tail command did not run"))
		}
		t.state.CloseDocument()
		t.state.SetDockerView(1)
		t.state.SelectRow(0)
		image, ok := t.state.SelectedImage()
		if !ok || image.Name() != "nginx:1.27" {
			return selftest.Failed(fmt.Errorf("selected image = %+v", image))
		}
		t.dockerStage = 5
	}
	return selftest.Passed(fmt.Sprintf(
		"docker stop and rm -f each ran only after ConfirmPending (stop %d command, rm 1); inspect and the --tail 200 logs loaded, and the image list showed %q",
		t.stopBefore+1, "nginx:1.27"),
		"ui/sysmanager confirmation gate + application/sysmanager Docker actions and documents")
}

func (t *selftest46) degradeStep(*ui.Context) selftest.Outcome {
	t.state.Flush()
	switch t.degradeStage {
	case 0:
		t.state.SetHost(selftest46Minimal)
		t.state.SetTab(appsysmanager.TabTmux)
		t.degradeDeadline = time.Now().Add(selftest46Wait)
		t.degradeStage = 1
		return selftest.Pending
	case 1:
		state := t.state.TabState(appsysmanager.TabTmux)
		if state.Status != appsysmanager.StatusUnavailable ||
			state.Reason != appsysmanager.ReasonMissingTmux {
			return pending(t.degradeDeadline, "tmux to degrade to missing_tmux")
		}
		if t.connector.hasPrefix(selftest46Minimal, "tmux ") {
			return selftest.Failed(errors.New("a Host without tmux ran a tmux command"))
		}
		t.state.SetTab(appsysmanager.TabDocker)
		t.degradeDeadline = time.Now().Add(selftest46Wait)
		t.degradeStage = 2
		return selftest.Pending
	case 2:
		state := t.state.TabState(appsysmanager.TabDocker)
		if state.Status != appsysmanager.StatusUnavailable ||
			state.Reason != appsysmanager.ReasonMissingDocker {
			return pending(t.degradeDeadline, "Docker to degrade to missing_docker")
		}
		if t.connector.hasPrefix(selftest46Minimal, "docker ") {
			return selftest.Failed(errors.New("a Host without Docker ran a docker command"))
		}
		t.state.SetTab(appsysmanager.TabGPU)
		t.degradeDeadline = time.Now().Add(selftest46Wait)
		t.degradeStage = 3
		return selftest.Pending
	case 3:
		state := t.state.TabState(appsysmanager.TabGPU)
		if state.Status != appsysmanager.StatusLive || len(state.GPU.Devices) != 0 {
			return pending(t.degradeDeadline, "the GPU tab to stay live and empty")
		}
		t.state.SetHost(selftest46Rich)
		t.state.SetTab(appsysmanager.TabTmux)
		t.degradeDeadline = time.Now().Add(selftest46Wait)
		t.degradeStage = 4
		return selftest.Pending
	case 4:
		state := t.state.TabState(appsysmanager.TabTmux)
		if state.Status != appsysmanager.StatusLive || len(state.Sessions) == 0 {
			return pending(t.degradeDeadline, "the rich Host to recover")
		}
		t.degradeStage = 5
	}
	return selftest.Passed(
		"a Host without tmux or Docker showed typed missing_tmux and missing_docker states without running either command, its GPU tab stayed live and empty, and switching back recovered the session list",
		"application/sysmanager.Capabilities.Available + ui/sysmanager reason text")
}

func (t *selftest46) layoutStep(*ui.Context) selftest.Outcome {
	t.state.Flush()
	switch t.layoutStage {
	case 0:
		if err := t.state.SetTabVisible(appsysmanager.TabTmux, false); err != nil {
			return selftest.Failed(fmt.Errorf("hide tmux: %w", err))
		}
		if t.state.TabVisible(appsysmanager.TabTmux) {
			return selftest.Failed(errors.New("tmux stayed visible"))
		}
		hidden := t.prefs.Settings().SysManager.HiddenTabs
		if len(hidden) != 1 || hidden[0] != "tmux" {
			return selftest.Failed(fmt.Errorf("persisted hidden tabs = %v", hidden))
		}
		t.state.SetTab(appsysmanager.TabDocker)
		if !t.state.MoveTab(-1) {
			return selftest.Failed(errors.New("moving the Docker tab left reported no change"))
		}
		order := t.prefs.Settings().SysManager.Order()
		if len(order) != 7 || string(order[4]) != "docker" || string(order[5]) != "tmux" {
			return selftest.Failed(fmt.Errorf("persisted order = %v", order))
		}
		if err := t.state.SetTabVisible(appsysmanager.TabTmux, true); err != nil {
			return selftest.Failed(fmt.Errorf("show tmux: %w", err))
		}
		t.layoutStage = 1
		t.layoutDeadline = time.Now().Add(selftest46Wait)
		return selftest.Pending
	case 1:
		// A fresh surface reloads the same persisted store. It is not
		// closed: closing would stop the live sampler it shares.
		restored := uisysmanager.NewState(t.service, t.hosts, t.prefs)
		restored.Activate()
		tabs := restored.Tabs()
		if len(tabs) != 7 || tabs[4] != appsysmanager.TabDocker ||
			tabs[5] != appsysmanager.TabTmux {
			return selftest.Failed(fmt.Errorf("restored tabs = %v", tabs))
		}
		if t.state.TabIndex() != 4 || t.state.Tab() != appsysmanager.TabDocker {
			return selftest.Failed(fmt.Errorf("active tab = %q at %d",
				t.state.Tab(), t.state.TabIndex()))
		}
		t.layoutStage = 2
	}
	return selftest.Passed(
		"hiding tmux and moving Docker before it persisted in the settings group, and a fresh surface restored the visibility and order while the overview stayed locked visible",
		"ui/sysmanager tab layout + domain/preferences.SysManager.TabOrder/HiddenTabs")
}

func (t *selftest46) renderStep(*ui.Context) selftest.Outcome {
	if t.directory == "" {
		t.directory = filepath.Join(selftestResultDir, "natcatty-selftest-46-shots")
		if err := os.MkdirAll(t.directory, 0o755); err != nil {
			return selftest.Failed(fmt.Errorf("create the screenshot directory: %w", err))
		}
	}
	type captureCase struct {
		tab   appsysmanager.Tab
		view  int
		label string
		en    string
		zh    string
	}
	cases := []captureCase{
		{appsysmanager.TabTmux, 0, "tmux", "Session", "会话"},
		{appsysmanager.TabDocker, 0, "docker-containers", "Container", "容器"},
		{appsysmanager.TabDocker, 1, "docker-images", "Images", "镜像"},
		{appsysmanager.TabGPU, 0, "gpu", "Utilization", "使用率"},
	}
	if t.renderTab >= len(cases) {
		entries, err := os.ReadDir(t.directory)
		if err != nil {
			return selftest.Failed(fmt.Errorf("read the screenshot directory: %w", err))
		}
		if len(entries) != 16 {
			return selftest.Failed(fmt.Errorf("captured %d screenshots, want 16", len(entries)))
		}
		return selftest.Passed(
			"tmux, Docker containers, Docker images, and GPU rendered at 1100x720 and 720x480 in en and zh-CN; sixteen frames captured",
			"uisysmanager.View + ui.Tester")
	}
	current := cases[t.renderTab]
	if t.state.Tab() != current.tab || (current.tab == appsysmanager.TabDocker &&
		t.state.DockerView() != current.view) {
		if current.tab == appsysmanager.TabDocker {
			t.state.SetDockerView(current.view)
		}
		t.state.SetTab(current.tab)
		t.renderDeadline = time.Now().Add(selftest46Wait)
		return selftest.Pending
	}
	t.state.Flush()
	state := t.state.TabState(current.tab)
	if state.Status != appsysmanager.StatusLive {
		return pending(t.renderDeadline, current.label+" to become live for capture")
	}
	// A warm frame lets the Table forget the previous tab's row identity
	// before the first row is chosen here.
	warm := ui.NewTester(uisysmanager.View(t.state), 1100, 720)
	warm.Frame()
	if current.tab != appsysmanager.TabGPU {
		t.state.SelectRow(0)
	}
	en := locales.In("en")
	zh := locales.In("zh-CN")
	for _, size := range []struct {
		width, height int
	}{{1100, 720}, {720, 480}} {
		for _, locale := range []string{"en", "zh-CN"} {
			tester := ui.NewTester(uisysmanager.View(t.state), size.width, size.height)
			tester.SetLocale(locale)
			tester.Frame()
			m := en
			want := current.en
			if locale == "zh-CN" {
				m = zh
				want = current.zh
			}
			if !inside(tester, m.SysManagerTitle(), size.width, size.height) {
				return selftest.Failed(fmt.Errorf("%s %dx%d title is clipped: %v",
					locale, size.width, size.height, tester.Texts()))
			}
			if !tester.HasText(want) {
				return selftest.Failed(fmt.Errorf("%s %dx%d %s does not show %q: %v",
					locale, size.width, size.height, current.label, want, tester.Texts()))
			}
			if current.tab == appsysmanager.TabGPU && !tester.HasText("NVIDIA A100") {
				return selftest.Failed(fmt.Errorf("%s %dx%d GPU does not show the device: %v",
					locale, size.width, size.height, tester.Texts()))
			}
			if current.tab == appsysmanager.TabTmux &&
				!tester.HasText(m.SysManagerAttach()) {
				return selftest.Failed(fmt.Errorf("%s %dx%d tmux action bar does not show %q: %v",
					locale, size.width, size.height, m.SysManagerAttach(), tester.Texts()))
			}
			if current.tab == appsysmanager.TabDocker && current.view == 0 &&
				!tester.HasText(m.SysManagerInspect()) {
				return selftest.Failed(fmt.Errorf("%s %dx%d Docker action bar does not show %q: %v",
					locale, size.width, size.height, m.SysManagerInspect(), tester.Texts()))
			}
			name := fmt.Sprintf("sysmanager-%s-%dx%d-%s.png",
				current.label, size.width, size.height, locale)
			if err := capture(tester, filepath.Join(t.directory, name)); err != nil {
				return selftest.Failed(err)
			}
			t.captured++
		}
	}
	t.renderTab++
	return selftest.Pending
}

func (t *selftest46) cleanupStep(*ui.Context) selftest.Outcome {
	if t.cleanupDeadline.IsZero() {
		t.cleanupDeadline = time.Now().Add(selftest46Wait)
		t.state.Deactivate()
		t.state.Close()
		t.service.Close()
	}
	t.service.Wait()
	if !t.connector.allClosed() {
		return pending(t.cleanupDeadline, "every leased transport to close")
	}
	return selftest.Passed(fmt.Sprintf(
		"deactivation and close released every sampler and action lease (%d leases); %d screenshots captured",
		t.connector.openCount(selftest46Rich), t.captured),
		"application/sysmanager.Service.Close + ui/sysmanager.State.Close")
}

// selftest46Connector serves the extended tabs. The core probe keeps ticket
// 45's line so this fake can answer both command templates.
type selftest46Connector struct {
	mu         sync.Mutex
	opens      map[string]int
	openLeases int
	closed     int
	commands   map[string][]string
	minimal    map[string]bool
	overviews  map[string]int
}

func (c *selftest46Connector) Open(ctx context.Context, host domainvault.Host) (appsysmanager.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := string(host.ID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.opens == nil {
		c.opens = make(map[string]int)
	}
	if c.overviews == nil {
		c.overviews = make(map[string]int)
	}
	c.opens[id]++
	c.openLeases++
	return &selftest46Connection{connector: c, id: id}, nil
}

func (c *selftest46Connector) openCount(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens[id]
}

func (c *selftest46Connector) countPrefix(id, prefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, command := range c.commands[id] {
		if strings.HasPrefix(command, prefix) {
			count++
		}
	}
	return count
}

func (c *selftest46Connector) hasPrefix(id, prefix string) bool {
	return c.countPrefix(id, prefix) > 0
}

func (c *selftest46Connector) hasCommand(id, want string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, command := range c.commands[id] {
		if command == want {
			return true
		}
	}
	return false
}

func (c *selftest46Connector) allClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed == c.openLeases
}

func (c *selftest46Connector) leaseClosed() {
	c.mu.Lock()
	c.closed++
	c.mu.Unlock()
}

type selftest46Connection struct {
	connector *selftest46Connector
	id        string
	closed    bool
}

func (c *selftest46Connection) Exec(ctx context.Context, command string) (appsysmanager.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return appsysmanager.ExecResult{}, err
	}
	connector := c.connector
	connector.mu.Lock()
	connector.commands[c.id] = append(connector.commands[c.id], command)
	minimal := connector.minimal[c.id]
	index := connector.overviews[c.id]
	if strings.Contains(command, "PRETTY_NAME") {
		connector.overviews[c.id] = index + 1
	}
	connector.mu.Unlock()

	if strings.Contains(command, "for c in ps top ss netstat lsof systemctl") {
		if minimal {
			return appsysmanager.ExecResult{Stdout: "__NC_OS=Linux\n__NC_HAS_ps=1\n"}, nil
		}
		return appsysmanager.ExecResult{Stdout: "__NC_OS=Linux\n__NC_HAS_ps=1\n" +
			"__NC_HAS_top=1\n__NC_HAS_ss=1\n__NC_HAS_netstat=1\n__NC_HAS_lsof=1\n" +
			"__NC_HAS_systemctl=1\n__NC_HAS_tmux=1\n__NC_HAS_docker=1\n" +
			"__NC_HAS_nvidia-smi=1\n"}, nil
	}
	switch {
	case strings.Contains(command, "PRETTY_NAME"):
		return appsysmanager.ExecResult{Stdout: selftest46Overview(index)}, nil
	case strings.Contains(command, "ps -eo"):
		return appsysmanager.ExecResult{Stdout: selftest45Processes}, nil
	case strings.Contains(command, "__NC_PORTS_BEGIN__"):
		return appsysmanager.ExecResult{Stdout: selftest45Ports}, nil
	case strings.Contains(command, "__NC_SERVICES_BEGIN__"):
		return appsysmanager.ExecResult{Stdout: selftest45Services}, nil
	case strings.Contains(command, "__NC_TMUX_BEGIN__"):
		return appsysmanager.ExecResult{Stdout: selftest46Sessions}, nil
	case strings.Contains(command, "__NC_DOCKER_BEGIN__"):
		return appsysmanager.ExecResult{Stdout: selftest46Docker}, nil
	case strings.Contains(command, "__NC_GPU_BEGIN__"):
		if minimal {
			return appsysmanager.ExecResult{
				Stdout: "__NC_GPU_BEGIN__\n__NC_GPU_END__\n"}, nil
		}
		return appsysmanager.ExecResult{Stdout: selftest46GPU}, nil
	case strings.HasPrefix(command, "tmux "), strings.HasPrefix(command, "docker "),
		strings.HasPrefix(command, "kill "), strings.HasPrefix(command, "systemctl "),
		strings.HasPrefix(command, "sudo -n systemctl "):
		switch {
		case strings.HasPrefix(command, "docker inspect "):
			return appsysmanager.ExecResult{Stdout: `{"Id":"3f2a1b","State":{"Running":true}}`}, nil
		case strings.HasPrefix(command, "docker logs "):
			return appsysmanager.ExecResult{Stdout: "listening on 0.0.0.0:80\n"}, nil
		default:
			return appsysmanager.ExecResult{Stdout: "ok", HasExitCode: true}, nil
		}
	default:
		return appsysmanager.ExecResult{}, nil
	}
}

func (c *selftest46Connection) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	c.connector.leaseClosed()
	return nil
}

const (
	selftest46Sessions = "__NC_TMUX_BEGIN__\n" +
		"work\t3\t1\t1700000000\n" +
		"idle\t1\t0\t1700000100\n" +
		"__NC_TMUX_END__\n"
	selftest46Docker = "__NC_DOCKER_BEGIN__\n" +
		"__NC_DOCKER_CONTAINERS__\n" +
		`{"ID":"3f2a1b","Names":"/web","Image":"nginx:1.27","State":"running","Status":"Up 2 hours","Ports":"0.0.0.0:80->80/tcp","CreatedAt":"2025-10-01 10:00:00 +0000 UTC"}` + "\n" +
		`{"ID":"9c8d7e","Names":"/db","Image":"postgres:16","State":"exited","Status":"Exited (0) 1 hour ago","Ports":"","CreatedAt":"2025-10-01 09:00:00 +0000 UTC"}` + "\n" +
		"__NC_DOCKER_IMAGES__\n" +
		`{"ID":"sha256:aa","Repository":"nginx","Tag":"1.27","Size":"192MB","CreatedAt":"2025-09-01 10:00:00 +0000 UTC","Digest":"sha256:bb"}` + "\n" +
		`{"ID":"sha256:cc","Repository":"<none>","Tag":"<none>","Size":"1.2GB","CreatedAt":"2025-08-01 10:00:00 +0000 UTC","Digest":"sha256:dd"}` + "\n" +
		"__NC_DOCKER_END__\n"
	selftest46GPU = "__NC_GPU_BEGIN__\n__NC_NVIDIA__\n" +
		"0, GPU-1111, NVIDIA A100, 12, 512, 40960, 41, 28.0, 300.0, 30, 560.35.03\n" +
		"1, GPU-2222, NVIDIA RTX 4090, 87, 18234, 24564, 72, 310.5, 450.0, 65, 560.35.03\n" +
		"__NC_GPU_END__\n"
)

func selftest46Overview(index int) string {
	total := 1000 + index*100
	idle := 500 + index*25
	return fmt.Sprintf("MODE:linux|HOST:selftest-46-alpha|OS:Debian 12|KERNEL:6.1.0-selftest|"+
		"UPTIME:90061|LOAD:0.52 0.48 0.41|CORES:8|CPURAW:%d %d|MEM:3072 8192|"+
		"SWAP:128 1024|DISK:25.0 100.0", total, idle)
}
