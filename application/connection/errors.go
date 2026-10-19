package connection

import (
	"errors"
	"fmt"
)

// Kind classifies a connection failure so the UI can choose a localized
// message. The values mirror the original's observable failure distinctions.
type Kind string

const (
	// KindAuth reports refused credentials or unavailable saved credentials.
	KindAuth Kind = "auth"
	// KindNetwork reports an unreachable or refused endpoint.
	KindNetwork Kind = "network"
	// KindTimeout reports the TCP dial or the authentication-ready deadline.
	KindTimeout Kind = "timeout"
	// KindHostKey reports a host key the user rejected or could not be
	// verified. It is raised before authentication begins.
	KindHostKey Kind = "host_key"
	// KindUnsupported reports an algorithm this transport cannot negotiate.
	KindUnsupported Kind = "unsupported"
	// KindCancelled reports an operation the caller cancelled.
	KindCancelled Kind = "cancelled"
	// KindInternal reports a local failure such as a storage error.
	KindInternal Kind = "internal"
)

// Error is a typed connection error. Cause carries the transport or storage
// detail; it is never user-visible text. Hop identifies the chain endpoint
// that failed when the failure happened during a jump chain. Error() is a
// developer diagnostic; the UI localizes Kind and Hop.Label instead.
type Error struct {
	Kind  Kind
	Cause error
	Hop   *HopInfo
}

func (e *Error) Error() string {
	message := fmt.Sprintf("connection: %s", e.Kind)
	if e.Cause != nil {
		message += fmt.Sprintf(": %v", e.Cause)
	}
	return message + hopErrorText(e.Hop)
}

func (e *Error) Unwrap() error { return e.Cause }

// Sentinels the application and the SSH adapter use to classify failures.
var (
	// ErrHostKeyRejected reports that the user rejected the server's key, or
	// that no trust decision could be made.
	ErrHostKeyRejected = errors.New("connection: host key rejected")
	// ErrAuthentication reports refused credentials.
	ErrAuthentication = errors.New("connection: authentication failed")
	// ErrDialTimeout reports that the TCP connection did not establish in
	// time.
	ErrDialTimeout = errors.New("connection: dial timed out")
	// ErrAuthTimeout reports that authentication did not become ready in
	// time.
	ErrAuthTimeout = errors.New("connection: authentication timed out")
	// ErrNetwork reports an unreachable, refused, or reset endpoint.
	ErrNetwork = errors.New("connection: network failure")
	// ErrUnsupportedAlgorithm reports a negotiation this transport cannot
	// perform.
	ErrUnsupportedAlgorithm = errors.New("connection: unsupported algorithm")
	// ErrNoCredentials reports that the Host has no saved password.
	ErrNoCredentials = errors.New("connection: no saved credential")
	// ErrNoDialer reports a Manager constructed without an SSH dialer.
	ErrNoDialer = errors.New("connection: no dialer")
	// ErrClosed reports an operation on a released lease or closed
	// Connection.
	ErrClosed = errors.New("connection: closed")
	// ErrPromptCancelled reports that the user dismissed a passphrase or
	// keyboard-interactive prompt.
	ErrPromptCancelled = errors.New("connection: authentication prompt cancelled")
	// ErrPromptUnavailable reports that the server requires interactive input
	// but no prompt implementation is wired.
	ErrPromptUnavailable = errors.New("connection: authentication prompt unavailable")
	// ErrKeyUnavailable reports a selected SSH Key that no longer exists or has
	// no stored private material. Explicit key and certificate modes fail
	// closed instead of substituting another credential.
	ErrKeyUnavailable = errors.New("connection: selected SSH Key unavailable")
	// ErrUnsupportedProxy reports a proxy transport this adapter does not
	// implement.
	ErrUnsupportedProxy = errors.New("connection: unsupported proxy type")
	// ErrInvalidProxy reports a proxy configuration the transport cannot use.
	ErrInvalidProxy = errors.New("connection: invalid proxy configuration")
	// ErrProxyHandshake reports a proxy that accepted the connection but
	// refused or malformed its tunnel handshake.
	ErrProxyHandshake = errors.New("connection: proxy handshake failed")
)

// AuthFailure wraps cause as an authentication error. AuthSource
// implementations use it so the Manager reports a missing or unusable
// credential as a typed auth failure rather than an internal error.
func AuthFailure(cause error) error { return authError(cause) }

func hostKeyError(cause error) error     { return &Error{Kind: KindHostKey, Cause: cause} }
func authError(cause error) error        { return &Error{Kind: KindAuth, Cause: cause} }
func networkError(cause error) error     { return &Error{Kind: KindNetwork, Cause: cause} }
func timeoutError(cause error) error     { return &Error{Kind: KindTimeout, Cause: cause} }
func unsupportedError(cause error) error { return &Error{Kind: KindUnsupported, Cause: cause} }
func cancelledError(cause error) error   { return &Error{Kind: KindCancelled, Cause: cause} }
func internalError(cause error) error    { return &Error{Kind: KindInternal, Cause: cause} }
