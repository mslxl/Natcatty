package plugin

import (
	"context"
	"strings"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// LanguageBuiltin is the host-owned base layer. Highlight always runs for a
// built-in language and can never be replaced by a plugin; the remaining
// capabilities, when wired, stack underneath plugin providers. Formatting is
// the exception: it is a fallback used only when no plugin provider exists.
type LanguageBuiltin struct {
	Highlight     func(LanguageDocument) []LanguageHighlight
	Complete      func(context.Context, LanguageDocument) []LanguageCompletionItem
	Diagnostics   func(context.Context, LanguageDocument) []LanguageDiagnostic
	Format        func(context.Context, LanguageDocument) []LanguageTextEdit
	Hover         func(context.Context, LanguageDocument) *LanguageHover
	SignatureHelp func(context.Context, LanguageDocument) *LanguageSignatureHelp
	Navigation    func(context.Context, LanguageDocument) []LanguageLocation
	CodeActions   func(context.Context, LanguageDocument) []LanguageCodeAction
}

// DefaultLanguageBuiltin returns the host's base highlighter. It tokenizes
// comments, strings, numbers, and keywords for every built-in language; a
// plugin-owned language has no host base layer until its provider supplies
// one. The host never parses semantic structure here.
func DefaultLanguageBuiltin() LanguageBuiltin {
	return LanguageBuiltin{Highlight: defaultBaseHighlight}
}

// builtinResult produces the host-owned contribution for one capability.
func (h *LanguageHost) builtinResult(ctx context.Context, document LanguageDocument,
	capability domainplugin.LanguageCapability) LanguageDocumentResults {
	result := LanguageDocumentResults{
		DocumentID: document.ID,
		Capability: capability,
	}
	builtin := h.builtin
	switch capability {
	case domainplugin.LanguageHighlight:
		if builtin.Highlight != nil {
			result.Highlights = builtin.Highlight(document)
		}
	case domainplugin.LanguageCompletion:
		if builtin.Complete != nil {
			result.Completions = builtin.Complete(ctx, document)
		}
	case domainplugin.LanguageDiagnostics:
		if builtin.Diagnostics != nil {
			result.Diagnostics = builtin.Diagnostics(ctx, document)
		}
	case domainplugin.LanguageFormatting:
		if builtin.Format != nil {
			result.Formatting = builtin.Format(ctx, document)
		}
	case domainplugin.LanguageHover:
		if builtin.Hover != nil {
			result.Hover = builtin.Hover(ctx, document)
		}
	case domainplugin.LanguageSignatureHelp:
		if builtin.SignatureHelp != nil {
			result.Signature = builtin.SignatureHelp(ctx, document)
		}
	case domainplugin.LanguageNavigation:
		if builtin.Navigation != nil {
			result.Navigation = builtin.Navigation(ctx, document)
		}
	case domainplugin.LanguageCodeActions:
		if builtin.CodeActions != nil {
			result.CodeActions = builtin.CodeActions(ctx, document)
		}
	}
	return result
}

// mergeBuiltin applies the base layer before any plugin contribution.
func (h *LanguageHost) mergeBuiltin(result *LanguageDocumentResults, builtin LanguageDocumentResults) {
	result.Highlights = append(result.Highlights, builtin.Highlights...)
	result.Completions = append(result.Completions, builtin.Completions...)
	result.Diagnostics = append(result.Diagnostics, builtin.Diagnostics...)
	result.Navigation = append(result.Navigation, builtin.Navigation...)
	result.CodeActions = append(result.CodeActions, builtin.CodeActions...)
	if builtin.Hover != nil {
		result.Hover = builtin.Hover
	}
	if builtin.Signature != nil {
		result.Signature = builtin.Signature
	}
}

// mergeProvider stacks one validated provider contribution over what is
// already present. Completion deduplicates by label; diagnostics concatenate;
// highlight ranges resolve by priority (the caller iterates highest first);
// hover and signature help stack; navigation deduplicates targets; code
// actions concatenate.
func (h *LanguageHost) mergeProvider(result *LanguageDocumentResults,
	provider *languageProvider, contribution LanguageDocumentResults) {
	switch result.Capability {
	case domainplugin.LanguageHighlight:
		for _, token := range contribution.Highlights {
			for _, remaining := range subtractLanguageRange(token.Range, result.Highlights) {
				result.Highlights = append(result.Highlights, LanguageHighlight{
					Range: remaining, Kind: token.Kind,
				})
			}
		}
	case domainplugin.LanguageCompletion:
		seen := make(map[string]bool, len(result.Completions)+len(contribution.Completions))
		for _, item := range result.Completions {
			seen[item.Label] = true
		}
		for _, item := range contribution.Completions {
			if seen[item.Label] {
				continue
			}
			seen[item.Label] = true
			result.Completions = append(result.Completions, item)
		}
	case domainplugin.LanguageDiagnostics:
		result.Diagnostics = append(result.Diagnostics, contribution.Diagnostics...)
	case domainplugin.LanguageHover:
		if contribution.Hover != nil {
			if result.Hover == nil {
				result.Hover = contribution.Hover
			} else {
				format := "plain"
				if result.Hover.Format == "markdown" || contribution.Hover.Format == "markdown" {
					format = "markdown"
				}
				contents := result.Hover.Contents
				if contents != "" && contribution.Hover.Contents != "" {
					contents += "\n\n"
				}
				contents += contribution.Hover.Contents
				result.Hover = &LanguageHover{Contents: contents, Format: format}
			}
		}
	case domainplugin.LanguageSignatureHelp:
		if contribution.Signature != nil {
			if result.Signature == nil {
				result.Signature = contribution.Signature
			} else {
				seen := make(map[string]bool, len(result.Signature.Signatures))
				for _, signature := range result.Signature.Signatures {
					seen[signature.Label] = true
				}
				for _, signature := range contribution.Signature.Signatures {
					if seen[signature.Label] {
						continue
					}
					seen[signature.Label] = true
					result.Signature.Signatures = append(result.Signature.Signatures, signature)
				}
			}
		}
	case domainplugin.LanguageNavigation:
		seen := make(map[string]bool, len(result.Navigation))
		for _, location := range result.Navigation {
			seen[languageLocationKey(location)] = true
		}
		for _, location := range contribution.Navigation {
			key := languageLocationKey(location)
			if seen[key] {
				continue
			}
			seen[key] = true
			result.Navigation = append(result.Navigation, location)
		}
	case domainplugin.LanguageCodeActions:
		result.CodeActions = append(result.CodeActions, contribution.CodeActions...)
	}
	result.Truncated = result.Truncated || contribution.Truncated
}

func languageLocationKey(location LanguageLocation) string {
	label := location.Label
	return location.DocumentID + ":" +
		positionKey(location.Range.Start) + "-" + positionKey(location.Range.End) + ":" + label
}

func positionKey(position LanguagePosition) string {
	return strings.Join([]string{
		u32String(position.Line), u32String(position.Column),
	}, ":")
}

func u32String(value uint32) string {
	if value == 0 {
		return "0"
	}
	var digits [10]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}

// subtractLanguageRange removes every part of candidate covered by the
// accepted highlight ranges, returning the visible remainders. This is how a
// lower-priority provider is clipped by the base layer and higher-priority
// providers without losing its non-overlapping tokens.
func subtractLanguageRange(candidate LanguageRange,
	accepted []LanguageHighlight) []LanguageRange {
	remaining := []LanguageRange{candidate}
	for _, highlight := range accepted {
		var next []LanguageRange
		for _, current := range remaining {
			next = append(next, subtractOneLanguageRange(current, highlight.Range)...)
		}
		remaining = next
		if len(remaining) == 0 {
			return nil
		}
	}
	return remaining
}

func subtractOneLanguageRange(candidate, blocker LanguageRange) []LanguageRange {
	if !languageRangesOverlap(candidate, blocker) {
		return []LanguageRange{candidate}
	}
	var out []LanguageRange
	if compareLanguagePosition(candidate.Start, blocker.Start) < 0 {
		out = append(out, LanguageRange{Start: candidate.Start, End: blocker.Start})
	}
	if compareLanguagePosition(candidate.End, blocker.End) > 0 {
		out = append(out, LanguageRange{Start: blocker.End, End: candidate.End})
	}
	return out
}

func languageRangesOverlap(a, b LanguageRange) bool {
	return compareLanguagePosition(a.Start, b.End) < 0 &&
		compareLanguagePosition(b.Start, a.End) < 0
}

func compareLanguagePosition(a, b LanguagePosition) int {
	switch {
	case a.Line < b.Line:
		return -1
	case a.Line > b.Line:
		return 1
	case a.Column < b.Column:
		return -1
	case a.Column > b.Column:
		return 1
	default:
		return 0
	}
}

// defaultBaseHighlight tokenizes the base layer for built-in languages. It is
// deliberately language-agnostic: comments, strings, numbers, and a shared
// keyword table. A plugin provider may add ranges on top but can never replace
// these.
func defaultBaseHighlight(document LanguageDocument) []LanguageHighlight {
	if document.LanguageID == "" || !domainplugin.IsBuiltinLanguageID(document.LanguageID) {
		return nil
	}
	lineComment := "//"
	blockComment := true
	switch document.LanguageID {
	case "sql":
		lineComment = "--"
		blockComment = true
	case "shell", "yaml", "toml", "python":
		lineComment = "#"
		blockComment = false
	}
	text := document.Content
	var tokens []LanguageHighlight
	line, column := uint32(0), uint32(0)
	advance := func() {
		column++
	}
	advanceLine := func() {
		line++
		column = 0
	}
	for i := 0; i < len(text); {
		c := text[i]
		if c == '\n' {
			advanceLine()
			i++
			continue
		}
		if strings.HasPrefix(text[i:], lineComment) {
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				end = len(text) - i
			}
			tokens = append(tokens, LanguageHighlight{
				Range: LanguageRange{
					Start: LanguagePosition{Line: line, Column: column},
					End:   LanguagePosition{Line: line, Column: column + uint32(end)},
				},
				Kind: "comment",
			})
			column += uint32(end)
			i += end
			continue
		}
		if blockComment && strings.HasPrefix(text[i:], "/*") {
			end := strings.Index(text[i+2:], "*/")
			endLine, endColumn := line, column+2
			if end < 0 {
				end = len(text) - i
				for j := i; j < len(text); j++ {
					if text[j] == '\n' {
						endLine++
						endColumn = 0
					} else {
						endColumn++
					}
				}
			} else {
				end += 2
				for j := i; j < i+end; j++ {
					if text[j] == '\n' {
						endLine++
						endColumn = 0
					} else {
						endColumn++
					}
				}
			}
			tokens = append(tokens, LanguageHighlight{
				Range: LanguageRange{
					Start: LanguagePosition{Line: line, Column: column},
					End:   LanguagePosition{Line: endLine, Column: endColumn},
				},
				Kind: "comment",
			})
			line, column = endLine, endColumn
			i += end
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			quote := c
			j := i + 1
			for j < len(text) {
				if text[j] == '\\' && j+1 < len(text) {
					j += 2
					continue
				}
				if text[j] == quote || text[j] == '\n' {
					break
				}
				j++
			}
			endLine, endColumn := line, column
			for k := i; k < j; k++ {
				if text[k] == '\n' {
					endLine++
					endColumn = 0
				} else {
					endColumn++
				}
			}
			tokens = append(tokens, LanguageHighlight{
				Range: LanguageRange{
					Start: LanguagePosition{Line: line, Column: column},
					End:   LanguagePosition{Line: endLine, Column: endColumn},
				},
				Kind: "string",
			})
			line, column = endLine, endColumn
			i = j
			continue
		}
		if c >= '0' && c <= '9' {
			j := i
			for j < len(text) && (text[j] >= '0' && text[j] <= '9' || text[j] == '.' ||
				text[j] >= 'a' && text[j] <= 'f' || text[j] >= 'A' && text[j] <= 'F' ||
				text[j] == 'x' || text[j] == 'X') {
				j++
			}
			tokens = append(tokens, LanguageHighlight{
				Range: LanguageRange{
					Start: LanguagePosition{Line: line, Column: column},
					End:   LanguagePosition{Line: line, Column: column + uint32(j-i)},
				},
				Kind: "number",
			})
			column += uint32(j - i)
			i = j
			continue
		}
		if isLanguageIdentStart(c) {
			j := i + 1
			for j < len(text) && isLanguageIdentPart(text[j]) {
				j++
			}
			word := text[i:j]
			if languageKeywords[strings.ToLower(word)] {
				tokens = append(tokens, LanguageHighlight{
					Range: LanguageRange{
						Start: LanguagePosition{Line: line, Column: column},
						End:   LanguagePosition{Line: line, Column: column + uint32(j-i)},
					},
					Kind: "keyword",
				})
			}
			column += uint32(j - i)
			i = j
			continue
		}
		advance()
		i++
	}
	return tokens
}

var languageKeywords = map[string]bool{
	"select": true, "from": true, "where": true, "insert": true, "update": true,
	"delete": true, "join": true, "group": true, "order": true, "limit": true,
	"null": true, "true": true, "false": true, "and": true, "or": true, "not": true,
	"func": true, "let": true, "const": true, "var": true, "if": true, "else": true,
	"for": true, "while": true, "return": true, "type": true, "struct": true,
	"class": true, "import": true, "export": true, "package": true, "go": true,
	"defer": true, "switch": true, "case": true, "break": true, "continue": true,
	"fn": true, "pub": true, "use": true, "match": true, "impl": true, "trait": true,
}

func isLanguageIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isLanguageIdentPart(c byte) bool {
	return isLanguageIdentStart(c) || c >= '0' && c <= '9'
}
