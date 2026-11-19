//go:build ios || android

package terminal

import (
	"fmt"
	"sync"

	"github.com/mslxl/natcatty/ui/terminal/internal/vt"
	"github.com/mslxl/natcatty/ui/terminal/mobile"
)

var load struct {
	once sync.Once
	err  error
}

// Load resolves the symbols of the libghostty-vt linked into the app and
// checks that the library reports the version the bindings expect. It is
// called at startup, so a library built from another commit fails there
// instead of misreading a cell later.
func Load() error {
	load.once.Do(func() {
		if err := vt.Load(""); err != nil {
			load.err = err
			return
		}
		got, err := vt.LibraryVersion()
		if err != nil {
			load.err = err
			return
		}
		if got != mobile.Version {
			load.err = fmt.Errorf("terminal: linked libghostty-vt reports %q, want %q", got, mobile.Version)
		}
	})
	return load.err
}

// LibraryPath returns "" on phones: a statically linked library has no file
// to name.
func LibraryPath() (string, error) { return "", nil }
