package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestInfoPillRendersItsLabelAndValueInBothStyles proves the pill shows the
// label and the value as separate texts and keeps an unknown value visible in
// Classic and macOS.
func TestInfoPillRendersItsLabelAndValueInBothStyles(t *testing.T) {
	t.Cleanup(func() { SetStyle(nil) })
	for _, style := range []Style{Classic, MacOS} {
		SetStyle(style)
		for _, value := range []string{"0.50 0.40 0.30", "—"} {
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Row(c).Padding(8).Children(func() {
					InfoPill(c, "Load", value)
				})
			}, 240, 80)
			tt.Frame()
			label, ok := tt.Find("Load")
			if !ok {
				t.Fatalf("%s: pill lacks its label: %v", style.ID(), tt.Texts())
			}
			rendered, ok := tt.Find(value)
			if !ok {
				t.Fatalf("%s: pill lacks value %q: %v", style.ID(), value, tt.Texts())
			}
			if label.X+label.W > rendered.X+0.5 {
				t.Errorf("%s: value %q starts at %v before the label ends at %v",
					style.ID(), value, rendered.X, label.X+label.W)
			}
		}
	}
}
