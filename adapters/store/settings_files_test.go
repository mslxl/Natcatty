package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
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

// TestPreferencesStoreUpgradesTerminalFollowing keeps the original's upgrade:
// a profile that predates following keeps a scheme its user picked, one still
// on the default follows the app, and a stored answer is kept as written.
func TestPreferencesStoreUpgradesTerminalFollowing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store := OpenPreferences(path)
	cases := []struct {
		document string
		want     bool
	}{
		{`{"version":1,"appearance":{"terminalTheme":"dracula"}}`, false},
		{`{"version":1,"appearance":{"terminalTheme":"netcatty-dark"}}`, true},
		{`{"version":1,"layout":{"showSftpTab":false}}`, true},
		{`{"version":1,"appearance":{"terminalTheme":"dracula","followAppTerminalTheme":true}}`, true},
		{`{"version":1,"appearance":{"followAppTerminalTheme":false}}`, false},
	}
	for _, c := range cases {
		if err := os.WriteFile(path, []byte(c.document), 0o600); err != nil {
			t.Fatal(err)
		}
		settings, err := store.Load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if settings.Appearance.FollowAppTerminalTheme != c.want {
			t.Errorf("%s: following = %v, want %v", c.document, settings.Appearance.FollowAppTerminalTheme, c.want)
		}
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
	if settings.Layout.RestorePreviousSession {
		t.Error("a missing preferences file enabled session restore")
	}

	loadDocument := func(document string) domainpreferences.Settings {
		t.Helper()
		if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		settings, err := store.Load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return settings
	}

	// An older or hand-edited document must keep the defaults for every
	// field it does not mention.
	settings = loadDocument(`{"version":1,"layout":{"showSftpTab":false}}`)
	if settings.Layout.ShowSFTPTab {
		t.Error("showSftpTab override was not loaded")
	}
	if !settings.Layout.ShowHostTreeSidebar || settings.Layout.RestorePreviousSession {
		t.Errorf("absent booleans lost their defaults: %+v", settings.Layout)
	}
	if settings.Appearance.TerminalFontSize != domainpreferences.DefaultTerminalFontSize {
		t.Errorf("absent appearance lost its default: %+v", settings.Appearance)
	}

	// Restore is opt-in: an explicit true in the file is honored, and a file
	// that omits the field keeps the off default (asserted above).
	settings = loadDocument(`{"version":1,"layout":{"restorePreviousSession":true}}`)
	if !settings.Layout.RestorePreviousSession {
		t.Error("explicit restorePreviousSession true was not loaded")
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

// postSwitchPolicy is the release policy after tickets 27 and 32 are green.
// It is constructed in tests only; production flips transport.ReleasePolicy.
func postSwitchPolicy() transportmodel.Policy {
	return transportmodel.Policy{
		Default: transportmodel.InProcess,
		VerifiedGates: []transportmodel.Gate{
			transportmodel.GateMoshParity, transportmodel.GateETParity,
		},
	}
}

// fixedClock keeps waiver expiry and the release default deterministic.
func fixedClock() func() time.Time {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

// TestTransportDefaultSwitchMigratesEachStoredCase is the table-driven
// migration contract: new installs and installs that never chose adopt the
// new default, an explicit bundled choice survives the flip, and old Hosts
// never need to be re-saved because their empty override inherits the resolved
// global value.
func TestTransportDefaultSwitchMigratesEachStoredCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	ctx := context.Background()
	writeDocument := func(document string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	loadPostSwitch := func() domainpreferences.Settings {
		t.Helper()
		store := OpenPreferences(path,
			WithReleasePolicy(postSwitchPolicy()), WithClock(fixedClock()))
		settings, err := store.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return settings
	}

	cases := []struct {
		name         string
		document     string // empty means no file, a fresh install
		want         transportmodel.Implementation
		wantExplicit bool
	}{
		{
			name:         "new install gets the configured default",
			want:         transportmodel.InProcess,
			wantExplicit: false,
		},
		{
			name:         "old document without the field adopts the new default",
			document:     `{"version":1,"layout":{"showSftpTab":false}}`,
			want:         transportmodel.InProcess,
			wantExplicit: false,
		},
		{
			name:         "old document without the implementation key adopts the new default",
			document:     `{"version":1,"transport":{"moshPredictionDisplay":"always"}}`,
			want:         transportmodel.InProcess,
			wantExplicit: false,
		},
		{
			name:         "old document with an empty implementation adopts the new default",
			document:     `{"version":1,"transport":{"implementation":""}}`,
			want:         transportmodel.InProcess,
			wantExplicit: false,
		},
		{
			name:         "old document holding the old default adopts the new default",
			document:     `{"version":1,"transport":{"implementation":"bundled"}}`,
			want:         transportmodel.InProcess,
			wantExplicit: false,
		},
		{
			name:         "old document that opted into the beta keeps its choice",
			document:     `{"version":1,"transport":{"implementation":"in_process"}}`,
			want:         transportmodel.InProcess,
			wantExplicit: true,
		},
		{
			name:         "explicit bundled choice survives the switch",
			document:     `{"version":2,"transport":{"implementation":"bundled","implementationExplicit":true}}`,
			want:         transportmodel.Bundled,
			wantExplicit: true,
		},
		{
			name:         "recorded non-choice follows the switch",
			document:     `{"version":2,"transport":{"implementation":"bundled","implementationExplicit":false}}`,
			want:         transportmodel.InProcess,
			wantExplicit: false,
		},
		{
			name:         "unknown value from another build fails closed and is kept",
			document:     `{"version":1,"transport":{"implementation":"bundled_v2"}}`,
			want:         transportmodel.Implementation("bundled_v2"),
			wantExplicit: true,
		},
		{
			name:         "malformed document falls back to the configured default",
			document:     `{not json`,
			want:         transportmodel.InProcess,
			wantExplicit: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_ = os.Remove(path)
			if c.document != "" {
				writeDocument(c.document)
			}
			settings := loadPostSwitch()
			if got := settings.Transport.Implementation; got != c.want {
				t.Fatalf("implementation = %q, want %q", got, c.want)
			}
			if got := settings.Transport.ImplementationExplicit; got != c.wantExplicit {
				t.Fatalf("explicit = %v, want %v", got, c.wantExplicit)
			}
		})
	}
}

// TestTransportDefaultSwitchSettingsFileRoundTrip proves a saved document
// always records the provenance flag, so a non-choice follows a later default
// flip while an explicit bundled choice does not. It also proves the file
// version is the new schema.
func TestTransportDefaultSwitchSettingsFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	ctx := context.Background()

	first := apppreferences.NewService(OpenPreferences(path))
	if err := first.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Update(func(settings *domainpreferences.Settings) {
		settings.Transport.Implementation = transportmodel.InProcess
		settings.Transport.ImplementationExplicit = true
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"version": 2`, `"implementationExplicit": true`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("saved document has no %s:\n%s", want, data)
		}
	}

	// The beta opt-in survives the switch.
	second := apppreferences.NewService(OpenPreferences(path,
		WithReleasePolicy(postSwitchPolicy()), WithClock(fixedClock())))
	if err := second.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := second.Settings().Transport.Implementation; got != transportmodel.InProcess {
		t.Fatalf("beta choice after switch = %q, want in_process", got)
	}

	// A user who locks the bundled client keeps it even after the flip.
	if err := second.Update(func(settings *domainpreferences.Settings) {
		settings.Transport.Implementation = transportmodel.Bundled
		settings.Transport.ImplementationExplicit = true
	}); err != nil {
		t.Fatal(err)
	}
	third := apppreferences.NewService(OpenPreferences(path,
		WithReleasePolicy(postSwitchPolicy()), WithClock(fixedClock())))
	if err := third.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := third.Settings().Transport.Implementation; got != transportmodel.Bundled {
		t.Fatalf("explicit bundled after switch = %q, want bundled", got)
	}
	if !third.Settings().Transport.ImplementationExplicit {
		t.Fatal("the explicit bundled choice lost its provenance")
	}

	// Selecting the release default clears provenance and follows the switch.
	if err := third.Update(func(settings *domainpreferences.Settings) {
		settings.Transport.Implementation = transportmodel.CurrentDefault()
		settings.Transport.ImplementationExplicit = false
	}); err != nil {
		t.Fatal(err)
	}
	fourth := apppreferences.NewService(OpenPreferences(path,
		WithReleasePolicy(postSwitchPolicy()), WithClock(fixedClock())))
	if err := fourth.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fourth.Settings().Transport.Implementation; got != transportmodel.InProcess {
		t.Fatalf("release-default choice after switch = %q, want in_process", got)
	}
	if fourth.Settings().Transport.ImplementationExplicit {
		t.Fatal("selecting the release default kept the explicit-choice flag")
	}
}

// TestTransportWaiverExpiresWithoutADataMigration proves the store resolves a
// platform waiver on every load, so an expiry takes effect by itself and no
// settings or Host record has to be rewritten.
func TestTransportWaiverExpiresWithoutADataMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	ctx := context.Background()
	expires := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	policy := postSwitchPolicy()
	policy.Waivers = []transportmodel.Waiver{{
		Platform: transportmodel.CurrentPlatform(),
		Reason:   transportmodel.WaiverParityBlocker,
		Gate:     transportmodel.GateETParity,
		Detail:   "ET parity summary 1234 fails on this platform",
		Expires:  expires,
	}}
	if err := policy.Validate(); err != nil {
		t.Fatalf("policy: %v", err)
	}

	loadAt := func(at time.Time) domainpreferences.Settings {
		t.Helper()
		store := OpenPreferences(path, WithReleasePolicy(policy), WithClock(func() time.Time { return at }))
		settings, err := store.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return settings
	}

	if got := loadAt(expires.Add(-time.Hour)).Transport.Implementation; got != transportmodel.Bundled {
		t.Fatalf("during the waiver = %q, want bundled", got)
	}
	if got := loadAt(expires).Transport.Implementation; got != transportmodel.InProcess {
		t.Fatalf("after the waiver = %q, want in_process", got)
	}
}

// TestTransportImplementationSelectionSurvivesRestart proves the global Mosh
// and EternalTerminal choice is saved and read back by a new service over the
// same preferences file. Expectations follow the release default, so the
// documented one-place flip does not break this test.
func TestTransportImplementationSelectionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	ctx := context.Background()
	wantDefault := transportmodel.CurrentDefault()

	first := apppreferences.NewService(OpenPreferences(path))
	if err := first.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := first.Settings().Transport.Implementation; got != wantDefault {
		t.Fatalf("default implementation = %q, want %q", got, wantDefault)
	}
	if err := first.Update(func(settings *domainpreferences.Settings) {
		settings.Transport.ChooseImplementation(transportmodel.InProcess)
	}); err != nil {
		t.Fatal(err)
	}

	second := apppreferences.NewService(OpenPreferences(path))
	if err := second.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := second.Settings().Transport.Implementation; got != transportmodel.InProcess {
		t.Fatalf("implementation after restart = %q, want in_process", got)
	}

	// A document written before the field existed must keep working under the
	// release default rather than failing to open Mosh and ET Hosts.
	if err := os.WriteFile(path, []byte(`{"version":1,"layout":{"showSftpTab":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := apppreferences.NewService(OpenPreferences(path))
	if err := legacy.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := legacy.Settings().Transport.Implementation; got != wantDefault {
		t.Fatalf("legacy document implementation = %q, want %q", got, wantDefault)
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
