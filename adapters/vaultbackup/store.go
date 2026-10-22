// Package vaultbackup stores encrypted local Vault backups.
//
// One file per backup lives under <userData>/vault-backups as
// vault-backup-<createdAt>-<id>.json. The record's metadata is cleartext so
// the list can be built without the key; the payload is AES-256-GCM
// ciphertext under a 32-byte key kept in the OS keychain, and the AAD binds
// the id, timestamp, fingerprint, and format version so tampering with the
// metadata is detected.
package vaultbackup

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
)

const (
	dirName        = "vault-backups"
	filePrefix     = "vault-backup-"
	fileSuffix     = ".json"
	settingsName   = "settings.json"
	keyName        = "vault-backup-key-v1"
	keyBytes       = 32
	nonceBytes     = 12
	label          = "natcatty:v1|vault-backup"
	payloadCipher  = "aes-256-gcm-v1"
	maxFileBytes   = domainbackup.MaxPayloadBytes * 2
	settingsTemp   = ".vault-backup-settings-"
	settingsFormat = 1
)

// Store implements appbackup.Store on the filesystem.
type Store struct {
	dir  string
	keys platformsecrets.Store
	read io.Reader

	mu    sync.Mutex
	key   []byte
	fault func(stage string) error
}

var _ appbackup.Store = (*Store)(nil)

// Option customizes a Store.
type Option func(*Store)

// WithRandom sets the randomness source for the backup key and nonces. Tests
// use it for deterministic ciphertext.
func WithRandom(reader io.Reader) Option {
	return func(s *Store) {
		if reader != nil {
			s.read = reader
		}
	}
}

// WithFault injects a write failure at a named stage. Tests use it to prove a
// failed write leaves no final file.
func WithFault(fault func(stage string) error) Option {
	return func(s *Store) { s.fault = fault }
}

// Open returns a Store rooted at dir. The directory is created on first use.
func Open(dir string, keys platformsecrets.Store, opts ...Option) *Store {
	store := &Store{dir: dir, keys: keys, read: rand.Reader}
	for _, opt := range opts {
		opt(store)
	}
	return store
}

// Dir returns the backup directory, for the "open folder" action and tests.
func (s *Store) Dir() string { return s.dir }

func (s *Store) checkFault(stage string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(stage)
}

// EncryptionAvailable reports whether the keychain can supply or hold the
// backup key.
func (s *Store) EncryptionAvailable() bool {
	if s.keys == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		return true
	}
	value, err := s.keys.Get(keyName)
	if err == nil {
		return len(value) == keyBytes
	}
	return errors.Is(err, platformsecrets.ErrNotFound)
}

func (s *Store) encryptionKey() ([]byte, error) {
	if s.keys == nil {
		return nil, appbackup.ErrEncryptionUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		return s.key, nil
	}
	value, err := s.keys.Get(keyName)
	switch {
	case err == nil:
		if len(value) != keyBytes {
			return nil, fmt.Errorf("vaultbackup: backup key is %d bytes, want %d", len(value), keyBytes)
		}
	case errors.Is(err, platformsecrets.ErrNotFound):
		value = make([]byte, keyBytes)
		if _, err := io.ReadFull(s.read, value); err != nil {
			return nil, fmt.Errorf("vaultbackup: generate backup key: %w", err)
		}
		if err := s.keys.Set(keyName, value); err != nil {
			return nil, appbackup.ErrEncryptionUnavailable
		}
	case errors.Is(err, platformsecrets.ErrUnsupported):
		return nil, appbackup.ErrEncryptionUnavailable
	default:
		return nil, appbackup.ErrEncryptionUnavailable
	}
	s.key = value
	return s.key, nil
}

// record is the on-disk envelope. Metadata is cleartext so listing never
// needs the key; payload is the encrypted canonical snapshot.
type record struct {
	FormatVersion    int                 `json:"formatVersion"`
	ID               string              `json:"id"`
	CreatedAt        int64               `json:"createdAt"`
	Reason           domainbackup.Reason `json:"reason"`
	SourceAppVersion string              `json:"sourceAppVersion,omitempty"`
	TargetAppVersion string              `json:"targetAppVersion,omitempty"`
	Fingerprint      string              `json:"fingerprint"`
	SizeBytes        int64               `json:"sizeBytes"`
	Counts           domainbackup.Counts `json:"counts"`
	PayloadEncoding  string              `json:"payloadEncoding"`
	Nonce            []byte              `json:"nonce"`
	Payload          []byte              `json:"payload"`
}

func (r record) summary() domainbackup.Summary {
	return domainbackup.Summary{
		ID:               r.ID,
		CreatedAt:        r.CreatedAt,
		Reason:           r.Reason,
		Fingerprint:      r.Fingerprint,
		SizeBytes:        r.SizeBytes,
		Counts:           r.Counts,
		SourceAppVersion: r.SourceAppVersion,
		TargetAppVersion: r.TargetAppVersion,
	}
}

type storedRecord struct {
	record record
	path   string
}

