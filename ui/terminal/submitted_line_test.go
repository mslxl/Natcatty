package terminal

// Tests for Terminal.SubmittedLine, the accessor the capture fallback uses to
// read the line the shell actually shows at Enter. The line is assembled from
// ghostty's semantic rows: the cursor's prompt row and its continuation rows,
// with no prompt/input split — that reconciliation is the tap's, which knows
// the typed buffer.

import (
	"strings"
	"testing"
)

func submittedLineHarness(t *testing.T, cols, rows int) *Terminal {
	t.Helper()
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), ShellIntegration: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Close() })
	term.Resize(cols, rows)
	return term
}

func TestSubmittedLineSimplePrompt(t *testing.T) {
	term := submittedLineHarness(t, 80, 24)
	term.Feed([]byte("output\r\n\x1b]133;A\auser@host:~$ echo hello"))

	line, cursor, ok := term.SubmittedLine()
	if !ok {
		t.Fatal("no submitted line on a live prompt")
	}
	if !strings.Contains(line, "echo hello") {
		t.Errorf("line %q does not contain the input", line)
	}
	if !strings.Contains(line, "user@host:~$") {
		t.Errorf("line %q does not contain the prompt (no split here)", line)
	}
	// The cursor is at the end of the input, so its offset within the logical
	// line is the line's full length.
	if cursor != len(strings.TrimRight(line, " ")) {
		t.Errorf("cursor offset = %d, line length %d", cursor, len(strings.TrimRight(line, " ")))
	}
}

func TestSubmittedLineWrappedInput(t *testing.T) {
	term := submittedLineHarness(t, 20, 24)
	// Narrow screen forces the input to wrap onto a continuation row.
	term.Feed([]byte("\x1b]133;A\a$ echo averylongcommandthatwraps"))

	line, _, ok := term.SubmittedLine()
	if !ok {
		t.Fatal("no submitted line on a wrapped prompt")
	}
	if !strings.Contains(line, "echo averylongcommandthatwraps") {
		t.Errorf("wrapped line %q does not reassemble the whole input", line)
	}
	if strings.Contains(line, "\n") {
		// A soft wrap joins without a newline; only a hard line break keeps one.
		t.Errorf("soft-wrapped line %q carries a newline", line)
	}
}

func TestSubmittedLineCursorMidLine(t *testing.T) {
	term := submittedLineHarness(t, 80, 24)
	// Type "echo hello", then move the cursor 5 left (after "echo ").
	term.Feed([]byte("\x1b]133;A\a$ echo hello\x1b[5D"))

	line, cursor, ok := term.SubmittedLine()
	if !ok {
		t.Fatal("no submitted line")
	}
	// The full logical line still holds the whole input, but the cursor offset
	// is 5 short of its end.
	full := len(strings.TrimRight(line, " "))
	if cursor != full-5 {
		t.Errorf("cursor offset = %d, want %d (full %d minus 5)", cursor, full-5, full)
	}
}

func TestSubmittedLineNoPrompt(t *testing.T) {
	term := submittedLineHarness(t, 80, 24)
	// Output with no OSC 133 prompt: the cursor is not on a prompt row.
	term.Feed([]byte("just a program running\r\nmore output"))

	if _, _, ok := term.SubmittedLine(); ok {
		t.Error("a submitted line was reported with no prompt under the cursor")
	}
}

func TestSubmittedLineEmptyInput(t *testing.T) {
	term := submittedLineHarness(t, 80, 24)
	// A fresh prompt with nothing typed yet.
	term.Feed([]byte("output\r\n\x1b]133;A\auser@host:~$ "))

	line, _, ok := term.SubmittedLine()
	if !ok {
		t.Fatal("no submitted line on an empty prompt")
	}
	if strings.TrimSpace(line) != strings.TrimSpace("user@host:~$") {
		t.Errorf("empty-input line = %q, want just the prompt", line)
	}
}

func TestSubmittedLineAltScreen(t *testing.T) {
	term := submittedLineHarness(t, 80, 24)
	term.Feed([]byte("\x1b]133;A\a$ echo hello"))
	// A full-screen program takes the alternate screen: there is no trustworthy
	// input line while it owns the display.
	term.Feed([]byte("\x1b[?1049h"))

	if _, _, ok := term.SubmittedLine(); ok {
		t.Error("a submitted line was reported while the alt screen is active")
	}
}

func TestSubmittedLineWideCharCursorRow(t *testing.T) {
	term := submittedLineHarness(t, 80, 24)
	// A wide character before the cursor occupies two cells for one rune. The
	// padding trim must measure runes through the column table, not the cursor's
	// cell column: counting cells as runes keeps padding blanks that no one typed.
	term.Feed([]byte("\x1b]133;A\a$ echo 世界"))

	line, cursor, ok := term.SubmittedLine()
	if !ok {
		t.Fatal("no submitted line on a wide-char prompt")
	}
	if got := strings.TrimRight(line, " "); got != "$ echo 世界" {
		t.Errorf("line %q, want exactly the prompt and input with no padding", got)
	}
	if line != "$ echo 世界" {
		t.Errorf("line %q keeps padding blanks beyond the input", line)
	}
	if cursor != len([]rune("$ echo 世界")) {
		t.Errorf("cursor offset = %d, want the rune length of the input", cursor)
	}
}
