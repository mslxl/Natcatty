package release

import "testing"

func TestParseTagAcceptsStrictSemVer(t *testing.T) {
	for _, test := range []struct {
		tag     string
		version string
	}{
		{"v0.1.0", "0.1.0"},
		{"v1.2.3", "1.2.3"},
		{"v10.20.30", "10.20.30"},
		{"v0.1.0-rc.1", "0.1.0-rc.1"},
		{"v1.0.0-alpha", "1.0.0-alpha"},
		{"v2.3.4-beta.2.1", "2.3.4-beta.2.1"},
		{"v1.0.0-x-y-z.--", "1.0.0-x-y-z.--"},
		{"v1.0.0-0.1.2", "1.0.0-0.1.2"},
	} {
		version, err := ParseTag(test.tag)
		if err != nil {
			t.Errorf("%s: %v", test.tag, err)
			continue
		}
		if got := version.String(); got != test.version {
			t.Errorf("%s: parsed %q, want %q", test.tag, got, test.version)
		}
	}
}

func TestParseTagRejectsInvalidSemVer(t *testing.T) {
	for _, tag := range []string{
		"",
		"v",
		"0.1.0",
		"1.2.3",
		"V1.2.3",
		"v1",
		"v1.2",
		"v1.2.3.4",
		"v01.2.3",
		"v1.02.3",
		"v1.2.03",
		"v1.2.-3",
		"v1.2.3-",
		"v1.2.3-01",
		"v1.2.3-rc.01",
		"v1.2.3-rc_1",
		"v1.2.3-rc..1",
		"v1.2.3+build",
		"v1.2.3-rc.1+build.5",
		"v1.2.3 ",
		" v1.2.3",
		"v1.2.3\t",
	} {
		if version, err := ParseTag(tag); err == nil {
			t.Errorf("%q accepted as %s", tag, version)
		}
	}
}

func TestParseVersionRejectsTagPrefixAndMetadata(t *testing.T) {
	for _, version := range []string{"v1.2.3", "1.2.3+meta", "1.2.3-rc.1+meta", "1.2", "", "01.0.0"} {
		if parsed, err := ParseVersion(version); err == nil {
			t.Errorf("%q accepted as %s", version, parsed)
		}
	}
}

func TestValidateTagMatchesManifestVersion(t *testing.T) {
	for _, test := range []struct {
		name    string
		tag     string
		version string
		wantErr bool
	}{
		{"exact", "v0.1.0", "0.1.0", false},
		{"prerelease", "v0.1.0-rc.1", "0.1.0-rc.1", false},
		{"different patch", "v0.1.1", "0.1.0", true},
		{"no prefix", "0.1.0", "0.1.0", true},
		{"other spelling", "v0.1.0", "00.1.0", true},
		{"tag prerelease only", "v0.1.0-rc.1", "0.1.0", true},
		{"manifest prerelease only", "v0.1.0", "0.1.0-rc.1", true},
		{"empty manifest", "v0.1.0", "", true},
	} {
		err := ValidateTag(test.tag, test.version)
		if test.wantErr && err == nil {
			t.Errorf("%s: ValidateTag(%q, %q) accepted", test.name, test.tag, test.version)
		}
		if !test.wantErr && err != nil {
			t.Errorf("%s: ValidateTag(%q, %q): %v", test.name, test.tag, test.version, err)
		}
	}
}
