// SPDX-License-Identifier: GPL-3.0-or-later

// Package tailscale implements the Network Provider for a Tailscale Network
// Profile with an in-process tsnet node.
//
// The adapter starts one tsnet.Server per Network Profile. It never talks to a
// system Tailscale daemon, never starts a sidecar, never creates a TUN device,
// and needs neither root nor administrator rights. tsnet runs its own
// LocalBackend inside the application process, so an embedded node is a
// separate tailnet device from any system Tailscale client.
//
// The state directory is credential material: it holds the node identity.
// Every profile therefore gets its own directory under the application data
// directory, created with mode 0700. It must never point at a system Tailscale
// state directory and must not be cloud-synced or exported.
//
// The Auth Key is memory-only. It is handed to tsnet.Server.AuthKey and never
// written to the state directory, a log line, an error, or a status event.
// Only HasAuthKey markers and non-secret status cross this package boundary.
//
// Without an Auth Key, or when a stored key no longer authenticates, the node
// reports NeedsLogin with a short-lived Auth URL. The server stays alive while
// the user completes the browser login; a status watcher publishes the later
// Running transition. Cancel stops one such attempt without closing the
// Provider, so a retry or the next Acquire can join again. The Auth URL is
// display material for that flow and never enters an error, a log line, or the
// Connection Log.
//
// Privacy: tsnet uploads diagnostic logs to log.tailscale.com by default. The
// Logf/UserLogf hooks below only redact and forward local lines; they do not
// disable that upload. This version cannot fully turn it off, and the Vault
// editor carries that disclosure.
package tailscale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

const (
	// defaultHostnamePrefix names the embedded node when the profile does not
	// provide a hostname. It keeps the application recognizable in the tailnet
	// device list.
	defaultHostnamePrefix = "natcatty"
	// maxHostnameLength matches the DNS label limit Tailscale applies to a
	// node name.
	maxHostnameLength = 63
	// authKeyLoginGrace is how long a keyed start may remain in NeedsLogin
	// before the provider reports that the key did not complete the login.
	// tsnet briefly enters NeedsLogin even for a valid key, so failing on the
	// first notification would produce false negatives.
	authKeyLoginGrace = 5 * time.Second
)

// Errors callers and tests use to classify a Tailscale Net failure. They are
// wrapped in the typed connection error, never returned bare.
var (
	// ErrNeedsLogin reports that the node is not registered and needs an
	// interactive login. The status observer carries the Auth URL separately;
	// it is deliberately not part of this error.
	ErrNeedsLogin = errors.New("tailscale: interactive login required")
	// ErrProviderClosed reports an operation on a closed Provider.
	ErrProviderClosed = errors.New("tailscale: provider is closed")
	// ErrNotRunning reports a Provider that is not in the Running phase and
	// could not be started.
	ErrNotRunning = errors.New("tailscale: provider is not running")
	// ErrUnsupportedNetwork reports a dial network outside tcp/tcp4/tcp6.
	// The Network Provider port deliberately has no UDP or Listen support.
	ErrUnsupportedNetwork = errors.New("tailscale: network is not supported")
	// ErrInvalidConfig reports a profile the adapter cannot start. It never
	// carries key material.
	ErrInvalidConfig = errors.New("tailscale: invalid configuration")
)

// Config is one Network Profile's non-secret configuration plus the Auth Key
// read from the Vault field-secret store. AuthKey is memory-only: it is passed
// to tsnet.Server and never rendered, persisted, or logged.
type Config struct {
	ProfileID    string
	Label        string
	Kind         domainvault.NetworkProfileKind
	StateBaseDir string

	Hostname      string
	ControlURL    string
	Ephemeral     bool
	AdvertiseTags []string

	AuthKey string

	// stateDir and nodeHostname are derived by NewProvider. They are unexported
	// so a caller cannot point tsnet at an arbitrary directory or bypass the
	// hostname sanitizer.
	stateDir     string
	nodeHostname string
	logf         func(format string, args ...any)
}

// Status is the non-secret runtime status of one Tailscale Net Provider. It is
// what the Vault surface and the Connection Log receive. AuthURL is a
// short-lived login URL, not a key, and is present only while the node needs
// an interactive login.
type Status struct {
	ProfileID    string
	Label        string
	Kind         string
	Phase        appconnection.ProviderPhase
	TailscaleIPs []string
	DNSName      string
	AuthURL      string
	// Err is the redacted last failure, or nil while healthy.
	Err error
}

