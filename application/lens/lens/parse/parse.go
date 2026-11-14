// Package parse is Terminal's shared output-parser table: "the same ones Terminal's
// built-in lenses use" (flow.exe plugin-SDK doc, strings line 802657-802668):
//
//	columns(lines) -> Columns?   whitespace-aligned lines, first is the header
//	packed(lines)  -> []string   splits packed multi-column listings (ls)
//	json_tree(text)-> Node?      a JSON document or stream of documents
//	location(s)    -> path,line,col   `file:line[:col]` at the start of s
//	size(s)        -> bytes      human size ("4.0K", "1.2GB")
//
// It depends on nothing but the standard library, so both the lens engine and
// the lenses can use it without an import cycle.
package parse

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Table is the result of Columns: the header cells, the data rows, and where
// each column starts in the line (byte offsets, as Terminal's alignment implies).
type Table struct {
	Heads []string
	Rows  [][]string
	Start []int
}

// ExpandTabs replaces tabs with spaces up to the next multiple of stop.
func ExpandTabs(line string, stop int) string {
	if stop <= 0 {
		stop = 8
	}
	var b strings.Builder
	col := 0
	for _, r := range line {
		if r != '\t' {
			b.WriteRune(r)
			col++
			continue
		}
		n := stop - col%stop
		for i := 0; i < n; i++ {
			b.WriteByte(' ')
		}
		col += n
	}
	return b.String()
}

// column is one aligned region of a table: where it starts and ends in a line, in
// display cells (a CJK glyph is two of them).
type column struct{ start, end int }

// displayCells lays a line out in cells, one per column of the terminal, so
// alignment is compared the way it is seen rather than by bytes: a rune that takes
// two cells occupies its glyph plus an empty cell. The returned slice has the same
// length as the line's display width.
func displayCells(line string) []rune {
	cells := make([]rune, 0, len(line))
	for _, r := range line {
		cells = append(cells, r)
		if w := RuneWidth(r); w > 1 {
			for i := 1; i < w; i++ {
				cells = append(cells, 0)
			}
		}
	}
	return cells
}

// cellsText rebuilds the text of a cell range, dropping the glyphs' padding cells.
func cellsText(cells []rune, start, end int) string {
	if start >= len(cells) {
		return ""
	}
	if end > len(cells) {
		end = len(cells)
	}
	var b strings.Builder
	for _, r := range cells[start:end] {
		if r != 0 {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// Columns reads whitespace-aligned lines whose first line is the header, and
// returns nil for prose. The header decides where the columns are: a run of two or
// more spaces starts the next one, a single space keeps a header's words together
// ("COMMAND  ARGS"). Every row is then cut at the same offsets, so a row shorter
// than the header simply has trailing empty cells and a table indented as a whole
// (git's --stat rows) is dedented first instead of being read as prose.
//
// Offsets are in display cells, not bytes: a table with CJK or emoji aligns the way
// it looks, like Terminal's parse.columns.
func Columns(lines []string) *Table {
	lines = TrimBlank(lines)
	if len(lines) < 2 {
		return nil
	}
	// Tab-aligned tools (`helm list`, some `docker`/`ps` output) put a tab at
	// each column boundary; expanding tabs to 8-column stops first lets the
	// header be read as the space-padded table it is.
	cells := make([][]rune, len(lines))
	for i, l := range lines {
		if strings.ContainsRune(l, '\t') {
			l = ExpandTabs(l, 8)
		}
		cells[i] = displayCells(l)
	}
	indent := -1
	for _, cl := range cells {
		n := 0
		for n < len(cl) && cl[n] == ' ' {
			n++
		}
		if n == len(cl) {
			continue
		}
		if indent < 0 || n < indent {
			indent = n
		}
	}
	if indent < 0 {
		return nil
	}
	for i := range cells {
		if indent < len(cells[i]) {
			cells[i] = cells[i][indent:]
		} else {
			cells[i] = nil
		}
	}
	head := cells[0]
	// Column starts: the beginning of each word run on the header, where a word run
	// continues across a single space and ends at a run of two or more.
	var starts []int
	i := 0
	for i < len(head) && head[i] == ' ' {
		i++
	}
	for i < len(head) {
		starts = append(starts, i)
		// the header's first word, then any words joined to it by a single space
		for i < len(head) && head[i] != ' ' {
			i++
		}
		for i < len(head) {
			gap := i
			for gap < len(head) && head[gap] == ' ' {
				gap++
			}
			if gap-i != 1 || gap >= len(head) {
				break
			}
			i = gap
			for i < len(head) && head[i] != ' ' {
				i++
			}
		}
		for i < len(head) && head[i] == ' ' {
			i++
		}
	}
	if len(starts) < 2 {
		return nil
	}
	if len(starts) > 32 {
		return nil // prose that happens to have gaps, not a table
	}
	cols := make([]column, len(starts))
	for k, st := range starts {
		end := len(head)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		// A column runs to where the next one begins, padding included, so a
		// value wider than its header is still read whole.
		cols[k] = column{start: st, end: end}
	}
	heads := make([]string, len(cols))
	nonEmpty := 0
	for k, c := range cols {
		heads[k] = cellsText(head, c.start, c.end)
		if heads[k] != "" {
			nonEmpty++
		}
	}
	if nonEmpty < 2 {
		return nil
	}
	t := &Table{Heads: heads}
	for _, c := range cols {
		t.Start = append(t.Start, c.start+indent)
	}
	for _, cl := range cells[1:] {
		if len(cl) == 0 {
			continue
		}
		row := make([]string, len(cols))
		for k, c := range cols {
			end := c.end
			if k == len(cols)-1 {
				end = len(cl) // the last column runs to the end of the row
			}
			row[k] = cellsText(cl, c.start, end)
		}
		t.Rows = append(t.Rows, row)
	}
	if len(t.Rows) == 0 {
		return nil
	}
	return t
}
func lineCells(line string, cols []column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		if c.start >= len(line) {
			continue
		}
		end := c.end
		if end > len(line) {
			end = len(line)
		}
		out[i] = strings.TrimSpace(line[c.start:end])
	}
	return out
}

// Packed splits packed multi-column listings (`ls` without -l) into their
// entries: each line may hold several entries separated by two or more spaces.
func Packed(lines []string) []string {
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "total ") {
			continue
		}
		for _, f := range splitWide(line) {
			f = strings.TrimSpace(f)
			if f != "" && f != "total" {
				out = append(out, f)
			}
		}
	}
	return out
}

