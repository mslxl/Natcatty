// Package surface holds the small platform questions the Terminal surfaces
// need: which font family a CSS stack resolves to on this machine, and the
// font the terminal itself is configured to draw with (recorded by the Session
// surface and read by the views that draw terminal text themselves).
package surface

import (
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// The stacks are Terminal's own, from its CSS:
//
//	--mono: 'Berkeley Mono', ui-monospace, 'SF Mono', SFMono-Regular, Menlo, monospace;
//	--sans: 'Geist', -apple-system, 'SF Pro Text', system-ui, -apple-system, sans-serif;
//
// ui-monospace, system-ui and -apple-system are keywords, not families, so the
// resolver compares the installed families with the named ones in order and falls
// back to Go's generic "monospace" or to the window's own sans.
var (
	monoStack = []string{
		"Berkeley Mono", "SF Mono", "SFMono-Regular", "Menlo",
		"Cascadia Mono", "Consolas", "monospace",
	}
	sansStack = []string{
		"Geist", "SF Pro Text", "Segoe UI Variable Text", "Segoe UI", "sans-serif",
	}
)

var (
	fontsOnce sync.Once
	installed []string

	monoOnce sync.Once
	monoName string

	sansOnce sync.Once
	sansName string
)

// MonoFamily is the family Terminal's --mono stack resolves to here.
func MonoFamily() string {
	monoOnce.Do(func() { monoName = pickFamily(installedFonts(), monoStack) })
	return monoName
}

// SansFamily is the family Terminal's --sans stack resolves to here; "" means the
// window's default sans.
func SansFamily() string {
	sansOnce.Do(func() { sansName = pickFamily(installedFonts(), sansStack) })
	return sansName
}

// The configured terminal font is process-wide: Appearance is one setting for
// the whole app, and the terminal text this app draws itself (the live grid, a
// finished command's rows, a lens view) has no options channel of its own.
var (
	configuredFontMu     sync.Mutex
	configuredFontFamily string
	configuredFontSize   float32
)

// SetTerminalFont records the font the terminal is configured to draw with: the
// same comma-separated family list and size the terminal plugin receives (see
// terminal.Font), not a resolved family or the UI theme's size. The app reports
// it when a Surface is built and again whenever the Appearance settings change.
//
// The rows this app draws itself use it, so a command's rows are the same font
// while they live in the grid and after they move into a block. Empty family or
// zero size means nothing has been reported (tests, a view with no terminal);
// callers fall back to the UI theme for the size and to the terminal plugin's
// own default ("monospace") for the family.
func SetTerminalFont(family string, size float32) {
	configuredFontMu.Lock()
	configuredFontFamily, configuredFontSize = strings.TrimSpace(family), size
	configuredFontMu.Unlock()
}

// TerminalFont is the configured terminal font: the comma-separated family list
// ui.Element.Font takes, and the size in DIPs. A zero size means no setting was
// reported.
func TerminalFont() (family string, size float32) {
	configuredFontMu.Lock()
	defer configuredFontMu.Unlock()
	return configuredFontFamily, configuredFontSize
}

// pickFamily is the first installed family of the stack, matched without case or
// spaces around it; a generic keyword at the end of the stack is kept as-is.
func pickFamily(have []string, stack []string) string {
	for _, want := range stack {
		if isGeneric(want) {
			return want
		}
		for _, got := range have {
			if sameFamily(got, want) {
				return got
			}
		}
	}
	if len(stack) > 0 {
		return stack[len(stack)-1]
	}
	return ""
}

func isGeneric(name string) bool {
	switch name {
	case "monospace", "sans-serif", "serif", "system-ui", "ui-monospace", "-apple-system":
		return true
	}
	return false
}

func sameFamily(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func installedFonts() []string {
	fontsOnce.Do(func() {
		// Prefer the families the renderer itself offers: those are the names
		// ui.Element.Font accepts, so a resolved stack entry is one text can use.
		for _, f := range ui.FontFamilies() {
			installed = append(installed, f.Name)
		}
		if len(installed) == 0 {
			installed = readInstalledFonts()
		}
	})
	return installed
}
