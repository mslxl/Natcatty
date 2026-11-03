// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"encoding/binary"
	"fmt"
)

const (
	fragmentHeaderSize = 10
	maxFragments       = 4096
)

// fragment is one mosh transport fragment. A datagram carries exactly one
// fragment; multi-fragment instructions are split across datagrams that share
// an ID.
type fragment struct {
	id      uint64
	number  uint16
	final   bool
	content []byte
}

func (f fragment) marshal() []byte {
	out := make([]byte, fragmentHeaderSize, fragmentHeaderSize+len(f.content))
	binary.BigEndian.PutUint64(out[:8], f.id)
	combined := f.number
	if f.final {
		combined |= 1 << 15
	}
	binary.BigEndian.PutUint16(out[8:], combined)
	return append(out, f.content...)
}

func unmarshalFragment(data []byte) (fragment, error) {
	if len(data) < fragmentHeaderSize {
		return fragment{}, fmt.Errorf("%w: fragment shorter than its header", ErrMalformed)
	}
	combined := binary.BigEndian.Uint16(data[8:10])
	return fragment{
		id:      binary.BigEndian.Uint64(data[:8]),
		number:  combined & 0x7fff,
		final:   combined&0x8000 != 0,
		content: data[fragmentHeaderSize:],
	}, nil
}

// fragmentAssembler mirrors mosh's FragmentAssembly: a new ID discards a
// partial instruction, and the final fragment fixes the expected count.
type fragmentAssembler struct {
	haveID  bool
	id      uint64
	parts   [][]byte
	present []bool
	arrived int
	total   int // -1 until the final fragment arrives
}

func newFragmentAssembler() fragmentAssembler {
	return fragmentAssembler{total: -1}
}

// add returns the reassembled payload when the fragment completes an
// instruction. A duplicate identical fragment is ignored; a conflicting
// duplicate is rejected without disturbing the assembly.
func (a *fragmentAssembler) add(f fragment) ([]byte, bool) {
	if f.number >= maxFragments {
		a.reset()
		return nil, false
	}
	if !a.haveID || a.id != f.id {
		a.reset()
		a.haveID = true
		a.id = f.id
	}
	index := int(f.number)
	for len(a.parts) <= index {
		a.parts = append(a.parts, nil)
		a.present = append(a.present, false)
	}
	if a.present[index] {
		if !bytesEqual(a.parts[index], f.content) {
			return nil, false
		}
	} else {
		a.parts[index] = append([]byte(nil), f.content...)
		a.present[index] = true
		a.arrived++
	}
	if f.final {
		a.total = index + 1
	}
	if a.total < 0 || a.arrived != a.total {
		return nil, false
	}
	var assembled []byte
	for i := 0; i < a.total; i++ {
		if !a.present[i] {
			return nil, false
		}
		assembled = append(assembled, a.parts[i]...)
	}
	a.reset()
	return assembled, true
}

func (a *fragmentAssembler) reset() {
	a.haveID = false
	a.id = 0
	a.parts = nil
	a.present = nil
	a.arrived = 0
	a.total = -1
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
