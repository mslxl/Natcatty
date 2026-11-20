package rfb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"
)

var (
	black = color.RGBA{A: 255}
	white = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	red   = color.RGBA{R: 255, A: 255}
	green = color.RGBA{G: 255, A: 255}
	blue  = color.RGBA{B: 255, A: 255}
)

func rectHeader(x, y, width, height uint16, encoding Encoding) []byte {
	var header [12]byte
	binary.BigEndian.PutUint16(header[0:2], x)
	binary.BigEndian.PutUint16(header[2:4], y)
	binary.BigEndian.PutUint16(header[4:6], width)
	binary.BigEndian.PutUint16(header[6:8], height)
	binary.BigEndian.PutUint32(header[8:12], uint32(encoding))
	return header[:]
}

func appendPixels(data []byte, pf PixelFormat, pixels ...color.RGBA) []byte {
	for _, pixel := range pixels {
		wire, err := pf.Encode(pixel)
		if err != nil {
			panic(err)
		}
		data = append(data, wire...)
	}
	return data
}

func rawRectangle(pf PixelFormat, x, y, width, height uint16, pixels []color.RGBA) []byte {
	data := rectHeader(x, y, width, height, EncodingRaw)
	return appendPixels(data, pf, pixels...)
}

func newFrame(t *testing.T, width, height int, pf PixelFormat) *Framebuffer {
	t.Helper()
	frame, err := NewFramebuffer(width, height, pf)
	if err != nil {
		t.Fatalf("NewFramebuffer: %v", err)
	}
	return frame
}

func readOne(t *testing.T, frame *Framebuffer, data []byte) Rectangle {
	t.Helper()
	rectangles, err := frame.ReadRectangles(bytes.NewReader(data), 1)
	if err != nil {
		t.Fatalf("ReadRectangles: %v", err)
	}
	if len(rectangles) != 1 {
		t.Fatalf("rectangles = %d, want 1", len(rectangles))
	}
	return rectangles[0]
}

func checkPixels(t *testing.T, frame *Framebuffer, want map[image.Point]color.RGBA) {
	t.Helper()
	for point, pixel := range want {
		got := frame.Image().RGBAAt(point.X, point.Y)
		if got != pixel {
			t.Fatalf("pixel %v = %+v, want %+v", point, got, pixel)
		}
	}
}

func TestDecodeRaw(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 2, 2, pf)
	data := rawRectangle(pf, 0, 0, 2, 2, []color.RGBA{
		red, green,
		blue, white,
	})
	rect := readOne(t, frame, data)
	if rect.X != 0 || rect.Y != 0 || rect.Width != 2 || rect.Height != 2 || rect.Encoding != EncodingRaw {
		t.Fatalf("rectangle = %+v", rect)
	}
	checkPixels(t, frame, map[image.Point]color.RGBA{
		{X: 0, Y: 0}: red,
		{X: 1, Y: 0}: green,
		{X: 0, Y: 1}: blue,
		{X: 1, Y: 1}: white,
	})
}

func TestDecodeRawSubRectangle(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 4, 3, pf)
	data := rawRectangle(pf, 1, 1, 2, 1, []color.RGBA{red, green})
	readOne(t, frame, data)
	checkPixels(t, frame, map[image.Point]color.RGBA{
		{X: 1, Y: 1}: red,
		{X: 2, Y: 1}: green,
		{X: 0, Y: 0}: {},
		{X: 3, Y: 2}: {},
	})
}

func TestDecodeCopyRect(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 4, 1, pf)
	frame.Image().SetRGBA(0, 0, red)
	frame.Image().SetRGBA(1, 0, green)
	frame.Image().SetRGBA(2, 0, blue)
	frame.Image().SetRGBA(3, 0, white)

	data := rectHeader(1, 0, 3, 1, EncodingCopyRect)
	var source [4]byte
	binary.BigEndian.PutUint16(source[0:2], 0)
	binary.BigEndian.PutUint16(source[2:4], 0)
	data = append(data, source[:]...)
	readOne(t, frame, data)

	checkPixels(t, frame, map[image.Point]color.RGBA{
		{X: 0, Y: 0}: red,
		{X: 1, Y: 0}: red,
		{X: 2, Y: 0}: green,
		{X: 3, Y: 0}: blue,
	})
}

