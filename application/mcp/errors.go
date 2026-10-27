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
)

// Error is a typed policy denial. Tool names the catalog tool whose call was
// rejected, so a client and a test can identify the affected action. Rule is
// set for command-blocklist rejections.
type Error struct {
	Code    ErrorCode
	Tool    string
	Rule    string
	Message string
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
	return result
}
