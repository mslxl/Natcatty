package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mslxl/natcatty/internal/release"
)

// assetRename is one flat-set filename change from the on-disk name to the
// name GitHub publishes.
type assetRename struct {
	From string
	To   string
}

// normalizeAssets renames the release assets in a flat release directory the
// way GitHub does for release assets: every space in the filename becomes a
// dot. Only the immediate entries of dir are considered, so the per-platform
// artifact trees are never touched.
//
// The whole set is validated before any rename happens: a subdirectory, a file
// that is not a release artifact, or two names that would collapse onto the
// same published name all fail without changing the directory.
func normalizeAssets(dir string) ([]assetRename, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s holds no release assets", dir)
	}
	targets := make(map[string]string, len(entries))
	var renames []assetRename
	for _, entry := range entries {
		name := entry.Name()
		entryPath := filepath.Join(dir, name)
		if entry.IsDir() {
			return nil, fmt.Errorf("%s is a directory; the flat release set holds only release assets", entryPath)
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file; the flat release set holds only release assets", entryPath)
		}
		if !release.IsArtifact(name) {
			return nil, fmt.Errorf("%s is not a release asset; the flat release set holds only the collected installers",
				entryPath)
		}
		normalized := release.GitHubAssetName(name)
		if previous, ok := targets[normalized]; ok {
			return nil, fmt.Errorf("%s: %q and %q would both normalize to %q",
				dir, previous, name, normalized)
		}
		targets[normalized] = name
		if normalized != name {
			renames = append(renames, assetRename{From: name, To: normalized})
		}
	}
	for _, rename := range renames {
		from := filepath.Join(dir, rename.From)
		to := filepath.Join(dir, rename.To)
		if err := os.Rename(from, to); err != nil {
			return nil, fmt.Errorf("rename %s to %s: %w", from, to, err)
		}
	}
	return renames, nil
}
