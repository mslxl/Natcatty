// Package sysmanager owns the per-Host system-manager use cases: probing the
// remote command inventory, sampling the overview, processes, listening
// ports, and systemd services, and running the confirmed destructive actions.
//
// The remote commands are fixed POSIX shell templates; a user cannot extend
// them. Every command runs through a Connection lease opened by the injected
// Connector, so the same host-key trust decision a Session uses gates every
// sample and action. The package is pure Go: it imports no adapter and no UI.
package sysmanager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Tab is one independently refreshed surface.
type Tab string

const (
	TabOverview  Tab = "overview"
	TabProcesses Tab = "processes"
	TabPorts     Tab = "ports"
	TabServices  Tab = "services"
	TabTmux      Tab = "tmux"
	TabDocker    Tab = "docker"
	TabGPU       Tab = "gpu"
)

// Tabs lists ticket 45's core tabs in display order. Ticket 46 extends the
// surface with ExtendedTabs; keeping the core list stable lets the core
// self-test and its callers enumerate exactly what they cover.
var Tabs = []Tab{TabOverview, TabProcesses, TabPorts, TabServices}

// ExtendedTabs lists every tab in the original's default display order.
var ExtendedTabs = []Tab{
	TabOverview, TabProcesses, TabPorts, TabServices, TabTmux, TabDocker, TabGPU,
}

// DefaultInterval returns the original's refresh cadence for tab: three
// seconds for overview, processes, ports, tmux, and GPU, and five for
// services and the Docker list.
func DefaultInterval(tab Tab) time.Duration {
	if tab == TabServices || tab == TabDocker {
		return 5 * time.Second
	}
	return 3 * time.Second
}

// Status is a tab's sampling state. It never carries display text; the
// surface localizes each value.
type Status string

const (
	// StatusIdle is a tab that has not sampled yet.
	StatusIdle Status = "idle"
	// StatusConnecting is a tab whose transport or capability probe is
	// under way.
	StatusConnecting Status = "connecting"
	// StatusLive is a tab whose last sample succeeded.
	StatusLive Status = "live"
	// StatusLogin is a Host whose credentials or host key were refused.
	StatusLogin Status = "login"
	// StatusError is a recoverable failure; the tab retries.
	StatusError Status = "error"
	// StatusUnsupported is an operating system the manager cannot inspect.
	StatusUnsupported Status = "unsupported"
	// StatusUnavailable is a typed degraded state for a missing remote
	// command. The other tabs keep working.
	StatusUnavailable Status = "unavailable"
)

// Reason classifies a status for localized display.
type Reason string

const (
	ReasonNone             Reason = ""
	ReasonHostKey          Reason = "host_key"
	ReasonAuth             Reason = "auth"
	ReasonNetwork          Reason = "network"
	ReasonTimeout          Reason = "timeout"
	ReasonOS               Reason = "os"
	ReasonCancelled        Reason = "cancelled"
	ReasonInternal         Reason = "internal"
	ReasonPermission       Reason = "permission"
	ReasonUnsupported      Reason = "unsupported"
	ReasonMissingProcesses Reason = "missing_processes"
	ReasonMissingPorts     Reason = "missing_ports"
	ReasonMissingSystemctl Reason = "missing_systemctl"
	ReasonMissingTmux      Reason = "missing_tmux"
	ReasonMissingDocker    Reason = "missing_docker"
	ReasonDockerDaemon     Reason = "docker_daemon"
)

// Error is a typed application failure. The surface maps Reason to a
// localized message; Detail stays a developer diagnostic.
type Error struct {
	Reason Reason
	Cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Reason)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ExecResult is one remote command's reply.
type ExecResult struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	HasExitCode bool
}

// Connector opens one channel-capable lease to a Host. The implementation
// must run the shared host-key trust path before returning.
type Connector interface {
	Open(ctx context.Context, host domainvault.Host) (Connection, error)
}

// Connection is a leased transport used only for exec commands.
type Connection interface {
	Exec(ctx context.Context, command string) (ExecResult, error)
	Close() error
}

