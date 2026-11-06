package sessionui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestAutocompleteCursorFromText(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		input string
		cols  int
		rows  int
		want  autocompleteCursor
	}{
		{
			name:  "prompt on the first line",
			text:  "$ git st",
			input: "git st",
			cols:  80,
			rows:  24,
			want:  autocompleteCursor{Row: 0, Column: 8, StartRow: 0},
		},
		{
			name:  "cleared screen keeps the prompt at the top",
			text:  "$",
			input: "g",
			cols:  80,
			rows:  24,
			want:  autocompleteCursor{Row: 0, Column: 2, StartRow: 0},
		},
		{
			name:  "output above the prompt",
			text:  "hello\nworld\n$ git",
			input: "git",
			cols:  80,
			rows:  24,
			want:  autocompleteCursor{Row: 2, Column: 5, StartRow: 2},
		},
		{
			name:  "a wrapped line puts the cursor on the next row",
			text:  "$ " + strings.Repeat("a", 25),
			input: "",
			cols:  20,
			rows:  6,
			want:  autocompleteCursor{Row: 1, Column: 7, StartRow: 0},
		},
		{
			name:  "a line on a column boundary keeps the cursor in the last cell",
			text:  strings.Repeat("a", 20),
			input: "",
			cols:  20,
			rows:  6,
			want:  autocompleteCursor{Row: 0, Column: 19, StartRow: 0},
		},
		{
			name: "scrollback moves the prompt to the bottom row",
			text: "$ " + strings.Repeat("a", 25) + "\nhello\n" +
				strings.Repeat("line\n", 10) + "$",
			input: "",
			cols:  20,
			rows:  6,
			want:  autocompleteCursor{Row: 5, Column: 1, StartRow: 5},
		},
		{
			name:  "trailing spaces the formatter trimmed still count",
			text:  "$ git",
			input: "git  ",
			cols:  80,
			rows:  24,
			want:  autocompleteCursor{Row: 0, Column: 7, StartRow: 0},
		},
		{
			name:  "wide runes take two cells",
			text:  "用户$ 你",
			input: "你",
			cols:  80,
			rows:  24,
			want:  autocompleteCursor{Row: 0, Column: 8, StartRow: 0},
		},
		{
			name:  "an echo one keystroke behind still predicts the cursor",
			text:  "$ g",
			input: "gi",
			cols:  80,
			rows:  24,
			want:  autocompleteCursor{Row: 0, Column: 4, StartRow: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := autocompleteCursorFromText(tc.text, tc.input, tc.cols, tc.rows)
			if got != tc.want {
				t.Fatalf("cursor = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAutocompleteCellWidth(t *testing.T) {
	cases := map[string]int{
		"git":  3,
		"用户":   4,
		"a\tb": 9,
		"é":   1,
		"a​b":  2,
		"\x1b": 0,
	}
	for text, want := range cases {
		if got := autocompleteCellWidth(text); got != want {
			t.Fatalf("width(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestPlaceAutocompleteFlipsAndClamps(t *testing.T) {
	terminal := ui.Rect{X: 0, Y: 30, W: 720, H: 450}
	const cols, rows = 80, 24
	promptBottom := autocompleteCursor{Row: rows - 1, Column: 5, StartRow: rows - 1}
	promptTop := autocompleteCursor{Row: 0, Column: 5, StartRow: 0}
	cellH := (terminal.H - 2*terminalPaddingY) / rows

	t.Run("prompt at the bottom opens upward above the input line", func(t *testing.T) {
		placement := placeAutocomplete(terminal, promptBottom, cols, rows, 7, true, false)
		if !placement.ShowPopup || placement.Rows != maxVisibleSuggestions {
			t.Fatalf("placement = %+v", placement)
		}
		inputTop := terminal.Y + terminalPaddingY + float32(rows-1)*cellH
		bottom := placement.PopupTop + autocompletePopupHeight(placement.Rows)
		if bottom > inputTop-1 {
			t.Fatalf("popup bottom %v covers input top %v: %+v", bottom, inputTop, placement)
		}
		if placement.PopupTop < terminal.Y {
			t.Fatalf("popup above the terminal: %+v", placement)
		}
		assertInsideTerminal(t, terminal, placement)
	})

	t.Run("prompt at the top opens downward below the input line", func(t *testing.T) {
		placement := placeAutocomplete(terminal, promptTop, cols, rows, 2, true, false)
		inputBottom := terminal.Y + terminalPaddingY + cellH
		if placement.PopupTop < inputBottom {
			t.Fatalf("popup top %v is above the input bottom %v", placement.PopupTop, inputBottom)
		}
		assertInsideTerminal(t, terminal, placement)
	})

	t.Run("ghost sits between the list and the input line", func(t *testing.T) {
		placement := placeAutocomplete(terminal, promptBottom, cols, rows, 3, true, true)
		if !placement.ShowGhost {
			t.Fatalf("ghost not shown: %+v", placement)
		}
		popupBottom := placement.PopupTop + autocompletePopupHeight(placement.Rows)
		if placement.GhostTop < popupBottom {
			t.Fatalf("ghost overlaps the popup: %+v", placement)
		}
		inputTop := terminal.Y + terminalPaddingY + float32(rows-1)*cellH
		if placement.GhostTop+autocompleteGhostHeight > inputTop-1 {
			t.Fatalf("ghost covers the input line: %+v", placement)
		}
	})

	t.Run("a short pane shows fewer rows", func(t *testing.T) {
		short := ui.Rect{X: 0, Y: 30, W: 720, H: 100}
		placement := placeAutocomplete(short, autocompleteCursor{Row: 9, StartRow: 9}, cols, 10, 7, true, false)
		if placement.Rows >= maxVisibleSuggestions {
			t.Fatalf("rows = %d, want fewer in a short pane", placement.Rows)
		}
		if placement.Rows < 1 {
			t.Fatalf("no row shown: %+v", placement)
		}
		top := short.Y + terminalPaddingY
		bottom := short.Y + short.H - terminalPaddingY
		if placement.PopupTop < top-0.5 || placement.PopupTop+autocompletePopupHeight(placement.Rows) > bottom+0.5 {
			t.Fatalf("popup outside the short pane: %+v", placement)
		}
	})

	t.Run("a cursor at the right edge keeps the popup inside", func(t *testing.T) {
		placement := placeAutocomplete(terminal, autocompleteCursor{Row: rows - 1, Column: cols - 1, StartRow: rows - 1}, cols, rows, 2, true, false)
		if placement.Left+placement.Width > terminal.X+terminal.W-terminalPaddingX+0.5 {
			t.Fatalf("popup overflows the right edge: %+v", placement)
		}
		if placement.Left < terminal.X+terminalPaddingX-0.5 {
			t.Fatalf("popup left of the terminal: %+v", placement)
		}
	})

	t.Run("a pane narrower than the widest popup uses its width", func(t *testing.T) {
		narrow := ui.Rect{X: 40, Y: 20, W: 220, H: 400}
		placement := placeAutocomplete(narrow, promptBottom, cols, rows, 4, true, false)
		if placement.Width > narrow.W-2*terminalPaddingX+0.5 {
			t.Fatalf("width %v exceeds the pane %v", placement.Width, narrow.W)
		}
		assertInsideTerminal(t, narrow, placement)
	})
}

func assertInsideTerminal(t *testing.T, terminal ui.Rect, placement autocompletePlacement) {
	t.Helper()
	left := terminal.X + terminalPaddingX
	right := terminal.X + terminal.W - terminalPaddingX
	if placement.Left < left-0.5 || placement.Left+placement.Width > right+0.5 {
		t.Fatalf("popup %+v is not inside the terminal horizontally %v..%v", placement, left, right)
	}
	top := terminal.Y + terminalPaddingY
	bottom := terminal.Y + terminal.H - terminalPaddingY
	popupBottom := placement.PopupTop + autocompletePopupHeight(placement.Rows)
	if placement.ShowPopup && (placement.PopupTop < top-0.5 || popupBottom > bottom+0.5) {
		t.Fatalf("popup %+v is not inside the terminal vertically %v..%v", placement, top, bottom)
	}
	if placement.ShowGhost {
		ghostBottom := placement.GhostTop + autocompleteGhostHeight
		if placement.GhostTop < top-0.5 || ghostBottom > bottom+0.5 {
			t.Fatalf("ghost %+v is not inside the terminal vertically %v..%v", placement, top, bottom)
		}
	}
}

func TestAutocompletePopupHeightMatchesItsRows(t *testing.T) {
	for rows := 1; rows <= maxVisibleSuggestions; rows++ {
		got := autocompletePopupHeight(rows)
		want := float32(2*autocompleteBorder+2*autocompletePadding+autocompleteHeaderHeight) +
			float32(rows)*float32(autocompleteRowHeight+autocompleteRowGap)
		if got != want {
			t.Fatalf("height(%d) = %v, want %v", rows, got, want)
		}
	}
	if got := autocompletePopupHeight(0); got != 0 {
		t.Fatalf("height(0) = %v", got)
	}
}

func TestAutocompleteCursorStaysInTheGrid(t *testing.T) {
	for _, size := range []struct{ cols, rows int }{{1, 1}, {10, 2}, {3, 1}} {
		cursor := autocompleteCursorFromText(fmt.Sprintf("$ %s", strings.Repeat("x", 40)), strings.Repeat("x", 40), size.cols, size.rows)
		if cursor.Row < 0 || cursor.Row >= size.rows {
			t.Fatalf("%+v: row %d outside %d rows", size, cursor.Row, size.rows)
		}
		if cursor.Column < 0 || cursor.Column >= size.cols {
			t.Fatalf("%+v: column %d outside %d cols", size, cursor.Column, size.cols)
		}
	}
}
