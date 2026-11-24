// Phone System Manager.
//
// The page inspects one eligible Host and drives its remote system tools. It
// reuses application/sysmanager/core — the same capability probe, tab
// sampling, Docker inspect/logs, tmux commands, Pod terminal command,
// permission classification, and confirmed-action executor the desktop
// surface uses — over the phone's own Connection Manager. The shared core is
// pure Go and imports no desktop package, which is what lets the phone link it
// without the desktop UI or the application's traditional package path.
//
// A destructive action never runs on the tap. It becomes a PendingAction shown
// by a confirmation bar; only Confirm opens the Host lease and runs the shared
// command. A permission failure keeps the target and a Retry entry, so a user
// who fixes the Host's permission can continue without re-selecting anything.

package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager/core"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// SystemService is the phone System Manager's seam onto the shared system
// manager use cases. application/sysmanager/core.Service implements it; tests
// provide a fake.
type SystemService interface {
	Start(host domainvault.Host, tab appsysmanager.Tab, interval time.Duration) error
	Stop()
	Refresh()
	Snapshot() appsysmanager.Snapshot
	Subscribe(func()) func()
	RunPending(ctx context.Context, pending appsysmanager.PendingAction) (appsysmanager.ActionResult, error)
	ContainerInspect(ctx context.Context, host domainvault.Host,
		runtime appsysmanager.ContainerRuntime, id string) (appsysmanager.DockerDocument, error)
	ContainerLogs(ctx context.Context, host domainvault.Host,
		runtime appsysmanager.ContainerRuntime, id string, tail int) (appsysmanager.DockerDocument, error)
	SetContainerRuntime(runtime appsysmanager.ContainerRuntime)
	SetKubernetesNamespace(namespace string) error
	Close()
}

// SystemProvider is the phone Session runtime's System Manager seam. Each
// surface owns one service, so a page that closes releases every lease it
// opened instead of sharing sampling demand with another page.
type SystemProvider interface {
	NewSystemService() SystemService
}

// WithSystemManager gives the Host detail pages and the Session Tools surface
// a factory for the shared system-manager runtime. Without it the entry is
// absent and the tool states that this build has no System Manager.
func WithSystemManager(factory func() SystemService) Option {
	return func(s *Shell) { s.systemFactory = factory }
}

// systemState is the page's own view state. The shared service owns the
// snapshot, capabilities, and tab data; everything here is a choice the page
// keeps between frames.
type systemState struct {
	host    domainvault.Host
	hostSet bool

	tab        appsysmanager.Tab
	search     string
	selected   int
	dockerView int

	containerRuntime    appsysmanager.ContainerRuntime
	kubernetesNamespace string
	tmuxName            string

	snapshot appsysmanager.Snapshot

	active bool

	pending      *appsysmanager.PendingAction
	actionBusy   bool
	actionDone   bool
	actionErr    error
	actionResult appsysmanager.ActionResult
	// lastPending is the confirmed action kept after a failure so a Retry
	// re-runs exactly what the user approved.
	lastPending  *appsysmanager.PendingAction
	actionCancel context.CancelFunc
	// actionGen and actionHost tag the action this state is waiting for. An
	// outcome from another Host, or from an action this state has replaced, is
	// discarded: it must not clear a newer action's busy flag, retry target, or
	// cancel function.
	actionGen  uint64
	actionHost string

	document        appsysmanager.DockerDocument
	documentOn      bool
	documentBusy    bool
	documentErr     error
	documentGen     uint64
	documentHost    domainvault.Host
	documentID      string
	docCancel       context.CancelFunc
	documentKind    appsysmanager.DocumentKind
	documentRuntime appsysmanager.ContainerRuntime

	problem string
	// serviceProblem is set when this build offers the entry but its runtime
	// factory returned no service. The page states the reason instead of
	// waiting forever on a sampler that does not exist.
	serviceProblem string
}

type systemActionOutcome struct {
	gen    uint64
	hostID string
	result appsysmanager.ActionResult
	err    error
}

type systemDocumentOutcome struct {
	gen      uint64
	document appsysmanager.DockerDocument
	err      error
}

// systemPages owns one System Manager surface. The Host detail route and the
// Session Tools overlay share the shell's instance because only one of them is
// on screen at a time; opening for another Host retargets the same service and
// discards the previous Host's rows.
type systemPages struct {
	factory  func() SystemService
	terminal func(domainvault.Host, string) error
	redraw   func()

	service SystemService
	sub     func()

	state systemState

	mu        sync.Mutex
	actions   []systemActionOutcome
	documents []systemDocumentOutcome
	actionSeq uint64
	closed    bool
}

func newSystemPages(factory func() SystemService, terminal func(domainvault.Host, string) error,
	redraw func()) *systemPages {
	return &systemPages{factory: factory, terminal: terminal, redraw: redraw}
}

// available reports whether this build wired the shared runtime.
func (p *systemPages) available() bool {
	return p != nil && p.factory != nil
}

// openFor targets host. A new Host resets the tab, search, selection,
// confirmation, and document, because rows and commands belong to one Host.
func (p *systemPages) openFor(host domainvault.Host) {
	if !p.available() || host.ID == "" {
		return
	}
	if p.service == nil {
		p.service = p.factory()
		if p.service == nil {
			p.state.serviceProblem = "System management could not start in this build."
			return
		}
		p.sub = p.service.Subscribe(p.requestRedraw)
	}
	if p.state.hostSet && p.state.host.ID == host.ID {
		p.state.host = host
		return
	}
	wasActive := p.state.active
	p.stopWork()
	p.state = systemState{tab: appsysmanager.TabOverview, selected: -1, host: host, hostSet: true}
	if wasActive {
		p.start()
	}
}

// activate starts sampling while the page is on screen.
func (p *systemPages) activate() {
	if !p.available() || !p.state.hostSet {
		return
	}
	if p.state.active {
		return
	}
	p.state.active = true
	p.start()
}

