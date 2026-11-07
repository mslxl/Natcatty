// SPDX-License-Identifier: GPL-3.0-or-later

// Package wireguard implements the Network Provider for a WireGuard Network
// Profile with an in-process userspace network stack.
//
// The adapter creates a gVisor netstack through wireguard-go's tun/netstack,
// attaches it to a wireguard-go device, and applies the profile through the
// device's UAPI. It never creates a TUN interface, installs a driver, changes
// the operating system's route table or firewall, opens a listening service, or
// needs root or administrator rights. There is no sidecar and no system
// WireGuard service. FwMark and Table have no meaning here and are never
// applied: inside the netstack, cryptokey routing by AllowedIPs decides which
// peer carries a packet.
//
// Peer Endpoint host names are resolved once at Start with the system resolver
// and re-resolved when the cached resolution expires or the Provider restarts.
// A resolution failure is a typed KindProvider failure, never a reason to dial
// the target outside the tunnel. Target host names are a different matter: they
// are resolved only through the tunnel DNS servers configured in the profile.
// There is deliberately no operating-system resolver fallback. A profile with
// no DNS server can reach IP-literal targets only; a host name then fails with
// a typed KindProvider error before any packet leaves the process.
//
// The Private Key and every Preshared Key are memory-only. They arrive in
// Config, are converted to the UAPI's hexadecimal form inside the backend, and
// never enter a Status, a log line, an error, a Pool key, or the Network Route.
//
// Lifecycle is lazy and retryable: Start may be called again after a failure,
// Close is idempotent, and a failed start never leaves a device behind. While
// running, a status poll keeps the last handshake time, the per-peer rx/tx
// counters, and the current Endpoint available to the Vault surface and the
// Connection Log.
package wireguard

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

const (
	// defaultMTU matches wg-quick's default when a profile leaves MTU unset.
	defaultMTU = domainvault.DefaultWireGuardMTU
	// defaultStatusPollInterval is how often the live device state is sampled
	// for handshake times and counters. It is a cadence, not a synchronization
	// mechanism: a slower or faster tick only changes how fresh the display is.
	defaultStatusPollInterval = 2 * time.Second
	// defaultEndpointTTL bounds how long a resolved Endpoint host name is
	// reused. A dial after the TTL re-resolves it, which is how a network change
	// reaches the device without a platform-specific network-change hook.
	defaultEndpointTTL = 30 * time.Second

	// Defensive limits mirror the Vault's Network Profile limits. The adapter
	// re-checks them because Config can be built by another caller.
	maxPeers      = 256
	maxAddresses  = 64
	maxDNSServers = 32
	maxAllowedIPs = 4096
	maxPeerIDLen  = 64
)

// Errors callers and tests use to classify a WireGuard Net failure. They are
// wrapped in the typed connection error, never returned bare.
var (
	// ErrProviderClosed reports an operation on a closed Provider.
	ErrProviderClosed = errors.New("wireguard: provider is closed")
	// ErrNotRunning reports a Provider that is not in the Running phase and
	// could not be started.
	ErrNotRunning = errors.New("wireguard: provider is not running")
	// ErrUnsupportedNetwork reports a network outside the capability that was
	// asked: tcp/tcp4/tcp6 for DialContext, udp/udp4/udp6 for DialDatagram.
	// The Network Provider port deliberately has no Listen support.
	ErrUnsupportedNetwork = errors.New("wireguard: network is not supported")
	// ErrInvalidConfig reports a profile the adapter cannot run. It never
	// carries key material.
	ErrInvalidConfig = errors.New("wireguard: invalid configuration")
	// ErrPrivateKeyMissing reports a profile without the required Private Key.
	// WireGuard has no interactive login, so this cannot be deferred.
	ErrPrivateKeyMissing = errors.New("wireguard: private key is missing")
	// ErrDNSUnavailable reports a target host name while the profile has no
	// tunnel DNS server. The operating system resolver is never used instead.
	ErrDNSUnavailable = errors.New("wireguard: no tunnel DNS server is configured")
	// ErrEndpointResolution reports a peer Endpoint host name that the system
	// resolver could not turn into an address of a configured family.
	ErrEndpointResolution = errors.New("wireguard: peer endpoint could not be resolved")
)

// HandshakeState is the stable, non-secret handshake summary of one peer or of
// a whole Provider. The UI localizes it; it is never display text.
type HandshakeState string

const (
	// HandshakePending reports that no handshake with the peer has completed.
	HandshakePending HandshakeState = "pending"
	// HandshakeEstablished reports that at least one handshake completed.
	HandshakeEstablished HandshakeState = "established"
)

// Config is one Network Profile's configuration plus the credential plaintext
// read from the Vault field-secret store. PrivateKey and each Peer's
// PresharedKey are memory-only: they never enter a Status, a log line, an
// error, or a persistent record owned by this package.
type Config struct {
	ProfileID string
	Label     string
	Kind      domainvault.NetworkProfileKind

	// Addresses are the local interface addresses, as IP literals or CIDR
	// prefixes. The netstack uses only each address's host part and installs a
	// default route for each enabled family.
	Addresses []string
	// DNS lists the tunnel DNS servers. They are the only resolver the netstack
	// uses; an empty list means only IP-literal targets work.
	DNS []string
	// MTU is the tunnel MTU; zero means defaultMTU.
	MTU int
	// ListenPort is the local UDP port; zero means a random port.
	ListenPort int

	PrivateKey string
	Peers      []Peer
}

// Peer is one WireGuard peer. Endpoint is the configured host:port; a host name
// is resolved by the Provider at Start and when its cached resolution expires.
// An empty Endpoint is valid for a peer that is expected to initiate the
// handshake, but it cannot be reached until it does.
type Peer struct {
	ID                  string
	PublicKey           string
	PresharedKey        string
	Endpoint            string
	AllowedIPs          []string
	PersistentKeepalive int
}

