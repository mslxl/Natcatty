package store

import (
	"crypto/cipher"
	"errors"
	"testing"
)

// recordingAEAD captures the plaintext buffers handed to Seal and Open so a
// test can prove the adapter zeroed them. It never performs real encryption.
type recordingAEAD struct {
	sealed   []byte
	lastOpen []byte
	openErr  error
}

var _ cipher.AEAD = (*recordingAEAD)(nil)

func (a *recordingAEAD) NonceSize() int { return nonceBytes }

func (a *recordingAEAD) Overhead() int { return tagBytes }

func (a *recordingAEAD) Seal(dst, nonce, plaintext, additionalData []byte) []byte {
	a.sealed = plaintext
	return append(dst, []byte("sealed")...)
}

func (a *recordingAEAD) Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error) {
	if a.openErr != nil {
		return nil, a.openErr
	}
	a.lastOpen = []byte("hunter2")
	return a.lastOpen, nil
}

func TestSealSecretValueWipesTheTransientPlaintext(t *testing.T) {
	aead := &recordingAEAD{}
	if got := string(sealSecretValue(aead, nil, "hunter2", nil)); got != "sealed" {
		t.Fatalf("sealSecretValue = %q, want the ciphertext", got)
	}
	if len(aead.sealed) != len("hunter2") {
		t.Fatalf("Seal saw %d plaintext bytes, want %d", len(aead.sealed), len("hunter2"))
	}
	for index, value := range aead.sealed {
		if value != 0 {
			t.Fatalf("plaintext byte %d = %d after seal, want zero", index, value)
		}
	}
}

func TestOpenSecretValueWipesTheDecryptedPlaintext(t *testing.T) {
	aead := &recordingAEAD{}
	value, err := openSecretValue(aead, nil, nil, nil)
	if err != nil || value != "hunter2" {
		// Print the length, not the decrypted value.
		t.Fatalf("openSecretValue returned %d bytes, err %v; want hunter2", len(value), err)
	}
	if len(aead.lastOpen) != len("hunter2") {
		t.Fatalf("Open returned %d bytes, want %d", len(aead.lastOpen), len("hunter2"))
	}
	for index, value := range aead.lastOpen {
		if value != 0 {
			t.Fatalf("decrypted byte %d = %d after open, want zero", index, value)
		}
	}
}

func TestOpenSecretValueReportsAnAuthFailure(t *testing.T) {
	failure := errors.New("auth failure")
	aead := &recordingAEAD{openErr: failure}
	if value, err := openSecretValue(aead, nil, nil, nil); !errors.Is(err, failure) || value != "" {
		// Print the length, not any plaintext the failure path returned.
		t.Fatalf("openSecretValue returned %d bytes, err %v; want the failure and an empty value", len(value), err)
	}
}
