package vaultimport

import (
	"crypto/des"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf8"
)

// FinalShell encrypts saved passwords with DES-EDE3 using a key derived from
// the first eight bytes of the stored value and a Java Random sequence. The
// key is repeated three times, so a single DES cipher is equivalent. The
// implementation below follows the original algorithm exactly; it is pure
// standard library and unauthenticated, so a wrong key fails the padding and
// UTF-8 checks rather than returning garbage.

const (
	javaRandomMultiplier = uint64(0x5deece66d)
	javaRandomAddend     = uint64(0xb)
	javaRandomMask       = (uint64(1) << 48) - 1
	// finalShellSeed is the constant the original uses to seed its Random.
	finalShellSeed = int64(3680984568597093857)
)

type javaRandom struct{ seed uint64 }

func newJavaRandom(seed int64) javaRandom {
	return javaRandom{seed: (uint64(seed) ^ javaRandomMultiplier) & javaRandomMask}
}

func (r *javaRandom) next(bits uint) uint64 {
	r.seed = (r.seed*javaRandomMultiplier + javaRandomAddend) & javaRandomMask
	return r.seed >> (48 - bits)
}

func (r *javaRandom) nextInt(bound int) int {
	if bound <= 0 {
		return 0
	}
	if bound&(bound-1) == 0 {
		return int((uint64(bound) * r.next(31)) >> 31)
	}
	for {
		bits := int(int32(r.next(31)))
		value := bits % bound
		if int32(bits-value+(bound-1)) >= 0 {
			return value
		}
	}
}

func (r *javaRandom) nextLong() int64 {
	high := int32(uint32(r.next(32)))
	low := int32(uint32(r.next(32)))
	return int64(uint64(uint32(high))<<32 | uint64(uint32(low)))
}

func signedByte(value byte) int64 { return int64(int8(value)) }

// deriveFinalShellKey reconstructs the DES key from the eight-byte header.
func deriveFinalShellKey(head []byte) ([]byte, bool) {
	if len(head) != 8 {
		return nil, false
	}
	randomForDivisor := newJavaRandom(signedByte(head[5]))
	divisor := randomForDivisor.nextInt(127)
	if divisor == 0 {
		return nil, false
	}
	random := newJavaRandom(finalShellSeed / int64(divisor))
	iterations := signedByte(head[0])
	for index := int64(0); index < iterations; index++ {
		random.nextLong()
	}
	second := newJavaRandom(random.nextLong())
	longs := []int64{
		signedByte(head[4]),
		second.nextLong(),
		signedByte(head[7]),
		signedByte(head[3]),
		second.nextLong(),
		signedByte(head[1]),
		random.nextLong(),
		signedByte(head[2]),
	}
	source := make([]byte, 64)
	for index, value := range longs {
		binary.BigEndian.PutUint64(source[index*8:], uint64(value))
	}
	sum := md5.Sum(source)
	return sum[:8], true
}

// decodeFinalShellPassword decrypts one stored FinalShell password. It
// returns false for malformed base64, a wrong key, bad padding, invalid
// UTF-8, or a value containing control characters.
func decodeFinalShellPassword(encoded string) (string, bool) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return "", false
	}
	if len(decoded) < 16 || (len(decoded)-8)%8 != 0 {
		return "", false
	}
	key, ok := deriveFinalShellKey(decoded[:8])
	if !ok {
		return "", false
	}
	block, err := des.NewCipher(key)
	if err != nil {
		return "", false
	}
	ciphertext := decoded[8:]
	plaintext := make([]byte, len(ciphertext))
	for offset := 0; offset < len(ciphertext); offset += 8 {
		block.Decrypt(plaintext[offset:offset+8], ciphertext[offset:offset+8])
	}
	padding := int(plaintext[len(plaintext)-1])
	if padding < 1 || padding > 8 || padding > len(plaintext) {
		return "", false
	}
	for index := len(plaintext) - padding; index < len(plaintext); index++ {
		if int(plaintext[index]) != padding {
			return "", false
		}
	}
	body := plaintext[:len(plaintext)-padding]
	if !utf8.Valid(body) {
		return "", false
	}
	for _, value := range body {
		if value <= 0x1f || (value >= 0x7f && value <= 0x9f) {
			return "", false
		}
	}
	if len(body) == 0 {
		return "", false
	}
	return string(body), true
}
