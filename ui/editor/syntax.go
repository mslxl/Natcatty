package editor

// Ticket 56: a dependency-light syntax highlighter. A Grammar classifies
// runs of a document's text; the view paints those runs through MyGo's
// TextRanges, which keeps the TextArea editable and its caret in place.
// Grammars are picked by file name, and a name with no grammar stays plain.

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// TokenKind classifies a highlighted run. The zero kind is a comment so a
// zero Token is never mistaken for text that carries no style.
type TokenKind int

const (
	TokenComment TokenKind = iota
	TokenString
	TokenDirective
	TokenVariable
)

// Token is a run of text Start up to End, in runes from the start of the
// document.
type Token struct {
	Start, End int
	Kind       TokenKind
}

// Grammar turns a document into the runs worth highlighting. Implementations
// are stateless: the view calls one every frame with the buffer's current
// text.
type Grammar interface {
	Highlight(text string) []Token
}

// GrammarForName selects the grammar of a file by its name or extension. It
// returns nil for plain text, which is every name the app does not know.
func GrammarForName(name string) Grammar {
	base := name
	if index := strings.LastIndexAny(base, `/\`); index >= 0 {
		base = base[index+1:]
	}
	base = strings.ToLower(base)
	if base == "nginx.conf" || strings.HasSuffix(base, ".conf") {
		return nginxGrammar{}
	}
	return nil
}

// nginxGrammar highlights Nginx configuration: # comments, single- and
// double-quoted strings, the directive that opens each statement, and
// $variables, including those inside strings.
type nginxGrammar struct{}

func (nginxGrammar) Highlight(text string) []Token {
	var tokens []Token
	// statement is true at the start of a directive and false after one,
	// until ; { or } ends the statement. It carries across lines because an
	// Nginx directive may span them.
	statement := true
	offset := 0
	for _, line := range strings.Split(text, "\n") {
		tokens = highlightNginxLine(line, offset, &statement, tokens)
		offset += utf8.RuneCountInString(line) + 1
	}
	return tokens
}

func highlightNginxLine(line string, offset int, statement *bool, tokens []Token) []Token {
	runes := []rune(line)
	add := func(start, end int, kind TokenKind) {
		if end > start {
			tokens = append(tokens, Token{Start: offset + start, End: offset + end, Kind: kind})
		}
	}
	for index := 0; index < len(runes); {
		switch r := runes[index]; {
		case r == '#':
			add(index, len(runes), TokenComment)
			return tokens
		case r == '"' || r == '\'':
			end := index + 1
			for end < len(runes) {
				if runes[end] == '\\' {
					end += 2
					continue
				}
				if runes[end] == r {
					end++
					break
				}
				end++
			}
			if end > len(runes) {
				end = len(runes)
			}
			// The string is added first: a variable inside it is added
			// after, and TextRanges paints the last run that sets a color.
			add(index, end, TokenString)
			index = scanNginxVariables(runes, index, end, offset, &tokens)
		case r == ';' || r == '{' || r == '}':
			*statement = true
			index++
		case unicode.IsSpace(r):
			index++
		case r == '$':
			if next, ok := nginxVariableAt(runes, index, len(runes), offset, &tokens); ok {
				index = next
			} else {
				index++
			}
		default:
			end := index
			for end < len(runes) && !isNginxDelimiter(runes[end]) {
				end++
			}
			if end == index {
				index++
				continue
			}
			if *statement && isNginxDirectiveStart(runes[index]) {
				add(index, end, TokenDirective)
				*statement = false
			}
			index = end
		}
	}
	return tokens
}

// scanNginxVariables adds the $variables in runes from up to end and
// returns where scanning resumes.
func scanNginxVariables(runes []rune, from, end, offset int, tokens *[]Token) int {
	index := from
	for index < end {
		next, ok := nginxVariableAt(runes, index, end, offset, tokens)
		if ok {
			index = next
			continue
		}
		index++
	}
	return index
}

// nginxVariableAt adds the variable starting at start, a $name, ${name}, or
// a capture such as $1, and returns the rune after it.
func nginxVariableAt(runes []rune, start, end, offset int, tokens *[]Token) (int, bool) {
	if runes[start] != '$' || start+1 >= end {
		return start, false
	}
	index := start + 1
	if runes[index] == '{' {
		index++
		for index < end && runes[index] != '}' {
			index++
		}
		if index < end {
			index++
		}
	} else {
		for index < end && isNginxVariableRune(runes[index]) {
			index++
		}
	}
	if index-start <= 1 {
		return start, false
	}
	*tokens = append(*tokens, Token{Start: offset + start, End: offset + index, Kind: TokenVariable})
	return index, true
}

// isNginxDelimiter reports a rune that ends an unquoted word.
func isNginxDelimiter(r rune) bool {
	return unicode.IsSpace(r) || r == ';' || r == '{' || r == '}' ||
		r == '#' || r == '"' || r == '\'' || r == '$'
}

// isNginxDirectiveStart reports a rune that may open a directive.
func isNginxDirectiveStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

// isNginxVariableRune reports a rune of an unbraced variable's name.
func isNginxVariableRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}
