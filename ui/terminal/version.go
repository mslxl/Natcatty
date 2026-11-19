package terminal

import "github.com/mslxl/natcatty/ui/terminal/internal/vt"

// LibraryVersion returns the version the loaded libghostty-vt reports. The
// phone Load calls it after resolving the statically linked symbols; a view
// test may call it after the desktop loader ran.
func LibraryVersion() (string, error) { return vt.LibraryVersion() }