// Option customizes a Provider. Observers run on the goroutine that changes the
// status; they must not block and must not call back into the Provider.
type Option func(*Provider)

// WithStatusObserver installs the status callback. It receives every phase
// transition, including the Auth URL when login is required. A nil callback is
// valid and discards status.
func WithStatusObserver(fn func(Status)) Option {
	return func(p *Provider) { p.observe = fn }
}

// WithLogf installs the redacted diagnostic logger tsnet and this adapter use.
// A nil logger discards diagnostics.
func WithLogf(fn func(format string, args ...any)) Option {
	return func(p *Provider) { p.logf = fn }
}

// withBackendFactory replaces the tsnet backend. Tests use it to drive the
// state machine without a network.
func withBackendFactory(fn func(Config) (backend, error)) Option {
	return func(p *Provider) {
		if fn != nil {
			p.newBackend = fn
		}
	}
}

// withLoginWatchInterval shortens the interactive-login watcher cadence in
// tests. Production uses statusPollInterval.
func withLoginWatchInterval(interval time.Duration) Option {
	return func(p *Provider) {
		if interval > 0 {
			p.watchInterval = interval
		}
	}
}

// Provider is one Network Profile's in-process Tailscale Net dial port. It
// implements application/connection's NetworkProvider and
// NetworkProviderCloser. Create it with NewProvider, start it with Start, and
// release it with Close. It is safe for concurrent use.
type Provider struct {
	cfg        Config
	newBackend func(Config) (backend, error)
	observe    func(Status)
	logf       func(format string, args ...any)

	mu            sync.Mutex
	backend       backend
	phase         appconnection.ProviderPhase
	status        Status
	closed        bool
	starting      bool
	startDone     chan struct{}
	startCancel   context.CancelFunc
	startErr      error
	startEpoch    uint64
	watchCancel   context.CancelFunc
	watchDone     chan struct{}
	watchInterval time.Duration

	// emitMu serializes status delivery so a snapshot taken before a newer
	// transition cannot reach an observer after it. statusSeq advances on every
	// status mutation; emittedSeq records the newest delivered sequence.
	emitMu     sync.Mutex
	statusSeq  uint64
	emittedSeq uint64
}

