package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	adaptersyncedsettings "github.com/mslxl/natcatty/adapters/syncedsettings"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsession "github.com/mslxl/natcatty/application/session"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// TestSettingsWritesTheSharedSchemaAndKeepsDeviceLocalFields drives the phone
// forms over the real preference and terminal-settings services: the values
// land in the same versioned JSON documents the desktop reads, survive a
// restart of the service, and the device-local fields are the ones
// domainpreferences.Portable strips from a synced document.
func TestSettingsWritesTheSharedSchemaAndKeepsDeviceLocalFields(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	preferencesPath := filepath.Join(dir, "preferences.json")
	terminalPath := filepath.Join(dir, "terminal-settings.json")

	preferences := apppreferences.NewService(store.OpenPreferences(preferencesPath))
	if err := preferences.Load(ctx); err != nil {
		t.Fatalf("load preferences: %v", err)
	}
	terminal := appterminalsettings.NewService(store.OpenTerminalSettings(terminalPath))
	if err := terminal.Load(ctx); err != nil {
		t.Fatalf("load terminal settings: %v", err)
	}

	shell := phoneshell.New(phoneshell.WithSettings(
		phoneshell.NewSettingsService(preferences, terminal)))
	tt := phonePage(t, shell, phoneshell.Settings, "Appearance")
	if err := tt.Click("Theme: Dark"); err != nil {
		t.Fatalf("click Theme: Dark: %v", err)
	}
	if err := tt.Click("Interface style: macOS"); err != nil {
		t.Fatalf("click Interface style: macOS: %v", err)
	}
	if err := tt.Click("App icon: Rainbow"); err != nil {
		t.Fatalf("click App icon: Rainbow: %v", err)
	}
	size := int(preferences.Settings().Appearance.TerminalFontSize)
	if err := tt.Click("Increase Terminal font size"); err != nil {
		t.Fatalf("click font size increase: %v", err)
	}
	if err := tt.Click("Back"); err != nil {
		t.Fatalf("click Back: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	if err := tt.Click("Input"); err != nil {
		t.Fatalf("click Input: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	if err := tt.Click("Paste protection"); err != nil {
		t.Fatalf("click Paste protection: %v", err)
	}

	// The services wrote the shared documents. A fresh service over the same
	// files reads the values back, which is what a phone restart does.
	reloaded := apppreferences.NewService(store.OpenPreferences(preferencesPath))
	if err := reloaded.Load(ctx); err != nil {
		t.Fatalf("reload preferences: %v", err)
	}
	got := reloaded.Settings().Appearance
	if got.ThemeMode != domainpreferences.ThemeDark {
		t.Fatalf("stored ThemeMode = %q, want dark", got.ThemeMode)
	}
	if got.InterfaceStyle != domainpreferences.InterfaceMacOS {
		t.Fatalf("stored InterfaceStyle = %q, want macos", got.InterfaceStyle)
	}
	if got.AppIcon != "rainbow" {
		t.Fatalf("stored AppIcon = %q, want rainbow", got.AppIcon)
	}
	if int(got.TerminalFontSize) != size+1 {
		t.Fatalf("stored TerminalFontSize = %v, want %d", got.TerminalFontSize, size+1)
	}
	reloadedTerminal := appterminalsettings.NewService(store.OpenTerminalSettings(terminalPath))
	if err := reloadedTerminal.Load(ctx); err != nil {
		t.Fatalf("reload terminal settings: %v", err)
	}
	if !reloadedTerminal.Settings().PasteProtection {
		t.Fatal("stored PasteProtection = false, want true")
	}

	// The device-local fields stay local; the portable document has the rest.
	portable := domainpreferences.Portable(reloaded.Settings())
	if portable.Appearance.ThemeMode != domainpreferences.ThemeDark {
		t.Fatalf("portable ThemeMode = %q, want dark", portable.Appearance.ThemeMode)
	}
	if portable.Appearance.InterfaceStyle != "" {
		t.Fatalf("portable InterfaceStyle = %q, want empty (device-local)", portable.Appearance.InterfaceStyle)
	}
	if portable.Appearance.AppIcon != "" {
		t.Fatalf("portable AppIcon = %q, want empty (device-local)", portable.Appearance.AppIcon)
	}
	if portable.Appearance.TerminalFontSize != float64(size+1) {
		t.Fatalf("portable TerminalFontSize = %v, want %d", portable.Appearance.TerminalFontSize, size+1)
	}
}

// TestPortableSettingsTravelWithTheSharedAdapter checks the other direction:
// the adapter the desktop's Vault sync uses reads the phone's services into
// the same namespaced documents, and applying a downloaded document changes
// the portable fields while leaving interface style and app icon alone.
func TestPortableSettingsTravelWithTheSharedAdapter(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	if err := preferences.Load(ctx); err != nil {
		t.Fatalf("load preferences: %v", err)
	}
	terminal := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		t.Fatalf("load terminal settings: %v", err)
	}
	if err := preferences.Update(func(s *domainpreferences.Settings) {
		s.Appearance.ThemeMode = domainpreferences.ThemeDark
		s.Appearance.InterfaceStyle = domainpreferences.InterfaceMacOS
		s.Appearance.AppIcon = "rainbow"
	}); err != nil {
		t.Fatalf("update preferences: %v", err)
	}
	if err := terminal.Update(func(s *domainterminal.Settings) {
		s.PasteProtection = true
		s.LogDirectory = "/phone/logs"
	}); err != nil {
		t.Fatalf("update terminal settings: %v", err)
	}

	adapter := adaptersyncedsettings.New(preferences, terminal, nil, nil, nil, nil)
	documents, err := adapter.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	var portable domainpreferences.PortableSettings
	if err := json.Unmarshal(documents[adaptersyncedsettings.KeyPreferences], &portable); err != nil {
		t.Fatalf("decode portable preferences: %v", err)
	}
	if portable.Appearance.ThemeMode != domainpreferences.ThemeDark {
		t.Fatalf("synced ThemeMode = %q, want dark", portable.Appearance.ThemeMode)
	}
	if portable.Appearance.InterfaceStyle != "" || portable.Appearance.AppIcon != "" {
		t.Fatalf("synced device-local fields were not stripped: %+v", portable.Appearance)
	}

	var syncedTerminal domainterminal.Settings
	if err := json.Unmarshal(documents[adaptersyncedsettings.KeyTerminal], &syncedTerminal); err != nil {
		t.Fatalf("decode portable terminal settings: %v", err)
	}
	if !syncedTerminal.PasteProtection {
		t.Fatal("synced PasteProtection = false, want true")
	}
	if syncedTerminal.LogDirectory != "" {
		t.Fatalf("synced LogDirectory = %q, want empty (device-local)", syncedTerminal.LogDirectory)
	}

	// A desktop document arrives: the portable theme changes, the phone keeps
	// its own interface style and icon.
	remote, err := json.Marshal(domainpreferences.PortableSettings{
		Appearance: domainpreferences.Appearance{ThemeMode: domainpreferences.ThemeLight},
	})
	if err != nil {
		t.Fatalf("encode remote preferences: %v", err)
	}
	if err := adapter.Apply(ctx, map[string]json.RawMessage{
		adaptersyncedsettings.KeyPreferences: remote,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	after := preferences.Settings().Appearance
	if after.ThemeMode != domainpreferences.ThemeLight {
		t.Fatalf("ThemeMode after apply = %q, want light", after.ThemeMode)
	}
	if after.InterfaceStyle != domainpreferences.InterfaceMacOS {
		t.Fatalf("InterfaceStyle after apply = %q, want the local macos", after.InterfaceStyle)
	}
	if after.AppIcon != "rainbow" {
		t.Fatalf("AppIcon after apply = %q, want the local rainbow", after.AppIcon)
	}
}

// TestPhoneSessionCarriesTheClipboardPolicy proves the terminal page's Remote
// clipboard control has an effect on the phone: the Session is built with the
// shared OSC 52 policy, and the change hook's SetClipboardPolicy reaches a
// Session that is already open.
func TestPhoneSessionCarriesTheClipboardPolicy(t *testing.T) {
	settings := domainterminal.Settings{OSC52: domainterminal.ClipboardOff}
	service := appsession.NewService(
		&wiringFactory{transport: &wiringTransport{closed: make(chan struct{})}},
		phoneSessionOptions(nil, nil, func() domainterminal.Settings { return settings })...)

	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, Label: "phone", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open Session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if got := session.ClipboardAccess(); got != domainterminal.ClipboardOff {
		t.Fatalf("opened with clipboard policy %q, want off", got)
	}

	// What the phone's terminal-settings change hook does.
	service.SetClipboardPolicy(domainterminal.ClipboardReadWrite)
	if got := session.ClipboardAccess(); got != domainterminal.ClipboardReadWrite {
		t.Fatalf("live Session clipboard policy = %q, want read-write", got)
	}
}
