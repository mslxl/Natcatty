package pluginpackage_test

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/pluginpackage"
)

type moduleCheck struct {
	err      error
	calls    int
	lastMax  int64
	lastWASM []byte
}

func (m *moduleCheck) ValidateModule(_ context.Context, wasm []byte, memoryMaxBytes int64) error {
	m.calls++
	m.lastMax = memoryMaxBytes
	m.lastWASM = append([]byte(nil), wasm...)
	return m.err
}

var fixedTime = time.Date(2020, 1, 2, 3, 4, 6, 0, time.UTC)

type archiveEntry struct {
	name    string
	data    []byte
	mode    os.FileMode
	modTime time.Time
	store   bool
}

func baseFiles(t *testing.T) map[string][]byte {
	t.Helper()
	module := []byte("wasm-module")
	sum := sha256.Sum256(module)
	return map[string][]byte{
		"plugin.toml":       []byte(manifestText(hex.EncodeToString(sum[:]), `required = ["commands"]`, "")),
		"plugin.wasm":       module,
		"i18n/en.toml":      []byte("hello = \"Hello\"\n"),
		"assets/readme.txt": []byte("asset"),
	}
}

func manifestText(hash, permissions, extra string) string {
	return fmt.Sprintf(`manifest_version = 1
id = "acme.notes"
name = "Acme Notes"
version = "1.0.0"
plugin_api = "^0.1"
abi = 1
default_locale = "en"

[wasm]
path = "plugin.wasm"
sha256 = "%s"
memory_max_mib = 64

[permissions]
%s
optional = []
%s`, hash, permissions, extra)
}

func sortedEntries(files map[string][]byte) []archiveEntry {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]archiveEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, archiveEntry{name: name, data: files[name], mode: 0o644})
	}
	return entries
}

func entriesWithExtra(t *testing.T, name string) []archiveEntry {
	t.Helper()
	files := baseFiles(t)
	files[name] = []byte("extra")
	return sortedEntries(files)
}

func entriesWithMode(t *testing.T, name string, mode os.FileMode) []archiveEntry {
	t.Helper()
	files := baseFiles(t)
	entries := sortedEntries(files)
	for i := range entries {
		if entries[i].name == name {
			entries[i].mode = mode
		}
	}
	return entries
}

func writeArchive(t *testing.T, files map[string][]byte) string {
	t.Helper()
	return writeEntries(t, sortedEntries(files))
}

func writeEntries(t *testing.T, entries []archiveEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.natplug")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		method := uint16(zip.Deflate)
		if entry.store {
			method = zip.Store
		}
		header := &zip.FileHeader{Name: entry.name, Method: method}
		modTime := entry.modTime
		if modTime.IsZero() {
			modTime = fixedTime
		}
		header.SetModTime(modTime)
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateAcceptsAWellFormedPackage(t *testing.T) {
	check := &moduleCheck{}
	validator := pluginpackage.New(check)
	pkg, err := validator.Validate(context.Background(), writeArchive(t, baseFiles(t)))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if pkg.Manifest.ID != "acme.notes" || len(pkg.Files) != 4 {
		t.Fatalf("package = %+v", pkg.Manifest)
	}
	if check.calls != 1 || check.lastMax != 64<<20 {
		t.Fatalf("module validator calls = %d max = %d", check.calls, check.lastMax)
	}
	if pkg.SHA256 == "" {
		t.Fatal("package hash is empty")
	}
}

func TestValidateAcceptsDeclaredPermissionScopes(t *testing.T) {
	files := baseFiles(t)
	hash := moduleHash(files)
	files["plugin.toml"] = []byte(manifestText(hash,
		`required = ["commands", "network.dial"]`,
		"\n[permissions.scopes.\"network.dial\"]\nhost = \"example.com\"\nports = [443]\nprotocol = \"tls\"\ntls = \"verify\"\n"))
	pkg, err := pluginpackage.New(&moduleCheck{}).Validate(context.Background(), writeArchive(t, files))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	required, _, err := pkg.Manifest.Permissions.Requests()
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}
	if required[1].Scope.Kind != "network" || required[1].Scope.Host != "example.com" {
		t.Fatalf("network scope = %+v", required[1].Scope)
	}
}

