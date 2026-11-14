//go:build windows

package surface

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// readInstalledFonts lists the font families Windows has registered, both for the
// machine and for the user: a value name is "Berkeley Mono (TrueType)" or
// "Berkeley Mono Bold (TrueType)", so the family is the part before the style.
func readInstalledFonts() []string {
	const path = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`
	var out []string
	seen := map[string]bool{}
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, path, registry.READ)
		if err != nil {
			continue
		}
		names, err := k.ReadValueNames(-1)
		k.Close()
		if err != nil {
			continue
		}
		for _, n := range names {
			fam := familyFromValueName(n)
			if fam == "" || seen[strings.ToLower(fam)] {
				continue
			}
			seen[strings.ToLower(fam)] = true
			out = append(out, fam)
		}
	}
	return out
}

// familyFromValueName cuts the style and format from a registry value name:
// "Berkeley Mono Bold Italic (TrueType)" is the family "Berkeley Mono".
func familyFromValueName(n string) string {
	if i := strings.LastIndex(n, " ("); i > 0 {
		n = n[:i]
	}
	for _, style := range []string{" Bold Italic", " Bold Oblique", " Bold", " Italic", " Oblique", " Light", " Medium", " Semibold", " Black", " Thin"} {
		n = strings.TrimSuffix(n, style)
	}
	return strings.TrimSpace(n)
}
