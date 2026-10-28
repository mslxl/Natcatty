package releasenotes

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/release"
	"github.com/mslxl/natcatty/internal/testutil"
)

const sampleChangelog = `# Changelog

All notable changes to this project are documented here.

## [Unreleased]

### Added

- Work that has not shipped yet.

## [0.1.0] - 2026-10-28

### Added

- Native app shell.
- Vault and terminal.

### Fixed

- A release-blocking contrast problem.

## [0.0.9] - 2026-10-01

### Added

- Old work.
`

func TestParseReadsVersionSections(t *testing.T) {
	sections, err := Parse([]byte(sampleChangelog))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(sections) != 3 {
		t.Fatalf("sections = %d, want 3", len(sections))
	}
	if sections[0].Version != "Unreleased" || sections[0].Date != "" {
		t.Errorf("first section = %+v", sections[0])
	}
	if sections[1].Version != "0.1.0" || sections[1].Date != "2026-10-28" {
		t.Errorf("second section = %+v", sections[1])
	}
	if !strings.Contains(sections[1].Body, "### Added") || !strings.Contains(sections[1].Body, "A release-blocking contrast problem.") {
		t.Errorf("0.1.0 body = %q", sections[1].Body)
	}
	if strings.Contains(sections[1].Body, "Old work") {
		t.Errorf("0.1.0 body leaked the next section: %q", sections[1].Body)
	}
}

func TestGenerateRendersOnlyTheRequestedVersion(t *testing.T) {
	notes, err := Generate([]byte(sampleChangelog), "0.1.0")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	text := string(notes)
	for _, want := range []string{
		"# Natcatty 0.1.0",
		"Released 2026-10-28.",
		"- Native app shell.",
		"### Fixed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("notes missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Unreleased", "Old work", "# Changelog"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("notes contain %q:\n%s", unwanted, text)
		}
	}

	again, err := Generate([]byte(sampleChangelog), "0.1.0")
	if err != nil {
		t.Fatalf("Generate again: %v", err)
	}
	if !bytes.Equal(notes, again) {
		t.Error("generation is not deterministic")
	}
}

func TestGenerateRejectsMissingVersion(t *testing.T) {
	_, err := Generate([]byte(sampleChangelog), "9.9.9")
	if err == nil || !strings.Contains(err.Error(), `no section for version "9.9.9"`) {
		t.Fatalf("error = %v, want a missing-version error", err)
	}

	_, err = Generate([]byte(sampleChangelog), "")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v, want an empty-version error", err)
	}
}

func TestGenerateRejectsDuplicateVersion(t *testing.T) {
	duplicate := sampleChangelog + "\n## [0.1.0] - 2026-10-29\n\n### Added\n\n- Duplicate.\n"
	_, err := Generate([]byte(duplicate), "0.1.0")
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("error = %v, want a duplicate-version error", err)
	}
}

func TestParseRejectsMalformedDocuments(t *testing.T) {
	if _, err := Parse([]byte("# Changelog\n\nNo versions.\n")); err == nil {
		t.Fatal("Parse accepted a changelog with no version section")
	}
	duplicate := "## [1.0.0] - 2026-01-01\n\n- A\n\n## [1.0.0] - 2026-01-02\n\n- B\n"
	if _, err := Parse([]byte(duplicate)); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("error = %v, want a duplicate error", err)
	}
}

func TestGenerateRejectsAnEmptyVersionBody(t *testing.T) {
	empty := "## [0.1.0] - 2026-10-28\n\n## [0.0.9] - 2026-10-01\n\n- Old.\n"
	_, err := Generate([]byte(empty), "0.1.0")
	if err == nil || !strings.Contains(err.Error(), "has no changes") {
		t.Fatalf("error = %v, want an empty-body error", err)
	}
}

func TestGenerateNormalizesCRLF(t *testing.T) {
	crlf := strings.ReplaceAll(sampleChangelog, "\n", "\r\n")
	lf, err := Generate([]byte(sampleChangelog), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	converted, err := Generate([]byte(crlf), "0.1.0")
	if err != nil {
		t.Fatalf("Generate CRLF: %v", err)
	}
	if !bytes.Equal(lf, converted) {
		t.Errorf("CRLF input changed the output:\n%q\n%q", lf, converted)
	}
}

// TestGenerateReleaseNotesForTheRealChangelog keeps the shipped changelog and
// mygo.json in agreement: the release workflow generates notes for exactly the
// manifest version, so a changelog that forgets the shipped version fails here.
func TestGenerateReleaseNotesForTheRealChangelog(t *testing.T) {
	root := testutil.ModuleRoot(t)
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	manifestData, err := os.ReadFile(filepath.Join(root, "mygo.json"))
	if err != nil {
		t.Fatalf("read mygo.json: %v", err)
	}
	manifest, err := release.ParseManifest(manifestData)
	if err != nil {
		t.Fatalf("parse mygo.json: %v", err)
	}
	notes, err := Generate(changelog, manifest.Version)
	if err != nil {
		t.Fatalf("generate notes for %s: %v", manifest.Version, err)
	}
	if !strings.Contains(string(notes), "# Natcatty "+manifest.Version) {
		t.Errorf("notes do not name version %s:\n%s", manifest.Version, notes)
	}
	if !strings.Contains(string(notes), "### ") {
		t.Errorf("notes have no change headings:\n%s", notes)
	}
	again, err := Generate(changelog, manifest.Version)
	if err != nil || !bytes.Equal(notes, again) {
		t.Errorf("real changelog generation is not deterministic (%v)", err)
	}
}