// Capabilities is the probed remote command inventory.
type Capabilities struct {
	OS           string
	HasPs        bool
	HasTop       bool
	HasSs        bool
	HasNetstat   bool
	HasLsof      bool
	HasSystemctl bool
	HasTmux      bool
	HasDocker    bool
	HasNvidiaSmi bool
	HasNpuSmi    bool
}

// Available reports whether tab can run for these capabilities, and the
// typed reason when it cannot.
func (c Capabilities) Available(tab Tab) (bool, Reason) {
	switch tab {
	case TabOverview:
		if c.OS == "linux" || c.OS == "darwin" {
			return true, ReasonNone
		}
		return false, ReasonOS
	case TabProcesses:
		if c.HasPs || c.HasTop {
			return true, ReasonNone
		}
		return false, ReasonMissingProcesses
	case TabPorts:
		if c.HasSs || c.HasNetstat || c.HasLsof {
			return true, ReasonNone
		}
		return false, ReasonMissingPorts
	case TabServices:
		if c.HasSystemctl {
			return true, ReasonNone
		}
		return false, ReasonMissingSystemctl
	case TabTmux:
		if c.HasTmux {
			return true, ReasonNone
		}
		return false, ReasonMissingTmux
	case TabDocker:
		if c.HasDocker {
			return true, ReasonNone
		}
		return false, ReasonMissingDocker
	case TabGPU:
		// A Host without a GPU is a normal, live empty state rather than an
		// unavailable tab; only an operating system this probe cannot read
		// makes the tab unsupported.
		if c.OS == "linux" || c.OS == "darwin" {
			return true, ReasonNone
		}
		return false, ReasonOS
	}
	return false, ReasonInternal
}

// Overview is the summary fields the original showed for a Host.
type Overview struct {
	Hostname      string
	OSName        string
	KernelRelease string
	UptimeSeconds float64
	LoadAverage   []float64
	Cores         int
	// CPUPercent is NaN until a second Linux counter reading establishes a
	// delta; macOS reports a direct percentage.
	CPUPercent  float64
	MemUsedMB   float64
	MemTotalMB  float64
	SwapUsedMB  float64
	SwapTotalMB float64
	DiskUsedGB  float64
	DiskTotalGB float64
}

// Process is one row of the process list.
type Process struct {
	PID        int
	PPID       int
	User       string
	Stat       string
	CPUPercent float64
	MemPercent float64
	RSSKB      int64
	VSZKB      int64
	Elapsed    string
	Command    string
}

// Port is one listening socket. PID is zero when the collector could not
// attribute it; ProcessName is then empty too.
type Port struct {
	Protocol    string
	Address     string
	Port        int
	PID         int
	ProcessName string
}

// Unit is one systemd unit.
type Unit struct {
	Name        string
	LoadState   string
	ActiveState string
	SubState    string
	Description string
	Scope       string
}

// TmuxSession is one tmux session.
type TmuxSession struct {
	Name     string
	Windows  int
	Attached bool
	// Created is the Unix creation timestamp; zero when the reply used the
	// plain list format, which does not include it.
	Created int64
}

// Container is one Docker container from `docker ps -a`.
type Container struct {
	ID        string
	Name      string
	Image     string
	State     string
	Status    string
	Ports     string
	CreatedAt string
}

// Image is one Docker image from `docker images`.
type Image struct {
	ID         string
	Repository string
	Tag        string
	Size       string
	CreatedAt  string
	Digest     string
}

// Name is the repository and tag as one display value. A dangling image has
// no repository, so its id identifies it instead of Docker's "<none>" text.
func (i Image) Name() string {
	if i.Repository != "" && i.Repository != "<none>" {
		if i.Tag != "" && i.Tag != "<none>" {
			return i.Repository + ":" + i.Tag
		}
		return i.Repository
	}
	return i.ID
}

// GPUDevice is one NVIDIA device from nvidia-smi. A metric the driver does
// not report is NaN, so the surface can render a dash instead of a zero.
type GPUDevice struct {
	Vendor             string
	Index              int
	UUID               string
	Name               string
	UtilizationPercent float64
	MemoryUsedMB       float64
	MemoryTotalMB      float64
	TemperatureC       float64
	PowerDrawW         float64
	PowerLimitW        float64
	FanPercent         float64
	DriverVersion      string
}

