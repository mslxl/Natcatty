package lensview

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens"
)

// A body that draws itself (Markdown, a diff, a code editor) must give its
// height to its content, so the flow's scroll reaches every line. Fixed caps
// used to clip long documents, diffs and code with no scroll inside to reach
// the rest.
func TestNatcattyLensViewLongBodiesGrowWithTheirContent(t *testing.T) {
	longDoc := func() string {
		var b strings.Builder
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&b, "paragraph %02d\n\n", i)
		}
		return b.String()
	}
	longDiff := func() string {
		var b strings.Builder
		b.WriteString("--- a/main.go\n+++ b/main.go\n@@ -1,40 +1,40 @@\n")
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&b, "-old line %02d\n+new line %02d\n", i, i)
		}
		return b.String()
	}
	longCode := func() string {
		var b strings.Builder
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&b, "var line%02d = %d\n", i, i)
		}
		return b.String()
	}
	cases := map[string]lens.Node{
		"markdown": lens.Markdown{Text: longDoc()},
		"diff":     lens.Diff{Path: "main.go", Text: longDiff()},
		"code":     lens.Code{Text: longCode(), Lang: "go"},
	}
	for name, node := range cases {
		t.Run(name, func(t *testing.T) {
			var scroll ui.ScrollState
			tester := ui.NewTester(func(c *ui.Context) {
				ui.Scroll(c).Fill().TrackScroll(&scroll).Children(func() {
					ui.Column(c).Padding(10).Children(func() { Render(c, node) })
				})
			}, 600, 300)
			tester.Frame()
			tester.Frame()
			// A 300 DIP window: the bodies here are well over 600 DIP, so a
			// body that follows its content reports far more scroll than a
			// cap (Markdown's was 420 DIP, the diff's 400) would allow.
			// Code's body used to be counted from line breaks instead of
			// asked of the editor, which would hide this.
			if scroll.MaxY < 300 {
				t.Fatalf("long %s body reports MaxY=%v, want it to grow with its content", name, scroll.MaxY)
			}
		})
	}
}

// Markdown code blocks used to scroll their long lines sideways, a scroll
// region of their own inside the document. They wrap now: ten 400-character
// lines fit in a 300 DIP window only by wrapping, so the flow has to scroll.
func TestNatcattyLensViewMarkdownCodeWrapsInsteadOfScrolling(t *testing.T) {
	long := strings.Repeat("x", 400)
	var b strings.Builder
	b.WriteString("```\n")
	for i := 0; i < 10; i++ {
		b.WriteString(long)
		b.WriteString("\n")
	}
	b.WriteString("```\n")
	var scroll ui.ScrollState
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().TrackScroll(&scroll).Children(func() {
			ui.Column(c).Padding(10).Children(func() { Render(c, lens.Markdown{Text: b.String()}) })
		})
	}, 300, 300)
	tester.Frame()
	tester.Frame()
	if scroll.MaxY <= 0 {
		t.Fatalf("wrapped markdown code reports MaxY=%v, want the flow to scroll it", scroll.MaxY)
	}
}

// The code node's editor used to keep a sideways scroll for a long line, and
// once its bars were hidden the rest of the line had no way to be reached. It
// soft-wraps instead, and the body grows by the wrapped rows, so the flow
// reaches all of the line.
func TestNatcattyLensViewCodeSoftWrapsLongLines(t *testing.T) {
	long := strings.Repeat("x", 400)
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString(long)
		b.WriteString("\n")
	}
	var scroll ui.ScrollState
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().TrackScroll(&scroll).Children(func() {
			ui.Column(c).Padding(10).Children(func() { Render(c, lens.Code{Text: b.String(), Lang: "go"}) })
		})
	}, 300, 300)
	tester.Frame()
	tester.Frame()
	if scroll.MaxY <= 0 {
		t.Fatalf("soft-wrapped code reports MaxY=%v, want the flow to scroll it", scroll.MaxY)
	}
}

