package release

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeArtifactFile(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyArtifactDirsNamesPlatforms(t *testing.T) {
	root := t.TempDir()
	sums := strings.Repeat("a", 64) + "  Natcatty 0.1.0.dmg\n"
	for _, dir := range []string{"natcatty-darwin-universal", "natcatty-linux", "natcatty-windows-amd64"} {
		writeArtifactFile(t, filepath.Join(root, dir, "SHA256SUMS"), sums)
	}
	writeArtifactFile(t, filepath.Join(root, "natcatty-darwin-universal", "Natcatty 0.1.0.dmg"), "dmg")
	writeArtifactFile(t, filepath.Join(root, "natcatty-linux", "install.sh"), "#!/bin/sh\n")
	writeArtifactFile(t, filepath.Join(root, "natcatty-windows-amd64", "Natcatty Setup 0.1.0 amd64.exe"), "setup")

	got, err := ClassifyArtifactDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []ArtifactDir{
		{Path: filepath.Join(root, "natcatty-darwin-universal"), Kind: ArtifactPlatform},
		{Path: filepath.Join(root, "natcatty-linux"), Kind: ArtifactPlatform},
		{Path: filepath.Join(root, "natcatty-windows-amd64"), Kind: ArtifactPlatform},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("classification = %+v, want %+v", got, want)
	}
}

func TestClassifyArtifactDirsRejectsWhatItCannotName(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  string
	}{
		{
			name: "release notes directory",
			setup: func(t *testing.T, root string) {
				writeArtifactFile(t, filepath.Join(root, "release-notes", "RELEASE_NOTES.md"), "# Natcatty 0.1.0\n")
			},
			want: "not a platform artifact set",
		},
		{
			name: "stray file without sums",
			setup: func(t *testing.T, root string) {
				writeArtifactFile(t, filepath.Join(root, "natcatty-mystery", "stray.bin"), "x")
			},
			want: "not a platform artifact set",
		},
		{
			name: "empty directory",
			setup: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "natcatty-empty"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "not a platform artifact set",
		},
		{
			name: "sha256sums is a directory",
			setup: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "natcatty-platform", "SHA256SUMS"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "not a regular file",
		},
		{
			name: "file where a directory belongs",
			setup: func(t *testing.T, root string) {
				writeArtifactFile(t, filepath.Join(root, "natcatty-stray"), "x")
			},
			want: "not a directory",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			_, err := ClassifyArtifactDirs(root)
			if err == nil {
				t.Fatal("the directory tree was classified")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not explain %q", err, test.want)
			}
		})
	}

	t.Run("no artifact directories", func(t *testing.T) {
		if _, err := ClassifyArtifactDirs(t.TempDir()); err == nil {
			t.Fatal("an empty download root was classified")
		}
	})
	t.Run("missing root", func(t *testing.T) {
		if _, err := ClassifyArtifactDirs(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("a missing download root was classified")
		}
	})
}

func TestIsArtifactNamesInstallersButNotRawExecutables(t *testing.T) {
	artifacts := []string{
		"darwin-universal/Natcatty 0.1.0.dmg",
		"windows-amd64/Natcatty Setup 0.1.0 amd64.exe",
		"linux-amd64/natcatty-0.1.0-linux-amd64.tar.gz",
		"linux-amd64/natcatty_0.1.0_amd64.deb",
		"linux-amd64/install.sh",
	}
	for _, name := range artifacts {
		if !IsArtifact(name) {
			t.Errorf("%s is not a release artifact", name)
		}
	}
	for _, name := range []string{
		"windows-amd64/Natcatty.exe",
		"linux-amd64/natcatty",
		"darwin-universal/Natcatty.app/Contents/MacOS/Natcatty",
		"linux-amd64/SHA256SUMS",
		"notes/RELEASE_NOTES.md",
	} {
		if IsArtifact(name) {
			t.Errorf("%s is not an installer handed to users", name)
		}
	}
}