// PeerStatus is the non-secret live state of one configured peer. ID is the
// profile's stable peer ID; PublicKey is deliberately absent because it is not
// needed by any surface. Endpoint is the current Endpoint the device uses,
// which may have roamed from the configured value after an authenticated
// packet.
type PeerStatus struct {
	ID            string
	Endpoint      string
	Handshake     HandshakeState
	LastHandshake time.Time
	RxBytes       uint64
	TxBytes       uint64
}

// Status is the non-secret runtime status of one WireGuard Net Provider. It is
// what the Vault surface and the Connection Log receive. It never contains a
// Private Key or Preshared Key, and its error is already redacted.
type Status struct {
	ProfileID  string
	Label      string
	Kind       string
	Phase      appconnection.ProviderPhase
	ListenPort int
	Peers      []PeerStatus
	Handshake  HandshakeState
	// LastHandshake is the most recent handshake across all peers.
	LastHandshake time.Time
	// RxBytes and TxBytes are the sum over all peers.
	RxBytes uint64
	TxBytes uint64
	// Err is the redacted last failure, or nil while healthy.
	Err error
}

// Endpoint returns the Endpoint a surface should show. It prefers the peer
// whose handshake is most recent, because that is the peer traffic is currently
// using; with no completed handshake it falls back to the first peer with a
// current Endpoint in profile order. It is empty before the first handshake for
// a peer configured without an Endpoint.
func (s Status) Endpoint() string {
	var (
		active     string
		activeTime time.Time
		fallback   string
	)
	for _, peer := range s.Peers {
		if peer.Endpoint == "" {
			continue
		}
		if fallback == "" {
			fallback = peer.Endpoint
		}
		if peer.Handshake == HandshakeEstablished && peer.LastHandshake.After(activeTime) {
			active = peer.Endpoint
			activeTime = peer.LastHandshake
		}
	}
	if active != "" {
		return active
	}
	return fallback
}

// Option customizes a Provider. Observers run on the goroutine that changes the
// status; they must not block and must not call back into the Provider.
type Option func(*Provider)

// WithStatusObserver installs the status callback. It receives every phase
// transition and every live status change (handshake, Endpoint, rx/tx). A nil
// callback is valid and discards status.
func WithStatusObserver(fn func(Status)) Option {
	return func(p *Provider) { p.observe = fn }
}

// WithLogf installs the redacted diagnostic logger the wireguard-go device and
// this adapter use. A nil logger discards diagnostics.
func WithLogf(fn func(format string, args ...any)) Option {
	return func(p *Provider) { p.logf = fn }
}

// withBackendFactory replaces the netstack device. Tests use it to drive the
// state machine without a network.
func withBackendFactory(fn func(backendConfig) (backend, error)) Option {
	return func(p *Provider) {
		if fn != nil {
			p.newBackend = fn
		}
	}
}

// withEndpointResolver replaces the system resolver used for peer Endpoints.
// It is deliberately not used for target host names; those always go through
// the tunnel.
func withEndpointResolver(fn endpointResolver) Option {
	return func(p *Provider) {
		if fn != nil {
			p.resolver = fn
		}
	}
}

// withStatusPollInterval changes the live-status sampling cadence.
func withStatusPollInterval(d time.Duration) Option {
	return func(p *Provider) {
		if d > 0 {
			p.pollInterval = d
		}
	}
}

// withEndpointTTL changes how long a resolved Endpoint is reused. Zero forces a
// re-resolution before every dial.
func withEndpointTTL(d time.Duration) Option {
	return func(p *Provider) {
		if d >= 0 {
			p.endpointTTL = d
		}
	}
}

// withClock replaces the clock used for the Endpoint TTL.
func withClock(fn func() time.Time) Option {
	return func(p *Provider) {
		if fn != nil {
			p.now = fn
		}
	}
}

// endpointResolver is the system-resolver seam. net.DefaultResolver implements
// it. It resolves peer Endpoint host names only.
type endpointResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Provider is one Network Profile's in-process WireGuard Net dial port. It
// implements application/connection's NetworkProvider,
// NetworkProviderDatagram, and NetworkProviderCloser. Create it with
// NewProvider, start it with Start, and release it with Close. It is safe for
// concurrent use.
type Provider struct {
	cfg        Config
	plan       providerPlan
	newBackend func(backendConfig) (backend, error)
	resolver   endpointResolver
	observe    func(Status)
	logf       func(format string, args ...any)
	redactor   secretRedactor

	pollInterval time.Duration
	endpointTTL  time.Duration
	now          func() time.Time

	// refreshMu serializes Endpoint re-resolution so two dials cannot apply
	// different resolution results at the same time.
	refreshMu sync.Mutex

	mu                  sync.Mutex
	backend             backend
	phase               appconnection.ProviderPhase
	status              Status
	closed              bool
	starting            bool
	startDone           chan struct{}
	startCancel         context.CancelFunc
	startErr            error
	endpointsResolvedAt time.Time
	pollCancel          context.CancelFunc
	pollDone            chan struct{}
}

// A mosh Session type-asserts the datagram capability at runtime; this
// assertion keeps dropping DialDatagram a compile error instead of a silent
// fail-closed regression.
var _ appconnection.NetworkProviderDatagram = (*Provider)(nil)

