package wire

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash"
	"strings"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20"
)

// Wire constants from the pinned Atuin client. These prefixes are part of the
// protocol and must never change.
const (
	pasetoHeader = "v4.local."

	paserkWrapPrefix  = "k4.local-wrap.pie."
	paserkKeyIDPrefix = "k4.lid."

	pasetoEncryptionKeyContext     = "paseto-encryption-key"
	pasetoAuthenticationKeyContext = "paseto-auth-key-for-aead"

	paserkVersionHeader = "k4."
	paserkWrapHeader    = "local-wrap."
	paserkOperation     = "pie."
	paserkKeyIDHeader   = "lid."
	paserkLocalHeader   = "local."
)

var base64URL = base64.RawURLEncoding.Strict()

// Key is a 32-byte Atuin master key or content encryption key. It is an array
// so copying it yields an independent value; callers that hold a master key
// should still Wipe it when the operation ends.
type Key [32]byte

// KeyFromBytes returns a key copied from bytes. It rejects any length other
// than 32 bytes.
func KeyFromBytes(bytes []byte) (Key, error) {
	var key Key
	if len(bytes) != len(key) {
		return key, fmt.Errorf("%w: key must be 32 bytes", ErrMalformed)
	}
	copy(key[:], bytes)
	return key, nil
}

// NewRandomKey returns a fresh random key from the OS CSPRNG. It is used for
// per-record content encryption keys and PASETO nonces.
func NewRandomKey() (Key, error) {
	var key Key
	if _, err := rand.Read(key[:]); err != nil {
		return key, err
	}
	return key, nil
}

// Bytes returns a copy of the key material.
func (key Key) Bytes() []byte {
	out := make([]byte, len(key))
	copy(out, key[:])
	return out
}

// Wipe zeroes the key in place.
func (key *Key) Wipe() {
	for index := range key {
		key[index] = 0
	}
}

// KeyID returns the PASERK key id for a local v4 key: BLAKE2b-264 over
// "k4.lid.k4.local." and the base64url key, encoded as "k4.lid.<b64>".
func KeyID(key Key) string {
	encoded := base64URL.EncodeToString(key[:])
	digest := blake2bSum(33, nil, []byte(paserkVersionHeader+paserkKeyIDHeader+
		paserkVersionHeader+paserkLocalHeader+encoded))
	return paserkKeyIDPrefix + base64URL.EncodeToString(digest)
}

// WrapPIE wraps plaintext with wrapping using PASERK k4.local-wrap.pie. The
// result is the full "k4.local-wrap.pie.<base64url>" string.
func WrapPIE(plaintext, wrapping Key) (string, error) {
	nonce, err := NewRandomKey()
	if err != nil {
		return "", err
	}
	return wrapPIEWithNonce(plaintext, wrapping, nonce)
}

func wrapPIEWithNonce(plaintext, wrapping Key, nonce Key) (string, error) {
	encryptionKey, counterNonce := pieEncryptionKey(wrapping, nonce)
	authenticationKey := pieAuthenticationKey(wrapping, nonce)

	cipher, err := chacha20.NewUnauthenticatedCipher(encryptionKey[:], counterNonce[:])
	if err != nil {
		return "", err
	}
	wrapped := make([]byte, len(plaintext))
	cipher.XORKeyStream(wrapped, plaintext[:])

	tag := blake2bSum(32, authenticationKey[:], pieTagInput(nonce, wrapped))
	payload := make([]byte, 0, len(tag)+len(nonce)+len(wrapped))
	payload = append(payload, tag...)
	payload = append(payload, nonce[:]...)
	payload = append(payload, wrapped...)
	return paserkWrapPrefix + base64URL.EncodeToString(payload), nil
}

