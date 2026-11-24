package rfb

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

// zrlePayload zlib-compresses raw tile bytes and wraps them in the 4-byte
// length prefix that begins a ZRLE rectangle.
func zrlePayload(t *testing.T, tiles []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(tiles); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(compressed.Len()))
	return append(payload, compressed.Bytes()...)
}

// zrleCPixel hand-encodes a CPIXEL for the default 32-bit little-endian
// format: the CPIXEL is the least-significant three bytes of the wire pixel,
// in blue, green, red order. Decoder tests deliberately do not call
// EncodeCPixel, so a symmetric encoder/decoder bug cannot cancel itself out.
func zrleCPixel(t *testing.T, pf PixelFormat, c color.RGBA) []byte {
	t.Helper()
	if pf != DefaultPixelFormat() {
		t.Fatalf("hand CPIXEL encoder only supports the default format")
	}
	return []byte{c.B, c.G, c.R}
}

func zrleRect(rect image.Rectangle, payload []byte) []byte {
	data := rectHeader(
		uint16(rect.Min.X), uint16(rect.Min.Y),
		uint16(rect.Dx()), uint16(rect.Dy()), EncodingZRLE,
	)
	return append(data, payload...)
}

func decodeZRLE(t *testing.T, frame *Framebuffer, rect image.Rectangle, tiles []byte) {
	t.Helper()
	readOne(t, frame, zrleRect(rect, zrlePayload(t, tiles)))
}

func packedRow(t *testing.T, indices []int, bitsPerPixel int) []byte {
	t.Helper()
	rowBytes := (len(indices)*bitsPerPixel + 7) / 8
	row := make([]byte, rowBytes)
	for x, index := range indices {
		bit := x * bitsPerPixel
		shift := 8 - bitsPerPixel - bit%8
		row[bit/8] |= byte(index&((1<<bitsPerPixel)-1)) << shift
	}
	return row
}

func runLength(length int) []byte {
	// The wire encodes runLength-1 as repeated 255 bytes and a final byte.
	remaining := length - 1
	var out []byte
	for remaining >= 255 {
		out = append(out, 255)
		remaining -= 255
	}
	return append(out, byte(remaining))
}

func TestDecodeZRLERawTile(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 2, 2, pf)
	var tiles []byte
	tiles = append(tiles, zrleRaw)
	for _, pixel := range []color.RGBA{red, green, blue, white} {
		tiles = append(tiles, zrleCPixel(t, pf, pixel)...)
	}
	decodeZRLE(t, frame, image.Rect(0, 0, 2, 2), tiles)
	checkPixels(t, frame, map[image.Point]color.RGBA{
		{X: 0, Y: 0}: red,
		{X: 1, Y: 0}: green,
		{X: 0, Y: 1}: blue,
		{X: 1, Y: 1}: white,
	})
}

func TestDecodeZRLESolidTile(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 3, 2, pf)
	tiles := []byte{zrleSolid}
	tiles = append(tiles, zrleCPixel(t, pf, green)...)
	decodeZRLE(t, frame, image.Rect(0, 0, 3, 2), tiles)
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			if got := frame.Image().RGBAAt(x, y); got != green {
				t.Fatalf("pixel %d,%d = %+v, want green", x, y, got)
			}
		}
	}
}

func TestDecodeZRLEPackedPalette(t *testing.T) {
	pf := DefaultPixelFormat()
	for _, test := range []struct {
		name         string
		paletteSize  int
		bitsPerPixel int
	}{
		{"two colours", 2, 1},
		{"four colours", 4, 2},
		{"eight colours", 8, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			palette := make([]color.RGBA, test.paletteSize)
			for i := range palette {
				palette[i] = color.RGBA{
					R: uint8(i * 20), G: uint8(255 - i*20), B: uint8(i * 7), A: 255,
				}
			}
			frame := newFrame(t, 4, 2, pf)
			var tiles []byte
			tiles = append(tiles, byte(test.paletteSize))
			for _, pixel := range palette {
				tiles = append(tiles, zrleCPixel(t, pf, pixel)...)
			}
			for y := 0; y < 2; y++ {
				indices := []int{y % test.paletteSize, (y + 1) % test.paletteSize, (y + 2) % test.paletteSize, (y + 3) % test.paletteSize}
				tiles = append(tiles, packedRow(t, indices, test.bitsPerPixel)...)
			}
			decodeZRLE(t, frame, image.Rect(0, 0, 4, 2), tiles)
			for y := 0; y < 2; y++ {
				for x := 0; x < 4; x++ {
					want := palette[(x+y)%test.paletteSize]
					if got := frame.Image().RGBAAt(x, y); got != want {
						t.Fatalf("pixel %d,%d = %+v, want %+v", x, y, got, want)
					}
				}
			}
		})
	}
}

