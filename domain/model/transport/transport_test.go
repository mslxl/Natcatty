package transport

import (
	"errors"
	"testing"
)

func TestResolveFallsBackToTheReleaseDefault(t *testing.T) {
	// A Host saved before the setting existed has no override, and a profile
	// saved before the setting existed has no global value. Both resolve to
	// whichever implementation ReleasePolicy currently selects, so this test
	// checks the resolution rule rather than pinning the release value.
	got, err := Resolve(Unspecified, Unspecified)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := CurrentDefault(); got != want {
		t.Fatalf("Resolve = %q, want the release default %q", got, want)
	}
}

func TestResolveUsesTheGlobalDefault(t *testing.T) {
	for _, value := range []Implementation{Bundled, InProcess} {
		got, err := Resolve(value, Unspecified)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", value, err)
		}
		if got != value {
			t.Fatalf("Resolve(%q) = %q, want the global default", value, got)
		}
	}
}

func TestResolveHostOverrideWins(t *testing.T) {
	cases := []struct {
		global   Implementation
		override Implementation
		want     Implementation
	}{
		{Bundled, Bundled, Bundled},
		{Bundled, InProcess, InProcess},
		{InProcess, Bundled, Bundled},
		{InProcess, InProcess, InProcess},
	}
	for _, c := range cases {
		got, err := Resolve(c.global, c.override)
		if err != nil {
			t.Fatalf("Resolve(%q, %q): %v", c.global, c.override, err)
		}
		if got != c.want {
			t.Fatalf("Resolve(%q, %q) = %q, want %q", c.global, c.override, got, c.want)
		}
	}
}

func TestResolveRejectsUnknownValuesInsteadOfFallingBack(t *testing.T) {
	cases := []struct {
		name     string
		global   Implementation
		override Implementation
	}{
		{"stale global", Implementation("bundled_v2"), Unspecified},
		{"stale override", Bundled, Implementation("bundled_v2")},
		{"stale override wins over a valid global", InProcess, Implementation("future")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Resolve(c.global, c.override)
			if !errors.Is(err, ErrUnknownImplementation) {
				t.Fatalf("Resolve(%q, %q) error = %v, want ErrUnknownImplementation", c.global, c.override, err)
			}
			if got != Unspecified {
				t.Fatalf("Resolve(%q, %q) = %q, want no implementation on failure", c.global, c.override, got)
			}
		})
	}
}

func TestResolveTreatsAnEmptyOverrideAsInheritance(t *testing.T) {
	got, err := Resolve(Bundled, Unspecified)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != Bundled {
		t.Fatalf("Resolve = %q, want bundled", got)
	}
}

func TestKnownReportsOnlySelectableValues(t *testing.T) {
	if !Known(Bundled) || !Known(InProcess) {
		t.Fatal("Known rejected a defined implementation")
	}
	if Known(Unspecified) || Known(Implementation("future")) {
		t.Fatal("Known accepted an undefined implementation")
	}
}
