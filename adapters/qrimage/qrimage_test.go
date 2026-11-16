package qrimage

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
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

func TestDecodeReadsARealEnrollmentQRCode(t *testing.T) {
	const payload = "otpauth://totp/Acme:deploy?secret=JBSWY3DPEHPK3PXP"
	matrix, err := qrcode.NewQRCodeWriter().Encode(
		payload, gozxing.BarcodeFormat_QR_CODE, 256, 256, nil)
	if err != nil {
		t.Fatalf("encode QR: %v", err)
	}
	img := image.NewGray(image.Rect(0, 0, matrix.GetWidth(), matrix.GetHeight()))
	for y := 0; y < matrix.GetHeight(); y++ {
		for x := 0; x < matrix.GetWidth(); x++ {
			value := uint8(255)
			if matrix.Get(x, y) {
				value = 0
			}
			img.SetGray(x, y, color.Gray{Y: value})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}

	payloads, err := Decode(buffer.Bytes())
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(payloads) != 1 || payloads[0] != payload {
		t.Fatalf("payloads = %v, want the enrollment URI", payloads)
	}
}
