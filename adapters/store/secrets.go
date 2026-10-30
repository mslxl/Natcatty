package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"gorm.io/gorm"

	appvault "github.com/mslxl/natcatty/application/vault"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
)

const (
	// fieldKeyName is the OS-secret entry for the 32-byte field key. The
	// versioned name is part of the on-disk contract.
	fieldKeyName = "vault-data-key-v1"
	// currentKeyVersion is the field key version new ciphertext uses.
	currentKeyVersion = 1
	// secretAlgorithm names the field cipher in secret_values.
	secretAlgorithm = "AES-256-GCM"
	// nonceBytes and tagBytes describe the GCM framing. Go's Seal appends
	// the tag to the ciphertext.
	nonceBytes = 12
	tagBytes   = 16
	// maxSecretValue bounds a field value before encryption.
	maxSecretValue = 64 << 10
)

// Argon2id parameters for wrapping the field key in passphrase mode.
const (
	defaultArgonTime    = 3
	defaultArgonMemory  = 64 * 1024
	defaultArgonThreads = 4
	wrapSaltBytes       = 16
)

// SecretStore is the field-secret adapter: AES-256-GCM per field with the
// key from the OS store, a passphrase-wrapped key, or an in-memory session
// key. It never writes credential plaintext to disk.
type SecretStore struct {
	db   *Store
	keys platformsecrets.Store

	mu      sync.Mutex
	mode    appvault.SecretMode
	key     []byte
	session map[appvault.SecretRef]string

	now   func() time.Time
	read  io.Reader
	argon argonParams
}

type argonParams struct {
	time    uint32
	memory  uint32
	threads uint8
}

// SecretOption customizes a SecretStore.
type SecretOption func(*SecretStore)

// WithSecretRandom sets the nonce and salt source. Tests use it for
// deterministic bytes.
func WithSecretRandom(reader io.Reader) SecretOption {
	return func(s *SecretStore) {
		if reader != nil {
			s.read = reader
		}
	}
}

// WithSecretClock sets the updated_at source.
func WithSecretClock(fn func() time.Time) SecretOption {
	return func(s *SecretStore) {
		if fn != nil {
			s.now = fn
		}
	}
}

// WithArgon2Params lowers the passphrase work factor. Tests use it to stay
// fast; production keeps the documented 64 MiB default.
func WithArgon2Params(iterations, memory uint32, threads uint8) SecretOption {
	return func(s *SecretStore) {
		if iterations > 0 {
			s.argon.time = iterations
		}
		if memory > 0 {
			s.argon.memory = memory
		}
		if threads > 0 {
			s.argon.threads = threads
		}
	}
}

