package phoneshell

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	"github.com/mslxl/natcatty/ui/terminal"
)

func newShortcutService(t *testing.T) *appkeybindings.Service {
	t.Helper()
	service := appkeybindings.NewService(nil, appkeybindings.WithGOOS("windows"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("load keybindings: %v", err)
	}
	return service
}

// TestShortcutsPageRecordsExternalKeyboardBinding proves the page writes a
// recorded external-keyboard chord into the shared binding document on the
// frame that records it.
func TestShortcutsPageRecordsExternalKeyboardBinding(t *testing.T) {
	settings := newFakeSettings()
	service := newShortcutService(t)
	shell := New(WithSettings(settings), WithShortcuts(service))
	tt := openSectionPage(t, shell, Settings, "Shortcuts")

	if err := tt.Click("Copy from Terminal shortcut"); err != nil {
		t.Fatalf("select Copy: %v", err)
	}
	// Tapping a binding starts recording; the recorder takes focus on the
	// frame after that tap.
	tt.Frame()
	tt.TypeKey(ui.Ctrl|ui.Shift, ui.KeyY, "y")

	if got := service.Chord("copy", domainkeybindings.SchemePC); got != "Ctrl + Shift + Y" {
		t.Fatalf("copy chord = %q, want Ctrl + Shift + Y", got)
	}
	if !service.IsOverridden("copy", domainkeybindings.SchemePC) {
		t.Fatal("the recorded chord was not persisted as an override")
	}
}

// TestShortcutsPageSavesTypedChord covers the touch-keyboard path: the catalog
// notation is validated and saved through the same service.
func TestShortcutsPageSavesTypedChord(t *testing.T) {
	settings := newFakeSettings()
	service := newShortcutService(t)
	shell := New(WithSettings(settings), WithShortcuts(service))
	tt := openSectionPage(t, shell, Settings, "Shortcuts")

	if err := tt.Click("Paste to Terminal shortcut"); err != nil {
		t.Fatalf("select Paste: %v", err)
	}
	if err := tt.Click("Type chord"); err != nil {
		t.Fatalf("switch to typed entry: %v", err)
	}
	if err := tt.Click("Shortcut chord"); err != nil {
		t.Fatalf("focus the chord field: %v", err)
	}
	tt.Frame()
	tt.Type("Ctrl+Alt+P")
	if err := tt.Click("Save chord"); err != nil {
		t.Fatalf("save chord: %v", err)
	}
	if got := service.Chord("paste", domainkeybindings.SchemePC); got != "Ctrl+Alt+P" {
		t.Fatalf("paste chord = %q, want Ctrl+Alt+P", got)
	}
	if shell.settings.problem != "" {
		t.Fatalf("inline problem = %q, want none", shell.settings.problem)
	}
}

// TestShortcutsPageRejectsInvalidChordWithoutSaving proves an invalid value is
// rejected with an inline reason and the previous binding is untouched.
func TestShortcutsPageRejectsInvalidChordWithoutSaving(t *testing.T) {
	settings := newFakeSettings()
	service := newShortcutService(t)
	shell := New(WithSettings(settings), WithShortcuts(service))
	tt := openSectionPage(t, shell, Settings, "Shortcuts")

	before := service.Chord("copy", domainkeybindings.SchemePC)
	if err := tt.Click("Copy from Terminal shortcut"); err != nil {
		t.Fatalf("select Copy: %v", err)
	}
	if err := tt.Click("Type chord"); err != nil {
		t.Fatalf("switch to typed entry: %v", err)
	}
	if err := tt.Click("Shortcut chord"); err != nil {
		t.Fatalf("focus the chord field: %v", err)
	}
	tt.Frame()
	tt.Type("Ctrl+")
	if err := tt.Click("Save chord"); err != nil {
		t.Fatalf("save invalid chord: %v", err)
	}
	if got := service.Chord("copy", domainkeybindings.SchemePC); got != before {
		t.Fatalf("copy chord after the rejected save = %q, want %q", got, before)
	}
	if service.IsOverridden("copy", domainkeybindings.SchemePC) {
		t.Fatal("the rejected chord was written as an override")
	}
	if !strings.Contains(shell.settings.problem, "not a valid shortcut") {
		t.Fatalf("inline problem = %q, want an invalid-chord reason", shell.settings.problem)
	}
}

// TestTerminalKeybindsKeepReservedKeysAndFollowOverrides proves the Shortcuts
// page's table leaves reserved keys to the terminal: an application binding on
// Ctrl+C is not installed into the terminal, while a moved terminal binding
// replaces its old chord. The Shell's appearance provider exposes the same
// table to the next terminal it builds, which is how the page reaches the
// Runtime.
func TestTerminalKeybindsKeepReservedKeysAndFollowOverrides(t *testing.T) {
	settings := newFakeSettings()
	service := newShortcutService(t)
	// Free Ctrl+C from the catalog's SFTP copy, then bind an application
	// action to it. The terminal must still keep Ctrl+C for SIGINT.
	if _, err := service.SetBinding("sftp-copy",
		domainkeybindings.SchemePC, domainkeybindings.Disabled, nil); err != nil {
		t.Fatalf("disable the SFTP copy binding: %v", err)
	}
	if conflicts, err := service.SetBinding("command-palette",
		domainkeybindings.SchemePC, "Ctrl+C", nil); err != nil || len(conflicts) > 0 {
		t.Fatalf("bind application action to Ctrl+C: conflicts=%v err=%v", conflicts, err)
	}
	if conflicts, err := service.SetBinding("copy",
		domainkeybindings.SchemePC, "Ctrl+Shift+Y", nil); err != nil || len(conflicts) > 0 {
		t.Fatalf("move the copy binding: conflicts=%v err=%v", conflicts, err)
	}

	binds := phoneTerminalKeybinds(service)
	for _, bind := range binds {
		if bind.Mods == ui.Ctrl && bind.Key == ui.KeyC {
			t.Fatal("the Shortcuts table took Ctrl+C, which the terminal reserves for SIGINT")
		}
	}
	if bind, ok := terminalBind(binds, ui.Ctrl|ui.Shift, ui.KeyY); !ok || bind.Action != terminal.ActionCopy {
		t.Fatalf("Ctrl+Shift+Y binding = %+v, %v; want copy", bind, ok)
	}
	if bind, ok := terminalBind(binds, ui.Ctrl|ui.Shift, ui.KeyC); !ok || bind.Action != terminal.ActionNone {
		t.Fatalf("old copy chord = %+v, %v; want it unbound", bind, ok)
	}

	shell := New(WithSettings(settings), WithShortcuts(service))
	appearance := shell.sessions.currentAppearance()
	if bind, ok := terminalBind(appearance.Keybinds, ui.Ctrl|ui.Shift, ui.KeyY); !ok || bind.Action != terminal.ActionCopy {
		t.Fatalf("new terminal keybinds = %+v, want the edited copy chord", appearance.Keybinds)
	}
}

func terminalBind(binds []terminal.Keybind, mods ui.Modifiers, key ui.Key) (terminal.Keybind, bool) {
	for _, bind := range binds {
		if bind.Mods == mods && bind.Key == key {
			return bind, true
		}
	}
	return terminal.Keybind{}, false
}
