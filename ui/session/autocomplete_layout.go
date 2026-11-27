package sessionui

import (
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"golang.org/x/text/width"

	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// The autocomplete list floats over the terminal instead of taking rows above
// it. A row inserted above the terminal shrinks its grid, the Session resizes
// the transport, and the shell repaints: a popup that appears on the first
// keystroke and leaves on the next makes the whole screen jump. The original
// app draws the list as a fixed layer anchored at the cursor cell and clamped
// inside the terminal, which is what these measurements reproduce.
const (
	autocompleteRowHeight    = 20
	autocompleteRowGap       = 1
	autocompleteHeaderHeight = 16
	autocompletePadding      = 3
	autocompleteBorder       = 1
	autocompleteAnchorGap    = 4
	autocompleteGhostHeight  = 18
	autocompleteMaxWidth     = 560
	// terminalPaddingX and terminalPaddingY are MyGo's default grid padding
	// (terminal.Options.Padding left zero), in DIPs. They are the fallback
	// estimate for a terminal whose own cell size is not known; the pane's
	// measured cell replaces them whenever a frame has been drawn.
	terminalPaddingX = 6
	terminalPaddingY = 4
)

// autocompleteCursor is the cursor cell in the terminal's grid and the first
// row of the command being typed. An upward list stops above the command's
// first row so it never covers what the user is completing.
type autocompleteCursor struct {
	Row      int
	Column   int
	StartRow int
}

// autocompletePlacement is where the floating autocomplete layers go, in
// window DIPs. Rows is how many suggestions fit; PopupTop and GhostTop are
// the two layers' top edges.
type autocompletePlacement struct {
	Left      float32
	Width     float32
	PopupTop  float32
	GhostTop  float32
	Rows      int
	ShowPopup bool
	ShowGhost bool
}

// autocompleteCursorFromSnapshot locates the cursor cell from the terminal's
// VT snapshot and the line being typed. The snapshot is the emulator's own
// state, so the cursor cell is authoritative: unlike the terminal's Text,
// which mixes scrollback and screen rows indistinguishably, the replay carries
// the cursor escape itself and never confuses a clear that left scrollback in
// place for a screen full of output. The row is the cursor's screen row, not
// its replay row: a clear that merged output into scrollback leaves the prompt
// at the screen's top, and anchoring at the replay row — which counts the
// scrollback in — drops the popup to the bottom of the window or off it. The
// command's first row is derived from the typed input: a shell keeps the
// cursor at the end of the typed line, so the rows the input wraps over count
// back from the cursor's row.
func autocompleteCursorFromSnapshot(snapshot []byte, input string, cols, rows int) autocompleteCursor {
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	col, row, ok := appview.CursorScreenCell(snapshot, appview.Size{Cols: cols, Rows: rows})
	if !ok {
		return autocompleteCursor{}
	}
	// The command being typed starts inputRows above the cursor: the rows the
	// tracked input wraps onto after the first. A keystroke that has not
	// echoed yet leaves both the snapshot cursor and the anchor one cell
	// behind for a frame; the echo catches up on the next output and the
	// anchor corrects itself, which the debounced popup absorbs.
	inputRows := 0
	if width := autocompleteCellWidth(input); width > 0 {
		inputRows = (width - 1) / cols
	}
	cursor := autocompleteCursor{
		Row:      min(max(row, 0), rows-1),
		StartRow: min(max(row-inputRows, 0), rows-1),
		Column:   min(max(col, 0), cols-1),
	}
	return cursor
}

// autocompleteCursorFromText locates the cursor cell from the terminal's text
// and the line being typed, the fallback for a terminal whose snapshot cannot
// be replayed. The last line and its width place both the cursor and the
// start of the command, with the screen-full assumption the old behaviour
// always made: correct for a contiguous screen, inflated when a clear leaves
// scrollback above an unfilled screen.
func autocompleteCursorFromText(text, input string, cols, rows int) autocompleteCursor {
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	before := 0
	for _, line := range lines[:len(lines)-1] {
		before += autocompleteLineRows(line, cols)
	}
	last := lines[len(lines)-1]
	lastWidth := autocompleteCellWidth(last)

	// The shell may not have echoed the last keystroke yet, and Text trims
	// trailing spaces. Count the typed line's echoed suffix so the predicted
	// cursor includes what is still missing.
	typed := []rune(input)
	echoedWidth := 0
	for n := len(typed); n > 0; n-- {
		if strings.HasSuffix(last, string(typed[:n])) {
			echoedWidth = autocompleteCellWidth(string(typed[:n]))
			break
		}
	}
	lineWidth := lastWidth + autocompleteCellWidth(input) - echoedWidth

	// A line that fills the last column leaves the cursor there until the
	// next character wraps: (width-1)/cols is the cursor's row, and a width
	// on a column boundary shows the cursor one cell left.
	cursorOffset := 0
	if lineWidth > 0 {
		cursorOffset = (lineWidth - 1) / cols
	}
	column := lineWidth % cols
	if lineWidth > 0 && column == 0 {
		column = cols - 1
	}

	total := before + autocompleteLineRows(last, cols)
	// The un-echoed tail may take rows the screen has not scrolled for yet;
	// the clamp below keeps the anchor on the visible grid.
	scrolled := max(total-rows, 0)
	cursor := autocompleteCursor{
		Row:      before + cursorOffset - scrolled,
		StartRow: before - scrolled,
		Column:   column,
	}

	cursor.Row = min(max(cursor.Row, 0), rows-1)
	cursor.StartRow = min(max(cursor.StartRow, 0), rows-1)
	cursor.Column = min(max(cursor.Column, 0), cols-1)
	return cursor
}

// autocompleteLineRows is how many physical rows a logical line takes.
func autocompleteLineRows(line string, cols int) int {
	return max(1, (autocompleteCellWidth(line)+cols-1)/cols)
}

// autocompleteCellWidth is the line width in terminal cells: wide and
// fullwidth runes take two, combining marks and joiners take none, and a tab
// advances to the next eight-cell stop.
func autocompleteCellWidth(line string) int {
	cells := 0
	for _, r := range line {
		switch {
		case r == '\t':
			cells += 8 - cells%8
		case r < 0x20 || r == 0x7f:
		case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r),
			r == '\u200b', r == '\u200c', r == '\u200d':
		default:
			kind := width.LookupRune(r).Kind()
			if kind == width.EastAsianWide || kind == width.EastAsianFullwidth {
				cells += 2
			} else {
				cells++
			}
		}
	}
	return cells
}

