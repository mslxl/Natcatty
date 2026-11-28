package appview

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/ui/terminal"
)

// joinSpans is the text of one screen row.
func joinSpans(spans []ui.Span) string {
	s := ""
	for _, sp := range spans {
		s += sp.Text
	}
	return s
}

// rowText is a row without the trailing cursor cell.
func rowText(spans []ui.Span) string { return strings.TrimRight(joinSpans(spans), " ") }

// A snapshot is replayed onto a fixed grid: text, CR/LF, cursor addressing, and
// the scroll at the bottom of the screen.
func TestScreenReplaysSnapshot(t *testing.T) {
	sc := parseScreen([]byte("one\r\ntwo\r\n\x1b[1;1Htop"), 20, 4, true)
	lines, curRow, curCol := sc.Lines()
	if got := rowText(lines[0]); got != "top" {
		t.Errorf("row 0 = %q, want the overwritten text %q", got, "top")
	}
	if got := rowText(lines[1]); got != "two" {
		t.Errorf("row 1 = %q, want two", got)
	}
	if curRow != 0 || curCol != 3 {
		t.Errorf("cursor = (%d,%d), want (0,3)", curRow, curCol)
	}

	// Cursor addressing in the middle of the screen.
	sc = parseScreen([]byte("\x1b[3;5HX"), 20, 5, true)
	lines, curRow, curCol = sc.Lines()
	if got := rowText(lines[2]); got != "    X" {
		t.Errorf("row 2 = %q, want 4 spaces then X", got)
	}
	if curRow != 2 || curCol != 5 {
		t.Errorf("cursor = (%d,%d), want (2,5)", curRow, curCol)
	}

	// More lines than rows: the earlier ones become scrollback, which the replay
	// keeps (the flow shows it), and the screen is the last rows.
	sc = parseScreen([]byte("a\r\nb\r\nc\r\n"), 20, 2, true)
	lines, curRow, _ = sc.Lines()
	if got := rowText(lines[0]); got != "a" {
		t.Errorf("scrollback row 0 = %q, want a", got)
	}
	if got := rowText(lines[2]); got != "c" {
		t.Errorf("screen row = %q, want c", got)
	}
	if curRow != 3 {
		t.Errorf("cursor row = %d, want 3 (below the last line)", curRow)
	}
}

// SGR colors and reverse video reach the spans.
func TestScreenStyles(t *testing.T) {
	sc := parseScreen([]byte("\x1b[32mgreen\x1b[0m plain"), 40, 2, true)
	lines, _, _ := sc.Lines()
	spans := lines[0]
	if len(spans) < 2 {
		t.Fatalf("row 0 has %d spans, want at least 2: %+v", len(spans), spans)
	}
	if spans[0].Text != "green" {
		t.Fatalf("first span = %q, want green", spans[0].Text)
	}
	if spans[0].Color == (ui.Color{}) {
		t.Errorf("the green span has no colour")
	}
	if spans[1].Color != (ui.Color{}) {
		t.Errorf("the plain span inherited the colour: %+v", spans[1])
	}
}

// nopConn is a terminal connection that only discards what it is given.
type nopConn struct {
	mu     sync.Mutex
	wrote  []byte
	closed chan struct{}
	once   sync.Once
}

func newNopConn() *nopConn { return &nopConn{closed: make(chan struct{})} }

func (c *nopConn) Read(p []byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *nopConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.wrote = append(c.wrote, p...)
	c.mu.Unlock()
	return len(p), nil
}

func (c *nopConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *nopConn) Written() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.wrote)
}

// The render path end to end: what the terminal is fed shows up in the rows this
// package draws, and a typed key reaches the program through Send.
func TestLiveScreenFromTerminal(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 6)
	term.Feed([]byte("hello terminal\r\nsecond line"))
	size := Size{Cols: 40, Rows: 6}
	lines, _, _ := Screen(term, size, true)
	if len(lines) < 2 {
		t.Fatalf("rows = %d, want at least 2", len(lines))
	}
	if got := rowText(lines[0]); got != "hello terminal" {
		t.Errorf("row 0 = %q, want hello terminal", got)
	}
	if got := rowText(lines[1]); got != "second line" {
		t.Errorf("row 1 = %q, want second line", got)
	}

	// Keys go to the program as bytes.
	term.Send([]byte("x"))
	for i := 0; i < 100; i++ {
		if conn.Written() == "x" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := conn.Written(); got != "x" {
		t.Errorf("the program received %q, want x", got)
	}
}

