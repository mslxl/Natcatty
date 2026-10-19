package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mslxl/natcatty/application/desktop"
)

// DesktopStore is one desktop settings file. It is safe from any goroutine.
type DesktopStore struct {
	path string
	mu   sync.Mutex
}

var _ desktop.Repository = (*DesktopStore)(nil)

// OpenDesktop returns a DesktopStore for path. The file need not exist yet.
func OpenDesktop(path string) *DesktopStore { return &DesktopStore{path: path} }

// Load reads the settings record. A missing file, or a malformed one, returns
// the zero record; the application Normalize turns that into defaults. A read
// failure other than a missing file is returned so the caller can surface it.
func (s *DesktopStore) Load(ctx context.Context) (desktop.Settings, error) {
	if err := ctx.Err(); err != nil {
		return desktop.Settings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return desktop.Settings{}, nil
	}
	if err != nil {
		return desktop.Settings{}, fmt.Errorf("store: read desktop settings: %w", err)
	}
	var settings desktop.Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return desktop.Settings{}, nil
	}
	return settings, nil
}

// Save atomically replaces the settings file with mode 0600. A unique
// temporary file in the same directory is written, synced, and renamed, so a
// crash cannot leave a truncated record.
func (s *DesktopStore) Save(ctx context.Context, settings desktop.Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode desktop settings: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create desktop settings directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".desktop-settings-*.tmp")
	if err != nil {
		return fmt.Errorf("store: create temporary desktop settings: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: set desktop settings mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write desktop settings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: sync desktop settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close desktop settings: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("store: replace desktop settings: %w", err)
	}
	tmpPath = ""
	return nil
}
