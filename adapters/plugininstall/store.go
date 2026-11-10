// Package plugininstall owns the versioned plugin install layout under the
// MyGo user-data directory. Install is transactional: files are validated
// before staging, written to .staging, renamed atomically into the version
// directory, and only then does install.json make the record visible. The end
// state is installed but disabled.
package plugininstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mslxl/natcatty/adapters/pluginmanifest"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// InstallFile is the host-owned metadata file beside the version directory.
const InstallFile = "install.json"

// TrustFile persists trust-on-first-use source pins across installs and
// updates.
const TrustFile = "trust.json"

// PreferencesFile persists registry policy such as automatic updates and
// per-plugin channels.
const PreferencesFile = "preferences.json"

var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Store implements application/plugin.PackageStore.
type Store struct {
	pluginRoot string
	dataRoot   string

	// mu serializes the read-modify-write trust and preference documents.
	mu sync.Mutex
}

var _ appplugin.PackageStore = (*Store)(nil)

// Open returns the store rooted at the MyGo user-data directory:
//
//	<userData>/plugins/<id>/<version>/   read-only package files
//	<userData>/plugins/<id>/install.json host-owned metadata
//	<userData>/plugin-data/<id>/         writable, version-independent data
//	<userData>/plugins/.staging/         interrupted installs
func Open(userData string) *Store {
	return &Store{
		pluginRoot: filepath.Join(userData, "plugins"),
		dataRoot:   filepath.Join(userData, "plugin-data"),
	}
}

// Install stages and atomically commits one validated package.
func (s *Store) Install(ctx context.Context, pkg appplugin.Package,
	options appplugin.InstallOptions, now time.Time) (domainplugin.InstallRecord, error) {
	id := pkg.Manifest.ID
	version := pkg.Manifest.Version
	if err := checkID(id); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if _, ok := domainplugin.ParseSemver(version); !ok {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: invalid version %q", version)
	}
	if existing, ok, err := s.Record(ctx, id); err != nil {
		return domainplugin.InstallRecord{}, err
	} else if ok {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s %s", appplugin.ErrAlreadyInstalled,
			existing.ID, existing.Version)
	}

	if err := os.MkdirAll(filepath.Join(s.pluginRoot, ".staging"), 0o700); err != nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Join(s.pluginRoot, ".staging"), id+"-")
	if err != nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: create staging: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := writeTree(stage, pkg.Files); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	// Defense in depth: verify the bytes that actually landed on disk before
	// the tree can become visible. Validation rejects filesystem-equivalent
	// entry names, but exclusive creation and this read-back are the last
	// guarantees that the installed module is the one whose hash was checked.
	if err := verifyExtractedTree(stage, pkg); err != nil {
		return domainplugin.InstallRecord{}, err
	}

	parent := filepath.Join(s.pluginRoot, id)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: %w", err)
	}
	versionDir := filepath.Join(parent, version)
	if _, err := os.Stat(versionDir); err == nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s %s", appplugin.ErrAlreadyInstalled, id, version)
	}
	if err := os.Rename(stage, versionDir); err != nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: commit version directory: %w", err)
	}
	committed = true
	if err := makeReadOnly(versionDir); err != nil {
		_ = os.RemoveAll(versionDir)
		return domainplugin.InstallRecord{}, err
	}

	required, optional, err := pkg.Manifest.Permissions.Requests()
	if err != nil {
		_ = os.RemoveAll(versionDir)
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: %w", err)
	}
	record := domainplugin.InstallRecord{
		ID:            id,
		Name:          pkg.Manifest.Name,
		Version:       version,
		Source:        options.Source,
		PackageSHA256: pkg.SHA256,
		InstalledAt:   now,
		Enabled:       false,
		TrustedSource: options.TrustedSource,
		Required:      required,
		Optional:      optional,
		Dependencies:  append([]domainplugin.Dependency(nil), pkg.Manifest.Dependencies...),
	}
	// Create the data directory before publishing the record. Otherwise a
	// failure here would leave a recorded install that the caller reports as
	// failed, and a retry would be rejected as already installed.
	if err := os.MkdirAll(s.dataDir(id), 0o700); err != nil {
		_ = os.RemoveAll(versionDir)
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: create data directory: %w", err)
	}
	if err := s.SaveRecord(ctx, record); err != nil {
		_ = os.RemoveAll(versionDir)
		return domainplugin.InstallRecord{}, err
	}
	return record, nil
}