// The key translator: text arrives as InputText, and only the keys a program needs
// as escape sequences are handled here.
func TestInputBytes(t *testing.T) {
	cases := []struct {
		name string
		ev   ui.InputEvent
		want string
	}{
		{"text", ui.InputEvent{Kind: ui.InputText, Text: "abc"}, "abc"},
		{"enter", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter}, "\r"},
		{"backspace", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyBackspace}, "\x7f"},
		{"tab", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyTab}, "\t"},
		{"shift-tab", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyTab, Mods: ui.Shift}, "\x1b[Z"},
		{"escape", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEscape}, "\x1b"},
		{"up", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyUp}, "\x1b[A"},
		{"left", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyLeft}, "\x1b[D"},
		{"home", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyHome}, "\x1b[H"},
		{"delete", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyDelete}, "\x1b[3~"},
		// A modified key carries xterm's modifier parameter; without it
		// Ctrl+Left was a plain Left and word-wise movement never happened in
		// readline, vim and less.
		{"ctrl-left", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyLeft, Mods: ui.Ctrl}, "\x1b[1;5D"},
		{"shift-up", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyUp, Mods: ui.Shift}, "\x1b[1;2A"},
		{"alt-left", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyLeft, Mods: ui.Alt}, "\x1b[1;3D"},
		{"ctrl-end", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnd, Mods: ui.Ctrl}, "\x1b[1;5F"},
		{"ctrl-page-up", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyPageUp, Mods: ui.Ctrl}, "\x1b[5;5~"},
		{"ctrl-delete", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyDelete, Mods: ui.Ctrl}, "\x1b[3;5~"},
		{"alt-backspace", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyBackspace, Mods: ui.Alt}, "\x1b\x7f"},
		// F1-F4 are SS3 keys, F5 and on are tilde keys; both grew a modifier.
		{"f1", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyF1}, "\x1bOP"},
		{"shift-f1", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyF1, Mods: ui.Shift}, "\x1b[1;2P"},
		{"f4", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyF4}, "\x1bOS"},
		{"f5", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyF5}, "\x1b[15~"},
		{"ctrl-f5", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyF5, Mods: ui.Ctrl}, "\x1b[15;5~"},
		{"f12", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyF12}, "\x1b[24~"},
		{"ctrl-c", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyC, Mods: ui.Ctrl}, "\x03"},
		{"ctrl-space", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeySpace, Mods: ui.Ctrl}, "\x00"},
		{"alt-f", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyF, Mods: ui.Alt}, "\x1bf"},
		{"plain letter", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyA}, ""},
		{"command", ui.InputEvent{Kind: ui.InputCommand, Text: "copy"}, ""},
	}
	for _, c := range cases {
		got := string(InputBytes(c.ev))
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The input-method client: composition is held, and the committed text goes to the
// program (that is all a terminal can do, since the shell's line editor owns the
// input line inside the VT).
func TestInputClientCommits(t *testing.T) {
	var sent []string
	c := NewInputClient(func(b []byte) { sent = append(sent, string(b)) })

	if _, ok := c.MarkedRange(); ok {
		t.Fatalf("a fresh client reports a marked range")
	}
	c.SetMarkedText(nil, "ni", ui.TextInputRange{Start: 0, End: 2})
	if got := c.Marked(); got != "ni" {
		t.Fatalf("marked = %q, want ni", got)
	}
	if r, ok := c.MarkedRange(); !ok || r.Start != 0 || r.End != 2 {
		t.Fatalf("marked range = %+v ok=%v", r, ok)
	}
	if text, r := c.TextForRange(ui.TextInputRange{Start: 0, End: 99}); text != "ni" || r.End != 2 {
		t.Errorf("TextForRange = %q %+v, want ni clamped to 2", text, r)
	}
	if sel := c.Selection(); sel.Range.Start != 2 || sel.Range.End != 2 {
		t.Errorf("selection = %+v, want the caret after ni", sel)
	}

	// Commit: the text is sent and the composition ends.
	c.ReplaceText(nil, "你")
	if len(sent) != 1 || sent[0] != "你" {
		t.Fatalf("sent = %q, want 你", sent)
	}
	if c.Marked() != "" {
		t.Errorf("the composition survived the commit: %q", c.Marked())
	}

	// Unmark drops the composition without sending anything.
	c.SetMarkedText(nil, "hao", ui.TextInputRange{Start: 0, End: 3})
	c.UnmarkText()
	if c.Marked() != "" || len(sent) != 1 {
		t.Errorf("unmark sent %q or kept %q", sent, c.Marked())
	}

	// A zero-length replacement sends nothing.
	c.ReplaceText(nil, "")
	if len(sent) != 1 {
		t.Errorf("an empty commit sent %q", sent)
	}
}

// UTF-16 ranges and surrogate pairs.
func TestSubstringUTF16(t *testing.T) {
	cases := []struct {
		s          string
		start, end int
		want       string
	}{
		{"abc", 0, 2, "ab"},
		{"abc", 1, 3, "bc"},
		{"abc", 2, 2, ""},
		{"你好", 0, 1, "你"},
		{"你好", 1, 2, "好"},
		{"a😀b", 0, 1, "a"}, // 😀 is two UTF-16 units
		{"a😀b", 1, 3, "😀"},
		{"a😀b", 3, 4, "b"},
	}
	for _, c := range cases {
		if got := substringUTF16(c.s, c.start, c.end); got != c.want {
			t.Errorf("substringUTF16(%q,%d,%d) = %q, want %q", c.s, c.start, c.end, got, c.want)
		}
	}
}

// The cursor blinks: the reversed cell exists only while the blink is on.
func TestScreenCursorBlinks(t *testing.T) {
	on := parseScreen([]byte("ab"), 10, 2, true)
	linesOn, _, _ := on.Lines()
	lastOn := linesOn[0][len(linesOn[0])-1]
	if lastOn.Background == (ui.Color{}) {
		t.Fatalf("the cursor is not drawn while the blink is on")
	}
	off := parseScreen([]byte("ab"), 10, 2, false)
	linesOff, curRow, curCol := off.Lines()
	if len(linesOff[0]) != 1 || linesOff[0][0].Background != (ui.Color{}) {
		t.Errorf("the cursor is still drawn while the blink is off: %+v", linesOff[0])
	}
	if curRow != 0 || curCol != 2 {
		t.Errorf("cursor = (%d,%d), want (0,2) even while hidden", curRow, curCol)
	}
}

// A full-screen program (vim, less) switches to the alternate screen: our replay
// shows that screen, and the normal one comes back when the program exits.
func TestLiveAltScreen(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 6)
	size := Size{Cols: 40, Rows: 6}

	term.Feed([]byte("normal prompt\r\n$ ls"))
	lines, _, _ := Screen(term, size, true)
	if got := rowText(lines[0]); got != "normal prompt" {
		t.Fatalf("before the alternate screen row 0 = %q", got)
	}

	term.Feed([]byte("\x1b[?1049h\x1b[H\x1b[2JVIM SCREEN"))
	lines, _, _ = Screen(term, size, true)
	if got := rowText(lines[0]); got != "VIM SCREEN" {
		t.Fatalf("alternate screen row 0 = %q, want VIM SCREEN", got)
	}
	if got := rowText(lines[1]); got != "" {
		t.Errorf("alternate screen row 1 = %q, want empty", got)
	}

	term.Feed([]byte("\x1b[?1049l"))
	lines, _, _ = Screen(term, size, true)
	found := false
	for _, spans := range lines {
		if strings.Contains(rowText(spans), "normal prompt") {
			found = true
		}
	}
	if !found {
		t.Errorf("the normal screen did not come back: %v", lines)
	}
}

