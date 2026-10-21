// Package markdown parses a small, deterministic subset of Markdown into
// blocks and inline runs. It has no dependencies beyond the standard library
// and never panics on arbitrary input. The Vault's native Note viewer renders
// the parsed document with native widgets, and the HTML export renders the
// same document as escaped, self-contained HTML.
//
// Supported: ATX and setext headings, paragraphs, emphasis, strong, inline
// code, fenced code, ordered and unordered lists (including nesting),
// blockquotes, thematic breaks, tables, links, images (as readable text), and
// autolinks. Raw HTML is treated as text and escaped on export.
package markdown

import (
	"html"
	"strconv"
	"strings"
)

// BlockKind identifies the kind of a parsed block.
type BlockKind int

const (
	// ParagraphBlock is a run of prose.
	ParagraphBlock BlockKind = iota
	// HeadingBlock is a heading; its Level is 1-6.
	HeadingBlock
	// CodeBlock is a fenced code block.
	CodeBlock
	// QuoteBlock is a blockquote; its Blocks hold the quoted content.
	QuoteBlock
	// ListBlock is an ordered or unordered list.
	ListBlock
	// RuleBlock is a thematic break.
	RuleBlock
	// TableBlock is a pipe table.
	TableBlock
)

// Alignment is a table column's alignment.
type Alignment int

const (
	AlignNone Alignment = iota
	AlignLeft
	AlignCenter
	AlignRight
)

// InlineKind identifies the kind of an inline run.
type InlineKind int

const (
	InlineText InlineKind = iota
	InlineEmphasis
	InlineStrong
	InlineStrike
	InlineCode
	InlineLink
	InlineImage
)

// Inline is one run of text with its style. Links and images carry Children
// (their label) and a URL.
type Inline struct {
	Kind     InlineKind
	Text     string
	URL      string
	Children []Inline
}

// ListItem is one list item. Its Blocks let an item hold a nested list or a
// second paragraph.
type ListItem struct {
	Blocks []Block
}

// Block is one parsed block.
type Block struct {
	Kind     BlockKind
	Level    int
	Inlines  []Inline
	Code     string
	Language string
	Items    []ListItem
	Ordered  bool
	Start    int
	Blocks   []Block
	Header   [][]Inline
	Rows     [][][]Inline
	Align    []Alignment
}

// Document is a parsed Markdown source.
type Document struct {
	Blocks []Block
}

const (
	maxBlockDepth  = 24
	maxInlineDepth = 12
)

// Parse parses source into a document. Input with CRLF or CR line endings is
// normalized first. Every construct is optional; malformed input becomes
// literal text rather than an error or a panic.
func Parse(source string) Document {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = strings.ReplaceAll(source, "\r", "\n")
	lines := strings.Split(source, "\n")
	// A trailing newline produces one empty final line, which is harmless.
	return Document{Blocks: parseBlocks(lines, 0)}
}

