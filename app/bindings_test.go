package app

import (
	"context"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/command"
	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	domain "github.com/mslxl/natcatty/domain/keybindings"
	"github.com/mslxl/natcatty/ui/shell/state"
)

type memoryBindingStore struct {
	state appkeybindings.State
	saves int
}

func (m *memoryBindingStore) Load(context.Context) (appkeybindings.State, error) {
	return m.state, nil
}
func (m *memoryBindingStore) Save(_ context.Context, state appkeybindings.State) error {
	m.state = state
	m.saves++
	return nil
}

func newBindingFixture(t *testing.T, goos string) (*appkeybindings.Service, *memoryBindingStore) {
	t.Helper()
	store := &memoryBindingStore{}
	service := appkeybindings.NewService(store, appkeybindings.WithGOOS(goos))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return service, store
}

func menuAcceleratorFor(t *testing.T, service *appkeybindings.Service, itemID string) string {
	t.Helper()
	menu := BuildMenuWithBindings(command.NewRegistry(), "en",
		menuBindingSource{service: service})
	return findItem(t, menu.Items(), itemID).Accelerator
}

func TestMenuAcceleratorsFollowTheActiveSchemeAndOverrides(t *testing.T) {
	service, _ := newBindingFixture(t, "windows")

	if got := menuAcceleratorFor(t, service, "menu.newTab"); got != "Ctrl+T" {
		t.Fatalf("PC new-tab = %q", got)
	}
	if got := menuAcceleratorFor(t, service, "menu.splitDown"); got != "Ctrl+Shift+D" {
		t.Fatalf("PC split-horizontal = %q", got)
	}
	if got := menuAcceleratorFor(t, service, "menu.splitRight"); got != "Ctrl+Shift+E" {
		t.Fatalf("PC split-vertical = %q", got)
	}

	if conflicts, err := service.SetBinding("new-tab", domain.SchemePC,
		"Ctrl + Alt + N", nil); err != nil || len(conflicts) != 0 {
		t.Fatalf("override: conflicts=%v err=%v", conflicts, err)
	}
	if got := menuAcceleratorFor(t, service, "menu.newTab"); got != "Ctrl+Alt+N" {
		t.Fatalf("overridden new-tab = %q", got)
	}

	if err := service.SetScheme(domain.SchemeMac); err != nil {
		t.Fatal(err)
	}
	if got := menuAcceleratorFor(t, service, "menu.newTab"); got != "Super+T" {
		t.Fatalf("mac new-tab = %q", got)
	}
	if got := menuAcceleratorFor(t, service, "menu.splitRight"); got != "Super+Shift+D" {
		t.Fatalf("mac split-vertical = %q", got)
	}

	if err := service.SetScheme(domain.SchemeDisabled); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"menu.newTab", "menu.closeTab", "menu.splitRight", "menu.settings"} {
		if got := menuAcceleratorFor(t, service, id); got != "" {
			t.Errorf("disabled scheme kept %s = %q", id, got)
		}
	}
	// The menu still offers Settings even when the catalog scheme is off.
	menu := BuildMenuWithBindings(command.NewRegistry(), "en",
		menuBindingSource{service: service})
	settingsItem := findItem(t, menu.Items(), "menu.settings")
	if settingsItem.Accelerator != "" || settingsItem.Click == nil {
		t.Fatalf("settings item = %+v", settingsItem)
	}
}

