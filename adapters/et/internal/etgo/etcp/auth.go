// SPDX-License-Identifier: Apache-2.0
package etcp

import (
	"crypto/subtle"
	"fmt"
	"strconv"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	"golang.org/x/crypto/blake2b"
)

// Sizes fixed by upstream CryptoHandler (src/base/CryptoHandler.hpp:15-16 at
// et-v7.1.1).
const (
	// authChallengeBytes is the fresh server challenge in the v7 handshake.
	authChallengeBytes = 32
	// epochSaltBytes is the length of the reset-recovery salt.
	epochSaltBytes = 32
)

// connectionProof answers a v7 authChallenge. It is a keyed BLAKE2b-256 over
// the client id, protocol version, challenge, and reset intent, exactly as
// CryptoHandler::connectionProof builds it
// (src/base/CryptoHandler.cpp:34-57,144-170 at et-v7.1.1). The key is the
// 32-byte session passkey.
func connectionProof(key []byte, clientID string, version int32, challenge []byte, resetIntent bool) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("etcp: connection proof key must be 32 bytes, got %d", len(key))
	}
	if len(challenge) != authChallengeBytes {
		return nil, fmt.Errorf("etcp: auth challenge must be %d bytes, got %d", authChallengeBytes, len(challenge))
	}
	message := connectionProofMessage(clientID, version, challenge, resetIntent)
	return keyedHash(key, message)
}

// connectionProofMessage mirrors upstream connectionProofMessage byte for
// byte, including the NUL separators and the decimal lengths.
func connectionProofMessage(clientID string, version int32, challenge []byte, resetIntent bool) []byte {
	message := append([]byte("EternalTerminal connect auth v2"), 0)
	message = strconv.AppendInt(message, int64(version), 10)
	message = append(message, 0)
	message = strconv.AppendInt(message, int64(len(clientID)), 10)
	message = append(message, 0)
	message = append(message, clientID...)
	message = append(message, 0)
	message = strconv.AppendInt(message, int64(len(challenge)), 10)
	message = append(message, 0)
	message = append(message, challenge...)
	message = append(message, 0)
	if resetIntent {
		message = append(message, '1')
	} else {
		message = append(message, '0')
	}
	return message
}

// verifyResetDecisionProof checks the authenticated status and reset decision
// in the final v7 ConnectResponse. The proof binds the challenge, so a captured
// response cannot be replayed into another handshake
// (src/base/CryptoHandler.cpp:60-113 at et-v7.1.1).
func verifyResetDecisionProof(proof, key []byte, clientID string, version int32, challenge []byte,
	status protocol.ConnectStatus, resetRequired bool, resetSalt []byte) bool {

	if len(proof) != blake2b.Size256 || len(key) != 32 || len(challenge) != authChallengeBytes {
		return false
	}
	if (resetRequired && len(resetSalt) != epochSaltBytes) || (!resetRequired && len(resetSalt) != 0) {
		return false
	}
	message := resetDecisionMessage(clientID, version, challenge, status, resetRequired, resetSalt)
	expected, err := keyedHash(key, message)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(proof, expected) == 1
}

// resetDecisionMessage mirrors upstream resetDecisionMessage. Unlike the
// connect proof, the client id is not length-prefixed.
func resetDecisionMessage(clientID string, version int32, challenge []byte,
	status protocol.ConnectStatus, resetRequired bool, resetSalt []byte) []byte {

	message := append([]byte("EternalTerminal reset decision v3"), 0)
	message = strconv.AppendInt(message, int64(version), 10)
	message = append(message, 0)
	message = append(message, clientID...)
	message = append(message, 0)
	message = strconv.AppendInt(message, int64(len(challenge)), 10)
	message = append(message, 0)
	message = append(message, challenge...)
	message = append(message, 0)
	message = strconv.AppendInt(message, int64(status), 10)
	message = append(message, 0)
	if resetRequired {
		message = append(message, '1')
	} else {
		message = append(message, '0')
	}
	message = append(message, 0)
	message = strconv.AppendInt(message, int64(len(resetSalt)), 10)
	message = append(message, 0)
	message = append(message, resetSalt...)
	return message
}

// ConnectionProof and ResetDecisionProof expose the two v7 proof
// constructions to the independent fake server and to integration tests.
// Production code uses connectionProof and verifyResetDecisionProof; these
// wrappers exist so a test server can check the client without copying the
// message formats. The algorithms are pinned against upstream vectors in
// auth_test.go.
func ConnectionProof(key []byte, clientID string, version int32, challenge []byte, resetIntent bool) ([]byte, error) {
	return connectionProof(key, clientID, version, challenge, resetIntent)
}

// ResetDecisionProof builds the proof a v7 server puts in its final
// ConnectResponse for the given decision.
func ResetDecisionProof(key []byte, clientID string, version int32, challenge []byte,
	status protocol.ConnectStatus, resetRequired bool, resetSalt []byte) ([]byte, error) {
	if len(key) != 32 || len(challenge) != authChallengeBytes {
		return nil, fmt.Errorf("etcp: reset decision proof needs a 32-byte key and challenge")
	}
	if (resetRequired && len(resetSalt) != epochSaltBytes) || (!resetRequired && len(resetSalt) != 0) {
		return nil, fmt.Errorf("etcp: reset decision salt does not match the reset decision")
	}
	return keyedHash(key, resetDecisionMessage(clientID, version, challenge, status, resetRequired, resetSalt))
}

func keyedHash(key, message []byte) ([]byte, error) {
	hash, err := blake2b.New256(key)
	if err != nil {
		return nil, fmt.Errorf("etcp: keyed BLAKE2b-256: %w", err)
	}
	_, _ = hash.Write(message)
	return hash.Sum(nil), nil
}
