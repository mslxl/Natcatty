package mcp

import (
	"fmt"
	"time"
)

// Execution bounds shared by the external MCP write surface. They mirror the
// original Electron bridge constants; tests assert both sides of each
// boundary.
const (
	// DefaultToolTimeout bounds one foreground tool call.
	DefaultToolTimeout = 60 * time.Second
	// JobStartupTimeout is how long terminal_start waits for the command's
	// start marker before it reports a typed timeout.
	JobStartupTimeout = 30 * time.Second
	// JobOverallTimeout bounds one background job.
	JobOverallTimeout = 60 * time.Minute
	// JobPollInterval is the recommended poll cadence.
	JobPollInterval = 30 * time.Second
	// JobRetention keeps a finished job's result available for polling.
	JobRetention = 10 * time.Minute
	// JobOutputLimit is the rolling background-output window.
	JobOutputLimit = 256 << 10
	// ForegroundOutputLimit is the foreground command capture window.
	ForegroundOutputLimit = 1 << 20
	// RawOutputLimit is the network-device and serial capture window.
	RawOutputLimit = 512 << 10
	// SFTPReadLimit is the largest file sftp_read_file will return.
	SFTPReadLimit = 10 << 20
	// SFTPListLimit bounds one directory listing.
	SFTPListLimit = 5000
	// MaxToolResultBytes is the stdio layer's tool-result bound.
	MaxToolResultBytes = 16 << 20
)

// CommandTimeoutError builds the typed per-call deadline failure.
func CommandTimeoutError(tool string, timeout time.Duration) error {
	return &Error{
		Code:    CodeCommandTimeout,
		Tool:    tool,
		Message: fmt.Sprintf("Command timed out after %s.", timeout),
	}
}

// ToolUnavailableError builds the explicit typed result for a catalog write
// this build cannot execute.
func ToolUnavailableError(tool, reason string) error {
	return &Error{
		Code:    CodeToolUnavailable,
		Tool:    tool,
		Message: reason,
	}
}

// InvalidArgumentError builds a typed missing/malformed argument failure.
func InvalidArgumentError(tool, message string) error {
	return &Error{Code: CodeInvalidArgument, Tool: tool, Message: message}
}

// ActivityStatus is the stable status shown in the Settings activity strip.
type ActivityStatus string

const (
	ActivityOK          ActivityStatus = "ok"
	ActivityBlocked     ActivityStatus = "blocked"
	ActivityTimeout     ActivityStatus = "timeout"
	ActivityTruncated   ActivityStatus = "truncated"
	ActivityUnavailable ActivityStatus = "unavailable"
	ActivityDenied      ActivityStatus = "denied"
	ActivityFailed      ActivityStatus = "failed"
)

// ActivityView is the last write outcome. Detail is already free of secrets;
// it never carries argument values for credential-named fields.
type ActivityView struct {
	At     time.Time
	Tool   string
	Status ActivityStatus
	Rule   string
	Detail string
}

// RecordActivity stores the last write outcome for the Settings surface.
func (s *Service) RecordActivity(view ActivityView) {
	if s == nil {
		return
	}
	if view.At.IsZero() {
		view.At = s.now()
	}
	s.mu.Lock()
	s.lastActivity = &view
	s.mu.Unlock()
	s.notify()
}
