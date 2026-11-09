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

// TestAVibrantMacOSWindowShowsItsMaterialBesideAnOpaquePage checks the
// main window's frame over a vibrancy: the title bar is clear so the
// window's material shows, while the page and a terminal stay opaque. A
// Classic window, or one with no vibrancy, is opaque everywhere.
func TestAVibrantMacOSWindowShowsItsMaterialBesideAnOpaquePage(t *testing.T) {
	for _, tc := range []struct {
		style   kit.Style
		vibrant bool
		clear   bool
	}{
		{kit.MacOS, true, true},
		{kit.MacOS, false, false},
		{kit.Classic, true, false},
	} {
		for _, work := range []bool{false, true} {
			kit.SetStyle(tc.style)
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
			tt.SetVibrancy(tc.vibrant)
			tt.Frame()
			img := tt.Image()
			if bar := img.RGBAAt(400, 18).A; (bar == 0) != tc.clear {
				t.Errorf("%s vibrancy=%v: title bar alpha %d, want clear=%v", tc.style.ID(), tc.vibrant, bar, tc.clear)
			}
			if page := img.RGBAAt(500, 400).A; page != 255 {
				t.Errorf("%s vibrancy=%v work=%v: page alpha %d, want opaque", tc.style.ID(), tc.vibrant, work, page)
			}
		}
	}
	kit.SetStyle(nil)
}
