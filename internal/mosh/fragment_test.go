// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"testing"
)

func TestFragmentMarshalRoundTrip(t *testing.T) {
	original := fragment{id: 0x1122334455667788, number: 3, final: true, content: []byte("payload")}
	decoded, err := unmarshalFragment(original.marshal())
	if err != nil {
		t.Fatalf("unmarshalFragment: %v", err)
	}
	if decoded.id != original.id || decoded.number != original.number ||
		decoded.final != original.final || !bytes.Equal(decoded.content, original.content) {
		t.Fatalf("round trip mismatch: %+v", decoded)
	}
}

func TestFragmentRejectsShortHeader(t *testing.T) {
	if _, err := unmarshalFragment(make([]byte, fragmentHeaderSize-1)); err == nil {
		t.Fatal("accepted a fragment shorter than its header")
	}
}

func TestFragmentAssembler(t *testing.T) {
	assembler := newFragmentAssembler()
	parts := [][]byte{[]byte("one "), []byte("two "), []byte("three")}
	id := uint64(42)

	if assembled, complete := assembler.add(fragment{id: id, number: 0, content: parts[0]}); complete {
		t.Fatalf("completed early with %q", assembled)
	}
	if _, complete := assembler.add(fragment{id: id, number: 1, content: parts[1]}); complete {
		t.Fatal("completed before the final fragment")
	}
	// A duplicate identical fragment is harmless.
	if _, complete := assembler.add(fragment{id: id, number: 0, content: parts[0]}); complete {
		t.Fatal("duplicate fragment completed the assembly")
	}
	// A conflicting duplicate is rejected without poisoning the assembly.
	if _, complete := assembler.add(fragment{id: id, number: 1, content: []byte("XXX")}); complete {
		t.Fatal("conflicting duplicate completed the assembly")
	}
	assembled, complete := assembler.add(fragment{id: id, number: 2, final: true, content: parts[2]})
	if !complete {
		t.Fatal("assembly did not complete")
	}
	if string(assembled) != "one two three" {
		t.Fatalf("assembled = %q", assembled)
	}
}

func TestFragmentAssemblerResetsOnNewID(t *testing.T) {
	assembler := newFragmentAssembler()
	if _, complete := assembler.add(fragment{id: 1, number: 0, content: []byte("discarded")}); complete {
		t.Fatal("unexpected completion")
	}
	assembled, complete := assembler.add(fragment{id: 2, number: 0, final: true, content: []byte("fresh")})
	if !complete || string(assembled) != "fresh" {
		t.Fatalf("new ID did not reset: %q, %v", assembled, complete)
	}
}

func TestFragmentAssemblerRejectsHugeFragmentNumber(t *testing.T) {
	assembler := newFragmentAssembler()
	if _, complete := assembler.add(fragment{id: 1, number: maxFragments, final: true, content: []byte("x")}); complete {
		t.Fatal("accepted a fragment number beyond the limit")
	}
}
