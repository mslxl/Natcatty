// Package model holds the shared domain vocabulary: the typed identifiers and
// values that every bounded context speaks.
package model

// SessionID identifies a Session for its lifetime. It is opaque: callers may
// compare and display it, never parse it.
type SessionID string