// Record returns one install record.
func (s *Store) Record(ctx context.Context, id string) (domainplugin.InstallRecord, bool, error) {
	if err := checkID(id); err != nil {
		return domainplugin.InstallRecord{}, false, err
	}
	data, err := os.ReadFile(filepath.Join(s.pluginRoot, id, InstallFile))
	if errors.Is(err, os.ErrNotExist) {
		return domainplugin.InstallRecord{}, false, nil
	}
	if err != nil {
		return domainplugin.InstallRecord{}, false, fmt.Errorf("plugin install: read record %s: %w", id, err)
	}
	var record domainplugin.InstallRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return domainplugin.InstallRecord{}, false,
			fmt.Errorf("plugin install: parse record %s: %w", id, err)
	}
	if record.ID != id {
		return domainplugin.InstallRecord{}, false,
			fmt.Errorf("plugin install: record %s names plugin %q", id, record.ID)
	}
	return record, true, nil
}

// Records returns every installed plugin in id order.
func (s *Store) Records(ctx context.Context) ([]domainplugin.InstallRecord, error) {
	entries, err := os.ReadDir(s.pluginRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("plugin install: list plugins: %w", err)
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".staging" {
			continue
		}
		ids = append(ids, entry.Name())
	}
	sort.Strings(ids)
	out := make([]domainplugin.InstallRecord, 0, len(ids))
	for _, id := range ids {
		record, ok, err := s.Record(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, record)
		}
	}
	return out, nil
}

// SaveRecord rewrites install.json atomically.
func (s *Store) SaveRecord(ctx context.Context, record domainplugin.InstallRecord) error {
	if err := checkID(record.ID); err != nil {
		return err
	}
	directory := filepath.Join(s.pluginRoot, record.ID)
	if err := writeJSONAtomic(directory, InstallFile, record); err != nil {
		return fmt.Errorf("plugin install: %w", err)
	}
	return nil
}

// writeJSONAtomic writes value to directory/name through a synced temporary
// file and an atomic rename. A crash leaves either the old file or the new
// one, never a partial document.
func writeJSONAtomic(directory, name string, value any) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(directory, "."+name+".*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync %s: %w", name, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Rename(tempName, filepath.Join(directory, name)); err != nil {
		return fmt.Errorf("commit %s: %w", name, err)
	}
	cleanup = false
	return nil
}

// PromoteVersion atomically replaces the current version of an installed
// plugin. The replaced version becomes the single retained previous version.
func (s *Store) PromoteVersion(ctx context.Context, pkg appplugin.Package,
	record domainplugin.InstallRecord, now time.Time) (domainplugin.InstallRecord, error) {
	id := pkg.Manifest.ID
	version := pkg.Manifest.Version
	if err := checkID(id); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if record.ID != id || record.Version != version {
		return domainplugin.InstallRecord{}, fmt.Errorf(
			"plugin install: record %s %s does not match package %s %s",
			record.ID, record.Version, id, version)
	}
	existing, ok, err := s.Record(ctx, id)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if !ok {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s", appplugin.ErrNotInstalled, id)
	}
	required, optional, err := pkg.Manifest.Permissions.Requests()
	if err != nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(s.pluginRoot, ".staging"), 0o700); err != nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Join(s.pluginRoot, ".staging"), id+"-update-")
	if err != nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: create staging: %w", err)
	}
	cleanupStage := true
	defer func() {
		if cleanupStage {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := writeTree(stage, pkg.Files); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if err := verifyExtractedTree(stage, pkg); err != nil {
		return domainplugin.InstallRecord{}, err
	}

	parent := filepath.Join(s.pluginRoot, id)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: %w", err)
	}
	versionDir := filepath.Join(parent, version)
	created := false
	if _, statErr := os.Stat(versionDir); statErr == nil {
		// A retained version may be re-promoted (for example when switching
		// back to a version the user already had). Its bytes must still match
		// the validated package.
		if err := verifyExtractedTree(versionDir, pkg); err != nil {
			return domainplugin.InstallRecord{}, err
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if err := os.Rename(stage, versionDir); err != nil {
			return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: commit version directory: %w", err)
		}
		cleanupStage = false
		created = true
		if err := makeReadOnly(versionDir); err != nil {
			_ = os.RemoveAll(versionDir)
			return domainplugin.InstallRecord{}, err
		}
	} else {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin install: inspect %s: %w", versionDir, statErr)
	}

	newRecord := record
	newRecord.ID = id
	newRecord.Name = pkg.Manifest.Name
	newRecord.Version = version
	newRecord.PackageSHA256 = pkg.SHA256
	newRecord.InstalledAt = now
	newRecord.Required = required
	newRecord.Optional = optional
	newRecord.Dependencies = append([]domainplugin.Dependency(nil), pkg.Manifest.Dependencies...)
	newRecord.Previous = previousFrom(existing)
	if err := s.SaveRecord(ctx, newRecord); err != nil {
		if created {
			_ = os.RemoveAll(versionDir)
		}
		return domainplugin.InstallRecord{}, err
	}
	_ = s.pruneVersions(id, version, newRecord.Previous.Version)
	return newRecord, nil
}

// RestorePrevious swaps the current version with the retained previous
// version. The swapped-out version becomes the new previous, so the rollback
// itself can be rolled back.
func (s *Store) RestorePrevious(ctx context.Context, id string,
	now time.Time) (domainplugin.InstallRecord, error) {
	if err := checkID(id); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	record, ok, err := s.Record(ctx, id)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if !ok {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s", appplugin.ErrNotInstalled, id)
	}
	previous := record.Previous
	if previous == nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s", appplugin.ErrNoPreviousVersion, id)
	}
	previousDir := filepath.Join(s.pluginRoot, id, previous.Version)
	info, err := os.Stat(previousDir)
	if err != nil || !info.IsDir() {
		return domainplugin.InstallRecord{}, fmt.Errorf(
			"plugin install: retained version %s of %s is missing", previous.Version, id)
	}
	restored := record
	restored.Name = previous.Name
	restored.Version = previous.Version
	restored.PackageSHA256 = previous.PackageSHA256
	restored.InstalledAt = now
	restored.Required = previous.Required
	restored.Optional = previous.Optional
	restored.Dependencies = append([]domainplugin.Dependency(nil), previous.Dependencies...)
	restored.Grants = previous.Grants
	restored.Remembered = previous.Remembered
	restored.Previous = previousFrom(record)
	if err := s.SaveRecord(ctx, restored); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	_ = s.pruneVersions(id, restored.Version, restored.Previous.Version)
	return restored, nil
}

