// Package ssh is the x/crypto/ssh transport adapter. It dials one endpoint or a
// jump chain, verifies every hop's host key through the application's callback,
// authenticates each hop with its own plan, and opens interactive shell
// channels on the final endpoint. It owns no trust state and makes no trust
// decision: a callback runs before authentication and its error aborts the
// handshake.
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
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
	defaultSSHPort     = 22
)

// DialFunc establishes the raw transport. Tests replace it to use an
// in-process SSH server.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Dialer opens authenticated SSH transports with x/crypto/ssh.
type Dialer struct {
	dial         DialFunc
	agentDial    AgentDialFunc
	processStart ProcessStartFunc
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

// WithAgentDial replaces the local SSH agent transport. Tests use an
// in-memory agent; production uses SSH_AUTH_SOCK on Unix and the OpenSSH named
// pipe on Windows.
func WithAgentDial(fn AgentDialFunc) Option {
	return func(d *Dialer) {
		if fn != nil {
			d.agentDial = fn
		}
	}
}

// WithProcessStart replaces ProxyCommand process spawning. Tests use an
// in-process helper; production uses the platform process seam.
func WithProcessStart(fn ProcessStartFunc) Option {
	return func(d *Dialer) {
		if fn != nil {
			d.processStart = fn
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

// WithAlgorithms replaces the transport's base offer. A nil or empty list
// keeps the default for that category. Each endpoint's Host policy is applied
// on top of this base, so a per-hop override still wins.
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
		agentDial:    defaultAgentDial,
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

// endpoint is one chain member: a jump, or the target when it is the last
// element. Each endpoint carries its own trust callback and auth plan.
type endpoint struct {
	label      string
	hostname   string
	port       int
	username   string
	password   string
	auth       appconnection.AuthPlan
	prompter   appconnection.AuthPrompter
	onAuth     func(candidateID string)
	verify     func(ctx context.Context, key domainconn.Key) error
	algorithms appconnection.AlgorithmPolicy
}

func (e endpoint) portOr22() int {
	if e.port == 0 {
		return defaultSSHPort
	}
	return e.port
}

func (e endpoint) address() string {
	return net.JoinHostPort(e.hostname, strconv.Itoa(e.portOr22()))
}

func endpointsFor(req appconnection.DialRequest) []endpoint {
	endpoints := make([]endpoint, 0, len(req.Jumps)+1)
	for _, hop := range req.Jumps {
		endpoints = append(endpoints, endpoint{
			label:      hop.Label,
			hostname:   hop.Hostname,
			port:       hop.Port,
			username:   hop.Username,
			password:   hop.Password,
			auth:       hop.Auth,
			prompter:   hop.Prompter,
			onAuth:     hop.OnAuth,
			verify:     hop.VerifyHostKey,
			algorithms: hop.Algorithms,
		})
	}
	targetLabel := req.Label
	if targetLabel == "" {
		targetLabel = req.Hostname
	}
	endpoints = append(endpoints, endpoint{
		label:      targetLabel,
		hostname:   req.Hostname,
		port:       req.Port,
		username:   req.Username,
		password:   req.Password,
		auth:       req.Auth,
		prompter:   req.Prompter,
		onAuth:     req.OnAuth,
		verify:     req.VerifyHostKey,
		algorithms: req.Algorithms,
	})
	return endpoints
}

// Dial establishes the first endpoint through the proxy, then reaches each
// later endpoint over a direct-tcpip channel on the previous hop. Every hop
// verifies its host key before offering a credential. A failure closes the
// hops already open and is tagged with the endpoint that produced it.
func (d *Dialer) Dial(ctx context.Context, req appconnection.DialRequest) (appconnection.Client, error) {
	endpoints := endpointsFor(req)
	total := len(endpoints)
	clients := make([]*xssh.Client, 0, total)
	versions := make([]string, 0, total)
	closeChain := func() {
		for i := len(clients) - 1; i >= 0; i-- {
			_ = clients[i].Close()
		}
	}

	for i, ep := range endpoints {
		label := ep.label
		if label == "" {
			label = ep.address()
		}
		reportChain(req, total, i+1, label, appconnection.ChainConnecting, nil)

		var (
			raw net.Conn
			err error
		)
		if i == 0 {
			raw, err = d.openFirst(ctx, req.Proxy, ep)
		} else {
			previous := endpoints[i-1]
			previousLabel := previous.label
			if previousLabel == "" {
				previousLabel = previous.address()
			}
			reportChain(req, total, i, previousLabel, appconnection.ChainForwarding, nil)
			raw, err = d.openForward(ctx, clients[i-1], ep)
		}
		if err != nil {
			closeChain()
			annotated := appconnection.TagHop(err, i+1, total, label)
			reportChain(req, total, i+1, label, appconnection.ChainError, annotated)
			return nil, annotated
		}
		reportChain(req, total, i+1, label, appconnection.ChainTCPConnected, nil)
		reportChain(req, total, i+1, label, appconnection.ChainAuthenticating, nil)

		versioned := &versionRecorder{Conn: raw}
		client, remoteVersion, err := d.handshake(ctx, versioned, ep)
		if err != nil {
			_ = raw.Close()
			closeChain()
			annotated := appconnection.TagHop(err, i+1, total, label)
			reportChain(req, total, i+1, label, appconnection.ChainError, annotated)
			return nil, annotated
		}
		clients = append(clients, client)
		versions = append(versions, remoteVersion)
		reportChain(req, total, i+1, label, appconnection.ChainConnected, nil)
	}

	final := clients[len(clients)-1]
	return &sshClient{
		client:        final,
		ancestors:     clients[:len(clients)-1],
		remoteVersion: versions[len(versions)-1],
	}, nil
}

func reportChain(req appconnection.DialRequest, total, index int, label string, status appconnection.ChainStatus, err error) {
	if req.OnProgress == nil {
		return
	}
	req.OnProgress(appconnection.ChainProgress{
		Index: index, Total: total, Label: label, Status: status, Err: err,
	})
}

// openFirst reaches the endpoint directly or through the proxy. The proxy
// applies only here: later hops travel inside the SSH chain.
func (d *Dialer) openFirst(ctx context.Context, proxy *appconnection.Proxy, ep endpoint) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, d.dialTimeout)
	defer cancel()
	if proxy != nil {
		return d.dialProxy(dialCtx, proxy, ep.hostname, ep.portOr22())
	}
	conn, err := d.dial(dialCtx, "tcp", ep.address())
	if err != nil {
		return nil, classifyDialFailure(ctx, dialCtx, err)
	}
	return conn, nil
}

// openForward asks the previous hop for a direct-tcpip channel to the next
// endpoint. The dial timeout bounds channel establishment, not the SSH
// transport underneath it.
func (d *Dialer) openForward(ctx context.Context, previous *xssh.Client, ep endpoint) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, d.dialTimeout)
	defer cancel()
	conn, err := previous.DialContext(dialCtx, "tcp", ep.address())
	if err != nil {
		return nil, classifyDialFailure(ctx, dialCtx, err)
	}
	return conn, nil
}

