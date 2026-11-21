package rfb

import (
	"fmt"
	"image/color"
	"io"
	"math/bits"
)

// PixelFormat is the RFB PIXEL_FORMAT structure. The wire form is 16 bytes:
// bits-per-pixel, depth, big-endian flag, true-colour flag, the three channel
// maxima, the three shifts, and three padding bytes.
type PixelFormat struct {
	BitsPerPixel uint8
	Depth        uint8
	BigEndian    bool
	TrueColour   bool
	RedMax       uint16
	GreenMax     uint16
	BlueMax      uint16
	RedShift     uint8
	GreenShift   uint8
	BlueShift    uint8
}

// DefaultPixelFormat is the 32-bit true-colour format the client requests:
// depth 24, little-endian, red in the second byte, green in the third and blue
// in the fourth.
func DefaultPixelFormat() PixelFormat {
	return PixelFormat{
		BitsPerPixel: 32,
		Depth:        24,
		BigEndian:    false,
		TrueColour:   true,
		RedMax:       255,
		GreenMax:     255,
		BlueMax:      255,
		RedShift:     16,
		GreenShift:   8,
		BlueShift:    0,
	}
}

// BytesPerPixel returns the encoded pixel width. It is only meaningful for a
// format that passed Validate.
func (p PixelFormat) BytesPerPixel() int { return int(p.BitsPerPixel) / 8 }

// Validate reports whether the format can be decoded into and encoded from
// 8-bit RGBA. Only true-colour formats are supported.
func (p PixelFormat) Validate() error {
	switch p.BitsPerPixel {
	case 8, 16, 24, 32:
	default:
		return fmt.Errorf("%w: bits-per-pixel %d", ErrInvalidPixelFormat, p.BitsPerPixel)
	}
	if p.Depth == 0 || p.Depth > p.BitsPerPixel {
		return fmt.Errorf("%w: depth %d with %d bits-per-pixel", ErrInvalidPixelFormat, p.Depth, p.BitsPerPixel)
	}
	if !p.TrueColour {
		return fmt.Errorf("%w: colour-map formats are not supported", ErrInvalidPixelFormat)
	}
	if p.RedMax == 0 || p.GreenMax == 0 || p.BlueMax == 0 {
		return fmt.Errorf("%w: a channel maximum is zero", ErrInvalidPixelFormat)
	}
	for _, channel := range []struct {
		name  string
		max   uint16
		shift uint8
	}{
		{"red", p.RedMax, p.RedShift},
		{"green", p.GreenMax, p.GreenShift},
		{"blue", p.BlueMax, p.BlueShift},
	} {
		need := bits.Len32(uint32(channel.max))
		if int(channel.shift)+need > int(p.BitsPerPixel) {
			return fmt.Errorf("%w: %s channel shift %d and %d bits exceed %d bits-per-pixel",
				ErrInvalidPixelFormat, channel.name, channel.shift, need, p.BitsPerPixel)
		}
	}
	return nil
}

// Decode turns one wire pixel into 32-bit RGBA, scaling each channel to
// 0..255. data must hold at least BytesPerPixel bytes.
func (p PixelFormat) Decode(data []byte) (color.RGBA, error) {
	if err := p.Validate(); err != nil {
		return color.RGBA{}, err
	}
	bpp := p.BytesPerPixel()
	if len(data) < bpp {
		return color.RGBA{}, fmt.Errorf("%w: pixel is %d bytes, want %d", ErrMalformed, len(data), bpp)
	}
	return p.decodePixel(data), nil
}

// decodePixel is the hot path for rectangle decoding. The caller must have
// validated the format and passed at least BytesPerPixel bytes.
func (p PixelFormat) decodePixel(data []byte) color.RGBA {
	return p.decodeValue(readPixelValue(data, p.BytesPerPixel(), p.BigEndian))
}

