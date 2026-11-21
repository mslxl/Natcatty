package rfb

import (
	"errors"
	"fmt"
)

var (
	// ErrHandshake reports a handshake that did not reach ServerInit.
	ErrHandshake = errors.New("rfb: handshake failed")
	// ErrUnsupportedVersion reports a server version this client cannot speak.
	ErrUnsupportedVersion = errors.New("rfb: unsupported protocol version")
	// ErrUnsupportedSecurity reports a server that offered no supported
	// security type.
	ErrUnsupportedSecurity = errors.New("rfb: no supported security type")
	// ErrSecurityRejected reports a server that failed or refused the
	// security handshake.
	ErrSecurityRejected = errors.New("rfb: security handshake rejected")
	// ErrMissingPassword reports that the server selected VNC password
	// authentication but the client was not given a password. The connection
	// fails without sending a guess.
	ErrMissingPassword = errors.New("rfb: server requires a VNC password")
	// ErrInvalidPixelFormat reports a pixel format the decoder cannot read.
	ErrInvalidPixelFormat = errors.New("rfb: invalid pixel format")
	// ErrFramebufferTooLarge reports a ServerInit size beyond the client's
	// bound, so a hostile server cannot exhaust memory.
	ErrFramebufferTooLarge = errors.New("rfb: framebuffer too large")
	// ErrInvalidRectangle reports a rectangle outside the framebuffer or
	// with a zero dimension.
	ErrInvalidRectangle = errors.New("rfb: invalid rectangle")
	// ErrUnsupportedEncoding reports a rectangle encoding this client did
	// not advertise and cannot decode.
	ErrUnsupportedEncoding = errors.New("rfb: unsupported encoding")
	// ErrMalformed reports a message that violates the wire framing.
	ErrMalformed = errors.New("rfb: malformed message")
	// ErrOversize reports a length field beyond the client's bound.
	ErrOversize = errors.New("rfb: oversized field")
)

// SecurityError describes a failed security negotiation. Offered holds the
// types the server advertised; Reason holds the server's failure text, when it
// supplies one. Rejected reports that the server refused the connection or a
// selected type failed, as opposed to offering only types this client does not
// implement.
type SecurityError struct {
	Offered  []SecurityType
	Reason   string
	Rejected bool
	// MissingPassword reports that the negotiated type needs a VNC password
	// the client was not given. The handshake stops before a challenge is
	// answered, so no guess is sent to the server.
	MissingPassword bool
}

// Error implements error.
func (e *SecurityError) Error() string {
	if e.MissingPassword {
		if len(e.Offered) > 0 {
			return fmt.Sprintf("rfb: %v requires a password but none is saved", e.Offered)
		}
		return ErrMissingPassword.Error()
	}
	rejected := e.Rejected || e.Reason != ""
	switch {
	case rejected && e.Reason != "" && len(e.Offered) > 0:
		return fmt.Sprintf("rfb: security handshake rejected: %s (offered %v)", e.Reason, e.Offered)
	case rejected && e.Reason != "":
		return fmt.Sprintf("rfb: security handshake rejected: %s", e.Reason)
	case rejected && len(e.Offered) > 0:
		return fmt.Sprintf("rfb: security handshake rejected (offered %v)", e.Offered)
	case rejected:
		return ErrSecurityRejected.Error()
	case len(e.Offered) > 0:
		return fmt.Sprintf("rfb: no supported security type (offered %v)", e.Offered)
	default:
		return ErrUnsupportedSecurity.Error()
	}
}

// Unwrap reports the nearest sentinel: a missing credential, a rejection, or
// an unsupported offer.
func (e *SecurityError) Unwrap() error {
	if e.MissingPassword {
		return ErrMissingPassword
	}
	if e.Rejected || e.Reason != "" {
		return ErrSecurityRejected
	}
	return ErrUnsupportedSecurity
}
