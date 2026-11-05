// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	adaptertailscale "github.com/mslxl/natcatty/adapters/tailscale"
	adapterwireguard "github.com/mslxl/natcatty/adapters/wireguard"
	appconnection "github.com/mslxl/natcatty/application/connection"
	apphistory "github.com/mslxl/natcatty/application/history"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// networkProfileVault is the Vault read surface the Network Provider factory
// needs. The profile record is non-secret; credential plaintext (the Tailscale
// Auth Key, the WireGuard Private Key, and each peer's Preshared Key) is read
// only when the Provider actually starts and never enters the route, a pool
// key, or a log.
type networkProfileVault interface {
	NetworkProfile(ctx context.Context, id string) (domainvault.NetworkProfile, error)
	NetworkProfileSecret(ctx context.Context, id, field string) (string, error)
	NetworkPeerPresharedKey(ctx context.Context, profileID, peerID string) (string, error)
}

// tailscaleOpener constructs and starts one Tailscale Net Provider. The
// composition root uses the real adapter; tests replace it to prove the
// factory's loading and dispatch rules without a network.
type tailscaleOpener func(context.Context, adaptertailscale.Config,
	...adaptertailscale.Option) (appconnection.NetworkProvider, error)

// wireguardOpener constructs and starts one WireGuard Net Provider. The
// composition root uses the real adapter; tests replace it to prove the
// factory's loading and dispatch rules without a userspace network stack.
type wireguardOpener func(context.Context, adapterwireguard.Config,
	...adapterwireguard.Option) (appconnection.NetworkProvider, error)

// networkStateCleaner implements the Vault's Network Profile state-cleanup
// port. Only Tailscale Net has local runtime state (the tsnet node directory);
// WireGuard keys are memory-only and leave no file behind. The cleaner runs
// only from the confirmed profile delete.
type networkStateCleaner struct {
	base string
}

func (c networkStateCleaner) RemoveNetworkProfileState(_ context.Context,
	profile domainvault.NetworkProfile) error {
	if profile.Kind != domainvault.NetworkKindTailscale {
		return nil
	}
	return adaptertailscale.RemoveStateDir(c.base, profile.ID)
}

// networkProviderFactory dispatches a Network Route to the Provider that can
// run its Kind. Tailscale Net uses the embedded tsnet adapter and WireGuard Net
// the in-process netstack adapter. Any other Kind fails closed with
// KindProvider and never falls back to a direct dial.
type networkProviderFactory struct {
	vault     networkProfileVault
	stateBase string
	observe   func(adaptertailscale.Status)
	logf      func(format string, args ...any)

	observeWireGuard func(adapterwireguard.Status)
	openTailscale    tailscaleOpener
	openWireGuard    wireguardOpener
}

var _ appconnection.NetworkProviderFactory = (*networkProviderFactory)(nil)

// Open starts the Provider for one route. Every failure is a typed provider
// error carrying the route's profile identity.
func (f *networkProviderFactory) Open(ctx context.Context,
	route appconnection.NetworkRoute) (appconnection.NetworkProvider, error) {
	switch domainvault.NetworkProfileKind(route.Kind) {
	case domainvault.NetworkKindTailscale:
		return f.openTailscaleRoute(ctx, route)
	case domainvault.NetworkKindWireGuard:
		return f.openWireGuardRoute(ctx, route)
	default:
		return nil, networkRouteError(route, appconnection.ProviderPhaseStopped,
			fmt.Errorf("%w: %q", appconnection.ErrNetworkProviderKind, route.Kind))
	}
}

