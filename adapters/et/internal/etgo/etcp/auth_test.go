// SPDX-License-Identifier: Apache-2.0
package etcp

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
)

func decodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("bad hex %q: %v", value, err)
	}
	return decoded
}

// TestConnectionProofVectors pins the exact NUL-separated message and keyed
// BLAKE2b-256 against values generated independently with Python hashlib.
// Upstream builds the same bytes in CryptoHandler::connectionProofMessage
// (src/base/CryptoHandler.cpp:34-57 at et-v7.1.1).
func TestConnectionProofVectors(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	challenge := decodeHex(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	tests := []struct {
		name        string
		resetIntent bool
		want        string
	}{
		{"connect", false, "744245dafd548832df0810445f7758d4183c814cdd4f3c8b4e8c807fb651fdce"},
		{"attach", true, "3da684f8312548aab9667e697e406d6096503140f414c81fc3c1b7c1b582f4bd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := connectionProof(key, "XXXtestclient001", 6, challenge, tt.resetIntent)
			if err != nil {
				t.Fatalf("connectionProof: %v", err)
			}
			if want := decodeHex(t, tt.want); !bytes.Equal(got, want) {
				t.Fatalf("proof = %x, want %x", got, want)
			}
		})
	}
}

// TestResetDecisionProofVectors pins the final authenticated response, whose
// client id is not length-prefixed (CryptoHandler::resetDecisionMessage at
// et-v7.1.1). It also proves the verifier rejects a proof for another status,
// bit, or salt.
func TestResetDecisionProofVectors(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	challenge := decodeHex(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	salt := decodeHex(t, "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f")
	tests := []struct {
		name          string
		status        protocol.ConnectStatus
		resetRequired bool
		salt          []byte
		want          string
	}{
		{"new client", protocol.ConnectStatus_NEW_CLIENT, false, nil,
			"f650cb270c64e5f6067a1adbde29effb00731cd28a133c6e54b2538d5dd88d3c"},
		{"returning reset", protocol.ConnectStatus_RETURNING_CLIENT, true, salt,
			"919db5de97eb5e65320617f0ce1d753261ba85508d5f4a9585c3dc66f1c2f9e9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := decodeHex(t, tt.want)
			if !verifyResetDecisionProof(want, key, "XXXtestclient001", 6, challenge,
				tt.status, tt.resetRequired, tt.salt) {
				t.Fatal("the known-good decision proof was rejected")
			}
			if verifyResetDecisionProof(want, key, "XXXtestclient001", 6, challenge,
				protocol.ConnectStatus_NEW_CLIENT, !tt.resetRequired, nil) {
				t.Fatal("a decision proof was accepted for another decision")
			}
			if verifyResetDecisionProof(want, key, "XXXtestclient001", 6, challenge,
				tt.status, tt.resetRequired, bytes.Repeat([]byte{9}, 32)) {
				t.Fatal("a decision proof was accepted with another salt")
			}
		})
	}
}

// TestProofRejectsWrongKeyAndSizes: a malformed passkey or challenge must
// never produce a proof that a server could accept.
func TestProofRejectsWrongKeyAndSizes(t *testing.T) {
	if _, err := connectionProof(make([]byte, 31), "XXXtestclient001", 6, make([]byte, 32), false); err == nil {
		t.Fatal("connectionProof accepted a 31-byte key")
	}
	if _, err := connectionProof(make([]byte, 32), "XXXtestclient001", 6, make([]byte, 31), false); err == nil {
		t.Fatal("connectionProof accepted a 31-byte challenge")
	}
	key := make([]byte, 32)
	challenge := make([]byte, 32)
	if verifyResetDecisionProof(make([]byte, 32), key, "id", 6, challenge,
		protocol.ConnectStatus_NEW_CLIENT, true, nil) {
		t.Fatal("verifyResetDecisionProof accepted resetRequired without a salt")
	}
	if verifyResetDecisionProof(make([]byte, 32), key, "id", 6, challenge,
		protocol.ConnectStatus_NEW_CLIENT, false, make([]byte, 32)) {
		t.Fatal("verifyResetDecisionProof accepted a salt without resetRequired")
	}
}
