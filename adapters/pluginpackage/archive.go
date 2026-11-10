// Package pluginpackage validates local .natplug archives: deterministic ZIP
// shape, path safety, the TOML manifest, permission vocabulary, catalogs, the
// module hash, and the structural WebAssembly rules. Validation never runs
// guest code.
package pluginpackage

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/mslxl/natcatty/adapters/pluginmanifest"
	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// Archive limits. Exceeding any of them rejects the package rather than
// truncating it.
const (
	// MaxArchiveBytes bounds the .natplug file itself.
	MaxArchiveBytes = 64 << 20
	// MaxEntries bounds the central directory entry count.
	MaxEntries = 4096
	// MaxFileBytes bounds one uncompressed entry.
	MaxFileBytes = 32 << 20
	// MaxTotalBytes bounds the uncompressed tree.
	MaxTotalBytes = 256 << 20
	// MaxCompressionRatio rejects zip bombs: an entry may not expand more
	// than this multiple of its compressed size once it is larger than 1 MiB.
	MaxCompressionRatio = 1000
)

var (
	windowsDrive = regexp.MustCompile(`^[A-Za-z]:`)

	// unicodeFold builds the same case-insensitive key for every spelling of a
	// name that Unicode full case folding treats as equivalent.
	unicodeFold = cases.Fold()

	// windowsReservedNames are device names Windows refuses to use as file
	// names. They stay reserved when an extension follows (NUL.txt).
	windowsReservedNames = map[string]struct{}{
		"con": {}, "prn": {}, "aux": {}, "nul": {},
		"com1": {}, "com2": {}, "com3": {}, "com4": {}, "com5": {},
		"com6": {}, "com7": {}, "com8": {}, "com9": {},
		"lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {}, "lpt5": {},
		"lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
	}
)

// Validator implements application/plugin.PackageValidator.
type Validator struct {
	module appplugin.ModuleValidator
}

var _ appplugin.PackageValidator = (*Validator)(nil)

// New returns an archive validator. module performs the structural WebAssembly
// check; a nil module skips only that check and is intended for catalog-only
// tests.
func New(module appplugin.ModuleValidator) *Validator {
	return &Validator{module: module}
}

// InvalidPackageError names one validation failure with the package-relative
// path that caused it.
type InvalidPackageError struct {
	Path    string
	Problem string
}

func (e *InvalidPackageError) Error() string {
	if e.Path == "" {
		return "plugin package: " + e.Problem
	}
	return "plugin package: " + e.Path + ": " + e.Problem
}

func invalidPackage(path, format string, args ...any) error {
	return &InvalidPackageError{Path: path, Problem: fmt.Sprintf(format, args...)}
}

// Validate reads and validates one archive. It returns every file the
// installer must write.
func (v *Validator) Validate(ctx context.Context, archivePath string) (appplugin.Package, error) {
	info, err := os.Stat(archivePath)
	if err != nil {
		return appplugin.Package{}, fmt.Errorf("plugin package: %w", err)
	}
	if info.IsDir() {
		return appplugin.Package{}, invalidPackage("", "%q is a directory; a .natplug archive is required", archivePath)
	}
	if info.Size() > MaxArchiveBytes {
		return appplugin.Package{}, invalidPackage("",
			"archive is %d bytes, above the %d byte limit", info.Size(), MaxArchiveBytes)
	}
	archiveHash, err := fileSHA256(archivePath)
	if err != nil {
		return appplugin.Package{}, err
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return appplugin.Package{}, invalidPackage("", "open ZIP: %v", err)
	}
	defer reader.Close()
	if len(reader.File) == 0 {
		return appplugin.Package{}, invalidPackage("", "archive is empty")
	}
	if len(reader.File) > MaxEntries {
		return appplugin.Package{}, invalidPackage("",
			"archive has %d entries, above the %d entry limit", len(reader.File), MaxEntries)
	}

	files := make(map[string][]byte, len(reader.File))
	seenNames := make(map[string]string, len(reader.File))
	var updated timeValue
	var total int64
	for i, entry := range reader.File {
		name := entry.Name
		if err := checkPathSafety(name); err != nil {
			return appplugin.Package{}, err
		}
		if i > 0 && name <= reader.File[i-1].Name {
			return appplugin.Package{}, invalidPackage(name,
				"entries must be sorted and unique for a deterministic archive")
		}
		if err := checkEntryCollision(name, seenNames); err != nil {
			return appplugin.Package{}, err
		}
		if entry.Flags&0x1 != 0 {
			return appplugin.Package{}, invalidPackage(name, "encrypted entries are not supported")
		}
		if !updated.accept(entry.Modified) {
			return appplugin.Package{}, invalidPackage(name,
				"all entries must share one fixed timestamp for a deterministic archive")
		}
		mode := entry.Mode()
		switch {
		case mode.IsDir():
			// Directory entries are metadata; the installer creates the
			// directories it needs from the file paths.
			continue
		case mode&os.ModeSymlink != 0:
			return appplugin.Package{}, invalidPackage(name, "symbolic links are not allowed")
		case !mode.IsRegular():
			return appplugin.Package{}, invalidPackage(name, "only regular files and directories are allowed")
		case mode.Perm()&0o111 != 0:
			return appplugin.Package{}, invalidPackage(name, "executable permission bits are not allowed")
		}
		if entry.UncompressedSize64 > MaxFileBytes {
			return appplugin.Package{}, invalidPackage(name,
				"file is %d bytes, above the %d byte limit", entry.UncompressedSize64, MaxFileBytes)
		}
		if entry.UncompressedSize64 > 1<<20 && entry.CompressedSize64 > 0 &&
			entry.UncompressedSize64/entry.CompressedSize64 > MaxCompressionRatio {
			return appplugin.Package{}, invalidPackage(name, "compression ratio exceeds the zip-bomb limit")
		}
		data, err := readEntry(entry)
		if err != nil {
			return appplugin.Package{}, err
		}
		total += int64(len(data))
		if total > MaxTotalBytes {
			return appplugin.Package{}, invalidPackage(name,
				"uncompressed total exceeds the %d byte limit", MaxTotalBytes)
		}
		files[name] = data
	}

	manifestBytes, ok := files[pluginmanifest.ManifestFile]
	if !ok {
		return appplugin.Package{}, invalidPackage("", "plugin.toml is missing at the archive root")
	}
	manifest, err := pluginmanifest.Decode(bytes.NewReader(manifestBytes))
	if err != nil {
		return appplugin.Package{}, invalidPackage(pluginmanifest.ManifestFile, "%v", err)
	}
	if err := manifest.Validate(); err != nil {
		return appplugin.Package{}, invalidPackage(pluginmanifest.ManifestFile, "%v", err)
	}
	modulePath := path.Clean(manifest.Wasm.Path)
	module, ok := files[modulePath]
	if !ok {
		return appplugin.Package{}, invalidPackage(modulePath, "declared module is missing from the archive")
	}
	sum := sha256.Sum256(module)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, manifest.Wasm.SHA256) {
		return appplugin.Package{}, invalidPackage(modulePath,
			"sha256 is %s, want %s", got, strings.ToLower(manifest.Wasm.SHA256))
	}
	if v.module != nil {
		// The manifest's effective maximum applies the host default (64 MiB)
		// when wasm.memory_max_mib is omitted; a raw zero would reject an
		// otherwise valid package.
		if err := v.module.ValidateModule(ctx, module,
			manifest.Wasm.EffectiveMemoryMaxBytes()); err != nil {
			return appplugin.Package{}, invalidPackage(modulePath, "%v", err)
		}
	}
	if err := validateCatalogs(files, manifest); err != nil {
		return appplugin.Package{}, err
	}
	return appplugin.Package{
		Manifest: manifest,
		Files:    files,
		SHA256:   archiveHash,
	}, nil
}

