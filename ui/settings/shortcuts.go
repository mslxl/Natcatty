package settings

import (
	"strconv"

	"github.com/egoist/mygo/ui"

	domain "github.com/mslxl/natcatty/domain/keybindings"
)

// KeyBindings is the keyboard-shortcut seam the Settings window drives. The
// application service implements it; tests provide a fake.
type KeyBindings interface {
	Scheme() domain.Scheme
	Catalog() []domain.Binding
	Chord(id string, scheme domain.Scheme) string
	IsOverridden(id string, scheme domain.Scheme) bool
	Conflicts(menus []domain.MenuShortcut) []domain.Conflict
	SetScheme(domain.Scheme) error
	SetBinding(id string, scheme domain.Scheme, chord string, menus []domain.MenuShortcut) ([]domain.Conflict, error)
	ReplaceBinding(id string, scheme domain.Scheme, chord string, menus []domain.MenuShortcut) error
	ResetBinding(id string, scheme domain.Scheme) error
	ResetAll() error
}

// PendingConflict is a recorded chord that is already in use. It is applied
// only after the user chooses Replace.
type PendingConflict struct {
	BindingID string
	Chord     string
	Conflicts []domain.Conflict
}

// ShortcutsState owns the Settings window's shortcut scheme, recorder, and
// conflict resolution. It is mutated only on the UI thread.
type ShortcutsState struct {
	bindings      KeyBindings
	menus         func() []domain.MenuShortcut
	available     func(string) bool
	recording     string
	pending       *PendingConflict
	resetAll      bool
	failed        bool
	requestRedraw func()
}

// ShortcutsOption customizes a ShortcutsState.
type ShortcutsOption func(*ShortcutsState)

// WithMenuShortcuts supplies the live menu accelerators so conflict
// detection covers a binding that would shadow a real menu command.
func WithMenuShortcuts(fn func() []domain.MenuShortcut) ShortcutsOption {
	return func(s *ShortcutsState) { s.menus = fn }
}

// WithBindingAvailability marks catalog entries this build cannot dispatch.
// An unavailable binding keeps its catalog entry and override controls but
// never fires.
func WithBindingAvailability(fn func(string) bool) ShortcutsOption {
	return func(s *ShortcutsState) { s.available = fn }
}

// WithShortcutsRedrawRequest sets the function called after a change.
func WithShortcutsRedrawRequest(fn func()) ShortcutsOption {
	return func(s *ShortcutsState) { s.requestRedraw = fn }
}

// NewShortcutsState returns the shortcut form for service.
func NewShortcutsState(service KeyBindings, opts ...ShortcutsOption) *ShortcutsState {
	state := &ShortcutsState{bindings: service, available: func(string) bool { return true }}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Scheme returns the active scheme.
func (s *ShortcutsState) Scheme() domain.Scheme { return s.bindings.Scheme() }

// Catalog returns the original 42-binding catalog.
func (s *ShortcutsState) Catalog() []domain.Binding { return s.bindings.Catalog() }

// Chord returns the effective chord of a binding in the active scheme.
func (s *ShortcutsState) Chord(id string) string {
	return s.bindings.Chord(id, s.Scheme())
}

// IsOverridden reports whether a binding differs from its catalog default.
func (s *ShortcutsState) IsOverridden(id string) bool {
	return s.bindings.IsOverridden(id, s.Scheme())
}

// Available reports whether this build can dispatch a binding.
func (s *ShortcutsState) Available(id string) bool {
	if s.available == nil {
		return true
	}
	return s.available(id)
}

// Recording returns the binding currently recording a chord, or "".
func (s *ShortcutsState) Recording() string { return s.recording }

// BeginRecording starts recording for id.
func (s *ShortcutsState) BeginRecording(id string) {
	s.recording = id
	s.pending = nil
	s.redraw()
}

// CancelRecording stops recording without changing the binding.
func (s *ShortcutsState) CancelRecording() {
	if s.recording == "" {
		return
	}
	s.recording = ""
	s.redraw()
}

// RecordKey handles a key pressed in the recorder. It reports whether the key
// was consumed. Escape cancels; Backspace clears the binding; a key with the
// platform shortcut modifier or a named key becomes the new chord.
func (s *ShortcutsState) RecordKey(id string, key ui.Key, mods ui.Modifiers) bool {
	if s.recording != id || id == "" {
		return false
	}
	if key == ui.KeyEscape && mods == 0 {
		s.recording = ""
		s.redraw()
		return true
	}
	if key == ui.KeyBackspace && mods == 0 {
		s.recording = ""
		s.apply(id, domain.Disabled)
		return true
	}
	chord, ok := recordedChord(key, mods, s.Scheme())
	if !ok {
		return true
	}
	s.recording = ""
	s.apply(id, chord)
	return true
}

// SetScheme switches the active scheme. A switch discards a recording and any
// unresolved conflict because both belong to the previous scheme.
func (s *ShortcutsState) SetScheme(scheme domain.Scheme) bool {
	s.recording = ""
	s.pending = nil
	if err := s.bindings.SetScheme(scheme); err != nil {
		s.failed = true
		s.redraw()
		return false
	}
	s.failed = false
	s.redraw()
	return true
}

// Pending returns the unresolved conflict, if any.
func (s *ShortcutsState) Pending() (PendingConflict, bool) {
	if s.pending == nil {
		return PendingConflict{}, false
	}
	return *s.pending, true
}

// Replace applies the pending chord, displacing the bindings and menu
// accelerators that used it.
func (s *ShortcutsState) Replace() bool {
	if s.pending == nil {
		return false
	}
	pending := *s.pending
	s.pending = nil
	err := s.bindings.ReplaceBinding(pending.BindingID, s.Scheme(), pending.Chord, s.menuShortcuts())
	if err != nil {
		s.failed = true
		s.redraw()
		return false
	}
	s.failed = false
	s.redraw()
	return true
}

// CancelConflict discards the pending chord and leaves every binding alone.
func (s *ShortcutsState) CancelConflict() {
	if s.pending == nil {
		return
	}
	s.pending = nil
	s.redraw()
}

// Conflicts returns the conflicts in the active scheme, including collisions
// with the live application menu.
func (s *ShortcutsState) Conflicts() []domain.Conflict {
	return s.bindings.Conflicts(s.menuShortcuts())
}

// ResetBinding restores one binding's catalog default.
func (s *ShortcutsState) ResetBinding(id string) bool {
	if err := s.bindings.ResetBinding(id, s.Scheme()); err != nil {
		s.failed = true
		s.redraw()
		return false
	}
	s.pending = nil
	s.failed = false
	s.redraw()
	return true
}

// AskResetAll opens the localized destructive confirmation.
func (s *ShortcutsState) AskResetAll() { s.resetAll = true; s.redraw() }

// CancelResetAll closes the confirmation without changing anything.
func (s *ShortcutsState) CancelResetAll() { s.resetAll = false; s.redraw() }

// ResettingAll reports whether the confirmation is open.
func (s *ShortcutsState) ResettingAll() bool { return s.resetAll }

// ResetAll restores every override after the confirmation.
func (s *ShortcutsState) ResetAll() bool {
	s.resetAll = false
	s.pending = nil
	if err := s.bindings.ResetAll(); err != nil {
		s.failed = true
		s.redraw()
		return false
	}
	s.failed = false
	s.redraw()
	return true
}

// Failed reports whether the last change could not be persisted.
func (s *ShortcutsState) Failed() bool { return s.failed }

func (s *ShortcutsState) apply(id, chord string) {
	conflicts, err := s.bindings.SetBinding(id, s.Scheme(), chord, s.menuShortcuts())
	if err != nil {
		s.failed = true
		s.redraw()
		return
	}
	s.failed = false
	if len(conflicts) > 0 {
		s.pending = &PendingConflict{BindingID: id, Chord: chord, Conflicts: conflicts}
	}
	s.redraw()
}

func (s *ShortcutsState) menuShortcuts() []domain.MenuShortcut {
	if s.menus == nil {
		return nil
	}
	return s.menus()
}

func (s *ShortcutsState) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}

