package session

import (
	"errors"
)

// Logging returns the current Session log state.
func (s *Session) Logging() LogStatus { return s.logStatus() }

// StartLogging starts a log for this Session, stopping a previous one first.
// It is available while the Session runs and is safe from the UI thread.
func (s *Session) StartLogging(options LogOptions) error {
	if options.Directory == "" {
		return errNoLogDirectory
	}
	if options.Label == "" {
		options.Label = s.spec.Label
	}
	select {
	case <-s.done:
		return ErrClosed
	default:
	}

	s.logMu.Lock()
	if s.logger != nil {
		s.lastLog = s.logger.stop()
		s.logger = nil
	}
	logger, err := newSessionLogger(options)
	if err != nil {
		s.lastLog = LogStatus{Format: normalizeLogFormat(options.Format), Problem: err.Error()}
		s.logMu.Unlock()
		s.publishLogStatus()
		return err
	}
	s.logger = logger
	s.logMu.Unlock()

	select {
	case <-s.done:
		s.finishLogging()
		s.publishLogStatus()
		return ErrClosed
	default:
	}
	s.publishLogStatus()
	return nil
}

// StopLogging flushes and closes the current log. It reports the final status;
// a Session with no log reports an inactive one.
func (s *Session) StopLogging() LogStatus {
	status := s.finishLogging()
	s.publishLogStatus()
	return status
}

// startAutoLog installs the Settings-driven log before the read loop starts,
// so the first output is captured.
func (s *Session) startAutoLog(options LogOptions) {
	if options.Directory == "" {
		return
	}
	if options.Label == "" {
		options.Label = s.spec.Label
	}
	logger, err := newSessionLogger(options)
	if err != nil {
		s.lastLog = LogStatus{Format: normalizeLogFormat(options.Format), Problem: err.Error()}
		s.mu.Lock()
		s.snapshot.Logging = s.lastLog
		s.mu.Unlock()
		return
	}
	s.logMu.Lock()
	s.logger = logger
	s.lastLog = logger.status()
	status := s.lastLog
	s.logMu.Unlock()
	s.mu.Lock()
	s.snapshot.Logging = status
	s.mu.Unlock()
}

func (s *Session) logStatus() LogStatus {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.logger != nil {
		return s.logger.status()
	}
	return s.lastLog
}

// finishLogging stops the logger without publishing. The caller decides when
// to fold the status into a Snapshot.
func (s *Session) finishLogging() LogStatus {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.logger != nil {
		s.lastLog = s.logger.stop()
		s.logger = nil
	}
	return s.lastLog
}

func (s *Session) writeLog(data []byte) {
	s.logMu.Lock()
	logger := s.logger
	s.logMu.Unlock()
	if logger != nil {
		logger.write(data)
	}
}

func (s *Session) publishLogStatus() {
	status := s.logStatus()
	s.update(func(snapshot *Snapshot) {
		snapshot.Logging = status
	})
}

// errNoLogDirectory refuses a log with no destination rather than silently
// dropping output.
var errNoLogDirectory = errors.New("session log: no directory")
