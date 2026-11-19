package terminal

// The terminal is its own text-input client.
//
// A client is how the terminal asks an input method for a keyboard. A phone
// host shows (or re-shows) the on-screen keyboard only for a focused element
// that owns a ui.TextInputClient: MyGo counts the request for a focused
// client and passes the count to the host, so a terminal that sets only a
// TextCaret can never bring back a keyboard the user dismissed. The same
// client serves desktop and phones; desktop key events still arrive as keys,
// and only text and composition are diverted here, so typing, the Edit menu,
// pointer selection and accessibility keep their paths. The keyboard traits
// (Keyboard, Capitalize, Autocorrect, ReturnKey) wait for the MyGo version
// that has them; the client alone is what the keyboard gate counts.
//
// The document is a read-only snapshot of the visible grid, not an editable
// buffer: a terminal has no text model to edit, and no API exposes the
// scrollback as rows. It is built on demand from vt.Terminal.RowText, row by
// row, in UTF-16 offsets (what native text services count) with a
// rune-to-cell table, so an offset can be turned back into a cell for a
// candidate window or a touch handle. The composition, or preedit, is a
// virtual insertion at the caret: it counts in every range and offset while
// it is shown, but nothing is written to the shell until it is committed
// through ReplaceText or UnmarkText, which both end in view.typed, the one
// path text takes to the PTY.

import (
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/ui/terminal/internal/vt"
)

// The client is the view pointer itself: it must stay the same for the life
// of the terminal view, or the framework releases it (unmarking and
// restarting composition) on every frame.
var _ ui.TextInputClient = (*view)(nil)

// textRow is a row of the snapshot: its UTF-16 offset in the grid part of
// the document, and each rune with the cell it starts in. Runes of one
// grapheme cluster share a cell.
type textRow struct {
	start int
	runes []rune
	cols  []int
}

// textDoc is a snapshot of the visible grid with the composition, if any,
// inserted at the caret. caret is the caret's offset in the grid part; mark
// is the composition's range in the document.
type textDoc struct {
	text   string
	rows   []textRow
	cols   int
	caret  int
	mark   ui.TextInputRange
	marked bool
	n      int // the document's length in UTF-16 units
	// caretRow and caretCol are the cell the caret occupies, where a
	// composition shows.
	caretRow, caretCol int
}

// textDoc builds the snapshot. It is O(viewport) and allocates only the
// rows' rune and column slices; callers build it on demand, never per frame.
func (v *view) textDoc() textDoc {
	t := v.t
	t.mu.Lock()
	term := t.term
	if term == nil {
		t.mu.Unlock()
		return textDoc{}
	}
	cols, rows := v.cols, v.rows
	if cols <= 0 || rows <= 0 {
		cols, rows = term.Size()
	}
	if rows < 0 {
		rows = 0
	}
	d := textDoc{cols: cols, rows: make([]textRow, 0, rows)}
	var b strings.Builder
	off := 0
	for y := 0; y < rows; y++ {
		rt, rc := term.RowText(y)
		// Trailing blanks are not text; interior ones are, so that a
		// caret in the middle of a row keeps its offset.
		for len(rt) > 0 && rt[len(rt)-1] == ' ' {
			rt, rc = rt[:len(rt)-1], rc[:len(rc)-1]
		}
		if y > 0 {
			b.WriteByte('\n')
			off++
		}
		d.rows = append(d.rows, textRow{start: off, runes: rt, cols: rc})
		b.WriteString(string(rt))
		for _, r := range rt {
			off += utf16RuneLen(r)
		}
	}
	grid := b.String()
	// The caret is the cursor's cell while the view is at the bottom, which
	// is where output lands and may have moved past the last frame; when
	// the viewport is scrolled back it is the cell the last frame showed.
	caretCol, caretRow := v.caretCell[0], v.caretCell[1]
	if term.AtBottom() {
		caretCol, caretRow = term.Cursor()
	}
	t.mu.Unlock()

	d.caretRow = min(max(caretRow, 0), max(len(d.rows)-1, 0))
	d.caretCol = max(caretCol, 0)
	d.caret = d.caretRowStart() + d.caretRowOffset()

	markLen := ui.UTF16Len(v.preedit)
	if markLen > 0 {
		at := ui.UTF16ByteOffset(grid, d.caret)
		d.text = grid[:at] + v.preedit + grid[at:]
		d.mark = ui.TextInputRange{Start: d.caret, End: d.caret + markLen}
		d.marked = true
		d.n = off + markLen
	} else {
		d.text = grid
		d.n = off
	}
	return d
}

