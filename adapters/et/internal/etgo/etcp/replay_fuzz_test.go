// SPDX-License-Identifier: Apache-2.0
package etcp

import "testing"

// FuzzReplayHandling fuzzes the replay window: random push and trim sequences
// followed by random since/bytesBetween queries. It pins the replay contract
// the reconnect path relies on: sequence numbers stay contiguous, trim only
// drops entries below its keep point, retained bytes match what was pushed,
// and an out-of-window query reports false instead of returning wrong data or
// panicking on arbitrary int64 inputs.
func FuzzReplayHandling(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, 0}, []byte{1, 2, 3, 4}, int64(0), int64(3))
	f.Add([]byte{0, 1, 0, 2, 1, 0, 2}, []byte("abcdef"), int64(-1), int64(1<<40))
	f.Add([]byte{2, 2, 2}, []byte{}, int64(0), int64(0))
	f.Add([]byte{0, 0, 1, 3, 3, 3}, []byte{0xff}, int64(1), int64(2))
	f.Fuzz(func(t *testing.T, ops []byte, payload []byte, queryFrom, queryTo int64) {
		r := &ring{limit: 4096}
		type entry struct {
			seq  int64
			data []byte
		}
		var model []entry
		next := int64(0)
		for i, op := range ops {
			switch op % 4 {
			case 0: // push one payload byte
				if len(payload) == 0 {
					continue
				}
				data := []byte{payload[i%len(payload)]}
				r.push(data)
				model = append(model, entry{seq: next, data: data})
				next++
				if got := r.next(); got != next {
					t.Fatalf("next = %d, want %d", got, next)
				}
			case 1: // trim at a caller-legal keep point
				if len(model) == 0 {
					continue
				}
				first := r.first
				keep := first + int64(op)%int64(len(model)+1)
				if keep > next {
					keep = next
				}
				unsent := int(op) % 4097
				r.trim(keep, unsent)
				if r.first < first || r.first > next {
					t.Fatalf("trim moved first to %d outside [%d,%d]", r.first, first, next)
				}
				for _, dropped := range model[:r.first-first] {
					if dropped.seq >= keep {
						t.Fatalf("trim dropped seq %d at keep %d", dropped.seq, keep)
					}
				}
				// Retained entries must be exactly the pushed entries at
				// [first, next).
				for index, seq := 0, r.first; seq < next; index, seq = index+1, seq+1 {
					if index >= len(r.entries) || string(r.entries[index]) != string(model[seq].data) {
						t.Fatalf("retained entry %d does not match pushed data", seq)
					}
				}
			case 2: // arbitrary since query
				got, ok := r.since(queryFrom, queryTo)
				wantOK := queryFrom >= r.first && queryFrom <= queryTo && queryTo <= next
				if ok != wantOK {
					t.Fatalf("since(%d,%d) = %v, want %v (window [%d,%d])",
						queryFrom, queryTo, ok, wantOK, r.first, next)
				}
				if !ok {
					continue
				}
				if len(got) != int(queryTo-queryFrom) {
					t.Fatalf("since returned %d entries, want %d", len(got), queryTo-queryFrom)
				}
				for index, data := range got {
					want := model[queryFrom+int64(index)].data
					if string(data) != string(want) {
						t.Fatalf("since entry %d = %q, want %q", index, data, want)
					}
				}
			case 3: // valid bytesBetween query when the window allows one
				if r.first > next {
					continue
				}
				from := r.first + int64(op)%int64(next-r.first+1)
				to := from + int64(ops[(i+1)%len(ops)])%int64(next-from+1)
				var want int
				for seq := from; seq < to; seq++ {
					want += len(model[seq].data)
				}
				if got := r.bytesBetween(from, to); got != want {
					t.Fatalf("bytesBetween(%d,%d) = %d, want %d", from, to, got, want)
				}
			}
		}
	})
}