// PlainText returns the document's text with one line per block. It is useful
// for summaries and tests; the viewer uses the structure instead.
func PlainText(blocks []Block) string {
	var parts []string
	for _, block := range blocks {
		switch block.Kind {
		case ParagraphBlock, HeadingBlock:
			parts = append(parts, inlinePlain(block.Inlines))
		case CodeBlock:
			parts = append(parts, block.Code)
		case QuoteBlock:
			parts = append(parts, PlainText(block.Blocks))
		case ListBlock:
			for _, item := range block.Items {
				parts = append(parts, PlainText(item.Blocks))
			}
		case TableBlock:
			row := make([]string, 0, len(block.Header))
			for _, cell := range block.Header {
				row = append(row, inlinePlain(cell))
			}
			parts = append(parts, strings.Join(row, " | "))
			for _, cells := range block.Rows {
				row = row[:0]
				for _, cell := range cells {
					row = append(row, inlinePlain(cell))
				}
				parts = append(parts, strings.Join(row, " | "))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func inlinePlain(inlines []Inline) string {
	var builder strings.Builder
	for _, inline := range inlines {
		switch inline.Kind {
		case InlineImage:
			builder.WriteString(inlinePlain(inline.Children))
			if inline.URL != "" {
				builder.WriteString(" (")
				builder.WriteString(inline.URL)
				builder.WriteString(")")
			}
		default:
			builder.WriteString(inline.Text)
			builder.WriteString(inlinePlain(inline.Children))
		}
	}
	return builder.String()
}

func parseBlocks(lines []string, depth int) []Block {
	if depth > maxBlockDepth {
		return []Block{{Kind: ParagraphBlock, Inlines: []Inline{{Kind: InlineText, Text: strings.Join(lines, "\n")}}}}
	}
	blocks := make([]Block, 0, len(lines)/2+1)
	for i := 0; i < len(lines); {
		line := lines[i]
		if isBlank(line) {
			i++
			continue
		}

		if marker, ok := fenceStart(line); ok {
			block, next := parseFence(lines, i, marker)
			blocks = append(blocks, block)
			i = next
			continue
		}
		if level, text, ok := atxHeading(line); ok {
			blocks = append(blocks, Block{Kind: HeadingBlock, Level: level, Inlines: parseInline(text, 0)})
			i++
			continue
		}
		if isRule(line) {
			blocks = append(blocks, Block{Kind: RuleBlock})
			i++
			continue
		}
		if _, ok := quoteStart(line); ok {
			block, next := parseQuote(lines, i, depth)
			blocks = append(blocks, block)
			i = next
			continue
		}
		if startsTable(lines, i) {
			block, next := parseTable(lines, i)
			blocks = append(blocks, block)
			i = next
			continue
		}
		if marker, ok := listMarkerAt(line); ok {
			block, next := parseList(lines, i, marker, depth)
			blocks = append(blocks, block)
			i = next
			continue
		}
		if i+1 < len(lines) {
			if level, ok := setextUnderline(lines[i+1]); ok && !isBlank(line) {
				blocks = append(blocks, Block{Kind: HeadingBlock, Level: level, Inlines: parseInline(line, 0)})
				i += 2
				continue
			}
		}

		block, next := parseParagraph(lines, i)
		blocks = append(blocks, block)
		i = next
	}
	return blocks
}

func isBlank(line string) bool { return strings.TrimSpace(line) == "" }

func leadingSpaces(line string) int {
	count := 0
	for count < len(line) && line[count] == ' ' {
		count++
	}
	return count
}

// fenceMarker is an opening code fence.
type fenceMarker struct {
	char   byte
	length int
	info   string
	indent int
}

func fenceStart(line string) (fenceMarker, bool) {
	indent := leadingSpaces(line)
	if indent > 3 {
		return fenceMarker{}, false
	}
	rest := line[indent:]
	if len(rest) < 3 || (rest[0] != '`' && rest[0] != '~') {
		return fenceMarker{}, false
	}
	char := rest[0]
	length := 1
	for length < len(rest) && rest[length] == char {
		length++
	}
	if length < 3 {
		return fenceMarker{}, false
	}
	return fenceMarker{char: char, length: length, info: strings.TrimSpace(rest[length:]), indent: indent}, true
}

func parseFence(lines []string, start int, marker fenceMarker) (Block, int) {
	code := make([]string, 0, 8)
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		indent := leadingSpaces(line)
		rest := line[indent:]
		if indent <= 3 && len(rest) >= marker.length && strings.Trim(rest, string(marker.char)) == "" {
			return Block{Kind: CodeBlock, Code: strings.Join(code, "\n"), Language: fenceLanguage(marker.info)}, i + 1
		}
		// Remove up to the opening fence's indentation, as CommonMark does.
		code = append(code, stripIndent(line, marker.indent))
	}
	return Block{Kind: CodeBlock, Code: strings.Join(code, "\n"), Language: fenceLanguage(marker.info)}, len(lines)
}

func fenceLanguage(info string) string {
	if info == "" {
		return ""
	}
	fields := strings.Fields(info)
	if len(fields) == 0 {
		return ""
	}
	language := fields[0]
	var builder strings.Builder
	for _, r := range language {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '+' || r == '#' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func atxHeading(line string) (int, string, bool) {
	indent := leadingSpaces(line)
	if indent > 3 {
		return 0, "", false
	}
	rest := line[indent:]
	level := 0
	for level < len(rest) && rest[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0, "", false
	}
	if level < len(rest) && rest[level] != ' ' && rest[level] != '\t' {
		return 0, "", false
	}
	text := strings.TrimSpace(rest[level:])
	text = strings.TrimSpace(strings.TrimRight(text, "#"))
	return level, text, true
}

func setextUnderline(line string) (int, bool) {
	indent := leadingSpaces(line)
	if indent > 3 {
		return 0, false
	}
	rest := strings.TrimSpace(line[indent:])
	if rest == "" {
		return 0, false
	}
	char := rest[0]
	if char != '=' && char != '-' {
		return 0, false
	}
	for i := 1; i < len(rest); i++ {
		if rest[i] != char {
			return 0, false
		}
	}
	if char == '=' {
		return 1, true
	}
	return 2, true
}

func isRule(line string) bool {
	indent := leadingSpaces(line)
	if indent > 3 {
		return false
	}
	rest := strings.TrimSpace(line[indent:])
	if rest == "" {
		return false
	}
	char := rest[0]
	if char != '-' && char != '*' && char != '_' {
		return false
	}
	count := 0
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case char:
			count++
		case ' ', '\t':
		default:
			return false
		}
	}
	return count >= 3
}

func quoteStart(line string) (string, bool) {
	indent := leadingSpaces(line)
	if indent > 3 {
		return "", false
	}
	rest := line[indent:]
	if !strings.HasPrefix(rest, ">") {
		return "", false
	}
	rest = rest[1:]
	if strings.HasPrefix(rest, " ") {
		rest = rest[1:]
	}
	return rest, true
}

func parseQuote(lines []string, start, depth int) (Block, int) {
	quoted := make([]string, 0, 8)
	i := start
	for i < len(lines) {
		content, ok := quoteStart(lines[i])
		if !ok {
			break
		}
		quoted = append(quoted, content)
		i++
	}
	// A blank line followed by another quoted line keeps the quote together.
	for i < len(lines) && isBlank(lines[i]) {
		if i+1 < len(lines) {
			if _, ok := quoteStart(lines[i+1]); ok {
				quoted = append(quoted, "")
				i++
				continue
			}
		}
		break
	}
	return Block{Kind: QuoteBlock, Blocks: parseBlocks(quoted, depth+1)}, i
}

// listMarker describes one list item marker.
type listMarker struct {
	indent  int
	ordered bool
	number  int
	content int
}

func listMarkerAt(line string) (listMarker, bool) {
	indent := leadingSpaces(line)
	rest := line[indent:]
	if rest == "" {
		return listMarker{}, false
	}
	switch rest[0] {
	case '-', '*', '+':
		if len(rest) == 1 {
			return listMarker{indent: indent, content: len(line)}, true
		}
		if rest[1] != ' ' && rest[1] != '\t' {
			return listMarker{}, false
		}
		content := indent + 1
		for content < len(line) && (line[content] == ' ' || line[content] == '\t') {
			content++
		}
		return listMarker{indent: indent, content: content}, true
	}
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits > 9 || digits == len(rest) {
		return listMarker{}, false
	}
	if rest[digits] != '.' && rest[digits] != ')' {
		return listMarker{}, false
	}
	after := digits + 1
	if after < len(rest) && rest[after] != ' ' && rest[after] != '\t' {
		return listMarker{}, false
	}
	number := 0
	for i := 0; i < digits; i++ {
		number = number*10 + int(rest[i]-'0')
	}
	content := indent + after
	for content < len(line) && (line[content] == ' ' || line[content] == '\t') {
		content++
	}
	return listMarker{indent: indent, ordered: true, number: number, content: content}, true
}

func parseList(lines []string, start int, first listMarker, depth int) (Block, int) {
	block := Block{Kind: ListBlock, Ordered: first.ordered, Start: first.number}
	if block.Start == 0 {
		block.Start = 1
	}
	i := start
	for i < len(lines) {
		marker, ok := listMarkerAt(lines[i])
		if !ok || marker.ordered != first.ordered || marker.indent != first.indent {
			break
		}
		itemLines := []string{lines[i][min(marker.content, len(lines[i])):]}
		i++
		for i < len(lines) {
			line := lines[i]
			if isBlank(line) {
				// A blank line belongs to the item only when the list continues.
				next := i + 1
				if next < len(lines) {
					if nextMarker, ok := listMarkerAt(lines[next]); ok &&
						nextMarker.ordered == first.ordered && nextMarker.indent == first.indent {
						i = next
						break
					}
					if leadingSpaces(lines[next]) > first.indent && !isBlank(lines[next]) {
						itemLines = append(itemLines, "")
						i++
						continue
					}
				}
				break
			}
			if nextMarker, ok := listMarkerAt(line); ok && nextMarker.indent <= first.indent {
				break
			}
			if leadingSpaces(line) > first.indent {
				itemLines = append(itemLines, stripIndent(line, min(marker.content, first.indent+2)))
				i++
				continue
			}
			if startsBlock(line) {
				break
			}
			// Lazy continuation of the item's paragraph.
			itemLines = append(itemLines, line)
			i++
		}
		block.Items = append(block.Items, ListItem{Blocks: parseBlocks(itemLines, depth+1)})
		if i < len(lines) {
			if nextMarker, ok := listMarkerAt(lines[i]); !ok ||
				nextMarker.ordered != first.ordered || nextMarker.indent != first.indent {
				break
			}
		}
	}
	return block, i
}

func stripIndent(line string, count int) string {
	removed := 0
	for removed < count && removed < len(line) && line[removed] == ' ' {
		removed++
	}
	return line[removed:]
}

func startsBlock(line string) bool {
	if isBlank(line) {
		return true
	}
	if _, ok := fenceStart(line); ok {
		return true
	}
	if _, _, ok := atxHeading(line); ok {
		return true
	}
	if isRule(line) {
		return true
	}
	if _, ok := quoteStart(line); ok {
		return true
	}
	if _, ok := listMarkerAt(line); ok {
		return true
	}
	return false
}

func parseParagraph(lines []string, start int) (Block, int) {
	parts := make([]string, 0, 4)
	i := start
	for i < len(lines) && !isBlank(lines[i]) {
		if i > start && startsBlock(lines[i]) {
			break
		}
		if i+1 < len(lines) && i > start {
			if _, ok := setextUnderline(lines[i+1]); ok {
				break
			}
		}
		parts = append(parts, lines[i])
		i++
	}
	if len(parts) == 0 {
		parts = append(parts, lines[start])
		i = start + 1
	}

	text := parts[0]
	for _, part := range parts[1:] {
		hard := strings.HasSuffix(text, "  ") || strings.HasSuffix(text, "\\")
		text = strings.TrimSuffix(text, "\\")
		if hard {
			text = strings.TrimRight(text, " ") + "\n" + strings.TrimSpace(part)
			continue
		}
		text = strings.TrimRight(text, " ") + " " + strings.TrimSpace(part)
	}
	return Block{Kind: ParagraphBlock, Inlines: parseInline(text, 0)}, i
}

func startsTable(lines []string, i int) bool {
	if i+1 >= len(lines) || !strings.Contains(lines[i], "|") {
		return false
	}
	return isTableSeparator(lines[i+1])
}

func isTableSeparator(line string) bool {
	cells := splitTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if len(cell) < 3 {
			return false
		}
		trimmed := strings.TrimSuffix(strings.TrimPrefix(cell, ":"), ":")
		if trimmed == "" {
			return false
		}
		for i := 0; i < len(trimmed); i++ {
			if trimmed[i] != '-' {
				return false
			}
		}
	}
	return true
}

func parseTable(lines []string, start int) (Block, int) {
	headerCells := splitTableRow(lines[start])
	separator := splitTableRow(lines[start+1])
	align := make([]Alignment, len(separator))
	for i, cell := range separator {
		cell = strings.TrimSpace(cell)
		left := strings.HasPrefix(cell, ":")
		right := strings.HasSuffix(cell, ":")
		switch {
		case left && right:
			align[i] = AlignCenter
		case left:
			align[i] = AlignLeft
		case right:
			align[i] = AlignRight
		default:
			align[i] = AlignNone
		}
	}

	block := Block{Kind: TableBlock, Align: align}
	for _, cell := range headerCells {
		block.Header = append(block.Header, parseInline(strings.TrimSpace(cell), 0))
	}
	for len(block.Header) < len(align) {
		block.Header = append(block.Header, nil)
	}
	i := start + 2
	for i < len(lines) {
		if isBlank(lines[i]) || !strings.Contains(lines[i], "|") || startsBlock(lines[i]) {
			break
		}
		cells := splitTableRow(lines[i])
		row := make([][]Inline, 0, len(align))
		for column := range align {
			if column < len(cells) {
				row = append(row, parseInline(strings.TrimSpace(cells[column]), 0))
			} else {
				row = append(row, nil)
			}
		}
		block.Rows = append(block.Rows, row)
		i++
	}
	return block, i
}

// splitTableRow splits a pipe row on unescaped pipes and drops the optional
// leading and trailing pipe.
func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	cells := make([]string, 0, 4)
	var current strings.Builder
	escaped := false
	for i := 0; i < len(line); i++ {
		if escaped {
			current.WriteByte(line[i])
			escaped = false
			continue
		}
		if line[i] == '\\' {
			escaped = true
			current.WriteByte(line[i])
			continue
		}
		if line[i] == '|' {
			cells = append(cells, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(line[i])
	}
	cells = append(cells, current.String())
	if len(cells) > 0 && strings.TrimSpace(cells[0]) == "" {
		cells = cells[1:]
	}
	if len(cells) > 0 && strings.TrimSpace(cells[len(cells)-1]) == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}

func parseInline(source string, depth int) []Inline {
	if source == "" {
		return nil
	}
	if depth > maxInlineDepth {
		return []Inline{{Kind: InlineText, Text: source}}
	}
	inlines := make([]Inline, 0, 8)
	var text strings.Builder
	flush := func() {
		if text.Len() == 0 {
			return
		}
		inlines = append(inlines, Inline{Kind: InlineText, Text: text.String()})
		text.Reset()
	}

	for i := 0; i < len(source); {
		switch {
		case source[i] == '\\' && i+1 < len(source) && isASCIIPunctuation(source[i+1]):
			text.WriteByte(source[i+1])
			i += 2
		case source[i] == '`':
			run := countRun(source, i, '`')
			if end := findExactRun(source, i+run, '`', run); end >= 0 {
				flush()
				code := strings.ReplaceAll(source[i+run:end], "\n", " ")
				if strings.HasPrefix(code, " ") && strings.HasSuffix(code, " ") && strings.TrimSpace(code) != "" {
					code = code[1 : len(code)-1]
				}
				inlines = append(inlines, Inline{Kind: InlineCode, Text: code})
				i = end + run
				continue
			}
			text.WriteString(source[i : i+run])
			i += run
		case source[i] == '*':
			next, ok := parseEmphasis(source, i, '*', depth, &inlines, flush)
			if ok {
				i = next
				continue
			}
			text.WriteByte(source[i])
			i++
		case source[i] == '_':
			if !canOpenUnderscore(source, i) {
				text.WriteByte(source[i])
				i++
				continue
			}
			next, ok := parseEmphasis(source, i, '_', depth, &inlines, flush)
			if ok {
				i = next
				continue
			}
			text.WriteByte(source[i])
			i++
		case source[i] == '~' && i+1 < len(source) && source[i+1] == '~':
			if end := findExactRun(source, i+2, '~', 2); end > i+2 {
				flush()
				inlines = append(inlines, Inline{Kind: InlineStrike, Children: parseInline(source[i+2:end], depth+1)})
				i = end + 2
				continue
			}
			text.WriteString("~~")
			i += 2
		case source[i] == '!' && i+1 < len(source) && source[i+1] == '[':
			if label, url, next, ok := parseLink(source, i+1); ok {
				flush()
				inlines = append(inlines, Inline{Kind: InlineImage, URL: url, Children: parseInline(label, depth+1)})
				i = next
				continue
			}
			text.WriteByte(source[i])
			i++
		case source[i] == '[':
			if label, url, next, ok := parseLink(source, i); ok {
				flush()
				inlines = append(inlines, Inline{Kind: InlineLink, URL: url, Children: parseInline(label, depth+1)})
				i = next
				continue
			}
			text.WriteByte(source[i])
			i++
		case source[i] == '<':
			if url, next, ok := parseAutolink(source, i); ok {
				flush()
				inlines = append(inlines, Inline{Kind: InlineLink, Text: url, URL: url})
				i = next
				continue
			}
			text.WriteByte(source[i])
			i++
		default:
			text.WriteByte(source[i])
			i++
		}
	}
	flush()
	return inlines
}

// parseEmphasis tries a strong or emphasis run starting at start. It reports
// the index after the closing marker, or false when the marker is literal.
func parseEmphasis(source string, start int, char byte, depth int, inlines *[]Inline, flush func()) (int, bool) {
	run := countRun(source, start, char)
	if run >= 2 {
		if end := findExactRun(source, start+2, char, 2); end > start+2 &&
			!isSpaceByte(source[start+2]) && !isSpaceByte(source[end-1]) {
			flush()
			*inlines = append(*inlines, Inline{
				Kind:     InlineStrong,
				Children: parseInline(source[start+2:end], depth+1),
			})
			return end + 2, true
		}
	}
	if run == 1 || run >= 3 {
		if end := findEmphasisClose(source, start+1, char); end > start+1 &&
			!isSpaceByte(source[start+1]) && !isSpaceByte(source[end-1]) {
			flush()
			*inlines = append(*inlines, Inline{
				Kind:     InlineEmphasis,
				Children: parseInline(source[start+1:end], depth+1),
			})
			return end + 1, true
		}
	}
	return start, false
}

func canOpenUnderscore(source string, index int) bool {
	if index+1 >= len(source) || isSpaceByte(source[index+1]) {
		return false
	}
	if index == 0 {
		return true
	}
	previous := rune(source[index-1])
	return !(previous >= '0' && previous <= '9') &&
		!(previous >= 'a' && previous <= 'z') &&
		!(previous >= 'A' && previous <= 'Z') && previous != '_'
}

func findEmphasisClose(source string, from int, char byte) int {
	for i := from; i < len(source); i++ {
		if source[i] == '\\' {
			i++
			continue
		}
		if source[i] != char {
			continue
		}
		run := countRun(source, i, char)
		if run != 1 {
			i += run - 1
			continue
		}
		if char == '_' && i+1 < len(source) {
			next := rune(source[i+1])
			if (next >= '0' && next <= '9') || (next >= 'a' && next <= 'z') ||
				(next >= 'A' && next <= 'Z') || next == '_' {
				continue
			}
		}
		return i
	}
	return -1
}

func parseLink(source string, open int) (label, url string, next int, ok bool) {
	if open >= len(source) || source[open] != '[' {
		return "", "", open, false
	}
	depth := 0
	closeBracket := -1
	for i := open; i < len(source); i++ {
		switch source[i] {
		case '\\':
			i++
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				closeBracket = i
			}
		}
		if closeBracket >= 0 {
			break
		}
	}
	if closeBracket < 0 || closeBracket+1 >= len(source) || source[closeBracket+1] != '(' {
		return "", "", open, false
	}
	depth = 0
	closeParen := -1
	for i := closeBracket + 2; i < len(source); i++ {
		switch source[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			if depth == 0 {
				closeParen = i
			} else {
				depth--
			}
		}
		if closeParen >= 0 {
			break
		}
	}
	if closeParen < 0 {
		return "", "", open, false
	}
	destination := strings.TrimSpace(source[closeBracket+2 : closeParen])
	if destination == "" {
		return "", "", open, false
	}
	// Drop an optional title after the URL.
	if space := strings.IndexAny(destination, " \t"); space >= 0 {
		destination = destination[:space]
	}
	destination = strings.TrimPrefix(destination, "<")
	destination = strings.TrimSuffix(destination, ">")
	if destination == "" {
		return "", "", open, false
	}
	return source[open+1 : closeBracket], destination, closeParen + 1, true
}

func parseAutolink(source string, open int) (string, int, bool) {
	close := strings.IndexByte(source[open+1:], '>')
	if close < 0 {
		return "", open, false
	}
	close += open + 1
	candidate := source[open+1 : close]
	if strings.ContainsAny(candidate, " \t\n") {
		return "", open, false
	}
	lower := strings.ToLower(candidate)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") &&
		!strings.HasPrefix(lower, "mailto:") {
		return "", open, false
	}
	return candidate, close + 1, true
}

func countRun(source string, start int, char byte) int {
	count := 0
	for start+count < len(source) && source[start+count] == char {
		count++
	}
	return count
}

func findExactRun(source string, from int, char byte, length int) int {
	for i := from; i+length <= len(source); i++ {
		if source[i] != char {
			continue
		}
		run := countRun(source, i, char)
		if run < length {
			i += run - 1
			continue
		}
		if run == length {
			return i
		}
		// A longer run contains a valid closing run at its end.
		if run > length {
			return i + run - length
		}
	}
	return -1
}

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\n' }

func isASCIIPunctuation(b byte) bool {
	switch b {
	case '!', '"', '#', '$', '%', '&', '\'', '(', ')', '*', '+', ',', '-', '.', '/',
		':', ';', '<', '=', '>', '?', '@', '[', '\\', ']', '^', '_', '`', '{', '|', '}', '~':
		return true
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// HTML renders the parsed blocks as an HTML fragment. Every text run and
// attribute is escaped; no source HTML passes through.
func HTML(blocks []Block) string {
	var builder strings.Builder
	writeHTMLBlocks(&builder, blocks, false)
	return builder.String()
}

// HTMLPage renders a complete, self-contained HTML document for one Note.
func HTMLPage(title, source string) string {
	var builder strings.Builder
	builder.WriteString("<!DOCTYPE html>\n<html><head><meta charset=\"utf-8\">")
	builder.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	builder.WriteString("<title>")
	builder.WriteString(html.EscapeString(title))
	builder.WriteString("</title><style>")
	builder.WriteString("body{margin:0;padding:32px;background:#fff;color:#1f2328;font-family:system-ui,-apple-system,'Segoe UI',sans-serif;line-height:1.55}")
	builder.WriteString("article{max-width:820px;margin:0 auto}")
	builder.WriteString(".note-title{margin:0 0 24px;font-size:2em;border-bottom:1px solid #d0d7de;padding-bottom:8px}")
	builder.WriteString("h1,h2,h3,h4,h5,h6{margin:1.4em 0 .5em;line-height:1.25}")
	builder.WriteString("pre{background:#f6f8fa;padding:12px;border-radius:6px;overflow:auto}")
	builder.WriteString("code{font-family:ui-monospace,SFMono-Regular,Consolas,'Liberation Mono',monospace;background:#f6f8fa;padding:.15em .35em;border-radius:4px}")
	builder.WriteString("pre code{padding:0;background:none}")
	builder.WriteString("blockquote{margin:1em 0;padding:.1em 1em;border-left:4px solid #d0d7de;color:#57606a}")
	builder.WriteString("table{border-collapse:collapse;margin:1em 0}")
	builder.WriteString("th,td{border:1px solid #d0d7de;padding:6px 12px}")
	builder.WriteString("th{background:#f6f8fa}")
	builder.WriteString("hr{border:0;border-top:1px solid #d0d7de;margin:1.5em 0}")
	builder.WriteString("a{color:#0969da}")
	builder.WriteString("</style></head><body><article><h1 class=\"note-title\">")
	builder.WriteString(html.EscapeString(title))
	builder.WriteString("</h1>")
	writeHTMLBlocks(&builder, Parse(source).Blocks, false)
	builder.WriteString("</article></body></html>\n")
	return builder.String()
}

func writeHTMLBlocks(builder *strings.Builder, blocks []Block, tight bool) {
	for _, block := range blocks {
		switch block.Kind {
		case ParagraphBlock:
			if tight {
				writeHTMLInlines(builder, block.Inlines)
				continue
			}
			builder.WriteString("<p>")
			writeHTMLInlines(builder, block.Inlines)
			builder.WriteString("</p>\n")
		case HeadingBlock:
			level := block.Level
			if level < 1 || level > 6 {
				level = 6
			}
			builder.WriteString("<h")
			builder.WriteByte(byte('0' + level))
			builder.WriteString(">")
			writeHTMLInlines(builder, block.Inlines)
			builder.WriteString("</h")
			builder.WriteByte(byte('0' + level))
			builder.WriteString(">\n")
		case CodeBlock:
			builder.WriteString("<pre><code")
			if block.Language != "" {
				builder.WriteString(" class=\"language-")
				builder.WriteString(html.EscapeString(block.Language))
				builder.WriteString("\"")
			}
			builder.WriteString(">")
			builder.WriteString(html.EscapeString(block.Code))
			builder.WriteString("</code></pre>\n")
		case QuoteBlock:
			builder.WriteString("<blockquote>\n")
			writeHTMLBlocks(builder, block.Blocks, false)
			builder.WriteString("</blockquote>\n")
		case ListBlock:
			if block.Ordered {
				start := block.Start
				if start <= 0 {
					start = 1
				}
				builder.WriteString("<ol")
				if start != 1 {
					builder.WriteString(" start=\"")
					builder.WriteString(strconv.Itoa(start))
					builder.WriteString("\"")
				}
				builder.WriteString(">\n")
			} else {
				builder.WriteString("<ul>\n")
			}
			for _, item := range block.Items {
				builder.WriteString("<li>")
				if len(item.Blocks) == 1 && item.Blocks[0].Kind == ParagraphBlock {
					writeHTMLInlines(builder, item.Blocks[0].Inlines)
				} else {
					writeHTMLBlocks(builder, item.Blocks, true)
				}
				builder.WriteString("</li>\n")
			}
			if block.Ordered {
				builder.WriteString("</ol>\n")
			} else {
				builder.WriteString("</ul>\n")
			}
		case RuleBlock:
			builder.WriteString("<hr>\n")
		case TableBlock:
			builder.WriteString("<table>\n<thead><tr>")
			for index, cell := range block.Header {
				builder.WriteString("<th")
				writeHTMLAlign(builder, block.Align, index)
				builder.WriteString(">")
				writeHTMLInlines(builder, cell)
				builder.WriteString("</th>")
			}
			builder.WriteString("</tr></thead>\n<tbody>\n")
			for _, row := range block.Rows {
				builder.WriteString("<tr>")
				for index := range block.Align {
					builder.WriteString("<td")
					writeHTMLAlign(builder, block.Align, index)
					builder.WriteString(">")
					if index < len(row) {
						writeHTMLInlines(builder, row[index])
					}
					builder.WriteString("</td>")
				}
				builder.WriteString("</tr>\n")
			}
			builder.WriteString("</tbody></table>\n")
		}
	}
}

func writeHTMLAlign(builder *strings.Builder, align []Alignment, index int) {
	if index >= len(align) {
		return
	}
	switch align[index] {
	case AlignLeft:
		builder.WriteString(" style=\"text-align:left\"")
	case AlignCenter:
		builder.WriteString(" style=\"text-align:center\"")
	case AlignRight:
		builder.WriteString(" style=\"text-align:right\"")
	}
}

func writeHTMLInlines(builder *strings.Builder, inlines []Inline) {
	for _, inline := range inlines {
		switch inline.Kind {
		case InlineText:
			builder.WriteString(html.EscapeString(inline.Text))
		case InlineCode:
			builder.WriteString("<code>")
			builder.WriteString(html.EscapeString(inline.Text))
			builder.WriteString("</code>")
		case InlineStrong:
			builder.WriteString("<strong>")
			writeHTMLInlines(builder, inline.Children)
			builder.WriteString("</strong>")
		case InlineEmphasis:
			builder.WriteString("<em>")
			writeHTMLInlines(builder, inline.Children)
			builder.WriteString("</em>")
		case InlineStrike:
			builder.WriteString("<del>")
			writeHTMLInlines(builder, inline.Children)
			builder.WriteString("</del>")
		case InlineLink:
			if !safeURL(inline.URL) {
				writeHTMLInlines(builder, inline.Children)
				if inline.Text != "" {
					builder.WriteString(html.EscapeString(inline.Text))
				}
				continue
			}
			builder.WriteString("<a href=\"")
			builder.WriteString(html.EscapeString(inline.URL))
			builder.WriteString("\" rel=\"noopener noreferrer\">")
			if len(inline.Children) > 0 {
				writeHTMLInlines(builder, inline.Children)
			} else {
				builder.WriteString(html.EscapeString(inline.Text))
			}
			builder.WriteString("</a>")
		case InlineImage:
			builder.WriteString("<span class=\"image\">")
			writeHTMLInlines(builder, inline.Children)
			if inline.URL != "" {
				builder.WriteString(" (")
				builder.WriteString(html.EscapeString(inline.URL))
				builder.WriteString(")")
			}
			builder.WriteString("</span>")
		}
	}
}

// safeURL keeps exported links to schemes a browser can open harmlessly.
func safeURL(url string) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "vbscript:") {
		return false
	}
	if index := strings.IndexByte(lower, ':'); index >= 0 {
		scheme := lower[:index]
		// A colon before any slash may be a scheme. Allow known safe ones.
		if !strings.ContainsAny(scheme, "/?#") {
			switch scheme {
			case "http", "https", "mailto", "file", "ftp":
				return true
			}
			return false
		}
	}
	return true
}
