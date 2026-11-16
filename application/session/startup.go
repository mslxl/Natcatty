package session

import "time"

// armStartupCommand marks the Host startup command pending for one transport
// generation. An empty command, or a Session without one, stays disarmed. The
// bounded fallback timer starts here: if the device never prints a prompt the
// heuristic recognizes, the command is typed after the timeout instead of
// being stranded.
func (s *Session) armStartupCommand(generation int, transport Transport) {
	if s.spec.StartupCommand == "" {
		return
	}
	s.startupMu.Lock()
	s.startupPending = true
	s.startupGeneration = generation
	s.startupTransport = transport
	s.startupTail = s.startupTail[:0]
	if s.startupTimer != nil {
		s.startupTimer.Stop()
	}
	s.startupTimer = time.AfterFunc(s.startupTimeout(), func() {
		s.startupReadinessElapsed(generation)
	})
	s.startupMu.Unlock()
}

// startupTimeout returns the configured readiness fallback, or the default.
func (s *Session) startupTimeout() time.Duration {
	if s.startupReadinessTimeout > 0 {
		return s.startupReadinessTimeout
	}
	return defaultStartupReadinessTimeout
}

// observeStartupOutput retains the decoded output of this generation so the
// prompt heuristic can see a prompt split across transport reads. It never
// writes: readiness is decided by maybeRunStartupCommand or the fallback.
func (s *Session) observeStartupOutput(generation int, decoded []byte) {
	s.startupMu.Lock()
	defer s.startupMu.Unlock()
	if !s.startupPending || s.startupGeneration != generation {
		return
	}
	s.startupTail = append(s.startupTail, decoded...)
	if excess := len(s.startupTail) - startupTailLimit; excess > 0 {
		s.startupTail = append([]byte(nil), s.startupTail[excess:]...)
	}
	if s.startupTimer == nil {
		// A fallback that fired while auto-login was still answering prompts
		// left the command pending; the next output restarts the bound.
		s.startupTimer = time.AfterFunc(s.startupTimeout(), func() {
			s.startupReadinessElapsed(generation)
		})
	}
}

// maybeRunStartupCommand types the startup command once the retained output
// ends on a shell prompt. It clears the pending mark before writing, so later
// output bursts cannot replay the command within the same connection. A
// Session that ends before readiness never reaches this call, or the timer is
// stopped by teardown, so the command does not run into a dead transport.
func (s *Session) maybeRunStartupCommand(generation int, transport Transport) {
	// Auto-login owns the console until it reaches the shell prompt. Typing the
	// startup command at a login or password prompt would feed it to the login.
	if !s.autoLoginAllowsStartup(generation) {
		return
	}
	s.startupMu.Lock()
	if !s.startupPending || s.startupGeneration != generation ||
		!startupPromptReady(string(s.startupTail)) {
		s.startupMu.Unlock()
		return
	}
	s.disarmStartupLocked()
	s.startupMu.Unlock()
	go s.sendStartupCommand(transport)
}

// startupReadinessElapsed is the bounded fallback. It never fires into an
// unfinished auto-login; a later output rearms the bound if the detector is
// still running.
func (s *Session) startupReadinessElapsed(generation int) {
	if !s.autoLoginAllowsStartup(generation) {
		s.startupMu.Lock()
		if s.startupPending && s.startupGeneration == generation {
			s.startupTimer = nil
		}
		s.startupMu.Unlock()
		return
	}
	s.startupMu.Lock()
	if !s.startupPending || s.startupGeneration != generation {
		s.startupMu.Unlock()
		return
	}
	transport := s.startupTransport
	s.disarmStartupLocked()
	s.startupMu.Unlock()
	go s.sendStartupCommand(transport)
}

// disarmStartupLocked clears the one-shot state. Callers hold startupMu.
func (s *Session) disarmStartupLocked() {
	s.startupPending = false
	s.startupTransport = nil
	s.startupTail = nil
	if s.startupTimer != nil {
		s.startupTimer.Stop()
		s.startupTimer = nil
	}
}

// stopStartupTimer cancels a pending readiness wait at teardown, so a timer
// that outlives the transport never types into a replacement connection or a
// closed one.
func (s *Session) stopStartupTimer() {
	s.startupMu.Lock()
	s.disarmStartupLocked()
	s.startupMu.Unlock()
}

// sendStartupCommand types the Host startup command as ordinary input, so the
// shell's line editor observes it exactly as if the user typed it. The bytes
// pass the tap for command tracking but are not attributed to this Session's
// input hook, so a broadcast cannot fan them out again.
func (s *Session) sendStartupCommand(transport Transport) {
	if transport == nil || s.ctx.Err() != nil {
		// The Session ended before readiness. Never type into a transport that
		// teardown is closing.
		return
	}
	forwarded, _, gated := s.handleInput([]byte(s.spec.StartupCommand+"\r"), false)
	if gated || len(forwarded) == 0 {
		return
	}
	copied := append([]byte(nil), forwarded...)
	s.writeMu.Lock()
	_, err := transport.Write(copied)
	s.writeMu.Unlock()
	if err == nil {
		s.tap.addInputBytes(len(copied))
	}
}
