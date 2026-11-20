package lensview

import (
	"fmt"
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

func TestNatcattyLensViewNarrowTableDropsLowPriorityColumn(t *testing.T) {
	cols := []lens.Col{
		{ID: "name", Head: "NAME", Priority: 9},
		{ID: "size", Head: "SIZE", Priority: 1},
		{ID: "usage", Head: "USAGE", Priority: 0},
	}
	kept, _ := tableLayout(cols, [][]lens.Cell{{}, {}, {}}, 40)
	if len(kept) != 1 || kept[0] != 0 {
		t.Fatalf("narrow table kept columns %v, want only high-priority NAME", kept)
	}
}