func TestDecodeZRLEPlainRLE(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 4, 4, pf)
	var tiles []byte
	tiles = append(tiles, zrlePlainRLE)
	for _, run := range []struct {
		pixel  color.RGBA
		length int
	}{
		{red, 3},
		{green, 10},
		{blue, 3},
	} {
		tiles = append(tiles, zrleCPixel(t, pf, run.pixel)...)
		tiles = append(tiles, runLength(run.length)...)
	}
	decodeZRLE(t, frame, image.Rect(0, 0, 4, 4), tiles)
	want := make([]color.RGBA, 16)
	for i := range want {
		switch {
		case i < 3:
			want[i] = red
		case i < 13:
			want[i] = green
		default:
			want[i] = blue
		}
	}
	for i, pixel := range want {
		if got := frame.Image().RGBAAt(i%4, i/4); got != pixel {
			t.Fatalf("pixel %d = %+v, want %+v", i, got, pixel)
		}
	}
}

func TestDecodeZRLEPlainRLELongRun(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 20, 16, pf)
	var tiles []byte
	tiles = append(tiles, zrlePlainRLE)
	tiles = append(tiles, zrleCPixel(t, pf, red)...)
	tiles = append(tiles, runLength(20*16)...)
	decodeZRLE(t, frame, image.Rect(0, 0, 20, 16), tiles)
	for i := 0; i < 20*16; i++ {
		if got := frame.Image().RGBAAt(i%20, i/20); got != red {
			t.Fatalf("pixel %d = %+v, want red", i, got)
		}
	}
}

func TestDecodeZRLEPaletteRLE(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 2, 2, pf)
	var tiles []byte
	tiles = append(tiles, byte(128+2)) // palette of two colours
	tiles = append(tiles, zrleCPixel(t, pf, red)...)
	tiles = append(tiles, zrleCPixel(t, pf, green)...)
	// One red pixel, then a run of three green pixels.
	tiles = append(tiles, 0)
	tiles = append(tiles, 0x80|1)
	tiles = append(tiles, runLength(3)...)
	decodeZRLE(t, frame, image.Rect(0, 0, 2, 2), tiles)
	checkPixels(t, frame, map[image.Point]color.RGBA{
		{X: 0, Y: 0}: red,
		{X: 1, Y: 0}: green,
		{X: 0, Y: 1}: green,
		{X: 1, Y: 1}: green,
	})
}

// TestDecodeZRLEGoldenImage decodes a multi-tile rectangle whose every tile
// uses a different subencoding and compares it with the reference image.
func TestDecodeZRLEGoldenImage(t *testing.T) {
	pf := DefaultPixelFormat()
	const width, height = 130, 70
	golden := image.NewRGBA(image.Rect(0, 0, width, height))
	// Tile (0,0): solid red.
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			golden.SetRGBA(x, y, red)
		}
	}
	// Tile (64,0): vertical green/blue stripes, a packed palette.
	for y := 0; y < 64; y++ {
		for x := 64; x < 128; x++ {
			if x%2 == 0 {
				golden.SetRGBA(x, y, green)
			} else {
				golden.SetRGBA(x, y, blue)
			}
		}
	}
	// Tile (128,0): two columns of white.
	for y := 0; y < 64; y++ {
		for x := 128; x < 130; x++ {
			golden.SetRGBA(x, y, white)
		}
	}
	// Tile (0,64): a 64x6 gradient, sent raw.
	for y := 64; y < 70; y++ {
		for x := 0; x < 64; x++ {
			golden.SetRGBA(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 10), B: uint8(x + y), A: 255})
		}
	}
	// Tile (64,64): red for 64 pixels then black for 320, plain RLE.
	for i := 0; i < 64*6; i++ {
		pixel := black
		if i < 64 {
			pixel = green
		}
		golden.SetRGBA(64+i%64, 64+i/64, pixel)
	}
	// Tile (128,64): two white pixels then a ten-pixel red run, palette RLE.
	for i := 0; i < 2*6; i++ {
		pixel := white
		if i >= 2 {
			pixel = red
		}
		golden.SetRGBA(128+i%2, 64+i/2, pixel)
	}

	var tiles []byte
	// Solid red.
	tiles = append(tiles, zrleSolid)
	tiles = append(tiles, zrleCPixel(t, pf, red)...)
	// Packed palette of green and blue.
	tiles = append(tiles, 2)
	tiles = append(tiles, zrleCPixel(t, pf, green)...)
	tiles = append(tiles, zrleCPixel(t, pf, blue)...)
	for y := 0; y < 64; y++ {
		indices := make([]int, 64)
		for x := range indices {
			indices[x] = x % 2
		}
		tiles = append(tiles, packedRow(t, indices, 1)...)
	}
	// White raw tile.
	tiles = append(tiles, zrleRaw)
	for i := 0; i < 2*64; i++ {
		tiles = append(tiles, zrleCPixel(t, pf, white)...)
	}
	// Raw gradient tile.
	tiles = append(tiles, zrleRaw)
	for y := 64; y < 70; y++ {
		for x := 0; x < 64; x++ {
			tiles = append(tiles, zrleCPixel(t, pf, golden.RGBAAt(x, y))...)
		}
	}
	// Plain RLE: one green run and one black run.
	tiles = append(tiles, zrlePlainRLE)
	tiles = append(tiles, zrleCPixel(t, pf, green)...)
	tiles = append(tiles, runLength(64)...)
	tiles = append(tiles, zrleCPixel(t, pf, black)...)
	tiles = append(tiles, runLength(320)...)
	// Palette RLE: two white singles and one red run.
	tiles = append(tiles, byte(128+2))
	tiles = append(tiles, zrleCPixel(t, pf, white)...)
	tiles = append(tiles, zrleCPixel(t, pf, red)...)
	tiles = append(tiles, 0, 0)
	tiles = append(tiles, 0x80|1)
	tiles = append(tiles, runLength(10)...)

	frame := newFrame(t, width, height, pf)
	decodeZRLE(t, frame, image.Rect(0, 0, width, height), tiles)
	if !bytes.Equal(frame.Image().Pix, golden.Pix) {
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				if got, want := frame.Image().RGBAAt(x, y), golden.RGBAAt(x, y); got != want {
					t.Fatalf("golden pixel %d,%d = %+v, want %+v", x, y, got, want)
				}
			}
		}
	}
}

