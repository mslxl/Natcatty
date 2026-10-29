package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildReleasecheck builds the tool the release workflow runs, so the script
// test exercises the real classifier and the real sum verifier.
func buildReleasecheck(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "releasecheck.exe")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build releasecheck: %v\n%s", err, output)
	}
	return binary
}

// verifyDownloads runs scripts/verify-downloads.sh, the step the release
// workflow's checksums job invokes. The tool binary is supplied so the script
// does not have to run go itself.
func verifyDownloads(t *testing.T, binary, root string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "verify-downloads.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, root)
	cmd.Env = append(os.Environ(), "RELEASECHECK="+binary)
	output, runErr := cmd.CombinedOutput()
	return string(output), runErr
}

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
}

// downloadTree creates one platform artifact directory whose SHA256SUMS
// matches its DMG, as the checksums job receives it. The notes and aggregate
// checksum artifacts stay outside the workflow's `natcatty-*` pattern, so a
// valid download root holds platform directories only.
func downloadTree(t *testing.T, binary string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "downloads")
	platform := filepath.Join(root, "natcatty-darwin-universal")
	writeFile(t, filepath.Join(platform, "Natcatty 0.1.0.dmg"), "dmg")
	generate := exec.Command(binary, "generate-sums", "-dir", platform)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate-sums: %v\n%s", err, output)
	}
	return root
}

func TestVerifyDownloadsScriptAcceptsPlatforms(t *testing.T) {
	requireBash(t)
	binary := buildReleasecheck(t)
	root := downloadTree(t, binary)

	output, err := verifyDownloads(t, binary, root)
	if err != nil {
		t.Fatalf("the script rejected a valid download tree: %v\n%s", err, output)
	}
	if !strings.Contains(output, "verified") {
		t.Errorf("output lacks %q:\n%s", "verified", output)
	}
}

func TestVerifyDownloadsScriptFailsClosed(t *testing.T) {
	requireBash(t)
	binary := buildReleasecheck(t)

	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "bad checksum",
			setup: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "natcatty-darwin-universal",
					"Natcatty 0.1.0.dmg"), "tampered")
			},
		},
		{
			name: "unlisted artifact",
			setup: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "natcatty-darwin-universal",
					"Natcatty 0.1.1.dmg"), "unlisted")
			},
		},
		{
			name: "release notes directory",
			setup: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "release-notes", "RELEASE_NOTES.md"), "# Natcatty 0.1.0\n")
			},
		},
		{
			name: "unknown directory",
			setup: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "natcatty-mystery", "stray.bin"), "x")
			},
		},
		{
			name: "platform without sums",
			setup: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "natcatty-linux-amd64", "install.sh"), "#!/bin/sh\n")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := downloadTree(t, binary)
			test.setup(t, root)
			output, err := verifyDownloads(t, binary, root)
			if err == nil {
				t.Fatalf("the script accepted a broken download tree:\n%s", output)
			}
		})
	}

	t.Run("no downloads", func(t *testing.T) {
		output, err := verifyDownloads(t, binary, filepath.Join(t.TempDir(), "empty"))
		if err == nil {
			t.Fatalf("the script accepted a missing download root:\n%s", output)
		}
	})

	t.Run("notes only", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "downloads")
		writeFile(t, filepath.Join(root, "release-notes", "RELEASE_NOTES.md"), "# Natcatty 0.1.0\n")
		output, err := verifyDownloads(t, binary, root)
		if err == nil {
			t.Fatalf("the script accepted a download tree with no platform artifacts:\n%s", output)
		}
	})
}
