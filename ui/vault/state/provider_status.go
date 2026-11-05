package state

import (
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// NetworkProviderStatus is the latest non-secret status of one Network
// Profile's Provider. It carries no Auth Key or credential material; AuthURL is
// the short-lived interactive-login address the user may open, not a secret the
// Vault persists. WireGuard fields describe the live provider: the current
// peer Endpoint, whether a handshake completed, when it completed, and the
// summed counters.
type NetworkProviderStatus struct {
	ProfileID    string
	Label        string
	Kind         string
	Phase        appconnection.ProviderPhase
	TailscaleIPs []string
	DNSName      string
	AuthURL      string
	LastError    string

	WireGuardEndpoint    string
	HandshakeEstablished bool
	LastHandshake        time.Time
	RxBytes              uint64
	TxBytes              uint64
}

// Address returns the name to show for the node: its tailnet DNS name when it
// has one, otherwise its first tailnet address, otherwise the WireGuard peer
// Endpoint the device currently uses.
func (status NetworkProviderStatus) Address() string {
	if status.DNSName != "" {
		return status.DNSName
	}
	if len(status.TailscaleIPs) > 0 {
		return status.TailscaleIPs[0]
	}
	return status.WireGuardEndpoint
}

// PostNetworkProviderStatus queues a status change for the Vault surface. The
// Provider's dial goroutine calls it; the queue is lock-protected and the view
// applies it with FlushNetworkProviderStatus on the UI thread. Callers must not
// include an Auth Key in the status.
func (s *State) PostNetworkProviderStatus(status NetworkProviderStatus) {
	if s == nil || status.ProfileID == "" {
		return
	}
	status.TailscaleIPs = append([]string(nil), status.TailscaleIPs...)
	s.networkStatusMu.Lock()
	s.networkStatusPending = append(s.networkStatusPending, status)
	s.networkStatusMu.Unlock()
	s.redraw()
}

// FlushNetworkProviderStatus publishes queued Provider statuses. The view calls
// it on the UI thread before rendering. A later status for the same Profile
// replaces the earlier one, so the row shows the current state.
func (s *State) FlushNetworkProviderStatus() {
	s.networkStatusMu.Lock()
	defer s.networkStatusMu.Unlock()
	if len(s.networkStatusPending) == 0 {
		return
	}
	if s.networkStatuses == nil {
		s.networkStatuses = make(map[string]NetworkProviderStatus, len(s.networkStatusPending))
	}
	for _, status := range s.networkStatusPending {
		s.networkStatuses[status.ProfileID] = status
	}
	s.networkStatusPending = nil
}

// NetworkStatus returns the latest published status of one Network Profile, or
// false when that Profile's Provider has not reported yet.
func (s *State) NetworkStatus(profileID string) (NetworkProviderStatus, bool) {
	s.networkStatusMu.Lock()
	defer s.networkStatusMu.Unlock()
	status, ok := s.networkStatuses[profileID]
	return status, ok
}
