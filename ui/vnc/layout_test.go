package vnc

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
)

func almostEqual(a, b float32) bool { return math.Abs(float64(a-b)) < 0.01 }

func checkRect(t *testing.T, got, want ui.Rect) {
	t.Helper()
	if !almostEqual(got.X, want.X) || !almostEqual(got.Y, want.Y) ||
		!almostEqual(got.W, want.W) || !almostEqual(got.H, want.H) {
		t.Fatalf("rect = %+v, want %+v", got, want)
	}
}

// TestFitRectLetterboxesBothAspectDirections covers a framebuffer wider and
// narrower than its pane: the content keeps its aspect ratio, centers, and
// leaves the bars on the long axis.
func TestFitRectLetterboxesBothAspectDirections(t *testing.T) {
	tests := []struct {
		name  string
		pane  ui.Rect
		frame image.Rectangle
		want  ui.Rect
	}{
		{
			name:  "wide desktop in a taller pane",
			pane:  ui.Rect{W: 400, H: 300},
			frame: image.Rect(0, 0, 640, 320),
			want:  ui.Rect{X: 0, Y: 50, W: 400, H: 200},
		},
		{
			name:  "4:3 desktop in a tall pane",
			pane:  ui.Rect{W: 300, H: 400},
			frame: image.Rect(0, 0, 800, 600),
			want:  ui.Rect{X: 0, Y: 87.5, W: 300, H: 225},
		},
		{
			name:  "larger desktop downscaled",
			pane:  ui.Rect{W: 100, H: 100},
			frame: image.Rect(0, 0, 1920, 1080),
			want:  ui.Rect{X: 0, Y: 21.875, W: 100, H: 56.25},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkRect(t, fitRect(test.pane, test.frame), test.want)
		})
	}
}

// TestRemotePointInvertsTheFit maps a pointer through the same scale the
// bitmap is drawn with, including a click in the letterbox band.
func TestRemotePointInvertsTheFit(t *testing.T) {
	pane := ui.Rect{W: 400, H: 300}
	frame := image.Rect(0, 0, 640, 320)
	tests := []struct {
		x, y         float32
		wantX, wantY int
	}{
		{x: 200, y: 150, wantX: 320, wantY: 160},
		{x: 0, y: 0, wantX: 0, wantY: 0},
		{x: 399, y: 299, wantX: 638, wantY: 319},
	}
	for _, test := range tests {
		x, y := remotePoint(test.x, test.y, pane, frame)
		if x != test.wantX || y != test.wantY {
			t.Fatalf("remotePoint(%v,%v) = %d,%d, want %d,%d",
				test.x, test.y, x, y, test.wantX, test.wantY)
		}
	}
}