// openTailscaleRoute loads the profile and its Auth Key through the Vault and
// starts the in-process tsnet node. A missing Auth Key is not an error: it is
// the interactive-login path, which the adapter reports as NeedsLogin with an
// Auth URL. A locked or unreadable secret is a provider failure.
func (f *networkProviderFactory) openTailscaleRoute(ctx context.Context,
	route appconnection.NetworkRoute) (appconnection.NetworkProvider, error) {
	if f.vault == nil {
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			errors.New("network profile vault is unavailable"))
	}
	profile, err := f.vault.NetworkProfile(ctx, route.ProfileID)
	if err != nil {
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			fmt.Errorf("read Network Profile %q: %w", route.ProfileID, err))
	}
	if profile.Kind != domainvault.NetworkKindTailscale {
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			fmt.Errorf("Network Profile %q is %q, route selects %q",
				route.ProfileID, profile.Kind, route.Kind))
	}
	if profile.Tailscale == nil {
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			fmt.Errorf("Network Profile %q has no Tailscale configuration", route.ProfileID))
	}
	if route.Label == "" {
		route.Label = profile.Label
	}

	// Read the Auth Key unconditionally. A session-only secret store keeps the
	// value while the record's HasAuthKey marker is false, so gating this read
	// on the marker would silently switch a keyed profile to interactive login.
	authKey, err := f.vault.NetworkProfileSecret(ctx, route.ProfileID, appvault.FieldNetworkAuthKey)
	switch {
	case errors.Is(err, appvault.ErrNotFound):
		// No stored key means the node uses interactive login. The adapter
		// reports NeedsLogin with the Auth URL instead of pretending to dial.
		// This also covers a stale HasAuthKey marker: absence is a defined
		// login mode for Tailscale rather than a credential mismatch.
		authKey = ""
	case err != nil:
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			fmt.Errorf("read the Auth Key for Network Profile %q: %w", route.ProfileID, err))
	}

	cfg := adaptertailscale.Config{
		ProfileID:     profile.ID,
		Label:         profile.Label,
		Kind:          profile.Kind,
		StateBaseDir:  f.stateBase,
		Hostname:      profile.Tailscale.Hostname,
		ControlURL:    profile.Tailscale.ControlURL,
		Ephemeral:     profile.Tailscale.Ephemeral,
		AdvertiseTags: append([]string(nil), profile.Tailscale.Tags...),
		AuthKey:       authKey,
	}
	var options []adaptertailscale.Option
	if f.observe != nil {
		// The adapter redacts its own diagnostics, but the app is the last
		// boundary before the Vault surface and the persisted Connection Log.
		// Redact again with the exact credential this route loaded so a future
		// adapter regression cannot make key material durable.
		observer := f.observe
		options = append(options, adaptertailscale.WithStatusObserver(func(status adaptertailscale.Status) {
			observer(redactTailscaleStatus(status, authKey))
		}))
	}
	if f.logf != nil {
		options = append(options, adaptertailscale.WithLogf(f.logf))
	}

	open := f.openTailscale
	if open == nil {
		open = openTailscaleProvider
	}
	provider, err := open(ctx, cfg, options...)
	if err != nil {
		return nil, networkProviderFailure(route, err)
	}
	if provider == nil {
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			errors.New("network provider factory returned no provider"))
	}
	return provider, nil
}

// openTailscaleProvider is the production opener: construct the adapter and
// join the tailnet before handing the Provider to the registry. A failed start
// closes the adapter so no node is left behind.
func openTailscaleProvider(ctx context.Context, cfg adaptertailscale.Config,
	options ...adaptertailscale.Option) (appconnection.NetworkProvider, error) {
	provider, err := adaptertailscale.NewProvider(cfg, options...)
	if err != nil {
		return nil, err
	}
	if err := provider.Start(ctx); err != nil {
		_ = provider.Close()
		return nil, err
	}
	return provider, nil
}

// openWireGuardProvider is the production opener: construct the adapter and
// start the in-process netstack device before handing the Provider to the
// registry. A failed start closes the adapter so no device is left behind.
func openWireGuardProvider(ctx context.Context, cfg adapterwireguard.Config,
	options ...adapterwireguard.Option) (appconnection.NetworkProvider, error) {
	provider, err := adapterwireguard.NewProvider(cfg, options...)
	if err != nil {
		return nil, err
	}
	if err := provider.Start(ctx); err != nil {
		_ = provider.Close()
		return nil, err
	}
	return provider, nil
}

