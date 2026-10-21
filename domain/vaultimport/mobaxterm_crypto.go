package vaultimport

import (
	"crypto/aes"
	"crypto/sha512"
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

// MobaXterm protects saved credentials in one of two ways. With a master
// password it uses AES-256-CFB8 under SHA-512(password). Without one it
// obfuscates the password with a "weak cipher": two hexadecimal characters
// drawn from a rotating 16-byte key. Both are reproduced here with the
// standard library so imported credentials never reach storage in plaintext.

const mobaWeakAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz+/"

// repeatToLength repeats value until it reaches minLength, matching the
// original's helper. An empty value stays empty instead of looping forever.
func repeatToLength(value string, minLength int) string {
	if value == "" {
		return value
	}
	out := value
	for len(out) < minLength {
		out += out
	}
	return out
}

// craftWeakKey builds the rotating key from the ordered key space. The base
// key is the same fixed string the original starts from.
func craftWeakKey(keySpace []string) []byte {
	key := []byte("0d5e9n1348/U2+67")
	if len(keySpace) == 0 {
		return key
	}
	for index := range key {
		source := keySpace[(index+1)%len(keySpace)]
		if index >= len(source) {
			continue
		}
		value := source[index]
		if value >= utf8.RuneSelf {
			continue
		}
		has := false
		for _, existing := range key {
			if existing == value {
				has = true
				break
			}
		}
		if !has && strings.IndexByte(mobaWeakAlphabet, value) >= 0 {
			key[index] = value
		}
	}
	return key
}

func craftMobaSessionPKey(sessionP string) []byte {
	value := repeatToLength(sessionP, 20)
	return craftWeakKey([]string{
		strings.ToUpper(value), strings.ToUpper(value),
		strings.ToLower(value), strings.ToLower(value),
	})
}

func craftMobaConnectionKey(sysUsername, sysHostname, connUsername, connHostname string) []byte {
	first := repeatToLength(sysUsername+sysHostname, 20)
	second := repeatToLength(connUsername+connHostname, 20)
	return craftWeakKey([]string{
		strings.ToUpper(first), strings.ToUpper(second),
		strings.ToLower(first), strings.ToLower(second),
	})
}

// rotateWeakKey moves the last key byte to the front, as the original does
// after every character.
func rotateWeakKey(key []byte) {
	last := key[len(key)-1]
	copy(key[1:], key[:len(key)-1])
	key[0] = last
}

// decryptMobaWeakCipher turns a weak-cipher string back into bytes. Only
// characters present in the key carry data; anything else is a separator.
func decryptMobaWeakCipher(ciphertext string, key []byte) []byte {
	filtered := make([]byte, 0, len(ciphertext))
	for index := 0; index < len(ciphertext); index++ {
		value := ciphertext[index]
		if strings.IndexByte(string(key), value) >= 0 {
			filtered = append(filtered, value)
		}
	}
	if len(filtered) == 0 || len(filtered)%2 != 0 {
		return nil
	}
	working := append([]byte(nil), key...)
	plaintext := make([]byte, len(filtered)/2)
	for index := 0; index < len(filtered); index += 2 {
		low := indexByte(working, filtered[index])
		rotateWeakKey(working)
		high := indexByte(working, filtered[index+1])
		rotateWeakKey(working)
		if low < 0 || high < 0 {
			return nil
		}
		plaintext[index/2] = byte((high << 4) + low)
	}
	return plaintext
}

func indexByte(value []byte, target byte) int {
	for index, current := range value {
		if current == target {
			return index
		}
	}
	return -1
}

// decodeMobaPlaintext accepts a C string with optional trailing NUL padding.
// AES-CFB is unauthenticated, so a wrong key can inject an interior NUL; that
// must fail instead of returning a short printable prefix.
func decodeMobaPlaintext(value []byte) (string, bool) {
	if len(value) == 0 {
		return "", false
	}
	slice := value
	if nul := indexByte(value, 0); nul >= 0 {
		if nul == 0 {
			return "", false
		}
		for index := nul; index < len(value); index++ {
			if value[index] != 0 {
				return "", false
			}
		}
		slice = value[:nul]
	}
	if len(slice) == 0 {
		return "", false
	}
	for _, current := range slice {
		if current < 0x20 && current != 0x09 {
			return "", false
		}
	}
	if !utf8.Valid(slice) {
		return "", false
	}
	return string(slice), true
}

// mobaBase64 decodes MobaXterm's base64. altChars also maps the URL-safe
// characters it sometimes uses for master-password ciphertext.
func mobaBase64(value string, altChars bool) []byte {
	normalized := value
	if altChars {
		normalized = strings.ReplaceAll(normalized, "@", "+")
		normalized = strings.ReplaceAll(normalized, "_", "/")
	}
	normalized = strings.Join(strings.Fields(normalized), "")
	if normalized == "" {
		return nil
	}
	if padding := (4 - len(normalized)%4) % 4; padding > 0 {
		normalized += strings.Repeat("=", padding)
	}
	decoded, err := base64.StdEncoding.DecodeString(normalized)
	if err != nil {
		return nil
	}
	return decoded
}

// aesCFB8Decrypt decrypts with AES-256 in 8-bit CFB mode. Standard
// library CFB is block-sized; MobaXterm uses a one-byte shift register.
func aesCFB8Decrypt(key, iv, ciphertext []byte) ([]byte, bool) {
	block, err := aes.NewCipher(key)
	if err != nil || len(iv) != block.BlockSize() {
		return nil, false
	}
	shift := append([]byte(nil), iv...)
	encrypted := make([]byte, 16)
	out := make([]byte, len(ciphertext))
	for index, current := range ciphertext {
		block.Encrypt(encrypted, shift)
		out[index] = current ^ encrypted[0]
		copy(shift, shift[1:])
		shift[len(shift)-1] = current
	}
	return out, true
}

// aesCFB8Encrypt is the inverse, used by tests and by tooling that must
// produce a fixture. It is not used on the import path.
func aesCFB8Encrypt(key, iv, plaintext []byte) ([]byte, bool) {
	block, err := aes.NewCipher(key)
	if err != nil || len(iv) != block.BlockSize() {
		return nil, false
	}
	shift := append([]byte(nil), iv...)
	encrypted := make([]byte, 16)
	out := make([]byte, len(plaintext))
	for index, current := range plaintext {
		block.Encrypt(encrypted, shift)
		out[index] = current ^ encrypted[0]
		copy(shift, shift[1:])
		shift[len(shift)-1] = out[index]
	}
	return out, true
}

func decryptMobaMasterCipher(ciphertext, masterPassword string) ([]byte, bool) {
	digest := sha512.Sum512([]byte(masterPassword))
	key := digest[:32]
	if strings.HasPrefix(ciphertext, "_@") && len(ciphertext) >= 20 {
		// The original treats the 16 characters after "_@" as latin-1.
		raw := []byte(ciphertext[2:18])
		iv := make([]byte, 16)
		copy(iv, raw)
		decoded := mobaBase64(ciphertext, true)
		if len(decoded) <= 15 {
			return nil, false
		}
		return aesCFB8Decrypt(key, iv, decoded[15:])
	}
	decoded := mobaBase64(ciphertext, false)
	if len(decoded) == 0 {
		return nil, false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	iv := make([]byte, 16)
	block.Encrypt(iv, iv)
	return aesCFB8Decrypt(key, iv, decoded)
}

// mobaSecretInput is one stored credential and the context available to
// decrypt it.
type mobaSecretInput struct {
	ciphertext   string
	master       string
	sessionP     string
	sysUsername  string
	sysHostname  string
	connUsername string
	connHostname string
}

// decryptMobaStoredSecret mirrors the original's fallback order: master
// password, then a connection-specific weak key, then SessionP.
func decryptMobaStoredSecret(input mobaSecretInput) (string, bool) {
	ciphertext := strings.TrimSpace(input.ciphertext)
	if ciphertext == "" {
		return "", false
	}
	if input.master != "" {
		plaintext, ok := decryptMobaMasterCipher(ciphertext, input.master)
		if !ok {
			return "", false
		}
		return decodeMobaPlaintext(plaintext)
	}
	if input.connUsername != "" && input.connHostname != "" &&
		input.sysUsername != "" && input.sysHostname != "" {
		weak := decryptMobaWeakCipher(ciphertext, craftMobaConnectionKey(
			input.sysUsername, input.sysHostname, input.connUsername, input.connHostname))
		if password, ok := decodeMobaPlaintext(weak); ok {
			return password, true
		}
	}
	if input.sessionP != "" {
		return decodeMobaPlaintext(decryptMobaWeakCipher(ciphertext,
			craftMobaSessionPKey(input.sessionP)))
	}
	return "", false
}
