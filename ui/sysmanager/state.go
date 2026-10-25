// Package sysmanager renders and drives the System manager surface. State
// owns Host selection, tab demand, search, ordering, refresh intervals, and
// the confirmation that gates every destructive action; the view localizes
// every message through the catalogs.
package sysmanager

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Service is the system-manager use-case seam. application/sysmanager.Service
// implements it; tests provide a fake.
type Service interface {
	Start(host domainvault.Host, tab appsysmanager.Tab, interval time.Duration) error
	Stop()
	Refresh()
	Snapshot() appsysmanager.Snapshot
	Subscribe(func()) func()
	Signal(ctx context.Context, host domainvault.Host, pid int, signal string) (appsysmanager.ActionResult, error)
	ServiceAction(ctx context.Context, host domainvault.Host, unit string,
		action appsysmanager.ServiceAction, scope appsysmanager.Scope) (appsysmanager.ActionResult, error)
}

// ExtendedService is the ticket-46 action seam. It is separate from Service so
// the core surface's fakes and self-test stay valid; application/sysmanager's
// Service implements both.
type ExtendedService interface {
	TmuxCreate(ctx context.Context, host domainvault.Host, name string) (appsysmanager.ActionResult, error)
	TmuxKill(ctx context.Context, host domainvault.Host, name string) (appsysmanager.ActionResult, error)
	DockerContainerAction(ctx context.Context, host domainvault.Host,
		id string, action appsysmanager.DockerAction) (appsysmanager.ActionResult, error)
	DockerInspect(ctx context.Context, host domainvault.Host,
		id string) (appsysmanager.DockerDocument, error)
	DockerLogs(ctx context.Context, host domainvault.Host,
		id string, tail int) (appsysmanager.DockerDocument, error)
}

// TerminalOpener opens a Session Tab against host and delivers command to it.
// The app wires it to the shell; opening runs on the UI thread, so it must not
// block on the network.
type TerminalOpener func(host domainvault.Host, command string) error

// Hosts is the Vault Host list the surface draws from.
type Hosts interface {
	List(context.Context) ([]domainvault.Host, error)
}

// Preferences is the existing settings store. Refresh intervals live in its
// own group so each tab's cadence survives a restart.
type Preferences interface {
	Settings() domainpreferences.Settings
	Update(func(*domainpreferences.Settings)) error
}

// Problem identifies a surface-level failure the view localizes.
type Problem string

const (
	ProblemNone       Problem = ""
	ProblemLoadHosts  Problem = "load_hosts"
	ProblemSave       Problem = "save"
	ProblemStart      Problem = "start"
	ProblemNoHost     Problem = "no_host"
	ProblemActionData Problem = "action_data"
	ProblemInternal   Problem = "internal"
	ProblemAttach     Problem = "attach"
	ProblemDocument   Problem = "document"
)

// Origin identifies where a confirmed signal came from, so the confirmation
// can name the process or the listening socket.
type Origin string

const (
	OriginProcess Origin = "process"
	OriginPort    Origin = "port"
)

// PendingAction is one destructive command awaiting confirmation. Requesting
// it does not run anything; only ConfirmPending opens a lease.
type PendingAction struct {
	Kind     appsysmanager.ActionKind
	Origin   Origin
	Host     domainvault.Host
	PID      int
	Signal   string
	Unit     string
	Action   appsysmanager.ServiceAction
	Scope    appsysmanager.Scope
	Command  string
	Address  string
	Port     int
	Protocol string
	// Session is the tmux session a create or kill targets.
	Session string
	// ContainerID is the Docker target; ContainerName is its display name.
	ContainerID   string
	ContainerName string
	DockerAction  appsysmanager.DockerAction
}

// State is the System manager surface's window-scoped state. It is mutated on
// the UI thread; sampler snapshots and action outcomes cross threads under mu.
type State struct {
	service  Service
	extended ExtendedService
	terminal TerminalOpener
	hosts    Hosts
	prefs    Preferences
	redraw   func()
	now      func() time.Time

	hostItems []domainvault.Host
	hostByID  map[string]domainvault.Host
	hostID    string
	tab       appsysmanager.Tab
	settings  domainpreferences.SysManager
	search    string
	problem   Problem
	loadErr   error
	active    bool

	pending      *PendingAction
	actionCancel context.CancelFunc
	actionBusy   bool
	actionResult appsysmanager.ActionResult
	actionErr    error
	actionDone   bool

	snapshot appsysmanager.Snapshot
	table    ui.ListState
	sort     ui.SortOrder
	selected int
	// rows cache the current tab's filtered and sorted view for one frame.
	// rowKey runs once per row, so it must not rebuild the whole list.
	processRows   []appsysmanager.Process
	portRows      []appsysmanager.Port
	serviceRows   []appsysmanager.Unit
	sessionRows   []appsysmanager.TmuxSession
	containerRows []appsysmanager.Container
	imageRows     []appsysmanager.Image

	tmuxName     string
	dockerView   int
	document     appsysmanager.DockerDocument
	documentOn   bool
	documentBusy bool
	documentErr  error
	documentGen  uint64
	docCancel    context.CancelFunc

	mu          sync.Mutex
	outcomes    []actionOutcome
	docOutcomes []documentOutcome
	unsubscribe func()
	closed      bool
}

type actionOutcome struct {
	result appsysmanager.ActionResult
	err    error
}

