// SPDX-License-Identifier: GPL-3.0-or-later

package tailscale

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// statusPollInterval is the LocalAPI poll cadence while a start waits for the
// node to reach a stable state. It is not a test synchronization mechanism; it
// only bounds how quickly a status change is observed.
const statusPollInterval = 500 * time.Millisecond

// backend is the minimal tsnet surface the Provider state machine drives. The
// seam exists so the auth-key, login, cancel, retry, dial, and close behavior
// is testable with a fake and never needs a network.
type backend interface {
	// Start joins the network. It returns the first stable status: Running, or
	// NeedsLogin with an Auth URL. An error means the node failed to start.
	Start(ctx context.Context) (backendStatus, error)
	// Dial opens one connection through the node.
	Dial(ctx context.Context, network, address string) (net.Conn, error)
	// Status reads the live node state without changing it.
	Status(ctx context.Context) (backendStatus, error)
	// Close releases the node. Implementations are idempotent.
	Close() error
}

// backendStatus is the non-secret node state the Provider maps onto its public
// Status. State carries the raw tsnet backend state for diagnostics.
type backendStatus struct {
	Phase        appconnection.ProviderPhase
	State        string
	TailscaleIPs []string
	DNSName      string
	AuthURL      string
}

// tsnetBackend wraps one tsnet.Server. It is the only type in this package
// that imports tailscale.com; the state machine above it knows only backend.
type tsnetBackend struct {
	server  *tsnet.Server
	authKey string

	closeOnce sync.Once
	closeErr  error
}

// newTSNetBackend builds an in-process node for one Network Profile. It does
// not start it: Start does, so an unused profile never joins the tailnet.
func newTSNetBackend(cfg Config) (backend, error) {
	if strings.TrimSpace(cfg.stateDir) == "" {
		return nil, fmt.Errorf("%w: no state directory", ErrInvalidConfig)
	}
	if isSystemTailscaleDir(cfg.stateDir) {
		return nil, fmt.Errorf("%w: refusing a system Tailscale state directory", ErrInvalidConfig)
	}
	debugf := redactingLogf(cfg.logf, cfg.AuthKey)
	server := &tsnet.Server{
		Dir:           cfg.stateDir,
		Hostname:      cfg.nodeHostname,
		AuthKey:       cfg.AuthKey,
		ControlURL:    cfg.ControlURL,
		Ephemeral:     cfg.Ephemeral,
		AdvertiseTags: append([]string(nil), cfg.AdvertiseTags...),
		// Logf only controls local debug output. It does not disable the logtail
		// upload to log.tailscale.com that tsnet performs by default.
		Logf: debugf,
		// The Auth URL is read from LocalClient().Status and delivered through
		// the status observer. Keeping UserLogf silent avoids persisting a
		// short-lived login URL in a console or log file.
		UserLogf: func(string, ...any) {},
	}
	return &tsnetBackend{server: server, authKey: cfg.AuthKey}, nil
}

// Start begins the node and waits for a stable state. It returns Running with a
// tailnet address, or NeedsLogin with an Auth URL while the server keeps
// running: a later Status can then observe the completed browser login. With an
// Auth Key it waits through tsnet's brief NeedsLogin transition; an expired or
// invalid key that still needs login after the grace window is reported as
// NeedsLogin so the interactive-login path can take over.
func (b *tsnetBackend) Start(ctx context.Context) (backendStatus, error) {
	if err := b.server.Start(); err != nil {
		return backendStatus{}, err
	}
	client, err := b.server.LocalClient()
	if err != nil {
		return backendStatus{}, err
	}

	var loginSince time.Time
	ticker := time.NewTicker(statusPollInterval)
	defer ticker.Stop()
	for {
		status, err := statusWithoutPeers(ctx, client)
		if err != nil {
			return backendStatus{}, err
		}
		state, _ := ipn.StateFromString(status.State)
		switch state {
		case ipn.Running:
			return status, nil
		case ipn.NeedsLogin:
			if status.AuthURL == "" {
				break
			}
			if b.authKey == "" {
				return status, nil
			}
			if loginSince.IsZero() {
				loginSince = time.Now()
			}
			if time.Since(loginSince) >= authKeyLoginGrace {
				return status, nil
			}
		case ipn.NeedsMachineAuth:
			return status, errors.New("tailscale: machine authorization required")
		case ipn.Stopped:
			return status, errors.New("tailscale: backend stopped")
		case ipn.InUseOtherUser:
			return status, errors.New("tailscale: backend is in use by another user")
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Status reads the current node state.
func (b *tsnetBackend) Status(ctx context.Context) (backendStatus, error) {
	client, err := b.server.LocalClient()
	if err != nil {
		return backendStatus{}, err
	}
	return statusWithoutPeers(ctx, client)
}

// Dial opens a TCP connection through the node.
func (b *tsnetBackend) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return b.server.Dial(ctx, network, address)
}

// Close stops the node exactly once. tsnet's Close must not be called twice,
// so the once guard also makes the Provider's idempotent Close safe.
func (b *tsnetBackend) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.server.Close()
	})
	return b.closeErr
}

func statusWithoutPeers(ctx context.Context, client *local.Client) (backendStatus, error) {
	status, err := client.StatusWithoutPeers(ctx)
	if err != nil {
		return backendStatus{}, err
	}
	return backendStatusFrom(status), nil
}

// backendStatusFrom maps the tsnet status onto this package's non-secret
// vocabulary. Self.DNSName ends with a dot; the UI shows the name without it.
func backendStatusFrom(status *ipnstate.Status) backendStatus {
	out := backendStatus{
		State:   status.BackendState,
		AuthURL: status.AuthURL,
	}
	for _, address := range status.TailscaleIPs {
		out.TailscaleIPs = append(out.TailscaleIPs, address.String())
	}
	if status.Self != nil {
		out.DNSName = strings.TrimSuffix(status.Self.DNSName, ".")
		if out.DNSName == "" {
			out.DNSName = strings.TrimSuffix(status.Self.HostName, ".")
		}
	}
	state, _ := ipn.StateFromString(status.BackendState)
	switch state {
	case ipn.Running:
		out.Phase = appconnection.ProviderPhaseRunning
	case ipn.NeedsLogin:
		out.Phase = appconnection.ProviderPhaseNeedsLogin
	case ipn.NeedsMachineAuth:
		out.Phase = appconnection.ProviderPhaseError
	case ipn.Starting, ipn.NoState:
		out.Phase = appconnection.ProviderPhaseStarting
	case ipn.Stopped:
		out.Phase = appconnection.ProviderPhaseStopped
	default:
		out.Phase = appconnection.ProviderPhaseError
	}
	return out
}
