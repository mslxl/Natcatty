package mcp

import (
	"context"
	"fmt"
	"sort"
)

// Tool is the catalog metadata the policy decision needs. The catalog stays in
// internal/mcp/catalog (an adapter-facing package); the composition root maps
// it into this value so the application layer keeps its dependency direction.
type Tool struct {
	Name                  string
	Write                 bool
	BypassesApproval      bool
	BypassesObserverBlock bool
}

// Target identifies what a call acts on. Display is a redaction-safe label for
// the approval prompt (a session title, a host name, or the tool target).
type Target struct {
	SessionID string
	HostID    string
	Display   string
}

// Request is one policy evaluation for one tool call.
type Request struct {
	Tool      Tool
	Client    string
	Scope     RequestedScope
	Target    Target
	Arguments map[string]any
}

// Decision is the allow path's result. Sessions is the effective intersection
// the caller must enforce for the rest of the call.
type Decision struct {
	Mode       Mode
	Sessions   []string
	Approved   bool
	ApprovalID string
}

// Authorize applies scope, then the current permission mode, then approval.
// Scope is checked first so a call outside the intersection is rejected before
// any prompt is shown or any action runs. Reads follow the same scope rules but
// never prompt. The mode is re-read for every call, so a Settings change takes
// effect on the next call through any tool path.
func (s *Service) Authorize(ctx context.Context, request Request) (Decision, error) {
	effective, err := s.EffectiveSessions(request.Scope)
	if err != nil {
		return Decision{}, withTool(err, request.Tool.Name)
	}
	if request.Target.SessionID != "" && !SessionAllowed(effective, request.Target.SessionID) {
		return Decision{}, &Error{
			Code:    CodeSessionOutOfScope,
			Tool:    request.Tool.Name,
			Message: fmt.Sprintf("Session %q is outside the external MCP scope.", request.Target.SessionID),
		}
	}
	mode := s.CurrentMode()
	decision := Decision{Mode: mode, Sessions: effective}
	if !request.Tool.Write {
		return decision, nil
	}
	if mode == ModeObserver && !request.Tool.BypassesObserverBlock {
		return Decision{}, &Error{
			Code: CodePermissionDenied,
			Tool: request.Tool.Name,
			Message: fmt.Sprintf(
				"Operation denied: permission mode is %q (read-only); tool %q is a write.",
				mode, request.Tool.Name),
		}
	}
	if mode == ModeConfirm && !request.Tool.BypassesApproval {
		resolution := s.RequestApproval(ctx, ApprovalRequest{
			Client:    request.Client,
			Tool:      request.Tool.Name,
			Target:    request.Target.Display,
			Summary:   SummarizeArguments(request.Arguments),
			Scope:     s.forcedChatScope,
			SessionID: request.Target.SessionID,
		})
		if !resolution.Approved {
			code := resolution.Code
			if code == "" {
				code = CodeApprovalDenied
			}
			message := resolution.Message
			if message == "" {
				message = "Operation denied."
			}
			return Decision{}, &Error{Code: code, Tool: request.Tool.Name, Message: message}
		}
		decision.Approved = true
		decision.ApprovalID = resolution.ApprovalID
	}
	return decision, nil
}

// Snapshot is the UI's read model: current mode, forced scope, effective
// session scope, the shared live Sessions, generation, live prompts, and
// recently expired prompts.
type Snapshot struct {
	Mode        Mode
	ForcedScope string
	// Allowlist is the effective session scope: the live Sessions while
	// sharing is on, intersected with the static env allowlist when present.
	Allowlist []string
	// EnvAllowlist is the static NETCATTY_MCP_SESSION_IDS value and
	// EnvAllowlistSet records that the variable was present.
	EnvAllowlist    []string
	EnvAllowlistSet bool
	// LiveScope reports that the app is wired to the live Session provider, so
	// the Settings surface can show the sharing control and the exposed list.
	LiveScope       bool
	SharingEnabled  bool
	ExposedSessions []ExposedSession
	Generation      uint64
	Pending         []ApprovalView
	Recent          []ApprovalView
	// LastActivity is the newest write outcome, or nil when no write ran.
	LastActivity *ActivityView
}

// Snapshot returns a consistent read model for the Settings surface. It first
// reconciles the absolute approval deadline under the same lock that builds the
// snapshot, so an expired prompt is never reported as pending even if its timer
// has not run yet.
func (s *Service) Snapshot() Snapshot {
	now := s.now()
	effective, exposed, live, sharing := s.scopeView()
	s.mu.Lock()
	expired := s.expireDueLocked(now)
	snapshot := Snapshot{
		ForcedScope:     s.forcedChatScope,
		Allowlist:       effective,
		EnvAllowlist:    append([]string(nil), s.allowlist...),
		EnvAllowlistSet: s.allowlistSet,
		LiveScope:       live,
		SharingEnabled:  sharing,
		ExposedSessions: exposed,
		Generation:      s.generation,
		Pending:         s.snapshotPendingLocked(now),
		Recent:          append([]ApprovalView(nil), s.recent...),
	}
	if s.lastActivity != nil {
		activity := *s.lastActivity
		snapshot.LastActivity = &activity
	}
	s.mu.Unlock()
	if expired {
		s.notify()
	}
	snapshot.Mode = s.CurrentMode()
	sort.SliceStable(snapshot.Recent, func(left, right int) bool {
		return approvalNewer(snapshot.Recent[left], snapshot.Recent[right])
	})
	return snapshot
}

// NotifyViewChanged asks the UI to redraw after the live Session scope or the
// sharing toggle changed. The policy itself re-reads the source on every call,
// so this only repaints the Settings read model.
func (s *Service) NotifyViewChanged() {
	if s == nil {
		return
	}
	s.notify()
}

// withTool attaches the catalog tool name to a policy error that was produced
// before the tool was known (scope parsing and intersection run first).
func withTool(err error, tool string) error {
	typed, ok := err.(*Error)
	if !ok || typed.Tool != "" {
		return err
	}
	copied := *typed
	copied.Tool = tool
	return &copied
}

// sortApprovalViews orders prompts oldest first, which is the order they
// arrived and the order the user should resolve them.
func sortApprovalViews(views []ApprovalView) {
	sort.SliceStable(views, func(left, right int) bool {
		if views[left].CreatedAt.Equal(views[right].CreatedAt) {
			return views[left].ID < views[right].ID
		}
		return views[left].CreatedAt.Before(views[right].CreatedAt)
	})
}

// approvalNewer reports whether left is newer than right, for the recent list.
func approvalNewer(left, right ApprovalView) bool {
	if left.CreatedAt.Equal(right.CreatedAt) {
		return left.ID > right.ID
	}
	return left.CreatedAt.After(right.CreatedAt)
}
