package history

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// MasterKeyBytes is the size of the Atuin master key. The key protects only
// the encrypted record payloads on the wire; local history stays plaintext.
const MasterKeyBytes = 32

// Master-key parsing errors. Callers display a localized message and never
// include the input, which is a credential.
var (
	// ErrMasterKeyEmpty reports an empty input.
	ErrMasterKeyEmpty = errors.New("history: empty master key")
	// ErrMasterKeyEncoding reports an input that is neither a BIP39 mnemonic
	// nor base64.
	ErrMasterKeyEncoding = errors.New("history: master key is neither a BIP39 mnemonic nor base64")
	// ErrMasterKeyMnemonic reports a BIP39 word count this format does not
	// define.
	ErrMasterKeyMnemonic = errors.New("history: master key mnemonic has an unsupported word count")
	// ErrMasterKeyWord reports a word outside the BIP39 English list.
	ErrMasterKeyWord = errors.New("history: master key mnemonic contains an unknown word")
	// ErrMasterKeyChecksum reports a mnemonic whose checksum does not match.
	ErrMasterKeyChecksum = errors.New("history: master key mnemonic checksum is invalid")
	// ErrMasterKeyLength reports a key that does not encode exactly 32 bytes.
	ErrMasterKeyLength = errors.New("history: master key must encode 32 bytes")
)

// ParseMasterKey accepts the two forms the official Atuin client exports: a
// 24-word BIP39 English mnemonic with a valid checksum, or a base64-encoded
// 32-byte key. It returns a fresh byte slice the caller must wipe.
func ParseMasterKey(input string) ([]byte, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, ErrMasterKeyEmpty
	}
	if words := strings.Fields(trimmed); len(words) > 1 {
		return parseMnemonic(words)
	}
	key, err := decodeMasterKeyBase64(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMasterKeyEncoding, err)
	}
	return key, nil
}

// KeyFingerprint returns a short, stable hash of the key for display and key
// change detection. It is not a secret and never contains the key.
func KeyFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// WipeMasterKey zeroes key material in place. Callers keep the key only for
// the duration of a sync operation.
func WipeMasterKey(key []byte) {
	for index := range key {
		key[index] = 0
	}
}

func decodeMasterKeyBase64(input string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var firstErr error
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(input)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(decoded) != MasterKeyBytes {
			return nil, fmt.Errorf("%w: got %d bytes", ErrMasterKeyLength, len(decoded))
		}
		return decoded, nil
	}
	return nil, firstErr
}

func parseMnemonic(words []string) ([]byte, error) {
	switch len(words) {
	case 12, 15, 18, 21, 24:
	default:
		return nil, fmt.Errorf("%w: %d words", ErrMasterKeyMnemonic, len(words))
	}

	indexes := make([]int, len(words))
	for position, word := range words {
		index, ok := bip39EnglishIndex(strings.ToLower(word))
		if !ok {
			return nil, fmt.Errorf("%w: position %d", ErrMasterKeyWord, position+1)
		}
		indexes[position] = index
	}

	totalBits := len(words) * 11
	checksumBits := totalBits / 33
	entropyBits := totalBits - checksumBits
	packed := make([]byte, (totalBits+7)/8)
	for position, index := range indexes {
		for bit := 0; bit < 11; bit++ {
			if index&(1<<(10-bit)) == 0 {
				continue
			}
			absolute := position*11 + bit
			packed[absolute/8] |= 1 << (7 - (absolute % 8))
		}
	}

	entropy := packed[:entropyBits/8]
	sum := sha256.Sum256(entropy)
	for bit := 0; bit < checksumBits; bit++ {
		absolute := entropyBits + bit
		actual := packed[absolute/8] >> (7 - (absolute % 8)) & 1
		expected := sum[bit/8] >> (7 - (bit % 8)) & 1
		if actual != expected {
			return nil, ErrMasterKeyChecksum
		}
	}
	if len(entropy) != MasterKeyBytes {
		return nil, fmt.Errorf("%w: got %d bytes", ErrMasterKeyLength, len(entropy))
	}
	return append([]byte(nil), entropy...), nil
}

// bip39EnglishIndexMap is built at package initialization so concurrent key
// parsing never races on a lazy map.
var bip39EnglishIndexMap = buildBIP39EnglishIndex()

func buildBIP39EnglishIndex() map[string]int {
	words := strings.Fields(bip39EnglishWordList)
	index := make(map[string]int, len(words))
	for position, candidate := range words {
		index[candidate] = position
	}
	return index
}

// bip39EnglishIndex looks a word up in the embedded list.
func bip39EnglishIndex(word string) (int, bool) {
	position, ok := bip39EnglishIndexMap[word]
	return position, ok
}
