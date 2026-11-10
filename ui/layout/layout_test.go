package layout

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/kit"
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

// TestTheMainWindowIsOpaqueInEveryStyleEvenOverAVibrancy checks the main
// window's frame: the title bar, the home page, and a terminal take the
// theme's colors in both styles, whether or not the window has a vibrancy.
func TestTheMainWindowIsOpaqueInEveryStyleEvenOverAVibrancy(t *testing.T) {
	for _, style := range []kit.Style{kit.MacOS, kit.Classic} {
		for _, vibrant := range []bool{false, true} {
			for _, work := range []bool{false, true} {
				kit.SetStyle(style)
				tt := ui.NewTester(func(c *ui.Context) {
					WindowFrame(c, FrameOptions{Opaque: true, Vibrant: true},
						func() { ui.Row(c).FillWidth().Height(36) },
						func() {
							if work {
								WorkLayout(c, WorkSlots{Panes: func() { ui.Text(c, "panes") }})
								return
							}
							HomeLayout(c, HomeSlots{Sidebar: func() {}, Content: func() { ui.Text(c, "page") }})
						})
				}, 800, 600)
				tt.SetVibrancy(vibrant)
				tt.Frame()
				img := tt.Image()
				for _, at := range [][2]int{{400, 18}, {100, 300}, {500, 400}} {
					if a := img.RGBAAt(at[0], at[1]).A; a != 255 {
						t.Errorf("%s vibrancy=%v work=%v: alpha %d at %v, want opaque", style.ID(), vibrant, work, a, at)
					}
				}
			}
		}
	}
	kit.SetStyle(nil)
}