// GPUInfo is one Host's accelerator inventory. An empty Devices slice is the
// normal state on a Host without a GPU.
type GPUInfo struct {
	Devices       []GPUDevice
	DriverVersion string
}

// DocumentKind names a read-only Docker output view.
type DocumentKind string

const (
	DocumentInspect DocumentKind = "inspect"
	DocumentLogs    DocumentKind = "logs"
)

// DockerDocument is a bounded inspect or log reply for the surface.
type DockerDocument struct {
	Kind        DocumentKind
	ContainerID string
	Text        string
	Truncated   bool
}

// TabState is one tab's latest sample and status.
type TabState struct {
	Tab        Tab
	Status     Status
	Reason     Reason
	Detail     string
	At         time.Time
	Latency    time.Duration
	Overview   Overview
	Processes  []Process
	Ports      []Port
	Services   []Unit
	Sessions   []TmuxSession
	Containers []Container
	Images     []Image
	GPU        GPUInfo
}

// Snapshot is the service's publication to the surface. Slices and maps are
// copies; a reader may not mutate service state.
type Snapshot struct {
	HostID       string
	Active       Tab
	Capabilities Capabilities
	Tabs         map[Tab]TabState
}

// ServiceAction is a systemd unit action the manager may run.
type ServiceAction string

const (
	ServiceStart   ServiceAction = "start"
	ServiceStop    ServiceAction = "stop"
	ServiceRestart ServiceAction = "restart"
)

// Scope is a unit's systemd scope.
type Scope string

const (
	ScopeSystem Scope = "system"
	ScopeUser   Scope = "user"
)

// ActionKind identifies a confirmed destructive action.
type ActionKind string

const (
	ActionSignal        ActionKind = "signal"
	ActionServiceAction ActionKind = "service"
	ActionTmuxCreate    ActionKind = "tmux_create"
	ActionTmuxKill      ActionKind = "tmux_kill"
	ActionDockerStart   ActionKind = "docker_start"
	ActionDockerStop    ActionKind = "docker_stop"
	ActionDockerRemove  ActionKind = "docker_remove"
	ActionDockerInspect ActionKind = "docker_inspect"
	ActionDockerLogs    ActionKind = "docker_logs"
)

// ActionResult reports the command that ran and its reply.
type ActionResult struct {
	Kind        ActionKind
	Command     string
	Stdout      string
	Stderr      string
	ExitCode    int
	HasExitCode bool
}

// Option customizes a Service.
type Option func(*Service)

// WithClock replaces the source of time. Tests use it for stable samples.
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// Service samples one Host's active tab off the UI thread and runs confirmed
// actions. Start, Stop, Refresh, and Snapshot are called on the UI thread;
// sampling and action goroutines publish through the lock.
type Service struct {
	connector Connector
	now       func() time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	host      domainvault.Host
	active    Tab
	interval  time.Duration
	caps      Capabilities
	capsHost  string
	tabs      map[Tab]TabState
	runCancel context.CancelFunc
	gen       uint64
	listeners map[int]func()
	nextID    int
	closed    bool
	runs      sync.WaitGroup
	actions   sync.WaitGroup
}

