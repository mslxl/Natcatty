package wire

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf8"
)

// This file implements the small slice of MessagePack the pinned Atuin client
// uses. It deliberately mirrors rust-rmp's marker choice: the client writes
// some integers with an explicit-width marker (write_u8, write_u16, write_u64)
// and others with the compact form (write_sint, write_uint8). Reproducing that
// choice is required for byte-level compatibility.

// MessagePack markers used by Atuin.
const (
	markerNil      = 0xc0
	markerUint8    = 0xcc
	markerUint16   = 0xcd
	markerUint32   = 0xce
	markerUint64   = 0xcf
	markerInt8     = 0xd0
	markerInt16    = 0xd1
	markerInt32    = 0xd2
	markerInt64    = 0xd3
	markerBin8     = 0xc4
	markerBin16    = 0xc5
	markerBin32    = 0xc6
	markerStr8     = 0xd9
	markerStr16    = 0xda
	markerStr32    = 0xdb
	markerArray16  = 0xdc
	markerArray32  = 0xdd
	fixStrMask     = 0xa0
	fixArrayMask   = 0x90
	positiveFixMax = 0x7f
	negativeFixMin = 0xe0
)

// encoder appends MessagePack values to a byte slice.
type encoder struct {
	buf []byte
}

func (e *encoder) writeNil() {
	e.buf = append(e.buf, markerNil)
}

// writeU8 writes the explicit uint8 marker. rust-rmp's write_u8 always uses
// it, even for values that fit a positive fixint.
func (e *encoder) writeU8(value uint8) {
	e.buf = append(e.buf, markerUint8, value)
}

// writeU16 writes the explicit uint16 marker.
func (e *encoder) writeU16(value uint16) {
	e.buf = append(e.buf, markerUint16, byte(value>>8), byte(value))
}

// writeU64 writes the explicit uint64 marker.
func (e *encoder) writeU64(value uint64) {
	var encoded [9]byte
	encoded[0] = markerUint64
	binary.BigEndian.PutUint64(encoded[1:], value)
	e.buf = append(e.buf, encoded[:]...)
}

// writeSInt writes the most compact signed representation, matching
// rust-rmp's write_sint.
func (e *encoder) writeSInt(value int64) {
	switch {
	case value < 0 && value >= -32:
		e.buf = append(e.buf, byte(value))
	case value < -32 && value >= math.MinInt8:
		e.buf = append(e.buf, markerInt8, byte(value))
	case value < math.MinInt8 && value >= math.MinInt16:
		e.buf = append(e.buf, markerInt16, byte(uint16(value)>>8), byte(uint16(value)))
	case value < math.MinInt16 && value >= math.MinInt32:
		e.buf = append(e.buf, markerInt32,
			byte(uint32(value)>>24), byte(uint32(value)>>16), byte(uint32(value)>>8), byte(uint32(value)))
	case value < math.MinInt32:
		var encoded [9]byte
		encoded[0] = markerInt64
		binary.BigEndian.PutUint64(encoded[1:], uint64(value))
		e.buf = append(e.buf, encoded[:]...)
	case value < 128:
		e.buf = append(e.buf, byte(value))
	case value < 256:
		e.buf = append(e.buf, markerUint8, byte(value))
	case value < 65536:
		e.buf = append(e.buf, markerUint16, byte(uint16(value)>>8), byte(uint16(value)))
	case value < 4294967296:
		e.buf = append(e.buf, markerUint32,
			byte(uint32(value)>>24), byte(uint32(value)>>16), byte(uint32(value)>>8), byte(uint32(value)))
	default:
		e.writeU64(uint64(value))
	}
}

// writeUInt8Compact writes a u8 using the compact form, matching rust-rmp's
// write_uint8.
func (e *encoder) writeUInt8Compact(value uint8) {
	if value < 128 {
		e.buf = append(e.buf, value)
		return
	}
	e.buf = append(e.buf, markerUint8, value)
}

