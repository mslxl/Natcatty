package connection

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// NetworkProvider is the only runtime dialing port through which a Network
// Profile is used. An implementation dials inside its virtual network; the
// production implementations are Tailscale Net and WireGuard Net, both
// in-process.
//
// Only "tcp", "tcp4", and "tcp6" are accepted. Listen is deliberately absent:
// the application never accepts inbound connections through a Network Profile.
// An implementation that knows a failure is provider-level (not logged in,
// tunnel unavailable, invalid configuration) returns a *Error with
// KindProvider and ProviderInfo; a target that is simply unreachable is an
// ordinary dial error and remains KindNetwork.
//
// Datagram traffic is a separate optional capability, NetworkProviderDatagram,
// so a Provider that can only carry streams is not forced to expose a UDP
// surface it cannot honor.
type NetworkProvider interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// NetworkProviderDatagram is the optional datagram capability of a Provider.
// mosh speaks UDP beside the SSH bootstrap, so a mosh Session whose route
// selects a Network Profile opens its datagram flow here; a Provider that does
// not implement this interface fails the Session closed instead of letting it
// dial outside the profile.
//
// The address is handed to the Provider unresolved, so the caller never uses
// the host resolver or opens a host socket; each implementation keeps its own
// target and DNS rules. Only "udp", "udp4", and "udp6" are accepted, and the
// returned net.Conn is one connected datagram flow. A provider-level failure
// keeps the same classification as DialContext.
type NetworkProviderDatagram interface {
	DialDatagram(ctx context.Context, network, address string) (net.Conn, error)
}

// NetworkProviderCloser is the optional lifecycle capability of a provider that
// owns resources such as sockets, a netstack device, or a tsnet node. The
// registry closes providers through this interface; a provider that does not
// implement it is simply dropped.
type NetworkProviderCloser interface {
	Close() error
}

// NetworkProviderCanceller is the optional capability of a Provider that can
// stop its current start or interactive login while remaining reusable for a
// later Acquire or retry. Cancel is a user action, so the Provider reports the
// resulting Stopped phase; Close stays the application-shutdown path and is
// silent.
type NetworkProviderCanceller interface {
	Cancel() error
}

// NetworkProviderPhaseReporter is the optional lifecycle capability of a
// Provider that can report its current phase without dialing. It lets the
// registry retire a Provider that died after a successful open and lets the
// Manager fail closed before it hands out a pooled transport. A Provider that
// does not implement it is treated as running; an empty phase is unknown and is
// likewise treated as usable. Phase must be safe to call concurrently and must
// not block on the network.
type NetworkProviderPhaseReporter interface {
	Phase() ProviderPhase
}

// NetworkProviderFactory opens the provider for one Network Profile. It is the
// seam Tailscale Net and WireGuard Net implement. Open may take time (joining a
// tailnet, starting a netstack) and must honor ctx. Until a factory is wired,
// selecting a profile fails closed instead of dialing directly.
type NetworkProviderFactory interface {
	Open(ctx context.Context, route NetworkRoute) (NetworkProvider, error)
}

// NetworkProviderRegistry owns one lazily opened Provider per Network Profile.
// It is safe for concurrent use. Acquire reuses a running Provider while its
// route fingerprint is unchanged and replaces it when the profile is edited or
// its credentials rotate. A failed open is retryable on the next Acquire.
//
// The registry never holds its mutex while a factory opens a provider, so a
// slow join does not block other profiles. Concurrent Acquires for the same
// profile share one open attempt and its result.
type NetworkProviderRegistry struct {
	factory NetworkProviderFactory

	mu      sync.Mutex
	entries map[string]*providerEntry
	closed  bool
	onClose func(profileID string)
	// afterOwnership, when non-nil, runs with mu held after an opener has made
	// its ownership decision and published the entry's readiness. Tests use it
	// to order Close, Cancel, or CloseAll against that handshake; production
	// leaves it nil.
	afterOwnership func(entry *providerEntry)
}

// providerEntry is one profile's open attempt or running Provider. ready closes
// once the attempt settles, so waiters share its result; provider and err are
// written before ready closes. The opener publishes ready while holding the
// registry lock, so Close, Cancel, and CloseAll observe either an unsettled
// attempt they can abandon or a settled one whose Provider they must release.
// cancel stops an open attempt that Close, Cancel, or CloseAll abandoned.
type providerEntry struct {
	fingerprint string
	ready       chan struct{}
	cancel      context.CancelFunc
	provider    NetworkProvider
	err         error
	// abandoned is set by Close while an open is in flight. The leader still
	// closes whatever the factory produced, and waiters observe the closed
	// result instead of a provider the registry no longer owns.
	abandoned bool
	// retired is set once when a cached Provider that reports a stopped or
	// failed phase is removed. It guarantees concurrent Acquires close that
	// Provider exactly once.
	retired bool
}

