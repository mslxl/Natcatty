package shell

import "github.com/mslxl/natcatty/ui/titlebar"

// The shell's layout tests measure the room the drawn window controls take,
// as on Linux and Windows, whatever platform runs them.
func init() { titlebar.NativeControls = false }
