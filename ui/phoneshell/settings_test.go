package phoneshell

import (
	"errors"
	"fmt"
	"testing"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// fakeSettings is the SettingsService the page tests drive. The real services
// and their shared-schema documents are covered by
// TestSettingsWritesTheSharedSchemaAndKeepsDeviceLocalFields and
// TestPortableSettingsTravelWithTheSharedAdapter.
type fakeSettings struct {
	prefs     domainpreferences.Settings
	term      domainterminal.Settings
	updateErr error
}

func newFakeSettings() *fakeSettings {
	return &fakeSettings{prefs: domainpreferences.Defaults(), term: domainterminal.Defaults()}
}

func (f *fakeSettings) Preferences() domainpreferences.Settings { return f.prefs }

func (f *fakeSettings) UpdatePreferences(change func(*domainpreferences.Settings)) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	change(&f.prefs)
	f.prefs = domainpreferences.Normalize(f.prefs)
	return nil
}

func (f *fakeSettings) Terminal() domainterminal.Settings { return f.term }

func (f *fakeSettings) UpdateTerminal(change func(*domainterminal.Settings)) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	change(&f.term)
	f.term = domainterminal.Normalize(f.term)
	return nil
}

// TestSettingsAppearanceAppliesImmediately covers the phone-relevant
// appearance controls: each tap writes through the seam on that click, and
// the page redraws the new value.
func TestSettingsAppearanceAppliesImmediately(t *testing.T) {
	settings := newFakeSettings()
	shell := New(WithSettings(settings))
	tt := openSectionPage(t, shell, Settings, "Appearance")

	if err := tt.Click("Theme: Dark"); err != nil {
		t.Fatalf("click Theme: Dark: %v", err)
	}
	if got := settings.prefs.Appearance.ThemeMode; got != domainpreferences.ThemeDark {
		t.Fatalf("ThemeMode = %q, want dark", got)
	}

	if err := tt.Click("Interface style: macOS"); err != nil {
		t.Fatalf("click Interface style: macOS: %v", err)
	}
	if got := settings.prefs.Appearance.InterfaceStyle; got != domainpreferences.InterfaceMacOS {
		t.Fatalf("InterfaceStyle = %q, want macos", got)
	}

	if err := tt.Click("App icon: Rainbow"); err != nil {
		t.Fatalf("click App icon: Rainbow: %v", err)
	}
	if got := settings.prefs.Appearance.AppIcon; got != "rainbow" {
		t.Fatalf("AppIcon = %q, want rainbow", got)
	}

	size := int(settings.prefs.Appearance.TerminalFontSize)
	if err := tt.Click("Increase Terminal font size"); err != nil {
		t.Fatalf("click font size increase: %v", err)
	}
	if got := int(settings.prefs.Appearance.TerminalFontSize); got != size+1 {
		t.Fatalf("TerminalFontSize = %d, want %d", got, size+1)
	}
	if want := fmt.Sprintf("Terminal font size: %d", size+1); !tt.HasText(want) {
		t.Fatalf("page does not show %q; texts: %v", want, texts(t, tt))
	}

	if err := tt.Click("Cursor: Bar"); err != nil {
		t.Fatalf("click Cursor: Bar: %v", err)
	}
	if got := settings.prefs.Appearance.TerminalCursorStyle; got != domainpreferences.CursorBar {
		t.Fatalf("TerminalCursorStyle = %q, want bar", got)
	}
}

// TestSettingsTerminalAppliesImmediately covers the remote clipboard policy
// the phone Session service reads.
func TestSettingsTerminalAppliesImmediately(t *testing.T) {
	settings := newFakeSettings()
	shell := New(WithSettings(settings))
	tt := openSectionPage(t, shell, Settings, "Terminal")

	if err := tt.Click("Remote clipboard: Off"); err != nil {
		t.Fatalf("click Remote clipboard: Off: %v", err)
	}
	if got := settings.term.OSC52; got != domainterminal.ClipboardOff {
		t.Fatalf("OSC52 = %q, want off", got)
	}
	if err := tt.Click("Remote clipboard: Read and write"); err != nil {
		t.Fatalf("click Remote clipboard: Read and write: %v", err)
	}
	if got := settings.term.OSC52; got != domainterminal.ClipboardReadWrite {
		t.Fatalf("OSC52 = %q, want read-write", got)
	}
}