// NewProvider validates and derives one profile's configuration. It creates no
// directory and no tsnet resource: an unused Network Profile must not leave a
// node behind. Start creates the state directory and the server.
func NewProvider(cfg Config, opts ...Option) (*Provider, error) {
	cfg.ProfileID = strings.TrimSpace(cfg.ProfileID)
	if cfg.ProfileID == "" {
		return nil, fmt.Errorf("%w: empty profile id", ErrInvalidConfig)
	}
	if cfg.Kind != domainvault.NetworkKindTailscale {
		return nil, fmt.Errorf("%w: kind %q", ErrInvalidConfig, cfg.Kind)
	}
	if strings.TrimSpace(cfg.StateBaseDir) == "" {
		return nil, fmt.Errorf("%w: empty state base directory", ErrInvalidConfig)
	}
	cfg.Label = strings.TrimSpace(cfg.Label)
	cfg.ControlURL = strings.TrimSpace(cfg.ControlURL)
	cfg.AuthKey = strings.TrimSpace(cfg.AuthKey)
	cfg.AdvertiseTags = normalizeTags(cfg.AdvertiseTags)
	cfg.nodeHostname = nodeHostname(cfg.Hostname, cfg.Label, cfg.ProfileID)
	if cfg.nodeHostname == "" {
		return nil, fmt.Errorf("%w: cannot derive a node hostname", ErrInvalidConfig)
	}
	stateDir, err := stateDirFor(cfg.StateBaseDir, cfg.ProfileID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	cfg.stateDir = stateDir

	provider := &Provider{
		cfg:           cfg,
		newBackend:    newTSNetBackend,
		watchInterval: statusPollInterval,
		phase:         appconnection.ProviderPhaseStopped,
		status: Status{
			ProfileID: cfg.ProfileID,
			Label:     cfg.Label,
			Kind:      string(domainvault.NetworkKindTailscale),
			Phase:     appconnection.ProviderPhaseStopped,
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(provider)
		}
	}
	return provider, nil
}

// Start joins the tailnet and waits until the node is Running or needs an
// interactive login. NeedsLogin is not a failure: the node stays alive with its
// Auth URL published through the status observer, and a background watcher
// reports Running once the user finishes the browser login. A previous failed
// or cancelled attempt is retried with a fresh server. Every failed start closes
// its server before Start returns, so an error never leaves a live node behind.
func (p *Provider) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return p.errorFor(appconnection.ProviderPhaseStopped, ErrProviderClosed)
	}
	if p.backend != nil &&
		(p.phase == appconnection.ProviderPhaseRunning ||
			p.phase == appconnection.ProviderPhaseNeedsLogin) {
		// Running is already up, and a NeedsLogin node is already waiting for
		// the user; a second Start must not replace either server.
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

	// A backend left by an earlier lost tunnel, cancelled login, or failed
	// attempt is retired before the replacement starts, so one profile never
	// runs two servers.
	old := p.backend
	p.backend = nil
	watchDone := p.stopLoginWatchLocked()
	p.starting = true
	p.startDone = make(chan struct{})
	startCtx, cancel := context.WithCancel(ctx)
	p.startCancel = cancel
	p.startEpoch++
	epoch := p.startEpoch
	p.startErr = nil
	p.resetStatusLocked(appconnection.ProviderPhaseStarting)
	p.mu.Unlock()
	p.emit()
	if watchDone != nil {
		<-watchDone
	}
	if old != nil {
		_ = old.Close()
	}

	err := p.runStart(startCtx, epoch)

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

// runStart creates and starts the backend. It owns the backend until it
// returns: on every failure path the backend is closed and cleared so the
// Provider never owns a live server for a failed attempt. epoch identifies this
// attempt so a concurrent Cancel can prevent a late success from resurrecting
// it.
func (p *Provider) runStart(ctx context.Context, epoch uint64) error {
	cfg := p.cfg
	cfg.logf = p.logf

	dir, err := prepareStateDir(cfg.StateBaseDir, cfg.ProfileID)
	if err != nil {
		return p.failStart(backendStatus{}, appconnection.ProviderPhaseError, err)
	}
	cfg.stateDir = dir

	b, err := p.newBackend(cfg)
	if err != nil {
		return p.failStart(backendStatus{}, appconnection.ProviderPhaseError, err)
	}

	p.mu.Lock()
	if p.closed || p.startEpoch != epoch {
		p.mu.Unlock()
		// The attempt was cancelled before this backend was published; it never
		// became the Provider's server, so runStart still owns closing it.
		_ = b.Close()
		return p.errorFor(appconnection.ProviderPhaseStopped, context.Canceled)
	}
	p.backend = b
	p.mu.Unlock()

	status, err := b.Start(ctx)
	if err != nil {
		phase := appconnection.ProviderPhaseError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			phase = appconnection.ProviderPhaseStopped
		}
		return p.failStartWithBackend(b, status, phase, err)
	}
	switch status.Phase {
	case appconnection.ProviderPhaseRunning:
		if len(status.TailscaleIPs) == 0 {
			return p.failStartWithBackend(b, status, appconnection.ProviderPhaseError,
				errors.New("tailscale: running without a tailnet address"))
		}
		p.mu.Lock()
		if p.startRetiredLocked(b, epoch) {
			p.mu.Unlock()
			return p.startRetired()
		}
		p.applyStatusLocked(status, appconnection.ProviderPhaseRunning, nil)
		p.mu.Unlock()
		p.emit()
		return nil
	case appconnection.ProviderPhaseNeedsLogin:
		p.mu.Lock()
		if p.startRetiredLocked(b, epoch) {
			p.mu.Unlock()
			return p.startRetired()
		}
		p.applyStatusLocked(status, appconnection.ProviderPhaseNeedsLogin, nil)
		p.startLoginWatchLocked(b)
		p.mu.Unlock()
		p.emit()
		return nil
	default:
		return p.failStartWithBackend(b, status, appconnection.ProviderPhaseError,
			fmt.Errorf("tailscale: backend reported phase %q", status.Phase))
	}
}

// startRetiredLocked reports whether a Cancel or Close retired this start
// attempt while its backend was joining. It must run under the Provider lock.
// When this attempt still holds the backend, it clears the ownership so the
// caller closes the server exactly once; the cancel path then has nothing left
// to close.
func (p *Provider) startRetiredLocked(b backend, epoch uint64) bool {
	if !p.closed && p.startEpoch == epoch && p.backend == b {
		return false
	}
	if p.backend == b {
		// This attempt still owned the server; close it here. A backend another
		// operation removed is that operation's to close exactly once.
		p.backend = nil
		_ = b.Close()
	}
	return true
}

