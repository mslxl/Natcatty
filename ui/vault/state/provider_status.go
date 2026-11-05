package state

import (
	"context"
	"errors"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// NetworkProviderActions starts, cancels, and retries one Network Profile's
// Provider for interactive login. The app wires the registry-backed controller;
// tests provide a fake. The calls may block while a node joins or obtains an
// Auth URL, so the State runs them off the UI thread.
type NetworkProviderActions interface {
	StartNetworkProvider(ctx context.Context, profileID string) error
	CancelNetworkProvider(ctx context.Context, profileID string) error
	RetryNetworkProvider(ctx context.Context, profileID string) error
}

// WithNetworkProviderActions gives the Vault surface the interactive-login
// lifecycle actions. Without it the Vault still shows Provider status, but the
// login and retry controls report an internal problem.
func WithNetworkProviderActions(actions NetworkProviderActions) Option {
	return func(s *State) { s.providerActions = actions }
}

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

// StartNetworkProviderLogin starts one Profile's Provider for interactive
// login. It reports false and records a problem when the Profile is gone or the
// action is not wired. The join runs off the UI thread; the Provider's status
// events carry the Auth URL and the final phase.
func (s *State) StartNetworkProviderLogin(profileID string) bool {
	return s.runNetworkProviderAction(profileID, func(ctx context.Context) error {
		return s.providerActions.StartNetworkProvider(ctx, profileID)
	})
}

// CancelNetworkProviderLogin stops the current start or login wait. The
// Provider reports Stopped and the next start begins a fresh node.
func (s *State) CancelNetworkProviderLogin(profileID string) bool {
	return s.runNetworkProviderAction(profileID, func(ctx context.Context) error {
		return s.providerActions.CancelNetworkProvider(ctx, profileID)
	})
}

// RetryNetworkProvider cancels any current attempt and starts a fresh node.
func (s *State) RetryNetworkProvider(profileID string) bool {
	return s.runNetworkProviderAction(profileID, func(ctx context.Context) error {
		return s.providerActions.RetryNetworkProvider(ctx, profileID)
	})
}

// runNetworkProviderAction runs one lifecycle action off the UI thread. A
// failure becomes an Error status for the Profile so the row shows the reason;
// a cancellation is the user's own Cancel and is not reported as a failure.
func (s *State) runNetworkProviderAction(profileID string,
	action func(context.Context) error) bool {
	profile, ok := s.NetworkProfileByID(profileID)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	if s.providerActions == nil {
		s.problem = ProblemInternal
		s.redraw()
		return false
	}
	s.problem = ProblemNone
	go func() {
		err := action(context.Background())
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		s.PostNetworkProviderStatus(NetworkProviderStatus{
			ProfileID: profile.ID,
			Label:     profile.Label,
			Kind:      string(profile.Kind),
			Phase:     appconnection.ProviderPhaseError,
			LastError: err.Error(),
		})
	}()
	return true
}
