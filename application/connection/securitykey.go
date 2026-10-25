package connection

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SecurityKeyDeviceDeadline bounds one hardware security key signature
// request. The device operation (touch, and PIN entry on the helper path) is
// handled by OpenSSH; the application only waits this long for the agent to
// return. A miss is a typed timeout, never a hung authentication attempt.
const SecurityKeyDeviceDeadline = 30 * time.Second

// The two OpenSSH security-key public-key algorithms. A security key is never
// signed in software: the private half is an agent-held handle that OpenSSH
// drives through the platform's FIDO stack.
const (
	// SecurityKeyAlgorithmEd25519 is the Ed25519 SK algorithm.
	SecurityKeyAlgorithmEd25519 = "sk-ssh-ed25519@openssh.com"
	// SecurityKeyAlgorithmECDSA is the ECDSA P-256 SK algorithm.
	SecurityKeyAlgorithmECDSA = "sk-ecdsa-sha2-nistp256@openssh.com"
)

// Sentinels that let the UI name a security-key-specific failure while the
// typed connection Kind stays the protocol-level category.
var (
	// ErrSecurityKeyUnsupported reports that the system OpenSSH agent (or
	// OpenSSH itself) is not available, so a hardware key cannot be used.
	ErrSecurityKeyUnsupported = errors.New("connection: security key needs the system OpenSSH agent")
	// ErrSecurityKeyDeviceTimeout reports that the device did not answer the
	// signature request within SecurityKeyDeviceDeadline.
	ErrSecurityKeyDeviceTimeout = errors.New("connection: security key device deadline expired")
	// ErrSecurityKeyProtocol reports a malformed agent identity or signature.
	ErrSecurityKeyProtocol = errors.New("connection: security key protocol failure")
)

// SecurityKey is one hardware-backed identity as the agent reports it. Blob is
// the exact SSH public-key wire blob (algorithm name, key bytes, and application
// string); it is never re-encoded, so the application string OpenSSH signed
// with is preserved.
type SecurityKey struct {
	Algorithm string
	Blob      []byte
	Comment   string
}

// SecurityKeySignature is the exact signature blob the agent returned. It
// carries the SK framing OpenSSH's PROTOCOL.u2f defines: the signature format,
// the raw signature, the SK flags (user presence), and the signature counter.
type SecurityKeySignature struct {
	Blob []byte
}

// SecurityKeyAgent is the consumer-owned port for the system OpenSSH agent's
// hardware-backed identities. Implementations must return key blobs and
// signature blobs byte-for-byte; they must not software-sign.
type SecurityKeyAgent interface {
	// SecurityKeys lists the agent's identities. It returns
	// ErrSecurityKeyUnsupported when no agent can be reached.
	SecurityKeys(ctx context.Context) ([]SecurityKey, error)
	// SignSecurityKey asks the agent to sign data with key. Implementations
	// preserve the returned SK framing exactly.
	SignSecurityKey(ctx context.Context, key SecurityKey, data []byte) (SecurityKeySignature, error)
}

// IsSecurityKeyAlgorithm reports whether name is one of the OpenSSH SK
// public-key algorithms.
func IsSecurityKeyAlgorithm(name string) bool {
	return name == SecurityKeyAlgorithmEd25519 || name == SecurityKeyAlgorithmECDSA
}

// SelectSecurityKeys returns only the agent's security-key identities, in the
// agent's order. Non-SK identities are left for the ordinary agent path.
func SelectSecurityKeys(keys []SecurityKey) []SecurityKey {
	selected := make([]SecurityKey, 0, len(keys))
	for _, key := range keys {
		if !IsSecurityKeyAlgorithm(key.Algorithm) || len(key.Blob) == 0 {
			continue
		}
		selected = append(selected, SecurityKey{
			Algorithm: key.Algorithm,
			Blob:      append([]byte(nil), key.Blob...),
			Comment:   key.Comment,
		})
	}
	return selected
}

// SecurityKeySummary is the display state of the system agent: whether it can
// be reached and how many hardware-backed identities it holds.
type SecurityKeySummary struct {
	Available bool
	Count     int
}

// SecurityKeyService applies the application's policy to a SecurityKeyAgent:
// it filters non-SK identities, enforces the device deadline, and maps failures
// onto typed connection errors. The zero Deadline uses SecurityKeyDeviceDeadline.
type SecurityKeyService struct {
	Agent    SecurityKeyAgent
	Deadline time.Duration
}

