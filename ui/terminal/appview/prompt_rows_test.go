package appview

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/ui/terminal"
)

// PromptRows is the block's prompt: the shell's own drawing, captured at the
// command's OSC 133;C mark. The rows run from the B boundary through the echoed
// input, wrapped just as the grid wrapped it; the blank line accept-line opened
// for the command's output is not part of the prompt.
func TestPromptRowsCapturesTheShellsPromptAndWrappedInput(t *testing.T) {
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: 4096})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 20, Rows: 8}
	term.Resize(size.Cols, size.Rows)

	// B is emitted in precmd before the prompt is drawn, so the cursor there is
	// the prompt's first row.
	term.Feed([]byte("\x1b]133;A\x07\x1b]133;B\x07"))
	promptRow, _, ok := CursorReplayRow(term.Snapshot(), size)
	if !ok {
		t.Fatal("the prompt row was not readable")
	}
	// A coloured prompt and 45 input cells at 20 columns: three grid rows.
	input := strings.Repeat("x", 45)
	term.Feed([]byte("\x1b[31mP> \x1b[0m\x1b[34m" + input + "\x1b[0m\r\n"))

	rows, ok := PromptRows(term.Snapshot(), size, promptRow, nil)
	if !ok {
		t.Fatal("the prompt rows were not captured")
	}
	want := []string{
		"P> " + input[:17],
		input[17:37],
		input[37:],
	}
	if len(rows) != len(want) {
		t.Fatalf("captured %d prompt rows, want %d: %#v", len(rows), len(want), rows)
	}
	for i, row := range rows {
		if got := rowTextOf(row); got != want[i] {
			t.Errorf("prompt row %d = %q, want %q", i, got, want[i])
		}
	}
	// The shell's own colours are kept, not replaced by the UI theme's.
	first := rows[0]
	if len(first) < 2 || first[0].Color == (ui.Color{}) || first[1].Color == (ui.Color{}) ||
		first[0].Color == first[1].Color {
		t.Errorf("the prompt's coloured runs were not preserved: %#v", first)
	}
}

// A prompt whose first rows scrolled out of the replay cap still yields the
// rows that are there, from the top; an unavailable capture reports false so
// the caller falls back to the typed line instead of drawing nothing.
func TestPromptRowsReportsWhenTheRowIsNotInTheReplay(t *testing.T) {
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: 4096})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 20, Rows: 4}
	term.Resize(size.Cols, size.Rows)
	term.Feed([]byte("prompt> echo hi"))

	if _, ok := PromptRows(nil, size, 0, nil); ok {
		t.Error("an empty snapshot produced prompt rows")
	}
	if _, ok := PromptRows(term.Snapshot(), size, 99, nil); ok {
		t.Error("a prompt row past the replay produced prompt rows")
	}
}