// openWireGuardRoute loads the profile, its Private Key, and every stored
// Preshared Key through the Vault and starts the in-process netstack device.
// WireGuard has no interactive login, so a missing Private Key is a provider
// failure: the route fails closed instead of dialing directly. A peer whose
// marker says it has a Preshared Key but whose secret is unreadable also fails
// closed, because starting without it would silently change authentication.
func (f *networkProviderFactory) openWireGuardRoute(ctx context.Context,
	route appconnection.NetworkRoute) (appconnection.NetworkProvider, error) {
	if f.vault == nil {
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			errors.New("network profile vault is unavailable"))
	}
	profile, err := f.vault.NetworkProfile(ctx, route.ProfileID)
	if err != nil {
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			fmt.Errorf("read Network Profile %q: %w", route.ProfileID, err))
	}
	if profile.Kind != domainvault.NetworkKindWireGuard {
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			fmt.Errorf("Network Profile %q is %q, route selects %q",
				route.ProfileID, profile.Kind, route.Kind))
	}
	if profile.WireGuard == nil {
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			fmt.Errorf("Network Profile %q has no WireGuard configuration", route.ProfileID))
	}
	if route.Label == "" {
		route.Label = profile.Label
	}

	// As with the Auth Key, read unconditionally: SecretModeSession stores the
	// key but records HasPrivateKey=false. WireGuard has no login fallback, so a
	// missing key always fails closed below.
	privateKey, err := f.vault.NetworkProfileSecret(ctx, route.ProfileID,
		appvault.FieldNetworkPrivateKey)
	switch {
	case errors.Is(err, appvault.ErrNotFound):
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			fmt.Errorf("Network Profile %q has no Private Key; WireGuard cannot start without one",
				route.ProfileID))
	case err != nil:
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			fmt.Errorf("read the Private Key for Network Profile %q: %w", route.ProfileID, err))
	}

	peers := make([]adapterwireguard.Peer, 0, len(profile.WireGuard.Peers))
	// Every credential this route loaded, used to scrub status diagnostics
	// before they reach the Vault surface or the Connection Log.
	routeSecrets := []string{privateKey}
	for _, peer := range profile.WireGuard.Peers {
		// Read every peer's Preshared Key, not only the marked ones. A
		// session-only secret store keeps the value while HasPresharedKey is
		// false; skipping the read there would start the tunnel without its
		// configured PSK and every handshake would fail.
		presharedKey, readErr := f.vault.NetworkPeerPresharedKey(ctx, route.ProfileID, peer.ID)
		switch {
		case readErr == nil:
			// A stored value is used even when the marker is false (session
			// mode) because it is the credential the user configured.
		case errors.Is(readErr, appvault.ErrNotFound):
			if peer.HasPresharedKey {
				// The record claims a stored Preshared Key but the secret is
				// gone. Starting without it would silently weaken or break
				// authentication, so fail closed.
				return nil, networkRouteError(route, appconnection.ProviderPhaseError,
					fmt.Errorf("Network Profile %q marks a Preshared Key for peer %q but none is stored; refusing to start without it",
						route.ProfileID, peer.ID))
			}
			presharedKey = ""
		default:
			return nil, networkRouteError(route, appconnection.ProviderPhaseError,
				fmt.Errorf("read the Preshared Key for peer %q of Network Profile %q: %w",
					peer.ID, route.ProfileID, readErr))
		}
		if presharedKey != "" {
			routeSecrets = append(routeSecrets, presharedKey)
		}
		peers = append(peers, adapterwireguard.Peer{
			ID:                  peer.ID,
			PublicKey:           peer.PublicKey,
			PresharedKey:        presharedKey,
			Endpoint:            peer.Endpoint,
			AllowedIPs:          append([]string(nil), peer.AllowedIPs...),
			PersistentKeepalive: peer.PersistentKeepalive,
		})
	}

	cfg := adapterwireguard.Config{
		ProfileID:  profile.ID,
		Label:      profile.Label,
		Kind:       profile.Kind,
		Addresses:  append([]string(nil), profile.WireGuard.Addresses...),
		DNS:        append([]string(nil), profile.WireGuard.DNS...),
		MTU:        profile.WireGuard.MTU,
		ListenPort: profile.WireGuard.ListenPort,
		PrivateKey: privateKey,
		Peers:      peers,
	}
	var options []adapterwireguard.Option
	if f.observeWireGuard != nil {
		// Defense in depth for the persisted Connection Log: an adapter bug
		// must not be able to write a Private Key or Preshared Key through the
		// status path.
		observer := f.observeWireGuard
		options = append(options, adapterwireguard.WithStatusObserver(func(status adapterwireguard.Status) {
			observer(redactWireGuardStatus(status, routeSecrets...))
		}))
	}
	if f.logf != nil {
		options = append(options, adapterwireguard.WithLogf(f.logf))
	}

	open := f.openWireGuard
	if open == nil {
		open = openWireGuardProvider
	}
	provider, err := open(ctx, cfg, options...)
	if err != nil {
		return nil, networkProviderFailure(route, err)
	}
	if provider == nil {
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			errors.New("network provider factory returned no provider"))
	}
	return provider, nil
}