// UnwrapPIE authenticates and unwraps a k4.local-wrap.pie string with
// wrapping. A tampered or wrong-key envelope returns ErrKeyMismatch.
func UnwrapPIE(wrapped string, wrapping Key) (Key, error) {
	var key Key
	if !strings.HasPrefix(wrapped, paserkWrapPrefix) {
		return key, fmt.Errorf("%w: not a k4.local-wrap.pie value", ErrMalformed)
	}
	payload, err := base64URL.DecodeString(strings.TrimPrefix(wrapped, paserkWrapPrefix))
	if err != nil || len(payload) != 32+32+32 {
		return key, fmt.Errorf("%w: invalid k4.local-wrap.pie payload", ErrMalformed)
	}
	tag := payload[:32]
	nonceBytes := payload[32:64]
	wrappedKey := payload[64:]

	authenticationKey := pieAuthenticationKey(wrapping, Key(nonceBytes))
	expected := blake2bSum(32, authenticationKey[:], pieTagInput(Key(nonceBytes), wrappedKey))
	if subtle.ConstantTimeCompare(tag, expected) != 1 {
		return key, fmt.Errorf("%w: content encryption key authentication failed", ErrKeyMismatch)
	}

	encryptionKey, counterNonce := pieEncryptionKey(wrapping, Key(nonceBytes))
	cipher, err := chacha20.NewUnauthenticatedCipher(encryptionKey[:], counterNonce[:])
	if err != nil {
		return key, err
	}
	cipher.XORKeyStream(key[:], wrappedKey)
	return key, nil
}

func pieEncryptionKey(wrapping, nonce Key) (encryption Key, counterNonce [24]byte) {
	// The pinned PIE v4 construction hashes 0x80 || nonce with keyed BLAKE2b
	// and takes a 56-byte output: 32 bytes of key and a 24-byte counter nonce.
	material := append([]byte{0x80}, nonce[:]...)
	derived := blake2bSum(56, wrapping[:], material)
	copy(encryption[:], derived[:32])
	copy(counterNonce[:], derived[32:])
	return encryption, counterNonce
}

func pieAuthenticationKey(wrapping, nonce Key) Key {
	var authenticationKey Key
	material := append([]byte{0x81}, nonce[:]...)
	copy(authenticationKey[:], blake2bSum(32, wrapping[:], material))
	return authenticationKey
}

func pieTagInput(nonce Key, wrapped []byte) []byte {
	input := make([]byte, 0, len(paserkVersionHeader)+len(paserkWrapHeader)+
		len(paserkOperation)+len(nonce)+len(wrapped))
	input = append(input, paserkVersionHeader...)
	input = append(input, paserkWrapHeader...)
	input = append(input, paserkOperation...)
	input = append(input, nonce[:]...)
	input = append(input, wrapped...)
	return input
}

// EncryptPASETO encrypts payload as a PASETO v4.local token with an implicit
// assertion. Atuin never uses a footer.
func EncryptPASETO(payload, assertion []byte, key Key) (string, error) {
	nonce, err := NewRandomKey()
	if err != nil {
		return "", err
	}
	return encryptPASETOWithNonce(payload, assertion, key, nonce)
}

func encryptPASETOWithNonce(payload, assertion []byte, key, nonce Key) (string, error) {
	encryptionKey, counterNonce := pasetoEncryptionKey(key, nonce)
	authenticationKey := pasetoAuthenticationKey(key, nonce)

	cipher, err := chacha20.NewUnauthenticatedCipher(encryptionKey[:], counterNonce[:])
	if err != nil {
		return "", err
	}
	ciphertext := make([]byte, len(payload))
	cipher.XORKeyStream(ciphertext, payload)

	tag := blake2bSum(32, authenticationKey[:], preAuthenticationEncoding(
		[]byte(pasetoHeader), nonce[:], ciphertext, nil, assertion))

	raw := make([]byte, 0, len(nonce)+len(ciphertext)+len(tag))
	raw = append(raw, nonce[:]...)
	raw = append(raw, ciphertext...)
	raw = append(raw, tag...)
	return pasetoHeader + base64URL.EncodeToString(raw), nil
}