// OpenSecretStore loads or creates the field key. When the OS store is
// available the key lives there. Otherwise the store reports
// passphrase-locked (a wrapped key exists) or unavailable (the user must
// choose passphrase or session-only mode).
func OpenSecretStore(ctx context.Context, db *Store, keys platformsecrets.Store, opts ...SecretOption) (*SecretStore, error) {
	if db == nil {
		return nil, errors.New("store: secret store needs a database")
	}
	if keys == nil {
		return nil, errors.New("store: secret store needs a key store")
	}
	store := &SecretStore{
		db:      db,
		keys:    keys,
		session: map[appvault.SecretRef]string{},
		now:     time.Now,
		read:    rand.Reader,
		argon: argonParams{
			time:    defaultArgonTime,
			memory:  defaultArgonMemory,
			threads: defaultArgonThreads,
		},
	}
	for _, opt := range opts {
		opt(store)
	}
	if err := store.loadKey(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

var _ appvault.FieldSecrets = (*SecretStore)(nil)

func (s *SecretStore) loadKey(ctx context.Context) error {
	key, err := s.keys.Get(fieldKeyName)
	switch {
	case err == nil:
		if len(key) != 32 {
			return fmt.Errorf("store: field key is %d bytes, want 32", len(key))
		}
		s.mu.Lock()
		s.key = append([]byte(nil), key...)
		s.mode = appvault.SecretModeOS
		s.mu.Unlock()
		return nil
	case errors.Is(err, platformsecrets.ErrNotFound):
		// A wrapped key means earlier credentials were encrypted in
		// passphrase mode. Never replace its missing OS key with a fresh
		// one: that would orphan every stored ciphertext.
		if _, found, err := s.db.loadCryptoKey(ctx, "field"); err != nil {
			return err
		} else if found {
			s.mu.Lock()
			s.mode = appvault.SecretModePassphraseLocked
			s.mu.Unlock()
			return nil
		}
		key = make([]byte, 32)
		if _, err := io.ReadFull(s.read, key); err != nil {
			return fmt.Errorf("store: generate field key: %w", err)
		}
		if err := s.keys.Set(fieldKeyName, key); err != nil {
			return fmt.Errorf("store: keep field key: %w", err)
		}
		s.mu.Lock()
		s.key = key
		s.mode = appvault.SecretModeOS
		s.mu.Unlock()
		return nil
	case errors.Is(err, platformsecrets.ErrUnsupported):
		_, found, err := s.db.loadCryptoKey(ctx, "field")
		if err != nil {
			return err
		}
		s.mu.Lock()
		if found {
			s.mode = appvault.SecretModePassphraseLocked
		} else {
			s.mode = appvault.SecretModeUnavailable
		}
		s.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("store: read field key: %w", err)
	}
}

// Mode reports how credentials are protected.
func (s *SecretStore) Mode() appvault.SecretMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// modeError reports why an encrypted operation cannot run. The caller holds
// the mutex.
func (s *SecretStore) modeError() error {
	switch s.mode {
	case appvault.SecretModePassphraseLocked:
		return appvault.ErrSecretsLocked
	case appvault.SecretModeUnavailable:
		return appvault.ErrSecretsUnavailable
	}
	return nil
}

// Set encrypts value and stores it, or keeps it in memory in session-only
// mode.
func (s *SecretStore) Set(ctx context.Context, ref appvault.SecretRef, value string) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	if len(value) > maxSecretValue {
		return fmt.Errorf("store: secret value is %d bytes, more than %d", len(value), maxSecretValue)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == appvault.SecretModeSession {
		s.session[ref] = value
		return nil
	}
	if err := s.modeError(); err != nil {
		return err
	}

	nonce := make([]byte, nonceBytes)
	if _, err := io.ReadFull(s.read, nonce); err != nil {
		return fmt.Errorf("store: generate nonce: %w", err)
	}
	aead, err := s.aead()
	if err != nil {
		return err
	}
	ciphertext := aead.Seal(nil, nonce, []byte(value), aad(ref, currentKeyVersion))
	return s.db.saveSecretValue(ctx, secretValueRow{
		Ref:        ref,
		Alg:        secretAlgorithm,
		KeyVersion: currentKeyVersion,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		UpdatedAt:  s.now(),
	})
}

// Get decrypts one field. A missing value is ErrSecretNotFound; undecryptable
// ciphertext is ErrSecretUnreadable and is left untouched.
func (s *SecretStore) Get(ctx context.Context, ref appvault.SecretRef) (string, error) {
	if err := validateRef(ref); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == appvault.SecretModeSession {
		value, ok := s.session[ref]
		if !ok {
			return "", appvault.ErrSecretNotFound
		}
		return value, nil
	}
	if err := s.modeError(); err != nil {
		return "", err
	}

	row, found, err := s.db.loadSecretValue(ctx, ref)
	if err != nil {
		return "", err
	}
	if !found {
		return "", appvault.ErrSecretNotFound
	}
	if row.Alg != secretAlgorithm || row.KeyVersion != currentKeyVersion ||
		len(row.Nonce) != nonceBytes || len(row.Ciphertext) < tagBytes {
		return "", appvault.ErrSecretUnreadable
	}
	aead, err := s.aead()
	if err != nil {
		return "", err
	}
	plaintext, err := aead.Open(nil, row.Nonce, row.Ciphertext, aad(ref, row.KeyVersion))
	if err != nil {
		return "", appvault.ErrSecretUnreadable
	}
	return string(plaintext), nil
}

// Delete removes one field. Deleting a missing value is not an error.
func (s *SecretStore) Delete(ctx context.Context, ref appvault.SecretRef) error {
	if err := validateRef(ref); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == appvault.SecretModeSession {
		delete(s.session, ref)
		return nil
	}
	if err := s.modeError(); err != nil {
		return err
	}
	return s.db.deleteSecretValue(ctx, ref)
}

// SetPassphrase configures passphrase mode when no OS store exists. It
// generates a new field key and stores only its Argon2id-wrapped form.
func (s *SecretStore) SetPassphrase(ctx context.Context, passphrase string) error {
	if passphrase == "" {
		return appvault.ErrPassphraseRequired
	}

	s.mu.Lock()
	if s.mode != appvault.SecretModeUnavailable {
		s.mu.Unlock()
		return appvault.ErrPassphraseAlreadySet
	}
	s.mu.Unlock()

	if _, found, err := s.db.loadCryptoKey(ctx, "field"); err != nil {
		return err
	} else if found {
		return appvault.ErrPassphraseAlreadySet
	}

	key := make([]byte, 32)
	if _, err := io.ReadFull(s.read, key); err != nil {
		return fmt.Errorf("store: generate field key: %w", err)
	}
	salt := make([]byte, wrapSaltBytes)
	if _, err := io.ReadFull(s.read, salt); err != nil {
		return fmt.Errorf("store: generate wrap salt: %w", err)
	}

	s.mu.Lock()
	params := s.argon
	reader := s.read
	s.mu.Unlock()
	wrapped, nonce, err := wrapKey(key, passphrase, salt, params, reader)
	if err != nil {
		return err
	}
	row := cryptoKeyRow{
		Version:         currentKeyVersion,
		Purpose:         "field",
		CreatedAt:       s.now(),
		WrappedKey:      wrapped,
		WrapNonce:       nonce,
		WrapKDF:         "argon2id",
		WrapSalt:        salt,
		WrapTime:        int(params.time),
		WrapMemory:      int(params.memory),
		WrapParallelism: int(params.threads),
	}
	if err := s.db.saveCryptoKey(ctx, row); err != nil {
		return err
	}

	s.mu.Lock()
	s.key = key
	s.mode = appvault.SecretModePassphrase
	s.mu.Unlock()
	return nil
}

// UnlockWithPassphrase unwraps the stored field key for this run.
func (s *SecretStore) UnlockWithPassphrase(ctx context.Context, passphrase string) error {
	if passphrase == "" {
		return appvault.ErrPassphraseRequired
	}
	row, found, err := s.db.loadCryptoKey(ctx, "field")
	if err != nil {
		return err
	}
	if !found {
		return appvault.ErrSecretsUnavailable
	}
	if row.WrapKDF != "argon2id" || len(row.WrapSalt) < wrapSaltBytes || len(row.WrapNonce) != nonceBytes {
		return appvault.ErrSecretUnreadable
	}
	key, err := unwrapKey(row, passphrase)
	if err != nil {
		return appvault.ErrWrongPassphrase
	}

	s.mu.Lock()
	s.key = key
	s.mode = appvault.SecretModePassphrase
	s.mu.Unlock()
	return nil
}

// UseSessionOnly keeps credentials in memory and persists none of them.
func (s *SecretStore) UseSessionOnly(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode != appvault.SecretModeUnavailable {
		return fmt.Errorf("store: session-only mode needs an unavailable OS store")
	}
	s.mode = appvault.SecretModeSession
	s.session = map[appvault.SecretRef]string{}
	return nil
}

// Close wipes the in-memory field key and session credentials.
func (s *SecretStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	wipe(s.key)
	s.key = nil
	for ref := range s.session {
		delete(s.session, ref)
	}
}

func (s *SecretStore) aead() (cipher.AEAD, error) {
	if len(s.key) != 32 {
		return nil, errors.New("store: field key is unavailable")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, fmt.Errorf("store: field cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("store: field GCM: %w", err)
	}
	return aead, nil
}

func wrapKey(key []byte, passphrase string, salt []byte, params argonParams, reader io.Reader) ([]byte, []byte, error) {
	derived := argon2.IDKey([]byte(passphrase), salt, params.time, params.memory, params.threads, 32)
	defer wipe(derived)
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, nil, fmt.Errorf("store: wrap cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("store: wrap GCM: %w", err)
	}
	nonce := make([]byte, nonceBytes)
	if _, err := io.ReadFull(reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("store: wrap nonce: %w", err)
	}
	wrapped := aead.Seal(nil, nonce, key, wrapAAD(currentKeyVersion))
	return wrapped, nonce, nil
}

func unwrapKey(row cryptoKeyRow, passphrase string) ([]byte, error) {
	if row.WrapNonce == nil || row.WrappedKey == nil || row.WrapSalt == nil {
		return nil, errors.New("store: wrapped key is incomplete")
	}
	derived := argon2.IDKey([]byte(passphrase), row.WrapSalt,
		uint32(row.WrapTime), uint32(row.WrapMemory), uint8(row.WrapParallelism), 32)
	defer wipe(derived)
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	key, err := aead.Open(nil, row.WrapNonce, row.WrappedKey, wrapAAD(row.Version))
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("store: unwrapped field key has the wrong size")
	}
	return key, nil
}

// aad binds a ciphertext to its entity, field, and key version, so rows
// cannot be swapped. The literal format is part of the on-disk contract.
func aad(ref appvault.SecretRef, keyVersion int) []byte {
	return []byte(fmt.Sprintf("natcatty:v1|%s|%s|%s|%d",
		ref.EntityType, ref.EntityID, ref.Field, keyVersion))
}

func wrapAAD(keyVersion int) []byte {
	return []byte(fmt.Sprintf("natcatty:v1|field-key-wrap|%d", keyVersion))
}

func validateRef(ref appvault.SecretRef) error {
	if ref.EntityType == "" || ref.EntityID == "" || ref.Field == "" {
		return errors.New("store: secret reference is incomplete")
	}
	if len(ref.EntityType) > 64 || len(ref.EntityID) > 256 || len(ref.Field) > 64 {
		return errors.New("store: secret reference is too long")
	}
	return nil
}

func wipe(data []byte) {
	for i := range data {
		data[i] = 0
	}
}

// secretValueRow is one secret_values row.
type secretValueRow struct {
	Ref        appvault.SecretRef
	Alg        string
	KeyVersion int
	Nonce      []byte
	Ciphertext []byte
	UpdatedAt  time.Time
}

// cryptoKeyRow is one crypto_keys row.
type cryptoKeyRow struct {
	Version         int
	Purpose         string
	CreatedAt       time.Time
	WrappedKey      []byte
	WrapNonce       []byte
	WrapKDF         string
	WrapSalt        []byte
	WrapTime        int
	WrapMemory      int
	WrapParallelism int
}

func (s *Store) saveSecretValue(ctx context.Context, row secretValueRow) error {
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = time.Now()
	}
	model := secretValueSchema{
		EntityType: row.Ref.EntityType, EntityID: row.Ref.EntityID, Field: row.Ref.Field,
		Alg: row.Alg, KeyVersion: int64(row.KeyVersion), Nonce: row.Nonce,
		Ciphertext: row.Ciphertext, UpdatedAt: row.UpdatedAt.UnixMilli(),
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModelWithConflicts(tx, &model, []string{"entity_type", "entity_id", "field"}, "alg", "key_version", "nonce", "ciphertext", "updated_at")
	}); err != nil {
		return fmt.Errorf("store: save secret: %w", err)
	}
	return nil
}

