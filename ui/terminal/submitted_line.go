package terminal

import (
	"strings"

	"github.com/mslxl/natcatty/ui/terminal/internal/vt"
)

// logicalLine is the cursor's prompt row joined with its continuation rows:
// the full text, and the cursor's character (rune) offset within it. It is
// what the shell actually shows on the input line, prompt chrome included.
type logicalLine struct {
	text         string
	cursorOffset int
}

// SubmittedLine returns the logical input line the cursor is on, as the shell
// actually shows it, and the cursor's character (rune) offset within that
// line. It is the capture fallback's witness of what Enter is about to
// submit: after a history recall or a reverse search the line editor repaints
// the full command on the prompt, so this text is the truth that a keystroke
// mirror cannot see. No prompt/input split happens here — trimming the prompt
// away is the capture tap's reconciliation, which subtracts the prompt prefix
// it captures at the first keystroke of a fresh line (the one moment echo is
// provably absent).
//
// ok is false when the cursor is not on a prompt row (no shell integration, a
// program running, the alternate screen, or the view scrolled into the
// scrollback): then there is no trustworthy line to read. t.mu is taken for
// the read; the reader goroutine holds it only for the brief output write,
// never across input, so the capture goroutine cannot deadlock against it.
func (t *Terminal) SubmittedLine() (line string, cursorOffset int, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	term := t.term
	if term == nil || term.AltScreen() || !term.AtBottom() {
		return "", 0, false
	}
	l, ok := readLogicalLine(term)
	if !ok {
		return "", 0, false
	}
	return l.text, l.cursorOffset, true
}

// readLogicalLine assembles the cursor's logical input line: the prompt row
// ghostty marks with OSC 133 joined with its continuation rows, upward and
// downward, so a long input wrapped onto several rows reassembles whole.
// Soft-wrapped rows join without a newline; hard line breaks in a multi-line
// input keep one. The cursor offset counts runes, matching how the capture's
// keystroke mirror counts. ok is false when the cursor is not on a prompt
// row. The caller holds the terminal's mutex.
func readLogicalLine(term *vt.Terminal) (logicalLine, bool) {
	cx, cy := term.Cursor()
	if p, _ := term.Row(cy); p == vt.NoPrompt {
		return logicalLine{}, false
	}
	_, rows := term.Size()

	// The logical line spans the prompt's continuation rows around the cursor:
	// up while the row above is a continuation, down while the row below is.
	top := cy
	for top > 0 {
		if p, _ := term.Row(top); p != vt.PromptContinuation {
			break
		}
		top--
	}
	bottom := cy
	for bottom+1 < rows {
		if p, _ := term.Row(bottom + 1); p != vt.PromptContinuation {
			break
		}
		bottom++
	}

	// Join the rows. A soft-wrapped row's text runs to the edge and joins with
	// no newline; a hard line break in a multi-line input keeps one.
	var b strings.Builder
	runesBefore := 0
	cursorOffset := 0
	for y := top; y <= bottom; y++ {
		text, cols := term.RowText(y)
		_, wrap := term.Row(y)
		rowText := logicalRowText(text, cols, wrap, y == cy, cx)
		if y == cy {
			cursorOffset = runesBefore + cursorCharsBefore(text, cols, cx)
		}
		b.WriteString(rowText)
		runesBefore += len([]rune(rowText))
		if !wrap && y < bottom {
			b.WriteByte('\n')
			runesBefore++
		}
	}
	return logicalLine{text: b.String(), cursorOffset: cursorOffset}, true
}

// logicalRowText returns the meaningful text of one row. RowText pads a row
// out to the width with blanks; those are dropped, except on the cursor's row
// where blanks up to the cursor are real input (spaces typed there). A
// soft-wrapped row keeps its full width because its text continues onto the
// next row. keep counts runes, never cells: cx is a cell column, and a wide
// character before the cursor takes two cells for one rune, so the trimming
// must compare rune positions through the column table, not cx itself.
func logicalRowText(text []rune, cols []int, wrap bool, isCursorRow bool, cx int) string {
	if len(text) == 0 {
		return ""
	}
	end := len(text)
	if !wrap {
		keep := 0
		if isCursorRow {
			keep = cursorCharsBefore(text, cols, cx)
		}
		for end > keep && text[end-1] == ' ' {
			end--
		}
	}
	return string(text[:end])
}

// cursorCharsBefore counts the row's characters whose column is before cx —
// the cursor's character offset into this row.
func cursorCharsBefore(text []rune, cols []int, cx int) int {
	n := 0
	for i, c := range cols {
		if i >= len(text) {
			break
		}
		if c < cx {
			n++
		}
	}
	return n
}
