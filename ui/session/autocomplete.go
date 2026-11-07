package sessionui

import (
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
)

// refreshSuggestions recomputes the popup rows from the line being typed. It
// runs once per frame, so the popup follows the Session's input buffer rather
// than duplicating terminal key handling. Suggestions are suppressed while a
// command runs: Tab must reach vim, less, and other full-screen programs even
// when the typed text matches history. Dismissing the popup keeps it hidden
// until the input changes again.
func (s *Surface) refreshSuggestions(input, cwd string, commandRunning bool) {
	popup, ghost := false, false
	if s.options.AutocompleteDisplay != nil {
		popup, ghost = s.options.AutocompleteDisplay()
	}
	if s.options.Suggest == nil || (!popup && !ghost) || commandRunning {
		s.suggestions = nil
		s.suggestionIndex = 0
		s.suggestionInput = input
		s.showAutocomplete = false
		s.showGhost = false
		return
	}
	delay := 0
	if s.options.AutocompleteDelay != nil {
		delay = s.options.AutocompleteDelay()
	}
	if input != s.suggestionInput {
		s.suggestionInput = input
		s.suggestionIndex = 0
		s.suggestions = nil
		s.suggestionDismissed = false
		s.suggestionPending = time.Now()
		if s.suggestionTimer != nil {
			s.suggestionTimer.Stop()
			s.suggestionTimer = nil
		}
		if input != "" && delay > 0 {
			// Redraw once the delay passes; typing again replaces this timer.
			s.suggestionTimer = time.AfterFunc(time.Duration(delay)*time.Millisecond, s.redraw)
		}
	}
	if delay > 0 && time.Since(s.suggestionPending) < time.Duration(delay)*time.Millisecond {
		s.showAutocomplete = false
		s.showGhost = false
		return
	}
	if input == "" {
		s.suggestions = nil
	}
	if !s.suggestionDismissed && s.suggestions == nil && input != "" {
		// Read under the same scope the capture pipeline writes: a saved Host,
		// an unsaved Host with its captured origin, a serial port, or local.
		// The cwd is the shell's last OSC 7 value for the ranking bonus.
		matches := s.options.Suggest(input, s.session.HistoryScope(), cwd)
		filtered := make([]autocomplete.Suggestion, 0, len(matches))
		for _, suggestion := range matches {
			if suggestion.Text != input {
				filtered = append(filtered, suggestion)
			}
		}
		s.suggestions = filtered
	}
	if s.suggestionIndex >= len(s.suggestions) {
		s.suggestionIndex = 0
	}
	s.showAutocomplete = popup && len(s.suggestions) > 0
	s.showGhost = ghost && len(s.suggestions) > 0
}

// handleAutocompleteKey runs before MyGo encodes a key, so Tab can replace the
// current line without the terminal seeing a tab character. It returns false
// for every key the popup does not use.
func (s *Surface) handleAutocompleteKey(mods ui.Modifiers, key ui.Key) bool {
	if mods != 0 {
		return false
	}
	switch key {
	case ui.KeyTab, ui.KeyEscape, ui.KeyUp, ui.KeyDown:
	default:
		return false
	}
	snapshot := s.session.Snapshot()
	s.refreshSuggestions(snapshot.Input, snapshot.CWD, snapshot.Command.Running)
	switch key {
	case ui.KeyTab:
		if len(s.suggestions) == 0 || (!s.showAutocomplete && !s.showGhost) {
			return false
		}
		s.acceptSuggestion(s.suggestionIndex)
	case ui.KeyEscape:
		if len(s.suggestions) == 0 {
			return false
		}
		s.dismissSuggestions()
	case ui.KeyUp:
		if len(s.suggestions) == 0 {
			return false
		}
		s.moveSuggestion(-1)
	case ui.KeyDown:
		if len(s.suggestions) == 0 {
			return false
		}
		s.moveSuggestion(1)
	}
	return true
}

// acceptSuggestion replaces the typed line with the suggestion through
// insertTextReplacing, the safe insertion seam the side panel also uses. The
// line is cleared with one Backspace (DEL) per character rather than Ctrl+U:
// PSReadLine binds Ctrl+U to Undo, so PowerShell rendered the raw byte and
// merged the suggestion into the typed text ("git s^Ugit stash", #18).
// Backspace is the one erase every target understands — readline, zle, and
// PSReadLine all delete the character to its left — and the Session's tap
// counts the same runes, so the tracked line stays aligned with the shell's.
// The erases and the replacement are queued as one write only after the
// sanitizer and bracketed-paste check, so a multi-line suggestion (for
// example a row synced from another device) is pasted into the editor instead
// of being executed, and without DEC 2004 it is refused with the typed prefix
// left intact. No Tab byte and no submit is ever sent.
func (s *Surface) acceptSuggestion(index int) {
	if index < 0 || index >= len(s.suggestions) {
		return
	}
	text := s.suggestions[index].Text
	if text == "" {
		return
	}
	input := s.session.Snapshot().Input
	eraseCount := utf8.RuneCountInString(input)
	if err := s.insertTextReplacing(eraseCount, text); err != nil {
		// Nothing reached the shell, so keep the popup and the typed line:
		// the user can still see, copy, or insert the row another way.
		s.redraw()
		return
	}
	s.suggestionInput = input
	s.suggestions = nil
	s.suggestionIndex = 0
	s.suggestionDismissed = true
	s.showAutocomplete = false
	s.showGhost = false
	s.redraw()
}

func (s *Surface) dismissSuggestions() {
	s.suggestions = nil
	s.suggestionIndex = 0
	s.suggestionDismissed = true
	s.showAutocomplete = false
	s.showGhost = false
	s.redraw()
}

func (s *Surface) moveSuggestion(delta int) {
	count := len(s.suggestions)
	if count == 0 {
		return
	}
	s.suggestionIndex = (s.suggestionIndex + delta + count) % count
	s.redraw()
}

// SuggestionRows is the popup rows the surface currently shows. Tests and the
// self-test use it to assert what Tab would accept.
func (s *Surface) SuggestionRows() []autocomplete.Suggestion {
	return append([]autocomplete.Suggestion(nil), s.suggestions...)
}

// InvalidateSuggestions drops the popup rows after the shared journal changed.
// The next frame recomputes them from the reloaded service, so a deleted,
// pruned, or cleared command cannot stay visible or be accepted by Tab.
func (s *Surface) InvalidateSuggestions() {
	s.suggestions = nil
	s.suggestionIndex = 0
	s.showAutocomplete = false
	s.showGhost = false
	s.redraw()
}
