//go:build !ios && !android

package terminal

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/mslxl/natcatty/ui/terminal/internal/vt"
	"github.com/mslxl/natcatty/ui/terminal/native"
)

var load struct {
	once sync.Once
	path string
	err  error
}

// Load loads libghostty-vt, once, from LibraryPath. New calls it; call it
// earlier to report a missing library before showing a terminal.
func Load() error {
	load.once.Do(func() {
		if !vt.Supported {
			load.err = fmt.Errorf("terminal: not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
			return
		}
		load.path, load.err = LibraryPath()
		if load.err == nil {
			load.err = vt.Load(load.path)
		}
	})
	return load.err
}

// LibraryPath returns the libghostty-vt the desktop loader finds: see
// native.LibraryPath.
func LibraryPath() (string, error) { return native.LibraryPath() }
