package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/application/desktop"
)

func TestDesktopStoreLoadMissingAndMalformedUseDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-settings.json")
	store := OpenDesktop(path)

	settings, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Version != 0 {
		t.Fatalf("missing file = %+v", settings)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err = store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Version != 0 {
		t.Fatalf("malformed file = %+v", settings)
	}
}

func TestDesktopStoreSaveIsAtomicAndReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "desktop-settings.json")
	store := OpenDesktop(path)
	settings := desktop.Defaults("windows")
	settings.ShowTrayIcon = false
	settings.ToggleWindowHotkey = "Ctrl+Shift+Space"
	if err := store.Save(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 && runtime.GOOS != "windows" {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	loaded = desktop.Normalize(loaded, "windows")
	if loaded != settings {
		t.Fatalf("loaded = %+v, want %+v", loaded, settings)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

// TestDesktopStoreIgnoresTheLegacyHotkeySwitch pins the no-migration rule:
// an old record's globalHotkeyEnabled is ignored, a missing hotkeyMode loads
// as quake, SettingsVersion stays 1, and every unrelated desktop value is
// preserved on load and after a save drops the legacy key.
func TestDesktopStoreIgnoresTheLegacyHotkeySwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-settings.json")
	legacy := `{
  "version": 1,
  "showTrayIcon": false,
  "closeToTray": false,
  "globalHotkeyEnabled": false,
  "toggleWindowHotkey": "Ctrl+Shift+Space",
  "autoStart": false,
  "sshDeepLinks": false,
  "notifySessionEnded": false,
  "notifyConnectionFailed": true,
  "notifyForwardingFailed": false
}
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := OpenDesktop(path)
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings := desktop.Normalize(loaded, "windows")
	if settings.Version != desktop.SettingsVersion {
		t.Fatalf("version = %d, want %d", settings.Version, desktop.SettingsVersion)
	}
	if settings.HotkeyMode != desktop.HotkeyModeQuake {
		t.Fatalf("hotkey mode = %q, want the quake default", settings.HotkeyMode)
	}
	if settings.ShowTrayIcon || settings.CloseToTray || settings.SSHDeepLinks {
		t.Fatalf("unrelated desktop values changed: %+v", settings)
	}
	if settings.NotifySessionEnded || !settings.NotifyConnectionFailed || settings.NotifyForwardingFailed {
		t.Fatalf("notification values changed: %+v", settings)
	}
	if settings.ToggleWindowHotkey != "Ctrl+Shift+Space" {
		t.Fatalf("shortcut = %q", settings.ToggleWindowHotkey)
	}

	if err := store.Save(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "globalHotkeyEnabled") {
		t.Fatalf("the removed field was written back:\n%s", text)
	}
	if !strings.Contains(text, `"hotkeyMode": "quake"`) {
		t.Fatalf("the mode was not persisted:\n%s", text)
	}
}

// TestDesktopStoreQuakeSourceDefaultsAndRoundTrips pins the source field's
// no-migration rule: an old record without it loads as own, an explicit follow
// survives a save/load cycle, and an unknown stored value normalizes to own.
func TestDesktopStoreQuakeSourceDefaultsAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-settings.json")
	store := OpenDesktop(path)

	legacy := "{\"version\": 1, \"showTrayIcon\": true, \"toggleWindowHotkey\": \"Ctrl + `\"}"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := desktop.Normalize(loaded, "windows").QuakeSource; got != desktop.QuakeSourceOwn {
		t.Fatalf("legacy source = %q, want own", got)
	}

	settings := desktop.Defaults("windows")
	settings.QuakeSource = desktop.QuakeSourceFollow
	if err := store.Save(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := desktop.Normalize(loaded, "windows"); got != settings {
		t.Fatalf("round trip = %+v, want %+v", got, settings)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"quakeSource": "follow"`) {
		t.Fatalf("the source was not persisted:\n%s", data)
	}

	unknown := `{"version": 1, "quakeSource": "bogus", "showTrayIcon": true}`
	if err := os.WriteFile(path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := desktop.Normalize(loaded, "windows").QuakeSource; got != desktop.QuakeSourceOwn {
		t.Fatalf("unknown source = %q, want own", got)
	}
}

// TestDesktopStoreRoundTripsEveryHotkeyMode ensures explicit modes survive a
// save/load cycle, including none, without version migration.
func TestDesktopStoreRoundTripsEveryHotkeyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-settings.json")
	store := OpenDesktop(path)
	for _, mode := range []desktop.HotkeyMode{
		desktop.HotkeyModeNone, desktop.HotkeyModeToggle,
		desktop.HotkeyModeQuake, desktop.HotkeyModeTopdown,
	} {
		settings := desktop.Defaults("windows")
		settings.HotkeyMode = mode
		if err := store.Save(context.Background(), settings); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.Load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		loaded = desktop.Normalize(loaded, "windows")
		if loaded != settings {
			t.Fatalf("mode %q round trip = %+v, want %+v", mode, loaded, settings)
		}
	}
}
