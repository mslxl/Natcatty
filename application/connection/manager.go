// Package connection owns authenticated SSH connections and the leases that
// Sessions and later features borrow from them. It declares the dial,
// credential, Known Host, and trust-prompt ports it consumes, so the SSH
// adapter and SQLite store implement them without this package knowing about
// x/crypto/ssh or storage.
//
// A Connection is one authenticated transport. A Lease is a counted claim on
// it. Ticket 06 dials one Connection per Session; the refcount and the
// Manager seam are the extension points for the shared pool and jump chains
// that later tickets add.
package connection

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
)

// Request describes an endpoint to connect to. Credentials are deliberately
// absent: the Manager reads the saved password through PasswordSource.
//
// Jumps, when present, are full endpoints reached before the target, ordered
// first = closest to the client. Proxy applies to the first endpoint only.
type Request struct {
	HostID     model.HostID
	Hostname   string
	Port       int
	Username   string
	Jumps      []Hop
	Proxy      *Proxy
	Algorithms AlgorithmPolicy
	// ConfigFingerprint is an opaque, non-secret digest of the effective
	// credential selection and connection policy. The Manager only compares
	// it: two requests with different values never share a Connection. The
	// application computes it, so credential references can scope reuse
	// without exposing secret material to the pool.
	ConfigFingerprint string
	// LeaseKind selects the reuse rule. The zero value is LeaseShell.
	LeaseKind LeaseKind
	// AgentForwarding records whether the transport exposes the local agent to
	// the remote Host. A shell requires an exact match; a channel may reuse a
	// forwarding transport but not the reverse.
	AgentForwarding bool
	// ForwardingAgentFingerprint identifies the forwarded agent socket
	// without storing its path in a pool key.
	ForwardingAgentFingerprint string
	// SingleChannel marks a bastion that accepts one session channel. Such a
	// transport is never reused and never serves another lease.
	SingleChannel bool
	// X11Forwarding marks an X11 transport. X11 sessions never enter the
	// general reuse pool; ticket 11 supplies the forwarding itself.
	X11Forwarding bool
	// Keepalive overrides the keepalive schedule. Nil keeps the original
	// 10 s / 3 fallback; a negative Interval disables it.
	Keepalive *KeepalivePolicy
	// OnProgress, when non-nil, receives chain progress. It runs on the dial
	// goroutine and must not block.
	OnProgress func(ChainProgress)
}

// Test connects, authenticates, and verifies the Host key, then releases the
// lease it took. It is the application use case behind the Vault's Test
// connection action.
func (m *Manager) Test(ctx context.Context, req Request) error {
	lease, err := m.Acquire(ctx, req)
	if err != nil {
		return err
	}
	return lease.Release()
}

// DialRequest is what the SSH adapter needs to dial one endpoint. VerifyHostKey
// runs during the handshake, before authentication; returning an error aborts
// the connection. Its context is cancelled when the handshake deadline
// expires, so a queued trust prompt cannot outlive the connection attempt.
//
// Auth carries the ordered attempts, including credential plaintext. It is
// memory-only and must never be persisted or logged. OnAuth, when non-nil, is
// called once with the candidate ID that completed authentication; the value
// is safe to cache because it is a method ID, not a credential.
type DialRequest struct {
	// Label names the target in progress and hop errors. The Manager fills it
	// with the Host label or its address so adapter and Manager annotations
	// agree.
	Label         string
	Hostname      string
	Port          int
	Username      string
	Password      string
	Auth          AuthPlan
	Prompter      AuthPrompter
	OnAuth        func(candidateID string)
	VerifyHostKey func(ctx context.Context, key domainconn.Key) error
	// Jumps are the endpoints reached before the target, ordered first =
	// closest to the client. Each carries its own authentication plan, trust
	// callback, and algorithm policy.
	Jumps []DialHop
	// Algorithms is the target endpoint's negotiation policy.
	Algorithms AlgorithmPolicy
	// Proxy applies to the first endpoint only: the first jump when present,
	// otherwise the target.
	Proxy *Proxy
	// OnProgress, when non-nil, receives chain progress on the dial goroutine.
	OnProgress func(ChainProgress)
}

