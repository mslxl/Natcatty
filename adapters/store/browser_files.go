package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
)

// BrowserBookmarkStore persists the host browser's bookmark list as the
// original JSON array, allowing a profile written by the Electron build to be
// read where the record shape overlaps.
type BrowserBookmarkStore struct {
	path string
}

// OpenBrowserBookmarks returns a bookmark store at path.
func OpenBrowserBookmarks(path string) *BrowserBookmarkStore {
	return &BrowserBookmarkStore{path: path}
}

// Load returns the stored bookmarks. Missing or malformed data is an empty
// list, so browser start never depends on this file.
func (s *BrowserBookmarkStore) Load() ([]domainbrowser.Bookmark, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read browser bookmarks: %w", err)
	}
	var items []domainbrowser.Bookmark
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, nil
	}
	return items, nil
}

// Save atomically replaces the bookmark array.
func (s *BrowserBookmarkStore) Save(items []domainbrowser.Bookmark) error {
	if items == nil {
		items = []domainbrowser.Bookmark{}
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode browser bookmarks: %w", err)
	}
	data = append(data, '\n')
	return writeSettingsFile(s.path, ".browser-bookmarks-*.tmp", data)
}

// BrowserSiteIconStore persists the favicon cache.
type BrowserSiteIconStore struct {
	path string
}

// OpenBrowserSiteIcons returns a favicon store at path.
func OpenBrowserSiteIcons(path string) *BrowserSiteIconStore {
	return &BrowserSiteIconStore{path: path}
}

// Load returns the cached icons. Missing or malformed data is an empty list.
func (s *BrowserSiteIconStore) Load() ([]domainbrowser.SiteIcon, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read browser site icons: %w", err)
	}
	var items []domainbrowser.SiteIcon
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, nil
	}
	return items, nil
}

// Save atomically replaces the favicon cache.
func (s *BrowserSiteIconStore) Save(items []domainbrowser.SiteIcon) error {
	if items == nil {
		items = []domainbrowser.SiteIcon{}
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode browser site icons: %w", err)
	}
	data = append(data, '\n')
	return writeSettingsFile(s.path, ".browser-site-icons-*.tmp", data)
}