// recordedChord turns a native key press into the catalog's chord notation.
// It reports false for a bare modifier or an unmappable key, which the
// recorder ignores.
func recordedChord(key ui.Key, mods ui.Modifiers, scheme domain.Scheme) (string, bool) {
	name, ok := recordedKeyName(key)
	if !ok {
		return "", false
	}
	var chord domain.Chord
	if mods&ui.Ctrl != 0 {
		chord.Mods |= domain.ModCtrl
	}
	if mods&ui.Alt != 0 {
		chord.Mods |= domain.ModAlt
	}
	if mods&ui.Shift != 0 {
		chord.Mods |= domain.ModShift
	}
	if mods&ui.Super != 0 {
		chord.Mods |= domain.ModSuper
	}
	chord.Key = name
	return chord.String(scheme), true
}

func recordedKeyName(key ui.Key) (string, bool) {
	switch key {
	case ui.KeyEnter:
		return "Enter", true
	case ui.KeyEscape:
		return "Esc", true
	case ui.KeyBackspace:
		return "Backspace", true
	case ui.KeyTab:
		return "Tab", true
	case ui.KeySpace:
		return "Space", true
	case ui.KeyDelete:
		return "Delete", true
	case ui.KeyInsert:
		return "Insert", true
	case ui.KeyHome:
		return "Home", true
	case ui.KeyEnd:
		return "End", true
	case ui.KeyPageUp:
		return "PageUp", true
	case ui.KeyPageDown:
		return "PageDown", true
	case ui.KeyLeft:
		return "Left", true
	case ui.KeyRight:
		return "Right", true
	case ui.KeyUp:
		return "Up", true
	case ui.KeyDown:
		return "Down", true
	case ui.KeyMinus:
		return "-", true
	case ui.KeyEqual:
		return "=", true
	case ui.KeyComma:
		return ",", true
	case ui.KeyPeriod:
		return ".", true
	case ui.KeySlash:
		return "/", true
	case ui.KeySemicolon:
		return ";", true
	case ui.KeyQuote:
		return "'", true
	case ui.KeyBracketLeft:
		return "[", true
	case ui.KeyBracketRight:
		return "]", true
	case ui.KeyBackslash:
		return "\\", true
	case ui.KeyBackquote:
		return "`", true
	}
	if key >= ui.KeyF1 && key <= ui.KeyF12 {
		return "F" + strconv.Itoa(int(key-ui.KeyF1)+1), true
	}
	if key >= ui.KeyA && key <= ui.KeyZ {
		return string(rune('A' + int(key-ui.KeyA))), true
	}
	if key >= ui.Key0 && key <= ui.Key9 {
		return string(rune('0' + int(key-ui.Key0))), true
	}
	return "", false
}
