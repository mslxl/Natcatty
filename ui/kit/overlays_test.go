package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPaletteClosesOnEscapeAndItsChords(t *testing.T) {
	for _, press := range []Chord{{Key: ui.KeyEscape}, {Mods: ui.Shift, Key: ui.KeyT}} {
		open, backdrop := true, 0
		tt := ui.NewTester(func(c *ui.Context) {
			if !open {
				return
			}
			if Palette(c, PaletteOptions{
				Width: 300, Height: 200, Top: 24, Padding: 12, Gap: 8,
				OnBackdrop: func() { backdrop++ },
				Close:      []Chord{{Mods: ui.Shift, Key: ui.KeyT}},
			}, func() { ui.Text(c, "inside") }) {
				open = false
			}
		}, 600, 400)
		tt.Key(press.Mods, press.Key)
		tt.Frame()
		if open {
			t.Fatalf("chord %+v did not close the palette", press)
		}
		if backdrop != 0 {
			t.Fatalf("chord %+v ran the backdrop handler", press)
		}
	}
}

func TestPaletteBackdropClickRunsOnBackdropButPanelClickDoesNot(t *testing.T) {
	backdrop := 0
	tt := ui.NewTester(func(c *ui.Context) {
		Palette(c, PaletteOptions{
			Width: 300, Height: 200, Top: 24, Padding: 12, Gap: 8,
			OnBackdrop: func() { backdrop++ },
		}, func() { ui.Text(c, "inside") })
	}, 600, 400)
	if err := tt.Click("inside"); err != nil {
		t.Fatal(err)
	}
	if backdrop != 0 {
		t.Fatalf("a click inside the panel ran the backdrop handler")
	}
	tt.ClickAt(20, 380)
	if backdrop != 1 {
		t.Fatalf("backdrop clicks = %d, want 1", backdrop)
	}
}
