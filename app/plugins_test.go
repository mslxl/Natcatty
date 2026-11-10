package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo"

	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	adapterwasm "github.com/mslxl/natcatty/adapters/wasm"
	"github.com/mslxl/natcatty/application/command"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	appsearch "github.com/mslxl/natcatty/application/search"
	appview "github.com/mslxl/natcatty/application/view"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/internal/testutil"
	uisearch "github.com/mslxl/natcatty/ui/search"
)

// TestDevelopmentPluginCompositionSmoke is the walking-skeleton end-to-end
// path: a real wazero module from a development directory registers a
// command, menu item, and keybinding; each surface runs it; unload removes
// all three and stale calls fail. It composes only the plugin subsystem, not
// the rest of the app.
func TestDevelopmentPluginCompositionSmoke(t *testing.T) {
	dir := filepath.Join(testutil.ModuleRoot(t), "adapters", "wasm", "testdata", "devplugin")
	registry := command.NewRegistry()
	surface := newPluginSurface(registry)
	var outputs []string
	container := appplugin.NewContainer(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		surface,
		appplugin.WithFeedback(func(_ appplugin.PluginRef, text string) {
			outputs = append(outputs, text)
		}),
	)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	if status := container.RuntimeStatus(); status.Engine == appplugin.EngineUnknown {
		t.Fatal("the container did not expose the engine mode for diagnostics")
	}

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	if fiber.State() != domainplugin.StateActive {
		t.Fatalf("state = %s, want ACTIVE (%s)", fiber.State(), fiber.Reason())
	}
	if !fiber.Development() {
		t.Fatal("the plugin is not labelled as a development plugin")
	}

	commandID := command.ID(domainplugin.QualifiedCommandID("acme.hello", "greet"))
	if !registry.Has(commandID) {
		t.Fatalf("command %q is not registered", commandID)
	}

	// The command palette lists the contributed command with its accelerator.
	palette := &palette{
		registry: func() *command.Registry { return registry },
		plugins:  surface,
	}
	records := palette.records(uisearch.ModeCommandPalette, "en")
	var paletteRecord = -1
	for i, record := range records {
		if command.ID(record.ID) == commandID {
			paletteRecord = i
			if record.Title != "Say Hello" {
				t.Fatalf("palette title = %q", record.Title)
			}
			if record.Group != uisearch.CategoryApp {
				t.Fatalf("palette group = %q", record.Group)
			}
			if record.Shortcut != "CmdOrCtrl+Alt+G" {
				t.Fatalf("palette shortcut = %q", record.Shortcut)
			}
			break
		}
	}
	if paletteRecord < 0 {
		t.Fatalf("palette records do not contain %q", commandID)
	}

	// The native menu contains the contributed item, and clicking it dispatches
	// through the runtime.
	menu := BuildMenuWithContributions(registry, "en", nil, surface.MenuItems())
	item := findMenuItem(menu.Items(), func(item *mygo.MenuItem) bool {
		return item.Label == "Say Hello"
	})
	if item == nil {
		t.Fatal("the contributed menu item is missing")
	}
	item.Click(item, nil)
	if len(outputs) != 1 || outputs[0] != "hello from plugin" {
		t.Fatalf("menu invocation output = %v", outputs)
	}

	// The keybinding runs the same command.
	shortcuts := surface.Shortcuts()
	if len(shortcuts) != 1 {
		t.Fatalf("shortcuts = %d, want 1", len(shortcuts))
	}
	mods, key, ok := parseAccelerator("CmdOrCtrl+Alt+G")
	if !ok {
		t.Fatal("test accelerator does not parse")
	}
	if shortcuts[0].mods != mods || shortcuts[0].key != key {
		t.Fatalf("shortcut chord = %v/%v, want %v/%v",
			shortcuts[0].mods, shortcuts[0].key, mods, key)
	}
	shortcuts[0].run()
	if len(outputs) != 2 || outputs[1] != "hello from plugin" {
		t.Fatalf("keybinding invocation output = %v", outputs)
	}

	// Unload removes the command, menu item, and keybinding, and the old
	// handler now fails as a stale invocation.
	if err := container.Unload(context.Background(), fiber.ID()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if registry.Has(commandID) {
		t.Fatal("the command survived unload")
	}
	registryErr := registry.Dispatch(context.Background(), commandID)
	var unknown *command.UnknownCommandError
	if !errors.As(registryErr, &unknown) {
		t.Fatalf("stale dispatch = %v, want UnknownCommandError", registryErr)
	}
	if items := surface.MenuItems(); len(items) != 0 {
		t.Fatalf("menu items after unload = %d", len(items))
	}
	if shortcuts := surface.Shortcuts(); len(shortcuts) != 0 {
		t.Fatalf("shortcuts after unload = %d", len(shortcuts))
	}
	if records := palette.records(uisearch.ModeCommandPalette, "en"); containsCommand(records, commandID) {
		t.Fatal("the command stayed in the palette after unload")
	}
}

// TestSampleCommandPluginDevelopmentMode runs the SDK sample through the same
// container and real wazero runtime the app's NATCATTY_PLUGIN_DIR development
// mode uses. The CLI fake host runs the identical module.
func TestSampleCommandPluginDevelopmentMode(t *testing.T) {
	dir := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "hello-command")
	registry := command.NewRegistry()
	surface := newPluginSurface(registry)
	var outputs []string
	container := appplugin.NewContainer(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		surface,
		appplugin.WithFeedback(func(_ appplugin.PluginRef, text string) {
			outputs = append(outputs, text)
		}),
	)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	if fiber.State() != domainplugin.StateActive {
		t.Fatalf("state = %s, want ACTIVE (%s)", fiber.State(), fiber.Reason())
	}
	commandID := command.ID(domainplugin.QualifiedCommandID("acme.hello", "greet"))
	if !registry.Has(commandID) {
		t.Fatalf("command %q is not registered", commandID)
	}
	if err := registry.Dispatch(context.Background(), commandID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(outputs) != 1 || !strings.HasPrefix(outputs[0], "hello from plugin now=") {
		t.Fatalf("visible feedback = %v", outputs)
	}
	if err := container.Unload(context.Background(), fiber.ID()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
}

func TestPluginRuntimeStartupLine(t *testing.T) {
	status := appplugin.RuntimeStatus{
		GOOS: "darwin", GOARCH: "amd64", Engine: appplugin.EngineInterpreter,
		Compatibility: true, Reason: "compiler self-test failed",
	}
	line := pluginRuntimeStartupLine(status)
	if !strings.HasPrefix(line, "natcatty: plugin runtime: engine=interpreter ") {
		t.Fatalf("startup line = %q", line)
	}
	if !strings.Contains(line, "platform=darwin/amd64") || !strings.Contains(line, "compatibility=true") {
		t.Fatalf("startup line = %q", line)
	}

	unavailable := pluginRuntimeStartupLine(appplugin.RuntimeStatus{
		GOOS: "windows", GOARCH: "arm64", Engine: appplugin.EngineUnavailable,
	})
	if !strings.Contains(unavailable, "engine=unavailable") ||
		!strings.Contains(unavailable, "windows/arm64") {
		t.Fatalf("unavailable startup line = %q", unavailable)
	}
}

// TestPluginSurfaceLocalizesCommandsAndMenusAtDisplayTime proves a locale
// change updates the palette and menu without re-registering a contribution.
func TestPluginSurfaceLocalizesCommandsAndMenusAtDisplayTime(t *testing.T) {
	registry := command.NewRegistry()
	surface := newPluginSurface(registry)
	ref := appplugin.PluginRef{ID: "acme.i18n", Name: "I18n", Version: "1.0.0"}
	translations := map[string]string{
		"command_greet": "Saludar",
		"menu_greet":    "Saludo",
	}
	surface.SetLocalizer(func(_ appplugin.PluginRef, key, literal string) string {
		if translated, ok := translations[key]; ok {
			return translated
		}
		return literal
	})
	disposeCommand, err := surface.RegisterCommand(ref, domainplugin.Command{
		ID: "greet", Title: "Say Hello", TitleKey: "command_greet",
	}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}
	t.Cleanup(func() { _ = disposeCommand(context.Background()) })
	disposeMenu, err := surface.RegisterMenu(ref, domainplugin.Menu{
		Command: "greet", Label: "Say Hello", LabelKey: "menu_greet",
	})
	if err != nil {
		t.Fatalf("RegisterMenu: %v", err)
	}
	t.Cleanup(func() { _ = disposeMenu(context.Background()) })

	commandID := command.ID(domainplugin.QualifiedCommandID(ref.ID, "greet"))
	presentation, ok := surface.Presentation(commandID)
	if !ok || presentation.title != "Saludar" {
		t.Fatalf("presentation = %+v, ok = %v", presentation, ok)
	}
	menuLabel := func() string {
		t.Helper()
		for _, item := range surface.MenuItems() {
			if item.Label != "" {
				return item.Label
			}
		}
		t.Fatal("the contributed menu item is missing")
		return ""
	}
	if got := menuLabel(); got != "Saludo" {
		t.Fatalf("menu label = %q", got)
	}

	translations["command_greet"] = "Saluer"
	translations["menu_greet"] = "Salutation"
	surface.RefreshLocale()
	presentation, _ = surface.Presentation(commandID)
	if presentation.title != "Saluer" || menuLabel() != "Salutation" {
		t.Fatalf("labels after locale change = %q / %q", presentation.title, menuLabel())
	}

	// Removing a translation falls back to the manifest literal.
	delete(translations, "command_greet")
	delete(translations, "menu_greet")
	surface.RefreshLocale()
	presentation, _ = surface.Presentation(commandID)
	if presentation.title != "Say Hello" || menuLabel() != "Say Hello" {
		t.Fatalf("literal fallback = %q / %q", presentation.title, menuLabel())
	}
}

func containsCommand(records []appsearch.Record, id command.ID) bool {
	for _, record := range records {
		if command.ID(record.ID) == id {
			return true
		}
	}
	return false
}

// TestKeybindingDisposerKeepsASurvivingBinding covers one command with two
// accelerators: disposing one must not erase the other from the palette.
func TestKeybindingDisposerKeepsASurvivingBinding(t *testing.T) {
	registry := command.NewRegistry()
	surface := newPluginSurface(registry)
	ref := appplugin.PluginRef{ID: "acme.keys", Name: "Keys", Development: true}
	commandID := command.ID(domainplugin.QualifiedCommandID(ref.ID, "greet"))

	disposeCommand, err := surface.RegisterCommand(ref,
		domainplugin.Command{ID: "greet", Title: "Say Hello"},
		func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}
	t.Cleanup(func() { _ = disposeCommand(context.Background()) })

	disposeFirst, err := surface.RegisterKeybinding(ref,
		domainplugin.Keybinding{Command: "greet", Accelerator: "Ctrl+Alt+K"})
	if err != nil {
		t.Fatalf("RegisterKeybinding(first): %v", err)
	}
	disposeSecond, err := surface.RegisterKeybinding(ref,
		domainplugin.Keybinding{Command: "greet", Accelerator: "Ctrl+Alt+L"})
	if err != nil {
		t.Fatalf("RegisterKeybinding(second): %v", err)
	}

	presentation := func() pluginCommandPresentation {
		t.Helper()
		got, ok := surface.Presentation(commandID)
		if !ok {
			t.Fatal("the command has no presentation")
		}
		return got
	}
	if got := presentation().chord; got != "Ctrl+Alt+L" {
		t.Fatalf("chord = %q, want the latest binding", got)
	}
	if err := disposeSecond(context.Background()); err != nil {
		t.Fatalf("dispose second binding: %v", err)
	}
	if got := presentation().chord; got != "Ctrl+Alt+K" {
		t.Fatalf("chord after latest dispose = %q, want the surviving binding", got)
	}
	if err := disposeFirst(context.Background()); err != nil {
		t.Fatalf("dispose first binding: %v", err)
	}
	if got := presentation().chord; got != "" {
		t.Fatalf("chord after both disposals = %q, want empty", got)
	}
}

func TestTruncateUTF8KeepsWholeRunes(t *testing.T) {
	text := strings.Repeat("世", 2000) // 6000 bytes; 4000 is mid-rune
	got := truncateUTF8(text, 4000)
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a UTF-8 rune")
	}
	if len(got) > 4000 {
		t.Fatalf("truncated length = %d, want at most 4000", len(got))
	}
	if want := strings.Repeat("世", 1333); got != want {
		t.Fatalf("truncated text = %q, want 1333 whole runes", got)
	}
	if got := truncateUTF8("short", 4000); got != "short" {
		t.Fatalf("short text = %q", got)
	}
}

// TestMenuItemsOmitsAnEmptyPluginSection proves that configuring the View
// registry before any plugin contributes a view adds nothing at all to the
// View menu: no lone separator.
func TestMenuItemsOmitsAnEmptyPluginSection(t *testing.T) {
	registry := command.NewRegistry()
	surface := newPluginSurface(registry)
	views := appview.NewRegistry()
	t.Cleanup(views.Close)
	surface.SetViewRegistry(views)
	surface.SetViewOpener(func(string) error { return nil })
	if items := surface.MenuItems(); len(items) != 0 {
		t.Fatalf("empty View menu items = %+v", items)
	}
}
