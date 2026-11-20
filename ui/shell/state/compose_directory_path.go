package state

import (
	"path/filepath"
	"strings"
)

// Path algebra for the compose directory picker. Remote paths may use POSIX
// separators while the app runs on Windows (and the reverse), so these helpers
// follow the shape of the path instead of the host's filepath rules.

// appendComposeDirectoryPath adds a path to the draft as one shell word. It
// inserts text only; the caller never sends the draft.
func appendComposeDirectoryPath(draft, path, shell string) string {
	word := quoteComposeDirectoryPath(path, shell)
	if draft == "" {
		return word
	}
	if last := draft[len(draft)-1]; last != ' ' && last != '\t' && last != '\n' {
		draft += " "
	}
	return draft + word
}

// quoteComposeDirectoryPath quotes a path for the Session's shell only when it
// contains a character the shell would interpret. Plain paths stay readable in
// the draft.
func quoteComposeDirectoryPath(path, shell string) string {
	if path == "" {
		return "''"
	}
	if !strings.ContainsAny(path, " \t\n'\"\\$&|;<>()*?[]{}!#`") {
		return path
	}
	switch shell {
	case "powershell":
		return "'" + strings.ReplaceAll(path, "'", "''") + "'"
	case "cmd":
		return `"` + strings.ReplaceAll(path, `"`, `""`) + `"`
	default:
		return singleQuotePOSIX(path)
	}
}

func singleQuotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// joinComposeDirectory appends one entry name to a directory path, choosing
// the separator the path already uses so a POSIX remote and a Windows local
// Session both work.
func joinComposeDirectory(parent, name string) string {
	if isAbsoluteComposeDirectory(name) || parent == "" {
		return name
	}
	separator := composeDirectorySeparator(parent)
	if strings.HasSuffix(parent, string(separator)) ||
		(separator == '/' && strings.HasSuffix(parent, `\`)) {
		return parent + name
	}
	return parent + string(separator) + name
}

// parentComposeDirectory returns the parent of a browsed directory, keeping a
// filesystem root as its own parent. A UNC share root is also its own parent:
// there is no meaningful directory above the share.
func parentComposeDirectory(path string) string {
	if path == "" {
		return ""
	}
	trimmed := strings.TrimRight(path, `/\`)
	if trimmed == "" {
		return path
	}
	if strings.HasPrefix(path, `\\`) && strings.Count(trimmed, `\`) <= 3 {
		return path
	}
	separator := composeDirectorySeparator(path)
	index := strings.LastIndexByte(trimmed, separator)
	if index < 0 {
		return path
	}
	parent := trimmed[:index]
	if parent == "" {
		return string(separator)
	}
	// A Windows drive root keeps its separator: C:\ rather than C:.
	if len(parent) == 2 && parent[1] == ':' {
		return parent + string(separator)
	}
	return parent
}

func composeDirectorySeparator(path string) byte {
	if strings.Contains(path, `\`) && !strings.Contains(path, "/") {
		return '\\'
	}
	if strings.Contains(path, "/") {
		return '/'
	}
	return filepath.Separator
}

func isAbsoluteComposeDirectory(path string) bool {
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\\`) {
		return true
	}
	return len(path) >= 3 && path[1] == ':' &&
		(path[2] == '\\' || path[2] == '/') &&
		((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z'))
}
