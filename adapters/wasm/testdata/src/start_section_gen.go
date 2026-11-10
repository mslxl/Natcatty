//go:build ignore

// insert_start_section rewrites a linked reactor so it carries a core start
// section (section id 8) without exporting the start function. wasm-ld only
// emits a start section for PIC or shared-memory output, so gen.sh links
// start-section.c with its start function exported and then runs this tool to
// drop the export and add the section.
//
// Usage: go run start_section_gen.go <input.wasm> <output.wasm> <export>
package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: start_section_gen <input.wasm> <output.wasm> <export>")
		os.Exit(2)
	}
	input, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	output, err := insertStartSection(input, os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], output, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type wasmSection struct {
	id      byte
	payload []byte
}

func insertStartSection(wasm []byte, export string) ([]byte, error) {
	if len(wasm) < 8 || string(wasm[:4]) != "\x00asm" {
		return nil, fmt.Errorf("not a core WebAssembly module")
	}
	var sections []wasmSection
	rest := wasm[8:]
	for len(rest) > 0 {
		id := rest[0]
		rest = rest[1:]
		size, n := binary.Uvarint(rest)
		if n <= 0 || size > uint64(len(rest)-n) {
			return nil, fmt.Errorf("section %d has an invalid size", id)
		}
		sections = append(sections, wasmSection{id: id, payload: rest[n : n+int(size)]})
		rest = rest[n+int(size):]
	}

	index := uint32(0)
	found := false
	for i := range sections {
		if sections[i].id != 7 { // export section
			continue
		}
		payload, functionIndex, err := removeFunctionExport(sections[i].payload, export)
		if err != nil {
			return nil, err
		}
		sections[i].payload = payload
		index, found = functionIndex, true
	}
	if !found {
		return nil, fmt.Errorf("export %q not found", export)
	}

	start := wasmSection{id: 8, payload: binary.AppendUvarint(nil, uint64(index))}
	// Sections must stay in ascending id order. Insert before the first
	// section with a higher id (the element or code section in practice).
	at := len(sections)
	for i, section := range sections {
		if section.id > start.id {
			at = i
			break
		}
	}
	sections = append(sections, wasmSection{})
	copy(sections[at+1:], sections[at:])
	sections[at] = start

	out := append([]byte(nil), wasm[:8]...)
	for _, section := range sections {
		out = append(out, section.id)
		out = binary.AppendUvarint(out, uint64(len(section.payload)))
		out = append(out, section.payload...)
	}
	return out, nil
}

func removeFunctionExport(payload []byte, name string) ([]byte, uint32, error) {
	count, n := binary.Uvarint(payload)
	if n <= 0 {
		return nil, 0, fmt.Errorf("export section has an invalid count")
	}
	offset := n
	var kept [][]byte
	index := uint32(0)
	found := false
	for i := uint64(0); i < count; i++ {
		start := offset
		nameLength, n := binary.Uvarint(payload[offset:])
		if n <= 0 || offset+n+int(nameLength) > len(payload) {
			return nil, 0, fmt.Errorf("export %d has an invalid name", i)
		}
		offset += n
		got := string(payload[offset : offset+int(nameLength)])
		offset += int(nameLength)
		if offset >= len(payload) {
			return nil, 0, fmt.Errorf("export %q has no kind", got)
		}
		kind := payload[offset]
		offset++
		entryIndex, n := binary.Uvarint(payload[offset:])
		if n <= 0 {
			return nil, 0, fmt.Errorf("export %q has an invalid index", got)
		}
		offset += n
		if got == name {
			if kind != 0 {
				return nil, 0, fmt.Errorf("export %q is not a function", name)
			}
			index = uint32(entryIndex)
			found = true
			continue
		}
		kept = append(kept, payload[start:offset])
	}
	if !found {
		return nil, 0, fmt.Errorf("export %q not found", name)
	}
	out := binary.AppendUvarint(nil, uint64(len(kept)))
	for _, entry := range kept {
		out = append(out, entry...)
	}
	return out, index, nil
}