// NewNetworkProviderRegistry returns an empty registry over factory. A nil
// factory is valid: every Acquire then fails closed with KindProvider.
func NewNetworkProviderRegistry(factory NetworkProviderFactory) *NetworkProviderRegistry {
	return &NetworkProviderRegistry{
		factory: factory,
		entries: make(map[string]*providerEntry),
	}
}

// SetCloseHook installs a callback invoked with the profile ID whenever this
// registry retires a live Provider, whether through Close, CloseAll, a
// fingerprint replacement, or Cancel. The Manager uses it to invalidate idle
// pooled Connections. The hook runs without any registry lock and must not
// block.
func (r *NetworkProviderRegistry) SetCloseHook(fn func(profileID string)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.onClose = fn
	r.mu.Unlock()
}

// Acquire returns the running Provider for route's profile, opening it on first
// use. An empty profile ID, a Kind this build cannot run, a nil factory, or a
// closed registry fails closed with a typed provider error; no caller may
// substitute a direct dial.
//
// A cached Provider that reports a stopped or failed phase is retired and
// reopened once in the same Acquire: a Provider can die between calls without
// any caller noticing. A Provider still starting, waiting for an interactive
// login, or shutting down is returned unchanged so the Manager can fail closed
// with the accurate phase.
func (r *NetworkProviderRegistry) Acquire(ctx context.Context, route NetworkRoute) (NetworkProvider, error) {
	if r == nil {
		return nil, providerRouteError(route, ProviderPhaseStopped,
			fmt.Errorf("%w: no registry", ErrNetworkProvider))
	}
	if err := validateNetworkRoute(route); err != nil {
		return nil, err
	}
	if r.factory == nil {
		return nil, providerRouteError(route, ProviderPhaseStopped,
			fmt.Errorf("%w: no factory", ErrNetworkProvider))
	}

	retriedDeadProvider := false
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, providerRouteError(route, ProviderPhaseStopped, ErrNetworkProviderClosed)
		}
		entry := r.entries[route.ProfileID]
		if entry != nil && entry.fingerprint != route.Fingerprint {
			select {
			case <-entry.ready:
				// The old attempt settled; drop it and open the new version.
				delete(r.entries, route.ProfileID)
				old := entry.provider
				r.mu.Unlock()
				r.closeProvider(old, route.ProfileID)
				continue
			default:
				// Another version is still opening. Wait for it, then replace.
				ready := entry.ready
				r.mu.Unlock()
				if err := waitForProvider(ctx, ready, route); err != nil {
					return nil, err
				}
				continue
			}
		}
		if entry != nil {
			select {
			case <-entry.ready:
				if entry.err != nil {
					err := entry.err
					r.mu.Unlock()
					return nil, err
				}
				provider := entry.provider
				if !retriedDeadProvider && providerNeedsRestart(provider) {
					// The cached Provider died after its open succeeded. Drop it
					// and open once more; a second dead result is returned so the
					// caller fails closed with that Provider's real phase.
					retriedDeadProvider = true
					if entry.retired {
						r.mu.Unlock()
						continue
					}
					entry.retired = true
					delete(r.entries, route.ProfileID)
					r.mu.Unlock()
					r.closeProvider(provider, route.ProfileID)
					continue
				}
				r.mu.Unlock()
				return provider, nil
			default:
				ready := entry.ready
				r.mu.Unlock()
				if err := waitForProvider(ctx, ready, route); err != nil {
					return nil, err
				}
				// A failed open is retryable, but the caller that shared the
				// attempt must observe its result rather than silently starting
				// a second one.
				if entry.err != nil {
					return nil, entry.err
				}
				continue
			}
		}

		entry = &providerEntry{fingerprint: route.Fingerprint, ready: make(chan struct{})}
		openCtx, cancel := context.WithCancel(ctx)
		entry.cancel = cancel
		r.entries[route.ProfileID] = entry
		r.mu.Unlock()

		provider, err := r.factory.Open(openCtx, route)
		cancel()
		if err == nil && provider == nil {
			err = fmt.Errorf("%w: factory returned no provider", ErrNetworkProvider)
		}
		var wrapped error
		if err != nil {
			wrapped = providerOpenError(route, err)
		}

		r.mu.Lock()
		entry.provider = provider
		entry.err = wrapped
		owned := r.entries[route.ProfileID] == entry && !r.closed && !entry.abandoned
		if wrapped != nil || !owned {
			// A failed open is retryable on the next Acquire. An abandoned or
			// closed registry must not leave a live provider behind.
			if r.entries[route.ProfileID] == entry {
				delete(r.entries, route.ProfileID)
			}
		}
		// Publish readiness inside the critical section. Close, Cancel, and
		// CloseAll decide ownership under this same lock: either they run first
		// (removing the entry and marking it abandoned, so owned is false and this
		// opener closes the Provider), or they run after (seeing ready closed and
		// taking responsibility for closing it). Publishing after the unlock left a
		// window where both sides could believe the other owned the Provider.
		close(entry.ready)
		if r.afterOwnership != nil {
			r.afterOwnership(entry)
		}
		r.mu.Unlock()

		if wrapped != nil {
			// A factory that returned a provider together with an error still
			// transferred ownership. Close it so a failed attempt cannot leak a
			// socket or a netstack.
			r.closeProvider(provider, route.ProfileID)
			return nil, wrapped
		}
		if !owned {
			r.closeProvider(provider, route.ProfileID)
			return nil, providerRouteError(route, ProviderPhaseStopped, ErrNetworkProviderClosed)
		}
		if !retriedDeadProvider && providerNeedsRestart(provider) {
			// The provider died while the open attempt settled. Retire it and
			// open once more rather than handing a dead dial port to the caller.
			r.mu.Lock()
			claimed := !entry.retired && r.entries[route.ProfileID] == entry
			if claimed {
				entry.retired = true
				delete(r.entries, route.ProfileID)
			}
			r.mu.Unlock()
			retriedDeadProvider = true
			if claimed {
				r.closeProvider(provider, route.ProfileID)
			}
			continue
		}
		return provider, nil
	}
}

