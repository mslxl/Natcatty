package terminal

import "testing"

func TestPasteLineCountIgnoresOneTrailingNewline(t *testing.T) {
	cases := map[string]int{
		"":            0,
		"one":         1,
		"one\n":       1,
		"one\ntwo":    2,
		"one\ntwo\n":  2,
		"one\r\ntwo":  2,
		"one\rtwo":    2,
		"\n":          0,
		"a\n\nb":      3,
		"a\nb\nc\n\n": 4,
	}
	for text, want := range cases {
		if got := PasteLineCount(text); got != want {
			t.Errorf("PasteLineCount(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestShouldConfirmPasteUsesTheOriginalDefaultThreshold(t *testing.T) {
	twoLines := "first\nsecond"
	if ShouldConfirmPaste(twoLines, false, DefaultPasteMinLines) {
		t.Fatal("protection off asked for confirmation")
	}
	if !ShouldConfirmPaste(twoLines, true, DefaultPasteMinLines) {
		t.Fatal("two lines were not guarded at the default threshold")
	}
	if ShouldConfirmPaste("one line\n", true, DefaultPasteMinLines) {
		t.Fatal("a single line with a trailing newline was guarded")
	}
	if ShouldConfirmPaste("first\nsecond", true, 3) {
		t.Fatal("two lines were guarded at a threshold of three")
	}
}

func TestNormalizePasteMinLinesKeepsTheOriginalRange(t *testing.T) {
	cases := map[int]int{0: 2, -4: 1, 1: 1, 50: 50, 1000: 1000, 5000: 1000}
	for input, want := range cases {
		if got := NormalizePasteMinLines(input); got != want {
			t.Errorf("NormalizePasteMinLines(%d) = %d, want %d", input, got, want)
		}
	}
}

func TestPreviewPasteIsBoundedAndNormalized(t *testing.T) {
	text, truncated := PreviewPaste("a\r\nb", 10)
	if text != "a\nb" || truncated {
		t.Fatalf("preview = %q, truncated %v", text, truncated)
	}
	text, truncated = PreviewPaste("日本語です", 3)
	if text != "日本語" || !truncated {
		t.Fatalf("preview = %q, truncated %v", text, truncated)
	}
}
