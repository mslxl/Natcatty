package transfer

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// ArchiveFormat is the archive container a Create Archive request produces.
type ArchiveFormat string

const (
	// ArchiveTarGz is a gzip-compressed tarball.
	ArchiveTarGz ArchiveFormat = "tar.gz"
	// ArchiveZip is a zip archive.
	ArchiveZip ArchiveFormat = "zip"
)

// Valid reports whether the format is one the command builder supports.
func (f ArchiveFormat) Valid() bool {
	return f == ArchiveTarGz || f == ArchiveZip
}

// Extension returns the archive's file extension, including the dot.
func (f ArchiveFormat) Extension() string { return "." + string(f) }

// Tool returns the remote program the format needs.
func (f ArchiveFormat) Tool() string {
	if f == ArchiveZip {
		return "zip"
	}
	return "tar"
}

// Archive request validation errors. They all mean the request never reached
// the remote host.
var (
	// ErrArchiveDirectory reports a directory that is not an absolute POSIX
	// path.
	ErrArchiveDirectory = errors.New("sftp: the archive directory must be an absolute POSIX path")
	// ErrArchiveName reports a name that is not one file name with the
	// selected format's extension.
	ErrArchiveName = errors.New("sftp: the archive name must be a file name with the selected extension")
	// ErrArchiveFormat reports an unknown archive format.
	ErrArchiveFormat = errors.New("sftp: unsupported archive format")
	// ErrArchiveSources reports an empty, too large, or out-of-directory
	// selection.
	ErrArchiveSources = errors.New("sftp: archive sources must be files in the archive directory")
)

// Archive failures the remote command reports. They are sentinels so callers
// can match the reason without parsing display text.
var (
	// ErrArchiveExists reports a target the request refused to overwrite.
	ErrArchiveExists = errors.New("an archive with this name already exists")
	// ErrArchiveToolMissing reports a remote host without the requested
	// archiver.
	ErrArchiveToolMissing = errors.New("the archive tool is not installed on the remote host")
	// ErrArchiveUnconfirmed reports a remote command that exited successfully
	// without the confirmation marker, so the archive cannot be trusted.
	ErrArchiveUnconfirmed = errors.New("the remote host did not confirm that the archive was created")
)

// Result markers the remote command prints as its last line. A successful
// command is confirmed by data, not only by its exit status: a truncated
// stream must never look like a finished archive.
const (
	ArchiveResultOK     = "NETCATTY_ARCHIVE_RESULT:OK"
	ArchiveResultExists = "NETCATTY_ARCHIVE_RESULT:EEXIST"
)

// ArchiveRequest asks a filesystem to build one archive beside its sources.
// Directory and Paths are absolute POSIX paths on the filesystem; Name is one
// path component inside Directory.
type ArchiveRequest struct {
	Directory string
	Name      string
	Format    ArchiveFormat
	Paths     []string
	// Overwrite replaces an existing regular file. The browser never sets it:
	// a collision is surfaced so the user can choose another name.
	Overwrite bool
}

// ArchivePlan is a validated request with its sources normalized relative to
// Directory and folded so a selected folder does not also list descendants.
type ArchivePlan struct {
	Directory string
	Name      string
	Format    ArchiveFormat
	Paths     []string
	Overwrite bool
}

// ArchiveCreator is an optional FileSystem capability. A filesystem that can
// run commands on the host implements it. The local pane does not, and the
// Create Archive action is then unavailable.
type ArchiveCreator interface {
	CreateArchive(ctx context.Context, request ArchiveRequest) error
}

// ArchiveCapability lets a filesystem report whether its transport can run an
// archive command right now. It exists because the type assertion alone is not
// enough: a remote pane may implement ArchiveCreator in every mode while its
// executor is absent (a single-channel Host), where CreateArchive can only
// return KindUnsupported. A filesystem that does not implement this interface
// keeps the historical meaning of implementing ArchiveCreator.
type ArchiveCapability interface {
	CanArchive() bool
}

// CanCreateArchive reports whether fs can run a Create Archive operation. The
// context menu and the prompt's submit path both gate on it, so a pane whose
// transport cannot archive never offers an operation that must fail.
func CanCreateArchive(fs FileSystem) bool {
	if _, ok := fs.(ArchiveCreator); !ok {
		return false
	}
	if capable, ok := fs.(ArchiveCapability); ok {
		return capable.CanArchive()
	}
	return true
}

