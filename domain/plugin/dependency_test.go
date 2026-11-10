package plugin

import (
	"strings"
	"testing"
)

func TestMatchesRangeUsesSemverRanges(t *testing.T) {
	tests := []struct {
		raw     string
		version string
		want    bool
	}{
		{"^1.2.0", "1.2.0", true},
		{"^1.2.0", "1.9.9", true},
		{"^1.2.0", "2.0.0", false},
		{"~1.2.0", "1.2.9", true},
		{"~1.2.0", "1.3.0", false},
		{"*", "9.9.9-beta.1", true},
		{">=1.0.0, <2.0.0", "1.5.0", true},
		{"1.2.3", "1.2.3", true},
		{"^1.0", "1.2.3", true},
		{"", "1.0.0", false},
	}
	for _, test := range tests {
		t.Run(test.raw+"_"+test.version, func(t *testing.T) {
			version, ok := ParseSemver(test.version)
			if !ok {
				t.Fatalf("ParseSemver(%q) failed", test.version)
			}
			if got := RangeContains(test.raw, version); got != test.want {
				t.Fatalf("RangeContains(%q, %q) = %v, want %v", test.raw, test.version, got, test.want)
			}
		})
	}
}

func TestManifestRejectsInvalidDependencies(t *testing.T) {
	tests := []struct {
		name string
		dep  Dependency
	}{
		{"empty id", Dependency{Version: "^1.0.0"}},
		{"bad id", Dependency{ID: "Acme", Version: "^1.0.0"}},
		{"empty range", Dependency{ID: "acme.core"}},
		{"bad range", Dependency{ID: "acme.core", Version: "latest"}},
		{"self dependency", Dependency{ID: "acme.hello", Version: "^1.0.0"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			manifest.Dependencies = []Dependency{test.dep}
			if err := manifest.Validate(); err == nil {
				t.Fatalf("Validate accepted dependency %+v", test.dep)
			}
		})
	}
}

func TestManifestAcceptsADeclaredDependency(t *testing.T) {
	manifest := validManifest()
	manifest.Dependencies = []Dependency{{ID: "acme.core", Version: "^2.1.0"}}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestDependencyConflictAndCyclesAreRejected(t *testing.T) {
	graph := map[string][]Dependency{
		"acme.root": {{ID: "acme.a", Version: "^1.0.0"}},
		"acme.a":    {{ID: "acme.b", Version: "^1.0.0"}},
		"acme.b":    {{ID: "acme.a", Version: "^1.0.0"}},
	}
	if _, err := ResolveDependencies("acme.root", graph, nil); err == nil ||
		!strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}

	conflict := map[string][]Dependency{
		"acme.root": {
			{ID: "acme.a", Version: "^1.0.0"},
			{ID: "acme.a", Version: "^2.0.0"},
		},
	}
	if _, err := ResolveDependencies("acme.root", conflict, nil); err == nil ||
		!strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestResolveDependenciesOrdersDeterministically(t *testing.T) {
	graph := map[string][]Dependency{
		"acme.root": {
			{ID: "acme.z", Version: "^1.0.0"},
			{ID: "acme.a", Version: "^1.0.0"},
		},
		"acme.a": {{ID: "acme.shared", Version: "^1.0.0"}},
		"acme.z": {{ID: "acme.shared", Version: ">=1.2.0, <2.0.0"}},
	}
	resolved, err := ResolveDependencies("acme.root", graph, map[string][]string{
		"acme.shared": {"1.3.0"},
		"acme.a":      {"1.0.0"},
		"acme.z":      {"1.4.0"},
	})
	if err != nil {
		t.Fatalf("ResolveDependencies: %v", err)
	}
	want := []string{"acme.a", "acme.shared", "acme.z"}
	if len(resolved) != len(want) {
		t.Fatalf("resolved = %+v, want %v", resolved, want)
	}
	for i := range want {
		if resolved[i].ID != want[i] {
			t.Fatalf("resolved order = %+v, want %v", resolved, want)
		}
	}
}

func TestResolveDependenciesRequiresAnAvailableSatisfyingVersion(t *testing.T) {
	graph := map[string][]Dependency{"acme.root": {{ID: "acme.a", Version: "^2.0.0"}}}
	if _, err := ResolveDependencies("acme.root", graph, map[string][]string{"acme.a": {"1.9.0"}}); err == nil ||
		!strings.Contains(err.Error(), "satisfies") {
		t.Fatalf("missing version error = %v", err)
	}
}

func TestResolveDependenciesPicksTheHighestSatisfyingVersion(t *testing.T) {
	graph := map[string][]Dependency{"acme.root": {{ID: "acme.a", Version: "^1.0.0"}}}
	resolved, err := ResolveDependencies("acme.root", graph, map[string][]string{
		"acme.a": {"1.0.0", "1.9.0", "1.2.0", "2.0.0"},
	})
	if err != nil {
		t.Fatalf("ResolveDependencies: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Version != "1.9.0" {
		t.Fatalf("resolved = %+v, want acme.a 1.9.0", resolved)
	}
}
