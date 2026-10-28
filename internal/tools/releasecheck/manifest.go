package main

import (
	"os"

	"github.com/mslxl/natcatty/internal/release"
)

// readManifest reads and validates mygo.json, the single version source.
func readManifest(path string) (release.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return release.Manifest{}, err
	}
	return release.ParseManifest(data)
}

// manifestVersion reads mygo.json and returns its version.
func manifestVersion(path string) (string, error) {
	manifest, err := readManifest(path)
	if err != nil {
		return "", err
	}
	return manifest.Version, nil
}

// checkManifest rejects the configuration that would produce signing,
// notarization, or update artifacts.
func checkManifest(path string) error {
	manifest, err := readManifest(path)
	if err != nil {
		return err
	}
	return manifest.CheckPackaging()
}
