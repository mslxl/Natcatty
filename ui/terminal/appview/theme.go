package appview

import (
	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/ui/terminal"

	"github.com/mslxl/natcatty/ui/terminal/theme"
)

// TerminalColors is the palette the self-drawn terminal grid uses. It mirrors
// the colors passed to MyGo's native terminal, while retaining Terminal's palette
// as the fallback for appview-only tests and callers that do not have a
// Natcatty terminal yet.
type TerminalColors struct {
	Foreground ui.Color
	Background ui.Color
	Palette    [16]ui.Color
}

func terminalColors(theme *terminal.Theme) TerminalColors {
	if theme != nil {
		return TerminalColors{
			Foreground: theme.Foreground,
			Background: theme.Background,
			Palette:    theme.Palette,
		}
	}
	fg, bg := terminaltheme.Terminal()
	return TerminalColors{Foreground: fg, Background: bg, Palette: terminaltheme.ANSI()}
}

// TermColors returns the terminal foreground and background used by the live
// screen. The context argument is retained for compatibility with the original
// appview helper; the optional terminal theme is the authoritative Natcatty
// terminal palette when the live Session supplies one.
func TermColors(_ *ui.Context, themes ...*terminal.Theme) (fg, bg ui.Color) {
	var theme *terminal.Theme
	if len(themes) > 0 {
		theme = themes[0]
	}
	colors := terminalColors(theme)
	return colors.Foreground, colors.Background
}

// termColorsFor is kept separate from TermColors because the screen replay also
// needs the 16-color ANSI palette for SGR 30-37/90-97 and xterm-256 colors.
func termColorsFor(theme *terminal.Theme) TerminalColors {
	return terminalColors(theme)
}

// applyDefaults gives every span without an explicit colour the terminal's default
// foreground, so text with no SGR of its own reads as terminal text.
func applyDefaults(lines [][]ui.Span, fg ui.Color) {
	for _, spans := range lines {
		for i := range spans {
			if spans[i].Color == (ui.Color{}) {
				spans[i].Color = fg
			}
		}
	}
}