type documentOutcome struct {
	gen      uint64
	document appsysmanager.DockerDocument
	err      error
}

// Option customizes a State.
func WithRedrawRequest(fn func()) Option {
	return func(s *State) { s.redraw = fn }
}

// WithClock replaces the source of time.
func WithClock(now func() time.Time) Option {
	return func(s *State) {
		if now != nil {
			s.now = now
		}
	}
}

// WithTerminalOpener wires tmux attach to the shell's Session opener.
func WithTerminalOpener(open TerminalOpener) Option {
	return func(s *State) { s.terminal = open }
}

// Option customizes a State.
type Option func(*State)

// NewState returns a System manager surface over its ports.
func NewState(service Service, hosts Hosts, prefs Preferences, opts ...Option) *State {
	s := &State{
		service:  service,
		hosts:    hosts,
		prefs:    prefs,
		now:      time.Now,
		tab:      appsysmanager.TabOverview,
		settings: domainpreferences.DefaultSysManager(),
		hostByID: make(map[string]domainvault.Host),
		selected: -1,
		sort:     ui.SortOrder{Column: "cpu", Descending: true},
		snapshot: appsysmanager.Snapshot{Tabs: make(map[appsysmanager.Tab]appsysmanager.TabState)},
	}
	for _, opt := range opts {
		opt(s)
	}
	s.table.Sort = &s.sort
	s.table.Selected = &s.selected
	s.table.Key = s.rowKey
	s.table.Label = s.rowLabel
	if service != nil {
		s.unsubscribe = service.Subscribe(func() { s.requestRedraw() })
		if extended, ok := service.(ExtendedService); ok {
			s.extended = extended
		}
	}
	return s
}

// Close releases the runtime subscription and stops sampling and any running
// action. The app closes the service separately.
func (s *State) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	unsubscribe := s.unsubscribe
	s.unsubscribe = nil
	s.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
	s.CancelPending()
	s.CloseDocument()
	if s.service != nil {
		s.service.Stop()
	}
	s.active = false
}

// Activate reloads the settings and Vault Hosts, then starts the active tab.
// The shell calls it when the surface becomes visible.
func (s *State) Activate() {
	s.active = true
	s.clearFinishedAction()
	s.reload()
	s.startActive()
	s.requestRedraw()
}

// Deactivate stops sampling and any pending action. The shell calls it when
// another surface shows.
func (s *State) Deactivate() {
	s.active = false
	s.CancelPending()
	s.CloseDocument()
	if s.service != nil {
		s.service.Stop()
	}
	s.requestRedraw()
}

func (s *State) reload() {
	if s.prefs != nil {
		s.settings = domainpreferences.NormalizeSysManager(s.prefs.Settings().SysManager)
	} else {
		s.settings = domainpreferences.DefaultSysManager()
	}
	if s.hosts == nil {
		return
	}
	items, err := s.hosts.List(context.Background())
	s.hostItems = items
	s.hostByID = make(map[string]domainvault.Host, len(items))
	for _, host := range items {
		s.hostByID[string(host.ID)] = host
	}
	s.loadErr = err
	if err != nil {
		s.problem = ProblemLoadHosts
	} else if s.problem == ProblemLoadHosts {
		s.problem = ProblemNone
	}
	if _, ok := s.hostByID[s.hostID]; !ok {
		s.hostID = ""
		if len(items) > 0 {
			s.hostID = string(items[0].ID)
		}
	}
}

func (s *State) startActive() {
	if s.service == nil || !s.active {
		return
	}
	host, ok := s.CurrentHost()
	if !ok {
		s.service.Stop()
		return
	}
	interval := time.Duration(s.settings.Interval(preferenceTab(s.tab))) * time.Second
	if err := s.service.Start(host, s.tab, interval); err != nil {
		s.problem = ProblemStart
	}
}

// Flush copies the service's runtime snapshot and applies completed action
// outcomes. The view calls it every frame.
func (s *State) Flush() {
	if s.service != nil {
		s.snapshot = s.service.Snapshot()
	}
	s.mu.Lock()
	outcomes := s.outcomes
	s.outcomes = nil
	docOutcomes := s.docOutcomes
	s.docOutcomes = nil
	s.mu.Unlock()
	for _, outcome := range docOutcomes {
		if outcome.gen != s.documentGen || !s.documentOn {
			// A document closed or replaced while its reply was in flight
			// must not reappear under the next container.
			continue
		}
		s.documentBusy = false
		s.documentErr = outcome.err
		if outcome.err == nil {
			s.document = outcome.document
		}
	}
	for _, outcome := range outcomes {
		s.actionBusy = false
		s.actionCancel = nil
		s.actionDone = true
		s.actionResult = outcome.result
		s.actionErr = outcome.err
		if outcome.err == nil && s.service != nil {
			// A successful signal or unit action changes the data the tab
			// shows, so ask for a fresh sample instead of waiting a cadence.
			s.service.Refresh()
		}
	}
	s.Prepare()
}

