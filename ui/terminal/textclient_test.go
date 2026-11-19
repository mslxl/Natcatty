package terminal

// These are the text-input client's contract tests. They drive the same
// ui.TextInputClient the desktop and the phones use, through the framework's
// headless tester where the host would, and call the client directly where
// a platform's input method is what would call it.

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// maxOffset is the length a native length query asks for.
func maxOffset() int { return int(^uint(0) >> 1) }

// newClientTerm makes a terminal shown in a tester with its client attached
// and focused.
func newClientTerm(t *testing.T) (*Terminal, *pipe, *ui.Tester) {
	t.Helper()
	loadLib(t)
	conn := newPipe()
	term, err := New(Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Close() })
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus() }, 400, 200)
	return term, conn, tt
}

// wholeDoc returns the client's whole document and the range it covers.
func wholeDoc(t *testing.T, term *Terminal) (string, ui.TextInputRange) {
	t.Helper()
	return term.v.TextForRange(ui.TextInputRange{End: maxOffset()})
}

func TestTerminalCompositionNotSent(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	term.Feed([]byte("abc"))
	tt.Frame()
	tt.Compose("にほ", 2)
	if got := conn.take(term); got != "" {
		t.Fatalf("composing sent %q", got)
	}
	// It shows (the paint path draws v.preedit at the caret) and is marked.
	if term.v.preedit != "にほ" || term.v.preeditCaret != 2 {
		t.Fatalf("composition = %q at %d", term.v.preedit, term.v.preeditCaret)
	}
	if _, ok := term.v.MarkedRange(); !ok {
		t.Fatal("the composition is not marked")
	}
	tt.Type("にほ")
	if got := conn.take(term); got != "にほ" {
		t.Fatalf("committing sent %q", got)
	}
	tt.Frame()
	if got := conn.take(term); got != "" {
		t.Fatalf("the commit was sent again: %q", got)
	}
	if term.v.preedit != "" {
		t.Fatalf("the composition survived its commit: %q", term.v.preedit)
	}
}

func TestTerminalUnmarkCommitsOnce(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	tt.Compose("ni", 2)
	term.v.UnmarkText()
	if got := conn.take(term); got != "ni" {
		t.Fatalf("unmarking committed %q", got)
	}
	term.v.UnmarkText()
	if got := conn.take(term); got != "" {
		t.Fatalf("the unmark committed again: %q", got)
	}
}

func TestTerminalCompositionCancel(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	tt.Compose("ni", 2)
	// A cancelled composition: the mark is cleared first, then unmarked.
	tt.Compose("", 0)
	term.v.UnmarkText()
	if got := conn.take(term); got != "" {
		t.Fatalf("cancelling sent %q", got)
	}
	if term.v.preedit != "" {
		t.Fatalf("the cancelled composition is %q", term.v.preedit)
	}
	// Typing after the cancellation sends only what is typed.
	tt.Type("x")
	if got := conn.take(term); got != "x" {
		t.Fatalf("typing after a cancel sent %q", got)
	}
}

func TestTerminalDeleteWhileComposingEditsPreedit(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	tt.Compose("abc", 3)
	mark, ok := term.v.MarkedRange()
	if !ok {
		t.Fatal("the composition is not marked")
	}
	// An input method's delete inside its composition: the last rune goes,
	// and the shell gets nothing.
	term.v.ReplaceText(&ui.TextInputRange{Start: mark.End - 1, End: mark.End}, "")
	if got := conn.take(term); got != "" {
		t.Fatalf("deleting in the composition sent %q", got)
	}
	if term.v.preedit != "ab" {
		t.Fatalf("the composition after a delete is %q", term.v.preedit)
	}
	// The Edit menu's Delete does the same while composing.
	tt.Command("delete")
	if got := conn.take(term); got != "" {
		t.Fatalf("the delete command in the composition sent %q", got)
	}
	if term.v.preedit != "a" {
		t.Fatalf("the composition after the command is %q", term.v.preedit)
	}
	term.v.UnmarkText()
	if got := conn.take(term); got != "a" {
		t.Fatalf("committing the rest sent %q", got)
	}
	// Outside a composition, Delete is exactly one key for the shell.
	tt.Command("delete")
	if got := conn.take(term); got != "\x7f" {
		t.Fatalf("the delete command outside a composition sent %q", got)
	}
}

