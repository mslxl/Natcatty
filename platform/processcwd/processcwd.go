// Package processcwd reads the working directory of a live process. It is the
// fallback for a local shell that never reports OSC 7: Linux reads
// /proc/<pid>/cwd, macOS asks lsof, and Windows reads the process's PEB.
//
// The lookup is best effort. A process the caller may not inspect, an exited
// shell, or a platform without a supported lookup reports an error and the
// caller keeps an unknown working directory.
package processcwd

import (
	"errors"
	"path/filepath"
	"strings"
)

// ErrInvalid reports a process id that cannot name a process.
var ErrInvalid = errors.New("processcwd: invalid process id")

// valid reports whether path can be a working directory. The check is
// deliberately platform-shaped: an absolute path with no embedded NUL is
// something a shell on this machine can chdir into.
func valid(path string) bool {
	return path != "" && filepath.IsAbs(path) && !strings.ContainsRune(path, 0)
}
