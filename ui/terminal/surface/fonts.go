// Package surface holds the small platform questions the Terminal surfaces need: which
// font family a CSS stack resolves to on this machine.
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