// mouseMode parsing: a program's DECSET shows in the snapshot.
func TestMouseModeFromSnapshot(t *testing.T) {
	sc := parseScreen([]byte("\x1b[?1000h\x1b[?1006h"), 40, 6, true)
	m := sc.Mouse()
	if !m.Enabled() || !m.press || !m.sgr || m.drag || m.motion {
		t.Fatalf("mode = %+v, want press+sgr", m)
	}
	// Reset turns it off again.
	sc = parseScreen([]byte("\x1b[?1000h\x1b[?1006h\x1b[?1000l"), 40, 6, true)
	if sc.Mouse().Enabled() {
		t.Errorf("the mode survived its reset: %+v", sc.Mouse())
	}
	// Drag and any-motion modes.
	sc = parseScreen([]byte("\x1b[?1002h\x1b[?1006h"), 40, 6, true)
	if !sc.Mouse().drag {
		t.Errorf("1002 was not recorded: %+v", sc.Mouse())
	}
}

// SGR mouse encoding: buttons, release, motion and the wheel.
func TestMouseSeqSGR(t *testing.T) {
	m := mouseMode{press: true, drag: true, sgr: true}
	cases := []struct {
		name string
		ev   ui.InputEvent
		col  int
		row  int
		want string
	}{
		{"left down", ui.InputEvent{Kind: ui.InputPointerDown, Button: 0}, 0, 0, "\x1b[<0;1;1M"},
		{"left up", ui.InputEvent{Kind: ui.InputPointerUp, Button: 0}, 4, 2, "\x1b[<0;5;3m"},
		{"right down", ui.InputEvent{Kind: ui.InputPointerDown, Button: 1}, 1, 1, "\x1b[<2;2;2M"},
		{"middle down", ui.InputEvent{Kind: ui.InputPointerDown, Button: 2}, 1, 1, "\x1b[<1;2;2M"},
		{"motion with button", ui.InputEvent{Kind: ui.InputPointerMove, Button: 0}, 3, 3, "\x1b[<32;4;4M"},
		{"wheel up", ui.InputEvent{Kind: ui.InputScroll, DY: -1}, 0, 1, "\x1b[<64;1;2M"},
		{"wheel down", ui.InputEvent{Kind: ui.InputScroll, DY: 1}, 0, 1, "\x1b[<65;1;2M"},
		// A horizontal wheel is a different button pair, not a dropped event.
		{"wheel left", ui.InputEvent{Kind: ui.InputScroll, DX: -1}, 0, 1, "\x1b[<66;1;2M"},
		{"wheel right", ui.InputEvent{Kind: ui.InputScroll, DX: 1}, 0, 1, "\x1b[<67;1;2M"},
		// xterm carries the modifier keys in the button byte: Shift 4,
		// Alt 8, Ctrl 16. Without them vim cannot tell a Shift-click from a
		// plain one, and Ctrl-click opened no context menu.
		{"ctrl-click", ui.InputEvent{Kind: ui.InputPointerDown, Button: 0, Mods: ui.Ctrl}, 0, 0, "\x1b[<16;1;1M"},
		{"alt-click", ui.InputEvent{Kind: ui.InputPointerDown, Button: 0, Mods: ui.Alt}, 0, 0, "\x1b[<8;1;1M"},
		{"shift-click", ui.InputEvent{Kind: ui.InputPointerDown, Button: 0, Mods: ui.Shift}, 0, 0, "\x1b[<4;1;1M"},
		{"ctrl-wheel", ui.InputEvent{Kind: ui.InputScroll, DY: -1, Mods: ui.Ctrl}, 0, 1, "\x1b[<80;1;2M"},
	}
	for _, c := range cases {
		if got := string(MouseSeq(m, c.ev, c.col, c.row)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	// A program that did not ask for the mouse gets nothing.
	if got := MouseSeq(mouseMode{}, ui.InputEvent{Kind: ui.InputPointerDown}, 0, 0); got != nil {
		t.Errorf("a disabled mode encoded %q", got)
	}
	// Motion is not sent to a program that only asked for press/release.
	if got := MouseSeq(mouseMode{press: true, sgr: true}, ui.InputEvent{Kind: ui.InputPointerMove}, 0, 0); got != nil {
		t.Errorf("motion was sent to a press-only program: %q", got)
	}
}

// The real plugin's snapshot carries the mode through to ScreenFrame.
func TestFrameCarriesMouseMode(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 6)
	term.Feed([]byte("x\x1b[?1000h\x1b[?1006h"))
	f := ScreenFrame(term, Size{Cols: 40, Rows: 6}, true)
	if !f.Mouse.Enabled() || !f.Mouse.sgr {
		t.Fatalf("frame mouse = %+v, want press+sgr", f.Mouse)
	}
	if len(f.Lines) == 0 || !strings.Contains(rowText(f.Lines[0]), "x") {
		t.Errorf("frame lines lost the text: %v", f.Lines)
	}
}

// A URL printed as text is opened by a modified click on any of its cells.
func TestURLAt(t *testing.T) {
	row := []ui.Span{{Text: "see https://example.com/x for more"}}
	// "https://example.com/x" starts at cell 4 and ends at cell 24.
	for _, col := range []int{4, 10, 23} {
		if uri, ok := URLAt(row, col); !ok || uri != "https://example.com/x" {
			t.Errorf("col %d: %q ok=%v, want the URL", col, uri, ok)
		}
	}
	for _, col := range []int{0, 2, 26, 30} {
		if uri, ok := URLAt(row, col); ok {
			t.Errorf("col %d found a URL where there is none: %q", col, uri)
		}
	}

	// Surrounding punctuation is trimmed, and a bare www host gets a scheme.
	paren := []ui.Span{{Text: "(https://a.example/b)."}}
	if uri, ok := URLAt(paren, 2); !ok || uri != "https://a.example/b" {
		t.Errorf("parenthesised URL = %q ok=%v", uri, ok)
	}
	bare := []ui.Span{{Text: "open www.example.com now"}}
	if uri, ok := URLAt(bare, 6); !ok || uri != "https://www.example.com" {
		t.Errorf("bare host = %q ok=%v", uri, ok)
	}

	// Wide runes take two cells, so the column mapping must count cells.
	wide := []ui.Span{{Text: "中文 https://x.example/y"}}
	// "中文 " is 5 cells, so the URL starts at cell 5.
	if uri, ok := URLAt(wide, 6); !ok || uri != "https://x.example/y" {
		t.Errorf("after wide runes = %q ok=%v", uri, ok)
	}
	if _, ok := URLAt(wide, 1); ok {
		t.Errorf("a URL was found inside the wide runes")
	}
}

// A program hides the cursor (DECSET ?25l, as vim does in command mode) and shows
// it again; the replay must follow that.
func TestScreenCursorVisibility(t *testing.T) {
	sc := parseScreen([]byte("ab\x1b[?25l"), 10, 2, true)
	lines, _, _ := sc.Lines()
	for _, sp := range lines[0] {
		if sp.Background != (ui.Color{}) {
			t.Errorf("the cursor is drawn although the program hid it: %+v", lines[0])
		}
	}
	sc = parseScreen([]byte("ab\x1b[?25l\x1b[?25h"), 10, 2, true)
	lines, _, _ = sc.Lines()
	last := lines[0][len(lines[0])-1]
	if last.Background == (ui.Color{}) {
		t.Errorf("the cursor is missing although the program shows it: %+v", lines[0])
	}
}

// A combining mark joins the cell before it instead of being dropped.
func TestScreenCombiningMarks(t *testing.T) {
	sc := parseScreen([]byte("e\u0301x"), 10, 2, true)
	lines, curRow, curCol := sc.Lines()
	if got := rowText(lines[0]); got != "e\u0301x" {
		t.Errorf("row 0 = %q, want é followed by x", got)
	}
	if curRow != 0 || curCol != 2 {
		t.Errorf("cursor = (%d,%d), want (0,2): the mark takes no cell", curRow, curCol)
	}

	// The mark attaches to the wide rune, not to its continuation cell.
	sc = parseScreen([]byte("中\u0301x"), 10, 2, true)
	lines, _, _ = sc.Lines()
	if got := rowText(lines[0]); got != "中\u0301x" {
		t.Errorf("wide rune with a mark = %q", got)
	}
}

// Extended SGR: underline styles (4:n, 21), strikethrough (9/29) and the underline
// colour (58/59). The plugin's snapshot keeps these escapes verbatim, so the replay
// can honour them.
func TestScreenExtendedSGR(t *testing.T) {
	cases := []struct {
		sgr                string
		under, wavy, strik bool
		color              bool
	}{
		{"\x1b[4m", true, false, false, false},
		{"\x1b[4:1m", true, false, false, false},
		{"\x1b[4:2m", true, false, false, false},
		{"\x1b[4:3m", true, true, false, false},
		{"\x1b[4:0m", false, false, false, false},
		{"\x1b[9m", false, false, true, false},
		{"\x1b[21m", true, false, false, false},
		{"\x1b[58;2;10;20;30m", false, false, false, true},
	}
	for _, c := range cases {
		sc := parseScreen([]byte(c.sgr+"x"), 20, 2, true)
		lines, _, _ := sc.Lines()
		if len(lines[0]) == 0 {
			t.Fatalf("%q produced an empty row", c.sgr)
		}
		sp := lines[0][0]
		if sp.Underline != c.under || sp.WavyUnderline != c.wavy || sp.Strikethrough != c.strik {
			t.Errorf("%q -> Underline=%v Wavy=%v Strike=%v", c.sgr, sp.Underline, sp.WavyUnderline, sp.Strikethrough)
		}
		hasColor := sp.DecorationColor != (ui.Color{})
		if hasColor != c.color {
			t.Errorf("%q -> decoration colour %+v, want set=%v", c.sgr, sp.DecorationColor, c.color)
		}
	}

	// 24 removes the underline, 29 the strikethrough, 59 the colour.
	sc := parseScreen([]byte("\x1b[4m\x1b[24m\x1b[9m\x1b[29m\x1b[58;5;1m\x1b[59mx"), 20, 2, true)
	lines, _, _ := sc.Lines()
	sp := lines[0][0]
	if sp.Underline || sp.Strikethrough || sp.DecorationColor != (ui.Color{}) {
		t.Errorf("the resets did not clear the attributes: %+v", sp)
	}
}

// Palette index 4 (ANSI blue) in 38;5;4, 38:5:4, 48;5;4 or RGB component 4 in
// 38;2;4;... must never be mistaken for SGR 4 (underline). Bash's default PS1
// colors the prompt path `~` in bold blue (\x1b[01;34m), which Ghostty's
// snapshot serializes with palette index 4 — mistaking that 4 for underline
// made prompt paths look like blue underlined links.
func TestSGRPaletteColor4DoesNotUnderlinePromptPath(t *testing.T) {
	for _, seq := range []string{
		"\x1b[1;38;5;4m~\x1b[0m",
		"\x1b[1;38:5:4m~\x1b[0m",
		"\x1b[38;2;4;10;20m~\x1b[0m",
		"\x1b[38:2::4:10:20m~\x1b[0m",
		"\x1b[48;5;4m~\x1b[0m",
		"\x1b[4;0m~",
	} {
		sc := parseScreen([]byte(seq), 20, 2, false)
		lines, _, _ := sc.Lines()
		if len(lines[0]) == 0 {
			t.Fatalf("%q produced empty row", seq)
		}
		if lines[0][0].Underline {
			t.Errorf("%q unexpectedly set Underline=true: %+v", seq, lines[0][0])
		}
	}

	// End-to-end through the real Ghostty terminal snapshot with Ubuntu/Debian PS1.
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 6)
	term.Feed([]byte("\x1b[01;32mmslxl@ecs65538\x1b[00m:\x1b[01;34m~\x1b[00m$ "))
	lines, _, _ := Screen(term, Size{Cols: 40, Rows: 6}, false)
	if len(lines) == 0 {
		t.Fatal("no rows")
	}
	foundTilde := false
	for _, sp := range lines[0] {
		if strings.Contains(sp.Text, "~") {
			foundTilde = true
			if sp.Underline {
				t.Fatalf("prompt path ~ was underlined in Ghostty snapshot: %+v", sp)
			}
		}
	}
	if !foundTilde {
		t.Fatalf("did not find ~ in prompt row: %+v", lines[0])
	}
}