// NewProvider validates and derives one profile's configuration. It creates no
// socket, device, or goroutine: an unused Network Profile must not leave a
// tunnel behind. Start creates the netstack and device.
func NewProvider(cfg Config, opts ...Option) (*Provider, error) {
	cfg.ProfileID = strings.TrimSpace(cfg.ProfileID)
	cfg.Label = strings.TrimSpace(cfg.Label)
	plan, err := planConfig(cfg)
	if err != nil {
		return nil, err
	}
	provider := &Provider{
		cfg:          cfg,
		plan:         plan,
		newBackend:   newNetstackBackend,
		resolver:     net.DefaultResolver,
		pollInterval: defaultStatusPollInterval,
		endpointTTL:  defaultEndpointTTL,
		now:          time.Now,
		redactor:     newSecretRedactor(cfg),
		phase:        appconnection.ProviderPhaseStopped,
	}
	provider.status = Status{
		ProfileID: cfg.ProfileID,
		Label:     cfg.Label,
		Kind:      string(domainvault.NetworkKindWireGuard),
		Phase:     appconnection.ProviderPhaseStopped,
		Handshake: HandshakePending,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(provider)
		}
	}
	if provider.newBackend == nil {
		return nil, fmt.Errorf("%w: no device factory", ErrInvalidConfig)
	}
	if provider.resolver == nil {
		return nil, fmt.Errorf("%w: no endpoint resolver", ErrInvalidConfig)
	}
	return provider, nil
}

// Start creates the netstack and device, applies the profile, and brings the
// device up. A previous failed attempt is retried with a fresh device. On
// failure the device is closed before Start returns, so a cancelled or failed
// start never leaves a live tunnel behind.
func (p *Provider) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return p.errorFor(appconnection.ProviderPhaseStopped, ErrProviderClosed)
	}
	if p.phase == appconnection.ProviderPhaseRunning && p.backend != nil {
		p.mu.Unlock()
		return nil
	}
	if p.starting {
		done := p.startDone
		p.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return p.errorFor(appconnection.ProviderPhaseStarting, ctx.Err())
		}
		p.mu.Lock()
		err := p.startErr
		p.mu.Unlock()
		return err
	}

	// A device left by an earlier runtime failure is retired before the
	// replacement starts, so one profile never runs two devices.
	old := p.backend
	p.backend = nil
	p.starting = true
	p.startDone = make(chan struct{})
	startCtx, cancel := context.WithCancel(ctx)
	p.startCancel = cancel
	p.startErr = nil
	p.phase = appconnection.ProviderPhaseStarting
	p.status.Phase = appconnection.ProviderPhaseStarting
	p.status.Peers = nil
	p.status.Handshake = HandshakePending
	p.status.LastHandshake = time.Time{}
	p.status.RxBytes = 0
	p.status.TxBytes = 0
	p.status.ListenPort = 0
	p.status.Err = nil
	pollCancel := p.pollCancel
	pollDone := p.pollDone
	p.pollCancel = nil
	p.pollDone = nil
	p.mu.Unlock()
	p.emit()
	if pollCancel != nil {
		pollCancel()
		if pollDone != nil {
			<-pollDone
		}
	}
	if old != nil {
		_ = old.Close()
	}

	err := p.runStart(startCtx)

	p.mu.Lock()
	p.starting = false
	p.startCancel = nil
	p.startErr = err
	done := p.startDone
	p.startDone = nil
	p.mu.Unlock()
	cancel()
	close(done)
	return err
}

// runStart builds the backend and starts it. It owns the backend until it
// returns: on every failure path the backend is closed and cleared so the
// Provider never owns a live device for a failed attempt.
func (p *Provider) runStart(ctx context.Context) error {
	resolved, err := p.resolveEndpoints(ctx, p.plan.peers)
	if err != nil {
		return p.failStart(backendStatus{}, appconnection.ProviderPhaseError, err)
	}
	deviceConfig := p.plan.backendConfig(resolved)
	deviceConfig.Logf = p.redactor.redactingLogf(p.logf)
	backend, err := p.newBackend(deviceConfig)
	if err != nil {
		return p.failStart(backendStatus{}, appconnection.ProviderPhaseError, err)
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = backend.Close()
		return p.errorFor(appconnection.ProviderPhaseStopped, ErrProviderClosed)
	}
	p.backend = backend
	p.endpointsResolvedAt = p.now()
	p.mu.Unlock()

	status, err := backend.Start(ctx)
	if err != nil {
		phase := appconnection.ProviderPhaseError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			phase = appconnection.ProviderPhaseStopped
		}
		return p.failStartWithBackend(backend, status, phase, err)
	}
	if status.Phase != "" && status.Phase != appconnection.ProviderPhaseRunning {
		return p.failStartWithBackend(backend, status, appconnection.ProviderPhaseError,
			fmt.Errorf("wireguard: device reported phase %q", status.Phase))
	}
	status.Phase = appconnection.ProviderPhaseRunning

	p.mu.Lock()
	if p.closed || p.backend != backend {
		p.mu.Unlock()
		_ = backend.Close()
		return p.errorFor(appconnection.ProviderPhaseStopped, ErrProviderClosed)
	}
	next := p.statusFromBackendLocked(status, nil)
	p.status = next
	p.phase = appconnection.ProviderPhaseRunning
	pollCtx, pollCancel := context.WithCancel(context.Background())
	pollDone := make(chan struct{})
	p.pollCancel = pollCancel
	p.pollDone = pollDone
	p.mu.Unlock()
	p.emit()

	go p.poll(pollCtx, backend, pollDone)
	return nil
}

// failStart records a startup failure with no backend to close.
func (p *Provider) failStart(status backendStatus, phase appconnection.ProviderPhase, cause error) error {
	return p.failStartWithBackend(nil, status, phase, cause)
}

// failStartWithBackend records a startup failure and closes the backend once.
// It must be called before the caller releases the Provider.
func (p *Provider) failStartWithBackend(b backend, status backendStatus,
	phase appconnection.ProviderPhase, cause error) error {
	cause = p.redactor.redactError(cause)
	p.mu.Lock()
	p.backend = nil
	next := p.statusFromBackendLocked(status, cause)
	next.Phase = phase
	p.status = next
	p.phase = phase
	p.mu.Unlock()
	p.emit()
	if b != nil {
		_ = b.Close()
	}
	return p.errorFor(phase, cause)
}

// poll samples the live device until the Provider closes or the device fails.
func (p *Provider) poll(ctx context.Context, backend backend, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		status, err := backend.Status(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.failRuntime(backend, err)
			return
		}
		alive, changed := p.applyLiveStatus(backend, status)
		if !alive {
			return
		}
		if changed {
			p.emit()
		}
	}
}

