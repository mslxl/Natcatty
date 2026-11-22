package appview

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens/parse"
)

// screen is the live terminal screen: the grid the plugin's VT holds, replayed
// from snapshot escapes so the flow can draw it as ordinary rows instead of a
// nested terminal widget. Terminal draws its own terminal the same way, which is what
// lets a command's cell sit in the same scroll space as the terminal lines.
type screen struct {
	cols, rows int
	// top is the grid row where the screen starts: the snapshot writes the
	// scrollback first, then the screen, and its final cursor position tells where
	// the screen is.
	top        int
	showCursor bool
	mouse      mouseMode
	links      []string // OSC 8 hyperlinks, by the index cells store
	link       int32    // the hyperlink in force, as cells store it
	cursorOn   bool     // DECSET ?25: whether the program shows the cursor
	// cursorShape is the shape a program asked for with DECSCUSR (CSI n SP q),
	// which overrides the application's cursor preference while it is set.
	cursorShape    terminal.CursorStyle
	cursorShapeSet bool
	// alt is DECSET ?47/?1047/?1049: a full-screen program owns the screen.
	// Its snapshot carries that screen alone, with no scrollback, so rows
	// recorded against the main screen's scrollback do not name its rows.
	alt    bool
	cursor Cursor
	colors TerminalColors
	grid   [][]cell
	// dropped is how many rows the replay cap threw away above the grid. A row
	// recorded in an earlier, uncapped replay is still found in this one by
	// subtracting it (see CursorReplayRow and Tail.PromptRow): the cap slides
	// the window down as output arrives, and without the count every such row
	// named a different line.
	dropped int
	r, c    int
	st      sgr
	savedR  int
	savedC  int
}

type cell struct {
	// s is the cell's text: one rune, or that rune with the combining marks that
	// follow it. Empty means the cell is blank or the second half of a wide rune.
	s string
	// cont marks the second cell of a wide rune: it draws nothing, because the
	// rune before it already takes the two columns.
	cont bool
	st   sgr
	// link is 1 + the index of the cell's OSC 8 hyperlink, or 0 for none.
	link int32
}

// sgr is the graphic rendition in force, with the colors already resolved.
type sgr struct {
	fg, bg  ui.Color
	hasFg   bool
	hasBg   bool
	bold    bool
	dim     bool
	italic  bool
	reverse bool
	// rule is the underline style: 0 none, 1 single, 2 double, 3 curly, 4 dotted,
	// 5 dashed, as SGR 4:n asks for. ruleColor is SGR 58's colour for it.
	rule         int
	strike       bool
	ruleColor    ui.Color
	hasRuleColor bool
}

// parseScreen replays VT escapes (as terminal.Snapshot returns them) onto a grid
// of the given size. Rows the content runs past scroll off the top, as they do on
// a real screen.
func parseScreen(data []byte, cols, rows int, showCursor bool) *screen {
	return parseScreenWithCursor(data, cols, rows, showCursor, Cursor{})
}

func parseScreenWithCursor(data []byte, cols, rows int, showCursor bool, cursor Cursor) *screen {
	return parseScreenWithTheme(data, cols, rows, showCursor, cursor, nil)
}