// startRetired is the classified result of a start attempt a Cancel or Close
// retired while the backend was joining. No live node is left behind: either
// the cancel path or the caller closed it.
func (p *Provider) startRetired() error {
	return p.errorFor(appconnection.ProviderPhaseStopped, context.Canceled)
}

// failStart records a startup failure with no backend to close.
func (p *Provider) failStart(status backendStatus, phase appconnection.ProviderPhase, cause error) error {
	return p.failStartWithBackend(nil, status, phase, cause)
}

// failStartWithBackend records a startup failure and closes the backend once.
// A backend a concurrent Cancel already retired is not closed here: the
// operation that removed it owns the one close.
func (p *Provider) failStartWithBackend(b backend, status backendStatus,
	phase appconnection.ProviderPhase, cause error) error {
	cause = redactError(cause, p.cfg.AuthKey)
	cause = redactError(cause, status.AuthURL)
	if status.Phase == appconnection.ProviderPhaseNeedsLogin {
		phase = appconnection.ProviderPhaseNeedsLogin
	}
	// A caller-cancelled start is a normal stop, not a failure: the caller still
	// receives the cancellation through the returned error, but the Vault and the
	// Connection Log must not render the user's Cancel as a red error.
	statusCause := cause
	if errors.Is(cause, context.Canceled) {
		statusCause = nil
	}
	p.mu.Lock()
	owned := b == nil || p.backend == b
	// A Cancel or Close that retired this attempt owns the status transition:
	// publishing the cancelled start's cause would turn the user's Cancel into a
	// spurious error row in the Vault and the Connection Log.
	retired := !owned || p.closed
	if owned {
		p.backend = nil
	}
	if !retired {
		p.applyStatusLocked(status, phase, statusCause)
	}
	p.mu.Unlock()
	if !retired {
		p.emit()
	}
	if owned && b != nil {
		_ = b.Close()
	}
	return p.errorFor(phase, cause)
}

// applyStatusLocked copies a backend status onto the Provider. The caller must
// hold p.mu.
func (p *Provider) applyStatusLocked(status backendStatus, phase appconnection.ProviderPhase, cause error) {
	p.phase = phase
	p.status.Phase = phase
	p.status.TailscaleIPs = append([]string(nil), status.TailscaleIPs...)
	p.status.DNSName = status.DNSName
	p.status.AuthURL = status.AuthURL
	p.status.Err = cause
	p.statusSeq++
}

// resetStatusLocked moves the Provider to phase and clears every field that
// belonged to the previous backend: addresses, login URL, and cause. The caller
// must hold p.mu.
func (p *Provider) resetStatusLocked(phase appconnection.ProviderPhase) {
	p.phase = phase
	p.status.Phase = phase
	p.status.TailscaleIPs = nil
	p.status.DNSName = ""
	p.status.AuthURL = ""
	p.status.Err = nil
	p.statusSeq++
}

// Cancel stops a start or a pending interactive login and leaves the Provider
// reusable. Unlike Close it keeps the object open, so the next Start can join
// again. A cancelled start is reported as Stopped rather than as a failure.
func (p *Provider) Cancel() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return p.errorFor(appconnection.ProviderPhaseStopped, ErrProviderClosed)
	}
	starting := p.starting
	startDone := p.startDone
	startCancel := p.startCancel
	watchDone := p.stopLoginWatchLocked()
	b := p.backend
	p.backend = nil
	// Advance the epoch so a backend that ignores the cancelled start context
	// cannot publish a late Running or NeedsLogin result.
	p.startEpoch++
	p.resetStatusLocked(appconnection.ProviderPhaseStopped)
	p.mu.Unlock()

	if starting {
		if startCancel != nil {
			startCancel()
		}
		if startDone != nil {
			<-startDone
		}
	}
	if watchDone != nil {
		<-watchDone
	}
	var err error
	if b != nil {
		err = b.Close()
	}
	p.emit()
	return err
}

