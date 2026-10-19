package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"sync"
)

var (
	trayIconOnce sync.Once
	trayIconData []byte
)

// trayIconPNG returns the 32x32 notification-area icon. The glyph is drawn
// from rectangles so the build needs no image asset and no C compiler.
func trayIconPNG() []byte {
	trayIconOnce.Do(func() {
		const size = 32
		icon := image.NewNRGBA(image.Rect(0, 0, size, size))
		background := color.NRGBA{R: 0x2f, G: 0x6f, B: 0xeb, A: 0xff}
		foreground := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
		fillRounded(icon, 3, 3, 29, 29, 7, background)
		// Prompt ">" and underscore, the terminal mark the original used.
		fillRect(icon, 9, 10, 2, 2, foreground)
		fillRect(icon, 11, 12, 2, 2, foreground)
		fillRect(icon, 13, 14, 2, 2, foreground)
		fillRect(icon, 11, 16, 2, 2, foreground)
		fillRect(icon, 9, 18, 2, 2, foreground)
		fillRect(icon, 16, 20, 7, 2, foreground)
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, icon); err != nil {
			return
		}
		trayIconData = buffer.Bytes()
	})
	return trayIconData
}

func fillRect(destination *image.NRGBA, x, y, width, height int, value color.NRGBA) {
	for row := y; row < y+height; row++ {
		for column := x; column < x+width; column++ {
			destination.SetNRGBA(column, row, value)
		}
	}
}

// fillRounded fills a rounded rectangle whose corners have the given radius.
func fillRounded(destination *image.NRGBA, left, top, right, bottom, radius int, value color.NRGBA) {
	for y := top; y < bottom; y++ {
		for x := left; x < right; x++ {
			dx, dy := 0, 0
			if x < left+radius {
				dx = left + radius - x
			} else if x >= right-radius {
				dx = x - (right - radius) + 1
			}
			if y < top+radius {
				dy = top + radius - y
			} else if y >= bottom-radius {
				dy = y - (bottom - radius) + 1
			}
			if dx*dx+dy*dy > radius*radius {
				continue
			}
			destination.SetNRGBA(x, y, value)
		}
	}
}