// deactivate pauses sampling and cancels a pending or running action. A
// cancelled confirmation executes nothing; a running command is asked to stop.
func (p *systemPages) deactivate() {
	if !p.available() || !p.state.active {
		return
	}
	p.state.active = false
	p.stopWork()
}

func (p *systemPages) stopWork() {
	if p.state.pending != nil {
		p.state.pending = nil
	}
	if p.state.actionCancel != nil {
		p.state.actionCancel()
		p.state.actionCancel = nil
	}
	p.closeDocument()
	if p.service != nil {
		p.service.Stop()
	}
}

// close releases the service and every lease it owns.
func (p *systemPages) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	if p.sub != nil {
		p.sub()
		p.sub = nil
	}
	if p.state.actionCancel != nil {
		p.state.actionCancel()
	}
	if p.state.docCancel != nil {
		p.state.docCancel()
	}
	if p.service != nil {
		p.service.Stop()
		p.service.Close()
	}
}

func (p *systemPages) start() {
	if p.service == nil || !p.state.hostSet {
		return
	}
	_ = p.service.Start(p.state.host, p.state.tab, appsysmanager.DefaultInterval(p.state.tab))
}

func (p *systemPages) setTab(tab appsysmanager.Tab) {
	if tab == p.state.tab {
		return
	}
	p.state.tab = tab
	p.state.selected = -1
	p.state.search = ""
	p.state.problem = ""
	p.closeDocument()
	p.clearFinishedAction()
	p.start()
}

func (p *systemPages) clearFinishedAction() {
	if p.state.actionBusy {
		return
	}
	p.state.actionDone = false
	p.state.actionResult = appsysmanager.ActionResult{}
	p.state.actionErr = nil
	p.state.lastPending = nil
}

func (p *systemPages) requestRedraw() {
	if p.redraw != nil {
		p.redraw()
	}
}

// flush copies the shared snapshot and applies finished background work.
func (p *systemPages) flush() {
	if p.service != nil {
		snapshot := p.service.Snapshot()
		if p.state.hostSet && snapshot.HostID == string(p.state.host.ID) {
			p.state.snapshot = snapshot
		}
	}
	p.reconcileRuntime()
	p.mu.Lock()
	actions := p.actions
	p.actions = nil
	documents := p.documents
	p.documents = nil
	p.mu.Unlock()
	for _, outcome := range documents {
		if outcome.gen != p.state.documentGen || !p.state.documentOn {
			continue
		}
		p.state.documentBusy = false
		p.state.documentErr = outcome.err
		if outcome.err == nil {
			p.state.document = outcome.document
		}
	}
	for _, outcome := range actions {
		// Documents use the same rule: only the action this Host's page is
		// waiting for may finish it. A stale outcome is dropped whole, so it
		// cannot clear another action's busy flag, retry target, or cancel.
		if outcome.gen != p.state.actionGen ||
			outcome.hostID != p.state.actionHost {
			continue
		}
		p.state.actionBusy = false
		p.state.actionCancel = nil
		p.state.actionDone = true
		p.state.actionResult = outcome.result
		p.state.actionErr = outcome.err
		if outcome.err == nil {
			p.state.lastPending = nil
			if p.service != nil {
				p.service.Refresh()
			}
		}
	}
}

// reconcileRuntime aligns the engine and namespace with the Host's probe, so a
// Host that only has podman never runs a docker command.
func (p *systemPages) reconcileRuntime() {
	snapshot := p.state.snapshot
	if snapshot.CapabilitiesHost != string(p.state.host.ID) {
		return
	}
	runtimes := snapshot.Capabilities.ContainerRuntimes()
	effective := p.state.containerRuntime
	found := false
	for _, runtime := range runtimes {
		if runtime == effective {
			found = true
		}
	}
	if !found {
		effective = ""
		if len(runtimes) > 0 {
			effective = runtimes[0]
		}
	}
	if effective != p.state.containerRuntime {
		p.state.containerRuntime = effective
		if p.service != nil && effective != "" {
			p.service.SetContainerRuntime(effective)
		}
	}
}

// ---- actions -----------------------------------------------------------

// requestSignal prepares a process signal. Nothing runs before confirm.
func (p *systemPages) requestSignal(pid int, signal string) error {
	if p.state.actionBusy {
		return errors.New("an action is already running")
	}
	command, err := appsysmanager.SignalCommand(pid, signal)
	if err != nil {
		return err
	}
	pending := appsysmanager.PendingAction{
		Kind: appsysmanager.ActionSignal, Origin: appsysmanager.OriginProcess,
		Host: p.state.host, PID: pid, Signal: strings.ToUpper(signal), Command: command,
	}
	p.state.pending = &pending
	p.state.actionDone = false
	p.requestRedraw()
	return nil
}

// requestPortTerminate prepares SIGTERM for the process owning a socket.
func (p *systemPages) requestPortTerminate(port appsysmanager.Port) error {
	if p.state.actionBusy {
		return errors.New("an action is already running")
	}
	if port.PID <= 0 {
		return errors.New("the port has no process owner")
	}
	command, err := appsysmanager.SignalCommand(port.PID, "TERM")
	if err != nil {
		return err
	}
	pending := appsysmanager.PendingAction{
		Kind: appsysmanager.ActionSignal, Origin: appsysmanager.OriginPort,
		Host: p.state.host, PID: port.PID, Signal: "TERM", Command: command,
		Address: port.Address, Port: port.Port, Protocol: port.Protocol,
	}
	p.state.pending = &pending
	p.state.actionDone = false
	p.requestRedraw()
	return nil
}

// requestServiceAction prepares a systemd unit action.
func (p *systemPages) requestServiceAction(unit string, action appsysmanager.ServiceAction,
	scope appsysmanager.Scope) error {
	if p.state.actionBusy {
		return errors.New("an action is already running")
	}
	command, err := appsysmanager.ServiceCommand(unit, action, scope)
	if err != nil {
		return err
	}
	pending := appsysmanager.PendingAction{
		Kind: appsysmanager.ActionServiceAction, Host: p.state.host,
		Unit: unit, Action: action, Scope: scope, Command: command,
	}
	p.state.pending = &pending
	p.state.actionDone = false
	p.requestRedraw()
	return nil
}

