// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

import (
	"strings"
	"unicode/utf8"
)

// Cell is Terminal::Cell: one grapheme in a row, its renditions, and mosh's
// wide, fallback, and wrap flags. Contents is the UTF-8 payload exactly as
// stock stores it, so the 32-byte combining limit matches byte-for-byte.
type Cell struct {
	Contents   string
	Renditions Renditions
	Wide       bool
	Fallback   bool
	Wrap       bool
}

// NewCell returns a blank cell with the given background color.
func NewCell(background uint32) Cell {
	return Cell{Renditions: NewRenditions(background)}
}

// Reset clears a cell in place, as Cell::reset does.
func (c *Cell) Reset(background uint32) {
	c.Contents = ""
	c.Renditions = NewRenditions(background)
	c.Wide = false
	c.Fallback = false
	c.Wrap = false
}

// Clear drops the cell's contents without touching its renditions or flags.
func (c *Cell) Clear() { c.Contents = "" }

// Empty reports whether no grapheme has been written.
func (c *Cell) Empty() bool { return c.Contents == "" }

// Full reports whether the 32-byte combining-character budget is used up.
func (c *Cell) Full() bool { return len(c.Contents) >= 32 }

// IsBlank reports empty, space, or no-break space.
func (c *Cell) IsBlank() bool {
	return c.Contents == "" || c.Contents == " " || c.Contents == "\xC2\xA0"
}

// ContentsMatch compares the drawn payloads, treating all blanks as equal.
func (c *Cell) ContentsMatch(other *Cell) bool {
	return (c.IsBlank() && other.IsBlank()) || c.Contents == other.Contents
}

// Append writes one rune to the cell. It is the UTF-8 encoding wcrtomb would
// produce in the UTF-8 locale mosh requires.
func (c *Cell) Append(r rune) {
	if r <= 0x7f {
		c.Contents += string(byte(r))
		return
	}
	var buf [utf8.UTFMax]byte
	n := utf8.EncodeRune(buf[:], r)
	c.Contents += string(buf[:n])
}

// Width reports the cell's display width: 1 or 2.
func (c *Cell) Width() int {
	if c.Wide {
		return 2
	}
	return 1
}

// PrintGrapheme appends the cell's drawn form to out.
func (c *Cell) PrintGrapheme(out *strings.Builder) {
	if c.Contents == "" {
		out.WriteByte(' ')
		return
	}
	if c.Fallback {
		out.WriteString("\xC2\xA0")
	}
	out.WriteString(c.Contents)
}

// Row is Terminal::Row. Gen is copied with the row and assigned on
// construction; stock compares it before cells as a cheap identity check.
type Row struct {
	Cells []Cell
	Gen   uint64
}

var rowGeneration uint64

func nextRowGeneration() uint64 {
	rowGeneration++
	return rowGeneration
}

// NewRow returns a blank row of width cells with the given background.
func NewRow(width int, background uint32) *Row {
	cells := make([]Cell, width)
	for i := range cells {
		cells[i] = NewCell(background)
	}
	return &Row{Cells: cells, Gen: nextRowGeneration()}
}

// Clone copies a row but preserves its generation, like a C++ copy.
func (r *Row) Clone() *Row {
	out := &Row{Cells: append([]Cell(nil), r.Cells...), Gen: r.Gen}
	return out
}

// Equal compares exactly as Row::operator== does.
func (r *Row) Equal(other *Row) bool {
	if r == other {
		return true
	}
	if r.Gen != other.Gen || len(r.Cells) != len(other.Cells) {
		return false
	}
	for i := range r.Cells {
		if r.Cells[i] != other.Cells[i] {
			return false
		}
	}
	return true
}

// Wrap reports the wrap flag stored on the last cell.
func (r *Row) Wrap() bool {
	if len(r.Cells) == 0 {
		return false
	}
	return r.Cells[len(r.Cells)-1].Wrap
}

// SetWrap stores the wrap flag on the last cell.
func (r *Row) SetWrap(wrap bool) {
	if len(r.Cells) > 0 {
		r.Cells[len(r.Cells)-1].Wrap = wrap
	}
}

// InsertCell inserts a blank cell at col and drops the last cell.
func (r *Row) InsertCell(col int, background uint32) {
	if col < 0 || col >= len(r.Cells) {
		return
	}
	copy(r.Cells[col+1:], r.Cells[col:])
	r.Cells[col] = NewCell(background)
}

