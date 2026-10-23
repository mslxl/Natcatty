// Package theme turns the domain appearance catalog into MyGo themes. It is
// the only place that maps the original's shadcn tokens onto MyGo's widget and
// terminal color fields.
package theme

import (
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

// Options describe the live appearance around a preset. Accent and font are
// applied on top of the preset's tokens, as the original's CSS variables were.
type Options struct {
	// CustomAccent selects AccentHSL over the preset's accent token.
	CustomAccent bool
	// AccentHSL is the custom accent token; ignored when CustomAccent is false.
	AccentHSL string
	// Font is the interface font family. Empty uses the system font.
	Font string
	// FontSize is the interface font size. Zero uses 14 DIPs.
	FontSize float32
}

// UI builds the MyGo widget theme for one preset.
func UI(dark bool, preset domaintheme.Preset, options Options) *ui.Theme {
	tokens := preset.Tokens
	background := hsl(tokens.Background, "0 0% 100%")
	foreground := hsl(tokens.Foreground, "0 0% 0%")
	card := hsl(tokens.Card, tokens.Background)
	border := hsl(tokens.Border, tokens.Background)
	textMuted := hsl(tokens.MutedForeground, tokens.Foreground)

	accentToken := tokens.Accent
	accent := hsl(accentToken, tokens.Primary)
	if options.CustomAccent {
		if parsed, ok := domaintheme.ParseHSL(options.AccentHSL); ok {
			accent = color(parsed)
			accentToken = options.AccentHSL
		}
	}
	accentText := hsl(domaintheme.ReadableForeground(accentToken,
		tokens.PrimaryForeground), "0 0% 100%")

	hover := mix(accent, background, 0.10)
	pressed := mix(accent, background, 0.20)
	surfaceHover := mix(card, foreground, 0.06)
	surfacePressed := mix(card, foreground, 0.12)

	selectionAlpha, focusAlpha := float32(0.25), float32(0.55)
	if dark {
		selectionAlpha, focusAlpha = 0.35, 0.65
	}
	warning := hsl("38 92% 50%", "38 92% 50%")
	success := hsl("142.1 76.2% 36.3%", "142.1 76.2% 36.3%")
	if dark {
		warning = hsl("38 92% 58%", "38 92% 58%")
		success = hsl("142 69% 50%", "142 69% 50%")
	}
	fontSize := options.FontSize
	if fontSize <= 0 {
		fontSize = 14
	}
	return &ui.Theme{
		Dark:           dark,
		Background:     background,
		Surface:        card,
		SurfaceHover:   surfaceHover,
		SurfacePressed: surfacePressed,
		Border:         border,
		Text:           foreground,
		TextMuted:      textMuted,
		Accent:         accent,
		AccentHover:    hover,
		AccentPressed:  pressed,
		AccentText:     accentText,
		Danger:         hsl(tokens.Destructive, "0 70% 50%"),
		Warning:        warning,
		Success:        success,
		Selection:      withAlpha(accent, selectionAlpha),
		Focus:          withAlpha(accent, focusAlpha),
		Inverse:        foreground,
		InverseText:    background,
		Scrollbar:      withAlpha(foreground, 0.30),
		ScrollbarWidth: 8,
		Radius:         6,
		Spacing:        4,
		FontSize:       fontSize,
		Font:           options.Font,
	}
}

// Terminal builds MyGo's terminal colors from a scheme.
func Terminal(preset domaintheme.TerminalTheme) *terminal.Theme {
	colors := preset.Colors
	foreground := hex(colors.Foreground, "#c9d1d9")
	cursor := foreground
	if domaintheme.ValidHex(colors.Cursor) {
		cursor = ui.Hex(colors.Cursor)
	}
	selection := hex(colors.Selection, "#264f78")
	return &terminal.Theme{
		Foreground: foreground,
		Background: hex(colors.Background, "#0d1117"),
		Cursor:     cursor,
		Selection:  selection,
		Palette: [16]ui.Color{
			hex(colors.Black, "#000000"),
			hex(colors.Red, "#ff0000"),
			hex(colors.Green, "#00ff00"),
			hex(colors.Yellow, "#ffff00"),
			hex(colors.Blue, "#0000ff"),
			hex(colors.Magenta, "#ff00ff"),
			hex(colors.Cyan, "#00ffff"),
			hex(colors.White, "#cccccc"),
			hex(colors.BrightBlack, "#666666"),
			hex(colors.BrightRed, "#ff0000"),
			hex(colors.BrightGreen, "#00ff00"),
			hex(colors.BrightYellow, "#ffff00"),
			hex(colors.BrightBlue, "#0000ff"),
			hex(colors.BrightMagenta, "#ff00ff"),
			hex(colors.BrightCyan, "#00ffff"),
			hex(colors.BrightWhite, "#ffffff"),
		},
	}
}

// DefaultTerminal returns the original's Netcatty Dark scheme.
func DefaultTerminal() *terminal.Theme {
	preset, _ := domaintheme.LookupTerminalTheme(domaintheme.DefaultTerminalTheme)
	return Terminal(preset)
}

// AccentColor returns the color a swatch or preview should use.
func AccentColor(token string) ui.Color { return hsl(token, "221.2 83.2% 53.3%") }

// AccentText returns the readable text color on an accent token, using the
// same contrast rule as the original's primary-foreground token.
func AccentText(token string) ui.Color {
	return hsl(domaintheme.ReadableForeground(token, "0 0% 100%"), "0 0% 100%")
}

// PresetBackground returns a preset's background, for picker swatches.
func PresetBackground(preset domaintheme.Preset) ui.Color {
	return hsl(preset.Tokens.Background, "0 0% 100%")
}

// PresetForeground returns the text color readable on the preset's own
// background, for picker swatches painted with it.
func PresetForeground(preset domaintheme.Preset) ui.Color {
	return hsl(domaintheme.ReadableForeground(preset.Tokens.Background,
		preset.Tokens.Foreground), "0 0% 0%")
}

// TerminalBackground returns a scheme's background, for picker swatches.
func TerminalBackground(preset domaintheme.TerminalTheme) ui.Color {
	return hex(preset.Colors.Background, "#000000")
}

// TerminalForeground returns a scheme's foreground, for picker swatches.
func TerminalForeground(preset domaintheme.TerminalTheme) ui.Color {
	return hex(preset.Colors.Foreground, "#ffffff")
}

func hsl(token, fallback string) ui.Color {
	parsed, ok := domaintheme.ParseHSL(token)
	if !ok {
		parsed, ok = domaintheme.ParseHSL(fallback)
	}
	if !ok {
		return ui.Transparent
	}
	return color(parsed)
}

func color(hsl domaintheme.HSL) ui.Color {
	r, g, b := hsl.RGB()
	return ui.RGB(r, g, b)
}

func mix(from, to ui.Color, weight float64) ui.Color {
	blend := func(a, b uint8) uint8 {
		return uint8(float64(a) + (float64(b)-float64(a))*weight + 0.5)
	}
	return ui.RGBA(blend(from.R, to.R), blend(from.G, to.G), blend(from.B, to.B), float32(from.A)/255)
}

func withAlpha(source ui.Color, alpha float32) ui.Color {
	return ui.RGBA(source.R, source.G, source.B, alpha)
}

func hex(value, fallback string) ui.Color {
	if domaintheme.ValidHex(value) {
		return ui.Hex(value)
	}
	if domaintheme.ValidHex(fallback) {
		return ui.Hex(fallback)
	}
	return ui.Transparent
}
