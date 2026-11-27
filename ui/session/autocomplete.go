package sessionui

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
)

// refreshSuggestionsFor recomputes the popup rows from the line being typed.
// It runs once per frame, so the popup follows the Session's input buffer
// rather than duplicating terminal key handling. The popup is automatic once
// the debounce passes — the original's model, and the only one left once Tab
// is the shell's own: the list never shadows a keystroke, because accepting
// it is an explicit arrow into a selection, which live-renders the candidate
// into the real line, and stepping back off the end restores the typed
// baseline. The ghost hint is the passive half: it draws the first match
// inline and needs no key. Suggestions are suppressed while a command runs:
// the keys must reach vim, less, and other full-screen programs even when the
// typed text matches history. A program input prompt (a colon-label read such
// as "Email: ") is the exception: the program is waiting for typed text, so
// history and Vault Snippets may be offered there. Dismissing the popup keeps
// it hidden until the input changes again.
//
// skipDelay bypasses the debounce for paths that cannot wait it out — the
// test forcing a frame, or a key acting on rows right after typing.
// screenSourced reports that the input is the rendered line rather than the
// keystroke mirror — a lagging witness whose shortenings may be echo that has
// not caught up rather than the user's edit.
func (s *Surface) refreshSuggestionsFor(input, cwd string, commandRunning, screenSourced, skipDelay bool) {
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
		s.suggestionIndex = noSelection
	}
	s.programPrompt = program
	if s.options.Suggest == nil || (!popup && !ghost) || (commandRunning && !program) {
		s.suggestions = nil
		s.suggestionIndex = noSelection
		s.suggestionInput = input
		s.showAutocomplete = false
		s.showGhost = false
		s.previewActive = false
		if commandRunning {
			// The dismissal is the line's own: it answers the text the user
			// was editing when they closed the popup, and the running command
			// means that line is submitted and gone. The next prompt starts
			// undismissed.
			s.suggestionDismissed = false
		}
		return
	}
	delay := 0
	if s.options.AutocompleteDelay != nil {
		delay = s.options.AutocompleteDelay()
	}
	if input != s.suggestionInput {
		if s.ownWriteLine != "" && input == s.ownWriteLine {
			// The change is the surface's own write landing in the mirror —
			// a preview, its revert, or a click's commit. It is not the
			// user's edit: the rows still answer the typed baseline, the
			// dismissal survives it, and the preview keeps its state. The
			// mirror tracks the write synchronously, so the recognition
			// fires on the first frame after it and the marker is spent.
			s.ownWriteLine = ""
			s.suggestionInput = input
		} else if s.ownWriteLine != "" && screenSourced && strings.HasPrefix(s.ownWriteLine, input) {
			// Echo of the surface's own write still in flight: the
			// screen-sourced input is a shortening of what the write
			// produced. Neither a user edit nor the write's completion —
			// hold everything as it is until the echo catches up.
		} else if s.suggestionDismissed && screenSourced && strings.HasPrefix(s.suggestionInput, input) {
			// A dismissed popup stays dismissed through a shortening of the
			// screen-sourced line it applied to. The screen is a lagging
			// witness: echo that has not caught up reads as exactly this
			// shape — a prefix of the real line — for as long as the shell
			// takes to repaint, and treating it as the user's edit would
			// reopen the popup the keystroke just closed. Only the
			// screen-sourced input gets this benefit: while the mirror is
			// reliable every change is a keystroke, and an erasure must
			// expire the dismissal exactly like typing does. The genuine
			// edits this also holds back — erasing into a dismissed line
			// after the mirror was lost — keep the rows away for this line,
			// which is the dismissal's point either way. Nothing is updated:
			// when the echo catches up the line is the dismissed text again,
			// and the change never happened.
		} else {
			// Any other change is the user's — or the shell's completion
			// landing after the Tab the popup yielded to — so the dismissal
			// expires with the text it applied to. A write marker the change
			// did not match is stale: the line moved on without it.
			s.ownWriteLine = ""
			if s.previewActive {
				// The user edited the line under a live preview: the text is
				// theirs now, Escape must not drag it back, and the next
				// preview starts from what they type here.
				s.previewActive = false
			}
			s.suggestionInput = input
			s.suggestionIndex = noSelection
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
	}
	if s.locale != s.suggestionLocale {
		// The service fills Description from the live locale's catalog, so the
		// cached rows are re-asked after a language switch; the text itself is
		// unchanged and the ranking stays the same.
		s.suggestionLocale = s.locale
		s.suggestions = nil
	}
	if !skipDelay && delay > 0 && time.Since(s.suggestionPending) < time.Duration(delay)*time.Millisecond {
		// The debounce gates the passive layers: rows and ghost that tracked
		// every keystroke would flicker with the echo. A live preview is
		// outside it — the user is steering the list, and closing it under
		// their arrows because the last keystroke is still inside the window
		// would answer navigation with the shell's history keys.
		if !s.previewActive {
			s.showAutocomplete = false
			s.showGhost = false
			return
		}
	} else {
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
	}
	if s.suggestionIndex >= len(s.suggestions) {
		s.suggestionIndex = noSelection
	}
	s.showAutocomplete = popup && len(s.suggestions) > 0 && !s.suggestionDismissed && input != ""
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

// handleAutocompleteKey runs before MyGo encodes a key. Its keymap follows the
// original's: Tab is never intercepted — natcatty's completion never shadows
// the shell's own, which is the completion the user means when they press it.
// The popup is steered with the arrow keys, and moving the selection live-
// renders the candidate into the real command line, so Enter needs no popup
// awareness at all: it passes through, and the line already holds what the
// user chose. The ghost hint accepts on the Right arrow. It returns false for
// every key the popup does not use.
func (s *Surface) handleAutocompleteKey(mods ui.Modifiers, key ui.Key) bool {
	if mods != 0 {
		return false
	}
	switch key {
	case ui.KeyTab:
		// Tab belongs to the shell's native completion in every state — the
		// original's #1005: intercepting it here shadows readline/zle behind
		// natcatty's rows, and the shell's completion is the one the line
		// editor itself acts on. The popup steps aside and stays aside for
		// this line as typed: the shell is answering the same question
		// natcatty's rows answered, and its menu paints over the same screen
		// space. What the shell leaves on the line is a new question — a
		// completed line asks the rows again on its own, as does typing the
		// next character. A preview in flight must be reverted first, or the
		// shell completes against a candidate the user never committed to.
		if !s.showAutocomplete && !s.showGhost {
			return false
		}
		if s.previewActive {
			s.revertPreview()
		}
		s.dismissSuggestions()
		return false
	case ui.KeyRight:
		if !s.showGhost || len(s.suggestions) == 0 {
			return false
		}
		// The ghost accepts only at the end of the line: mid-line the hint
		// does not continue the input, and Right is the shell's cursor key —
		// the original's guard, and the difference between accepting a hint
		// and trapping every rightward move while rows happen to exist.
		snapshot := s.session.Snapshot()
		input := snapshot.Input
		text := s.suggestions[0].Text
		if text == "" || !strings.HasPrefix(text, input) || snapshot.InputCursor != len([]rune(input)) {
			return false
		}
		s.acceptGhost()
		return true
	case ui.KeyEscape:
		if !s.showAutocomplete {
			return false
		}
		// Escape restores the line the user typed: a live-preview rewrote it
		// under their fingers, and the baseline is the only text they chose.
		if s.previewActive {
			s.revertPreview()
		}
		s.dismissSuggestions()
	case ui.KeyUp:
		// The keys steer the open list. With the list closed they are the
		// shell's own — history recall, a menu's selection — even when the rows
		// exist, because rows exist for every typed prefix and the user never
		// asked to see them.
		if !s.showAutocomplete {
			return false
		}
		s.moveSuggestion(-1)
	case ui.KeyDown:
		if !s.showAutocomplete {
			return false
		}
		s.moveSuggestion(1)
	default:
		return false
	}
	return true
}

// noSelection is the popup's resting selection: the typed baseline itself.
// Stepping onto a row live-renders it; stepping back off restores what the
// user typed, so the cycle cost of looking at the list is zero.
const noSelection = -1

// writeLivePreview rewrites the shell line so it holds text, the original's
// live preview: appending the tail when the candidate continues the line,
// erasing left of the cursor and writing whole otherwise — at a shell prompt
// the erasure is everything left of the cursor, at a program input prompt
// only the current query word, because that is the text a completion answer
// replaces in either place. The erases are Backspace (DEL) rather than
// Ctrl+U: PSReadLine binds Ctrl+U to Undo, so PowerShell rendered the raw
// byte and merged the suggestion into the typed text ("git s^Ugit stash",
// #18). Backspace is the one erase every target understands — readline, zle,
// and PSReadLine all delete the character to their left — and the Session's
// tap counts the same runes, so the tracked line stays aligned with the
// shell's. The bytes go through the insertion seam, so a multi-line candidate
// is paste-wrapped or refused before any erasure leaves the line
// half-cleared. No submit byte is ever sent.
//
// It returns what the write did to the line — the full post-write line
// content and the cursor's rune offset within it — so the caller can
// recognize its own write in the mirror and invert it exactly; the candidate
// alone is neither, because a mid-line or program-prompt write keeps text
// around the candidate.
func (s *Surface) writeLivePreview(input string, cursor int, text string) (line string, cursorAfter int, err error) {
	inputRunes := []rune(input)
	if input == text {
		return input, cursor, nil
	}
	if strings.HasPrefix(text, input) && cursor == len(inputRunes) {
		// The candidate extends the line as-is: appending the tail is the
		// whole rewrite, and the user sees their text grow rather than the
		// line flash away and back. The line becomes the candidate verbatim.
		if err := s.insertTextReplacing(0, text[len(input):]); err != nil {
			return "", 0, err
		}
		return text, len([]rune(text)), nil
	}
	eraseCount := autocomplete.AcceptEraseCount(input, cursor, s.programPrompt)
	if err := s.insertTextReplacing(eraseCount, text); err != nil {
		return "", 0, err
	}
	// The write erases eraseCount runes left of the cursor and inserts the
	// candidate there. Text right of the cursor is never touched, so the
	// post-write line is deterministic.
	head := string(inputRunes[:cursor-eraseCount])
	suffix := string(inputRunes[cursor:])
	return head + text + suffix, len([]rune(head + text)), nil
}

// moveSuggestion steers the open list, live-rendering the row it lands on
// into the real command line. The cycle includes the no-selection slot:
// stepping past either end reverts to the typed baseline, so circling the
// list and letting go leaves the line exactly as typed.
func (s *Surface) moveSuggestion(delta int) {
	count := len(s.suggestions)
	if count == 0 {
		return
	}
	// The cycle runs [-1, 0, …, count-1]: count+1 slots.
	next := s.suggestionIndex + delta
	if next >= count {
		next = noSelection
	} else if next < noSelection {
		next = count - 1
	}
	s.suggestionIndex = next
	snapshot := s.session.Snapshot()
	if next == noSelection {
		s.revertPreview()
		return
	}
	text := s.suggestions[next].Text
	if text == "" {
		return
	}
	if !s.previewActive {
		// The baseline is the line as typed before any preview touched it:
		// what Escape and the no-selection slot must be able to restore. The
		// head is the part left of the cursor — the half of the baseline the
		// preview chain rewrites, and the half the revert writes back.
		s.previewBaseline = snapshot.Input
		s.previewBaselineHead = string([]rune(snapshot.Input)[:snapshot.InputCursor])
		s.previewActive = true
	}
	line, cursorAfter, err := s.writeLivePreview(snapshot.Input, snapshot.InputCursor, text)
	if err != nil {
		// Nothing reached the shell — the row is not writable, as a
		// multi-line candidate without bracketed paste. The selection still
		// lands on it, but the line keeps whatever the last successful write
		// left and the preview bookkeeping stays valid for it: cycling on
		// writes from the unchanged line, and the no-selection slot and
		// Escape still restore the baseline exactly.
		s.redraw()
		return
	}
	s.previewLine = line
	s.previewCursor = cursorAfter
	s.ownWriteLine = line
	s.redraw()
}

// revertPreview restores the typed baseline over whatever the live preview
// last wrote. It is the no-op the user expects when nothing was previewed.
func (s *Surface) revertPreview() {
	if !s.previewActive {
		return
	}
	snapshot := s.session.Snapshot()
	if snapshot.Input != s.previewBaseline {
		// Reverting rewrites the line only when the line and cursor are still
		// exactly what the last preview write left: a line the user edited
		// since is theirs, and a cursor they moved re-points every erase.
		if snapshot.Input == s.previewLine && snapshot.InputCursor == s.previewCursor {
			// Erase everything the preview chain left of the cursor and write
			// back the baseline's head. Preview writes never touch the line
			// right of the cursor, so the suffix the line still holds is the
			// baseline's own: head plus suffix reconstructs the baseline
			// exactly, and the cursor lands at the end of the inserted head —
			// the baseline's cursor position — however many rows the chain
			// visited and whichever prompt shape measured the erasures.
			if err := s.insertTextReplacing(snapshot.InputCursor, s.previewBaselineHead); err == nil {
				s.ownWriteLine = s.previewBaseline
			}
		}
	}
	s.previewActive = false
	s.previewLine = ""
	s.redraw()
}

// acceptGhost writes the ghost hint's completion tail: the rest of the first
// row beyond what is already typed. It never erases — the hint continues the
// line or it is not a hint — so no baseline handling is needed, and the typed
// text is never at risk. After the write the line is the full row, so the
// rows recompute against it on the next frame. The key path checks the
// end-of-line guard before consuming Right; the same check here is the
// belt-and-braces half, in case the line moved between the key event and the
// write.
func (s *Surface) acceptGhost() {
	text := s.suggestions[0].Text
	if text == "" {
		return
	}
	snapshot := s.session.Snapshot()
	input := snapshot.Input
	if !strings.HasPrefix(text, input) || snapshot.InputCursor != len([]rune(input)) {
		// The hint no longer continues the line (the shell repainted, or the
		// cursor moved mid-line): there is no tail to write.
		return
	}
	if err := s.insertTextReplacing(0, text[len(input):]); err != nil {
		s.redraw()
		return
	}
	s.suggestions = nil
	s.suggestionIndex = noSelection
	s.showGhost = false
	s.redraw()
}

func (s *Surface) dismissSuggestions() {
	s.suggestions = nil
	s.suggestionIndex = noSelection
	s.suggestionDismissed = true
	s.showAutocomplete = false
	s.showGhost = false
	s.redraw()
}

// acceptSuggestionClick commits a clicked row into the line: unlike the
// arrow-driven live preview, a click is a decisive pick, so the write is not
// a revertable preview — the line becomes the candidate, the list closes, and
// editing continues from there. The write itself is the same seam, so a
// refusal (an unsafe multi-line row without bracketed paste) leaves the typed
// text untouched and the rows visible.
func (s *Surface) acceptSuggestionClick(index int) {
	if index < 0 || index >= len(s.suggestions) {
		return
	}
	text := s.suggestions[index].Text
	if text == "" {
		return
	}
	snapshot := s.session.Snapshot()
	eraseCount := autocomplete.AcceptEraseCount(snapshot.Input, snapshot.InputCursor, s.programPrompt)
	if err := s.insertTextReplacing(eraseCount, text); err != nil {
		s.redraw()
		return
	}
	// The click's own write must not read as a user edit when it lands in the
	// mirror a frame later: against the pre-write input the change would look
	// like typing, the dismissal would expire, and the popup would reopen with
	// rows answering the candidate it just committed. The write's effect on
	// the line is deterministic — eraseCount runes left of the cursor
	// replaced by the candidate — so the recognition can be exact, the same
	// one the live preview's own writes get.
	inputRunes := []rune(snapshot.Input)
	s.ownWriteLine = string(inputRunes[:snapshot.InputCursor-eraseCount]) + text + string(inputRunes[snapshot.InputCursor:])
	s.suggestions = nil
	s.suggestionIndex = noSelection
	s.suggestionDismissed = true
	s.showAutocomplete = false
	s.showGhost = false
	s.previewActive = false
	s.redraw()
}

// SuggestionRows is the popup rows the surface currently shows. Tests and the
// self-test use it to assert what the arrows would preview.
func (s *Surface) SuggestionRows() []autocomplete.Suggestion {
	return append([]autocomplete.Suggestion(nil), s.suggestions...)
}

// InvalidateSuggestions drops the popup rows after the shared journal changed.
// The next frame recomputes them from the reloaded service, so a deleted,
// pruned, or cleared command cannot stay visible or be previewed.
func (s *Surface) InvalidateSuggestions() {
	s.suggestions = nil
	s.suggestionIndex = noSelection
	s.showAutocomplete = false
	s.showGhost = false
	s.redraw()
}
