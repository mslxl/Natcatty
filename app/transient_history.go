package app

import (
	"sync"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

// maxTransientCommands bounds the per-Session in-memory history. A Host that
// runs Atuin is authoritative for persistence, so the buffer only feeds
// suggestions for the Session's lifetime; the oldest entries are dropped once
// the bound is reached.
const maxTransientCommands = 2000

// maxClosedTombstones bounds the Drop tombstones kept in memory. Session IDs
// are never reused, so only the newest tombstones need to block a late
// completion from a concurrent prompt recheck; older ones are forgotten so a
// long-running app cannot grow the map without bound.
const maxClosedTombstones = 4096

// transientCommands keeps commands a Host-Atuin Session captured but must not
// persist. Entries live only in this process, are never written to the
// journal, and are dropped when the Session ends. It is safe for concurrent
// use: the command hook writes from the Session's reader goroutine while the
// UI reads suggestions.
type transientCommands struct {
	mu       sync.Mutex
	sessions map[model.SessionID][]domainhistory.Entry
	watched  map[model.SessionID]struct{}
	// closed tombstones Session IDs whose teardown already ran, so a late
	// Remember from a concurrent completion cannot re-create a buffer that no
	// watcher remains to drop. closedOrder keeps those IDs in Drop order so the
	// map stays bounded: once more than maxClosedTombstones IDs are recorded,
	// the oldest tombstone is forgotten.
	closed      map[model.SessionID]struct{}
	closedOrder []model.SessionID
}

// newTransientCommands returns an empty registry.
func newTransientCommands() *transientCommands {
	return &transientCommands{closed: make(map[model.SessionID]struct{})}
}

// Remember appends one captured command for a Session, keeping at most
// maxTransientCommands of the newest entries. An empty Session ID and a
// Session that already dropped its buffer are ignored.
func (t *transientCommands) Remember(id model.SessionID, entry domainhistory.Entry) {
	if t == nil || id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, closed := t.closed[id]; closed {
		return
	}
	if t.sessions == nil {
		t.sessions = make(map[model.SessionID][]domainhistory.Entry)
	}
	entries := append(t.sessions[id], entry)
	if len(entries) > maxTransientCommands {
		entries = append([]domainhistory.Entry(nil),
			entries[len(entries)-maxTransientCommands:]...)
	}
	t.sessions[id] = entries
}

// Entries returns a copy of a Session's transient commands, newest last. It
// returns nil for an unknown Session.
func (t *transientCommands) Entries(id model.SessionID) []domainhistory.Entry {
	if t == nil || id == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entries := t.sessions[id]
	if len(entries) == 0 {
		return nil
	}
	return append([]domainhistory.Entry(nil), entries...)
}

// Drop forgets a Session's entries and its watch registration. It is terminal
// for the newest maxClosedTombstones Session IDs, so a late completion cannot
// re-create a buffer no watcher remains to drop. Older tombstones are forgotten
// so the map stays bounded; a completion that late would have had to outlive
// thousands of later Session teardowns.
func (t *transientCommands) Drop(id model.SessionID) {
	if t == nil || id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessions, id)
	delete(t.watched, id)
	if t.closed == nil {
		t.closed = make(map[model.SessionID]struct{})
	}
	if _, ok := t.closed[id]; !ok {
		t.closed[id] = struct{}{}
		t.closedOrder = append(t.closedOrder, id)
	}
	if len(t.closedOrder) > maxClosedTombstones {
		oldest := t.closedOrder[0]
		delete(t.closed, oldest)
		t.closedOrder = append(t.closedOrder[:0], t.closedOrder[1:]...)
	}
}

// Watch starts one goroutine per Session that drops its transient entries when
// done closes. It is idempotent: the UI surface factory may run more than once
// for the same Session. A Session that already dropped its buffer is ignored,
// because the Session ID is tombstoned.
func (t *transientCommands) Watch(id model.SessionID, done <-chan struct{}) {
	if t == nil || id == "" || done == nil {
		return
	}
	t.mu.Lock()
	if _, closed := t.closed[id]; closed {
		t.mu.Unlock()
		return
	}
	if t.watched == nil {
		t.watched = make(map[model.SessionID]struct{})
	}
	if _, ok := t.watched[id]; ok {
		t.mu.Unlock()
		return
	}
	t.watched[id] = struct{}{}
	t.mu.Unlock()
	go func() {
		<-done
		t.Drop(id)
	}()
}
