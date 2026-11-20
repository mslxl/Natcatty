// Package terminaltheme carries Terminal 0.9.4's own dark and light themes.
//
// Terminal has no theme format of its own: flow.exe embeds omp's theme catalog
// (themes/omp.json, 315,276 bytes at flow.exe@0x91342FB, generated from omp
// commit 8ebb7daf2ee0735c5f6047f4da93795355f8706b) and draws the theme its
// appearance picks as its own colours. Terminal's default appearance is Light; its
// dark appearance follows omp's theme.dark, which with no omp configuration
// resolves to the catalog's bare "dark" entry. Each Theme here is that entry's
// numbers ("light" is the bare "light" one), read from the catalog:
//
//	terminal  background foreground ansi[16]   the grid
//	export    pageBg cardBg                    the page and the cards
//	terminal  chrome                           the window chrome
//	colors    accent borderMuted muted error warning success selectedBg
//
// This package is a leaf so that both the app view (internal/appview) and the
// lens renderer (internal/lensview), which draws its ANSI mini terminal in the
// same colours, read one copy of the themes. Evidence:
// Terminal-0.9.4-主题与终端配色分析.md and findings/theme/catalog.json in the
// research checkout this code came from.
package terminaltheme

import "github.com/egoist/mygo/ui"

// theme is one entry of the catalog, resolved: the terminal colours a terminal
// draws with, the surfaces a window draws with, and the semantic colours Terminal
// compiles into its CSS variables.
type theme struct {
	dark bool
	// The terminal's own colours: terminal.background and terminal.foreground.
	// They are the grid's defaults (SGR 39/49) and the pane's fill.
	fg, bg ui.Color
	// ansi is terminal.ansi[16]: the grid's 16-colour palette, whose bright half
	// a program reaches with SGR 90-97.
	ansi [16]ui.Color
	// The window's own colours: export.pageBg fills the window behind the panes,
	// export.cardBg the cards and controls (Terminal's terminal.widget), and
	// terminal.chrome the chrome Terminal draws its status line and headers in.
	page, card, chrome ui.Color
	// The semantic colours Terminal compiles into its CSS variables: colors.text
	// (empty in the catalog, which Terminal reads as the terminal foreground),
	// colors.muted, colors.borderMuted, colors.accent, colors.error,
	// colors.warning, colors.success and colors.selectedBg.
	text, muted, border, accent     ui.Color
	danger, warn, success, selected ui.Color
}

// darkTheme is the catalog's "dark" entry.
var darkTheme = theme{
	dark: true,
	bg:   ui.Hex("#18181e"),
	fg:   ui.Hex("#cccccc"),
	ansi: [16]ui.Color{
		ui.Hex("#000000"), ui.Hex("#cd3131"), ui.Hex("#0dbc79"), ui.Hex("#e5e510"),
		ui.Hex("#2472c8"), ui.Hex("#bc3fbc"), ui.Hex("#11a8cd"), ui.Hex("#e5e5e5"),
		ui.Hex("#666666"), ui.Hex("#f14c4c"), ui.Hex("#23d18b"), ui.Hex("#f5f543"),
		ui.Hex("#3b8eea"), ui.Hex("#d670d6"), ui.Hex("#29b8db"), ui.Hex("#e5e5e5"),
	},
	page:     ui.Hex("#18181e"),
	card:     ui.Hex("#1e1e24"),
	chrome:   ui.Hex("#121212"),
	text:     ui.Hex("#cccccc"),
	muted:    ui.Hex("#777d88"),
	border:   ui.Hex("#3d424a"),
	accent:   ui.Hex("#febc38"),
	danger:   ui.Hex("#fc3a4b"),
	warn:     ui.Hex("#e4c00f"),
	success:  ui.Hex("#89d281"),
	selected: ui.Hex("#31363f"),
}