// The real plugin preserves extended SGR in its snapshot, so the whole path works.
func TestLiveExtendedSGRFromTerminal(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 6)
	term.Feed([]byte("\x1b[4:3mcurly\x1b[0m\x1b[9mstrike\x1b[0m"))
	lines, _, _ := Screen(term, Size{Cols: 40, Rows: 6}, true)
	if len(lines) == 0 {
		t.Fatalf("no rows")
	}
	var sawWavy, sawStrike bool
	for _, sp := range lines[0] {
		if sp.WavyUnderline {
			sawWavy = true
		}
		if sp.Strikethrough {
			sawStrike = true
		}
	}
	if !sawWavy || !sawStrike {
		t.Fatalf("extended SGR lost: wavy=%v strike=%v row=%+v", sawWavy, sawStrike, lines[0])
	}
}

// The live screen draws with the terminal's own palette, and text without an SGR
// colour takes the terminal's default foreground.
func TestTermColorsAndDefaults(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		fg, bg := TermColors(c)
		if fg == (ui.Color{}) || bg == (ui.Color{}) {
			t.Errorf("TermColors returned an empty colour: fg=%v bg=%v", fg, bg)
		}
		if fg == bg {
			t.Errorf("the terminal's foreground and background are the same")
		}
		lines := [][]ui.Span{
			{{Text: "plain"}, {Text: "styled", Color: ui.RGBA(1, 2, 3, 1)}},
		}
		applyDefaults(lines, fg)
		if lines[0][0].Color != fg {
			t.Errorf("a span without a colour kept %v, want the terminal foreground", lines[0][0].Color)
		}
		if lines[0][1].Color != ui.RGBA(1, 2, 3, 1) {
			t.Errorf("an explicit colour was overwritten: %v", lines[0][1].Color)
		}
	}, 400, 100)
	tt.Frame()
}

