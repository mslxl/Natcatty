package history

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// The 24-word BIP39 vectors and their entropy are the published BIP39 test
// vectors; they are independent of this implementation.
const (
	bip39VectorKey = "legal winner thank year wave sausage worth useful legal winner thank year wave sausage worth useful legal winner thank year wave sausage worth title"
	bip39ZeroKey   = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"
)

func TestEmbeddedListIsThePublishedBIP39EnglishList(t *testing.T) {
	words := strings.Fields(bip39EnglishWordList)
	if len(words) != 2048 {
		t.Fatalf("word list has %d words, want 2048", len(words))
	}
	if words[0] != "abandon" || words[2047] != "zoo" {
		t.Fatalf("boundary words = %q, %q", words[0], words[2047])
	}
	seen := make(map[string]bool, len(words))
	for position, word := range words {
		if word != strings.ToLower(word) {
			t.Fatalf("word %d is not lowercase: %q", position, word)
		}
		if seen[word] {
			t.Fatalf("duplicate word %q at position %d", word, position)
		}
		seen[word] = true
	}
}

func TestParseMasterKeyAcceptsBIP39(t *testing.T) {
	key, err := ParseMasterKey(bip39VectorKey)
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	want := bytes.Repeat([]byte{0x7f}, MasterKeyBytes)
	if !bytes.Equal(key, want) {
		t.Fatalf("key = %x, want %x", key, want)
	}

	zero, err := ParseMasterKey(bip39ZeroKey)
	if err != nil {
		t.Fatalf("ParseMasterKey(zero vector): %v", err)
	}
	if !bytes.Equal(zero, make([]byte, MasterKeyBytes)) {
		t.Fatalf("zero key = %x", zero)
	}
}

func TestParseMasterKeyRejectsAnInvalidBIP39Checksum(t *testing.T) {
	invalid := strings.Repeat("abandon ", 23) + "abandon"
	if _, err := ParseMasterKey(invalid); err == nil {
		t.Fatal("a mnemonic with a bad checksum was accepted")
	} else if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error %q does not report the checksum", err)
	}
}

func TestParseMasterKeyRejectsAWrongMnemonicLength(t *testing.T) {
	// A 12-word mnemonic has a valid checksum but encodes 16 bytes, not the
	// 32-byte Atuin master key.
	twelve := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	if _, err := ParseMasterKey(twelve); err == nil {
		t.Fatal("a 12-word mnemonic was accepted as a 32-byte key")
	}
}

func TestParseMasterKeyAcceptsBase64(t *testing.T) {
	want := make([]byte, MasterKeyBytes)
	for index := range want {
		want[index] = byte(index)
	}
	encodings := []struct {
		name     string
		encoding *base64.Encoding
	}{
		{"std", base64.StdEncoding},
		{"raw-std", base64.RawStdEncoding},
		{"url", base64.URLEncoding},
		{"raw-url", base64.RawURLEncoding},
	}
	for _, candidate := range encodings {
		key, err := ParseMasterKey(candidate.encoding.EncodeToString(want))
		if err != nil {
			t.Fatalf("%s: ParseMasterKey: %v", candidate.name, err)
		}
		if !bytes.Equal(key, want) {
			t.Fatalf("%s: key = %x, want %x", candidate.name, key, want)
		}
	}
}

func TestParseMasterKeyRejectsBase64OfTheWrongLength(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, MasterKeyBytes-1))
	if _, err := ParseMasterKey(short); err == nil {
		t.Fatal("a 31-byte base64 key was accepted")
	}
}

// atuinKeyArray16 is the canonical encoding `atuin key --base64` writes:
// a MessagePack array16 of 32 integers, each with rmp's compact unsigned form.
func atuinKeyArray16(key []byte) []byte {
	encoded := []byte{0xdc, 0x00, 0x20}
	for _, value := range key {
		if value < 0x80 {
			encoded = append(encoded, value)
			continue
		}
		encoded = append(encoded, 0xcc, value)
	}
	return encoded
}

func testMasterKey() []byte {
	key := make([]byte, MasterKeyBytes)
	for index := range key {
		key[index] = byte(index * 7)
	}
	return key
}

func TestParseMasterKeyAcceptsAtuinMessagePackEncodings(t *testing.T) {
	want := testMasterKey()
	encodings := []struct {
		name  string
		bytes []byte
	}{
		{"array16 canonical", atuinKeyArray16(want)},
		{"bin8", append([]byte{0xc4, 0x20}, want...)},
	}
	for _, candidate := range encodings {
		t.Run(candidate.name, func(t *testing.T) {
			input := base64.StdEncoding.EncodeToString(candidate.bytes)
			key, err := ParseMasterKey(input)
			if err != nil {
				t.Fatalf("ParseMasterKey: %v", err)
			}
			if !bytes.Equal(key, want) {
				t.Fatal("the decoded key differs from the expected test key")
			}
			// The official client trims trailing whitespace from the encoded
			// key before base64-decoding it.
			key, err = ParseMasterKey(input + "\n")
			if err != nil || !bytes.Equal(key, want) {
				t.Fatalf("trailing newline: key=%x err=%v", key, err)
			}
		})
	}
}

func TestParseMasterKeyAcceptsAtuinArray16IntegerForms(t *testing.T) {
	want := bytes.Repeat([]byte{0x05}, MasterKeyBytes)
	var encoded []byte
	encoded = append(encoded, 0xdc, 0x00, 0x20)
	for index := 0; index < MasterKeyBytes; index++ {
		// A uint16 marker for a value that fits in a byte; rmp's
		// read_int::<u8> accepts it, and so does Atuin.
		encoded = append(encoded, 0xcd, 0x00, 0x05)
	}
	key, err := ParseMasterKey(base64.StdEncoding.EncodeToString(encoded))
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	if !bytes.Equal(key, want) {
		t.Fatalf("key = %x, want %x", key, want)
	}
}

