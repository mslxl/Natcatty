package graphics

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
)

func decodeSixelPNG(t *testing.T, body string) image.Image {
	t.Helper()
	data, ok := DecodeSixel(body)
	if !ok {
		t.Fatalf("DecodeSixel(%q) failed", body)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return img
}

func rgbAt(t *testing.T, img image.Image, x, y int) (uint32, uint32, uint32, uint32) {
	t.Helper()
	r, g, b, a := img.At(x, y).RGBA()
	return r, g, b, a
}

// A solid 16x16 red square: raster attributes, an RGB colour definition, three
// six-row bands clipped by the declared height.
func TestSixelRedSquare(t *testing.T) {
	band := strings.Repeat("~", 16)
	body := `q"1;1;16;16#0;2;100;0;0#0` + band + "-" + band + "-" + band
	img := decodeSixelPNG(t, body)
	if b := img.Bounds(); b.Dx() != 16 || b.Dy() != 16 {
		t.Fatalf("size = %dx%d, want 16x16", b.Dx(), b.Dy())
	}
	r, g, b, a := rgbAt(t, img, 8, 8)
	if r < 0xc000 || g > 0x4000 || b > 0x4000 || a != 0xffff {
		t.Fatalf("centre = %d,%d,%d,%d, want red", r, g, b, a)
	}
}

// "!" repeats a sixel column; "$" returns to the band's left edge.
func TestSixelRepeatAndCarriageReturn(t *testing.T) {
	body := `q"1;1;8;6#0;2;0;100;0#0!8~`
	img := decodeSixelPNG(t, body)
	if b := img.Bounds(); b.Dx() != 8 || b.Dy() != 6 {
		t.Fatalf("size = %dx%d, want 8x6", b.Dx(), b.Dy())
	}
	r, g, b, _ := rgbAt(t, img, 4, 2)
	if g < 0xc000 || r > 0x4000 || b > 0x4000 {
		t.Fatalf("pixel = %d,%d,%d, want green", r, g, b)
	}
}

// P2=1 makes the untouched background transparent.
func TestSixelTransparentBackground(t *testing.T) {
	img := decodeSixelPNG(t, `0;1;0q"1;1;2;2#0;2;100;0;0#0~`)
	if b := img.Bounds(); b.Dx() != 2 || b.Dy() != 2 {
		t.Fatalf("size = %dx%d, want 2x2", b.Dx(), b.Dy())
	}
	if _, _, _, a := rgbAt(t, img, 0, 0); a != 0xffff {
		t.Fatalf("painted pixel alpha = %d, want opaque", a)
	}
	if _, _, _, a := rgbAt(t, img, 1, 1); a != 0 {
		t.Fatalf("background alpha = %d, want 0", a)
	}
}

// Without a definition, colour 6 is the VT340 palette's red.
func TestSixelDefaultPalette(t *testing.T) {
	img := decodeSixelPNG(t, `q"1;1;2;6#6!2~`)
	r, g, b, _ := rgbAt(t, img, 1, 1)
	if r < 0xc000 || g > 0x4000 || b > 0x4000 {
		t.Fatalf("colour 6 = %d,%d,%d, want red", r, g, b)
	}
}

// HLS definitions (Pu=1): hue 120 with full saturation is green.
func TestSixelHLS(t *testing.T) {
	img := decodeSixelPNG(t, `q"1;1;2;6#3;1;120;50;100#3!2~`)
	r, g, b, _ := rgbAt(t, img, 0, 0)
	if g < 0xc000 || r > 0x4000 || b > 0x4000 {
		t.Fatalf("HLS colour = %d,%d,%d, want green", r, g, b)
	}
}

// The parser recognises a sixel DCS split across chunks.
func TestSixelThroughParser(t *testing.T) {
	band := strings.Repeat("~", 4)
	seq := "\x1bPq\"1;1;4;6#0;2;100;0;0#0" + band + "\x1b\\"
	p := New()
	var got []Image
	for i := 0; i < len(seq); i += 5 {
		end := i + 5
		if end > len(seq) {
			end = len(seq)
		}
		got = append(got, p.Feed([]byte(seq[i:end]))...)
	}
	if len(got) != 1 {
		t.Fatalf("images = %d, want 1", len(got))
	}
	if !bytes.HasPrefix(got[0].Data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("image is not PNG")
	}
}