// DialHop is one jump endpoint as the transport needs it. Password and Auth
// carry credential plaintext in memory only.
type DialHop struct {
	Label         string
	Hostname      string
	Port          int
	Username      string
	Password      string
	Auth          AuthPlan
	Prompter      AuthPrompter
	OnAuth        func(candidateID string)
	VerifyHostKey func(ctx context.Context, key domainconn.Key) error
	Algorithms    AlgorithmPolicy
}

// TerminalSpec is one interactive shell's initial terminal state.
type TerminalSpec struct {
	Cols int
	Rows int
	Term string
}

// Terminal is an interactive shell channel. Read is output, Write is input,
// Resize changes the remote PTY size, and Close hangs up the channel.
type Terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

// Client is an authenticated SSH transport. The adapter owns the concrete
// client; the application only opens channels and closes it.
type Client interface {
	OpenTerminal(ctx context.Context, spec TerminalSpec) (Terminal, error)
	Close() error
}

// Dialer opens an authenticated transport. Tests provide a fake; the app
// wires adapters/ssh.
type Dialer interface {
	Dial(ctx context.Context, req DialRequest) (Client, error)
}

// PasswordSource reads a Host's saved password. The app bridges the Vault
// secret service; tests provide a fake. The Manager never writes the value
// anywhere.
type PasswordSource interface {
	Password(ctx context.Context, hostID model.HostID) (string, error)
}

// KnownHostRepository persists Known Hosts. The app wires adapters/store;
// tests provide a fake.
type KnownHostRepository interface {
	ListKnownHosts(ctx context.Context) ([]domainconn.KnownHost, error)
	UpsertKnownHost(ctx context.Context, known domainconn.KnownHost) error
}

// PromptDecision is the user's answer to a host-key prompt.
type PromptDecision struct {
	// Trust accepts the key for this connection. False rejects it.
	Trust bool
	// Persist records the key as a Known Host. It is ignored when Trust is
	// false.
	Persist bool
}

// Prompter asks the user to trust an unknown or changed host key. The UI
// implements it; a test provides a fake. A Manager without a Prompter rejects
// untrusted keys, because the trust boundary fails closed.
type Prompter interface {
	Confirm(ctx context.Context, key domainconn.Key) (PromptDecision, error)
}

// Manager opens Connections and reads their credentials, Known Hosts, and
// trust decisions through its ports.
type Manager struct {
	dialer        Dialer
	passwords     PasswordSource
	authSource    AuthSource
	authPrompter  AuthPrompter
	authCache     *AuthMethodCache
	known         KnownHostRepository
	prompter      Prompter
	newID         func() string
	now           func() time.Time
	idleTTL       time.Duration
	maxIdle       int
	livenessProbe time.Duration
	schedule      timerScheduler
	pool          *pool
}

// Option customizes a Manager.
type Option func(*Manager)

// WithPrompter sets the trust prompt. A nil prompter rejects every untrusted
// key.
func WithPrompter(prompter Prompter) Option {
	return func(m *Manager) { m.prompter = prompter }
}

// WithAuthSource gives the Manager the Vault-backed authentication resolver.
// Without it, a Manager falls back to the saved password and offers password
// and keyboard-interactive only, which preserves ticket 06 behavior.
func WithAuthSource(source AuthSource) Option {
	return func(m *Manager) { m.authSource = source }
}

// WithAuthPrompter sets the passphrase and keyboard-interactive prompter.
func WithAuthPrompter(prompter AuthPrompter) Option {
	return func(m *Manager) { m.authPrompter = prompter }
}

