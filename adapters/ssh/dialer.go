// Package ssh is the x/crypto/ssh transport adapter. It dials one endpoint,
// verifies the host key through the application's callback, authenticates with
// a password, and opens interactive shell channels. It owns no trust state and
// makes no trust decision: the callback runs before authentication and its
// error aborts the handshake.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	xssh "golang.org/x/crypto/ssh"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
)

// Defaults mirror the original transport's separate timers: 20 seconds to
// establish TCP and 120 seconds to become authenticated.
const (
	defaultDialTimeout = 20 * time.Second
	defaultAuthTimeout = 120 * time.Second
)

// DialFunc establishes the raw transport. Tests replace it to use an
// in-process SSH server.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Dialer opens authenticated SSH transports with x/crypto/ssh.
type Dialer struct {
	dial         DialFunc
	dialTimeout  time.Duration
	authTimeout  time.Duration
	ciphers      []string
	keyExchanges []string
	macs         []string
	hostKeyAlgos []string
}

// Option customizes a Dialer.
type Option func(*Dialer)

// WithDialFunc replaces the TCP dialer.
func WithDialFunc(fn DialFunc) Option {
	return func(d *Dialer) {
		if fn != nil {
			d.dial = fn
		}
	}
}

// WithTimeouts sets the TCP dial and authentication-ready deadlines. A zero
// duration keeps the default for that timer.
func WithTimeouts(dialTimeout, authTimeout time.Duration) Option {
	return func(d *Dialer) {
		if dialTimeout > 0 {
			d.dialTimeout = dialTimeout
		}
		if authTimeout > 0 {
			d.authTimeout = authTimeout
		}
	}
}

// WithAlgorithms overrides the negotiated offers. A nil or empty list keeps
// the default for that category.
func WithAlgorithms(ciphers, keyExchanges, macs, hostKeys []string) Option {
	return func(d *Dialer) {
		if len(ciphers) > 0 {
			d.ciphers = copyStrings(ciphers)
		}
		if len(keyExchanges) > 0 {
			d.keyExchanges = copyStrings(keyExchanges)
		}
		if len(macs) > 0 {
			d.macs = copyStrings(macs)
		}
		if len(hostKeys) > 0 {
			d.hostKeyAlgos = copyStrings(hostKeys)
		}
	}
}

// NewDialer returns an SSH dialer over x/crypto/ssh.
func NewDialer(opts ...Option) *Dialer {
	dialer := &Dialer{
		dial:         (&net.Dialer{}).DialContext,
		dialTimeout:  defaultDialTimeout,
		authTimeout:  defaultAuthTimeout,
		ciphers:      copyStrings(defaultCiphers),
		keyExchanges: copyStrings(defaultKeyExchanges),
		macs:         copyStrings(defaultMACs),
		hostKeyAlgos: copyStrings(defaultHostKeys),
	}
	for _, opt := range opts {
		opt(dialer)
	}
	return dialer
}

var _ appconnection.Dialer = (*Dialer)(nil)

