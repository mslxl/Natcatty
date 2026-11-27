package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"sync"
)

// MemCredentialStore keeps a Quick Connect Pane's one-shot credential in
// memory for the Session's life. The credential is the only way a retry or a
// manual reconnect can reopen the Session after its first dial fails: it is
// never persisted in the Workspace descriptor or the Vault, and it is dropped
// when the Pane closes. The desktop and the phone shell both use it, so a
// one-shot password is protected the same way on every platform.
//
// Values are AES-256-GCM sealed under a random per-run key, so a memory dump
// or a careless debug print of the map never exposes plaintext. The key is
// process-wide and never leaves memory; the nonce is random per value.
//
// A failure to create the store is not fatal to a shell: it leaves the store
// nil, which downgrades a Quick Connect retry to a redial without the one-shot
// credential — the same behavior as before the store existed.
type MemCredentialStore struct {
	mu   sync.Mutex
	key  []byte
	aead cipher.AEAD
	// values maps an opaque owner identity (a Pane id) onto nonce||ciphertext.
	values map[string][]byte
}

// NewMemCredentialStore creates the store with a fresh random key.
func NewMemCredentialStore() (*MemCredentialStore, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("session: generate in-memory credential key: %w", err)
	}
	aead, err := memCredentialAEAD(key)
	if err != nil {
		return nil, err
	}
	return &MemCredentialStore{key: key, aead: aead, values: map[string][]byte{}}, nil
}

func memCredentialAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("session: in-memory credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("session: in-memory credential GCM: %w", err)
	}
	return aead, nil
}

// Set seals and stores value for owner. An empty value drops the entry, so a
// Quick Connect without a password never holds one.
func (s *MemCredentialStore) Set(owner, value string) error {
	if s == nil || owner == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if value == "" {
		s.dropLocked(owner)
		return nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("session: generate in-memory credential nonce: %w", err)
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(value), []byte(owner))
	s.dropLocked(owner)
	s.values[owner] = sealed
	return nil
}

// Get opens the owner's stored credential. A missing entry or ciphertext that
// fails authentication reports false; nothing is invented in its place.
func (s *MemCredentialStore) Get(owner string) (string, bool) {
	if s == nil || owner == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sealed, ok := s.values[owner]
	if !ok {
		return "", false
	}
	if len(sealed) < s.aead.NonceSize()+s.aead.Overhead() {
		return "", false
	}
	nonce := sealed[:s.aead.NonceSize()]
	plaintext, err := s.aead.Open(nil, nonce, sealed[s.aead.NonceSize():], []byte(owner))
	if err != nil {
		return "", false
	}
	return string(plaintext), true
}

// Drop removes one owner's credential, wiping the ciphertext bytes.
func (s *MemCredentialStore) Drop(owner string) {
	if s == nil || owner == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropLocked(owner)
}

// dropLocked removes one entry. The caller holds the mutex.
func (s *MemCredentialStore) dropLocked(owner string) {
	if sealed, ok := s.values[owner]; ok {
		for i := range sealed {
			sealed[i] = 0
		}
		delete(s.values, owner)
	}
}

// Wipe destroys the key and every stored credential. Called when the shell
// shuts down; afterwards no value can be opened.
func (s *MemCredentialStore) Wipe() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.values {
		s.dropLocked(id)
	}
	if s.key != nil {
		for i := range s.key {
			s.key[i] = 0
		}
	}
}
