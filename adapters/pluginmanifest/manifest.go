// Package pluginmanifest decodes plugin.toml into the pure domain model. The
// TOML decoder is an adapter because the application layer stays free of
// third-party dependencies.
package pluginmanifest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// ManifestFile is the fixed name of the manifest at a package or development
// directory root.
const ManifestFile = "plugin.toml"

// Source loads plugin.toml from a directory.
type Source struct{}

// Load decodes the manifest but does not validate it; the container applies
// domain validation so the failure is attached to a FAILED fiber.
func (Source) Load(directory string) (domainplugin.Manifest, error) {
	file, err := os.Open(filepath.Join(directory, ManifestFile))
	if err != nil {
		return domainplugin.Manifest{}, err
	}
	defer file.Close()
	return Decode(file)
}

// Decode reads one TOML manifest. Unknown keys are ignored, as the package
// format requires for forward compatibility; unknown top-level keys will be
// reported as warnings by the installer ticket.
func Decode(r io.Reader) (domainplugin.Manifest, error) {
	var manifest domainplugin.Manifest
	if _, err := toml.NewDecoder(r).Decode(&manifest); err != nil {
		return domainplugin.Manifest{}, fmt.Errorf("plugin manifest: %w", err)
	}
	return manifest, nil
}
