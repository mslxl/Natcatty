package release

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// IsArtifact reports whether name is a release artifact handed to users: the
// macOS disk image, the Windows NSIS Setup executable, the Linux tar.gz and
// deb packages, and the Linux install script. The raw executable and the .app
// bundle are carried with them but are not installers of their own.
//
// This is the one rule for what a release contains: SHA256SUMS generation,
// verification, and collection all use it.
func IsArtifact(name string) bool {
	base := path.Base(name)
	switch {
	case strings.HasSuffix(base, ".dmg"),
		strings.HasSuffix(base, ".deb"),
		strings.HasSuffix(base, ".tar.gz"):
		return true
	case base == "install.sh":
		return true
	case strings.HasSuffix(base, ".exe") && strings.Contains(base, " Setup "):
		return true
	}
	return false
}

// ArtifactKind names what one downloaded workflow-artifact directory holds.
type ArtifactKind string

const (
	// ArtifactPlatform is a build job's directory: release artifacts and the
	// SHA256SUMS that list them.
	ArtifactPlatform ArtifactKind = "platform"
)

// ArtifactDir is one directory under a download root, classified.
type ArtifactDir struct {
	Path string
	Kind ArtifactKind
}

const sumsName = "SHA256SUMS"

// ClassifyArtifactDirs classifies every immediate subdirectory of root. The
// release workflow downloads the `natcatty-*` artifacts, which are exactly the
// platform build directories; the notes and aggregate checksum artifacts are
// named outside that pattern. This is the one place that decides what counts.
//
// It fails closed: a file where a directory belongs, a directory that is not a
// platform artifact set, and an empty or missing root are all errors.
func ClassifyArtifactDirs(root string) ([]ArtifactDir, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s holds no artifact directories", root)
	}
	dirs := make([]ArtifactDir, 0, len(entries))
	for _, entry := range entries {
		dirPath := filepath.Join(root, entry.Name())
		if !entry.IsDir() {
			return nil, fmt.Errorf("%s is not a directory; the download root must hold one artifact directory per upload",
				dirPath)
		}
		kind, err := ClassifyArtifactDir(dirPath)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, ArtifactDir{Path: dirPath, Kind: kind})
	}
	return dirs, nil
}

// ClassifyArtifactDir classifies one downloaded workflow-artifact directory.
// A directory is a platform artifact set only when it holds a regular
// SHA256SUMS; anything else is an error, so a notes-only directory, a stray
// file, an empty directory, or a missing checksum list cannot slip past the
// checksums job unnamed.
func ClassifyArtifactDir(dir string) (ArtifactKind, error) {
	sumsPath := filepath.Join(dir, sumsName)
	info, err := os.Stat(sumsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s is not a platform artifact set: it holds no regular %s", dir, sumsName)
		}
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", sumsPath)
	}
	return ArtifactPlatform, nil
}