// lightTheme is the catalog's "light" entry.
var lightTheme = theme{
	dark: false,
	bg:   ui.Hex("#f8f8f8"),
	fg:   ui.Hex("#3b3b3b"),
	ansi: [16]ui.Color{
		ui.Hex("#000000"), ui.Hex("#cd3131"), ui.Hex("#107c10"), ui.Hex("#919514"),
		ui.Hex("#0451a5"), ui.Hex("#bc05bc"), ui.Hex("#0598bc"), ui.Hex("#555555"),
		ui.Hex("#666666"), ui.Hex("#f14c4c"), ui.Hex("#09a60a"), ui.Hex("#919516"),
		ui.Hex("#3b8eea"), ui.Hex("#ce68ce"), ui.Hex("#1a9cba"), ui.Hex("#a5a5a5"),
	},
	page:     ui.Hex("#f8f8f8"),
	card:     ui.Hex("#ffffff"),
	chrome:   ui.Hex("#e0e0e0"),
	text:     ui.Hex("#3b3b3b"),
	muted:    ui.Hex("#6c6c6c"),
	border:   ui.Hex("#b0b0b0"),
	accent:   ui.Hex("#5a8080"),
	danger:   ui.Hex("#aa5555"),
	warn:     ui.Hex("#9a7326"),
	success:  ui.Hex("#588458"),
	selected: ui.Hex("#d0d0e0"),
}

// mygoTheme is the MyGo theme Terminal's appearance draws its widgets with.
//
// Terminal's CSS derives the surfaces above and below a card's fill from the
// theme's ink at 6/9/14/22% (themes.css: --l1..--l4), and its selection from
// the theme's selected background at 22% (light) or 34% (dark) (--tv-sel); the
// same arithmetic is done here so a hover or a selection sits in the theme's
// own colour family, as it does in Terminal.
func (t *theme) mygoTheme() *ui.Theme {
	surface, ink := t.card, t.text
	alpha := float32(0.34)
	if !t.dark {
		alpha = 0.22
	}
	return &ui.Theme{
		Dark:           t.dark,
		Background:     t.page,
		Surface:        surface,
		SurfaceHover:   surface.Mix(ink, 0.06),
		SurfacePressed: surface.Mix(ink, 0.14),
		Border:         t.border,
		Text:           t.text,
		TextMuted:      t.muted,
		Accent:         t.accent,
		AccentHover:    t.accent.Mix(ui.Hex("#ffffff"), 0.18),
		AccentPressed:  t.accent.Mix(ui.Hex("#000000"), 0.18),
		AccentText:     t.bg,
		Danger:         t.danger,
		Warning:        t.warn,
		Success:        t.success,
		Selection:      t.selected.Alpha(alpha),
		Focus:          t.accent.Alpha(0.55),
		Inverse:        surface,
		InverseText:    t.text,
		Scrollbar:      ink.Alpha(0.3),
		Radius:         8, // Terminal's --r-ctl
		Spacing:        4,
		FontSize:       13, // Terminal's DEFAULT_FONT, the grid's size the UI follows
	}
}

var (
	darkUI  = darkTheme.mygoTheme()
	lightUI = lightTheme.mygoTheme()
	// current is the appearance in use; it opens dark, which this app asks for
	// (Terminal's own default is light).
	current = &darkTheme
)

// Use selects Terminal's dark or light appearance. Call it before the window is
// built, and again if the appearance changes.
func Use(dark bool) {
	if dark {
		current = &darkTheme
		return
	}
	current = &lightTheme
}

// Theme is MyGo's theme of the Terminal appearance in use.
func Theme() *ui.Theme {
	if current.dark {
		return darkUI
	}
	return lightUI
}

// Terminal is the appearance's terminal.background and terminal.foreground: the
// grid's defaults, and the fill of a terminal pane.
func Terminal() (fg, bg ui.Color) { return current.fg, current.bg }

// ANSI is the appearance's terminal.ansi[16]: the 16-colour palette a terminal
// draws SGR 30-37/90-97 with, and what Terminal's ANSI lens previews draw with.
func ANSI() [16]ui.Color { return current.ansi }

// Chrome is the appearance's terminal.chrome: what Terminal draws the window chrome
// (its pane headers and status line) on.
func Chrome() ui.Color { return current.chrome }