func (s *Store) loadSecretValue(ctx context.Context, ref appvault.SecretRef) (secretValueRow, bool, error) {
	var model secretValueSchema
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return tx.Where(&secretValueSchema{EntityType: ref.EntityType, EntityID: ref.EntityID, Field: ref.Field}).First(&model).Error
	}); err != nil {
		if err == gorm.ErrRecordNotFound {
			return secretValueRow{}, false, nil
		}
		return secretValueRow{}, false, fmt.Errorf("store: read secret: %w", err)
	}
	return secretValueRow{
		Ref: ref, Alg: model.Alg, KeyVersion: int(model.KeyVersion), Nonce: model.Nonce,
		Ciphertext: model.Ciphertext, UpdatedAt: time.UnixMilli(model.UpdatedAt),
	}, true, nil
}

func (s *Store) deleteSecretValue(ctx context.Context, ref appvault.SecretRef) error {
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		_, err := deleteModel(tx, &secretValueSchema{}, &secretValueSchema{EntityType: ref.EntityType, EntityID: ref.EntityID, Field: ref.Field})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete secret: %w", err)
	}
	return nil
}

func (s *Store) saveCryptoKey(ctx context.Context, row cryptoKeyRow) error {
	model := cryptoKeySchema{
		Version: int64(row.Version), Purpose: row.Purpose, CreatedAt: row.CreatedAt.UnixMilli(),
		WrappedKey: row.WrappedKey, WrapNonce: row.WrapNonce, WrapKDF: row.WrapKDF,
		WrapSalt: row.WrapSalt, WrapTime: int64Ptr(row.WrapTime), WrapMemory: int64Ptr(row.WrapMemory),
		WrapParallelism: int64Ptr(row.WrapParallelism),
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "version", "purpose", "created_at", "wrapped_key", "wrap_nonce", "wrap_kdf", "wrap_salt", "wrap_time", "wrap_memory", "wrap_parallelism")
	}); err != nil {
		return fmt.Errorf("store: save crypto key: %w", err)
	}
	return nil
}

func (s *Store) loadCryptoKey(ctx context.Context, purpose string) (cryptoKeyRow, bool, error) {
	var model cryptoKeySchema
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return tx.Where(&cryptoKeySchema{Purpose: purpose}).Order("version DESC").First(&model).Error
	}); err != nil {
		if err == gorm.ErrRecordNotFound {
			return cryptoKeyRow{}, false, nil
		}
		return cryptoKeyRow{}, false, fmt.Errorf("store: read crypto key: %w", err)
	}
	return cryptoKeyRow{
		Version: int(model.Version), Purpose: model.Purpose, CreatedAt: time.UnixMilli(model.CreatedAt),
		WrappedKey: model.WrappedKey, WrapNonce: model.WrapNonce, WrapKDF: model.WrapKDF,
		WrapSalt: model.WrapSalt, WrapTime: intPtr(model.WrapTime), WrapMemory: intPtr(model.WrapMemory),
		WrapParallelism: intPtr(model.WrapParallelism),
	}, true, nil
}

func int64Ptr(value int) *int64 {
	converted := int64(value)
	return &converted
}

func intPtr(value *int64) int {
	if value == nil {
		return 0
	}
	return int(*value)
}