// Prepare rebuilds the current tab's displayed rows. Call it once per frame
// before rendering, after Flush and after a search or sort change.
func (s *State) Prepare() {
	query := strings.ToLower(strings.TrimSpace(s.search))
	s.processRows = s.prepareProcesses(query)
	s.portRows = s.preparePorts(query)
	s.serviceRows = s.prepareServices(query)
	s.sessionRows = s.prepareSessions(query)
	s.containerRows = s.prepareContainers(query)
	s.imageRows = s.prepareImages(query)
	switch s.tab {
	case appsysmanager.TabProcesses:
		if s.selected >= len(s.processRows) {
			s.selected = -1
		}
	case appsysmanager.TabPorts:
		if s.selected >= len(s.portRows) {
			s.selected = -1
		}
	case appsysmanager.TabServices:
		if s.selected >= len(s.serviceRows) {
			s.selected = -1
		}
	case appsysmanager.TabTmux:
		if s.selected >= len(s.sessionRows) {
			s.selected = -1
		}
	case appsysmanager.TabDocker:
		limit := len(s.containerRows)
		if s.dockerView == 1 {
			limit = len(s.imageRows)
		}
		if s.selected >= limit {
			s.selected = -1
		}
	}
}

// Snapshot returns the latest runtime snapshot.
func (s *State) Snapshot() appsysmanager.Snapshot { return s.snapshot }

// TabState returns one tab's latest state, or an idle state.
func (s *State) TabState(tab appsysmanager.Tab) appsysmanager.TabState {
	if state, ok := s.snapshot.Tabs[tab]; ok {
		return state
	}
	return appsysmanager.TabState{Tab: tab, Status: appsysmanager.StatusIdle}
}

// Hosts returns the Vault Hosts the surface knows.
func (s *State) Hosts() []domainvault.Host { return s.hostItems }

// CurrentHost returns the selected Host.
func (s *State) CurrentHost() (domainvault.Host, bool) {
	host, ok := s.hostByID[s.hostID]
	return host, ok
}

// HostID returns the selected Host's ID.
func (s *State) HostID() string { return s.hostID }

// HostLabel returns a Host's display label, falling back to its hostname and
// then its id.
func (s *State) HostLabel(host domainvault.Host) string {
	if label := strings.TrimSpace(host.Label); label != "" {
		return label
	}
	if host.Hostname != "" {
		return host.Hostname
	}
	return string(host.ID)
}

// SetHost displays another Host and re-targets sampling. Samples from the
// previous Host are discarded by the service.
func (s *State) SetHost(id string) {
	if id == s.hostID {
		return
	}
	if _, ok := s.hostByID[id]; !ok {
		return
	}
	s.hostID = id
	s.selected = -1
	s.problem = ProblemNone
	s.closeDocument()
	s.clearFinishedAction()
	s.startActive()
	s.requestRedraw()
}

// Tab returns the active tab.
func (s *State) Tab() appsysmanager.Tab { return s.tab }

// AllTabs returns every known tab in the original display order.
func (s *State) AllTabs() []appsysmanager.Tab {
	return append([]appsysmanager.Tab(nil), appsysmanager.ExtendedTabs...)
}

// Tabs returns the visible tabs in the persisted order.
func (s *State) Tabs() []appsysmanager.Tab {
	visible := s.settings.VisibleTabs()
	tabs := make([]appsysmanager.Tab, 0, len(visible))
	for _, tab := range visible {
		tabs = append(tabs, appsysmanager.Tab(tab))
	}
	if len(tabs) == 0 {
		return []appsysmanager.Tab{appsysmanager.TabOverview}
	}
	return tabs
}

// TabVisible reports whether tab is shown in the strip. The overview is
// always visible.
func (s *State) TabVisible(tab appsysmanager.Tab) bool {
	return s.settings.Visible(domainpreferences.SysManagerTab(tab))
}

// TabIndex returns the active tab's position in the visible strip.
func (s *State) TabIndex() int {
	for index, candidate := range s.Tabs() {
		if candidate == s.tab {
			return index
		}
	}
	return 0
}

// SetTab displays another visible tab and starts its sampling.
func (s *State) SetTab(tab appsysmanager.Tab) {
	if !s.TabVisible(tab) || tab == s.tab {
		return
	}
	known := false
	for _, candidate := range appsysmanager.ExtendedTabs {
		if candidate == tab {
			known = true
		}
	}
	if !known {
		return
	}
	s.tab = tab
	s.selected = -1
	s.search = ""
	s.closeDocument()
	if s.problem == ProblemAttach || s.problem == ProblemDocument {
		s.problem = ProblemNone
	}
	s.clearFinishedAction()
	s.startActive()
	s.requestRedraw()
}

// SetTabVisible shows or hides a tab and persists the layout. Hiding the
// active tab moves to the nearest visible one; the overview cannot be hidden.
func (s *State) SetTabVisible(tab appsysmanager.Tab, visible bool) error {
	if s.TabVisible(tab) == visible {
		return nil
	}
	next := s.settings.WithTabHidden(domainpreferences.SysManagerTab(tab), !visible)
	if !s.saveLayout(next) {
		return errors.New("sysmanager: could not save the tab layout")
	}
	s.settings = next
	if !s.ensureActiveVisible() {
		s.startActive()
	}
	s.requestRedraw()
	return nil
}