// requestTmuxKill prepares killing one session.
func (p *systemPages) requestTmuxKill(session string) error {
	if p.state.actionBusy {
		return errors.New("an action is already running")
	}
	command, err := appsysmanager.TmuxKillCommand(session)
	if err != nil {
		return err
	}
	pending := appsysmanager.PendingAction{
		Kind: appsysmanager.ActionTmuxKill, Host: p.state.host,
		Session: session, Command: command,
	}
	p.state.pending = &pending
	p.state.actionDone = false
	p.requestRedraw()
	return nil
}

// requestTmuxCreate creates a detached session. Creating is not destructive,
// so it runs without a confirmation.
func (p *systemPages) requestTmuxCreate() error {
	if p.state.actionBusy {
		return errors.New("an action is already running")
	}
	name := strings.TrimSpace(p.state.tmuxName)
	command, err := appsysmanager.TmuxCreateCommand(name)
	if err != nil {
		p.state.problem = err.Error()
		p.requestRedraw()
		return err
	}
	p.state.tmuxName = ""
	p.startAction(appsysmanager.PendingAction{
		Kind: appsysmanager.ActionTmuxCreate, Host: p.state.host,
		Session: name, Command: command,
	})
	return nil
}

// requestDockerAction prepares a container start, stop, or remove.
func (p *systemPages) requestDockerAction(container appsysmanager.Container,
	action appsysmanager.DockerAction) error {
	if p.state.actionBusy {
		return errors.New("an action is already running")
	}
	runtime := p.containerRuntime()
	command, err := appsysmanager.ContainerActionCommand(runtime, container.ID, action)
	if err != nil {
		return err
	}
	kind, ok := appsysmanager.DockerActionKind(action)
	if !ok {
		return errors.New("unknown container action")
	}
	pending := appsysmanager.PendingAction{
		Kind: kind, Host: p.state.host,
		ContainerID: container.ID, ContainerName: appsysmanager.ContainerDisplayName(container),
		DockerAction: action, Runtime: runtime, Command: command,
	}
	p.state.pending = &pending
	p.state.actionDone = false
	p.requestRedraw()
	return nil
}

func (p *systemPages) containerRuntime() appsysmanager.ContainerRuntime {
	if p.state.containerRuntime != "" {
		return p.state.containerRuntime
	}
	return appsysmanager.RuntimeDocker
}

// confirm runs the pending action. It is the only path from the confirmation
// bar to a remote command.
func (p *systemPages) confirm() {
	pending := p.state.pending
	if pending == nil {
		return
	}
	p.state.pending = nil
	p.startAction(*pending)
}

// cancel discards the confirmation or cancels a running command. Either way no
// new command starts.
func (p *systemPages) cancel() {
	if p.state.pending != nil {
		p.state.pending = nil
	}
	if p.state.actionCancel != nil {
		p.state.actionCancel()
	}
	p.requestRedraw()
}

func (p *systemPages) startAction(pending appsysmanager.PendingAction) {
	if p.service == nil {
		return
	}
	p.mu.Lock()
	p.actionSeq++
	gen := p.actionSeq
	p.mu.Unlock()
	p.state.actionGen = gen
	p.state.actionHost = string(pending.Host.ID)
	p.state.actionBusy = true
	p.state.actionDone = false
	p.state.actionErr = nil
	p.state.actionResult = appsysmanager.ActionResult{Kind: pending.Kind, Command: pending.Command}
	kept := pending
	p.state.lastPending = &kept
	ctx, cancel := context.WithCancel(context.Background())
	p.state.actionCancel = cancel
	service := p.service
	go func() {
		result, err := service.RunPending(ctx, pending)
		cancel()
		p.mu.Lock()
		if !p.closed {
			p.actions = append(p.actions, systemActionOutcome{
				gen: gen, hostID: string(pending.Host.ID), result: result, err: err,
			})
		}
		p.mu.Unlock()
		p.requestRedraw()
	}()
	p.requestRedraw()
}

// retryAction re-runs the confirmed action after a failure, such as a Host
// permission that was fixed in another Session.
func (p *systemPages) retryAction() {
	if p.state.actionBusy || p.state.lastPending == nil {
		return
	}
	p.startAction(*p.state.lastPending)
}

// ---- documents ---------------------------------------------------------

func (p *systemPages) inspectContainer(container appsysmanager.Container) error {
	return p.loadDocument(appsysmanager.DocumentInspect, container)
}

func (p *systemPages) containerLogs(container appsysmanager.Container) error {
	return p.loadDocument(appsysmanager.DocumentLogs, container)
}

func (p *systemPages) loadDocument(kind appsysmanager.DocumentKind,
	container appsysmanager.Container) error {
	if p.service == nil {
		return errors.New("System management is not available")
	}
	runtime := p.containerRuntime()
	switch kind {
	case appsysmanager.DocumentInspect:
		if _, err := appsysmanager.ContainerInspectCommand(runtime, container.ID); err != nil {
			return err
		}
	default:
		if _, err := appsysmanager.ContainerLogsCommand(runtime, container.ID,
			appsysmanager.DefaultDockerLogTail); err != nil {
			return err
		}
	}
	p.closeDocument()
	p.state.documentGen++
	gen := p.state.documentGen
	p.state.document = appsysmanager.DockerDocument{Kind: kind, ContainerID: container.ID}
	p.state.documentOn = true
	p.state.documentBusy = true
	p.state.documentErr = nil
	p.state.documentHost = p.state.host
	p.state.documentID = container.ID
	p.state.documentKind = kind
	p.state.documentRuntime = runtime
	ctx, cancel := context.WithCancel(context.Background())
	p.state.docCancel = cancel
	service := p.service
	host := p.state.host
	id := container.ID
	go func() {
		var document appsysmanager.DockerDocument
		var err error
		if kind == appsysmanager.DocumentInspect {
			document, err = service.ContainerInspect(ctx, host, runtime, id)
		} else {
			document, err = service.ContainerLogs(ctx, host, runtime, id,
				appsysmanager.DefaultDockerLogTail)
		}
		cancel()
		p.mu.Lock()
		if !p.closed {
			p.documents = append(p.documents, systemDocumentOutcome{
				gen: gen, document: document, err: err,
			})
		}
		p.mu.Unlock()
		p.requestRedraw()
	}()
	p.requestRedraw()
	return nil
}

