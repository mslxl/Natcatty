// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ProtocolVersion is the only mosh wire protocol version this client speaks.
const ProtocolVersion = 2

// ErrMalformed reports a protobuf or framing error from an authenticated
// packet. It is a protocol failure, not a transient packet loss.
var ErrMalformed = errors.New("mosh: malformed protocol message")

// Field numbers from the upstream proto2 schemas. Mosh uses a hand-rolled
// subset of protobuf; unknown fields are skipped for forward compatibility.
const (
	fieldInstruction     = 1
	fieldProtocolVersion = 1
	fieldOldNum          = 2
	fieldNewNum          = 3
	fieldAckNum          = 4
	fieldThrowawayNum    = 5
	fieldDiff            = 6
	fieldChaff           = 7
	fieldHostBytes       = 2
	fieldHostString      = 4
	fieldResize          = 3
	fieldResizeWidth     = 5
	fieldResizeHeight    = 6
	fieldEchoAck         = 7
	fieldEchoAckNum      = 8
	fieldKeystroke       = 2
	fieldKeys            = 4
	wireVarint           = 0
	wire64Bit            = 1
	wireLengthDelimited  = 2
	wire32Bit            = 5
)

// TransportInstruction is TransportBuffers.Instruction: one SSP state
// transition and its acknowledgements.
type TransportInstruction struct {
	ProtocolVersion uint32
	OldNum          uint64
	NewNum          uint64
	AckNum          uint64
	ThrowawayNum    uint64
	Diff            []byte
	Chaff           []byte
}

// HostInstruction is one instruction in a HostMessage. The zero value holds no
// instruction; EchoAckPresent distinguishes an echo ack of zero.
type HostInstruction struct {
	HostString     []byte
	Resize         bool
	Width          int32
	Height         int32
	EchoAckPresent bool
	EchoAckNum     uint64
}

// UserInstruction is one instruction in a UserMessage: a keystroke or a
// terminal resize.
type UserInstruction struct {
	Keys   []byte
	Resize bool
	Width  int32
	Height int32
}

// Marshal encodes a TransportInstruction.
func (ti TransportInstruction) Marshal() []byte {
	var out []byte
	if ti.ProtocolVersion != 0 {
		out = appendTagVarint(out, fieldProtocolVersion, uint64(ti.ProtocolVersion))
	}
	out = appendTagVarint(out, fieldOldNum, ti.OldNum)
	out = appendTagVarint(out, fieldNewNum, ti.NewNum)
	out = appendTagVarint(out, fieldAckNum, ti.AckNum)
	out = appendTagVarint(out, fieldThrowawayNum, ti.ThrowawayNum)
	if len(ti.Diff) > 0 {
		out = appendTagBytes(out, fieldDiff, ti.Diff)
	}
	if len(ti.Chaff) > 0 {
		out = appendTagBytes(out, fieldChaff, ti.Chaff)
	}
	return out
}

// UnmarshalTransportInstruction decodes a TransportInstruction.
func UnmarshalTransportInstruction(data []byte) (TransportInstruction, error) {
	var ti TransportInstruction
	for len(data) > 0 {
		field, wire, n, err := consumeTag(data)
		if err != nil {
			return ti, err
		}
		data = data[n:]
		switch field {
		case fieldProtocolVersion:
			if err := requireWire(field, wire, wireVarint); err != nil {
				return ti, err
			}
			value, n, err := consumeVarint(data)
			if err != nil {
				return ti, err
			}
			ti.ProtocolVersion = uint32(value)
			data = data[n:]
		case fieldOldNum, fieldNewNum, fieldAckNum, fieldThrowawayNum:
			if err := requireWire(field, wire, wireVarint); err != nil {
				return ti, err
			}
			value, n, err := consumeVarint(data)
			if err != nil {
				return ti, err
			}
			switch field {
			case fieldOldNum:
				ti.OldNum = value
			case fieldNewNum:
				ti.NewNum = value
			case fieldAckNum:
				ti.AckNum = value
			case fieldThrowawayNum:
				ti.ThrowawayNum = value
			}
			data = data[n:]
		case fieldDiff, fieldChaff:
			if err := requireWire(field, wire, wireLengthDelimited); err != nil {
				return ti, err
			}
			value, n, err := consumeBytes(data)
			if err != nil {
				return ti, err
			}
			if field == fieldDiff {
				ti.Diff = append([]byte(nil), value...)
			} else {
				ti.Chaff = append([]byte(nil), value...)
			}
			data = data[n:]
		default:
			n, err := skipField(data, wire)
			if err != nil {
				return ti, err
			}
			data = data[n:]
		}
	}
	return ti, nil
}

