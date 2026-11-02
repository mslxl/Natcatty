package preferences

import (
	"strings"
	"testing"
)

func TestTerminalFontFamilyAlwaysEndsInMonospace(t *testing.T) {
	for setting, want := range map[string]string{
		"menlo":          "Menlo, monospace",
		"jetbrains-mono": "JetBrains Mono, monospace",
		"My Font":        "My Font, monospace",
		"":               "monospace",
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

func TestDefaultTerminalFontIsOneTheOSShips(t *testing.T) {
	for goos, want := range map[string]string{"windows": "consolas", "linux": "dejavu-sans-mono", "darwin": "menlo"} {
		if got := DefaultTerminalFontFor(goos); got != want {
			t.Errorf("DefaultTerminalFontFor(%q) = %q, want %q", goos, got, want)
		}
		if _, ok := LookupTerminalFont(DefaultTerminalFontFor(goos)); !ok {
			t.Errorf("the %s default is not in the catalog", goos)
		}
	}
}