// retryDocument repeats the last read, keeping the same target.
func (p *systemPages) retryDocument() {
	if p.state.documentBusy || p.state.documentID == "" {
		return
	}
	container := appsysmanager.Container{ID: p.state.documentID}
	_ = p.loadDocument(p.state.documentKind, container)
}

func (p *systemPages) closeDocument() {
	if !p.state.documentOn && p.state.docCancel == nil {
		return
	}
	p.state.documentGen++
	if p.state.docCancel != nil {
		p.state.docCancel()
		p.state.docCancel = nil
	}
	p.state.documentOn = false
	p.state.documentBusy = false
	p.state.documentErr = nil
	p.state.document = appsysmanager.DockerDocument{}
}

// openTerminal opens a Session Tab and sends command once it is ready.
func (p *systemPages) openTerminal(host domainvault.Host, command string) error {
	if p.terminal == nil {
		return errors.New("the terminal opener is unavailable")
	}
	return p.terminal(host, command)
}

// ---- drawing -----------------------------------------------------------

// draw renders the page for host. flush runs before any status is read, so a
// background sample that finished between frames is shown on this one.
func (p *systemPages) draw(c *ui.Context, host domainvault.Host) {
	if !p.available() {
		ui.Column(c).Fill().Padding(16).Children(func() {
			kit.EmptyHint(c, "System management is not available in this build.")
		})
		return
	}
	p.openFor(host)
	if p.state.serviceProblem != "" {
		ui.Column(c).Fill().Padding(16).Children(func() {
			kit.EmptyHint(c, p.state.serviceProblem)
		})
		return
	}
	p.flush()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(12).Children(func() {
			kit.SectionBar(c, "System · "+hostName(host), nil)
			p.drawCapabilities(c)
			p.drawTabs(c)
			p.drawProblem(c)
			p.drawTab(c)
			p.drawPending(c)
			p.drawActionResult(c)
			p.drawDocument(c)
		})
	})
}

// drawCapabilities is the probe result: the tab strip shows what the Host can
// do, and this line names the tools actually detected.
func (p *systemPages) drawCapabilities(c *ui.Context) {
	if !p.state.hostSet || p.state.snapshot.CapabilitiesHost != string(p.state.host.ID) {
		ui.Text(c, "Detecting the Host's system tools…").FontSize(12).
			TextColor(c.Theme().TextMuted)
		return
	}
	caps := p.state.snapshot.Capabilities
	tools := make([]string, 0, 12)
	for _, tool := range []struct {
		name    string
		present bool
	}{
		{"ps", caps.HasPs}, {"top", caps.HasTop}, {"ss", caps.HasSs},
		{"netstat", caps.HasNetstat}, {"lsof", caps.HasLsof},
		{"systemctl", caps.HasSystemctl}, {"tmux", caps.HasTmux},
		{"docker", caps.HasDocker}, {"podman", caps.HasPodman},
		{"nerdctl", caps.HasNerdctl}, {"kubectl", caps.HasKubectl},
		{"nvidia-smi", caps.HasNvidiaSmi}, {"npu-smi", caps.HasNpuSmi},
	} {
		if tool.present {
			tools = append(tools, tool.name)
		}
	}
	osName := caps.OS
	if osName == "" {
		osName = "unknown OS"
	}
	if len(tools) == 0 {
		ui.Text(c, fmt.Sprintf("%s · no supported system tools detected", osName)).
			FontSize(12).TextColor(c.Theme().Warning)
		return
	}
	ui.Text(c, fmt.Sprintf("%s · detected: %s", osName, strings.Join(tools, ", "))).
		FontSize(12).TextColor(c.Theme().TextMuted)
}

// drawTabs is the tab strip. A tab the probe says this Host cannot run still
// appears so its reason is reachable, but it is drawn as secondary.
func (p *systemPages) drawTabs(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		for _, tab := range appsysmanager.ExtendedTabs {
			tab := tab
			available := true
			if p.state.snapshot.CapabilitiesHost == string(p.state.host.ID) {
				available, _ = p.state.snapshot.Capabilities.Available(tab)
			}
			label := systemTabLabel(tab)
			var clicked bool
			if tab == p.state.tab {
				clicked = kit.PrimaryButton(c, systemTabIcon(tab), label).Clicked()
			} else if available {
				clicked = kit.SecondaryButton(c, systemTabIcon(tab), label).Clicked()
			} else {
				clicked = kit.GhostButton(c, systemTabIcon(tab), label).Clicked()
			}
			if clicked {
				p.setTab(tab)
			}
		}
	})
}

func (p *systemPages) drawProblem(c *ui.Context) {
	if p.state.problem == "" {
		return
	}
	kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.state.problem, func() {
		if kit.GhostButton(c, icons.X, "Dismiss").Clicked() {
			p.state.problem = ""
		}
	})
}