// DeleteCell removes the cell at col and appends a blank one.
func (r *Row) DeleteCell(col int, background uint32) {
	if col < 0 || col >= len(r.Cells) {
		return
	}
	copy(r.Cells[col:], r.Cells[col+1:])
	r.Cells[len(r.Cells)-1] = NewCell(background)
}

// Reset clears every cell and advances the row generation.
func (r *Row) Reset(background uint32) {
	r.Gen = nextRowGeneration()
	for i := range r.Cells {
		r.Cells[i].Reset(background)
	}
}

// SavedCursor is Terminal::SavedCursor.
type SavedCursor struct {
	CursorCol, CursorRow int
	Renditions           Renditions
	AutoWrapMode         bool
	OriginMode           bool
}

// NewSavedCursor returns the stock default saved cursor.
func NewSavedCursor() SavedCursor {
	return SavedCursor{AutoWrapMode: true}
}

// Mouse reporting modes, matching stock mosh's enum values because the values
// are emitted in DEC private mode sequences.
type MouseReportingMode int

const (
	MouseReportingNone         MouseReportingMode = 0
	MouseReportingX10          MouseReportingMode = 9
	MouseReportingVT220        MouseReportingMode = 1000
	MouseReportingVT220Hilight MouseReportingMode = 1001
	MouseReportingBtnEvent     MouseReportingMode = 1002
	MouseReportingAnyEvent     MouseReportingMode = 1003
)

// Mouse encoding modes, matching stock mosh's enum values.
type MouseEncodingMode int

const (
	MouseEncodingDefault MouseEncodingMode = 0
	MouseEncodingUTF8    MouseEncodingMode = 1005
	MouseEncodingSGR     MouseEncodingMode = 1006
	MouseEncodingURXVT   MouseEncodingMode = 1015
)

// DrawState is Terminal::DrawState: the cursor, modes, tabs, scroll region,
// and current renditions of one framebuffer.
type DrawState struct {
	width, height int

	cursorCol, cursorRow               int
	combiningCharCol, combiningCharRow int

	defaultTabs bool
	tabs        []bool

	scrollingRegionTopRow, scrollingRegionBottomRow int

	renditions Renditions
	save       SavedCursor

	NextPrintWillWrap bool
	OriginMode        bool
	AutoWrapMode      bool
	InsertMode        bool
	CursorVisible     bool
	ReverseVideo      bool
	BracketedPaste    bool

	MouseReportingMode   MouseReportingMode
	MouseFocusEvent      bool
	MouseAlternateScroll bool
	MouseEncodingMode    MouseEncodingMode

	ApplicationModeCursorKeys bool
}

// NewDrawState returns the stock initial draw state for a width by height
// framebuffer.
func NewDrawState(width, height int) DrawState {
	ds := DrawState{
		width: width, height: height,
		defaultTabs:              true,
		tabs:                     make([]bool, width),
		scrollingRegionBottomRow: height - 1,
		renditions:               NewRenditions(0),
		save:                     NewSavedCursor(),
		AutoWrapMode:             true,
		CursorVisible:            true,
	}
	ds.reinitializeTabs(0)
	return ds
}

func (ds *DrawState) reinitializeTabs(start int) {
	if start < 0 {
		start = 0
	}
	for i := start; i < len(ds.tabs); i++ {
		ds.tabs[i] = i%8 == 0
	}
}

// Size returns the framebuffer width and height.
func (ds *DrawState) Size() (int, int) { return ds.width, ds.height }

// Width returns the width.
func (ds *DrawState) Width() int { return ds.width }

// Height returns the height.
func (ds *DrawState) Height() int { return ds.height }

// Cursor returns the cursor column and row.
func (ds *DrawState) Cursor() (int, int) { return ds.cursorCol, ds.cursorRow }

// SetCursor moves the cursor absolutely without touching wrap state, for
// tests and overlays. It snaps like a normal absolute move.
func (ds *DrawState) SetCursor(col, row int) {
	ds.cursorCol, ds.cursorRow = col, row
	ds.snapCursorToBorder()
	ds.newGrapheme()
	ds.NextPrintWillWrap = false
}

// Renditions returns the current renditions.
func (ds *DrawState) Renditions() Renditions { return ds.renditions }

// SetRenditions replaces the current renditions.
func (ds *DrawState) SetRenditions(r Renditions) { ds.renditions = r }