// Close shuts down the Provider for one profile. It is idempotent: closing an
// unknown or already closed profile is a no-op. If the provider is still
// opening, Close marks it abandoned and returns; the open attempt closes the
// provider as soon as it settles.
func (r *NetworkProviderRegistry) Close(profileID string) error {
	if r == nil || profileID == "" {
		return nil
	}
	r.mu.Lock()
	entry := r.entries[profileID]
	if entry == nil {
		r.mu.Unlock()
		return nil
	}
	delete(r.entries, profileID)
	select {
	case <-entry.ready:
		provider := entry.provider
		r.mu.Unlock()
		return r.closeProvider(provider, profileID)
	default:
		entry.abandoned = true
		if entry.cancel != nil {
			entry.cancel()
		}
		r.mu.Unlock()
		return nil
	}
}

// Cancel stops the current start or interactive login for one profile and
// removes it from the registry, so the next Acquire opens a fresh Provider.
// Cancelling an unknown or already finished profile is a no-op. A Provider that
// does not implement NetworkProviderCanceller is closed instead, which is safe
// but leaves it unreusable until the next Acquire. Cancelling while an open is
// in flight cancels that open; its caller observes the cancellation, and the
// registry closes whatever the open produced.
func (r *NetworkProviderRegistry) Cancel(profileID string) error {
	if r == nil || profileID == "" {
		return nil
	}
	r.mu.Lock()
	entry := r.entries[profileID]
	if entry == nil {
		r.mu.Unlock()
		return nil
	}
	delete(r.entries, profileID)
	select {
	case <-entry.ready:
		provider := entry.provider
		hook := r.onClose
		r.mu.Unlock()
		var err error
		if canceller, ok := provider.(NetworkProviderCanceller); ok {
			err = canceller.Cancel()
			if hook != nil && provider != nil {
				// A cancelled Provider no longer carries this profile's idle
				// connections, so they must not be reused.
				hook(profileID)
			}
			return err
		}
		return r.closeProvider(provider, profileID)
	default:
		entry.abandoned = true
		if entry.cancel != nil {
			entry.cancel()
		}
		r.mu.Unlock()
		return nil
	}
}