func TestValidateRejectsUnsafeOrNonDeterministicArchives(t *testing.T) {
	tests := []struct {
		name    string
		entries func(*testing.T) []archiveEntry
	}{
		{"path traversal", func(t *testing.T) []archiveEntry { return entriesWithExtra(t, "../escape.txt") }},
		{"absolute path", func(t *testing.T) []archiveEntry { return entriesWithExtra(t, "/tmp/escape.txt") }},
		{"backslash", func(t *testing.T) []archiveEntry { return entriesWithExtra(t, `assets\readme.txt`) }},
		{"unsorted", func(t *testing.T) []archiveEntry {
			return []archiveEntry{
				{name: "plugin.toml", data: []byte("x"), mode: 0o644},
				{name: "assets/a.txt", data: []byte("x"), mode: 0o644},
			}
		}},
		{"duplicate", func(t *testing.T) []archiveEntry {
			return []archiveEntry{
				{name: "a.txt", data: []byte("x"), mode: 0o644},
				{name: "a.txt", data: []byte("y"), mode: 0o644},
			}
		}},
		{"timestamp mismatch", func(t *testing.T) []archiveEntry {
			return []archiveEntry{
				{name: "a.txt", data: []byte("x"), mode: 0o644, modTime: fixedTime},
				{name: "b.txt", data: []byte("x"), mode: 0o644, modTime: fixedTime.Add(time.Hour)},
			}
		}},
		{"symlink", func(t *testing.T) []archiveEntry {
			return entriesWithMode(t, "plugin.wasm", os.ModeSymlink|0o777)
		}},
		{"executable bit", func(t *testing.T) []archiveEntry {
			return entriesWithMode(t, "plugin.wasm", 0o755)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeEntries(t, test.entries(t))
			if _, err := pluginpackage.New(&moduleCheck{}).Validate(context.Background(), path); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
		})
	}
}

