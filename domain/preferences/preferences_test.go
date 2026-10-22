package preferences

import "testing"

func TestDefaultsMatchTheOriginal(t *testing.T) {
	got := Defaults()
	if got.Appearance.ThemeMode != ThemeSystem || got.Appearance.WindowOpacity != 1 ||
		got.Appearance.TerminalFontSize != 13 || got.Appearance.TerminalFontFamily != "menlo" {
		t.Errorf("appearance defaults = %+v", got.Appearance)
	}
	if !got.Layout.ShowHostTreeSidebar || !got.Layout.ShowSFTPTab || !got.Layout.ShowRecentHosts ||
		got.Layout.ShellOnlyTabNumberShortcuts || !got.Layout.ShowTabNumberBadges ||
		!got.Layout.RestorePreviousSession || got.Layout.TabBarPosition != TabBarTop {
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

func TestNormalizeClampsAndValidates(t *testing.T) {
	settings := Settings{
		Appearance: Appearance{
			ThemeMode:          "neon",
			AccentMode:         "neon",
			WindowOpacity:      2,
			TerminalFontSize:   100,
			TerminalFontFamily: "",
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
		got.Appearance.TerminalFontFamily != "menlo" {
		t.Errorf("appearance values = %+v", got.Appearance)
	}
	if got.Layout.TabBarPosition != TabBarTop || got.Layout.StartupLanding != StartupVault ||
		got.Layout.SidePanelDock != DockRight {
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
	if len(seen) != 33 {
		t.Errorf("fields = %d, want 33", len(seen))
	}
}

func TestResetFieldIgnoresAnUnknownField(t *testing.T) {
	settings := Defaults()
	settings.Appearance.WindowOpacity = 0.75
	if got := ResetField(settings, Field("missing")); got != settings {
		t.Errorf("ResetField(unknown) = %+v", got)
	}
}