// applyLiveStatus copies one live backend status onto the Provider. alive is
// false when the Provider was closed or replaced the backend while Status was
// being read; changed reports whether a surface-visible value moved.
func (p *Provider) applyLiveStatus(backend backend, status backendStatus) (alive, changed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.backend != backend {
		return false, false
	}
	status.Phase = appconnection.ProviderPhaseRunning
	next := p.statusFromBackendLocked(status, nil)
	changed = !statusEqual(p.status, next)
	p.status = next
	p.phase = appconnection.ProviderPhaseRunning
	return true, changed
}

// failRuntime records a device failure observed while the Provider was running.
// The failed backend is released so a later Start builds a fresh device.
func (p *Provider) failRuntime(backend backend, cause error) {
	cause = p.redactor.redactError(cause)
	p.mu.Lock()
	if p.closed || p.backend != backend {
		p.mu.Unlock()
		return
	}
	p.backend = nil
	p.phase = appconnection.ProviderPhaseError
	p.status.Phase = appconnection.ProviderPhaseError
	p.status.Err = cause
	p.mu.Unlock()
	p.emit()
	_ = backend.Close()
}

// DialContext dials one TCP address through the netstack. It accepts tcp, tcp4,
// and tcp6 only; datagrams have their own capability, DialDatagram. A
// provider-level failure is KindProvider with profile context; an unreachable
// target remains KindNetwork and a deadline remains KindTimeout.
func (p *Provider) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if !tcpNetwork(network) {
		return nil, p.unsupportedNetwork(network)
	}
	return p.dial(ctx, network, address)
}

// DialDatagram opens one connected UDP flow through the netstack. The address
// is resolved by the netstack with the tunnel's DNS servers only, so the
// operating-system resolver is never consulted; checkTargetDNS rejects a name
// when the profile has no tunnel DNS. Only udp, udp4, and udp6 are accepted.
func (p *Provider) DialDatagram(ctx context.Context, network, address string) (net.Conn, error) {
	if !udpNetwork(network) {
		return nil, p.unsupportedNetwork(network)
	}
	return p.dial(ctx, network, address)
}

func (p *Provider) unsupportedNetwork(network string) error {
	return &appconnection.Error{
		Kind:     appconnection.KindUnsupported,
		Cause:    fmt.Errorf("%w: %q", ErrUnsupportedNetwork, network),
		Provider: p.providerInfo(p.Phase()),
	}
}

// dial carries one flow through the running netstack and classifies a failure
// against the device's live phase.
func (p *Provider) dial(ctx context.Context, network, address string) (net.Conn, error) {
	p.mu.Lock()
	closed := p.closed
	phase := p.phase
	b := p.backend
	p.mu.Unlock()
	if closed {
		return nil, p.errorFor(appconnection.ProviderPhaseStopped, ErrProviderClosed)
	}
	if b == nil || phase != appconnection.ProviderPhaseRunning {
		if err := p.Start(ctx); err != nil {
			return nil, err
		}
		p.mu.Lock()
		b = p.backend
		phase = p.phase
		p.mu.Unlock()
	}
	if b == nil || phase != appconnection.ProviderPhaseRunning {
		return nil, p.errorFor(phase, ErrNotRunning)
	}

	// Refresh a stale Endpoint host-name resolution before dialing. A failure
	// here is provider-level: the target must never be reached by another path.
	if err := p.refreshEndpoints(ctx, b); err != nil {
		return nil, err
	}
	if err := p.checkTargetDNS(address); err != nil {
		return nil, err
	}

	conn, err := b.Dial(ctx, network, address)
	if err == nil {
		return p.wrapConn(conn, b), nil
	}
	cause := p.redactor.redactError(err)

	// A failed dial can mean the device stopped after startup. Ask the backend
	// for its live phase even when Status reports an error: a dead device
	// returns PhaseError together with "device stopped unexpectedly", and
	// trusting only statusErr == nil would classify that race as an ordinary
	// network failure with a stale running phase. Caller cancellation stays
	// authoritative: the dial was interrupted, not necessarily the Provider.
	live, statusErr := b.Status(ctx)
	if live.Phase == "" && statusErr != nil && ctx.Err() == nil {
		// The backend could not report a phase. Unknown on a live error means
		// the tunnel cannot be trusted to carry the retry, so fail closed.
		live.Phase = appconnection.ProviderPhaseError
	}
	if live.Phase != "" && live.Phase != appconnection.ProviderPhaseRunning &&
		ctx.Err() == nil {
		failure := cause
		if statusErr != nil {
			failure = p.redactor.redactError(statusErr)
		}
		applied := false
		p.mu.Lock()
		if !p.closed && p.backend == b {
			next := p.statusFromBackendLocked(live, failure)
			next.Phase = live.Phase
			p.status = next
			p.phase = live.Phase
			applied = true
		}
		p.mu.Unlock()
		if applied {
			p.emit()
		}
		return nil, p.errorFor(live.Phase, failure)
	}
	// netstack translates a cancelled context into its own sentinel, so the
	// caller's context is the authoritative cancellation signal.
	kind := appconnection.KindNetwork
	switch {
	case errors.Is(ctx.Err(), context.Canceled) || errors.Is(cause, context.Canceled):
		kind = appconnection.KindCancelled
	case errors.Is(ctx.Err(), context.DeadlineExceeded),
		errors.Is(cause, context.DeadlineExceeded),
		isTimeout(cause):
		kind = appconnection.KindTimeout
	}
	// A failed dial can mean a network change invalidated a peer address. Mark
	// cached host-name resolutions stale so the next attempt re-resolves them
	// instead of reusing a dead address until the TTL expires. Cancellation says
	// nothing about the network and keeps the cache.
	if kind != appconnection.KindCancelled {
		p.mu.Lock()
		if !p.closed && p.backend == b {
			p.endpointsResolvedAt = time.Time{}
		}
		p.mu.Unlock()
	}
	return nil, &appconnection.Error{Kind: kind, Cause: cause,
		Provider: p.providerInfo(phase)}
}