// autocompletePopupHeight is the bubble's border box: border, padding, the
// header, one gap between every pair of children, and one row per suggestion.
func autocompletePopupHeight(rows int) float32 {
	if rows <= 0 {
		return 0
	}
	return float32(2*autocompleteBorder+2*autocompletePadding+autocompleteHeaderHeight) +
		float32(rows)*float32(autocompleteRowHeight+autocompleteRowGap)
}

// autocompleteAssemblyHeight is the floating stack's height: the popup, the
// ghost hint, and the gap between them.
func autocompleteAssemblyHeight(popupRows int, ghost bool) float32 {
	height := autocompletePopupHeight(popupRows)
	if ghost {
		if height > 0 {
			height += autocompleteAnchorGap
		}
		height += autocompleteGhostHeight
	}
	return height
}

// placeAutocomplete decides where the floating layers go. It mirrors the
// original's cursor-anchored placement: prefer below the input line, flip up
// when the list cannot fit below and above is the better side, shrink the
// list to the room actually available, then clamp inside the terminal's
// visible area.
//
// The anchor is visual, not derived from the terminal's screen rows. The live
// grid is not the terminal's full screen: the flow crops the rows above the
// current prompt into finished blocks and trims the blank rows below the
// cursor, so the cursor's screen row no longer counts down the rows the grid
// shows — the arithmetic that anchored top-down displaced the popup by the
// cropped rows, which put it over the finished blocks at the pane's top
// instead of beside the prompt. The Pane draws the cursor row, so its box is
// exact: cursorRow is that box in window coordinates. viewport is the visible
// area the list may float over — the flow's own box, blocks and grid together,
// because the list is an overlay and covering a finished block behind it is
// what floating means; the grid alone is often a single row tall, far too
// small to hold the list. gridCell is the measured cell the pane paints its
// rows with; a zero cell falls back to dividing the viewport by the grid, the
// estimate the plugin's padding assumptions make, and a zero cursorRow (no
// frame captured yet) falls back to the arithmetic the screen rows imply,
// exact only for an empty flow.
func placeAutocomplete(viewport ui.Rect, cursorRow ui.Rect, cursor autocompleteCursor, cols, rows, suggestions int, popup, ghost bool, gridCell appview.CellGeometry) autocompletePlacement {
	cellW, cellH := gridCell.W, gridCell.H
	insetX, insetY := appview.LiveGridPadding()
	if cellW <= 0 || cellH <= 0 {
		cellW, cellH = autocompleteCellSize(viewport, cols, rows)
	}
	width := min(viewport.W-2*insetX, autocompleteMaxWidth)
	if width <= 0 || cellW <= 0 || cellH <= 0 {
		return autocompletePlacement{}
	}
	screenLeft := viewport.X + insetX
	screenRight := viewport.X + viewport.W - insetX
	screenTop := viewport.Y + insetY
	screenBottom := viewport.Y + viewport.H - insetY

	// The input line's vertical span. The cursor row's box is exact when the
	// Pane has captured one; the command's first row sits inputRows above it
	// because the rows a wrapped input occupies count back from the cursor.
	rowTop, rowBottom := cursorRow.Y, cursorRow.Y+cursorRow.H
	rowCellH := cellH
	if cursorRow.H <= 0 {
		// No captured row yet: the first frames of an empty flow, where the
		// grid fills the viewport and the screen's rows spread across it. The
		// cell that arithmetic needs is the viewport divided by the grid —
		// not the measured cell, which sizes one rendered row of the cropped
		// tail and bears no relation to where screen row N sits in the box.
		_, rowCellH = autocompleteCellSize(viewport, cols, rows)
		rowTop = screenTop + float32(cursor.Row)*rowCellH
		rowBottom = rowTop + rowCellH
	}
	inputRows := max(cursor.Row-cursor.StartRow, 0)
	inputTop := rowTop - float32(inputRows)*rowCellH

	// The space beside the input is what the visible area shows above the
	// command's first row and below the cursor's row. The list may cover the
	// finished blocks behind it — it floats — so the room is measured against
	// the flow's box, not the live grid's. At a shell prompt the cursor sits
	// at the bottom of the visible screen, so the space below is small and
	// the list opens upward beside the prompt.
	spaceAbove := max(inputTop-screenTop, 0)
	spaceBelow := max(screenBottom-rowBottom, 0)

	wantRows := 0
	if popup {
		wantRows = min(suggestions, maxVisibleSuggestions)
	}
	want := autocompleteAssemblyHeight(wantRows, ghost)
	upward := false
	switch {
	case spaceBelow >= want:
	case spaceAbove >= want:
		upward = true
	case spaceAbove >= spaceBelow:
		upward = true
	}

	space := spaceBelow
	if upward {
		space = spaceAbove
	}
	available := space - autocompleteAnchorGap
	fitRows := wantRows
	for fitRows > 1 && autocompleteAssemblyHeight(fitRows, ghost) > available {
		fitRows--
	}
	if wantRows == 0 {
		fitRows = 0
	}
	assemblyHeight := autocompleteAssemblyHeight(fitRows, ghost)

	var top float32
	if upward {
		top = inputTop - autocompleteAnchorGap - assemblyHeight
		if top < screenTop {
			top = screenTop
		}
		// The anchor moves with the scroll: a reader scrolling toward the
		// start carries the input line down past the viewport's end, and the
		// upward placement would otherwise follow it out — floating over
		// whatever sits below the terminal rather than over the flow. Clamp
		// the assembly into the visible area, as the downward side does.
		if top+assemblyHeight > screenBottom {
			top = max(screenBottom-assemblyHeight, screenTop)
		}
	} else {
		top = rowBottom + autocompleteAnchorGap
		if top+assemblyHeight > screenBottom {
			top = max(screenBottom-assemblyHeight, screenTop)
		}
	}

	// Horizontally the cursor column counts cells from the grid's content
	// left edge. The captured row's left is exactly that edge — it is the
	// grid's own box, inset — so the column lands on the cursor's cell rather
	// than on an estimate from the flow's box, whose padding differs from the
	// grid's by the flow's margins.
	gridLeft := cursorRow.X
	if cursorRow.H <= 0 {
		gridLeft = screenLeft
	}
	anchorLeft := gridLeft + float32(cursor.Column)*cellW
	left := min(max(anchorLeft, screenLeft), screenRight-width)

	placement := autocompletePlacement{
		Left:      left,
		Width:     width,
		Rows:      fitRows,
		ShowPopup: fitRows > 0,
		ShowGhost: ghost,
	}
	if ghost {
		if upward {
			placement.GhostTop = top + autocompleteAssemblyHeight(fitRows, false)
			if fitRows > 0 {
				placement.GhostTop += autocompleteAnchorGap
			}
		} else {
			placement.GhostTop = top
		}
	}
	if fitRows > 0 {
		placement.PopupTop = top
		if !upward && ghost {
			placement.PopupTop += autocompleteGhostHeight + autocompleteAnchorGap
		}
	}
	return placement
}

func autocompleteCellSize(bounds ui.Rect, cols, rows int) (float32, float32) {
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	cellW := (bounds.W - 2*terminalPaddingX) / float32(cols)
	cellH := (bounds.H - 2*terminalPaddingY) / float32(rows)
	if cellW <= 0 || cellH <= 0 {
		return 0, 0
	}
	return cellW, cellH
}