// WithAuthMethodCache replaces the successful-method cache. A nil value keeps
// the Manager's 512-entry default.
func WithAuthMethodCache(cache *AuthMethodCache) Option {
	return func(m *Manager) {
		if cache != nil {
			m.authCache = cache
		}
	}
}

// WithIDGenerator sets how the Manager names Known Hosts.
func WithIDGenerator(fn func() string) Option {
	return func(m *Manager) {
		if fn != nil {
			m.newID = fn
		}
	}
}

// WithClock sets the Manager's source of time.
func WithClock(fn func() time.Time) Option {
	return func(m *Manager) {
		if fn != nil {
			m.now = fn
		}
	}
}

// WithIdleTTL sets how long a zero-lease Connection parks before it closes.
// Zero means until the Manager closes, matching the original "Until app quit".
func WithIdleTTL(ttl time.Duration) Option {
	return func(m *Manager) { m.idleTTL = ttl }
}

// WithMaxIdleConnections bounds parked Connections. Active Connections are
// never evicted.
func WithMaxIdleConnections(limit int) Option {
	return func(m *Manager) {
		if limit > 0 {
			m.maxIdle = limit
		}
	}
}

// WithLivenessProbe sets how long a shell opened on a reused parked transport
// must survive before it is accepted. Zero disables the probe.
func WithLivenessProbe(duration time.Duration) Option {
	return func(m *Manager) {
		if duration >= 0 {
			m.livenessProbe = duration
		}
	}
}

// WithTimerScheduler replaces wall-clock idle timers. Tests use it to fire a
// parked Connection's deadline deterministically.
func WithTimerScheduler(fn func(time.Duration, func()) (stop func())) Option {
	return func(m *Manager) {
		if fn != nil {
			m.schedule = fn
		}
	}
}

// NewManager returns a Manager over its ports.
func NewManager(dialer Dialer, passwords PasswordSource, known KnownHostRepository, opts ...Option) *Manager {
	manager := &Manager{
		dialer:        dialer,
		passwords:     passwords,
		authCache:     NewAuthMethodCache(MaxAuthMethodCacheEntries),
		known:         known,
		newID:         newKnownHostID,
		now:           time.Now,
		idleTTL:       DefaultIdleTTL,
		maxIdle:       MaxIdleConnections,
		livenessProbe: DefaultLivenessProbe,
	}
	for _, opt := range opts {
		opt(manager)
	}
	if manager.schedule == nil {
		manager.schedule = func(duration time.Duration, fn func()) func() {
			timer := time.AfterFunc(duration, fn)
			return func() { timer.Stop() }
		}
	}
	manager.pool = newPool(manager.idleTTL, manager.maxIdle, manager.now, manager.schedule)
	return manager
}

// CloseAll closes every pooled Connection and stops accepting new ones. The
// app calls it on quit after closing Sessions.
func (m *Manager) CloseAll() {
	if m == nil || m.pool == nil {
		return
	}
	closeConnections(m.pool.closeAll())
}

// Acquire opens a new Connection and returns its first Lease. The caller's
// context bounds dialing, the trust prompt, and authentication; after Acquire
// returns, the Connection owns its transport.
//
// Every jump is resolved independently: its own auth plan, auth-method cache
// entry, and host-key verification run against the same Known Host snapshot.
// A failure is tagged with the endpoint that produced it.
func (m *Manager) Acquire(ctx context.Context, req Request) (*Lease, error) {
	if m.dialer == nil {
		return nil, internalError(ErrNoDialer)
	}

	endpoint := endpointFor(req)
	kind := req.LeaseKind.orShell()
	reuse, wait, lead := m.pool.begin(endpoint, kind)
	if reuse != nil {
		return m.readyLease(reuse), nil
	}
	if wait != nil {
		select {
		case <-wait.done:
			if wait.err != nil {
				var typed *Error
				if errors.As(wait.err, &typed) {
					return nil, wait.err
				}
				return nil, internalError(wait.err)
			}
			lease := m.pool.borrow(wait.conn, endpoint, kind)
			if lease == nil {
				return nil, internalError(ErrClosed)
			}
			return m.readyLease(lease), nil
		case <-ctx.Done():
			return nil, classifyDialError(ctx.Err())
		}
	}
	if lead.err != nil {
		return nil, internalError(lead.err)
	}

	conn, err := m.dial(ctx, req, endpoint)
	if completeErr := m.pool.complete(lead, conn, err); completeErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, internalError(completeErr)
	}
	lease := m.pool.borrow(conn, endpoint, kind)
	if lease == nil {
		return nil, internalError(ErrClosed)
	}
	return m.readyLease(lease), nil
}