// caretRowStart is the UTF-16 offset of the caret row in the grid.
func (d textDoc) caretRowStart() int {
	if d.caretRow < 0 || d.caretRow >= len(d.rows) {
		return 0
	}
	return d.rows[d.caretRow].start
}

// caretRowOffset is how far into its row the caret sits, in UTF-16 units.
func (d textDoc) caretRowOffset() int {
	if d.caretRow < 0 || d.caretRow >= len(d.rows) {
		return 0
	}
	r := d.rows[d.caretRow]
	off := 0
	for i, c := range r.cols {
		if c >= d.caretCol {
			break
		}
		off += utf16RuneLen(r.runes[i])
	}
	return off
}

// gridOf turns a document offset into the offset of the grid part: an
// offset inside the composition is the caret, and one after it moves back
// by the composition's length.
func (d textDoc) gridOf(full int) int {
	if !d.marked {
		return full
	}
	switch {
	case full < d.mark.Start:
		return full
	case full <= d.mark.End:
		return d.caret
	}
	return full - (d.mark.End - d.mark.Start)
}

// fullOf turns a grid offset into a document offset: the composition takes
// no room before the caret, and is inserted after it.
func (d textDoc) fullOf(grid int) int {
	if d.marked && grid > d.caret {
		return grid + d.mark.End - d.mark.Start
	}
	return grid
}

// cellAtOffset returns the cell a document offset starts in, or false for
// an empty document.
func (d textDoc) cellAtOffset(full int) (col, row int, ok bool) {
	if len(d.rows) == 0 {
		return 0, 0, false
	}
	if d.marked && full >= d.mark.Start && full <= d.mark.End {
		return d.caretCol, d.caretRow, true
	}
	grid := d.gridOf(full)
	// The row is the last one starting at or before the offset.
	r := sort.Search(len(d.rows), func(i int) bool { return d.rows[i].start > grid }) - 1
	r = max(r, 0)
	rd := d.rows[r]
	off := rd.start
	for i, c := range rd.cols {
		n := utf16RuneLen(rd.runes[i])
		if grid < off+n {
			return c, r, true
		}
		off += n
	}
	// The end of the row: the cell after its last rune, and the start of
	// the next row past the newline.
	if grid > off && r+1 < len(d.rows) {
		return d.rows[r+1].colsAt(0), r + 1, true
	}
	col = 0
	if n := len(rd.cols); n > 0 {
		col = min(rd.cols[n-1]+1, max(d.cols-1, 0))
	}
	return col, r, true
}

// colsAt returns the column of the row's i-th rune, or 0 for none.
func (r textRow) colsAt(i int) int {
	if i < 0 || i >= len(r.cols) {
		return 0
	}
	return r.cols[i]
}

// offsetAtCell returns the document offset a cell starts at, for a point.
func (d textDoc) offsetAtCell(row, col int) int {
	if len(d.rows) == 0 {
		return 0
	}
	row = min(max(row, 0), len(d.rows)-1)
	r := d.rows[row]
	off := r.start
	for i, c := range r.cols {
		if c >= col {
			return d.fullOf(off)
		}
		off += utf16RuneLen(r.runes[i])
	}
	return d.fullOf(off)
}

func utf16RuneLen(r rune) int {
	if n := utf16.RuneLen(r); n > 0 {
		return n
	}
	return 1
}

// normalizeRange orders a range and clears its negative ends; the adapter
// already does this for the native calls, but a test or a host may not.
func normalizeRange(r ui.TextInputRange) ui.TextInputRange {
	if r.Start > r.End {
		r.Start, r.End = r.End, r.Start
	}
	return ui.TextInputRange{Start: max(r.Start, 0), End: max(r.End, 0)}
}

// clampRange fits a normalized range into a document of n units.
func clampRange(r ui.TextInputRange, n int) ui.TextInputRange {
	r.Start = min(r.Start, n)
	r.End = min(r.End, n)
	if r.End < r.Start {
		r.End = r.Start
	}
	return r
}