// MyGo builds every Markdown table inside a horizontal scroll. A table wider
// than the view now wraps its cells at column widths that fit, so the label of
// a cell stays inside its half of a 300 DIP window instead of panning.
func TestNatcattyLensViewMarkdownTableWrapsInsteadOfScrolling(t *testing.T) {
	left := strings.Join([]string{"alpha", "alpha", "alpha", "alpha", "alpha", "alpha", "alpha", "alpha"}, " ")
	right := strings.ReplaceAll(left, "alpha", "bravo")
	doc := "| left | right |\n|---|---|\n| " + left + " | " + right + " |\n"
	tester := ui.NewTester(func(c *ui.Context) { Render(c, lens.Markdown{Text: doc}) }, 300, 300)
	tester.Frame()
	tester.Frame()
	r, ok := tester.Find(left)
	if !ok {
		t.Fatalf("table cell %q is not shown: %v", left, tester.Texts())
	}
	if r.W > 160 {
		t.Fatalf("table cell is %v DIP wide in a 300 DIP view, want it to wrap in its column", r.W)
	}
}

// A snippet that starts at a line other than 1 is numbered from that line,
// and a soft-wrapped line keeps its number on its first row: the numbers are
// laid out one box per line, as high as the rows the editor gave the line, so
// the number after a wrapped line is pushed down with it. One number per
// logical line in a plain column drifted as soon as a line wrapped.
func TestNatcattyLensViewSnippetNumbersFollowWrappedLines(t *testing.T) {
	long := strings.Repeat("x", 200)
	node := lens.Code{Text: "alpha\n" + long + "\nomega", Start: 40}
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(10).Children(func() {
			// A stable scope keeps the editor's layout from frame to frame, as
			// a block's view does.
			RenderInto(c, node, "natcatty-snippet", nil)
		})
	}, 400, 400)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}
	top, ok := tester.Find("40")
	if !ok {
		t.Fatalf("snippet line 40 is not numbered: %v", tester.Texts())
	}
	middle, ok := tester.Find("41")
	if !ok {
		t.Fatalf("snippet line 41 is not numbered: %v", tester.Texts())
	}
	bottom, ok := tester.Find("42")
	if !ok {
		t.Fatalf("snippet line 42 is not numbered: %v", tester.Texts())
	}
	// 200 characters in a 400 DIP window wrap over several rows, so the next
	// number is far below the wrapped line's number; the number above it is
	// one row away.
	if dy := middle.Y - top.Y; dy > 30 {
		t.Fatalf("line 41's number is %v DIP below line 40's, want one row", dy)
	}
	if dy := bottom.Y - middle.Y; dy < 60 {
		t.Fatalf("line 42's number is %v DIP below the wrapped line's, want the line's rows", dy)
	}
}

func TestNatcattyLensViewRendersCoreNodes(t *testing.T) {
	tree := lens.Column{Children: []lens.Node{
		lens.Text{Spans: []lens.Span{{Text: "hello"}, {Text: " world", Style: "strong accent"}}},
		lens.Table{
			Cols: []lens.Col{{ID: "name", Head: "NAME", Priority: 2}, {ID: "size", Head: "SIZE", Priority: 1}},
			Rows: [][]lens.Cell{{{Spans: []lens.Span{{Text: "a.go"}}}, {Spans: []lens.Span{{Text: "12"}}}}},
		},
		lens.Diff{Path: "main.go", Text: "--- a/main.go\n+++ b/main.go\n@@ -1,1 +1,1 @@\n-old\n+new\n"},
		lens.ANSI{Text: "\x1b[31mred\x1b[0m plain"},
	}}
	tester := ui.NewTester(func(c *ui.Context) { Render(c, tree) }, 800, 500)
	tester.Frame()
	text := strings.Join(tester.Texts(), "\n")
	for _, want := range []string{"hello", "NAME", "a.go", "main.go", "red", "plain"} {
		if !strings.Contains(text, want) {
			t.Errorf("lens view is missing %q: %v", want, tester.Texts())
		}
	}
}

