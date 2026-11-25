package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestWorkTabWidthBoundCoversLaidOutTabs is the proof the strip's open
// decision rests on: for every title length and marker state, in both
// Interface Styles, WorkTabWidthBound is at least the width workTab lays out,
// and at most the safety margin wider. The bound charges the status-dot lead,
// the two reserved Mini controls, the row's gaps and padding, and the measured
// title and marker, then clamps to the Work Tab's own width range, so a Tab
// can never be wider than the bound a first-overflow check trusts (ticket 20).
func TestWorkTabWidthBoundCoversLaidOutTabs(t *testing.T) {
	titles := []string{
		"",
		"a",
		"medium-title-here",
		"wide-overflow-16",
		"zzzz-this-is-a-max-width-title-zzzz-zzzz",
	}
	markers := []string{"", "●"}
	active := []bool{false, true}

	for _, style := range []struct {
		name string
		s    Style
	}{{"classic", Classic}, {"macos", MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			SetStyle(style.s)
			t.Cleanup(func() { SetStyle(Classic) })

			type measured struct {
				title, marker string
				active        bool
				bound, actual float32
			}
			var all []measured
			var shortBound, longBound float32
			tt := ui.NewTester(func(c *ui.Context) {
				all = all[:0]
				shortBound = WorkTabWidthBound(c, "a", "")
				longBound = WorkTabWidthBound(c, titles[len(titles)-1], "")
				TabStrip(c, TabWork, TabStripOptions{Height: 36}, func() {
					for _, title := range titles {
						for _, marker := range markers {
							for _, on := range active {
								body, _ := Tab(c, TabWork, TabSpec{
									Title: title, Marker: marker, Active: on,
									Close: "x", MenuLabel: "menu", Menu: func(*ui.Menu) {},
									Lead: func() { StatusDot(c, c.Theme().Accent) },
								})
								all = append(all, measured{
									title: title, marker: marker, active: on,
									bound:  WorkTabWidthBound(c, title, marker),
									actual: body.LayoutBounds().W,
								})
							}
						}
					}
				})
			}, 8000, 200)
			tt.SetLocale("en")
			tt.Frame()
			tt.Frame()

			if want := len(titles) * len(markers) * len(active); len(all) != want {
				t.Fatalf("measured %d Tabs, want %d", len(all), want)
			}
			for _, m := range all {
				if m.actual <= 0 {
					t.Fatalf("Tab %q marker=%q active=%v has no laid-out width", m.title, m.marker, m.active)
				}
				if m.bound < m.actual {
					t.Fatalf("Tab %q marker=%q active=%v: bound %v is below its laid-out %v",
						m.title, m.marker, m.active, m.bound, m.actual)
				}
				if m.bound-m.actual > workTabWidthSafety+2 {
					t.Fatalf("Tab %q marker=%q active=%v: bound %v is %v above its laid-out %v, want at most the safety margin",
						m.title, m.marker, m.active, m.bound, m.bound-m.actual, m.actual)
				}
				if m.bound < WorkTabMinWidth || m.bound > WorkTabMaxWidth {
					t.Fatalf("Tab %q marker=%q active=%v: bound %v is outside the Work Tab range [%v, %v]",
						m.title, m.marker, m.active, m.bound, WorkTabMinWidth, WorkTabMaxWidth)
				}
			}

			// The shortest row is not clamped to the minimum: the fixed room
			// plus the title's own minimum already exceeds it, so a Tab with
			// a one-glyph title is bounded by its real content.
			if shortBound <= WorkTabMinWidth {
				t.Fatalf("the one-glyph bound %v is not above the minimum %v", shortBound, WorkTabMinWidth)
			}
			// The longest row is capped exactly at the maximum, which is what
			// workTab's MaxWidth lays out.
			if longBound != WorkTabMaxWidth {
				t.Fatalf("the maximum title bound = %v, want the %v cap", longBound, WorkTabMaxWidth)
			}
		})
	}
}
