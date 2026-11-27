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
	"context"
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
	if tap.promptPrefix != "% " {
		t.Fatalf("prompt prefix = %q, want %% ", tap.promptPrefix)
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

func TestTapCtrlRDegradesToTheDiscard(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	typeIn(tap, screen, "echo seed", tapAt(0))
	tap.handleInput([]byte("\r"), tapAt(time.Second))

	// Ctrl+R: at the Enter keystroke the screen still shows the reverse-search
	// mini-buffer, not the accepted command — that is painted only after Enter
	// reaches the shell, which is after the tap reads. The mini-buffer does not
	// start with the prompt chrome, so the strip fails and the discard stands:
	// reverse search cannot be captured at Enter.
	screen.input = ""
	screen.prompt = "bck-i-search: status_"
	tap.handleInput([]byte("\x12"), tapAt(2*time.Second))
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(3*time.Second))
	if started {
		t.Fatal("a Ctrl+R mini-buffer line started a capture")
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

func TestTapCompletionRecordsTheRenderedLine(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	typeIn(tap, screen, "echo seed", tapAt(0))
	tap.handleInput([]byte("\r"), tapAt(time.Second))

	// Tab completion: the user types "git st" and presses Tab; the line editor
	// grows the line to "git status" invisibly. Tab is untrackable, so the
	// mirror is lost exactly as in a recall — and the same rescue answers: the
	// rendered line is the completed truth.
	screen.input = ""
	typeIn(tap, screen, "git st", tapAt(2*time.Second))
	tap.handleInput([]byte("\t"), tapAt(3*time.Second))
	screen.input = "git status" // the completion repaints the line
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(4*time.Second))
	if !started {
		t.Fatal("a completed line did not start a capture")
	}
	if got := tap.state(tapAt(4 * time.Second)).Command.Text; got != "git status" {
		t.Fatalf("command = %q, want the completed line the editor shows", got)
	}
}

func TestTapTabInvalidatesTheMirror(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	// Even when the shell answers Tab with nothing — an ambiguous completion,
	// a bell — the mirror is lost: the tap cannot know that nothing changed,
	// so the capture degrades to the discard where no screen can prove the
	// line. Without a reader the safest answer is no row.
	tap.handleInput([]byte("git st"), tapAt(0))
	tap.handleInput([]byte("\t"), tapAt(time.Second))
	if !tap.inputLost {
		t.Fatal("Tab did not invalidate the mirror")
	}
}

func TestTapEraseEchoInFlightKeepsTheMirror(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	// Type "gits", erase the s, and press Enter fast on a slow link: the erase
	// echo is still in flight, so the screen still shows the pre-erasure line.
	// No keystroke on this line was untrackable, so the mirror is reliable and
	// wins outright: "git" is what runs, not the stale "gits" — with no
	// fragment guessing involved, since every invisible edit invalidates.
	screen.input = ""
	typeIn(tap, screen, "gits", tapAt(0))
	tap.handleInput([]byte{0x7f}, tapAt(time.Second))
	screen.input = "gits" // erase echo has not landed yet
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(2*time.Second))
	if !started {
		t.Fatal("an erased line did not start a capture")
	}
	if got := tap.state(tapAt(2 * time.Second)).Command.Text; got != "git" {
		t.Fatalf("command = %q, want the erased mirror, not the stale echo", got)
	}
}

func TestTapEchoLagKeepsTheMirror(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	// Genuine echo lag: the screen trails the mirror, and the mirror wins —
	// it is the more current witness.
	typeIn(tap, screen, "echo seed", tapAt(0))
	screen.input = "echo" // only part of the echo has landed
	_, _, started, _, _ := tap.handleInput([]byte("\r"), tapAt(time.Second))
	if !started {
		t.Fatal("plain typing did not start a capture")
	}
	if got := tap.state(tapAt(time.Second)).Command.Text; got != "echo seed" {
		t.Fatalf("command = %q, want the typed buffer (echo lag)", got)
	}
}

