package appview

// The live rows are painted by hand now (see gridRow), so nothing above the
// painter checks that the glyphs land inside the row. These tests pin the
// painter's coordinate space: the Draw callback's rect is the canvas's own, and
// a glyph painted from it must fall inside the row's box, not at twice its
// offset or outside the clip.

import (
	"image"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestGridRowPaintsItsGlyphs checks a painted row draws its text where the label
// says the row is: ink inside the row's box, and none on the blank row below.
func TestGridRowPaintsItsGlyphs(t *testing.T) {
	cellWidth, cellHeight = 8, 16
	defer func() { cellWidth, cellHeight = 0, 0 }()
	tester := ui.NewTester(func(c *ui.Context) {
		// The padding keeps the row away from the canvas corner, so a painter that
		// treated the Draw rect as local coordinates would draw the glyph outside
		// the row instead of at its place on the canvas.
		ui.Column(c).Gap(0).Padding(20, 0, 0, 30).Children(func() {
			gridRow(c, []ui.Span{{Text: "M", Font: "monospace", Color: ui.RGBA(255, 0, 0, 1)}}, 13)
			gridRow(c, []ui.Span{{Text: " ", Font: "monospace"}}, 13)
		})
	}, 200, 100)
	tester.Frame()

	rect, ok := findTextPrefix(tester, "M")
	if !ok {
		t.Fatalf("the painted row is not drawn: %q", tester.Texts())
	}
	if rect.X < 20 || rect.Y < 10 {
		t.Fatalf("the row is at %v, want it inside the column's padding", rect)
	}
	if rect.H < cellHeight-0.5 || rect.H > cellHeight+0.5 {
		t.Fatalf("the painted row is %.3f tall, want the cell %.3f", rect.H, cellHeight)
	}
	img := tester.Image()
	ink := 0
	maxX := 0
	for y := int(rect.Y); y < int(rect.Y+rect.H); y++ {
		for x := int(rect.X); x < int(rect.X+rect.W); x++ {
			if isRed(img, x, y) {
				ink++
				maxX = max(maxX, x)
			}
		}
	}
	if ink < 4 {
		t.Fatalf("the row painted %d ink pixels, want its glyph inside the row box %v", ink, rect)
	}
	// "M" is the first cell's glyph: ink past it would mean the column's place was
	// added twice, as when a painter-local rect is used as a canvas rect.
	if maxX > int(rect.X+cellWidth)+1 {
		t.Fatalf("the glyph reached x=%d, past its first cell ending at %.1f", maxX, rect.X+cellWidth)
	}
	// Nothing may spill onto the next row: that would mean the rect was used in
	// the wrong coordinate space, or the row was taller than a cell.
	for y := int(rect.Y+rect.H) + 1; y < int(rect.Y+rect.H)+8 && y < img.Bounds().Max.Y; y++ {
		for x := int(rect.X); x < int(rect.X+rect.W); x++ {
			if isRed(img, x, y) {
				t.Fatalf("ink spilled below the row at (%d,%d)", x, y)
			}
		}
	}
}

func isRed(img *image.RGBA, x, y int) bool {
	off := img.PixOffset(x, y)
	return img.Pix[off] > 160 && img.Pix[off+1] < 96 && img.Pix[off+2] < 96
}
