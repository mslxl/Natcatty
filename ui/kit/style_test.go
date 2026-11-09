package kit

import "testing"

func TestStyleForFallsBackToClassic(t *testing.T) {
	for id, want := range map[string]string{"macos": StyleMacOS, "classic": StyleClassic, "": StyleClassic, "aqua": StyleClassic} {
		if got := StyleFor(id).ID(); got != want {
			t.Errorf("StyleFor(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestSetStyleChangesTheActiveStyleAndNilRestoresClassic(t *testing.T) {
	SetStyle(MacOS)
	if CurrentStyle().ID() != StyleMacOS {
		t.Fatalf("current = %q after SetStyle(MacOS)", CurrentStyle().ID())
	}
	SetStyle(nil)
	if CurrentStyle().ID() != StyleClassic {
		t.Fatalf("current = %q after SetStyle(nil)", CurrentStyle().ID())
	}
}
