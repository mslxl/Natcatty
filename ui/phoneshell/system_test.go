package phoneshell

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager/core"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeSystemService records the shared runtime's demand and lets a test publish
// a snapshot. It stands in for application/sysmanager/core.Service.
type fakeSystemService struct {
	mu        sync.Mutex
	starts    []systemStartCall
	stops     int
	refreshes int
	closes    int
	snapshot  appsysmanager.Snapshot
	listeners map[int]func()
	nextID    int

	runs       []*systemRunRecord
	runErr     error
	runErrOnce bool
	runErrs    map[int]error
	// block makes every RunPending wait for its release channel. A call listed
	// in ignoreCancels finishes on release even after its context is cancelled,
	// which models a remote command that has not noticed the cancellation yet.
	block         bool
	ignoreCancels map[int]bool

	inspectCalls int
	logCalls     int
	document     appsysmanager.DockerDocument
	documentErr  error

	runtimes   []appsysmanager.ContainerRuntime
	namespaces []string
	// capabilitiesSeeded makes Start report the seeded probe as belonging to
	// the Host currently being sampled.
	capabilitiesSeeded bool
}

type systemRunRecord struct {
	pending  appsysmanager.PendingAction
	release  chan struct{}
	err      error
	canceled bool
}

type systemStartCall struct {
	host     domainvault.Host
	tab      appsysmanager.Tab
	interval time.Duration
}

func newFakeSystemService() *fakeSystemService {
	return &fakeSystemService{
		snapshot: appsysmanager.Snapshot{
			HostID: "prod",
			Active: appsysmanager.TabOverview,
			Tabs:   make(map[appsysmanager.Tab]appsysmanager.TabState),
		},
		listeners: make(map[int]func()),
		document: appsysmanager.DockerDocument{
			Kind: appsysmanager.DocumentInspect, ContainerID: "c1",
			Text: `{"Id":"c1"}`,
		},
	}
}

func (f *fakeSystemService) Start(host domainvault.Host, tab appsysmanager.Tab,
	interval time.Duration) error {
	f.mu.Lock()
	f.starts = append(f.starts, systemStartCall{host: host, tab: tab, interval: interval})
	f.snapshot.HostID = string(host.ID)
	f.snapshot.Active = tab
	if f.capabilitiesSeeded {
		f.snapshot.CapabilitiesHost = string(host.ID)
	}
	f.mu.Unlock()
	f.notify()
	return nil
}

func (f *fakeSystemService) Stop() {
	f.mu.Lock()
	f.stops++
	f.mu.Unlock()
}

func (f *fakeSystemService) Refresh() {
	f.mu.Lock()
	f.refreshes++
	f.mu.Unlock()
	f.notify()
}

func (f *fakeSystemService) Snapshot() appsysmanager.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := f.snapshot
	copied.Tabs = make(map[appsysmanager.Tab]appsysmanager.TabState, len(f.snapshot.Tabs))
	for tab, state := range f.snapshot.Tabs {
		copied.Tabs[tab] = state
	}
	copied.Capabilities = f.snapshot.Capabilities
	return copied
}

func (f *fakeSystemService) Subscribe(fn func()) func() {
	f.mu.Lock()
	id := f.nextID
	f.nextID++
	f.listeners[id] = fn
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.listeners, id)
		f.mu.Unlock()
	}
}

func (f *fakeSystemService) notify() {
	f.mu.Lock()
	listeners := make([]func(), 0, len(f.listeners))
	for _, fn := range f.listeners {
		listeners = append(listeners, fn)
	}
	f.mu.Unlock()
	for _, fn := range listeners {
		fn()
	}
}