// MoveTab moves the active tab one place earlier (delta < 0) or later in the
// persisted order. A move past either edge changes nothing.
func (s *State) MoveTab(delta int) bool {
	if delta == 0 {
		return false
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	next := s.settings
	changed := false
	for range absInt(delta) {
		moved := next.MoveTab(domainpreferences.SysManagerTab(s.tab), step)
		if sameTabOrder(moved.Order(), next.Order()) {
			break
		}
		next = moved
		changed = true
	}
	if !changed {
		return false
	}
	if !s.saveLayout(next) {
		return false
	}
	s.settings = next
	s.requestRedraw()
	return true
}

func (s *State) saveLayout(next domainpreferences.SysManager) bool {
	if s.prefs == nil {
		return true
	}
	if err := s.prefs.Update(func(settings *domainpreferences.Settings) {
		settings.SysManager = next
	}); err != nil {
		s.problem = ProblemSave
		s.requestRedraw()
		return false
	}
	if s.problem == ProblemSave {
		s.problem = ProblemNone
	}
	return true
}

// ensureActiveVisible moves the active tab to the first visible tab when it
// was hidden and reports whether it moved.
func (s *State) ensureActiveVisible() bool {
	if s.TabVisible(s.tab) {
		return false
	}
	tabs := s.Tabs()
	s.tab = tabs[0]
	s.selected = -1
	s.search = ""
	s.closeDocument()
	return true
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func sameTabOrder(left, right []domainpreferences.SysManagerTab) bool {
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

// clearFinishedAction drops a completed notice when the user changes tab or
// Host, so a result cannot look like it belongs to another target. A running
// action keeps its notice until it finishes.
func (s *State) clearFinishedAction() {
	if s.actionBusy {
		return
	}
	s.actionDone = false
	s.actionResult = appsysmanager.ActionResult{}
	s.actionErr = nil
}

// Search returns the active tab's filter text.
func (s *State) Search() string { return s.search }

// SetSearch replaces the filter text.
func (s *State) SetSearch(value string) {
	if value == s.search {
		return
	}
	s.search = value
	s.Prepare()
	s.requestRedraw()
}

// Settings returns the active refresh configuration.
func (s *State) Settings() domainpreferences.SysManager { return s.settings }

// Interval returns the active tab's cadence in seconds.
func (s *State) Interval() int { return s.settings.Interval(preferenceTab(s.tab)) }

// SetInterval persists the active tab's cadence and restarts sampling with it.
func (s *State) SetInterval(seconds int) {
	valid := false
	for _, choice := range domainpreferences.SysManagerIntervalChoices {
		if choice == seconds {
			valid = true
		}
	}
	if !valid || seconds == s.settings.Interval(preferenceTab(s.tab)) {
		return
	}
	next := s.settings.WithInterval(preferenceTab(s.tab), seconds)
	if s.prefs != nil {
		if err := s.prefs.Update(func(settings *domainpreferences.Settings) {
			settings.SysManager = next
		}); err != nil {
			s.problem = ProblemSave
			s.requestRedraw()
			return
		}
	}
	s.settings = next
	if s.problem == ProblemSave {
		s.problem = ProblemNone
	}
	s.startActive()
	s.requestRedraw()
}

// Refresh asks for an immediate sample.
func (s *State) Refresh() {
	if s.service == nil {
		return
	}
	if _, ok := s.CurrentHost(); !ok {
		s.problem = ProblemNoHost
		s.requestRedraw()
		return
	}
	s.service.Refresh()
	s.requestRedraw()
}

// Problem returns the current surface failure, if any.
func (s *State) Problem() Problem { return s.problem }

// LoadingError returns the Host list failure, if any.
func (s *State) LoadingError() error { return s.loadErr }

// Pending returns the action awaiting confirmation.
func (s *State) Pending() (PendingAction, bool) {
	if s.pending == nil {
		return PendingAction{}, false
	}
	return *s.pending, true
}

// RequestSignal prepares a process signal. No command runs until
// ConfirmPending; CancelPending discards it.
func (s *State) RequestSignal(pid int, signal string) error {
	if s.actionBusy {
		return errors.New("sysmanager: an action is already running")
	}
	command, err := appsysmanager.SignalCommand(pid, signal)
	if err != nil {
		return err
	}
	host, err := s.actionHost()
	if err != nil {
		return err
	}
	s.pending = &PendingAction{
		Kind:    appsysmanager.ActionSignal,
		Origin:  OriginProcess,
		Host:    host,
		PID:     pid,
		Signal:  strings.ToUpper(signal),
		Command: command,
	}
	s.actionDone = false
	s.requestRedraw()
	return nil
}

// RequestPortTerminate prepares SIGTERM for the process that owns port. A
// port without an attributed PID cannot be acted on.
func (s *State) RequestPortTerminate(port appsysmanager.Port) error {
	if s.actionBusy {
		return errors.New("sysmanager: an action is already running")
	}
	if port.PID <= 0 {
		return errors.New("sysmanager: the port has no process owner")
	}
	command, err := appsysmanager.SignalCommand(port.PID, "TERM")
	if err != nil {
		return err
	}
	host, err := s.actionHost()
	if err != nil {
		return err
	}
	s.pending = &PendingAction{
		Kind:     appsysmanager.ActionSignal,
		Origin:   OriginPort,
		Host:     host,
		PID:      port.PID,
		Signal:   "TERM",
		Command:  command,
		Address:  port.Address,
		Port:     port.Port,
		Protocol: port.Protocol,
	}
	s.actionDone = false
	s.requestRedraw()
	return nil
}

// RequestServiceAction prepares a systemd unit action.
func (s *State) RequestServiceAction(unit string, action appsysmanager.ServiceAction,
	scope appsysmanager.Scope) error {
	if s.actionBusy {
		return errors.New("sysmanager: an action is already running")
	}
	command, err := appsysmanager.ServiceCommand(unit, action, scope)
	if err != nil {
		return err
	}
	host, err := s.actionHost()
	if err != nil {
		return err
	}
	s.pending = &PendingAction{
		Kind:    appsysmanager.ActionServiceAction,
		Host:    host,
		Unit:    unit,
		Action:  action,
		Scope:   scope,
		Command: command,
	}
	s.actionDone = false
	s.requestRedraw()
	return nil
}

// TmuxName returns the new-session name being edited.
func (s *State) TmuxName() string { return s.tmuxName }

// SetTmuxName replaces the new-session name.
func (s *State) SetTmuxName(value string) { s.tmuxName = value }

// CreateSession validates the typed name and creates a detached tmux
// session. Creating is not destructive, so it runs without a confirmation.
func (s *State) CreateSession() error {
	if s.actionBusy {
		return errors.New("sysmanager: an action is already running")
	}
	name := strings.TrimSpace(s.tmuxName)
	command, err := appsysmanager.TmuxCreateCommand(name)
	if err != nil {
		s.problem = ProblemActionData
		s.requestRedraw()
		return err
	}
	host, err := s.actionHost()
	if err != nil {
		return err
	}
	if s.extended == nil {
		s.problem = ProblemInternal
		s.requestRedraw()
		return errNoExtendedService
	}
	s.tmuxName = ""
	s.startAction(PendingAction{
		Kind:    appsysmanager.ActionTmuxCreate,
		Host:    host,
		Session: name,
		Command: command,
	})
	return nil
}

// RequestTmuxKill prepares killing a session. Nothing runs until
// ConfirmPending.
func (s *State) RequestTmuxKill(session string) error {
	if s.actionBusy {
		return errors.New("sysmanager: an action is already running")
	}
	command, err := appsysmanager.TmuxKillCommand(session)
	if err != nil {
		return err
	}
	host, err := s.actionHost()
	if err != nil {
		return err
	}
	s.pending = &PendingAction{
		Kind:    appsysmanager.ActionTmuxKill,
		Host:    host,
		Session: session,
		Command: command,
	}
	s.actionDone = false
	s.requestRedraw()
	return nil
}

// AttachSession opens a terminal Tab attached to session. The command is
// validated first; the opener runs on the UI thread and must not block.
func (s *State) AttachSession(session string) error {
	command, err := appsysmanager.TmuxAttachCommand(session)
	if err != nil {
		return err
	}
	host, err := s.actionHost()
	if err != nil {
		return err
	}
	if s.terminal == nil {
		s.problem = ProblemAttach
		s.requestRedraw()
		return errors.New("sysmanager: no terminal opener")
	}
	if err := s.terminal(host, command); err != nil {
		s.problem = ProblemAttach
		s.requestRedraw()
		return err
	}
	if s.problem == ProblemAttach {
		s.problem = ProblemNone
	}
	s.requestRedraw()
	return nil
}

// RequestDockerAction prepares a container start, stop, or force-remove.
func (s *State) RequestDockerAction(container appsysmanager.Container,
	action appsysmanager.DockerAction) error {
	if s.actionBusy {
		return errors.New("sysmanager: an action is already running")
	}
	command, err := appsysmanager.DockerActionCommand(container.ID, action)
	if err != nil {
		return err
	}
	kind, ok := dockerActionKind(action)
	if !ok {
		return errors.New("sysmanager: unknown container action")
	}
	host, err := s.actionHost()
	if err != nil {
		return err
	}
	s.pending = &PendingAction{
		Kind:          kind,
		Host:          host,
		ContainerID:   container.ID,
		ContainerName: containerDisplayName(container),
		DockerAction:  action,
		Command:       command,
	}
	s.actionDone = false
	s.requestRedraw()
	return nil
}

func dockerActionKind(action appsysmanager.DockerAction) (appsysmanager.ActionKind, bool) {
	switch action {
	case appsysmanager.DockerStart:
		return appsysmanager.ActionDockerStart, true
	case appsysmanager.DockerStop:
		return appsysmanager.ActionDockerStop, true
	case appsysmanager.DockerRemove:
		return appsysmanager.ActionDockerRemove, true
	default:
		return "", false
	}
}

func containerDisplayName(container appsysmanager.Container) string {
	if name := strings.TrimSpace(container.Name); name != "" {
		return name
	}
	return container.ID
}

// DockerView returns 0 for containers and 1 for images.
func (s *State) DockerView() int { return s.dockerView }

// SetDockerView switches the Docker tab's list. The selection and any open
// document belong to the previous list, so both are cleared.
func (s *State) SetDockerView(view int) {
	if view != 0 && view != 1 {
		view = 0
	}
	if view == s.dockerView {
		return
	}
	s.dockerView = view
	s.selected = -1
	s.search = ""
	s.closeDocument()
	s.requestRedraw()
}

// InspectContainer loads a container's inspect output into the document
// panel. The read runs off the UI thread and is cancelled when the panel
// closes or the surface deactivates.
func (s *State) InspectContainer(container appsysmanager.Container) error {
	return s.loadDockerDocument(appsysmanager.DocumentInspect, container)
}

// ContainerLogs loads a bounded log tail into the document panel.
func (s *State) ContainerLogs(container appsysmanager.Container) error {
	return s.loadDockerDocument(appsysmanager.DocumentLogs, container)
}

func (s *State) loadDockerDocument(kind appsysmanager.DocumentKind,
	container appsysmanager.Container) error {
	if s.extended == nil {
		s.problem = ProblemInternal
		s.requestRedraw()
		return errNoExtendedService
	}
	var err error
	switch kind {
	case appsysmanager.DocumentInspect:
		_, err = appsysmanager.DockerInspectCommand(container.ID)
	default:
		_, err = appsysmanager.DockerLogsCommand(container.ID, appsysmanager.DefaultDockerLogTail)
	}
	if err != nil {
		return err
	}
	host, err := s.actionHost()
	if err != nil {
		return err
	}
	s.closeDocument()
	s.documentGen++
	gen := s.documentGen
	s.document = appsysmanager.DockerDocument{Kind: kind, ContainerID: container.ID}
	s.documentOn = true
	s.documentBusy = true
	s.documentErr = nil
	ctx, cancel := context.WithCancel(context.Background())
	s.docCancel = cancel
	extended := s.extended
	id := container.ID
	go func() {
		var document appsysmanager.DockerDocument
		var loadErr error
		if kind == appsysmanager.DocumentInspect {
			document, loadErr = extended.DockerInspect(ctx, host, id)
		} else {
			document, loadErr = extended.DockerLogs(ctx, host, id,
				appsysmanager.DefaultDockerLogTail)
		}
		cancel()
		s.mu.Lock()
		if !s.closed {
			s.docOutcomes = append(s.docOutcomes, documentOutcome{
				gen: gen, document: document, err: loadErr,
			})
		}
		s.mu.Unlock()
		s.requestRedraw()
	}()
	s.requestRedraw()
	return nil
}

// CloseDocument cancels a read and hides the document panel.
func (s *State) CloseDocument() {
	if !s.documentOn && s.docCancel == nil {
		return
	}
	s.closeDocument()
	s.requestRedraw()
}

func (s *State) closeDocument() {
	s.documentGen++
	if s.docCancel != nil {
		s.docCancel()
		s.docCancel = nil
	}
	s.documentOn = false
	s.documentBusy = false
	s.documentErr = nil
	s.document = appsysmanager.DockerDocument{}
}

// Document returns the open document and whether the panel is shown.
func (s *State) Document() (appsysmanager.DockerDocument, bool) {
	return s.document, s.documentOn
}

// DocumentBusy reports whether a document read is in flight.
func (s *State) DocumentBusy() bool { return s.documentOn && s.documentBusy }

// DocumentError returns a document read's failure.
func (s *State) DocumentError() error { return s.documentErr }

func (s *State) actionHost() (domainvault.Host, error) {
	host, ok := s.CurrentHost()
	if !ok {
		s.problem = ProblemNoHost
		return domainvault.Host{}, errors.New("sysmanager: no Host selected")
	}
	if s.service == nil {
		s.problem = ProblemInternal
		return domainvault.Host{}, errors.New("sysmanager: no service")
	}
	return host, nil
}

var (
	errNoExtendedService = errors.New("sysmanager: extended actions are unavailable")
	errUnknownAction     = errors.New("sysmanager: unknown action")
)

// ConfirmPending runs the pending command off the UI thread. It is the only
// path from a confirmation to the remote command.
func (s *State) ConfirmPending() {
	pending := s.pending
	if pending == nil {
		return
	}
	s.pending = nil
	s.startAction(*pending)
}

// startAction runs one validated action off the UI thread and publishes its
// outcome to Flush. Nothing here touches the UI.
func (s *State) startAction(pending PendingAction) {
	s.actionBusy = true
	s.actionDone = false
	s.actionErr = nil
	s.actionResult = appsysmanager.ActionResult{Kind: pending.Kind, Command: pending.Command}
	ctx, cancel := context.WithCancel(context.Background())
	s.actionCancel = cancel
	service := s.service
	extended := s.extended
	go func() {
		var result appsysmanager.ActionResult
		var err error
		switch {
		case pending.Kind == appsysmanager.ActionSignal:
			if service == nil {
				err = errors.New("sysmanager: no service")
			} else {
				result, err = service.Signal(ctx, pending.Host, pending.PID, pending.Signal)
			}
		case pending.Kind == appsysmanager.ActionServiceAction:
			if service == nil {
				err = errors.New("sysmanager: no service")
			} else {
				result, err = service.ServiceAction(ctx, pending.Host, pending.Unit,
					pending.Action, pending.Scope)
			}
		case pending.Kind == appsysmanager.ActionTmuxCreate,
			pending.Kind == appsysmanager.ActionTmuxKill,
			pending.Kind == appsysmanager.ActionDockerStart,
			pending.Kind == appsysmanager.ActionDockerStop,
			pending.Kind == appsysmanager.ActionDockerRemove:
			if extended == nil {
				err = errNoExtendedService
				break
			}
			switch pending.Kind {
			case appsysmanager.ActionTmuxCreate:
				result, err = extended.TmuxCreate(ctx, pending.Host, pending.Session)
			case appsysmanager.ActionTmuxKill:
				result, err = extended.TmuxKill(ctx, pending.Host, pending.Session)
			default:
				result, err = extended.DockerContainerAction(ctx, pending.Host,
					pending.ContainerID, pending.DockerAction)
			}
		default:
			err = errUnknownAction
		}
		cancel()
		s.mu.Lock()
		if !s.closed {
			s.outcomes = append(s.outcomes, actionOutcome{result: result, err: err})
		}
		s.mu.Unlock()
		s.requestRedraw()
	}()
	s.requestRedraw()
}

// CancelPending discards a confirmation, or cancels a running action. Either
// way no new command starts.
func (s *State) CancelPending() {
	if s.pending != nil {
		s.pending = nil
		s.requestRedraw()
	}
	if s.actionCancel != nil {
		s.actionCancel()
	}
}

// ActionBusy reports whether a confirmed command is in flight.
func (s *State) ActionBusy() bool { return s.actionBusy }

// ActionDone reports whether an action finished since the surface opened.
func (s *State) ActionDone() bool { return s.actionDone }

// ActionError returns the last action's failure.
func (s *State) ActionError() error { return s.actionErr }

// ActionResult returns the last action's result.
func (s *State) ActionResult() (appsysmanager.ActionResult, bool) {
	return s.actionResult, s.actionDone
}

// ActionCommand returns the last action's exact command.
func (s *State) ActionCommand() string { return s.actionResult.Command }

// DisplayProcesses returns the active Host's processes after search and the
// table's sort. Prepare refreshes the rows once per frame.
func (s *State) DisplayProcesses() []appsysmanager.Process { return s.processRows }

func (s *State) prepareProcesses(query string) []appsysmanager.Process {
	state := s.TabState(appsysmanager.TabProcesses)
	rows := make([]appsysmanager.Process, 0, len(state.Processes))
	for _, process := range state.Processes {
		if query != "" && !processMatches(process, query) {
			continue
		}
		rows = append(rows, process)
	}
	order := s.sort
	column := order.Column
	if column == "" {
		column = "cpu"
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		comparison := 0
		switch column {
		case "pid":
			comparison = compareInt(left.PID, right.PID)
		case "user":
			comparison = strings.Compare(strings.ToLower(left.User), strings.ToLower(right.User))
		case "mem":
			comparison = compareFloat(left.MemPercent, right.MemPercent)
		case "command":
			comparison = strings.Compare(strings.ToLower(left.Command), strings.ToLower(right.Command))
		default:
			comparison = compareFloat(left.CPUPercent, right.CPUPercent)
		}
		if comparison == 0 {
			return left.PID < right.PID
		}
		if order.Descending {
			return comparison > 0
		}
		return comparison < 0
	})
	return rows
}

func compareInt(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareFloat(left, right float64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func processMatches(process appsysmanager.Process, query string) bool {
	fields := []string{strconv.Itoa(process.PID), process.User, process.Stat, process.Command}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// DisplayPorts returns the active Host's listening sockets after search.
func (s *State) DisplayPorts() []appsysmanager.Port { return s.portRows }

func (s *State) preparePorts(query string) []appsysmanager.Port {
	state := s.TabState(appsysmanager.TabPorts)
	rows := make([]appsysmanager.Port, 0, len(state.Ports))
	for _, port := range state.Ports {
		if query != "" {
			fields := []string{strconv.Itoa(port.Port), port.Protocol, port.Address,
				port.ProcessName, strconv.Itoa(port.PID)}
			matched := false
			for _, field := range fields {
				if strings.Contains(strings.ToLower(field), query) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		rows = append(rows, port)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Port != rows[j].Port {
			return rows[i].Port < rows[j].Port
		}
		return rows[i].Protocol < rows[j].Protocol
	})
	return rows
}

// DisplayServices returns the active Host's units after search. The service
// layer already orders failed units first.
func (s *State) DisplayServices() []appsysmanager.Unit { return s.serviceRows }

func (s *State) prepareServices(query string) []appsysmanager.Unit {
	state := s.TabState(appsysmanager.TabServices)
	rows := make([]appsysmanager.Unit, 0, len(state.Services))
	for _, unit := range state.Services {
		if query != "" {
			fields := []string{unit.Name, unit.Description, unit.Scope,
				unit.ActiveState, unit.SubState}
			matched := false
			for _, field := range fields {
				if strings.Contains(strings.ToLower(field), query) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		rows = append(rows, unit)
	}
	return rows
}

// SelectRow chooses a displayed row for the action bar, or -1 to clear it.
func (s *State) SelectRow(index int) {
	if index < -1 {
		index = -1
	}
	s.selected = index
	s.requestRedraw()
}

// SelectedProcess returns the process the table has chosen.
func (s *State) SelectedProcess() (appsysmanager.Process, bool) {
	if s.selected < 0 || s.selected >= len(s.processRows) {
		return appsysmanager.Process{}, false
	}
	return s.processRows[s.selected], true
}

// SelectedPort returns the listening socket the table has chosen.
func (s *State) SelectedPort() (appsysmanager.Port, bool) {
	if s.selected < 0 || s.selected >= len(s.portRows) {
		return appsysmanager.Port{}, false
	}
	return s.portRows[s.selected], true
}

// SelectedService returns the unit the table has chosen.
func (s *State) SelectedService() (appsysmanager.Unit, bool) {
	if s.selected < 0 || s.selected >= len(s.serviceRows) {
		return appsysmanager.Unit{}, false
	}
	return s.serviceRows[s.selected], true
}

// DisplaySessions returns the active Host's tmux sessions after search.
func (s *State) DisplaySessions() []appsysmanager.TmuxSession { return s.sessionRows }

func (s *State) prepareSessions(query string) []appsysmanager.TmuxSession {
	state := s.TabState(appsysmanager.TabTmux)
	rows := make([]appsysmanager.TmuxSession, 0, len(state.Sessions))
	for _, session := range state.Sessions {
		if query != "" && !strings.Contains(strings.ToLower(session.Name), query) {
			continue
		}
		rows = append(rows, session)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	return rows
}

// DisplayContainers returns the Docker containers after search.
func (s *State) DisplayContainers() []appsysmanager.Container { return s.containerRows }

func (s *State) prepareContainers(query string) []appsysmanager.Container {
	state := s.TabState(appsysmanager.TabDocker)
	rows := make([]appsysmanager.Container, 0, len(state.Containers))
	for _, container := range state.Containers {
		if query != "" {
			fields := []string{container.ID, container.Name, container.Image,
				container.State, container.Status, container.Ports}
			matched := false
			for _, field := range fields {
				if strings.Contains(strings.ToLower(field), query) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		rows = append(rows, container)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(containerDisplayName(rows[i])) <
			strings.ToLower(containerDisplayName(rows[j]))
	})
	return rows
}

// DisplayImages returns the Docker images after search.
func (s *State) DisplayImages() []appsysmanager.Image { return s.imageRows }

func (s *State) prepareImages(query string) []appsysmanager.Image {
	state := s.TabState(appsysmanager.TabDocker)
	rows := make([]appsysmanager.Image, 0, len(state.Images))
	for _, image := range state.Images {
		if query != "" {
			fields := []string{image.ID, image.Repository, image.Tag,
				image.Name(), image.Size}
			matched := false
			for _, field := range fields {
				if strings.Contains(strings.ToLower(field), query) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		rows = append(rows, image)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Name()) < strings.ToLower(rows[j].Name())
	})
	return rows
}

// SelectedSession returns the tmux session the table has chosen.
func (s *State) SelectedSession() (appsysmanager.TmuxSession, bool) {
	if s.selected < 0 || s.selected >= len(s.sessionRows) {
		return appsysmanager.TmuxSession{}, false
	}
	return s.sessionRows[s.selected], true
}

// SelectedContainer returns the container the table has chosen.
func (s *State) SelectedContainer() (appsysmanager.Container, bool) {
	if s.dockerView != 0 || s.selected < 0 || s.selected >= len(s.containerRows) {
		return appsysmanager.Container{}, false
	}
	return s.containerRows[s.selected], true
}

// SelectedImage returns the image the table has chosen.
func (s *State) SelectedImage() (appsysmanager.Image, bool) {
	if s.dockerView != 1 || s.selected < 0 || s.selected >= len(s.imageRows) {
		return appsysmanager.Image{}, false
	}
	return s.imageRows[s.selected], true
}

func (s *State) rowKey(row int) any {
	switch s.tab {
	case appsysmanager.TabProcesses:
		if row >= 0 && row < len(s.processRows) {
			return "p:" + strconv.Itoa(s.processRows[row].PID)
		}
	case appsysmanager.TabPorts:
		if row >= 0 && row < len(s.portRows) {
			port := s.portRows[row]
			return "l:" + port.Protocol + ":" + port.Address + ":" +
				strconv.Itoa(port.Port) + ":" + strconv.Itoa(port.PID)
		}
	case appsysmanager.TabServices:
		if row >= 0 && row < len(s.serviceRows) {
			return "u:" + s.serviceRows[row].Scope + ":" + s.serviceRows[row].Name
		}
	case appsysmanager.TabTmux:
		if row >= 0 && row < len(s.sessionRows) {
			return "t:" + s.sessionRows[row].Name
		}
	case appsysmanager.TabDocker:
		if s.dockerView == 1 {
			if row >= 0 && row < len(s.imageRows) {
				return "i:" + s.imageRows[row].ID
			}
		} else if row >= 0 && row < len(s.containerRows) {
			return "c:" + s.containerRows[row].ID
		}
	}
	return row
}

func (s *State) rowLabel(row int) string {
	switch s.tab {
	case appsysmanager.TabProcesses:
		if row >= 0 && row < len(s.processRows) {
			return "PID " + strconv.Itoa(s.processRows[row].PID) + " " + s.processRows[row].Command
		}
	case appsysmanager.TabPorts:
		if row >= 0 && row < len(s.portRows) {
			port := s.portRows[row]
			return port.Protocol + " " + port.Address + ":" + strconv.Itoa(port.Port)
		}
	case appsysmanager.TabServices:
		if row >= 0 && row < len(s.serviceRows) {
			return s.serviceRows[row].Name
		}
	case appsysmanager.TabTmux:
		if row >= 0 && row < len(s.sessionRows) {
			return s.sessionRows[row].Name
		}
	case appsysmanager.TabDocker:
		if s.dockerView == 1 {
			if row >= 0 && row < len(s.imageRows) {
				return s.imageRows[row].Name()
			}
		} else if row >= 0 && row < len(s.containerRows) {
			return containerDisplayName(s.containerRows[row])
		}
	}
	return ""
}

func (s *State) requestRedraw() {
	if s.redraw != nil {
		s.redraw()
	}
}

func preferenceTab(tab appsysmanager.Tab) domainpreferences.SysManagerTab {
	return domainpreferences.SysManagerTab(tab)
}