// networkProviderFailure attaches the route identity to an adapter failure and
// preserves a more specific Kind and Phase the adapter reported. It never
// converts a provider failure into a direct-dial path.
func networkProviderFailure(route appconnection.NetworkRoute, cause error) error {
	phase := appconnection.ProviderPhaseError
	kind := appconnection.KindProvider
	var typed *appconnection.Error
	if errors.As(cause, &typed) {
		switch typed.Kind {
		case appconnection.KindCancelled, appconnection.KindTimeout,
			appconnection.KindUnsupported:
			kind = typed.Kind
		}
		if typed.Provider != nil && typed.Provider.Phase != "" {
			phase = typed.Provider.Phase
		}
	}
	return &appconnection.Error{Kind: kind, Cause: cause, Provider: &appconnection.ProviderInfo{
		ProfileID: route.ProfileID,
		Label:     route.Label,
		Kind:      route.Kind,
		Phase:     phase,
	}}
}

// networkRouteError builds a typed provider failure for a route-level problem.
func networkRouteError(route appconnection.NetworkRoute,
	phase appconnection.ProviderPhase, cause error) error {
	return &appconnection.Error{Kind: appconnection.KindProvider, Cause: cause,
		Provider: &appconnection.ProviderInfo{
			ProfileID: route.ProfileID,
			Label:     route.Label,
			Kind:      route.Kind,
			Phase:     phase,
		}}
}

// networkProviderRegistry is the lifecycle surface the interactive-login
// controller needs. The connection registry implements it; tests script it.
type networkProviderRegistry interface {
	Acquire(ctx context.Context, route appconnection.NetworkRoute) (appconnection.NetworkProvider, error)
	Cancel(profileID string) error
}

// networkProviderStart is one in-flight start or login attempt the Vault
// triggered. Cancel and a later Start use it to stop the previous attempt
// before opening the next, so two tsnet servers never share one profile's state
// directory.
type networkProviderStart struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// networkProviderController drives a Network Profile's Provider from the Vault
// surface, without a Host dial. It resolves the profile's non-secret route on
// every call, so an edit between attempts starts the edited configuration. The
// Auth Key is still read only by the Provider factory when the node starts.
type networkProviderController struct {
	vault    networkProfileVault
	registry networkProviderRegistry

	mu     sync.Mutex
	starts map[string]*networkProviderStart
}

var _ vaultstate.NetworkProviderActions = (*networkProviderController)(nil)

// StartNetworkProvider starts one profile's Provider for interactive login. It
// returns when the node reaches Running or NeedsLogin; the status observer
// carries the Auth URL. A start already in flight for the same profile is
// cancelled and awaited first, so repeated clicks cannot leave two nodes behind.
func (c *networkProviderController) StartNetworkProvider(ctx context.Context,
	profileID string) error {
	if c == nil || c.registry == nil {
		return networkRouteError(appconnection.NetworkRoute{ProfileID: profileID},
			appconnection.ProviderPhaseError, errors.New("network provider registry is unavailable"))
	}
	profile, err := c.loadProfile(ctx, profileID)
	if err != nil {
		return err
	}
	route := appconnection.NetworkRoute{
		ProfileID:   profile.ID,
		Kind:        string(profile.Kind),
		Label:       profile.Label,
		Fingerprint: domainvault.NetworkProfileFingerprint(profile),
	}

	for {
		c.mu.Lock()
		previous := c.starts[profile.ID]
		if previous == nil {
			startCtx, cancel := context.WithCancel(ctx)
			start := &networkProviderStart{cancel: cancel, done: make(chan struct{})}
			if c.starts == nil {
				c.starts = make(map[string]*networkProviderStart)
			}
			c.starts[profile.ID] = start
			c.mu.Unlock()

			_, err := c.registry.Acquire(startCtx, route)
			c.finishStart(profile.ID, start)
			return err
		}
		c.mu.Unlock()
		// Retire the older attempt before opening another node for the profile.
		previous.cancel()
		<-previous.done
	}
}

