package session

import (
	"strings"
	"testing"
)

func TestStripTerminalControlKeepsTextAndDropsSequences(t *testing.T) {
	raw := "\x1b[31mred\x1b[0m\r\n" +
		"\x1b]0;title\x07visible\n" +
		"\x1b[2Kcleared\n" +
		"tab\there\b\n" +
		"bad\x00byte"
	got := stripTerminalControl(raw)
	if !strings.Contains(got, "red\n") || !strings.Contains(got, "visible\n") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\x07") || strings.Contains(got, "\x00") {
		t.Fatalf("control bytes remain: %q", got)
	}
	if !strings.Contains(got, "tab\there\n") {
		t.Fatalf("tab was not preserved: %q", got)
	}
}

func TestTerminalLinesMatchesTheOriginalSplitting(t *testing.T) {
	if lines := TerminalLines(""); lines != nil {
		t.Fatalf("empty text = %v, want nil", lines)
	}
	lines := TerminalLines("a\r\nb\rc\n")
	want := []string{"a", "b", "c", ""}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v", lines)
	}
	for index := range want {
		if lines[index] != want[index] {
			t.Fatalf("lines = %v, want %v", lines, want)
		}
	}
}

func TestResolveTerminalLineWindowMatchesTheOriginal(t *testing.T) {
	tests := []struct {
		name          string
		rangeName     TerminalContextRange
		total         int
		viewportStart int
		viewportEnd   int
		startLine     int
		maxLines      int
		wantStart     int
		wantEnd       int
		wantMax       int
	}{
		{"empty buffer", TerminalRangeViewport, 0, 0, -1, 0, 0, 0, -1, TerminalContextDefaultMaxLines},
		{"viewport default", TerminalRangeViewport, 200, 150, 199, 0, 0, 150, 199, TerminalContextDefaultMaxLines},
		{"head caps at max", TerminalRangeHead, 200, 0, 0, 0, 500, 0, 199, TerminalContextMaxLines},
		{"tail", TerminalRangeTail, 200, 0, 0, 0, 30, 170, 199, 30},
		{"lines clamps tail", TerminalRangeLines, 100, 0, 0, 95, 20, 95, 99, 20},
		{"lines start clamps", TerminalRangeLines, 100, 0, 0, 500, 20, 99, 99, 20},
		{"unknown defaults to viewport", TerminalContextRange("nope"), 10, 8, 9, 0, 5, 8, 9, 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			window := ResolveTerminalLineWindow(test.rangeName, test.total,
				test.viewportStart, test.viewportEnd, test.startLine, test.maxLines)
			if window.StartLine != test.wantStart || window.EndLine != test.wantEnd ||
				window.MaxLines != test.wantMax {
				t.Fatalf("window = %+v, want %d..%d max %d",
					window, test.wantStart, test.wantEnd, test.wantMax)
			}
		})
	}
}

func TestUsesAlternateScreen(t *testing.T) {
	if !usesAlternateScreen("before\x1b[?1049hafter") {
		t.Fatal("1049h was not recognized")
	}
	if usesAlternateScreen("plain") {
		t.Fatal("plain text looked like an alternate screen")
	}
}
