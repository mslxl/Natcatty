package shell

import "github.com/egoist/mygo/ui"

// cardInset is the distance from the content card's outer edge to the page
// drawn inside it.
const cardInset = cardBorder + cardPadding

// homeContent is where a home page (the Vault, monitoring, System, Logs) is
// drawn in a width×height window: right of the sidebar, below the Tab bar,
// inside the content card.
func homeContent(width, height float32) ui.Rect {
	left, top := sidebarWidth+cardInset, topBarHeight+cardInset
	return ui.Rect{X: left, Y: top, W: width - cardGap - cardInset - left, H: height - cardGap - cardInset - top}
}

// wideContent is where SFTP is drawn: the content card spans the window
// below the Tab bar.
func wideContent(width, height float32) ui.Rect {
	left, top := cardGap+cardInset, topBarHeight+cardInset
	return ui.Rect{X: left, Y: top, W: width - cardGap - cardInset - left, H: height - cardGap - cardInset - top}
}
