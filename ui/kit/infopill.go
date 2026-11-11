package kit

import (
	"github.com/egoist/mygo/ui"
)

// InfoPill is one compact label/value pair, as the Overview's identity and
// health pills. Classic uses a muted fill; the macOS Interface Style raises it
// on the surface as a bordered capsule.
func InfoPill(c *ui.Context, label, value string) ui.Element {
	t := c.Theme()
	pill := ui.Row(c).Gap(5).AlignItems(ui.Center).Padding(3, 8).Radius(M(c).Radius.Pill).
		Background(Muted(t)).Border(1, t.Border).Shrink(0)
	if CurrentStyle().Native() {
		pill.Background(t.Surface).Border(1, t.Border)
	}
	return pill.Children(func() {
		ui.Text(c, label).FontSize(M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
		ui.Text(c, value).FontSize(M(c).Font.Caption).TextColor(t.Text).SingleLine()
	})
}
