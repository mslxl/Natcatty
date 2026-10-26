package browser

import (
	"errors"
	"fmt"
	"sync"
	"time"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
)

// BookmarkStore persists the complete bookmark list. The adapter owns the
// file format; the application layer owns scope, ownership, and ordering.
type BookmarkStore interface {
	Load() ([]domainbrowser.Bookmark, error)
	Save([]domainbrowser.Bookmark) error
}

// Bookmarks is the process-wide bookmark use case. One service is shared by
// every Host's browser panel so an all-Host edit is visible everywhere.
type Bookmarks struct {
	mu      sync.Mutex
	store   BookmarkStore
	items   []domainbrowser.Bookmark
	now     func() time.Time
	newID   func() string
	listens map[int]func()
	loadErr error

	muListen sync.Mutex
	nextID   int
}

// NewBookmarks loads the stored bookmarks. Malformed entries are skipped, as
// the original store did, so one bad record cannot hide the rest. A read error
// starts with an empty in-memory list but blocks every save, so an unreadable
// file is never replaced by the first edit; the file can be repaired and the
// app restarted without losing the user's bookmarks.
func NewBookmarks(store BookmarkStore, now func() time.Time, newID func() string) (*Bookmarks, error) {
	if store == nil {
		return nil, errors.New("browser: bookmark store is required")
	}
	if now == nil {
		now = time.Now
	}
	raw, err := store.Load()
	service := &Bookmarks{
		store:   store,
		now:     now,
		newID:   newID,
		listens: make(map[int]func()),
		loadErr: err,
	}
	service.items = service.sanitize(raw)
	return service, nil
}

func (b *Bookmarks) sanitize(raw []domainbrowser.Bookmark) []domainbrowser.Bookmark {
	if len(raw) > domainbrowser.MaxBookmarks {
		return nil
	}
	out := make([]domainbrowser.Bookmark, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		normalized, err := domainbrowser.NormalizeBookmark(domainbrowser.BookmarkInput{
			ID: item.ID, Title: item.Title, URL: item.URL, Scope: item.Scope, HostID: item.HostID,
			Icon: item.Icon, IconMode: item.IconMode, IconDataURL: item.IconDataURL, Color: item.Color,
			Order: item.Order, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		}, nil, b.now(), func() string {
			if b.newID != nil {
				return b.newID()
			}
			return item.ID
		})
		if err != nil {
			continue
		}
		if _, ok := seen[normalized.ID]; ok {
			continue
		}
		seen[normalized.ID] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

// All returns a copy of every bookmark.
func (b *Bookmarks) All() []domainbrowser.Bookmark {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domainbrowser.Bookmark(nil), b.items...)
}

// List returns the bookmarks visible to hostID, in display order.
func (b *Bookmarks) List(hostID model.HostID) []domainbrowser.Bookmark {
	b.mu.Lock()
	items := append([]domainbrowser.Bookmark(nil), b.items...)
	b.mu.Unlock()
	return domainbrowser.VisibleBookmarks(items, hostID)
}

// Get looks up one bookmark.
func (b *Bookmarks) Get(id string) (domainbrowser.Bookmark, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, item := range b.items {
		if item.ID == id {
			return item, true
		}
	}
	return domainbrowser.Bookmark{}, false
}

// Upsert creates or edits a bookmark on behalf of hostID. Host-scoped edits
// are refused when the bookmark belongs to another Host; all-Host bookmarks
// are shared and editable from any Host. A failed persist leaves the previous
// in-memory list intact.
func (b *Bookmarks) Upsert(hostID model.HostID, input domainbrowser.BookmarkInput) (domainbrowser.Bookmark, error) {
	b.mu.Lock()

	previousIndex := -1
	var previous *domainbrowser.Bookmark
	if input.ID != "" {
		for i := range b.items {
			if b.items[i].ID == input.ID {
				previousIndex = i
				copy := b.items[i]
				previous = &copy
				break
			}
		}
	}
	if previous != nil && previous.Scope == domainbrowser.ScopeHost && previous.HostID != hostID {
		b.mu.Unlock()
		return domainbrowser.Bookmark{}, domainbrowser.ErrBookmarkOwner
	}
	if previousIndex < 0 && len(b.items) >= domainbrowser.MaxBookmarks {
		b.mu.Unlock()
		return domainbrowser.Bookmark{}, domainbrowser.ErrBookmarkLimit
	}
	scope := domainbrowser.ScopeHost
	if input.Scope == domainbrowser.ScopeAllHosts {
		scope = domainbrowser.ScopeAllHosts
	}
	input.Scope = scope
	if previous == nil && input.Order == 0 {
		// New entries append in creation order; the original UI used the same
		// explicit order field for drag reordering.
		input.Order = int64(len(b.items) + 1)
	}
	if scope == domainbrowser.ScopeAllHosts {
		input.HostID = ""
	} else {
		input.HostID = hostID
	}
	next, err := domainbrowser.NormalizeBookmark(input, previous, b.now(), b.newID)
	if err != nil {
		b.mu.Unlock()
		return domainbrowser.Bookmark{}, err
	}

	items := make([]domainbrowser.Bookmark, 0, len(b.items)+1)
	if previousIndex >= 0 {
		items = append(items, b.items[:previousIndex]...)
		items = append(items, next)
		items = append(items, b.items[previousIndex+1:]...)
	} else {
		items = append(items, b.items...)
		items = append(items, next)
	}
	if err := b.saveErrorLocked(); err != nil {
		b.mu.Unlock()
		return domainbrowser.Bookmark{}, err
	}
	if err := b.store.Save(items); err != nil {
		b.mu.Unlock()
		return domainbrowser.Bookmark{}, err
	}
	b.items = items
	b.mu.Unlock()
	b.notify()
	return next, nil
}

// Remove deletes a bookmark on behalf of hostID. It reports false for an
// unknown id and refuses another Host's bookmark without an error.
func (b *Bookmarks) Remove(hostID model.HostID, id string) (bool, error) {
	b.mu.Lock()
	index := -1
	for i := range b.items {
		if b.items[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		b.mu.Unlock()
		return false, nil
	}
	if b.items[index].Scope == domainbrowser.ScopeHost && b.items[index].HostID != hostID {
		b.mu.Unlock()
		return false, nil
	}
	items := append([]domainbrowser.Bookmark(nil), b.items[:index]...)
	items = append(items, b.items[index+1:]...)
	if err := b.saveErrorLocked(); err != nil {
		b.mu.Unlock()
		return false, err
	}
	if err := b.store.Save(items); err != nil {
		b.mu.Unlock()
		return false, err
	}
	b.items = items
	b.mu.Unlock()
	b.notify()
	return true, nil
}

// saveErrorLocked reports why persistence is unavailable. It must be called
// with b.mu held.
func (b *Bookmarks) saveErrorLocked() error {
	if b.loadErr != nil {
		return fmt.Errorf("browser: bookmarks could not be loaded: %w", b.loadErr)
	}
	return nil
}

// Subscribe observes a committed change. The callback runs without the
// service lock held.
func (b *Bookmarks) Subscribe(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	b.muListen.Lock()
	id := b.nextID
	b.nextID++
	b.listens[id] = fn
	b.muListen.Unlock()
	return func() {
		b.muListen.Lock()
		delete(b.listens, id)
		b.muListen.Unlock()
	}
}

func (b *Bookmarks) notify() {
	b.muListen.Lock()
	listeners := make([]func(), 0, len(b.listens))
	for _, fn := range b.listens {
		listeners = append(listeners, fn)
	}
	b.muListen.Unlock()
	for _, fn := range listeners {
		fn()
	}
}
