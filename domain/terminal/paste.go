package terminal

import "strings"

// DefaultPasteMinLines is the original's minimum line count for a guarded
// paste. A one-line paste never asks for confirmation.
const DefaultPasteMinLines = 2

// NormalizePasteMinLines validates a stored threshold. Zero means unset and
// becomes the original default; other values are clamped to the original's
// 1..1000 range.
func NormalizePasteMinLines(lines int) int {
	if lines == 0 {
		return DefaultPasteMinLines
	}
	if lines < 1 {
		return 1
	}
	if lines > 1000 {
		return 1000
	}
	return lines
}

// NormalizePasteText converts the line endings a clipboard may carry to the
// single newline form the count and preview use.
func NormalizePasteText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// PasteLineCount returns the line count shown to the user. One trailing
// newline is not an extra line, matching the original confirmation.
func PasteLineCount(text string) int {
	normalized := NormalizePasteText(text)
	normalized = strings.TrimSuffix(normalized, "\n")
	if normalized == "" {
		return 0
	}
	return strings.Count(normalized, "\n") + 1
}

// ShouldConfirmPaste reports whether a paste needs confirmation. Protection
// off is always direct, as in the original.
func ShouldConfirmPaste(text string, protection bool, minLines int) bool {
	if !protection {
		return false
	}
	lines := PasteLineCount(text)
	return lines > 0 && lines >= NormalizePasteMinLines(minLines)
}

// PreviewPaste returns at most limit runes of the normalized text, so a
// confirmation banner can show what will be sent without growing without
// bound. It reports whether text was truncated.
func PreviewPaste(text string, limit int) (string, bool) {
	normalized := NormalizePasteText(text)
	if limit <= 0 {
		return "", normalized != ""
	}
	runes := []rune(normalized)
	if len(runes) <= limit {
		return string(runes), false
	}
	return string(runes[:limit]), true
}
