//go:build selftest

package app

// Ticket 27 self-test. Press F11, then 2, then 7. The check proves the
// Settings window and the keyboard-binding subsystem through the real
// services, views, and dispatch tables:
//
//   - Search: typing a setting name in the whole-tree search crosses areas,
//     and Enter opens the matching area.
//   - Live apply and reset: a Layout change persists through the preferences
//     service, the per-field reset restores its default, and Reset All asks
//     before restoring every group.
//   - Scheme and overrides: mac/PC chord rendering, a persisted override, a
//     duplicate-chord conflict that is not persisted until Replace, Cancel
//     leaving every binding alone, and reset-to-default.
//   - Dispatch: a real binding (Broadcast) fires through MyGo's key routing,
//     a terminal-owned chord (Ctrl+Shift+C) is never registered globally, and
//     an unavailable feature never produces a shortcut.
//   - Ctrl+,: the menu accelerator table carries the Settings accelerator and
//     pressing it opens the singleton Settings window.
//
// Every temporary service lives in its own directory, removed at the end of
// its step, so F11,2,7 can run twice in one process.
//
// Result: %TEMP%\natcatty-selftest-27.json.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/application/command"
	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	"github.com/mslxl/natcatty/ui/settings"
	"github.com/mslxl/natcatty/ui/shell/state"
)

const selftest27Ticket = 27

type selftest27 struct {
	windows *windowManager
	state   *state.State
}

func (t *selftest27) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest27Ticket,
		Name:   "Settings window and keyboard shortcuts",
		Steps: []selftest.Step{
			{Name: "search-live-apply-reset", Run: t.searchStep},
			{Name: "scheme-overrides-conflicts", Run: t.overrideStep},
			{Name: "dispatch-precedence", Run: t.dispatchStep},
			{Name: "settings-accelerator", Run: t.settingsStep},
		},
	}
}

