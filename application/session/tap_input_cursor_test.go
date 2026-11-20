package session

import (
	"testing"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// The tap mirrors the line editor's cursor so an accepted suggestion can
// replace only the text to the left of it. These keys are the ones readline,
// zle, and PSReadLine share.
func TestTapTracksTheLineEditorCursor(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("git status"), tapAt(0))
	assertCursor(t, tap, "git status", 10)

	// Left arrow twice, then typing inserts at the cursor rather than at the
	// end.
	tap.handleInput([]byte("\x1b[D\x1b[D"), tapAt(0))
	assertCursor(t, tap, "git status", 8)
	tap.handleInput([]byte("X"), tapAt(0))
	assertCursor(t, tap, "git statXus", 9)

	// Home and Delete remove the first rune; End returns to the line's end.
	tap.handleInput([]byte("\x1b[H"), tapAt(0))
	assertCursor(t, tap, "git statXus", 0)
	tap.handleInput([]byte("\x1b[3~"), tapAt(0))
	assertCursor(t, tap, "it statXus", 0)
	tap.handleInput([]byte("\x1b[4~"), tapAt(0))
	assertCursor(t, tap, "it statXus", 10)

	// Backspace removes the rune to the left of the cursor, not the last rune
	// of the line.
	tap.handleInput([]byte("\x1b[D\x1b[D"), tapAt(0))
	assertCursor(t, tap, "it statXus", 8)
	tap.handleInput([]byte{0x7f}, tapAt(0))
	assertCursor(t, tap, "it status", 7)

	// Ctrl+A/Ctrl+E and Ctrl+K kill the right side only.
	tap.handleInput([]byte{0x01}, tapAt(0))
	assertCursor(t, tap, "it status", 0)
	tap.handleInput([]byte("\x1b[C\x1b[C"), tapAt(0))
	assertCursor(t, tap, "it status", 2)
	tap.handleInput([]byte{0x0b}, tapAt(0))
	assertCursor(t, tap, "it", 2)
	tap.handleInput([]byte{0x05}, tapAt(0))
	assertCursor(t, tap, "it", 2)

	// Ctrl+U kills the left side only, leaving the suffix.
	tap.handleInput([]byte("\x1b[D"), tapAt(0))
	tap.handleInput([]byte{0x15}, tapAt(0))
	assertCursor(t, tap, "t", 0)

	// Enter submits and resets the cursor with the line.
	tap.handleInput([]byte("ok\r"), tapAt(0))
	state := tap.state(tapAt(0))
	if state.Input != "" || state.InputCursor != 0 {
		t.Fatalf("after Enter input = %q cursor = %d, want empty", state.Input, state.InputCursor)
	}
}

func TestTapCursorClampsAtBothEnds(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("ab"), tapAt(0))
	tap.handleInput([]byte("\x1b[D\x1b[D\x1b[D"), tapAt(0))
	assertCursor(t, tap, "ab", 0)
	tap.handleInput([]byte("\x1b[C\x1b[C\x1b[C"), tapAt(0))
	assertCursor(t, tap, "ab", 2)
}

func assertCursor(t *testing.T, tap *terminalTap, wantInput string, wantCursor int) {
	t.Helper()
	state := tap.state(tapAt(0))
	if state.Input != wantInput || state.InputCursor != wantCursor {
		t.Fatalf("input = %q cursor = %d, want %q @ %d",
			state.Input, state.InputCursor, wantInput, wantCursor)
	}
}