func previousFrom(record domainplugin.InstallRecord) *domainplugin.PreviousInstall {
	return &domainplugin.PreviousInstall{
		Name:          record.Name,
		Version:       record.Version,
		PackageSHA256: record.PackageSHA256,
		Required:      append([]domainplugin.PermissionRequest(nil), record.Required...),
		Optional:      append([]domainplugin.PermissionRequest(nil), record.Optional...),
		Dependencies:  append([]domainplugin.Dependency(nil), record.Dependencies...),
		Grants:        append([]domainplugin.PermissionGrant(nil), record.Grants...),
		Remembered:    append([]domainplugin.RememberedGrant(nil), record.Remembered...),
	}
}

// pruneVersions removes every retained version directory except keep. The
// install record is authoritative; a leftover directory is harmless, so a
// cleanup failure does not fail the version transition.
func (s *Store) pruneVersions(id string, keep ...string) error {
	parent := filepath.Join(s.pluginRoot, id)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	kept := make(map[string]bool, len(keep))
	for _, version := range keep {
		kept[version] = true
	}
	for _, entry := range entries {
		if !entry.IsDir() || kept[entry.Name()] {
			continue
		}
		_ = os.RemoveAll(filepath.Join(parent, entry.Name()))
	}
	return nil
}

// Remove deletes the package and install metadata, keeping the data directory
// unless purge is true.
func (s *Store) Remove(ctx context.Context, id string, purge bool) error {
	if _, ok, err := s.Record(ctx, id); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", appplugin.ErrNotInstalled, id)
	}
	// Remove the whole plugin directory: the current version, the retained
	// previous version, and install.json. The trust pin deliberately survives
	// uninstall so reinstalling from a different source still needs
	// confirmation.
	if err := os.RemoveAll(filepath.Join(s.pluginRoot, id)); err != nil {
		return fmt.Errorf("plugin install: remove %s: %w", id, err)
	}
	if purge {
		if err := s.PurgeData(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// PurgeData deletes the version-independent data directory.
func (s *Store) PurgeData(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if err := os.RemoveAll(s.dataDir(id)); err != nil {
		return fmt.Errorf("plugin install: purge data for %s: %w", id, err)
	}
	return nil
}

// PackageDir returns the read-only version directory for one record.
func (s *Store) PackageDir(ctx context.Context, id string) (string, error) {
	record, ok, err := s.Record(ctx, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: %s", appplugin.ErrNotInstalled, id)
	}
	return filepath.Join(s.pluginRoot, record.ID, record.Version), nil
}

// DataDir returns the writable data directory. It exists after install.
func (s *Store) DataDir(id string) string { return s.dataDir(id) }

func (s *Store) dataDir(id string) string { return filepath.Join(s.dataRoot, id) }

func checkID(id string) error {
	if !pluginIDPattern.MatchString(id) || strings.Count(id, ".") != 1 {
		return fmt.Errorf("plugin install: invalid plugin id %q", id)
	}
	return nil
}

// writeTree writes package files into stage without escaping it. The
// validator already checked every path; this repeats the check so a store
// caller cannot bypass it. Every file is created exclusively: a name that
// collides with an existing entry aborts the install instead of overwriting
// validated bytes.
func writeTree(stage string, files map[string][]byte) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := checkPackagePath(name); err != nil {
			return err
		}
		target := filepath.Join(stage, filepath.FromSlash(name))
		if err := makeDirExclusive(stage, filepath.Dir(target)); err != nil {
			return err
		}
		if err := writeFileExclusive(target, files[name]); err != nil {
			return err
		}
	}
	return nil
}

