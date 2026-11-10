package plugin

import "testing"

func TestPluginAPIMatches(t *testing.T) {
	tests := []struct {
		declared string
		want     bool
	}{
		{"^0.1", true},
		{"^0.1.0", true},
		{"0.1.0", true},
		{"*", true},
		{"~0.1", true},
		{">=0.1.0, <0.2.0", true},
		{"^0.2", false},
		{"~0.2", false},
		{"^1.0", false},
		{"0.2.0", false},
		{">=0.2", false},
		{"<0.1.0", false},
		{"", false},
		{"not a range", false},
	}
	for _, test := range tests {
		t.Run(test.declared, func(t *testing.T) {
			if got := PluginAPIMatches(test.declared); got != test.want {
				t.Fatalf("PluginAPIMatches(%q) = %v, want %v", test.declared, got, test.want)
			}
		})
	}
}

func TestCompareFollowsSemverPrecedence(t *testing.T) {
	order := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
		"1.1.0",
	}
	for i := 0; i < len(order); i++ {
		for j := 0; j < len(order); j++ {
			left, ok := ParseSemver(order[i])
			if !ok {
				t.Fatalf("ParseSemver(%q) failed", order[i])
			}
			right, ok := ParseSemver(order[j])
			if !ok {
				t.Fatalf("ParseSemver(%q) failed", order[j])
			}
			want := compareInt(i, j)
			if got := left.Compare(right); got != want {
				t.Fatalf("%s.Compare(%s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestParseSemver(t *testing.T) {
	tests := []struct {
		raw  string
		ok   bool
		want Semver
	}{
		{"1.2.3", true, Semver{Major: 1, Minor: 2, Patch: 3}},
		{"0.1.0-beta.1", true, Semver{Major: 0, Minor: 1, Patch: 0, Pre: "beta.1"}},
		{"1.2.3+build.7", true, Semver{Major: 1, Minor: 2, Patch: 3}},
		{"1.2", false, Semver{}},
		{"1.2.3.4", false, Semver{}},
		{"01.2.3", false, Semver{}},
		{"1.2.3-01", false, Semver{}},
		{"v1.2.3", false, Semver{}},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, ok := ParseSemver(test.raw)
			if ok != test.ok || got != test.want {
				t.Fatalf("ParseSemver(%q) = %+v, %v; want %+v, %v", test.raw, got, ok, test.want, test.ok)
			}
		})
	}
}
