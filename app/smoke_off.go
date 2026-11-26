//go:build !natcatty_smoke && (ios || android)

package app

import "github.com/mslxl/natcatty/ui/phoneshell"

// smokeStart is empty in a normal phone build.
func smokeStart(*phoneshell.Shell) {}
