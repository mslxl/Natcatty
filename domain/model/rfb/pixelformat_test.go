package rfb

import (
	"bytes"
	"errors"
	"image/color"
	"testing"
)

func TestPixelFormatWireRoundTrip(t *testing.T) {
	formats := []PixelFormat{
		DefaultPixelFormat(),
		{BitsPerPixel: 8, Depth: 8, TrueColour: true, RedMax: 7, GreenMax: 7, BlueMax: 3, RedShift: 0, GreenShift: 3, BlueShift: 6},
		{BitsPerPixel: 16, Depth: 16, TrueColour: true, RedMax: 31, GreenMax: 63, BlueMax: 31, RedShift: 11, GreenShift: 5, BlueShift: 0},
		{BitsPerPixel: 32, Depth: 24, BigEndian: true, TrueColour: true, RedMax: 255, GreenMax: 255, BlueMax: 255, RedShift: 16, GreenShift: 8, BlueShift: 0},
	}
	for _, format := range formats {
		var wire bytes.Buffer
		if _, err := format.WriteTo(&wire); err != nil {
			t.Fatalf("WriteTo(%+v): %v", format, err)
		}
		if wire.Len() != 16 {
			t.Fatalf("WriteTo(%+v) wrote %d bytes", format, wire.Len())
		}
		got, err := ReadPixelFormat(&wire)
		if err != nil {
			t.Fatalf("ReadPixelFormat(%+v): %v", format, err)
		}
		if got != format {
			t.Fatalf("round trip = %+v, want %+v", got, format)
		}
	}
}

func TestPixelFormatDecodeEncodeRoundTrip(t *testing.T) {
	formats := []PixelFormat{
		DefaultPixelFormat(),
		{BitsPerPixel: 8, Depth: 8, TrueColour: true, RedMax: 7, GreenMax: 7, BlueMax: 3, RedShift: 0, GreenShift: 3, BlueShift: 6},
		{BitsPerPixel: 16, Depth: 16, TrueColour: true, RedMax: 31, GreenMax: 63, BlueMax: 31, RedShift: 11, GreenShift: 5, BlueShift: 0},
		{BitsPerPixel: 24, Depth: 24, TrueColour: true, RedMax: 255, GreenMax: 255, BlueMax: 255, RedShift: 16, GreenShift: 8, BlueShift: 0},
		{BitsPerPixel: 32, Depth: 24, BigEndian: true, TrueColour: true, RedMax: 255, GreenMax: 255, BlueMax: 255, RedShift: 16, GreenShift: 8, BlueShift: 0},
	}
	for _, format := range formats {
		for _, c := range []color.RGBA{
			{R: 0, G: 0, B: 0, A: 255},
			{R: 255, G: 255, B: 255, A: 255},
			{R: 255, G: 0, B: 0, A: 255},
			{R: 0, G: 255, B: 0, A: 255},
			{R: 0, G: 0, B: 255, A: 255},
		} {
			wire, err := format.Encode(c)
			if err != nil {
				t.Fatalf("Encode(%+v, %+v): %v", format, c, err)
			}
			got, err := format.Decode(wire)
			if err != nil {
				t.Fatalf("Decode(%+v, %v): %v", format, wire, err)
			}
			want := color.RGBA{
				R: scaleChannel(channelToWire(c.R, format.RedMax), format.RedMax),
				G: scaleChannel(channelToWire(c.G, format.GreenMax), format.GreenMax),
				B: scaleChannel(channelToWire(c.B, format.BlueMax), format.BlueMax),
				A: 255,
			}
			if got != want {
				t.Fatalf("%+v: Decode(Encode(%+v)) = %+v, want %+v", format, c, got, want)
			}
		}
	}
}

func TestPixelFormatDecodeBigEndian(t *testing.T) {
	little := PixelFormat{BitsPerPixel: 32, Depth: 24, TrueColour: true, RedMax: 255, GreenMax: 255, BlueMax: 255, RedShift: 16, GreenShift: 8, BlueShift: 0}
	big := little
	big.BigEndian = true

	littleWire, err := little.Encode(color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 255})
	if err != nil {
		t.Fatal(err)
	}
	bigWire, err := big.Encode(color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 255})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(littleWire, bigWire) {
		t.Fatalf("little and big endian wire bytes are equal: %v", littleWire)
	}
	for _, format := range []PixelFormat{little, big} {
		wire, err := format.Encode(color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 255})
		if err != nil {
			t.Fatal(err)
		}
		got, err := format.Decode(wire)
		if err != nil {
			t.Fatal(err)
		}
		if got != (color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 255}) {
			t.Fatalf("%+v decoded %+v", format, got)
		}
	}
}

func TestPixelFormatValidateRejects(t *testing.T) {
	cases := []PixelFormat{
		{},
		{BitsPerPixel: 12, Depth: 12, TrueColour: true, RedMax: 15, GreenMax: 15, BlueMax: 15},
		{BitsPerPixel: 16, Depth: 0, TrueColour: true, RedMax: 31, GreenMax: 63, BlueMax: 31},
		{BitsPerPixel: 16, Depth: 16, TrueColour: false, RedMax: 31, GreenMax: 63, BlueMax: 31},
		{BitsPerPixel: 16, Depth: 16, TrueColour: true, RedMax: 0, GreenMax: 63, BlueMax: 31},
		{BitsPerPixel: 8, Depth: 8, TrueColour: true, RedMax: 7, GreenMax: 7, BlueMax: 3, RedShift: 6},
	}
	for _, format := range cases {
		if err := format.Validate(); !errors.Is(err, ErrInvalidPixelFormat) {
			t.Fatalf("Validate(%+v) = %v, want ErrInvalidPixelFormat", format, err)
		}
	}
}

func TestPixelFormatDecodeShortInput(t *testing.T) {
	if _, err := DefaultPixelFormat().Decode([]byte{1, 2}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("short decode = %v, want ErrMalformed", err)
	}
}
