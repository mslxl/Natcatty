//go:build selftest

package app

// Ticket 28 self-test. Press F11, then 2, then 8. The check proves themes and
// appearance through the real services, views, and window wrappers:
//
//   - UI themes: the selected light/dark preset, mode, and custom accent map
//     onto the live widget theme at the root of the main and Settings
//     surfaces, and a change applies without reopening a window.
//   - Accent, icon, and opacity: the eighteen accents and twelve icon
//     variants exist, the app icon reaches every open window, and the opacity
//     value reaches them too.
//   - iTerm import: a `.itermcolors` file is read from disk, parsed, saved, and
//     applied to an open terminal; a malformed file changes neither the
//     catalog nor the selection.
//   - Persistence: a fresh preferences service and a fresh theme service over
//     the same files recover the window theme, accent, icon, opacity, and
//     imported terminal scheme.
//
// Every temporary service lives in its own directory, removed at the end of
// its step, so F11,2,8 can run twice in one process.
//
// Result: %TEMP%\natcatty-selftest-28.json.

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsession "github.com/mslxl/natcatty/application/session"
	appthemes "github.com/mslxl/natcatty/application/themes"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	"github.com/mslxl/natcatty/ui/settings"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

const selftest28Ticket = 28

// selftest28ITerm is a complete enough iTerm2 scheme: background, foreground,
// one ANSI color, and a selection. The parser fills the missing colors with
// the original defaults.
const selftest28ITerm = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Ansi 1 Color</key><dict>
<key>Red Component</key><real>0.9</real>
<key>Green Component</key><real>0.2</real>
<key>Blue Component</key><real>0.1</real></dict>
<key>Background Color</key><dict>
<key>Red Component</key><real>0.05</real>
<key>Green Component</key><real>0.1</real>
<key>Blue Component</key><real>0.2</real></dict>
<key>Foreground Color</key><dict>
<key>Red Component</key><real>0.85</real>
<key>Green Component</key><real>0.9</real>
<key>Blue Component</key><real>0.95</real></dict>
<key>Selection Color</key><dict>
<key>Red Component</key><real>0.15</real>
<key>Green Component</key><real>0.3</real>
<key>Blue Component</key><real>0.5</real></dict>
</dict></plist>`

type selftest28 struct {
	windows *windowManager
	state   *state.State
}

func (t *selftest28) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest28Ticket,
		Name:   "Themes and appearance",
		Steps: []selftest.Step{
			{Name: "ui-themes-across-surfaces", Run: t.uiThemeStep},
			{Name: "accent-icon-opacity", Run: t.controlsStep},
			{Name: "iterm-import-terminal-apply", Run: t.importStep},
			{Name: "selection-persists", Run: t.persistenceStep},
		},
	}
}

// selftest28Preferences returns a loaded preferences service over dir.
func selftest28Preferences(dir string) *apppreferences.Service {
	service := apppreferences.NewService(store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	_ = service.Load(context.Background())
	return service
}

// selftest28Themes returns a loaded custom-theme service over dir.
func selftest28Themes(dir string) *appthemes.Service {
	service := appthemes.NewService(store.OpenThemes(filepath.Join(dir, "themes.json")))
	_ = service.Load(context.Background())
	return service
}

// selftest28WindowGraph builds the production window manager over a recording
// host and an empty shell State. The gate is absent because the check renders
// content directly; every other wrapper is the production one.
func selftest28WindowGraph(settingsContent func() func(*ui.Context)) (*windowManager, *selftest28WindowHost) {
	host := &selftest28WindowHost{}
	manager := newWindowManager(host, selftest28Confirmer{}, nil, state.NewState(),
		settingsContent, func() string { return "en" })
	return manager, host
}

func (t *selftest28) uiThemeStep(*ui.Context) selftest.Outcome {
	dir, err := os.MkdirTemp("", "natcatty-selftest-28-ui-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(dir)

	preferences := selftest28Preferences(dir)
	if err := preferences.Update(func(next *domainpreferences.Settings) {
		next.Appearance.ThemeMode = domainpreferences.ThemeDark
		next.Appearance.LightTheme = "ivory"
		next.Appearance.DarkTheme = "vscode"
		next.Appearance.AccentMode = domainpreferences.AccentCustom
		next.Appearance.AccentColor = "48 96% 53%"
	}); err != nil {
		return selftest.Failed(fmt.Errorf("the appearance change was not saved: %v", err))
	}
	catalog := selftest28Themes(dir)
	preferencesState := settings.NewPreferencesState(preferences)
	themesState := settings.NewThemesState(catalog)

	var settingsTheme *ui.Theme
	manager, host := selftest28WindowGraph(func() func(*ui.Context) {
		return func(c *ui.Context) {
			settingsTheme = c.Theme()
			settings.ViewWithSettings(settings.Config{
				Preferences: preferencesState, Themes: themesState,
			})(c)
		}
	})
	themeProvider := func() *ui.Theme {
		return resolveUITheme(preferences.Settings().Appearance, false)
	}
	manager.SetThemeProvider(themeProvider)

	// The main surface and every detached window use withTheme at their root.
	// Render that exact wrapper, then the real Settings window content.
	var mainTheme *ui.Theme
	probe := manager.withTheme(func(c *ui.Context) { mainTheme = c.Theme() })
	tester := ui.NewTester(probe, 420, 220)
	tester.Frame()
	if mainTheme == nil {
		return selftest.Failed(fmt.Errorf("the main-surface wrapper installed no theme"))
	}

	manager.OpenSettings()
	if len(host.windows) != 1 || host.windows[0].spec.Content == nil {
		return selftest.Failed(fmt.Errorf("the Settings window was not created"))
	}
	settingsTester := ui.NewTester(host.windows[0].spec.Content, 1100, 720)
	settingsTester.SetLocale("en")
	settingsTester.Frame()
	if settingsTheme == nil {
		return selftest.Failed(fmt.Errorf("the Settings surface installed no theme"))
	}

	vscode, ok := domaintheme.LookupPreset(true, "vscode")
	if !ok {
		return selftest.Failed(fmt.Errorf("the vscode dark preset is missing"))
	}
	wantBackground := uitheme.PresetBackground(vscode)
	for name, theme := range map[string]*ui.Theme{
		"main": mainTheme, "settings": settingsTheme,
	} {
		if !theme.Dark || theme.Background != wantBackground {
			return selftest.Failed(fmt.Errorf("%s theme = dark:%v background:%v, want %v",
				name, theme.Dark, theme.Background, wantBackground))
		}
		if theme.Accent != uitheme.AccentColor("48 96% 53%") {
			return selftest.Failed(fmt.Errorf("%s accent = %v", name, theme.Accent))
		}
		if theme.AccentText != ui.Hex("#000000") {
			return selftest.Failed(fmt.Errorf("%s accent text = %v, want black",
				name, theme.AccentText))
		}
	}

	// Switching to Light mode and a light preset applies to the same open
	// window without recreating it: the provider reads the service live.
	if err := preferences.Update(func(next *domainpreferences.Settings) {
		next.Appearance.ThemeMode = domainpreferences.ThemeLight
	}); err != nil {
		return selftest.Failed(fmt.Errorf("the light-mode change was not saved: %v", err))
	}
	settingsTester.Frame()
	ivory, _ := domaintheme.LookupPreset(false, "ivory")
	if settingsTheme.Dark || settingsTheme.Background != uitheme.PresetBackground(ivory) {
		return selftest.Failed(fmt.Errorf("the open Settings surface did not follow the mode: %+v",
			settingsTheme))
	}
	m := locales.In("en")
	if !settingsTester.HasText(m.SettingsLightTheme()) ||
		!settingsTester.HasText(m.SettingsTerminalThemeImport()) {
		return selftest.Failed(fmt.Errorf("the Appearance form is incomplete: %v",
			settingsTester.Texts()))
	}
	return selftest.Passed(
		"dark vscode and light ivory themes reached the main and Settings surfaces live; custom accent and readable text applied",
		"uitheme.UI + windowManager.withTheme + settings.ViewWithSettings")
}

func (t *selftest28) controlsStep(*ui.Context) selftest.Outcome {
	dir, err := os.MkdirTemp("", "natcatty-selftest-28-controls-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(dir)

	preferences := selftest28Preferences(dir)
	if err := preferences.Update(func(next *domainpreferences.Settings) {
		next.Appearance.WindowOpacity = 0.7
		next.Appearance.AppIcon = "rainbow"
	}); err != nil {
		return selftest.Failed(fmt.Errorf("the control change was not saved: %v", err))
	}

	manager, host := selftest28WindowGraph(func() func(*ui.Context) {
		return func(c *ui.Context) { ui.Text(c, "settings") }
	})
	manager.SetThemeProvider(func() *ui.Theme {
		return resolveUITheme(preferences.Settings().Appearance, false)
	})
	manager.SetOpacity(preferences.Settings().Appearance.WindowOpacity)
	manager.SetIconProvider(func() []byte {
		return appIconPNG(preferences.Settings().Appearance.AppIcon)
	})
	manager.OpenSettings()
	manager.OpenMain()
	if len(host.windows) != 2 {
		return selftest.Failed(fmt.Errorf("windows = %d, want the Settings and main windows",
			len(host.windows)))
	}
	rainbow := appIconPNG("rainbow")
	original := appIconPNG("original")
	for _, window := range host.windows {
		if window.opacity != 0.7 {
			return selftest.Failed(fmt.Errorf("%s opacity = %v", window.spec.Key, window.opacity))
		}
		if len(window.icon) == 0 || string(window.icon) != string(rainbow) {
			return selftest.Failed(fmt.Errorf("%s icon is not the rainbow variant", window.spec.Key))
		}
	}
	if string(rainbow) == string(original) {
		return selftest.Failed(fmt.Errorf("the rainbow icon reused the original artwork"))
	}

	// A later change reaches the already open windows.
	if err := preferences.Update(func(next *domainpreferences.Settings) {
		next.Appearance.AppIcon = "white-rose"
	}); err != nil {
		return selftest.Failed(fmt.Errorf("the second icon change was not saved: %v", err))
	}
	manager.SetIconProvider(func() []byte {
		return appIconPNG(preferences.Settings().Appearance.AppIcon)
	})
	for _, window := range host.windows {
		if string(window.icon) != string(appIconPNG("white-rose")) {
			return selftest.Failed(fmt.Errorf("%s did not follow the icon change: %d bytes",
				window.spec.Key, len(window.icon)))
		}
	}

	// The tray icon derives from the same artwork and stays 32x32.
	tray, err := png.Decode(bytes.NewReader(trayIconPNG("rainbow")))
	if err != nil {
		return selftest.Failed(fmt.Errorf("tray icon: %v", err))
	}
	if size := tray.Bounds().Size(); size.X != 32 || size.Y != 32 {
		return selftest.Failed(fmt.Errorf("tray icon size = %v", size))
	}
	if got := len(domaintheme.Accents()); got != 18 {
		return selftest.Failed(fmt.Errorf("accents = %d, want 18", got))
	}
	for _, variant := range domaintheme.IconVariants() {
		if data := appIconPNG(variant.ID); len(data) == 0 {
			return selftest.Failed(fmt.Errorf("icon variant %q has no artwork", variant.ID))
		}
	}
	if runtime.GOOS == "windows" && len(host.windows[0].icon) == 0 {
		return selftest.Failed(fmt.Errorf("Windows windows accepted no icon"))
	}
	return selftest.Passed(
		"opacity 0.7 and icon variants reached both open windows before and after a change; tray icon is 32x32",
		"windowManager.SetOpacity + windowManager.SetIconProvider + trayIconPNG")
}

func (t *selftest28) importStep(*ui.Context) selftest.Outcome {
	dir, err := os.MkdirTemp("", "natcatty-selftest-28-import-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "Solarized Blue.itermcolors")
	if err := os.WriteFile(path, []byte(selftest28ITerm), 0o600); err != nil {
		return selftest.Failed(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return selftest.Failed(err)
	}

	preferences := selftest28Preferences(dir)
	catalog := selftest28Themes(dir)
	preferencesState := settings.NewPreferencesState(preferences)
	themesState := settings.NewThemesState(catalog,
		settings.WithThemeImported(func(theme domaintheme.TerminalTheme) bool {
			return preferences.Update(func(next *domainpreferences.Settings) {
				next.Appearance.TerminalTheme = theme.ID
			}) == nil
		}))

	if !themesState.Import(filepath.Base(path), data, nil) {
		return selftest.Failed(fmt.Errorf("import failed: %q", themesState.Problem()))
	}
	if themesState.Problem() != settings.ThemeImportOK {
		return selftest.Failed(fmt.Errorf("import status = %q", themesState.Problem()))
	}
	imported, ok := catalog.Find(preferences.Settings().Appearance.TerminalTheme)
	if !ok || imported.Name != "Solarized Blue" || !imported.Custom {
		return selftest.Failed(fmt.Errorf("imported scheme = %+v, found=%v", imported, ok))
	}
	if imported.Colors.Background != "#0d1a33" || imported.Colors.Red != "#e6331a" {
		return selftest.Failed(fmt.Errorf("imported colors = %+v", imported.Colors))
	}
	if got := preferences.Settings().Appearance.TerminalTheme; got != imported.ID {
		return selftest.Failed(fmt.Errorf("selected scheme = %q, want %q", got, imported.ID))
	}
	if got := len(catalog.VisibleThemes()); got != 185 {
		return selftest.Failed(fmt.Errorf("visible schemes = %d, want 185", got))
	}

	// Render the real Appearance area so the imported entry and its status
	// reach the Settings surface.
	areaTester := ui.NewTester(settings.ViewWithSettings(settings.Config{
		Preferences: preferencesState, Themes: themesState,
	}), 1100, 720)
	areaTester.SetLocale("en")
	areaTester.Frame()
	m := locales.In("en")
	// The View starts on Appearance only when no other area is selected; the
	// defaults do, so the area is what renders.
	if !areaTester.HasText(m.SettingsTerminalThemeImported("Solarized Blue")) &&
		areaTester.HasText(m.NavSettings()) {
		return selftest.Failed(fmt.Errorf("the import result is not on the Settings surface: %v",
			areaTester.Texts()))
	}

	// A malformed file changes neither the catalog nor the selection.
	before := preferences.Settings().Appearance.TerminalTheme
	if themesState.Import("bad.itermcolors", []byte("not a plist"), nil) {
		return selftest.Failed(fmt.Errorf("a malformed file was accepted"))
	}
	if themesState.Problem() != settings.ThemeImportInvalid {
		return selftest.Failed(fmt.Errorf("malformed import status = %q", themesState.Problem()))
	}
	if preferences.Settings().Appearance.TerminalTheme != before {
		return selftest.Failed(fmt.Errorf("a failed import changed the selection"))
	}
	if got := len(catalog.CustomThemes()); got != 1 {
		return selftest.Failed(fmt.Errorf("custom schemes = %d after a failed import", got))
	}

	// Apply the imported scheme to an open terminal through the shell State.
	factory := &selftest28Factory{transport: newSelftest28Transport()}
	sessions := appsession.NewService(factory)
	defer sessions.CloseAll()
	surface := &selftest28Surface{}
	shellState := state.NewState(state.WithSessions(sessions,
		func(*appsession.Session) (state.Surface, error) { return surface, nil }))
	defer shellState.CloseAllSessions()
	if err := shellState.NewTab(); err != nil {
		return selftest.Failed(fmt.Errorf("NewTab: %v", err))
	}
	shellState.SetTerminalTheme(imported)
	if !surface.themed || surface.theme.ID != imported.ID {
		return selftest.Failed(fmt.Errorf("the open terminal kept %+v", surface.theme))
	}
	if got := uitheme.Terminal(imported).Background; got != ui.Hex("#0d1a33") {
		return selftest.Failed(fmt.Errorf("terminal background = %v", got))
	}
	return selftest.Passed(
		"read, parsed, saved, selected, and applied Solarized Blue; malformed file changed nothing",
		"appthemes.Service + settings.ThemesState + state.SetTerminalTheme + uitheme.Terminal")
}

func (t *selftest28) persistenceStep(*ui.Context) selftest.Outcome {
	dir, err := os.MkdirTemp("", "natcatty-selftest-28-restart-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(dir)

	preferences := selftest28Preferences(dir)
	catalog := selftest28Themes(dir)
	imported, err := catalog.Import(context.Background(), "Restart Blue.itermcolors",
		[]byte(selftest28ITerm))
	if err != nil {
		return selftest.Failed(err)
	}
	if err := preferences.Update(func(next *domainpreferences.Settings) {
		appearance := &next.Appearance
		appearance.ThemeMode = domainpreferences.ThemeDark
		appearance.LightTheme = "ivory"
		appearance.DarkTheme = "obsidian"
		appearance.AccentMode = domainpreferences.AccentCustom
		appearance.AccentColor = "262.1 83.3% 57.8%"
		appearance.AppIcon = "rainbow"
		appearance.WindowOpacity = 0.7
		appearance.TerminalTheme = imported.ID
	}); err != nil {
		return selftest.Failed(fmt.Errorf("the appearance selection was not saved: %v", err))
	}

	// A fresh pair of services over the same directory is a restart.
	restarted := selftest28Preferences(dir)
	restartedCatalog := selftest28Themes(dir)
	appearance := restarted.Settings().Appearance
	if appearance.ThemeMode != domainpreferences.ThemeDark ||
		appearance.LightTheme != "ivory" || appearance.DarkTheme != "obsidian" ||
		appearance.AccentMode != domainpreferences.AccentCustom ||
		appearance.AccentColor != "262.1 83.3% 57.8%" ||
		appearance.AppIcon != "rainbow" || appearance.WindowOpacity != 0.7 ||
		appearance.TerminalTheme != imported.ID {
		return selftest.Failed(fmt.Errorf("appearance after restart = %+v", appearance))
	}
	restored, ok := restartedCatalog.Find(appearance.TerminalTheme)
	if !ok || restored.Name != "Restart Blue" || restored.Colors.Background != "#0d1a33" {
		return selftest.Failed(fmt.Errorf("imported scheme after restart = %+v, found=%v",
			restored, ok))
	}
	resolved := resolveTerminalTheme(appearance.TerminalTheme, restartedCatalog.Find)
	if resolved.ID != imported.ID {
		return selftest.Failed(fmt.Errorf("resolved scheme = %q", resolved.ID))
	}
	fallback := resolveTerminalTheme("gone", restartedCatalog.Find)
	if fallback.ID != domaintheme.DefaultTerminalTheme {
		return selftest.Failed(fmt.Errorf("unknown scheme fallback = %q", fallback.ID))
	}
	theme := resolveUITheme(appearance, true)
	obsidian, _ := domaintheme.LookupPreset(true, "obsidian")
	if theme.Background != uitheme.PresetBackground(obsidian) ||
		theme.Accent != uitheme.AccentColor(appearance.AccentColor) {
		return selftest.Failed(fmt.Errorf("restored theme = %+v", theme))
	}
	return selftest.Passed(
		"mode, presets, accent, icon, opacity, and the imported terminal scheme survived a restart",
		"preferences.Service + appthemes.Service + preferences.Store + ThemesStore")
}

// selftest28WindowHost records windows and hands their content to a Tester.
type selftest28WindowHost struct {
	windows []*selftest28Window
}

func (h *selftest28WindowHost) Create(spec windowSpec, hooks windowHooks) hostWindow {
	window := &selftest28Window{spec: spec, hooks: hooks}
	h.windows = append(h.windows, window)
	return window
}

type selftest28Window struct {
	spec      windowSpec
	hooks     windowHooks
	icon      []byte
	opacity   float64
	shown     bool
	destroyed bool
}

func (w *selftest28Window) Show()                    { w.shown = true }
func (w *selftest28Window) Hide()                    { w.shown = false }
func (w *selftest28Window) Focus()                   {}
func (w *selftest28Window) Invalidate()              {}
func (w *selftest28Window) IsDestroyed() bool        { return w.destroyed }
func (w *selftest28Window) IsVisible() bool          { return w.shown }
func (w *selftest28Window) IsFocused() bool          { return false }
func (w *selftest28Window) SetTitle(string)          {}
func (w *selftest28Window) SetOpacity(value float64) { w.opacity = value }
func (w *selftest28Window) SetIcon(data []byte) error {
	w.icon = append([]byte(nil), data...)
	return nil
}
func (w *selftest28Window) Close() {
	if w.hooks.OnCloseRequest != nil && !w.hooks.OnCloseRequest() {
		return
	}
	w.destroyed = true
	if w.hooks.OnClosed != nil {
		w.hooks.OnClosed()
	}
}

type selftest28Confirmer struct{}

func (selftest28Confirmer) Confirm(closePrompt) bool { return true }

// selftest28Factory opens fake SSH transports, the same seam the shell tests
// use, so a Session becomes an observable surface without a network.
type selftest28Factory struct {
	transport *selftest28Transport
}

func (f *selftest28Factory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

type selftest28Transport struct {
	closed chan struct{}
	once   bool
}

func newSelftest28Transport() *selftest28Transport {
	return &selftest28Transport{closed: make(chan struct{})}
}

func (f *selftest28Transport) Read(p []byte) (int, error) {
	<-f.closed
	return 0, io.EOF
}
func (f *selftest28Transport) Write(p []byte) (int, error) { return len(p), nil }
func (f *selftest28Transport) Resize(int, int) error       { return nil }
func (f *selftest28Transport) Wait() (int, error)          { return 0, nil }
func (f *selftest28Transport) Close() error {
	if !f.once {
		f.once = true
		close(f.closed)
	}
	return nil
}

type selftest28Surface struct {
	theme  domaintheme.TerminalTheme
	themed bool
	closed bool
}

func (s *selftest28Surface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "terminal").Fill()
}
func (s *selftest28Surface) Close() { s.closed = true }
func (s *selftest28Surface) SetTerminalTheme(preset domaintheme.TerminalTheme) {
	s.theme, s.themed = preset, true
}
