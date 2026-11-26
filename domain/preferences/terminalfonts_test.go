package preferences

import (
	"strings"
	"testing"
)

func TestTerminalFontFamilyAlwaysEndsInMonospace(t *testing.T) {
	for setting, want := range map[string]string{
		BundledTerminalFontID: BundledTerminalFontFamily + ", monospace",
		"menlo":               "Menlo, monospace",
		"jetbrains-mono":      "JetBrains Mono, monospace",
		"My Font":             "My Font, monospace",
		"":                    "monospace",
	} {
		if got := TerminalFontFamily(setting); got != want {
			t.Errorf("TerminalFontFamily(%q) = %q, want %q", setting, got, want)
		}
	}
	for _, font := range TerminalFonts() {
		if !strings.HasSuffix(font.Family, ", monospace") {
			t.Errorf("%s draws with %q, which lacks the monospace fallback", font.ID, font.Family)
		}
	}
}

func TestDefaultTerminalFontIsBundledMapleMonoNF(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin", "ios", "android"} {
		if got := DefaultTerminalFontFor(goos); got != BundledTerminalFontID {
			t.Errorf("DefaultTerminalFontFor(%q) = %q, want %q", goos, got, BundledTerminalFontID)
		}
		font, ok := LookupTerminalFont(DefaultTerminalFontFor(goos))
		if !ok {
			t.Errorf("the %s default is not in the catalog", goos)
			continue
		}
		if font.Family != BundledTerminalFontFamily+", monospace" {
			t.Errorf("the %s default family = %q, want %q", goos, font.Family, BundledTerminalFontFamily+", monospace")
		}
	}
}