// wrapConn tags a post-dial I/O failure with the Provider state. An open
// Connection whose Provider was closed or replaced must surface KindProvider on
// its next I/O instead of an unclassified transport error that could invite a
// direct retry. While the same device is running, ordinary transport errors
// pass through unchanged.
func (p *Provider) wrapConn(conn net.Conn, b backend) net.Conn {
	return appconnection.WrapProviderConn(conn,
		func() (appconnection.ProviderPhase, *appconnection.ProviderInfo) {
			p.mu.Lock()
			phase := p.phase
			switch {
			case p.closed:
				phase = appconnection.ProviderPhaseStopped
			case p.backend != b:
				phase = appconnection.ProviderPhaseError
			}
			p.mu.Unlock()
			return phase, p.providerInfo(phase)
		})
}

// isTimeout reports whether err carries a net.Error timeout classification.
func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// checkTargetDNS enforces the tunnel-only DNS rule before a packet is sent.
// An IP literal is always allowed. A host name is allowed only when at least
// one tunnel DNS server is configured; netstack then resolves it through the
// tunnel. The operating system resolver is never consulted for a target.
func (p *Provider) checkTargetDNS(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		// The netstack reports a malformed address as an ordinary dial error.
		return nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	if len(p.plan.dnsServers) == 0 {
		return p.errorFor(p.Phase(), fmt.Errorf("%w: %q is not an IP literal",
			ErrDNSUnavailable, host))
	}
	return nil
}

// Close releases the device. It is idempotent, waits for an in-flight start so
// Close is never concurrent with Start, and closes the device exactly once.
func (p *Provider) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	starting := p.starting
	done := p.startDone
	cancel := p.startCancel
	pollCancel := p.pollCancel
	pollDone := p.pollDone
	p.pollCancel = nil
	p.pollDone = nil
	p.phase = appconnection.ProviderPhaseStopping
	p.status.Phase = appconnection.ProviderPhaseStopping
	p.mu.Unlock()
	p.emit()

	if starting {
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
	}
	if pollCancel != nil {
		pollCancel()
		if pollDone != nil {
			<-pollDone
		}
	}

	p.mu.Lock()
	b := p.backend
	p.backend = nil
	p.phase = appconnection.ProviderPhaseStopped
	p.status.Phase = appconnection.ProviderPhaseStopped
	p.status.Err = nil
	p.mu.Unlock()
	p.emit()
	if b == nil {
		return nil
	}
	return b.Close()
}

// Status returns a snapshot of the last reported state. It never consults the
// network.
func (p *Provider) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneStatus(p.status)
}

// Phase returns the current lifecycle phase.
func (p *Provider) Phase() appconnection.ProviderPhase {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.phase
}

// emit publishes a status snapshot outside the Provider lock. The observer
// must be non-blocking and must not call back into this Provider.
func (p *Provider) emit() {
	p.mu.Lock()
	if p.observe == nil {
		p.mu.Unlock()
		return
	}
	status := cloneStatus(p.status)
	observe := p.observe
	p.mu.Unlock()
	observe(status)
}

// errorFor attaches the profile identity to a provider-level failure. A
// cancelled or expired caller keeps its transport Kind; everything else is
// KindProvider.
func (p *Provider) errorFor(phase appconnection.ProviderPhase, cause error) error {
	cause = p.redactor.redactError(cause)
	kind := appconnection.KindProvider
	switch {
	case errors.Is(cause, context.Canceled):
		kind = appconnection.KindCancelled
	case errors.Is(cause, context.DeadlineExceeded):
		kind = appconnection.KindTimeout
	}
	return &appconnection.Error{Kind: kind, Cause: cause, Provider: p.providerInfo(phase)}
}

func (p *Provider) providerInfo(phase appconnection.ProviderPhase) *appconnection.ProviderInfo {
	return &appconnection.ProviderInfo{
		ProfileID: p.cfg.ProfileID,
		Label:     p.cfg.Label,
		Kind:      string(domainvault.NetworkKindWireGuard),
		Phase:     phase,
	}
}

// statusFromBackendLocked maps a backend status onto the public Status. The
// caller must hold p.mu. Only configured peers are reported, in profile order,
// and only non-secret fields are copied.
func (p *Provider) statusFromBackendLocked(status backendStatus, cause error) Status {
	next := Status{
		ProfileID:  p.cfg.ProfileID,
		Label:      p.cfg.Label,
		Kind:       string(domainvault.NetworkKindWireGuard),
		Phase:      status.Phase,
		ListenPort: status.ListenPort,
		Handshake:  HandshakePending,
		Err:        cause,
	}
	if next.Phase == "" {
		next.Phase = appconnection.ProviderPhaseRunning
	}
	byKey := make(map[string]backendPeerStatus, len(status.Peers))
	for _, peer := range status.Peers {
		byKey[peer.PublicKeyHex] = peer
	}
	for _, configured := range p.plan.peers {
		peer := PeerStatus{ID: configured.id, Handshake: HandshakePending}
		if live, ok := byKey[configured.publicKeyHex]; ok {
			peer.Endpoint = live.Endpoint
			peer.RxBytes = live.RxBytes
			peer.TxBytes = live.TxBytes
			if !live.LastHandshake.IsZero() {
				peer.LastHandshake = live.LastHandshake
				peer.Handshake = HandshakeEstablished
				next.Handshake = HandshakeEstablished
				if live.LastHandshake.After(next.LastHandshake) {
					next.LastHandshake = live.LastHandshake
				}
			}
		}
		next.RxBytes += peer.RxBytes
		next.TxBytes += peer.TxBytes
		next.Peers = append(next.Peers, peer)
	}
	return next
}