func TestMenuSettingsAcceleratorOpensSettingsThroughTheAcceleratorEngine(t *testing.T) {
	service, _ := newBindingFixture(t, "windows")
	registry := command.NewRegistry()
	opened := 0
	if err := registry.Register(command.OpenSettings, func(context.Context) error {
		opened++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	menu := BuildMenuWithBindings(registry, "en", menuBindingSource{service: service})
	settingsItem := findItem(t, menu.Items(), "menu.settings")
	if settingsItem.Accelerator != "Ctrl+," {
		t.Fatalf("settings accelerator = %q", settingsItem.Accelerator)
	}
	bindings := MenuAccelerators(menu)
	var found bool
	for _, binding := range bindings {
		if binding.mods == ui.Ctrl && binding.key == ui.KeyComma {
			found = true
		}
	}
	if !found {
		t.Fatal("Ctrl+, is not in the accelerator engine table")
	}
	settingsItem.Click(settingsItem, nil)
	if opened != 1 {
		t.Fatalf("opened = %d", opened)
	}
}

func TestDisplacedMenuAcceleratorDisappears(t *testing.T) {
	service, _ := newBindingFixture(t, "windows")
	menus := menuShortcuts(BuildMenuWithBindings(command.NewRegistry(), "en",
		menuBindingSource{service: service}))
	if conflicts, err := service.SetBinding("broadcast", domain.SchemePC,
		"Ctrl + Shift + B", menus); err != nil || len(conflicts) == 0 {
		t.Fatalf("expected a conflict with the Backups accelerator: %v %v", conflicts, err)
	}
	if err := service.ReplaceBinding("broadcast", domain.SchemePC,
		"Ctrl + Shift + B", menus); err != nil {
		t.Fatal(err)
	}
	if got := menuAcceleratorFor(t, service, "menu.backups"); got != "" {
		t.Fatalf("the displaced Backups accelerator survived: %q", got)
	}
	if got := menuAcceleratorFor(t, service, "menu.newTab"); got == "" {
		t.Fatal("an unrelated accelerator disappeared")
	}
}

// TestATerminalChordNeverBecomesAMenuAccelerator protects SIGINT and
// terminal copy: even after the user explicitly replaces the terminal binding
// with a menu command, the menu must not register a window accelerator the
// focused terminal needs. The conflict is reported before the replacement.
func TestATerminalChordNeverBecomesAMenuAccelerator(t *testing.T) {
	for _, chord := range []string{"Ctrl + Shift + C", "Ctrl + C"} {
		service, _ := newBindingFixture(t, "windows")
		if conflicts, err := service.SetBinding("new-tab", domain.SchemePC, chord, nil); err != nil {
			t.Fatalf("%s: SetBinding: %v", chord, err)
		} else if len(conflicts) == 0 {
			t.Fatalf("%s: no conflict with the terminal", chord)
		}
		if err := service.ReplaceBinding("new-tab", domain.SchemePC, chord, nil); err != nil {
			t.Fatalf("%s: ReplaceBinding: %v", chord, err)
		}
		if got := menuAcceleratorFor(t, service, "menu.newTab"); got != "" {
			t.Errorf("%s: menu accelerator = %q, want none", chord, got)
		}
	}
}

// TestTerminalReservedChordsKeepTheCatalogDefaultAfterAnOverride pins the
// reserved set to each binding's catalog default as well as its effective
// chord. ReplaceBinding("new-tab", PC, "Ctrl+Shift+C") disables the displaced
// copy binding, and the freed chord must still count as terminal-owned.
func TestTerminalReservedChordsKeepTheCatalogDefaultAfterAnOverride(t *testing.T) {
	ctrlShiftC := chordKey{mods: ui.Ctrl | ui.Shift, key: ui.KeyC}

	t.Run("disabled copy on PC", func(t *testing.T) {
		// terminal.DefaultKeybinds() also reserves this chord on Windows, so
		// this subtest only fails at the base revision on macOS and Linux.
		overrides := domain.Overrides{}.Set("copy", domain.SchemePC, domain.Disabled)
		if !terminalReservedChords(domain.SchemePC, overrides)[ctrlShiftC] {
			t.Fatal("Ctrl+Shift+C is not reserved after copy was disabled")
		}
	})

	t.Run("new-tab displaced copy", func(t *testing.T) {
		service, _ := newBindingFixture(t, "windows")
		if err := service.ReplaceBinding("new-tab", domain.SchemePC,
			"Ctrl + Shift + C", nil); err != nil {
			t.Fatal(err)
		}
		reserved := terminalReservedChords(domain.SchemePC, service.State().Overrides)
		if !reserved[ctrlShiftC] {
			t.Fatal("Ctrl+Shift+C is not reserved after new-tab displaced copy")
		}
	})

	// Cmd+Ctrl+K is clear-buffer's Mac default and appears in no platform's
	// terminal.DefaultKeybinds(), so this subtest fails at the base revision
	// on Windows, Linux, and macOS alike. Cmd+C and Ctrl+Shift+C cannot make
	// that claim: MyGo's native defaults reserve one on every platform.
	t.Run("disabled clear-buffer on Mac", func(t *testing.T) {
		overrides := domain.Overrides{}.Set("clear-buffer", domain.SchemeMac, domain.Disabled)
		reserved := terminalReservedChords(domain.SchemeMac, overrides)
		if !reserved[chordKey{mods: ui.Super | ui.Ctrl, key: ui.KeyK}] {
			t.Fatal("Cmd+Ctrl+K is not reserved after clear-buffer was disabled")
		}
	})
}

func TestMenuShortcutsMarkBindingDerivedItems(t *testing.T) {
	service, _ := newBindingFixture(t, "windows")
	shortcuts := menuShortcuts(BuildMenuWithBindings(command.NewRegistry(), "en",
		menuBindingSource{service: service}))
	seen := make(map[string]domain.MenuShortcut, len(shortcuts))
	for _, shortcut := range shortcuts {
		seen[shortcut.ID] = shortcut
	}
	for _, id := range []string{"menu.newTab", "menu.closeTab", "menu.splitRight", "menu.focusLeft", "menu.settings"} {
		if !seen[id].Derived {
			t.Errorf("%s is not marked derived", id)
		}
	}
	if seen["menu.backups"].Derived {
		t.Error("Backups is a foreign accelerator but was marked derived")
	}
}

func TestDispatcherRunsRealActionsAndLeavesTerminalChordsAlone(t *testing.T) {
	service, _ := newBindingFixture(t, "windows")
	shellState := state.NewState()
	opacityToggles := 0
	dispatcher := &bindingDispatcher{
		service: service,
		shell:   shellState,
		toggleOpacity: func() {
			opacityToggles++
		},
	}
	shortcuts := dispatcher.Shortcuts()
	has := func(mods ui.Modifiers, key ui.Key) bool {
		for _, shortcut := range shortcuts {
			if shortcut.mods == mods && shortcut.key == key {
				return true
			}
		}
		return false
	}
	if !has(ui.Ctrl, ui.KeyB) {
		t.Fatal("Broadcast (Ctrl+B) is missing")
	}
	if !has(ui.Ctrl, ui.Key1) {
		t.Fatal("switch-to-tab 1 is missing")
	}
	for _, terminalChord := range []ui.Key{ui.KeyC, ui.KeyV} {
		if has(ui.Ctrl|ui.Shift, terminalChord) {
			t.Errorf("a terminal-owned Ctrl+Shift chord was dispatched globally")
		}
	}

	// A global override onto SIGINT must not shadow the terminal.
	if _, err := service.SetBinding("open-hosts", domain.SchemePC, "Ctrl + C", nil); err != nil {
		t.Fatal(err)
	}
	shortcuts = dispatcher.Shortcuts()
	has = func(mods ui.Modifiers, key ui.Key) bool {
		for _, shortcut := range shortcuts {
			if shortcut.mods == mods && shortcut.key == key {
				return true
			}
		}
		return false
	}
	if has(ui.Ctrl, ui.KeyC) {
		t.Fatal("Ctrl+C was registered as a global shortcut")
	}

	// An unavailable binding never reaches the dispatcher.
	if _, err := service.SetBinding("toggle-pane-zoom", domain.SchemePC, "Ctrl + Alt + Z", nil); err != nil {
		t.Fatal(err)
	}
	for _, shortcut := range dispatcher.Shortcuts() {
		if shortcut.mods == ui.Ctrl|ui.Alt && shortcut.key == ui.KeyZ {
			t.Fatal("an unavailable binding produced a shortcut")
		}
	}

	// Toggle transparency is a real action.
	if _, err := service.SetBinding("toggle-opacity", domain.SchemePC, "Ctrl + Shift + U", nil); err != nil {
		t.Fatal(err)
	}
	for _, shortcut := range dispatcher.Shortcuts() {
		if shortcut.mods == ui.Ctrl|ui.Shift && shortcut.key == ui.KeyU {
			shortcut.run()
		}
	}
	if opacityToggles != 1 {
		t.Fatalf("opacity toggles = %d", opacityToggles)
	}

	// Broadcast really opens the panel through the shell.
	shellState.CloseBroadcastPanel()
	for _, shortcut := range dispatcher.Shortcuts() {
		if shortcut.mods == ui.Ctrl && shortcut.key == ui.KeyB {
			shortcut.run()
		}
	}
	if !shellState.BroadcastPanelOpen() {
		t.Fatal("Ctrl+B did not open the Broadcast panel")
	}
}

func TestDispatcherRegistersAFocusOverride(t *testing.T) {
	service, _ := newBindingFixture(t, "windows")
	shellState := state.NewState()
	dispatcher := &bindingDispatcher{service: service, shell: shellState}
	has := func(mods ui.Modifiers, key ui.Key) bool {
		for _, shortcut := range dispatcher.Shortcuts() {
			if shortcut.mods == mods && shortcut.key == key {
				return true
			}
		}
		return false
	}
	// The default pattern is shown by the menu, not registered twice.
	if has(ui.Ctrl|ui.Alt, ui.KeyLeft) {
		t.Fatal("the default focus pattern was dispatched twice")
	}
	if _, err := service.SetBinding("move-focus", domain.SchemePC,
		"Shift + Alt + Left", nil); err != nil {
		t.Fatal(err)
	}
	if has(ui.Ctrl|ui.Alt, ui.KeyLeft) {
		t.Fatal("the old focus chord survived the override")
	}
	if !has(ui.Shift|ui.Alt, ui.KeyLeft) {
		t.Fatal("the overridden focus chord is missing")
	}
}

func TestTerminalKeybindsFollowOverridesAndClearOldDefaults(t *testing.T) {
	service, _ := newBindingFixture(t, "windows")
	keybindOf := func(binds []terminal.Keybind, mods ui.Modifiers, key ui.Key) (terminal.Keybind, bool) {
		for _, bind := range binds {
			if bind.Mods == mods && bind.Key == key {
				return bind, true
			}
		}
		return terminal.Keybind{}, false
	}

	binds := terminalKeybinds(service)
	copyBind, ok := keybindOf(binds, ui.Ctrl|ui.Shift, ui.KeyC)
	if !ok || copyBind.Action != terminal.ActionCopy || !copyBind.Performable {
		t.Fatalf("default copy binding = %+v", copyBind)
	}
	// MyGo's own Ctrl+C is never overridden: it must keep interrupting.
	if _, ok := keybindOf(binds, ui.Ctrl, ui.KeyC); ok {
		t.Fatal("the app replaced MyGo's Ctrl+C binding")
	}

	if _, err := service.SetBinding("copy", domain.SchemePC,
		"Ctrl + Alt + C", nil); err != nil {
		t.Fatal(err)
	}
	binds = terminalKeybinds(service)
	if bind, ok := keybindOf(binds, ui.Ctrl|ui.Alt, ui.KeyC); !ok || bind.Action != terminal.ActionCopy {
		t.Fatalf("override copy = %+v", bind)
	}
	if bind, ok := keybindOf(binds, ui.Ctrl|ui.Shift, ui.KeyC); !ok || bind.Action != terminal.ActionNone {
		t.Fatalf("old copy chord was not cleared: %+v", bind)
	}

	if _, err := service.SetBinding("paste", domain.SchemePC, domain.Disabled, nil); err != nil {
		t.Fatal(err)
	}
	binds = terminalKeybinds(service)
	if bind, ok := keybindOf(binds, ui.Ctrl|ui.Shift, ui.KeyV); !ok || bind.Action != terminal.ActionNone {
		t.Fatalf("disabled paste chord was not cleared: %+v", bind)
	}
}

func TestUnavailableCatalogEntriesKeepTheirLabels(t *testing.T) {
	for _, id := range []string{"open-sftp", "sftp-copy", "search-terminal", "toggle-pane-zoom"} {
		if catalogBindingAvailable(id) {
			t.Errorf("%s is marked available", id)
		}
	}
	for _, id := range []string{"new-tab", "copy", "open-settings", "broadcast"} {
		if !catalogBindingAvailable(id) {
			t.Errorf("%s is marked unavailable", id)
		}
	}
}
