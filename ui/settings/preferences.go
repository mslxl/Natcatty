package settings

import (
	domain "github.com/mslxl/natcatty/domain/preferences"
)

// Preferences is the typed Settings seam the window drives. The application
// service implements it; tests provide a fake.
type Preferences interface {
	Settings() domain.Settings
	Update(func(*domain.Settings)) error
	ResetGroup(domain.Group) error
	ResetAll() error
}

// PreferencesState owns the Settings window's typed preference form. It is
// mutated only on the UI thread.
type PreferencesState struct {
	prefs         Preferences
	failed        bool
	resetAll      bool
	drafts        map[domain.Field]*string
	requestRedraw func()
}

// PreferencesOption customizes a PreferencesState.
type PreferencesOption func(*PreferencesState)

// WithPreferencesRedrawRequest sets the function called after a change.
func WithPreferencesRedrawRequest(fn func()) PreferencesOption {
	return func(s *PreferencesState) { s.requestRedraw = fn }
}

// NewPreferencesState returns the typed preference form for service.
func NewPreferencesState(service Preferences, opts ...PreferencesOption) *PreferencesState {
	state := &PreferencesState{prefs: service}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Settings returns the current persisted document.
func (s *PreferencesState) Settings() domain.Settings { return s.prefs.Settings() }

// Failed reports whether the last change could not be saved.
func (s *PreferencesState) Failed() bool { return s.failed }

// Update applies change through the service. A save failure leaves the old
// value in place and is reported next to the area's reset control.
func (s *PreferencesState) Update(change func(*domain.Settings)) bool {
	if change == nil {
		return true
	}
	if err := s.prefs.Update(change); err != nil {
		s.failed = true
		s.redraw()
		return false
	}
	s.failed = false
	s.drafts = nil
	s.redraw()
	return true
}

// Field returns the bound draft for a text setting. The draft keeps typing
// local until the control reports a change, so a failed save does not leave
// the input showing a value the app is not using.
func (s *PreferencesState) Field(field domain.Field) *string {
	if s.drafts == nil {
		s.drafts = make(map[domain.Field]*string)
	}
	if draft, ok := s.drafts[field]; ok {
		return draft
	}
	value := fieldValue(s.Settings(), field)
	s.drafts[field] = &value
	return &value
}

// FieldValue returns the current draft of a text setting.
func (s *PreferencesState) FieldValue(field domain.Field) string {
	if draft, ok := s.drafts[field]; ok {
		return *draft
	}
	return fieldValue(s.Settings(), field)
}

// AskResetAll opens the localized destructive confirmation.
func (s *PreferencesState) AskResetAll() { s.resetAll = true; s.redraw() }

// CancelResetAll closes the confirmation without changing anything.
func (s *PreferencesState) CancelResetAll() { s.resetAll = false; s.redraw() }

// ResettingAll reports whether the confirmation is open.
func (s *PreferencesState) ResettingAll() bool { return s.resetAll }

// ResetField restores a single setting to its default.
func (s *PreferencesState) ResetField(field domain.Field) bool {
	return s.Update(func(settings *domain.Settings) {
		*settings = domain.ResetField(*settings, field)
	})
}

// ResetGroup restores one Settings area to its defaults.
func (s *PreferencesState) ResetGroup(group domain.Group) bool {
	if err := s.prefs.ResetGroup(group); err != nil {
		s.failed = true
		s.redraw()
		return false
	}
	s.failed = false
	s.drafts = nil
	s.redraw()
	return true
}

// ResetAll restores every group. The caller has already asked the user.
func (s *PreferencesState) ResetAll() bool {
	s.resetAll = false
	if err := s.prefs.ResetAll(); err != nil {
		s.failed = true
		s.redraw()
		return false
	}
	s.failed = false
	s.drafts = nil
	s.redraw()
	return true
}

// IsDefault reports whether field currently holds its default value. It is
// what decides whether the row needs a reset control.
func (s *PreferencesState) IsDefault(field domain.Field) bool {
	return domain.Equal(domain.ResetField(s.Settings(), field), s.Settings())
}

func (s *PreferencesState) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}

func fieldValue(settings domain.Settings, field domain.Field) string {
	switch field {
	case domain.FieldAccentColor:
		return settings.Appearance.AccentColor
	case domain.FieldUIFont:
		return settings.Appearance.UIFont
	case domain.FieldTerminalTheme:
		return settings.Appearance.TerminalTheme
	case domain.FieldTerminalFontFamily:
		return settings.Appearance.TerminalFontFamily
	case domain.FieldSyncProvider:
		return settings.Sync.Provider
	default:
		return ""
	}
}
