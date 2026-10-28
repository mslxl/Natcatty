package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/testutil"
)

const fixtureChangelog = `# Changelog

## [Unreleased]

### Added

- Unreleased work.

## [1.2.3] - 2026-10-28

### Added

- The shipped feature.

### Fixed

- A defect.
`

func writeChangelog(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGenerateWritesReleaseNotes(t *testing.T) {
	changelog := writeChangelog(t, fixtureChangelog)
	out := filepath.Join(t.TempDir(), "RELEASE_NOTES.md")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-version", "v1.2.3", "-changelog", changelog, "-out", out}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	notes, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read notes: %v", err)
	}
	for _, want := range []string{"# Natcatty 1.2.3", "Released 2026-10-28.", "- The shipped feature."} {
		if !strings.Contains(string(notes), want) {
			t.Errorf("notes missing %q:\n%s", want, notes)
		}
	}
	if strings.Contains(string(notes), "Unreleased work") {
		t.Errorf("notes contain unreleased work:\n%s", notes)
	}
	if !strings.Contains(stdout.String(), "wrote release notes for 1.2.3") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestGenerateToStdout(t *testing.T) {
	changelog := writeChangelog(t, fixtureChangelog)
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-version", "1.2.3", "-changelog", changelog}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(stdout.String(), "# Natcatty 1.2.3\n") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestMissingAndDuplicateVersionsFail(t *testing.T) {
	changelog := writeChangelog(t, fixtureChangelog)
	var stdout, stderr bytes.Buffer
	err := run([]string{"-version", "9.9.9", "-changelog", changelog}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), `no section for version "9.9.9"`) {
		t.Fatalf("error = %v, want a missing-version error", err)
	}

	duplicate := writeChangelog(t, fixtureChangelog+"\n## [1.2.3] - 2026-10-29\n\n- Again.\n")
	err = run([]string{"-version", "1.2.3", "-changelog", duplicate}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("error = %v, want a duplicate-version error", err)
	}
}

func TestInvalidVersionsFail(t *testing.T) {
	changelog := writeChangelog(t, fixtureChangelog)
	for _, version := range []string{"v", "1.2", "01.2.3", "1.2.3+build"} {
		var stdout, stderr bytes.Buffer
		if err := run([]string{"-version", version, "-changelog", changelog}, &stdout, &stderr); err == nil {
			t.Errorf("version %q was accepted", version)
		}
	}
}

func TestRealChangelogGeneratesForTheManifestVersion(t *testing.T) {
	root := testutil.ModuleRoot(t)
	out := filepath.Join(t.TempDir(), "RELEASE_NOTES.md")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"-version", "0.1.0",
		"-changelog", filepath.Join(root, "CHANGELOG.md"),
		"-out", out,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("run on the real changelog: %v\n%s", err, stderr.String())
	}
	notes, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(notes), "# Natcatty 0.1.0") {
		t.Fatalf("real notes do not name 0.1.0:\n%s", notes)
	}
}