func statusEqual(a, b Status) bool {
	if a.ProfileID != b.ProfileID || a.Label != b.Label || a.Kind != b.Kind ||
		a.Phase != b.Phase || a.ListenPort != b.ListenPort ||
		a.Handshake != b.Handshake || !a.LastHandshake.Equal(b.LastHandshake) ||
		a.RxBytes != b.RxBytes || a.TxBytes != b.TxBytes ||
		errorText(a.Err) != errorText(b.Err) || len(a.Peers) != len(b.Peers) {
		return false
	}
	for i := range a.Peers {
		one, two := a.Peers[i], b.Peers[i]
		if one.ID != two.ID || one.Endpoint != two.Endpoint ||
			one.Handshake != two.Handshake || !one.LastHandshake.Equal(two.LastHandshake) ||
			one.RxBytes != two.RxBytes || one.TxBytes != two.TxBytes {
			return false
		}
	}
	return true
}

func cloneStatus(status Status) Status {
	status.Peers = append([]PeerStatus(nil), status.Peers...)
	return status
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func tcpNetwork(network string) bool {
	switch network {
	case "tcp", "tcp4", "tcp6":
		return true
	default:
		return false
	}
}

func udpNetwork(network string) bool {
	switch network {
	case "udp", "udp4", "udp6":
		return true
	default:
		return false
	}
}

// resolveEndpoints resolves every peer Endpoint that has a host name. IP
// literals are used as-is after a family check against the configured local
// addresses.
func (p *Provider) resolveEndpoints(ctx context.Context, peers []normalizedPeer) (map[string]string, error) {
	resolved := make(map[string]string, len(peers))
	for _, peer := range peers {
		if !peer.hasEndpoint {
			continue
		}
		address, err := p.resolveEndpoint(ctx, peer)
		if err != nil {
			return nil, err
		}
		resolved[peer.publicKeyHex] = address
	}
	return resolved, nil
}

func (p *Provider) resolveEndpoint(ctx context.Context, peer normalizedPeer) (string, error) {
	if address, err := netip.ParseAddr(peer.endpointHost); err == nil {
		address = address.Unmap()
		if err := p.checkAddressFamily(address); err != nil {
			return "", fmt.Errorf("%w: peer %q: %v", ErrEndpointResolution, peer.name(), err)
		}
		return netip.AddrPortFrom(address, peer.endpointPort).String(), nil
	}
	addresses, err := p.resolver.LookupNetIP(ctx, "ip", peer.endpointHost)
	if err != nil {
		return "", fmt.Errorf("%w: peer %q: %v", ErrEndpointResolution, peer.name(), err)
	}
	address, err := p.pickAddress(addresses)
	if err != nil {
		return "", fmt.Errorf("%w: peer %q: %v", ErrEndpointResolution, peer.name(), err)
	}
	return netip.AddrPortFrom(address, peer.endpointPort).String(), nil
}

// checkAddressFamily rejects an address the netstack has no route for. A
// profile with no IPv4 address cannot carry IPv4, and vice versa.
func (p *Provider) checkAddressFamily(address netip.Addr) error {
	switch {
	case address.Is4() && !p.plan.hasV4:
		return errors.New("no IPv4 address is configured for this profile")
	case address.Is6() && !p.plan.hasV6:
		return errors.New("no IPv6 address is configured for this profile")
	default:
		return nil
	}
}

// pickAddress chooses an address of a configured family, preferring IPv4 to
// match the common deployment and the order most system resolvers return.
func (p *Provider) pickAddress(addresses []netip.Addr) (netip.Addr, error) {
	var v4, v6 []netip.Addr
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || address.Zone() != "" {
			continue
		}
		if address.Is4() {
			v4 = append(v4, address)
		} else {
			v6 = append(v6, address)
		}
	}
	if p.plan.hasV4 && len(v4) > 0 {
		return v4[0], nil
	}
	if p.plan.hasV6 && len(v6) > 0 {
		return v6[0], nil
	}
	return netip.Addr{}, errors.New("the resolver returned no address of a configured family")
}

// refreshEndpoints re-resolves host-name Endpoints whose cached resolution is
// older than the TTL and applies the result to the live device.
func (p *Provider) refreshEndpoints(ctx context.Context, backend backend) error {
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()

	p.mu.Lock()
	if p.closed || p.phase != appconnection.ProviderPhaseRunning || p.backend != backend {
		p.mu.Unlock()
		return nil
	}
	resolvedAt := p.endpointsResolvedAt
	p.mu.Unlock()
	if !p.plan.hasHostnameEndpoints() {
		return nil
	}
	if p.now().Sub(resolvedAt) < p.endpointTTL {
		return nil
	}

	updates := make(map[string]string)
	for _, peer := range p.plan.peers {
		if !peer.hasEndpoint || peer.endpointIsIP {
			continue
		}
		address, err := p.resolveEndpoint(ctx, peer)
		if err != nil {
			return p.errorFor(appconnection.ProviderPhaseRunning, err)
		}
		updates[peer.publicKeyHex] = address
	}
	if len(updates) == 0 {
		return nil
	}
	if err := backend.SetEndpoints(ctx, updates); err != nil {
		return p.errorFor(appconnection.ProviderPhaseRunning,
			fmt.Errorf("%w: %v", ErrEndpointResolution, p.redactor.redactError(err)))
	}
	p.mu.Lock()
	if p.backend == backend {
		p.endpointsResolvedAt = p.now()
	}
	p.mu.Unlock()
	return nil
}

// providerPlan is the normalized, immutable configuration the Provider applies.
type providerPlan struct {
	privateKeyHex  string
	listenPort     uint16
	mtu            int
	localAddresses []netip.Addr
	dnsServers     []netip.Addr
	hasV4          bool
	hasV6          bool
	peers          []normalizedPeer
}

type normalizedPeer struct {
	id                  string
	publicKeyHex        string
	presharedKeyHex     string
	endpointHost        string
	endpointPort        uint16
	endpointIsIP        bool
	hasEndpoint         bool
	allowedIPs          []netip.Prefix
	persistentKeepalive uint16
}