func TestTerminalDeleteAroundMapsToKeys(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	term.Feed([]byte("abcdef"))
	tt.Frame()
	sel := term.v.Selection().Range
	if sel.Start != 6 {
		t.Fatalf("the caret is at %d, want 6", sel.Start)
	}
	// Text before the caret becomes that many Backspaces.
	term.v.ReplaceText(&ui.TextInputRange{Start: 0, End: 2}, "")
	if got := conn.take(term); got != "\x7f\x7f" {
		t.Fatalf("deleting two cells before the caret sent %q", got)
	}
	// Text after the caret becomes Deletes; move the shell's cursor back.
	term.Feed([]byte("\x1b[6D"))
	waitFor(t, tt, "the cursor to move home", func() bool {
		return term.v.Selection().Range.Start == 0
	})
	term.v.ReplaceText(&ui.TextInputRange{Start: 0, End: 1}, "")
	if got := conn.take(term); got != "\x1b[3~" {
		t.Fatalf("deleting a cell after the caret sent %q", got)
	}
	// The cap keeps one input method delete from sending a screen of keys.
	term.Feed([]byte("\x1b[6C"))
	waitFor(t, tt, "the cursor to move to the end", func() bool {
		return term.v.Selection().Range.Start == 6
	})
	term.v.ReplaceText(&ui.TextInputRange{Start: 0, End: 6}, "")
	if got := conn.take(term); got != strings.Repeat("\x7f", 6) {
		t.Fatalf("deleting six cells sent %q", got)
	}
	wide := strings.Repeat("x", 100)
	term.Feed([]byte("\r\n" + wide))
	waitFor(t, tt, "the wide line", func() bool {
		doc, _ := wholeDoc(t, term)
		return strings.Count(doc, "x") == len(wide)
	})
	tt.Frame() // the caret's cell follows the output on the next build
	term.v.ReplaceText(&ui.TextInputRange{Start: 0, End: 200}, "")
	if got := conn.take(term); len(got) != maxDeleteKeys {
		t.Fatalf("a delete of 200 cells sent %d keys (%q), want the cap %d", len(got), got[:min(len(got), 40)], maxDeleteKeys)
	}
}

func TestTerminalDocumentIncludesPreedit(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	term.Feed([]byte("hello"))
	tt.Frame()
	sel := term.v.Selection().Range
	if sel.Start != 5 {
		t.Fatalf("the caret is at %d, want 5", sel.Start)
	}
	tt.Compose("ni", 2)
	doc, actual := wholeDoc(t, term)
	if !strings.HasPrefix(doc, "helloni\n") || actual.End != ui.UTF16Len(doc) {
		t.Fatalf("the document is %q in %+v", doc, actual)
	}
	mark, ok := term.v.MarkedRange()
	if !ok || mark != (ui.TextInputRange{Start: 5, End: 7}) {
		t.Fatalf("the marked range is %+v (%v), want {5 7}", mark, ok)
	}
	if s := term.v.Selection().Range; s.Start != 7 || s.End != 7 {
		t.Fatalf("the selection is %+v, want the caret inside the composition", s)
	}
	// Output still arrives while the composition shows: the composition
	// moves with the caret and its range stays valid.
	term.Feed([]byte("X"))
	waitFor(t, tt, "the output", func() bool {
		doc, _ := wholeDoc(t, term)
		return strings.HasPrefix(doc, "helloXni\n")
	})
	if mark, ok := term.v.MarkedRange(); !ok || mark != (ui.TextInputRange{Start: 6, End: 8}) {
		t.Fatalf("after output the marked range is %+v (%v), want {6 8}", mark, ok)
	}
	tt.Type("ni")
	if got := conn.take(term); got != "ni" {
		t.Fatalf("committing after output sent %q", got)
	}
}

func TestTerminalMarkedRangeMatchesPreedit(t *testing.T) {
	term, _, tt := newClientTerm(t)
	term.Feed([]byte("ab"))
	tt.Frame()
	tt.Compose("にほ", 1)
	mark, ok := term.v.MarkedRange()
	if !ok || mark != (ui.TextInputRange{Start: 2, End: 4}) {
		t.Fatalf("the marked range is %+v (%v), want the composition at {2 4}", mark, ok)
	}
	// A cancelled composition is no longer marked.
	tt.Compose("", 0)
	if mark, ok := term.v.MarkedRange(); ok {
		t.Fatalf("the cleared composition is still marked at %+v", mark)
	}
}

