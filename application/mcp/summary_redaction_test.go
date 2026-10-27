package mcp

import (
	"strings"
	"testing"
)

// TestMalformedStructuredArgumentsStillRedactBodyFields covers the fallback
// path: text that looks like JSON but is incomplete cannot be redacted field by
// field, so the whole value must disappear when it names a body field. Content
// and the import "text" payload are the two markers most likely to carry a
// credential in the original write tools.
func TestMalformedStructuredArgumentsStillRedactBodyFields(t *testing.T) {
	for _, raw := range []string{
		`[{"hostname":"example","content":"TOP-SECRET-BODY"`,
		`{"note":{"content":"TOP-SECRET-BODY"}`,
		`[{"hostname":"example","text":"TOP-SECRET-BODY"`,
	} {
		summary := SummarizeArguments(map[string]any{"payload": raw})
		if strings.Contains(summary, "TOP-SECRET-BODY") {
			t.Fatalf("malformed structured value leaked a body: %q", summary)
		}
	}
}
