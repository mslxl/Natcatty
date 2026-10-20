package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mslxl/natcatty/application/autocomplete"
)

// CommandHistoryStore persists recorded command history for autocomplete as a
// small JSON file beside the terminal settings. It is safe from any goroutine.
type CommandHistoryStore struct {
	path string
	mu   sync.Mutex
}

var _ autocomplete.Store = (*CommandHistoryStore)(nil)

// OpenCommandHistory returns a store at path. The file need not exist yet.
func OpenCommandHistory(path string) *CommandHistoryStore {
	return &CommandHistoryStore{path: path}
}

type commandHistoryRecord struct {
	Version int                  `json:"version"`
	Entries []autocomplete.Entry `json:"entries"`
}

// commandHistoryVersion is the current record version.
const commandHistoryVersion = 1

// Load returns the stored entries. A missing or malformed file is an empty
// history, not an error: autocomplete must keep working after a corrupt write.
func (s *CommandHistoryStore) Load(ctx context.Context) ([]autocomplete.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read command history: %w", err)
	}
	var record commandHistoryRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, nil
	}
	if len(record.Entries) == 0 {
		return nil, nil
	}
	return record.Entries, nil
}

// Save atomically replaces the history file with mode 0600.
func (s *CommandHistoryStore) Save(ctx context.Context, entries []autocomplete.Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := commandHistoryRecord{Version: commandHistoryVersion, Entries: entries}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode command history: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create command history directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".command-history-*.tmp")
	if err != nil {
		return fmt.Errorf("store: create temporary command history: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporaryPath != "" {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("store: set command history mode: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("store: write command history: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("store: sync command history: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("store: close command history: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("store: replace command history: %w", err)
	}
	temporaryPath = ""
	return nil
}