// SetForegroundColor sets the current foreground color.
func (ds *DrawState) SetForegroundColor(color uint32) {
	ds.renditions.SetForegroundColor(color)
}

// SetBackgroundColor sets the current background color.
func (ds *DrawState) SetBackgroundColor(color uint32) {
	ds.renditions.SetBackgroundColor(color)
}

// SetRendition applies one SGR parameter to the current renditions.
func (ds *DrawState) SetRendition(num int) { ds.renditions.SetRendition(num) }

// CursorIsVisible reports the DEC private mode 25 state.
func (ds *DrawState) CursorIsVisible() bool { return ds.CursorVisible }

// SetCursorVisible sets the DEC private mode 25 state.
func (ds *DrawState) SetCursorVisible(visible bool) { ds.CursorVisible = visible }

// ReverseVideoEnabled reports the DEC private mode 5 state.
func (ds *DrawState) ReverseVideoEnabled() bool { return ds.ReverseVideo }

// BracketedPasteEnabled reports the DEC private mode 2004 state.
func (ds *DrawState) BracketedPasteEnabled() bool { return ds.BracketedPaste }

// MouseReporting returns the DEC mouse reporting mode.
func (ds *DrawState) MouseReporting() MouseReportingMode { return ds.MouseReportingMode }

// MouseFocus returns the DEC private mode 1004 state.
func (ds *DrawState) MouseFocus() bool { return ds.MouseFocusEvent }

// MouseEncoding returns the DEC mouse encoding mode.
func (ds *DrawState) MouseEncoding() MouseEncodingMode { return ds.MouseEncodingMode }

// BackgroundRendition returns the background color programs set.
func (ds *DrawState) BackgroundRendition() uint32 { return ds.renditions.Background }

func (ds *DrawState) newGrapheme() {
	ds.combiningCharCol = ds.cursorCol
	ds.combiningCharRow = ds.cursorRow
}

func (ds *DrawState) snapCursorToBorder() {
	if ds.cursorRow < ds.limitTop() {
		ds.cursorRow = ds.limitTop()
	}
	if ds.cursorRow > ds.limitBottom() {
		ds.cursorRow = ds.limitBottom()
	}
	if ds.cursorCol < 0 {
		ds.cursorCol = 0
	}
	if ds.cursorCol >= ds.width {
		ds.cursorCol = ds.width - 1
	}
}

// MoveRow is DrawState::move_row.
func (ds *DrawState) MoveRow(n int, relative bool) {
	if relative {
		ds.cursorRow += n
	} else {
		ds.cursorRow = n + ds.limitTop()
	}
	ds.snapCursorToBorder()
	ds.newGrapheme()
	ds.NextPrintWillWrap = false
}

// MoveCol is DrawState::move_col.
func (ds *DrawState) MoveCol(n int, relative, implicit bool) {
	if implicit {
		ds.newGrapheme()
	}
	if relative {
		ds.cursorCol += n
	} else {
		ds.cursorCol = n
	}
	if implicit {
		ds.NextPrintWillWrap = ds.cursorCol >= ds.width
	}
	ds.snapCursorToBorder()
	if !implicit {
		ds.newGrapheme()
		ds.NextPrintWillWrap = false
	}
}

// SetTab marks the cursor column as a tab stop.
func (ds *DrawState) SetTab() {
	if ds.cursorCol >= 0 && ds.cursorCol < len(ds.tabs) {
		ds.tabs[ds.cursorCol] = true
	}
}

// ClearTab removes one tab stop.
func (ds *DrawState) ClearTab(col int) {
	if col >= 0 && col < len(ds.tabs) {
		ds.tabs[col] = false
	}
}

// ClearDefaultTabs stops reinitializing new columns as default tab stops.
func (ds *DrawState) ClearDefaultTabs() { ds.defaultTabs = false }

// NextTab returns the column of the count-th next tab stop, or -1.
func (ds *DrawState) NextTab(count int) int {
	if count >= 0 {
		for i := ds.cursorCol + 1; i < ds.width; i++ {
			if ds.tabs[i] {
				count--
				if count == 0 {
					return i
				}
			}
		}
		return -1
	}
	for i := ds.cursorCol - 1; i > 0; i-- {
		if ds.tabs[i] {
			count++
			if count == 0 {
				return i
			}
		}
	}
	return 0
}

