// Package atuin holds the shared vocabulary of the Atuin sync integration:
// authentication modes, account state, and the sync status vocabulary used by
// the preferences document, the sync service, and the UI.
//
// Atuin's own terms are used only where they describe the protocol. Local
// Command History stays plaintext; the Atuin master key protects wire payloads
// only.
package atuin

// AuthMode selects the Authorization header the Atuin HTTP API receives.
// Session is the traditional CLI session token sent as `Token`; Bearer is a
// Hub token sent as `Bearer`.
type AuthMode string

const (
	// AuthModeSession sends `Authorization: Token <session>`.
	AuthModeSession AuthMode = "session"
	// AuthModeBearer sends `Authorization: Bearer <token>`.
	AuthModeBearer AuthMode = "bearer"
)

// Valid reports whether mode is one this build knows.
func (mode AuthMode) Valid() bool {
	return mode == AuthModeSession || mode == AuthModeBearer
}

// NormalizeAuthMode replaces an unknown or empty persisted value with the
// session default.
func NormalizeAuthMode(mode AuthMode) AuthMode {
	if mode.Valid() {
		return mode
	}
	return AuthModeSession
}

// SyncState is the account and sync status shown to the user. It names the
// state, not a localized sentence.
type SyncState string

const (
	// SyncDisabled reports that the user turned history sync off.
	SyncDisabled SyncState = "disabled"
	// SyncNotConfigured reports that the account, token, or master key is
	// missing.
	SyncNotConfigured SyncState = "not_configured"
	// SyncLocked reports a passphrase-locked field-secret store.
	SyncLocked SyncState = "locked"
	// SyncIdle reports a configured account with no active sync. Ticket 13
	// validates the account and key but does not sync records.
	SyncIdle SyncState = "idle"
	// SyncSyncing reports an operation in progress.
	SyncSyncing SyncState = "syncing"
	// SyncOffline reports that the server could not be reached.
	SyncOffline SyncState = "offline"
	// SyncAuthExpired reports that the session token is no longer accepted.
	SyncAuthExpired SyncState = "auth_expired"
	// SyncKeyMismatch reports that the master key cannot decrypt the
	// account's records.
	SyncKeyMismatch SyncState = "key_mismatch"
	// SyncIncompatibleServer reports an unknown server major version or a
	// capability this client cannot satisfy.
	SyncIncompatibleServer SyncState = "incompatible_server"
	// SyncError reports any other failure.
	SyncError SyncState = "error"
)

// Problem is a stable problem code. The UI localizes it; it is never a raw
// server or command string.
type Problem string

const (
	ProblemNone               Problem = ""
	ProblemDisabled           Problem = "disabled"
	ProblemNotConfigured      Problem = "not_configured"
	ProblemLocked             Problem = "locked"
	ProblemAuthExpired        Problem = "auth_expired"
	ProblemKeyMismatch        Problem = "key_mismatch"
	ProblemIncompatibleServer Problem = "incompatible_server"
	ProblemOffline            Problem = "offline"
	ProblemError              Problem = "error"
	ProblemKeyMissing         Problem = "key_missing"
	ProblemKeyUnverified      Problem = "key_unverified"
)

// KeyState reports what is known about the stored master key.
type KeyState string

const (
	// KeyStateNone reports that no key or fingerprint has been stored.
	KeyStateNone KeyState = "none"
	// KeyStateMissing reports a saved fingerprint whose key is not in the
	// field-secret store, for example after logout or a key loss.
	KeyStateMissing KeyState = "missing"
	// KeyStateUnverified reports a key accepted for an account with no record
	// to validate against. The user must confirm the warning before the first
	// upload.
	KeyStateUnverified KeyState = "unverified"
	// KeyStateVerified reports a key that decrypted an existing record.
	KeyStateVerified KeyState = "verified"
	// KeyStateMismatch reports a key that failed to decrypt an existing
	// record.
	KeyStateMismatch KeyState = "mismatch"
)
