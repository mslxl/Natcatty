package sessionui

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
)

// refreshSuggestions recomputes the popup rows from the line being typed. It
// runs once per frame, so the popup follows the Session's input buffer rather
// than duplicating terminal key handling. Suggestions are suppressed while a
// command runs: Tab must reach vim, less, and other full-screen programs even
// when the typed text matches history. A program input prompt (a colon-label
// read such as "Email: ") is the exception: the program is waiting for typed
// text, so history and Vault Snippets may be offered there. Dismissing the
// popup keeps it hidden until the input changes again.
func (s *Surface) refreshSuggestions(input, cwd string, commandRunning bool) {
	s.refreshSuggestionsFor(input, cwd, commandRunning, false)
}

// refreshSuggestionsFor is refreshSuggestions with the debounce made optional.
// A key that acts on the rows (Tab accepting one) cannot wait out the delay the
// popup uses: pressing Tab within the debounce window after typing looked at an
// empty list, declined the key, and the Pane sent a literal tab to the shell.
// skipDelay recomputes the rows right away; the delay still governs when the
// popup first appears, since the frame path keeps calling without it.
func (s *Surface) refreshSuggestionsFor(input, cwd string, commandRunning, skipDelay bool) {
	popup, ghost := false, false
	if s.options.AutocompleteDisplay != nil {
		popup, ghost = s.options.AutocompleteDisplay()
	}
	program := s.atProgramPrompt(input, commandRunning)
	if program != s.suggestionProgram {
		// Entering or leaving a program prompt changes the source set, so the
		// cached rows are re-asked even when the typed text is unchanged.
		s.suggestionProgram = program
		s.suggestions = nil
		s.suggestionIndex = 0
	}
	s.programPrompt = program
	if s.options.Suggest == nil || (!popup && !ghost) || (commandRunning && !program) {
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
	if s.locale != s.suggestionLocale {
		// The service fills Description from the live locale's catalog, so the
		// cached rows are re-asked after a language switch; the text itself is
		// unchanged and the ranking stays the same.
		s.suggestionLocale = s.locale
		s.suggestions = nil
	}
	if !skipDelay && delay > 0 && time.Since(s.suggestionPending) < time.Duration(delay)*time.Millisecond {
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
		var matches []autocomplete.Suggestion
		if s.options.SuggestAtPrompt != nil {
			matches = s.options.SuggestAtPrompt(input, s.session.HistoryScope(), cwd, program)
		} else {
			matches = s.options.Suggest(input, s.session.HistoryScope(), cwd)
		}
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

// atProgramPrompt reports whether the terminal's last visible line has the
// shape of a non-shell program's input prompt. The command must be running:
// a shell's own PS1 cannot match the allowlist, but requiring a running
// command also keeps a normal prompt that merely mentions "Email:" out of
// this path. Only the prompt text matters, so an un-echoed keystroke does not
// hide it.
func (s *Surface) atProgramPrompt(input string, commandRunning bool) bool {
	if !commandRunning || input == "" || s.terminal == nil {
		return false
	}
	text := s.terminal.Text()
	line := text
	if index := strings.LastIndexByte(text, '\n'); index >= 0 {
		line = text[index+1:]
	}
	return autocomplete.ProgramInputPrompt(line)
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
	// Tab is an explicit request for completion, so a popup the user dismissed
	// earlier (Escape, or the click that accepted the previous row) must not
	// keep it from answering: without this the key path recomputed nothing,
	// declined, and the Pane handed the shell a literal tab.
	if key == ui.KeyTab {
		s.suggestionDismissed = false
	}
	// The key path cannot ride the popup's debounce: a Tab pressed right after
	// typing must still see the rows it acts on, or it falls through and the
	// shell receives a tab.
	s.refreshSuggestionsFor(snapshot.Input, snapshot.CWD, snapshot.Command.Running, true)
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

// acceptSuggestion replaces the typed text with the suggestion through
// insertTextReplacing, the safe insertion seam the side panel also uses. The
// text to erase is counted from the tracked cursor, never from the line's
// length: at a shell line start it is the whole typed line, after another
// word it is just the current word, and mid-line it is only the prefix left
// of the cursor, so the rest of the line is never truncated. At a program
// input prompt the typed text is a query, so only the query word is replaced.
// The erases are Backspace (DEL) rather than Ctrl+U: PSReadLine binds Ctrl+U
// to Undo, so PowerShell rendered the raw byte and merged the suggestion into
// the typed text ("git s^Ugit stash", #18). Backspace is the one erase every
// target understands — readline, zle, and PSReadLine all delete the character
// to its left — and the Session's tap counts the same runes, so the tracked
// line stays aligned with the shell's. The erases and the replacement are
// queued as one write only after the sanitizer and bracketed-paste check, so
// a multi-line suggestion (for example a snippet or a row synced from another
// device) is pasted into the editor instead of being executed, and without
// DEC 2004 it is refused with the typed prefix left intact. No Tab byte and no
// submit is ever sent.
func (s *Surface) acceptSuggestion(index int) {
	if index < 0 || index >= len(s.suggestions) {
		return
	}
	text := s.suggestions[index].Text
	if text == "" {
		return
	}
	snapshot := s.session.Snapshot()
	input := snapshot.Input
	eraseCount := autocomplete.AcceptEraseCount(input, snapshot.InputCursor, s.programPrompt)
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
