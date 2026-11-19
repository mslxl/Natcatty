package processcwd

import "strings"

// parseLsofOutput reads the cwd name from `lsof -F n` records. lsof appends
// annotations such as "(readlink: Permission denied)" to names it could not
// resolve; those are not directories a shell can enter.
func parseLsofOutput(output string) string {
	name := ""
	isDir := true
	sawType := false
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "f"):
			isDir = true
			sawType = false
		case strings.HasPrefix(line, "t"):
			sawType = true
			isDir = line == "tDIR" || line == "tVDIR"
		case strings.HasPrefix(line, "n"):
			if !sawType || isDir {
				name = line[1:]
			}
		}
	}
	// lsof speaks POSIX paths even when this parser is compiled on Windows
	// for a unit test, so filepath.IsAbs is the wrong shape here.
	if !strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) || annotatedLsofName(name) {
		return ""
	}
	return name
}

// annotatedLsofName reports lsof's parenthesized note on an unresolved name.
func annotatedLsofName(name string) bool {
	open := strings.LastIndexByte(name, '(')
	return open > 0 && strings.HasSuffix(name, ")") &&
		strings.ContainsRune(name[open:], ':')
}