func TestNatcattyTerminalThemeDrivesLiveGrid(t *testing.T) {
	theme := &terminal.Theme{
		Foreground: ui.Hex("#123456"),
		Background: ui.Hex("#654321"),
		Palette: [16]ui.Color{
			ui.Hex("#000001"), ui.Hex("#010203"), ui.Hex("#020304"), ui.Hex("#030405"),
			ui.Hex("#040506"), ui.Hex("#050607"), ui.Hex("#060708"), ui.Hex("#070809"),
			ui.Hex("#08090a"), ui.Hex("#090a0b"), ui.Hex("#0a0b0c"), ui.Hex("#0b0c0d"),
			ui.Hex("#0c0d0e"), ui.Hex("#0d0e0f"), ui.Hex("#0e0f10"), ui.Hex("#0f1011"),
		},
	}
	fg, bg := TermColors(nil, theme)
	if fg != theme.Foreground || bg != theme.Background {
		t.Fatalf("theme colors = %v / %v, want %v / %v", fg, bg, theme.Foreground, theme.Background)
	}
	sc := parseScreenWithTheme([]byte("\x1b[31mred"), 20, 2, true, Cursor{}, theme)
	lines, _, _ := sc.Lines()
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatal("the themed screen produced no spans")
	}
	if lines[0][0].Color != theme.Palette[1] {
		t.Fatalf("ANSI red = %v, want Natcatty palette %v", lines[0][0].Color, theme.Palette[1])
	}
	plain := [][]ui.Span{{{Text: "plain"}}}
	applyDefaults(plain, fg)
	if plain[0][0].Color != theme.Foreground {
		t.Fatalf("plain text = %v, want Natcatty foreground %v", plain[0][0].Color, theme.Foreground)
	}
}