func TestTerminalTextForRangeLengthAndWindow(t *testing.T) {
	term, _, tt := newClientTerm(t)
	term.Feed([]byte("hello\r\nworld"))
	tt.Frame()
	doc, actual := wholeDoc(t, term)
	if !strings.HasPrefix(doc, "hello\nworld\n") {
		t.Fatalf("the document starts %q", doc)
	}
	if actual.Start != 0 || actual.End != ui.UTF16Len(doc) {
		t.Fatalf("the length query returned %+v for %d units", actual, ui.UTF16Len(doc))
	}
	// Clamping, ordering and negative ends.
	if got, r := term.v.TextForRange(ui.TextInputRange{Start: -5, End: 5}); got != doc[:5] || r != (ui.TextInputRange{Start: 0, End: 5}) {
		t.Fatalf("a negative range returned %q in %+v", got, r)
	}
	if got, r := term.v.TextForRange(ui.TextInputRange{Start: 5, End: 2}); got != doc[2:5] || r != (ui.TextInputRange{Start: 2, End: 5}) {
		t.Fatalf("a reversed range returned %q in %+v", got, r)
	}
	if _, r := term.v.TextForRange(ui.TextInputRange{Start: 0, End: maxOffset()}); r.End != ui.UTF16Len(doc) {
		t.Fatalf("an oversized range returned %+v", r)
	}
	// The window an input method asks for in both directions.
	at := 5
	got, r := term.v.TextForRange(ui.TextInputRange{Start: at - 4096, End: at + 4096})
	if got != doc || r != (ui.TextInputRange{Start: 0, End: ui.UTF16Len(doc)}) {
		t.Fatalf("the window returned %q in %+v", got, r)
	}
	// UTF-16 units, not bytes: an emoji is two units and one rune.
	term.Feed([]byte("\r\n😀"))
	waitFor(t, tt, "the emoji", func() bool {
		doc, _ := wholeDoc(t, term)
		return strings.Contains(doc, "😀")
	})
	doc, _ = wholeDoc(t, term)
	i := strings.Index(doc, "😀")
	if i < 0 {
		t.Fatalf("the emoji is missing from %q", doc)
	}
	off := ui.UTF16Len(doc[:i])
	if got, r := term.v.TextForRange(ui.TextInputRange{Start: off, End: off + 2}); got != "😀" || r != (ui.TextInputRange{Start: off, End: off + 2}) {
		t.Fatalf("the emoji is at %d and returned %q in %+v", off, got, r)
	}
}

func TestTerminalSelectionIsCaret(t *testing.T) {
	term, _, tt := newClientTerm(t)
	term.Feed([]byte("abc"))
	tt.Frame()
	if s := term.v.Selection(); s.Range != (ui.TextInputRange{Start: 3, End: 3}) || s.Reversed {
		t.Fatalf("the selection is %+v, want the caret at 3", s)
	}
	tt.Compose("ni", 1)
	if s := term.v.Selection(); s.Range != (ui.TextInputRange{Start: 4, End: 4}) {
		t.Fatalf("inside the composition the selection is %+v, want {4 4}", s)
	}
}

func TestTerminalReplaceTextCommits(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	tt.Compose("ni", 2)
	term.v.ReplaceText(nil, "你好")
	if got := conn.take(term); got != "你好" {
		t.Fatalf("the commit sent %q", got)
	}
	if term.v.preedit != "" {
		t.Fatalf("the composition survived the commit: %q", term.v.preedit)
	}
	term.v.ReplaceText(nil, "")
	if got := conn.take(term); got != "" {
		t.Fatalf("an empty commit sent %q", got)
	}
	// A host may commit a composition with its marked range as the
	// replacement range.
	tt.Compose("ab", 2)
	mark, _ := term.v.MarkedRange()
	term.v.ReplaceText(&mark, "ab")
	if got := conn.take(term); got != "ab" {
		t.Fatalf("the ranged commit sent %q", got)
	}
}

func TestTerminalReplaceTextIgnoresRangeEdits(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	term.Feed([]byte("screen text"))
	tt.Frame()
	// A non-collapsed range with text: screen cells cannot be rewritten.
	term.v.ReplaceText(&ui.TextInputRange{Start: 0, End: 3}, "zzz")
	if got := conn.take(term); got != "" {
		t.Fatalf("a range edit sent %q", got)
	}
	// Accessibility setValue: MyGo asks for the whole span and replaces it
	// with the new value. It must type nothing.
	_, span := term.v.TextForRange(ui.TextInputRange{End: maxOffset()})
	term.v.ReplaceText(&span, "blocked")
	if got := conn.take(term); got != "" {
		t.Fatalf("an accessibility setValue sent %q", got)
	}
	doc, _ := wholeDoc(t, term)
	if !strings.HasPrefix(doc, "screen text\n") {
		t.Fatalf("the document changed to %q", doc)
	}
	// Text inserted at the caret, as accessibility insert-text does, is
	// typed.
	term.v.ReplaceText(nil, "x")
	if got := conn.take(term); got != "x" {
		t.Fatalf("an insertion sent %q", got)
	}
}

