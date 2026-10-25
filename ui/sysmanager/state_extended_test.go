package sysmanager

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
)

// fakeExtendedService adds ticket 46's actions to the core fake. Embedding
// keeps the ticket-45 behaviour (starts, stops, signals) intact.
type fakeExtendedService struct {
	*fakeService

	mu            sync.Mutex
	tmuxCreates   []string
	tmuxKills     []string
	dockerActions []dockerCall
	inspectCalls  int
	logCalls      int
	logTail       int
	document      appsysmanager.DockerDocument
	documentErr   error
	extendedErr   error
	block         chan struct{}
	started       chan struct{}
	finished      chan struct{}
}

type dockerCall struct {
	id     string
	action appsysmanager.DockerAction
}

func newFakeExtendedService() *fakeExtendedService {
	return &fakeExtendedService{
		fakeService: newFakeService(),
		document: appsysmanager.DockerDocument{
			Kind: appsysmanager.DocumentInspect, ContainerID: "3f2a1b",
			Text: "{\n  \"Id\": \"3f2a1b\"\n}",
		},
	}
}

func (f *fakeExtendedService) TmuxCreate(ctx context.Context, host domainvault.Host,
	name string) (appsysmanager.ActionResult, error) {
	command, _ := appsysmanager.TmuxCreateCommand(name)
	f.mu.Lock()
	f.tmuxCreates = append(f.tmuxCreates, name)
	err := f.extendedErr
	f.mu.Unlock()
	if err != nil {
		return appsysmanager.ActionResult{Kind: appsysmanager.ActionTmuxCreate, Command: command}, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return appsysmanager.ActionResult{Kind: appsysmanager.ActionTmuxCreate, Command: command}, ctxErr
	}
	return appsysmanager.ActionResult{Kind: appsysmanager.ActionTmuxCreate, Command: command,
		Stdout: "created", HasExitCode: true}, nil
}

func (f *fakeExtendedService) TmuxKill(ctx context.Context, host domainvault.Host,
	name string) (appsysmanager.ActionResult, error) {
	command, _ := appsysmanager.TmuxKillCommand(name)
	f.mu.Lock()
	f.tmuxKills = append(f.tmuxKills, name)
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return appsysmanager.ActionResult{Kind: appsysmanager.ActionTmuxKill, Command: command}, err
	}
	return appsysmanager.ActionResult{Kind: appsysmanager.ActionTmuxKill, Command: command,
		Stdout: "killed", HasExitCode: true}, nil
}

func (f *fakeExtendedService) DockerContainerAction(ctx context.Context, host domainvault.Host,
	id string, action appsysmanager.DockerAction) (appsysmanager.ActionResult, error) {
	command, _ := appsysmanager.DockerActionCommand(id, action)
	f.mu.Lock()
	f.dockerActions = append(f.dockerActions, dockerCall{id: id, action: action})
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return appsysmanager.ActionResult{Command: command}, err
	}
	return appsysmanager.ActionResult{Command: command, Stdout: "ok", HasExitCode: true}, nil
}

func (f *fakeExtendedService) DockerInspect(ctx context.Context, host domainvault.Host,
	id string) (appsysmanager.DockerDocument, error) {
	f.mu.Lock()
	f.inspectCalls++
	block := f.block
	started := f.started
	document := f.document
	err := f.documentErr
	f.mu.Unlock()
	f.signal(started)
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			f.signal(f.finished)
			return appsysmanager.DockerDocument{}, ctx.Err()
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		f.signal(f.finished)
		return appsysmanager.DockerDocument{}, ctxErr
	}
	f.signal(f.finished)
	document.Kind = appsysmanager.DocumentInspect
	document.ContainerID = id
	return document, err
}

func (f *fakeExtendedService) DockerLogs(ctx context.Context, host domainvault.Host,
	id string, tail int) (appsysmanager.DockerDocument, error) {
	f.mu.Lock()
	f.logCalls++
	f.logTail = tail
	block := f.block
	started := f.started
	document := f.document
	err := f.documentErr
	f.mu.Unlock()
	f.signal(started)
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			f.signal(f.finished)
			return appsysmanager.DockerDocument{}, ctx.Err()
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		f.signal(f.finished)
		return appsysmanager.DockerDocument{}, ctxErr
	}
	f.signal(f.finished)
	document.Kind = appsysmanager.DocumentLogs
	document.ContainerID = id
	return document, err
}