// DialContext dials one tailnet TCP address through the embedded node. It
// accepts tcp, tcp4, and tcp6 only; UDP stays out of the port until the mosh
// rewrite lands. A provider-level failure is KindProvider with profile
// context; an unreachable target remains KindNetwork and a deadline remains
// KindTimeout.
func (p *Provider) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if !tcpNetwork(network) {
		phase := p.Phase()
		return nil, &appconnection.Error{
			Kind:     appconnection.KindUnsupported,
			Cause:    fmt.Errorf("%w: %q", ErrUnsupportedNetwork, network),
			Provider: p.providerInfo(phase),
		}
	}

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
	if phase == appconnection.ProviderPhaseNeedsLogin {
		return nil, p.errorFor(phase, ErrNeedsLogin)
	}
	if b == nil || phase != appconnection.ProviderPhaseRunning {
		return nil, p.errorFor(phase, ErrNotRunning)
	}

	conn, err := b.Dial(ctx, network, address)
	if err == nil {
		return conn, nil
	}
	cause := redactError(err, p.cfg.AuthKey)

	// A failed dial can mean the tunnel was lost after startup. Ask the
	// backend for its live phase so login loss and a stopped backend fail
	// closed with KindProvider instead of masquerading as an ordinary network
	// error.
	if live, statusErr := b.Status(ctx); statusErr == nil && live.Phase != appconnection.ProviderPhaseRunning {
		statusCause := cause
		if live.Phase == appconnection.ProviderPhaseNeedsLogin {
			// The failed dial is not the login's error. Publish a clean
			// NeedsLogin state and let the watcher observe the completed login.
			statusCause = nil
		}
		p.mu.Lock()
		if p.backend == b {
			p.applyStatusLocked(live, live.Phase, statusCause)
			if live.Phase == appconnection.ProviderPhaseNeedsLogin {
				p.startLoginWatchLocked(b)
			}
		}
		p.mu.Unlock()
		p.emit()
		if live.Phase == appconnection.ProviderPhaseNeedsLogin {
			return nil, p.errorFor(live.Phase, ErrNeedsLogin)
		}
		return nil, p.errorFor(live.Phase, cause)
	}
	switch {
	case errors.Is(cause, context.Canceled):
		return nil, &appconnection.Error{Kind: appconnection.KindCancelled, Cause: cause,
			Provider: p.providerInfo(phase)}
	case errors.Is(cause, context.DeadlineExceeded):
		return nil, &appconnection.Error{Kind: appconnection.KindTimeout, Cause: cause,
			Provider: p.providerInfo(phase)}
	default:
		return nil, &appconnection.Error{Kind: appconnection.KindNetwork, Cause: cause,
			Provider: p.providerInfo(phase)}
	}
}

// Close releases the node. It is idempotent and waits for an in-flight start and
// login watcher so tsnet's Close is never concurrent with Start. A Provider
// closes its server exactly once, whether the server ended through Cancel, a
// failed start, or Close.
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
	watchDone := p.stopLoginWatchLocked()
	p.mu.Unlock()

	if starting {
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
	}
	if watchDone != nil {
		<-watchDone
	}

	p.mu.Lock()
	b := p.backend
	p.backend = nil
	p.resetStatusLocked(appconnection.ProviderPhaseStopped)
	p.mu.Unlock()
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
	status := p.status
	status.TailscaleIPs = append([]string(nil), p.status.TailscaleIPs...)
	return status
}

// Phase returns the current lifecycle phase.
func (p *Provider) Phase() appconnection.ProviderPhase {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.phase
}

// emit publishes the current status outside the Provider lock. The observer
// must be non-blocking and must not call back into this Provider. Delivery is
// serialized, and a transition whose snapshot a newer one already superseded is
// dropped, so concurrent transitions cannot reach an observer out of order.
func (p *Provider) emit() {
	p.emitMu.Lock()
	defer p.emitMu.Unlock()

	p.mu.Lock()
	if p.observe == nil {
		p.mu.Unlock()
		return
	}
	seq := p.statusSeq
	if seq <= p.emittedSeq {
		p.mu.Unlock()
		return
	}
	status := p.status
	status.TailscaleIPs = append([]string(nil), p.status.TailscaleIPs...)
	observe := p.observe
	p.emittedSeq = seq
	p.mu.Unlock()
	observe(status)
}