// DecryptPASETO authenticates and decrypts a PASETO v4.local token. A
// tampered token, assertion, or wrong key returns ErrKeyMismatch; a token that
// is not a well-formed v4.local token returns ErrMalformed.
func DecryptPASETO(token string, assertion []byte, key Key) ([]byte, error) {
	if !strings.HasPrefix(token, pasetoHeader) {
		return nil, fmt.Errorf("%w: not a v4.local token", ErrMalformed)
	}
	encoded := strings.TrimPrefix(token, pasetoHeader)
	if strings.Contains(encoded, ".") {
		// Atuin passes no footer; the pinned client rejects any non-empty
		// footer because it compares against the empty default.
		return nil, fmt.Errorf("%w: unexpected PASETO footer", ErrMalformed)
	}
	raw, err := base64URL.DecodeString(encoded)
	if err != nil || len(raw) < 64 {
		return nil, fmt.Errorf("%w: invalid v4.local payload", ErrMalformed)
	}
	nonce := Key(raw[:32])
	ciphertext := raw[32 : len(raw)-32]
	tag := raw[len(raw)-32:]

	authenticationKey := pasetoAuthenticationKey(key, nonce)
	expected := blake2bSum(32, authenticationKey[:], preAuthenticationEncoding(
		[]byte(pasetoHeader), nonce[:], ciphertext, nil, assertion))
	if subtle.ConstantTimeCompare(tag, expected) != 1 {
		return nil, fmt.Errorf("%w: payload authentication failed", ErrKeyMismatch)
	}

	encryptionKey, counterNonce := pasetoEncryptionKey(key, nonce)
	cipher, err := chacha20.NewUnauthenticatedCipher(encryptionKey[:], counterNonce[:])
	if err != nil {
		return nil, err
	}
	payload := make([]byte, len(ciphertext))
	cipher.XORKeyStream(payload, ciphertext)
	return payload, nil
}

func pasetoEncryptionKey(key, nonce Key) (encryption Key, counterNonce [24]byte) {
	material := make([]byte, 0, len(pasetoEncryptionKeyContext)+len(nonce))
	material = append(material, pasetoEncryptionKeyContext...)
	material = append(material, nonce[:]...)
	derived := blake2bSum(56, key[:], material)
	copy(encryption[:], derived[:32])
	copy(counterNonce[:], derived[32:])
	return encryption, counterNonce
}

func pasetoAuthenticationKey(key, nonce Key) Key {
	var authenticationKey Key
	material := make([]byte, 0, len(pasetoAuthenticationKeyContext)+len(nonce))
	material = append(material, pasetoAuthenticationKeyContext...)
	material = append(material, nonce[:]...)
	copy(authenticationKey[:], blake2bSum(32, key[:], material))
	return authenticationKey
}

// preAuthenticationEncoding implements PASETO PAE: the piece count, then each
// piece's length and bytes, all as little-endian u64.
func preAuthenticationEncoding(pieces ...[]byte) []byte {
	total := 8
	for _, piece := range pieces {
		total += 8 + len(piece)
	}
	output := make([]byte, 0, total)
	output = appendLE64(output, uint64(len(pieces)))
	for _, piece := range pieces {
		output = appendLE64(output, uint64(len(piece)))
		output = append(output, piece...)
	}
	return output
}

func appendLE64(output []byte, value uint64) []byte {
	for shift := 0; shift < 64; shift += 8 {
		output = append(output, byte(value>>shift))
	}
	return output
}

func blake2bSum(size int, key, message []byte) []byte {
	var hasher hash.Hash
	var err error
	if key == nil {
		hasher, err = blake2b.New(size, nil)
	} else {
		hasher, err = blake2b.New(size, key)
	}
	if err != nil {
		// Sizes and keys used here are fixed and valid; a failure is a
		// programming error.
		panic(err)
	}
	hasher.Write(message)
	return hasher.Sum(nil)
}

// EncryptedData is the encrypted payload of one Atuin record. The JSON field
// names are the sync wire contract.
type EncryptedData struct {
	Raw string `json:"data"`
	CEK string `json:"content_encryption_key"`
}

type encryptedPayload struct {
	// A pointer distinguishes a missing field (malformed) from the empty
	// string, which is the valid base64url encoding of an empty payload.
	Data *string `json:"data"`
}