// TestContentRectKeepsPixelsAtOneToOneAndClip covers the three scale modes: fit
// letterboxes, 1:1 centers without resampling or clipping the rectangle itself,
// and clip shifts that same rectangle by a drag offset.
func TestContentRectKeepsPixelsAtOneToOneAndClip(t *testing.T) {
	frame := image.Rect(0, 0, 640, 320)
	tests := []struct {
		name  string
		pane  ui.Rect
		mode  modelvnc.FitMode
		panX  float32
		panY  float32
		frame image.Rectangle
		want  ui.Rect
	}{
		{
			name:  "fit letterboxes a wide desktop",
			pane:  ui.Rect{W: 400, H: 300},
			mode:  modelvnc.Fit,
			frame: frame,
			want:  ui.Rect{X: 0, Y: 50, W: 400, H: 200},
		},
		{
			name:  "one to one centers a desktop larger than the pane",
			pane:  ui.Rect{W: 400, H: 300},
			mode:  modelvnc.OneToOne,
			frame: frame,
			want:  ui.Rect{X: -120, Y: -10, W: 640, H: 320},
		},
		{
			name:  "clip centers before any drag",
			pane:  ui.Rect{W: 400, H: 300},
			mode:  modelvnc.Clip,
			frame: frame,
			want:  ui.Rect{X: -120, Y: -10, W: 640, H: 320},
		},
		{
			name:  "clip follows the drag offset",
			pane:  ui.Rect{W: 400, H: 300},
			mode:  modelvnc.Clip,
			panX:  30,
			panY:  20,
			frame: frame,
			want:  ui.Rect{X: -90, Y: 0, W: 640, H: 320},
		},
		{
			name:  "one to one centers a desktop smaller than the pane",
			pane:  ui.Rect{W: 160, H: 120},
			mode:  modelvnc.OneToOne,
			frame: image.Rect(0, 0, 4, 4),
			want:  ui.Rect{X: 78, Y: 58, W: 4, H: 4},
		},
		{
			name:  "a small desktop cannot pan in clip mode",
			pane:  ui.Rect{W: 160, H: 120},
			mode:  modelvnc.Clip,
			panX:  50,
			panY:  50,
			frame: image.Rect(0, 0, 4, 4),
			want:  ui.Rect{X: 78, Y: 58, W: 4, H: 4},
		},
		{
			name:  "the pane origin is preserved",
			pane:  ui.Rect{X: 10, Y: 20, W: 100, H: 100},
			mode:  modelvnc.OneToOne,
			frame: image.Rect(0, 0, 20, 20),
			want:  ui.Rect{X: 50, Y: 60, W: 20, H: 20},
		},
		{
			name:  "an empty framebuffer draws nothing",
			pane:  ui.Rect{W: 100, H: 100},
			mode:  modelvnc.Fit,
			frame: image.Rectangle{},
			want:  ui.Rect{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkRect(t, contentRect(test.pane, test.frame, test.mode, test.panX, test.panY), test.want)
		})
	}
}

// TestClampPanStopsAtTheDesktopEdges proves a dragged viewport cannot show a
// gap past either edge, and that a desktop no larger than the pane stays
// centered.
func TestClampPanStopsAtTheDesktopEdges(t *testing.T) {
	tests := []struct {
		pan, content, pane float32
		want               float32
	}{
		{pan: 0, content: 320, pane: 160, want: 0},
		{pan: 30, content: 320, pane: 160, want: 30},
		{pan: 1000, content: 320, pane: 160, want: 80},
		{pan: -1000, content: 320, pane: 160, want: -80},
		{pan: 1000, content: 200, pane: 160, want: 20},
		{pan: -1000, content: 200, pane: 160, want: -20},
		{pan: 1000, content: 160, pane: 160, want: 0},
		{pan: 1000, content: 4, pane: 160, want: 0},
	}
	for _, test := range tests {
		if got := clampPan(test.pan, test.content, test.pane); !almostEqual(got, test.want) {
			t.Errorf("clampPan(%v,%v,%v) = %v, want %v",
				test.pan, test.content, test.pane, got, test.want)
		}
	}
}

// TestRemotePointForInvertsEveryScaleMode maps a pointer through the rectangle
// the draw path paints, so a click names the pixel under the pointer in fit,
// 1:1 and a panned clip viewport. Out-of-content positions clamp to the
// framebuffer instead of naming a nonexistent pixel.
func TestRemotePointForInvertsEveryScaleMode(t *testing.T) {
	frame := image.Rect(0, 0, 640, 320)
	pane := ui.Rect{W: 400, H: 300}
	tests := []struct {
		name         string
		x, y         float32
		mode         modelvnc.FitMode
		panX, panY   float32
		wantX, wantY int
	}{
		{name: "fit center", x: 200, y: 150, mode: modelvnc.Fit, wantX: 320, wantY: 160},
		{name: "fit top left", x: 0, y: 50, mode: modelvnc.Fit, wantX: 0, wantY: 0},
		{name: "fit bottom right", x: 399, y: 249, mode: modelvnc.Fit, wantX: 638, wantY: 318},
		{name: "fit letterbox clamps", x: 0, y: 0, mode: modelvnc.Fit, wantX: 0, wantY: 0},
		{name: "one to one center", x: 200, y: 150, mode: modelvnc.OneToOne, wantX: 320, wantY: 160},
		{name: "one to one top left", x: 0, y: 0, mode: modelvnc.OneToOne, wantX: 120, wantY: 10},
		{name: "one to one bottom right", x: 399, y: 299, mode: modelvnc.OneToOne, wantX: 519, wantY: 309},
		{name: "clip pan shifts the origin", x: 200, y: 150, mode: modelvnc.Clip, panX: 30, panY: 20, wantX: 290, wantY: 150},
		{name: "clip clamps past the left edge", x: 0, y: 0, mode: modelvnc.Clip, panX: 30, panY: 20, wantX: 90, wantY: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			x, y := remotePointFor(test.x, test.y, pane, frame, test.mode, test.panX, test.panY)
			if x != test.wantX || y != test.wantY {
				t.Fatalf("remotePointFor = %d,%d, want %d,%d", x, y, test.wantX, test.wantY)
			}
		})
	}

	// A framed pane larger than the framebuffer with the maximum pan shows the
	// far corner under its bottom-right pixel.
	small := ui.Rect{W: 160, H: 120}
	if x, y := remotePointFor(159, 119, small, image.Rect(0, 0, 320, 240), modelvnc.Clip, 1000, 1000); x != 159 || y != 119 {
		t.Fatalf("clamped clip point = %d,%d, want 159,119", x, y)
	}
}