// NewService returns a system-manager service over connector.
func NewService(connector Connector, opts ...Option) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		connector: connector,
		now:       time.Now,
		ctx:       ctx,
		cancel:    cancel,
		tabs:      make(map[Tab]TabState),
		listeners: make(map[int]func()),
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// Subscribe adds a redraw listener and returns its unsubscribe function. The
// listener runs outside the service lock and must not block.
func (s *Service) Subscribe(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.listeners[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

// Start samples host's tab at interval. It replaces the previous demand; a
// Host change clears the cached tab data so one Host's rows never show under
// another. An interval at or below zero uses tab's default.
func (s *Service) Start(host domainvault.Host, tab Tab, interval time.Duration) error {
	if s.connector == nil {
		return ErrNoConnector
	}
	if string(host.ID) == "" {
		return ErrNoHost
	}
	if interval <= 0 {
		interval = DefaultInterval(tab)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if string(s.host.ID) != string(host.ID) {
		// A Host switch must never show the previous Host's rows under the
		// new name, and its capability probe does not carry over.
		s.tabs = make(map[Tab]TabState)
		s.caps = Capabilities{}
		s.capsHost = ""
	}
	s.host = host
	s.active = tab
	s.interval = interval
	s.restartLocked()
	s.mu.Unlock()
	s.notify()
	return nil
}

// Stop ends sampling but keeps the cached samples for the next activation.
func (s *Service) Stop() {
	s.mu.Lock()
	if s.runCancel != nil {
		s.runCancel()
		s.runCancel = nil
	}
	s.mu.Unlock()
}

// Refresh restarts the active tab immediately.
func (s *Service) Refresh() {
	s.mu.Lock()
	if s.closed || string(s.host.ID) == "" {
		s.mu.Unlock()
		return
	}
	s.restartLocked()
	s.mu.Unlock()
	s.notify()
}

// restartLocked cancels the current sampler and starts one for the stored
// demand. The caller holds s.mu.
func (s *Service) restartLocked() {
	if s.runCancel != nil {
		s.runCancel()
		s.runCancel = nil
	}
	tab := s.active
	state, ok := s.tabs[tab]
	if !ok {
		state = TabState{Tab: tab, Status: StatusConnecting, Reason: ReasonNone, At: s.now()}
	} else if state.Status != StatusLive {
		// Keep a live sample visible while the retry runs; a degraded or
		// failed tab shows that it is checking again.
		state.Status = StatusConnecting
		state.Reason = ReasonNone
		state.Detail = ""
		state.At = s.now()
	}
	s.tabs[tab] = state
	ctx, cancel := context.WithCancel(s.ctx)
	s.runCancel = cancel
	s.gen++
	gen := s.gen
	host := s.host
	interval := s.interval
	s.runs.Add(1)
	go s.run(ctx, host, tab, interval, gen)
}

// Close stops sampling, cancels actions, and releases listeners. It is safe
// to call more than once.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.runCancel != nil {
		s.runCancel()
		s.runCancel = nil
	}
	s.listeners = make(map[int]func())
	s.mu.Unlock()
	s.cancel()
}

// Snapshot returns an immutable copy of the current state.
func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := Snapshot{
		HostID:       string(s.host.ID),
		Active:       s.active,
		Capabilities: s.caps,
		Tabs:         make(map[Tab]TabState, len(s.tabs)),
	}
	for tab, state := range s.tabs {
		state.Processes = append([]Process(nil), state.Processes...)
		state.Ports = append([]Port(nil), state.Ports...)
		state.Services = append([]Unit(nil), state.Services...)
		state.Sessions = append([]TmuxSession(nil), state.Sessions...)
		state.Containers = append([]Container(nil), state.Containers...)
		state.Images = append([]Image(nil), state.Images...)
		state.GPU.Devices = append([]GPUDevice(nil), state.GPU.Devices...)
		state.Overview.LoadAverage = append([]float64(nil), state.Overview.LoadAverage...)
		snapshot.Tabs[tab] = state
	}
	return snapshot
}

