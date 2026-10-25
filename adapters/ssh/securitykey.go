package ssh

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	xssh "golang.org/x/crypto/ssh"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// SSH agent protocol message numbers from OpenSSH PROTOCOL.agent. The
// security-key path speaks them directly instead of using x/crypto's agent
// client, because the SK signature carries trailing fields (flags and counter)
// that must be preserved exactly.
const (
	agentRequestIdentities = 11
	agentIdentitiesAnswer  = 12
	agentSignRequest       = 13
	agentSignResponse      = 14
	agentFailure           = 5

	// maxAgentFrame bounds one agent message. Identity lists and signatures are
	// small; a larger frame is a protocol violation, not a slower device.
	maxAgentFrame = 1 << 20

	// securityKeySignatureTail is the number of trailing bytes after the raw
	// signature: one flags byte and a uint32 counter.
	securityKeySignatureTail = 5
)

// AgentSecurityKeyAgent is the system OpenSSH agent viewed as a hardware
// security key provider. It lists the agent's identities and forwards signing
// requests, but it never software-signs and never re-encodes a key or signature
// blob.
type AgentSecurityKeyAgent struct {
	dial AgentDialFunc
}

// SecurityKeyAgentOption customizes an AgentSecurityKeyAgent.
type SecurityKeyAgentOption func(*AgentSecurityKeyAgent)

// WithSecurityKeyAgentDial replaces the agent transport. Production uses
// SSH_AUTH_SOCK on Unix and the OpenSSH named pipe on Windows.
func WithSecurityKeyAgentDial(dial AgentDialFunc) SecurityKeyAgentOption {
	return func(a *AgentSecurityKeyAgent) {
		if dial != nil {
			a.dial = dial
		}
	}
}

// NewSecurityKeyAgent returns an agent-backed security key provider.
func NewSecurityKeyAgent(opts ...SecurityKeyAgentOption) *AgentSecurityKeyAgent {
	agent := &AgentSecurityKeyAgent{dial: defaultAgentDial}
	for _, opt := range opts {
		opt(agent)
	}
	return agent
}

var _ appconnection.SecurityKeyAgent = (*AgentSecurityKeyAgent)(nil)

// SecurityKeys lists every identity the agent holds. Non-SK identities are
// included so application/connection owns the filtering policy; the agent's
// order and the exact public-key blobs are preserved.
func (a *AgentSecurityKeyAgent) SecurityKeys(ctx context.Context) ([]appconnection.SecurityKey, error) {
	payload, err := a.roundTrip(ctx, []byte{agentRequestIdentities})
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, securityKeyProtocolError("empty identities answer")
	}
	switch payload[0] {
	case agentIdentitiesAnswer:
		return parseSecurityKeyIdentities(payload)
	case agentFailure:
		return nil, errors.New("ssh: the agent refused to list identities")
	default:
		return nil, securityKeyProtocolError(fmt.Sprintf("unexpected identities answer type %d", payload[0]))
	}
}

// SignSecurityKey asks the agent to sign data with key. The returned blob is
// the agent's exact SSH signature blob, including the SK flags and counter.
func (a *AgentSecurityKeyAgent) SignSecurityKey(ctx context.Context, key appconnection.SecurityKey, data []byte) (appconnection.SecurityKeySignature, error) {
	if !appconnection.IsSecurityKeyAlgorithm(key.Algorithm) || len(key.Blob) == 0 {
		return appconnection.SecurityKeySignature{}, securityKeyProtocolError("not a security key identity")
	}
	payload, err := a.roundTrip(ctx, securityKeySignRequestPayload(key.Blob, data))
	if err != nil {
		return appconnection.SecurityKeySignature{}, err
	}
	if len(payload) == 0 {
		return appconnection.SecurityKeySignature{}, securityKeyProtocolError("empty sign answer")
	}
	switch payload[0] {
	case agentSignResponse:
		if len(payload) < 5 {
			return appconnection.SecurityKeySignature{}, securityKeyProtocolError("truncated sign answer")
		}
		length := binary.BigEndian.Uint32(payload[1:5])
		if uint64(length) != uint64(len(payload)-5) {
			return appconnection.SecurityKeySignature{}, securityKeyProtocolError("sign answer length mismatch")
		}
		// Validate the framing and prove it round-trips byte-for-byte before the
		// blob is offered to x/crypto.
		blob := payload[5:]
		if _, err := ParseSecurityKeySignature(blob); err != nil {
			return appconnection.SecurityKeySignature{}, err
		}
		return appconnection.SecurityKeySignature{Blob: append([]byte(nil), blob...)}, nil
	case agentFailure:
		return appconnection.SecurityKeySignature{}, errors.New("ssh: the agent refused to sign with the security key")
	default:
		return appconnection.SecurityKeySignature{}, securityKeyProtocolError(fmt.Sprintf("unexpected sign answer type %d", payload[0]))
	}
}