func TestDecodeCopyRectOverlappingVertically(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 3, 3, pf)
	frame.Image().SetRGBA(0, 0, red)
	frame.Image().SetRGBA(1, 0, green)
	frame.Image().SetRGBA(0, 1, blue)
	frame.Image().SetRGBA(1, 1, white)

	data := rectHeader(1, 1, 2, 2, EncodingCopyRect)
	var source [4]byte
	data = append(data, source[:]...)
	readOne(t, frame, data)

	checkPixels(t, frame, map[image.Point]color.RGBA{
		{X: 1, Y: 1}: red,
		{X: 2, Y: 1}: green,
		{X: 1, Y: 2}: blue,
		{X: 2, Y: 2}: white,
	})
}

func TestDecodeRRE(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 4, 4, pf)

	data := rectHeader(0, 0, 4, 4, EncodingRRE)
	data = append(data, 0, 0, 0, 2) // two subrectangles
	data = appendPixels(data, pf, blue)
	data = appendPixels(data, pf, red)
	data = append(data, 0, 1, 0, 1, 0, 2, 0, 1) // 1,1 2x1
	data = appendPixels(data, pf, green)
	data = append(data, 0, 0, 0, 3, 0, 3, 0, 1) // 0,3 3x1
	readOne(t, frame, data)

	want := map[image.Point]color.RGBA{}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			want[image.Point{X: x, Y: y}] = blue
		}
	}
	want[image.Point{X: 1, Y: 1}] = red
	want[image.Point{X: 2, Y: 1}] = red
	for x := 0; x < 3; x++ {
		want[image.Point{X: x, Y: 3}] = green
	}
	checkPixels(t, frame, want)
}

func TestDecodeRREBackgroundOnly(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 2, 2, pf)
	data := rectHeader(0, 0, 2, 2, EncodingRRE)
	data = append(data, 0, 0, 0, 0)
	data = appendPixels(data, pf, red)
	readOne(t, frame, data)
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			if got := frame.Image().RGBAAt(x, y); got != red {
				t.Fatalf("pixel %d,%d = %+v", x, y, got)
			}
		}
	}
}

func TestDecodeHextileRawTile(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 2, 2, pf)
	data := rectHeader(0, 0, 2, 2, EncodingHextile)
	data = append(data, hextileRaw)
	data = appendPixels(data, pf, red, green, blue, white)
	readOne(t, frame, data)
	checkPixels(t, frame, map[image.Point]color.RGBA{
		{X: 0, Y: 0}: red,
		{X: 1, Y: 0}: green,
		{X: 0, Y: 1}: blue,
		{X: 1, Y: 1}: white,
	})
}

func TestDecodeHextileBackgroundForegroundSubrect(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 4, 4, pf)
	data := rectHeader(0, 0, 4, 4, EncodingHextile)
	data = append(data, hextileBackgroundSpecified|hextileForegroundSpecified|hextileAnySubrects)
	data = appendPixels(data, pf, black, white)
	data = append(data, 1, 0x00, 0x11) // one 2x2 subrect at the tile origin
	readOne(t, frame, data)

	want := map[image.Point]color.RGBA{}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			want[image.Point{X: x, Y: y}] = black
		}
	}
	want[image.Point{X: 0, Y: 0}] = white
	want[image.Point{X: 1, Y: 0}] = white
	want[image.Point{X: 0, Y: 1}] = white
	want[image.Point{X: 1, Y: 1}] = white
	checkPixels(t, frame, want)
}

func TestDecodeHextileCarriesBackgroundAndForegroundAcrossTiles(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 20, 4, pf)
	data := rectHeader(0, 0, 20, 4, EncodingHextile)
	// Tile 1: background red and foreground green, no subrects.
	data = append(data, hextileBackgroundSpecified|hextileForegroundSpecified)
	data = appendPixels(data, pf, red, green)
	// Tile 2: no background or foreground, one uncoloured 1x1 subrect.
	data = append(data, hextileAnySubrects)
	data = append(data, 1, 0x00, 0x00)
	readOne(t, frame, data)

	for y := 0; y < 4; y++ {
		for x := 0; x < 20; x++ {
			want := red
			if x == 16 && y == 0 {
				want = green
			}
			if got := frame.Image().RGBAAt(x, y); got != want {
				t.Fatalf("pixel %d,%d = %+v, want %+v", x, y, got, want)
			}
		}
	}
}

