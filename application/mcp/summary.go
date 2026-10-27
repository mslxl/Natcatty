package mcp

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// approvalSummaryLimit bounds the argument summary shown in an approval
// prompt. It is long enough to identify the action and short enough not to
// dominate the card at the minimum window size.
const approvalSummaryLimit = 300

// valuePreviewLimit bounds one scalar argument in the summary.
const valuePreviewLimit = 120

// reservedArguments are transport and scope fields that never belong in an
// approval summary.
var reservedArguments = map[string]bool{
	"_mcpClient":       true,
	"chatSessionId":    true,
	"scopedSessionIds": true,
}

// secretArgument reports whether an argument likely carries a credential or
// file body. Its value is never echoed into a prompt or a log.
func secretArgument(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range []string{
		"password", "passphrase", "secret", "token", "privatekey", "private_key",
		"apikey", "api_key", "credential", "content",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// SummarizeArguments renders a deterministic, redacted one-line summary of a
// tool call's arguments for the approval prompt. Secrets and file contents are
// replaced by a placeholder; everything else is truncated. The original
// bridge showed raw arguments; the ticket requires redaction.
func SummarizeArguments(args map[string]any) string {
	if len(args) == 0 {
		return "(no arguments)"
	}
	names := make([]string, 0, len(args))
	for name := range args {
		if reservedArguments[name] {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return "(no arguments)"
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		value := args[name]
		if secretArgument(name) {
			if text, ok := value.(string); ok {
				parts = append(parts, fmt.Sprintf("%s=[redacted %d chars]", name, utf8.RuneCountInString(text)))
			} else {
				parts = append(parts, name+"=[redacted]")
			}
			continue
		}
		parts = append(parts, name+"="+previewValue(value))
	}
	summary := strings.Join(parts, " ")
	if utf8.RuneCountInString(summary) > approvalSummaryLimit {
		summary = truncateRunes(summary, approvalSummaryLimit) + "…"
	}
	return summary
}

// previewValue renders one non-secret argument value on one line.
func previewValue(value any) string {
	var text string
	switch typed := value.(type) {
	case string:
		text = typed
	case nil:
		text = "null"
	case bool:
		text = strconv.FormatBool(typed)
	case float64:
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		text = strconv.Itoa(typed)
	case int64:
		text = strconv.FormatInt(typed, 10)
	default:
		text = fmt.Sprintf("%v", typed)
	}
	text = strings.ReplaceAll(text, "\r", "\\r")
	text = strings.ReplaceAll(text, "\n", "\\n")
	text = strings.ReplaceAll(text, "\t", "\\t")
	if utf8.RuneCountInString(text) > valuePreviewLimit {
		text = truncateRunes(text, valuePreviewLimit) + "…"
	}
	return strconv.Quote(text)
}

// truncateRunes cuts text to limit runes without splitting a character.
func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for index := range text {
		if count == limit {
			return text[:index]
		}
		count++
	}
	return text
}
