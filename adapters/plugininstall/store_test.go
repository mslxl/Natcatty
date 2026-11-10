package plugininstall_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/plugininstall"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func testPackage() appplugin.Package {
	return appplugin.Package{
		Manifest: domainplugin.Manifest{
			ManifestVersion: 1,
			ID:              "acme.notes",
			Name:            "Acme Notes",
			Version:         "1.2.3",
			PluginAPI:       "^0.1",
			ABI:             1,
			DefaultLocale:   "en",
			Wasm: domainplugin.Wasm{
				Path:         "plugin.wasm",
				SHA256:       moduleHash([]byte("module")),
				MemoryMaxMiB: 64,
			},
			Permissions: domainplugin.Permissions{Required: []string{"commands"}},
		},
		Files: map[string][]byte{
			"plugin.toml":  []byte("manifest"),
			"plugin.wasm":  []byte("module"),
			"assets/a.txt": []byte("asset"),
		},
		SHA256: "package-hash",
	}
}

func moduleHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestInstallWritesAVersionedDisabledRecordAndKeepsData(t *testing.T) {
	userData := t.TempDir()
	store := plugininstall.Open(userData)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	record, err := store.Install(context.Background(), testPackage(),
		appplugin.InstallOptions{Source: domainplugin.SourceLocalFile}, now)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if record.Enabled {
		t.Fatal("install left the plugin enabled")
	}
	if record.Source != domainplugin.SourceLocalFile || record.PackageSHA256 != "package-hash" {
		t.Fatalf("record = %+v", record)
	}
	versionDir := filepath.Join(userData, "plugins", "acme.notes", "1.2.3")
	for _, name := range []string{"plugin.toml", "plugin.wasm", "assets/a.txt"} {
		info, err := os.Stat(filepath.Join(versionDir, name))
		if err != nil {
			t.Fatalf("installed file %s: %v", name, err)
		}
		if info.Mode().Perm()&0o200 != 0 {
			t.Errorf("%s is writable after install (mode %v)", name, info.Mode())
		}
	}
	if _, err := os.Stat(filepath.Join(userData, "plugins", "acme.notes", "install.json")); err != nil {
		t.Fatalf("install.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(userData, "plugin-data", "acme.notes")); err != nil {
		t.Fatalf("data directory: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(userData, "plugins", ".staging")); len(entries) != 0 {
		t.Fatalf("staging was not cleaned: %v", entries)
	}
}

func TestInstallRejectsASecondVersionOfTheSameID(t *testing.T) {
	store := plugininstall.Open(t.TempDir())
	now := time.Now().UTC()
	if _, err := store.Install(context.Background(), testPackage(),
		appplugin.InstallOptions{Source: domainplugin.SourceLocalFile}, now); err != nil {
		t.Fatalf("first install: %v", err)
	}
	_, err := store.Install(context.Background(), testPackage(), appplugin.InstallOptions{Source: domainplugin.SourceLocalFile}, now)
	if !errors.Is(err, appplugin.ErrAlreadyInstalled) {
		t.Fatalf("second install = %v, want ErrAlreadyInstalled", err)
	}
}

func TestInstallNeverOverwritesAnExistingPathAndRollsBack(t *testing.T) {
	userData := t.TempDir()
	store := plugininstall.Open(userData)
	pkg := testPackage()
	// "assets/a.txt" must be a file, but a later entry needs it to be a
	// directory. Extraction must refuse to replace the file and roll back the
	// whole install rather than resolve the conflict either way.
	pkg.Files["assets/a.txt/nested.txt"] = []byte("nested")
	_, err := store.Install(context.Background(), pkg, appplugin.InstallOptions{Source: domainplugin.SourceLocalFile}, time.Now())
	if err == nil {
		t.Fatal("install accepted a file/directory path collision")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("install error = %v, want an overwrite refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(userData, "plugins", "acme.notes")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed install left a record directory: %v", statErr)
	}
	if entries, _ := os.ReadDir(filepath.Join(userData, "plugins", ".staging")); len(entries) != 0 {
		t.Fatalf("failed install left staging entries: %v", entries)
	}
}

func TestInstallRejectsAModuleHashMismatchAndRollsBack(t *testing.T) {
	userData := t.TempDir()
	store := plugininstall.Open(userData)
	pkg := testPackage()
	pkg.Manifest.Wasm.SHA256 = strings.Repeat("b", 64)
	_, err := store.Install(context.Background(), pkg, appplugin.InstallOptions{Source: domainplugin.SourceLocalFile}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("install error = %v, want a post-write sha256 mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(userData, "plugins", "acme.notes")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed install left a record directory: %v", statErr)
	}
	if entries, _ := os.ReadDir(filepath.Join(userData, "plugins", ".staging")); len(entries) != 0 {
		t.Fatalf("failed install left staging entries: %v", entries)
	}
}

func TestInstallFailureLeavesNoVisibleRecord(t *testing.T) {
	userData := t.TempDir()
	store := plugininstall.Open(userData)
	pkg := testPackage()
	pkg.Files["../escape.txt"] = []byte("nope")
	_, err := store.Install(context.Background(), pkg, appplugin.InstallOptions{Source: domainplugin.SourceLocalFile}, time.Now())
	if err == nil {
		t.Fatal("an unsafe package path was installed")
	}
	if _, statErr := os.Stat(filepath.Join(userData, "plugins", "acme.notes")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed install left a record directory: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(userData, "escape.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsafe package path escaped staging: %v", statErr)
	}
}

func TestUninstallKeepsDataUnlessPurged(t *testing.T) {
	userData := t.TempDir()
	store := plugininstall.Open(userData)
	record, err := store.Install(context.Background(), testPackage(),
		appplugin.InstallOptions{Source: domainplugin.SourceLocalFile}, time.Now())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	dataDir := filepath.Join(userData, "plugin-data", record.ID)
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), record.ID, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(userData, "plugins", record.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("package directory survived uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "config.json")); err != nil {
		t.Fatalf("uninstall did not keep data: %v", err)
	}
	if err := store.PurgeData(context.Background(), record.ID); err != nil {
		t.Fatalf("PurgeData: %v", err)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purge did not remove data: %v", err)
	}
}

func TestRecordsIgnoreInterruptedStagingAndLoadInstallMetadata(t *testing.T) {
	userData := t.TempDir()
	store := plugininstall.Open(userData)
	if _, err := store.Install(context.Background(), testPackage(),
		appplugin.InstallOptions{Source: domainplugin.SourceLocalFile}, time.Now()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(userData, "plugins", ".staging", "acme.interrupted"), 0o700); err != nil {
		t.Fatal(err)
	}
	records, err := store.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(records) != 1 || records[0].ID != "acme.notes" {
		t.Fatalf("Records = %+v", records)
	}
	raw, err := os.ReadFile(filepath.Join(userData, "plugins", "acme.notes", plugininstall.InstallFile))
	if err != nil {
		t.Fatal(err)
	}
	var decoded domainplugin.InstallRecord
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("install.json is not valid JSON: %v", err)
	}
	if decoded.Required[0].Permission != domainplugin.PermissionCommands {
		t.Fatalf("install.json permissions = %+v", decoded.Required)
	}
}

func TestPackageDirPointsAtTheInstalledVersion(t *testing.T) {
	userData := t.TempDir()
	store := plugininstall.Open(userData)
	if _, err := store.Install(context.Background(), testPackage(),
		appplugin.InstallOptions{Source: domainplugin.SourceLocalFile}, time.Now()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	dir, err := store.PackageDir(context.Background(), "acme.notes")
	if err != nil {
		t.Fatalf("PackageDir: %v", err)
	}
	want := filepath.Join(userData, "plugins", "acme.notes", "1.2.3")
	if dir != want {
		t.Fatalf("PackageDir = %q, want %q", dir, want)
	}
}