// CancelNetworkProvider stops the current start or login wait and releases the
// Provider through the registry. The profile's next Acquire starts afresh.
func (c *networkProviderController) CancelNetworkProvider(_ context.Context,
	profileID string) error {
	if c == nil || c.registry == nil {
		return nil
	}
	c.mu.Lock()
	start := c.starts[profileID]
	c.mu.Unlock()
	if start != nil {
		start.cancel()
		<-start.done
	}
	return c.registry.Cancel(profileID)
}

// RetryNetworkProvider cancels any current attempt and starts a new node. It is
// the same fail-closed path as a first login: no direct dial is ever tried.
func (c *networkProviderController) RetryNetworkProvider(ctx context.Context,
	profileID string) error {
	if err := c.CancelNetworkProvider(ctx, profileID); err != nil {
		return err
	}
	return c.StartNetworkProvider(ctx, profileID)
}

// finishStart removes one attempt from the in-flight table and releases its
// context. The map entry is compared by identity so a newer attempt survives.
func (c *networkProviderController) finishStart(profileID string, start *networkProviderStart) {
	c.mu.Lock()
	if c.starts[profileID] == start {
		delete(c.starts, profileID)
	}
	c.mu.Unlock()
	start.cancel()
	close(start.done)
}

// loadProfile reads one Network Profile and classifies a failure as a typed
// provider error with the profile identity.
func (c *networkProviderController) loadProfile(ctx context.Context,
	profileID string) (domainvault.NetworkProfile, error) {
	route := appconnection.NetworkRoute{ProfileID: profileID}
	if c.vault == nil {
		return domainvault.NetworkProfile{}, networkRouteError(route,
			appconnection.ProviderPhaseError, errors.New("network profile vault is unavailable"))
	}
	profile, err := c.vault.NetworkProfile(ctx, profileID)
	if err != nil {
		return domainvault.NetworkProfile{}, networkRouteError(route,
			appconnection.ProviderPhaseError,
			fmt.Errorf("read Network Profile %q: %w", profileID, err))
	}
	return profile, nil
}

// redactCredentialText replaces every non-empty credential with a placeholder.
// It is the app boundary's defense in depth: adapters already redact their own
// diagnostics, but a status text here becomes durable in the Connection Log.
func redactCredentialText(text string, secrets ...string) string {
	if text == "" {
		return ""
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return text
}

// redactedStatusError keeps the original error when nothing matched, so
// errors.Is still classifies an untouched failure. A redacted error keeps its
// cause for the same reason while exposing only the scrubbed text.
func redactedStatusError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	redacted := redactCredentialText(text, secrets...)
	if redacted == text {
		return err
	}
	return redactedError{text: redacted, cause: err}
}

type redactedError struct {
	text  string
	cause error
}

func (e redactedError) Error() string { return e.text }

func (e redactedError) Unwrap() error { return e.cause }

// redactTailscaleStatus scrubs every free-form field of a status before it
// crosses into the Vault surface or the Connection Log. Numeric, identity, and
// phase fields are non-secret and stay intact.
func redactTailscaleStatus(status adaptertailscale.Status, secrets ...string) adaptertailscale.Status {
	status.Label = redactCredentialText(status.Label, secrets...)
	status.DNSName = redactCredentialText(status.DNSName, secrets...)
	status.AuthURL = redactCredentialText(status.AuthURL, secrets...)
	status.TailscaleIPs = append([]string(nil), status.TailscaleIPs...)
	for index, address := range status.TailscaleIPs {
		status.TailscaleIPs[index] = redactCredentialText(address, secrets...)
	}
	status.Err = redactedStatusError(status.Err, secrets...)
	return status
}

