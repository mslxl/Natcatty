// Package sync holds the cloud-sync wire format and the pure rules that decide
// which side of a sync wins. The envelope is a compatibility contract with the
// original Electron app: netcatty-vault.json is a plaintext meta object beside
// an AES-256-GCM payload under a PBKDF2-SHA-256 key. This package imports only
// the standard library so both the application layer and adapters can share it.
package sync

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// The original's envelope constants. Changing any of them breaks decryption of
// files already published by existing installs (ADR-0011).
const (
	// SyncFileName is the Gist file that carries the envelope.
	SyncFileName = "netcatty-vault.json"
	// GistDescription identifies Netcatty's own Gist in the account.
	GistDescription = "Netcatty Encrypted Vault (DO NOT EDIT MANUALLY)"

	// KeyBytes is the derived AES-256 key length.
	KeyBytes = 32
	// SaltBytes is the per-file PBKDF2 salt length.
	SaltBytes = 32
	// IVBytes is the AES-GCM nonce length.
	IVBytes = 12
	// TagBytes is the AES-GCM authentication tag length.
	TagBytes = 16
	// KDFIterations is the original's PBKDF2 work factor.
	KDFIterations = 600000

	// AlgorithmAESGCM and KDFPBKDF2 are the only envelope identifiers this
	// build writes or accepts.
	AlgorithmAESGCM = "AES-256-GCM"
	KDFPBKDF2       = "PBKDF2"
)

// Errors callers localize or map onto a status.
var (
	// ErrCorrupt reports an envelope whose fields do not describe a valid
	// PBKDF2/AES-256-GCM file.
	ErrCorrupt = errors.New("sync: corrupt envelope")
	// ErrWrongPassword reports ciphertext that does not authenticate under the
	// supplied password.
	ErrWrongPassword = errors.New("sync: incorrect master password")
	// ErrPasswordRequired reports an empty master password.
	ErrPasswordRequired = errors.New("sync: master password required")
)

// Meta is the cleartext part of a sync file. Field order matches the original
// Electron writer, and the JSON must not omit deviceName even when it is empty.
type Meta struct {
	Version       int    `json:"version"`
	UpdatedAt     int64  `json:"updatedAt"`
	DeviceID      string `json:"deviceId"`
	DeviceName    string `json:"deviceName"`
	AppVersion    string `json:"appVersion"`
	IV            string `json:"iv"`
	Salt          string `json:"salt"`
	Algorithm     string `json:"algorithm"`
	KDF           string `json:"kdf"`
	KDFIterations int    `json:"kdfIterations"`
	// SyncSchemaVersion is present only for the original's convergent v2
	// payload. This build writes v1 snapshots.
	SyncSchemaVersion int `json:"syncSchemaVersion,omitempty"`
}

// File is one netcatty-vault.json document.
type File struct {
	Meta    Meta   `json:"meta"`
	Payload string `json:"payload"`
}

// EncryptOptions carries the metadata and deterministic test seams for
// EncryptPayload. Production callers leave Salt and IV empty so a fresh random
// value is generated for every file, exactly as the original did.
type EncryptOptions struct {
	Version    int
	UpdatedAt  int64
	DeviceID   string
	DeviceName string
	AppVersion string
	Random     io.Reader
	Now        func() time.Time
	Salt       []byte
	IV         []byte
	// SyncSchemaVersion is written only when non-zero.
	SyncSchemaVersion int
}

// EncryptPayload serializes payload as compact JSON and seals it into the
// original envelope. It never authenticates meta: the original kept metadata
// outside GCM, and the compatibility contract preserves that weakness.
func EncryptPayload(payload any, password string, opts EncryptOptions) (File, error) {
	if password == "" {
		return File{}, ErrPasswordRequired
	}
	plaintext, err := MarshalJSON(payload)
	if err != nil {
		return File{}, err
	}
	return EncryptBytes(plaintext, password, opts)
}

