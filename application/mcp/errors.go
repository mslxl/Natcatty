package mcp

import "fmt"

// ErrorCode is a stable protocol code an external client can branch on. The
// code is English and language-independent, per ADR 0009's MCP rule.
type ErrorCode string

const (
	// CodeScopeMismatch reports a requested chat scope that cannot intersect
	// the server's forced scope.
	CodeScopeMismatch ErrorCode = "mcp_scope_mismatch"
	// CodeSessionOutOfScope reports a session outside the effective scope.
	CodeSessionOutOfScope ErrorCode = "mcp_session_out_of_scope"
	// CodePermissionDenied reports writer-blocking observer mode.
	CodePermissionDenied ErrorCode = "mcp_permission_denied"
	// CodeApprovalDenied reports an explicit denial.
	CodeApprovalDenied ErrorCode = "mcp_approval_denied"
	// CodeApprovalExpired reports an approval whose absolute deadline passed.
	CodeApprovalExpired ErrorCode = "mcp_approval_expired"
	// CodeApprovalInvalid reports an approval id that is not pending, usually
	// a replayed or already-consumed approval.
	CodeApprovalInvalid ErrorCode = "mcp_approval_invalid"
	// CodeStaleGeneration reports a call whose ownership generation was
	// superseded by a Host re-open or session replacement.
	CodeStaleGeneration ErrorCode = "mcp_stale_generation"
	// CodeSessionNotOwned reports a close of a session this scope did not open
	// in the current generation.
	CodeSessionNotOwned ErrorCode = "mcp_session_not_owned"
	// CodeCommandBlocked reports a command rejected by the default blocklist.
	CodeCommandBlocked ErrorCode = "mcp_command_blocked"
	// CodeCommandTimeout reports a command or transfer that exceeded its
	// per-call deadline.
	CodeCommandTimeout ErrorCode = "mcp_command_timeout"
	// CodeToolUnavailable reports a catalog write this build cannot execute.
	CodeToolUnavailable ErrorCode = "mcp_tool_unavailable"
	// CodeInvalidArgument reports a missing or malformed tool argument.
	CodeInvalidArgument ErrorCode = "mcp_invalid_argument"
	// CodePluginToolDenied reports the plugin-side gate: the plugin is
	// installed and active but its mcp.tools permission is not granted.
	CodePluginToolDenied ErrorCode = "mcp_plugin_tool_denied"
	// CodePluginToolUnavailable reports a plugin tool that is not installed,
	// is disabled, or is no longer declared.
	CodePluginToolUnavailable ErrorCode = "mcp_plugin_tool_unavailable"
	// CodePluginToolReloading reports a call during a plugin reload or while
	// the fiber is leaving ACTIVE. It is retryable.
	CodePluginToolReloading ErrorCode = "mcp_plugin_reloading"
	// CodePluginToolBusy reports a full per-instance call queue. It is
	// retryable.
	CodePluginToolBusy ErrorCode = "mcp_plugin_tool_busy"
	// CodePluginToolTimeout reports a call that exceeded its declared timeout
	// class. It is retryable.
	CodePluginToolTimeout ErrorCode = "mcp_plugin_tool_timeout"
	// CodePluginToolCancelled reports a call cancelled because the client
	// disconnected or the request was cancelled. It is retryable.
	CodePluginToolCancelled ErrorCode = "mcp_plugin_tool_cancelled"
	// CodePluginToolFailed reports a guest trap, crash, or guest-reported
	// tool error.
	CodePluginToolFailed ErrorCode = "mcp_plugin_tool_failed"
	// CodePluginToolResultLimit reports a result above the 16 MiB bound.
	CodePluginToolResultLimit ErrorCode = "mcp_plugin_tool_result_limit"
	// CodePluginToolInvalidResult reports a malformed result envelope.
	CodePluginToolInvalidResult ErrorCode = "mcp_plugin_tool_invalid_result"
)

// Error is a typed policy denial. Tool names the catalog tool whose call was
// rejected, so a client and a test can identify the affected action. Rule is
// set for command-blocklist rejections.
type Error struct {
	Code    ErrorCode
	Tool    string
	Rule    string
	Message string
	// Retryable tells an automated client that the same call may succeed
	// later without any operator action. It is set for reloading, busy,
	// timeout, and cancellation failures, never for a policy denial.
	Retryable bool
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Tool != "" {
		return fmt.Sprintf("%s (tool %s)", e.Message, e.Tool)
	}
	return e.Message
}

// Result renders a typed denial in the original bridge's application-result
// envelope: the call is a normal tool result with ok:false, plus the stable
// code. The stdio layer turns ok:false into an MCP tool error.
func Result(err error) map[string]any {
	typed, ok := err.(*Error)
	if !ok {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	result := map[string]any{
		"ok":    false,
		"code":  string(typed.Code),
		"error": typed.Message,
	}
	if typed.Tool != "" {
		result["tool"] = typed.Tool
	}
	if typed.Rule != "" {
		result["rule"] = typed.Rule
	}
	if typed.Retryable {
		result["retryable"] = true
	}
	return result
}
