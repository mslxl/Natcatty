package plugin

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Storage key bounds. Keys are opaque names, not paths: the store encodes them
// so a key can never escape the plugin's data directory.
const (
	// MaxStorageKeyBytes is 180 so the base64url file name stays below the
	// common 255-byte file-name limit.
	MaxStorageKeyBytes = 180
	// MaxStoragePrefixBytes bounds a list prefix.
	MaxStoragePrefixBytes = 180
)

// StorageKeyError reports an invalid private-storage key.
type StorageKeyError struct {
	Key     string
	Problem string
}

func (e *StorageKeyError) Error() string {
	return fmt.Sprintf("plugin storage key %q: %s", e.Key, e.Problem)
}

// StorageNotFoundError reports a get or delete for a missing key.
type StorageNotFoundError struct {
	Key string
}

func (e *StorageNotFoundError) Error() string {
	return fmt.Sprintf("plugin storage: key %q does not exist", e.Key)
}

// PrivateStorage is the plugin-private key-value store. Values persist under
// the plugin's version-independent data directory, so a restart or package
// update keeps them. Quotas apply per plugin across instances. A nil root
// function keeps the store in memory; production wires the package store's
// data directory.
type PrivateStorage struct {
	mu          sync.Mutex
	root        func(pluginID string) string
	valueLimit  int
	pluginLimit int64
	memory      map[string]map[string][]byte
}

// NewPrivateStorage returns a KV store. Non-positive quotas use the contract
// defaults. root may be nil.
func NewPrivateStorage(root func(pluginID string) string, valueLimit int, pluginLimit int64) *PrivateStorage {
	defaults := DefaultServiceLimits()
	if valueLimit <= 0 {
		valueLimit = defaults.StorageValueBytes
	}
	if pluginLimit <= 0 {
		pluginLimit = defaults.StoragePluginBytes
	}
	return &PrivateStorage{
		root:        root,
		valueLimit:  valueLimit,
		pluginLimit: pluginLimit,
		memory:      make(map[string]map[string][]byte),
	}
}

// Get returns one stored value.
func (s *PrivateStorage) Get(pluginID, key string) ([]byte, bool, error) {
	if err := validateStorageKey(key); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.dirLocked(pluginID)
	if err != nil {
		return nil, false, err
	}
	if dir == "" {
		value, ok := s.memory[pluginID][key]
		if !ok {
			return nil, false, nil
		}
		return append([]byte(nil), value...), true, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, storageFileName(key)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("plugin storage: read %q: %w", key, err)
	}
	return data, true, nil
}

// Set stores one value, enforcing the per-value and per-plugin quotas.
func (s *PrivateStorage) Set(pluginID, key string, value []byte) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	if len(value) > s.valueLimit {
		return &LimitError{Resource: "storage value bytes", Limit: s.valueLimit}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.dirLocked(pluginID)
	if err != nil {
		return err
	}
	if dir == "" {
		if s.memory[pluginID] == nil {
			s.memory[pluginID] = make(map[string][]byte)
		}
		total := int64(0)
		for _, existing := range s.memory[pluginID] {
			total += int64(len(existing))
		}
		total -= int64(len(s.memory[pluginID][key]))
		if total+int64(len(value)) > s.pluginLimit {
			return &LimitError{Resource: "plugin storage bytes", Limit: int(s.pluginLimit)}
		}
		s.memory[pluginID][key] = append([]byte(nil), value...)
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("plugin storage: create directory: %w", err)
	}
	total, err := storageTotal(dir)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, storageFileName(key))
	if info, statErr := os.Stat(path); statErr == nil {
		total -= info.Size()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("plugin storage: inspect %q: %w", key, statErr)
	}
	if total+int64(len(value)) > s.pluginLimit {
		return &LimitError{Resource: "plugin storage bytes", Limit: int(s.pluginLimit)}
	}
	return writeFileAtomic(path, value, 0o600)
}

