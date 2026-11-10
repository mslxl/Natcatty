package plugin

import (
	"fmt"
	"sync"

	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// Handle is an opaque, generation-tagged reference to a host-owned resource.
// The low 32 bits name a table slot and the high 32 bits name its generation;
// revoking or releasing a resource advances the generation, so an old handle
// never resolves to a newer resource in the same slot. It is generated from
// the plugin IDL; Slot and Generation come with it.
type Handle = domainpluginabi.Handle

// InvalidHandleError reports a handle that was released, revoked with its
// instance, or never issued by this table.
type InvalidHandleError struct {
	Handle Handle
}

func (e *InvalidHandleError) Error() string {
	return fmt.Sprintf("plugin: handle %#x is invalid or revoked", uint64(e.Handle))
}

type handleEntry struct {
	generation uint32
	value      any
	used       bool
}

// HandleTable stores generation-tagged host-owned resources for one Plugin
// Instance. It is safe for concurrent use. RevokeAll invalidates every
// handle, which is what disposal, permission revocation, and instance
// recycling require.
type HandleTable struct {
	mu      sync.Mutex
	entries []handleEntry
	free    []uint32
	limit   int
}

// NewHandleTable returns a table with the given slot limit. A non-positive
// limit means MaxHandles.
func NewHandleTable(limit int) *HandleTable {
	if limit <= 0 {
		limit = MaxHandles
	}
	return &HandleTable{limit: limit}
}

// Issue stores value and returns a fresh handle. It returns a LimitError when
// the table is full.
func (t *HandleTable) Issue(value any) (Handle, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var slot uint32
	if n := len(t.free); n > 0 {
		slot = t.free[n-1]
		t.free = t.free[:n-1]
	} else {
		if len(t.entries) >= t.limit {
			return 0, &LimitError{Resource: "plugin handles", Limit: t.limit}
		}
		t.entries = append(t.entries, handleEntry{generation: 1})
		slot = uint32(len(t.entries))
	}
	entry := &t.entries[slot-1]
	entry.used = true
	entry.value = value
	return Handle(uint64(entry.generation)<<32 | uint64(slot)), nil
}

// Resolve returns the value behind h. A released, revoked, or invented handle
// yields InvalidHandleError.
func (t *HandleTable) Resolve(h Handle) (any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, err := t.entryLocked(h)
	if err != nil {
		return nil, err
	}
	return entry.value, nil
}

// Release invalidates h and frees its slot for reuse.
func (t *HandleTable) Release(h Handle) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, err := t.entryLocked(h)
	if err != nil {
		return err
	}
	slot := h.Slot()
	entry.value = nil
	entry.used = false
	entry.generation++
	if entry.generation == 0 {
		entry.generation = 1
	}
	t.free = append(t.free, slot)
	return nil
}

// RevokeAll invalidates every handle in the table. It is called when an
// instance traps, shuts down, is closed, or loses a capability epoch.
func (t *HandleTable) RevokeAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.free = t.free[:0]
	for i := range t.entries {
		t.entries[i].value = nil
		t.entries[i].used = false
		t.entries[i].generation++
		if t.entries[i].generation == 0 {
			t.entries[i].generation = 1
		}
		t.free = append(t.free, uint32(i+1))
	}
}

// Len returns the number of live handles.
func (t *HandleTable) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	live := 0
	for i := range t.entries {
		if t.entries[i].used {
			live++
		}
	}
	return live
}

func (t *HandleTable) entryLocked(h Handle) (*handleEntry, error) {
	slot := h.Slot()
	if slot == 0 || int(slot) > len(t.entries) {
		return nil, &InvalidHandleError{Handle: h}
	}
	entry := &t.entries[slot-1]
	if !entry.used || entry.generation != h.Generation() {
		return nil, &InvalidHandleError{Handle: h}
	}
	return entry, nil
}