// EncryptBytes seals already-serialized plaintext. Tests and the ticket
// self-test use it to reproduce the original compatibility vector exactly.
func EncryptBytes(plaintext []byte, password string, opts EncryptOptions) (File, error) {
	if password == "" {
		return File{}, ErrPasswordRequired
	}
	random := opts.Random
	if random == nil {
		random = rand.Reader
	}
	salt := opts.Salt
	if salt == nil {
		salt = make([]byte, SaltBytes)
		if _, err := io.ReadFull(random, salt); err != nil {
			return File{}, fmt.Errorf("sync: generate salt: %w", err)
		}
	}
	if len(salt) != SaltBytes {
		return File{}, fmt.Errorf("%w: salt is %d bytes, want %d", ErrCorrupt, len(salt), SaltBytes)
	}
	iv := opts.IV
	if iv == nil {
		iv = make([]byte, IVBytes)
		if _, err := io.ReadFull(random, iv); err != nil {
			return File{}, fmt.Errorf("sync: generate iv: %w", err)
		}
	}
	if len(iv) != IVBytes {
		return File{}, fmt.Errorf("%w: iv is %d bytes, want %d", ErrCorrupt, len(iv), IVBytes)
	}

	key, err := DeriveKey(password, salt, KDFIterations)
	if err != nil {
		return File{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return File{}, fmt.Errorf("sync: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return File{}, fmt.Errorf("sync: gcm: %w", err)
	}
	sealed := aead.Seal(nil, iv, plaintext, nil)

	updatedAt := opts.UpdatedAt
	if updatedAt == 0 {
		now := time.Now
		if opts.Now != nil {
			now = opts.Now
		}
		updatedAt = now().UnixMilli()
	}
	meta := Meta{
		Version:       opts.Version,
		UpdatedAt:     updatedAt,
		DeviceID:      opts.DeviceID,
		DeviceName:    opts.DeviceName,
		AppVersion:    opts.AppVersion,
		IV:            base64.StdEncoding.EncodeToString(iv),
		Salt:          base64.StdEncoding.EncodeToString(salt),
		Algorithm:     AlgorithmAESGCM,
		KDF:           KDFPBKDF2,
		KDFIterations: KDFIterations,
	}
	if opts.SyncSchemaVersion != 0 {
		meta.SyncSchemaVersion = opts.SyncSchemaVersion
	}
	return File{Meta: meta, Payload: base64.StdEncoding.EncodeToString(sealed)}, nil
}

// DecryptPayload opens an envelope and returns its plaintext. A malformed
// envelope is ErrCorrupt; authenticated decryption failure is ErrWrongPassword.
func DecryptPayload(file File, password string) ([]byte, error) {
	if password == "" {
		return nil, ErrPasswordRequired
	}
	meta := file.Meta
	if meta.Algorithm != AlgorithmAESGCM || meta.KDF != KDFPBKDF2 {
		return nil, fmt.Errorf("%w: algorithm %q kdf %q", ErrCorrupt, meta.Algorithm, meta.KDF)
	}
	salt, err := base64.StdEncoding.DecodeString(meta.Salt)
	if err != nil || len(salt) != SaltBytes {
		return nil, fmt.Errorf("%w: salt", ErrCorrupt)
	}
	iv, err := base64.StdEncoding.DecodeString(meta.IV)
	if err != nil || len(iv) != IVBytes {
		return nil, fmt.Errorf("%w: iv", ErrCorrupt)
	}
	sealed, err := base64.StdEncoding.DecodeString(file.Payload)
	if err != nil || len(sealed) < TagBytes {
		return nil, fmt.Errorf("%w: payload", ErrCorrupt)
	}
	iterations := meta.KDFIterations
	if iterations <= 0 {
		// The original used `meta.kdfIterations || 600000`.
		iterations = KDFIterations
	}
	key, err := DeriveKey(password, salt, iterations)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: cipher: %v", ErrCorrupt, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: gcm: %v", ErrCorrupt, err)
	}
	plaintext, err := aead.Open(nil, iv, sealed, nil)
	if err != nil {
		return nil, ErrWrongPassword
	}
	return plaintext, nil
}

// DeriveKey derives the 32-byte AES key with PBKDF2-HMAC-SHA-256.
func DeriveKey(password string, salt []byte, iterations int) ([]byte, error) {
	if iterations <= 0 {
		iterations = KDFIterations
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, KeyBytes)
	if err != nil {
		return nil, fmt.Errorf("sync: derive key: %w", err)
	}
	return key, nil
}

// MarshalJSON encodes v the way JSON.stringify does: compact, field order as
// declared, and no HTML escaping. The original wrote this exact byte string
// into the ciphertext, so escaping <, >, and & would change the plaintext.
func MarshalJSON(v any) ([]byte, error) {
	return encodeJSON(v, "")
}

// encodeJSON is the shared JSON.stringify-shaped encoder. indent is the second
// argument JSON.stringify would receive.
func encodeJSON(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if indent != "" {
		encoder.SetIndent("", indent)
	}
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return unescapeLineSeparators(data), nil
}

