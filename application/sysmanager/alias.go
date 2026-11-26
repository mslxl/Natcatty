// Package sysmanager is the desktop composition's name for the shared
// System Manager implementation. The implementation lives in the desktop-free
// core subpackage, which the phone composition root imports directly: the
// phone dependency gate forbids this package's exact path, so the shared state
// and actions must be reachable without it. These aliases keep the desktop
// imports and their tests on the existing names.
package sysmanager

import (
	"time"

	"github.com/mslxl/natcatty/application/sysmanager/core"
)

type (
	Tab              = core.Tab
	Status           = core.Status
	Reason           = core.Reason
	Error            = core.Error
	ExecResult       = core.ExecResult
	Connector        = core.Connector
	Connection       = core.Connection
	Capabilities     = core.Capabilities
	Overview         = core.Overview
	Process          = core.Process
	Port             = core.Port
	Unit             = core.Unit
	TmuxSession      = core.TmuxSession
	Container        = core.Container
	Image            = core.Image
	Pod              = core.Pod
	Kubernetes       = core.Kubernetes
	GPUDevice        = core.GPUDevice
	GPUInfo          = core.GPUInfo
	DocumentKind     = core.DocumentKind
	DockerDocument   = core.DockerDocument
	TabState         = core.TabState
	Snapshot         = core.Snapshot
	ServiceAction    = core.ServiceAction
	Scope            = core.Scope
	ActionKind       = core.ActionKind
	ActionResult     = core.ActionResult
	Option           = core.Option
	Service          = core.Service
	DockerAction     = core.DockerAction
	ContainerRuntime = core.ContainerRuntime
	PendingAction    = core.PendingAction
	Origin           = core.Origin
)

const (
	TabOverview   = core.TabOverview
	TabProcesses  = core.TabProcesses
	TabPorts      = core.TabPorts
	TabServices   = core.TabServices
	TabTmux       = core.TabTmux
	TabDocker     = core.TabDocker
	TabKubernetes = core.TabKubernetes
	TabGPU        = core.TabGPU

	StatusIdle        = core.StatusIdle
	StatusConnecting  = core.StatusConnecting
	StatusLive        = core.StatusLive
	StatusLogin       = core.StatusLogin
	StatusError       = core.StatusError
	StatusUnsupported = core.StatusUnsupported
	StatusUnavailable = core.StatusUnavailable

	ReasonNone                    = core.ReasonNone
	ReasonHostKey                 = core.ReasonHostKey
	ReasonAuth                    = core.ReasonAuth
	ReasonNetwork                 = core.ReasonNetwork
	ReasonTimeout                 = core.ReasonTimeout
	ReasonOS                      = core.ReasonOS
	ReasonCancelled               = core.ReasonCancelled
	ReasonInternal                = core.ReasonInternal
	ReasonPermission              = core.ReasonPermission
	ReasonUnsupported             = core.ReasonUnsupported
	ReasonMissingProcesses        = core.ReasonMissingProcesses
	ReasonMissingPorts            = core.ReasonMissingPorts
	ReasonMissingSystemctl        = core.ReasonMissingSystemctl
	ReasonMissingTmux             = core.ReasonMissingTmux
	ReasonMissingDocker           = core.ReasonMissingDocker
	ReasonDockerDaemon            = core.ReasonDockerDaemon
	ReasonMissingContainerRuntime = core.ReasonMissingContainerRuntime
	ReasonContainerDaemon         = core.ReasonContainerDaemon
	ReasonMissingKubectl          = core.ReasonMissingKubectl
	ReasonKubernetesForbidden     = core.ReasonKubernetesForbidden
	ReasonClusterUnreachable      = core.ReasonClusterUnreachable

	DocumentInspect = core.DocumentInspect
	DocumentLogs    = core.DocumentLogs

	ServiceStart   = core.ServiceStart
	ServiceStop    = core.ServiceStop
	ServiceRestart = core.ServiceRestart

	ScopeSystem = core.ScopeSystem
	ScopeUser   = core.ScopeUser

	ActionSignal        = core.ActionSignal
	ActionServiceAction = core.ActionServiceAction
	ActionTmuxCreate    = core.ActionTmuxCreate
	ActionTmuxKill      = core.ActionTmuxKill
	ActionDockerStart   = core.ActionDockerStart
	ActionDockerStop    = core.ActionDockerStop
	ActionDockerRemove  = core.ActionDockerRemove
	ActionDockerInspect = core.ActionDockerInspect
	ActionDockerLogs    = core.ActionDockerLogs

	DockerStart  = core.DockerStart
	DockerStop   = core.DockerStop
	DockerRemove = core.DockerRemove

	RuntimeDocker  = core.RuntimeDocker
	RuntimePodman  = core.RuntimePodman
	RuntimeNerdctl = core.RuntimeNerdctl

	OriginProcess = core.OriginProcess
	OriginPort    = core.OriginPort

	MaxDockerLogTail     = core.MaxDockerLogTail
	DefaultDockerLogTail = core.DefaultDockerLogTail
)