// readyLease arms the reused-shell liveness probe. Only a shell opened on a
// transport that was parked needs it; a live transport already proved itself.
func (m *Manager) readyLease(lease *Lease) *Lease {
	if lease != nil && lease.kind == LeaseShell && lease.fromIdle &&
		m.livenessProbe > 0 && lease.conn.needsLivenessCheck() {
		lease.probeDuration = m.livenessProbe
	}
	return lease
}

// dial opens a physical transport and returns its first Connection.
func (m *Manager) dial(ctx context.Context, req Request, endpoint endpointConfig) (*Connection, error) {
	known, err := m.knownHosts(ctx)
	if err != nil {
		return nil, err
	}

	total := len(req.Jumps) + 1
	type endpointPlan struct {
		hop      Hop
		plan     AuthPlan
		cacheKey string
	}
	endpoints := make([]endpointPlan, 0, total)
	for i, hop := range req.Jumps {
		plan, err := m.authPlan(ctx, requestForHop(hop))
		if err != nil {
			return nil, TagHop(err, i+1, total, hop.LabelOrAddress())
		}
		endpoints = append(endpoints, endpointPlan{
			hop:      hop,
			plan:     plan,
			cacheKey: authCacheKey(hop.Username, hop.Hostname, hop.Port),
		})
	}
	plan, err := m.authPlan(ctx, req)
	if err != nil {
		target := targetHop(req)
		return nil, TagHop(err, total, total, target.LabelOrAddress())
	}
	endpoints = append(endpoints, endpointPlan{
		hop:      targetHop(req),
		plan:     plan,
		cacheKey: authCacheKey(req.Username, req.Hostname, req.Port),
	})

	// Promote a cached successful method per endpoint. The cache stores method
	// IDs, never credentials.
	for i := range endpoints {
		if cached, ok := m.authCache.Get(endpoints[i].cacheKey); ok {
			PromoteCachedCandidate(&endpoints[i].plan, cached, endpoints[i].plan.Mode)
		}
	}

	succeeded := make([]string, total)
	verify := func(verifyCtx context.Context, key domainconn.Key) error {
		return m.verifyHostKey(verifyCtx, key, known)
	}
	username := func(plan AuthPlan, fallback string) string {
		if plan.Username != "" {
			return plan.Username
		}
		return fallback
	}

	dialRequest := DialRequest{
		Proxy:      req.Proxy,
		Algorithms: req.Algorithms,
		OnProgress: req.OnProgress,
	}
	for i := range endpoints {
		endpoint := &endpoints[i]
		index := i
		onAuth := func(candidateID string) { succeeded[index] = candidateID }

		if i < len(req.Jumps) {
			// Legacy mode is inherited additively from the target; ECDSA-skip
			// and explicit overrides stay strictly per-hop.
			algorithms := endpoint.hop.Algorithms
			algorithms.Legacy = algorithms.Legacy || req.Algorithms.Legacy
			dialRequest.Jumps = append(dialRequest.Jumps, DialHop{
				Label:         endpoint.hop.LabelOrAddress(),
				Hostname:      endpoint.hop.Hostname,
				Port:          endpoint.hop.Port,
				Username:      username(endpoint.plan, endpoint.hop.Username),
				Password:      endpoint.plan.Password,
				Auth:          endpoint.plan,
				Prompter:      m.authPrompter,
				OnAuth:        onAuth,
				VerifyHostKey: verify,
				Algorithms:    algorithms,
			})
			continue
		}

		dialRequest.Label = endpoint.hop.LabelOrAddress()
		dialRequest.Hostname = endpoint.hop.Hostname
		dialRequest.Port = endpoint.hop.Port
		dialRequest.Username = username(endpoint.plan, endpoint.hop.Username)
		dialRequest.Password = endpoint.plan.Password
		dialRequest.Auth = endpoint.plan
		dialRequest.Prompter = m.authPrompter
		dialRequest.OnAuth = onAuth
		dialRequest.VerifyHostKey = verify
	}

	client, err := m.dialer.Dial(ctx, dialRequest)
	if err != nil {
		classified := classifyDialError(err)
		if isAuthenticationFailure(classified) {
			// A failed login means the cached method may have been revoked or
			// the server's policy changed; try every method next time. Clear
			// only the endpoint that failed.
			index := total - 1
			var typed *Error
			if errors.As(classified, &typed) && typed.Hop != nil &&
				typed.Hop.Index >= 1 && typed.Hop.Index <= total {
				index = typed.Hop.Index - 1
			}
			m.authCache.Delete(endpoints[index].cacheKey)
		}
		return nil, classified
	}
	for i := range endpoints {
		if succeeded[i] != "" {
			m.authCache.Put(endpoints[i].cacheKey, succeeded[i])
		}
	}
	conn := newPooledConnection(client, endpoint, m)
	conn.startKeepalive(endpoint.keepalive, func() { m.pool.discard(conn, "keepalive") })
	return conn, nil
}

