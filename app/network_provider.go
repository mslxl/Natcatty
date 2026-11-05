// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	adaptertailscale "github.com/mslxl/natcatty/adapters/tailscale"
	appconnection "github.com/mslxl/natcatty/application/connection"
	apphistory "github.com/mslxl/natcatty/application/history"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// networkProfileVault is the Vault read surface the Network Provider factory
// needs. The profile record is non-secret; the Auth Key is read only when the
// Provider actually starts and never enters the route, a pool key, or a log.
type networkProfileVault interface {
	NetworkProfile(ctx context.Context, id string) (domainvault.NetworkProfile, error)
	NetworkProfileSecret(ctx context.Context, id, field string) (string, error)
}

// tailscaleOpener constructs and starts one Tailscale Net Provider. The
// composition root uses the real adapter; tests replace it to prove the
// factory's loading and dispatch rules without a network.
type tailscaleOpener func(context.Context, adaptertailscale.Config,
	...adaptertailscale.Option) (appconnection.NetworkProvider, error)

// networkProviderFactory dispatches a Network Route to the Provider that can
// run its Kind. WireGuard Net lands in ticket 08; until then selecting one
// fails closed with KindProvider and never falls back to a direct dial.
type networkProviderFactory struct {
	vault     networkProfileVault
	stateBase string
	observe   func(adaptertailscale.Status)
	logf      func(format string, args ...any)

	openTailscale tailscaleOpener
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
		return nil, networkRouteError(route, appconnection.ProviderPhaseError,
			errors.New("WireGuard Net is not available in this build yet"))
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

	authKey, err := f.vault.NetworkProfileSecret(ctx, route.ProfileID, appvault.FieldNetworkAuthKey)
	switch {
	case errors.Is(err, appvault.ErrNotFound):
		// No stored key means the node uses interactive login. The adapter
		// reports NeedsLogin with the Auth URL instead of pretending to dial.
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
		options = append(options, adaptertailscale.WithStatusObserver(f.observe))
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

// networkProviderStatusSink fans one Provider status out to the Vault surface
// and the Connection Log. It is safe to call from the Provider's dial
// goroutine: the Vault state enqueues for the UI thread, and the history
// service owns its own synchronization.
type networkProviderStatusSink struct {
	vault   func() *vaultstate.State
	history *apphistory.Service
	now     func() time.Time
}

// Observe publishes a non-secret status. The Auth URL stays in the Vault
// surface for the login flow; it is deliberately not written to the connection
// log.
func (s *networkProviderStatusSink) Observe(status adaptertailscale.Status) {
	if s == nil {
		return
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

// connectionLogFromProviderStatus maps one provider status change onto a
// Connection Log row. Starting and stopping are transient progress, so they are
// not recorded; running, needs-login, error, and stopped are useful diagnostic
// events. AuthURL is never part of the row.
func connectionLogFromProviderStatus(status adaptertailscale.Status,
	at time.Time) (domainhistory.ConnectionLog, bool) {
	switch status.Phase {
	case appconnection.ProviderPhaseRunning, appconnection.ProviderPhaseNeedsLogin,
		appconnection.ProviderPhaseError, appconnection.ProviderPhaseStopped:
	default:
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
