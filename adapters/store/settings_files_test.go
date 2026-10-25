package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

func TestKeyBindingsStoreRoundTripsAndToleratesBadFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	store := OpenKeyBindings(path)

	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Scheme != "" || len(state.Overrides) != 0 {
		t.Fatalf("missing file = %+v", state)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state, err = store.Load(context.Background()); err != nil || len(state.Overrides) != 0 {
		t.Fatalf("malformed file = %+v, err = %v", state, err)
	}

	want := appkeybindings.State{
		Scheme: domainkeybindings.SchemePC,
		Overrides: domainkeybindings.Overrides{
			string(domainkeybindings.SchemePC): {"open-local": "Ctrl + Shift + L"},
		},
		SuppressedMenus: []string{"menu.quit"},
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Scheme != want.Scheme || got.Overrides[string(domainkeybindings.SchemePC)]["open-local"] != "Ctrl + Shift + L" {
		t.Fatalf("loaded = %+v, want %+v", got, want)
	}
	if len(got.SuppressedMenus) != 1 || got.SuppressedMenus[0] != "menu.quit" {
		t.Fatalf("suppressed = %v", got.SuppressedMenus)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestPreferencesStoreOverlaysPartialFilesOnDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store := OpenPreferences(path)

	settings, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !domainpreferences.Equal(settings, domainpreferences.Defaults()) {
		t.Fatalf("missing file = %+v", settings)
	}

	// An older or hand-edited document must keep the defaults for every
	// field it does not mention.
	if err := os.WriteFile(path, []byte(`{"version":1,"layout":{"showSftpTab":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err = store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Layout.ShowSFTPTab {
		t.Error("showSftpTab override was not loaded")
	}
	if !settings.Layout.ShowHostTreeSidebar || !settings.Layout.RestorePreviousSession {
		t.Errorf("absent booleans lost their defaults: %+v", settings.Layout)
	}
	if settings.Appearance.TerminalFontSize != domainpreferences.DefaultTerminalFontSize {
		t.Errorf("absent appearance lost its default: %+v", settings.Appearance)
	}

	if err := store.Save(context.Background(), domainpreferences.Defaults()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !domainpreferences.Equal(loaded, domainpreferences.Defaults()) {
		t.Fatalf("round trip = %+v", loaded)
	}
}

// TestSFTPTransferSettingsSurviveRestart proves the three transfer settings,
// including compressed upload's default-on state, are saved and read back by a
// new service over the same preferences file after a restart.
func TestSFTPTransferSettingsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	ctx := context.Background()

	first := apppreferences.NewService(OpenPreferences(path))
	if err := first.Load(ctx); err != nil {
		t.Fatal(err)
	}
	defaults := first.Settings().SFTPEditor
	if !defaults.UseCompressedUpload {
		t.Errorf("compressed upload is not on by default: %+v", defaults)
	}
	if defaults.AutoSync || defaults.FollowTerminalCwd {
		t.Errorf("auto-sync and follow-cwd are not off by default: %+v", defaults)
	}

	if err := first.Update(func(settings *domainpreferences.Settings) {
		settings.SFTPEditor.UseCompressedUpload = false
		settings.SFTPEditor.AutoSync = true
		settings.SFTPEditor.FollowTerminalCwd = true
	}); err != nil {
		t.Fatal(err)
	}

	second := apppreferences.NewService(OpenPreferences(path))
	if err := second.Load(ctx); err != nil {
		t.Fatal(err)
	}
	got := second.Settings().SFTPEditor
	if got.UseCompressedUpload || !got.AutoSync || !got.FollowTerminalCwd {
		t.Fatalf("SFTP transfer settings after restart = %+v", got)
	}
}

// TestAppearanceSelectionSurvivesRestart proves the Appearance document is
// saved before it is published and reads back as the same selection after a
// new service over the same file starts.
func TestAppearanceSelectionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	ctx := context.Background()

	first := apppreferences.NewService(OpenPreferences(path))
	if err := first.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Update(func(settings *domainpreferences.Settings) {
		appearance := &settings.Appearance
		appearance.ThemeMode = domainpreferences.ThemeDark
		appearance.LightTheme = "ivory"
		appearance.DarkTheme = "vscode"
		appearance.AccentMode = domainpreferences.AccentCustom
		appearance.AccentColor = "48 96% 53%"
		appearance.AppIcon = "rainbow"
		appearance.WindowOpacity = 0.7
		appearance.TerminalTheme = "custom-imported"
	}); err != nil {
		t.Fatal(err)
	}

	second := apppreferences.NewService(OpenPreferences(path))
	if err := second.Load(ctx); err != nil {
		t.Fatal(err)
	}
	got := second.Settings().Appearance
	if got.ThemeMode != domainpreferences.ThemeDark || got.LightTheme != "ivory" ||
		got.DarkTheme != "vscode" || got.AccentMode != domainpreferences.AccentCustom ||
		got.AccentColor != "48 96% 53%" || got.AppIcon != "rainbow" ||
		got.WindowOpacity != 0.7 || got.TerminalTheme != "custom-imported" {
		t.Fatalf("appearance after restart = %+v", got)
	}
}