// unescapeLineSeparators rewrites encoding/json's mandatory   and
// escapes into the literal UTF-8 characters. JSON.stringify leaves them raw, so
// the original's plaintext contains those bytes; Go always escapes them. An
// escape preceded by an odd run of backslashes is a real JSON escape; an even
// run means the backslash itself was escaped, so the text is literal.
func unescapeLineSeparators(data []byte) []byte {
	var out []byte
	for index := 0; index < len(data); {
		if data[index] == '\\' && index+6 <= len(data) &&
			data[index+1] == 'u' && data[index+2] == '2' && data[index+3] == '0' &&
			(data[index+4] == '2' && (data[index+5] == '8' || data[index+5] == '9')) {
			backslashes := 0
			for cursor := index - 1; cursor >= 0 && data[cursor] == '\\'; cursor-- {
				backslashes++
			}
			if backslashes%2 == 0 {
				if data[index+5] == '8' {
					out = append(out, 0xe2, 0x80, 0xa8)
				} else {
					out = append(out, 0xe2, 0x80, 0xa9)
				}
				index += 6
				continue
			}
		}
		out = append(out, data[index])
		index++
	}
	return out
}

// MarshalFile renders the pretty envelope the original uploaded:
// JSON.stringify(file, null, 2) with no trailing newline.
func MarshalFile(file File) ([]byte, error) {
	return encodeJSON(file, "  ")
}

// UnmarshalFile parses an envelope. Unknown fields are accepted so a newer
// writer can add metadata without breaking older readers.
func UnmarshalFile(data []byte) (File, error) {
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return File{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if file.Meta.Algorithm == "" || file.Meta.KDF == "" || file.Payload == "" {
		return File{}, fmt.Errorf("%w: missing envelope fields", ErrCorrupt)
	}
	return file, nil
}

// MasterKeyConfig is the original's stored password verifier. It never holds
// the password or the derived key.
type MasterKeyConfig struct {
	VerificationHash string `json:"verificationHash"`
	Salt             string `json:"salt"`
	KDF              string `json:"kdf"`
	KDFIterations    int    `json:"kdfIterations"`
	CreatedAt        int64  `json:"createdAt"`
}

// NewMasterKeyConfig derives a verifier for password. The salt is fresh on
// every call, so changing the password is a new record, as in the original.
func NewMasterKeyConfig(password string, random io.Reader, now time.Time) (MasterKeyConfig, error) {
	if password == "" {
		return MasterKeyConfig{}, ErrPasswordRequired
	}
	if random == nil {
		random = rand.Reader
	}
	salt := make([]byte, SaltBytes)
	if _, err := io.ReadFull(random, salt); err != nil {
		return MasterKeyConfig{}, fmt.Errorf("sync: generate master salt: %w", err)
	}
	key, err := DeriveKey(password, salt, KDFIterations)
	if err != nil {
		return MasterKeyConfig{}, err
	}
	sum := sha256.Sum256(key)
	return MasterKeyConfig{
		VerificationHash: base64.StdEncoding.EncodeToString(sum[:]),
		Salt:             base64.StdEncoding.EncodeToString(salt),
		KDF:              KDFPBKDF2,
		KDFIterations:    KDFIterations,
		CreatedAt:        now.UnixMilli(),
	}, nil
}

// Valid reports whether the record has the fields this build understands.
func (c MasterKeyConfig) Valid() bool {
	if c.KDF != KDFPBKDF2 || c.KDFIterations <= 0 || c.VerificationHash == "" {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(c.Salt)
	return err == nil && len(salt) == SaltBytes
}

// Verify reports whether password matches the stored verifier in constant
// time.
func (c MasterKeyConfig) Verify(password string) bool {
	if password == "" || !c.Valid() {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(c.Salt)
	if err != nil {
		return false
	}
	key, err := DeriveKey(password, salt, c.KDFIterations)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(key)
	expected, err := base64.StdEncoding.DecodeString(c.VerificationHash)
	if err != nil || len(expected) != len(sum) {
		return false
	}
	return subtle.ConstantTimeCompare(sum[:], expected) == 1
}

// Token is one provider credential. GitHub OAuth tokens normally do not
// expire, but the shape keeps a refresh token and expiry for providers and
// GitHub App user tokens that do.
type Token struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	TokenType    string `json:"tokenType,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ExpiresAt    int64  `json:"expiresAt,omitempty"`
	// Provider binds the credential to the provider that issued it, so a
	// stored GitHub token can never be sent to Google Drive or vice versa.
	Provider string `json:"provider,omitempty"`
}
