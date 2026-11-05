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

// Provider is one Network Profile's in-process Tailscale Net dial port. It
// implements application/connection's NetworkProvider and
// NetworkProviderCloser. Create it with NewProvider, start it with Start, and
// release it with Close. It is safe for concurrent use.
type Provider struct {
	cfg        Config
	newBackend func(Config) (backend, error)
	observe    func(Status)
	logf       func(format string, args ...any)

	mu          sync.Mutex
	backend     backend
	phase       appconnection.ProviderPhase
	status      Status
	closed      bool
	starting    bool
	startDone   chan struct{}
	startCancel context.CancelFunc
	startErr    error
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
		cfg:        cfg,
		newBackend: newTSNetBackend,
		phase:      appconnection.ProviderPhaseStopped,
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

// Start joins the tailnet and waits until the node is Running or is known to
// need an interactive login. A previous failed attempt is retried with a fresh
// server. On failure the server is closed before Start returns, so a cancelled
// or failed start never leaves a live node behind.
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

	// A backend left by an earlier lost tunnel or failed attempt is retired
	// before the replacement starts, so one profile never runs two servers.
	old := p.backend
	p.backend = nil
	p.starting = true
	p.startDone = make(chan struct{})
	startCtx, cancel := context.WithCancel(ctx)
	p.startCancel = cancel
	p.startErr = nil
	p.phase = appconnection.ProviderPhaseStarting
	p.status.Phase = appconnection.ProviderPhaseStarting
	p.status.TailscaleIPs = nil
	p.status.DNSName = ""
	p.status.AuthURL = ""
	p.status.Err = nil
	p.mu.Unlock()
	p.emit()
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

// runStart creates and starts the backend. It owns the backend until it
// returns: on every failure path the backend is closed and cleared so the
// Provider never owns a live server for a failed attempt.
func (p *Provider) runStart(ctx context.Context) error {
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
	if p.closed {
		p.mu.Unlock()
		_ = b.Close()
		return p.errorFor(appconnection.ProviderPhaseStopped, ErrProviderClosed)
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
	if status.Phase != appconnection.ProviderPhaseRunning &&
		status.Phase != appconnection.ProviderPhaseNeedsLogin {
		return p.failStartWithBackend(b, status, appconnection.ProviderPhaseError,
			fmt.Errorf("tailscale: backend reported phase %q", status.Phase))
	}
	if status.Phase == appconnection.ProviderPhaseRunning && len(status.TailscaleIPs) == 0 {
		return p.failStartWithBackend(b, status, appconnection.ProviderPhaseError,
			errors.New("tailscale: running without a tailnet address"))
	}
	if status.Phase == appconnection.ProviderPhaseNeedsLogin {
		return p.failStartWithBackend(b, status, appconnection.ProviderPhaseNeedsLogin,
			fmt.Errorf("%w", ErrNeedsLogin))
	}

	p.mu.Lock()
	p.applyStatusLocked(status, appconnection.ProviderPhaseRunning, nil)
	p.mu.Unlock()
	p.emit()
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
	cause = redactError(cause, p.cfg.AuthKey)
	if status.Phase == appconnection.ProviderPhaseNeedsLogin {
		phase = appconnection.ProviderPhaseNeedsLogin
	}
	p.mu.Lock()
	p.backend = nil
	p.applyStatusLocked(status, phase, cause)
	p.mu.Unlock()
	p.emit()
	if b != nil {
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
		p.mu.Lock()
		p.applyStatusLocked(live, live.Phase, cause)
		p.mu.Unlock()
		p.emit()
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

// Close releases the node. It is idempotent and waits for an in-flight start so
// tsnet's Close is never concurrent with Start. A Provider closes its server
// exactly once.
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
	p.mu.Unlock()

	if starting {
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
	}

	p.mu.Lock()
	b := p.backend
	p.backend = nil
	p.phase = appconnection.ProviderPhaseStopped
	p.status.Phase = appconnection.ProviderPhaseStopped
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

// emit publishes a status snapshot outside the Provider lock. The observer
// must be non-blocking and must not call back into this Provider.
func (p *Provider) emit() {
	p.mu.Lock()
	if p.observe == nil {
		p.mu.Unlock()
		return
	}
	status := p.status
	status.TailscaleIPs = append([]string(nil), p.status.TailscaleIPs...)
	observe := p.observe
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