// requestForHop turns a jump Hop into the Request an AuthSource consumes.
func requestForHop(hop Hop) Request {
	return Request{
		HostID:   hop.HostID,
		Hostname: hop.Hostname,
		Port:     hop.Port,
		Username: hop.Username,
	}
}

// targetHop is the target endpoint as a Hop, for labels and error context.
func targetHop(req Request) Hop {
	return Hop{
		HostID:   req.HostID,
		Hostname: req.Hostname,
		Port:     req.Port,
		Username: req.Username,
	}
}

// authPlan resolves the Host's authentication material. Without a Vault-backed
// AuthSource, it preserves the ticket 06 behavior of using the saved password
// and offering password plus keyboard-interactive.
func (m *Manager) authPlan(ctx context.Context, req Request) (AuthPlan, error) {
	if m.authSource != nil {
		plan, err := m.authSource.AuthPlan(ctx, req)
		if err != nil {
			var typed *Error
			if errors.As(err, &typed) {
				return AuthPlan{}, err
			}
			if errors.Is(err, context.Canceled) {
				return AuthPlan{}, cancelledError(err)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return AuthPlan{}, timeoutError(err)
			}
			return AuthPlan{}, internalError(err)
		}
		if plan.Mode == "" {
			plan.Mode = AuthModeAuto
		}
		return plan, nil
	}

	password, err := m.password(ctx, req.HostID)
	if err != nil {
		return AuthPlan{}, err
	}
	return BuildAuthPlan(AuthSpec{
		Username: req.Username,
		Mode:     AuthModePassword,
		Password: password,
	}), nil
}

func authCacheKey(username, hostname string, port int) string {
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%s@%s:%d", username, hostname, port)
}

// isAuthenticationFailure reports whether a classified failure was a refused
// login. It is the only class that invalidates the method cache.
func isAuthenticationFailure(err error) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Kind == KindAuth
}