func parseScreenWithTheme(data []byte, cols, rows int, showCursor bool, cursor Cursor, theme *terminal.Theme) *screen {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	s := &screen{
		cols: cols, rows: rows, showCursor: showCursor, cursorOn: true,
		top: 0, cursor: cursor, colors: termColorsFor(theme),
	}
	s.clearAll()
	text := string(data)
	for i := 0; i < len(text); {
		ch := text[i]
		switch {
		case ch == 0x1b && i+1 < len(text) && text[i+1] == '[':
			j := i + 2
			for j < len(text) && (text[j] < 0x40 || text[j] > 0x7e) {
				j++
			}
			if j >= len(text) {
				return s
			}
			s.csi(text[i+2:j], text[j])
			i = j + 1
		case ch == 0x1b && i+1 < len(text) && (text[i+1] == ']' || text[i+1] == 'P' || text[i+1] == '_'):
			// OSC/DCS/APC: an OSC 8 opens or closes a hyperlink; the rest is
			// skipped. APC carries Kitty graphics (ESC _ G …, the base64 of an
			// image): skipped whole, its payload would otherwise be drawn as
			// text where a program placed a picture.
			j := i + 2
			for j < len(text) {
				if text[j] == 0x07 {
					j++
					break
				}
				if text[j] == 0x1b && j+1 < len(text) && text[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			if text[i+1] == ']' {
				payload := text[i+2 : min(j, len(text))]
				payload = strings.TrimSuffix(payload, "\x1b\\")
				payload = strings.TrimSuffix(payload, "\x07")
				s.osc(payload)
			}
			i = j
		case ch == 0x1b:
			// A one- or two-byte escape: ESC 7 saves the cursor, ESC ( B
			// designates the ASCII charset, ESC # 8 fills the screen with E.
			// The cell grid ignores them, but it has to skip the whole
			// sequence: an escape with an intermediate byte (0x20–0x2f) ends
			// on the byte after it, and skipping only two bytes left that
			// final byte — the "B" of ESC ( B, which vim and other
			// full-screen programs emit constantly — to be drawn as text.
			i++
			if i < len(text) && text[i] >= 0x20 && text[i] <= 0x2f {
				i++
			}
			i++
		case ch == '\r':
			s.c = 0
			i++
		case ch == '\n':
			s.lineFeed()
			i++
		case ch == '\b':
			if s.c > 0 {
				s.c--
			}
			i++
		case ch == '\t':
			s.c = (s.c/8 + 1) * 8
			if s.c >= s.cols {
				s.c = s.cols - 1
			}
			i++
		case ch < 0x20:
			i++
		default:
			r, size := decodeRune(text[i:])
			s.put(r)
			i += size
		}
	}
	return s
}

func decodeRune(s string) (rune, int) {
	r, size := utf8.DecodeRuneInString(s)
	return r, size
}

func (s *screen) clearAll() {
	s.grid = make([][]cell, s.rows)
	for i := range s.grid {
		s.grid[i] = make([]cell, s.cols)
	}
	s.top = 0
	// The clear starts a new replay: its first row is the origin rows recorded
	// against this screen count from.
	s.dropped = 0
}

func (s *screen) lineFeed() {
	if s.r+1 < len(s.grid) {
		s.r++
		return
	}
	s.ensureRow(s.r + 1)
	s.r++
	s.top = len(s.grid) - s.rows
	if s.top < 0 {
		s.top = 0
	}
}

// maxGridRows bounds the replay: the flow shows the screen and a stretch of its
// scrollback, and a longer snapshot is cut at the top.
const maxGridRows = 400

// ensureRow grows the grid to reach row, dropping the oldest rows past the cap so
// the newest stay.
func (s *screen) ensureRow(row int) {
	for len(s.grid) <= row {
		s.grid = append(s.grid, make([]cell, s.cols))
	}
	if len(s.grid) > maxGridRows {
		drop := len(s.grid) - maxGridRows
		s.grid = s.grid[drop:]
		s.dropped += drop
		// The cursor and the saved cursor index into the grid. When the oldest
		// rows go, the rows holding them move up by the same count; without
		// this the cursor pointed past the last row after the 400th line of a
		// long snapshot, and put skipped every later write: the newest rows —
		// the prompt and the line being typed on them — stayed blank.
		s.r -= drop
		s.savedR -= drop
		if s.r < 0 {
			s.r = 0
		}
		if s.savedR < 0 {
			s.savedR = 0
		}
		s.top -= drop
		if s.top < 0 {
			s.top = 0
		}
	}
}

func (s *screen) put(r rune) {
	w := parse.RuneWidth(r)
	if w <= 0 {
		// A combining mark belongs to the cell before the cursor, skipping the
		// second half of a wide rune: "e" + U+0301 is one cell showing "é".
		for c := s.c - 1; c >= 0; c-- {
			if cell := &s.grid[s.r][c]; cell.s != "" {
				cell.s += string(r)
				return
			}
		}
		return
	}
	if s.c+w > s.cols {
		s.c = 0
		s.lineFeed()
	}
	s.ensureRow(s.r)
	if s.r < len(s.grid) && s.c < s.cols {
		s.grid[s.r][s.c] = cell{s: string(r), st: s.st, link: s.link}
		for k := 1; k < w && s.c+k < s.cols; k++ {
			s.grid[s.r][s.c+k] = cell{cont: true} // the wide rune's continuation
		}
	}
	s.c += w
}

func (s *screen) csi(params string, final byte) {
	if strings.HasPrefix(params, "?") {
		// DECSET/DECRST: the modes a program set, which the snapshot replays.
		if final == 'h' || final == 'l' {
			s.decset(csiNums(params[1:]), final == 'h')
		}
		return
	}
	if len(params) > 0 && (params[0] == '=' || params[0] == '>' || params[0] == '<') {
		// Private-parameter sequences change input encoding, not the cell
		// grid: the kitty keyboard protocol's CSI = flags ; mode u sets its
		// flags and CSI > flags u pushes them, and xterm's CSI > 4 ; n m sets
		// modifyOtherKeys. Read as the drawing commands whose final bytes
		// they share (CSI u restores the cursor, CSI m is Select Graphic
		// Rendition), they moved the cursor to the saved position and
		// repainted text: Ghostty's snapshot of a vim screen ends with
		// CSI = 3 ; 1 u, which put the cursor back at the top-left and drew
		// the charset escape after it there.
		return
	}
	nums := csiNums(params)
	first := 1
	if len(nums) > 0 && nums[0] > 0 {
		first = nums[0]
	}
	switch final {
	case 'A':
		s.r -= first
	case 'B':
		s.r += first
	case 'C':
		s.c += first
	case 'D':
		s.c -= first
	case 'E':
		s.r += first
		s.c = 0
	case 'F':
		s.r -= first
		s.c = 0
	case 'G':
		s.c = first - 1
	case 'H', 'f':
		row, col := 1, 1
		if len(nums) > 0 && nums[0] > 0 {
			row = nums[0]
		}
		if len(nums) > 1 && nums[1] > 0 {
			col = nums[1]
		}
		// The snapshot writes the scrollback, then the screen, then puts the cursor
		// at its screen position. That position tells where the screen starts: the
		// content written so far ends at the cursor's line.
		// The screen is the last `rows` lines written; anything above is scrollback.
		if s.top == 0 {
			s.top = (s.r + 1) - s.rows
			if s.top < 0 {
				s.top = 0
			}
		}
		s.r, s.c = s.top+row-1, col-1
	case 'J':
		switch first {
		case 2, 3:
			s.clearAll()
		case 0:
			s.eraseLine(s.r, s.c, s.cols)
			for r := s.r + 1; r < len(s.grid); r++ {
				s.eraseLine(r, 0, s.cols)
			}
		case 1:
			s.eraseLine(s.r, 0, s.c+1)
			for r := 0; r < s.r; r++ {
				s.eraseLine(r, 0, s.cols)
			}
		}
	case 'K':
		switch first {
		case 2:
			s.eraseLine(s.r, 0, s.cols)
		case 0:
			s.eraseLine(s.r, s.c, s.cols)
		case 1:
			s.eraseLine(s.r, 0, s.c+1)
		}
	case 'm':
		s.sgrTokens(strings.Split(params, ";"))
	case 's':
		s.savedR, s.savedC = s.r, s.c
	case 'u':
		s.r, s.c = s.savedR, s.savedC
	case 'q':
		// DECSCUSR is CSI n SP q: the shape a program asks for, as vim shows
		// a bar in insert mode. The blink half of the number is the
		// application's own setting; 0 returns to the default shape.
		if strings.HasSuffix(params, " ") {
			n, _ := strconv.Atoi(strings.TrimSpace(params))
			switch {
			case n == 0:
				s.cursorShapeSet = false
			case n == 3 || n == 4:
				s.cursorShape, s.cursorShapeSet = terminal.CursorUnderline, true
			case n == 5 || n == 6:
				s.cursorShape, s.cursorShapeSet = terminal.CursorBar, true
			case n == 1 || n == 2:
				s.cursorShape, s.cursorShapeSet = terminal.CursorBlock, true
			}
		}
	}
	if s.r < 0 {
		s.r = 0
	}
	if s.r >= len(s.grid) {
		s.r = len(s.grid) - 1
	}
	if s.c < 0 {
		s.c = 0
	}
	if s.c >= s.cols {
		s.c = s.cols - 1
	}
}

func (s *screen) eraseLine(row, from, to int) {
	if row < 0 || row >= len(s.grid) {
		return
	}
	if from < 0 {
		from = 0
	}
	if to > s.cols {
		to = s.cols
	}
	for c := from; c < to; c++ {
		s.grid[row][c] = cell{}
	}
}

func csiNums(params string) []int {
	if params == "" {
		return nil
	}
	parts := strings.Split(params, ";")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			out = append(out, 0)
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			out = append(out, 0)
			continue
		}
		out = append(out, n)
	}
	return out
}

