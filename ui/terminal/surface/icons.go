package surface

import (
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// Terminal's icons are inline 16-grid stroke drawings
// (`viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.45"`,
// round caps and joins), painted at the size the rule gives them. These are the
// drawings copied out of the bundle, with the same attributes, so they render as
// Terminal's do.
const (
	iconOpen  = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.45" stroke-linecap="round" stroke-linejoin="round">`
	iconClose = `</svg>`
)

func parseIcon(body string) *ui.SVG { return ui.MustParseSVG([]byte(iconOpen + body + iconClose)) }

var (
	iconsOnce sync.Once
	iconSet   = map[string]*ui.SVG{}
)

func loadIcons() {
	iconSet["copy"] = parseIcon(`<rect x="5.25" y="5.25" width="8.5" height="8.5" rx="1.5"/><path d="M10.75 5.25v-1.5a1.5 1.5 0 0 0-1.5-1.5h-5.5a1.5 1.5 0 0 0-1.5 1.5v5.5a1.5 1.5 0 0 0 1.5 1.5h1.5"/>`)
	iconSet["book"] = parseIcon(`<path d="M9.88 11.75h-3.12"/><path d="M11.75 9.25h-5"/><path d="M3 14.25h10a1.25 1.25 0 0 0 1.25 -1.25V3a1.25 1.25 0 0 0 -1.25 -1.25H5.5a1.25 1.25 0 0 0 -1.25 1.25v10a1.25 1.25 0 0 1 -2.5 0v-5.62a1.25 1.25 0 0 1 1.25 -1.25h1.25"/><rect x="6.75" y="4.25" width="5" height="2.5" rx="0.62"/>`)
	iconSet["arrow-down"] = parseIcon(`<path d="M8 3v10"/><path d="m4 9 4 4 4-4"/>`)
	// Terminal's disclosure arrow is the down arrow turned to point along the tree; the
	// drawing is the same stroke turned by a quarter (x, y) -> (y, 16 - x).
	iconSet["arrow-right"] = parseIcon(`<path d="M3 8h10"/><path d="m9 12 4-4-4-4"/>`)
	iconSet["file"] = parseIcon(`<path d="M9 1.75H4.25a1.5 1.5 0 0 0-1.5 1.5v9.5a1.5 1.5 0 0 0 1.5 1.5h7.5a1.5 1.5 0 0 0 1.5-1.5V6L9 1.75Z"/><path d="M9 1.75V6h4.25"/>`)
	iconSet["braces"] = parseIcon(`<path d="M5.5 2H5a1.75 1.75 0 0 0-1.75 1.75v2.1c0 .9-.6 1.65-1.5 2.15.9.5 1.5 1.25 1.5 2.15v2.1A1.75 1.75 0 0 0 5 14h.5"/><path d="M10.5 2h.5a1.75 1.75 0 0 1 1.75 1.75v2.1c0 .9.6 1.65 1.5 2.15-.9.5-1.5 1.25-1.5 2.15v2.1A1.75 1.75 0 0 1 11 14h-.5"/>`)
	iconSet["check"] = parseIcon(`<path d="m3.5 8.5 3 3 6-7"/>`)
	iconSet["database"] = parseIcon(`<ellipse cx="8" cy="3.75" rx="5.25" ry="1.75"/><path d="M2.75 3.75v8.5c0 .97 2.35 1.75 5.25 1.75s5.25-.78 5.25-1.75v-8.5M2.75 8c0 .97 2.35 1.75 5.25 1.75S13.25 8.97 13.25 8"/>`)
	iconSet["globe"] = parseIcon(`<circle cx="8" cy="8" r="6.25"/><path d="M1.75 8h12.5M8 1.75c1.75 1.75 2.5 3.9 2.5 6.25S9.75 12.5 8 14.25C6.25 12.5 5.5 10.35 5.5 8S6.25 3.5 8 1.75z"/>`)
	iconSet["package"] = parseIcon(`<path d="M2.75 2.75h8.1l2.4 2.4v8.1H2.75z"/><path d="M5.25 2.9v2.6h4.5V2.9M4.75 13.1V9.25h6.5v3.85"/>`)
	iconSet["terminal"] = parseIcon(`<rect x="1.75" y="2.75" width="12.5" height="10.5" rx="2"/><path d="m4.75 6.25 2 1.75-2 1.75M8.5 10h2.75"/>`)
	iconSet["git"] = parseIcon(`<circle cx="11.75" cy="3.62" r="1.88"/><circle cx="4.25" cy="8" r="1.88"/><circle cx="11.75" cy="12.38" r="1.88"/><path d="M5.87 8.94 10.14 11.43"/><path d="M10.13 4.57 5.87 7.06"/>`)
}

// Icon returns one of the bundle's named drawings that the surfaces use.
func Icon(name string) (*ui.SVG, bool) {
	iconsOnce.Do(loadIcons)
	s, ok := iconSet[name]
	return s, ok
}

// CopyIcon is Terminal's copy glyph (the two overlapping sheets): .sf-block-copy .ico
// is 13x13.
func CopyIcon() *ui.SVG {
	s, _ := Icon("copy")
	return s
}

// DisclosureArrow is the arrow a collapsible head turns: down while open, pointing
// along the tree while collapsed.
func DisclosureArrow(open bool) *ui.SVG {
	name := "arrow-down"
	if !open {
		name = "arrow-right"
	}
	s, _ := Icon(name)
	return s
}

// IconForLens names the icon a lens declares. Terminal's lens manifest carries
// `icon: string?` and .sf-block-ic paints it (a 14 DIP box with a 12 DIP drawing);
// these are the names our built-in lenses would declare. An empty name leaves the
// generic mark.
func IconForLens(lensID string) string {
	switch {
	case lensID == "":
		return ""
	case strings.HasPrefix(lensID, "git."):
		return "git"
	case strings.HasPrefix(lensID, "view."):
		return "file"
	case strings.HasPrefix(lensID, "docs."),
		strings.HasPrefix(lensID, "markdown"),
		strings.HasPrefix(lensID, "man."),
		strings.HasPrefix(lensID, "file.read"),
		strings.HasPrefix(lensID, "file.show"):
		return "book"
	case strings.HasPrefix(lensID, "files."),
		strings.HasPrefix(lensID, "file."):
		return "file"
	case strings.HasPrefix(lensID, "http."),
		strings.HasPrefix(lensID, "net."),
		strings.HasPrefix(lensID, "curl"),
		strings.HasPrefix(lensID, "wget"):
		return "globe"
	case strings.HasPrefix(lensID, "sql."),
		strings.HasPrefix(lensID, "db."),
		strings.HasPrefix(lensID, "psql"),
		strings.HasPrefix(lensID, "mysql"),
		strings.HasPrefix(lensID, "redis"):
		return "database"
	case strings.HasPrefix(lensID, "pkg."),
		strings.HasPrefix(lensID, "npm"),
		strings.HasPrefix(lensID, "pnpm"),
		strings.HasPrefix(lensID, "yarn"),
		strings.HasPrefix(lensID, "brew"),
		strings.HasPrefix(lensID, "apt"),
		strings.HasPrefix(lensID, "pip"),
		strings.HasPrefix(lensID, "cargo"),
		strings.HasPrefix(lensID, "go."),
		strings.HasPrefix(lensID, "docker."),
		strings.HasPrefix(lensID, "js.pkg"),
		strings.HasPrefix(lensID, "js.install"):
		return "package"
	case strings.HasPrefix(lensID, "js.test"),
		strings.HasPrefix(lensID, "js.lint"),
		strings.HasPrefix(lensID, "js.tsc"),
		strings.HasPrefix(lensID, "test."):
		return "check"
	case strings.HasPrefix(lensID, "json."):
		return "braces"
	case strings.HasPrefix(lensID, "shell."),
		strings.HasPrefix(lensID, "exec."),
		strings.HasPrefix(lensID, "bash"),
		strings.HasPrefix(lensID, "pwsh"):
		return "terminal"
	case strings.HasPrefix(lensID, "build."):
		return "package"
	}
	return ""
}
