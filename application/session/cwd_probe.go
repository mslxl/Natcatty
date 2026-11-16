package session

// bindCWDProbe attaches the working-directory capability of a new transport
// and drops any value cached for the previous one. A transport without the
// capability keeps the probe nil, which is how a remote Session stays
// unknown.
func (s *Session) bindCWDProbe(transport Transport) {
	probe, _ := transport.(CWDProbe)
	s.cwdProbeMu.Lock()
	s.cwdProbe = probe
	s.cwdProbeValue = ""
	s.cwdProbeValid = false
	s.cwdProbeStale = false
	s.cwdProbeMu.Unlock()
}

// probedCWD returns the local shell's working directory. The first consumer
// after a prompt or a finished command pays for one process lookup; every
// other ask reuses the cached value. An unavailable probe or a failed lookup
// caches an empty value so a frame loop cannot repeat the work.
func (s *Session) probedCWD() string {
	s.cwdProbeMu.Lock()
	defer s.cwdProbeMu.Unlock()
	return s.probedCWDLocked()
}

func (s *Session) probedCWDLocked() string {
	if s.cwdProbe == nil {
		return ""
	}
	if s.cwdProbeValid && !s.cwdProbeStale {
		return s.cwdProbeValue
	}
	cwd, err := s.cwdProbe.WorkingDirectory()
	if err != nil {
		cwd = ""
	}
	s.cwdProbeValue = cwd
	s.cwdProbeValid = true
	s.cwdProbeStale = false
	return cwd
}

// invalidateCWDProbe marks the cached value stale. The next consumer probes
// the shell process again; nothing runs on a timer.
func (s *Session) invalidateCWDProbe() {
	s.cwdProbeMu.Lock()
	s.cwdProbeStale = true
	s.cwdProbeMu.Unlock()
}

// fillProbedCWD adds the probe value to a snapshot the change hook publishes,
// matching what Snapshot reports to the UI.
func (s *Session) fillProbedCWD(snapshot *Snapshot) {
	if snapshot.CWD != "" || snapshot.Kind != KindLocal {
		return
	}
	snapshot.CWD = s.probedCWD()
}