// name returns a non-secret diagnostic name for the peer.
func (p normalizedPeer) name() string {
	if p.id != "" {
		return p.id
	}
	return "without an id"
}

// hasHostnameEndpoints reports whether any Endpoint needs the system resolver.
func (p providerPlan) hasHostnameEndpoints() bool {
	for _, peer := range p.peers {
		if peer.hasEndpoint && !peer.endpointIsIP {
			return true
		}
	}
	return false
}

// backendConfig converts the plan plus resolved Endpoints into the backend's
// configuration. Key material remains in memory only.
func (p providerPlan) backendConfig(resolved map[string]string) backendConfig {
	config := backendConfig{
		PrivateKeyHex:  p.privateKeyHex,
		ListenPort:     p.listenPort,
		MTU:            p.mtu,
		LocalAddresses: append([]netip.Addr(nil), p.localAddresses...),
		DNSServers:     append([]netip.Addr(nil), p.dnsServers...),
	}
	for _, peer := range p.peers {
		backendPeer := backendPeer{
			PublicKeyHex:        peer.publicKeyHex,
			PresharedKeyHex:     peer.presharedKeyHex,
			AllowedIPs:          append([]netip.Prefix(nil), peer.allowedIPs...),
			PersistentKeepalive: peer.persistentKeepalive,
		}
		if peer.hasEndpoint {
			backendPeer.Endpoint = resolved[peer.publicKeyHex]
		}
		config.Peers = append(config.Peers, backendPeer)
	}
	return config
}

// planConfig validates and normalizes one profile. It returns an error wrapping
// ErrInvalidConfig and never echoes a key value.
func planConfig(cfg Config) (providerPlan, error) {
	if cfg.ProfileID == "" {
		return providerPlan{}, invalid("empty profile id")
	}
	if cfg.Kind != domainvault.NetworkKindWireGuard {
		return providerPlan{}, invalid("kind %q", cfg.Kind)
	}
	if len(cfg.Addresses) == 0 {
		return providerPlan{}, invalid("at least one Address is required")
	}
	if len(cfg.Addresses) > maxAddresses {
		return providerPlan{}, invalid("too many Address entries")
	}
	plan := providerPlan{mtu: cfg.MTU}
	if plan.mtu == 0 {
		plan.mtu = defaultMTU
	}
	if plan.mtu < 576 || plan.mtu > 65535 {
		return providerPlan{}, invalid("MTU must be between 576 and 65535")
	}
	if cfg.ListenPort < 0 || cfg.ListenPort > 65535 {
		return providerPlan{}, invalid("ListenPort must be between 0 and 65535")
	}
	plan.listenPort = uint16(cfg.ListenPort)

	seenAddresses := make(map[netip.Addr]bool, len(cfg.Addresses))
	for _, raw := range cfg.Addresses {
		address, ok := parseLocalAddress(raw)
		if !ok {
			return providerPlan{}, invalid("Address is not an IP address or prefix")
		}
		if seenAddresses[address] {
			continue
		}
		seenAddresses[address] = true
		plan.localAddresses = append(plan.localAddresses, address)
		if address.Is4() {
			plan.hasV4 = true
		} else {
			plan.hasV6 = true
		}
	}

	if len(cfg.DNS) > maxDNSServers {
		return providerPlan{}, invalid("too many DNS servers")
	}
	seenDNS := make(map[netip.Addr]bool, len(cfg.DNS))
	for _, raw := range cfg.DNS {
		address, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || address.Zone() != "" {
			return providerPlan{}, invalid("DNS is not an IP address")
		}
		address = address.Unmap()
		if seenDNS[address] {
			continue
		}
		seenDNS[address] = true
		plan.dnsServers = append(plan.dnsServers, address)
	}

	key, ok := decodeKey(cfg.PrivateKey)
	if cfg.PrivateKey == "" {
		return providerPlan{}, fmt.Errorf("%w: PrivateKey is required", ErrPrivateKeyMissing)
	}
	if !ok {
		return providerPlan{}, invalid("PrivateKey is not a 32-byte WireGuard key")
	}
	plan.privateKeyHex = hex.EncodeToString(key[:])

	if len(cfg.Peers) == 0 {
		return providerPlan{}, invalid("at least one Peer is required")
	}
	if len(cfg.Peers) > maxPeers {
		return providerPlan{}, invalid("too many Peer entries")
	}
	publicKeys := make(map[string]bool, len(cfg.Peers))
	totalAllowedIPs := 0
	for _, raw := range cfg.Peers {
		peer, count, err := normalizePeer(raw)
		if err != nil {
			return providerPlan{}, err
		}
		if publicKeys[peer.publicKeyHex] {
			return providerPlan{}, invalid("another Peer already uses this PublicKey")
		}
		publicKeys[peer.publicKeyHex] = true
		totalAllowedIPs += count
		plan.peers = append(plan.peers, peer)
	}
	if totalAllowedIPs == 0 {
		return providerPlan{}, invalid("at least one AllowedIPs entry is required")
	}
	return plan, nil
}