func (s *Service) run(ctx context.Context, host domainvault.Host, tab Tab, interval time.Duration, gen uint64) {
	defer s.runs.Done()

	var (
		connection Connection
		previous   overviewCounters
		failures   int
	)
	defer func() { closeConnection(&connection) }()

	for {
		if ctx.Err() != nil {
			return
		}
		stop, err := s.sampleOnce(ctx, host, tab, gen, &connection, &previous)
		if ctx.Err() != nil {
			return
		}
		if stop {
			return
		}
		delay := interval
		if err != nil {
			failures++
			if failures >= 2 && delay < interval*2 {
				delay = interval * 2
			}
			if delay > 6*interval {
				delay = 6 * interval
			}
		} else {
			failures = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// sampleOnce ensures trust and capabilities, then publishes one tab sample.
// stop reports a terminal state that retrying cannot change.
func (s *Service) sampleOnce(ctx context.Context, host domainvault.Host, tab Tab, gen uint64,
	connection *Connection, previous *overviewCounters) (bool, error) {
	if *connection == nil {
		s.update(string(host.ID), tab, gen, func(state *TabState) {
			state.Status = StatusConnecting
			state.Reason = ReasonNone
			state.Detail = ""
			state.At = s.now()
		})
		opened, err := s.connector.Open(ctx, host)
		if err != nil {
			return s.fail(string(host.ID), tab, gen, err)
		}
		*connection = opened
	}
	caps, err := s.ensureCapabilities(ctx, host, *connection, gen, tab)
	if err != nil {
		closeConnection(connection)
		return s.fail(string(host.ID), tab, gen, err)
	}
	if ok, reason := caps.Available(tab); !ok {
		status := StatusUnavailable
		if reason == ReasonOS {
			status = StatusUnsupported
		}
		s.update(string(host.ID), tab, gen, func(state *TabState) {
			state.Status = status
			state.Reason = reason
			state.Detail = ""
			state.At = s.now()
		})
		return true, nil
	}

	command := commandFor(tab)
	started := s.now()
	result, err := (*connection).Exec(ctx, command)
	latency := s.now().Sub(started)
	if err != nil {
		closeConnection(connection)
		return s.fail(string(host.ID), tab, gen, err)
	}
	if result.HasExitCode && result.ExitCode != 0 && result.Stdout == "" {
		closeConnection(connection)
		return s.fail(string(host.ID), tab, gen, commandFailure(result, ""))
	}

	switch tab {
	case TabOverview:
		sample, parseErr := parseOverview(result.Stdout)
		if parseErr != nil {
			return s.fail(string(host.ID), tab, gen, parseErr)
		}
		overview := sample.overview
		overview.CPUPercent = sample.cpuPercent(*previous)
		*previous = sample.counters()
		s.update(string(host.ID), tab, gen, func(state *TabState) {
			state.Status = StatusLive
			state.Reason = ReasonNone
			state.Detail = ""
			state.At = s.now()
			state.Latency = latency
			state.Overview = overview
		})
	case TabProcesses:
		processes, parseErr := parseProcesses(result.Stdout)
		if parseErr != nil {
			return s.fail(string(host.ID), tab, gen, parseErr)
		}
		s.update(string(host.ID), tab, gen, func(state *TabState) {
			state.Status = StatusLive
			state.Reason = ReasonNone
			state.Detail = ""
			state.At = s.now()
			state.Latency = latency
			state.Processes = processes
		})
	case TabPorts:
		ports, parseErr := parsePorts(result.Stdout)
		if parseErr != nil {
			return s.fail(string(host.ID), tab, gen, parseErr)
		}
		s.update(string(host.ID), tab, gen, func(state *TabState) {
			state.Status = StatusLive
			state.Reason = ReasonNone
			state.Detail = ""
			state.At = s.now()
			state.Latency = latency
			state.Ports = ports
		})
	case TabServices:
		services, parseErr := parseServices(result.Stdout)
		if parseErr != nil {
			return s.fail(string(host.ID), tab, gen, parseErr)
		}
		s.update(string(host.ID), tab, gen, func(state *TabState) {
			state.Status = StatusLive
			state.Reason = ReasonNone
			state.Detail = ""
			state.At = s.now()
			state.Latency = latency
			state.Services = services
		})
	case TabTmux:
		sessions, parseErr := parseTmuxSessions(result.Stdout)
		if parseErr != nil {
			return s.fail(string(host.ID), tab, gen, parseErr)
		}
		s.update(string(host.ID), tab, gen, func(state *TabState) {
			state.Status = StatusLive
			state.Reason = ReasonNone
			state.Detail = ""
			state.At = s.now()
			state.Latency = latency
			state.Sessions = sessions
		})
	case TabDocker:
		containers, images, parseErr := parseDockerList(result.Stdout)
		if parseErr != nil {
			return s.fail(string(host.ID), tab, gen, parseErr)
		}
		s.update(string(host.ID), tab, gen, func(state *TabState) {
			state.Status = StatusLive
			state.Reason = ReasonNone
			state.Detail = ""
			state.At = s.now()
			state.Latency = latency
			state.Containers = containers
			state.Images = images
		})
	case TabGPU:
		info, parseErr := parseGPU(result.Stdout)
		if parseErr != nil {
			return s.fail(string(host.ID), tab, gen, parseErr)
		}
		s.update(string(host.ID), tab, gen, func(state *TabState) {
			state.Status = StatusLive
			state.Reason = ReasonNone
			state.Detail = ""
			state.At = s.now()
			state.Latency = latency
			state.GPU = info
		})
	}
	return false, nil
}

// fail records a typed failure and reports whether the run must stop.
func (s *Service) fail(hostID string, tab Tab, gen uint64, err error) (bool, error) {
	reason := failureReason(err)
	status := StatusError
	stop := false
	switch reason {
	case ReasonCancelled:
		return true, err
	case ReasonHostKey, ReasonAuth:
		status = StatusLogin
		stop = true
	case ReasonOS, ReasonMissingProcesses, ReasonMissingPorts, ReasonMissingSystemctl,
		ReasonMissingTmux, ReasonMissingDocker, ReasonDockerDaemon, ReasonPermission:
		status = StatusUnavailable
		if reason == ReasonOS {
			status = StatusUnsupported
		}
		stop = true
	case ReasonTimeout, ReasonNetwork:
		status = StatusError
	default:
		status = StatusError
	}
	s.update(hostID, tab, gen, func(state *TabState) {
		state.Status = status
		state.Reason = reason
		state.Detail = err.Error()
		state.At = s.now()
	})
	return stop, err
}

func (s *Service) ensureCapabilities(ctx context.Context, host domainvault.Host,
	connection Connection, gen uint64, tab Tab) (Capabilities, error) {
	id := string(host.ID)
	s.mu.Lock()
	if s.capsHost == id {
		caps := s.caps
		s.mu.Unlock()
		return caps, nil
	}
	s.mu.Unlock()

	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	result, err := connection.Exec(probeCtx, probeCommand())
	cancel()
	if err != nil {
		return Capabilities{}, err
	}
	caps := parseCapabilities(result.Stdout)
	s.mu.Lock()
	s.caps = caps
	s.capsHost = id
	s.mu.Unlock()
	return caps, nil
}

func (s *Service) update(hostID string, tab Tab, gen uint64, change func(*TabState)) {
	s.mu.Lock()
	if s.gen != gen || string(s.host.ID) != hostID {
		s.mu.Unlock()
		return
	}
	state, ok := s.tabs[tab]
	if !ok {
		state = TabState{Tab: tab}
	}
	change(&state)
	s.tabs[tab] = state
	s.mu.Unlock()
	s.notify()
}

func (s *Service) notify() {
	s.mu.Lock()
	listeners := make([]func(), 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

func failureReason(err error) Reason {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Reason
	}
	if errors.Is(err, context.Canceled) {
		return ReasonCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonTimeout
	}
	return ReasonInternal
}

func commandFailure(result ExecResult, fallback string) error {
	detail := result.Stderr
	if detail == "" {
		detail = fallback
	}
	if detail == "" {
		detail = fmt.Sprintf("command exited with code %d", result.ExitCode)
	}
	reason := ReasonInternal
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "operation not permitted"),
		strings.Contains(lower, "not authorized"):
		reason = ReasonPermission
	case strings.Contains(lower, "cannot connect to the docker daemon"),
		strings.Contains(lower, "is the docker daemon running"),
		strings.Contains(lower, "error during connect"):
		reason = ReasonDockerDaemon
	case strings.Contains(lower, "command not found"),
		strings.Contains(lower, "no such file or directory"):
		switch {
		case strings.Contains(lower, "docker"):
			reason = ReasonMissingDocker
		case strings.Contains(lower, "tmux"):
			reason = ReasonMissingTmux
		}
	}
	return &Error{Reason: reason, Cause: errors.New(detail)}
}

func closeConnection(connection *Connection) {
	if connection != nil && *connection != nil {
		_ = (*connection).Close()
		*connection = nil
	}
}

// ErrNoConnector is returned when a Service has no Connector.
var ErrNoConnector = errors.New("sysmanager: no connector")

// ErrNoHost is returned when Start has no Host.
var ErrNoHost = errors.New("sysmanager: no Host")

// ErrClosed is returned after Close.
var ErrClosed = errors.New("sysmanager: service is closed")

// Valid reports whether value is a usable measurement.
func Valid(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
