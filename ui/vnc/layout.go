package vnc

import (
	"image"

	"github.com/egoist/mygo/ui"

	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
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

// contentRect returns where the framebuffer is painted for a scale mode. Fit
// letterboxes the desktop; 1:1 and clip show one remote pixel per pane pixel,
// centered and clipped by the pane, with clip adding the drag offset. pan is
// relative to the centered position and is clamped so a clipped viewport
// never shows a gap past an edge.
func contentRect(pane ui.Rect, frame image.Rectangle, mode modelvnc.FitMode,
	panX, panY float32) ui.Rect {
	if frame.Empty() || pane.W <= 0 || pane.H <= 0 {
		return ui.Rect{}
	}
	mode = modelvnc.NormalizeFitMode(mode)
	if mode == modelvnc.Fit {
		return fitRect(pane, frame)
	}
	width, height := float32(frame.Dx()), float32(frame.Dy())
	x := pane.X + (pane.W-width)/2
	y := pane.Y + (pane.H-height)/2
	if mode == modelvnc.Clip {
		x += clampPan(panX, width, pane.W)
		y += clampPan(panY, height, pane.H)
	}
	return ui.Rect{X: x, Y: y, W: width, H: height}
}

// clampPan keeps a clip offset inside the pane. A framebuffer no larger than
// the pane has nothing to pan and is centered; a larger one may move only
// until one of its edges meets the pane's matching edge.
func clampPan(pan, content, pane float32) float32 {
	if content <= pane {
		return 0
	}
	centered := (pane - content) / 2
	clamped := min(max(centered+pan, pane-content), 0)
	return clamped - centered
}

// remotePoint maps a pointer position local to the pane (origin 0,0, as MyGo
// reports input) onto a framebuffer pixel, inverting the fit scaling.
// Positions in a letterbox band clamp to the nearest edge, so a click always
// names a real pixel.
func remotePoint(x, y float32, pane ui.Rect, frame image.Rectangle) (int, int) {
	return remotePointFor(x, y, pane, frame, modelvnc.Fit, 0, 0)
}

// remotePointFor inverts contentRect for every scale mode, including a clip
// pan offset, and clamps the result to the framebuffer so a pointer in a
// letterbox band or past a clipped edge always names a real pixel.
func remotePointFor(x, y float32, pane ui.Rect, frame image.Rectangle,
	mode modelvnc.FitMode, panX, panY float32) (int, int) {
	if frame.Empty() {
		return 0, 0
	}
	content := contentRect(pane, frame, mode, panX, panY)
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
