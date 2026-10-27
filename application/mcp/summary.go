package mcp

import (
	"encoding/json"
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

// secretNameMarkers are the field-name fragments that mark a credential or
// file body. Both direct arguments and the fallback scan for malformed
// structured text use this one list, so a new marker cannot drift between them.
var secretNameMarkers = []string{
	"password", "passphrase", "secret", "token", "privatekey", "private_key",
	"apikey", "api_key", "credential", "content",
}

// secretArgument reports whether an argument likely carries a credential or
// file body. Its value is never echoed into a prompt or a log.
func secretArgument(name string) bool {
	lower := strings.ToLower(name)
	// "text" is the raw exported-host payload of vault_hosts_import. It can
	// contain passwords in several export formats, so it is treated as a
	// secret body rather than previewed.
	if lower == "text" {
		return true
	}
	for _, marker := range secretNameMarkers {
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
	value = redactNestedSecrets(value)
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

// redactedValue replaces credential-bearing fields inside an already-decoded
// structure. It recurses through arrays and objects so a JSON string argument
// (such as vault_hosts_create's hosts array) cannot carry a password into the
// approval prompt.
func redactedValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, entry := range typed {
			if secretArgument(key) {
				redacted[key] = "[redacted]"
				continue
			}
			redacted[key] = redactedValue(entry)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, entry := range typed {
			redacted[index] = redactedValue(entry)
		}
		return redacted
	default:
		return value
	}
}

// redactNestedSecrets sanitizes structured argument values, including JSON
// encoded in a string. A malformed JSON-looking value that names a secret is
// replaced as a whole because its fields cannot be localized safely.
func redactNestedSecrets(value any) any {
	switch typed := value.(type) {
	case map[string]any, []any:
		return redactedValue(typed)
	case string:
		trimmed := strings.TrimSpace(typed)
		if len(trimmed) == 0 || (trimmed[0] != '[' && trimmed[0] != '{') {
			return typed
		}
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			if namesSecretField(trimmed) {
				return "[structured value with secrets redacted]"
			}
			return typed
		}
		var trailing any
		if err := decoder.Decode(&trailing); err == nil {
			// More than one JSON value: not a structure this summary can
			// sanitize field by field.
			if namesSecretField(trimmed) {
				return "[structured value with secrets redacted]"
			}
			return typed
		}
		encoded, err := json.Marshal(redactedValue(decoded))
		if err != nil {
			return "[structured value with secrets redacted]"
		}
		return string(encoded)
	default:
		return value
	}
}

// namesSecretField reports whether text names a credential field, used only
// for malformed structured values that cannot be redacted in place.
func namesSecretField(text string) bool {
	lower := strings.ToLower(text)
	// A raw export payload uses the exact key "text"; check it separately so
	// unrelated names such as "context" are not redacted as a whole.
	if strings.Contains(lower, `"text"`) {
		return true
	}
	for _, marker := range secretNameMarkers {
		// Match the opening quote only: a malformed key such as password_hash
		// still carries the marker even when its closing quote is not intact.
		if strings.Contains(lower, `"`+marker) {
			return true
		}
	}
	return false
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
