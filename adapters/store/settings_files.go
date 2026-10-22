package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// KeyBindingsStore persists the keyboard binding document as a small
// versioned JSON file.
type KeyBindingsStore struct {
	path string
}

// OpenKeyBindings returns a store at path.
func OpenKeyBindings(path string) *KeyBindingsStore { return &KeyBindingsStore{path: path} }

type keyBindingsRecord struct {
	Version         int                         `json:"version"`
	Scheme          domainkeybindings.Scheme    `json:"scheme"`
	Overrides       domainkeybindings.Overrides `json:"overrides,omitempty"`
	SuppressedMenus []string                    `json:"suppressedMenus,omitempty"`
}

const keyBindingsVersion = 1

// Load returns the stored document. A missing file means no overrides; the
// service supplies the platform's default scheme.
func (s *KeyBindingsStore) Load(ctx context.Context) (appkeybindings.State, error) {
	if err := ctx.Err(); err != nil {
		return appkeybindings.State{}, err
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return appkeybindings.State{}, nil
	}
	if err != nil {
		return appkeybindings.State{}, fmt.Errorf("store: read key bindings: %w", err)
	}
	var record keyBindingsRecord
	if err := json.Unmarshal(data, &record); err != nil {
		// A malformed file must not stop the app from starting.
		return appkeybindings.State{}, nil
	}
	return appkeybindings.State{
		Scheme:          record.Scheme,
		Overrides:       record.Overrides,
		SuppressedMenus: record.SuppressedMenus,
	}, nil
}

// Save atomically replaces the document.
func (s *KeyBindingsStore) Save(ctx context.Context, state appkeybindings.State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := keyBindingsRecord{
		Version:         keyBindingsVersion,
		Scheme:          state.Scheme,
		Overrides:       state.Overrides,
		SuppressedMenus: state.SuppressedMenus,
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode key bindings: %w", err)
	}
	data = append(data, '\n')
	return writeSettingsFile(s.path, ".keybindings-*.tmp", data)
}

// PreferencesStore persists the typed preferences document.
type PreferencesStore struct {
	path string
}

// OpenPreferences returns a store at path.
func OpenPreferences(path string) *PreferencesStore { return &PreferencesStore{path: path} }

type preferencesRecord struct {
	Version int `json:"version"`
	// The groups are inlined so the file stays readable and a future field
	// is a plain addition.
	domainpreferences.Settings
}

const preferencesVersion = 1

// Load starts from the defaults and overlays the stored document, so a
// record written before a field existed keeps the intended default.
func (s *PreferencesStore) Load(ctx context.Context) (domainpreferences.Settings, error) {
	if err := ctx.Err(); err != nil {
		return domainpreferences.Defaults(), err
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return domainpreferences.Defaults(), nil
	}
	if err != nil {
		return domainpreferences.Defaults(), fmt.Errorf("store: read preferences: %w", err)
	}
	settings := domainpreferences.Defaults()
	record := preferencesRecord{Settings: settings}
	if err := json.Unmarshal(data, &record); err != nil {
		return settings, nil
	}
	// Unmarshal overlays present fields onto the defaults; absent fields
	// stay at their defaults.
	return domainpreferences.Normalize(record.Settings), nil
}

// Save atomically replaces the document.
func (s *PreferencesStore) Save(ctx context.Context, settings domainpreferences.Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := preferencesRecord{Version: preferencesVersion, Settings: settings}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode preferences: %w", err)
	}
	data = append(data, '\n')
	return writeSettingsFile(s.path, ".preferences-*.tmp", data)
}

// writeSettingsFile writes data through a synced temporary file and a rename,
// so an interrupted write cannot leave a truncated document.
func writeSettingsFile(path, pattern string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create settings directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("store: create temporary settings file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: set settings file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write settings file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: sync settings file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close settings file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("store: replace settings file: %w", err)
	}
	tmpPath = ""
	return nil
}