// splitWide splits on runs of two or more spaces (or a tab), which is what
// separates entries in a packed listing.
func splitWide(line string) []string {
	var out []string
	start := 0
	i := 0
	for i < len(line) {
		if line[i] == '\t' {
			out = append(out, line[start:i])
			i++
			start = i
			continue
		}
		if line[i] == ' ' {
			j := i
			for j < len(line) && line[j] == ' ' {
				j++
			}
			if j-i >= 2 {
				out = append(out, line[start:i])
				start = j
			}
			i = j
			continue
		}
		i++
	}
	out = append(out, line[start:])
	return out
}

// Value is one node of a decoded JSON document, flattened enough to render as
// a collapsible tree.
type Value struct {
	Key      string // object member name, or "[i]" for an array element
	Scalar   string // rendered scalar ("null", "true", `"text"`, `12`)
	IsScalar bool
	Children []Value
}

// JSONValue decodes text as one JSON document, or as a stream of documents
// (JSONL), and returns its tree. It returns ok=false when the text is not JSON,
// which is Terminal's "nil when not JSON".
func JSONValue(text string) (Value, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Value{}, false
	}
	// One document.
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err == nil {
		var rest any
		if err := dec.Decode(&rest); err != nil && err.Error() == "EOF" {
			return build("", v), true
		}
	}
	// A stream of documents, one per line.
	var roots []Value
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		d := json.NewDecoder(strings.NewReader(line))
		d.UseNumber()
		var item any
		if err := d.Decode(&item); err != nil {
			return Value{}, false
		}
		roots = append(roots, build("", item))
	}
	if len(roots) == 0 {
		return Value{}, false
	}
	return Value{Children: roots}, true
}

func build(key string, v any) Value {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortStrings(keys)
		out := Value{Key: key}
		for _, k := range keys {
			out.Children = append(out.Children, build(k, t[k]))
		}
		return out
	case []any:
		out := Value{Key: key}
		for i, item := range t {
			out.Children = append(out.Children, build("["+strconv.Itoa(i)+"]", item))
		}
		return out
	case string:
		return Value{Key: key, Scalar: strconv.Quote(t), IsScalar: true}
	case json.Number:
		return Value{Key: key, Scalar: string(t), IsScalar: true}
	case bool:
		return Value{Key: key, Scalar: strconv.FormatBool(t), IsScalar: true}
	case nil:
		return Value{Key: key, Scalar: "null", IsScalar: true}
	}
	return Value{Key: key, Scalar: "null", IsScalar: true}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Location reads `file:line[:col]` at the start of s, as Terminal's parse.location.