// handshake verifies the host key, authenticates, and returns the client. raw
// is closed by the caller on failure.
func (d *Dialer) handshake(ctx context.Context, raw net.Conn, ep endpoint) (*xssh.Client, string, error) {
	port := ep.portOr22()
	address := net.JoinHostPort(ep.hostname, strconv.Itoa(port))

	// The handshake has no context parameter, so cancellation reaches the
	// host-key and auth callbacks through this context. A prompt blocked on a
	// user decision is released when the authentication deadline expires.
	authCtx, cancelAuth := context.WithCancel(ctx)
	defer cancelAuth()

	plan := ep.auth
	if plan.Password == "" {
		plan.Password = ep.password
	}
	if plan.Mode == "" && len(plan.Candidates) == 0 {
		// Compatibility for callers that predate AuthPlan: password plus
		// keyboard-interactive only.
		plan = appconnection.BuildAuthPlan(appconnection.AuthSpec{
			Username: ep.username,
			Mode:     appconnection.AuthModePassword,
			Password: plan.Password,
		})
	}
	machine := newAuthMachine(authCtx, plan, ep.prompter, ep.hostname, d.agentDial)
	if err := machine.Prepare(); err != nil {
		return nil, "", err
	}
	defer machine.Close()

	offer, err := d.offerFor(ep.algorithms)
	if err != nil {
		return nil, "", err
	}
	config := &xssh.ClientConfig{
		User:         ep.username,
		AuthCallback: machine.Next,
		HostKeyCallback: func(_ string, _ net.Addr, key xssh.PublicKey) error {
			if ep.verify == nil {
				return &appconnection.Error{Kind: appconnection.KindHostKey, Cause: appconnection.ErrHostKeyRejected}
			}
			blob := key.Marshal()
			return ep.verify(authCtx, domainconn.Key{
				Hostname:    ep.hostname,
				Port:        port,
				KeyType:     key.Type(),
				Fingerprint: domainconn.Fingerprint(blob),
				PublicKey:   domainconn.PublicKey(key.Type(), blob),
			})
		},
		HostKeyAlgorithms: offer.hostKeys,
		Config: xssh.Config{
			Ciphers:      offer.ciphers,
			KeyExchanges: offer.keyExchanges,
			MACs:         offer.macs,
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
		serverConn, channels, requests, err := xssh.NewClientConn(raw, address, config)
		done <- handshake{conn: serverConn, chans: channels, reqs: requests, err: err}
	}()

	timer := time.NewTimer(d.authTimeout)
	defer timer.Stop()

	// abort stops the handshake goroutine before the deferred machine.Close
	// runs. Closing raw unblocks the transport read; waiting for done keeps an
	// in-flight auth callback from touching the agent after it is released.
	abort := func(cause error) error {
		cancelAuth()
		_ = raw.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return cause
	}

	select {
	case result := <-done:
		if result.err != nil {
			return nil, "", classifyHandshakeFailure(ctx, handshakeDetail(raw, result.err))
		}
		if ep.onAuth != nil {
			if candidate := machine.SuccessID(); candidate != "" {
				ep.onAuth(candidate)
			}
		}
		return xssh.NewClient(result.conn, result.chans, result.reqs), versionOf(raw), nil
	case <-timer.C:
		return nil, "", abort(&appconnection.Error{
			Kind:  appconnection.KindTimeout,
			Cause: fmt.Errorf("%w: authentication did not become ready within %s", appconnection.ErrAuthTimeout, d.authTimeout),
		})
	case <-ctx.Done():
		return nil, "", abort(classifyDialFailure(ctx, ctx, ctx.Err()))
	}
}

// versionRecorder passively captures the server's SSH identification line as
// x/crypto reads from the transport. It never consumes bytes the handshake
// needs.
type versionRecorder struct {
	net.Conn
	mu      sync.Mutex
	buffer  []byte
	version string
	done    bool
}

func (v *versionRecorder) Read(p []byte) (int, error) {
	n, err := v.Conn.Read(p)
	if n > 0 {
		v.record(p[:n])
	}
	return n, err
}

func (v *versionRecorder) record(data []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.done {
		return
	}
	v.buffer = append(v.buffer, data...)
	for {
		index := bytes.IndexByte(v.buffer, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSpace(string(v.buffer[:index]))
		v.buffer = v.buffer[index+1:]
		if strings.HasPrefix(line, "SSH-") {
			v.version = line
			v.done = true
			v.buffer = nil
			return
		}
	}
	// A malformed or absurd banner must not grow without bound; x/crypto will
	// reject the handshake itself.
	if len(v.buffer) > 4<<10 {
		v.buffer = nil
		v.done = true
	}
}

func (v *versionRecorder) Version() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.version
}

func versionOf(raw net.Conn) string {
	if recorder, ok := raw.(*versionRecorder); ok {
		return recorder.Version()
	}
	return ""
}

// handshakeDetail folds a process-backed transport's stderr into the
// handshake error. ProxyCommand failures are otherwise an opaque EOF.
func handshakeDetail(raw net.Conn, err error) error {
	diagnostic, ok := raw.(interface{ Stderr() string })
	if !ok {
		return err
	}
	detail := strings.TrimSpace(diagnostic.Stderr())
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

// classifyDialFailure distinguishes the caller's cancellation from the dial
// deadline and everything else, which is a network failure. A typed error from
// the proxy transport keeps its classification.
func classifyDialFailure(parent, dialCtx context.Context, err error) error {
	var typed *appconnection.Error
	if errors.As(err, &typed) {
		return err
	}
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