// password reads the saved credential. A missing credential is an auth error,
// not a storage error: the user can fix it by editing the Host.
func (m *Manager) password(ctx context.Context, hostID model.HostID) (string, error) {
	if m.passwords == nil {
		return "", authError(ErrNoCredentials)
	}
	value, err := m.passwords.Password(ctx, hostID)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return "", err
		}
		if errors.Is(err, context.Canceled) {
			return "", cancelledError(err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", timeoutError(err)
		}
		return "", authError(err)
	}
	return value, nil
}

func (m *Manager) knownHosts(ctx context.Context) ([]domainconn.KnownHost, error) {
	if m.known == nil {
		return nil, internalError(errors.New("connection: no Known Host store"))
	}
	known, err := m.known.ListKnownHosts(ctx)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return nil, err
		}
		if errors.Is(err, context.Canceled) {
			return nil, cancelledError(err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, timeoutError(err)
		}
		return nil, internalError(err)
	}
	return known, nil
}

// verifyHostKey classifies the live key and asks the user when the key is not
// already trusted. Trust upserts the Known Host before authentication starts.
func (m *Manager) verifyHostKey(ctx context.Context, key domainconn.Key, known []domainconn.KnownHost) error {
	classified := domainconn.Classify(known, key.Hostname, key.Port, key.KeyType, key.Fingerprint)
	if classified.PublicKey == "" {
		classified.PublicKey = key.PublicKey
	}
	if classified.Status == domainconn.StatusTrusted {
		return nil
	}
	if m.prompter == nil {
		return hostKeyError(ErrHostKeyRejected)
	}

	decision, err := m.prompter.Confirm(ctx, classified)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return err
		}
		if errors.Is(err, context.Canceled) {
			return cancelledError(err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return timeoutError(err)
		}
		return hostKeyError(err)
	}
	if !decision.Trust {
		return hostKeyError(ErrHostKeyRejected)
	}
	if !decision.Persist {
		return nil
	}

	record := domainconn.NewKnownHost(m.newID(), classified, m.now())
	if classified.KnownHostID != "" {
		record.ID = classified.KnownHostID
	}
	if err := m.known.UpsertKnownHost(ctx, record); err != nil {
		return internalError(err)
	}
	return nil
}

// classifyDialError maps an adapter failure to a typed connection error. An
// adapter that already returns *Error keeps its classification.
func classifyDialError(err error) error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return cancelledError(err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return timeoutError(err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return timeoutError(err)
		}
		return networkError(err)
	}
	return networkError(err)
}

// KeepaliveClient is implemented by transports that can send an
// openssh-style keepalive request. A failed request counts toward the
// configured unanswered-probe limit.
type KeepaliveClient interface {
	Keepalive() error
}

// VersionedClient is implemented by transports that know the server's SSH
// identification banner. The pool uses it to skip the reused-shell liveness
// probe for OpenSSH and Dropbear, which multiplex cleanly.
type VersionedClient interface {
	RemoteVersion() string
}

// Connection is one authenticated transport with a lease count. It is the
// application's owner of the adapter's Client; the UI and domain never see it.
type Connection struct {
	mu     sync.Mutex
	client Client
	refs   int
	closed bool
	// done closes when the physical transport is closed or discarded, so a
	// forwarding rule can stop reporting active after its transport dies.
	done chan struct{}
	// idle is true while the Connection is parked with no leases.
	idle      bool
	idleSince time.Time
	endpoint  endpointConfig
	// allowIdlePark is false for single-channel, X11, and vendor bastions
	// that cannot host a later shell.
	allowIdlePark bool
	lruElement    *list.Element
	stopTimer     func()
	keepaliveStop chan struct{}
	onZero        func(*Connection)
	onDiscard     func(*Connection, string)
}

func newConnection(client Client) *Connection {
	return &Connection{client: client, done: make(chan struct{})}
}

func newPooledConnection(client Client, endpoint endpointConfig, manager *Manager) *Connection {
	return &Connection{
		client:        client,
		done:          make(chan struct{}),
		endpoint:      endpoint,
		allowIdlePark: !endpoint.noReuse && RemoteAllowsIdleParkedShellReuse(remoteVersion(client)),
		onZero:        manager.pool.park,
		onDiscard:     manager.pool.discard,
	}
}

