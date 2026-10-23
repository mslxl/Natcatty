package transfer

import (
	"strings"

	"github.com/mslxl/natcatty/domain/model"
)

// Bookmark is a remembered directory. A remote Bookmark keeps the Host it
// belongs to; a local Bookmark has no HostID. Global bookmarks are persisted
// and shared between panes; local-pane bookmarks are persisted on this device
// and offered only to local panes.
type Bookmark struct {
	Label     string       `json:"label"`
	Path      string       `json:"path"`
	HostID    model.HostID `json:"hostId,omitempty"`
	HostLabel string       `json:"hostLabel,omitempty"`
}

// Key identifies a bookmark's location: its Host and path, not its label.
// Labels are display text and may be renamed independently.
func (b Bookmark) Key() string {
	return string(b.HostID) + "\x00" + cleanKeyPath(b.Path)
}

func cleanKeyPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	// A trailing separator never distinguishes two directories for the
	// purpose of "is this location already bookmarked".
	for len(path) > 1 && (strings.HasSuffix(path, "/") || strings.HasSuffix(path, `\`)) {
		path = path[:len(path)-1]
	}
	return path
}

// BookmarkSet is an ordered bookmark list with stable, duplicate-free keys.
// The zero value is empty.
type BookmarkSet struct {
	items []Bookmark
}

// NewBookmarkSet returns a set over a copy of items. Entries with an empty
// path or a repeated key are dropped, keeping the first occurrence.
func NewBookmarkSet(items []Bookmark) BookmarkSet {
	set := BookmarkSet{}
	for _, item := range items {
		set.Add(item)
	}
	return set
}

// Items returns a copy of the bookmarks in display order.
func (s BookmarkSet) Items() []Bookmark {
	return append([]Bookmark(nil), s.items...)
}

// Len reports how many bookmarks the set holds.
func (s BookmarkSet) Len() int { return len(s.items) }

// Contains reports whether the location is bookmarked.
func (s BookmarkSet) Contains(bookmark Bookmark) bool {
	_, ok := s.Find(bookmark.Key())
	return ok
}

// Find returns the bookmark with the location's key.
func (s BookmarkSet) Find(key string) (Bookmark, bool) {
	for _, item := range s.items {
		if item.Key() == key {
			return item, true
		}
	}
	return Bookmark{}, false
}

// Add appends a bookmark, or replaces the label of an existing one with the
// same key. An empty path is ignored. It reports whether the set changed.
func (s *BookmarkSet) Add(bookmark Bookmark) bool {
	if strings.TrimSpace(bookmark.Path) == "" {
		return false
	}
	if bookmark.Label == "" {
		bookmark.Label = bookmark.Path
	}
	key := bookmark.Key()
	for i, item := range s.items {
		if item.Key() == key {
			if item == bookmark {
				return false
			}
			s.items[i] = bookmark
			return true
		}
	}
	s.items = append(s.items, bookmark)
	return true
}

// Remove deletes the bookmark at the key. It reports whether the set changed.
func (s *BookmarkSet) Remove(key string) bool {
	for i, item := range s.items {
		if item.Key() == key {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return true
		}
	}
	return false
}

// Toggle adds the bookmark when absent and removes it when present. It
// reports whether the location is bookmarked afterwards.
func (s *BookmarkSet) Toggle(bookmark Bookmark) bool {
	if s.Contains(bookmark) {
		s.Remove(bookmark.Key())
		return false
	}
	s.Add(bookmark)
	return true
}
