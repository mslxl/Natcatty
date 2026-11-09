package preferences

import (
	"runtime"
	"testing"
)

// A Mac starts in the macOS style; every other platform keeps Classic.
func TestInterfaceStyleDefaultsPerPlatform(t *testing.T) {
	for goos, want := range map[string]InterfaceStyle{
		"darwin":  InterfaceMacOS,
		"linux":   InterfaceClassic,
		"windows": InterfaceClassic,
	} {
		if got := DefaultInterfaceStyle(goos); got != want {
			t.Errorf("DefaultInterfaceStyle(%q) = %q, want %q", goos, got, want)
		}
	}
	if got := Defaults().Appearance.InterfaceStyle; got != DefaultInterfaceStyle(runtime.GOOS) {
		t.Fatalf("Defaults() style = %q on %s", got, runtime.GOOS)
	}
}

func TestNormalizeKeepsValidInterfaceStyleOnly(t *testing.T) {
	for _, style := range []InterfaceStyle{InterfaceClassic, InterfaceMacOS} {
		settings := Defaults()
		settings.Appearance.InterfaceStyle = style
		if got := Normalize(settings).Appearance.InterfaceStyle; got != style {
			t.Errorf("Normalize changed %q to %q", style, got)
		}
	}
	for _, bad := range []InterfaceStyle{"", "aqua"} {
		settings := Defaults()
		settings.Appearance.InterfaceStyle = bad
		if got := Normalize(settings).Appearance.InterfaceStyle; got != DefaultInterfaceStyle(runtime.GOOS) {
			t.Errorf("Normalize(%q) = %q, want the platform default", bad, got)
		}
	}
}

// The best style differs by platform, so a synced document never carries it
// and a download never changes it.
func TestInterfaceStyleStaysOnTheDevice(t *testing.T) {
	settings := Defaults()
	settings.Appearance.InterfaceStyle = InterfaceClassic
	portable := Portable(settings)
	if portable.Appearance.InterfaceStyle != "" {
		t.Fatalf("portable appearance carries the style %q", portable.Appearance.InterfaceStyle)
	}
	remote := portable
	remote.Appearance.InterfaceStyle = InterfaceMacOS
	if got := MergePortable(settings, remote).Appearance.InterfaceStyle; got != InterfaceClassic {
		t.Fatalf("merge replaced the local style with %q", got)
	}
}

func TestInterfaceStyleResetsAlone(t *testing.T) {
	settings := Defaults()
	settings.Appearance.InterfaceStyle = InterfaceClassic
	settings.Appearance.UIFont = "Inter"
	if _, ok := GroupOf(FieldInterfaceStyle); !ok {
		t.Fatal("the style field has no group")
	}
	reset := ResetField(settings, FieldInterfaceStyle)
	if reset.Appearance.InterfaceStyle != DefaultInterfaceStyle(runtime.GOOS) || reset.Appearance.UIFont != "Inter" {
		t.Fatalf("reset = %+v", reset.Appearance)
	}
}