// sgrTokens applies a Select Graphic Rendition parameter list, where one parameter
// may carry colon sub-parameters (4:3 is a curly underline).
func (s *screen) sgrTokens(tokens []string) {
	if len(tokens) == 0 || (len(tokens) == 1 && tokens[0] == "") {
		s.st = sgr{}
		return
	}
	nums := make([]int, 0, len(tokens))
	for _, tk := range tokens {
		main, sub, _ := strings.Cut(tk, ":")
		n, err := strconv.Atoi(main)
		if err != nil {
			n = 0
		}
		nums = append(nums, n)
		// The underline style arrives as "4:3".
		if n == 4 {
			if sub == "" {
				s.st.rule = 1
			} else if v, err := strconv.Atoi(sub); err == nil {
				s.st.rule = v
			}
		}
	}
	for i := 0; i < len(nums); i++ {
		n := nums[i]
		switch {
		case n == 0:
			s.st = sgr{}
		case n == 1:
			s.st.bold = true
		case n == 2:
			s.st.dim = true
		case n == 3:
			s.st.italic = true
		case n == 9:
			s.st.strike = true
		case n == 21:
			s.st.rule = 2 // double underline
		case n == 24:
			s.st.rule = 0
		case n == 29:
			s.st.strike = false
		case n == 59:
			s.st.hasRuleColor = false
		case n == 7:
			s.st.reverse = true
		case n == 22:
			s.st.bold, s.st.dim = false, false
		case n == 23:
			s.st.italic = false

		case n == 27:
			s.st.reverse = false
		case n >= 30 && n <= 37:
			s.st.fg, s.st.hasFg = s.palette16(n-30), true
		case n == 39:
			s.st.hasFg = false
		case n >= 40 && n <= 47:
			s.st.bg, s.st.hasBg = s.palette16(n-40), true
		case n == 49:
			s.st.hasBg = false
		case n >= 90 && n <= 97:
			s.st.fg, s.st.hasFg = s.palette16(n-90+8), true
		case n >= 100 && n <= 107:
			s.st.bg, s.st.hasBg = s.palette16(n-100+8), true
		case n == 58 && i+1 < len(nums):
			var col ui.Color
			if nums[i+1] == 5 && i+2 < len(nums) {
				col = s.palette256(nums[i+2])
				i += 2
			} else if nums[i+1] == 2 && i+4 < len(nums) {
				col = ui.RGBA(uint8(nums[i+2]), uint8(nums[i+3]), uint8(nums[i+4]), 1)
				i += 4
			}
			s.st.ruleColor, s.st.hasRuleColor = col, true
		case (n == 38 || n == 48) && i+1 < len(nums):
			var col ui.Color
			if nums[i+1] == 5 && i+2 < len(nums) {
				col = s.palette256(nums[i+2])
				i += 2
			} else if nums[i+1] == 2 && i+4 < len(nums) {
				col = ui.RGBA(uint8(nums[i+2]), uint8(nums[i+3]), uint8(nums[i+4]), 1)
				i += 4
			}
			if n == 38 {
				s.st.fg, s.st.hasFg = col, true
			} else {
				s.st.bg, s.st.hasBg = col, true
			}
		}
	}
}

