// Package applock holds App Lock's pure domain model: the password verifier
// and the timeout rules. App Lock is a UI gate, not database encryption; it
// imports only the standard library.
package applock

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
)

// The verifier format matches the original Netcatty app-lock record.
const (
	// VerifierVersion is the only verifier format this app writes.
	VerifierVersion = 1
	// Algorithm names the key derivation function.
	Algorithm = "PBKDF2-SHA256"
	// Iterations is the work factor of a new verifier.
	Iterations = 210000
	// MinIterations is the lowest work factor a stored verifier may use.
	MinIterations = 100000
	// SaltBytes is the verifier salt length.
	SaltBytes = 16
	// HashBytes is the derived hash length.
	HashBytes = 32
	// DefaultTimeoutMinutes is the idle timeout until the user chooses one.
	DefaultTimeoutMinutes = 15
)

// TimeoutOptions are the idle timeouts the UI offers, in minutes. Zero means
// never lock automatically; startup and manual locks still apply.
var TimeoutOptions = []int{0, 1, 5, 15, 30, 60}

// ErrPasswordRequired reports an empty App Lock password.
var ErrPasswordRequired = errors.New("applock: password required")

// Verifier is the versioned record that can check a password without storing
// it. Salt and Hash are base64 in JSON, as in the original format.
type Verifier struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
	Hash       []byte `json:"hash"`
}

// Settings is the App Lock configuration. System unlock flags are kept for
// format compatibility; MyGo exposes no biometric API, so the app never
// claims they work.
type Settings struct {
	Enabled                       bool      `json:"enabled"`
	TimeoutMinutes                int       `json:"timeoutMinutes"`
	SystemUnlockEnabled           bool      `json:"systemUnlockEnabled"`
	SystemUnlockAutoPromptEnabled bool      `json:"systemUnlockAutoPromptEnabled"`
	PasswordVerifier              *Verifier `json:"passwordVerifier"`
}

// DefaultSettings returns App Lock disabled with the default timeout.
func DefaultSettings() Settings {
	return Settings{TimeoutMinutes: DefaultTimeoutMinutes}
}

// NormalizeTimeoutMinutes maps an unsupported timeout to the default. A
// string that parses to one of the offered numbers is accepted by callers
// that decode form input.
func NormalizeTimeoutMinutes(minutes int) int {
	for _, option := range TimeoutOptions {
		if minutes == option {
			return minutes
		}
	}
	return DefaultTimeoutMinutes
}

// NormalizeSettings validates a stored record. App Lock can only be enabled
// with a verifier the app understands, and system unlock only with App Lock.
func NormalizeSettings(settings Settings) Settings {
	normalized := Settings{
		TimeoutMinutes:   NormalizeTimeoutMinutes(settings.TimeoutMinutes),
		PasswordVerifier: NormalizeVerifier(settings.PasswordVerifier),
	}
	normalized.Enabled = settings.Enabled && normalized.PasswordVerifier != nil
	normalized.SystemUnlockEnabled = settings.SystemUnlockEnabled && normalized.Enabled
	normalized.SystemUnlockAutoPromptEnabled = settings.SystemUnlockAutoPromptEnabled && normalized.SystemUnlockEnabled
	return normalized
}

// NormalizeVerifier returns verifier when every field is valid, else nil.
func NormalizeVerifier(verifier *Verifier) *Verifier {
	if verifier == nil {
		return nil
	}
	if verifier.Version != VerifierVersion {
		return nil
	}
	if verifier.Algorithm != Algorithm {
		return nil
	}
	if verifier.Iterations < MinIterations {
		return nil
	}
	if len(verifier.Salt) != SaltBytes || len(verifier.Hash) != HashBytes {
		return nil
	}
	return &Verifier{
		Version:    verifier.Version,
		Algorithm:  verifier.Algorithm,
		Iterations: verifier.Iterations,
		Salt:       append([]byte(nil), verifier.Salt...),
		Hash:       append([]byte(nil), verifier.Hash...),
	}
}

// CanLock reports whether App Lock can engage: it is enabled with a valid
// verifier.
func (settings Settings) CanLock() bool {
	normalized := NormalizeSettings(settings)
	return normalized.Enabled && normalized.PasswordVerifier != nil
}

// ShouldLockAfterIdle reports whether an idle period of elapsedSeconds
// minutes' worth of seconds closes the gate. A zero timeout never locks
// automatically.
func (settings Settings) ShouldLockAfterIdle(elapsedSeconds int64) bool {
	normalized := NormalizeSettings(settings)
	if !normalized.CanLock() || normalized.TimeoutMinutes <= 0 {
		return false
	}
	return elapsedSeconds >= int64(normalized.TimeoutMinutes)*60
}

// NewVerifier hashes password with a fresh salt read from random. It returns
// ErrPasswordRequired for an empty password.
func NewVerifier(password string, random io.Reader) (*Verifier, error) {
	if password == "" {
		return nil, ErrPasswordRequired
	}
	if random == nil {
		random = rand.Reader
	}
	salt := make([]byte, SaltBytes)
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, err
	}
	hash, err := derive(password, salt, Iterations)
	if err != nil {
		return nil, err
	}
	return &Verifier{
		Version:    VerifierVersion,
		Algorithm:  Algorithm,
		Iterations: Iterations,
		Salt:       salt,
		Hash:       hash,
	}, nil
}

// Verify reports whether password matches the stored verifier in constant
// time. A malformed verifier never matches.
func (verifier *Verifier) Verify(password string) bool {
	normalized := NormalizeVerifier(verifier)
	if normalized == nil || password == "" {
		return false
	}
	candidate, err := derive(password, normalized.Salt, normalized.Iterations)
	if err != nil || len(candidate) != len(normalized.Hash) {
		return false
	}
	return subtle.ConstantTimeCompare(candidate, normalized.Hash) == 1
}

func derive(password string, salt []byte, iterations int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, iterations, HashBytes)
}