// Identities lists the hardware-backed identities. A missing agent is a typed
// unsupported failure, so the UI can say OpenSSH is required instead of
// claiming the credential was refused.
func (s SecurityKeyService) Identities(ctx context.Context) ([]SecurityKey, error) {
	if s.Agent == nil {
		return nil, securityKeyUnsupported(nil)
	}
	keys, err := s.Agent.SecurityKeys(ctx)
	if err != nil {
		if errors.Is(err, ErrSecurityKeyUnsupported) {
			return nil, securityKeyUnsupported(err)
		}
		if errors.Is(err, context.DeadlineExceeded) ||
			(ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
			return nil, &Error{
				Kind:  KindTimeout,
				Cause: fmt.Errorf("%w: %w", ErrSecurityKeyDeviceTimeout, err),
			}
		}
		return nil, &Error{Kind: KindInternal, Cause: err}
	}
	selected := SelectSecurityKeys(keys)
	return selected, nil
}

// Summary reports the agent state for the UI. It never fails: an unreachable
// agent is simply unavailable.
func (s SecurityKeyService) Summary(ctx context.Context) SecurityKeySummary {
	keys, err := s.Identities(ctx)
	if err != nil {
		return SecurityKeySummary{}
	}
	return SecurityKeySummary{Available: true, Count: len(keys)}
}

// Sign asks the agent to sign data and bounds the request by the device
// deadline. The returned blob is exactly what the agent produced.
func (s SecurityKeyService) Sign(ctx context.Context, key SecurityKey, data []byte) (SecurityKeySignature, error) {
	if s.Agent == nil {
		return SecurityKeySignature{}, securityKeyUnsupported(nil)
	}
	if !IsSecurityKeyAlgorithm(key.Algorithm) || len(key.Blob) == 0 {
		return SecurityKeySignature{}, securityKeyProtocol(nil)
	}

	deadline := s.Deadline
	if deadline <= 0 {
		deadline = SecurityKeyDeviceDeadline
	}
	signCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	signature, err := s.Agent.SignSecurityKey(signCtx, key, data)
	if err != nil {
		switch {
		case errors.Is(err, ErrSecurityKeyUnsupported):
			return SecurityKeySignature{}, securityKeyUnsupported(err)
		case errors.Is(err, context.DeadlineExceeded),
			errors.Is(signCtx.Err(), context.DeadlineExceeded):
			if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
				return SecurityKeySignature{}, &Error{Kind: KindCancelled, Cause: err}
			}
			return SecurityKeySignature{}, &Error{
				Kind:  KindTimeout,
				Cause: fmt.Errorf("%w: %w", ErrSecurityKeyDeviceTimeout, err),
			}
		case errors.Is(err, context.Canceled):
			return SecurityKeySignature{}, &Error{Kind: KindCancelled, Cause: err}
		default:
			return SecurityKeySignature{}, &Error{Kind: KindAuth, Cause: err}
		}
	}
	if len(signature.Blob) == 0 {
		return SecurityKeySignature{}, securityKeyProtocol(nil)
	}
	return SecurityKeySignature{Blob: append([]byte(nil), signature.Blob...)}, nil
}

// SecurityKeyUnsupported builds the typed unsupported failure for a missing
// OpenSSH agent. It is exported so the transport adapter can classify a failed
// agent dial without duplicating the cause join.
func SecurityKeyUnsupported(cause error) error { return securityKeyUnsupported(cause) }

// SecurityKeyTimeout builds the typed timeout failure for the device deadline.
func SecurityKeyTimeout(cause error) error {
	if cause == nil {
		cause = ErrSecurityKeyDeviceTimeout
	} else {
		cause = errors.Join(ErrSecurityKeyDeviceTimeout, cause)
	}
	return &Error{Kind: KindTimeout, Cause: cause}
}

func securityKeyUnsupported(cause error) error {
	if cause == nil {
		cause = ErrSecurityKeyUnsupported
	} else {
		cause = errors.Join(ErrSecurityKeyUnsupported, cause)
	}
	return &Error{Kind: KindUnsupported, Cause: cause}
}

func securityKeyProtocol(cause error) error {
	if cause == nil {
		cause = ErrSecurityKeyProtocol
	} else {
		cause = errors.Join(ErrSecurityKeyProtocol, cause)
	}
	return &Error{Kind: KindInternal, Cause: cause}
}
