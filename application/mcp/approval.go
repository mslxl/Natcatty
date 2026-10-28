package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DefaultApprovalTimeout is the original bridge's absolute approval deadline.
// It never slides: an approval created at T expires at T+110 s regardless of
// when the user starts reviewing it.
const DefaultApprovalTimeout = 110 * time.Second

// ApprovalStatus is the lifecycle of one approval prompt.
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
	ApprovalExpired  ApprovalStatus = "expired"
)

// ApprovalRequest is the data an approval prompt shows. Summary is already
// redacted by SummarizeArguments.
type ApprovalRequest struct {
	Client    string
	Tool      string
	Target    string
	Summary   string
	Scope     string
	SessionID string
}

// ApprovalView is one prompt as the UI renders it. Remaining is clamped at
// zero; Expired is true at and after the absolute deadline.
type ApprovalView struct {
	ID        string         `json:"id"`
	Client    string         `json:"client"`
	Tool      string         `json:"tool"`
	Target    string         `json:"target"`
	Summary   string         `json:"summary"`
	Scope     string         `json:"scope"`
	SessionID string         `json:"sessionId,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	Deadline  time.Time      `json:"deadline"`
	Status    ApprovalStatus `json:"status"`
	Remaining time.Duration  `json:"remaining"`
	Expired   bool           `json:"expired"`
}

// Resolution is the outcome the blocked tool call receives.
type Resolution struct {
	Approved   bool
	ApprovalID string
	Code       ErrorCode
	Message    string
}

type pendingApproval struct {
	view ApprovalView
	wait chan Resolution
	stop func()
}

// Service is the external MCP policy. All methods are safe for concurrent
// use: tool calls arrive on connection goroutines, the UI reads snapshots and
// resolves prompts on the UI thread, and Session changes may arrive on any
// goroutine.
type Service struct {
	mu sync.Mutex

	forcedChatScope string
	allowlist       []string
	allowlistSet    bool
	sessions        LiveSessionSource
	mode            func() Mode
	now             func() time.Time
	timeout         time.Duration
	redraw          func()
	schedule        func(time.Duration, func()) func()

	nextApproval int
	pending      map[string]*pendingApproval
	recent       []ApprovalView
	// invalidated records approval ids cancelled by a generation change so a
	// replay reports a stale generation rather than an unknown id.
	invalidated      map[string]bool
	invalidatedOrder []string

	generation  uint64
	grants      map[string]grant
	staleGrants map[string]uint64

	// lastActivity is the newest write outcome shown by the Settings surface.
	lastActivity *ActivityView
}

// Config builds a policy service. The composition root supplies the clock and
// mode source, so tests can advance time and change the mode deterministically.
type Config struct {
	// ForcedChatSessionID is the synthetic scope every external call runs in.
	ForcedChatSessionID string
	// Allowlist is the per-session allowlist from NETCATTY_MCP_SESSION_IDS. An
	// empty list means no session access, never a wildcard.
	Allowlist []string
	// AllowlistSet records that NETCATTY_MCP_SESSION_IDS was present, so a
	// dynamic live-Session scope is intersected with it (an explicitly empty
	// value means no access) instead of ignoring it. It has no effect when
	// Sessions is nil.
	AllowlistSet bool
	// Sessions is the live-Session port. When set, every live Session is the
	// dynamic scope while SharingEnabled reports true, matching the original
	// useExternalMcpSessionSync. Nil keeps the static allowlist behavior used
	// by ticket-57 unit fixtures.
	Sessions LiveSessionSource
	// Mode is consulted on every write so a Settings change applies to the
	// next call. Nil defaults to confirm.
	Mode func() Mode
	// Now is the clock used for approval deadlines. Nil defaults to time.Now.
	Now func() time.Time
	// Timeout overrides the 110 s approval deadline; tests use short values,
	// production keeps the original absolute 110 s.
	Timeout time.Duration
	// Redraw asks the UI to render a changed prompt list. It may be nil.
	Redraw func()
	// Schedule arms the deadline timer. It returns a stop function. Nil uses
	// time.AfterFunc. Tests leave it nil and call Sweep explicitly.
	Schedule func(time.Duration, func()) func()
}

// NewService returns an empty policy at ownership generation 1.
func NewService(config Config) *Service {
	mode := config.Mode
	if mode == nil {
		mode = func() Mode { return DefaultMode }
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DefaultApprovalTimeout
	}
	schedule := config.Schedule
	if schedule == nil {
		schedule = func(delay time.Duration, fn func()) func() {
			timer := time.AfterFunc(delay, fn)
			return func() { timer.Stop() }
		}
	}
	return &Service{
		forcedChatScope: config.ForcedChatSessionID,
		allowlist:       append([]string(nil), config.Allowlist...),
		allowlistSet:    config.AllowlistSet,
		sessions:        config.Sessions,
		mode:            mode,
		now:             now,
		timeout:         timeout,
		redraw:          config.Redraw,
		schedule:        schedule,
		pending:         map[string]*pendingApproval{},
		invalidated:     map[string]bool{},
		generation:      1,
		grants:          map[string]grant{},
		staleGrants:     map[string]uint64{},
	}
}

// ForcedScope returns the synthetic chat scope.
func (s *Service) ForcedScope() string { return s.forcedChatScope }

// Allowlist returns a copy of the effective session scope.
func (s *Service) Allowlist() []string { return s.effectiveAllowlist() }

// effectiveAllowlist reads the dynamic live-Session scope or the static
// ticket-57 allowlist on every call, so a Session opening, closing, or the
// sharing toggle applies to the next tool call without a cached list.
func (s *Service) effectiveAllowlist() []string {
	effective, _, _, _ := s.scopeView()
	return effective
}

// CurrentMode reads the mode source now. It is used for status displays; the
// enforcement path re-reads it on every write.
func (s *Service) CurrentMode() Mode { return s.mode() }

// EffectiveSessions intersects a request with the forced scope. Anything
// outside the intersection is rejected here, before any action runs.
func (s *Service) EffectiveSessions(requested RequestedScope) ([]string, error) {
	return intersect(s.forcedChatScope, s.effectiveAllowlist(), requested)
}

// RequestApproval creates a prompt, arms the absolute deadline, and blocks the
// calling tool until the user resolves it or the deadline passes. It returns
// exactly one resolution; the approval is consumed either way.
func (s *Service) RequestApproval(ctx context.Context, request ApprovalRequest) Resolution {
	now := s.now()
	s.mu.Lock()
	s.nextApproval++
	id := fmt.Sprintf("mcp_approval_%d_%d", s.nextApproval, now.UnixMilli())
	view := ApprovalView{
		ID:        id,
		Client:    request.Client,
		Tool:      request.Tool,
		Target:    request.Target,
		Summary:   request.Summary,
		Scope:     request.Scope,
		SessionID: request.SessionID,
		CreatedAt: now,
		Deadline:  now.Add(s.timeout),
		Status:    ApprovalPending,
		Remaining: s.timeout,
	}
	pending := &pendingApproval{view: view, wait: make(chan Resolution, 1)}
	s.pending[id] = pending
	stop := s.schedule(s.timeout, s.Sweep)
	pending.stop = stop
	s.mu.Unlock()
	s.notify()

	select {
	case resolution := <-pending.wait:
		return resolution
	case <-ctx.Done():
		s.mu.Lock()
		if current, ok := s.pending[id]; ok && current == pending {
			delete(s.pending, id)
		}
		s.mu.Unlock()
		s.notify()
		return Resolution{
			Approved: false,
			Code:     CodeApprovalDenied,
			Message:  "Operation cancelled.",
		}
	}
}

// Approve consumes the pending approval. At or after the deadline it expires
// instead, even when a timer has not fired yet. A second approve, or an
// approve after expiry, is rejected as invalid/expired and never replays.
func (s *Service) Approve(id string) error {
	s.mu.Lock()
	pending, ok := s.pending[id]
	if !ok {
		if s.invalidated[id] {
			s.mu.Unlock()
			return &Error{Code: CodeStaleGeneration, Message: "The approval was invalidated by an ownership generation change."}
		}
		expired := s.recentHas(id, ApprovalExpired)
		s.mu.Unlock()
		if expired {
			return &Error{Code: CodeApprovalExpired, Message: "The approval expired and cannot be replayed."}
		}
		return &Error{Code: CodeApprovalInvalid, Message: "The approval is not pending or was already used."}
	}
	if now := s.now(); !now.Before(pending.view.Deadline) {
		s.expireLocked(pending)
		s.mu.Unlock()
		s.notify()
		return &Error{Code: CodeApprovalExpired, Message: "The approval expired and cannot be replayed."}
	}
	delete(s.pending, id)
	pending.view.Status = ApprovalApproved
	pending.stopTimer()
	pending.wait <- Resolution{Approved: true, ApprovalID: id}
	s.mu.Unlock()
	s.notify()
	return nil
}

// Deny consumes the pending approval and resolves its call as denied.
func (s *Service) Deny(id string) error {
	s.mu.Lock()
	pending, ok := s.pending[id]
	if !ok {
		if s.invalidated[id] {
			s.mu.Unlock()
			return &Error{Code: CodeStaleGeneration, Message: "The approval was invalidated by an ownership generation change."}
		}
		s.mu.Unlock()
		return &Error{Code: CodeApprovalInvalid, Message: "The approval is not pending or was already used."}
	}
	if now := s.now(); !now.Before(pending.view.Deadline) {
		s.expireLocked(pending)
		s.mu.Unlock()
		s.notify()
		return &Error{Code: CodeApprovalExpired, Message: "The approval expired and cannot be replayed."}
	}
	delete(s.pending, id)
	pending.view.Status = ApprovalDenied
	pending.stopTimer()
	pending.wait <- Resolution{
		Approved: false,
		Code:     CodeApprovalDenied,
		Message:  "Operation denied by user.",
	}
	s.mu.Unlock()
	s.notify()
	return nil
}

// Dismiss removes an expired prompt from the UI's recent list.
func (s *Service) Dismiss(id string) {
	s.mu.Lock()
	out := s.recent[:0]
	for _, view := range s.recent {
		if view.ID != id {
			out = append(out, view)
		}
	}
	s.recent = out
	s.mu.Unlock()
	s.notify()
}

// Sweep expires every pending approval whose absolute deadline has passed.
// The per-request timer calls it in production; tests call it after advancing
// the injected clock. Snapshot also reconciles the deadline, so the UI cannot
// show a live countdown or Approve/Deny for a prompt that is already expired.
func (s *Service) Sweep() {
	s.mu.Lock()
	changed := s.expireDueLocked(s.now())
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

// expireDueLocked expires every pending approval whose absolute deadline is at
// or before now. The caller holds mu.
func (s *Service) expireDueLocked(now time.Time) bool {
	changed := false
	for _, pending := range s.pending {
		if !now.Before(pending.view.Deadline) {
			s.expireLocked(pending)
			changed = true
		}
	}
	return changed
}

// expireLocked resolves one pending approval as expired and keeps it in the
// recent list so the UI can show the expired state. The caller holds mu.
func (s *Service) expireLocked(pending *pendingApproval) {
	delete(s.pending, pending.view.ID)
	pending.view.Status = ApprovalExpired
	pending.view.Remaining = 0
	pending.view.Expired = true
	pending.stopTimer()
	pending.wait <- Resolution{
		Approved: false,
		Code:     CodeApprovalExpired,
		Message:  "The approval expired before it was approved.",
	}
	s.recent = append(s.recent, pending.view)
	if len(s.recent) > 8 {
		s.recent = append([]ApprovalView(nil), s.recent[len(s.recent)-8:]...)
	}
}

// recentHas reports whether the recent list holds id with status.
func (s *Service) recentHas(id string, status ApprovalStatus) bool {
	for _, view := range s.recent {
		if view.ID == id && view.Status == status {
			return true
		}
	}
	return false
}

// snapshotPendingLocked copies pending prompts sorted by creation order.
func (s *Service) snapshotPendingLocked(now time.Time) []ApprovalView {
	views := make([]ApprovalView, 0, len(s.pending))
	for _, pending := range s.pending {
		view := pending.view
		view.Remaining = pending.view.Deadline.Sub(now)
		if view.Remaining < 0 {
			view.Remaining = 0
		}
		view.Expired = !now.Before(pending.view.Deadline)
		if view.Expired {
			view.Status = ApprovalExpired
		}
		views = append(views, view)
	}
	sortApprovalViews(views)
	return views
}

func (s *Service) notify() {
	if s.redraw != nil {
		s.redraw()
	}
}

func (p *pendingApproval) stopTimer() {
	if p.stop != nil {
		p.stop()
		p.stop = nil
	}
}
