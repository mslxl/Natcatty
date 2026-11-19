package preferences

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDefaultsMatchTheOriginal(t *testing.T) {
	got := Defaults()
	if got.Appearance.ThemeMode != ThemeSystem || got.Appearance.WindowOpacity != 1 ||
		got.Appearance.TerminalFontSize != 13 || got.Appearance.TerminalFontFamily != DefaultTerminalFont ||
		got.Appearance.LightTheme != "snow" || got.Appearance.DarkTheme != "midnight" ||
		got.Appearance.AppIcon != "original" || got.Appearance.AccentMode != AccentTheme ||
		got.Appearance.TerminalCursorStyle != CursorBlock || got.Appearance.TerminalCursorSteady {
		t.Errorf("appearance defaults = %+v", got.Appearance)
	}
	if !got.Layout.ShowHostTreeSidebar || !got.Layout.ShowSFTPTab || !got.Layout.ShowRecentHosts ||
		got.Layout.ShellOnlyTabNumberShortcuts || !got.Layout.ShowTabNumberBadges ||
		got.Layout.TabBarPosition != TabBarTop {
		t.Errorf("layout defaults = %+v", got.Layout)
	}
	if got.SFTPEditor.DoubleClickBehavior != DoubleClickOpen || !got.SFTPEditor.UseCompressedUpload ||
		got.SFTPEditor.AutoSync || got.SFTPEditor.ShowHiddenFiles || got.SFTPEditor.DefaultViewMode != ViewList {
		t.Errorf("sftp defaults = %+v", got.SFTPEditor)
	}
	if got.Logs.SSHDebug || got.Sync.Enabled || !got.Security.ConfirmCloseSessions {
		t.Errorf("logs/sync/security defaults = %+v %+v %+v", got.Logs, got.Sync, got.Security)
	}
}

// TestSessionRestoreIsOptIn covers the deliberate divergence from the
// original, which restored the previous session by default.
func TestSessionRestoreIsOptIn(t *testing.T) {
	got := Defaults()
	if got.Layout.RestorePreviousSession {
		t.Error("session restore is on by default")
	}
	if got.Layout.StartupLanding != StartupVault {
		t.Errorf("startup landing = %q, want %q", got.Layout.StartupLanding, StartupVault)
	}
}

// TestGlobalDefaultProxyIsNormalizedPortableAndMergeable covers the app-wide
// proxy preference: a saved profile id is trimmed, travels with synced
// settings, and an empty value means no default.
func TestGlobalDefaultProxyIsNormalizedPortableAndMergeable(t *testing.T) {
	settings := Defaults()
	if got := settings.DefaultProxyProfileID(); got != "" {
		t.Fatalf("default proxy = %q, want none", got)
	}

	settings.Proxy.DefaultProfileID = "  proxy-1  "
	normalized := Normalize(settings)
	if got := normalized.DefaultProxyProfileID(); got != "proxy-1" {
		t.Fatalf("normalized default proxy = %q, want proxy-1", got)
	}

	portable := Portable(normalized)
	if portable.Proxy.DefaultProfileID != "proxy-1" {
		t.Fatalf("Portable dropped the default proxy: %+v", portable.Proxy)
	}
	remote := portable
	remote.Proxy.DefaultProfileID = "proxy-2"
	merged := MergePortable(normalized, remote)
	if got := merged.DefaultProxyProfileID(); got != "proxy-2" {
		t.Fatalf("MergePortable default proxy = %q, want proxy-2", got)
	}

	off := portable
	off.Proxy.DefaultProfileID = ""
	if got := MergePortable(normalized, off).DefaultProxyProfileID(); got != "" {
		t.Fatalf("turning the default off left %q", got)
	}
}

