// Package fakeagent is an in-process OpenSSH agent used by tests and the
// ticket 42 self-test. It speaks the agent wire protocol over a fresh
// net.Pipe per connection and can hold hardware-backed identities. Signing
// produces real SK-framed signatures over an ephemeral key, so an x/crypto SSH
// server verifies the application string, the user-presence flag, and the
// counter exactly as it would for a hardware key.
//
// There is no FIDO device here: the private key is software, the framing is
// real. Real-hardware parity remains a documented gap.
package fakeagent

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"

	xssh "golang.org/x/crypto/ssh"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

const (
	agentRequestIdentities = 11
	agentIdentitiesAnswer  = 12
	agentSignRequest       = 13
	agentSignResponse      = 14
	agentFailure           = 5

	maxFrame = 1 << 20

	// signature flags: the user-presence bit, as OpenSSH reports it.
	userPresence = 0x01
	// signatureCounter is deliberately neither zero nor one, so a test can
	// prove the counter travelled through the whole path unmodified.
	signatureCounter = 7
)

// Identity is one agent identity. Blob is the exact SSH public-key wire blob.
type Identity struct {
	Algorithm   string
	Application string
	Comment     string
	Blob        []byte

	private  ed25519.PrivateKey
	ecdsaKey *ecdsa.PrivateKey
}

// Ed25519SK returns an sk-ssh-ed25519@openssh.com identity with application
// "ssh:".
func Ed25519SK(comment string) Identity { return ed25519SK(comment, "ssh:") }

// Ed25519SKWithApplication returns an Ed25519 SK identity with a custom
// application string, used to prove the application string is preserved.
func Ed25519SKWithApplication(comment, application string) Identity {
	return ed25519SK(comment, application)
}

func ed25519SK(comment, application string) Identity {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("fakeagent: generate Ed25519 key: %v", err))
	}
	blob := xssh.Marshal(struct {
		Name        string
		Key         []byte
		Application string
	}{appconnection.SecurityKeyAlgorithmEd25519, public, application})
	return Identity{
		Algorithm:   appconnection.SecurityKeyAlgorithmEd25519,
		Application: application,
		Comment:     comment,
		Blob:        blob,
		private:     private,
	}
}

// ECDSASK returns an sk-ecdsa-sha2-nistp256@openssh.com identity.
func ECDSASK(comment string) Identity {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("fakeagent: generate ECDSA key: %v", err))
	}
	application := "ssh:"
	blob := xssh.Marshal(struct {
		Name        string
		Curve       string
		Key         []byte
		Application string
	}{
		appconnection.SecurityKeyAlgorithmECDSA,
		"nistp256",
		elliptic.Marshal(key.Curve, key.X, key.Y),
		application,
	})
	return Identity{
		Algorithm:   appconnection.SecurityKeyAlgorithmECDSA,
		Application: application,
		Comment:     comment,
		Blob:        blob,
		ecdsaKey:    key,
	}
}

// SoftwareEd25519 returns a non-SK identity. The security key path must leave
// it to the ordinary agent path.
func SoftwareEd25519(comment string) Identity {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("fakeagent: generate Ed25519 key: %v", err))
	}
	return Identity{
		Algorithm: "ssh-ed25519",
		Comment:   comment,
		Blob: xssh.Marshal(struct {
			Name string
			Key  []byte
		}{"ssh-ed25519", public}),
		private: private,
	}
}

// SignRequest is one recorded SSH_AGENTC_SIGN_REQUEST.
type SignRequest struct {
	KeyBlob []byte
	Data    []byte
	Flags   uint32
}

// Option customizes an Agent.
type Option func(*Agent)

// WithIdentity adds one identity, preserving the order options are applied.
func WithIdentity(identity Identity) Option {
	return func(a *Agent) {
		a.identities = append(a.identities, identity)
	}
}

// WithBlockSign accepts signing requests but never answers them. The client
// must interrupt the request (by closing the connection or expiring its
// context), which is how the device deadline is exercised.
func WithBlockSign() Option { return func(a *Agent) { a.blockSign = true } }

