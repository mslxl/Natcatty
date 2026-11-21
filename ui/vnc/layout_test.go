package vnc

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
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
