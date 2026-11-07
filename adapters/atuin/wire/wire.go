// Package wire implements the Atuin v18 record encoding and encryption used by
// the sync adapter. It owns Netcatty's MessagePack sync-record codec, PASETO
// v4.local, and PASERK k4.local-wrap.pie. The package is pinned to the Atuin
// client at commit
// ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e (v18.24.5); the official 18.24.2
// CLI still speaks the same wire format.
//
// Records are encrypted with a fresh content encryption key per record. The
// CEK is wrapped with the account master key using PASERK
// k4.local-wrap.pie; the payload is a PASETO v4.local token whose implicit
// assertion binds the record id, index, version, tag, and series host.
package wire

import "errors"

// Errors reported by this package. Callers map ErrKeyMismatch to the account
// key-mismatch state; a wrong key must never be treated as an absent record.
var (
	// ErrKeyMismatch reports that a record could not be authenticated with the
	// supplied master key. It covers a CEK key-id mismatch and every
	// authentication-tag failure.
	ErrKeyMismatch = errors.New("atuin wire: key mismatch")
	// ErrMalformed reports a structurally invalid encoding. It never means
	// "skip this record silently".
	ErrMalformed = errors.New("atuin wire: malformed encoding")
	// ErrUnsupportedVersion reports a history record version this package does
	// not decode. The pinned client accepts v0, v1, and v2.
	ErrUnsupportedVersion = errors.New("atuin wire: unsupported history version")
)
