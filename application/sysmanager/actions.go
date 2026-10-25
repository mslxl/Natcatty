package sysmanager

import (
	"context"
	"errors"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// actionTimeout bounds one destructive command. The surface cancels the
// action's context when it is closed; the deadline keeps a stalled transport
// from leaving the confirm state busy forever.
const actionTimeout = 30 * time.Second

// Signal validates the target, opens a trusted lease, and runs the signal
// command. It is called after the surface's confirmation is accepted.
func (s *Service) Signal(ctx context.Context, host domainvault.Host, pid int, signal string) (ActionResult, error) {
	command, err := SignalCommand(pid, signal)
	if err != nil {
		return ActionResult{Kind: ActionSignal, Command: command}, err
	}
	return s.execute(ctx, host, ActionSignal, command)
}

// ServiceAction validates the unit action, opens a trusted lease, and runs
// systemctl. A permission denial retries once with non-interactive sudo, as
// the original did; this build has no session sudo password prompt.
func (s *Service) ServiceAction(ctx context.Context, host domainvault.Host,
	unit string, action ServiceAction, scope Scope) (ActionResult, error) {
	command, err := ServiceCommand(unit, action, scope)
	if err != nil {
		return ActionResult{Kind: ActionServiceAction, Command: command}, err
	}
	result, err := s.execute(ctx, host, ActionServiceAction, command)
	if err == nil || scope == ScopeUser {
		return result, err
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Reason != ReasonPermission {
		return result, err
	}
	name, nameErr := sanitizeUnitName(unit)
	if nameErr != nil {
		return result, nameErr
	}
	sudoCommand := "sudo -n systemctl " + string(action) + " " + shellQuote(name)
	sudoResult, sudoErr := s.execute(ctx, host, ActionServiceAction, sudoCommand)
	if sudoErr == nil {
		return sudoResult, nil
	}
	// Keep the original failure when sudo is not available either; the
	// surface localizes the permission reason.
	return result, err
}

// execute opens one short-lived lease and runs command. Opening goes through
// the Connector, so the host-key trust decision precedes the command here
// exactly as it does for a sample.
func (s *Service) execute(ctx context.Context, host domainvault.Host,
	kind ActionKind, command string) (ActionResult, error) {
	result := ActionResult{Kind: kind, Command: command}
	if s.connector == nil {
		return result, ErrNoConnector
	}
	if string(host.ID) == "" {
		return result, ErrNoHost
	}
	actionCtx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()

	s.actions.Add(1)
	defer s.actions.Done()

	connection, err := s.connector.Open(actionCtx, host)
	if err != nil {
		return result, err
	}
	defer func() { _ = connection.Close() }()
	reply, err := connection.Exec(actionCtx, command)
	if err != nil {
		return result, err
	}
	result.Stdout = reply.Stdout
	result.Stderr = reply.Stderr
	result.ExitCode = reply.ExitCode
	result.HasExitCode = reply.HasExitCode
	if reply.HasExitCode && reply.ExitCode != 0 {
		return result, commandFailure(reply, "the command failed")
	}
	return result, nil
}

// Wait blocks until every sampler and action goroutine has returned. Tests
// and shutdown use it after Close.
func (s *Service) Wait() {
	s.runs.Wait()
	s.actions.Wait()
}
