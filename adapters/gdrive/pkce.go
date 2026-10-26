package gdrive

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// PKCE is one authorization attempt's proof key and anti-forgery state.
// Google requires the S256 challenge for a loopback redirect.
type PKCE struct {
	Verifier  string
	Challenge string
	State     string
}

// NewPKCE builds a verifier from 32 random bytes (43 base64url characters),
// its S256 challenge, and a 16-byte state. It reads from random; nil uses the
// process CSPRNG.
func NewPKCE(random io.Reader) (PKCE, error) {
	if random == nil {
		random = rand.Reader
	}
	verifier, err := randomToken(random, 32)
	if err != nil {
		return PKCE{}, fmt.Errorf("gdrive: generate the PKCE verifier: %w", err)
	}
	state, err := randomToken(random, 16)
	if err != nil {
		return PKCE{}, fmt.Errorf("gdrive: generate the authorization state: %w", err)
	}
	return PKCE{Verifier: verifier, Challenge: challengeFor(verifier), State: state}, nil
}

func randomToken(random io.Reader, size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := io.ReadFull(random, buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