// WithFailSign answers signing requests with SSH_AGENT_FAILURE.
func WithFailSign() Option { return func(a *Agent) { a.failSign = true } }

// WithFailList answers identity requests with SSH_AGENT_FAILURE.
func WithFailList() Option { return func(a *Agent) { a.failList = true } }

// Agent is a fake OpenSSH agent. Each Dial creates a fresh in-memory
// connection and serves one client at a time on it.
type Agent struct {
	identities []Identity
	blockSign  bool
	failSign   bool
	failList   bool

	mu           sync.Mutex
	dials        int
	listRequests int
	signRequests []SignRequest
}

// New returns an agent holding identities in the given order.
func New(identities ...Identity) *Agent {
	return &Agent{identities: append([]Identity(nil), identities...)}
}

// NewWithOptions returns an agent configured by options, including failure and
// blocking modes.
func NewWithOptions(opts ...Option) *Agent {
	agent := &Agent{}
	for _, opt := range opts {
		opt(agent)
	}
	return agent
}

// Default returns an agent with one Ed25519 SK identity.
func Default() *Agent { return New(Ed25519SK("fake-sk")) }

// Dial implements the adapter's agent dial seam.
func (a *Agent) Dial(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, server := net.Pipe()
	a.mu.Lock()
	a.dials++
	a.mu.Unlock()
	go a.serve(server)
	return client, nil
}

// Dials reports how many connections were opened.
func (a *Agent) Dials() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dials
}

// ListRequests reports how many identity listings were served.
func (a *Agent) ListRequests() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.listRequests
}

// SignRequests returns a copy of every recorded signing request.
func (a *Agent) SignRequests() []SignRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]SignRequest, len(a.signRequests))
	for i, req := range a.signRequests {
		out[i] = SignRequest{
			KeyBlob: append([]byte(nil), req.KeyBlob...),
			Data:    append([]byte(nil), req.Data...),
			Flags:   req.Flags,
		}
	}
	return out
}

// SecurityKeys returns the identities as the application port sees them.
func (a *Agent) SecurityKeys() []appconnection.SecurityKey {
	out := make([]appconnection.SecurityKey, 0, len(a.identities))
	for _, identity := range a.identities {
		out = append(out, appconnection.SecurityKey{
			Algorithm: identity.Algorithm,
			Blob:      append([]byte(nil), identity.Blob...),
			Comment:   identity.Comment,
		})
	}
	return out
}

// PublicKeys returns the SK identities parsed by x/crypto, so a test SSH server
// can authorize them.
func (a *Agent) PublicKeys() []xssh.PublicKey {
	var keys []xssh.PublicKey
	for _, identity := range a.identities {
		if !appconnection.IsSecurityKeyAlgorithm(identity.Algorithm) {
			continue
		}
		key, err := xssh.ParsePublicKey(identity.Blob)
		if err != nil {
			panic(fmt.Sprintf("fakeagent: parse %s: %v", identity.Algorithm, err))
		}
		keys = append(keys, key)
	}
	return keys
}

func (a *Agent) serve(conn net.Conn) {
	defer conn.Close()
	for {
		payload, err := readFrame(conn)
		if err != nil || len(payload) == 0 {
			return
		}
		switch payload[0] {
		case agentRequestIdentities:
			a.mu.Lock()
			a.listRequests++
			fail := a.failList
			a.mu.Unlock()
			if fail {
				_ = writeFrame(conn, []byte{agentFailure})
				continue
			}
			_ = writeFrame(conn, a.identitiesAnswer())
		case agentSignRequest:
			request, err := parseSignRequest(payload)
			if err != nil {
				_ = writeFrame(conn, []byte{agentFailure})
				continue
			}
			a.mu.Lock()
			a.signRequests = append(a.signRequests, request)
			block, fail := a.blockSign, a.failSign
			a.mu.Unlock()
			if block {
				// Hold the request until the client closes the connection. The
				// adapter's deadline watchdog is what releases it.
				_, _ = io.Copy(io.Discard, conn)
				return
			}
			if fail {
				_ = writeFrame(conn, []byte{agentFailure})
				continue
			}
			signature, err := a.sign(request)
			if err != nil {
				_ = writeFrame(conn, []byte{agentFailure})
				continue
			}
			_ = writeFrame(conn, signature)
		default:
			_ = writeFrame(conn, []byte{agentFailure})
		}
	}
}

