package sessionui

import (
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"golang.org/x/text/width"
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
	// (terminal.Options.Padding left zero), in DIPs. The cursor cell is
	// measured from the padding box, so they place the list beside it.
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

// autocompleteCursorFromText locates the cursor cell from the terminal's text
// and the line being typed. MyGo's terminal does not expose its cursor, but a
// shell keeps the cursor at the end of the typed line, so the last line and
// its width place both the cursor and the start of the command. Text includes
// scrollback; counting from the bottom of the screen keeps the row in the
// viewport when output has scrolled.
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
// list to the room actually available, then clamp into the terminal's screen.
func placeAutocomplete(bounds ui.Rect, cursor autocompleteCursor, cols, rows, suggestions int, popup, ghost bool) autocompletePlacement {
	width := min(bounds.W-2*terminalPaddingX, autocompleteMaxWidth)
	cellW, cellH := autocompleteCellSize(bounds, cols, rows)
	if width <= 0 || cellW <= 0 || cellH <= 0 {
		return autocompletePlacement{}
	}
	screenLeft := bounds.X + terminalPaddingX
	screenRight := bounds.X + bounds.W - terminalPaddingX
	screenTop := bounds.Y + terminalPaddingY
	screenBottom := bounds.Y + bounds.H - terminalPaddingY

	wantRows := 0
	if popup {
		wantRows = min(suggestions, maxVisibleSuggestions)
	}
	upwardRow := min(cursor.StartRow, cursor.Row)
	downwardRow := max(cursor.StartRow, cursor.Row)
	spaceAbove := float32(upwardRow) * cellH
	spaceBelow := float32(max(rows-downwardRow-1, 0)) * cellH

	want := autocompleteAssemblyHeight(wantRows, ghost)
	upward := false
	switch {
	case spaceBelow >= want:
	case spaceAbove >= want:
		upward = true
	case cursor.Row > 2 && spaceAbove >= spaceBelow:
		upward = true
	}

	anchorRow, space := cursor.Row, spaceBelow
	if upward {
		anchorRow, space = upwardRow, spaceAbove
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

	anchorTop := screenTop + float32(anchorRow)*cellH
	anchorBottom := anchorTop + cellH
	var top float32
	if upward {
		top = anchorTop - autocompleteAnchorGap - assemblyHeight
		if top < screenTop {
			top = screenTop
		}
	} else {
		top = anchorBottom + autocompleteAnchorGap
		if top+assemblyHeight > screenBottom {
			top = max(screenBottom-assemblyHeight, screenTop)
		}
	}

	anchorLeft := screenLeft + float32(cursor.Column)*cellW
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
