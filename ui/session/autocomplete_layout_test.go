package sessionui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/terminal"
	"github.com/mslxl/natcatty/ui/terminal/appview"
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

// placeForTest places the popup the way the Pane's capture drives it: the
// cursor row's box is where the cursor's screen row would render in an empty
// flow — the screen rows count down the whole box exactly there — so these
// cases exercise the flip, clamp and shrink arithmetic with the visual inputs
// the placement really consumes, without a measured font.
func placeForTest(bounds ui.Rect, cursor autocompleteCursor, cols, rows, suggestions int, popup, ghost bool) autocompletePlacement {
	insetX, insetY := appview.LiveGridPadding()
	cellH := (bounds.H - 2*insetY) / float32(rows)
	rowTop := bounds.Y + insetY + float32(cursor.Row)*cellH
	cursorRow := ui.Rect{X: bounds.X + insetX, Y: rowTop, W: bounds.W - 2*insetX, H: cellH}
	return placeAutocomplete(bounds, cursorRow, cursor, cols, rows, suggestions, popup, ghost,
		appview.CellGeometry{W: (bounds.W - 2*insetX) / float32(cols), H: cellH})
}

func TestPlaceAutocompleteFlipsAndClamps(t *testing.T) {
	terminal := ui.Rect{X: 0, Y: 30, W: 720, H: 450}
	const cols, rows = 80, 24
	promptBottom := autocompleteCursor{Row: rows - 1, Column: 5, StartRow: rows - 1}
	promptTop := autocompleteCursor{Row: 0, Column: 5, StartRow: 0}
	insetX, insetY := appview.LiveGridPadding()
	cellH := (terminal.H - 2*insetY) / rows

	t.Run("prompt at the bottom opens upward above the input line", func(t *testing.T) {
		placement := placeForTest(terminal, promptBottom, cols, rows, 7, true, false)
		if !placement.ShowPopup || placement.Rows != maxVisibleSuggestions {
			t.Fatalf("placement = %+v", placement)
		}
		inputTop := terminal.Y + insetY + float32(rows-1)*cellH
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
		placement := placeForTest(terminal, promptTop, cols, rows, 2, true, false)
		inputBottom := terminal.Y + insetY + cellH
		if placement.PopupTop < inputBottom {
			t.Fatalf("popup top %v is above the input bottom %v", placement.PopupTop, inputBottom)
		}
		assertInsideTerminal(t, terminal, placement)
	})

	t.Run("ghost sits between the list and the input line", func(t *testing.T) {
		placement := placeForTest(terminal, promptBottom, cols, rows, 3, true, true)
		if !placement.ShowGhost {
			t.Fatalf("ghost not shown: %+v", placement)
		}
		popupBottom := placement.PopupTop + autocompletePopupHeight(placement.Rows)
		if placement.GhostTop < popupBottom {
			t.Fatalf("ghost overlaps the popup: %+v", placement)
		}
		inputTop := terminal.Y + insetY + float32(rows-1)*cellH
		if placement.GhostTop+autocompleteGhostHeight > inputTop-1 {
			t.Fatalf("ghost covers the input line: %+v", placement)
		}
	})

	t.Run("a short pane shows fewer rows", func(t *testing.T) {
		short := ui.Rect{X: 0, Y: 30, W: 720, H: 100}
		placement := placeForTest(short, autocompleteCursor{Row: 9, StartRow: 9}, cols, 10, 7, true, false)
		if placement.Rows >= maxVisibleSuggestions {
			t.Fatalf("rows = %d, want fewer in a short pane", placement.Rows)
		}
		if placement.Rows < 1 {
			t.Fatalf("no row shown: %+v", placement)
		}
		top := short.Y + insetY
		bottom := short.Y + short.H - insetY
		if placement.PopupTop < top-0.5 || placement.PopupTop+autocompletePopupHeight(placement.Rows) > bottom+0.5 {
			t.Fatalf("popup outside the short pane: %+v", placement)
		}
	})

	t.Run("a cursor at the right edge keeps the popup inside", func(t *testing.T) {
		placement := placeForTest(terminal, autocompleteCursor{Row: rows - 1, Column: cols - 1, StartRow: rows - 1}, cols, rows, 2, true, false)
		if placement.Left+placement.Width > terminal.X+terminal.W-insetX+0.5 {
			t.Fatalf("popup overflows the right edge: %+v", placement)
		}
		if placement.Left < terminal.X+insetX-0.5 {
			t.Fatalf("popup left of the terminal: %+v", placement)
		}
	})

	t.Run("a pane narrower than the widest popup uses its width", func(t *testing.T) {
		narrow := ui.Rect{X: 40, Y: 20, W: 220, H: 400}
		placement := placeForTest(narrow, promptBottom, cols, rows, 4, true, false)
		if placement.Width > narrow.W-2*insetX+0.5 {
			t.Fatalf("width %v exceeds the pane %v", placement.Width, narrow.W)
		}
		assertInsideTerminal(t, narrow, placement)
	})

	t.Run("a scrolled-out cursor row keeps the popup inside the viewport", func(t *testing.T) {
		// The reader scrolls toward the start with the popup open: the
		// captured cursor row — window coordinates — moves below the visible
		// area, and the upward placement must not follow it out over whatever
		// sits below the terminal.
		scrolledRow := ui.Rect{X: terminal.X + 16, Y: terminal.Y + terminal.H + 40, W: 600, H: cellH}
		placement := placeAutocomplete(terminal, scrolledRow, promptBottom, cols, rows, 4, true, true,
			appview.CellGeometry{W: 8, H: cellH})
		assertInsideTerminal(t, terminal, placement)
	})
}