func selftest27Preferences(dir string) (*apppreferences.Service, *settings.PreferencesState) {
	service := apppreferences.NewService(store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	_ = service.Load(context.Background())
	return service, settings.NewPreferencesState(service)
}

func selftest27Bindings(dir string) *appkeybindings.Service {
	service := appkeybindings.NewService(
		store.OpenKeyBindings(filepath.Join(dir, "keybindings.json")),
		appkeybindings.WithGOOS(runtime.GOOS))
	_ = service.Load(context.Background())
	return service
}

func (t *selftest27) searchStep(*ui.Context) selftest.Outcome {
	dir, err := os.MkdirTemp("", "natcatty-selftest-27-search-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(dir)
	m := locales.In("en")

	// The whole-tree search: the only control is the search field, so one Tab
	// reaches it without a pointer.
	search := settings.NewSearchState()
	tester := ui.NewTester(settings.ViewWithSettings(settings.Config{Search: search}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	tester.Key(0, ui.KeyTab)
	tester.Type("app lock")
	tester.Frame()
	if !search.Filtering() {
		return selftest.Failed(fmt.Errorf("typing did not reach the search field: %v", tester.Texts()))
	}
	if !tester.HasText(m.SettingsAppLockTitle()) {
		return selftest.Failed(fmt.Errorf("search did not cross into Security: %v", tester.Texts()))
	}
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if search.Area() != settings.AreaSecurity || search.Filtering() {
		return selftest.Failed(fmt.Errorf("Enter left area=%s filtering=%v",
			search.Area(), search.Filtering()))
	}

	// Live apply and both reset surfaces through the real service.
	service, preferences := selftest27Preferences(dir)
	layout := ui.NewTester(viewAtSettings(settings.AreaLayout, settings.Config{Preferences: preferences}), 1100, 720)
	layout.SetLocale("en")
	layout.Frame()
	if err := layout.Click(m.SettingsShowSFTPTab()); err != nil {
		return selftest.Failed(err)
	}
	layout.Frame()
	if service.Settings().Layout.ShowSFTPTab {
		return selftest.Failed(fmt.Errorf("the Layout change did not reach the service"))
	}
	if err := layout.Click(m.SettingsReset()); err != nil {
		return selftest.Failed(err)
	}
	layout.Frame()
	if !service.Settings().Layout.ShowSFTPTab {
		return selftest.Failed(fmt.Errorf("the field reset did not restore the default"))
	}
	if !preferences.Update(func(next *domainpreferences.Settings) {
		next.Appearance.WindowOpacity = 0.6
	}) {
		return selftest.Failed(fmt.Errorf("the opacity change did not persist"))
	}
	if err := layout.Click(m.SettingsResetAll()); err != nil {
		return selftest.Failed(err)
	}
	layout.Frame()
	if !layout.HasText(m.SettingsResetAllBody()) {
		return selftest.Failed(fmt.Errorf("Reset All did not ask first"))
	}
	if err := layout.Click(m.SettingsResetAllConfirm()); err != nil {
		return selftest.Failed(err)
	}
	layout.Frame()
	if service.Settings().Appearance.WindowOpacity != 1 {
		return selftest.Failed(fmt.Errorf("Reset All left opacity at %v",
			service.Settings().Appearance.WindowOpacity))
	}
	return selftest.Passed("search crossed areas; field reset and Reset All restored defaults",
		"settings.ViewWithSettings + apppreferences.Service + preferences.Store")
}

// viewAtSettings is the selftest's local copy of the test helper: render one
// Settings area.
func viewAtSettings(area settings.Area, config settings.Config) func(*ui.Context) {
	search := settings.NewSearchState()
	search.SetArea(area)
	config.Search = search
	return settings.ViewWithSettings(config)
}

func (t *selftest27) overrideStep(*ui.Context) selftest.Outcome {
	dir, err := os.MkdirTemp("", "natcatty-selftest-27-keys-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(dir)
	m := locales.In("en")

	service := selftest27Bindings(dir)
	shortcuts := settings.NewShortcutsState(service)
	tester := ui.NewTester(viewAtSettings(settings.AreaShortcuts,
		settings.Config{Shortcuts: shortcuts}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	// Ctrl+B already belongs to Broadcast, so open-local needs Replace.
	shortcuts.BeginRecording("open-local")
	if !shortcuts.RecordKey("open-local", ui.KeyB, ui.Ctrl) {
		return selftest.Failed(fmt.Errorf("the recorder did not consume Ctrl+B"))
	}
	tester.Frame()
	if _, ok := shortcuts.Pending(); !ok {
		return selftest.Failed(fmt.Errorf("no conflict was reported: %v", tester.Texts()))
	}
	if service.IsOverridden("open-local", domainkeybindings.SchemePC) {
		return selftest.Failed(fmt.Errorf("the conflicting chord was persisted before Replace"))
	}
	if !tester.HasText(m.SettingsShortcutConflictTitle()) {
		return selftest.Failed(fmt.Errorf("the conflict is not shown: %v", tester.Texts()))
	}
	if err := tester.Click(m.SettingsShortcutConflictReplace()); err != nil {
		return selftest.Failed(err)
	}
	tester.Frame()
	if got := shortcuts.Chord("open-local"); got != "Ctrl + B" {
		return selftest.Failed(fmt.Errorf("open-local = %q", got))
	}
	if got := shortcuts.Chord("broadcast"); got != domainkeybindings.Disabled {
		return selftest.Failed(fmt.Errorf("the displaced broadcast = %q", got))
	}
	if got := service.State().Overrides["open-local"]["pc"]; got != "Ctrl + B" {
		return selftest.Failed(fmt.Errorf("override was not persisted: %q", got))
	}

	// Cancel leaves everything alone.
	shortcuts.BeginRecording("open-local")
	shortcuts.RecordKey("open-local", ui.KeyB, ui.Ctrl)
	shortcuts.CancelConflict()
	if got := shortcuts.Chord("open-local"); got != "Ctrl + B" {
		return selftest.Failed(fmt.Errorf("Cancel changed open-local to %q", got))
	}

	// Scheme rendering and reset-to-default.
	if !shortcuts.SetScheme(domainkeybindings.SchemeMac) {
		return selftest.Failed(fmt.Errorf("switching to the mac scheme failed"))
	}
	if got := shortcuts.Chord("copy"); got != "⌘ + C" {
		return selftest.Failed(fmt.Errorf("mac copy = %q", got))
	}
	if !shortcuts.SetScheme(domainkeybindings.SchemePC) {
		return selftest.Failed(fmt.Errorf("switching back to the PC scheme failed"))
	}
	if !shortcuts.ResetBinding("open-local") {
		return selftest.Failed(fmt.Errorf("resetting open-local failed"))
	}
	if got := shortcuts.Chord("open-local"); got != domainkeybindings.Disabled {
		return selftest.Failed(fmt.Errorf("reset open-local = %q", got))
	}
	if service.IsOverridden("open-local", domainkeybindings.SchemePC) {
		return selftest.Failed(fmt.Errorf("reset left an override behind"))
	}
	return selftest.Passed("conflict required Replace; Cancel kept bindings; scheme and reset persisted",
		"appkeybindings.Service + settings.ShortcutsState + keybindings.Store")
}

func (t *selftest27) dispatchStep(*ui.Context) selftest.Outcome {
	dir, err := os.MkdirTemp("", "natcatty-selftest-27-dispatch-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(dir)

	service := selftest27Bindings(dir)
	dispatcher := &bindingDispatcher{service: service, shell: t.state}
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
		return selftest.Failed(fmt.Errorf("the Broadcast binding is not dispatched"))
	}
	if has(ui.Ctrl|ui.Shift, ui.KeyC) {
		return selftest.Failed(fmt.Errorf("a terminal copy chord was registered globally"))
	}

	// Prove the table works through MyGo's key routing, not just in memory.
	if t.state.BroadcastPanelOpen() {
		t.state.CloseBroadcastPanel()
	}
	view := func(c *ui.Context) {
		bindShortcuts(c, dispatcher.Shortcuts())
		ui.Text(c, "binding dispatch")
	}
	tester := ui.NewTester(view, 420, 220)
	tester.Frame()
	tester.Key(ui.Ctrl, ui.KeyB)
	tester.Frame()
	if !t.state.BroadcastPanelOpen() {
		return selftest.Failed(fmt.Errorf("Ctrl+B did not open the Broadcast panel"))
	}
	t.state.CloseBroadcastPanel()

	// An unavailable feature never fires, even when it has a chord.
	if _, err := service.SetBinding("toggle-pane-zoom", domainkeybindings.SchemePC,
		"Ctrl + Alt + Z", nil); err != nil {
		return selftest.Failed(err)
	}
	for _, shortcut := range dispatcher.Shortcuts() {
		if shortcut.mods == ui.Ctrl|ui.Alt && shortcut.key == ui.KeyZ {
			return selftest.Failed(fmt.Errorf("an unavailable binding produced a shortcut"))
		}
	}
	return selftest.Passed("Ctrl+B fired through MyGo routing; terminal and unavailable chords stayed unbound",
		"bindingDispatcher + state.State + ui.Context.OnShortcut")
}

func (t *selftest27) settingsStep(*ui.Context) selftest.Outcome {
	dir, err := os.MkdirTemp("", "natcatty-selftest-27-menu-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(dir)

	service := selftest27Bindings(dir)
	registry := command.NewRegistry()
	if err := registry.Register(command.OpenSettings, func(context.Context) error {
		t.windows.OpenSettings()
		return nil
	}); err != nil {
		return selftest.Failed(err)
	}
	menu := BuildMenuWithBindings(registry, "en", menuBindingSource{service: service})
	item := selftest27MenuItem(menu.Items(), "menu.settings")
	if item == nil {
		return selftest.Failed(fmt.Errorf("the Settings menu item is missing"))
	}
	mods, key := ui.Ctrl, ui.KeyComma
	want := "Ctrl+,"
	if runtime.GOOS == "darwin" {
		mods, want = ui.Super, "Super+,"
	}
	if item.Accelerator != want {
		return selftest.Failed(fmt.Errorf("Settings accelerator = %q, want %q",
			item.Accelerator, want))
	}

	settingsWindow := t.windows.settings
	var accelerators []acceleratorBinding
	for _, binding := range MenuAccelerators(menu) {
		if binding.mods == mods && binding.key == key {
			accelerators = append(accelerators, binding)
		}
	}
	if len(accelerators) == 0 {
		return selftest.Failed(fmt.Errorf("Ctrl+, is not in the accelerator table"))
	}
	view := func(c *ui.Context) {
		bindAccelerators(c, accelerators)
		ui.Text(c, "menu dispatch")
	}
	tester := ui.NewTester(view, 420, 220)
	tester.Frame()
	tester.Key(mods, key)
	tester.Frame()
	if t.windows.settings == nil {
		return selftest.Failed(fmt.Errorf("Ctrl+, did not open Settings"))
	}
	if settingsWindow == nil {
		t.windows.settings.Close()
	}
	return selftest.Passed("the menu table carries Ctrl+, and pressing it opened the Settings window",
		"BuildMenuWithBindings + MenuAccelerators + windowManager.OpenSettings")
}

// selftest27MenuItem finds a menu item by ID without depending on a test
// helper, which is not linked into the selftest build.
func selftest27MenuItem(items []*mygo.MenuItem, id string) *mygo.MenuItem {
	for _, item := range items {
		if item == nil {
			continue
		}
		if item.ID == id {
			return item
		}
		if found := selftest27MenuItem(item.Submenu, id); found != nil {
			return found
		}
	}
	return nil
}