func (p *systemPages) drawTab(c *ui.Context) {
	tab := p.state.tab
	state := p.state.snapshot.Tabs[tab]
	if p.state.snapshot.CapabilitiesHost == string(p.state.host.ID) {
		if available, reason := p.state.snapshot.Capabilities.Available(tab); !available {
			kit.EmptyHint(c, systemReasonText(reason))
			return
		}
	}
	switch state.Status {
	case "", appsysmanager.StatusIdle, appsysmanager.StatusConnecting:
		ui.Text(c, "Reading this tab…").FontSize(12).TextColor(c.Theme().TextMuted)
	case appsysmanager.StatusLogin, appsysmanager.StatusError,
		appsysmanager.StatusUnsupported, appsysmanager.StatusUnavailable:
		p.drawTabFailure(c, state)
	}
	switch tab {
	case appsysmanager.TabOverview:
		p.drawOverview(c, state)
	case appsysmanager.TabProcesses:
		p.drawProcesses(c, state)
	case appsysmanager.TabPorts:
		p.drawPorts(c, state)
	case appsysmanager.TabServices:
		p.drawServices(c, state)
	case appsysmanager.TabTmux:
		p.drawTmux(c, state)
	case appsysmanager.TabDocker:
		p.drawDocker(c, state)
	case appsysmanager.TabKubernetes:
		p.drawKubernetes(c, state)
	case appsysmanager.TabGPU:
		p.drawGPU(c, state)
	}
}

// drawTabFailure shows the typed reason and a Retry that restarts sampling. A
// permission denial is exactly this path: fixing it outside the phone and
// tapping Retry continues without leaving the page.
func (p *systemPages) drawTabFailure(c *ui.Context, state appsysmanager.TabState) {
	reason := systemReasonText(state.Reason)
	if reason == "" {
		reason = "This tab could not be read."
	}
	kit.Notice(c, icons.TriangleAlert, c.Theme().Warning, reason, func() {
		if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() && p.service != nil {
			p.service.Refresh()
		}
	})
}

func (p *systemPages) drawOverview(c *ui.Context, state appsysmanager.TabState) {
	overview := state.Overview
	rows := [][2]string{
		{"Host", overview.Hostname},
		{"OS", overview.OSName},
		{"Kernel", overview.KernelRelease},
		{"Uptime", durationText(overview.UptimeSeconds)},
		{"Load", loadText(overview.LoadAverage)},
		{"Cores", fmt.Sprintf("%d", overview.Cores)},
		{"CPU", percentText(overview.CPUPercent)},
		{"Memory", memoryText(overview.MemUsedMB, overview.MemTotalMB)},
		{"Swap", memoryText(overview.SwapUsedMB, overview.SwapTotalMB)},
		{"Disk", diskText(overview.DiskUsedGB, overview.DiskTotalGB)},
	}
	for _, row := range rows {
		if row[1] == "" || row[1] == "0" {
			continue
		}
		systemDetailRow(c, row[0], row[1])
	}
	if overview.Hostname == "" && overview.OSName == "" {
		if state.Status == appsysmanager.StatusLive {
			kit.EmptyHint(c, "The Host reported no overview fields.")
		}
	}
}

func (p *systemPages) drawProcesses(c *ui.Context, state appsysmanager.TabState) {
	rows := append([]appsysmanager.Process(nil), state.Processes...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CPUPercent != rows[j].CPUPercent {
			return rows[i].CPUPercent > rows[j].CPUPercent
		}
		return rows[i].PID < rows[j].PID
	})
	p.drawSearch(c)
	count := 0
	for _, process := range rows {
		process := process
		if !systemMatches(p.state.search, process.Command, process.User,
			fmt.Sprintf("%d", process.PID)) {
			continue
		}
		count++
		row := kit.ListRow(c).Label(fmt.Sprintf("Process %d %s", process.PID, process.Command))
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, fmt.Sprintf("%d · %s", process.PID, process.Command)).
					FontSize(13).SingleLine()
				ui.Text(c, fmt.Sprintf("%s · CPU %s · Mem %s", process.User,
					percentText(process.CPUPercent), percentText(process.MemPercent))).
					FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.GhostButton(c, icons.Power, "SIGTERM").Clicked() {
				p.report(p.requestSignal(process.PID, "TERM"))
			}
			if kit.GhostButton(c, icons.CircleX, "SIGKILL").Clicked() {
				p.report(p.requestSignal(process.PID, "KILL"))
			}
		})
	}
	if count == 0 {
		kit.EmptyHint(c, "No processes match this search.")
	}
}

func (p *systemPages) drawPorts(c *ui.Context, state appsysmanager.TabState) {
	rows := append([]appsysmanager.Port(nil), state.Ports...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Port != rows[j].Port {
			return rows[i].Port < rows[j].Port
		}
		return rows[i].Address < rows[j].Address
	})
	p.drawSearch(c)
	count := 0
	for _, port := range rows {
		port := port
		label := fmt.Sprintf("%s %s:%d", port.Protocol, port.Address, port.Port)
		if !systemMatches(p.state.search, label, port.ProcessName,
			fmt.Sprintf("%d", port.PID)) {
			continue
		}
		count++
		row := kit.ListRow(c).Label("Port " + label)
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, label).FontSize(13).SingleLine()
				owner := port.ProcessName
				if owner == "" {
					owner = "unknown process"
				}
				if port.PID > 0 {
					owner = fmt.Sprintf("%s · PID %d", owner, port.PID)
				}
				ui.Text(c, owner).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			})
			if port.PID > 0 {
				if kit.GhostButton(c, icons.Power, "Terminate").Clicked() {
					p.report(p.requestPortTerminate(port))
				}
			}
		})
	}
	if count == 0 {
		kit.EmptyHint(c, "No listening ports match this search.")
	}
}

