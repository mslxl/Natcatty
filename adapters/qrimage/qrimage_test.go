package qrimage

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func encodeBlankPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.Gray{Y: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buffer.Bytes()
}

func TestDecodeRejectsNonImageBytes(t *testing.T) {
	if _, err := Decode([]byte("not an image")); !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("error = %v, want ErrUnsupportedImage", err)
	}
}

func TestDecodeReportsAnImageWithoutAQRCode(t *testing.T) {
	if _, err := Decode(encodeBlankPNG(t)); !errors.Is(err, ErrNoQRCode) {
		t.Fatalf("error = %v, want ErrNoQRCode", err)
	}
}