// errorFor attaches the profile identity to a provider-level failure. A
// cancelled or expired caller keeps its transport Kind; everything else is
// KindProvider.
func (p *Provider) errorFor(phase appconnection.ProviderPhase, cause error) error {
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
		Kind:      string(domainvault.NetworkKindTailscale),
		Phase:     phase,
	}
}

func tcpNetwork(network string) bool {
	switch network {
	case "tcp", "tcp4", "tcp6":
		return true
	default:
		return false
	}
}

// nodeHostname sanitizes an explicit profile hostname or derives one from the
// profile label and ID.
func nodeHostname(explicit, label, profileID string) string {
	if hostname := sanitizeHostname(explicit); hostname != "" {
		return hostname
	}
	base := sanitizeHostname(label)
	if base == "" {
		base = sanitizeHostname(profileID)
	}
	if base == "" {
		base = shortHash(profileID)
	}
	return sanitizeHostname(defaultHostnamePrefix + "-" + base)
}

// sanitizeHostname reduces a value to one DNS-label-shaped node name. It
// lowercases, maps separators to hyphens, drops characters Tailscale rejects,
// and truncates to the label limit.
func sanitizeHostname(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastHyphen := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastHyphen = false
		case r == '-' || r == '_' || r == '.' || r == ' ':
			if builder.Len() > 0 && !lastHyphen {
				builder.WriteByte('-')
				lastHyphen = true
			}
		default:
			// Drop characters a Tailscale node name cannot carry.
		}
	}
	out := strings.Trim(builder.String(), "-")
	if len(out) > maxHostnameLength {
		out = strings.TrimRight(out[:maxHostnameLength], "-")
	}
	return out
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

// stateDirFor computes a profile's isolated state directory under base. The
// directory name is safe for every supported filesystem and includes a stable
// digest of the profile ID, so two IDs can never map to the same directory.
func stateDirFor(base, profileID string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", errors.New("empty state base directory")
	}
	absoluteBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve state base: %w", err)
	}
	dir := filepath.Join(absoluteBase, "tailscale", stateDirName(profileID))
	if !within(absoluteBase, dir) {
		return "", errors.New("state directory escapes its base")
	}
	if isSystemTailscaleDir(dir) {
		return "", errors.New("state directory must not be a system Tailscale directory")
	}
	return dir, nil
}