func TestNormalizeClampsAndValidates(t *testing.T) {
	settings := Settings{
		Appearance: Appearance{
			ThemeMode:           "neon",
			LightTheme:          "midnight",
			DarkTheme:           "snow",
			AccentMode:          "neon",
			AppIcon:             "neon",
			WindowOpacity:       2,
			TerminalFontSize:    100,
			TerminalFontFamily:  "",
			TerminalCursorStyle: "neon",
		},
		Layout: Layout{
			TabBarPosition: "middle",
			StartupLanding: "moon",
			SidePanelDock:  "ceiling",
		},
		SFTPEditor: SFTPEditor{DoubleClickBehavior: "explode", DefaultViewMode: "grid", EditorFontSize: 1},
		Sync:       Sync{ConflictStrategy: "coinflip"},
	}
	got := Normalize(settings)
	if got.Appearance.ThemeMode != ThemeSystem || got.Appearance.AccentMode != AccentTheme {
		t.Errorf("appearance enums = %+v", got.Appearance)
	}
	if got.Appearance.WindowOpacity != 1 || got.Appearance.TerminalFontSize != 32 ||
		got.Appearance.TerminalFontFamily != DefaultTerminalFont {
		t.Errorf("appearance values = %+v", got.Appearance)
	}
	if got.Appearance.LightTheme != "snow" || got.Appearance.DarkTheme != "midnight" ||
		got.Appearance.AppIcon != "original" ||
		got.Appearance.TerminalCursorStyle != DefaultCursorStyle {
		t.Errorf("appearance theme enums = %+v", got.Appearance)
	}
	if got.Layout.TabBarPosition != TabBarTop || got.Layout.StartupLanding != StartupVault ||
		got.Layout.SidePanelDock != DockLeft {
		t.Errorf("layout enums = %+v", got.Layout)
	}
	if got.SFTPEditor.DoubleClickBehavior != DoubleClickOpen || got.SFTPEditor.DefaultViewMode != ViewList ||
		got.SFTPEditor.EditorFontSize != 8 {
		t.Errorf("sftp values = %+v", got.SFTPEditor)
	}
	if got.Sync.ConflictStrategy != SyncNewest {
		t.Errorf("sync strategy = %q", got.Sync.ConflictStrategy)
	}
}

func TestNormalizeKeepsEveryKnownStartupLanding(t *testing.T) {
	for _, landing := range []StartupLanding{
		StartupVault, StartupLast, StartupTerminal, StartupMonitoring,
	} {
		settings := Defaults()
		settings.Layout.StartupLanding = landing
		if got := Normalize(settings).Layout.StartupLanding; got != landing {
			t.Errorf("Normalize(%q) = %q, want %q", landing, got, landing)
		}
	}
}

func TestClampWindowOpacity(t *testing.T) {
	cases := map[float64]float64{-1: 0.5, 0.25: 0.5, 0.8: 0.8, 3: 1}
	for input, want := range cases {
		if got := ClampWindowOpacity(input); got != want {
			t.Errorf("ClampWindowOpacity(%v) = %v, want %v", input, got, want)
		}
	}
}

func TestResetGroupLeavesOtherGroupsAlone(t *testing.T) {
	settings := Defaults()
	settings.Appearance.WindowOpacity = 0.7
	settings.Layout.ShowTabNumberBadges = false
	reset := ResetGroup(settings, GroupAppearance)
	if reset.Appearance.WindowOpacity != 1 {
		t.Errorf("appearance was not reset: %v", reset.Appearance.WindowOpacity)
	}
	if reset.Layout.ShowTabNumberBadges {
		t.Error("resetting appearance touched layout")
	}
}

func TestResetFieldRestoresOnlyThatField(t *testing.T) {
	settings := Defaults()
	settings.Appearance.WindowOpacity = 0.7
	settings.Appearance.TerminalFontSize = 20
	settings.Layout.StartupLanding = StartupLast
	settings.Sync.Enabled = true

	reset := ResetField(settings, FieldTerminalFontSize)
	if reset.Appearance.TerminalFontSize != DefaultTerminalFontSize {
		t.Errorf("font size = %v", reset.Appearance.TerminalFontSize)
	}
	if reset.Appearance.WindowOpacity != 0.7 || reset.Layout.StartupLanding != StartupLast ||
		!reset.Sync.Enabled {
		t.Errorf("another field was reset: %+v", reset)
	}
}

