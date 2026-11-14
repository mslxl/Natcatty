package appview

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens/parse"
)

// URLAt returns the URL printed in a row that the given cell points at. Terminal opens
// "a hyperlink (OSC 8), a URL printed as text, or a file path" on a modified click;
// the plugin's snapshot keeps no OSC 8 data, so the text form is what can be found
// from the replay.
func URLAt(spans []ui.Span, col int) (string, bool) {
	text := rowTextOf(spans)
	if text == "" || col < 0 {
		return "", false
	}
	// Walk the row in cells, so a wide rune counts twice, and note where the token
	// under the pointer starts and ends.
	cell, start, end := 0, -1, -1
	for i, r := range text {
		w := parse.RuneWidth(r)
		if w <= 0 {
			continue
		}
		if cell <= col && col < cell+w {
			start, end = i, i+len(string(r))
		}
		cell += w
	}
	if start < 0 {
		return "", false
	}
	// Grow to the whitespace-delimited token around that rune.
	isSpace := func(b byte) bool {
		return b == ' ' || b == '\t' || b == '\x00'
	}
	from, to := start, end
	for from > 0 && !isSpace(text[from-1]) {
		from--
	}
	for to < len(text) && !isSpace(text[to]) {
		to++
	}
	token := text[from:to]
	token = strings.Trim(token, "\"'`()[]{}<>,;:.!?")
	if uri, ok := normalizeURL(token); ok {
		return uri, true
	}
	return "", false
}

// normalizeURL accepts what a click should open: an absolute URL, or a bare host
// that starts with www.
func normalizeURL(token string) (string, bool) {
	low := strings.ToLower(token)
	for _, p := range []string{"https://", "http://", "file://", "mailto:", "ftp://"} {
		if strings.HasPrefix(low, p) && len(token) > len(p) {
			return token, true
		}
	}
	if strings.HasPrefix(low, "www.") && strings.Contains(low[4:], ".") {
		return "https://" + token, true
	}
	return "", false
}

// rowTextOf is the text of a row of spans.
func rowTextOf(spans []ui.Span) string {
	var b strings.Builder
	for _, sp := range spans {
		b.WriteString(sp.Text)
	}
	return b.String()
}

// RowText is a row's text, for callers that need it (a diagnostic, a lens).
func RowText(spans []ui.Span) string { return rowTextOf(spans) }