// SetScrollingRegion sets the absolute top and bottom rows.
func (ds *DrawState) SetScrollingRegion(top, bottom int) {
	if ds.height < 1 {
		return
	}
	ds.scrollingRegionTopRow = top
	ds.scrollingRegionBottomRow = bottom
	if ds.scrollingRegionTopRow < 0 {
		ds.scrollingRegionTopRow = 0
	}
	if ds.scrollingRegionBottomRow >= ds.height {
		ds.scrollingRegionBottomRow = ds.height - 1
	}
	if ds.scrollingRegionBottomRow < ds.scrollingRegionTopRow {
		ds.scrollingRegionBottomRow = ds.scrollingRegionTopRow
	}
	if ds.OriginMode {
		ds.snapCursorToBorder()
		ds.newGrapheme()
	}
}

// ScrollingRegion returns the top and bottom rows.
func (ds *DrawState) ScrollingRegion() (int, int) {
	return ds.scrollingRegionTopRow, ds.scrollingRegionBottomRow
}

func (ds *DrawState) limitTop() int {
	if ds.OriginMode {
		return ds.scrollingRegionTopRow
	}
	return 0
}

func (ds *DrawState) limitBottom() int {
	if ds.OriginMode {
		return ds.scrollingRegionBottomRow
	}
	return ds.height - 1
}

// SaveCursor is DrawState::save_cursor.
func (ds *DrawState) SaveCursor() {
	ds.save = SavedCursor{
		CursorCol:    ds.cursorCol,
		CursorRow:    ds.cursorRow,
		Renditions:   ds.renditions,
		AutoWrapMode: ds.AutoWrapMode,
		OriginMode:   ds.OriginMode,
	}
}

// RestoreCursor is DrawState::restore_cursor.
func (ds *DrawState) RestoreCursor() {
	ds.cursorCol = ds.save.CursorCol
	ds.cursorRow = ds.save.CursorRow
	ds.renditions = ds.save.Renditions
	ds.AutoWrapMode = ds.save.AutoWrapMode
	ds.OriginMode = ds.save.OriginMode
	ds.snapCursorToBorder()
	ds.newGrapheme()
}

// ClearSavedCursor resets the saved cursor.
func (ds *DrawState) ClearSavedCursor() { ds.save = NewSavedCursor() }

// Resize is DrawState::resize.
func (ds *DrawState) Resize(width, height int) {
	if ds.width != width || ds.height != height {
		ds.scrollingRegionTopRow = 0
		ds.scrollingRegionBottomRow = height - 1
	}

	oldWidth := ds.width
	if width < len(ds.tabs) {
		ds.tabs = ds.tabs[:width]
	} else if width > len(ds.tabs) {
		ds.tabs = append(ds.tabs, make([]bool, width-len(ds.tabs))...)
	}
	if ds.defaultTabs {
		ds.reinitializeTabs(oldWidth)
	}

	ds.width = width
	ds.height = height
	ds.snapCursorToBorder()

	if ds.combiningCharCol >= ds.width || ds.combiningCharRow >= ds.height {
		ds.combiningCharCol = -1
		ds.combiningCharRow = -1
	}
}

// Equal compares only the fields stock's DrawState::operator== compares: the
// fields that affect the display frame.
func (ds *DrawState) Equal(other *DrawState) bool {
	return ds.width == other.width && ds.height == other.height &&
		ds.cursorCol == other.cursorCol && ds.cursorRow == other.cursorRow &&
		ds.CursorVisible == other.CursorVisible &&
		ds.ReverseVideo == other.ReverseVideo &&
		ds.renditions == other.Renditions() &&
		ds.BracketedPaste == other.BracketedPaste &&
		ds.MouseReportingMode == other.MouseReportingMode &&
		ds.MouseFocusEvent == other.MouseFocusEvent &&
		ds.MouseAlternateScroll == other.MouseAlternateScroll &&
		ds.MouseEncodingMode == other.MouseEncodingMode
}

// Framebuffer is Terminal::Framebuffer. Rows are pointers so the display code
// can preserve stock mosh's identity shortcut; Clone deep-copies them.
type Framebuffer struct {
	Rows             []*Row
	IconName         []rune
	WindowTitle      []rune
	Clipboard        []rune
	BellCount        uint
	TitleInitialized bool
	DS               DrawState
}

// NewFramebuffer returns a width by height blank framebuffer.
func NewFramebuffer(width, height int) *Framebuffer {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	fb := &Framebuffer{
		Rows: make([]*Row, height),
		DS:   NewDrawState(width, height),
	}
	blank := NewRow(width, 0)
	for i := range fb.Rows {
		// Stock constructs one shared blank row; clone it so rows have the
		// same generation without aliasing mutations.
		fb.Rows[i] = blank.Clone()
	}
	return fb
}