// It understands a Windows drive letter and a trailing separator/text.
func Location(s string) (path string, line, col int, ok bool) {
	rest := s
	prefix := ""
	if len(s) >= 2 && s[1] == ':' && isDriveLetter(s[0]) {
		prefix = s[:2]
		rest = s[2:]
	}
	// Find the first colon whose following run is digits.
	for i := 0; i < len(rest); i++ {
		if rest[i] != ':' {
			continue
		}
		j := i + 1
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j == i+1 {
			continue
		}
		n, err := strconv.Atoi(rest[i+1 : j])
		if err != nil {
			continue
		}
		p := prefix + rest[:i]
		if p == "" || allDigits(p) || strings.Contains(rest[:i], ":") {
			continue // "12:30:00" is a time, not a file
		}
		line = n
		if j < len(rest) && rest[j] == ':' {
			k := j + 1
			for k < len(rest) && rest[k] >= '0' && rest[k] <= '9' {
				k++
			}
			if k > j+1 {
				if c, err := strconv.Atoi(rest[j+1 : k]); err == nil {
					col = c
				}
			}
		}
		return p, line, col, true
	}
	return "", 0, 0, false
}

func isDriveLetter(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// Size converts a human size ("4.0K", "1.2GB", "512", "1,024", "3.0KiB") to
// bytes, as Terminal's parse.size.
func Size(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	s = strings.ReplaceAll(s, ",", "")
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0, false
	}
	num, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, false
	}
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	unit = strings.TrimSuffix(unit, "ib")
	unit = strings.TrimSuffix(unit, "b")
	mult := float64(1)
	switch unit {
	case "":
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	case "t":
		mult = 1 << 40
	case "p":
		mult = 1 << 50
	default:
		return 0, false
	}
	return int64(num * mult), true
}

// HasJSONFlag reports whether the command line asks a tool for JSON output, the
// case where Terminal's json_tree parser applies.
func HasJSONFlag(line string) bool {
	fields := strings.Fields(line)
	for i, f := range fields {
		switch f {
		case "--json", "--json=v2", "-json", "-j", "--output=json", "--format=json",
			"--format", "--output":
			if f == "--format" || f == "--output" {
				if i+1 < len(fields) && strings.Contains(fields[i+1], "json") {
					return true
				}
				continue
			}
			return true
		}
		if strings.HasPrefix(f, "-o=") && strings.Contains(f, "json") {
			return true
		}
		if f == "-o" && i+1 < len(fields) && strings.Contains(fields[i+1], "json") {
			return true
		}
	}
	return false
}

// RuneWidth is the number of terminal cells a rune occupies: 2 for the East
// Asian wide ranges and emoji, 0 for combining marks. It is what lets the tap
// join soft-wrapped rows at the capture width.
func RuneWidth(r rune) int {
	switch {
	case r < 0x20:
		return 0
	// Combining marks and zero-width controls take no cell. This must come before
	// the "narrow" catch-all: a mark like U+0301 is below 0x1100.
	case r >= 0x0300 && r <= 0x036F, // combining diacritics
		r >= 0x0483 && r <= 0x0489,
		r >= 0x0591 && r <= 0x05BD,
		r >= 0x0610 && r <= 0x061A,
		r >= 0x064B && r <= 0x065F,
		r >= 0x0E31 && r <= 0x0E3A,
		r >= 0x1AB0 && r <= 0x1AFF,
		r >= 0x1DC0 && r <= 0x1DFF,
		r >= 0x200B && r <= 0x200F, // zero-width space … RLM
		r >= 0x20D0 && r <= 0x20FF, // combining marks for symbols
		r >= 0xFE00 && r <= 0xFE0F, // variation selectors
		r >= 0xFE20 && r <= 0xFE2F:
		return 0
	case r < 0x1100:
		return 1
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals … Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, // emoji
		r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}

	return 1
}

// Width is the number of cells s occupies.
func Width(s string) int {
	n := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			n++
			s = s[1:]
			continue
		}
		n += RuneWidth(r)
		s = s[size:]
	}
	return n
}

// FirstLine is a small helper the lenses use to skip blank lines.
func FirstLine(lines []string) (string, bool) {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return l, true
		}
	}
	return "", false
}

// TrimBlank drops leading and trailing blank lines.
func TrimBlank(lines []string) []string {
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[start:end]
}

// EqualBytes is a helper for raw comparisons in tests.
func EqualBytes(a, b []byte) bool { return bytes.Equal(a, b) }
