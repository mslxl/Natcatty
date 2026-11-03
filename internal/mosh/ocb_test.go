// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Vectors from RFC 7253 Appendix A with empty associated data. mosh only
// uses empty AAD, a 12-byte nonce, and a 128-bit tag.
func TestOCB3RFC7253Vectors(t *testing.T) {
	key := mustHex(t, "000102030405060708090A0B0C0D0E0F")
	vectors := []struct {
		name       string
		nonce      string
		plaintext  string
		ciphertext string
	}{
		{
			name:       "empty plaintext",
			nonce:      "BBAA99887766554433221100",
			plaintext:  "",
			ciphertext: "785407BFFFC8AD9EDCC5520AC9111EE6",
		},
		{
			name:       "8-byte plaintext",
			nonce:      "BBAA99887766554433221103",
			plaintext:  "0001020304050607",
			ciphertext: "45DD69F8F5AAE72414054CD1F35D82760B2CD00D2F99BFA9",
		},
		{
			name:       "16-byte plaintext",
			nonce:      "BBAA99887766554433221106",
			plaintext:  "000102030405060708090A0B0C0D0E0F",
			ciphertext: "5CE88EC2E0692706A915C00AEB8B2396F40E1C743F52436BDF06D8FA1ECA343D",
		},
		{
			name:       "24-byte plaintext",
			nonce:      "BBAA99887766554433221109",
			plaintext:  "000102030405060708090A0B0C0D0E0F1011121314151617",
			ciphertext: "221BD0DE7FA6FE993ECCD769460A0AF2D6CDED0C395B1C3CE725F32494B9F914D85C0B1EB38357FF",
		},
		{
			name:       "32-byte plaintext",
			nonce:      "BBAA9988776655443322110C",
			plaintext:  "000102030405060708090A0B0C0D0E0F101112131415161718191A1B1C1D1E1F",
			ciphertext: "2942BFC773BDA23CABC6ACFD9BFD5835BD300F0973792EF46040C53F1432BCDFB5E1DDE3BC18A5F840B52E653444D5DF",
		},
	}

	for _, tt := range vectors {
		t.Run(tt.name, func(t *testing.T) {
			ocb, err := NewOCB(key)
			if err != nil {
				t.Fatalf("NewOCB: %v", err)
			}
			defer ocb.Close()

			nonce := mustHex(t, tt.nonce)
			plaintext := mustHex(t, tt.plaintext)
			want := mustHex(t, tt.ciphertext)

			got, err := ocb.Encrypt(nonce, plaintext)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("ciphertext mismatch\n got: %X\nwant: %X", got, want)
			}

			decrypted, ok := ocb.Decrypt(nonce, want)
			if !ok {
				t.Fatal("Decrypt rejected a valid tag")
			}
			if !bytes.Equal(decrypted, plaintext) {
				t.Fatalf("decrypted mismatch\n got: %X\nwant: %X", decrypted, plaintext)
			}
		})
	}
}

func TestOCB3RejectsTampering(t *testing.T) {
	ocb, err := NewOCB(make([]byte, 16))
	if err != nil {
		t.Fatalf("NewOCB: %v", err)
	}
	defer ocb.Close()

	nonce := make([]byte, 12)
	ciphertext, err := ocb.Encrypt(nonce, []byte("attack at dawn"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	for i := range ciphertext {
		tampered := append([]byte(nil), ciphertext...)
		tampered[i] ^= 0x01
		if _, ok := ocb.Decrypt(nonce, tampered); ok {
			t.Fatalf("accepted tampering at byte %d", i)
		}
	}
	if _, ok := ocb.Decrypt(nonce, ciphertext[:len(ciphertext)-1]); ok {
		t.Fatal("accepted truncated tag")
	}
	otherNonce := make([]byte, 12)
	otherNonce[11] = 1
	if _, ok := ocb.Decrypt(otherNonce, ciphertext); ok {
		t.Fatal("accepted wrong nonce")
	}
}

func TestOCB3CloseZeroizes(t *testing.T) {
	ocb, err := NewOCB(bytes.Repeat([]byte{0xAA}, 16))
	if err != nil {
		t.Fatalf("NewOCB: %v", err)
	}
	ocb.Close()
	for i, b := range ocb.key {
		if b != 0 {
			t.Fatalf("key byte %d not zeroized: %#x", i, b)
		}
	}
	if _, err := ocb.Encrypt(make([]byte, 12), []byte("x")); err == nil {
		t.Fatal("Encrypt succeeded after Close")
	}
	if _, ok := ocb.Decrypt(make([]byte, 12), make([]byte, 16)); ok {
		t.Fatal("Decrypt succeeded after Close")
	}
	// Close is idempotent.
	ocb.Close()
}

func TestNewOCBRejectsKeySizes(t *testing.T) {
	for _, size := range []int{0, 15, 17, 32} {
		if _, err := NewOCB(make([]byte, size)); err == nil {
			t.Fatalf("NewOCB accepted a %d-byte key", size)
		}
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}