// With the plugin keeping scrollback, the replay shows the running command's earlier
// output above the screen, which is what lets the flow be scrolled back through it.
func TestLiveScrollback(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 1 << 20})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(20, 6)
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "L%02d\r\n", i)
	}
	term.Feed([]byte(b.String()))
	lines, curRow, _ := Screen(term, Size{Cols: 20, Rows: 6}, true)
	if len(lines) <= 6 {
		t.Fatalf("rows = %d, want scrollback above the 6-row screen", len(lines))
	}
	var joined []string
	for _, spans := range lines {
		joined = append(joined, rowText(spans))
	}
	all := strings.Join(joined, "\n")
	if !strings.Contains(all, "L01") {
		t.Errorf("the first line is missing from the replay:\n%s", all)
	}
	if !strings.Contains(all, "L30") {
		t.Errorf("the last line is missing:\n%s", all)
	}
	if curRow < 6 {
		t.Errorf("cursor row = %d, want it inside the screen at the bottom", curRow)
	}
}

// Replaying a snapshot of the largest grid the app draws stays well inside a frame:
// the cap is maxGridRows rows of one line each.
func TestScreenParseBudget(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxGridRows; i++ {
		b.WriteString(strings.Repeat("x", 140))
		b.WriteString("\r\n")
	}
	start := time.Now()
	sc := parseScreen([]byte(b.String()), 140, 24, true)
	_, _, _ = sc.Lines()
	if d := time.Since(start); d > 120*time.Millisecond {
		t.Errorf("replaying %d rows took %v, which is too slow for a frame", maxGridRows, d)
	}
}

// The grid follows the room the live screen gets: a terminal sizes itself to its
// pane, so a long line wraps where the reader sees the edge (with a fixed grid it
// wrapped "for no reason" while empty space sat to its right).
func TestGridFor(t *testing.T) {
	cellWidth, cellHeight = 8, 16
	defer func() { cellWidth, cellHeight = 0, 0 }()

	size, ok := GridFor(Viewport{W: 1100, H: 780})
	if !ok {
		t.Fatalf("no grid for a 1100x780 viewport")
	}
	if want := int((1100 - 12 - 14) / 8); size.Cols != want {
		t.Errorf("cols = %d, want %d", size.Cols, want)
	}
	if want := int((780 - 12) / 16); size.Rows != want {
		t.Errorf("rows = %d, want %d", size.Rows, want)
	}

	// A wider viewport has more columns, a narrower one fewer.
	wide, _ := GridFor(Viewport{W: 1600, H: 780})
	narrow, _ := GridFor(Viewport{W: 700, H: 780})
	if wide.Cols <= size.Cols || narrow.Cols >= size.Cols {
		t.Errorf("the grid does not follow the viewport: %d / %d / %d", narrow.Cols, size.Cols, wide.Cols)
	}
	// Extremes are clamped, not zero or negative.
	tiny, _ := GridFor(Viewport{W: 60, H: 60})
	if tiny.Cols != 20 || tiny.Rows != 8 {
		t.Errorf("tiny viewport = %+v, want the 20x8 floor", tiny)
	}
	// Without a measurement there is no answer.
	if _, ok := GridFor(Viewport{}); ok {
		t.Errorf("a grid was reported before the viewport was measured")
	}
}