// redactWireGuardStatus scrubs the free-form fields of a WireGuard status. The
// indexed loop keeps the caller's slice untouched.
func redactWireGuardStatus(status adapterwireguard.Status, secrets ...string) adapterwireguard.Status {
	status.Label = redactCredentialText(status.Label, secrets...)
	status.Peers = append([]adapterwireguard.PeerStatus(nil), status.Peers...)
	for index := range status.Peers {
		status.Peers[index].Endpoint = redactCredentialText(status.Peers[index].Endpoint, secrets...)
	}
	status.Err = redactedStatusError(status.Err, secrets...)
	return status
}

// networkProviderStatusSink fans one Provider status out to the Vault surface
// and the Connection Log. It is safe to call from the Provider's dial
// goroutine: the Vault state enqueues for the UI thread, and the history
// service owns its own synchronization.
type networkProviderStatusSink struct {
	vault   func() *vaultstate.State
	history *apphistory.Service
	now     func() time.Time
	// onPhase receives every Provider phase change. The runtime uses terminal
	// phases to invalidate idle Connections built through that Profile. It runs
	// on the Provider goroutine and must not block for long.
	onPhase func(profileID string, phase appconnection.ProviderPhase)

	// mu guards wireGuardLogState, which suppresses counter-only updates from
	// the live WireGuard status poll. A busy tunnel would otherwise append a
	// Connection Log row on every sample; the log records lifecycle changes and
	// the first completed handshake instead.
	mu                sync.Mutex
	wireGuardLogState map[string]wireGuardLogEvent
}

// wireGuardLogEvent is the part of a WireGuard status that decides whether a
// Connection Log row is a new event. Counters are deliberately excluded.
type wireGuardLogEvent struct {
	phase       appconnection.ProviderPhase
	established bool
}