// PromptRows is what the shell actually drew for a command's prompt and its
// echoed input, as the rows of the live grid: from the prompt run's first row
// (the OSC 133;B boundary) through the row the cursor was on at OSC 133;C,
// where the input's echo ends. Captured from the terminal mirror at the marker,
// they are the shell's own prompt — starship, powerlevel10k, any PS1 — with its
// glyphs, colours and wrapping, so a finished block can show what was on screen
// instead of drawing a prompt of its own.
//
// promptRow is an absolute replay row, as CursorReplayRow reports it. The
// cursor row at the marker is the fresh line accept-line opened for the
// command's output; when it is blank it is dropped, because it was never a row
// of the prompt. ok is false when the rows are not in this replay (the mirror
// was not bound at the prompt, or the prompt's first rows have scrolled out of
// the replay cap).
func PromptRows(data []byte, size Size, promptRow int, theme *terminal.Theme) ([][]ui.Span, bool) {
	if len(data) == 0 || size.Cols <= 0 || size.Rows <= 0 {
		return nil, false
	}
	sc := parseScreenWithTheme(data, size.Cols, size.Rows, false, Cursor{}, theme)
	lines, cursorRow, _ := sc.Lines()
	from := promptRow - sc.dropped
	if from < 0 {
		// The prompt's first rows are above the replay's cap. Keep the rows
		// that are here, from the top: they are still the shell's own drawing.
		from = 0
	}
	if cursorRow < from || from >= len(lines) {
		return nil, false
	}
	if cursorRow >= len(lines) {
		cursorRow = len(lines) - 1
	}
	if cursorRow > from && rowBlank(lines[cursorRow]) {
		cursorRow--
	}
	rows := make([][]ui.Span, 0, cursorRow-from+1)
	for r := from; r <= cursorRow; r++ {
		rows = append(rows, lines[r])
	}
	if len(rows) == 0 {
		return nil, false
	}
	// Resolve the default foreground the way the live rows do: a prompt run
	// with no SGR colour of its own is drawn in the terminal's text colour, not
	// in whatever colour the UI theme happens to give unstyled text.
	applyDefaults(rows, sc.colors.Foreground)
	return rows, true
}