// decodeValue turns one already-assembled wire pixel value into 32-bit RGBA.
// The ZRLE decoder uses it after reconstructing a CPIXEL.
func (p PixelFormat) decodeValue(value uint32) color.RGBA {
	return color.RGBA{
		R: scaleChannel(channelValue(value, p.RedShift, p.RedMax), p.RedMax),
		G: scaleChannel(channelValue(value, p.GreenShift, p.GreenMax), p.GreenMax),
		B: scaleChannel(channelValue(value, p.BlueShift, p.BlueMax), p.BlueMax),
		A: 255,
	}
}

// Encode turns 32-bit RGBA into one wire pixel in this format.
func (p PixelFormat) Encode(c color.RGBA) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	value := channelToWire(c.R, p.RedMax)<<p.RedShift |
		channelToWire(c.G, p.GreenMax)<<p.GreenShift |
		channelToWire(c.B, p.BlueMax)<<p.BlueShift
	out := make([]byte, p.BytesPerPixel())
	writePixelValue(out, value, p.BigEndian)
	return out, nil
}

// usedBitsMask returns the bits the red, green and blue channels occupy.
func (p PixelFormat) usedBitsMask() uint32 {
	mask := func(max uint16, shift uint8) uint32 {
		return ((uint32(1) << bits.Len32(uint32(max))) - 1) << shift
	}
	return mask(p.RedMax, p.RedShift) | mask(p.GreenMax, p.GreenShift) | mask(p.BlueMax, p.BlueShift)
}

// CPixelBytes is the width of one ZRLE CPIXEL in this format. Encoders use it
// to write the same compact form the decoder expects.
func (p PixelFormat) CPixelBytes() int { return p.cpixelBytes() }

// EncodeCPixel encodes one colour as a ZRLE CPIXEL.
func (p PixelFormat) EncodeCPixel(c color.RGBA) ([]byte, error) {
	wire, err := p.Encode(c)
	if err != nil {
		return nil, err
	}
	if p.cpixelBytes() != 3 {
		return wire, nil
	}
	if p.usedBitsMask()&0xFF000000 == 0 {
		// Least significant three bytes.
		if p.BigEndian {
			return wire[1:4], nil
		}
		return wire[0:3], nil
	}
	// Most significant three bytes.
	if p.BigEndian {
		return wire[0:3], nil
	}
	return wire[1:4], nil
}

// cpixelBytes is the CPIXEL width the ZRLE decoder must read. For 32-bit true
// colour with depth at most 24 and every used bit in the least or most
// significant three bytes, a CPIXEL is three bytes; otherwise it is a PIXEL.
func (p PixelFormat) cpixelBytes() int {
	if p.BitsPerPixel != 32 || !p.TrueColour || p.Depth > 24 {
		return p.BytesPerPixel()
	}
	used := p.usedBitsMask()
	if used&0xFF000000 == 0 || used&0x000000FF == 0 {
		return 3
	}
	return 4
}

// cpixelValue reconstructs a wire pixel value from a CPIXEL. The caller must
// pass cpixelBytes bytes.
func (p PixelFormat) cpixelValue(data []byte) uint32 {
	if p.cpixelBytes() != 3 {
		return readPixelValue(data, len(data), p.BigEndian)
	}
	// Choose the three bytes the used channels live in and rebuild the full
	// 32-bit value in the wire byte order.
	if p.usedBitsMask()&0xFF000000 == 0 {
		// Least significant three bytes.
		if p.BigEndian {
			return uint32(data[0])<<16 | uint32(data[1])<<8 | uint32(data[2])
		}
		return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
	}
	// Most significant three bytes.
	if p.BigEndian {
		return uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8
	}
	return uint32(data[0])<<8 | uint32(data[1])<<16 | uint32(data[2])<<24
}