// TextForRange returns the document's text in a range, clamped to it, and
// the actual range. The length query iOS makes (a range ending at MaxInt32)
// gets the real length here: an empty answer would say the terminal has no
// text and blind every input method that asks for context.
func (v *view) TextForRange(r ui.TextInputRange) (string, ui.TextInputRange) {
	d := v.textDoc()
	r = clampRange(normalizeRange(r), d.n)
	a := ui.UTF16ByteOffset(d.text, r.Start)
	b := ui.UTF16ByteOffset(d.text, r.End)
	return d.text[a:b], r
}

// Selection returns the caret, collapsed, or the composition's caret while
// one is shown. It is never {0,0} on a live view: the caret anchors the
// candidate window and the keyboard's inset.
func (v *view) Selection() ui.TextInputSelection {
	d := v.textDoc()
	pos := d.caret
	if d.marked {
		pos += min(max(v.preeditCaret, 0), d.mark.End-d.mark.Start)
	}
	return ui.TextInputSelection{Range: ui.TextInputRange{Start: pos, End: pos}}
}

// MarkedRange returns the composition's range, if one is shown.
func (v *view) MarkedRange() (ui.TextInputRange, bool) {
	if v.preedit == "" {
		return ui.TextInputRange{}, false
	}
	d := v.textDoc()
	if !d.marked {
		return ui.TextInputRange{}, false
	}
	return d.mark, true
}

// ReplaceText is the host's commit, or an edit it makes in the document.
//
// A nil range is the current composition or selection: it commits text
// through v.typed, the one path to the PTY, and clears the composition. An
// empty text replacing a range inside the composition trims the
// composition and writes nothing. An empty text replacing document text
// becomes Backspace keys before the caret or Delete keys after it, one per
// rune and capped: a terminal's screen is the shell's, and an input method's
// deleteSurroundingText must reach it as keys, not as an edit of a buffer
// the terminal does not have. Any other non-empty text with a range is
// ignored, as screen cells cannot be rewritten: that is what keeps an
// accessibility setValue (which replaces the whole span) from typing the
// screen into the shell. The one exception is the composition's own range,
// which a host may pass as the replacement when it commits a composition.
func (v *view) ReplaceText(r *ui.TextInputRange, text string) {
	if r == nil {
		v.typed(text)
		return
	}
	d := v.textDoc()
	rr := clampRange(normalizeRange(*r), d.n)
	if text == "" {
		if d.marked && rr.Start >= d.mark.Start && rr.End <= d.mark.End {
			// An erase inside the composition: the composition is the
			// document there, so edit it and write nothing to the shell.
			v.splicePreedit(rr.Start-d.mark.Start, rr.End-d.mark.Start, "")
			return
		}
		v.deleteAround(d, rr)
		return
	}
	if d.marked && rr.Start == d.mark.Start && rr.End == d.mark.End {
		// The host committed the composition, naming its marked range as
		// the replacement range, as AppKit does.
		v.typed(text)
		return
	}
	if rr.Start == rr.End && rr.Start == d.caret {
		v.typed(text)
	}
}

// maxDeleteKeys caps how many keys one deleteSurroundingText can send: a
// terminal has no buffer to rewrite, and deleting a whole screen would be
// a burst of keys the user did not press.
const maxDeleteKeys = 64

// deleteAround sends the keys that delete a document range: Backspace for
// the part before the composition (or the caret), Delete for the part after
// it. Everything inside a composition is the composition's, and is left to
// its own edits.
func (v *view) deleteAround(d textDoc, r ui.TextInputRange) {
	markStart, markEnd := d.caret, d.caret
	if d.marked {
		markStart, markEnd = d.mark.Start, d.mark.End
	}
	before := ui.TextInputRange{Start: r.Start, End: min(r.End, markStart)}
	after := ui.TextInputRange{Start: max(r.Start, markEnd), End: r.End}
	back, del := 0, 0
	if before.End > before.Start {
		back = utf16RuneCount(d.text, before)
	}
	if after.End > after.Start {
		del = utf16RuneCount(d.text, after)
	}
	if n := back + del; n > maxDeleteKeys {
		back = min(back, maxDeleteKeys)
		del = min(del, maxDeleteKeys-back)
	}
	for range back {
		v.sendKey(vt.KeyEvent{Action: vt.KeyPress, Key: vt.KeyBackspace})
	}
	for range del {
		v.sendKey(vt.KeyEvent{Action: vt.KeyPress, Key: vt.KeyDelete})
	}
}

// utf16RuneCount counts the runes of a UTF-16 range of s.
func utf16RuneCount(s string, r ui.TextInputRange) int {
	a, b := ui.UTF16ByteOffset(s, r.Start), ui.UTF16ByteOffset(s, r.End)
	if b < a {
		a, b = b, a
	}
	return len([]rune(s[a:b]))
}

