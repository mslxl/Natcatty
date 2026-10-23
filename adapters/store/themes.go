package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

// ThemesStore persists imported terminal color schemes in a small versioned
// JSON file beside the other settings. Built-in schemes are never written.
type ThemesStore struct {
	path string
}

// OpenThemes returns a store at path.
func OpenThemes(path string) *ThemesStore { return &ThemesStore{path: path} }

type themesRecord struct {
	Version int                         `json:"version"`
	Themes  []domaintheme.TerminalTheme `json:"themes"`
}

// themesVersion is the current record version.
const themesVersion = 1

// Load returns the custom schemes. A missing file is empty; a malformed file
// also loads as empty so a corrupt import cannot stop the application.
func (s *ThemesStore) Load(ctx context.Context) ([]domaintheme.TerminalTheme, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read themes: %w", err)
	}
	var record themesRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, nil
	}
	if record.Version > themesVersion {
		// A future version may mean something else; do not guess.
		return nil, nil
	}
	return record.Themes, nil
}

// Save atomically replaces the custom theme document.
func (s *ThemesStore) Save(ctx context.Context, themes []domaintheme.TerminalTheme) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := themesRecord{Version: themesVersion, Themes: themes}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode themes: %w", err)
	}
	data = append(data, '\n')
	return writeSettingsFile(s.path, ".themes-*.tmp", data)
}
