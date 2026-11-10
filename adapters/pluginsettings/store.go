// Package pluginsettings owns host-side, schema-validated plugin
// configuration under the version-independent plugin data directory. Values
// are written as TOML beside a plugin's private storage and are never visible
// to the guest: the host hands the guest only the effective configuration at
// activation.
package pluginsettings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"

	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// FileName is the configuration file inside a plugin's data directory.
const FileName = "settings.toml"

var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Store implements application/plugin.ConfigStore.
type Store struct {
	dataDir func(pluginID string) string
}

var _ appplugin.ConfigStore = (*Store)(nil)

// Open returns a store that writes one settings.toml per plugin below the
// directory the function returns.
func Open(dataDir func(pluginID string) string) *Store {
	return &Store{dataDir: dataDir}
}

// Load implements ConfigStore. A missing file is an empty configuration.
func (s *Store) Load(_ context.Context, pluginID string) (map[string]any, error) {
	path, err := s.path(pluginID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("plugin settings: read %q: %w", path, err)
	}
	values := make(map[string]any)
	if err := toml.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("plugin settings: decode %q: %w", path, err)
	}
	return values, nil
}

// Save implements ConfigStore with an atomic replace.
func (s *Store) Save(_ context.Context, pluginID string, values map[string]any) error {
	path, err := s.path(pluginID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("plugin settings: create directory: %w", err)
	}
	var buffer bytes.Buffer
	if err := toml.NewEncoder(&buffer).Encode(values); err != nil {
		return fmt.Errorf("plugin settings: encode: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("plugin settings: create temporary file: %w", err)
	}
	name := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("plugin settings: set permissions: %w", err)
	}
	if _, err := temp.Write(buffer.Bytes()); err != nil {
		temp.Close()
		return fmt.Errorf("plugin settings: write: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("plugin settings: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("plugin settings: close: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("plugin settings: commit: %w", err)
	}
	cleanup = false
	return nil
}

func (s *Store) path(pluginID string) (string, error) {
	if s == nil || s.dataDir == nil {
		return "", errors.New("plugin settings: data directory is not configured")
	}
	if !pluginIDPattern.MatchString(pluginID) || filepath.Base(pluginID) != pluginID {
		return "", fmt.Errorf("plugin settings: invalid plugin id %q", pluginID)
	}
	return filepath.Join(s.dataDir(pluginID), FileName), nil
}