func (s *Store) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("vaultbackup: create backup directory: %w", err)
	}
	return nil
}

func safeID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, char := range id {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-' || char == '_':
		default:
			return false
		}
	}
	return true
}

func validFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func aad(r record) []byte {
	return []byte(label + "|" + strconv.Itoa(r.FormatVersion) + "|" + r.ID + "|" +
		strconv.FormatInt(r.CreatedAt, 10) + "|" + r.Fingerprint)
}

// Save encrypts and atomically writes one backup.
func (s *Store) Save(ctx context.Context, input appbackup.SaveInput) (domainbackup.Summary, error) {
	if err := ctx.Err(); err != nil {
		return domainbackup.Summary{}, err
	}
	if !safeID(input.ID) {
		return domainbackup.Summary{}, fmt.Errorf("vaultbackup: invalid backup id %q", input.ID)
	}
	if !validFingerprint(input.Fingerprint) {
		return domainbackup.Summary{}, fmt.Errorf("vaultbackup: invalid fingerprint")
	}
	if input.CreatedAt <= 0 {
		return domainbackup.Summary{}, fmt.Errorf("vaultbackup: invalid creation time")
	}
	if !input.Reason.Valid() {
		return domainbackup.Summary{}, fmt.Errorf("vaultbackup: invalid reason %q", input.Reason)
	}
	if len(input.Payload) > domainbackup.MaxPayloadBytes {
		return domainbackup.Summary{}, appbackup.ErrPayloadTooLarge
	}
	key, err := s.encryptionKey()
	if err != nil {
		return domainbackup.Summary{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return domainbackup.Summary{}, fmt.Errorf("vaultbackup: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return domainbackup.Summary{}, fmt.Errorf("vaultbackup: cipher: %w", err)
	}
	nonce := make([]byte, nonceBytes)
	if _, err := io.ReadFull(s.read, nonce); err != nil {
		return domainbackup.Summary{}, fmt.Errorf("vaultbackup: nonce: %w", err)
	}
	entry := record{
		FormatVersion:    domainbackup.FormatVersion,
		ID:               input.ID,
		CreatedAt:        input.CreatedAt,
		Reason:           input.Reason,
		SourceAppVersion: input.SourceAppVersion,
		TargetAppVersion: input.TargetAppVersion,
		Fingerprint:      input.Fingerprint,
		SizeBytes:        int64(len(input.Payload)),
		Counts:           input.Counts,
		PayloadEncoding:  payloadCipher,
		Nonce:            nonce,
	}
	entry.Payload = aead.Seal(nil, nonce, input.Payload, aad(entry))
	data, err := json.Marshal(entry)
	if err != nil {
		return domainbackup.Summary{}, fmt.Errorf("vaultbackup: encode record: %w", err)
	}
	if err := s.ensureDir(); err != nil {
		return domainbackup.Summary{}, err
	}
	path := filepath.Join(s.dir, filePrefix+strconv.FormatInt(entry.CreatedAt, 10)+"-"+entry.ID+fileSuffix)
	if err := s.writeAtomic(path, data); err != nil {
		return domainbackup.Summary{}, err
	}
	return entry.summary(), nil
}

// writeAtomic writes to a sibling temp file, syncs it, renames it into place,
// and syncs the directory where the platform supports it. A failure never
// leaves a partial final file.
func (s *Store) writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(s.dir, "vault-backup-*.json.tmp-")
	if err != nil {
		return fmt.Errorf("vaultbackup: create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("vaultbackup: set backup permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("vaultbackup: write backup: %w", err)
	}
	if err := s.checkFault("sync"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("vaultbackup: sync backup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("vaultbackup: close backup: %w", err)
	}
	if err := s.checkFault("rename"); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("vaultbackup: rename backup: %w", err)
	}
	cleanup = false
	if runtime.GOOS != "windows" {
		if dir, err := os.Open(s.dir); err == nil {
			// Defense in depth: a durable rename survives a crash. Filesystems
			// that refuse the directory sync still have the renamed file.
			_ = dir.Sync()
			_ = dir.Close()
		}
	}
	return nil
}

func (s *Store) listRecords() ([]storedRecord, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("vaultbackup: read backup directory: %w", err)
	}
	records := make([]storedRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if len(name) <= len(filePrefix)+len(fileSuffix) ||
			name[:len(filePrefix)] != filePrefix ||
			name[len(name)-len(fileSuffix):] != fileSuffix {
			continue
		}
		path := filepath.Join(s.dir, name)
		info, err := entry.Info()
		if err != nil || info.Size() > maxFileBytes {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var parsed record
		if err := json.Unmarshal(raw, &parsed); err != nil {
			continue
		}
		if parsed.FormatVersion != domainbackup.FormatVersion || !safeID(parsed.ID) ||
			parsed.CreatedAt <= 0 || !parsed.Reason.Valid() || !validFingerprint(parsed.Fingerprint) {
			continue
		}
		records = append(records, storedRecord{record: parsed, path: path})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].record.CreatedAt != records[j].record.CreatedAt {
			return records[i].record.CreatedAt > records[j].record.CreatedAt
		}
		return records[i].record.ID > records[j].record.ID
	})
	return records, nil
}