func TestCursorFieldsResetIndependently(t *testing.T) {
	settings := Defaults()
	settings.Appearance.TerminalCursorStyle = CursorBar
	settings.Appearance.TerminalCursorSteady = true

	reset := ResetField(settings, FieldTerminalCursorBlink)
	if reset.Appearance.TerminalCursorSteady {
		t.Error("the blinking default was not restored")
	}
	if reset.Appearance.TerminalCursorStyle != CursorBar {
		t.Errorf("cursor style = %q, want it left alone", reset.Appearance.TerminalCursorStyle)
	}

	reset = ResetField(reset, FieldTerminalCursorStyle)
	if reset.Appearance.TerminalCursorStyle != DefaultCursorStyle {
		t.Errorf("cursor style = %q, want %q", reset.Appearance.TerminalCursorStyle, DefaultCursorStyle)
	}
	if reset.Appearance.TerminalCursorSteady {
		t.Error("resetting the style touched the blink setting")
	}
}

// TestShiftEnterDefaultsToEscCR pins the default: Shift+Enter sends ESC CR
// (the legacy Alt+Enter encoding). The field is stored inverted, so a settings
// document written before it existed keeps that default.
func TestShiftEnterDefaultsToEscCR(t *testing.T) {
	if got := Defaults().Appearance.TerminalShiftEnterPlain; got {
		t.Error("Shift+Enter leaves the key to the terminal by default")
	}
	var older Appearance
	if older.TerminalShiftEnterPlain {
		t.Error("an appearance document without the field asks for the plain encoding")
	}
	if group, ok := GroupOf(FieldTerminalShiftEnter); !ok || group != GroupAppearance {
		t.Errorf("GroupOf(%q) = %q, %v", FieldTerminalShiftEnter, group, ok)
	}
}

func TestShiftEnterPreferenceResetsAlone(t *testing.T) {
	settings := Defaults()
	settings.Appearance.TerminalShiftEnterPlain = true
	settings.Appearance.TerminalCursorSteady = true

	reset := ResetField(settings, FieldTerminalShiftEnter)
	if reset.Appearance.TerminalShiftEnterPlain {
		t.Error("the ESC CR default was not restored")
	}
	if !reset.Appearance.TerminalCursorSteady {
		t.Error("resetting Shift+Enter touched the cursor setting")
	}
}