func (a *Agent) identitiesAnswer() []byte {
	payload := make([]byte, 1, 64)
	payload[0] = agentIdentitiesAnswer
	payload = appendUint32(payload, uint32(len(a.identities)))
	for _, identity := range a.identities {
		payload = appendString(payload, identity.Blob)
		payload = appendString(payload, []byte(identity.Comment))
	}
	return payload
}

func (a *Agent) sign(request SignRequest) ([]byte, error) {
	var identity *Identity
	for i := range a.identities {
		if string(a.identities[i].Blob) == string(request.KeyBlob) {
			identity = &a.identities[i]
			break
		}
	}
	if identity == nil {
		return nil, fmt.Errorf("fakeagent: unknown key blob")
	}

	// The signed layout from PROTOCOL.u2f: SHA256(application), flags, counter,
	// SHA256(message). No extensions are defined for SSH.
	applicationDigest := sha256.Sum256([]byte(identity.Application))
	messageDigest := sha256.Sum256(request.Data)
	signed := make([]byte, 0, len(applicationDigest)+5+len(messageDigest))
	signed = append(signed, applicationDigest[:]...)
	signed = append(signed, userPresence)
	signed = appendUint32(signed, signatureCounter)
	signed = append(signed, messageDigest[:]...)

	var raw []byte
	switch {
	case identity.private != nil:
		raw = ed25519.Sign(identity.private, signed)
	case identity.ecdsaKey != nil:
		digest := sha256.Sum256(signed)
		r, s, err := ecdsa.Sign(rand.Reader, identity.ecdsaKey, digest[:])
		if err != nil {
			return nil, err
		}
		raw = xssh.Marshal(struct {
			R *big.Int
			S *big.Int
		}{r, s})
	default:
		return nil, fmt.Errorf("fakeagent: identity has no private key")
	}

	tail := make([]byte, 0, 5)
	tail = append(tail, userPresence)
	tail = appendUint32(tail, signatureCounter)
	wire := xssh.Marshal(struct {
		Format string
		Blob   []byte
		Rest   []byte `ssh:"rest"`
	}{identity.Algorithm, raw, tail})

	response := make([]byte, 1, 1+4+len(wire))
	response[0] = agentSignResponse
	return appendString(response, wire), nil
}

func parseSignRequest(payload []byte) (SignRequest, error) {
	if len(payload) < 1 || payload[0] != agentSignRequest {
		return SignRequest{}, fmt.Errorf("fakeagent: not a sign request")
	}
	data := payload[1:]
	keyBlob, data, err := readString(data)
	if err != nil {
		return SignRequest{}, err
	}
	challenge, data, err := readString(data)
	if err != nil {
		return SignRequest{}, err
	}
	if len(data) != 4 {
		return SignRequest{}, fmt.Errorf("fakeagent: sign request flags length %d", len(data))
	}
	return SignRequest{
		KeyBlob: append([]byte(nil), keyBlob...),
		Data:    append([]byte(nil), challenge...),
		Flags:   binary.BigEndian.Uint32(data),
	}, nil
}

func readString(data []byte) (value, rest []byte, err error) {
	if len(data) < 4 {
		return nil, nil, fmt.Errorf("fakeagent: truncated string length")
	}
	length := binary.BigEndian.Uint32(data)
	data = data[4:]
	if uint64(length) > uint64(len(data)) {
		return nil, nil, fmt.Errorf("fakeagent: string length exceeds the frame")
	}
	return data[:length], data[length:], nil
}

func appendString(dst, value []byte) []byte {
	dst = appendUint32(dst, uint32(len(value)))
	return append(dst, value...)
}

func appendUint32(dst []byte, value uint32) []byte {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	return append(dst, encoded[:]...)
}

func writeFrame(w io.Writer, payload []byte) error {
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	_, err := w.Write(frame)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > maxFrame {
		return nil, fmt.Errorf("fakeagent: frame length %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}