func (p *systemPages) drawServices(c *ui.Context, state appsysmanager.TabState) {
	rows := append([]appsysmanager.Unit(nil), state.Services...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	p.drawSearch(c)
	count := 0
	for _, unit := range rows {
		unit := unit
		if !systemMatches(p.state.search, unit.Name, unit.Description, unit.ActiveState) {
			continue
		}
		count++
		row := kit.ListRow(c).Label("Service " + unit.Name)
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, unit.Name).FontSize(13).SingleLine()
				detail := unit.ActiveState
				if unit.Description != "" {
					detail += " · " + unit.Description
				}
				ui.Text(c, detail).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.GhostButton(c, icons.Play, "Start").Clicked() {
				p.report(p.requestServiceAction(unit.Name, appsysmanager.ServiceStart, systemScope(unit)))
			}
			if kit.GhostButton(c, icons.Square, "Stop").Clicked() {
				p.report(p.requestServiceAction(unit.Name, appsysmanager.ServiceStop, systemScope(unit)))
			}
			if kit.GhostButton(c, icons.RefreshCw, "Restart").Clicked() {
				p.report(p.requestServiceAction(unit.Name, appsysmanager.ServiceRestart, systemScope(unit)))
			}
		})
	}
	if count == 0 {
		kit.EmptyHint(c, "No services match this search.")
	}
}

func (p *systemPages) drawTmux(c *ui.Context, state appsysmanager.TabState) {
	kit.FormRow(c, "New session", func() {
		ui.TextInput(c, &p.state.tmuxName).Placeholder("work").FillWidth()
	})
	if kit.PrimaryButton(c, icons.Plus, "Create session").Clicked() {
		p.report(p.requestTmuxCreate())
	}
	rows := append([]appsysmanager.TmuxSession(nil), state.Sessions...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	p.drawSearch(c)
	count := 0
	for _, session := range rows {
		session := session
		if !systemMatches(p.state.search, session.Name) {
			continue
		}
		count++
		row := kit.ListRow(c).Label("tmux session " + session.Name)
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, session.Name).FontSize(13).SingleLine()
				detail := fmt.Sprintf("%d windows", session.Windows)
				if session.Attached {
					detail += " · attached"
				}
				ui.Text(c, detail).FontSize(12).TextColor(c.Theme().TextMuted)
			})
			if kit.GhostButton(c, icons.ExternalLink, "Attach").Clicked() {
				command, err := appsysmanager.TmuxAttachCommand(session.Name)
				if err == nil {
					err = p.openTerminal(p.state.host, command)
				}
				p.report(err)
			}
			if kit.GhostButton(c, icons.Trash, "Kill").Clicked() {
				p.report(p.requestTmuxKill(session.Name))
			}
		})
	}
	if count == 0 {
		kit.EmptyHint(c, "No tmux sessions on this Host.")
	}
}

func (p *systemPages) drawDocker(c *ui.Context, state appsysmanager.TabState) {
	runtimes := p.availableRuntimes()
	if len(runtimes) > 1 {
		ui.Row(c).Gap(8).Wrap().Children(func() {
			for _, runtime := range runtimes {
				runtime := runtime
				if runtime == p.containerRuntime() {
					if kit.PrimaryButton(c, icons.Server, runtime.Binary()).Clicked() {
						p.selectRuntime(runtime)
					}
				} else if kit.SecondaryButton(c, icons.Server, runtime.Binary()).Clicked() {
					p.selectRuntime(runtime)
				}
			}
		})
	}
	ui.Row(c).Gap(8).Children(func() {
		if p.state.dockerView == 0 {
			if kit.PrimaryButton(c, icons.Rows, "Containers").Clicked() {
				p.state.dockerView = 0
			}
			if kit.SecondaryButton(c, icons.File, "Images").Clicked() {
				p.state.dockerView = 1
			}
		} else {
			if kit.SecondaryButton(c, icons.Rows, "Containers").Clicked() {
				p.state.dockerView = 0
			}
			if kit.PrimaryButton(c, icons.File, "Images").Clicked() {
				p.state.dockerView = 1
			}
		}
	})
	if p.state.dockerView == 1 {
		p.drawImages(c, state)
		return
	}
	rows := append([]appsysmanager.Container(nil), state.Containers...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	p.drawSearch(c)
	count := 0
	for _, container := range rows {
		container := container
		if !systemMatches(p.state.search, container.Name, container.Image,
			container.ID, container.State) {
			continue
		}
		count++
		row := kit.ListRow(c).Label("Container " + appsysmanager.ContainerDisplayName(container))
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, appsysmanager.ContainerDisplayName(container)).FontSize(13).SingleLine()
				ui.Text(c, container.Image+" · "+container.State).FontSize(12).
					TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.GhostButton(c, icons.FileText, "Inspect").Clicked() {
				p.report(p.inspectContainer(container))
			}
			if kit.GhostButton(c, icons.ScrollText, "Logs").Clicked() {
				p.report(p.containerLogs(container))
			}
			if kit.GhostButton(c, icons.Play, "Start").Clicked() {
				p.report(p.requestDockerAction(container, appsysmanager.DockerStart))
			}
			if kit.GhostButton(c, icons.Square, "Stop").Clicked() {
				p.report(p.requestDockerAction(container, appsysmanager.DockerStop))
			}
			if kit.GhostButton(c, icons.Trash, "Remove").Clicked() {
				p.report(p.requestDockerAction(container, appsysmanager.DockerRemove))
			}
		})
	}
	if count == 0 {
		kit.EmptyHint(c, "No containers match this search.")
	}
}

func (p *systemPages) drawImages(c *ui.Context, state appsysmanager.TabState) {
	rows := append([]appsysmanager.Image(nil), state.Images...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name() < rows[j].Name() })
	p.drawSearch(c)
	count := 0
	for _, image := range rows {
		image := image
		if !systemMatches(p.state.search, image.Name(), image.ID) {
			continue
		}
		count++
		row := kit.ListRow(c).Label("Image " + image.Name())
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, image.Name()).FontSize(13).SingleLine()
				ui.Text(c, image.Size).FontSize(12).TextColor(c.Theme().TextMuted)
			})
		})
	}
	if count == 0 {
		kit.EmptyHint(c, "No images match this search.")
	}
}

