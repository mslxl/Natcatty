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

func TestWipeMasterKeyZeroesTheBytes(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, MasterKeyBytes)
	WipeMasterKey(key)
	if !bytes.Equal(key, make([]byte, MasterKeyBytes)) {
		t.Fatalf("key was not wiped: %x", key)
	}
}
