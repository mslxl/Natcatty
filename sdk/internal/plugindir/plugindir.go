// Package plugindir resolves manifest-relative paths without escaping the
// plugin directory. The CLI, fake host, and conformance checker share one
// implementation so their safety rules cannot drift.
package plugindir

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Resolve joins relative onto directory. It rejects empty and absolute paths,
// NUL bytes, and any path that escapes directory, including through "..".
func Resolve(directory, relative string) (string, error) {
	if relative == "" || path.IsAbs(relative) || filepath.IsAbs(relative) ||
		strings.ContainsRune(relative, 0) {
		return "", fmt.Errorf("invalid plugin-relative path %q", relative)
	}
	clean := path.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("plugin-relative path %q escapes the plugin directory", relative)
	}
	full := filepath.Join(directory, filepath.FromSlash(clean))
	rel, err := filepath.Rel(directory, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin-relative path %q escapes the plugin directory", relative)
	}
	return full, nil
}