// CloseAll shuts down every Provider and makes the registry reject later
// Acquires. It is idempotent. Providers still opening are abandoned and close
// themselves when their factory returns.
func (r *NetworkProviderRegistry) CloseAll() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	entries := r.entries
	r.entries = make(map[string]*providerEntry)

	var providers []NetworkProvider
	var profiles []string
	for profileID, entry := range entries {
		select {
		case <-entry.ready:
			if entry.provider != nil {
				providers = append(providers, entry.provider)
				profiles = append(profiles, profileID)
			}
		default:
			entry.abandoned = true
			if entry.cancel != nil {
				entry.cancel()
			}
		}
	}
	r.mu.Unlock()

	var errs []error
	for i, provider := range providers {
		if err := r.closeProvider(provider, profiles[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// closeProvider releases one Provider through its optional lifecycle contract
// and notifies the close hook. It runs without the registry lock.
func (r *NetworkProviderRegistry) closeProvider(provider NetworkProvider, profileID string) error {
	if provider == nil {
		return nil
	}
	var err error
	if closer, ok := provider.(NetworkProviderCloser); ok {
		err = closer.Close()
	}
	if r != nil {
		r.mu.Lock()
		hook := r.onClose
		r.mu.Unlock()
		if hook != nil && profileID != "" {
			hook(profileID)
		}
	}
	return err
}

// waitForProvider waits for a shared open attempt to settle.
func waitForProvider(ctx context.Context, ready <-chan struct{}, route NetworkRoute) error {
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return providerWaitError(route, ctx.Err())
	}
}

// validateNetworkRoute rejects a route the registry cannot run, before any
// socket is opened.
func validateNetworkRoute(route NetworkRoute) error {
	if strings.TrimSpace(route.ProfileID) == "" {
		return providerRouteError(route, ProviderPhaseStopped,
			fmt.Errorf("%w: empty profile id", ErrNetworkProviderProfile))
	}
	switch route.Kind {
	case string(domainvault.NetworkKindTailscale), string(domainvault.NetworkKindWireGuard):
		return nil
	default:
		return providerRouteError(route, ProviderPhaseStopped,
			fmt.Errorf("%w: %q", ErrNetworkProviderKind, route.Kind))
	}
}

// providerPhase returns the Provider's live phase, or "" when it does not
// report one. An unknown phase is never treated as a failure.
func providerPhase(provider NetworkProvider) ProviderPhase {
	reporter, ok := provider.(NetworkProviderPhaseReporter)
	if !ok {
		return ""
	}
	return reporter.Phase()
}

// providerNeedsRestart reports a cached Provider whose phase proves it cannot
// serve a new dial. NeedsLogin, Starting, and Stopping are deliberately not
// retired: those phases are part of a live lifecycle (an interactive login can
// complete, a start can finish), and the Manager fails closed with the accurate
// phase instead of hiding it behind an extra open attempt.
func providerNeedsRestart(provider NetworkProvider) bool {
	switch providerPhase(provider) {
	case ProviderPhaseStopped, ProviderPhaseError:
		return true
	default:
		return false
	}
}

// providerInfo names one route's profile for diagnostics.
func providerInfo(route NetworkRoute, phase ProviderPhase) ProviderInfo {
	return ProviderInfo{
		ProfileID: route.ProfileID,
		Label:     route.Label,
		Kind:      route.Kind,
		Phase:     phase,
	}
}

// providerRouteError builds a typed provider failure for a route-level problem.
func providerRouteError(route NetworkRoute, phase ProviderPhase, cause error) error {
	info := providerInfo(route, phase)
	return &Error{Kind: KindProvider, Cause: cause, Provider: &info}
}

// providerWaitError classifies a caller cancelled while waiting for another
// Acquire's open attempt.
func providerWaitError(route NetworkRoute, cause error) error {
	return providerCauseError(providerInfo(route, ProviderPhaseStarting), cause)
}

// providerOpenError classifies a factory failure. A factory that already
// classified a cancellation or deadline keeps that Kind; every other startup
// failure (login, configuration, tunnel) is KindProvider. ProviderInfo is
// always present and preserves a phase the factory reported.
func providerOpenError(route NetworkRoute, cause error) error {
	info := providerInfo(route, ProviderPhaseError)
	var typed *Error
	if errors.As(cause, &typed) {
		if typed.Provider != nil {
			mergeProviderInfo(&info, *typed.Provider)
		}
		clone := *typed
		clone.Provider = &info
		switch clone.Kind {
		case KindCancelled, KindTimeout:
		default:
			clone.Kind = KindProvider
		}
		return &clone
	}
	return providerCauseError(info, cause)
}

// providerCauseError classifies an untyped provider cause. Cancellation and a
// deadline keep their transport Kind; every other provider-level failure is
// KindProvider.
func providerCauseError(info ProviderInfo, cause error) error {
	switch {
	case errors.Is(cause, context.Canceled):
		return &Error{Kind: KindCancelled, Cause: cause, Provider: &info}
	case errors.Is(cause, context.DeadlineExceeded):
		return &Error{Kind: KindTimeout, Cause: cause, Provider: &info}
	default:
		return &Error{Kind: KindProvider, Cause: cause, Provider: &info}
	}
}

// mergeProviderInfo fills fields the factory reported over the route-derived
// defaults. A factory knows the live lifecycle phase, so its value wins.
func mergeProviderInfo(dst *ProviderInfo, src ProviderInfo) {
	if src.ProfileID != "" {
		dst.ProfileID = src.ProfileID
	}
	if src.Label != "" {
		dst.Label = src.Label
	}
	if src.Kind != "" {
		dst.Kind = src.Kind
	}
	if src.Phase != "" {
		dst.Phase = src.Phase
	}
}
