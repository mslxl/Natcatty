package theme

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

func TestUIMapsPresetTokens(t *testing.T) {
	preset, ok := domaintheme.LookupPreset(false, "snow")
	if !ok {
		t.Fatal("snow missing")
	}
	built := UI(false, preset, Options{Font: "Inter", FontSize: 15})
	background, _ := domaintheme.ParseHSL(preset.Tokens.Background)
	if built.Dark {
		t.Error("light preset built a dark theme")
	}
	if built.Background != hslColor(preset.Tokens.Background) {
		t.Errorf("background = %v, want %v", built.Background, hslColor(preset.Tokens.Background))
	}
	if built.Text != hslColor(preset.Tokens.Foreground) ||
		built.Surface != hslColor(preset.Tokens.Card) ||
		built.Border != hslColor(preset.Tokens.Border) {
		t.Errorf("token mapping = %+v", built)
	}
	if built.Background != ui.RGB(mustRGB(background)) {
		t.Error("background conversion disagreed with HSL.RGB")
	}
	if built.Font != "Inter" || built.FontSize != 15 {
		t.Errorf("font = %q %v", built.Font, built.FontSize)
	}
	if built.SurfaceHover == built.Surface || built.SurfacePressed == built.SurfaceHover {
		t.Error("hover states were not derived")
	}
	if built.AccentText != ui.Hex("#000000") {
		t.Errorf("snow accent text = %v", built.AccentText)
	}
}

func TestUICustomAccentAndContrast(t *testing.T) {
	preset, _ := domaintheme.LookupPreset(true, "midnight")
	themed := UI(true, preset, Options{})
	custom := UI(true, preset, Options{CustomAccent: true, AccentHSL: "48 96% 53%"})
	if themed.Accent == custom.Accent {
		t.Error("custom accent did not replace the preset accent")
	}
	if custom.Accent != ui.RGB(mustRGB(domaintheme.HSL{H: 48, S: 0.96, L: 0.53})) {
		t.Errorf("custom accent = %v", custom.Accent)
	}
	// Yellow is light, so the original's contrast rule picks black text.
	if custom.AccentText != ui.Hex("#000000") {
		t.Errorf("custom accent text = %v", custom.AccentText)
	}
	// A malformed custom token falls back to the preset accent.
	fallback := UI(true, preset, Options{CustomAccent: true, AccentHSL: "nope"})
	if fallback.Accent != themed.Accent {
		t.Error("malformed custom accent was applied")
	}
	if custom.Selection.A == 255 || custom.Focus.A == 255 {
		t.Error("selection and focus lost their alpha")
	}
}

func TestUIFallbackForMalformedPreset(t *testing.T) {
	broken := domaintheme.Preset{Tokens: domaintheme.Tokens{
		Background: "bad", Foreground: "bad", Card: "bad", Border: "bad",
		MutedForeground: "bad", Accent: "bad", Primary: "bad",
		PrimaryForeground: "bad", Destructive: "bad",
	}}
	built := UI(false, broken, Options{})
	if built.Background != ui.Hex("#ffffff") || built.Text != ui.Hex("#000000") {
		t.Errorf("fallbacks = %v / %v", built.Background, built.Text)
	}
}

func TestTerminalMapping(t *testing.T) {
	preset, _ := domaintheme.LookupTerminalTheme("netcatty-dark")
	built := Terminal(preset)
	if built.Background != ui.Hex("#0d1117") || built.Foreground != ui.Hex("#c9d1d9") {
		t.Errorf("background/foreground = %v / %v", built.Background, built.Foreground)
	}
	if built.Cursor != ui.Hex("#58a6ff") || built.Selection != ui.Hex("#264f78") {
		t.Errorf("cursor/selection = %v / %v", built.Cursor, built.Selection)
	}
	if built.Palette[0] != ui.Hex("#0d1117") || built.Palette[1] != ui.Hex("#ff7b72") ||
		built.Palette[15] != ui.Hex("#f0f6fc") {
		t.Errorf("palette = %v", built.Palette)
	}
	if DefaultTerminal().Background != built.Background {
		t.Error("default terminal is not Netcatty Dark")
	}
}

func TestMalformedTerminalColorsUseFallbacks(t *testing.T) {
	broken := domaintheme.TerminalTheme{Colors: domaintheme.TerminalColors{
		Background: "nope", Foreground: "nope", Cursor: "nope", Selection: "nope",
	}}
	built := Terminal(broken)
	if built.Background != ui.Hex("#0d1117") || built.Foreground != ui.Hex("#c9d1d9") {
		t.Errorf("terminal fallbacks = %v / %v", built.Background, built.Foreground)
	}
	if built.Cursor != built.Foreground {
		t.Error("cursor fallback should follow the foreground")
	}
}

func hslColor(token string) ui.Color {
	parsed, _ := domaintheme.ParseHSL(token)
	r, g, b := parsed.RGB()
	return ui.RGB(r, g, b)
}

func mustRGB(c domaintheme.HSL) (uint8, uint8, uint8) { return c.RGB() }
