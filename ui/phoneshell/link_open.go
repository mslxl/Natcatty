package phoneshell

// The phone's touch link path. Desktop opens a link on Command+click, which a
// phone does not have: a finger taps the printed text. This file resolves the
// link under a tap with the same rules the desktop uses — the shared URL token
// matcher and the shared archive command builder — so the phone never grows a
// second idea of what a link is.
//
// A URL goes to the system browser through MyGo's OpenURLThen, so a refusal
// (no browser, an unsupported scheme) can show a notice without touching the
// terminal. An archive name fills its extraction command at the prompt through
// the terminal's own input path, exactly as the desktop does. A local path that
// is not an archive, an unsupported scheme, and an unremarkable token are not
// links and do nothing.

import (
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens/parse"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/terminal"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// linkKind is what a tap on a printed target opens.
type linkKind int

const (
	// linkNone is not a link: a local file path, an unsupported scheme, or
	// ordinary text.
	linkNone linkKind = iota
	// linkURL opens in the system browser.
	linkURL
	// linkArchive fills its extraction command at the prompt.
	linkArchive
)

// linkAtRow returns the target under a cell of a replayed row and how to open
// it. URLAt is the shared printed-URL rule; SafeToOpen is MyGo's own rule for
// links other people wrote, which refuses local files, paths, and schemes that
// run programs. An archive name comes from the shared domain builder, so the
// suffix table and the safety checks are not duplicated here.
func linkAtRow(spans []ui.Span, col int) (string, linkKind) {
	if uri, ok := appview.URLAt(spans, col); ok && ui.SafeToOpen(uri) {
		return uri, linkURL
	}
	text := appview.RowText(spans)
	if token, ok := tokenAtColumn(text, col); ok {
		if _, ok := domainterminal.ArchiveExtractCommand(token); ok {
			return token, linkArchive
		}
	}
	return "", linkNone
}

// tokenAtColumn returns the whitespace-delimited token that covers col, with
// surrounding punctuation removed by the same set URLAt trims. Column
// arithmetic uses the terminal's cell rules, so a wide rune still names the
// token it belongs to.
func tokenAtColumn(text string, col int) (string, bool) {
	cell := 0
	startCell, startByte := -1, 0
	end := func(at int) (string, bool) {
		if startCell < 0 || col < startCell || col >= cell {
			return "", false
		}
		token := strings.TrimFunc(text[startByte:at], isLinkPunctuation)
		if token == "" {
			return "", false
		}
		return token, true
	}
	for i, r := range text {
		if unicode.IsSpace(r) {
			if token, ok := end(i); ok {
				return token, true
			}
			startCell = -1
			cell += max(parse.RuneWidth(r), 0)
			continue
		}
		if startCell < 0 {
			startCell, startByte = cell, i
		}
		cell += max(parse.RuneWidth(r), 0)
	}
	return end(len(text))
}

// isLinkPunctuation is the punctuation URLAt trims from a printed token.
func isLinkPunctuation(r rune) bool {
	return strings.ContainsRune("\"'`()[]{}<>,;:.!?", r)
}

// tapGeometry is the geometry of the grid a tap lands on, in the coordinates
// of the element that observes the pointer.
type tapGeometry struct {
	width, height float32
	cellW, cellH  float32
	// padX and padY are the live grid's origin inside the observed box, and
	// bottomInset is the room below its last row.
	padX, padY  float32
	bottomInset float32
	cols, rows  int
	// lens says the grid is the flow's live tail, pinned to the bottom of the
	// viewport rather than starting at padY.
	lens bool
}

// cellAt turns a pointer position into a grid cell.
func (g tapGeometry) cellAt(x, y float32) (col, row int, ok bool) {
	if g.cellW <= 0 || g.cellH <= 0 || g.cols <= 0 || g.rows <= 0 {
		return 0, 0, false
	}
	top := g.padY
	if g.lens {
		top = g.height - g.bottomInset - float32(g.rows)*g.cellH
	}
	gx, gy := x-g.padX, y-top
	if gx < 0 || gy < 0 {
		return 0, 0, false
	}
	col, row = int(gx/g.cellW), int(gy/g.cellH)
	if col < 0 || row < 0 || col >= g.cols || row >= g.rows {
		return 0, 0, false
	}
	return col, row, true
}

// linkAtPoint resolves the link a pointer position names in the terminal's
// visible screen. The snapshot replays the scrollback and then the screen, so
// the last rows of the frame are the viewport the pointer is on.
func linkAtPoint(term *terminal.Terminal, g tapGeometry, x, y float32) (string, linkKind, bool) {
	if term == nil {
		return "", linkNone, false
	}
	col, row, ok := g.cellAt(x, y)
	if !ok {
		return "", linkNone, false
	}
	cols, rows := term.Size()
	if rows <= 0 || cols <= 0 {
		return "", linkNone, false
	}
	frame := appview.ScreenFrame(term, appview.Size{Cols: cols, Rows: rows}, false)
	lines := frame.Lines
	if len(lines) == 0 {
		return "", linkNone, false
	}
	base := len(lines) - rows
	if base < 0 {
		base = 0
	}
	line := base + row
	if line < 0 || line >= len(lines) {
		return "", linkNone, false
	}
	target, kind := linkAtRow(lines[line], col)
	if kind == linkNone {
		return "", linkNone, false
	}
	return target, kind, true
}

// linkNoticeText is the notice a failed open leaves. It names what could not
// open so the user can tell a stale link from a missing browser.
func linkNoticeText(target string, err error) string {
	target = strings.TrimSpace(target)
	if target == "" {
		target = "the link"
	}
	reason := ""
	if err != nil {
		reason = strings.TrimSpace(err.Error())
	}
	if reason == "" {
		reason = "no application on this device can open it"
	}
	return "Could not open " + target + ": " + reason + "."
}