// acquire takes a reference and returns a Lease. It returns nil when the
// connection is closed, mirroring a pool that has discarded it.
func (c *Connection) acquire() *Lease {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.refs++
	return &Lease{conn: c, kind: LeaseShell}
}

// release drops a reference. The last release either returns the Connection
// to idle parking or closes it when it has no pool owner.
func (c *Connection) release() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.refs--
	zero := c.refs <= 0
	onZero := c.onZero
	var client Client
	if zero && onZero == nil {
		c.closed = true
		c.closeDoneLocked()
		client = c.client
		c.client = nil
	}
	c.mu.Unlock()
	if !zero {
		return nil
	}
	if onZero != nil {
		onZero(c)
		return nil
	}
	if client != nil {
		return client.Close()
	}
	return nil
}

// discard removes a Connection that can no longer be reused and closes it.
func (c *Connection) discard(reason string) {
	if c.onDiscard != nil {
		c.onDiscard(c, reason)
		return
	}
	_ = c.closeTransport(reason)
}

// closeTransport closes the physical transport exactly once.
func (c *Connection) closeTransport(reason string) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.closeDoneLocked()
	c.idle = false
	client := c.client
	c.client = nil
	stop := c.keepaliveStop
	c.keepaliveStop = nil
	c.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if client != nil {
		return client.Close()
	}
	return nil
}

func (c *Connection) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// closeDoneLocked signals transport death exactly once. The caller owns the
// lock that guards closed.
func (c *Connection) closeDoneLocked() {
	if c.done == nil {
		c.done = make(chan struct{})
		close(c.done)
		return
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

func remoteVersion(client Client) string {
	if versioned, ok := client.(VersionedClient); ok {
		return versioned.RemoteVersion()
	}
	return ""
}

// needsLivenessCheck reports whether a shell opened on this parked transport
// must survive the settle probe.
func (c *Connection) needsLivenessCheck() bool {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	return RemoteNeedsReusedShellLivenessCheck(remoteVersion(client))
}

// startKeepalive sends keepalive requests until the transport closes or the
// configured number of consecutive requests go unanswered.
func (c *Connection) startKeepalive(policy KeepalivePolicy, onFailure func()) {
	policy = policy.resolved()
	client, ok := c.client.(KeepaliveClient)
	if !ok || policy.Interval <= 0 || policy.CountMax <= 0 {
		return
	}
	stop := make(chan struct{})
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.keepaliveStop = stop
	c.mu.Unlock()

	go func() {
		ticker := time.NewTicker(policy.Interval)
		defer ticker.Stop()
		failures := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				c.mu.Lock()
				closed := c.closed
				c.mu.Unlock()
				if closed {
					return
				}
				if err := client.Keepalive(); err != nil {
					failures++
					if failures >= policy.CountMax {
						onFailure()
						return
					}
					continue
				}
				failures = 0
			}
		}
	}()
}

// ForwardingClient is the forwarding capability of an authenticated
// transport. DialContext opens a channel through the SSH Host; ListenTCP asks
// the SSH Host to listen for remote forwarding.
type ForwardingClient interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	ListenTCP(address string) (net.Listener, error)
}

// ErrForwardingUnavailable reports a transport that cannot forward, such as a
// test fake or a vendor client without direct-tcpip support.
var ErrForwardingUnavailable = errors.New("connection: forwarding unavailable")

// ErrBindConflict reports a forwarding listener the SSH Host refused to bind.
// It is permanent: retrying the same address cannot succeed.
var ErrBindConflict = errors.New("connection: forwarding bind conflict")

