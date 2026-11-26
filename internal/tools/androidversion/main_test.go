// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"testing"
)

func TestVersionCode(t *testing.T) {
	tests := []struct {
		version string
		want    int
	}{
		{"0.0.1", 19999},
		{"0.1.0-beta.1", 1001001},
		{"0.1.0-beta.2", 1001002},
		{"0.1.0-rc.1", 1002001},
		{"0.1.0", 1009999},
		{"0.1.1-beta.1", 1011001},
		{"0.1.1", 1019999},
		{"0.2.0", 2009999},
		{"1.0.0", 100009999},
		// A leading v, as GitHub tags carry, and build metadata do not
		// change the number Android orders installs by.
		{"v0.1.0-beta.2", 1001002},
		{"0.1.0-beta.2+build.5", 1001002},
		// A pre-release without a number precedes the same pre-release
		// with one: beta < beta.1 < beta.999 < rc < final.
		{"0.1.0-beta", 1001000},
		{"0.1.0-beta.999", 1001999},
	}
	for _, tt := range tests {
		got, err := versionCode(tt.version)
		if err != nil {
			t.Errorf("versionCode(%q): %v", tt.version, err)
			continue
		}
		if got != tt.want {
			t.Errorf("versionCode(%q) = %d, want %d", tt.version, got, tt.want)
		}
	}
}

// TestVersionCodeIncreasesForSuccessiveReleases computes the code for a
// sequence of releases in SemVer order and requires each to be strictly
// greater than its predecessor. This is the property an in-place upgrade
// needs: Android installs a newer APK over an older one only when its
// versionCode is greater.
func TestVersionCodeIncreasesForSuccessiveReleases(t *testing.T) {
	releases := []string{
		"0.1.0-beta.1",
		"0.1.0-beta.2",
		"0.1.0-rc.1",
		"0.1.0",
		"0.1.1-beta.1",
		"0.1.1",
		"0.2.0-beta.1",
		"0.2.0",
		"1.0.0",
	}
	previous := 0
	for _, version := range releases {
		code, err := versionCode(version)
		if err != nil {
			t.Fatalf("versionCode(%q): %v", version, err)
		}
		if code <= previous {
			t.Fatalf("versionCode(%q) = %d, want greater than the previous release's %d", version, code, previous)
		}
		previous = code
	}
}

func TestVersionCodeRejectsMalformedVersions(t *testing.T) {
	for _, version := range []string{
		"",
		"v",
		"1.0",
		"1.0.0.0",
		"1..0",
		"-1.0.0",
		"1.0.0-",
		"1.0.0-dev.1",
		"1.0.0-beta.x",
		"1.0.0-beta.1000",
		"1.0.0-beta.1.2",
		"21.0.0",
		"0.100.0",
		"0.0.100",
	} {
		if code, err := versionCode(version); err == nil {
			t.Errorf("versionCode(%q) = %d, want an error", version, code)
		}
	}
}

func TestCheckIncrease(t *testing.T) {
	if _, err := checkIncrease("0.1.0", "0.1.0-beta.2"); err != nil {
		t.Fatalf("checkIncrease: %v", err)
	}
	if _, err := checkIncrease("0.1.0-rc.1", "0.1.0-beta.2"); err != nil {
		t.Fatalf("checkIncrease: %v", err)
	}
	for _, pair := range [][2]string{
		{"0.1.0", "0.1.0"},
		{"0.1.0-beta.1", "0.1.0-beta.2"},
		{"0.1.0", "0.1.1"},
	} {
		if _, err := checkIncrease(pair[0], pair[1]); err == nil {
			t.Errorf("checkIncrease(%q, %q) accepted a non-increasing version code", pair[0], pair[1])
		}
	}
	// A tag's leading v is part of the tag, not the version.
	got, err := checkIncrease("v0.1.0", "v0.1.0-rc.1")
	if err != nil {
		t.Fatalf("checkIncrease with tags: %v", err)
	}
	if got != 1009999 {
		t.Fatalf("checkIncrease with tags = %d, want 1009999", got)
	}
}

func TestVersionCodeIsWithinAndroidLimit(t *testing.T) {
	// 20.99.99 is the largest release this scheme represents; Android caps
	// versionCode at 2100000000.
	code, err := versionCode("20.99.99")
	if err != nil {
		t.Fatalf("versionCode(20.99.99): %v", err)
	}
	if code > 2100000000 {
		t.Fatalf("versionCode(20.99.99) = %d, above Android's 2100000000", code)
	}
}
