package transfer

import "strings"

// SameBacking reports whether two filesystems address the same tree. A nil
// filesystem, or one without a Backing, never matches: identity must be
// explicit rather than inferred from an adapter instance.
func SameBacking(a, b FileSystem) bool {
	if a == nil || b == nil {
		return false
	}
	backing := a.Backing()
	return backing != "" && backing == b.Backing()
}

// SameLocation reports whether two paths address one entry. Paths in
// different backings are never the same entry, even when their text matches.
func SameLocation(a FileSystem, aPath string, b FileSystem, bPath string) bool {
	if !SameBacking(a, b) {
		return false
	}
	return comparePath(a, aPath) == comparePath(b, bPath)
}

// PathWithin reports whether candidate is ancestorPath or lives below it.
// The comparison follows symlinks for local filesystems and cleans POSIX
// paths for remote ones, so an alias of the source tree cannot bypass the
// self-copy guard.
func PathWithin(ancestor FileSystem, ancestorPath string, candidate FileSystem, candidatePath string) bool {
	if !SameBacking(ancestor, candidate) {
		return false
	}
	parent := comparePath(ancestor, ancestorPath)
	child := comparePath(candidate, candidatePath)
	if parent == "" || child == "" {
		return false
	}
	if parent == child {
		return true
	}
	separator := pathSeparator(ancestor.Style())
	if !strings.HasSuffix(parent, separator) {
		parent += separator
	}
	return strings.HasPrefix(child, parent)
}

func comparePath(files FileSystem, path string) string {
	canonical := files.Canonical(path)
	if files.Style() == StyleWindows {
		return strings.ToLower(canonical)
	}
	return canonical
}

func pathSeparator(style PathStyle) string {
	if style == StyleWindows {
		return `\`
	}
	return "/"
}
