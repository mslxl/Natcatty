//go:build ios || android

package terminal

import (
	"errors"
	"sync"
)

// errUnsupported is returned on phones until the static library backend
// lands: the shared core carries no loader of its own.
var errUnsupported = errors.New("terminal: libghostty-vt is not linked on this platform yet")

var load struct {
	once sync.Once
	err  error
}

// Load reports that the statically linked backend is not available yet.
func Load() error {
	load.once.Do(func() { load.err = errUnsupported })
	return load.err
}

// LibraryPath returns "" on phones: a statically linked library has no
// file to name.
func LibraryPath() (string, error) { return "", nil }
