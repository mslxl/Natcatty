package session

// Tests for the tap's arbitration of the keystroke mirror against the logical
// line read from the screen. The rendered line carries the prompt chrome, so
// the tap captures the prompt prefix as it types — the line is the prompt plus
// a leading prefix of the mirror, the echo so far — and strips that prefix when
// a history recall leaves the mirror empty or lost.
//
// The readers here model the screen faithfully: the line is the prompt plus
// whatever input the line editor is showing, exactly as Terminal.SubmittedLine
// returns it.

import (
	"testing"
	"time"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// screenReader is a model of the rendered input line: a fixed prompt and the
// input the line editor currently shows. line() is the reader the tap
// consults, answering prompt+input with the cursor at the end.
type screenReader struct {
	prompt string
	input  string
	ok     bool
}

func (s *screenReader) line() func() (string, int, bool) {
	return func() (string, int, bool) {
		line := s.prompt + s.input
		return line, len([]rune(line)), s.ok
	}
}

// typeIn drives printable input into the tap while the model echoes it, as the
// shell does: each typed character joins the editor's line. keystrokes is the
// literal text typed.
func typeIn(tap *terminalTap, screen *screenReader, text string, at time.Time) {
	for _, r := range text {
		tap.handleInput([]byte(string(r)), at)
		screen.input += string(r)
	}
}

func TestTapRecallRecordsTheRenderedLine(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	// Type a first command so the prompt prefix is captured, run it, then the
	// line editor repaints a recalled command that the mirror never sees.
	typeIn(tap, screen, "echo seed", tapAt(0))
	if _, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(time.Second)); !started {
		t.Fatal("the seed command did not start a capture")
	}
	if !tap.promptPrefixSet || tap.promptPrefix != "% " {
		t.Fatalf("prompt prefix = %q (set %v), want %% ", tap.promptPrefix, tap.promptPrefixSet)
	}

	// Up recall: the line editor swaps the buffer invisibly. The mirror sees
	// only the arrow (inputLost); the screen shows the recalled command whole.
	screen.input = ""
	tap.handleInput([]byte("\x1b[A"), tapAt(2*time.Second))
	screen.input = "git status --short"
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(3*time.Second))
	if !started {
		t.Fatal("a recall with a live screen reader did not start a capture")
	}
	if got := tap.state(tapAt(3 * time.Second)).Command.Text; got != "git status --short" {
		t.Fatalf("command = %q, want the rendered recalled command", got)
	}
}

func TestTapCtrlRRecordsTheRenderedLine(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	typeIn(tap, screen, "echo seed", tapAt(0))
	tap.handleInput([]byte("\r"), tapAt(time.Second))

	// Ctrl+R: the reverse search rewrites the buffer from history. The mirror
	// is lost; the screen shows the found command.
	screen.input = ""
	tap.handleInput([]byte("\x12"), tapAt(2*time.Second))
	screen.input = "echo reverse-found"
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(3*time.Second))
	if !started {
		t.Fatal("a Ctrl+R line with a live reader did not start a capture")
	}
	if got := tap.state(tapAt(3 * time.Second)).Command.Text; got != "echo reverse-found" {
		t.Fatalf("command = %q", got)
	}
}

func TestTapRecallWithoutCapturedPrefixKeepsTheDiscard(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	// Nothing was ever typed, so the prompt prefix is unknown. A recall as the
	// very first action cannot be split from its prompt, and the discard stands
	// rather than recording chrome as a command.
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	screen.input = "git status"
	tap.handleInput([]byte("\x1b[A"), tapAt(0))
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(time.Second))
	if started {
		t.Fatal("a recall with no captured prompt prefix started a capture")
	}
}

func TestTapReliableBufferWinsOverTheReader(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	// Echo lag on a slow link: the mirror is reliable and ahead, so it wins even
	// though the screen trails it. The echo in the model lags deliberately: it
	// shows only part of what was typed.
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	tap.handleInput([]byte("git status"), tapAt(0))
	screen.input = "git" // echo lags behind the full "git status"
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(time.Second))
	if !started {
		t.Fatal("plain typing did not start a capture")
	}
	if got := tap.state(tapAt(time.Second)).Command.Text; got != "git status" {
		t.Fatalf("command = %q, want the typed buffer (echo lag)", got)
	}
}

func TestTapEmptyReaderKeepsTheDiscard(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	typeIn(tap, screen, "echo seed", tapAt(0))
	tap.handleInput([]byte("\r"), tapAt(time.Second))

	// No usable screen input (no integration, or a program running): the recall
	// discard stands.
	screen.input = ""
	screen.ok = false
	tap.handleInput([]byte("\x1b[A"), tapAt(2*time.Second))
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(3*time.Second))
	if started {
		t.Fatal("a recall with no usable screen input started a capture of the hybrid")
	}
}

func TestTapNoReaderKeepsTheDiscard(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	// No reader registered at all (no live view): the discard stands.
	tap.handleInput([]byte("git st"), tapAt(0))
	tap.handleInput([]byte("\x1b[A"), tapAt(time.Second))
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(2*time.Second))
	if started {
		t.Fatal("a recall with no reader started a capture of the hybrid")
	}
}

func TestTapBarePromptKeepsTheDiscard(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	typeIn(tap, screen, "echo seed", tapAt(0))
	tap.handleInput([]byte("\r"), tapAt(time.Second))

	// The rendered line is the bare prompt — empty input, the same shape a
	// no-echo password prompt presents. Never record it, and never surface the
	// secret the mirror may still hold.
	screen.input = ""
	tap.handleInput([]byte("\x1b[A"), tapAt(2*time.Second))
	// The recall painted nothing: the line is the prompt alone.
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(3*time.Second))
	if started {
		t.Fatal("a bare-prompt rendered line started a capture")
	}
}