// PlanArchive validates a request and turns its sources into relative paths.
// It refuses anything that would write outside Directory, overwrite a source,
// or run with an ambiguous name.
func PlanArchive(request ArchiveRequest) (ArchivePlan, error) {
	if request.Directory == "" || strings.ContainsRune(request.Directory, '\x00') ||
		strings.ContainsAny(request.Directory, "\\\r\n") ||
		!path.IsAbs(request.Directory) {
		return ArchivePlan{}, ErrArchiveDirectory
	}
	directory := path.Clean(request.Directory)
	if directory == "." {
		return ArchivePlan{}, ErrArchiveDirectory
	}
	if !request.Format.Valid() {
		return ArchivePlan{}, ErrArchiveFormat
	}
	name := request.Name
	if strings.ContainsRune(name, '\x00') || strings.ContainsAny(name, "\r\n") {
		return ArchivePlan{}, ErrArchiveName
	}
	if err := domainsftp.ValidateName(name); err != nil {
		return ArchivePlan{}, ErrArchiveName
	}
	extension := request.Format.Extension()
	if len(name) <= len(extension) || !strings.HasSuffix(strings.ToLower(name), extension) {
		return ArchivePlan{}, ErrArchiveName
	}
	if len(request.Paths) == 0 || len(request.Paths) > 1000 {
		return ArchivePlan{}, ErrArchiveSources
	}

	candidates := make([]string, 0, len(request.Paths))
	seen := make(map[string]bool, len(request.Paths))
	prefix := directory
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	for _, source := range request.Paths {
		if source == "" || strings.ContainsRune(source, '\x00') ||
			strings.ContainsAny(source, "\\\r\n") || !path.IsAbs(source) {
			return ArchivePlan{}, ErrArchiveSources
		}
		cleaned := path.Clean(source)
		if cleaned == directory || !strings.HasPrefix(cleaned, prefix) {
			return ArchivePlan{}, ErrArchiveSources
		}
		relative := strings.TrimPrefix(cleaned, prefix)
		if relative == "" || relative == "." ||
			relative == name || strings.HasPrefix(relative, name+"/") ||
			relative == ".." || strings.HasPrefix(relative, "../") {
			return ArchivePlan{}, ErrArchiveSources
		}
		if seen[relative] {
			continue
		}
		seen[relative] = true
		candidates = append(candidates, relative)
	}
	if len(candidates) == 0 {
		return ArchivePlan{}, ErrArchiveSources
	}
	// A selected folder already includes everything below it. Sorting by
	// depth first makes the parent win regardless of selection order.
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := strings.Count(candidates[i], "/"), strings.Count(candidates[j], "/")
		if left != right {
			return left < right
		}
		return candidates[i] < candidates[j]
	})
	roots := candidates[:0]
	for _, candidate := range candidates {
		if hasArchiveParent(roots, candidate) {
			continue
		}
		roots = append(roots, candidate)
	}
	return ArchivePlan{
		Directory: directory,
		Name:      name,
		Format:    request.Format,
		Paths:     append([]string(nil), roots...),
		Overwrite: request.Overwrite,
	}, nil
}

// hasArchiveParent reports whether one of roots already names candidate or one
// of its ancestors.
func hasArchiveParent(roots []string, candidate string) bool {
	for _, root := range roots {
		if candidate == root || strings.HasPrefix(candidate, root+"/") {
			return true
		}
	}
	return false
}

// DefaultArchiveName derives the name the dialog offers: the single selected
// entry's name, or the shared parent's name for a multi-selection. A name that
// already ends in an archive extension gains "-archive" so the new file never
// looks like the source it replaces.
func DefaultArchiveName(directory string, sources []string, format ArchiveFormat) string {
	if !format.Valid() {
		format = ArchiveTarGz
	}
	base := ""
	if len(sources) == 1 {
		base = path.Base(path.Clean(sources[0]))
	} else if len(sources) > 1 {
		common := path.Dir(path.Clean(sources[0]))
		for _, source := range sources[1:] {
			other := path.Clean(source)
			for common != "/" && other != common && !strings.HasPrefix(other, common+"/") {
				common = path.Dir(common)
			}
		}
		base = path.Base(common)
	}
	if base == "" || base == "." || base == "/" {
		base = "archive"
	}
	if hasArchiveExtension(base) {
		base = trimArchiveExtension(base) + "-archive"
	}
	return base + format.Extension()
}

// WithArchiveExtension rewrites name to the selected format's extension. It
// leaves a name with no archive extension alone except for appending the new
// one, which is what a format toggle in the prompt needs.
func WithArchiveExtension(name string, format ArchiveFormat) string {
	if !format.Valid() {
		return name
	}
	if hasArchiveExtension(name) {
		name = trimArchiveExtension(name)
	}
	return name + format.Extension()
}

// hasArchiveExtension reports the extensions the prompt treats as an archive
// suffix. tgz is recognized only to be replaced, never produced.
func hasArchiveExtension(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tar.gz") ||
		strings.HasSuffix(lower, ".tgz") ||
		strings.HasSuffix(lower, ".zip")
}

// trimArchiveExtension removes one recognized archive suffix, preserving the
// original case of the stem.
func trimArchiveExtension(name string) string {
	lower := strings.ToLower(name)
	for _, extension := range []string{".tar.gz", ".tgz", ".zip"} {
		if strings.HasSuffix(lower, extension) {
			return name[:len(name)-len(extension)]
		}
	}
	return name
}

