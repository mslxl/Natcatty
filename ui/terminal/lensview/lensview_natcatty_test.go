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
			// A 300 DIP window: the old caps (420 for Markdown, 400 for the
			// diff, 320 for code) left at most ~120 DIP of scroll. The bodies
			// here are well over 600 DIP, so a real expansion reports much
			// more than the cap would allow.
			if scroll.MaxY < 300 {
				t.Fatalf("long %s body reports MaxY=%v, want it to grow with its content", name, scroll.MaxY)
			}
		})
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
}

// A list may repeat a label: a listing can hold two equal names, and a plain
// ls used to emit the icon glyph as a row of its own. MyGo gives one state and
// one id to elements that share a key (a Tester panics for it), so repeated
// labels must still get distinct keys and keep their own rows.
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
