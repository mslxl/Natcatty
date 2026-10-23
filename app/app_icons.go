package app

import (
	"bytes"
	"embed"
	"image"
	"image/png"
	"sync"

	xdraw "golang.org/x/image/draw"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

// The twelve icon variants are the original's own PNG artwork. Embedding them
// keeps the build asset-free at runtime and preserves the exact visuals the
// Electron application shipped.
//
//go:embed icons/variants/*.png
var iconAssets embed.FS

// appIconPNG returns the variant's full-resolution artwork. An unknown ID or a
// failed read falls back to the original icon, as the original manager did.
func appIconPNG(variant string) []byte {
	id := domaintheme.ResolveIconVariant(variant).ID
	data, err := iconAssets.ReadFile("icons/variants/" + id + ".png")
	if err != nil {
		data, _ = iconAssets.ReadFile("icons/variants/original.png")
	}
	return data
}

var (
	trayVariantMu    sync.Mutex
	trayVariantCache = make(map[string][]byte)
)

// trayIconPNG returns the notification-area icon for a variant. The original
// variant keeps the hand-drawn 32px mark; every other variant is cropped to
// its artwork and scaled to 32px, because a tray icon taken from the 1024px
// asset would render tiny inside its transparent margin.
func trayIconPNG(variant string) []byte {
	id := domaintheme.ResolveIconVariant(variant).ID
	if id == domaintheme.DefaultIconVariant {
		return trayIconGlyphPNG()
	}
	trayVariantMu.Lock()
	defer trayVariantMu.Unlock()
	if data, ok := trayVariantCache[id]; ok {
		return data
	}
	data := scaleTrayIcon(appIconPNG(id))
	if data == nil {
		data = trayIconGlyphPNG()
	}
	trayVariantCache[id] = data
	return data
}

// scaleTrayIcon crops transparent margins and scales the result to 32x32,
// following the original's Windows tray path.
func scaleTrayIcon(data []byte) []byte {
	source, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	cropped := cropIconArtwork(source)
	const size = 32
	target := image.NewNRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(target, target.Bounds(), cropped, cropped.Bounds(), xdraw.Over, nil)
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, target); err != nil {
		return nil
	}
	return buffer.Bytes()
}

// cropIconArtwork trims transparent pixels and returns a centered square, as
// the original cropTransparentMargin did. An empty image is returned as is.
func cropIconArtwork(source image.Image) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	minX, minY := width, height
	maxX, maxY := -1, -1
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			_, _, _, alpha := source.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			if alpha>>8 <= 16 {
				continue
			}
			if x < minX {
				minX = x
			}
			if y < minY {
				minY = y
			}
			if x > maxX {
				maxX = x
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	if maxX < minX || maxY < minY {
		return source
	}
	const pad = 1
	left := max(0, minX-pad)
	top := max(0, minY-pad)
	right := min(width-1, maxX+pad)
	bottom := min(height-1, maxY+pad)
	side := min(max(right-left+1, bottom-top+1), min(width, height))
	offsetX := int(roundHalf((float64(left+right) - float64(side-1)) / 2))
	offsetY := int(roundHalf((float64(top+bottom) - float64(side-1)) / 2))
	offsetX = max(0, min(offsetX, width-side))
	offsetY = max(0, min(offsetY, height-side))
	rect := image.Rect(bounds.Min.X+offsetX, bounds.Min.Y+offsetY,
		bounds.Min.X+offsetX+side, bounds.Min.Y+offsetY+side)
	type subImager interface {
		SubImage(image.Rectangle) image.Image
	}
	if sub, ok := source.(subImager); ok {
		return sub.SubImage(rect)
	}
	return source
}

func roundHalf(value float64) float64 {
	if value < 0 {
		return float64(int(value - 0.5))
	}
	return float64(int(value + 0.5))
}