// timeValue remembers the first entry timestamp and reports whether a later
// entry matches it.
type timeValue struct {
	set   bool
	value time.Time
}

func (t *timeValue) accept(next time.Time) bool {
	if !t.set {
		t.set = true
		t.value = next
		return true
	}
	return t.value.Equal(next)
}

func fileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("plugin package: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, MaxArchiveBytes+1)); err != nil {
		return "", fmt.Errorf("plugin package: hash archive: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readEntry(entry *zip.File) ([]byte, error) {
	reader, err := entry.Open()
	if err != nil {
		return nil, invalidPackage(entry.Name, "open entry: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, MaxFileBytes+1))
	if err != nil {
		return nil, invalidPackage(entry.Name, "read entry: %v", err)
	}
	if len(data) > MaxFileBytes {
		return nil, invalidPackage(entry.Name, "file exceeds the %d byte limit", MaxFileBytes)
	}
	return data, nil
}

func checkPathSafety(name string) error {
	if name == "" {
		return invalidPackage("", "archive contains an empty entry name")
	}
	if !utf8.ValidString(name) {
		return invalidPackage(name, "entry name is not valid UTF-8")
	}
	if strings.ContainsRune(name, 0) {
		return invalidPackage(name, "entry name contains a NUL byte")
	}
	if strings.Contains(name, "\\") {
		return invalidPackage(name, "entry name must use forward slashes")
	}
	trimmed := strings.TrimSuffix(name, "/")
	if path.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") || windowsDrive.MatchString(trimmed) {
		return invalidPackage(name, "absolute entry paths are not allowed")
	}
	clean := path.Clean(trimmed)
	if clean != trimmed || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return invalidPackage(name, "entry path escapes the package root or is not clean")
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if err := checkWindowsSegment(name, segment); err != nil {
			return err
		}
	}
	return nil
}

// checkWindowsSegment rejects names Windows cannot store faithfully. Windows
// strips trailing dots and spaces from every path component and treats the
// reserved device names as devices even when an extension follows.
func checkWindowsSegment(entry, segment string) error {
	if strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
		return invalidPackage(entry,
			"entry name segment %q ends with a dot or space, which Windows strips", segment)
	}
	base := segment
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	if _, reserved := windowsReservedNames[strings.ToLower(base)]; reserved {
		return invalidPackage(entry,
			"entry name segment %q is a reserved Windows device name", segment)
	}
	return nil
}

// checkEntryCollision rejects a name that another entry already claimed on a
// supported filesystem. APFS and NTFS compare names case-insensitively, and
// APFS also treats canonically equivalent Unicode spellings as the same name.
func checkEntryCollision(name string, seen map[string]string) error {
	key := filesystemNameKey(name)
	if previous, ok := seen[key]; ok {
		return invalidPackage(name,
			"entry name collides with %q on case-insensitive or normalization-insensitive filesystems",
			previous)
	}
	seen[key] = name
	return nil
}

// filesystemNameKey maps every spelling of a name that a supported filesystem
// treats as the same file to one key: canonical composition plus Unicode full
// case folding.
func filesystemNameKey(name string) string {
	return norm.NFC.String(unicodeFold.String(norm.NFC.String(name)))
}