func TestDecodeZRLEThreeByteCPixel(t *testing.T) {
	pf := DefaultPixelFormat()
	if got := pf.CPixelBytes(); got != 3 {
		t.Fatalf("default CPixelBytes = %d, want 3", got)
	}
	frame := newFrame(t, 2, 1, pf)
	tiles := []byte{zrleRaw}
	tiles = append(tiles, zrleCPixel(t, pf, red)...)
	tiles = append(tiles, zrleCPixel(t, pf, blue)...)
	decodeZRLE(t, frame, image.Rect(0, 0, 2, 1), tiles)
	checkPixels(t, frame, map[image.Point]color.RGBA{
		{X: 0, Y: 0}: red,
		{X: 1, Y: 0}: blue,
	})
}

func TestDecodeZRLEMalformed(t *testing.T) {
	pf := DefaultPixelFormat()
	cases := []struct {
		name  string
		tiles []byte
	}{
		{"empty", nil},
		{"unknown subencoding", []byte{17}},
		{"palette index outside", func() []byte {
			// A three-colour palette uses two bits, so index 3 is out of range.
			data := []byte{3}
			for _, pixel := range []color.RGBA{red, green, blue} {
				data = append(data, zrleCPixel(t, pf, pixel)...)
			}
			return append(data, packedRow(t, []int{3, 0}, 2)[:1]...)
		}()},
		{"plain RLE overrun", append(append([]byte{zrlePlainRLE}, zrleCPixel(t, pf, red)...), runLength(9)...)},
		{"truncated raw", append([]byte{zrleRaw}, zrleCPixel(t, pf, red)...)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			frame := newFrame(t, 2, 2, pf)
			data := zrleRect(image.Rect(0, 0, 2, 2), zrlePayload(t, test.tiles))
			if _, err := frame.ReadRectangles(bytes.NewReader(data), 1); err == nil {
				t.Fatal("ReadRectangles accepted malformed ZRLE input")
			}
		})
	}
}