// TestScaleNearestRepeatsWholePixels proves both shrinking and growing keep
// the source's exact colors rather than blending neighbors.
func TestScaleNearestRepeatsWholePixels(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if x < 2 && y < 2 {
				src.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
			} else {
				src.SetRGBA(x, y, color.RGBA{B: 255, A: 255})
			}
		}
	}
	for _, size := range []image.Point{{X: 2, Y: 2}, {X: 8, Y: 8}, {X: 3, Y: 3}} {
		dst := scaleNearest(src, size.X, size.Y)
		if dst.Bounds() != image.Rect(0, 0, size.X, size.Y) {
			t.Fatalf("scaleNearest bounds = %v, want %v", dst.Bounds(), size)
		}
		for y := 0; y < size.Y; y++ {
			for x := 0; x < size.X; x++ {
				pixel := dst.RGBAAt(x, y)
				if pixel != (color.RGBA{R: 255, A: 255}) && pixel != (color.RGBA{B: 255, A: 255}) {
					t.Fatalf("scaleNearest(%v) pixel (%d,%d) = %+v, want a source color", size, x, y, pixel)
				}
			}
		}
	}
}

// TestKeysymForRune covers printable ASCII, Latin-1, and the Unicode range.
func TestKeysymForRune(t *testing.T) {
	tests := map[rune]uint32{
		'A': 0x41,
		'a': 0x61,
		' ': 0x20,
		'é': 0xe9,
		'中': unicodeKeysymBase | 0x4e2d,
		'☃': unicodeKeysymBase | 0x2603,
	}
	for r, want := range tests {
		if got := KeysymForRune(r); got != want {
			t.Fatalf("KeysymForRune(%q) = %#x, want %#x", r, got, want)
		}
	}
}

// TestButtonMaskMapsEveryPointerButton pins the wire bits MyGo's button
// numbers map onto: primary 1, middle 2, secondary 4, combined while several
// are held. Clip mode defers only the primary press, so the other buttons must
// keep working.
func TestButtonMaskMapsEveryPointerButton(t *testing.T) {
	cases := []struct {
		button int
		bit    uint8
	}{
		{button: 0, bit: 1},
		{button: 1, bit: 4},
		{button: 2, bit: 2},
		{button: 3, bit: 0},
	}
	for _, test := range cases {
		if got := buttonMask(test.button); got != test.bit {
			t.Errorf("buttonMask(%d) = %d, want %d", test.button, got, test.bit)
		}
	}
	mask, ok := applyButton(0, 2, true) // middle down
	if !ok || mask != 2 {
		t.Fatalf("middle down = %d, %v; want 2, true", mask, ok)
	}
	mask, ok = applyButton(mask, 1, true) // secondary joins the held middle
	if !ok || mask != 6 {
		t.Fatalf("secondary joins = %d, %v; want 6, true", mask, ok)
	}
	mask, ok = applyButton(mask, 2, false)
	if !ok || mask != 4 {
		t.Fatalf("middle up = %d, %v; want 4, true", mask, ok)
	}
}