func TestNatcattyLensViewSectionCollapsePersists(t *testing.T) {
	tree := lens.Section{
		Head:     []lens.Span{{Text: "ANSWER"}},
		Children: []lens.Node{lens.Text{Spans: []lens.Span{{Text: "body-line"}}}},
	}
	render := func() string {
		tester := ui.NewTester(func(c *ui.Context) { RenderScoped(c, tree, "natcatty-collapse") }, 500, 250)
		tester.Frame()
		return strings.Join(tester.Texts(), "\n")
	}
	if text := render(); !strings.Contains(text, "body-line") {
		t.Fatalf("section body missing before collapse: %s", text)
	}
	collapseSet(NodeStateKey("natcatty-collapse", "section", "ANSWER", 0), false)
	if text := render(); strings.Contains(text, "body-line") {
		t.Fatalf("section body remained visible after collapse: %s", text)
	}
}

// A lens view never scrolls inside itself: a long list is as tall as its rows,
// so the flow's scroll reaches the end. A 240 DIP list box with its own bar
// left everything past the box reachable only through that inner scroll.
func TestNatcattyLensViewLongListGrowsWithTheFlow(t *testing.T) {
	items := make([]lens.Item, 60)
	for i := range items {
		items[i] = lens.Item{Label: []lens.Span{{Text: fmt.Sprintf("row %02d", i)}}}
	}
	var scroll ui.ScrollState
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().TrackScroll(&scroll).Children(func() {
			ui.Column(c).Padding(10).Children(func() { Render(c, lens.List{Items: items}) })
		})
	}, 600, 300)
	tester.Frame()
	tester.Frame()
	if scroll.MaxY < 600 {
		t.Fatalf("long list reports MaxY=%v, want the flow to scroll it", scroll.MaxY)
	}
}

// The ANSI node used to sit in a 300 DIP box with its own scroll: coloured
// output past the box could only be reached by scrolling inside it. The whole
// screen now belongs to the flow.
func TestNatcattyLensViewLongANSIGrowsWithTheFlow(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "line %02d\n", i)
	}
	var scroll ui.ScrollState
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().TrackScroll(&scroll).Children(func() {
			ui.Column(c).Padding(10).Children(func() { Render(c, lens.ANSI{Text: b.String()}) })
		})
	}, 600, 300)
	tester.Frame()
	tester.Frame()
	if scroll.MaxY < 600 {
		t.Fatalf("long ANSI body reports MaxY=%v, want the flow to scroll it", scroll.MaxY)
	}
}

func TestNatcattyLensViewListAndTableEvents(t *testing.T) {
	var events []lens.Event
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(10).Children(func() {
			RenderInto(c, lens.List{Items: []lens.Item{
				{Label: []lens.Span{{Text: "api"}}},
				{Label: []lens.Span{{Text: "worker"}}},
			}}, "natcatty-events", func(ev lens.Event) { events = append(events, ev) })
		})
	}, 300, 250)
	tester.Frame()
	tester.Frame()
	tester.Press(40, 18)
	tester.Release(40, 18)
	tester.Frame()
	if len(events) == 0 || events[0].Action != "select" || events[0].Value != "api" {
		t.Fatalf("list selection event = %+v", events)
	}
	// The row stays chosen, so a second click activates it (a table's gesture).
	tester.Press(40, 18)
	tester.Release(40, 18)
	tester.Frame()
	if len(events) < 2 || events[1].Action != "activate" || events[1].Value != "api" {
		t.Fatalf("list activation event = %+v", events)
	}
	// The rows are not a keyboard list: an arrow key scrolls the terminal and
	// moves no selection inside the widget. A ui.List here would take focus on
	// the click and report the next row on Down.
	tester.Key(0, ui.KeyDown)
	tester.Frame()
	if len(events) != 2 {
		t.Fatalf("arrow key moved a selection inside the list: %+v", events)
	}
}

