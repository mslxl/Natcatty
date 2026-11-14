//go:build !windows

package surface

// readInstalledFonts is empty elsewhere: the generic families are used.
func readInstalledFonts() []string { return nil }