func (p *systemPages) drawKubernetes(c *ui.Context, state appsysmanager.TabState) {
	if namespaces := state.Kubernetes.Namespaces; len(namespaces) > 0 {
		ui.Text(c, "Namespace").FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			options := append([]string{""}, namespaces...)
			for _, namespace := range options {
				namespace := namespace
				label := namespace
				if label == "" {
					label = "(default)"
				}
				if namespace == p.state.kubernetesNamespace {
					if kit.PrimaryButton(c, icons.Globe, label).Clicked() {
						p.selectNamespace(namespace)
					}
				} else if kit.SecondaryButton(c, icons.Globe, label).Clicked() {
					p.selectNamespace(namespace)
				}
			}
		})
	}
	rows := append([]appsysmanager.Pod(nil), state.Kubernetes.Pods...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Name < rows[j].Name
	})
	p.drawSearch(c)
	count := 0
	for _, pod := range rows {
		pod := pod
		if !systemMatches(p.state.search, pod.Name, pod.Namespace, pod.Status) {
			continue
		}
		count++
		row := kit.ListRow(c).Label("Pod " + pod.Name)
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, pod.Name).FontSize(13).SingleLine()
				ui.Text(c, pod.Namespace+" · "+pod.Status).FontSize(12).
					TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.GhostButton(c, icons.Terminal, "Terminal").Clicked() {
				command, err := appsysmanager.KubectlExecCommand(pod.Namespace, pod.Name,
					pod.TerminalContainer())
				if err == nil {
					err = p.openTerminal(p.state.host, command)
				}
				p.report(err)
			}
		})
	}
	if count == 0 {
		kit.EmptyHint(c, "No pods match this search.")
	}
}

func (p *systemPages) drawGPU(c *ui.Context, state appsysmanager.TabState) {
	if len(state.GPU.Devices) == 0 {
		kit.EmptyHint(c, "This Host reported no accelerator devices.")
		return
	}
	for _, device := range state.GPU.Devices {
		systemDetailRow(c, fmt.Sprintf("%s %d", device.Vendor, device.Index),
			fmt.Sprintf("%s · %s memory · %s · %s", device.Name,
				memoryText(device.MemoryUsedMB, device.MemoryTotalMB),
				percentText(device.UtilizationPercent),
				fmt.Sprintf("%.0f°C", device.TemperatureC)))
	}
}

func (p *systemPages) drawSearch(c *ui.Context) {
	kit.FormRow(c, "Search", func() {
		ui.TextInput(c, &p.state.search).Placeholder("Filter this tab").FillWidth()
	})
}

// drawPending is the confirmation bar. Cancel executes nothing; Confirm is the
// only path into startAction.
func (p *systemPages) drawPending(c *ui.Context) {
	pending := p.state.pending
	if pending == nil {
		return
	}
	text, detail := systemPendingText(*pending)
	cancelled, confirmed := kit.ConfirmBar(c, text, func() {
		ui.Text(c, detail).FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Text(c, "Runs: "+pending.Command).FontSize(11).
			TextColor(c.Theme().TextMuted).SingleLine()
	}, "Cancel", "Confirm")
	switch {
	case cancelled:
		p.cancel()
	case confirmed:
		p.confirm()
	}
}

// drawActionResult is the confirmed command's outcome. A permission failure
// keeps a Retry that re-runs exactly the approved action.
func (p *systemPages) drawActionResult(c *ui.Context) {
	if p.state.actionBusy {
		ui.Text(c, "Running the confirmed command…").FontSize(12).
			TextColor(c.Theme().TextMuted)
		return
	}
	if !p.state.actionDone {
		return
	}
	if p.state.actionErr != nil {
		reason := systemErrorText(p.state.actionErr)
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
			"The action failed: "+reason, func() {
				if p.state.lastPending != nil && kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
					p.retryAction()
				}
			})
		return
	}
	output := strings.TrimSpace(p.state.actionResult.Stdout)
	if output == "" {
		output = "Done."
	}
	kit.Notice(c, icons.CircleCheck, c.Theme().Success, output, func() {
		if kit.GhostButton(c, icons.X, "Dismiss").Clicked() {
			p.state.actionDone = false
		}
	})
}

// drawDocument is the read-only inspect or logs panel.
func (p *systemPages) drawDocument(c *ui.Context) {
	if !p.state.documentOn {
		return
	}
	title := "Inspect"
	if p.state.document.Kind == appsysmanager.DocumentLogs {
		title = "Logs"
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title+" · "+p.state.document.ContainerID).FontWeight(600)
		ui.Spacer(c)
		if kit.GhostButton(c, icons.X, "Close document").Clicked() {
			p.closeDocument()
		}
	})
	if p.state.documentBusy {
		ui.Text(c, "Reading…").FontSize(12).TextColor(c.Theme().TextMuted)
		return
	}
	if p.state.documentErr != nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
			systemErrorText(p.state.documentErr), func() {
				if kit.GhostButton(c, icons.RefreshCw, "Retry read").Clicked() {
					p.retryDocument()
				}
			})
		return
	}
	text := p.state.document.Text
	if text == "" {
		text = "(empty)"
	}
	ui.Text(c, text).FontSize(12).TextColor(c.Theme().TextMuted)
	if p.state.document.Truncated {
		ui.Text(c, "The reply was truncated.").FontSize(12).
			TextColor(c.Theme().Warning)
	}
}

// ---- helpers -----------------------------------------------------------

func (p *systemPages) report(err error) {
	if err != nil {
		p.state.problem = err.Error()
		p.requestRedraw()
	}
}

func (p *systemPages) availableRuntimes() []appsysmanager.ContainerRuntime {
	if p.state.snapshot.CapabilitiesHost == string(p.state.host.ID) {
		if runtimes := p.state.snapshot.Capabilities.ContainerRuntimes(); len(runtimes) > 0 {
			return runtimes
		}
	}
	return []appsysmanager.ContainerRuntime{appsysmanager.RuntimeDocker}
}