// Each Pane's grid comes from that Pane's own viewport. Two Panes measured in
// one frame must not read each other: the wide Pane keeps the wide grid and the
// narrow Pane the narrow one, whatever order they ask in.
func TestGridForKeepsViewportsApart(t *testing.T) {
	cellWidth, cellHeight = 8, 16
	defer func() { cellWidth, cellHeight = 0, 0 }()

	wide := Viewport{W: 619, H: 504}
	narrow := Viewport{W: 260, H: 504}
	wideGrid, ok := GridFor(wide)
	if !ok {
		t.Fatal("no grid for the wide Pane")
	}
	narrowGrid, ok := GridFor(narrow)
	if !ok {
		t.Fatal("no grid for the narrow Pane")
	}
	if wideGrid.Cols <= narrowGrid.Cols {
		t.Fatalf("wide Pane cols = %d, narrow Pane cols = %d: the viewports were swapped",
			wideGrid.Cols, narrowGrid.Cols)
	}
	// Asking again in the other order must not change either answer.
	if again, _ := GridFor(narrow); again != narrowGrid {
		t.Errorf("narrow Pane changed after the wide Pane asked: %+v, want %+v", again, narrowGrid)
	}
	if again, _ := GridFor(wide); again != wideGrid {
		t.Errorf("wide Pane changed after the narrow Pane asked: %+v, want %+v", again, wideGrid)
	}
}

// The cell is measured from real text: a broken measurement made a cell 0.1 DIP wide,
// which put every mouse report and the grid size ten times off.
func TestCellIsMeasured(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		measureCell(c)
	}, 900, 600)
	tt.Frame()
	w, h := CellSize()
	if w < 4 || w > 20 {
		t.Errorf("cell width = %v, want a monospace cell", w)
	}
	if h < 8 || h > 40 {
		t.Errorf("cell height = %v, want a line box", h)
	}
}

// The alternate screen is what a full-screen program's snapshot holds; the
// replay must know it, because that snapshot has no scrollback for the shell
// integration's rows to index. Programs use three mode numbers for it.
func TestParseScreenRecognizesTheAlternateScreen(t *testing.T) {
	for _, mode := range []string{"47", "1047", "1049"} {
		sc := parseScreen([]byte("\x1b[?"+mode+"hvim"), 20, 3, false)
		if !sc.alt {
			t.Errorf("?%sh was not read as the alternate screen", mode)
		}
		// A program may turn it off again in the same stream.
		off := parseScreen([]byte("\x1b[?"+mode+"h\x1b[?"+mode+"lmain"), 20, 3, false)
		if off.alt {
			t.Errorf("?%sl left the replay on the alternate screen", mode)
		}
	}
	main := parseScreen([]byte("PS C:\\work> vim"), 20, 3, false)
	if main.alt {
		t.Error("a main-screen replay was read as the alternate screen")
	}
}

// A snapshot carries escapes the cell grid does not draw. ESC ( B designates a
// charset, ESC # 8 fills the screen with E, and Ghostty's formatter ends a
// full-screen program's snapshot with them. Skipping only ESC and one byte left
// their final byte ("B" in ESC ( B) to be drawn as text, which overwrote the
// first character of vim's top row.
func TestParseScreenSkipsCharsetEscapesWhole(t *testing.T) {
	sc := parseScreen([]byte("\x1b(Bhello\x1b)0 world\x1b#8"), 20, 3, false)
	lines, curRow, curCol := sc.Lines()
	if got := rowText(lines[0]); got != "hello world" {
		t.Errorf("row 0 = %q, want the text with no charset byte in it", got)
	}
	if curRow != 0 || curCol != 11 {
		t.Errorf("cursor = (%d,%d), want (0,11): a skipped escape must not move it", curRow, curCol)
	}
}

// Kitty graphics is an APC (ESC _ G … ; base64 payload ESC \), and a snapshot
// of a program that placed a picture carries it. Skipping only ESC and the
// "G" drew the image's base64 where the picture belonged, which is what a
// yazi preview looked like.
func TestParseScreenSkipsGraphicsAPCWhole(t *testing.T) {
	sc := parseScreen([]byte("before\x1b_Ga=T,f=24,s=1,v=1,i=1;AAAA\x1b\\after"), 20, 3, false)
	lines, curRow, curCol := sc.Lines()
	if got := rowText(lines[0]); got != "beforeafter" {
		t.Errorf("row 0 = %q, want the text around the graphics APC", got)
	}
	if curRow != 0 || curCol != 11 {
		t.Errorf("cursor = (%d,%d), want (0,11): a skipped APC must not move it", curRow, curCol)
	}
	// An APC is ended by ST only, as the protocol says; an ESC that is not one
	// does not end it, and nothing of it may show.
	sc = parseScreen([]byte("\x1b_Gi=1;AA\x1b[0mBB\x1b\\ok"), 20, 3, false)
	lines, _, _ = sc.Lines()
	if got := rowText(lines[0]); got != "ok" {
		t.Errorf("row 0 = %q, want only the text after the APC", got)
	}
}