// Lease is a counted claim on a Connection. It is safe to release more than
// once.
type Lease struct {
	conn *Connection
	kind LeaseKind
	// fromIdle records that the lease woke a parked transport. A shell opened
	// on one needs the 350 ms liveness probe.
	fromIdle      bool
	probeDuration time.Duration
	mu            sync.Mutex
	released      bool
}

// ReusedFromIdle reports whether this lease woke a parked Connection.
func (l *Lease) ReusedFromIdle() bool { return l != nil && l.fromIdle }

// Done closes when the leased Connection's transport is closed or discarded.
// A forwarding rule uses it to leave the active state when the transport dies.
func (l *Lease) Done() <-chan struct{} {
	if l == nil || l.conn == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return l.conn.done
}

// Forwarding returns the leased Connection's forwarding capability. A released
// lease, a closed Connection, and a transport that cannot forward all fail
// closed.
func (l *Lease) Forwarding() (ForwardingClient, error) {
	if l == nil {
		return nil, ErrClosed
	}
	l.mu.Lock()
	released := l.released
	l.mu.Unlock()
	if released || l.conn == nil {
		return nil, ErrClosed
	}
	l.conn.mu.Lock()
	client := l.conn.client
	closed := l.conn.closed
	l.conn.mu.Unlock()
	if closed || client == nil {
		return nil, ErrClosed
	}
	forwarding, ok := client.(ForwardingClient)
	if !ok {
		return nil, unsupportedError(ErrForwardingUnavailable)
	}
	return forwarding, nil
}

// OpenTerminal opens an interactive shell channel on the leased Connection.
// When the lease woke a parked transport whose server banner is unknown, the
// shell must survive the liveness window first; a transport that dies
// immediately is discarded and reported as a stale-transport failure.
func (l *Lease) OpenTerminal(ctx context.Context, spec TerminalSpec) (Terminal, error) {
	l.mu.Lock()
	released := l.released
	l.mu.Unlock()
	if released || l.conn == nil {
		return nil, ErrClosed
	}

	l.conn.mu.Lock()
	client := l.conn.client
	closed := l.conn.closed
	l.conn.mu.Unlock()
	if closed || client == nil {
		return nil, ErrClosed
	}

	terminal, err := client.OpenTerminal(ctx, spec)
	if err != nil {
		return nil, classifyDialError(err)
	}
	if l.kind == LeaseShell && l.probeDuration > 0 {
		probed, alive := probeReusedTerminal(terminal, l.probeDuration)
		if !alive {
			l.conn.discard("reused-shell-liveness")
			return nil, staleTransportError()
		}
		terminal = probed
	}
	return terminal, nil
}

// Discard abandons the Connection after the transport proved unusable. The
// lease is released and the physical transport is closed instead of parked.
func (l *Lease) Discard() error {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	l.released = true
	l.mu.Unlock()
	if l.conn != nil {
		l.conn.discard("discarded")
	}
	return nil
}

// Release drops the lease. The last lease parks the Connection for reuse, or
// closes it when it has no pool owner.
func (l *Lease) Release() error {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	l.released = true
	l.mu.Unlock()
	if l.conn == nil {
		return nil
	}
	return l.conn.release()
}

// knownHostIDEncoding is Crockford's base32, the alphabet ULIDs use.
var knownHostIDEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// newKnownHostID returns a ULID for a Known Host.
func newKnownHostID() string {
	var id [16]byte
	milliseconds := uint64(time.Now().UnixMilli())
	id[0] = byte(milliseconds >> 40)
	id[1] = byte(milliseconds >> 32)
	id[2] = byte(milliseconds >> 24)
	id[3] = byte(milliseconds >> 16)
	id[4] = byte(milliseconds >> 8)
	id[5] = byte(milliseconds)
	if _, err := rand.Read(id[6:]); err != nil {
		binary.BigEndian.PutUint64(id[8:], uint64(time.Now().UnixNano()))
	}
	return knownHostIDEncoding.EncodeToString(id[:])
}
