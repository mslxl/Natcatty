// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"errors"
	"testing"
)

func TestTransportInstructionRoundTrip(t *testing.T) {
	original := TransportInstruction{
		ProtocolVersion: ProtocolVersion,
		OldNum:          3,
		NewNum:          4,
		AckNum:          9,
		ThrowawayNum:    2,
		Diff:            []byte{0xde, 0xad, 0xbe, 0xef},
		Chaff:           []byte{1, 2, 3},
	}
	decoded, err := UnmarshalTransportInstruction(original.Marshal())
	if err != nil {
		t.Fatalf("UnmarshalTransportInstruction: %v", err)
	}
	if decoded.ProtocolVersion != original.ProtocolVersion || decoded.OldNum != original.OldNum ||
		decoded.NewNum != original.NewNum || decoded.AckNum != original.AckNum ||
		decoded.ThrowawayNum != original.ThrowawayNum ||
		!bytes.Equal(decoded.Diff, original.Diff) || !bytes.Equal(decoded.Chaff, original.Chaff) {
		t.Fatalf("round trip mismatch: %+v", decoded)
	}
}

func TestHostMessageRoundTrip(t *testing.T) {
	original := []HostInstruction{
		{HostString: []byte("hello")},
		{Resize: true, Width: 100, Height: 30},
		{EchoAckPresent: true, EchoAckNum: 7},
		{HostString: []byte("world")},
	}
	decoded, err := UnmarshalHostMessage(MarshalHostMessage(original))
	if err != nil {
		t.Fatalf("UnmarshalHostMessage: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("decoded %d instructions, want %d", len(decoded), len(original))
	}
	if string(decoded[0].HostString) != "hello" || string(decoded[3].HostString) != "world" {
		t.Fatalf("host strings lost: %+v", decoded)
	}
	if !decoded[1].Resize || decoded[1].Width != 100 || decoded[1].Height != 30 {
		t.Fatalf("resize lost: %+v", decoded[1])
	}
	if !decoded[2].EchoAckPresent || decoded[2].EchoAckNum != 7 {
		t.Fatalf("echo ack lost: %+v", decoded[2])
	}
}

func TestUserMessageRoundTrip(t *testing.T) {
	original := []UserInstruction{
		{Keys: []byte("ls -la\r")},
		{Resize: true, Width: 132, Height: 43},
	}
	decoded, err := UnmarshalUserMessage(MarshalUserMessage(original))
	if err != nil {
		t.Fatalf("UnmarshalUserMessage: %v", err)
	}
	if len(decoded) != 2 || string(decoded[0].Keys) != "ls -la\r" {
		t.Fatalf("keystrokes lost: %+v", decoded)
	}
	if !decoded[1].Resize || decoded[1].Width != 132 || decoded[1].Height != 43 {
		t.Fatalf("resize lost: %+v", decoded[1])
	}
}

func TestInstructionSkipsUnknownFields(t *testing.T) {
	// field 99, 64-bit value, followed by protocol_version=2 and old_num=5.
	var encoded []byte
	encoded = appendTag(encoded, 99, wire64Bit)
	encoded = append(encoded, 1, 2, 3, 4, 5, 6, 7, 8)
	encoded = appendTagVarint(encoded, fieldProtocolVersion, 2)
	encoded = appendTagVarint(encoded, fieldOldNum, 5)
	decoded, err := UnmarshalTransportInstruction(encoded)
	if err != nil {
		t.Fatalf("UnmarshalTransportInstruction: %v", err)
	}
	if decoded.ProtocolVersion != 2 || decoded.OldNum != 5 {
		t.Fatalf("known fields lost: %+v", decoded)
	}
}

func TestInstructionRejectsTruncation(t *testing.T) {
	cases := [][]byte{
		{0x18},                            // new_num tag with no varint
		{0x08, 0x80},                      // incomplete varint
		{0x32, 0x05, 0x01},                // diff length longer than the data
		{0x3b, 0x01, 0x02},                // unsupported wire type 3
		{0x00},                            // field number zero
		appendTag(nil, 99, wire32Bit)[:2], // truncated 32-bit field tag
	}
	for _, encoded := range cases {
		if _, err := UnmarshalTransportInstruction(encoded); !errors.Is(err, ErrMalformed) {
			t.Fatalf("UnmarshalTransportInstruction(%x) error = %v, want ErrMalformed", encoded, err)
		}
	}
}