var (
	Tabs              = core.Tabs
	ExtendedTabs      = core.ExtendedTabs
	ContainerRuntimes = core.ContainerRuntimes
	ErrClosed         = core.ErrClosed
	ErrNoConnector    = core.ErrNoConnector
	ErrNoHost         = core.ErrNoHost
	ErrUnknownAction  = core.ErrUnknownAction
)

// DefaultInterval returns a tab's default refresh cadence.
func DefaultInterval(tab Tab) time.Duration { return core.DefaultInterval(tab) }

// WithClock replaces the service's source of time.
func WithClock(now func() time.Time) Option { return core.WithClock(now) }

// NewService returns a System Manager service over connector.
func NewService(connector Connector, opts ...Option) *Service {
	return core.NewService(connector, opts...)
}

// NormalizeContainerRuntime returns a known engine, defaulting to Docker.
func NormalizeContainerRuntime(runtime ContainerRuntime) ContainerRuntime {
	return core.NormalizeContainerRuntime(runtime)
}

// Valid reports whether value is a usable measurement.
func Valid(value float64) bool { return core.Valid(value) }

// SignalCommand validates a process signal target and returns its command.
func SignalCommand(pid int, signal string) (string, error) {
	return core.SignalCommand(pid, signal)
}

// ServiceCommand validates a unit action and returns its command.
func ServiceCommand(unit string, action ServiceAction, scope Scope) (string, error) {
	return core.ServiceCommand(unit, action, scope)
}

// TmuxCreateCommand validates a session name and returns its create command.
func TmuxCreateCommand(name string) (string, error) { return core.TmuxCreateCommand(name) }

// TmuxKillCommand validates a session name and returns its kill command.
func TmuxKillCommand(name string) (string, error) { return core.TmuxKillCommand(name) }

// TmuxAttachCommand validates a session name and returns its attach command.
func TmuxAttachCommand(name string) (string, error) { return core.TmuxAttachCommand(name) }

// ContainerActionCommand validates a container action and returns its command.
func ContainerActionCommand(runtime ContainerRuntime, id string, action DockerAction) (string, error) {
	return core.ContainerActionCommand(runtime, id, action)
}

// DockerActionCommand is the Docker-only ContainerActionCommand form.
func DockerActionCommand(id string, action DockerAction) (string, error) {
	return core.DockerActionCommand(id, action)
}

// ContainerInspectCommand returns the inspect command for id.
func ContainerInspectCommand(runtime ContainerRuntime, id string) (string, error) {
	return core.ContainerInspectCommand(runtime, id)
}

// DockerInspectCommand is the Docker-only ContainerInspectCommand form.
func DockerInspectCommand(id string) (string, error) { return core.DockerInspectCommand(id) }

// ContainerLogsCommand returns a bounded, non-following logs command.
func ContainerLogsCommand(runtime ContainerRuntime, id string, tail int) (string, error) {
	return core.ContainerLogsCommand(runtime, id, tail)
}

// DockerLogsCommand is the Docker-only ContainerLogsCommand form.
func DockerLogsCommand(id string, tail int) (string, error) {
	return core.DockerLogsCommand(id, tail)
}

// KubectlExecCommand returns the interactive command sent to a terminal Tab.
func KubectlExecCommand(namespace, pod, container string) (string, error) {
	return core.KubectlExecCommand(namespace, pod, container)
}

// DockerActionKind maps a container action to its result kind.
func DockerActionKind(action DockerAction) (ActionKind, bool) {
	return core.DockerActionKind(action)
}

// ContainerDisplayName prefers a container's name and falls back to its ID.
func ContainerDisplayName(container Container) string {
	return core.ContainerDisplayName(container)
}

// The core package must not depend on this alias package; keeping the import
// direction one-way is what lets the phone import core alone.