func TestTapErasedAndRetypedLineDoesNotCaptureResidueAsChrome(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	typeIn(tap, screen, "echo seed", tapAt(0))
	tap.handleInput([]byte("\r"), tapAt(time.Second))

	// Type "git", erase it back to empty, then retype. On a slow link the erase
	// echo is still in flight when the mirror empties, so the screen can show
	// the half-erased line. The line is no longer pristine, so no new capture
	// happens: the first capture ("% ") from the seed line stands, and the
	// residue "% g" never becomes chrome.
	screen.input = ""
	for _, r := range "git" {
		tap.handleInput([]byte(string(r)), tapAt(2*time.Second))
		screen.input += string(r)
	}
	for i := 0; i < len("git"); i++ {
		tap.handleInput([]byte{0x7f}, tapAt(3*time.Second))
	}
	screen.input = "g" // erase echo still in flight: a half-erased line
	if tap.promptPrefix != "% " {
		t.Fatalf("prompt prefix = %q after erasing, want the original %% ", tap.promptPrefix)
	}
	tap.handleInput([]byte("x"), tapAt(4*time.Second))
	if tap.promptPrefix != "% " {
		t.Fatalf("prompt prefix = %q after retyping, want the original %% ", tap.promptPrefix)
	}
}

func TestTapDeliveredCopyNeverConsultsTheScreen(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	typeIn(tap, screen, "echo seed", tapAt(0))
	tap.handleInput([]byte("\r"), tapAt(time.Second))

	// A broadcast-delivered copy on a target session: its keystroke timing is
	// not the user's, so the reader stays out of its path entirely — no prompt
	// capture at its first byte, no arbitration at its Enter. The mirror still
	// captures the copy itself, as before the fallback existed.
	screen.input = "unrelated target line"
	if _, _, started, _, _ := tap.handleInputProvenance([]byte("uptime\r"), tapAt(2*time.Second), false); !started {
		t.Fatal("a delivered copy did not start a capture")
	}
	if got := tap.state(tapAt(2 * time.Second)).Command.Text; got != "uptime" {
		t.Fatalf("command = %q, want the delivered copy verbatim", got)
	}
}

func TestTapDeliveredCopyKeepsTheRecallDiscard(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	screen := &screenReader{prompt: "% ", ok: true}
	tap.setSubmittedInputReader(screen.line())
	typeIn(tap, screen, "echo seed", tapAt(0))
	tap.handleInput([]byte("\r"), tapAt(time.Second))

	// The user recalls on the target (mirror lost), then a broadcast copy's
	// Enter arrives. The screen could prove the line — but the copy is not the
	// user's keystrokes, so the discard stands rather than read a moment that
	// has nothing to do with the delivery.
	screen.input = ""
	tap.handleInput([]byte("\x1b[A"), tapAt(2*time.Second))
	screen.input = "git status"
	_, _, started, _, _ := tap.handleInputProvenance([]byte("\r"), tapAt(3*time.Second), false)
	if started {
		t.Fatal("a delivered Enter over a recalled line consulted the screen")
	}
}

func TestSessionSubmittedInputReaderReachesTheTap(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory, WithClipboardPolicy(func() domainterminal.ClipboardAccess {
		return domainterminal.ClipboardWriteOnly
	}))
	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal, Label: "host"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// The reader the surface registers is the one the tap invokes: a recall
	// typed on the session captures the line the reader proves. The model
	// echoes as the shell does, one byte at a time, so the first keystroke's
	// prompt capture sees the bare prompt.
	screen := &screenReader{prompt: "% ", ok: true}
	session.SetSubmittedInputReader(screen.line())
	writeEchoing := func(text string) {
		t.Helper()
		for _, r := range text {
			if _, err := session.TerminalConn().Write([]byte(string(r))); err != nil {
				t.Fatal(err)
			}
			screen.input += string(r)
		}
	}
	writeEchoing("echo seed")
	if _, err := session.TerminalConn().Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	screen.input = ""
	if _, err := session.TerminalConn().Write([]byte("\x1b[A")); err != nil {
		t.Fatal(err)
	}
	screen.input = "git status"
	if _, err := session.TerminalConn().Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the recalled capture starts", func() bool {
		return session.Snapshot().Command.Text == "git status"
	})

	// Detachment clears the reader: a later recall degrades to the discard
	// instead of reading a screen the user is no longer typing on.
	session.SetSubmittedInputReader(nil)
	if _, err := session.TerminalConn().Write([]byte("\x1b[A")); err != nil {
		t.Fatal(err)
	}
	before := session.Snapshot().Command.Text
	if _, err := session.TerminalConn().Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := session.Snapshot().Command.Text; got != before && got != "" {
			t.Fatalf("a recall with no reader captured %q", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