func (f *fakeSystemService) RunPending(ctx context.Context,
	pending appsysmanager.PendingAction) (appsysmanager.ActionResult, error) {
	f.mu.Lock()
	index := len(f.runs)
	record := &systemRunRecord{pending: pending, release: make(chan struct{})}
	f.runs = append(f.runs, record)
	err, hasIndexed := f.runErrs[index]
	if !hasIndexed {
		err = f.runErr
		if f.runErrOnce {
			f.runErr = nil
			f.runErrOnce = false
		}
	}
	block, ignoreCancel := f.block, f.ignoreCancels[index]
	f.mu.Unlock()
	result := appsysmanager.ActionResult{Kind: pending.Kind, Command: pending.Command,
		Stdout: "ok", HasExitCode: true}
	if block {
		if ignoreCancel {
			<-record.release
		} else {
			select {
			case <-record.release:
			case <-ctx.Done():
				f.mu.Lock()
				f.runs[index].canceled = true
				f.mu.Unlock()
				return result, ctx.Err()
			}
		}
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

// setRunError makes the call at index return err after its release, so one
// action can fail while an earlier one succeeds.
func (f *fakeSystemService) setRunError(index int, err error) {
	f.mu.Lock()
	if f.runErrs == nil {
		f.runErrs = make(map[int]error)
	}
	f.runErrs[index] = err
	f.mu.Unlock()
}

// setRunBlocking makes every confirmed action wait for releaseRun.
func (f *fakeSystemService) setRunBlocking() {
	f.mu.Lock()
	f.block = true
	f.mu.Unlock()
}

// setRunIgnoreCancel makes the call at index finish on release even after its
// context is cancelled.
func (f *fakeSystemService) setRunIgnoreCancel(index int) {
	f.mu.Lock()
	if f.ignoreCancels == nil {
		f.ignoreCancels = make(map[int]bool)
	}
	f.ignoreCancels[index] = true
	f.mu.Unlock()
}

// releaseRun lets a blocked call return. Its context may already be cancelled.
func (f *fakeSystemService) releaseRun(index int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < len(f.runs) {
		close(f.runs[index].release)
	}
}

func (f *fakeSystemService) runCanceled(index int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return index < len(f.runs) && f.runs[index].canceled
}

func (f *fakeSystemService) ContainerInspect(ctx context.Context, host domainvault.Host,
	runtime appsysmanager.ContainerRuntime, id string) (appsysmanager.DockerDocument, error) {
	f.mu.Lock()
	f.inspectCalls++
	document, err := f.document, f.documentErr
	f.mu.Unlock()
	return document, err
}

func (f *fakeSystemService) ContainerLogs(ctx context.Context, host domainvault.Host,
	runtime appsysmanager.ContainerRuntime, id string, tail int) (appsysmanager.DockerDocument, error) {
	f.mu.Lock()
	f.logCalls++
	document, err := f.document, f.documentErr
	document.Kind = appsysmanager.DocumentLogs
	f.mu.Unlock()
	return document, err
}

func (f *fakeSystemService) SetContainerRuntime(runtime appsysmanager.ContainerRuntime) {
	f.mu.Lock()
	f.runtimes = append(f.runtimes, runtime)
	f.snapshot.ContainerRuntime = runtime
	f.mu.Unlock()
}

func (f *fakeSystemService) SetKubernetesNamespace(namespace string) error {
	f.mu.Lock()
	f.namespaces = append(f.namespaces, namespace)
	f.mu.Unlock()
	return nil
}

func (f *fakeSystemService) Close() {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()
}

func (f *fakeSystemService) seedCapabilities(caps appsysmanager.Capabilities) {
	f.mu.Lock()
	f.snapshot.CapabilitiesHost = "prod"
	f.snapshot.Capabilities = caps
	f.capabilitiesSeeded = true
	f.mu.Unlock()
}

func (f *fakeSystemService) seedTab(tab appsysmanager.Tab, state appsysmanager.TabState) {
	f.mu.Lock()
	state.Tab = tab
	f.snapshot.Tabs[tab] = state
	f.mu.Unlock()
}

func (f *fakeSystemService) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func (f *fakeSystemService) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops
}

func (f *fakeSystemService) refreshCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshes
}

func (f *fakeSystemService) inspectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inspectCalls
}

func (f *fakeSystemService) logCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logCalls
}

func (f *fakeSystemService) lastStart(t *testing.T) systemStartCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) == 0 {
		t.Fatal("the shared System Manager runtime was never asked to sample")
	}
	return f.starts[len(f.starts)-1]
}

func (f *fakeSystemService) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

func (f *fakeSystemService) lastRun(t *testing.T) appsysmanager.PendingAction {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runs) == 0 {
		t.Fatal("no confirmed action was executed")
	}
	return f.runs[len(f.runs)-1].pending
}

// commandOpener records a Session opened with a startup command, so the tmux
// attach and Pod terminal paths have an observable destination.
func (o *fakeOpener) OpenHostWithCommand(_ context.Context, host domainvault.Host,
	command string) (SessionHandle, error) {
	o.mu.Lock()
	o.hosts = append(o.hosts, host)
	o.startupCommands = append(o.startupCommands, command)
	output := o.nextOutputLocked()
	o.mu.Unlock()
	handle := newFakeHandle(output)
	o.mu.Lock()
	o.handles = append(o.handles, handle)
	o.mu.Unlock()
	return handle, nil
}

