package settings

import (
	"strconv"
	"strings"

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
	SetHotkeyMode(mode desktop.HotkeyMode) error
	SetQuakeSource(source desktop.QuakeSource) error
	SetQuakeHeight(value int, unit desktop.HeightUnit) error
	SetTopdownHeight(value int, unit desktop.HeightUnit) error
	SetHideOnBlur(enabled bool) error
	SetHideOnEscape(enabled bool) error
	SetHideOnClickOutside(enabled bool) error
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
	// DesktopProblemHeight reports a dropdown height that is not a number or
	// is outside its unit's accepted range. The height section renders it
	// inline; the value is not persisted.
	DesktopProblemHeight DesktopProblem = "height"
)

// HeightDraft is the editable text and unit of one dropdown height. The text
// stays a string so an invalid entry stays visible beside its error instead
// of silently snapping to a bound; a successful apply replaces it with the
// persisted value.
type HeightDraft struct {
	Value string
	Unit  desktop.HeightUnit
}

// DesktopState owns the Settings window's desktop integration form. It is
// mutated only on the UI thread.
type DesktopState struct {
	desktop       Desktop
	hotkey        string
	quakeHeight   HeightDraft
	topdownHeight HeightDraft
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
	settings := service.Settings()
	state := &DesktopState{
		desktop:       service,
		hotkey:        settings.ToggleWindowHotkey,
		quakeHeight:   newHeightDraft(settings.QuakeHeight, desktop.DefaultQuakeHeight, settings.QuakeHeightUnit),
		topdownHeight: newHeightDraft(settings.TopdownHeight, desktop.DefaultTopdownHeight, settings.TopdownHeightUnit),
	}
	for _, opt := range opts {
		opt(state)
	}
	if err := service.HotkeyProblem(); err != nil {
		state.problem = desktopProblemFor(err)
	}
	return state
}

// newHeightDraft builds the editable field from persisted settings. A missing
// or invalid value falls back to the mode's default so the input never starts
// on a number the service would reject.
func newHeightDraft(value, fallback int, unit desktop.HeightUnit) HeightDraft {
	if !unit.Valid() {
		unit = desktop.DefaultHeightUnit
	}
	if value <= 0 {
		value = fallback
	}
	if minimum, maximum, ok := desktop.HeightBounds(unit); ok {
		if value < minimum {
			value = minimum
		}
		if value > maximum {
			value = maximum
		}
	}
	return HeightDraft{Value: strconv.Itoa(value), Unit: unit}
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

// SetHotkeyMode changes what the global shortcut does. A registration failure
// rolls the service back and surfaces as the inline hotkey problem.
func (s *DesktopState) SetHotkeyMode(mode desktop.HotkeyMode) bool {
	return s.apply(func() error { return s.desktop.SetHotkeyMode(mode) })
}

// SetQuakeSource selects what the Quake dropdown renders: its own Workspace
// or the main window's active Tab. The app retires a visible Panel so the
// render ownership is restored under the new route.
func (s *DesktopState) SetQuakeSource(source desktop.QuakeSource) bool {
	return s.apply(func() error { return s.desktop.SetQuakeSource(source) })
}

// QuakeHeight returns the editable Quake height.
func (s *DesktopState) QuakeHeight() *HeightDraft { return &s.quakeHeight }

// TopdownHeight returns the editable top-down height.
func (s *DesktopState) TopdownHeight() *HeightDraft { return &s.topdownHeight }

// ApplyQuakeHeight validates the typed value with the chosen unit and
// persists it. An unparsable or out-of-range entry sets DesktopProblemHeight
// and changes nothing, so the invalid text stays visible for correction.
func (s *DesktopState) ApplyQuakeHeight() bool {
	if !s.applyHeight(&s.quakeHeight, s.desktop.SetQuakeHeight) {
		return false
	}
	settings := s.desktop.Settings()
	s.quakeHeight = newHeightDraft(settings.QuakeHeight, desktop.DefaultQuakeHeight, settings.QuakeHeightUnit)
	return true
}

// ApplyTopdownHeight is ApplyQuakeHeight for the top-down mode.
func (s *DesktopState) ApplyTopdownHeight() bool {
	if !s.applyHeight(&s.topdownHeight, s.desktop.SetTopdownHeight) {
		return false
	}
	settings := s.desktop.Settings()
	s.topdownHeight = newHeightDraft(settings.TopdownHeight, desktop.DefaultTopdownHeight, settings.TopdownHeightUnit)
	return true
}

func (s *DesktopState) applyHeight(draft *HeightDraft, set func(int, desktop.HeightUnit) error) bool {
	value, err := strconv.Atoi(strings.TrimSpace(draft.Value))
	if err != nil {
		s.problem = DesktopProblemHeight
		s.redraw()
		return false
	}
	return s.apply(func() error { return set(value, draft.Unit) })
}

// SetHideOnBlur toggles hiding the Dropdown Panel when it loses focus.
func (s *DesktopState) SetHideOnBlur(enabled bool) bool {
	return s.apply(func() error { return s.desktop.SetHideOnBlur(enabled) })
}

// SetHideOnEscape toggles hiding the Dropdown Panel on Escape.
func (s *DesktopState) SetHideOnEscape(enabled bool) bool {
	return s.apply(func() error { return s.desktop.SetHideOnEscape(enabled) })
}

// SetHideOnClickOutside toggles hiding the Dropdown Panel when the user clicks
// outside it. On platforms without a separate click-outside signal the blur
// switch governs the shared gesture.
func (s *DesktopState) SetHideOnClickOutside(enabled bool) bool {
	return s.apply(func() error { return s.desktop.SetHideOnClickOutside(enabled) })
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
	case desktop.KindValidation:
		// The only validating controls in this form are the height fields.
		return DesktopProblemHeight
	default:
		return DesktopProblemSave
	}
}

func (s *DesktopState) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}