// makeDirExclusive creates directory and its missing parents below root. An
// existing directory is reused (sibling files share parents); an existing
// non-directory is a collision and fails the install.
func makeDirExclusive(root, directory string) error {
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("plugin install: directory %q escapes staging", directory)
	}
	if relative == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		mkErr := os.Mkdir(current, 0o755)
		if mkErr == nil {
			continue
		}
		if !errors.Is(mkErr, os.ErrExist) {
			return fmt.Errorf("plugin install: create directory %s: %w", current, mkErr)
		}
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return fmt.Errorf("plugin install: inspect %s: %w", current, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("plugin install: refusing to overwrite %s: path is not a directory", current)
		}
	}
	return nil
}

// writeFileExclusive creates target with O_EXCL and fails when any entry
// already exists, including a case- or normalization-equivalent one.
func writeFileExclusive(target string, data []byte) error {
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("plugin install: refusing to overwrite existing entry %s", target)
	}
	if err != nil {
		return fmt.Errorf("plugin install: create %s: %w", target, err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("plugin install: write %s: %w", target, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("plugin install: close %s: %w", target, closeErr)
	}
	return nil
}

// verifyExtractedTree proves the staged tree holds exactly the validated
// manifest bytes and a module whose sha256 matches the manifest. It runs
// before the version directory is published.
func verifyExtractedTree(stage string, pkg appplugin.Package) error {
	manifestBytes, ok := pkg.Files[pluginmanifest.ManifestFile]
	if !ok {
		return fmt.Errorf("plugin install: validated package has no %s", pluginmanifest.ManifestFile)
	}
	written, err := os.ReadFile(filepath.Join(stage, pluginmanifest.ManifestFile))
	if err != nil {
		return fmt.Errorf("plugin install: verify %s: %w", pluginmanifest.ManifestFile, err)
	}
	if !bytes.Equal(written, manifestBytes) {
		return fmt.Errorf("plugin install: %s bytes changed during extraction", pluginmanifest.ManifestFile)
	}

	modulePath := path.Clean(pkg.Manifest.Wasm.Path)
	if err := checkPackagePath(modulePath); err != nil {
		return err
	}
	module, err := os.ReadFile(filepath.Join(stage, filepath.FromSlash(modulePath)))
	if err != nil {
		return fmt.Errorf("plugin install: verify module %s: %w", modulePath, err)
	}
	sum := sha256.Sum256(module)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, pkg.Manifest.Wasm.SHA256) {
		return fmt.Errorf("plugin install: module %s sha256 is %s, want %s",
			modulePath, got, strings.ToLower(pkg.Manifest.Wasm.SHA256))
	}
	return nil
}

func checkPackagePath(name string) error {
	clean := path.Clean(name)
	if clean != name || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, "../") || path.IsAbs(clean) ||
		strings.Contains(name, "\\") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("plugin install: unsafe package path %q", name)
	}
	return nil
}

// makeReadOnly clears write permission from package files. Directories stay
// owner-writable because unlinking an entry requires write permission on its
// parent; the package files themselves cannot be modified in place.
func makeReadOnly(directory string) error {
	return filepath.Walk(directory, func(current string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("plugin install: %w", err)
		}
		if info.IsDir() {
			return nil
		}
		return os.Chmod(current, 0o444)
	})
}
