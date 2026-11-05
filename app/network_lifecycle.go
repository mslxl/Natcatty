// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// networkProfileProviderCloser shuts down one profile's live Provider.
type networkProfileProviderCloser interface {
	Close(profileID string) error
}

// networkProfileLeaseInvalidator drops idle pooled Connections for one profile.
type networkProfileLeaseInvalidator interface {
	InvalidateNetworkProfile(profileID string)
}

// networkProfileRuntime retires runtime state after a Network Profile edit or
// deletion has been persisted. The Vault service is constructed before the
// Provider registry and Connection Manager, so this small indirection is bound
// after both exist.
type networkProfileRuntime struct {
	providers   networkProfileProviderCloser
	connections networkProfileLeaseInvalidator
}

// InvalidateProfile closes the profile's Provider and invalidates its idle
// pooled Connections. Closing the Provider is what makes the next I/O on an
// active Session a typed KindProvider failure; invalidation is repeated
// explicitly so even a Provider without the optional closer contract still
// drops parked transports built from the old configuration.
func (r *networkProfileRuntime) InvalidateProfile(profileID string) {
	if r == nil || profileID == "" {
		return
	}
	if r.providers != nil {
		_ = r.providers.Close(profileID)
	}
	if r.connections != nil {
		r.connections.InvalidateNetworkProfile(profileID)
	}
}

// PhaseChanged drops idle pooled Connections when a live Provider stops, fails,
// or loses its authorization. Those transports were opened through a tunnel
// that can no longer be trusted; active leases keep theirs so the owner sees a
// typed failure on its next I/O. The runtime never opens or replaces a
// connection itself.
func (r *networkProfileRuntime) PhaseChanged(profileID string, phase appconnection.ProviderPhase) {
	if r == nil || profileID == "" || r.connections == nil {
		return
	}
	switch phase {
	case appconnection.ProviderPhaseStopped, appconnection.ProviderPhaseError,
		appconnection.ProviderPhaseNeedsLogin:
		r.connections.InvalidateNetworkProfile(profileID)
	}
}

// liveSessionLister is the Session read surface the delete warning uses. The
// Session Service implements it; a test substitutes a fake.
type liveSessionLister interface {
	DescribeAll() []appsession.SessionDescription
}

// sessionNetworkProfileResolver resolves the Network Profile a Host's first
// endpoint would use without loading credentials. The app's route source
// implements it.
type sessionNetworkProfileResolver interface {
	NetworkProfileIDForHost(ctx context.Context, id model.HostID) (string, error)
}

// networkProfileLeaseCounter reports active Connection leases for a profile.
type networkProfileLeaseCounter interface {
	ActiveNetworkProfileLeases(profileID string) int
}

// networkProfileRuntimeInUse reports whether a delete would pull a Network
// Profile out from under something that is running now. It checks both live
// Connection leases and every Session whose current route resolves to the
// profile; the latter covers transports that never enter the pool, such as
// Telnet. A resolution failure is ignored: the warning is advisory and must
// never block the delete confirmation.
func networkProfileRuntimeInUse(ctx context.Context, profileID string,
	sessions liveSessionLister, routes sessionNetworkProfileResolver,
	leases networkProfileLeaseCounter) bool {

	if profileID == "" {
		return false
	}
	if leases != nil && leases.ActiveNetworkProfileLeases(profileID) > 0 {
		return true
	}
	if sessions == nil || routes == nil {
		return false
	}
	for _, description := range sessions.DescribeAll() {
		if description.HostID == "" {
			continue
		}
		// Only transports that can select a Network Profile count. A local or
		// serial Session has no route through a Provider.
		switch description.Kind {
		case appsession.KindSSH, appsession.KindTelnet, appsession.KindET:
		default:
			continue
		}
		resolved, err := routes.NetworkProfileIDForHost(ctx, description.HostID)
		if err != nil {
			continue
		}
		if resolved == profileID {
			return true
		}
	}
	return false
}