func TestTerminalBoundsFollowCaret(t *testing.T) {
	term, _, tt := newClientTerm(t)
	term.Feed([]byte("abc"))
	tt.Frame()
	want := term.v.caret()
	b, actual, ok := term.v.BoundsForRange(term.v.Selection().Range)
	if !ok || b != want || actual != (ui.TextInputRange{Start: 3, End: 3}) {
		t.Fatalf("the caret's bounds are %+v in %+v (%v), want %+v", b, actual, ok, want)
	}
	x, y := cellCenter(term, 0, 0)
	if b, _, ok := term.v.BoundsForRange(ui.TextInputRange{Start: 0, End: 0}); !ok || !b.Contains(x, y) {
		t.Fatalf("the first cell's bounds are %+v (%v), not around %v, %v", b, ok, x, y)
	}
	tt.Compose("ni", 1)
	compose, _, ok := term.v.BoundsForRange(term.v.Selection().Range)
	if !ok || compose.Y != want.Y {
		t.Fatalf("the composition's bounds are %+v (%v), want the caret's row", compose, ok)
	}
}

func TestTerminalIndexForPointMapsCells(t *testing.T) {
	term, _, tt := newClientTerm(t)
	term.Feed([]byte("abc\r\ndef"))
	tt.Frame()
	at := func(col, row int) int {
		t.Helper()
		x, y := cellCenter(term, col, row)
		off, ok := term.v.IndexForPoint(ui.Point{X: x, Y: y})
		if !ok {
			t.Fatalf("the cell %d, %d is not in the grid", col, row)
		}
		return off
	}
	if got := at(1, 0); got != 1 {
		t.Fatalf("the second cell is offset %d, want 1", got)
	}
	if got := at(0, 1); got != 4 {
		t.Fatalf("the first cell of the second row is offset %d, want 4", got)
	}
	if got := at(2, 1); got != 6 {
		t.Fatalf("the third cell of the second row is offset %d, want 6", got)
	}
	if _, ok := term.v.IndexForPoint(ui.Point{X: -1, Y: 0}); ok {
		t.Fatal("a point left of the grid mapped")
	}
	if _, ok := term.v.IndexForPoint(ui.Point{X: float32(term.v.size[0]) + 1, Y: 0}); ok {
		t.Fatal("a point right of the grid mapped")
	}
}

func TestTerminalClientStablePointer(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	v := term.v
	tt.Frame()
	if term.v != v {
		t.Fatal("the first frame replaced the view")
	}
	tt.Compose("ni", 2)
	for range 3 {
		tt.Frame()
		if term.v != v {
			t.Fatal("a redraw replaced the view")
		}
		if term.v.preedit != "ni" {
			t.Fatalf("a redraw released the composition: %q", term.v.preedit)
		}
		if got := conn.take(term); got != "" {
			t.Fatalf("a redraw sent %q", got)
		}
	}
	tt.SetFocused(false)
	tt.Frame()
	if term.v != v {
		t.Fatal("losing the focus replaced the view")
	}
	tt.SetFocused(true)
	tt.Frame()
	if term.v != v {
		t.Fatal("gaining the focus replaced the view")
	}
}

func TestTerminalEnterAndTab(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	tt.Key(0, ui.KeyEnter)
	if got := conn.take(term); got != "\r" {
		t.Fatalf("Enter sent %q", got)
	}
	tt.Key(0, ui.KeyTab)
	if got := conn.take(term); got != "\t" {
		t.Fatalf("Tab sent %q", got)
	}
	// The same keys while an input method composes, passed on by it, are
	// still keys: the composition is untouched and each arrives once.
	tt.Compose("ni", 2)
	term.v.keyDown(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter})
	if got := conn.take(term); got != "\r" {
		t.Fatalf("Enter while composing sent %q", got)
	}
	term.v.keyDown(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyTab})
	if got := conn.take(term); got != "\t" {
		t.Fatalf("Tab while composing sent %q", got)
	}
	if term.v.preedit != "ni" {
		t.Fatalf("the composition became %q", term.v.preedit)
	}
}