// MarshalHostMessage encodes host instructions.
func MarshalHostMessage(instructions []HostInstruction) []byte {
	var out []byte
	for _, instruction := range instructions {
		out = appendTagBytes(out, fieldInstruction, instruction.marshal())
	}
	return out
}

func (hi HostInstruction) marshal() []byte {
	var out []byte
	if len(hi.HostString) > 0 {
		out = appendTagBytes(out, fieldHostBytes, appendTagBytes(nil, fieldHostString, hi.HostString))
	}
	if hi.Resize {
		var resize []byte
		resize = appendTagVarint(resize, fieldResizeWidth, uint64(hi.Width))
		resize = appendTagVarint(resize, fieldResizeHeight, uint64(hi.Height))
		out = appendTagBytes(out, fieldResize, resize)
	}
	if hi.EchoAckPresent {
		out = appendTagBytes(out, fieldEchoAck, appendTagVarint(nil, fieldEchoAckNum, hi.EchoAckNum))
	}
	return out
}

// UnmarshalHostMessage decodes host instructions.
func UnmarshalHostMessage(data []byte) ([]HostInstruction, error) {
	var instructions []HostInstruction
	for len(data) > 0 {
		field, wire, n, err := consumeTag(data)
		if err != nil {
			return nil, err
		}
		data = data[n:]
		if field != fieldInstruction {
			n, err := skipField(data, wire)
			if err != nil {
				return nil, err
			}
			data = data[n:]
			continue
		}
		if err := requireWire(field, wire, wireLengthDelimited); err != nil {
			return nil, err
		}
		value, n, err := consumeBytes(data)
		if err != nil {
			return nil, err
		}
		instruction, err := unmarshalHostInstruction(value)
		if err != nil {
			return nil, err
		}
		instructions = append(instructions, instruction)
		data = data[n:]
	}
	return instructions, nil
}

func unmarshalHostInstruction(data []byte) (HostInstruction, error) {
	var instruction HostInstruction
	for len(data) > 0 {
		field, wire, n, err := consumeTag(data)
		if err != nil {
			return instruction, err
		}
		data = data[n:]
		switch field {
		case fieldHostBytes:
			if err := requireWire(field, wire, wireLengthDelimited); err != nil {
				return instruction, err
			}
			value, n, err := consumeBytes(data)
			if err != nil {
				return instruction, err
			}
			if err := decodeHostBytes(value, &instruction); err != nil {
				return instruction, err
			}
			data = data[n:]
		case fieldResize:
			if err := requireWire(field, wire, wireLengthDelimited); err != nil {
				return instruction, err
			}
			value, n, err := consumeBytes(data)
			if err != nil {
				return instruction, err
			}
			if err := decodeResize(value, &instruction.Width, &instruction.Height); err != nil {
				return instruction, err
			}
			instruction.Resize = true
			data = data[n:]
		case fieldEchoAck:
			if err := requireWire(field, wire, wireLengthDelimited); err != nil {
				return instruction, err
			}
			value, n, err := consumeBytes(data)
			if err != nil {
				return instruction, err
			}
			number, err := decodeEchoAck(value)
			if err != nil {
				return instruction, err
			}
			instruction.EchoAckPresent = true
			instruction.EchoAckNum = number
			data = data[n:]
		default:
			n, err := skipField(data, wire)
			if err != nil {
				return instruction, err
			}
			data = data[n:]
		}
	}
	return instruction, nil
}

