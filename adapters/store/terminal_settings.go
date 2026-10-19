package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// TerminalSettingsStore persists the terminal behavior group as a small
// versioned JSON file beside the desktop settings. Keeping it out of the
// Vault database means a corrupt settings file cannot affect connections.
type TerminalSettingsStore struct {
	path string
}

// OpenTerminalSettings returns a store at path.
func OpenTerminalSettings(path string) *TerminalSettingsStore {
	return &TerminalSettingsStore{path: path}
}

type terminalSettingsRecord struct {
	Version          int                             `json:"version"`
	LoggingEnabled   bool                            `json:"loggingEnabled"`
	LogDirectory     string                          `json:"logDirectory"`
	LogFormat        domainterminal.LogFormat        `json:"logFormat"`
	LogTimestamps    bool                            `json:"logTimestamps"`
	OSC52            domainterminal.ClipboardAccess  `json:"osc52"`
	OSCNotifications domainterminal.NotificationMode `json:"oscNotifications"`
}

// Load returns the stored settings. A missing file is the original defaults,
// not an error.
func (s *TerminalSettingsStore) Load(context.Context) (domainterminal.Settings, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return domainterminal.Defaults(), nil
	}
	if err != nil {
		return domainterminal.Defaults(), err
	}
	var record terminalSettingsRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return domainterminal.Defaults(), err
	}
	return domainterminal.Normalize(domainterminal.Settings{
		LoggingEnabled:   record.LoggingEnabled,
		LogDirectory:     record.LogDirectory,
		LogFormat:        record.LogFormat,
		LogTimestamps:    record.LogTimestamps,
		OSC52:            record.OSC52,
		OSCNotifications: record.OSCNotifications,
	}), nil
}

// Save writes the settings atomically so an interrupted write cannot leave a
// truncated file behind.
func (s *TerminalSettingsStore) Save(_ context.Context, settings domainterminal.Settings) error {
	record := terminalSettingsRecord{
		Version:          1,
		LoggingEnabled:   settings.LoggingEnabled,
		LogDirectory:     settings.LogDirectory,
		LogFormat:        settings.LogFormat,
		LogTimestamps:    settings.LogTimestamps,
		OSC52:            settings.OSC52,
		OSCNotifications: settings.OSCNotifications,
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".terminal-settings-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, s.path)
}
