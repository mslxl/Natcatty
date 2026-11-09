package settings

import (
	"bytes"
	"image"
	"image/png"

	"github.com/egoist/mygo/ui"
	xdraw "golang.org/x/image/draw"
)

// iconThumbSize is the pixel size an icon variant's artwork is scaled to for
// the picker: twice its drawn size, so it stays sharp on a Retina display.
const iconThumbSize = 80

// WithIconArtwork sets the reader of an app-icon variant's PNG artwork, which
// the icon picker draws as a thumbnail. Without it the picker shows names.
func WithIconArtwork(fn func(variant string) []byte) PreferencesOption {
	return func(s *PreferencesState) { s.iconArtwork = fn }
}

// iconThumb returns the variant's picker thumbnail, or nil when there is no
// artwork. The 1024px artwork is scaled once and kept.
func (s *PreferencesState) iconThumb(variant string) *ui.Bitmap {
	if s.iconArtwork == nil {
		return nil
	}
	if bitmap, ok := s.iconThumbs[variant]; ok {
		return bitmap
	}
	if s.iconThumbs == nil {
		s.iconThumbs = make(map[string]*ui.Bitmap)
	}
	var bitmap *ui.Bitmap
	if source, err := png.Decode(bytes.NewReader(s.iconArtwork(variant))); err == nil {
		target := image.NewNRGBA(image.Rect(0, 0, iconThumbSize, iconThumbSize))
		xdraw.CatmullRom.Scale(target, target.Bounds(), source, source.Bounds(), xdraw.Over, nil)
		bitmap = ui.NewBitmap(target)
	}
	s.iconThumbs[variant] = bitmap
	return bitmap
}