func (o *fakeOpener) lastStartupCommand() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.startupCommands) == 0 {
		return ""
	}
	return o.startupCommands[len(o.startupCommands)-1]
}

func newSystemFixture(t *testing.T, service *fakeSystemService) (*phoneFixture, *fakeOpener) {
	t.Helper()
	opener := &fakeOpener{output: "remote prompt $ "}
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener,
		WithScreenFactory(fakeScreens),
		WithSystemManager(func() SystemService { return service }))
	t.Cleanup(fixture.shell.Close)
	return fixture, opener
}

// TestHostDetailOpensSystemManagerWithThatHost covers the Host-detail entry:
// an eligible Host opens the page with itself as the target, and a Host
// without an SSH shell hides the entry with a reason.
func TestHostDetailOpensSystemManagerWithThatHost(t *testing.T) {
	service := newFakeSystemService()
	fixture, _ := newSystemFixture(t, service)

	fixture.await(t, "the Hosts list", func() bool {
		return fixture.tester.HasText("prod box") && fixture.tester.HasText("console")
	})
	if err := fixture.tester.Click("Details for prod box"); err != nil {
		t.Fatalf("open Host detail: %v", err)
	}
	fixture.settle()
	if err := fixture.tester.Click("Open System management for prod box"); err != nil {
		t.Fatalf("open System management: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.settle()
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod/system" {
		t.Fatalf("Hosts path = %q, want /host/prod/system", got)
	}
	if !fixture.tester.HasText("System · prod box") {
		t.Fatalf("the page did not name its Host; texts: %q", fixture.tester.Texts())
	}
	start := service.lastStart(t)
	if start.host.ID != "prod" {
		t.Fatalf("target Host = %q, want prod", start.host.ID)
	}

	// A Telnet Host has no SSH shell: the entry is absent with a reason.
	// Back leaves the System page for the detail page, then the list.
	fixture.shell.Router(Hosts).Back()
	fixture.shell.Router(Hosts).Back()
	fixture.settle()
	if err := fixture.tester.Click("Details for console"); err != nil {
		t.Fatalf("open the Telnet Host's detail: %v", err)
	}
	fixture.settle()
	if fixture.tester.HasText("Open System management for console") {
		t.Fatalf("the Telnet Host offered System management; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("This Host has no SSH shell for system management.") {
		t.Fatalf("the ineligible Host did not state a reason; texts: %q", fixture.tester.Texts())
	}
}

// TestSessionToolsOpensSystemManagerWithTheSessionHost covers the Session
// Tools entry and its target.
func TestSessionToolsOpensSystemManagerWithTheSessionHost(t *testing.T) {
	service := newFakeSystemService()
	fixture, _ := newSystemFixture(t, service)
	fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal", func() bool {
		return fixture.tester.HasText("remote prompt $")
	})
	fixture.openToolsPanel(t, "prod box")
	if err := fixture.tester.Click("Open System management"); err != nil {
		t.Fatalf("open System management: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("System · prod box") {
		t.Fatalf("the tool did not draw the System Manager; texts: %q", fixture.tester.Texts())
	}
	if start := service.lastStart(t); start.host.ID != "prod" {
		t.Fatalf("target Host = %q, want prod", start.host.ID)
	}

	// All tools returns to the catalog without closing the page's Session.
	if err := fixture.tester.Click("All tools"); err != nil {
		t.Fatalf("return to the catalog: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("Command history") {
		t.Fatalf("the catalog did not return; texts: %q", fixture.tester.Texts())
	}
}

// TestSystemManagerHidesTheToolForAnIneligibleSessionHost covers the second
// eligibility rule: a Telnet Session lists no System management row and says
// why.
func TestSystemManagerHidesTheToolForAnIneligibleSessionHost(t *testing.T) {
	service := newFakeSystemService()
	fixture, _ := newSystemFixture(t, service)
	fixture.openSession(t, "console")
	fixture.await(t, "the terminal", func() bool {
		return fixture.tester.HasText("remote prompt $")
	})
	fixture.openToolsPanel(t, "console")
	if fixture.tester.HasText("System management") {
		t.Fatalf("the Telnet Session offered System management; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("This Host has no SSH shell for system management.") {
		t.Fatalf("the ineligible Session Host did not state a reason; texts: %q", fixture.tester.Texts())
	}
}

// TestSystemManagerShowsCapabilitiesAndOperatesDockerTmuxAndPods covers the
// capability summary and the three remote-tool paths: Docker inspect/logs,
// tmux attach, and a Pod terminal.
func TestSystemManagerShowsCapabilitiesAndOperatesDockerTmuxAndPods(t *testing.T) {
	service := newFakeSystemService()
	service.seedCapabilities(appsysmanager.Capabilities{
		OS: "linux", HasTmux: true, HasDocker: true, HasKubectl: true,
	})
	service.seedTab(appsysmanager.TabDocker, appsysmanager.TabState{
		Status: appsysmanager.StatusLive,
		Containers: []appsysmanager.Container{
			{ID: "c1", Name: "web", Image: "nginx", State: "running"},
		},
	})
	service.seedTab(appsysmanager.TabTmux, appsysmanager.TabState{
		Status:   appsysmanager.StatusLive,
		Sessions: []appsysmanager.TmuxSession{{Name: "work", Windows: 2}},
	})
	service.seedTab(appsysmanager.TabKubernetes, appsysmanager.TabState{
		Status: appsysmanager.StatusLive,
		Kubernetes: appsysmanager.Kubernetes{
			Namespaces: []string{"default"},
			Pods: []appsysmanager.Pod{{
				Name: "api-1", Namespace: "default", Status: "Running",
				Containers: []string{"api"}, RunningContainers: []string{"api"},
			}},
		},
	})
	fixture, opener := newSystemFixture(t, service)
	if err := fixture.tester.Click("Details for prod box"); err != nil {
		t.Fatalf("open Host detail: %v", err)
	}
	fixture.settle()
	if err := fixture.tester.Click("Open System management for prod box"); err != nil {
		t.Fatalf("open System management: %v", err)
	}
	fixture.settle()
	if !fixture.tester.HasText("linux · detected: tmux, docker, kubectl") {
		t.Fatalf("capability summary missing; texts: %q", fixture.tester.Texts())
	}

	// Docker inspect and logs are read-only and leave the containers list.
	if err := fixture.tester.Click("Docker"); err != nil {
		t.Fatalf("open the Docker tab: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("web") {
		t.Fatalf("the container list is missing; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Inspect"); err != nil {
		t.Fatalf("inspect the container: %v", err)
	}
	fixture.await(t, "the inspect document", func() bool {
		return service.inspectCount() == 1 && fixture.tester.HasText(`{"Id":"c1"}`)
	})
	if err := fixture.tester.Click("Logs"); err != nil {
		t.Fatalf("load the container logs: %v", err)
	}
	fixture.await(t, "the log document", func() bool {
		return service.logCount() == 1 && fixture.tester.HasText("Logs · c1")
	})

	// tmux attach opens a Session that types the attach command.
	if err := fixture.tester.Click("tmux"); err != nil {
		t.Fatalf("open the tmux tab: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Attach"); err != nil {
		t.Fatalf("attach the session: %v", err)
	}
	fixture.await(t, "the tmux Session", func() bool {
		return strings.Contains(opener.lastStartupCommand(), "tmux attach")
	})

	// Attaching moved the phone to the new Session. Returning to the Hosts
	// section shows the System page the router kept underneath.
	fixture.shell.Select(Hosts)
	fixture.settle()
	if !fixture.tester.HasText("System · prod box") {
		t.Fatalf("the System page did not return; texts: %q", fixture.tester.Texts())
	}

	// A Pod terminal does the same with kubectl exec.
	if err := fixture.tester.Click("Kubernetes"); err != nil {
		t.Fatalf("open the Kubernetes tab: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Terminal"); err != nil {
		t.Fatalf("open the Pod terminal: %v", err)
	}
	fixture.await(t, "the Pod Session", func() bool {
		command := opener.lastStartupCommand()
		return strings.Contains(command, "kubectl") && strings.Contains(command, "exec")
	})
}

// TestDangerousActionsWaitForConfirmation covers the confirmation gate: a
// destructive tap only records a pending action, Cancel runs nothing, and a
// Confirm executes exactly the approved command.
func TestDangerousActionsWaitForConfirmation(t *testing.T) {
	service := newFakeSystemService()
	service.seedCapabilities(appsysmanager.Capabilities{OS: "linux", HasDocker: true})
	service.seedTab(appsysmanager.TabDocker, appsysmanager.TabState{
		Status: appsysmanager.StatusLive,
		Containers: []appsysmanager.Container{
			{ID: "c1", Name: "web", Image: "nginx", State: "running"},
		},
	})
	fixture, _ := newSystemFixture(t, service)
	fixture.openSystemFromHostDetail(t, "prod box")
	if err := fixture.tester.Click("Docker"); err != nil {
		t.Fatalf("open the Docker tab: %v", err)
	}
	fixture.tester.Frame()

	if err := fixture.tester.Click("Stop"); err != nil {
		t.Fatalf("request Stop: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("Stop container web?") {
		t.Fatalf("the confirmation did not name the target; texts: %q", fixture.tester.Texts())
	}
	if got := service.runCount(); got != 0 {
		t.Fatalf("%d commands ran before confirmation, want 0", got)
	}
	if err := fixture.tester.Click("Cancel"); err != nil {
		t.Fatalf("cancel the confirmation: %v", err)
	}
	fixture.tester.Frame()
	if got := service.runCount(); got != 0 {
		t.Fatalf("%d commands ran after Cancel, want 0", got)
	}

	if err := fixture.tester.Click("Stop"); err != nil {
		t.Fatalf("request Stop again: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Confirm"); err != nil {
		t.Fatalf("confirm the action: %v", err)
	}
	fixture.await(t, "the confirmed command", func() bool { return service.runCount() == 1 })
	pending := service.lastRun(t)
	if pending.Kind != appsysmanager.ActionDockerStop || pending.ContainerID != "c1" {
		t.Fatalf("confirmed action = %+v, want docker stop c1", pending)
	}
}

// TestPermissionFailureShowsReasonAndRetries covers the permission path: a
// denied sample and a denied action each state the reason and keep a Retry
// that continues after the Host is fixed.
func TestPermissionFailureShowsReasonAndRetries(t *testing.T) {
	service := newFakeSystemService()
	service.seedCapabilities(appsysmanager.Capabilities{
		OS: "linux", HasPs: true, HasDocker: true,
	})
	service.seedTab(appsysmanager.TabProcesses, appsysmanager.TabState{
		Status: appsysmanager.StatusError, Reason: appsysmanager.ReasonPermission,
	})
	service.seedTab(appsysmanager.TabDocker, appsysmanager.TabState{
		Status: appsysmanager.StatusLive,
		Containers: []appsysmanager.Container{
			{ID: "c1", Name: "web", Image: "nginx", State: "running"},
		},
	})
	service.runErr = &appsysmanager.Error{Reason: appsysmanager.ReasonPermission,
		Cause: errors.New("permission denied")}
	service.runErrOnce = true
	fixture, _ := newSystemFixture(t, service)
	fixture.openSystemFromHostDetail(t, "prod box")

	if err := fixture.tester.Click("Processes"); err != nil {
		t.Fatalf("open the Processes tab: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("Permission denied. Fix the Host's permissions, then Retry.") {
		t.Fatalf("the permission reason is missing; texts: %q", fixture.tester.Texts())
	}
	before := service.refreshCount()
	if err := fixture.tester.Click("Retry"); err != nil {
		t.Fatalf("retry the sample: %v", err)
	}
	fixture.tester.Frame()
	if after := service.refreshCount(); after <= before {
		t.Fatalf("Retry did not ask for a fresh sample (%d -> %d)", before, after)
	}

	// The confirmed action is denied, then retried after the fix.
	if err := fixture.tester.Click("Docker"); err != nil {
		t.Fatalf("open the Docker tab: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Stop"); err != nil {
		t.Fatalf("request Stop: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Confirm"); err != nil {
		t.Fatalf("confirm the action: %v", err)
	}
	fixture.await(t, "the denied action", func() bool {
		return service.runCount() == 1 && fixture.tester.HasText(
			"The action failed: Permission denied. Fix the Host's permissions, then Retry.")
	})
	if err := fixture.tester.Click("Retry"); err != nil {
		t.Fatalf("retry the action: %v", err)
	}
	fixture.await(t, "the retried action", func() bool { return service.runCount() == 2 })
	if got := service.lastRun(t).Kind; got != appsysmanager.ActionDockerStop {
		t.Fatalf("retried kind = %q, want docker stop", got)
	}
}

// TestSystemManagerKeepsTheSelectedTabAcrossSuspend covers the spec's rule
// that a suspended app resumes each tool on the page it was left on: the
// sampler stops in the background and restarts the same tab on return.
func TestSystemManagerKeepsTheSelectedTabAcrossSuspend(t *testing.T) {
	service := newFakeSystemService()
	service.seedCapabilities(appsysmanager.Capabilities{OS: "linux", HasDocker: true})
	service.seedTab(appsysmanager.TabDocker, appsysmanager.TabState{
		Status: appsysmanager.StatusLive,
		Containers: []appsysmanager.Container{
			{ID: "c1", Name: "web", Image: "nginx", State: "running"},
		},
	})
	fixture, _ := newSystemFixture(t, service)
	fixture.openSystemFromHostDetail(t, "prod box")
	if err := fixture.tester.Click("Docker"); err != nil {
		t.Fatalf("open the Docker tab: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("web") {
		t.Fatalf("the Docker rows are missing before suspend; texts: %q", fixture.tester.Texts())
	}

	stops := service.stopCount()
	fixture.shell.Background()
	fixture.tester.Frame()
	if got := service.stopCount(); got <= stops {
		t.Fatalf("background did not stop the sampler (%d -> %d)", stops, got)
	}
	fixture.shell.Foreground()
	fixture.settle()
	if !fixture.tester.HasText("System · prod box") || !fixture.tester.HasText("web") {
		t.Fatalf("the page did not resume on its Docker tab; texts: %q", fixture.tester.Texts())
	}
	start := service.lastStart(t)
	if start.tab != appsysmanager.TabDocker {
		t.Fatalf("resumed tab = %q, want Docker", start.tab)
	}
}

// TestSystemManagerStatesWhenTheRuntimeFactoryReturnsNil covers a miswired
// build: the entry opens, but the page says why it cannot start instead of
// waiting forever on a sampler that does not exist.
func TestSystemManagerStatesWhenTheRuntimeFactoryReturnsNil(t *testing.T) {
	opener := &fakeOpener{output: "remote prompt $ "}
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener,
		WithScreenFactory(fakeScreens),
		WithSystemManager(func() SystemService { return nil }))
	t.Cleanup(fixture.shell.Close)
	if err := fixture.tester.Click("Details for prod box"); err != nil {
		t.Fatalf("open Host detail: %v", err)
	}
	fixture.settle()
	if err := fixture.tester.Click("Open System management for prod box"); err != nil {
		t.Fatalf("open System management: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.settle()
	if !fixture.tester.HasText("System management could not start in this build.") {
		t.Fatalf("the empty runtime did not state a reason; texts: %q", fixture.tester.Texts())
	}
}

// TestSystemManagerStaleActionOutcomeCannotClearTheNextHostsAction reproduces a confirmed
// action from Host A finishing after the page has moved to Host B. The stale
// outcome must be discarded: B stays busy, a concurrent request is rejected,
// and Close still cancels B instead of letting it run to the transport
// timeout.
func TestSystemManagerStaleActionOutcomeCannotClearTheNextHostsAction(t *testing.T) {
	service := newFakeSystemService()
	service.seedCapabilities(appsysmanager.Capabilities{OS: "linux", HasDocker: true})
	service.seedTab(appsysmanager.TabDocker, appsysmanager.TabState{
		Status: appsysmanager.StatusLive,
		Containers: []appsysmanager.Container{
			{ID: "c1", Name: "web", Image: "nginx", State: "running"},
		},
	})
	service.setRunBlocking()
	service.setRunIgnoreCancel(0)
	opener := &fakeOpener{output: "remote prompt $ "}
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener,
		WithScreenFactory(fakeScreens),
		WithSystemManager(func() SystemService { return service }))
	var closeOnce sync.Once
	closeShell := func() { closeOnce.Do(fixture.shell.Close) }
	t.Cleanup(closeShell)

	fixture.openSystemFromHostDetail(t, "prod box")
	startConfirmedDockerStop(t, fixture)
	fixture.await(t, "Host A's action", func() bool { return service.runCount() == 1 })

	// Switching Hosts cancels A and retargets the page at the Mosh Host B.
	fixture.switchSystemHost(t, "udp box")
	startConfirmedDockerStop(t, fixture)
	fixture.await(t, "Host B's action", func() bool { return service.runCount() == 2 })

	service.releaseRun(0)
	waitForQueuedActions(t, fixture.shell.system, 1)
	fixture.tester.Frame()
	if !fixture.tester.HasText("Running the confirmed command…") {
		t.Fatalf("A's stale outcome finished B's action; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Stop"); err != nil {
		t.Fatalf("request a concurrent action: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("an action is already running") {
		t.Fatalf("a concurrent action was admitted; texts: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText("Stop container web?") {
		t.Fatalf("a concurrent confirmation was armed; texts: %q", fixture.tester.Texts())
	}
	if got := service.runCount(); got != 2 {
		t.Fatalf("concurrent request started %d runs, want 2", got)
	}

	// Close must still reach B's cancel function.
	closeShell()
	deadline := time.Now().Add(5 * time.Second)
	for !service.runCanceled(1) {
		if time.Now().After(deadline) {
			t.Fatal("Close did not cancel the running action")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSystemManagerStaleActionOutcomeKeepsTheNewHostsRetryTarget is the failure variant of
// the same race: B is denied after A's stale success lands, and B's approved
// target must still be there for Retry.
func TestSystemManagerStaleActionOutcomeKeepsTheNewHostsRetryTarget(t *testing.T) {
	service := newFakeSystemService()
	service.seedCapabilities(appsysmanager.Capabilities{OS: "linux", HasDocker: true})
	service.seedTab(appsysmanager.TabDocker, appsysmanager.TabState{
		Status: appsysmanager.StatusLive,
		Containers: []appsysmanager.Container{
			{ID: "c1", Name: "web", Image: "nginx", State: "running"},
		},
	})
	service.setRunBlocking()
	service.setRunIgnoreCancel(0)
	service.setRunError(1, &appsysmanager.Error{Reason: appsysmanager.ReasonPermission,
		Cause: errors.New("permission denied")})
	opener := &fakeOpener{output: "remote prompt $ "}
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener,
		WithScreenFactory(fakeScreens),
		WithSystemManager(func() SystemService { return service }))
	t.Cleanup(fixture.shell.Close)

	fixture.openSystemFromHostDetail(t, "prod box")
	startConfirmedDockerStop(t, fixture)
	fixture.await(t, "Host A's action", func() bool { return service.runCount() == 1 })
	fixture.switchSystemHost(t, "udp box")
	startConfirmedDockerStop(t, fixture)
	fixture.await(t, "Host B's action", func() bool { return service.runCount() == 2 })

	service.releaseRun(0)
	waitForQueuedActions(t, fixture.shell.system, 1)
	service.releaseRun(1)
	waitForQueuedActions(t, fixture.shell.system, 2)
	fixture.tester.Frame()
	if !fixture.tester.HasText(
		"The action failed: Permission denied. Fix the Host's permissions, then Retry.") {
		t.Fatalf("B's failure is missing; texts: %q", fixture.tester.Texts())
	}
	if !hasExactText(fixture.tester.Texts(), "Retry") {
		t.Fatalf("A's stale outcome discarded B's retry target; texts: %q", fixture.tester.Texts())
	}
}

// startConfirmedDockerStop requests Stop and confirms it, leaving the action
// running (the fake blocks) and the page on the Docker tab.
func startConfirmedDockerStop(t *testing.T, f *phoneFixture) {
	t.Helper()
	if err := f.tester.Click("Docker"); err != nil {
		t.Fatalf("open the Docker tab: %v", err)
	}
	f.tester.Frame()
	if err := f.tester.Click("Stop"); err != nil {
		t.Fatalf("request Stop: %v; texts: %q", err, f.tester.Texts())
	}
	f.tester.Frame()
	if err := f.tester.Click("Confirm"); err != nil {
		t.Fatalf("confirm Stop: %v; texts: %q", err, f.tester.Texts())
	}
}

// switchSystemHost leaves from's System page for the Hosts list and opens
// target's System page.
func (f *phoneFixture) switchSystemHost(t *testing.T, target string) {
	t.Helper()
	f.shell.Router(Hosts).Back()
	f.shell.Router(Hosts).Back()
	f.settle()
	f.openSystemFromHostDetail(t, target)
	if !f.tester.HasText("System · " + target) {
		t.Fatalf("the System page did not retarget to %s; texts: %q", target, f.tester.Texts())
	}
}

// waitForQueuedActions waits until the shell has queued want finished action
// outcomes. It deliberately draws no frame, because a frame would drain them.
func waitForQueuedActions(t *testing.T, page *systemPages, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		page.mu.Lock()
		queued := len(page.actions)
		page.mu.Unlock()
		if queued >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d queued action outcome(s), got %d", want, queued)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSystemManagerUnavailableTabStatesTheReason covers the capability filter:
// a Host with no container engine shows the reason and no Docker rows, even if
// the snapshot still carries one.
func TestSystemManagerUnavailableTabStatesTheReason(t *testing.T) {
	service := newFakeSystemService()
	service.seedCapabilities(appsysmanager.Capabilities{OS: "linux", HasPs: true})
	service.seedTab(appsysmanager.TabDocker, appsysmanager.TabState{
		Status: appsysmanager.StatusLive,
		Containers: []appsysmanager.Container{
			{ID: "c1", Name: "web", Image: "nginx", State: "running"},
		},
	})
	fixture, _ := newSystemFixture(t, service)
	fixture.openSystemFromHostDetail(t, "prod box")
	if err := fixture.tester.Click("Docker"); err != nil {
		t.Fatalf("open the Docker tab: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("No container engine is installed on this Host.") {
		t.Fatalf("the unavailable reason is missing; texts: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText("Container web") || fixture.tester.HasText("Stop") {
		t.Fatalf("an action was offered for an unavailable engine; texts: %q", fixture.tester.Texts())
	}
}

// TestSystemManagerStatesTheMissingRuntimeOnBothEntries covers a build without
// the shared runtime: Host detail and Session Tools both state the reason.
func TestSystemManagerStatesTheMissingRuntimeOnBothEntries(t *testing.T) {
	opener := &fakeOpener{output: "remote prompt $ "}
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener,
		WithScreenFactory(fakeScreens))
	t.Cleanup(fixture.shell.Close)
	fixture.await(t, "the Hosts list", func() bool { return fixture.tester.HasText("prod box") })
	if err := fixture.tester.Click("Details for prod box"); err != nil {
		t.Fatalf("open Host detail: %v", err)
	}
	fixture.settle()
	if !fixture.tester.HasText("System management is not available in this build.") {
		t.Fatalf("Host detail omitted the build reason; texts: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText("Open System management for prod box") {
		t.Fatalf("Host detail offered an entry without its runtime; texts: %q", fixture.tester.Texts())
	}

	fixture.shell.Router(Hosts).Back()
	fixture.settle()
	fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal", func() bool { return fixture.tester.HasText("remote prompt $") })
	fixture.openToolsPanel(t, "prod box")
	if !fixture.tester.HasText("Open System management") ||
		!fixture.tester.HasText("Unavailable") {
		t.Fatalf("Session Tools omitted the unavailable entry; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("System management is not available in this build.") {
		t.Fatalf("Session Tools omitted the build reason; texts: %q", fixture.tester.Texts())
	}
}

// TestMoshHostOpensSystemManagerWithItsCapabilities documents the eligibility
// rule: Mosh bootstraps through SSH, so UsesSSHAuth (the desktop's rule) makes
// it eligible. Its page opens and reports the probe result from the shared
// runtime rather than being hidden as a non-SSH Host.
func TestMoshHostOpensSystemManagerWithItsCapabilities(t *testing.T) {
	service := newFakeSystemService()
	service.seedCapabilities(appsysmanager.Capabilities{OS: "linux", HasPs: true})
	fixture, _ := newSystemFixture(t, service)
	fixture.await(t, "the Hosts list", func() bool { return fixture.tester.HasText("udp box") })
	if err := fixture.tester.Click("Details for udp box"); err != nil {
		t.Fatalf("open the Mosh Host's detail: %v", err)
	}
	fixture.settle()
	if fixture.tester.HasText("This Host has no SSH shell for system management.") {
		t.Fatalf("the Mosh Host was treated as ineligible; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Open System management for udp box"); err != nil {
		t.Fatalf("open System management for the Mosh Host: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.settle()
	if !fixture.tester.HasText("System · udp box") {
		t.Fatalf("the Mosh Host's page did not open; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("linux · detected: ps") {
		t.Fatalf("the probe result was not shown; texts: %q", fixture.tester.Texts())
	}
	start := service.lastStart(t)
	if start.host.Protocol != domainvault.ProtocolMosh {
		t.Fatalf("sampled protocol = %q, want Mosh", start.host.Protocol)
	}
}

// openSystemFromHostDetail opens prod box's detail and pushes its System
// Manager page, leaving the tester on the page.
func (f *phoneFixture) openSystemFromHostDetail(t *testing.T, label string) {
	t.Helper()
	// The Hosts section is already selected by the fixture.
	if err := f.tester.Click("Details for " + label); err != nil {
		t.Fatalf("open Host detail: %v; texts: %q", err, f.tester.Texts())
	}
	f.settle()
	if err := f.tester.Click("Open System management for " + label); err != nil {
		t.Fatalf("open System management: %v; texts: %q", err, f.tester.Texts())
	}
	f.settle()
}