func (f *fakeExtendedService) signal(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (f *fakeExtendedService) calls() (creates, kills, actions, inspects, logs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tmuxCreates), len(f.tmuxKills), len(f.dockerActions),
		f.inspectCalls, f.logCalls
}

func (f *fakeExtendedService) lastDocker() (dockerCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.dockerActions) == 0 {
		return dockerCall{}, false
	}
	return f.dockerActions[len(f.dockerActions)-1], true
}

func demoExtendedSnapshot() appsysmanager.Snapshot {
	snapshot := demoSnapshot()
	snapshot.Tabs[appsysmanager.TabTmux] = appsysmanager.TabState{
		Tab: appsysmanager.TabTmux, Status: appsysmanager.StatusLive, At: time.Now(),
		Sessions: []appsysmanager.TmuxSession{
			{Name: "work", Windows: 3, Attached: true, Created: 1700000000},
			{Name: "idle", Windows: 1},
		},
	}
	snapshot.Tabs[appsysmanager.TabDocker] = appsysmanager.TabState{
		Tab: appsysmanager.TabDocker, Status: appsysmanager.StatusLive, At: time.Now(),
		Containers: []appsysmanager.Container{
			{ID: "3f2a1b", Name: "web", Image: "nginx:1.27", State: "running",
				Status: "Up 2 hours", Ports: "0.0.0.0:80->80/tcp"},
			{ID: "9c8d7e", Name: "db", Image: "postgres:16", State: "exited",
				Status: "Exited (0) 1 hour ago"},
		},
		Images: []appsysmanager.Image{
			{ID: "sha256:aa", Repository: "nginx", Tag: "1.27", Size: "192MB",
				CreatedAt: "2025-09-01 10:00:00 +0000 UTC"},
			{ID: "sha256:cc", Repository: "<none>", Tag: "<none>", Size: "1.2GB"},
		},
	}
	snapshot.Tabs[appsysmanager.TabGPU] = appsysmanager.TabState{
		Tab: appsysmanager.TabGPU, Status: appsysmanager.StatusLive, At: time.Now(),
		GPU: appsysmanager.GPUInfo{
			DriverVersion: "560.35.03",
			Devices: []appsysmanager.GPUDevice{
				{Vendor: "nvidia", Index: 0, Name: "NVIDIA A100",
					UtilizationPercent: 12, MemoryUsedMB: 512, MemoryTotalMB: 40960,
					TemperatureC: 41, PowerDrawW: 28, PowerLimitW: 300, FanPercent: 30,
					DriverVersion: "560.35.03"},
			},
		},
	}
	return snapshot
}

func newExtendedTestState(service *fakeExtendedService,
	prefs *fakePreferences) *State {
	return NewState(service, fakeHosts{hosts: testHosts()}, prefs)
}

