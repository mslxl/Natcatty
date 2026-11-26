package preferences

import (
	"runtime"
	"strings"
)

// TerminalFont is one entry of the terminal font catalog, as the original's
// TERMINAL_FONTS: the setting stores ID, and Family is the font list the
// terminal draws with, ending in the system's monospace font so a font the
// machine lacks still lays out on the cell grid.
type TerminalFont struct {
	ID     string
	Name   string
	Family string
}

const (
	// BundledTerminalFontID is the catalog ID of the Maple Mono NF font bundled
	// under resources/fonts/maple-mono-nf and registered at startup.
	BundledTerminalFontID = "maple-mono-nf"
	// BundledTerminalFontFamily is the exact family name registered with MyGo's
	// ui.RegisterFont for the bundled terminal font.
	BundledTerminalFontFamily = "Maple Mono NF"
)

// terminalFonts is the catalog of monospace fonts. The bundled Maple Mono NF
// font is listed first as the cross-platform default; the remaining entries
// preserve the original's IDs so existing settings stay compatible.
var terminalFonts = []TerminalFont{
	{BundledTerminalFontID, BundledTerminalFontFamily, BundledTerminalFontFamily + ", monospace"},
	{"menlo", "Menlo", "Menlo, monospace"},
	{"monaco", "Monaco", "Monaco, monospace"},
	{"consolas", "Consolas", "Consolas, monospace"},
	{"courier-new", "Courier New", "Courier New, monospace"},
	{"source-code-pro", "Source Code Pro", "Source Code Pro, monospace"},
	{"fira-code", "Fira Code", "Fira Code, monospace"},
	{"fira-mono", "Fira Mono", "Fira Mono, monospace"},
	{"inconsolata", "Inconsolata", "Inconsolata, monospace"},
	{"dejavu-sans-mono", "DejaVu Sans Mono", "DejaVu Sans Mono, monospace"},
	{"liberation-mono", "Liberation Mono", "Liberation Mono, monospace"},
	{"jetbrains-mono", "JetBrains Mono", "JetBrains Mono, monospace"},
	{"victor-mono", "Victor Mono", "Victor Mono, monospace"},
	{"cascadia-code", "Cascadia Code", "Cascadia Code, monospace"},
	{"cascadia-mono", "Cascadia Mono", "Cascadia Mono, monospace"},
	{"droid-sans-mono", "Droid Sans Mono", "Droid Sans Mono, monospace"},
	{"ubuntu-mono", "Ubuntu Mono", "Ubuntu Mono, monospace"},
	{"roboto-mono", "Roboto Mono", "Roboto Mono, monospace"},
	{"ibm-plex-mono", "IBM Plex Mono", "IBM Plex Mono, monospace"},
	{"space-mono", "Space Mono", "Space Mono, monospace"},
	{"input-mono", "Input Mono", "Input Mono, monospace"},
	{"hack", "Hack", "Hack, monospace"},
	{"anonymous-pro", "Anonymous Pro", "Anonymous Pro, monospace"},
	{"programmer-fonts", "Programmer Fonts", "Programmer Fonts, monospace"},
	{"pt-mono", "PT Mono", "PT Mono, monospace"},
	{"iosevka", "Iosevka", "Iosevka, monospace"},
	{"ioskeley-mono", "Ioskeley Mono", "Ioskeley Mono, monospace"},
	{"mononoki", "Mononoki", "Mononoki, monospace"},
	{"go-mono", "Go Mono", "Go Mono, monospace"},
	{"overpass-mono", "Overpass Mono", "Overpass Mono, monospace"},
	{"sarasa-mono-sc", "Sarasa Mono SC", "Sarasa Mono SC, monospace"},
	{"sarasa-mono-tc", "Sarasa Mono TC", "Sarasa Mono TC, monospace"},
	{"maple-mono-cn", "Maple Mono CN", "Maple Mono CN, monospace"},
	{"lxgw-wenkai-mono", "LXGW WenKai Mono", "LXGW WenKai Mono, monospace"},
}

// TerminalFonts returns the catalog in the original's order.
func TerminalFonts() []TerminalFont {
	return append([]TerminalFont(nil), terminalFonts...)
}

// LookupTerminalFont returns the catalog entry with the id.
func LookupTerminalFont(id string) (TerminalFont, bool) {
	for _, font := range terminalFonts {
		if font.ID == id {
			return font, true
		}
	}
	return TerminalFont{}, false
}

// TerminalFontFamily is the font list the terminal draws a setting with: a
// catalog id's family, or a family typed by hand followed by the system's
// monospace font. It always ends in monospace, so text never falls back to
// a proportional font.
func TerminalFontFamily(setting string) string {
	setting = strings.TrimSpace(setting)
	if font, ok := LookupTerminalFont(setting); ok {
		return font.Family
	}
	if setting == "" {
		return "monospace"
	}
	return setting + ", monospace"
}

// DefaultTerminalFontFor returns the default terminal font ID for goos.
// Natcatty bundles Maple Mono NF on every desktop and phone target, and its
// family stack still falls back to monospace when unavailable.
func DefaultTerminalFontFor(goos string) string {
	_ = goos
	return BundledTerminalFontID
}

// DefaultTerminalFont is the default font id on this OS.
var DefaultTerminalFont = DefaultTerminalFontFor(runtime.GOOS)