// A list may repeat a label: a listing can hold two equal names, and both rows
// must stay visible in the flow.
func TestNatcattyLensViewRepeatedListLabels(t *testing.T) {
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(10).Children(func() {
			RenderInto(c, lens.List{Items: []lens.Item{
				{Label: []lens.Span{{Text: "dup"}}},
				{Label: []lens.Span{{Text: "first"}}},
				{Label: []lens.Span{{Text: "dup"}}},
				{Label: []lens.Span{{Text: "second"}}},
			}}, "natcatty-repeated", func(lens.Event) {})
		})
	}, 400, 300)
	tester.Frame()
	tester.Frame()
	texts := tester.Texts()
	text := strings.Join(texts, "\n")
	for _, want := range []string{"first", "second"} {
		if !strings.Contains(text, want) {
			t.Fatalf("list is missing %q: %v", want, texts)
		}
	}
	if n := strings.Count(text, "dup"); n != 2 {
		t.Fatalf("the repeated label shows %d times, want 2: %v", n, texts)
	}
}

// A column with a higher Priority number hides first: the builtin lenses give
// the name and size the lowest numbers and their decorative columns the
// highest, so a narrow table keeps the content the user asked for.
func TestNatcattyLensViewTableHidesTheMostExpendableColumn(t *testing.T) {
	cols := []tableColumn{
		{Priority: 0, Natural: 60, Min: 40, key: "name"},
		{Priority: 1, Natural: 40, Min: 30, key: "size"},
		{Priority: 9, Natural: 90, Min: 40, key: "usage"},
	}
	// The three minimums (110) do not fit, so the highest number goes first.
	kept, _ := tableAlloc(cols, 80)
	if len(kept) != 2 || kept[0] != 0 || kept[1] != 1 {
		t.Fatalf("narrow table kept columns %v, want NAME and SIZE", kept)
	}
	// A zero never hides, even when one column is left.
	kept, _ = tableAlloc(cols, 20)
	if len(kept) != 1 || kept[0] != 0 {
		t.Fatalf("tiny table kept columns %v, want the zero-priority NAME", kept)
	}
}

// The growing columns take the spare width in proportion to their shares, and
// a fixed column keeps the width its content needs.
func TestNatcattyLensViewTableGrowSharesTheSpare(t *testing.T) {
	cols := []tableColumn{
		{Priority: 0, Natural: 100, Min: 50},
		{Priority: 0, Natural: 120, Min: 40, Grow: 6},
		{Priority: 0, Natural: 80, Min: 30, Grow: 2},
	}
	kept, widths := tableAlloc(cols, 400)
	if len(kept) != 3 {
		t.Fatalf("kept columns %v, want all three", kept)
	}
	if widths[0] != 100 {
		t.Fatalf("fixed column = %v, want its natural 100", widths[0])
	}
	// 400 - 300 = 100 spare, 6:2 → 75 to the name and 25 to the meter.
	if got := widths[1]; got < 194.9 || got > 195.1 {
		t.Fatalf("name column = %v, want 195", got)
	}
	if got := widths[2]; got < 104.9 || got > 105.1 {
		t.Fatalf("usage column = %v, want 105", got)
	}
}

// A width the user dragged stays put, and the growing columns still fill the
// row around it.
func TestNatcattyLensViewTableKeepsTheDraggedWidth(t *testing.T) {
	cols := []tableColumn{
		{Priority: 0, Natural: 100, Min: 40, User: 160},
		{Priority: 1, Natural: 100, Min: 40, Grow: 1},
	}
	_, widths := tableAlloc(cols, 400)
	if widths[0] != 160 {
		t.Fatalf("dragged column = %v, want 160", widths[0])
	}
	if got := widths[1]; got < 239.9 || got > 240.1 {
		t.Fatalf("the other column = %v, want the rest", got)
	}
}

// tableTestRenders a table in a column of width DIPs, as a block's body gives
// it: a left inset and, past the body's right edge, the flow's scroll, which
// clips. It pumps the frames the wrapper's width needs.
func tableTestRenders(t *testing.T, scope string, width float32, table lens.Table) *ui.Tester {
	t.Helper()
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(0, 0, 0, 10).Children(func() {
			ui.Box(c).Width(width).Clip().Children(func() {
				ui.Column(c).FillWidth().Children(func() {
					RenderInto(c, table, scope, nil)
				})
			})
		})
	}, 900, 300)
	tester.SetLocale("en")
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	return tester
}