// Clone deep-copies a framebuffer, preserving row generations.
func (f *Framebuffer) Clone() *Framebuffer {
	out := &Framebuffer{
		Rows:             make([]*Row, len(f.Rows)),
		IconName:         append([]rune(nil), f.IconName...),
		WindowTitle:      append([]rune(nil), f.WindowTitle...),
		Clipboard:        append([]rune(nil), f.Clipboard...),
		BellCount:        f.BellCount,
		TitleInitialized: f.TitleInitialized,
		DS:               f.DS,
	}
	out.DS.tabs = append([]bool(nil), f.DS.tabs...)
	for i, row := range f.Rows {
		out.Rows[i] = row.Clone()
	}
	return out
}

// Row returns a row, with -1 meaning the cursor row.
func (f *Framebuffer) Row(row int) *Row {
	if row == -1 {
		row = f.DS.cursorRow
	}
	return f.Rows[row]
}

// Cell returns a cell, with -1 meaning the cursor's.
func (f *Framebuffer) Cell(row, col int) *Cell {
	if row == -1 {
		row = f.DS.cursorRow
	}
	if col == -1 {
		col = f.DS.cursorCol
	}
	return &f.Rows[row].Cells[col]
}

// ResetCell clears one cell using the current background.
func (f *Framebuffer) ResetCell(cell *Cell) { cell.Reset(f.DS.BackgroundRendition()) }

// ResetRow clears every cell in a row.
func (f *Framebuffer) ResetRow(row *Row) { row.Reset(f.DS.BackgroundRendition()) }

// CombiningCell returns the cell the last printed grapheme started in, or nil
// when a resize pushed it offscreen.
func (f *Framebuffer) CombiningCell() *Cell {
	if f.DS.combiningCharCol < 0 || f.DS.combiningCharRow < 0 ||
		f.DS.combiningCharCol >= f.DS.width || f.DS.combiningCharRow >= f.DS.height {
		return nil
	}
	return f.Cell(f.DS.combiningCharRow, f.DS.combiningCharCol)
}

// ApplyRenditionsToCell stamps the current renditions onto a cell; nil means
// the cursor cell.
func (f *Framebuffer) ApplyRenditionsToCell(cell *Cell) {
	if cell == nil {
		cell = f.Cell(-1, -1)
	}
	cell.Renditions = f.DS.renditions
}

// Scroll scrolls the scrolling region by N lines, positive down.
func (f *Framebuffer) Scroll(n int) {
	if n >= 0 {
		f.DeleteLine(f.DS.scrollingRegionTopRow, n)
	} else {
		f.InsertLine(f.DS.scrollingRegionTopRow, -n)
	}
}

// MoveRowsAutoscroll is Framebuffer::move_rows_autoscroll.
func (f *Framebuffer) MoveRowsAutoscroll(rows int) {
	if f.DS.cursorRow < f.DS.scrollingRegionTopRow ||
		f.DS.cursorRow > f.DS.scrollingRegionBottomRow {
		f.DS.MoveRow(rows, true)
		return
	}
	if f.DS.cursorRow+rows > f.DS.scrollingRegionBottomRow {
		n := f.DS.cursorRow + rows - f.DS.scrollingRegionBottomRow
		f.Scroll(n)
		f.DS.MoveRow(-n, true)
	} else if f.DS.cursorRow+rows < f.DS.scrollingRegionTopRow {
		n := f.DS.cursorRow + rows - f.DS.scrollingRegionTopRow
		f.Scroll(n)
		f.DS.MoveRow(-n, true)
	}
	f.DS.MoveRow(rows, true)
}

// InsertLine inserts count blank rows before beforeRow inside the region.
func (f *Framebuffer) InsertLine(beforeRow, count int) {
	if beforeRow < f.DS.scrollingRegionTopRow ||
		beforeRow > f.DS.scrollingRegionBottomRow+1 {
		return
	}
	scroll := f.DS.scrollingRegionBottomRow + 1 - beforeRow
	if count < scroll {
		scroll = count
	}
	if scroll <= 0 {
		return
	}
	start := f.DS.scrollingRegionBottomRow + 1 - scroll
	f.Rows = append(f.Rows[:start], f.Rows[start+scroll:]...)
	blank := f.newRow()
	inserted := make([]*Row, scroll)
	for i := range inserted {
		inserted[i] = blank.Clone()
	}
	f.Rows = append(f.Rows[:beforeRow], append(inserted, f.Rows[beforeRow:]...)...)
}