func TestDecodeMalformed(t *testing.T) {
	pf := DefaultPixelFormat()
	cases := []struct {
		name string
		data []byte
	}{
		{
			name: "raw truncated",
			data: rawRectangle(pf, 0, 0, 2, 2, []color.RGBA{red}),
		},
		{
			name: "raw outside framebuffer",
			data: rawRectangle(pf, 1, 0, 2, 2, []color.RGBA{red, green, blue, white}),
		},
		{
			name: "zero size",
			data: rectHeader(0, 0, 0, 1, EncodingRaw),
		},
		{
			name: "unknown encoding",
			data: rectHeader(0, 0, 1, 1, EncodingTight),
		},
		{
			name: "copyrect source outside",
			data: append(rectHeader(0, 0, 2, 2, EncodingCopyRect), 0, 1, 0, 1),
		},
		{
			name: "rre negative count",
			data: append(append(rectHeader(0, 0, 2, 2, EncodingRRE), 0xff, 0xff, 0xff, 0xff), appendPixels(nil, pf, red)...),
		},
		{
			name: "rre subrect outside",
			data: func() []byte {
				data := append(rectHeader(0, 0, 2, 2, EncodingRRE), 0, 0, 0, 1)
				data = appendPixels(data, pf, red, green)
				return append(data, 0, 0, 0, 1, 0, 2, 0, 2)
			}(),
		},
		{
			name: "hextile without background",
			data: append(rectHeader(0, 0, 2, 2, EncodingHextile), 0),
		},
		{
			name: "hextile subrect outside tile",
			data: func() []byte {
				data := append(rectHeader(0, 0, 4, 4, EncodingHextile),
					hextileBackgroundSpecified|hextileForegroundSpecified|hextileAnySubrects)
				data = appendPixels(data, pf, black, white)
				return append(data, 1, 0xff, 0xff)
			}(),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			frame := newFrame(t, 2, 2, pf)
			if test.name == "hextile subrect outside tile" {
				frame = newFrame(t, 4, 4, pf)
			}
			if _, err := frame.ReadRectangles(bytes.NewReader(test.data), 1); err == nil {
				t.Fatal("ReadRectangles accepted malformed input")
			}
		})
	}
}

func TestNon32BitPixelFormatRendersIdenticalPixels(t *testing.T) {
	formats := []struct {
		name string
		pf   PixelFormat
	}{
		{
			name: "8-bit 3-3-2",
			pf:   PixelFormat{BitsPerPixel: 8, Depth: 8, TrueColour: true, RedMax: 7, GreenMax: 7, BlueMax: 3, RedShift: 0, GreenShift: 3, BlueShift: 6},
		},
		{
			name: "16-bit 5-6-5",
			pf:   PixelFormat{BitsPerPixel: 16, Depth: 16, TrueColour: true, RedMax: 31, GreenMax: 63, BlueMax: 31, RedShift: 11, GreenShift: 5, BlueShift: 0},
		},
		{
			name: "16-bit big endian",
			pf:   PixelFormat{BitsPerPixel: 16, Depth: 16, BigEndian: true, TrueColour: true, RedMax: 31, GreenMax: 63, BlueMax: 31, RedShift: 11, GreenShift: 5, BlueShift: 0},
		},
		{
			name: "24-bit",
			pf:   PixelFormat{BitsPerPixel: 24, Depth: 24, TrueColour: true, RedMax: 255, GreenMax: 255, BlueMax: 255, RedShift: 16, GreenShift: 8, BlueShift: 0},
		},
	}
	for _, test := range formats {
		t.Run(test.name, func(t *testing.T) {
			reference := newFrame(t, 2, 2, DefaultPixelFormat())
			pixels := []color.RGBA{black, red, green, white}
			for i, pixel := range pixels {
				reference.Image().SetRGBA(i%2, i/2, pixel)
			}
			frame := newFrame(t, 2, 2, test.pf)
			readOne(t, frame, rawRectangle(test.pf, 0, 0, 2, 2, pixels))
			if !bytes.Equal(frame.Image().Pix, reference.Image().Pix) {
				t.Fatalf("%s rendered pixels differ from the 32-bit reference", test.name)
			}
		})
	}
}

func TestFramebufferBounds(t *testing.T) {
	if _, err := NewFramebuffer(0, 1, DefaultPixelFormat()); !errors.Is(err, ErrInvalidRectangle) {
		t.Fatalf("zero width = %v", err)
	}
	if _, err := NewFramebuffer(maxFramebufferPixels, 2, DefaultPixelFormat()); !errors.Is(err, ErrFramebufferTooLarge) {
		t.Fatalf("huge frame = %v", err)
	}
}

func TestReadZeroRectangles(t *testing.T) {
	frame := newFrame(t, 2, 2, DefaultPixelFormat())
	rectangles, err := frame.ReadRectangles(bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatalf("ReadRectangles(0): %v", err)
	}
	if len(rectangles) != 0 {
		t.Fatalf("rectangles = %v", rectangles)
	}
}