func (e *encoder) writeArrayLen(length uint32) {
	switch {
	case length < 16:
		e.buf = append(e.buf, byte(fixArrayMask)|byte(length))
	case length <= math.MaxUint16:
		e.buf = append(e.buf, markerArray16, byte(length>>8), byte(length))
	default:
		e.buf = append(e.buf, markerArray32,
			byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
}

func (e *encoder) writeBin(value []byte) {
	switch {
	case len(value) <= math.MaxUint8:
		e.buf = append(e.buf, markerBin8, byte(len(value)))
	case len(value) <= math.MaxUint16:
		e.buf = append(e.buf, markerBin16, byte(len(value)>>8), byte(len(value)))
	default:
		e.buf = append(e.buf, markerBin32,
			byte(uint32(len(value))>>24), byte(uint32(len(value))>>16),
			byte(uint32(len(value))>>8), byte(uint32(len(value))))
	}
	e.buf = append(e.buf, value...)
}

func (e *encoder) writeStr(value string) {
	length := len(value)
	switch {
	case length < 32:
		e.buf = append(e.buf, byte(fixStrMask)|byte(length))
	case length <= math.MaxUint8:
		e.buf = append(e.buf, markerStr8, byte(length))
	case length <= math.MaxUint16:
		e.buf = append(e.buf, markerStr16, byte(length>>8), byte(length))
	default:
		e.buf = append(e.buf, markerStr32,
			byte(uint32(length)>>24), byte(uint32(length)>>16),
			byte(uint32(length)>>8), byte(uint32(length)))
	}
	e.buf = append(e.buf, value...)
}

func (e *encoder) writeOptionalStr(value *string) {
	if value == nil {
		e.writeNil()
		return
	}
	e.writeStr(*value)
}

func (e *encoder) writeOptionalU64(value *uint64) {
	if value == nil {
		e.writeNil()
		return
	}
	e.writeU64(*value)
}

// decoder reads MessagePack values from a byte slice. Every read checks
// bounds; a malformed record produces ErrMalformed instead of a panic.
type decoder struct {
	data []byte
	pos  int
}

func newDecoder(data []byte) *decoder {
	return &decoder{data: data}
}

func (d *decoder) remaining() int {
	return len(d.data) - d.pos
}

func (d *decoder) readMarker() (byte, error) {
	if d.pos >= len(d.data) {
		return 0, fmt.Errorf("%w: unexpected end of input", ErrMalformed)
	}
	marker := d.data[d.pos]
	d.pos++
	return marker, nil
}

func (d *decoder) peekMarker() (byte, error) {
	if d.pos >= len(d.data) {
		return 0, fmt.Errorf("%w: unexpected end of input", ErrMalformed)
	}
	return d.data[d.pos], nil
}

func (d *decoder) take(length int) ([]byte, error) {
	if length < 0 || d.remaining() < length {
		return nil, fmt.Errorf("%w: truncated value", ErrMalformed)
	}
	value := d.data[d.pos : d.pos+length]
	d.pos += length
	return value, nil
}

// readU8Explicit accepts only the uint8 marker, like rust-rmp's read_u8.
func (d *decoder) readU8Explicit() (uint8, error) {
	marker, err := d.readMarker()
	if err != nil {
		return 0, err
	}
	if marker != markerUint8 {
		return 0, fmt.Errorf("%w: expected uint8 marker, got 0x%02x", ErrMalformed, marker)
	}
	value, err := d.take(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

// readU16Explicit accepts only the uint16 marker, like rust-rmp's read_u16.
func (d *decoder) readU16Explicit() (uint16, error) {
	marker, err := d.readMarker()
	if err != nil {
		return 0, err
	}
	if marker != markerUint16 {
		return 0, fmt.Errorf("%w: expected uint16 marker, got 0x%02x", ErrMalformed, marker)
	}
	value, err := d.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(value), nil
}

// readU64Explicit accepts only the uint64 marker, like rust-rmp's read_u64.
func (d *decoder) readU64Explicit() (uint64, error) {
	marker, err := d.readMarker()
	if err != nil {
		return 0, err
	}
	if marker != markerUint64 {
		return 0, fmt.Errorf("%w: expected uint64 marker, got 0x%02x", ErrMalformed, marker)
	}
	value, err := d.take(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(value), nil
}

// readInt64 accepts every integer marker and range-checks the result as i64,
// like rmp's read_int::<i64>.
func (d *decoder) readInt64() (int64, error) {
	marker, err := d.readMarker()
	if err != nil {
		return 0, err
	}
	switch {
	case marker <= positiveFixMax:
		return int64(marker), nil
	case marker >= negativeFixMin:
		return int64(int8(marker)), nil
	case marker == markerUint8:
		value, err := d.take(1)
		if err != nil {
			return 0, err
		}
		return int64(value[0]), nil
	case marker == markerUint16:
		value, err := d.take(2)
		if err != nil {
			return 0, err
		}
		return int64(binary.BigEndian.Uint16(value)), nil
	case marker == markerUint32:
		value, err := d.take(4)
		if err != nil {
			return 0, err
		}
		return int64(binary.BigEndian.Uint32(value)), nil
	case marker == markerUint64:
		value, err := d.take(8)
		if err != nil {
			return 0, err
		}
		unsigned := binary.BigEndian.Uint64(value)
		if unsigned > math.MaxInt64 {
			return 0, fmt.Errorf("%w: integer out of range", ErrMalformed)
		}
		return int64(unsigned), nil
	case marker == markerInt8:
		value, err := d.take(1)
		if err != nil {
			return 0, err
		}
		return int64(int8(value[0])), nil
	case marker == markerInt16:
		value, err := d.take(2)
		if err != nil {
			return 0, err
		}
		return int64(int16(binary.BigEndian.Uint16(value))), nil
	case marker == markerInt32:
		value, err := d.take(4)
		if err != nil {
			return 0, err
		}
		return int64(int32(binary.BigEndian.Uint32(value))), nil
	case marker == markerInt64:
		value, err := d.take(8)
		if err != nil {
			return 0, err
		}
		return int64(binary.BigEndian.Uint64(value)), nil
	default:
		return 0, fmt.Errorf("%w: expected an integer, got 0x%02x", ErrMalformed, marker)
	}
}

// readIntegerByte accepts every integer marker and range-checks it as u8,
// like rmp's read_int::<u8>. Values outside 0..255 are malformed.
func (d *decoder) readIntegerByte() (uint8, error) {
	value, err := d.readInt64()
	if err != nil {
		return 0, err
	}
	if value < 0 || value > math.MaxUint8 {
		return 0, fmt.Errorf("%w: unsigned byte out of range", ErrMalformed)
	}
	return uint8(value), nil
}

func (d *decoder) readArrayLen() (uint32, error) {
	marker, err := d.readMarker()
	if err != nil {
		return 0, err
	}
	switch {
	case marker&0xf0 == fixArrayMask:
		return uint32(marker & 0x0f), nil
	case marker == markerArray16:
		value, err := d.take(2)
		if err != nil {
			return 0, err
		}
		return uint32(binary.BigEndian.Uint16(value)), nil
	case marker == markerArray32:
		value, err := d.take(4)
		if err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint32(value), nil
	default:
		return 0, fmt.Errorf("%w: expected an array, got 0x%02x", ErrMalformed, marker)
	}
}

func (d *decoder) readBinLen() (uint32, error) {
	marker, err := d.readMarker()
	if err != nil {
		return 0, err
	}
	switch marker {
	case markerBin8:
		value, err := d.take(1)
		if err != nil {
			return 0, err
		}
		return uint32(value[0]), nil
	case markerBin16:
		value, err := d.take(2)
		if err != nil {
			return 0, err
		}
		return uint32(binary.BigEndian.Uint16(value)), nil
	case markerBin32:
		value, err := d.take(4)
		if err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint32(value), nil
	default:
		return 0, fmt.Errorf("%w: expected binary data, got 0x%02x", ErrMalformed, marker)
	}
}

func (d *decoder) readStr() (string, error) {
	marker, err := d.readMarker()
	if err != nil {
		return "", err
	}
	var length int
	switch {
	case marker&0xe0 == fixStrMask:
		length = int(marker & 0x1f)
	case marker == markerStr8:
		value, err := d.take(1)
		if err != nil {
			return "", err
		}
		length = int(value[0])
	case marker == markerStr16:
		value, err := d.take(2)
		if err != nil {
			return "", err
		}
		length = int(binary.BigEndian.Uint16(value))
	case marker == markerStr32:
		value, err := d.take(4)
		if err != nil {
			return "", err
		}
		unsigned := binary.BigEndian.Uint32(value)
		if uint64(unsigned) > uint64(math.MaxInt) {
			return "", fmt.Errorf("%w: string too large", ErrMalformed)
		}
		length = int(unsigned)
	default:
		return "", fmt.Errorf("%w: expected a string, got 0x%02x", ErrMalformed, marker)
	}
	value, err := d.take(length)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(value) {
		return "", fmt.Errorf("%w: string is not valid UTF-8", ErrMalformed)
	}
	return string(value), nil
}

func (d *decoder) readOptionalStr() (*string, error) {
	marker, err := d.peekMarker()
	if err != nil {
		return nil, err
	}
	if marker == markerNil {
		d.pos++
		return nil, nil
	}
	value, err := d.readStr()
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (d *decoder) readOptionalU64() (*uint64, error) {
	marker, err := d.peekMarker()
	if err != nil {
		return nil, err
	}
	if marker == markerNil {
		d.pos++
		return nil, nil
	}
	value, err := d.readU64Explicit()
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (d *decoder) readOptionalUint8() (*uint8, error) {
	marker, err := d.peekMarker()
	if err != nil {
		return nil, err
	}
	if marker == markerNil {
		d.pos++
		return nil, nil
	}
	value, err := d.readIntegerByte()
	if err != nil {
		return nil, err
	}
	return &value, nil
}