func normalizePeer(raw Peer) (normalizedPeer, int, error) {
	peer := normalizedPeer{id: strings.TrimSpace(raw.ID)}
	if len(peer.id) > maxPeerIDLen {
		return normalizedPeer{}, 0, invalid("Peer id is too long")
	}
	publicKey, ok := decodeKey(raw.PublicKey)
	if !ok {
		return normalizedPeer{}, 0, invalid("Peer PublicKey is not a 32-byte WireGuard key")
	}
	peer.publicKeyHex = hex.EncodeToString(publicKey[:])
	if raw.PresharedKey != "" {
		presharedKey, ok := decodeKey(raw.PresharedKey)
		if !ok {
			return normalizedPeer{}, 0, invalid("Peer PresharedKey is not a 32-byte WireGuard key")
		}
		peer.presharedKeyHex = hex.EncodeToString(presharedKey[:])
	}
	if raw.PersistentKeepalive < 0 || raw.PersistentKeepalive > 65535 {
		return normalizedPeer{}, 0, invalid("PersistentKeepalive must be between 0 and 65535")
	}
	peer.persistentKeepalive = uint16(raw.PersistentKeepalive)

	if endpoint := strings.TrimSpace(raw.Endpoint); endpoint != "" {
		host, port, isIP, err := parseEndpoint(endpoint)
		if err != nil {
			return normalizedPeer{}, 0, err
		}
		peer.endpointHost = host
		peer.endpointPort = port
		peer.endpointIsIP = isIP
		peer.hasEndpoint = true
	}

	if len(raw.AllowedIPs) > maxAllowedIPs {
		return normalizedPeer{}, 0, invalid("too many AllowedIPs entries")
	}
	for _, value := range raw.AllowedIPs {
		prefix, ok := parseAllowedPrefix(value)
		if !ok {
			return normalizedPeer{}, 0, invalid("AllowedIPs entry is not an IP prefix")
		}
		peer.allowedIPs = append(peer.allowedIPs, prefix)
	}
	return peer, len(peer.allowedIPs), nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}

// parseLocalAddress returns the host part of one Address entry. A CIDR mask is
// deliberately discarded: the netstack assigns the host address and installs a
// default route, which is the only routing this Provider provides.
func parseLocalAddress(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if prefix, err := netip.ParsePrefix(value); err == nil {
		address := prefix.Addr()
		if address.Zone() != "" {
			return netip.Addr{}, false
		}
		return address.Unmap(), true
	}
	address, err := netip.ParseAddr(value)
	if err != nil || address.Zone() != "" {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func parseAllowedPrefix(value string) (netip.Prefix, bool) {
	value = strings.TrimSpace(value)
	if prefix, err := netip.ParsePrefix(value); err == nil {
		if prefix.Addr().Zone() != "" {
			return netip.Prefix{}, false
		}
		return prefix.Masked(), true
	}
	address, err := netip.ParseAddr(value)
	if err != nil || address.Zone() != "" {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(address, address.BitLen()), true
}

// parseEndpoint splits one configured host:port. An IPv6 literal must be
// bracketed, exactly as the UAPI expects. isIP reports that no system resolver
// is needed.
func parseEndpoint(value string) (host string, port uint16, isIP bool, err error) {
	host, portText, splitErr := net.SplitHostPort(value)
	if splitErr != nil || strings.TrimSpace(host) == "" {
		return "", 0, false, invalid("Peer Endpoint is not host:port")
	}
	parsedPort, parseErr := strconv.ParseUint(portText, 10, 16)
	if parseErr != nil || parsedPort == 0 {
		return "", 0, false, invalid("Peer Endpoint port must be between 1 and 65535")
	}
	if len(host) > 255 {
		return "", 0, false, invalid("Peer Endpoint host is too long")
	}
	for _, r := range host {
		if r <= ' ' || r == 0x7f {
			return "", 0, false, invalid("Peer Endpoint host is invalid")
		}
		switch r {
		case '/', '\\', '?', '#', '@', '[', ']':
			return "", 0, false, invalid("Peer Endpoint host is invalid")
		}
	}
	if address, addressErr := netip.ParseAddr(host); addressErr == nil {
		if address.Zone() != "" {
			return "", 0, false, invalid("Peer Endpoint host is invalid")
		}
		return host, uint16(parsedPort), true, nil
	}
	return host, uint16(parsedPort), false, nil
}

// decodeKey accepts the standard 44-character base64 form and the 64-character
// hexadecimal form of a 32-byte WireGuard key. Strict decoding rejects
// non-canonical padding bits.
func decodeKey(value string) ([32]byte, bool) {
	var key [32]byte
	value = strings.TrimSpace(value)
	if value == "" {
		return key, false
	}
	if len(value) == 64 {
		decoded, err := hex.DecodeString(value)
		if err == nil && len(decoded) == 32 {
			copy(key[:], decoded)
			return key, true
		}
	}
	if len(value) != 44 {
		return key, false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return key, false
	}
	copy(key[:], decoded)
	return key, true
}

// ValidateForNetstack checks the runtime constraints an imported or hand-edited
// profile must satisfy before a netstack device can be created: at least one
// local Address, at least one Peer, a usable Private Key, and at least one
// AllowedIPs entry. It returns an error wrapping ErrInvalidConfig and never
// includes key material.
func ValidateForNetstack(cfg Config) error {
	_, err := planConfig(cfg)
	return err
}

// secretRedactor removes every configured credential from diagnostics while
// preserving the error chain so errors.Is and errors.As still classify it.
type secretRedactor []string

func newSecretRedactor(cfg Config) secretRedactor {
	secrets := make([]string, 0, 1+2*len(cfg.Peers))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		secrets = append(secrets, value)
		if key, ok := decodeKey(value); ok {
			secrets = append(secrets, hex.EncodeToString(key[:]))
		}
	}
	add(cfg.PrivateKey)
	for _, peer := range cfg.Peers {
		add(peer.PresharedKey)
	}
	return secrets
}

func (r secretRedactor) redactError(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	redacted := r.redactText(text)
	if redacted == text {
		return err
	}
	return &redactedError{cause: err, message: redacted}
}

func (r secretRedactor) redactText(text string) string {
	if text == "" {
		return text
	}
	for _, secret := range r {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return text
}

// redactingLogf adapts a diagnostic logger so no line can carry a credential.
func (r secretRedactor) redactingLogf(logf func(format string, args ...any)) func(format string, args ...any) {
	if logf == nil {
		return nil
	}
	return func(format string, args ...any) {
		logf("%s", r.redactText(fmt.Sprintf(format, args...)))
	}
}

type redactedError struct {
	cause   error
	message string
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.cause }
