package mcp

import (
	"fmt"
	"strings"
)

// RequestedScope is the scope a client asks a call to run under. The original
// clients could send a chat scope and a narrower session list; the external
// bridge ignores nothing and instead intersects the request with its forced
// scope, rejecting anything outside the intersection before any action runs.
type RequestedScope struct {
	// ChatSessionID is the requested dynamic chat scope; empty means "use the
	// forced scope".
	ChatSessionID string
	// SessionIDs is the requested static session list. SessionIDsPresent
	// distinguishes an explicit empty list (no access) from an omitted list
	// (inherit the forced allowlist).
	SessionIDs        []string
	SessionIDsPresent bool
}

// RequestedScopeFrom reads the optional scope parameters from one call's
// arguments. A malformed value is a scope error, not a silently ignored one.
func RequestedScopeFrom(params map[string]any) (RequestedScope, error) {
	var requested RequestedScope
	if raw, present := params["chatSessionId"]; present && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return requested, &Error{
				Code:    CodeScopeMismatch,
				Message: "chatSessionId must be a string.",
			}
		}
		requested.ChatSessionID = strings.TrimSpace(value)
	}
	raw, present := params["scopedSessionIds"]
	if !present || raw == nil {
		return requested, nil
	}
	requested.SessionIDsPresent = true
	switch values := raw.(type) {
	case []string:
		requested.SessionIDs = append(requested.SessionIDs, values...)
	case []any:
		for _, value := range values {
			id, ok := value.(string)
			if !ok {
				return requested, &Error{
					Code:    CodeSessionOutOfScope,
					Message: "scopedSessionIds must contain session id strings.",
				}
			}
			requested.SessionIDs = append(requested.SessionIDs, id)
		}
	default:
		return requested, &Error{
			Code:    CodeSessionOutOfScope,
			Message: "scopedSessionIds must be an array of session ids.",
		}
	}
	return requested, nil
}

// ParseSessionAllowlist reads the comma-separated NETCATTY_MCP_SESSION_IDS
// value. An empty value is intentional no access, never a wildcard.
func ParseSessionAllowlist(value string) []string {
	seen := map[string]bool{}
	ids := make([]string, 0)
	for _, part := range strings.Split(value, ",") {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// SessionAllowed reports whether id is present in sessions.
func SessionAllowed(sessions []string, id string) bool {
	for _, candidate := range sessions {
		if candidate == id {
			return true
		}
	}
	return false
}

// intersect returns the effective session scope. A requested chat scope that
// is not the forced scope has an empty intersection and is rejected. A
// requested session list may narrow the forced allowlist but may not name a
// session outside it.
func intersect(forcedChatScope string, allowlist []string, requested RequestedScope) ([]string, error) {
	if requested.ChatSessionID != "" && requested.ChatSessionID != forcedChatScope {
		return nil, &Error{
			Code: CodeScopeMismatch,
			Message: fmt.Sprintf(
				"The requested scope %q is not within the forced scope %q.",
				requested.ChatSessionID, forcedChatScope),
		}
	}
	if !requested.SessionIDsPresent {
		return append([]string(nil), allowlist...), nil
	}
	effective := make([]string, 0, len(requested.SessionIDs))
	seen := map[string]bool{}
	for _, id := range requested.SessionIDs {
		if !SessionAllowed(allowlist, id) {
			return nil, &Error{
				Code:    CodeSessionOutOfScope,
				Message: fmt.Sprintf("Session %q is outside the external MCP scope.", id),
			}
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		effective = append(effective, id)
	}
	return effective, nil
}
