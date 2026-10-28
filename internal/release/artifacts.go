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
// verification, collection, and workflow-artifact directory classification all
// use it.
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
	// ArtifactReleaseNotes is the validate job's directory: the generated
	// RELEASE_NOTES.md and nothing else. It has no SHA256SUMS because notes are
	// not a release artifact.
	ArtifactReleaseNotes ArtifactKind = "notes"
)

// ArtifactDir is one directory under a download root, classified.
type ArtifactDir struct {
	Path string
	Kind ArtifactKind
}

const (
	sumsName  = "SHA256SUMS"
	notesName = "RELEASE_NOTES.md"
)

// ClassifyArtifactDirs classifies every immediate subdirectory of root. The
// release workflow downloads `natcatty-*` artifacts and must verify exactly
// the platform directories, skip the release-notes directory, and fail on
// anything else; this is the one place that decides which is which.
//
// It fails closed: a file where a directory belongs, a directory that is
// neither a platform nor the notes, a missing or empty RELEASE_NOTES.md, and
// an empty root are all errors.
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

// ClassifyArtifactDir classifies one downloaded workflow-artifact directory as
// a platform directory when it carries SHA256SUMS, or as the release-notes
// directory when it carries only a non-empty RELEASE_NOTES.md. Anything else
// is an error, so a new upload cannot slip past the checksums job unnamed.
func ClassifyArtifactDir(dir string) (ArtifactKind, error) {
	sumsPath := filepath.Join(dir, sumsName)
	if info, err := os.Stat(sumsPath); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", sumsPath)
		}
		return ArtifactPlatform, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	notesPath := filepath.Join(dir, notesName)
	notesFound := false
	for _, entry := range entries {
		if entry.Name() != notesName {
			return "", fmt.Errorf("%s holds %s, which is neither a platform artifact nor the release notes",
				dir, entry.Name())
		}
		notesFound = true
	}
	if !notesFound {
		return "", fmt.Errorf("%s holds neither %s nor %s", dir, sumsName, notesName)
	}
	info, err := os.Stat(notesPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", notesPath)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("%s is empty", notesPath)
	}
	return ArtifactReleaseNotes, nil
}