func TestTabVisibilityAndOrderPersist(t *testing.T) {
	service := newFakeExtendedService()
	service.snapshot = demoExtendedSnapshot()
	prefs := newFakePreferences()
	state := newExtendedTestState(service, prefs)
	state.Activate()
	state.Flush()

	if got := state.Tabs(); len(got) != len(appsysmanager.ExtendedTabs) ||
		got[4] != appsysmanager.TabTmux || got[6] != appsysmanager.TabGPU {
		t.Fatalf("default tabs = %v", got)
	}
	if !state.TabVisible(appsysmanager.TabDocker) || state.TabIndex() != 0 {
		t.Fatalf("initial visibility/index = %v/%d",
			state.TabVisible(appsysmanager.TabDocker), state.TabIndex())
	}

	if err := state.SetTabVisible(appsysmanager.TabTmux, false); err != nil {
		t.Fatalf("hide tmux: %v", err)
	}
	if state.TabVisible(appsysmanager.TabTmux) {
		t.Error("tmux stayed visible")
	}
	if got := prefs.Settings().SysManager.HiddenTabs; len(got) != 1 || got[0] != "tmux" {
		t.Errorf("persisted hidden tabs = %v", got)
	}
	state.SetTab(appsysmanager.TabTmux)
	if state.Tab() != appsysmanager.TabOverview {
		t.Errorf("a hidden tab was activated: %q", state.Tab())
	}

	if err := state.SetTabVisible(appsysmanager.TabTmux, true); err != nil {
		t.Fatalf("show tmux: %v", err)
	}
	state.SetTab(appsysmanager.TabDocker)
	if state.TabIndex() != 5 {
		t.Fatalf("docker index = %d, want 5", state.TabIndex())
	}
	if !state.MoveTab(-1) {
		t.Fatal("moving the docker tab left reported no change")
	}
	want := []appsysmanager.Tab{
		appsysmanager.TabOverview, appsysmanager.TabProcesses, appsysmanager.TabPorts,
		appsysmanager.TabServices, appsysmanager.TabDocker, appsysmanager.TabTmux,
		appsysmanager.TabGPU,
	}
	if got := state.Tabs(); !sameTabList(got, want) {
		t.Errorf("tabs after move = %v, want %v", got, want)
	}
	if got := prefs.Settings().SysManager.Order(); len(got) != len(want) ||
		string(got[4]) != "docker" || string(got[5]) != "tmux" {
		t.Errorf("persisted order = %v", got)
	}
	if state.MoveTab(1) {
		state.MoveTab(-1)
	}
	state.SetTab(appsysmanager.TabOverview)
	if state.MoveTab(-1) {
		t.Error("a move past the front reported a change")
	}
	state.SetTab(appsysmanager.TabDocker)

	// Hiding the active tab moves to the first visible one.
	if err := state.SetTabVisible(appsysmanager.TabDocker, false); err != nil {
		t.Fatalf("hide active tab: %v", err)
	}
	if state.Tab() != appsysmanager.TabOverview {
		t.Errorf("active tab after hiding it = %q", state.Tab())
	}
	if err := state.SetTabVisible(appsysmanager.TabOverview, false); err != nil {
		t.Fatalf("hide overview: %v", err)
	}
	if !state.TabVisible(appsysmanager.TabOverview) {
		t.Error("the overview could be hidden")
	}

	// A fresh surface over the same store restores the layout.
	restored := newExtendedTestState(service, prefs)
	restored.Activate()
	tabs := restored.Tabs()
	if len(tabs) != len(want)-1 || tabs[4] != appsysmanager.TabTmux {
		t.Errorf("restored tabs = %v", tabs)
	}
	if restored.TabVisible(appsysmanager.TabDocker) {
		t.Error("docker visibility was not restored")
	}
}

func sameTabList(left, right []appsysmanager.Tab) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestTmuxCreateRunsWithoutConfirmation(t *testing.T) {
	service := newFakeExtendedService()
	service.snapshot = demoExtendedSnapshot()
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	state.SetTmuxName("work")
	if err := state.CreateSession(); err != nil {
		t.Fatalf("create: %v", err)
	}
	waitFor(t, 2*time.Second, "the detached session", func() bool {
		creates, _, _, _, _ := service.calls()
		if creates > 0 {
			state.Flush()
		}
		return creates == 1 && !state.ActionBusy()
	})
	service.mu.Lock()
	name := service.tmuxCreates[0]
	service.mu.Unlock()
	if name != "work" {
		t.Errorf("created name = %q", name)
	}
	if result, ok := state.ActionResult(); !ok ||
		result.Command != "tmux new-session -d -s 'work'" ||
		result.Kind != appsysmanager.ActionTmuxCreate {
		t.Errorf("action result = %+v, %v", result, ok)
	}
	if state.TmuxName() != "" {
		t.Errorf("the created name stayed in the field: %q", state.TmuxName())
	}
	if _, ok := state.Pending(); ok {
		t.Error("creating a session opened a confirmation")
	}
	if service.refreshes == 0 {
		t.Error("a created session did not request a fresh sample")
	}

	before, _, _, _, _ := service.calls()
	state.SetTmuxName("bad:name")
	if err := state.CreateSession(); err == nil {
		t.Fatal("an invalid session name was accepted")
	}
	after, _, _, _, _ := service.calls()
	if after != before {
		t.Errorf("an invalid name ran %d command(s)", after-before)
	}
	if state.Problem() != ProblemActionData {
		t.Errorf("invalid name problem = %q", state.Problem())
	}
}

