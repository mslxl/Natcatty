package terminal

import (
	"fmt"

	"github.com/mslxl/natcatty/ui/terminal/mobile"
)

// loadLinked is what Load does on a phone once the statically linked symbols
// are resolved: the library must report the ABI version the bindings mirror,
// or the load fails before any cell is read. It lives outside the
// ios||android file so a host test can prove a mismatched version fails the
// path phone Load takes; the resolver keeps it free of the native library.
func loadLinked(reported func() (string, error)) error {
	got, err := reported()
	if err != nil {
		return err
	}
	if got != mobile.Version {
		return fmt.Errorf("terminal: linked libghostty-vt reports %q, want %q", got, mobile.Version)
	}
	return nil
}