func TestEveryFieldBelongsToAGroup(t *testing.T) {
	seen := make(map[Field]bool)
	for _, group := range Groups() {
		if group == "" {
			t.Fatal("empty group")
		}
	}
	for _, field := range Fields() {
		if seen[field] {
			t.Errorf("field %q is listed twice", field)
		}
		seen[field] = true
		group, ok := GroupOf(field)
		if !ok {
			t.Errorf("field %q has no group", field)
			continue
		}
		found := false
		for _, candidate := range Groups() {
			if candidate == group {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("field %q has unknown group %q", field, group)
		}
	}
	if len(seen) != 52 {
		t.Errorf("fields = %d, want 52", len(seen))
	}
}

// TestLocalShellPreferenceRoundTrips proves the path and its arguments survive
// the stored document, and that the device-local value follows the local
// machine rather than a synced one.
func TestLocalShellPreferenceRoundTrips(t *testing.T) {
	settings := Defaults()
	settings.Appearance.LocalShell = LocalShell{
		Path: "/opt/custom shell/bin/zsh",
		Args: `-l "arg with space" "quote\"inside"`,
	}

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Settings
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if got := Normalize(loaded).Appearance.LocalShell; !reflect.DeepEqual(got, settings.Appearance.LocalShell) {
		t.Fatalf("round-trip LocalShell = %+v, want %+v", got, settings.Appearance.LocalShell)
	}

	portable := Portable(settings)
	if !reflect.DeepEqual(portable.Appearance.LocalShell, LocalShell{}) {
		t.Fatalf("portable LocalShell = %+v, want it stripped", portable.Appearance.LocalShell)
	}
	current := Defaults()
	current.Appearance.LocalShell = LocalShell{Path: `C:\Program Files\PowerShell\7\pwsh.exe`}
	merged := MergePortable(current, portable)
	if merged.Appearance.LocalShell.Path != current.Appearance.LocalShell.Path {
		t.Fatalf("merged LocalShell = %+v, want the local value", merged.Appearance.LocalShell)
	}
}

// TestLocalShellKeepsAnArgumentDraftBeforeItsPath proves arguments typed
// before the path survive every save. Normalization used to drop the whole
// LocalShell when Path was empty, so the Settings editor erased the draft on
// the next keystroke.
func TestLocalShellKeepsAnArgumentDraftBeforeItsPath(t *testing.T) {
	settings := Defaults()
	settings.Appearance.LocalShell = LocalShell{Args: `-l --color=always "a b"`}

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Settings
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	normalized := Normalize(loaded)
	if got := normalized.Appearance.LocalShell.Args; got != settings.Appearance.LocalShell.Args {
		t.Fatalf("argument draft = %q after a save, want %q", got, settings.Appearance.LocalShell.Args)
	}
	// The draft is inert until a path exists: the OS login shell still starts.
	if command, fallback := normalized.Appearance.LocalShell.Command(nil); command != nil || fallback {
		t.Fatalf("empty-path Command = %v/%v, want the OS login shell", command, fallback)
	}
	// Setting the path later starts the shell with the preserved arguments.
	normalized.Appearance.LocalShell.Path = "/opt/zsh"
	if command, fallback := normalized.Appearance.LocalShell.Command(nil); fallback ||
		!reflect.DeepEqual(command, []string{"/opt/zsh", "-l", "--color=always", "a b"}) {
		t.Fatalf("Command = %v/%v, want the path with the preserved arguments", command, fallback)
	}
}

func TestLocalShellCommandFallsBackWhenThePathIsMissing(t *testing.T) {
	shell := LocalShell{Path: " /opt/zsh ", Args: `-l --flag "a b"`}
	command, fallback := shell.Command(func(path string) bool { return path == "/opt/zsh" })
	if fallback || !reflect.DeepEqual(command, []string{"/opt/zsh", "-l", "--flag", "a b"}) {
		t.Fatalf("Command = %v/%v, want the configured shell", command, fallback)
	}

	command, fallback = shell.Command(func(string) bool { return false })
	if !fallback || command != nil {
		t.Fatalf("missing path Command = %v/%v, want a fallback with no command", command, fallback)
	}

	// An empty path asks for the OS login shell and is not a fallback.
	if command, fallback = (LocalShell{Args: "-l"}).Command(nil); command != nil || fallback {
		t.Fatalf("empty path Command = %v/%v", command, fallback)
	}
}

func TestLocalShellArgumentsParseAndFormat(t *testing.T) {
	args := []string{"-l", "arg with space", `quote"inside`, `back\slash`, "", "plain"}
	formatted := FormatArgs(args)
	parsed := ParseArgs(formatted)
	if !reflect.DeepEqual(parsed, args) {
		t.Fatalf("ParseArgs(FormatArgs(%q)) = %q", args, parsed)
	}
	if got := ParseArgs(`--flag 'a b' "c \"d\""`); !reflect.DeepEqual(got,
		[]string{"--flag", "a b", `c "d"`}) {
		t.Fatalf("ParseArgs = %q", got)
	}
}

func TestAppearanceThemeFieldsResetIndependently(t *testing.T) {
	settings := Defaults()
	settings.Appearance.LightTheme = "ivory"
	settings.Appearance.DarkTheme = "obsidian"
	settings.Appearance.AppIcon = "rainbow"

	reset := ResetField(settings, FieldLightTheme)
	if reset.Appearance.LightTheme != "snow" {
		t.Errorf("light theme = %q", reset.Appearance.LightTheme)
	}
	if reset.Appearance.DarkTheme != "obsidian" || reset.Appearance.AppIcon != "rainbow" {
		t.Errorf("another appearance field was reset: %+v", reset.Appearance)
	}
	reset = ResetField(settings, FieldDarkTheme)
	if reset.Appearance.DarkTheme != "midnight" || reset.Appearance.LightTheme != "ivory" {
		t.Errorf("dark reset = %+v", reset.Appearance)
	}
	reset = ResetField(settings, FieldAppIcon)
	if reset.Appearance.AppIcon != "original" {
		t.Errorf("icon reset = %q", reset.Appearance.AppIcon)
	}
}

func TestNormalizeKeepsValidThemeIDs(t *testing.T) {
	settings := Defaults()
	settings.Appearance.LightTheme = "ivory"
	settings.Appearance.DarkTheme = "obsidian"
	settings.Appearance.AppIcon = "white-rose"
	if got := Normalize(settings); !Equal(got, settings) {
		t.Errorf("valid theme fields changed: %+v", got.Appearance)
	}
}

func TestResetFieldIgnoresAnUnknownField(t *testing.T) {
	settings := Defaults()
	settings.Appearance.WindowOpacity = 0.75
	if got := ResetField(settings, Field("missing")); !Equal(got, settings) {
		t.Errorf("ResetField(unknown) = %+v", got)
	}
}

func TestPortableStripsDeviceLocalPreferences(t *testing.T) {
	settings := Defaults()
	settings.Appearance.AppIcon = "device-icon"
	settings.Appearance.TerminalFontSize = 18
	settings.SFTPEditor.DefaultOpener = Opener{Kind: OpenerExternal, Application: ExternalApplication{Path: "/opt/editor"}}
	settings.SFTPEditor.FileAssociations = []FileAssociation{{Extension: "txt", Kind: OpenerExternal, Application: ExternalApplication{Path: "/opt/viewer"}}}
	settings.Security.ConfirmDestructive = false

	portable := Portable(settings)
	if portable.Appearance.AppIcon != "" {
		t.Fatalf("portable AppIcon = %q, want it stripped", portable.Appearance.AppIcon)
	}
	if portable.Appearance.TerminalFontSize != 18 {
		t.Fatalf("portable TerminalFontSize = %v", portable.Appearance.TerminalFontSize)
	}
	if portable.SFTPEditor.DefaultOpener.Kind != "" || len(portable.SFTPEditor.FileAssociations) != 0 {
		t.Fatalf("portable SFTP editor = %+v", portable.SFTPEditor)
	}
	if portable.Security.ConfirmDestructive {
		t.Fatal("portable security settings did not follow the source")
	}

	current := Defaults()
	current.Appearance.AppIcon = "bright"
	current.SFTPEditor.DefaultOpener = Opener{Kind: OpenerSystem}
	current.SFTPEditor.FileAssociations = []FileAssociation{{Extension: "md", Kind: OpenerExternal, Application: ExternalApplication{Path: "/local/app"}}}
	merged := MergePortable(current, portable)
	if merged.Appearance.AppIcon != "bright" {
		t.Fatalf("merged AppIcon = %q, want the local value", merged.Appearance.AppIcon)
	}
	if merged.Appearance.TerminalFontSize != 18 {
		t.Fatalf("merged TerminalFontSize = %v", merged.Appearance.TerminalFontSize)
	}
	if merged.SFTPEditor.DefaultOpener.Kind != OpenerSystem ||
		len(merged.SFTPEditor.FileAssociations) != 1 {
		t.Fatalf("merged SFTP editor = %+v, want device-local fields preserved", merged.SFTPEditor)
	}
	if merged.Security.ConfirmDestructive {
		t.Fatal("merged security settings did not take the portable value")
	}
	if merged.Sync != current.Sync {
		t.Fatalf("merged Sync = %+v, want the device-local value", merged.Sync)
	}
}