func TestTmuxKillRequiresConfirmation(t *testing.T) {
	service := newFakeExtendedService()
	service.snapshot = demoExtendedSnapshot()
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	if err := state.RequestTmuxKill("work"); err != nil {
		t.Fatalf("request kill: %v", err)
	}
	pending, ok := state.Pending()
	if !ok || pending.Kind != appsysmanager.ActionTmuxKill ||
		pending.Session != "work" || pending.Command != "tmux kill-session -t 'work'" {
		t.Fatalf("pending = %+v, %v", pending, ok)
	}
	if _, kills, _, _, _ := service.calls(); kills != 0 {
		t.Fatalf("a kill ran before confirmation: %d", kills)
	}
	state.ConfirmPending()
	waitFor(t, 2*time.Second, "the confirmed kill", func() bool {
		_, kills, _, _, _ := service.calls()
		if kills > 0 {
			state.Flush()
		}
		return kills == 1 && !state.ActionBusy()
	})
	if state.ActionError() != nil {
		t.Errorf("kill error = %v", state.ActionError())
	}

	if err := state.RequestTmuxKill("idle"); err != nil {
		t.Fatalf("request second kill: %v", err)
	}
	state.CancelPending()
	state.Flush()
	if _, kills, _, _, _ := service.calls(); kills != 1 {
		t.Errorf("a cancelled kill ran: %d", kills)
	}
}

