package lensview

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens"
)

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