// Observe publishes a non-secret status. The Auth URL stays in the Vault
// surface for the login flow; it is deliberately not written to the connection
// log.
func (s *networkProviderStatusSink) Observe(status adaptertailscale.Status) {
	if s == nil {
		return
	}
	if s.onPhase != nil && status.ProfileID != "" {
		s.onPhase(status.ProfileID, status.Phase)
	}
	if s.vault != nil {
		if state := s.vault(); state != nil {
			state.PostNetworkProviderStatus(vaultstate.NetworkProviderStatus{
				ProfileID:    status.ProfileID,
				Label:        status.Label,
				Kind:         status.Kind,
				Phase:        status.Phase,
				TailscaleIPs: append([]string(nil), status.TailscaleIPs...),
				DNSName:      status.DNSName,
				AuthURL:      status.AuthURL,
				LastError:    statusErrorText(status.Err),
			})
		}
	}
	if s.history == nil {
		return
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	log, ok := connectionLogFromProviderStatus(status, now())
	if !ok {
		return
	}
	_ = s.history.RecordConnection(context.Background(), log)
}

func statusErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ObserveWireGuard publishes one WireGuard Net status. The Vault surface
// receives every change so its row shows live counters; the Connection Log
// receives only lifecycle changes and the first completed handshake, because a
// counter poll is not a new event. Nothing here contains key material.
func (s *networkProviderStatusSink) ObserveWireGuard(status adapterwireguard.Status) {
	if s == nil {
		return
	}
	if s.onPhase != nil && status.ProfileID != "" {
		s.onPhase(status.ProfileID, status.Phase)
	}
	if s.vault != nil {
		if state := s.vault(); state != nil {
			state.PostNetworkProviderStatus(vaultstate.NetworkProviderStatus{
				ProfileID:            status.ProfileID,
				Label:                status.Label,
				Kind:                 status.Kind,
				Phase:                status.Phase,
				WireGuardEndpoint:    status.Endpoint(),
				HandshakeEstablished: status.Handshake == adapterwireguard.HandshakeEstablished,
				LastHandshake:        status.LastHandshake,
				RxBytes:              status.RxBytes,
				TxBytes:              status.TxBytes,
				LastError:            statusErrorText(status.Err),
			})
		}
	}
	if s.history == nil {
		return
	}
	if !s.shouldRecordWireGuard(status) {
		return
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	log, ok := connectionLogFromWireGuardStatus(status, now())
	if !ok {
		return
	}
	_ = s.history.RecordConnection(context.Background(), log)
}

// providerLoggablePhase reports whether a Provider phase is a useful
// Connection Log event. Starting and stopping are transient progress, so they
// are not recorded; running, needs-login, error, and stopped are diagnostic
// events.
func providerLoggablePhase(phase appconnection.ProviderPhase) bool {
	switch phase {
	case appconnection.ProviderPhaseRunning, appconnection.ProviderPhaseNeedsLogin,
		appconnection.ProviderPhaseError, appconnection.ProviderPhaseStopped:
		return true
	}
	return false
}

// shouldRecordWireGuard reports whether a status is a new loggable event. A
// changed phase or the first completed handshake is new; a counter-only update
// is not.
func (s *networkProviderStatusSink) shouldRecordWireGuard(status adapterwireguard.Status) bool {
	if status.ProfileID == "" {
		return false
	}
	if !providerLoggablePhase(status.Phase) {
		return false
	}
	current := wireGuardLogEvent{
		phase:       status.Phase,
		established: status.Handshake == adapterwireguard.HandshakeEstablished,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wireGuardLogState == nil {
		s.wireGuardLogState = make(map[string]wireGuardLogEvent)
	}
	if previous, seen := s.wireGuardLogState[status.ProfileID]; seen && previous == current {
		return false
	}
	s.wireGuardLogState[status.ProfileID] = current
	return true
}

// connectionLogFromProviderStatus maps one provider status change onto a
// Connection Log row. Starting and stopping are transient progress, so they are
// not recorded; running, needs-login, error, and stopped are useful diagnostic
// events. AuthURL is never part of the row.
func connectionLogFromProviderStatus(status adaptertailscale.Status,
	at time.Time) (domainhistory.ConnectionLog, bool) {
	if !providerLoggablePhase(status.Phase) {
		return domainhistory.ConnectionLog{}, false
	}
	kind := status.Kind
	if kind == "" {
		kind = string(domainvault.NetworkKindTailscale)
	}
	address := status.DNSName
	if address == "" && len(status.TailscaleIPs) > 0 {
		address = status.TailscaleIPs[0]
	}
	connectionStatus := domainhistory.ConnectionConnected
	if status.Phase != appconnection.ProviderPhaseRunning {
		connectionStatus = domainhistory.ConnectionFailed
	}
	return domainhistory.ConnectionLog{
		HostLabel:            status.Label,
		Kind:                 kind,
		Status:               connectionStatus,
		Error:                statusErrorText(status.Err),
		NetworkProfileID:     status.ProfileID,
		NetworkProfileLabel:  status.Label,
		NetworkProviderKind:  kind,
		NetworkProviderPhase: string(status.Phase),
		NetworkAddress:       address,
		StartedAt:            at,
		EndedAt:              at,
	}, true
}

// connectionLogFromWireGuardStatus maps one WireGuard Net status onto a
// Connection Log row. Starting and stopping are transient progress, so they are
// not recorded; running, error, and stopped are useful diagnostic events. The
// row keeps the current peer Endpoint, the most recent handshake, and the
// summed counters, and never carries key material.
func connectionLogFromWireGuardStatus(status adapterwireguard.Status,
	at time.Time) (domainhistory.ConnectionLog, bool) {
	if !providerLoggablePhase(status.Phase) {
		return domainhistory.ConnectionLog{}, false
	}
	kind := status.Kind
	if kind == "" {
		kind = string(domainvault.NetworkKindWireGuard)
	}
	connectionStatus := domainhistory.ConnectionConnected
	if status.Phase != appconnection.ProviderPhaseRunning {
		connectionStatus = domainhistory.ConnectionFailed
	}
	return domainhistory.ConnectionLog{
		HostLabel:            status.Label,
		Kind:                 kind,
		Status:               connectionStatus,
		Error:                statusErrorText(status.Err),
		NetworkProfileID:     status.ProfileID,
		NetworkProfileLabel:  status.Label,
		NetworkProviderKind:  kind,
		NetworkProviderPhase: string(status.Phase),
		NetworkAddress:       status.Endpoint(),
		NetworkLastHandshake: status.LastHandshake,
		NetworkRxBytes:       status.RxBytes,
		NetworkTxBytes:       status.TxBytes,
		StartedAt:            at,
		EndedAt:              at,
	}, true
}
