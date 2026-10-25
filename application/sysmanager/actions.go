package sysmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// actionTimeout bounds one destructive command. The surface cancels the
// action's context when it is closed; the deadline keeps a stalled transport
// from leaving the confirm state busy forever.
const actionTimeout = 30 * time.Second

// maxDockerDocumentBytes bounds an inspect or log document the surface
// displays. The logs command already tails a bounded line count; this keeps a
// single very long line from growing the frame without limit.
const maxDockerDocumentBytes = 64 << 10

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

// TmuxCreate validates the name, opens a trusted lease, and creates a
// detached session. Creating is not destructive, so the surface runs it
// without a confirmation.
func (s *Service) TmuxCreate(ctx context.Context, host domainvault.Host, name string) (ActionResult, error) {
	command, err := TmuxCreateCommand(name)
	if err != nil {
		return ActionResult{Kind: ActionTmuxCreate, Command: command}, err
	}
	return s.execute(ctx, host, ActionTmuxCreate, command)
}

// TmuxKill validates the name, opens a trusted lease, and kills the session.
// It is called only after the surface's confirmation is accepted.
func (s *Service) TmuxKill(ctx context.Context, host domainvault.Host, name string) (ActionResult, error) {
	command, err := TmuxKillCommand(name)
	if err != nil {
		return ActionResult{Kind: ActionTmuxKill, Command: command}, err
	}
	return s.execute(ctx, host, ActionTmuxKill, command)
}

// DockerContainerAction validates the target, opens a trusted lease, and
// starts, stops, or force-removes the container. It is called only after the
// surface's confirmation is accepted.
func (s *Service) DockerContainerAction(ctx context.Context, host domainvault.Host,
	id string, action DockerAction) (ActionResult, error) {
	kind, ok := dockerActionKind(action)
	if !ok {
		return ActionResult{Command: ""}, &Error{Reason: ReasonInternal,
			Cause: errors.New("sysmanager: invalid container action")}
	}
	command, err := DockerActionCommand(id, action)
	if err != nil {
		return ActionResult{Kind: kind}, err
	}
	return s.execute(ctx, host, kind, command)
}

func dockerActionKind(action DockerAction) (ActionKind, bool) {
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

// DockerInspect returns the container's inspect JSON, indented and bounded.
func (s *Service) DockerInspect(ctx context.Context, host domainvault.Host,
	id string) (DockerDocument, error) {
	document := DockerDocument{Kind: DocumentInspect, ContainerID: id}
	command, err := DockerInspectCommand(id)
	if err != nil {
		return document, err
	}
	result, err := s.execute(ctx, host, ActionDockerInspect, command)
	if err != nil {
		return document, err
	}
	text := indentJSON(result.Stdout)
	document.Text, document.Truncated = boundDockerDocument(text)
	return document, nil
}

// DockerLogs returns a bounded, non-following log tail. docker writes
// container stdout and stderr on separate streams, so both are kept in the
// document in that order.
func (s *Service) DockerLogs(ctx context.Context, host domainvault.Host,
	id string, tail int) (DockerDocument, error) {
	document := DockerDocument{Kind: DocumentLogs, ContainerID: id}
	command, err := DockerLogsCommand(id, tail)
	if err != nil {
		return document, err
	}
	result, err := s.execute(ctx, host, ActionDockerLogs, command)
	if err != nil {
		return document, err
	}
	text := result.Stdout
	if result.Stderr != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += result.Stderr
	}
	document.Text, document.Truncated = boundDockerDocument(text)
	return document, nil
}

// indentJSON pretty-prints an inspect reply; an unparseable reply is shown as
// it arrived rather than replaced with an error.
func indentJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(trimmed), "", "  "); err != nil {
		return trimmed
	}
	return out.String()
}

// boundDockerDocument truncates to the document budget on a rune boundary.
func boundDockerDocument(text string) (string, bool) {
	if len(text) <= maxDockerDocumentBytes {
		return text, false
	}
	cut := maxDockerDocumentBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n…", true
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
