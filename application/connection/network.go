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
// TODO(network-provider): mosh needs UDP datagrams, but mosh is being rewritten
// without its sidecar. Do not extend this interface until that rewrite lands;
// keep adapters/mosh and internal/mosh untouched in the meantime. The follow-up
// adds an optional datagram capability beside this port, not to it.
type NetworkProvider interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
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
}

// providerEntry is one profile's open attempt or running Provider. ready closes
// once the attempt settles, so waiters share its result; provider and err are
// written before ready closes. cancel stops an open attempt that Close, Cancel,
// or CloseAll abandoned.
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
		r.mu.Unlock()

		close(entry.ready)
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
