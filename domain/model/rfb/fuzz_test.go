// SPDX-License-Identifier: GPL-3.0-or-later

package rfb

// Fuzz targets for the rectangle decoders and the rectangle dispatcher. Each
// target decodes arbitrary bytes into a fixed 8x8 framebuffer, so dimensions
// cannot drive an allocation; the decoders must return an error rather than
// panic. The seed corpus is also exercised by the ordinary `go test` job.
//
//	go test ./domain/model/rfb -run '^$' -fuzz '^FuzzDecodeRaw$' -fuzztime 20s

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

const fuzzInputLimit = 64 << 10

func fuzzPixels() []color.RGBA {
	pixels := make([]color.RGBA, 64)
	for i := range pixels {
		pixels[i] = color.RGBA{R: uint8(i * 4), G: uint8(255 - i*3), B: uint8(i * 7), A: 255}
	}
	return pixels
}

func FuzzDecodeRaw(f *testing.F) {
	pf := DefaultPixelFormat()
	f.Add(appendPixels(nil, pf, fuzzPixels()...))
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 32))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzInputLimit {
			t.Skip()
		}
		frame, err := NewFramebuffer(8, 8, pf)
		if err != nil {
			t.Fatalf("NewFramebuffer: %v", err)
		}
		_ = frame.decodeRaw(bytes.NewReader(data), image.Rect(0, 0, 8, 8))
	})
}

func FuzzDecodeCopyRect(f *testing.F) {
	pf := DefaultPixelFormat()
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 8))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzInputLimit {
			t.Skip()
		}
		frame, err := NewFramebuffer(8, 8, pf)
		if err != nil {
			t.Fatalf("NewFramebuffer: %v", err)
		}
		_ = frame.decodeCopyRect(bytes.NewReader(data), image.Rect(0, 0, 8, 8))
	})
}

func FuzzDecodeRRE(f *testing.F) {
	pf := DefaultPixelFormat()
	seed := []byte{0, 0, 0, 0}
	seed = appendPixels(seed, pf, red)
	f.Add(seed)
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 32))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzInputLimit {
			t.Skip()
		}
		frame, err := NewFramebuffer(8, 8, pf)
		if err != nil {
			t.Fatalf("NewFramebuffer: %v", err)
		}
		_ = frame.decodeRRE(bytes.NewReader(data), image.Rect(0, 0, 8, 8))
	})
}

func FuzzDecodeHextile(f *testing.F) {
	pf := DefaultPixelFormat()
	seed := []byte{hextileRaw}
	seed = appendPixels(seed, pf, fuzzPixels()...)
	f.Add(seed)
	f.Add([]byte{0})
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 32))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzInputLimit {
			t.Skip()
		}
		frame, err := NewFramebuffer(8, 8, pf)
		if err != nil {
			t.Fatalf("NewFramebuffer: %v", err)
		}
		_ = frame.decodeHextile(bytes.NewReader(data), image.Rect(0, 0, 8, 8))
	})
}

func FuzzReadRectangles(f *testing.F) {
	f.Add(append(rectHeader(0, 0, 1, 1, EncodingRaw), appendPixels(nil, DefaultPixelFormat(), red)...))
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 64))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzInputLimit {
			t.Skip()
		}
		frame, err := NewFramebuffer(8, 8, DefaultPixelFormat())
		if err != nil {
			t.Fatalf("NewFramebuffer: %v", err)
		}
		_, _ = frame.ReadRectangles(bytes.NewReader(data), 4)
	})
}
