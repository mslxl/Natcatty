// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

// Go native fuzz targets for the mosh wire seams: raw datagrams, fragments,
// transport instructions, and their zlib/protobuf decode. The gate runs each
// target for a bounded period (internal/tools/moshparity fuzz suite), and the
// seed corpus below is also exercised by the ordinary `go test` job.
//
// Run one target directly:
//
//	go test ./internal/mosh -run '^$' -fuzz '^FuzzDatagramReceive$' -fuzztime 20s

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

const fuzzInputLimit = 64 << 10

// FuzzDatagramReceive feeds arbitrary wire bytes to the authenticated
// datagram parser. It covers the pre-authentication bounds: short datagrams,
// the direction bit, nonce construction, OCB authentication failure, and the
// timestamp/fragment length checks before any protocol instruction is decoded.
func FuzzDatagramReceive(f *testing.F) {
	key := bytes.Repeat([]byte{0x5a}, 16)
	client, err := NewClient(key, 80, 24)
	if err != nil {
		f.Fatalf("NewClient: %v", err)
	}
	if err := client.Input([]byte("seed")); err != nil {
		f.Fatalf("Input: %v", err)
	}
	for _, datagram := range client.SendDatagrams(time.Unix(1, 0)) {
		f.Add(datagram)
	}
	f.Add([]byte{})
	f.Add([]byte{0x80, 0, 0, 0, 0, 0, 0, 1})
	f.Add(bytes.Repeat([]byte{0xff}, 64))

	f.Fuzz(func(t *testing.T, wire []byte) {
		if len(wire) > fuzzInputLimit {
			t.Skip()
		}
		client, err := NewClient(key, 80, 24)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		_, _, _ = client.Receive(wire, time.Unix(1, 0))
		client.Close()
	})
}

// FuzzFragmentReassembly fuzzes the fragment header and the reassembly state
// machine: out-of-order numbers, missing finals, duplicates, conflicting
// duplicates, and the maxFragments bound. Every accepted datagram must leave
// the assembler bounded and must never assemble without a final fragment.
func FuzzFragmentReassembly(f *testing.F) {
	seed := fragment{id: 7, number: 0, final: true, content: []byte("payload")}
	f.Add(seed.marshal())
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0x01}, fragmentHeaderSize))
	bad := make([]byte, fragmentHeaderSize+3)
	binary.BigEndian.PutUint64(bad[:8], 1)
	binary.BigEndian.PutUint16(bad[8:10], 0x8000|maxFragments)
	f.Add(bad)

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzInputLimit {
			t.Skip()
		}
		parsed, err := unmarshalFragment(data)
		if err != nil {
			return
		}
		assembler := newFragmentAssembler()
		for attempt := 0; attempt < 4; attempt++ {
			_, complete := assembler.add(parsed)
			if len(assembler.parts) > maxFragments {
				t.Fatalf("assembler kept %d parts, max %d", len(assembler.parts), maxFragments)
			}
			if complete {
				break
			}
		}
	})
}

// FuzzTransportInstruction fuzzes the zlib-compressed protobuf instruction
// decode and the three message decoders. It covers truncation, unknown wire
// types, false field numbers, and decompression bounds (maxAssembledInstruction).
func FuzzTransportInstruction(f *testing.F) {
	instruction := TransportInstruction{
		ProtocolVersion: ProtocolVersion,
		OldNum:          1,
		NewNum:          2,
		AckNum:          1,
		ThrowawayNum:    1,
		Diff:            MarshalHostMessage([]HostInstruction{{HostString: []byte("hi")}}),
	}
	compressed, err := compressInstruction(instruction.Marshal())
	if err != nil {
		f.Fatalf("compressInstruction: %v", err)
	}
	f.Add(compressed)
	f.Add(instruction.Marshal())
	f.Add(MarshalHostMessage([]HostInstruction{{Resize: true, Width: 80, Height: 24}}))
	f.Add(MarshalUserMessage([]UserInstruction{{Keys: []byte("x")}}))
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 32))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzInputLimit {
			t.Skip()
		}
		_, _ = decodeInstruction(data)
		_, _ = UnmarshalTransportInstruction(data)
		_, _ = UnmarshalHostMessage(data)
		_, _ = UnmarshalUserMessage(data)
	})
}
