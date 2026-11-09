package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// BenchmarkPaletteFrame is a palette, as the Transfer center over a
// terminal, repainted whole every frame on the CPU renderer in each style:
// the macOS style blurs what is under its glass.
func BenchmarkPaletteFrame(b *testing.B) {
	b.Cleanup(func() { SetStyle(nil) })
	for _, style := range []Style{Classic, MacOS} {
		b.Run(style.ID(), func(b *testing.B) {
			tt := ui.NewTester(func(c *ui.Context) {
				SetStyle(style)
				ui.Column(c).Fill().Children(func() {
					for range 40 {
						ui.Text(c, "drwxr-xr-x  2 deploy deploy 4096 Nov 10 12:00 releases")
					}
				})
				Palette(c, PaletteOptions{Width: 640, Height: 420, Top: 48, Padding: 12, Gap: 8},
					func() { ui.Text(c, "Transfers") })
			}, 1280, 800)
			tt.Frame()
			b.ResetTimer()
			for i := range b.N {
				tt.SetSize(1280+i%2, 800)
				tt.Frame()
			}
		})
	}
}
