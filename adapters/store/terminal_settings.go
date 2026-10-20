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
	Version          int                                `json:"version"`
	LoggingEnabled   bool                               `json:"loggingEnabled"`
	LogDirectory     string                             `json:"logDirectory"`
	LogFormat        domainterminal.LogFormat           `json:"logFormat"`
	LogTimestamps    bool                               `json:"logTimestamps"`
	OSC52            domainterminal.ClipboardAccess     `json:"osc52"`
	OSCNotifications domainterminal.NotificationMode    `json:"oscNotifications"`
	Encoding         domainterminal.Encoding            `json:"encoding"`
	Encodings        map[string]domainterminal.Encoding `json:"encodings,omitempty"`
	PasteProtection  bool                               `json:"pasteProtection"`
	PasteMinLines    int                                `json:"pasteMinLines"`
	DropAction       domainterminal.DropAction          `json:"dropAction"`

	AutocompleteEnabled        bool                                    `json:"autocompleteEnabled"`
	AutocompleteGhostText      bool                                    `json:"autocompleteGhostText"`
	AutocompletePopupMenu      bool                                    `json:"autocompletePopupMenu"`
	AutocompleteDebounceMs     int                                     `json:"autocompleteDebounceMs"`
	AutocompleteMinChars       int                                     `json:"autocompleteMinChars"`
	AutocompleteMaxSuggestions int                                     `json:"autocompleteMaxSuggestions"`
	AutocompleteHistoryScope   domainterminal.AutocompleteHistoryScope `json:"autocompleteHistoryScope"`
	KeywordHighlightEnabled    bool                                    `json:"keywordHighlightEnabled"`
	KeywordHighlightRules      []domainterminal.KeywordHighlightRule   `json:"keywordHighlightRules,omitempty"`
	ShowSessionStats           bool                                    `json:"showSessionStats"`
}

// terminalSettingsVersion is the current record version. Version 1 files
// lack the encoding, paste, and drop fields; version 2 files lack the
// autocomplete, keyword highlight, and statistics fields. Zero values for a
// missing group are replaced by the original defaults on load.
const terminalSettingsVersion = 3

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
	settings := domainterminal.Defaults()
	settings.LoggingEnabled = record.LoggingEnabled
	settings.LogDirectory = record.LogDirectory
	settings.LogFormat = record.LogFormat
	settings.LogTimestamps = record.LogTimestamps
	settings.OSC52 = record.OSC52
	settings.OSCNotifications = record.OSCNotifications
	settings.Encoding = record.Encoding
	settings.Encodings = record.Encodings
	settings.PasteProtection = record.PasteProtection
	settings.PasteMinLines = record.PasteMinLines
	settings.DropAction = record.DropAction
	if record.Version >= 3 {
		// Version 2 files have no power-UX group. Leaving the defaults in
		// place keeps autocomplete, highlighting, and statistics on for them.
		settings.AutocompleteEnabled = record.AutocompleteEnabled
		settings.AutocompleteGhostText = record.AutocompleteGhostText
		settings.AutocompletePopupMenu = record.AutocompletePopupMenu
		settings.AutocompleteDebounceMs = record.AutocompleteDebounceMs
		settings.AutocompleteMinChars = record.AutocompleteMinChars
		settings.AutocompleteMaxSuggestions = record.AutocompleteMaxSuggestions
		settings.AutocompleteHistoryScope = record.AutocompleteHistoryScope
		settings.KeywordHighlightEnabled = record.KeywordHighlightEnabled
		settings.KeywordHighlightRules = record.KeywordHighlightRules
		settings.ShowSessionStats = record.ShowSessionStats
	}
	return domainterminal.Normalize(settings), nil
}

// Save writes the settings atomically so an interrupted write cannot leave a
// truncated file behind.
func (s *TerminalSettingsStore) Save(_ context.Context, settings domainterminal.Settings) error {
	record := terminalSettingsRecord{
		Version:          terminalSettingsVersion,
		LoggingEnabled:   settings.LoggingEnabled,
		LogDirectory:     settings.LogDirectory,
		LogFormat:        settings.LogFormat,
		LogTimestamps:    settings.LogTimestamps,
		OSC52:            settings.OSC52,
		OSCNotifications: settings.OSCNotifications,
		Encoding:         settings.Encoding,
		Encodings:        settings.Encodings,
		PasteProtection:  settings.PasteProtection,
		PasteMinLines:    settings.PasteMinLines,
		DropAction:       settings.DropAction,

		AutocompleteEnabled:        settings.AutocompleteEnabled,
		AutocompleteGhostText:      settings.AutocompleteGhostText,
		AutocompletePopupMenu:      settings.AutocompletePopupMenu,
		AutocompleteDebounceMs:     settings.AutocompleteDebounceMs,
		AutocompleteMinChars:       settings.AutocompleteMinChars,
		AutocompleteMaxSuggestions: settings.AutocompleteMaxSuggestions,
		AutocompleteHistoryScope:   settings.AutocompleteHistoryScope,
		KeywordHighlightEnabled:    settings.KeywordHighlightEnabled,
		KeywordHighlightRules:      settings.KeywordHighlightRules,
		ShowSessionStats:           settings.ShowSessionStats,
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