// is also restore-cursor, so reading the flag set as that jumped the cursor to
// the saved position: Ghostty's vim snapshot ends with CSI = 3 ; 1 u, which put
// the cursor back at the top-left, drew the charset escape's "B" there, and
// reported the wrong cursor row. CSI > flags u pushes the flags, and xterm's
// CSI > 4 ; n m sets modifyOtherKeys: neither draws anything.
func TestParseScreenKeepsKeyboardModesOutOfTheCursor(t *testing.T) {
	sc := parseScreen([]byte("ab\x1b[=3;1uXY\x1b[>3u\x1b[<1uZ"), 20, 3, false)
	lines, curRow, curCol := sc.Lines()
	if got := rowText(lines[0]); got != "abXYZ" {
		t.Errorf("row 0 = %q, want the text to keep its place", got)
	}
	if curRow != 0 || curCol != 5 {
		t.Errorf("cursor = (%d,%d), want (0,5): a keyboard flag set must not move it", curRow, curCol)
	}

	// Without a private prefix the pair still saves and restores the cursor.
	sc = parseScreen([]byte("ab\x1b[s\x1b[2;1Hcd\x1b[uZ"), 20, 3, false)
	lines, curRow, curCol = sc.Lines()
	if got := rowText(lines[0]); got != "abZ" {
		t.Errorf("row 0 = %q, want the restored cursor to continue the line", got)
	}
	if curRow != 0 || curCol != 3 {
		t.Errorf("cursor = (%d,%d), want (0,3)", curRow, curCol)
	}

	// modifyOtherKeys is a mode, not Select Graphic Rendition: the text after
	// it stays plain.
	sc = parseScreen([]byte("\x1b[>4;2mplain"), 20, 3, false)
	lines, _, _ = sc.Lines()
	if got := rowText(lines[0]); got != "plain" {
		t.Errorf("row 0 = %q, want plain", got)
	}
	if len(lines[0]) != 1 || lines[0][0].Weight != 0 || lines[0][0].Italic {
		t.Errorf("the mode was drawn as a rendition: %+v", lines[0])
	}
}

// The program owns the cursor's visibility and shape: vim hides it while
// redrawing (DECSET ?25l) and asks for a bar in insert mode (DECSCUSR). The
// live overlay read neither, so a hidden cursor stayed on screen as a block at
// the old position, and the insert-mode bar stayed the configured block.
func TestParseScreenTracksTheProgramsCursor(t *testing.T) {
	sc := parseScreen([]byte("ab\x1b[?25l"), 10, 2, true)
	if sc.cursorOn {
		t.Error("DECSET ?25l left the cursor on")
	}
	sc = parseScreen([]byte("ab\x1b[?25h"), 10, 2, true)
	if !sc.cursorOn {
		t.Error("DECSET ?25h did not show the cursor")
	}

	cases := []struct {
		seq  string
		want terminal.CursorStyle
		set  bool
	}{
		{"\x1b[2 q", terminal.CursorBlock, true},
		{"\x1b[4 q", terminal.CursorUnderline, true},
		{"\x1b[5 q", terminal.CursorBar, true},
		{"\x1b[6 q", terminal.CursorBar, true},
		{"\x1b[0 q", terminal.CursorBlock, false},
	}
	for _, c := range cases {
		sc = parseScreen([]byte("ab"+c.seq), 10, 2, true)
		if sc.cursorShapeSet != c.set || c.set && sc.cursorShape != c.want {
			t.Errorf("%q: shape = (%v, %t), want (%v, %t)", c.seq, sc.cursorShape, sc.cursorShapeSet, c.want, c.set)
		}
	}
}

// indexes into the grid the cap trims, so a drop has to move it up by the same
// count. When it did not, a snapshot of 400 or more rows (a shell with a long
// scrollback) left the cursor one row past the end: every later cell write was
// skipped, and the rows that arrived after the 400th — the prompt and the line
// being typed on the last rows of the window — stayed blank.
func TestParseScreenKeepsTheTailOfALongReplay(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 410; i++ {
		fmt.Fprintf(&b, "line-%03d\r\n", i)
	}
	b.WriteString("PROMPT> typed")
	sc := parseScreen([]byte(b.String()), 40, 8, false)
	lines, curRow, curCol := sc.Lines()
	if len(lines) != maxGridRows {
		t.Fatalf("rows = %d, want the %d-row cap", len(lines), maxGridRows)
	}
	last := len(lines) - 1
	if got := rowText(lines[last]); got != "PROMPT> typed" {
		t.Errorf("last row = %q, want the newest line: the replay lost its tail", got)
	}
	if got := rowText(lines[last-1]); got != "line-409" {
		t.Errorf("row %d = %q, want line-409", last-1, got)
	}
	if curRow != last || curCol != 13 {
		t.Errorf("cursor = (%d,%d), want (%d,13)", curRow, curCol, last)
	}
}
