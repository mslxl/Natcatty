package session

// armStartupCommand marks the Host startup command pending for one transport
// generation. An empty command, or a Session without one, stays disarmed.
func (s *Session) armStartupCommand(generation int) {
	if s.spec.StartupCommand == "" {
		return
	}
	s.startupMu.Lock()
	s.startupPending = true
	s.startupGeneration = generation
	s.startupMu.Unlock()
}

// maybeRunStartupCommand queues the startup command after the first output of
// a connection. It clears the pending mark before writing, so later output
// bursts cannot replay the command within the same connection. A Session that
// ends before producing output never reaches this call, so it never runs the
// command. The write targets this generation's transport directly: a byte
// still in the shared queue must not leak into a replacement connection.
func (s *Session) maybeRunStartupCommand(generation int, transport Transport) {
	s.startupMu.Lock()
	if !s.startupPending || s.startupGeneration != generation {
		s.startupMu.Unlock()
		return
	}
	s.startupPending = false
	s.startupMu.Unlock()
	go s.sendStartupCommand(transport)
}

// sendStartupCommand types the Host startup command as ordinary input, so the
// shell's line editor observes it exactly as if the user typed it. The bytes
// pass the tap for command tracking but are not attributed to this Session's
// input hook, so a broadcast cannot fan them out again.
func (s *Session) sendStartupCommand(transport Transport) {
	if transport == nil {
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