// Delete removes one value.
func (s *PrivateStorage) Delete(pluginID, key string) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.dirLocked(pluginID)
	if err != nil {
		return err
	}
	if dir == "" {
		if _, ok := s.memory[pluginID][key]; !ok {
			return &StorageNotFoundError{Key: key}
		}
		delete(s.memory[pluginID], key)
		return nil
	}
	path := filepath.Join(dir, storageFileName(key))
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &StorageNotFoundError{Key: key}
		}
		return fmt.Errorf("plugin storage: delete %q: %w", key, err)
	}
	return nil
}

// List returns the stored keys with the prefix in lexical order.
func (s *PrivateStorage) List(pluginID, prefix string) ([]string, error) {
	if len(prefix) > MaxStoragePrefixBytes {
		return nil, &StorageKeyError{Problem: "prefix is too long"}
	}
	if strings.ContainsRune(prefix, 0) {
		return nil, &StorageKeyError{Key: prefix, Problem: "contains a NUL byte"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.dirLocked(pluginID)
	if err != nil {
		return nil, err
	}
	var keys []string
	if dir == "" {
		for key := range s.memory[pluginID] {
			if strings.HasPrefix(key, prefix) {
				keys = append(keys, key)
			}
		}
	} else {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("plugin storage: list: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Name() == "" {
				continue
			}
			key, decodeErr := base64.RawURLEncoding.DecodeString(entry.Name())
			if decodeErr != nil || !utf8.Valid(key) || !strings.HasPrefix(string(key), prefix) {
				continue
			}
			keys = append(keys, string(key))
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Total returns the plugin's current stored bytes.
func (s *PrivateStorage) Total(pluginID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.dirLocked(pluginID)
	if err != nil {
		return 0, err
	}
	if dir == "" {
		total := int64(0)
		for _, value := range s.memory[pluginID] {
			total += int64(len(value))
		}
		return total, nil
	}
	return storageTotal(dir)
}

// StorageGet returns one private value. The storage permission is unscoped.
func (s *ServiceSession) StorageGet(key string) ([]byte, error) {
	if err := s.check(ServiceStorage, "get", domainplugin.PermissionStorage,
		domainplugin.Scope{}); err != nil {
		return nil, err
	}
	value, ok, err := s.host.storage.Get(s.ref.ID, key)
	if err != nil {
		return nil, s.fail(ServiceStorage, "get", domainplugin.PermissionStorage,
			domainplugin.Scope{}, storageResult(err),
			serviceError(ServiceStorage, "get", domainplugin.PermissionStorage,
				domainplugin.Scope{}, err))
	}
	if !ok {
		return nil, s.fail(ServiceStorage, "get", domainplugin.PermissionStorage,
			domainplugin.Scope{}, ServiceCodeNotFound,
			&ServiceError{Service: ServiceStorage, Method: "get", Code: ServiceCodeNotFound,
				Permission: domainplugin.PermissionStorage,
				Message:    fmt.Sprintf("key %q does not exist", key),
				Cause:      &StorageNotFoundError{Key: key}})
	}
	s.record(ServiceStorage, "get", domainplugin.PermissionStorage, domainplugin.Scope{}, ServiceCodeOK)
	return value, nil
}

// StorageSet writes one private value.
func (s *ServiceSession) StorageSet(key string, value []byte) error {
	if err := s.check(ServiceStorage, "set", domainplugin.PermissionStorage,
		domainplugin.Scope{}); err != nil {
		return err
	}
	if err := s.host.storage.Set(s.ref.ID, key, value); err != nil {
		return s.fail(ServiceStorage, "set", domainplugin.PermissionStorage,
			domainplugin.Scope{}, storageResult(err),
			serviceError(ServiceStorage, "set", domainplugin.PermissionStorage,
				domainplugin.Scope{}, err))
	}
	s.record(ServiceStorage, "set", domainplugin.PermissionStorage, domainplugin.Scope{}, ServiceCodeOK)
	return nil
}

// StorageDelete removes one private value.
func (s *ServiceSession) StorageDelete(key string) error {
	if err := s.check(ServiceStorage, "delete", domainplugin.PermissionStorage,
		domainplugin.Scope{}); err != nil {
		return err
	}
	if err := s.host.storage.Delete(s.ref.ID, key); err != nil {
		return s.fail(ServiceStorage, "delete", domainplugin.PermissionStorage,
			domainplugin.Scope{}, storageResult(err),
			serviceError(ServiceStorage, "delete", domainplugin.PermissionStorage,
				domainplugin.Scope{}, err))
	}
	s.record(ServiceStorage, "delete", domainplugin.PermissionStorage, domainplugin.Scope{}, ServiceCodeOK)
	return nil
}

// StorageList returns the private keys with prefix in lexical order.
func (s *ServiceSession) StorageList(prefix string) ([]string, error) {
	if err := s.check(ServiceStorage, "list", domainplugin.PermissionStorage,
		domainplugin.Scope{}); err != nil {
		return nil, err
	}
	keys, err := s.host.storage.List(s.ref.ID, prefix)
	if err != nil {
		return nil, s.fail(ServiceStorage, "list", domainplugin.PermissionStorage,
			domainplugin.Scope{}, storageResult(err),
			serviceError(ServiceStorage, "list", domainplugin.PermissionStorage,
				domainplugin.Scope{}, err))
	}
	s.record(ServiceStorage, "list", domainplugin.PermissionStorage, domainplugin.Scope{}, ServiceCodeOK)
	return keys, nil
}

// StorageTotal returns the plugin's stored bytes for diagnostics.
func (s *ServiceSession) StorageTotal() (int64, error) {
	if s == nil {
		return 0, nil
	}
	return s.host.storage.Total(s.ref.ID)
}

func storageResult(err error) string {
	var missing *StorageNotFoundError
	var invalid *StorageKeyError
	var limit *LimitError
	switch {
	case errors.As(err, &missing):
		return ServiceCodeNotFound
	case errors.As(err, &invalid):
		return ServiceCodeInvalid
	case errors.As(err, &limit):
		return ServiceCodeLimit
	default:
		return ServiceCodeFault
	}
}

func (s *PrivateStorage) dirLocked(pluginID string) (string, error) {
	if s.root == nil {
		return "", nil
	}
	base := s.root(pluginID)
	if base == "" {
		return "", nil
	}
	if filepath.Base(pluginID) != pluginID || pluginID == "." || pluginID == ".." {
		return "", fmt.Errorf("plugin storage: invalid plugin id %q", pluginID)
	}
	return filepath.Join(base, "kv"), nil
}

func validateStorageKey(key string) error {
	if key == "" {
		return &StorageKeyError{Problem: "must not be empty"}
	}
	if len(key) > MaxStorageKeyBytes {
		return &StorageKeyError{Key: key, Problem: fmt.Sprintf("must be at most %d bytes", MaxStorageKeyBytes)}
	}
	if !utf8.ValidString(key) {
		return &StorageKeyError{Key: key, Problem: "must be valid UTF-8"}
	}
	if strings.ContainsRune(key, 0) {
		return &StorageKeyError{Key: key, Problem: "contains a NUL byte"}
	}
	if strings.ContainsAny(key, `/\`) {
		return &StorageKeyError{Key: key, Problem: "must not contain path separators"}
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return &StorageKeyError{Key: key, Problem: "must not contain control characters"}
		}
	}
	return nil
}

func storageFileName(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

func storageTotal(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("plugin storage: measure: %w", err)
	}
	total := int64(0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, fmt.Errorf("plugin storage: measure %q: %w", entry.Name(), err)
		}
		total += info.Size()
	}
	return total, nil
}

// writeFileAtomic writes data beside target and renames it into place, so a
// crash never leaves a partially written value.
func writeFileAtomic(target string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(target), ".kv-*.tmp")
	if err != nil {
		return fmt.Errorf("plugin storage: create temporary file: %w", err)
	}
	name := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("plugin storage: set permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("plugin storage: write: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("plugin storage: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("plugin storage: close: %w", err)
	}
	if err := os.Rename(name, target); err != nil {
		return fmt.Errorf("plugin storage: commit: %w", err)
	}
	cleanup = false
	return nil
}
