package state

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"sync"

	"github.com/mslxl/natcatty/domain/model"
)

// paneSecrets keeps a Quick Connect Pane's one-shot credential in memory for
// the Session's life. The credential is the only way a retry or a manual
// reconnect can reopen the Session after its first dial fails: it is never
// persisted in the Workspace descriptor or the Vault, and it is dropped when
// the Pane closes.
//
// Values are AES-256-GCM sealed under a random per-run key, so a memory dump
// or a careless debug print of the map never exposes plaintext. The key is
// process-wide and never leaves memory; the nonce is random per value.
//
// A failure to create the store is not fatal to the shell: NewState leaves it
// nil, which downgrades a Quick Connect retry to a redial without the one-shot
// credential — the same behavior as before the store existed.
type paneSecrets struct {
	mu   sync.Mutex
	key  []byte
	aead cipher.AEAD
	// values maps a Pane identity onto nonce||ciphertext.
	values map[model.PaneID][]byte
}

func newPaneSecrets() (*paneSecrets, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("shell: generate pane secret key: %w", err)
	}
	aead, err := paneSecretsAEAD(key)
	if err != nil {
		return nil, err
	}
	return &paneSecrets{key: key, aead: aead, values: map[model.PaneID][]byte{}}, nil
}

func paneSecretsAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("shell: pane secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("shell: pane secret GCM: %w", err)
	}
	return aead, nil
}

// set seals and stores value for paneID. An empty value drops the entry, so a
// Quick Connect without a password never holds one.
func (s *paneSecrets) set(paneID model.PaneID, value string) error {
	if s == nil || paneID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if value == "" {
		s.dropLocked(paneID)
		return nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("shell: generate pane secret nonce: %w", err)
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(value), []byte(paneID))
	s.dropLocked(paneID)
	s.values[paneID] = sealed
	return nil
}

// get opens the Pane's stored credential. A missing entry or ciphertext that
// fails authentication reports false; nothing is invented in its place.
func (s *paneSecrets) get(paneID model.PaneID) (string, bool) {
	if s == nil || paneID == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sealed, ok := s.values[paneID]
	if !ok {
		return "", false
	}
	if len(sealed) < s.aead.NonceSize()+s.aead.Overhead() {
		return "", false
	}
	nonce := sealed[:s.aead.NonceSize()]
	plaintext, err := s.aead.Open(nil, nonce, sealed[s.aead.NonceSize():], []byte(paneID))
	if err != nil {
		return "", false
	}
	return string(plaintext), true
}

// drop removes one Pane's credential, wiping the ciphertext bytes.
func (s *paneSecrets) drop(paneID model.PaneID) {
	if s == nil || paneID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropLocked(paneID)
}

// dropLocked removes one entry. The caller holds the mutex.
func (s *paneSecrets) dropLocked(paneID model.PaneID) {
	if sealed, ok := s.values[paneID]; ok {
		for i := range sealed {
			sealed[i] = 0
		}
		delete(s.values, paneID)
	}
}

// wipe destroys the key and every stored credential. Called when the shell
// shuts down; afterwards no value can be opened.
func (s *paneSecrets) wipe() {
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

