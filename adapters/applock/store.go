// Package applock stores App Lock settings in a JSON file beside the Vault,
// as the original app did. It never stores the App Lock password, only the
// versioned PBKDF2 verifier. Writes are serialized and atomic.
package applock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	appapplock "github.com/mslxl/natcatty/application/applock"
	domainapplock "github.com/mslxl/natcatty/domain/applock"
)

// Store is one App Lock settings file. It is safe from any goroutine.
type Store struct {
	path string
	mu   sync.Mutex
}

var _ appapplock.Repository = (*Store)(nil)

// Open returns a Store for path. The file need not exist yet.
func Open(path string) *Store { return &Store{path: path} }

// Load reads and normalizes the settings. A missing file means defaults; a
// malformed file is also treated as defaults, matching the original app's
// fail-safe behavior.
func (s *Store) Load(ctx context.Context) (domainapplock.Settings, error) {
	if err := ctx.Err(); err != nil {
		return domainapplock.Settings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return domainapplock.DefaultSettings(), nil
	}
	if err != nil {
		return domainapplock.Settings{}, fmt.Errorf("applock: read settings: %w", err)
	}

	var settings domainapplock.Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return domainapplock.DefaultSettings(), nil
	}
	return domainapplock.NormalizeSettings(settings), nil
}

// Save normalizes the settings and atomically replaces the file. A unique
// temporary file is written, synced, and renamed, so a crash cannot leave a
// truncated record.
func (s *Store) Save(ctx context.Context, settings domainapplock.Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(domainapplock.NormalizeSettings(settings), "", "  ")
	if err != nil {
		return fmt.Errorf("applock: encode settings: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("applock: create settings directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".app-lock-*.tmp")
	if err != nil {
		return fmt.Errorf("applock: create temporary settings: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("applock: set settings mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("applock: write settings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("applock: sync settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("applock: close settings: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("applock: replace settings: %w", err)
	}
	tmpPath = ""
	return nil
}