func (p *systemPages) selectRuntime(runtime appsysmanager.ContainerRuntime) {
	p.state.containerRuntime = runtime
	p.state.selected = -1
	p.closeDocument()
	if p.service != nil {
		p.service.SetContainerRuntime(runtime)
	}
	p.requestRedraw()
}

func (p *systemPages) selectNamespace(namespace string) {
	p.state.kubernetesNamespace = namespace
	p.state.selected = -1
	p.closeDocument()
	if p.service != nil {
		if err := p.service.SetKubernetesNamespace(namespace); err != nil {
			p.report(err)
		}
	}
	p.requestRedraw()
}

func systemTabLabel(tab appsysmanager.Tab) string {
	switch tab {
	case appsysmanager.TabOverview:
		return "Overview"
	case appsysmanager.TabProcesses:
		return "Processes"
	case appsysmanager.TabPorts:
		return "Ports"
	case appsysmanager.TabServices:
		return "Services"
	case appsysmanager.TabTmux:
		return "tmux"
	case appsysmanager.TabDocker:
		return "Docker"
	case appsysmanager.TabKubernetes:
		return "Kubernetes"
	case appsysmanager.TabGPU:
		return "GPU"
	}
	return string(tab)
}

func systemTabIcon(tab appsysmanager.Tab) string {
	switch tab {
	case appsysmanager.TabDocker, appsysmanager.TabKubernetes:
		return icons.Server
	case appsysmanager.TabServices, appsysmanager.TabTmux:
		return icons.Rows
	case appsysmanager.TabGPU:
		return icons.Cpu
	}
	return icons.Activity
}

func systemScope(unit appsysmanager.Unit) appsysmanager.Scope {
	if unit.Scope == "user" {
		return appsysmanager.ScopeUser
	}
	return appsysmanager.ScopeSystem
}

func systemPendingText(pending appsysmanager.PendingAction) (string, string) {
	switch {
	case pending.Kind == appsysmanager.ActionSignal && pending.Origin == appsysmanager.OriginPort:
		return fmt.Sprintf("Terminate the process on %s:%d?", pending.Address, pending.Port),
			fmt.Sprintf("Sends %s to PID %d.", pending.Signal, pending.PID)
	case pending.Kind == appsysmanager.ActionSignal:
		return fmt.Sprintf("Send %s to PID %d?", pending.Signal, pending.PID),
			"The process may end immediately."
	case pending.Kind == appsysmanager.ActionServiceAction:
		return fmt.Sprintf("%s %s?", titleFirst(string(pending.Action)), pending.Unit),
			"A systemd unit action is a remote service change."
	case pending.Kind == appsysmanager.ActionTmuxKill:
		return "Kill tmux session " + pending.Session + "?",
			"Everything running inside the session stops."
	case pending.Kind == appsysmanager.ActionDockerRemove:
		return "Remove container " + pending.ContainerName + "?",
			"The container is force-removed."
	case pending.Kind == appsysmanager.ActionDockerStop:
		return "Stop container " + pending.ContainerName + "?",
			"The container's processes stop."
	case pending.Kind == appsysmanager.ActionDockerStart:
		return "Start container " + pending.ContainerName + "?", ""
	}
	return "Run this remote command?", pending.Command
}

func systemReasonText(reason appsysmanager.Reason) string {
	switch reason {
	case appsysmanager.ReasonPermission:
		return "Permission denied. Fix the Host's permissions, then Retry."
	case appsysmanager.ReasonHostKey:
		return "The Host key was rejected."
	case appsysmanager.ReasonAuth:
		return "The Host rejected the credentials."
	case appsysmanager.ReasonNetwork:
		return "The Host could not be reached."
	case appsysmanager.ReasonTimeout:
		return "The Host did not answer in time."
	case appsysmanager.ReasonOS:
		return "This Host's operating system cannot be inspected."
	case appsysmanager.ReasonMissingProcesses:
		return "Neither ps nor top is installed on this Host."
	case appsysmanager.ReasonMissingPorts:
		return "No port collector (ss, netstat, or lsof) is installed."
	case appsysmanager.ReasonMissingSystemctl:
		return "systemctl is not installed on this Host."
	case appsysmanager.ReasonMissingTmux:
		return "tmux is not installed on this Host."
	case appsysmanager.ReasonMissingDocker:
		return "docker is not installed on this Host."
	case appsysmanager.ReasonDockerDaemon:
		return "The container daemon is not reachable."
	case appsysmanager.ReasonMissingContainerRuntime:
		return "No container engine is installed on this Host."
	case appsysmanager.ReasonContainerDaemon:
		return "The container daemon is not reachable."
	case appsysmanager.ReasonMissingKubectl:
		return "kubectl is not installed on this Host."
	case appsysmanager.ReasonKubernetesForbidden:
		return "The cluster refused the list; check the Host's kubeconfig permissions."
	case appsysmanager.ReasonClusterUnreachable:
		return "The Kubernetes API server did not answer."
	case appsysmanager.ReasonUnsupported:
		return "This Host does not support system management."
	case appsysmanager.ReasonCancelled:
		return "The request was cancelled."
	}
	return ""
}

func systemErrorText(err error) string {
	var typed *appsysmanager.Error
	if errors.As(err, &typed) {
		if text := systemReasonText(typed.Reason); text != "" {
			return text
		}
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func titleFirst(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func systemMatches(query string, fields ...string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

func systemDetailRow(c *ui.Context, label, value string) {
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted).Width(80)
		ui.Text(c, value).FontSize(13)
	})
}

func durationText(seconds float64) string {
	if seconds <= 0 {
		return ""
	}
	total := int64(seconds)
	days := total / 86400
	hours := (total % 86400) / 3600
	minutes := (total % 3600) / 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func loadText(values []float64) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%.2f", value))
	}
	return strings.Join(parts, " ")
}

func memoryText(used, total float64) string {
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf("%.0f / %.0f MB", used, total)
}

func diskText(used, total float64) string {
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1f / %.1f GB", used, total)
}