// prepareStateDir creates the profile's private state directory and returns its
// canonical path. It rejects symlinked or otherwise redirected path components,
// a directory that resolves outside the base, and a system Tailscale directory
// before creating or chmodding anything, then tightens the directory to 0700.
func prepareStateDir(base, profileID string) (string, error) {
	dir, err := stateDirFor(base, profileID)
	if err != nil {
		return "", err
	}
	absoluteBase, err := filepath.Abs(strings.TrimSpace(base))
	if err != nil {
		return "", fmt.Errorf("resolve state base: %w", err)
	}
	// Vet the deepest existing ancestor before creating anything. A symlink or
	// junction above the base can redirect MkdirAll into a system Tailscale root
	// even when the leaf itself is missing, so this has to precede the first
	// filesystem write.
	if err := requireStateBaseOutsideSystemDirs(absoluteBase); err != nil {
		return "", err
	}
	if err := os.MkdirAll(absoluteBase, 0o700); err != nil {
		return "", fmt.Errorf("create state base: %w", err)
	}
	resolvedBase, err := filepath.EvalSymlinks(absoluteBase)
	if err != nil {
		return "", fmt.Errorf("resolve state base: %w", err)
	}
	resolvedBase, err = resolveStateLinkTarget(resolvedBase)
	if err != nil {
		return "", fmt.Errorf("resolve state base: %w", err)
	}
	relativeDir, err := filepath.Rel(absoluteBase, dir)
	if err != nil {
		return "", fmt.Errorf("resolve state directory: %w", err)
	}
	// Create and check the canonical path the directory will actually occupy,
	// so a redirected base cannot leak writes elsewhere.
	dir = filepath.Join(resolvedBase, relativeDir)
	if isSystemTailscaleDir(dir) {
		return "", errors.New("state directory must not be a system Tailscale directory")
	}
	if err := requireStatePathDirectories(resolvedBase, relativeDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil && runtime.GOOS != "windows" {
		return "", fmt.Errorf("protect state directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve state directory: %w", err)
	}
	if !within(resolvedBase, resolved) {
		return "", errors.New("state directory escapes its base")
	}
	if isSystemTailscaleDir(resolved) {
		return "", errors.New("state directory must not be a system Tailscale directory")
	}
	return resolved, nil
}

// RemoveStateDir deletes one Network Profile's state directory under base. It
// is the delete-time counterpart to prepareStateDir: it removes only the exact
// directory derived from profileID, returns nil when nothing exists, and
// refuses a base or path component that resolves into a system Tailscale
// directory, outside base, or through a link. A link at the profile directory
// itself is removed as a link by RemoveAll, but is refused here so a redirected
// node state is never treated as this profile's data.
func RemoveStateDir(base, profileID string) error {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return errors.New("empty profile id")
	}
	dir, err := stateDirFor(base, profileID)
	if err != nil {
		return err
	}
	absoluteBase, err := filepath.Abs(strings.TrimSpace(base))
	if err != nil {
		return fmt.Errorf("resolve state base: %w", err)
	}
	// Vet the base and every existing ancestor before removal, so a link above
	// the base cannot redirect the delete into a protected root.
	if err := requireStateBaseOutsideSystemDirs(absoluteBase); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A base that has never been created cannot own profile state.
			return nil
		}
		return err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect state directory: %w", err)
	}
	if _, linkErr := os.Readlink(dir); linkErr == nil {
		return errors.New("state directory is a link")
	}
	if !info.IsDir() {
		return errors.New("state directory is not a directory")
	}

	resolvedBase, err := canonicalStatePath(absoluteBase)
	if err != nil {
		return err
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve state directory: %w", err)
	}
	resolvedDir, err = resolveStateLinkTarget(resolvedDir)
	if err != nil {
		return fmt.Errorf("resolve state directory: %w", err)
	}
	if !within(resolvedBase, resolvedDir) || filepath.Clean(resolvedDir) == filepath.Clean(resolvedBase) {
		return errors.New("state directory escapes its base")
	}
	if isSystemTailscaleDir(resolvedDir) {
		return errors.New("state directory must not be a system Tailscale directory")
	}
	// After canonicalization the target must still be exactly this profile's
	// derived directory. A redirected intermediate component is refused rather
	// than deleted.
	expected := filepath.Join("tailscale", stateDirName(profileID))
	relative, err := filepath.Rel(resolvedBase, resolvedDir)
	if err != nil || relative != expected {
		return errors.New("state directory escapes its base")
	}
	if err := os.RemoveAll(resolvedDir); err != nil {
		return fmt.Errorf("remove state directory: %w", err)
	}
	return nil
}

// requireStateBaseOutsideSystemDirs rejects a base whose deepest existing
// ancestor is not a real directory or resolves into a system Tailscale root. It
// must run before any MkdirAll or Chmod: a link above the base can redirect
// creation into a protected root even when the base's leaf components are
// missing. Missing leaf components are fine and are created later.
func requireStateBaseOutsideSystemDirs(absoluteBase string) error {
	existing, err := deepestExistingStateAncestor(absoluteBase)
	if err != nil {
		return err
	}
	canonical, err := canonicalStatePath(existing)
	if err != nil {
		return err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return fmt.Errorf("resolve state base: %w", err)
	}
	if !info.IsDir() {
		return errors.New("state base is not a directory")
	}
	if isSystemTailscaleDir(canonical) {
		return errors.New("state directory must not be a system Tailscale directory")
	}
	return nil
}

// deepestExistingStateAncestor walks up from path to the first component that
// exists. A component that exists but is neither a directory nor a link is
// rejected outright, so a base beneath a regular file can never be treated as
// merely missing and later created. Link components are returned for
// canonicalization by the caller. Windows junctions surface as ModeIrregular
// rather than ModeSymlink under Go's winsymlink semantics, so a successful
// Readlink, not a mode bit, identifies a link.
func deepestExistingStateAncestor(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		info, err := os.Lstat(current)
		switch {
		case err == nil:
			if info.IsDir() {
				return current, nil
			}
			if _, linkErr := os.Readlink(current); linkErr == nil {
				return current, nil
			}
			return "", errors.New("state base is not a directory")
		case errors.Is(err, os.ErrNotExist):
			parent := filepath.Dir(current)
			if parent == current {
				return "", fmt.Errorf("resolve state base: %w", err)
			}
			current = parent
		default:
			return "", fmt.Errorf("resolve state base: %w", err)
		}
	}
}