// rowBlank reports a row with no text on it. Its cells may still carry a
// background; those are not prompt text.
func rowBlank(spans []ui.Span) bool {
	for _, sp := range spans {
		if strings.TrimSpace(sp.Text) != "" {
			return false
		}
	}
	return true
}

// Lines renders the grid as spans, one slice per row, with the cursor cell drawn
// as a reversed block; cursorRow/cursorCol say where it is.
func (s *screen) Lines() (lines [][]ui.Span, cursorRow, cursorCol int) {
	lines = make([][]ui.Span, 0, len(s.grid))
	for r := 0; r < len(s.grid); r++ {
		lines = append(lines, s.rowSpans(r, 0, -1, ui.Color{}))
	}
	return lines, s.r, s.c
}

// rowSpans renders one row, painting the cells from colFrom through colTo with
// bg behind the text, as Terminal fills its selection before the glyphs.
// colFrom > colTo paints nothing.
func (s *screen) rowSpans(r, colFrom, colTo int, bg ui.Color) []ui.Span {
	var spans []ui.Span
	var run strings.Builder
	var runSt sgr
	flush := func() {
		if run.Len() == 0 {
			return
		}
		spans = append(spans, runSt.span(run.String(), s.colors))
		run.Reset()
	}
	for c := 0; c < s.cols; c++ {
		cl := s.grid[r][c]
		st := cl.st
		if s.showCursor && s.cursorOn && r == s.r && c == s.c {
			// A block cursor uses the terminal's cursor color. The other
			// shapes are represented by the closest span decoration; the
			// live cell overlay keeps the grid independent of the plugin.
			if s.cursor.Style == terminal.CursorUnderline {
				st.rule = 1
				st.ruleColor = s.cursorColor()
				st.hasRuleColor = true
			} else {
				st.reverse = !st.reverse
			}
			if cl.s == "" {
				cl.s = " "
			}
		}
		if bg.A != 0 && colFrom <= colTo && c >= colFrom && c <= colTo {
			st.bg, st.hasBg = bg, true
		}
		if st != runSt {
			flush()
			runSt = st
		}
		switch {
		case cl.cont:
			// nothing: the wide rune before it owns both columns
		case cl.s == "":
			run.WriteByte(' ')
		default:
			run.WriteString(cl.s)
		}
	}
	flush()
	// Drop trailing blanks so a row does not paint the whole width, but keep a
	// styled blank: that is the cursor, a program's coloured background, or the
	// selection.
	for len(spans) > 0 {
		last := spans[len(spans)-1]
		if strings.TrimSpace(last.Text) != "" || last.Background != (ui.Color{}) {
			break
		}
		spans = spans[:len(spans)-1]
	}
	if len(spans) > 0 {
		last := &spans[len(spans)-1]
		if last.Background == (ui.Color{}) {
			last.Text = strings.TrimRight(last.Text, " ")
		}
	}
	return spans
}

func (s *screen) cursorColor() ui.Color {
	if s.cursor.Color != (ui.Color{}) {
		return s.cursor.Color
	}
	return s.colors.Foreground
}