// gripX finds the resize grip at the end of a header cell the way a user
// does, by hovering where it is: the grip draws only under the pointer, and
// the last column's is inset so the scroll bar over the table's right edge
// does not take it.
func gripX(t *testing.T, tester *ui.Tester, box ui.Rect) float32 {
	t.Helper()
	y := box.Y + box.H/2
	for x := box.X + box.W - 1; x > box.X+box.W-40; x -= 2 {
		tester.Move(x, y)
		tester.Frame()
		if tester.Cursor() == ui.CursorResizeEW {
			return x
		}
	}
	t.Fatalf("no resize grip at the end of the header at %v", box)
	return 0
}

// tableTestNode is an ls-like listing: a wide name, and a decorative usage
// meter that should go before the names do.
func tableTestNode() lens.Table {
	return lens.Table{
		Cols: []lens.Col{
			{ID: "mode", Head: "Mode", Priority: 4, Truncate: lens.TruncateEnd},
			{ID: "size", Head: "Size", Align: lens.AlignEnd, Priority: 1},
			{ID: "usage", Head: "Usage", Priority: 9},
			{ID: "name", Head: "Name", Grow: 6, Truncate: lens.TruncateMiddle},
		},
		Rows: [][]lens.Cell{{
			{Spans: []lens.Span{{Text: "-rw-r--r--"}}},
			{Spans: []lens.Span{{Text: "4.0K"}}},
			{Meter: &lens.MeterCell{Value: 0.5}},
			{Spans: []lens.Span{{Text: "a-very-long-name-that-keeps-going-and-going.txt"}}},
		}},
	}
}

// A table with room shows every column in full. The width is a border box
// including the cell padding; the old one-cell slack cut a ten-character mode
// by a character even here.
func TestNatcattyLensViewTableShowsItsContentWhenWide(t *testing.T) {
	tester := tableTestRenders(t, "table-wide", 860, tableTestNode())
	for _, want := range []string{"Mode", "Size", "Usage", "Name", "-rw-r--r--", "4.0K",
		"a-very-long-name-that-keeps-going-and-going.txt"} {
		if !tester.HasText(want) {
			t.Fatalf("wide table is missing %q: %v", want, tester.Texts())
		}
	}
}

// A table narrower than its columns' least widths hides the most expendable
// columns and keeps the named ones, instead of dropping the name first.
func TestNatcattyLensViewTableHidesUsageBeforeTheName(t *testing.T) {
	tester := tableTestRenders(t, "table-narrow", 100, tableTestNode())
	for _, want := range []string{"Name", "Size"} {
		if !tester.HasText(want) {
			t.Fatalf("narrow table is missing %q: %v", want, tester.Texts())
		}
	}
	for _, gone := range []string{"Usage", "Mode"} {
		if tester.HasText(gone) {
			t.Fatalf("narrow table still shows the expendable column %q: %v", gone, tester.Texts())
		}
	}
}

