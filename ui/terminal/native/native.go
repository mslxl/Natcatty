//go:build !ios && !android

// Package native is the desktop build of the terminal: the plugin
// manifest and the run-time loader for libghostty-vt. Its build tag keeps
// both out of phone builds, where MyGo cannot use a plugin manifest.
package native

import (
	_ "embed"

	"github.com/mslxl/natcatty/ui/terminal/native/internal/library"
)

//go:embed mygo-plugin.json
var manifest []byte

// LibraryPath returns the libghostty-vt found for this platform, as
// internal/library finds it.
func LibraryPath() (string, error) { return library.Find(manifest) }
