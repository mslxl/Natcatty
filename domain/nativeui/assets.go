package nativeui

import "strings"

// ValidAssetID reports whether id names a packaged asset rather than a
// remote URL or a path that escapes the package. The empty id is allowed and
// means the node has no asset.
//
// An asset id is a forward-slash relative path of non-empty, non-dot segments.
// Absolute paths, drive letters, URL schemes, query strings, fragments,
// backslashes, control characters, and parent traversal are rejected. The host
// applies the same rule at the render layer and resolves the id inside the
// plugin's read-only package directory.
func ValidAssetID(id string) bool {
	if id == "" {
		return true
	}
	if len(id) > MaxAssetIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] == 0x7f {
			return false
		}
		switch id[i] {
		case '\\', ':', '?', '#':
			// Backslash is a Windows separator. A colon carries a URL scheme
			// or drive letter; a query or fragment has no meaning in a file.
			return false
		}
	}
	if strings.HasPrefix(id, "/") {
		return false
	}
	for _, segment := range strings.Split(id, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
