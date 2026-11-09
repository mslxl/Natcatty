package styletest

import (
	"testing"

	"github.com/mslxl/natcatty/ui/kit"
)

func TestEachRunsOncePerStyleWithItActive(t *testing.T) {
	var seen []string
	Each(t, func(t *testing.T) { seen = append(seen, kit.CurrentStyle().ID()) })
	if len(seen) != 2 || seen[0] != kit.StyleClassic || seen[1] != kit.StyleMacOS {
		t.Fatalf("styles seen = %v, want [classic macos]", seen)
	}
	if got := kit.CurrentStyle().ID(); got != kit.StyleClassic {
		t.Fatalf("style after Each = %q, want classic", got)
	}
}