// roundTrip sends one request over a fresh agent connection. The connection is
// closed when ctx ends, which is how the 30-second device deadline interrupts a
// blocked read: net.Pipe and the Windows agent pipe both unblock on close.
func (a *AgentSecurityKeyAgent) roundTrip(ctx context.Context, payload []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := a.dial(ctx)
	if err != nil {
		// Every dial failure means the system OpenSSH agent cannot be reached.
		return nil, appconnection.SecurityKeyUnsupported(err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		// Best effort: not every agent transport supports deadlines. Closing the
		// connection from the watchdog below is the authoritative release.
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if err := writeAgentFrame(conn, payload); err != nil {
		return nil, ctxOrProtocolError(ctx, err)
	}
	answer, err := readAgentFrame(conn)
	if err != nil {
		return nil, ctxOrProtocolError(ctx, err)
	}
	return answer, nil
}

func ctxOrProtocolError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	// The connection deadline is set from ctx, so a transport timeout means the
	// context deadline fired; the context's own timer may not have run yet.
	var transportTimeout interface{ Timeout() bool }
	if _, hasDeadline := ctx.Deadline(); hasDeadline &&
		errors.As(err, &transportTimeout) && transportTimeout.Timeout() {
		return context.DeadlineExceeded
	}
	return securityKeyProtocolErrorf("agent transport: %v", err)
}

// ParseSecurityKeySignature parses one OpenSSH SK signature blob into the
// x/crypto Signature shape, keeping the flags and counter in Rest so that
// x/crypto's Marshal emits the original bytes unchanged. The round-trip is
// checked here; a mismatch is a protocol failure rather than a silently
// rewritten signature.
func ParseSecurityKeySignature(blob []byte) (*xssh.Signature, error) {
	var wire struct {
		Format string
		Blob   []byte
		Rest   []byte `ssh:"rest"`
	}
	if err := xssh.Unmarshal(blob, &wire); err != nil {
		return nil, securityKeyProtocolErrorf("signature framing: %v", err)
	}
	if !appconnection.IsSecurityKeyAlgorithm(wire.Format) {
		return nil, securityKeyProtocolErrorf("signature format %q is not a security key algorithm", wire.Format)
	}
	if len(wire.Rest) != securityKeySignatureTail {
		return nil, securityKeyProtocolErrorf("security key signature tail is %d bytes, want %d",
			len(wire.Rest), securityKeySignatureTail)
	}
	signature := &xssh.Signature{
		Format: wire.Format,
		Blob:   append([]byte(nil), wire.Blob...),
		Rest:   append([]byte(nil), wire.Rest...),
	}
	if !bytes.Equal(xssh.Marshal(signature), blob) {
		return nil, securityKeyProtocolError("security key signature does not round-trip")
	}
	return signature, nil
}

// securityKeySigner is the x/crypto Signer for one agent-held SK identity. Its
// public key is the agent's exact blob, and Sign returns the agent's exact
// signature framing.
type securityKeySigner struct {
	ctx     context.Context
	service appconnection.SecurityKeyService
	key     appconnection.SecurityKey
	public  xssh.PublicKey
	record  func(error)
}

func newSecurityKeySigner(ctx context.Context, service appconnection.SecurityKeyService,
	key appconnection.SecurityKey, record func(error)) (*securityKeySigner, error) {
	public, err := xssh.ParsePublicKey(key.Blob)
	if err != nil {
		return nil, securityKeyProtocolErrorf("parse %s public key: %v", key.Algorithm, err)
	}
	if public.Type() != key.Algorithm {
		return nil, securityKeyProtocolErrorf("public key type %q does not match algorithm %q",
			public.Type(), key.Algorithm)
	}
	if !bytes.Equal(public.Marshal(), key.Blob) {
		return nil, securityKeyProtocolError("security key public blob does not round-trip")
	}
	return &securityKeySigner{ctx: ctx, service: service, key: key, public: public, record: record}, nil
}

func (s *securityKeySigner) PublicKey() xssh.PublicKey { return s.public }

func (s *securityKeySigner) Sign(_ io.Reader, data []byte) (*xssh.Signature, error) {
	result, err := s.service.Sign(s.ctx, s.key, data)
	if err != nil {
		// x/crypto remembers a method error and retries the auth callback; it
		// only returns the error if no method is left. Record the actionable
		// reason so the callback can report the typed timeout at exhaustion.
		if s.record != nil {
			s.record(err)
		}
		return nil, err
	}
	signature, err := ParseSecurityKeySignature(result.Blob)
	if err != nil {
		return nil, err
	}
	if signature.Format != s.key.Algorithm {
		return nil, securityKeyProtocolErrorf("signed with %q for a %q key",
			signature.Format, s.key.Algorithm)
	}
	return signature, nil
}

// securityKeySignRequestPayload builds SSH_AGENTC_SIGN_REQUEST. The flags field
// is zero: OpenSSH decides user presence from the key's own metadata and never
// takes it from this field.
func securityKeySignRequestPayload(keyBlob, data []byte) []byte {
	payload := make([]byte, 1, 1+4+len(keyBlob)+4+len(data)+4)
	payload[0] = agentSignRequest
	payload = appendAgentString(payload, keyBlob)
	payload = appendAgentString(payload, data)
	return append(payload, 0, 0, 0, 0)
}

func parseSecurityKeyIdentities(payload []byte) ([]appconnection.SecurityKey, error) {
	data := payload[1:]
	if len(data) < 4 {
		return nil, securityKeyProtocolError("truncated identities answer")
	}
	count := binary.BigEndian.Uint32(data[:4])
	data = data[4:]
	// Each identity carries at least two length prefixes.
	if uint64(count) > uint64(len(data))/8 {
		return nil, securityKeyProtocolErrorf("identity count %d exceeds the answer", count)
	}
	keys := make([]appconnection.SecurityKey, 0, count)
	for i := uint32(0); i < count; i++ {
		blob, rest, err := readAgentString(data)
		if err != nil {
			return nil, err
		}
		comment, rest, err := readAgentString(rest)
		if err != nil {
			return nil, err
		}
		algorithm, _, err := readAgentString(blob)
		if err != nil {
			return nil, securityKeyProtocolErrorf("identity %d blob: %v", i, err)
		}
		keys = append(keys, appconnection.SecurityKey{
			Algorithm: string(algorithm),
			Blob:      append([]byte(nil), blob...),
			Comment:   string(comment),
		})
		data = rest
	}
	if len(data) != 0 {
		return nil, securityKeyProtocolError("trailing bytes in identities answer")
	}
	return keys, nil
}

func readAgentString(data []byte) (value, rest []byte, err error) {
	if len(data) < 4 {
		return nil, nil, securityKeyProtocolError("truncated string length")
	}
	length := binary.BigEndian.Uint32(data[:4])
	data = data[4:]
	if uint64(length) > uint64(len(data)) {
		return nil, nil, securityKeyProtocolError("string length exceeds the frame")
	}
	return data[:length], data[length:], nil
}

func appendAgentString(dst, value []byte) []byte {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	dst = append(dst, length[:]...)
	return append(dst, value...)
}

func writeAgentFrame(w io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxAgentFrame {
		return securityKeyProtocolErrorf("agent request length %d", len(payload))
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	_, err := w.Write(frame)
	return err
}

func readAgentFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > maxAgentFrame {
		return nil, securityKeyProtocolErrorf("agent answer length %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// securityKeyProtocolError uses the application's typed internal category so a
// framing bug is never mistaken for a rejected credential.
func securityKeyProtocolError(message string) error {
	return &appconnection.Error{
		Kind:  appconnection.KindInternal,
		Cause: fmt.Errorf("%w: %s", appconnection.ErrSecurityKeyProtocol, message),
	}
}

func securityKeyProtocolErrorf(format string, args ...any) error {
	return &appconnection.Error{
		Kind:  appconnection.KindInternal,
		Cause: fmt.Errorf("%w: %s", appconnection.ErrSecurityKeyProtocol, fmt.Sprintf(format, args...)),
	}
}