// canonicalStatePath resolves symlinks and Windows junctions in every
// component of an existing absolute path. filepath.EvalSymlinks resolves POSIX
// and Windows symlinks but leaves Windows directory junctions in place under
// Go's winsymlink semantics, so each component is also passed through
// resolveStateLinkTarget.
func canonicalStatePath(path string) (string, error) {
	volume := filepath.VolumeName(path)
	remainder := strings.TrimPrefix(path, volume)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(remainder, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		next, err := filepath.EvalSymlinks(filepath.Join(current, part))
		if err != nil {
			return "", fmt.Errorf("resolve state base: %w", err)
		}
		next, err = resolveStateLinkTarget(next)
		if err != nil {
			return "", fmt.Errorf("resolve state base: %w", err)
		}
		current = next
	}
	return filepath.Clean(current), nil
}

// resolveStateLinkTarget follows a link whose target is a directory.
// filepath.EvalSymlinks already resolves POSIX symlinks and Windows symlinks,
// but it deliberately leaves Windows directory junctions in place; without this
// step a junction base could hide a system Tailscale directory.
func resolveStateLinkTarget(path string) (string, error) {
	for i := 0; i < 32; i++ {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return path, nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path, err = filepath.EvalSymlinks(target)
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("state base resolves through too many links")
}

// requireStatePathDirectories rejects every existing component between the
// resolved base and the profile directory that is not a real directory. That
// covers POSIX symlinks and Windows symlinks and junctions, which Lstat reports
// as non-directories. It runs before MkdirAll so a rejected path cannot create
// or chmod a directory outside the base.
func requireStatePathDirectories(resolvedBase, relativeDir string) error {
	current := resolvedBase
	for _, part := range strings.Split(relativeDir, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect state directory: %w", err)
		}
		if !info.IsDir() {
			return errors.New("state directory escapes its base")
		}
	}
	return nil
}

// stateDirName builds a filesystem-safe, collision-free directory name. The
// digest keeps two profile IDs distinct even when their sanitized names match.
func stateDirName(profileID string) string {
	var builder strings.Builder
	for _, r := range profileID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	safe := strings.Trim(builder.String(), ".-_")
	if safe == "" {
		safe = "profile"
	}
	return safe + "-" + shortHash(profileID)
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func within(base, target string) bool {
	relative, err := filepath.Rel(base, target)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." || (relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// isSystemTailscaleDir reports whether dir is a state location owned by a
// system Tailscale client. The embedded node must never reuse one: sharing it
// would be an unsupported use that can corrupt the user's system identity.
func isSystemTailscaleDir(dir string) bool {
	dir = filepath.Clean(dir)
	for _, root := range systemTailscaleDirs() {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if dir == root || within(root, dir) {
			return true
		}
	}
	return false
}

func systemTailscaleDirs() []string {
	var dirs []string
	switch runtime.GOOS {
	case "windows":
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			dirs = append(dirs, filepath.Join(programData, "Tailscale"))
		}
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			dirs = append(dirs, filepath.Join(localAppData, "Tailscale"))
		}
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			dirs = append(dirs, filepath.Join(appData, "Tailscale"))
		}
	case "darwin":
		dirs = append(dirs, "/Library/Tailscale")
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs,
				filepath.Join(home, "Library", "Application Support", "Tailscale"),
				filepath.Join(home, "Library", "Containers", "io.tailscale.ipn.macsys"),
			)
		}
	default:
		dirs = append(dirs, "/var/lib/tailscale")
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home, ".config", "tailscale"))
		}
	}
	return dirs
}

// redactError removes every occurrence of the Auth Key from an error's text
// while preserving its chain so errors.Is and errors.As still classify it.
func redactError(err error, secret string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	redacted := redactText(text, secret)
	if redacted == text {
		return err
	}
	return &redactedError{cause: err, message: redacted}
}

type redactedError struct {
	cause   error
	message string
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.cause }

// redactText removes a secret from one diagnostic line. An empty secret is
// never treated as a match.
func redactText(text, secret string) string {
	if secret == "" || text == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[redacted]")
}

// redactingLogf adapts a diagnostic logger so no line can carry the Auth Key.
func redactingLogf(logf func(format string, args ...any), secret string) func(format string, args ...any) {
	if logf == nil {
		return nil
	}
	return func(format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		logf("%s", redactText(message, secret))
	}
}