func TestTmuxAttachOpensATerminalWithTheAttachCommand(t *testing.T) {
	service := newFakeExtendedService()
	service.snapshot = demoExtendedSnapshot()
	state := NewState(service, fakeHosts{hosts: testHosts()}, newFakePreferences(),
		WithTerminalOpener(func(host domainvault.Host, command string) error {
			if string(host.ID) != "alpha" {
				t.Errorf("attach host = %q", host.ID)
			}
			if !strings.Contains(command, "exec tmux attach -t 'work'") {
				t.Errorf("attach command = %q", command)
			}
			return nil
		}))
	state.Activate()
	state.Flush()

	if err := state.AttachSession("work"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if state.Problem() != ProblemNone {
		t.Errorf("problem = %q", state.Problem())
	}

	noOpener := newExtendedTestState(service, newFakePreferences())
	noOpener.Activate()
	if err := noOpener.AttachSession("work"); err == nil {
		t.Fatal("attach without an opener was accepted")
	}
	if noOpener.Problem() != ProblemAttach {
		t.Errorf("no-opener problem = %q", noOpener.Problem())
	}
}

func TestDockerActionsRequireConfirmation(t *testing.T) {
	service := newFakeExtendedService()
	service.snapshot = demoExtendedSnapshot()
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	rows := state.DisplayContainers()
	if len(rows) != 2 {
		t.Fatalf("displayed containers = %d", len(rows))
	}
	// Rows follow the container name: db, then web.
	state.SelectRow(1)
	container, ok := state.SelectedContainer()
	if !ok || container.ID != "3f2a1b" {
		t.Fatalf("selected container = %+v", container)
	}

	tests := []struct {
		action  appsysmanager.DockerAction
		kind    appsysmanager.ActionKind
		command string
	}{
		{appsysmanager.DockerStop, appsysmanager.ActionDockerStop, "docker stop 3f2a1b"},
		{appsysmanager.DockerStart, appsysmanager.ActionDockerStart, "docker start 3f2a1b"},
		{appsysmanager.DockerRemove, appsysmanager.ActionDockerRemove, "docker rm -f 3f2a1b"},
	}
	for _, test := range tests {
		_, _, before, _, _ := service.calls()
		if err := state.RequestDockerAction(container, test.action); err != nil {
			t.Fatalf("request %s: %v", test.action, err)
		}
		pending, ok := state.Pending()
		if !ok || pending.Kind != test.kind || pending.ContainerID != "3f2a1b" ||
			pending.ContainerName != "web" || pending.Command != test.command ||
			pending.DockerAction != test.action {
			t.Fatalf("%s pending = %+v, %v", test.action, pending, ok)
		}
		if _, _, during, _, _ := service.calls(); during != before {
			t.Fatalf("%s ran before confirmation: %d", test.action, during)
		}
		state.ConfirmPending()
		waitFor(t, 2*time.Second, "the confirmed "+string(test.action), func() bool {
			_, _, actions, _, _ := service.calls()
			if actions > before {
				state.Flush()
			}
			return actions == before+1 && !state.ActionBusy()
		})
		call, _ := service.lastDocker()
		if call.id != "3f2a1b" || call.action != test.action {
			t.Errorf("%s call = %+v", test.action, call)
		}
	}

	if err := state.RequestDockerAction(container, "pause"); err == nil {
		t.Error("an unsupported container action was accepted")
	}
}

func TestDockerInspectAndLogsLoadAndCloseTheDocument(t *testing.T) {
	service := newFakeExtendedService()
	service.document = appsysmanager.DockerDocument{
		Kind: appsysmanager.DocumentInspect, ContainerID: "3f2a1b",
		Text: "{\n  \"Id\": \"3f2a1b\"\n}", Truncated: false,
	}
	service.snapshot = demoExtendedSnapshot()
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()
	container := appsysmanager.Container{ID: "3f2a1b", Name: "web"}

	if err := state.InspectContainer(container); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !state.DocumentBusy() {
		t.Error("the inspect panel is not busy after starting")
	}
	waitFor(t, 2*time.Second, "the inspect reply", func() bool {
		state.Flush()
		return !state.DocumentBusy()
	})
	document, open := state.Document()
	if !open || document.Kind != appsysmanager.DocumentInspect ||
		!strings.Contains(document.Text, "\"Id\"") {
		t.Errorf("inspect document = %+v, %v", document, open)
	}

	if err := state.ContainerLogs(container); err != nil {
		t.Fatalf("logs: %v", err)
	}
	waitFor(t, 2*time.Second, "the logs reply", func() bool {
		state.Flush()
		return !state.DocumentBusy()
	})
	document, open = state.Document()
	if !open || document.Kind != appsysmanager.DocumentLogs {
		t.Errorf("logs document = %+v, %v", document, open)
	}
	service.mu.Lock()
	tail := service.logTail
	service.mu.Unlock()
	if tail != appsysmanager.DefaultDockerLogTail {
		t.Errorf("log tail = %d, want the bounded default", tail)
	}

	state.CloseDocument()
	if _, open := state.Document(); open {
		t.Error("the document panel stayed open after closing")
	}
}

func TestDockerDocumentCancelsAndDropsStaleReplies(t *testing.T) {
	service := newFakeExtendedService()
	service.snapshot = demoExtendedSnapshot()
	service.block = make(chan struct{})
	service.started = make(chan struct{}, 4)
	service.finished = make(chan struct{}, 4)
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	container := appsysmanager.Container{ID: "3f2a1b", Name: "web"}
	if err := state.InspectContainer(container); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	select {
	case <-service.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the document read never started")
	}
	state.CloseDocument()
	close(service.block)
	select {
	case <-service.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled read never returned")
	}
	state.Flush()
	if _, open := state.Document(); open {
		t.Error("a late reply reopened the closed document")
	}

	// Deactivate cancels a read in flight too.
	service.block = make(chan struct{})
	if err := state.InspectContainer(container); err != nil {
		t.Fatalf("second inspect: %v", err)
	}
	select {
	case <-service.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the second read never started")
	}
	state.Deactivate()
	close(service.block)
	state.Flush()
	if _, open := state.Document(); open {
		t.Error("deactivating left a document open")
	}
}

func TestDockerViewSwitchesListsAndClearsSelection(t *testing.T) {
	service := newFakeExtendedService()
	service.snapshot = demoExtendedSnapshot()
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	state.SelectRow(0)
	if _, ok := state.SelectedContainer(); !ok {
		t.Fatal("no container selected")
	}
	if _, ok := state.SelectedImage(); ok {
		t.Error("an image was selected in the container view")
	}
	state.SetDockerView(1)
	if state.DockerView() != 1 {
		t.Errorf("view = %d", state.DockerView())
	}
	if _, ok := state.SelectedContainer(); ok {
		t.Error("the container selection survived the view change")
	}
	state.SelectRow(0)
	image, ok := state.SelectedImage()
	if !ok || image.Name() != "nginx:1.27" {
		t.Errorf("selected image = %+v, %v", image, ok)
	}
	rows := state.DisplayImages()
	if len(rows) != 2 || rows[1].Name() != "sha256:cc" {
		t.Errorf("displayed images = %+v", rows)
	}
	state.SetDockerView(0)
	if _, ok := state.SelectedImage(); ok {
		t.Error("the image selection survived the view change back")
	}
}

func TestConfirmationTextNamesTabsAndDockerTargets(t *testing.T) {
	m := locales.In("en")
	title, message, confirm := confirmationText(PendingAction{
		Kind:    appsysmanager.ActionTmuxKill,
		Session: "work",
		Command: "tmux kill-session -t 'work'",
	}, m)
	if title != m.SysManagerKill() || confirm != title ||
		!strings.Contains(message, "work") || !strings.Contains(message, "kill-session") {
		t.Errorf("tmux confirmation = %q / %q / %q", title, message, confirm)
	}

	title, message, confirm = confirmationText(PendingAction{
		Kind:          appsysmanager.ActionDockerStop,
		ContainerName: "web",
		DockerAction:  appsysmanager.DockerStop,
		Command:       "docker stop 3f2a1b",
	}, m)
	if title != m.SysManagerStop() || confirm != title ||
		!strings.Contains(message, "web") || !strings.Contains(message, "docker stop") {
		t.Errorf("docker confirmation = %q / %q / %q", title, message, confirm)
	}
}

func TestExtendedProblemsAndReasonsAreLocalized(t *testing.T) {
	for _, locale := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(locale)
		for _, text := range []string{
			m.SysManagerTabTmux(), m.SysManagerTabDocker(), m.SysManagerTabGPU(),
			m.SysManagerTabsLayout(), m.SysManagerMoveTabLeft(),
			m.SysManagerMoveTabRight(), m.SysManagerReasonMissingTmux(),
			m.SysManagerReasonMissingDocker(), m.SysManagerReasonDockerDaemon(),
			m.SysManagerProblemAttach(), m.SysManagerNoSessions(),
			m.SysManagerNoContainers(), m.SysManagerNoImages(),
			m.SysManagerGPUEmpty(), m.SysManagerDocumentFailed(),
			m.SysManagerConfirmDocker(m.SysManagerStart(), "web", "docker start 3f2a1b"),
			m.SysManagerConfirmTmuxKill("work", "tmux kill-session -t 'work'"),
		} {
			if strings.TrimSpace(text) == "" {
				t.Fatalf("%s has an empty new message", locale)
			}
		}
		if got := reasonText(m, appsysmanager.ReasonMissingTmux); got == "" {
			t.Errorf("%s has no tmux reason text", locale)
		}
	}
}

