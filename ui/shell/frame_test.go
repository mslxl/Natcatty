package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/kit"
)

// cardInset is the distance from the content card's outer edge to the page
// drawn inside it.
const cardInset = cardBorder + cardPadding

// homeContent is where a home page (the Vault, monitoring, System, Logs) is
// drawn in a width×height window: right of the sidebar, below the Tab bar,
// inside the content card, or in the macOS style inside the flush page.
func homeContent(width, height float32) ui.Rect {
	return content(sidebarWidth, width, height)
}

// wideContent is where SFTP is drawn: the content card, or the flush page,
// spans the window below the Tab bar.
func wideContent(width, height float32) ui.Rect {
	return content(0, width, height)
}

func content(sidebar, width, height float32) ui.Rect {
	if kit.CurrentStyle().Native() {
		left, top := sidebar+shellPadding, topBarHeight+shellPadding
		return ui.Rect{X: left, Y: top, W: width - shellPadding - left, H: height - shellPadding - top}
	}
	left := sidebar + cardInset
	if sidebar == 0 {
		left = cardGap + cardInset
	}
	top := topBarHeight + cardInset
	return ui.Rect{X: left, Y: top, W: width - cardGap - cardInset - left, H: height - cardGap - cardInset - top}
}