type cekEnvelope struct {
	WPK *string `json:"wpk"`
	Kid *string `json:"kid"`
}

// EncryptData implements Atuin's envelope encryption: a random CEK encrypts
// {"data":"<base64url payload>"} as a PASETO token, and the CEK is wrapped
// with key using PASERK PIE.
func EncryptData(payload, assertion []byte, key Key) (EncryptedData, error) {
	var encrypted EncryptedData

	cek, err := NewRandomKey()
	if err != nil {
		return encrypted, err
	}
	defer cek.Wipe()

	encodedPayload := base64URL.EncodeToString(payload)
	inner, err := json.Marshal(encryptedPayload{Data: &encodedPayload})
	if err != nil {
		return encrypted, err
	}
	token, err := EncryptPASETO(inner, assertion, cek)
	if err != nil {
		return encrypted, err
	}
	wrapped, err := WrapPIE(cek, key)
	if err != nil {
		return encrypted, err
	}

	// Field order and compact spelling match serde's cek::Json struct.
	cekJSON := fmt.Sprintf(`{"wpk":%s,"kid":%s}`,
		quoteJSON(wrapped), quoteJSON(KeyID(key)))
	return EncryptedData{Raw: token, CEK: cekJSON}, nil
}

// DecryptData reverses EncryptData. A wrong key, a tampered CEK, or a
// tampered ciphertext returns ErrKeyMismatch; a structurally invalid envelope
// returns ErrMalformed.
func DecryptData(encrypted EncryptedData, assertion []byte, key Key) ([]byte, error) {
	var envelope cekEnvelope
	if err := json.Unmarshal([]byte(encrypted.CEK), &envelope); err != nil {
		return nil, fmt.Errorf("%w: invalid content encryption key envelope", ErrMalformed)
	}
	if envelope.WPK == nil || envelope.Kid == nil {
		return nil, fmt.Errorf("%w: content encryption key envelope is incomplete", ErrMalformed)
	}
	if !strings.HasPrefix(*envelope.Kid, paserkKeyIDPrefix) {
		return nil, fmt.Errorf("%w: invalid content encryption key id", ErrMalformed)
	}
	if *envelope.Kid != KeyID(key) {
		return nil, fmt.Errorf("%w: content encryption key was wrapped for another master key", ErrKeyMismatch)
	}

	cek, err := UnwrapPIE(*envelope.WPK, key)
	if err != nil {
		return nil, err
	}
	defer cek.Wipe()

	inner, err := DecryptPASETO(encrypted.Raw, assertion, cek)
	if err != nil {
		return nil, err
	}

	var payload encryptedPayload
	if err := json.Unmarshal(inner, &payload); err != nil {
		return nil, fmt.Errorf("%w: decrypted payload is not JSON", ErrMalformed)
	}
	if payload.Data == nil {
		return nil, fmt.Errorf("%w: decrypted payload has no data field", ErrMalformed)
	}
	decoded, err := base64URL.DecodeString(*payload.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: decrypted payload is not base64url", ErrMalformed)
	}
	return decoded, nil
}

// UnmarshalJSON accepts both the canonical wire names and the historical
// "raw"/"cek" aliases used by Atuin's Rust struct field names.
func (data *EncryptedData) UnmarshalJSON(input []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return fmt.Errorf("%w: invalid encrypted data object: %v", ErrMalformed, err)
	}
	raw, err := stringField(fields, "data", "raw")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	cek, err := stringField(fields, "content_encryption_key", "cek")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	data.Raw = raw
	data.CEK = cek
	return nil
}

func stringField(fields map[string]json.RawMessage, names ...string) (string, error) {
	var found string
	var present bool
	for _, name := range names {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		if value == nil {
			return "", fmt.Errorf("encrypted data field is null")
		}
		if present {
			return "", fmt.Errorf("duplicate encrypted data field")
		}
		found, present = *value, true
	}
	if !present {
		return "", fmt.Errorf("missing encrypted data field")
	}
	return found, nil
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