// span is the text element for one run of cells with the same rendition.
func (st sgr) span(text string, colors TerminalColors) ui.Span {
	fg, bg := st.fg, st.bg
	hasFg, hasBg := st.hasFg, st.hasBg
	if st.reverse {
		fg, bg = bg, fg
		hasFg, hasBg = hasBg, hasFg
		// Reverse video with no explicit colour swaps the terminal's own
		// foreground and background (SGR 7 on the default colours is a
		// foreground-on-background block).
		termFg, termBg := colors.Foreground, colors.Background
		if !hasFg {
			fg, hasFg = termBg, true
		}
		if !hasBg {
			bg, hasBg = termFg, true
		}
	}
	// The family is left to the grid's painter (spanFont), which uses the
	// terminal's configured font: fixing "monospace" here made the rows draw
	// with a different family than the terminal plugin, and every column moved.
	sp := ui.Span{Text: text}
	if hasFg {
		col := fg
		if st.dim {
			col = col.Alpha(0.6)
		}
		sp.Color = col
	} else if st.dim {
		// SGR 2 with no colour of its own dims the theme's terminal foreground.
		termFg := colors.Foreground
		sp.Color = termFg.Alpha(0.6)
	}
	if hasBg {
		sp.Background = bg
	}
	if st.bold {
		sp.Weight = 600
	}
	if st.italic {
		sp.Italic = true
	}
	if st.rule != 0 {
		sp.Underline = true
		if st.rule == 3 {
			sp.WavyUnderline = true
		}
	}
	if st.strike {
		sp.Strikethrough = true
	}
	if st.hasRuleColor {
		sp.DecorationColor = st.ruleColor
	}
	return sp
}

// palette16 is the grid's 16-colour palette from the terminal theme. Entries
// 8-15 are the bright half, which a program reaches with SGR 90-97.
func (s *screen) palette16(i int) ui.Color {
	if i < 0 || i >= len(s.colors.Palette) {
		// Callers only ask for 0-15; the default foreground is the sane answer.
		return s.colors.Palette[7]
	}
	return s.colors.Palette[i]
}

// palette256 resolves an xterm-256 index: the 16 theme colors, the 6×6×6 cube,
// then the 24-step gray ramp.
func (s *screen) palette256(n int) ui.Color {
	switch {
	case n < 0:
		return s.palette16(7)
	case n < 16:
		return s.palette16(n)
	case n < 232:
		levels := [6]uint8{0, 95, 135, 175, 215, 255}
		n -= 16
		return ui.RGBA(levels[n/36], levels[(n/6)%6], levels[n%6], 1)
	default:
		if n > 255 {
			n = 255
		}
		v := uint8(8 + 10*(n-232))
		return ui.RGBA(v, v, v, 1)
	}
}

// mouseMode is the pointer reporting a program asked for (DECSET 1000/1002/1003),
// plus the encoding it expects (1006 SGR, or the legacy X10 form for 9).
type mouseMode struct {
	press  bool // 1000: press and release
	drag   bool // 1002: press, release and motion with a button held
	motion bool // 1003: all motion
	sgr    bool // 1006: ESC[<b;x;yM
	x10    bool // 9: ESC[M<byte><byte><byte>
}

// Enabled reports whether the program wants pointer events at all.
func (m mouseMode) Enabled() bool { return m.press || m.drag || m.motion }

// decset applies one DEC private mode set (on) or reset (off).
func (s *screen) decset(nums []int, on bool) {
	for _, n := range nums {
		switch n {
		case 9:
			s.mouse.x10 = on
		case 25:
			s.cursorOn = on
		case 1000:
			s.mouse.press = on
		case 1002:
			s.mouse.drag = on
		case 1003:
			s.mouse.motion = on
		case 1006:
			s.mouse.sgr = on
		case 47, 1047, 1049:
			s.alt = on
		}
	}
}

// Mouse returns the pointer reporting the screen's program asked for.
func (s *screen) Mouse() mouseMode { return s.mouse }

// osc handles the operating-system commands the replay needs: an OSC 8 opens or
// closes a hyperlink, and every other OSC is ignored.
func (s *screen) osc(payload string) {
	if !strings.HasPrefix(payload, "8;") {
		return
	}
	rest := payload[2:]
	parts := strings.SplitN(rest, ";", 2)
	if len(parts) < 2 {
		return
	}
	uri := strings.TrimSpace(parts[1])
	if uri == "" {
		s.link = 0 // the closing OSC 8
		return
	}
	s.links = append(s.links, uri)
	s.link = int32(len(s.links)) // cells store 1 + the index
}

// LinkAt returns the hyperlink of a cell, if it has one.
func (s *screen) LinkAt(row, col int) (string, bool) {
	if row < 0 || row >= len(s.grid) || col < 0 || col >= s.cols {
		return "", false
	}
	id := s.grid[row][col].link
	if id <= 0 || int(id) > len(s.links) {
		return "", false
	}
	return s.links[id-1], true
}