// Dial establishes TCP, verifies the host key, authenticates with the saved
// password, and returns the transport. Host-key rejection happens during the
// handshake, so no credential is ever offered to an unverified server.
func (d *Dialer) Dial(ctx context.Context, req appconnection.DialRequest) (appconnection.Client, error) {
	port := req.Port
	if port == 0 {
		port = 22
	}
	address := net.JoinHostPort(req.Hostname, fmt.Sprintf("%d", port))

	dialCtx, cancel := context.WithTimeout(ctx, d.dialTimeout)
	defer cancel()

	conn, err := d.dial(dialCtx, "tcp", address)
	if err != nil {
		return nil, classifyDialFailure(ctx, dialCtx, err)
	}

	// The handshake has no context parameter, so cancellation reaches the
	// host-key callback through this context. A prompt blocked on a user
	// decision is released when the authentication deadline expires.
	authCtx, cancelAuth := context.WithCancel(ctx)
	defer cancelAuth()

	config := &xssh.ClientConfig{
		User: req.Username,
		Auth: []xssh.AuthMethod{xssh.Password(req.Password)},
		HostKeyCallback: func(_ string, _ net.Addr, key xssh.PublicKey) error {
			if req.VerifyHostKey == nil {
				return &appconnection.Error{Kind: appconnection.KindHostKey, Cause: appconnection.ErrHostKeyRejected}
			}
			blob := key.Marshal()
			return req.VerifyHostKey(authCtx, domainconn.Key{
				Hostname:    req.Hostname,
				Port:        port,
				KeyType:     key.Type(),
				Fingerprint: domainconn.Fingerprint(blob),
				PublicKey:   domainconn.PublicKey(key.Type(), blob),
			})
		},
		HostKeyAlgorithms: copyStrings(d.hostKeyAlgos),
		Config: xssh.Config{
			Ciphers:      copyStrings(d.ciphers),
			KeyExchanges: copyStrings(d.keyExchanges),
			MACs:         copyStrings(d.macs),
		},
	}

	type handshake struct {
		conn  xssh.Conn
		chans <-chan xssh.NewChannel
		reqs  <-chan *xssh.Request
		err   error
	}
	done := make(chan handshake, 1)
	go func() {
		serverConn, channels, requests, err := xssh.NewClientConn(conn, address, config)
		done <- handshake{conn: serverConn, chans: channels, reqs: requests, err: err}
	}()

	timer := time.NewTimer(d.authTimeout)
	defer timer.Stop()

	select {
	case result := <-done:
		if result.err != nil {
			_ = conn.Close()
			return nil, classifyHandshakeFailure(ctx, result.err)
		}
		return &sshClient{client: xssh.NewClient(result.conn, result.chans, result.reqs)}, nil
	case <-timer.C:
		cancelAuth()
		_ = conn.Close()
		return nil, &appconnection.Error{
			Kind:  appconnection.KindTimeout,
			Cause: fmt.Errorf("%w: authentication did not become ready within %s", appconnection.ErrAuthTimeout, d.authTimeout),
		}
	case <-ctx.Done():
		cancelAuth()
		_ = conn.Close()
		return nil, classifyDialFailure(ctx, ctx, ctx.Err())
	}
}

// classifyDialFailure distinguishes the caller's cancellation from the dial
// deadline and everything else, which is a network failure.
func classifyDialFailure(parent, dialCtx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || (parent.Err() != nil && errors.Is(parent.Err(), context.Canceled)) {
		return &appconnection.Error{Kind: appconnection.KindCancelled, Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(dialCtx.Err(), context.DeadlineExceeded) ||
		errors.Is(parent.Err(), context.DeadlineExceeded) {
		return &appconnection.Error{
			Kind:  appconnection.KindTimeout,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrDialTimeout, err),
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &appconnection.Error{
			Kind:  appconnection.KindTimeout,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrDialTimeout, err),
		}
	}
	return &appconnection.Error{
		Kind:  appconnection.KindNetwork,
		Cause: fmt.Errorf("%w: %v", appconnection.ErrNetwork, err),
	}
}

// classifyHandshakeFailure maps x/crypto's wrapped handshake errors. A typed
// error from the host-key callback keeps its kind and cause so rejection is
// reported as a host-key failure, not a network one.
func classifyHandshakeFailure(ctx context.Context, err error) error {
	var typed *appconnection.Error
	if errors.As(err, &typed) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return &appconnection.Error{Kind: appconnection.KindCancelled, Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &appconnection.Error{Kind: appconnection.KindTimeout, Cause: err}
	}

	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unable to authenticate") ||
		strings.Contains(message, "no supported methods remain") {
		return &appconnection.Error{
			Kind:  appconnection.KindAuth,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrAuthentication, err),
		}
	}
	if isAlgorithmFailure(message) {
		return &appconnection.Error{
			Kind:  appconnection.KindUnsupported,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrUnsupportedAlgorithm, err),
		}
	}
	return &appconnection.Error{
		Kind:  appconnection.KindNetwork,
		Cause: fmt.Errorf("%w: %v", appconnection.ErrNetwork, err),
	}
}

// isAlgorithmFailure recognizes x/crypto's negotiation failures without
// depending on one exact sentence per algorithm family. The auth phrases are
// checked before this, so "no supported methods remain" stays an auth error.
func isAlgorithmFailure(message string) bool {
	if strings.Contains(message, "no common") || strings.Contains(message, "negotiation failed") {
		return true
	}
	return strings.Contains(message, "algorithm") &&
		(strings.Contains(message, "no ") || strings.Contains(message, "unsupported") || strings.Contains(message, "not supported"))
}
