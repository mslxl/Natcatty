//go:build ios || android

package vt

import (
	"fmt"

	"github.com/mslxl/natcatty/ui/terminal/mobile"
)

// open reports a handle, which the static loader does not use: the symbols
// are already at fixed addresses in the app's image.
func open(string) (uintptr, error) { return 1, nil }

// sym resolves a symbol through the address table the C linker filled in.
func sym(_ uintptr, name string) (uintptr, error) {
	if a := mobile.Addr(name); a != 0 {
		return a, nil
	}
	return 0, fmt.Errorf("%s is not in the linked libghostty-vt", name)
}
