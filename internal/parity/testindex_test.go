package parity

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestBuildTestIndexSeparatesCITestsFromTaggedOnes(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "application/vault/store_test.go", `package vault

import "testing"

func TestSaveKeepsTheEntry(t *testing.T) {}
`)
	writeTestFile(t, root, "application/vault/selftest_test.go", `//go:build selftest

package vault

import "testing"

func TestSelftestOnlyRoundTrip(t *testing.T) {}
`)
	writeTestFile(t, root, "app/smoke_test.go", `package app

import "testing"

func TestLaunchSmoke(t *testing.T) {}
`)
	// The same name in a tagged and an untagged file: either declaration is
	// enough for the default CI build to run it.
	writeTestFile(t, root, "app/taggedsmoke_test.go", `//go:build launchsmoke

package app

import "testing"

func TestLaunchSmoke(t *testing.T) {}
`)
	// Build output and scratch trees must not contribute tests.
	writeTestFile(t, root, "build/out_test.go", `package build

import "testing"

func TestStaleBuildCopy(t *testing.T) {}
`)
	writeTestFile(t, root, ".scratch/notes_test.go", `package notes

import "testing"

func TestScratchCopy(t *testing.T) {}
`)

	tests, err := BuildTestIndex(root)
	if err != nil {
		t.Fatalf("BuildTestIndex: %v", err)
	}

	cases := []struct {
		entry    string
		resolves bool
		inCI     bool
	}{
		{"application/vault.TestSaveKeepsTheEntry", true, true},
		{"./application/vault.TestSaveKeepsTheEntry", true, true},
		{ModulePath + "/application/vault.TestSaveKeepsTheEntry", true, true},
		{"application/vault.TestSelftestOnlyRoundTrip", true, false},
		{"TestSelftestOnlyRoundTrip", true, false},
		{"app.TestLaunchSmoke", true, true},
		{"app.TestStaleBuildCopy", false, false},
		{"build.TestStaleBuildCopy", false, false},
		{"TestStaleBuildCopy", false, false},
		{"TestScratchCopy", false, false},
		{"application/vault.TestMissing", false, false},
	}
	for _, tc := range cases {
		if got := tests.Resolves(tc.entry); got != tc.resolves {
			t.Errorf("Resolves(%q) = %v, want %v", tc.entry, got, tc.resolves)
		}
		if got := tests.RunsInCI(tc.entry); got != tc.inCI {
			t.Errorf("RunsInCI(%q) = %v, want %v", tc.entry, got, tc.inCI)
		}
	}
}

func TestHasCIRunnableTestIgnoresManualQAAndTaggedTests(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "app/selftest_test.go", `//go:build selftest

package app

import "testing"

func TestSelftestOnly(t *testing.T) {}
`)
	writeTestFile(t, root, "app/smoke_test.go", `package app

import "testing"

func TestLaunchSmoke(t *testing.T) {}
`)
	tests, err := BuildTestIndex(root)
	if err != nil {
		t.Fatalf("BuildTestIndex: %v", err)
	}

	taggedOnly := &Row{ID: "app.selftest", AcceptanceTests: []string{"app.TestSelftestOnly", "QA-001"}}
	if HasCIRunnableTest(taggedOnly, tests) {
		t.Errorf("a selftest-tagged test and manual QA must not satisfy the CI rule")
	}
	mixed := &Row{ID: "app.mixed", AcceptanceTests: []string{"QA-001", "app.TestLaunchSmoke"}}
	if !HasCIRunnableTest(mixed, tests) {
		t.Errorf("an untagged test alongside manual QA must satisfy the CI rule")
	}
	none := &Row{ID: "app.none"}
	if HasCIRunnableTest(none, tests) {
		t.Errorf("a row with no acceptance tests must not satisfy the CI rule")
	}
}

func TestBuildTestIndexReportsUnparsableTestFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "broken/broken_test.go", "package broken\n\nfunc TestBroken( {\n")
	if _, err := BuildTestIndex(root); err == nil {
		t.Fatalf("BuildTestIndex accepted a file that does not parse")
	}
}
