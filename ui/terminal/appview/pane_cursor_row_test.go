package appview

// A fast, font-independent-adjacent check of where the live cursor is drawn.
//
// The overlay used to be placed at cursorRow*cellHeight, but rows stack by
// their real line boxes, and a row holding a glyph the font falls back for
// (an emoji or a Nerd Font icon) lays out a little taller than the measured
// cell. With glyph rows above the input the overlay therefore sat above the
// row it belongs to, and the error grew with the number of such rows. The
// end-to-end test in app/ presses Enter on a glyph prompt many times; this
// one pins the geometry in a single frame.

import (
	"fmt"
	"image"
	"strings"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// TestPaneCursorSitsOnItsRowAfterGlyphRows lays out rows whose glyph line
// boxes are taller than the measured cell, then checks that the cursor is
// drawn on the prompt row's own top, one cell tall and in its column. The
// three cursor shapes are checked because their tops and heights differ.
func TestPaneCursorSitsOnItsRowAfterGlyphRows(t *testing.T) {
	const (
		glyphRows = 8
		width     = 900
		height    = 300
	)
	for _, head := range []struct {
		name string
		text string
	}{
		{name: "ascii-control", text: "READY"},
		{name: "glyph", text: "❄"},
		{name: "nerd-glyph", text: ""},
	} {
		for _, style := range []struct {
			name  string
			style terminal.CursorStyle
		}{
			{name: "block", style: terminal.CursorBlock},
			{name: "bar", style: terminal.CursorBar},
			{name: "underline", style: terminal.CursorUnderline},
		} {
			t.Run(head.name+"/"+style.name, func(t *testing.T) {
				var text strings.Builder
				for i := 0; i < glyphRows; i++ {
					fmt.Fprintf(&text, "%s row %02d\r\n", head.text, i)
				}
				text.WriteString("PROMPT> ")

				conn := newNopConn()
				term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 64 << 10})
				if err != nil {
					t.Skipf("the terminal plugin is unavailable here: %v", err)
				}
				defer func() { _ = term.Close() }()
				size := Size{Cols: 40, Rows: 10}
				term.Resize(size.Cols, size.Rows)
				term.Feed([]byte(text.String()))

				eng := lens.NewEngine()
				store := model.NewStore(eng)
				eng.Attach(store)
				var scroll ui.ScrollState
				var follow Follow
				tester := ui.NewTester(func(c *ui.Context) {
					Pane(c, store, eng, term, size, nil, &scroll, &follow,
						nil, Bell{Col: -1, Row: -1},
						// No prompt boundary: the live area is the whole
						// screen, so the cursor row has every glyph row above
						// it.
						Tail{},
						Cursor{Style: style.style, Color: ui.RGBA(255, 0, 0, 1)},
						nil, Hooks{}, func(string) {})
				}, width, height)
				for i := 0; i < 3; i++ {
					tester.Frame()
				}

				// Underline cursors trim the trailing cursor cell's text, so
				// the row's label may end at "PROMPT>" rather than keep the
				// blank the cursor sits on.
				rect, ok := findTextPrefix(tester, "PROMPT>")
				if !ok {
					t.Fatalf("the prompt row is not drawn: %q", tester.Texts())
				}
				cellW, cellH := CellSize()
				if cellW <= 0 || cellH <= 0 {
					t.Fatal("the live cells were not measured")
				}
				top, bottom, ok := cursorBandNear(tester.Image(), int(rect.X)-2, int(rect.Y))
				if !ok {
					t.Fatalf("no cursor is drawn on the prompt row at (%.1f,%.1f)", rect.X, rect.Y)
				}
				wantTop, wantH, wantW := float32(rect.Y), cellH, cellW
				switch style.style {
				case terminal.CursorBar:
					wantW = 2
				case terminal.CursorUnderline:
					wantTop, wantH = float32(rect.Y)+cellH-2, 2
				}
				if got := float32(top) - wantTop; got < -1 || got > 1 {
					t.Fatalf("the cursor is not on the prompt row: cursor top %d, want %.1f (delta %.1f)",
						top, wantTop, got)
				}
				if got := float32(bottom - top + 1); got < wantH-2 || got > wantH+2 {
					t.Fatalf("the cursor height is wrong: band %d..%d (%d DIP), want %.1f",
						top, bottom, bottom-top+1, wantH)
				}
				if float32(bottom) > rect.Y+rect.H+1 {
					t.Fatalf("the cursor spills below its row: band %d..%d, row %.1f..%.1f",
						top, bottom, rect.Y, rect.Y+rect.H)
				}
				// The cursor is at the end of "PROMPT> ", column 8. Its
				// horizontal position must agree with the row's text origin.
				left, right := cursorXRange(tester.Image(), top, bottom, int(rect.X)-2)
				wantLeft := float32(rect.X) + 8*cellW
				if left < wantLeft-2 || left > wantLeft+2 {
					t.Fatalf("the cursor is not at column 8: left %.1f, want %.1f", left, wantLeft)
				}
				if got := right - left + 1; got < wantW-2 || got > wantW+2 {
					t.Fatalf("the cursor width is wrong: %.0f..%.0f (%.0f DIP), want %.1f",
						left, right, right-left+1, wantW)
				}
			})
		}
	}
}

// cursorBandNear returns the contiguous red band whose top is closest to
// wantY, so a cursor row is measured even if the image carries other red.
func cursorBandNear(img *image.RGBA, minX, wantY int) (top, bottom int, ok bool) {
	rows := redRows(img, minX)
	if len(rows) == 0 {
		return 0, 0, false
	}
	bestDist := 1 << 30
	for i := 0; i < len(rows); {
		start, end := rows[i], rows[i]
		for i++; i < len(rows) && rows[i] == end+1; i++ {
			end = rows[i]
		}
		dist := start - wantY
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			bestDist, top, bottom, ok = dist, start, end, true
		}
	}
	return top, bottom, ok
}

// redRows returns the image rows that carry the steady red cursor. Only the
// cursor is red in these tests.
func redRows(img *image.RGBA, minX int) []int {
	b := img.Bounds()
	if minX < b.Min.X {
		minX = b.Min.X
	}
	rows := map[int]bool{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := minX; x < b.Max.X; x++ {
			off := img.PixOffset(x, y)
			if img.Pix[off] > 160 && img.Pix[off+1] < 96 && img.Pix[off+2] < 96 {
				rows[y] = true
				break
			}
		}
	}
	var out []int
	for y := 0; y < b.Max.Y; y++ {
		if rows[y] {
			out = append(out, y)
		}
	}
	return out
}

// cursorXRange is the first and last red column in the given band.
func cursorXRange(img *image.RGBA, top, bottom, minX int) (left, right float32) {
	b := img.Bounds()
	if minX < b.Min.X {
		minX = b.Min.X
	}
	left, right = -1, -1
	for x := minX; x < b.Max.X; x++ {
		for y := top; y <= bottom; y++ {
			off := img.PixOffset(x, y)
			if img.Pix[off] > 160 && img.Pix[off+1] < 96 && img.Pix[off+2] < 96 {
				if left < 0 {
					left = float32(x)
				}
				right = float32(x)
				break
			}
		}
	}
	return left, right
}