// TestValidateRejectsFilesystemEquivalentEntryNames covers the names that a
// supported filesystem would merge during extraction: case-folded duplicates,
// Unicode normalization variants, Windows trailing-dot/space stripping, and
// reserved Windows device names. Each rejection names the offending entries.
func TestValidateRejectsFilesystemEquivalentEntryNames(t *testing.T) {
	pair := func(first, second string) func(*testing.T) []archiveEntry {
		return func(t *testing.T) []archiveEntry {
			t.Helper()
			files := baseFiles(t)
			files[first] = []byte("first")
			files[second] = []byte("second")
			return sortedEntries(files)
		}
	}
	extra := func(name string) func(*testing.T) []archiveEntry {
		return func(t *testing.T) []archiveEntry {
			t.Helper()
			return entriesWithExtra(t, name)
		}
	}
	tests := []struct {
		name    string
		entries func(*testing.T) []archiveEntry
		mention []string
	}{
		{
			name:    "case-insensitive module names",
			entries: pair("Plugin.wasm", "plugin.wasm"),
			mention: []string{"Plugin.wasm", "plugin.wasm"},
		},
		{
			name:    "unicode case folding",
			entries: pair("assets/Ä.txt", "assets/ä.txt"),
			mention: []string{"Ä.txt", "ä.txt"},
		},
		{
			name:    "unicode normalization",
			entries: pair("assets/café.txt", "assets/café.txt"),
			mention: []string{"café.txt", "café.txt"},
		},
		{
			name:    "windows trailing dot",
			entries: extra("assets/readme.txt."),
			mention: []string{"readme.txt."},
		},
		{
			name:    "windows trailing space",
			entries: extra("assets/readme.txt "),
			mention: []string{"readme.txt "},
		},
		{
			name:    "windows reserved device name",
			entries: extra("CON"),
			mention: []string{`"CON"`},
		},
		{
			name:    "windows reserved device name with extension",
			entries: extra("NUL.txt"),
			mention: []string{`"NUL.txt"`},
		},
		{
			name:    "windows reserved device name in a subdirectory",
			entries: extra("assets/com1.log"),
			mention: []string{`"com1.log"`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := pluginpackage.New(&moduleCheck{}).
				Validate(context.Background(), writeEntries(t, test.entries(t)))
			if err == nil {
				t.Fatal("filesystem-equivalent archive names were accepted")
			}
			var invalid *pluginpackage.InvalidPackageError
			if !errors.As(err, &invalid) {
				t.Fatalf("Validate error = %T %v, want InvalidPackageError", err, err)
			}
			for _, want := range test.mention {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Validate error = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

func TestValidateRejectsBadManifestPermissionsAndCatalogs(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(*testing.T, map[string][]byte, string)
		module *moduleCheck
	}{
		{"missing manifest", func(t *testing.T, files map[string][]byte, _ string) {
			delete(files, "plugin.toml")
		}, &moduleCheck{}},
		{"missing module", func(t *testing.T, files map[string][]byte, _ string) {
			delete(files, "plugin.wasm")
		}, &moduleCheck{}},
		{"hash mismatch", func(t *testing.T, files map[string][]byte, _ string) {
			files["plugin.toml"] = []byte(manifestText(strings.Repeat("b", 64), `required = ["commands"]`, ""))
		}, &moduleCheck{}},
		{"unknown permission", func(t *testing.T, files map[string][]byte, hash string) {
			files["plugin.toml"] = []byte(manifestText(hash,
				`required = ["commands", "provider.authentication"]`, ""))
		}, &moduleCheck{}},
		{"bad catalog key", func(t *testing.T, files map[string][]byte, _ string) {
			files["i18n/en.toml"] = []byte("Bad Key = \"x\"\n")
		}, &moduleCheck{}},
		{"missing default catalog", func(t *testing.T, files map[string][]byte, _ string) {
			delete(files, "i18n/en.toml")
			files["i18n/de.toml"] = []byte("hello = \"Hallo\"\n")
		}, &moduleCheck{}},
		{"duplicate catalog key", func(t *testing.T, files map[string][]byte, _ string) {
			files["i18n/en.toml"] = []byte("hello = \"Hello\"\nhello = \"Again\"\n")
		}, &moduleCheck{}},
		{"non-string catalog value", func(t *testing.T, files map[string][]byte, _ string) {
			files["i18n/en.toml"] = []byte("hello = 1\n")
		}, &moduleCheck{}},
		{"bad locale filename", func(t *testing.T, files map[string][]byte, _ string) {
			files["i18n/not_a_tag.toml"] = []byte("hello = \"Hello\"\n")
		}, &moduleCheck{}},
		{"untranslated manifest key", func(t *testing.T, files map[string][]byte, hash string) {
			files["plugin.toml"] = []byte(manifestText(hash, `required = ["commands"]`, `
[[contributions.commands]]
id = "greet"
title_key = "command_greet"
`))
		}, &moduleCheck{}},
		{"module rejected", func(t *testing.T, files map[string][]byte, _ string) {
		}, &moduleCheck{err: fmt.Errorf("forbidden feature")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := baseFiles(t)
			test.edit(t, files, moduleHash(files))
			path := writeArchive(t, files)
			if _, err := pluginpackage.New(test.module).Validate(context.Background(), path); err == nil {
				t.Fatal("invalid package was accepted")
			}
		})
	}
}

func TestValidateRejectsADirectory(t *testing.T) {
	_, err := pluginpackage.New(&moduleCheck{}).Validate(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("a directory was accepted as a .natplug archive")
	}
}

// TestValidateEnforcesArchiveLimits covers every independent archive budget:
// entry count, one file, the uncompressed total, the zip-bomb ratio, and the
// archive file size itself.
func TestValidateEnforcesArchiveLimits(t *testing.T) {
	t.Run("entry count", func(t *testing.T) {
		entries := make([]archiveEntry, 0, pluginpackage.MaxEntries+1)
		for i := 0; i <= pluginpackage.MaxEntries; i++ {
			entries = append(entries, archiveEntry{
				name: fmt.Sprintf("f%04d.txt", i), data: []byte("x"), mode: 0o644,
			})
		}
		_, err := pluginpackage.New(&moduleCheck{}).
			Validate(context.Background(), writeEntries(t, entries))
		if err == nil || !strings.Contains(err.Error(), "entry limit") {
			t.Fatalf("Validate = %v, want an entry-count limit error", err)
		}
	})

	t.Run("per-file size", func(t *testing.T) {
		entries := []archiveEntry{{
			name: "big.bin", data: make([]byte, pluginpackage.MaxFileBytes+1), mode: 0o644,
		}}
		_, err := pluginpackage.New(&moduleCheck{}).
			Validate(context.Background(), writeEntries(t, entries))
		if err == nil || !strings.Contains(err.Error(), "byte limit") {
			t.Fatalf("Validate = %v, want a per-file size limit error", err)
		}
	})

	t.Run("compression ratio", func(t *testing.T) {
		// 16 MiB of zeros deflates to roughly 16 KiB, above the 1000x cap
		// while staying below the per-file and total limits.
		entries := []archiveEntry{{
			name: "bomb.bin", data: make([]byte, 16<<20), mode: 0o644,
		}}
		_, err := pluginpackage.New(&moduleCheck{}).
			Validate(context.Background(), writeEntries(t, entries))
		if err == nil || !strings.Contains(err.Error(), "compression ratio") {
			t.Fatalf("Validate = %v, want a compression-ratio limit error", err)
		}
	})

	t.Run("uncompressed total", func(t *testing.T) {
		// Nine 30 MiB entries exceed MaxTotalBytes without hitting the
		// per-file cap. The repeating 16 KiB block compresses well below the
		// zip-bomb ratio, and the archive itself stays below MaxArchiveBytes.
		block := make([]byte, 16<<10)
		random := rand.New(rand.NewSource(1))
		if _, err := random.Read(block); err != nil {
			t.Fatal(err)
		}
		perFile := 30 << 20
		data := make([]byte, perFile)
		for offset := 0; offset < perFile; offset += len(block) {
			copy(data[offset:], block)
		}
		entries := make([]archiveEntry, 0, 9)
		for i := 0; i < 9; i++ {
			entries = append(entries, archiveEntry{
				name: fmt.Sprintf("chunk-%02d.bin", i), data: data, mode: 0o644,
			})
		}
		_, err := pluginpackage.New(&moduleCheck{}).
			Validate(context.Background(), writeEntries(t, entries))
		if err == nil || !strings.Contains(err.Error(), "uncompressed total") {
			t.Fatalf("Validate = %v, want a total-size limit error", err)
		}
	})

	t.Run("archive size", func(t *testing.T) {
		// Stored (uncompressed) entries make the .natplug itself larger than
		// MaxArchiveBytes while each entry stays below MaxFileBytes.
		data := make([]byte, 25<<20)
		entries := make([]archiveEntry, 0, 3)
		for i := 0; i < 3; i++ {
			entries = append(entries, archiveEntry{
				name: fmt.Sprintf("stored-%d.bin", i), data: data, mode: 0o644,
				store: true,
			})
		}
		_, err := pluginpackage.New(&moduleCheck{}).
			Validate(context.Background(), writeEntries(t, entries))
		if err == nil || !strings.Contains(err.Error(), "above the") {
			t.Fatalf("Validate = %v, want an archive-size limit error", err)
		}
	})
}

func TestValidateCatalogErrorsKeepThePackagePath(t *testing.T) {
	files := baseFiles(t)
	files["i18n/en.toml"] = []byte("hello = \"Hello\"\nhello = \"Again\"\n")
	_, err := pluginpackage.New(&moduleCheck{}).
		Validate(context.Background(), writeArchive(t, files))
	var invalid *pluginpackage.InvalidPackageError
	if !errors.As(err, &invalid) {
		t.Fatalf("Validate error = %T %v, want InvalidPackageError", err, err)
	}
	if invalid.Path != "i18n/en.toml" || !strings.Contains(invalid.Problem, "already been defined") {
		t.Fatalf("invalid package = %+v", invalid)
	}
}

func TestValidateAcceptsManifestKeysTheDefaultCatalogDefines(t *testing.T) {
	files := baseFiles(t)
	files["plugin.toml"] = []byte(manifestText(moduleHash(files), `required = ["commands"]`, `
[[contributions.commands]]
id = "greet"
title_key = "command_greet"
`))
	files["i18n/en.toml"] = []byte(strings.Join([]string{
		`command_greet = "Say Hello"`,
		`"files.one" = "{count} file"`,
		`"files.other" = "{count} files"`,
		"[meta]",
		`locale = "en"`,
	}, "\n"))
	if _, err := pluginpackage.New(&moduleCheck{}).
		Validate(context.Background(), writeArchive(t, files)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func moduleHash(files map[string][]byte) string {
	sum := sha256.Sum256(files["plugin.wasm"])
	return hex.EncodeToString(sum[:])
}
