package settings

import (
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// Terminal is the terminal settings seam the Settings window drives. The
// application terminalsettings.Service implements it; tests provide a fake.
type Terminal interface {
	Settings() domainterminal.Settings
	SetLoggingEnabled(bool) error
	SetLogDirectory(string) error
	SetLogFormat(domainterminal.LogFormat) error
	SetLogTimestamps(bool) error
	SetOSC52(domainterminal.ClipboardAccess) error
	SetOSCNotifications(domainterminal.NotificationMode) error
	SetEncoding(domainterminal.Encoding) error
	SetPasteProtection(bool) error
	SetPasteMinLines(int) error
	SetDropAction(domainterminal.DropAction) error
}

// TerminalState owns the Settings window's terminal form. It is mutated only
// on the UI thread.
type TerminalState struct {
	terminal  Terminal
	directory string
	problem   bool
	redraw    func()
}

// TerminalOption customizes a TerminalState.
type TerminalOption func(*TerminalState)

// WithTerminalRedrawRequest sets the function called after the form changes.
func WithTerminalRedrawRequest(fn func()) TerminalOption {
	return func(state *TerminalState) { state.redraw = fn }
}

// NewTerminalState returns the form for service.
func NewTerminalState(service Terminal, opts ...TerminalOption) *TerminalState {
	state := &TerminalState{terminal: service, directory: service.Settings().LogDirectory}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Settings returns the persisted terminal settings.
func (s *TerminalState) Settings() domainterminal.Settings { return s.terminal.Settings() }

// Directory returns the editable log directory draft.
func (s *TerminalState) Directory() *string { return &s.directory }

// Problem reports whether the last save failed.
func (s *TerminalState) Problem() bool { return s.problem }

// SetLoggingEnabled stores automatic logging.
func (s *TerminalState) SetLoggingEnabled(enabled bool) bool {
	return s.apply(func() error { return s.terminal.SetLoggingEnabled(enabled) })
}

// SetLogDirectory stores the log root.
func (s *TerminalState) SetLogDirectory(directory string) bool {
	return s.apply(func() error { return s.terminal.SetLogDirectory(directory) })
}

// SetLogFormat stores the log format.
func (s *TerminalState) SetLogFormat(format domainterminal.LogFormat) bool {
	return s.apply(func() error { return s.terminal.SetLogFormat(format) })
}

// SetLogTimestamps stores the timestamp policy.
func (s *TerminalState) SetLogTimestamps(enabled bool) bool {
	return s.apply(func() error { return s.terminal.SetLogTimestamps(enabled) })
}

// SetOSC52 stores the clipboard policy.
func (s *TerminalState) SetOSC52(access domainterminal.ClipboardAccess) bool {
	return s.apply(func() error { return s.terminal.SetOSC52(access) })
}

// SetOSCNotifications stores when program notifications appear.
func (s *TerminalState) SetOSCNotifications(mode domainterminal.NotificationMode) bool {
	return s.apply(func() error { return s.terminal.SetOSCNotifications(mode) })
}

// SetEncoding stores the default terminal encoding.
func (s *TerminalState) SetEncoding(encoding domainterminal.Encoding) bool {
	return s.apply(func() error { return s.terminal.SetEncoding(encoding) })
}

// SetPasteProtection stores the multiline paste guard.
func (s *TerminalState) SetPasteProtection(enabled bool) bool {
	return s.apply(func() error { return s.terminal.SetPasteProtection(enabled) })
}

// SetPasteMinLines stores the line count at which the guard asks.
func (s *TerminalState) SetPasteMinLines(lines int) bool {
	return s.apply(func() error { return s.terminal.SetPasteMinLines(lines) })
}

// SetDropAction stores what dropping files on a terminal does.
func (s *TerminalState) SetDropAction(action domainterminal.DropAction) bool {
	return s.apply(func() error { return s.terminal.SetDropAction(action) })
}

func (s *TerminalState) apply(change func() error) bool {
	if err := change(); err != nil {
		s.problem = true
		s.requestRedraw()
		return false
	}
	s.problem = false
	s.requestRedraw()
	return true
}

func (s *TerminalState) requestRedraw() {
	if s.redraw != nil {
		s.redraw()
	}
}