func TestKeyRepeatTypesOnDarwin(t *testing.T) {
	was := goos
	defer func() { goos = was }()
	goos = "darwin"
	term, conn, tt := newClientTerm(t)
	// The repeat is sent by the fork itself, since macOS's input method
	// path would show the press-and-hold menu instead.
	tt.KeyRepeat(0, ui.KeyJ)
	if got := conn.take(term); got != "j" {
		t.Fatalf("a repeat sent %q, want j", got)
	}
	// The text the input method may still deliver for the repeat is
	// dropped, not typed twice.
	tt.Type("j")
	if got := conn.take(term); got != "" {
		t.Fatalf("the repeat typed twice: %q", got)
	}
	tt.KeyUp(ui.KeyJ)
	if got := conn.take(term); got != "" {
		t.Fatalf("the release typed %q", got)
	}
	tt.KeyRepeat(ui.Shift, ui.KeyJ)
	if got := conn.take(term); got != "J" {
		t.Fatalf("a shifted repeat sent %q, want J", got)
	}
	// Elsewhere repeats still come through the input method: the key waits
	// for its text, which is typed once.
	goos = "linux"
	tt.KeyRepeat(0, ui.KeyK)
	if got := conn.take(term); got != "" {
		t.Fatalf("a repeat before its text sent %q", got)
	}
	tt.Type("k")
	if got := conn.take(term); got != "k" {
		t.Fatalf("the repeat's text sent %q", got)
	}
}

func TestTerminalOptionDeadKeyComposes(t *testing.T) {
	was := goos
	defer func() { goos = was }()
	goos = "darwin"
	term, conn, tt := newClientTerm(t)
	// Option-E asks the input method for a dead key, which it shows as a
	// marked composition and does not commit.
	term.v.keyDown(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyE, Mods: ui.Alt})
	tt.Compose("´", 1)
	term.v.keyUp(ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.KeyE, Mods: ui.Alt})
	if got := conn.take(term); got != "" {
		t.Fatalf("the dead key sent %q", got)
	}
	if term.v.preedit != "´" {
		t.Fatalf("the dead key's mark is %q", term.v.preedit)
	}
	// The e that completes it commits é, once.
	tt.TypeKey(0, ui.KeyE, "é")
	if got := conn.take(term); got != "é" {
		t.Fatalf("Option-E then e sent %q, want é", got)
	}
	if term.v.preedit != "" {
		t.Fatalf("the dead key's mark survived: %q", term.v.preedit)
	}
}

func TestTerminalPickedAccentTypesNothing(t *testing.T) {
	was := goos
	defer func() { goos = was }()
	goos = "darwin"
	term, conn, tt := newClientTerm(t)
	tt.KeyRepeat(0, ui.KeyE)
	if got := conn.take(term); got != "e" {
		t.Fatalf("the repeat sent %q", got)
	}
	// The press-and-hold popup's chosen accent arrives as text; the repeat
	// owns the key, so it types nothing (accepted).
	tt.Type("é")
	if got := conn.take(term); got != "" {
		t.Fatalf("a picked accent typed %q", got)
	}
}

func TestSingleRuneCJK(t *testing.T) {
	term, conn, tt := newClientTerm(t)
	// A single CJK rune with no key: an input method's commit.
	tt.Type("你")
	if got := conn.take(term); got != "你" {
		t.Fatalf("a single CJK rune typed %q", got)
	}
}

func TestTerminalTapKeepsKeyboardTarget(t *testing.T) {
	term, _, tt := newClientTerm(t)
	term.Feed([]byte("abc"))
	tt.Frame()
	// A click on the focused terminal keeps the focus and the text target:
	// MyGo requests the on-screen keyboard for exactly that state when the
	// host is one of the phones (it counts the request for a focused
	// element owning a client).
	x, y := cellCenter(term, 1, 0)
	tt.ClickAt(x, y)
	if !tt.Focused("Terminal") {
		t.Fatal("the click took the terminal's focus")
	}
	if active, _ := tt.TextInputActive(); !active {
		t.Fatal("the terminal is no longer a text target")
	}
	// And the same client still composes after the click.
	tt.Compose("ni", 2)
	if _, ok := term.v.MarkedRange(); !ok {
		t.Fatal("the click released the text client")
	}
}
