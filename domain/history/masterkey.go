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
	wipeBytes(key)
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
		return decodeMasterKeyBytes(decoded)
	}
	return nil, firstErr
}

// decodeMasterKeyBytes interprets one base64-decoded payload and wipes that
// intermediate buffer before returning. The returned key is always a fresh
// copy the caller owns and must wipe.
func decodeMasterKeyBytes(decoded []byte) ([]byte, error) {
	defer wipeBytes(decoded)
	if len(decoded) == MasterKeyBytes {
		// Legacy Atuin releases base64-encoded the raw 32 bytes.
		return append([]byte(nil), decoded...), nil
	}
	if key, ok := decodeAtuinKeyEncoding(decoded); ok {
		return key, nil
	}
	return nil, fmt.Errorf("%w: got %d bytes", ErrMasterKeyLength, len(decoded))
}

// wipeBytes zeroes a buffer that held key material. It is best effort: Go
// strings and by-value copies cannot all be reached.
func wipeBytes(data []byte) {
	for index := range data {
		data[index] = 0
	}
}

// decodeAtuinKeyEncoding decodes the two MessagePack shapes the official
// client accepts after base64: an array16 of 32 integers (the canonical form
// written by `atuin key --base64` and the key file since v15) and a bin8 of 32
// bytes. The raw 32-byte legacy form is handled by the caller.
//
// This tiny MessagePack envelope is the official key-export format, not a sync
// record; parsing it beside the BIP39 and base64 forms lets the account service
// import `atuin key --base64` without depending on the Atuin adapter. The sync
// record codec, PASETO, and PASERK remain in adapters/atuin/wire.
func decodeAtuinKeyEncoding(decoded []byte) ([]byte, bool) {
	if len(decoded) == 0 {
		return nil, false
	}
	switch decoded[0] {
	case 0xc4: // bin8
		if len(decoded) != 2+MasterKeyBytes || decoded[1] != MasterKeyBytes {
			return nil, false
		}
		return append([]byte(nil), decoded[2:]...), true
	case 0xdc: // array16
		if len(decoded) < 3 || int(decoded[1])<<8|int(decoded[2]) != MasterKeyBytes {
			return nil, false
		}
		key := make([]byte, 0, MasterKeyBytes)
		position := 3
		for len(key) < MasterKeyBytes {
			value, next, ok := decodeAtuinKeyInt(decoded, position)
			if !ok {
				wipeBytes(key)
				return nil, false
			}
			key = append(key, byte(value))
			position = next
		}
		// The pinned client does not require the array to end here; trailing
		// bytes are ignored.
		return key, true
	default:
		return nil, false
	}
}

// decodeAtuinKeyInt reads one MessagePack integer the way rust-rmp's
// read_int::<u8> does. It accepts every integer marker and rejects values
// outside 0..255.
func decodeAtuinKeyInt(decoded []byte, position int) (uint8, int, bool) {
	if position >= len(decoded) {
		return 0, position, false
	}
	marker := decoded[position]
	position++
	readBigEndian := func(size int) (uint64, bool) {
		if len(decoded)-position < size {
			return 0, false
		}
		var value uint64
		for index := 0; index < size; index++ {
			value = value<<8 | uint64(decoded[position+index])
		}
		position += size
		return value, true
	}
	var value uint64
	var ok bool
	switch {
	case marker <= 0x7f:
		value, ok = uint64(marker), true
	case marker >= 0xe0:
		return 0, position, false
	case marker == 0xcc:
		value, ok = readBigEndian(1)
	case marker == 0xcd:
		value, ok = readBigEndian(2)
	case marker == 0xce:
		value, ok = readBigEndian(4)
	case marker == 0xcf:
		value, ok = readBigEndian(8)
	case marker == 0xd0:
		signed, valid := readBigEndian(1)
		ok = valid && int8(signed) >= 0
		value = uint64(int8(signed))
	case marker == 0xd1:
		signed, valid := readBigEndian(2)
		ok = valid && int16(signed) >= 0
		value = uint64(int16(signed))
	case marker == 0xd2:
		signed, valid := readBigEndian(4)
		ok = valid && int32(signed) >= 0
		value = uint64(int32(signed))
	case marker == 0xd3:
		signed, valid := readBigEndian(8)
		ok = valid && int64(signed) >= 0
		value = uint64(int64(signed))
	default:
		return 0, position, false
	}
	if !ok || value > 255 {
		return 0, position, false
	}
	return uint8(value), position, true
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
	// packed carries the mnemonic entropy bits, which are the key itself. Wipe
	// it on every return path; the caller receives a separate copy.
	defer wipeBytes(packed)
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
