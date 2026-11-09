package layout

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestWorkLayoutDocksThePanelAtItsEdge(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dock  Dock
		where func(panel, panes ui.Rect) bool
	}{
		{"left", DockLeft, func(p, q ui.Rect) bool { return p.X < q.X }},
		{"right", DockRight, func(p, q ui.Rect) bool { return p.X > q.X }},
		{"bottom", DockBottom, func(p, q ui.Rect) bool { return p.Y > q.Y }},
	} {
		width, ratio := float32(200), float32(60)
		tt := ui.NewTester(func(c *ui.Context) {
			WorkLayout(c, WorkSlots{
				Panes: func() { ui.Text(c, "panes") },
				Panel: func() { ui.Text(c, "panel") },
				Dock:  tc.dock, Width: &width, Ratio: &ratio,
			})
		}, 800, 600)
		tt.Frame()
		panel, okPanel := tt.Find("panel")
		panes, okPanes := tt.Find("panes")
		if !okPanel || !okPanes {
			t.Fatalf("%s: panel shown %v, panes shown %v", tc.name, okPanel, okPanes)
		}
		if !tc.where(panel, panes) {
			t.Fatalf("%s: panel at %+v, panes at %+v", tc.name, panel, panes)
		}
	}
}

func TestWorkLayoutWithoutAPanelShowsOnlyThePanes(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		WorkLayout(c, WorkSlots{Top: func() { ui.Text(c, "notice") }, Panes: func() { ui.Text(c, "panes") }})
	}, 800, 600)
	tt.Frame()
	notice, _ := tt.Find("notice")
	panes, ok := tt.Find("panes")
	if !ok || panes.Y <= notice.Y {
		t.Fatalf("panes at %+v, notice at %+v; want the panes below the notice", panes, notice)
	}
}