// The header's end edge drags the column wider; the width survives later
// frames, and a double click gives the column back to its content.
func TestNatcattyLensViewTableHeaderDragResizesTheColumn(t *testing.T) {
	// A growing column fills the row, so the NAME column, last, ends at the
	// body's right edge where the flow's scroll bar lies over it.
	table := lens.Table{
		Cols: []lens.Col{
			{ID: "size", Head: "SIZE", Grow: 1},
			{ID: "name", Head: "NAME", Truncate: lens.TruncateMiddle},
		},
		Rows: [][]lens.Cell{{
			{Spans: []lens.Span{{Text: "4.0K"}}},
			{Spans: []lens.Span{{Text: "a.txt"}}},
		}},
	}
	tester := tableTestRenders(t, "table-drag", 500, table)
	before, ok := tester.Find("NAME")
	if !ok {
		t.Fatalf("no NAME header: %v", tester.Texts())
	}
	edge := gripX(t, tester, before)
	y := before.Y + before.H/2
	tester.Press(edge, y)
	tester.Move(edge+60, y)
	tester.Release(edge+60, y)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}
	after, ok := tester.Find("NAME")
	if !ok {
		t.Fatal("the NAME header disappeared after the drag")
	}
	if got := after.W - before.W; got < 59 || got > 61 {
		t.Fatalf("the header is %v wide after a 60 DIP drag, from %v", after.W, before.W)
	}
	for i := 0; i < 3; i++ {
		tester.Frame()
	}
	kept, _ := tester.Find("NAME")
	if kept.W < before.W+59 || kept.W > before.W+61 {
		t.Fatalf("the dragged width did not survive later frames: %v", kept.W)
	}
	// The edge sits at the new end now.
	edge = gripX(t, tester, kept)
	tester.ClickAt(edge, kept.Y+kept.H/2)
	tester.ClickAt(edge, kept.Y+kept.H/2)
	tester.Frame()
	tester.Frame()
	fitted, ok := tester.Find("NAME")
	if !ok {
		t.Fatal("the NAME header disappeared after the double click")
	}
	if got := fitted.W - before.W; got > 1 || got < -1 {
		t.Fatalf("a double click left the column at %v, want its fitted %v", fitted.W, before.W)
	}
}

// Growing PNG decoding must reset its previous-row filter buffer on each frame
// (otherwise frame 2+ corrupts row 0) and isolate state per block scope so
// running `cat` on the same path a second time starts fresh.
func TestGrowingPNGClearsPrevRowAndIsolatesRepeatedRuns(t *testing.T) {
	const w, h = 64, 64
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.SetRGBA(x, y, color.RGBA{
				R: uint8((x*7 + y*13) % 256),
				G: uint8((x*11 + y*5) % 256),
				B: uint8((x*3 + y*17) % 256),
				A: 0xFF,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	// Verify Growing PNG decoder across multiple frames preserves row 0 accurately.
	g := NewGrowing()
	defer g.Close()
	// Feed in two frames: first ~70%, then 100%.
	g.Bitmap(data[:len(data)*7/10], nil)
	if _, ok := g.Bitmap(data, nil); !ok {
		t.Fatal("Growing.Bitmap returned false for complete PNG")
	}
	if g.img == nil || g.Rows() != h {
		t.Fatalf("Growing rows = %d, want %d", g.Rows(), h)
	}
	for x := 0; x < w; x++ {
		got := g.img.RGBAAt(x, 0)
		want := src.RGBAAt(x, 0)
		if got != want {
			t.Fatalf("row 0 pixel (%d,0) corrupted across frames: got %v, want %v", x, got, want)
		}
	}

	// Verify bitmapForImage isolates repeated runs of the same path via growKey
	// and cleans up growing state on completion.
	for _, scope := range []string{"run-1", "run-2"} {
		growKey := scope + ":/tmp/same.png"
		partial := lens.Image{
			Data: data[:len(data)*7/10], Path: "/tmp/same.png",
			Growing: true, Complete: len(data), Total: int64(len(data)),
		}
		bitmapForImage(partial, growKey)
		full := lens.Image{
			Data: data, Path: "/tmp/same.png",
			Growing: false, Complete: len(data), Total: int64(len(data)),
		}
		bmp, ok := bitmapForImage(full, growKey)
		if !ok {
			t.Fatalf("%s: complete PNG did not decode", scope)
		}
		bw, bh := bmp.Size()
		if bw != w || bh != h {
			t.Fatalf("%s: size = %dx%d, want %dx%d", scope, bw, bh, w, h)
		}
		growingMu.Lock()
		_, stillCached := growingStates[growKey]
		growingMu.Unlock()
		if stillCached {
			t.Fatalf("%s: growingStates entry was not cleaned up after full decode", scope)
		}
	}
}
