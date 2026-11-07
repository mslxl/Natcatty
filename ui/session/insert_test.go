package sessionui

import "testing"

func TestSanitizeInsertTextKeepsCommandLineEndingsAndDropsControls(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"single line", "git status", "git status"},
		{"crlf", "touch marker\r\necho run", "touch marker\necho run"},
		{"cr", "touch marker\recho run", "touch marker\necho run"},
		{"trailing newline", "echo done\r\n", "echo done\n"},
		{"tab kept", "printf 'a\tb'", "printf 'a\tb'"},
		{"escape and cursor controls removed",
			"echo \x1b[201~safe\x08", "echo [201~safe"},
		{"zero-width removed", "git​ status", "git status"},
	}
	for _, test := range tests {
		if got := sanitizeInsertText(test.in); got != test.want {
			t.Errorf("%s: sanitizeInsertText(%q) = %q, want %q",
				test.name, test.in, got, test.want)
		}
	}
}

func TestBracketedPasteEnabledReadsTheTerminalSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		want     bool
	}{
		{"enabled", "\x1b[?2004h", true},
		{"disabled after enabled", "\x1b[?2004h\x1b[?2004l", false},
		{"other modes only", "\x1b[?1004h\x1b[?1h", false},
		{"multi-parameter sequence", "\x1b[?1;2004h", true},
	}
	for _, test := range tests {
		if got := bracketedPasteEnabled([]byte(test.snapshot)); got != test.want {
			t.Errorf("%s: bracketedPasteEnabled(%q) = %v, want %v",
				test.name, test.snapshot, got, test.want)
		}
	}
}