// DeleteLine deletes count rows at row inside the region.
func (f *Framebuffer) DeleteLine(row, count int) {
	if row < f.DS.scrollingRegionTopRow || row > f.DS.scrollingRegionBottomRow {
		return
	}
	scroll := f.DS.scrollingRegionBottomRow + 1 - row
	if count < scroll {
		scroll = count
	}
	if scroll <= 0 {
		return
	}
	f.Rows = append(f.Rows[:row], f.Rows[row+scroll:]...)
	at := f.DS.scrollingRegionBottomRow + 1 - scroll
	blank := f.newRow()
	inserted := make([]*Row, scroll)
	for i := range inserted {
		inserted[i] = blank.Clone()
	}
	f.Rows = append(f.Rows[:at], append(inserted, f.Rows[at:]...)...)
}

func (f *Framebuffer) newRow() *Row {
	return NewRow(f.DS.width, f.DS.BackgroundRendition())
}

// InsertCell shifts a row right at col.
func (f *Framebuffer) InsertCell(row, col int) {
	f.Row(row).InsertCell(col, f.DS.BackgroundRendition())
}

// DeleteCell shifts a row left at col.
func (f *Framebuffer) DeleteCell(row, col int) {
	f.Row(row).DeleteCell(col, f.DS.BackgroundRendition())
}

// Reset is Framebuffer::reset.
func (f *Framebuffer) Reset() {
	width, height := f.DS.width, f.DS.height
	f.DS = NewDrawState(width, height)
	f.Rows = make([]*Row, height)
	blank := f.newRow()
	for i := range f.Rows {
		f.Rows[i] = blank.Clone()
	}
	f.WindowTitle = nil
	f.Clipboard = nil
	// bell count survives
}

// SoftReset is Framebuffer::soft_reset.
func (f *Framebuffer) SoftReset() {
	f.DS.InsertMode = false
	f.DS.OriginMode = false
	f.DS.CursorVisible = true
	f.DS.ApplicationModeCursorKeys = false
	f.DS.SetScrollingRegion(0, f.DS.height-1)
	f.DS.renditions.SetRendition(0)
	f.DS.ClearSavedCursor()
}

// Resize is Framebuffer::resize.
func (f *Framebuffer) Resize(width, height int) {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	oldHeight, oldWidth := f.DS.height, f.DS.width
	f.DS.Resize(width, height)

	blankRow := f.newRow()
	if oldHeight != height {
		if height < len(f.Rows) {
			f.Rows = f.Rows[:height]
		} else {
			for len(f.Rows) < height {
				f.Rows = append(f.Rows, blankRow.Clone())
			}
		}
	}
	if oldWidth == width {
		return
	}
	for _, row := range f.Rows {
		if row == blankRow {
			break
		}
		row.SetWrap(false)
		if width < len(row.Cells) {
			row.Cells = row.Cells[:width]
		} else if width > len(row.Cells) {
			for len(row.Cells) < width {
				row.Cells = append(row.Cells, NewCell(f.DS.BackgroundRendition()))
			}
		}
	}
}

// PrefixWindowTitle prepends s, preserving icon/title equivalence.
func (f *Framebuffer) PrefixWindowTitle(s []rune) {
	if runesEqual(f.IconName, f.WindowTitle) {
		f.IconName = append(append([]rune(nil), s...), f.IconName...)
	}
	f.WindowTitle = append(append([]rune(nil), s...), f.WindowTitle...)
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Equal reports whether two framebuffers have the same visible state. Stock
// compares row pointers first (unchanged rows are shared); this port compares
// the rows' contents, which is the same predicate after copy-on-write.
func (f *Framebuffer) Equal(other *Framebuffer) bool {
	if f.BellCount != other.BellCount || !f.DS.Equal(&other.DS) {
		return false
	}
	if !runesEqual(f.WindowTitle, other.WindowTitle) || !runesEqual(f.Clipboard, other.Clipboard) {
		return false
	}
	if len(f.Rows) != len(other.Rows) {
		return false
	}
	for i := range f.Rows {
		if !f.Rows[i].Equal(other.Rows[i]) {
			return false
		}
	}
	return true
}