// BuildArchiveCommand renders a POSIX shell command that creates the archive
// on the remote host. The archive is staged in a private sibling directory and
// published only after the archiver succeeds, so a failure never leaves a
// partial file under the final name. Sources are quoted with the same
// single-quote rule as the compress path.
func BuildArchiveCommand(request ArchiveRequest) (string, ArchivePlan, error) {
	plan, err := PlanArchive(request)
	if err != nil {
		return "", ArchivePlan{}, err
	}
	quote := func(value string) (string, error) {
		if strings.ContainsRune(value, '\x00') {
			return "", ErrArchiveSources
		}
		return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'", nil
	}
	quotedDirectory, err := quote(plan.Directory)
	if err != nil {
		return "", ArchivePlan{}, err
	}
	quotedName, err := quote(plan.Name)
	if err != nil {
		return "", ArchivePlan{}, err
	}
	sourceArgs := make([]string, 0, len(plan.Paths))
	quotedSources := make([]string, 0, len(plan.Paths))
	parents := make([]string, 0, len(plan.Paths))
	seenParents := map[string]bool{}
	for _, source := range plan.Paths {
		quoted, err := quote("./" + source)
		if err != nil {
			return "", ArchivePlan{}, err
		}
		sourceArgs = append(sourceArgs, quoted)
		quotedSources = append(quotedSources, quoted)
		parts := strings.Split(source, "/")
		for index := 1; index < len(parts); index++ {
			parent := "./" + strings.Join(parts[:index], "/")
			if !seenParents[parent] {
				seenParents[parent] = true
				quotedParent, err := quote(parent)
				if err != nil {
					return "", ArchivePlan{}, err
				}
				parents = append(parents, quotedParent)
			}
		}
	}

	lines := []string{
		"set -e",
		"umask 077",
		"cd " + quotedDirectory,
		"name=" + quotedName,
		`out="./$name"`,
		"check_target() {",
		`  if [ -L "$out" ] || { [ -e "$out" ] && [ ! -f "$out" ]; }; then`,
		`    echo 'Archive target must be a regular file, not a directory or symbolic link' >&2`,
		"    exit 1",
		"  fi",
	}
	for _, source := range quotedSources {
		lines = append(lines,
			`  if [ -e "$out" ] && [ "$out" -ef `+source+` ]; then echo 'Archive output refers to a selected source' >&2; exit 1; fi`)
	}
	if !plan.Overwrite {
		lines = append(lines,
			`  if [ -e "$out" ]; then`,
			`    printf '%s\n' '`+ArchiveResultExists+`'`,
			"    exit 0",
			"  fi")
	}
	lines = append(lines, "}")
	lines = append(lines, "check_target")
	for _, parent := range parents {
		lines = append(lines, `if [ -L `+parent+` ]; then echo 'Archive source traverses a symbolic link' >&2; exit 1; fi`)
	}
	for _, source := range quotedSources {
		lines = append(lines, `if [ ! -e `+source+` ] && [ ! -L `+source+` ]; then echo 'Archive source no longer exists' >&2; exit 1; fi`)
	}
	tool := plan.Format.Tool()
	lines = append(lines,
		`command -v `+tool+` >/dev/null 2>&1 || { echo '`+tool+` is not installed on the remote host' >&2; exit 127; }`,
		`stage=$(mktemp -d "./.netcatty-archive.XXXXXXXXXX")`,
		`trap 'rm -rf -- "$stage"' EXIT`,
		`trap 'exit 1' HUP INT TERM`,
	)
	if plan.Format == ArchiveZip {
		lines = append(lines,
			`zip -q -r -y -nw -MM "$stage/$name" -- `+strings.Join(sourceArgs, " ")+` >/dev/null`)
	} else {
		lines = append(lines,
			`tar -czf "$stage/$name" -- `+strings.Join(sourceArgs, " ")+` >/dev/null`)
	}
	lines = append(lines, "check_target")
	if plan.Overwrite {
		lines = append(lines, `mv -f -- "$stage/$name" .`)
	} else {
		lines = append(lines,
			`if ! ln -- "$stage/$name" .; then`,
			`  if [ -f "$out" ] && [ ! -L "$out" ]; then`,
			`    printf '%s\n' '`+ArchiveResultExists+`'`,
			"    exit 0",
			"  fi",
			`  echo 'Could not publish archive without replacing an existing target' >&2`,
			"  exit 1",
			"fi")
	}
	lines = append(lines, `printf '%s\n' '`+ArchiveResultOK+`'`)
	command := strings.Join(lines, "\n")
	if len(command) > 256*1024 {
		return "", ArchivePlan{}, fmt.Errorf("sftp: archive selection is too large")
	}
	return command, plan, nil
}

// ParseArchiveResult reads the confirmation marker from a finished remote
// command. Anything but the two known markers means the archive cannot be
// trusted, including an empty stdout from a shell that exited zero.
func ParseArchiveResult(stdout string) error {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	marker := strings.TrimSpace(lines[len(lines)-1])
	switch marker {
	case ArchiveResultOK:
		return nil
	case ArchiveResultExists:
		return ErrArchiveExists
	default:
		return ErrArchiveUnconfirmed
	}
}
