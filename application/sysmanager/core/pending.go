package core

import (
	"context"
	"errors"
	"strings"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Origin identifies where a confirmed signal came from, so a confirmation can
// name the process or the listening socket.
type Origin string

const (
	OriginProcess Origin = "process"
	OriginPort    Origin = "port"
)

// PendingAction is one destructive command awaiting confirmation. Requesting
// it does not run anything; only RunPending opens a lease. The desktop System
// manager and the phone System Manager share this value, so the confirmation
// a user sees names the same target on both surfaces.
type PendingAction struct {
	Kind     ActionKind
	Origin   Origin
	Host     domainvault.Host
	PID      int
	Signal   string
	Unit     string
	Action   ServiceAction
	Scope    Scope
	Command  string
	Address  string
	Port     int
	Protocol string
	// Session is the tmux session a create or kill targets.
	Session string
	// ContainerID is the Docker target; ContainerName is its display name.
	ContainerID   string
	ContainerName string
	DockerAction  DockerAction
	// Runtime is the container engine the confirmed command runs through.
	Runtime ContainerRuntime
}

// RunPending executes one already-confirmed action. It is the only path from
// a confirmation to a remote command, so both surfaces open the same trusted
// lease through the same Service method. The caller runs it off the UI thread.
func (s *Service) RunPending(ctx context.Context, pending PendingAction) (ActionResult, error) {
	switch pending.Kind {
	case ActionSignal:
		return s.Signal(ctx, pending.Host, pending.PID, pending.Signal)
	case ActionServiceAction:
		return s.ServiceAction(ctx, pending.Host, pending.Unit, pending.Action, pending.Scope)
	case ActionTmuxCreate:
		return s.TmuxCreate(ctx, pending.Host, pending.Session)
	case ActionTmuxKill:
		return s.TmuxKill(ctx, pending.Host, pending.Session)
	case ActionDockerStart:
		return s.ContainerAction(ctx, pending.Host,
			NormalizeContainerRuntime(pending.Runtime), pending.ContainerID, DockerStart)
	case ActionDockerStop:
		return s.ContainerAction(ctx, pending.Host,
			NormalizeContainerRuntime(pending.Runtime), pending.ContainerID, DockerStop)
	case ActionDockerRemove:
		// The Kind is the confirmation's single source of truth. Deriving the
		// engine action from it means a malformed DockerAction field cannot
		// run something other than the command the user approved.
		return s.ContainerAction(ctx, pending.Host,
			NormalizeContainerRuntime(pending.Runtime), pending.ContainerID, DockerRemove)
	default:
		return ActionResult{Kind: pending.Kind, Command: pending.Command}, ErrUnknownAction
	}
}

// ErrUnknownAction reports a PendingAction whose Kind has no executor.
var ErrUnknownAction = errors.New("sysmanager: unknown action")

// DockerActionKind maps a container action to the ActionKind a result reports.
func DockerActionKind(action DockerAction) (ActionKind, bool) {
	switch action {
	case DockerStart:
		return ActionDockerStart, true
	case DockerStop:
		return ActionDockerStop, true
	case DockerRemove:
		return ActionDockerRemove, true
	default:
		return "", false
	}
}

// ContainerDisplayName prefers a container's name and falls back to its ID.
func ContainerDisplayName(container Container) string {
	if name := strings.TrimSpace(container.Name); name != "" {
		return name
	}
	return container.ID
}