func TestParseMasterKeyRejectsMalformedAtuinEncodings(t *testing.T) {
	key := testMasterKey()
	tests := []struct {
		name  string
		bytes []byte
	}{
		{"array16 wrong length", append([]byte{0xdc, 0x00, 0x1f}, atuinKeyArray16(key)[3:]...)},
		{"array16 truncated", atuinKeyArray16(key)[:3+MasterKeyBytes-1]},
		{"array16 negative", encodedArray16Values(bytes.Repeat([]byte{0x05}, MasterKeyBytes-1), []byte{0xff})},
		{"array16 out of range", encodedArray16Values(bytes.Repeat([]byte{0x05}, MasterKeyBytes-1), []byte{0xcd, 0x01, 0x00})},
		{"bin8 wrong length", append([]byte{0xc4, 0x1f}, key[:MasterKeyBytes-1]...)},
		{"bin8 truncated", append([]byte{0xc4, 0x20}, key[:MasterKeyBytes-1]...)},
		{"bin8 trailing", append(append([]byte{0xc4, 0x20}, key...), 0x00)},
		{"unknown marker", append([]byte{0xc5, 0x00, 0x20}, key...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseMasterKey(base64.StdEncoding.EncodeToString(test.bytes)); err == nil {
				t.Fatal("malformed Atuin key encoding was accepted")
			}
		})
	}
}

func TestParseMasterKeyIgnoresArray16TrailingBytes(t *testing.T) {
	// The pinned decoder reads 32 integers and never checks for trailing
	// bytes, so neither do we.
	want := testMasterKey()
	encoded := append(atuinKeyArray16(want), 0xc0, 0x00)
	key, err := ParseMasterKey(base64.StdEncoding.EncodeToString(encoded))
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	if !bytes.Equal(key, want) {
		t.Fatalf("key = %x, want %x", key, want)
	}
}

func encodedArray16Values(prefix []byte, last []byte) []byte {
	encoded := []byte{0xdc, 0x00, 0x20}
	for _, value := range prefix {
		if value < 0x80 {
			encoded = append(encoded, value)
		} else {
			encoded = append(encoded, 0xcc, value)
		}
	}
	return append(encoded, last...)
}

func TestParseMasterKeyRejectsAnUnknownWord(t *testing.T) {
	invalid := strings.Replace(bip39VectorKey, "legal", "notaword", 1)
	if _, err := ParseMasterKey(invalid); err == nil {
		t.Fatal("a mnemonic containing an unknown word was accepted")
	}
}

func TestKeyFingerprintIsStableAndDoesNotContainTheKey(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, MasterKeyBytes)
	fingerprint := KeyFingerprint(key)
	if fingerprint == "" {
		t.Fatal("empty fingerprint")
	}
	if again := KeyFingerprint(key); again != fingerprint {
		t.Fatalf("fingerprint changed: %q then %q", fingerprint, again)
	}
	other := bytes.Repeat([]byte{0x43}, MasterKeyBytes)
	if KeyFingerprint(other) == fingerprint {
		t.Fatal("different keys share a fingerprint")
	}
	if strings.Contains(fingerprint, base64.StdEncoding.EncodeToString(key)) ||
		strings.Contains(fingerprint, bip39VectorKey) {
		t.Fatalf("fingerprint %q contains the key", fingerprint)
	}
}

// TestDecodeMasterKeyBytesWipesTheIntermediateBuffer covers every base64
// decode shape. The base64-decoded buffer and the MessagePack envelope hold the
// key bytes, so decodeMasterKeyBytes must zero its input before returning and
// hand back a separate copy the caller owns.
func TestDecodeMasterKeyBytesWipesTheIntermediateBuffer(t *testing.T) {
	want := testMasterKey()
	tests := []struct {
		name  string
		input []byte
	}{
		{"legacy raw", append([]byte(nil), want...)},
		{"array16", atuinKeyArray16(want)},
		{"bin8", append([]byte{0xc4, 0x20}, want...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, err := decodeMasterKeyBytes(test.input)
			if err != nil {
				t.Fatalf("decodeMasterKeyBytes: %v", err)
			}
			if !bytes.Equal(key, want) {
				t.Fatal("the decoded key differs from the expected test key")
			}
			if !bytes.Equal(test.input, make([]byte, len(test.input))) {
				t.Fatal("the decode intermediate was not wiped")
			}
			// The returned key must not alias the wiped buffer.
			if bytes.Equal(key, test.input) {
				t.Fatal("the returned key aliases the wiped intermediate")
			}
		})
	}
}

func TestDecodeMasterKeyBytesWipesOnFailure(t *testing.T) {
	input := append([]byte(nil), testMasterKey()[:MasterKeyBytes-1]...)
	if _, err := decodeMasterKeyBytes(input); err == nil {
		t.Fatal("a short key was accepted")
	}
	if !bytes.Equal(input, make([]byte, len(input))) {
		t.Fatal("the failed decode intermediate was not wiped")
	}
}

func TestWipeMasterKeyZeroesTheBytes(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, MasterKeyBytes)
	WipeMasterKey(key)
	if !bytes.Equal(key, make([]byte, MasterKeyBytes)) {
		t.Fatalf("key was not wiped: %x", key)
	}
}
