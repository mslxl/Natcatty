// Package qrimage decodes a QR symbol from PNG and JPEG files. It is the only
// place that knows the decoder, so the UI depends on payload strings rather
// than an image library.
package qrimage

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	// Register the formats a user can pick from the file dialog.
	_ "image/jpeg"
	_ "image/png"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// ErrNoQRCode reports an image that contains no readable QR symbol.
var ErrNoQRCode = errors.New("qrimage: no QR code found")

// ErrUnsupportedImage reports bytes that are not a PNG or JPEG image.
var ErrUnsupportedImage = errors.New("qrimage: unsupported image")

// Decode returns the payload of the strongest QR symbol in data. One symbol is
// enough for an authenticator import; a screen grab or a photo of an
// enrollment page carries one code. The payload may contain credential
// material, so callers must not log it.
func Decode(data []byte) ([]string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedImage, err)
	}
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil, fmt.Errorf("qrimage: binarize: %v", err)
	}
	result, err := qrcode.NewQRCodeReader().Decode(bitmap, nil)
	if err != nil {
		// A binarized image with no code, a damaged symbol, and a checksum
		// failure are all "nothing usable here" from the user's point of view.
		return nil, fmt.Errorf("%w: %v", ErrNoQRCode, err)
	}
	text := result.GetText()
	if text == "" {
		return nil, ErrNoQRCode
	}
	return []string{text}, nil
}
