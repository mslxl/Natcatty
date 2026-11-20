package appview

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens/parse"
)

// pathPattern finds the file paths programs print as text: absolute, from
// ~, ./ or ../, with a slash, or a file name with an extension; each may end in
// :line or :line:col.
var pathPattern = regexp.MustCompile(`(?:(?:~|\.{1,2})?/[^\s"'<>` + "`" + `\x00-\x1f:]+|[\w.@+-]+(?:/[\w.@+-]+)+/?)(?::\d+(?::\d+)?)?|[\w@+-][\w.@+-]*\.\w+(?::\d+(?::\d+)?)?`)

// URLAt returns the URL printed in a row that the given cell points at. Terminal opens
// "a hyperlink (OSC 8), a URL printed as text, or a file path" on a modified click;
// the plugin's snapshot keeps no OSC 8 data, so the text form is what can be found
// from the replay.
func URLAt(spans []ui.Span, col int) (string, bool) {
	uri, _, _, ok := urlAtRow(spans, col)
	return uri, ok
}

// urlAtRow returns the URL printed in a row at the given cell, and the cells
// it spans: x0 up to x1 in screen columns.
func urlAtRow(spans []ui.Span, col int) (uri string, x0, x1 int, ok bool) {
	text, starts, widths := rowCells(spans)
	idx := runeAt(starts, widths, col)
	if idx < 0 {
		return "", 0, 0, false
	}
	isSpace := func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\x00'
	}
	// Grow to the whitespace-delimited token around that rune.
	runes := []rune(text)
	from, to := idx, idx+1
	for from > 0 && !isSpace(runes[from-1]) {
		from--
	}
	for to < len(runes) && !isSpace(runes[to]) {
		to++
	}
	token := runes[from:to]
	lead := 0
	for lead < len(token) && strings.ContainsRune("\"'`()[]{}<>,;:.!?", token[lead]) {
		lead++
	}
	trail := len(token)
	for trail > lead && strings.ContainsRune("\"'`()[]{}<>,;:.!?", token[trail-1]) {
		trail--
	}
	if lead == trail {
		return "", 0, 0, false
	}
	if u, ok := normalizeURL(string(token[lead:trail])); ok {
		first := from + lead
		last := from + trail - 1
		return u, starts[first], starts[last] + max(widths[last], 1), true
	}
	return "", 0, 0, false
}

// pathAtRow returns the file path printed in a row at the given cell, as
// urlAtRow returns a URL. It uses the terminal plugin's path rules so a
// Command+click names the same path the plugin's own view names.
func pathAtRow(spans []ui.Span, col int) (path string, x0, x1 int, ok bool) {
	text, starts, widths := rowCells(spans)
	for _, m := range pathPattern.FindAllStringIndex(text, -1) {
		p := trimURLEnd(text[m[0]:m[1]])
		if p == "" {
			continue
		}
		// Do not turn dotted numbers such as version 1.2.3 into file links.
		if !strings.ContainsAny(p, "/:") && strings.IndexFunc(p, unicode.IsLetter) < 0 {
			continue
		}
		first := runeIndex(text, m[0])
		last := first + len([]rune(p)) - 1
		if first < 0 || last >= len(starts) {
			continue
		}
		start := starts[first]
		end := starts[last] + max(widths[last], 1)
		if start <= col && col < end {
			return p, start, end, true
		}
	}
	return "", 0, 0, false
}

// trimURLEnd trims what likely ends the sentence around a URL rather than the
// URL: punctuation, and closing brackets that the URL did not open.
func trimURLEnd(u string) string {
	for len(u) > 0 {
		switch last := u[len(u)-1]; last {
		case '.', ',', ';', ':', '!', '?':
			u = u[:len(u)-1]
			continue
		case ')', ']', '}':
			open := map[byte]string{')': "(", ']': "[", '}': "{"}[last]
			if strings.Count(u, open) < strings.Count(u, string(last)) {
				u = u[:len(u)-1]
				continue
			}
		}
		return u
	}
	return u
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

// rowCells returns a row's text, the screen column each rune starts at, and
// each rune's width in cells. A wide rune occupies two columns; a combining
// mark occupies none and starts where the rune before it did.
func rowCells(spans []ui.Span) (text string, starts, widths []int) {
	var b strings.Builder
	cell := 0
	for _, sp := range spans {
		for _, r := range sp.Text {
			starts = append(starts, cell)
			w := parse.RuneWidth(r)
			widths = append(widths, w)
			b.WriteRune(r)
			if w > 0 {
				cell += w
			}
		}
	}
	return b.String(), starts, widths
}

// runeAt returns the index of the rune that covers col, or -1. A zero-width
// rune is not by itself under the pointer; a column past the row's text names
// no rune.
func runeAt(starts, widths []int, col int) int {
	for i := range starts {
		w := widths[i]
		if w <= 0 {
			continue
		}
		if starts[i] <= col && col < starts[i]+w {
			return i
		}
	}
	return -1
}

// runeIndex returns the rune index of the byte offset in text, or -1.
func runeIndex(text string, off int) int {
	if off < 0 || off > len(text) {
		return -1
	}
	return len([]rune(text[:off]))
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
