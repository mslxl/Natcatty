package blockview

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// barWindowTop is where the bar lands on screen: the card's border box plus its
// 1 DIP top padding (the padding-box origin Top is measured from) plus the
// offset the bar asks for.
func barWindowTop(viewport, card ui.Rect) float32 {
	return card.Y + 1 + actionBarOffset(viewport, card)
}

// The Native/Raw bar belongs to the block's visible range, not to the block's
// first row: scrolling a tall Lens used to carry it off the top, so the switch
// could not be reached without scrolling back. It must stay at the top-right of
// whatever part of the block is on screen, and stop before the block's end.
func TestActionBarSticksToTheVisibleTopOfTheBlock(t *testing.T) {
	viewport := ui.Rect{X: 0, Y: 0, W: 600, H: 300}
	// The card is taller than the viewport, as a long Markdown document is.
	const cardH = float32(1200)
	for _, cardY := range []float32{-1000, -400, -200, -30} {
		card := ui.Rect{Y: cardY, H: cardH}
		if got := barWindowTop(viewport, card); got < 3.9 || got > 4.1 {
			t.Fatalf("card.Y=%v puts the bar at %v, want the viewport top 4", cardY, got)
		}
	}
	// While the block's own top is on screen, the bar sits on the block
	// instead of jumping up to the viewport (a short block follows the flow,
	// it does not float), exactly where it is without any scrolling.
	card := ui.Rect{Y: 0, H: cardH}
	if got := barWindowTop(viewport, card); got < 4.9 || got > 5.1 {
		t.Fatalf("block at the viewport top puts its bar at %v, want 5", got)
	}
	card = ui.Rect{Y: 50, H: cardH}
	if got := barWindowTop(viewport, card); got < 54.9 || got > 55.1 {
		t.Fatalf("block starting below the viewport puts its bar at %v, want 55", got)
	}
	// At the block's end the bar stops above the last rows and never leaves
	// the lens.
	short := ui.Rect{Y: 0, H: 40}
	if got := barWindowTop(viewport, short); got < 4.9 || got > 5.1 {
		t.Fatalf("bar in a short block sits at %v, want 5", got)
	}
	if got := actionBarOffset(ui.Rect{}, short); got != actionBarTop {
		t.Fatalf("without a laid-out viewport the bar offset = %v, want %v", got, actionBarTop)
	}
}
