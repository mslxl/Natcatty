package secrets

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// FileStore keeps secrets in a local JSON file instead of the operating
// system's credential store. It exists for isolated development runs whose
// data lives in a throwaway directory: the file is plaintext at 0600, so it
// must never hold production credentials. The operating system stores nothing
// the app can forget, and a removed directory removes every secret.
//
// The file is replaced atomically, so a crash never leaves a partial key that
// would orphan every credential encrypted with it.
type FileStore struct {
	path string

	mu      sync.Mutex
	entries map[string][]byte
}

const (
	// fileStoreVersion is the on-disk format version. A file from another
	// version is a hard error: silently reinterpreting it could mint a new
	// field key and orphan every stored ciphertext.
	fileStoreVersion = 1
	// fileStoreMaxValue and fileStoreMaxTotal bound what an unattended run
	// can write. A field key is 32 bytes and a sync token is small.
	fileStoreMaxValue   = 64 << 10
	fileStoreMaxTotal   = 1 << 20
	fileStoreMaxEntries = 256
)

// fileStoreDocument is the JSON shape of the store. Values are base64 so the
// file stays text and no byte sequence can break the document.
type fileStoreDocument struct {
	Version int               `json:"version"`
	Entries map[string]string `json:"entries"`
}

var _ Store = (*FileStore)(nil)

// OpenFileStore opens path, creating an empty store when the file does not
// exist. A corrupt file, an unknown version, or a file other users can read
// is an error rather than a silent reset.
func OpenFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("secrets: file store needs a path")
	}
	store := &FileStore{path: path, entries: map[string][]byte{}}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

// load reads the file. A missing file is an empty store; every other failure
// stops the app before it can mint a field key.
func (s *FileStore) load() error {
	info, err := os.Stat(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("secrets: stat %s: %w", s.path, err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return fmt.Errorf("secrets: %s is readable by other users (%04o), want 0600", s.path, perm)
		}
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("secrets: read %s: %w", s.path, err)
	}
	var document fileStoreDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("secrets: decode %s: %w", s.path, err)
	}
	if document.Version != fileStoreVersion {
		return fmt.Errorf("secrets: %s has version %d, want %d", s.path, document.Version, fileStoreVersion)
	}
	entries := make(map[string][]byte, len(document.Entries))
	for key, encoded := range document.Entries {
		value, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("secrets: decode %q in %s: %w", key, s.path, err)
		}
		entries[key] = value
	}
	s.entries = entries
	return nil
}

// Get reads a secret. The caller owns the returned bytes.
func (s *FileStore) Get(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.entries[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

// Set writes a secret, replacing an existing value. The file is durable
// before Set returns.
func (s *FileStore) Set(key string, value []byte) error {
	if key == "" {
		return errors.New("secrets: a file-store key must not be empty")
	}
	if len(value) > fileStoreMaxValue {
		return fmt.Errorf("secrets: value for %q is %d bytes, more than %d", key, len(value), fileStoreMaxValue)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneFileStoreEntries(s.entries)
	next[key] = append([]byte(nil), value...)
	if err := checkFileStoreLimits(next); err != nil {
		return err
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.entries = next
	return nil
}

// Delete removes a secret. Deleting a missing key is not an error.
func (s *FileStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[key]; !ok {
		return nil
	}
	next := cloneFileStoreEntries(s.entries)
	delete(next, key)
	if err := s.save(next); err != nil {
		return err
	}
	s.entries = next
	return nil
}

// save replaces the file atomically. The rename is the commit point; a crash
// before it leaves the previous document intact.
func (s *FileStore) save(entries map[string][]byte) error {
	document := fileStoreDocument{Version: fileStoreVersion, Entries: map[string]string{}}
	for key, value := range entries {
		document.Entries[key] = base64.StdEncoding.EncodeToString(value)
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("secrets: encode %s: %w", s.path, err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("secrets: create %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".secrets-*")
	if err != nil {
		return fmt.Errorf("secrets: create a temporary file in %s: %w", dir, err)
	}
	tempPath := temp.Name()
	defer func() {
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secrets: chmod the temporary file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secrets: write the temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secrets: sync the temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("secrets: close the temporary file: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("secrets: replace %s: %w", s.path, err)
	}
	tempPath = ""
	if runtime.GOOS != "windows" {
		// The rename is only durable once the directory entry is on disk. A
		// failed sync must reach the caller: it decides whether the freshly
		// minted field key can survive a power loss, and silently returning
		// success there would later orphan every credential encrypted with
		// that key. Windows has no directory handle to sync.
		directory, err := os.Open(dir)
		if err != nil {
			return fmt.Errorf("secrets: open %s to sync the directory: %w", dir, err)
		}
		if err := directory.Sync(); err != nil {
			_ = directory.Close()
			return fmt.Errorf("secrets: sync %s: %w", dir, err)
		}
		if err := directory.Close(); err != nil {
			return fmt.Errorf("secrets: close %s: %w", dir, err)
		}
	}
	return nil
}

func checkFileStoreLimits(entries map[string][]byte) error {
	if len(entries) > fileStoreMaxEntries {
		return fmt.Errorf("secrets: the file store holds %d entries, more than %d", len(entries), fileStoreMaxEntries)
	}
	total := 0
	for _, value := range entries {
		total += len(value)
	}
	if total > fileStoreMaxTotal {
		return fmt.Errorf("secrets: the file store holds %d bytes, more than %d", total, fileStoreMaxTotal)
	}
	return nil
}

func cloneFileStoreEntries(entries map[string][]byte) map[string][]byte {
	clone := make(map[string][]byte, len(entries))
	for key, value := range entries {
		clone[key] = append([]byte(nil), value...)
	}
	return clone
}