func assertInsideTerminal(t *testing.T, terminal ui.Rect, placement autocompletePlacement) {
	t.Helper()
	insetX, insetY := appview.LiveGridPadding()
	left := terminal.X + insetX
	right := terminal.X + terminal.W - insetX
	if placement.Left < left-0.5 || placement.Left+placement.Width > right+0.5 {
		t.Fatalf("popup %+v is not inside the terminal horizontally %v..%v", placement, left, right)
	}
	top := terminal.Y + insetY
	bottom := terminal.Y + terminal.H - insetY
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

// snapshotTerminal feeds bytes into a real terminal plugin so the snapshot
// replays exactly what a live Session would hold: ghostty's erase merges the
// screen into scrollback, which is what the screen-row conversion must see
// through.
func snapshotTerminal(t *testing.T, cols, rows int, chunks ...string) []byte {
	t.Helper()
	term, err := terminal.New(terminal.Options{Conn: newTerminalFlowTransport(), Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })
	term.Resize(cols, rows)
	for _, chunk := range chunks {
		term.Feed([]byte(chunk))
	}
	return term.Snapshot()
}

// TestAutocompleteCursorFromSnapshot pins the row the popup anchors at when
// the screen does not fill from the replay's first row. The replay counts
// scrollback rows in, so the raw cursor row is inflated exactly in the case
// that broke the placement: a clear pushed output into scrollback and the
// prompt came back at the visible screen's top, where everything below it is
// empty. Anchoring at the inflated row measured the space above as ample and
// flipped the popup up over the input even though the whole screen below was
// free.
func TestAutocompleteCursorFromSnapshot(t *testing.T) {
	screenRows := func(lines int) string {
		var b strings.Builder
		for i := 0; i < lines; i++ {
			fmt.Fprintf(&b, "line%02d\r\n", i)
		}
		return b.String()
	}
	cases := []struct {
		name   string
		cols   int
		rows   int
		chunks []string
		input  string
		want   autocompleteCursor
	}{
		{
			name:   "a fresh prompt at the top row",
			cols:   40,
			rows:   10,
			chunks: []string{"$ git st"},
			input:  "git st",
			want:   autocompleteCursor{Row: 0, Column: 8, StartRow: 0},
		},
		{
			name:   "output scrolled the prompt to the bottom row",
			cols:   40,
			rows:   10,
			chunks: []string{screenRows(30) + "$ ls"},
			input:  "ls",
			want:   autocompleteCursor{Row: 9, Column: 4, StartRow: 9},
		},
		{
			name:   "a clear merged the output into scrollback",
			cols:   40,
			rows:   10,
			chunks: []string{screenRows(30), "\x1b[H\x1b[2J", "$ ls"},
			input:  "ls",
			want:   autocompleteCursor{Row: 0, Column: 4, StartRow: 0},
		},
		{
			name:   "a half-filled screen keeps the prompt mid-screen",
			cols:   40,
			rows:   10,
			chunks: []string{"hello\r\nworld\r\n$ ls"},
			input:  "ls",
			want:   autocompleteCursor{Row: 2, Column: 4, StartRow: 2},
		},
		{
			name:   "a wrapped input counts back to the command's first row",
			cols:   20,
			rows:   10,
			chunks: []string{"$ " + strings.Repeat("a", 25)},
			input:  strings.Repeat("a", 25),
			want:   autocompleteCursor{Row: 1, Column: 7, StartRow: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := snapshotTerminal(t, tc.cols, tc.rows, tc.chunks...)
			got := autocompleteCursorFromSnapshot(snapshot, tc.input, tc.cols, tc.rows)
			if got != tc.want {
				t.Fatalf("cursor = %+v, want %+v", got, tc.want)
			}
		})
	}
}
