package vnc

import (
	"image"

	"github.com/egoist/mygo/ui"
)

// fitRect returns where the framebuffer is painted inside the pane. It
// preserves the aspect ratio and centers the desktop, leaving letterbox bands
// when the aspect ratios differ.
func fitRect(pane ui.Rect, frame image.Rectangle) ui.Rect {
	if frame.Empty() || pane.W <= 0 || pane.H <= 0 {
		return ui.Rect{}
	}
	scale := min(pane.W/float32(frame.Dx()), pane.H/float32(frame.Dy()))
	if scale <= 0 {
		return ui.Rect{}
	}
	width := float32(frame.Dx()) * scale
	height := float32(frame.Dy()) * scale
	return ui.Rect{
		X: pane.X + (pane.W-width)/2,
		Y: pane.Y + (pane.H-height)/2,
		W: width,
		H: height,
	}
}

// remotePoint maps a pointer position local to the pane (origin 0,0, as MyGo
// reports input) onto a framebuffer pixel, inverting fitRect's scaling.
// Positions in a letterbox band clamp to the nearest edge, so a click always
// names a real pixel.
func remotePoint(x, y float32, pane ui.Rect, frame image.Rectangle) (int, int) {
	if frame.Empty() {
		return 0, 0
	}
	content := fitRect(pane, frame)
	if content.W <= 0 || content.H <= 0 {
		return 0, 0
	}
	scaleX := content.W / float32(frame.Dx())
	scaleY := content.H / float32(frame.Dy())
	rx := int((x - content.X) / scaleX)
	ry := int((y - content.Y) / scaleY)
	rx = min(max(rx, 0), frame.Dx()-1)
	ry = min(max(ry, 0), frame.Dy()-1)
	return rx, ry
}

// scaleNearest resamples src to width by height pixels by repeating the
// nearest source pixel. MyGo's Pixelated bitmap is crisp only when it grows,
// so the viewer resamples every frame it does not show at 1:1.
func scaleNearest(src *image.RGBA, width, height int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	if sw <= 0 || sh <= 0 || width <= 0 || height <= 0 {
		return dst
	}
	for y := 0; y < height; y++ {
		sy := y * sh / height
		row := dst.Pix[y*dst.Stride : y*dst.Stride+width*4]
		for x := 0; x < width; x++ {
			sx := x * sw / width
			si := src.PixOffset(src.Rect.Min.X+sx, src.Rect.Min.Y+sy)
			copy(row[x*4:x*4+4], src.Pix[si:si+4])
		}
	}
	return dst
}
