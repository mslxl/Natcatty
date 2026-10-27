package mcp

import "fmt"

// grant records that the external scope opened (or, in this build, adopted
// through host_open) one Session in one ownership generation.
type grant struct {
	hostID     string
	generation uint64
}

// SessionRef is the minimal live-Session identity the ownership observer
// needs. SessionDescription satisfies it through the composition root.
type SessionRef struct {
	ID     string
	HostID string
}

// Generation returns the current ownership generation.
func (s *Service) Generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}

// GrantOwnedSession registers sessionID as owned by the scope in the current
// generation and returns that generation.
func (s *Service) GrantOwnedSession(sessionID, hostID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.staleGrants, sessionID)
	s.grants[sessionID] = grant{hostID: hostID, generation: s.generation}
	return s.generation
}

// ReleaseOwnedSession forgets a grant after its Session closed normally.
func (s *Service) ReleaseOwnedSession(sessionID string) {
	s.mu.Lock()
	delete(s.grants, sessionID)
	delete(s.staleGrants, sessionID)
	s.mu.Unlock()
}

// ValidateOwnership reports whether sessionID has a grant in the current
// generation. A prior generation's grant is explicitly stale; an unknown
// session was never owned by this scope.
func (s *Service) ValidateOwnership(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.grants[sessionID]; ok {
		if current.generation == s.generation {
			return nil
		}
		return &Error{
			Code: CodeStaleGeneration,
			Message: fmt.Sprintf(
				"Session %q belongs to ownership generation %d; the current generation is %d. Re-open the Host.",
				sessionID, current.generation, s.generation),
		}
	}
	if stale := s.staleGrants[sessionID]; stale != 0 {
		return &Error{
			Code: CodeStaleGeneration,
			Message: fmt.Sprintf(
				"Session %q belongs to ownership generation %d; the current generation is %d. Re-open the Host.",
				sessionID, stale, s.generation),
		}
	}
	return &Error{
		Code:    CodeSessionNotOwned,
		Message: fmt.Sprintf("Session %q was not opened by the current external MCP scope.", sessionID),
	}
}

// ObserveSessions reconciles ownership with the live Session list. When an
// owned Session is gone and another live Session exists for the same Host, the
// Host was re-opened or its Session replaced: the generation advances, every
// prior grant is invalidated, and every pending approval tied to the scope is
// denied as stale. Merely closing a Session drops its grant without advancing
// the generation.
func (s *Service) ObserveSessions(sessions []SessionRef) {
	live := make(map[string]SessionRef, len(sessions))
	byHost := map[string][]string{}
	for _, session := range sessions {
		if session.ID == "" {
			continue
		}
		live[session.ID] = session
		if session.HostID != "" {
			byHost[session.HostID] = append(byHost[session.HostID], session.ID)
		}
	}

	replaced := false
	s.mu.Lock()
	for id, current := range s.grants {
		if _, ok := live[id]; ok {
			continue
		}
		if current.hostID == "" {
			delete(s.grants, id)
			continue
		}
		replacement := ""
		for _, candidate := range byHost[current.hostID] {
			if candidate != id {
				replacement = candidate
				break
			}
		}
		if replacement == "" {
			delete(s.grants, id)
			continue
		}
		replaced = true
		break
	}
	changed := replaced
	if replaced {
		s.invalidateOwnershipLocked()
	}
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

// InvalidateOwnership advances the generation without a Session observation.
// The app uses it when a Host is explicitly re-opened outside the Session
// observer's detection, and tests use it to drive the stale path.
func (s *Service) InvalidateOwnership() uint64 {
	s.mu.Lock()
	s.invalidateOwnershipLocked()
	generation := s.generation
	s.mu.Unlock()
	s.notify()
	return generation
}

// invalidateOwnershipLocked advances the generation, clears every grant, and
// denies pending approvals as stale. The caller holds mu.
func (s *Service) invalidateOwnershipLocked() {
	for id, current := range s.grants {
		s.staleGrants[id] = current.generation
	}
	s.generation++
	s.grants = map[string]grant{}
	for id, pending := range s.pending {
		s.markInvalidatedLocked(id)
		delete(s.pending, id)
		pending.stopTimer()
		pending.wait <- Resolution{
			Approved: false,
			Code:     CodeStaleGeneration,
			Message:  "The ownership generation changed while the approval was pending. Re-open the Host and retry.",
		}
	}
}

// invalidatedLimit bounds the replay memory for cancelled approvals.
const invalidatedLimit = 32

// markInvalidatedLocked remembers a cancelled approval id, evicting the oldest
// entry beyond the bound. The caller holds mu.
func (s *Service) markInvalidatedLocked(id string) {
	if s.invalidated == nil {
		s.invalidated = map[string]bool{}
	}
	if !s.invalidated[id] {
		s.invalidatedOrder = append(s.invalidatedOrder, id)
	}
	s.invalidated[id] = true
	for len(s.invalidatedOrder) > invalidatedLimit {
		oldest := s.invalidatedOrder[0]
		s.invalidatedOrder = s.invalidatedOrder[1:]
		delete(s.invalidated, oldest)
	}
}