// SetMarkedText shows an input method's composition. It stores the text and
// its caret and never writes to the PTY; an empty text cancels it. The
// replacement range is advisory: the composition always sits at the caret,
// which is where the terminal shows it.
func (v *view) SetMarkedText(_ *ui.TextInputRange, text string, selected ui.TextInputRange) {
	if p := v.pending; p != nil && p.sent {
		// A key sent as Alt, or a repeat sent by keyDown: anything the
		// input method makes of it is dropped, as macOS's dead key of a
		// key the terminal already sent.
		return
	}
	if text == "" {
		v.preedit, v.preeditCaret, v.pending = "", 0, nil
		return
	}
	n := ui.UTF16Len(text)
	sel := normalizeRange(selected)
	caret := min(sel.End, n)
	// Round an offset inside a surrogate pair down to its rune.
	caret = ui.UTF16Len(text[:ui.UTF16ByteOffset(text, caret)])
	v.preedit, v.preeditCaret, v.pending = text, caret, nil
}

// UnmarkText ends a composition, committing its text if it still shows one.
// A ReplaceText commit already cleared it, so this never commits twice.
func (v *view) UnmarkText() {
	if v.preedit == "" {
		return
	}
	v.typed(v.preedit)
}

// splicePreedit replaces a UTF-16 range of the composition with text.
func (v *view) splicePreedit(start, end int, text string) {
	a := ui.UTF16ByteOffset(v.preedit, start)
	b := ui.UTF16ByteOffset(v.preedit, end)
	v.preedit = v.preedit[:a] + text + v.preedit[b:]
	v.preeditCaret = ui.UTF16Len(v.preedit[:a]) + ui.UTF16Len(text)
	if v.preeditCaret > ui.UTF16Len(v.preedit) {
		v.preeditCaret = ui.UTF16Len(v.preedit)
	}
}

// deletePreedit deletes one rune of the composition, before its caret or
// after it, for a host whose erase key reaches the terminal while the
// composition shows.
func (v *view) deletePreedit(backwards bool) {
	if v.preedit == "" {
		return
	}
	runes := []rune(v.preedit)
	at := len([]rune(v.preedit[:ui.UTF16ByteOffset(v.preedit, v.preeditCaret)]))
	switch {
	case backwards && at > 0:
		runes = append(runes[:at-1], runes[at:]...)
		at--
	case !backwards && at < len(runes):
		runes = append(runes[:at], runes[at+1:]...)
	}
	v.preedit = string(runes)
	v.preeditCaret = ui.UTF16Len(string(runes[:at]))
}

// BoundsForRange returns the rectangle of the cell a range starts in, in
// DIPs relative to the element, and the range it actually covers. False
// would leave the candidate window at the element's corner, so only an
// empty view answers false.
func (v *view) BoundsForRange(r ui.TextInputRange) (ui.Rect, ui.TextInputRange, bool) {
	d := v.textDoc()
	r = clampRange(normalizeRange(r), d.n)
	if v.scale == 0 || v.cellW == 0 || v.cellH == 0 {
		return ui.Rect{}, r, false
	}
	col, row, ok := d.cellAtOffset(r.Start)
	if !ok {
		return ui.Rect{}, r, false
	}
	cw, ch := float32(v.cellW)/v.scale, float32(v.cellH)/v.scale
	return ui.Rect{
		X: float32(v.ox)/v.scale + float32(col)*cw,
		Y: float32(v.oy)/v.scale + float32(row)*ch,
		W: cw,
		H: ch,
	}, r, true
}

// IndexForPoint returns the document offset of the cell under a point in
// DIPs relative to the element, and false outside the grid.
func (v *view) IndexForPoint(p ui.Point) (int, bool) {
	if v.scale == 0 || v.cellW == 0 || v.cellH == 0 || v.cols <= 0 || v.rows <= 0 {
		return 0, false
	}
	x := p.X*v.scale - float32(v.ox)
	y := p.Y*v.scale - float32(v.oy)
	if x < 0 || y < 0 {
		return 0, false
	}
	col, row := int(x)/v.cellW, int(y)/v.cellH
	if col >= v.cols || row >= v.rows {
		return 0, false
	}
	d := v.textDoc()
	return d.offsetAtCell(row, col), true
}
