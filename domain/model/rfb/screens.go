package rfb

import "fmt"

// MaxScreens is the number of screens an ExtendedDesktopSize layout may carry.
// The wire field is one octet.
const MaxScreens = 255

// ScreenRowLayout builds a layout of count screens, each width by height, laid
// left to right in one row, and returns the desktop size their union spans.
//
// It is the layout a viewer asks for when it wants one screen per pane: every
// pane keeps a monitor of the size the desktop already had. Screens are given
// IDs 1..count, which are stable within this layout.
func ScreenRowLayout(count int, width, height uint16) (uint16, uint16, []Screen, error) {
	if count < 1 || count > MaxScreens {
		return 0, 0, nil, fmt.Errorf("%w: %d screens", ErrInvalidRectangle, count)
	}
	if width == 0 || height == 0 {
		return 0, 0, nil, fmt.Errorf("%w: screen %dx%d", ErrInvalidRectangle, width, height)
	}
	if int64(count)*int64(width) > 0xffff {
		return 0, 0, nil, fmt.Errorf("%w: %d screens of width %d", ErrFramebufferTooLarge, count, width)
	}
	screens := make([]Screen, count)
	for i := range screens {
		screens[i] = Screen{
			ID:     uint32(i + 1),
			X:      uint16(i) * width,
			Width:  width,
			Height: height,
		}
	}
	return uint16(int(count) * int(width)), height, screens, nil
}