func readPixelValue(data []byte, bpp int, bigEndian bool) uint32 {
	switch bpp {
	case 1:
		return uint32(data[0])
	case 2:
		if bigEndian {
			return uint32(data[0])<<8 | uint32(data[1])
		}
		return uint32(data[1])<<8 | uint32(data[0])
	case 3:
		if bigEndian {
			return uint32(data[0])<<16 | uint32(data[1])<<8 | uint32(data[2])
		}
		return uint32(data[2])<<16 | uint32(data[1])<<8 | uint32(data[0])
	case 4:
		if bigEndian {
			return uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
		}
		return uint32(data[3])<<24 | uint32(data[2])<<16 | uint32(data[1])<<8 | uint32(data[0])
	default:
		return 0
	}
}

func writePixelValue(dst []byte, value uint32, bigEndian bool) {
	switch len(dst) {
	case 1:
		dst[0] = byte(value)
	case 2:
		if bigEndian {
			dst[0], dst[1] = byte(value>>8), byte(value)
		} else {
			dst[1], dst[0] = byte(value>>8), byte(value)
		}
	case 3:
		if bigEndian {
			dst[0], dst[1], dst[2] = byte(value>>16), byte(value>>8), byte(value)
		} else {
			dst[2], dst[1], dst[0] = byte(value>>16), byte(value>>8), byte(value)
		}
	case 4:
		if bigEndian {
			dst[0], dst[1], dst[2], dst[3] = byte(value>>24), byte(value>>16), byte(value>>8), byte(value)
		} else {
			dst[3], dst[2], dst[1], dst[0] = byte(value>>24), byte(value>>16), byte(value>>8), byte(value)
		}
	}
}

func channelValue(value uint32, shift uint8, max uint16) uint32 {
	mask := (uint32(1) << bits.Len32(uint32(max))) - 1
	v := (value >> shift) & mask
	if v > uint32(max) {
		v = uint32(max)
	}
	return v
}

func channelToWire(v uint8, max uint16) uint32 {
	return uint32(v) * uint32(max) / 255
}

func scaleChannel(v uint32, max uint16) uint8 {
	if v > uint32(max) {
		v = uint32(max)
	}
	return uint8(v * 255 / uint32(max))
}

// ReadPixelFormat reads the 16-byte wire form. It does not validate the
// format: a server may advertise a colour-map format and switch to the
// client's requested true-colour format after SetPixelFormat.
func ReadPixelFormat(r io.Reader) (PixelFormat, error) {
	var wire [16]byte
	if _, err := io.ReadFull(r, wire[:]); err != nil {
		return PixelFormat{}, err
	}
	return PixelFormat{
		BitsPerPixel: wire[0],
		Depth:        wire[1],
		BigEndian:    wire[2] != 0,
		TrueColour:   wire[3] != 0,
		RedMax:       uint16(wire[4])<<8 | uint16(wire[5]),
		GreenMax:     uint16(wire[6])<<8 | uint16(wire[7]),
		BlueMax:      uint16(wire[8])<<8 | uint16(wire[9]),
		RedShift:     wire[10],
		GreenShift:   wire[11],
		BlueShift:    wire[12],
	}, nil
}

// WriteTo writes the 16-byte wire form.
func (p PixelFormat) WriteTo(w io.Writer) (int64, error) {
	wire := p.appendWire(make([]byte, 0, 16))
	return int64(len(wire)), writeAll(w, wire)
}

func (p PixelFormat) appendWire(dst []byte) []byte {
	bigEndian := byte(0)
	if p.BigEndian {
		bigEndian = 1
	}
	trueColour := byte(0)
	if p.TrueColour {
		trueColour = 1
	}
	return append(dst,
		p.BitsPerPixel,
		p.Depth,
		bigEndian,
		trueColour,
		byte(p.RedMax>>8), byte(p.RedMax),
		byte(p.GreenMax>>8), byte(p.GreenMax),
		byte(p.BlueMax>>8), byte(p.BlueMax),
		p.RedShift, p.GreenShift, p.BlueShift,
		0, 0, 0,
	)
}
