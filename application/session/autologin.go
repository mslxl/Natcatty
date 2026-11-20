package session

import (
	"github.com/mslxl/natcatty/application/session/autologin"
)

// armAutoLogin builds the console detector for one transport generation.
// Only Serial and Telnet Sessions carry a login prompt on their transport;
// every other kind authenticates before its shell exists. The detector is
// recreated per generation so a stale prompt cannot answer a replacement
// connection.
func (s *Session) armAutoLogin(generation int, transport Transport) {
	if s.spec.AutoLogin == nil || transport == nil {
		return
	}
	switch s.spec.Kind {
	case KindSerial, KindTelnet:
	default:
		return
	}
	options := *s.spec.AutoLogin
	options.Write = func(line []byte) {
		s.writeAutoLogin(generation, transport, line)
	}
	engine := autologin.New(options)
	if !engine.Active() {
		return
	}
	s.autoLoginMu.Lock()
	previous := s.autoLogin
	s.autoLogin = engine
	s.autoLoginGeneration = generation
	s.autoLoginMu.Unlock()
	if previous != nil {
		previous.Cancel()
	}
}

// feedAutoLogin passes decoded output of one generation to its detector. The
// engine is read under the lock and fed outside it: the detector calls back
// into writeAutoLogin, which needs the same lock.
func (s *Session) feedAutoLogin(generation int, text string) {
	s.autoLoginMu.Lock()
	engine := s.autoLogin
	matches := engine != nil && s.autoLoginGeneration == generation
	s.autoLoginMu.Unlock()
	if matches {
		engine.Feed(text)
	}
}

// userInputAutoLogin reports typed input to the detector, so ordinary typing
// or an Enter at a login prompt disarms it.
func (s *Session) userInputAutoLogin(data []byte) {
	s.autoLoginMu.Lock()
	engine := s.autoLogin
	s.autoLoginMu.Unlock()
	if engine != nil {
		engine.UserInput(data)
	}
}

// takeoverAutoLogin disarms the detector for input that did not come from this
// Session's own keyboard, such as a broadcast or an inserted snippet.
func (s *Session) takeoverAutoLogin() {
	s.autoLoginMu.Lock()
	engine := s.autoLogin
	s.autoLoginMu.Unlock()
	if engine != nil {
		engine.Takeover()
	}
}

// autoLoginAllowsStartup reports whether this generation may type its Host
// startup command. The command waits until the saved login succeeded, so it
// is never typed into a login or password prompt. A detector that stopped at
// its attempt limit or lost the prompt to a user takeover never completes, so
// that connection leaves the console alone at its prompt instead of running
// the command into it.
func (s *Session) autoLoginAllowsStartup(generation int) bool {
	s.autoLoginMu.Lock()
	defer s.autoLoginMu.Unlock()
	if s.autoLogin == nil || s.autoLoginGeneration != generation {
		return true
	}
	return s.autoLogin.Completed()
}

// cancelAutoLogin disarms the detector without writing anything, as when its
// Session is closed or replaced by a reconnect.
func (s *Session) cancelAutoLogin() {
	s.autoLoginMu.Lock()
	engine := s.autoLogin
	s.autoLogin = nil
	s.autoLoginMu.Unlock()
	if engine != nil {
		engine.Cancel()
	}
}

// writeAutoLogin sends one answer directly to the generation's transport.
// The closure binds its own generation and transport, so an answer observed
// on an old connection cannot land on its replacement. A write that was
// already in flight when the detector was cancelled still targets that old
// transport; it never reaches the new one. The bytes bypass the Session log
// and the tap's input history: credentials are never recorded.
func (s *Session) writeAutoLogin(generation int, transport Transport, line []byte) {
	s.autoLoginMu.Lock()
	current := s.autoLogin != nil && s.autoLoginGeneration == generation
	s.autoLoginMu.Unlock()
	if !current || len(line) == 0 {
		return
	}
	copied := append([]byte(nil), line...)
	s.writeMu.Lock()
	_, _ = transport.Write(copied)
	s.writeMu.Unlock()
}
