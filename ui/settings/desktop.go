package settings

import (
	"github.com/mslxl/natcatty/application/desktop"
)

// Desktop is the desktop integration seam the Settings window drives. The
// application Service implements it; tests provide a fake.
type Desktop interface {
	Settings() desktop.Settings
	Capabilities() desktop.Capabilities
	HotkeyProblem() error
	SetShowTrayIcon(show bool) error
	SetCloseToTray(enabled bool) error
	SetGlobalHotkeyEnabled(enabled bool) error
	SetToggleWindowHotkey(accelerator string) error
	ResetToggleHotkey() error
	SetAutoStart(enabled bool) error
	SetSSHDeepLinks(enabled bool) error
	SetNotification(event desktop.Event, enabled bool) error
}

// DesktopProblem identifies a Settings failure the view must localize.
type DesktopProblem string

const (
	DesktopProblemNone        DesktopProblem = ""
	DesktopProblemHotkey      DesktopProblem = "hotkey"
	DesktopProblemUnsupported DesktopProblem = "unsupported"
	DesktopProblemSave        DesktopProblem = "save"
	DesktopProblemApply       DesktopProblem = "apply"
)

// DesktopState owns the Settings window's desktop integration form. It is
// mutated only on the UI thread.
type DesktopState struct {
	desktop       Desktop
	hotkey        string
	problem       DesktopProblem
	requestRedraw func()
}

// DesktopOption customizes a DesktopState.
type DesktopOption func(*DesktopState)

// WithDesktopRedrawRequest sets the function called after the form changes.
func WithDesktopRedrawRequest(fn func()) DesktopOption {
	return func(s *DesktopState) { s.requestRedraw = fn }
}

// NewDesktopState returns the form for service. It reflects the last hotkey
// registration failure immediately, so an unsupported default is visible
// instead of silently ignored.
func NewDesktopState(service Desktop, opts ...DesktopOption) *DesktopState {
	state := &DesktopState{desktop: service, hotkey: service.Settings().ToggleWindowHotkey}
	for _, opt := range opts {
		opt(state)
	}
	if err := service.HotkeyProblem(); err != nil {
		state.problem = desktopProblemFor(err)
	}
	return state
}

// Settings returns the current persisted settings.
func (s *DesktopState) Settings() desktop.Settings { return s.desktop.Settings() }

// Capabilities reports which controls are useful on this host.
func (s *DesktopState) Capabilities() desktop.Capabilities { return s.desktop.Capabilities() }

// Hotkey returns the editable accelerator input.
func (s *DesktopState) Hotkey() *string { return &s.hotkey }

// Problem returns the current form failure.
func (s *DesktopState) Problem() DesktopProblem { return s.problem }

// RefreshProblem picks up a registration failure that happened outside the
// form, such as the initial global-hotkey registration at startup.
func (s *DesktopState) RefreshProblem() {
	if err := s.desktop.HotkeyProblem(); err != nil && s.problem == DesktopProblemNone {
		s.problem = desktopProblemFor(err)
	}
}

// SetShowTrayIcon stores the tray visibility preference.
func (s *DesktopState) SetShowTrayIcon(show bool) bool {
	return s.apply(func() error { return s.desktop.SetShowTrayIcon(show) })
}

// SetCloseToTray stores the close-to-tray preference.
func (s *DesktopState) SetCloseToTray(enabled bool) bool {
	return s.apply(func() error { return s.desktop.SetCloseToTray(enabled) })
}

// SetSSHDeepLinks stores the ssh:// deep-link preference.
func (s *DesktopState) SetSSHDeepLinks(enabled bool) bool {
	return s.apply(func() error { return s.desktop.SetSSHDeepLinks(enabled) })
}

// SetGlobalHotkeyEnabled turns the global shortcut on or off.
func (s *DesktopState) SetGlobalHotkeyEnabled(enabled bool) bool {
	return s.apply(func() error { return s.desktop.SetGlobalHotkeyEnabled(enabled) })
}

// SetAutoStart changes the OS login item.
func (s *DesktopState) SetAutoStart(enabled bool) bool {
	return s.apply(func() error { return s.desktop.SetAutoStart(enabled) })
}

// SetNotification stores one notification event toggle.
func (s *DesktopState) SetNotification(event desktop.Event, enabled bool) bool {
	return s.apply(func() error { return s.desktop.SetNotification(event, enabled) })
}

// ApplyHotkey registers the typed accelerator and persists it on success.
func (s *DesktopState) ApplyHotkey() bool {
	ok := s.apply(func() error { return s.desktop.SetToggleWindowHotkey(s.hotkey) })
	if ok {
		s.hotkey = s.desktop.Settings().ToggleWindowHotkey
	}
	return ok
}

// ResetHotkey restores the platform default and shows it in the input.
func (s *DesktopState) ResetHotkey() bool {
	ok := s.apply(s.desktop.ResetToggleHotkey)
	if ok {
		s.hotkey = s.desktop.Settings().ToggleWindowHotkey
	}
	return ok
}

func (s *DesktopState) apply(change func() error) bool {
	err := change()
	if err != nil {
		s.problem = desktopProblemFor(err)
		s.redraw()
		return false
	}
	s.problem = DesktopProblemNone
	s.redraw()
	return true
}

func desktopProblemFor(err error) DesktopProblem {
	switch desktop.KindOf(err) {
	case desktop.KindUnsupported:
		return DesktopProblemUnsupported
	case desktop.KindHotkey:
		return DesktopProblemHotkey
	case desktop.KindAutoStart:
		return DesktopProblemApply
	default:
		return DesktopProblemSave
	}
}

func (s *DesktopState) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}