func TestDocumentErrorsKeepThePanelOpen(t *testing.T) {
	service := newFakeExtendedService()
	service.snapshot = demoExtendedSnapshot()
	service.documentErr = errors.New("docker: permission denied")
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	if err := state.InspectContainer(appsysmanager.Container{ID: "3f2a1b"}); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	waitFor(t, 2*time.Second, "the failed inspect", func() bool {
		state.Flush()
		return !state.DocumentBusy()
	})
	if _, open := state.Document(); !open {
		t.Fatal("a failed read closed the panel")
	}
	if state.DocumentError() == nil {
		t.Error("the document failure was lost")
	}
	state.CloseDocument()
}

func TestDockerDocumentRejectsUnknownTargetsBeforeOpening(t *testing.T) {
	service := newFakeExtendedService()
	state := newExtendedTestState(service, newFakePreferences())
	state.Activate()
	state.Flush()

	if err := state.InspectContainer(appsysmanager.Container{}); err == nil {
		t.Fatal("an empty container id was accepted")
	}
	if err := state.ContainerLogs(appsysmanager.Container{}); err == nil {
		t.Fatal("an empty logs id was accepted")
	}
	if _, _, _, inspects, logs := service.calls(); inspects != 0 || logs != 0 {
		t.Errorf("invalid targets ran inspect=%d logs=%d", inspects, logs)
	}
}
