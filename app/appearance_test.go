package app

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

func TestResolveUIThemeFollowsModePresetAndAccent(t *testing.T) {
	appearance := domainpreferences.Defaults().Appearance
	appearance.ThemeMode = domainpreferences.ThemeSystem
	appearance.LightTheme = "ivory"
	appearance.DarkTheme = "vscode"
	appearance.AccentMode = domainpreferences.AccentCustom
	appearance.AccentColor = "48 96% 53%"

	light := resolveUITheme(appearance, false)
	dark := resolveUITheme(appearance, true)
	if light.Dark || !dark.Dark {
		t.Fatalf("mode mapping light=%v dark=%v", light.Dark, dark.Dark)
	}
	ivory, _ := domaintheme.LookupPreset(false, "ivory")
	vscode, _ := domaintheme.LookupPreset(true, "vscode")
	if light.Background != uitheme.PresetBackground(ivory) ||
		dark.Background != uitheme.PresetBackground(vscode) {
		t.Error("the selected presets were not applied")
	}
	if light.Accent != dark.Accent {
		t.Error("custom accent differs between appearances")
	}
	if light.AccentText != ui.Hex("#000000") {
		t.Errorf("yellow accent text = %v", light.AccentText)
	}

	// An explicit mode ignores the desktop appearance.
	appearance.ThemeMode = domainpreferences.ThemeLight
	if resolveUITheme(appearance, true).Dark {
		t.Error("Light mode followed a dark desktop")
	}
}

func TestResolveTerminalThemeFallsBack(t *testing.T) {
	find := func(id string) (domaintheme.TerminalTheme, bool) {
		if id == "custom-x" {
			return domaintheme.TerminalTheme{ID: "custom-x", Name: "X"}, true
		}
		return domaintheme.TerminalTheme{}, false
	}
	if got := resolveTerminalTheme("custom-x", find); got.ID != "custom-x" {
		t.Errorf("stored scheme = %+v", got)
	}
	if got := resolveTerminalTheme("gone", find); got.ID != domaintheme.DefaultTerminalTheme {
		t.Errorf("fallback = %+v", got)
	}
	if got := resolveTerminalTheme("", nil); got.ID != domaintheme.DefaultTerminalTheme {
		t.Errorf("nil finder fallback = %+v", got)
	}
}

func TestIconVariantsDecodeAndTrayIconIsSmall(t *testing.T) {
	seen := make(map[string][]byte)
	for _, variant := range domaintheme.IconVariants() {
		data := appIconPNG(variant.ID)
		image, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", variant.ID, err)
		}
		if got := image.Bounds().Size(); got.X != 1024 || got.Y != 1024 {
			t.Errorf("%s: size %v", variant.ID, got)
		}
		seen[variant.ID] = data
	}
	if bytes.Equal(seen["original"], seen["rainbow"]) {
		t.Error("the rainbow variant reused the original artwork")
	}
	if len(appIconPNG("unknown")) == 0 {
		t.Error("an unknown variant returned no icon")
	}

	tray, err := png.Decode(bytes.NewReader(trayIconPNG("rainbow")))
	if err != nil {
		t.Fatal(err)
	}
	if got := tray.Bounds().Size(); got.X != 32 || got.Y != 32 {
		t.Errorf("tray size = %v", got)
	}
	glyph, err := png.Decode(bytes.NewReader(trayIconPNG("original")))
	if err != nil {
		t.Fatal(err)
	}
	if got := glyph.Bounds().Size(); got.X != 32 || got.Y != 32 {
		t.Errorf("default tray size = %v", got)
	}
}

func TestWindowManagerAppliesThemeAndIconToOpenAndFutureWindows(t *testing.T) {
	host := &fakeWindowHost{}
	content := func(c *ui.Context) {
		ui.Text(c, "settings")
	}
	manager := newWindowManager(host, fakeConfirmer{}, nil, nil,
		func() func(*ui.Context) { return content },
		func() string { return "en" })

	theme := &ui.Theme{Dark: true, Background: ui.Hex("#101010"), Text: ui.Hex("#ffffff")}
	manager.SetThemeProvider(func() *ui.Theme { return theme })
	manager.SetIconProvider(func() []byte { return []byte("first") })
	manager.OpenSettings()
	if len(host.windows) != 1 {
		t.Fatalf("windows = %d", len(host.windows))
	}
	window := host.windows[0]
	if !bytes.Equal(window.icon, []byte("first")) {
		t.Errorf("icon = %q", window.icon)
	}

	var got *ui.Theme
	tester := ui.NewTester(manager.withTheme(func(c *ui.Context) { got = c.Theme() }), 100, 100)
	tester.Frame()
	if got != theme {
		t.Errorf("theme provider = %+v", got)
	}

	manager.SetIconProvider(func() []byte { return []byte("second") })
	if !bytes.Equal(window.icon, []byte("second")) {
		t.Errorf("open window did not follow the icon change: %q", window.icon)
	}
}

// fakeConfirmer accepts every close question.
type fakeConfirmer struct{}

func (fakeConfirmer) Confirm(closePrompt) bool { return true }
