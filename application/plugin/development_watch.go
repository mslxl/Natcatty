package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// ManifestFileName is the file every unpacked plugin directory uses for its
// manifest. The development watcher includes it in the change fingerprint.
const ManifestFileName = "plugin.toml"

// DefaultDevelopmentWatchInterval is how often the development watcher checks
// the manifest and module bytes. It is short enough for an edit-build-reload
// loop and long enough to stay off the CPU.
const DefaultDevelopmentWatchInterval = 750 * time.Millisecond

// DevelopmentChange names one unpacked directory whose manifest or module
// bytes changed and was reloaded exactly once.
type DevelopmentChange struct {
	ID        string               `json:"id"`
	Directory string               `json:"directory"`
	Fiber     domainplugin.FiberID `json:"fiber"`
}

// DevelopmentWatcher detects manifest and module changes for unpacked
// development directories. It compares content hashes, not timestamps, so a
// same-size edit cannot be missed. Each observed change causes exactly one
// container reload; no retry loop is entered when the reload itself fails.
type DevelopmentWatcher struct {
	manager  *Manager
	interval time.Duration

	mu           sync.Mutex
	fingerprints map[string]string
}

// NewDevelopmentWatcher returns a watcher for one manager. A non-positive
// interval keeps DefaultDevelopmentWatchInterval.
func (m *Manager) NewDevelopmentWatcher(interval time.Duration) *DevelopmentWatcher {
	if interval <= 0 {
		interval = DefaultDevelopmentWatchInterval
	}
	return &DevelopmentWatcher{
		manager:      m,
		interval:     interval,
		fingerprints: make(map[string]string),
	}
}

// Check compares every loaded development directory with its previous
// fingerprint and reloads each changed one once. The first observation of a
// directory only records its fingerprint: the initial load already happened.
// After a reload attempt the baseline is recomputed from the manifest the
// fiber now runs: a reload may replace the manifest and therefore the module
// path, and a failed reload must not be retried until the files change.
func (w *DevelopmentWatcher) Check(ctx context.Context) ([]DevelopmentChange, error) {
	if w == nil || w.manager == nil {
		return nil, nil
	}
	type directory struct {
		id    string
		dir   string
		fiber *Fiber
	}
	w.manager.mu.Lock()
	directories := make([]directory, 0, len(w.manager.dev))
	for id, dev := range w.manager.dev {
		directories = append(directories, directory{id: id, dir: dev.Directory, fiber: dev.Fiber})
	}
	w.manager.mu.Unlock()

	var changes []DevelopmentChange
	var errs []error
	for _, entry := range directories {
		if entry.fiber == nil {
			continue
		}
		fingerprint := developmentFingerprint(entry.dir, entry.fiber.Manifest().Wasm.Path)
		w.mu.Lock()
		previous, seen := w.fingerprints[entry.id]
		w.fingerprints[entry.id] = fingerprint
		w.mu.Unlock()
		if !seen || previous == fingerprint {
			continue
		}
		change := DevelopmentChange{ID: entry.id, Directory: entry.dir, Fiber: entry.fiber.ID()}
		// Target the development identity explicitly: an installed copy with
		// the same plugin id is a separate fiber and must never receive this
		// reload.
		key := string(domainplugin.SourceDevelopment) + "/" + entry.id
		if err := w.manager.DevelopmentChanged(ctx, key); err != nil {
			errs = append(errs, err)
		}
		// Re-baseline against the manifest the fiber now runs. A successful
		// reload replaces it, including Wasm.Path, and a failed reload may
		// have replaced it before activation failed; either way the next
		// Check with unchanged files must report nothing.
		w.mu.Lock()
		w.fingerprints[entry.id] = developmentFingerprint(entry.dir,
			entry.fiber.Manifest().Wasm.Path)
		w.mu.Unlock()
		changes = append(changes, change)
	}
	return changes, errors.Join(errs...)
}

// WatchDevelopment polls until ctx is cancelled. A change is reloaded exactly
// once; onError receives reload and filesystem failures so the app can log
// them without stopping the watch.
func (m *Manager) WatchDevelopment(ctx context.Context, interval time.Duration,
	onError func(error)) {
	if m == nil {
		return
	}
	watcher := m.NewDevelopmentWatcher(interval)
	ticker := time.NewTicker(watcher.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := watcher.Check(ctx); err != nil && onError != nil && ctx.Err() == nil {
				onError(err)
			}
		}
	}
}

// developmentFingerprint hashes the manifest and declared module bytes. A
// missing file hashes its error text, so deleting the directory also counts as
// one change and produces one FAILED reload rather than a retry loop.
func developmentFingerprint(directory, modulePath string) string {
	hash := sha256.New()
	for _, path := range []string{
		filepath.Join(directory, ManifestFileName),
		filepath.Join(directory, filepath.FromSlash(modulePath)),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			hash.Write([]byte("error:" + filepath.Base(path) + ":" + err.Error()))
			continue
		}
		hash.Write([]byte("file:" + filepath.Base(path) + ":"))
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