func decodeHostBytes(data []byte, instruction *HostInstruction) error {
	for len(data) > 0 {
		field, wire, n, err := consumeTag(data)
		if err != nil {
			return err
		}
		data = data[n:]
		if field == fieldHostString {
			if err := requireWire(field, wire, wireLengthDelimited); err != nil {
				return err
			}
			value, n, err := consumeBytes(data)
			if err != nil {
				return err
			}
			instruction.HostString = append(instruction.HostString, value...)
			data = data[n:]
			continue
		}
		n, err = skipField(data, wire)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func decodeEchoAck(data []byte) (uint64, error) {
	var number uint64
	for len(data) > 0 {
		field, wire, n, err := consumeTag(data)
		if err != nil {
			return 0, err
		}
		data = data[n:]
		if field == fieldEchoAckNum {
			if err = requireWire(field, wire, wireVarint); err != nil {
				return 0, err
			}
			number, n, err = consumeVarint(data)
			if err != nil {
				return 0, err
			}
			data = data[n:]
			continue
		}
		n, err = skipField(data, wire)
		if err != nil {
			return 0, err
		}
		data = data[n:]
	}
	return number, nil
}

// MarshalUserMessage encodes user instructions.
func MarshalUserMessage(instructions []UserInstruction) []byte {
	var out []byte
	for _, instruction := range instructions {
		out = appendTagBytes(out, fieldInstruction, instruction.marshal())
	}
	return out
}

func (ui UserInstruction) marshal() []byte {
	var out []byte
	if len(ui.Keys) > 0 {
		out = appendTagBytes(out, fieldKeystroke, appendTagBytes(nil, fieldKeys, ui.Keys))
	}
	if ui.Resize {
		var resize []byte
		resize = appendTagVarint(resize, fieldResizeWidth, uint64(ui.Width))
		resize = appendTagVarint(resize, fieldResizeHeight, uint64(ui.Height))
		out = appendTagBytes(out, fieldResize, resize)
	}
	return out
}

// UnmarshalUserMessage decodes user instructions. The client does not use this
// direction in production, but the round-trip is tested so the encoder and the
// mosh-server expectations stay pinned to the same schema.
func UnmarshalUserMessage(data []byte) ([]UserInstruction, error) {
	var instructions []UserInstruction
	for len(data) > 0 {
		field, wire, n, err := consumeTag(data)
		if err != nil {
			return nil, err
		}
		data = data[n:]
		if field != fieldInstruction {
			n, err := skipField(data, wire)
			if err != nil {
				return nil, err
			}
			data = data[n:]
			continue
		}
		if err := requireWire(field, wire, wireLengthDelimited); err != nil {
			return nil, err
		}
		value, n, err := consumeBytes(data)
		if err != nil {
			return nil, err
		}
		instruction, err := unmarshalUserInstruction(value)
		if err != nil {
			return nil, err
		}
		instructions = append(instructions, instruction)
		data = data[n:]
	}
	return instructions, nil
}

func unmarshalUserInstruction(data []byte) (UserInstruction, error) {
	var instruction UserInstruction
	for len(data) > 0 {
		field, wire, n, err := consumeTag(data)
		if err != nil {
			return instruction, err
		}
		data = data[n:]
		switch field {
		case fieldKeystroke:
			if err := requireWire(field, wire, wireLengthDelimited); err != nil {
				return instruction, err
			}
			value, n, err := consumeBytes(data)
			if err != nil {
				return instruction, err
			}
			keys, err := decodeKeystroke(value)
			if err != nil {
				return instruction, err
			}
			instruction.Keys = append(instruction.Keys, keys...)
			data = data[n:]
		case fieldResize:
			if err := requireWire(field, wire, wireLengthDelimited); err != nil {
				return instruction, err
			}
			value, n, err := consumeBytes(data)
			if err != nil {
				return instruction, err
			}
			if err := decodeResize(value, &instruction.Width, &instruction.Height); err != nil {
				return instruction, err
			}
			instruction.Resize = true
			data = data[n:]
		default:
			n, err := skipField(data, wire)
			if err != nil {
				return instruction, err
			}
			data = data[n:]
		}
	}
	return instruction, nil
}

func decodeKeystroke(data []byte) ([]byte, error) {
	var keys []byte
	for len(data) > 0 {
		field, wire, n, err := consumeTag(data)
		if err != nil {
			return nil, err
		}
		data = data[n:]
		if field == fieldKeys {
			if err := requireWire(field, wire, wireLengthDelimited); err != nil {
				return nil, err
			}
			value, n, err := consumeBytes(data)
			if err != nil {
				return nil, err
			}
			keys = append(keys, value...)
			data = data[n:]
			continue
		}
		n, err = skipField(data, wire)
		if err != nil {
			return nil, err
		}
		data = data[n:]
	}
	return keys, nil
}

func decodeResize(data []byte, width, height *int32) error {
	for len(data) > 0 {
		field, wire, n, err := consumeTag(data)
		if err != nil {
			return err
		}
		data = data[n:]
		switch field {
		case fieldResizeWidth, fieldResizeHeight:
			if err := requireWire(field, wire, wireVarint); err != nil {
				return err
			}
			value, n, err := consumeVarint(data)
			if err != nil {
				return err
			}
			if field == fieldResizeWidth {
				*width = int32(value)
			} else {
				*height = int32(value)
			}
			data = data[n:]
		default:
			n, err := skipField(data, wire)
			if err != nil {
				return err
			}
			data = data[n:]
		}
	}
	return nil
}

func requireWire(field, wire, want int) error {
	if wire != want {
		return fmt.Errorf("%w: field %d has wire type %d, want %d", ErrMalformed, field, wire, want)
	}
	return nil
}

func appendTag(out []byte, field, wire int) []byte {
	return appendVarint(out, uint64(field)<<3|uint64(wire))
}

func appendTagVarint(out []byte, field int, value uint64) []byte {
	out = appendTag(out, field, wireVarint)
	return appendVarint(out, value)
}

func appendTagBytes(out []byte, field int, value []byte) []byte {
	out = appendTag(out, field, wireLengthDelimited)
	out = appendVarint(out, uint64(len(value)))
	return append(out, value...)
}

func appendVarint(out []byte, value uint64) []byte {
	for value >= 0x80 {
		out = append(out, byte(value)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}

func consumeVarint(data []byte) (uint64, int, error) {
	var value uint64
	for i := 0; i < binary.MaxVarintLen64; i++ {
		if i >= len(data) {
			return 0, 0, fmt.Errorf("%w: truncated varint", ErrMalformed)
		}
		b := data[i]
		if i == binary.MaxVarintLen64-1 && b > 1 {
			return 0, 0, fmt.Errorf("%w: varint overflow", ErrMalformed)
		}
		value |= uint64(b&0x7f) << (7 * i)
		if b < 0x80 {
			return value, i + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("%w: oversized varint", ErrMalformed)
}

func consumeTag(data []byte) (field, wire, n int, err error) {
	value, n, err := consumeVarint(data)
	if err != nil {
		return 0, 0, 0, err
	}
	field = int(value >> 3)
	wire = int(value & 7)
	if field == 0 {
		return 0, 0, 0, fmt.Errorf("%w: field number zero", ErrMalformed)
	}
	return field, wire, n, nil
}

func consumeBytes(data []byte) ([]byte, int, error) {
	length, n, err := consumeVarint(data)
	if err != nil {
		return nil, 0, err
	}
	if length > uint64(len(data)-n) {
		return nil, 0, fmt.Errorf("%w: truncated length-delimited field", ErrMalformed)
	}
	return data[n : n+int(length)], n + int(length), nil
}

func skipField(data []byte, wire int) (int, error) {
	switch wire {
	case wireVarint:
		_, n, err := consumeVarint(data)
		return n, err
	case wire64Bit:
		if len(data) < 8 {
			return 0, fmt.Errorf("%w: truncated 64-bit field", ErrMalformed)
		}
		return 8, nil
	case wireLengthDelimited:
		_, n, err := consumeBytes(data)
		return n, err
	case wire32Bit:
		if len(data) < 4 {
			return 0, fmt.Errorf("%w: truncated 32-bit field", ErrMalformed)
		}
		return 4, nil
	default:
		return 0, fmt.Errorf("%w: unsupported wire type %d", ErrMalformed, wire)
	}
}
