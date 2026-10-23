package sftp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// Bookmarks persists the SFTP bookmark lists as two small JSON documents:
// global bookmarks shared by remote panes, and local-pane bookmarks kept on
// this device. Each file is replaced atomically, so a crash mid-write cannot
// leave a truncated list; a missing file is an empty list, while a malformed
// one is reported so the surface can tell the user instead of silently losing
// locations.
type Bookmarks struct {
	globalPath string
	localPath  string
}

// NewBookmarks returns a repository over the global and local bookmark files.
// An empty path makes that set memory-only.
func NewBookmarks(globalPath, localPath string) *Bookmarks {
	return &Bookmarks{globalPath: globalPath, localPath: localPath}
}

var _ apptransfer.BookmarkRepository = (*Bookmarks)(nil)

// LoadBookmarks reads the saved global list.
func (b *Bookmarks) LoadBookmarks(ctx context.Context) ([]domainsftp.Bookmark, error) {
	if b == nil {
		return nil, nil
	}
	return loadBookmarks(ctx, b.globalPath)
}

// SaveBookmarks replaces the saved global list.
func (b *Bookmarks) SaveBookmarks(ctx context.Context, items []domainsftp.Bookmark) error {
	if b == nil {
		return nil
	}
	return saveBookmarks(ctx, b.globalPath, items)
}

// LoadLocalBookmarks reads the saved local-pane list.
func (b *Bookmarks) LoadLocalBookmarks(ctx context.Context) ([]domainsftp.Bookmark, error) {
	if b == nil {
		return nil, nil
	}
	return loadBookmarks(ctx, b.localPath)
}

// SaveLocalBookmarks replaces the saved local-pane list.
func (b *Bookmarks) SaveLocalBookmarks(ctx context.Context, items []domainsftp.Bookmark) error {
	if b == nil {
		return nil
	}
	return saveBookmarks(ctx, b.localPath, items)
}

func loadBookmarks(ctx context.Context, path string) ([]domainsftp.Bookmark, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sftp bookmarks: read: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var items []domainsftp.Bookmark
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("sftp bookmarks: parse: %w", err)
	}
	return items, nil
}

// saveBookmarks replaces one list. The directory is created when needed; the
// file is written beside its destination and renamed over it.
func saveBookmarks(ctx context.Context, path string, items []domainsftp.Bookmark) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("sftp bookmarks: encode: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sftp bookmarks: directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".sftp-bookmarks-*.tmp")
	if err != nil {
		return fmt.Errorf("sftp bookmarks: temporary file: %w", err)
	}
	temp := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(temp)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("sftp bookmarks: permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("sftp bookmarks: write: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("sftp bookmarks: close: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("sftp bookmarks: replace: %w", err)
	}
	return nil
}