// TestSettingsInputAppliesImmediately covers the paste controls: the phone's
// own Paste action reads them on every tap. The desktop's autocomplete
// controls are gone because the phone terminal has no suggestion surface.
func TestSettingsInputAppliesImmediately(t *testing.T) {
	settings := newFakeSettings()
	shell := New(WithSettings(settings))
	tt := openSectionPage(t, shell, Settings, "Input")

	for _, gone := range []string{"Autocomplete", "Inline suggestion", "Suggestion list"} {
		if tt.HasText(gone) {
			t.Fatalf("the Input page still offers %q, which the phone terminal does not consume", gone)
		}
	}

	wasProtected := settings.term.PasteProtection
	if err := tt.Click("Paste protection"); err != nil {
		t.Fatalf("click Paste protection: %v", err)
	}
	if got := settings.term.PasteProtection; got == wasProtected {
		t.Fatalf("PasteProtection did not change from %v", wasProtected)
	}

	lines := settings.term.PasteMinLines
	if err := tt.Click("Increase Paste warning after lines"); err != nil {
		t.Fatalf("click paste lines increase: %v", err)
	}
	if got := settings.term.PasteMinLines; got != lines+1 {
		t.Fatalf("PasteMinLines = %d, want %d", got, lines+1)
	}
}

// TestSettingsSecurityAppliesImmediately covers the shared Security group.
func TestSettingsSecurityAppliesImmediately(t *testing.T) {
	settings := newFakeSettings()
	shell := New(WithSettings(settings))
	tt := openSectionPage(t, shell, Settings, "Security")

	wasDestructive := settings.prefs.Security.ConfirmDestructive
	if err := tt.Click("Confirm destructive actions"); err != nil {
		t.Fatalf("click Confirm destructive actions: %v", err)
	}
	if got := settings.prefs.Security.ConfirmDestructive; got == wasDestructive {
		t.Fatalf("ConfirmDestructive did not change from %v", wasDestructive)
	}

	wasClose := settings.prefs.Security.ConfirmCloseSessions
	if err := tt.Click("Confirm closing sessions"); err != nil {
		t.Fatalf("click Confirm closing sessions: %v", err)
	}
	if got := settings.prefs.Security.ConfirmCloseSessions; got == wasClose {
		t.Fatalf("ConfirmCloseSessions did not change from %v", wasClose)
	}
}

// TestSettingsSaveErrorIsShown pins that a failed write is surfaced instead of
// leaving the page showing a value the app is not using.
func TestSettingsSaveErrorIsShown(t *testing.T) {
	settings := newFakeSettings()
	settings.updateErr = errors.New("disk full")
	shell := New(WithSettings(settings))
	tt := openSectionPage(t, shell, Settings, "Appearance")

	if err := tt.Click("Theme: Dark"); err != nil {
		t.Fatalf("click Theme: Dark: %v", err)
	}
	if !tt.HasText("Could not save the setting.") {
		t.Fatalf("save error is not shown; texts: %v", texts(t, tt))
	}
	if got := settings.prefs.Appearance.ThemeMode; got == domainpreferences.ThemeDark {
		t.Fatal("the page changed the value even though the save failed")
	}
}

// TestSettingsUnavailableState pins that a shell without the services says so
// rather than drawing an empty form.
func TestSettingsUnavailableState(t *testing.T) {
	shell := New()
	tt := openSectionPage(t, shell, Settings, "Appearance")
	if !tt.HasText("Settings are not available on this device.") {
		t.Fatalf("unavailable state is not shown; texts: %v", texts(t, tt))
	}
}