// TestDecodeZRLEIndependentCPixelVectors decodes hand-written CPIXEL bytes for
// each wire width. The bytes are not produced by EncodeCPixel, so a symmetric
// encoder/decoder mistake cannot hide here.
func TestDecodeZRLEIndependentCPixelVectors(t *testing.T) {
	tests := []struct {
		name string
		pf   PixelFormat
		wire []byte
		want color.RGBA
	}{
		{
			name: "one byte",
			pf: PixelFormat{
				BitsPerPixel: 8, Depth: 8, TrueColour: true,
				RedMax: 7, GreenMax: 7, BlueMax: 3,
				RedShift: 5, GreenShift: 2, BlueShift: 0,
			},
			// 111 001 00: red 7 -> 255, green 1 -> 36, blue 0.
			wire: []byte{0xE4},
			want: color.RGBA{R: 255, G: 36, A: 255},
		},
		{
			name: "two bytes",
			pf: PixelFormat{
				BitsPerPixel: 16, Depth: 16, TrueColour: true,
				RedMax: 31, GreenMax: 63, BlueMax: 31,
				RedShift: 11, GreenShift: 5, BlueShift: 0,
			},
			// Little-endian 0xF800: red 31 -> 255, green and blue 0.
			wire: []byte{0x00, 0xF8},
			want: color.RGBA{R: 255, A: 255},
		},
		{
			name: "three bytes",
			pf:   DefaultPixelFormat(),
			// Little-endian least-significant three bytes of 0x00FF0000.
			wire: []byte{0x00, 0x00, 0xFF},
			want: color.RGBA{R: 255, A: 255},
		},
		{
			name: "four bytes",
			pf: PixelFormat{
				BitsPerPixel: 32, Depth: 30, TrueColour: true,
				RedMax: 1023, GreenMax: 1023, BlueMax: 1023,
				RedShift: 20, GreenShift: 10, BlueShift: 0,
			},
			// Little-endian 0x0FF00000: red 255 of 1023 -> 63.
			wire: []byte{0x00, 0x00, 0xF0, 0x0F},
			want: color.RGBA{R: 63, A: 255},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.pf.CPixelBytes(); got != len(test.wire) {
				t.Fatalf("CPixelBytes = %d, want %d", got, len(test.wire))
			}
			frame := newFrame(t, 1, 1, test.pf)
			tiles := append([]byte{zrleSolid}, test.wire...)
			decodeZRLE(t, frame, image.Rect(0, 0, 1, 1), tiles)
			if got := frame.Image().RGBAAt(0, 0); got != test.want {
				t.Fatalf("pixel = %+v, want %+v", got, test.want)
			}
		})
	}
}

// TestDecodeZRLEPersistentStreamAcrossRectangles proves two consecutive ZRLE
// rectangles on one connection share the persistent zlib stream: the second
// rectangle's tile bytes are only decodable by continuing the first stream.
func TestDecodeZRLEPersistentStreamAcrossRectangles(t *testing.T) {
	pf := DefaultPixelFormat()
	frame := newFrame(t, 2, 1, pf)
	var stream bytes.Buffer
	writer := zlib.NewWriter(&stream)
	chunk := func() []byte {
		if err := writer.Flush(); err != nil {
			t.Fatalf("zlib flush: %v", err)
		}
		flushed := append([]byte(nil), stream.Bytes()...)
		stream.Reset()
		length := make([]byte, 4)
		binary.BigEndian.PutUint32(length, uint32(len(flushed)))
		return append(length, flushed...)
	}

	// First rectangle: a solid red 1x1 tile.
	if _, err := writer.Write([]byte{zrleSolid, 0x00, 0x00, 0xFF}); err != nil {
		t.Fatalf("write first tile: %v", err)
	}
	first := chunk()
	// Second rectangle continues the same zlib stream with a solid blue tile.
	if _, err := writer.Write([]byte{zrleSolid, 0xFF, 0x00, 0x00}); err != nil {
		t.Fatalf("write second tile: %v", err)
	}
	second := chunk()

	data := append(rectHeader(0, 0, 1, 1, EncodingZRLE), first...)
	data = append(data, rectHeader(1, 0, 1, 1, EncodingZRLE)...)
	data = append(data, second...)

	rectangles, err := frame.ReadRectangles(bytes.NewReader(data), 2)
	if err != nil {
		t.Fatalf("ReadRectangles: %v", err)
	}
	if len(rectangles) != 2 {
		t.Fatalf("rectangles = %+v, want two", rectangles)
	}
	checkPixels(t, frame, map[image.Point]color.RGBA{
		{X: 0, Y: 0}: red,
		{X: 1, Y: 0}: blue,
	})
}

func FuzzDecodeZRLE(f *testing.F) {
	pf := DefaultPixelFormat()
	var seed []byte
	seed = append(seed, zrleRaw)
	for i := 0; i < 64; i++ {
		seed = append(seed, 0, 0, 0)
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write(seed)
	_ = writer.Close()
	f.Add(compressed.Bytes())
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 32))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzInputLimit {
			t.Skip()
		}
		payload := make([]byte, 4+len(data))
		binary.BigEndian.PutUint32(payload, uint32(len(data)))
		copy(payload[4:], data)
		frame, err := NewFramebuffer(8, 8, pf)
		if err != nil {
			t.Fatalf("NewFramebuffer: %v", err)
		}
		_ = frame.decodeZRLE(bytes.NewReader(payload), image.Rect(0, 0, 8, 8))
	})
}

func FuzzDecodeZRLETile(f *testing.F) {
	pf := DefaultPixelFormat()
	seed := []byte{zrleSolid, 0, 0, 0}
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
		_ = frame.decodeZRLETile(bytes.NewReader(data), image.Rect(0, 0, 8, 8), pf.CPixelBytes())
	})
}
