package terminal

import (
	"strings"
	"unicode"
)

// archiveSuffixes maps a printed archive name's suffix to the tar switch that
// extracts it. The order matters: ".tar.gz" must be tested before ".gz" would
// be, and it is also the order the popup and docs present them in.
var archiveSuffixes = []struct {
	suffix string
	flag   string
}{
	{".tar.gz", "-xzf"},
	{".tar.bz2", "-xjf"},
	{".tar.xz", "-xJf"},
	{".tbz2", "-xjf"},
	{".tgz", "-xzf"},
	{".tar", "-xf"},
}

const zipSuffix = ".zip"

// ArchiveExtractCommand returns the fill-only command that extracts the
// printed archive name, or false when filePath is not a safe archive name.
//
// The command is meant to be inserted at the prompt, never run: it has no
// trailing newline and only refers to the name as printed. A relative name is
// left relative because the terminal's output does not record the directory
// that was current when the line was printed; resolving it against the current
// directory could retarget an older line after a cd.
func ArchiveExtractCommand(filePath string) (string, bool) {
	suffix, ok := archiveSuffix(filePath)
	if !ok || unsafeArchivePath(filePath, suffix) {
		return "", false
	}
	if suffix == zipSuffix {
		return "unzip " + shellQuote(filePath), true
	}
	for _, candidate := range archiveSuffixes {
		if candidate.suffix == suffix {
			return "tar " + candidate.flag + " " + shellQuote(filePath), true
		}
	}
	return "", false
}

// archiveSuffix reports the lower-case archive suffix of name. A bare ".tar"
// is a suffix with no file name and is not an archive.
func archiveSuffix(name string) (string, bool) {
	lowered := strings.ToLower(name)
	for _, candidate := range archiveSuffixes {
		if len(lowered) > len(candidate.suffix) && strings.HasSuffix(lowered, candidate.suffix) {
			return candidate.suffix, true
		}
	}
	if len(lowered) > len(zipSuffix) && strings.HasSuffix(lowered, zipSuffix) {
		return zipSuffix, true
	}
	return "", false
}

// unsafeArchivePath refuses names that would not survive a shell word or that
// could turn into an option or another host's path. The link layer only sees
// text printed by a program, so these are cheap, conservative checks rather
// than a claim about the remote file system.
func unsafeArchivePath(filePath, suffix string) bool {
	if filePath == "" || strings.HasPrefix(filePath, "~") || strings.HasPrefix(filePath, "-") {
		return true
	}
	if strings.ContainsAny(filePath, "\\!\t\r\n") {
		return true
	}
	if strings.Contains(filePath, ":") {
		// A colon in the first segment is a host or URL scheme, not a path.
		head := filePath
		if slash := strings.IndexByte(head, '/'); slash >= 0 {
			head = head[:slash]
		}
		if strings.Contains(head, ":") {
			return true
		}
	}
	if suffix == zipSuffix && strings.ContainsAny(filePath, "*?[") {
		return true
	}
	for _, r := range filePath {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// shellQuote makes value one POSIX shell word. The printed path never carries
// a newline; quoting still keeps a name with a quote or a space from becoming
// shell syntax.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
