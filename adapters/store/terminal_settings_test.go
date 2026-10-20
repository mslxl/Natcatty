package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

func TestTerminalSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal-settings.json")
	store := OpenTerminalSettings(path)
	want := domainterminal.Settings{
		LoggingEnabled:   true,
		LogDirectory:     `C:\logs`,
		LogFormat:        domainterminal.LogFormatHTML,
		LogTimestamps:    true,
		OSC52:            domainterminal.ClipboardPrompt,
		OSCNotifications: domainterminal.NotificationUnfocused,
		Encoding:         domainterminal.EncodingGB18030,
		Encodings:        map[string]domainterminal.Encoding{"host:abc": domainterminal.EncodingShiftJIS},
		PasteProtection:  true,
		PasteMinLines:    5,
		DropAction:       domainterminal.DropUpload,

		AutocompleteEnabled:        false,
		AutocompleteGhostText:      true,
		AutocompletePopupMenu:      false,
		AutocompleteDebounceMs:     250,
		AutocompleteMinChars:       2,
		AutocompleteMaxSuggestions: 20,
		AutocompleteHistoryScope:   domainterminal.AutocompleteHistoryGlobal,
		KeywordHighlightEnabled:    false,
		KeywordHighlightRules: []domainterminal.KeywordHighlightRule{
			{ID: "custom", Patterns: []string{`boom`}, Color: "#123456", Enabled: true},
		},
		ShowSessionStats: false,
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded = %+v, want %+v", got, want)
	}
}

func TestTerminalSettingsMissingFileIsTheOriginalDefaults(t *testing.T) {
	store := OpenTerminalSettings(filepath.Join(t.TempDir(), "missing.json"))
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, domainterminal.Defaults()) {
		t.Fatalf("loaded = %+v, want defaults", got)
	}
}

func TestTerminalSettingsVersion2GetsThePowerUxDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal-settings.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"logFormat":"raw"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := OpenTerminalSettings(path).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutocompleteEnabled || !got.AutocompletePopupMenu ||
		got.AutocompleteHistoryScope != domainterminal.AutocompleteHistoryHost ||
		!got.KeywordHighlightEnabled || len(got.KeywordHighlightRules) != 7 ||
		!got.ShowSessionStats {
		t.Fatalf("version 2 settings = %+v, want the power-UX defaults", got)
	}
}

func TestTerminalSettingsSaveLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	store := OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json"))
	if err := store.Save(context.Background(), domainterminal.Defaults()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "terminal-settings.json" {
		t.Fatalf("directory entries = %v", entries)
	}
}