// List returns every readable backup, newest first. A corrupt file is skipped
// rather than hiding the rest of the history.
func (s *Store) List(ctx context.Context) ([]domainbackup.Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := s.listRecords()
	if err != nil {
		return nil, err
	}
	summaries := make([]domainbackup.Summary, len(records))
	for index, item := range records {
		summaries[index] = item.record.summary()
	}
	return summaries, nil
}

// Read decrypts one backup and verifies its payload against the recorded
// fingerprint.
func (s *Store) Read(ctx context.Context, id string) (domainbackup.Summary, []byte, error) {
	if err := ctx.Err(); err != nil {
		return domainbackup.Summary{}, nil, err
	}
	records, err := s.listRecords()
	if err != nil {
		return domainbackup.Summary{}, nil, err
	}
	var match *storedRecord
	for index := range records {
		if records[index].record.ID == id {
			match = &records[index]
			break
		}
	}
	if match == nil {
		return domainbackup.Summary{}, nil, appbackup.ErrNotFound
	}
	if match.record.PayloadEncoding != payloadCipher {
		return domainbackup.Summary{}, nil, fmt.Errorf("%w: encoding %q", appbackup.ErrCorrupt, match.record.PayloadEncoding)
	}
	key, err := s.encryptionKey()
	if err != nil {
		return domainbackup.Summary{}, nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return domainbackup.Summary{}, nil, appbackup.ErrCorrupt
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return domainbackup.Summary{}, nil, appbackup.ErrCorrupt
	}
	payload, err := aead.Open(nil, match.record.Nonce, match.record.Payload, aad(match.record))
	if err != nil {
		return domainbackup.Summary{}, nil, fmt.Errorf("%w: %v", appbackup.ErrCorrupt, err)
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != match.record.Fingerprint {
		return domainbackup.Summary{}, nil, fmt.Errorf("%w: fingerprint mismatch", appbackup.ErrCorrupt)
	}
	if len(payload) > domainbackup.MaxPayloadBytes {
		return domainbackup.Summary{}, nil, appbackup.ErrPayloadTooLarge
	}
	return match.record.summary(), payload, nil
}

// Prune deletes the oldest backups beyond keep and reports how many were
// removed.
func (s *Store) Prune(ctx context.Context, keep int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	keep = domainbackup.SanitizeRetention(keep)
	records, err := s.listRecords()
	if err != nil {
		return 0, err
	}
	if keep >= len(records) {
		return 0, nil
	}
	deleted := 0
	for _, item := range records[keep:] {
		if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return deleted, fmt.Errorf("vaultbackup: remove %s: %w", item.path, err)
		}
		deleted++
	}
	return deleted, nil
}

// Clear deletes every backup file, including interrupted writes. Unrelated
// files in the directory are preserved.
func (s *Store) Clear(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := s.ensureDir(); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("vaultbackup: read backup directory: %w", err)
	}
	deleted := 0
	for _, entry := range entries {
		name := entry.Name()
		if len(name) <= len(filePrefix) || name[:len(filePrefix)] != filePrefix {
			continue
		}
		backup := (len(name) > len(fileSuffix) && name[len(name)-len(fileSuffix):] == fileSuffix) ||
			containsTemp(name)
		if !backup {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return deleted, fmt.Errorf("vaultbackup: remove %s: %w", name, err)
		}
		deleted++
	}
	return deleted, nil
}

func containsTemp(name string) bool {
	return strings.Contains(name, fileSuffix+".tmp-")
}

// Load reports the persisted retention, or the default when unset.
func (s *Store) Load() int {
	raw, err := os.ReadFile(filepath.Join(s.dir, settingsName))
	if err != nil {
		return domainbackup.DefaultRetention
	}
	var parsed struct {
		FormatVersion int `json:"formatVersion"`
		Retention     int `json:"retention"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil ||
		parsed.FormatVersion != settingsFormat {
		return domainbackup.DefaultRetention
	}
	if parsed.Retention < domainbackup.MinRetention || parsed.Retention > domainbackup.MaxRetention {
		return domainbackup.DefaultRetention
	}
	return parsed.Retention
}

// SaveRetention implements appbackup.RetentionStore.
func (s *Store) SaveRetention(retention int) error {
	applied := domainbackup.SanitizeRetention(retention)
	if err := s.ensureDir(); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		FormatVersion int `json:"formatVersion"`
		Retention     int `json:"retention"`
	}{FormatVersion: settingsFormat, Retention: applied})
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, settingsName)
	tmp, err := os.CreateTemp(s.dir, settingsTemp+"*.tmp")
	if err != nil {
		return fmt.Errorf("vaultbackup: write settings: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("vaultbackup: write settings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("vaultbackup: sync settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("vaultbackup: close settings: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("vaultbackup: rename settings: %w", err)
	}
	return nil
}
