// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"net"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type lifecycleProvider struct {
	closed int
}

type lifecycleInvalidator struct {
	profiles []string
}

func (i *lifecycleInvalidator) InvalidateNetworkProfile(profileID string) {
	i.profiles = append(i.profiles, profileID)
}

func (p *lifecycleProvider) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("lifecycle provider does not dial in this test")
}

func (p *lifecycleProvider) Close() error {
	p.closed++
	return nil
}

func TestNetworkProfileRuntimeChangedClosesTheProvider(t *testing.T) {
	provider := &lifecycleProvider{}
	registry := appconnection.NewNetworkProviderRegistry(
		networkProviderFactoryFunc(func(context.Context, appconnection.NetworkRoute) (appconnection.NetworkProvider, error) {
			return provider, nil
		}))
	t.Cleanup(func() { _ = registry.CloseAll() })
	route := appconnection.NetworkRoute{ProfileID: "net-1", Kind: "tailscale"}
	if _, err := registry.Acquire(context.Background(), route); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	invalidator := &lifecycleInvalidator{}
	runtime := &networkProfileRuntime{providers: registry, connections: invalidator}
	runtime.InvalidateProfile("net-1")
	if provider.closed != 1 {
		t.Fatalf("provider closes = %d, want exactly 1", provider.closed)
	}
	if len(invalidator.profiles) != 1 || invalidator.profiles[0] != "net-1" {
		t.Fatalf("invalidated profiles = %v, want net-1", invalidator.profiles)
	}
	// A repeated notification for the already-retired profile is harmless; it
	// still drops any idle transport that was rebuilt in between.
	runtime.InvalidateProfile("net-1")
	if provider.closed != 1 || len(invalidator.profiles) != 2 {
		t.Fatalf("repeated change: closes=%d invalidations=%v", provider.closed, invalidator.profiles)
	}
	runtime.InvalidateProfile("")
	var nilRuntime *networkProfileRuntime
	nilRuntime.InvalidateProfile("net-1")
	nilRuntime.PhaseChanged("net-1", appconnection.ProviderPhaseError)
	if len(invalidator.profiles) != 2 {
		t.Fatalf("an empty or nil notification invalidated %v", invalidator.profiles)
	}
}

// TestNetworkProfileRuntimePhaseChangedDropsIdleConnectionsForTerminalPhases
// proves an authorization loss or a dead Provider invalidates parked
// transports immediately, while transient phases leave them alone.
func TestNetworkProfileRuntimePhaseChangedDropsIdleConnectionsForTerminalPhases(t *testing.T) {
	invalidator := &lifecycleInvalidator{}
	runtime := &networkProfileRuntime{connections: invalidator}

	for _, phase := range []appconnection.ProviderPhase{
		appconnection.ProviderPhaseRunning,
		appconnection.ProviderPhaseStarting,
		appconnection.ProviderPhaseStopping,
		"",
	} {
		runtime.PhaseChanged("net-1", phase)
	}
	if len(invalidator.profiles) != 0 {
		t.Fatalf("transient phases invalidated %v", invalidator.profiles)
	}

	runtime.PhaseChanged("net-1", appconnection.ProviderPhaseNeedsLogin)
	runtime.PhaseChanged("net-2", appconnection.ProviderPhaseError)
	runtime.PhaseChanged("net-3", appconnection.ProviderPhaseStopped)
	runtime.PhaseChanged("", appconnection.ProviderPhaseError)
	want := []string{"net-1", "net-2", "net-3"}
	if len(invalidator.profiles) != len(want) {
		t.Fatalf("invalidated profiles = %v, want %v", invalidator.profiles, want)
	}
	for i := range want {
		if invalidator.profiles[i] != want[i] {
			t.Fatalf("invalidated profiles = %v, want %v", invalidator.profiles, want)
		}
	}
}

type networkProviderFactoryFunc func(context.Context, appconnection.NetworkRoute) (appconnection.NetworkProvider, error)

func (f networkProviderFactoryFunc) Open(ctx context.Context,
	route appconnection.NetworkRoute) (appconnection.NetworkProvider, error) {
	return f(ctx, route)
}

type lifecycleSessions struct {
	descriptions []appsession.SessionDescription
}

func (s lifecycleSessions) DescribeAll() []appsession.SessionDescription {
	return s.descriptions
}

type lifecycleRoutes struct {
	profiles map[model.HostID]string
	err      error
}

func (r *lifecycleRoutes) NetworkProfileIDForHost(_ context.Context, id model.HostID) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.profiles[id], nil
}

type lifecycleLeases map[string]int

func (l lifecycleLeases) ActiveNetworkProfileLeases(profileID string) int { return l[profileID] }

func TestNetworkProfileRuntimeInUseChecksLeasesAndSessionRoutes(t *testing.T) {
	sessions := lifecycleSessions{descriptions: []appsession.SessionDescription{
		{ID: "ssh-1", HostID: "host-ssh", Kind: appsession.KindSSH},
		{ID: "telnet-1", HostID: "host-telnet", Kind: appsession.KindTelnet},
		{ID: "local-1", Kind: appsession.KindLocal},
		{ID: "serial-1", HostID: "host-serial", Kind: appsession.KindSerial},
	}}
	routes := &lifecycleRoutes{profiles: map[model.HostID]string{
		"host-ssh":    "net-1",
		"host-telnet": "net-telnet",
		"host-serial": "net-1",
	}}
	leases := lifecycleLeases{"net-lease": 2}

	if !networkProfileRuntimeInUse(context.Background(), "net-lease", sessions, routes, leases) {
		t.Fatal("an active lease did not count as in use")
	}
	if !networkProfileRuntimeInUse(context.Background(), "net-1", sessions, routes, leases) {
		t.Fatal("an SSH session route did not count as in use")
	}
	if !networkProfileRuntimeInUse(context.Background(), "net-telnet", sessions, routes, leases) {
		t.Fatal("a Telnet session route did not count as in use")
	}
	if networkProfileRuntimeInUse(context.Background(), "net-other", sessions, routes, leases) {
		t.Fatal("an unused profile was reported in use")
	}
	if networkProfileRuntimeInUse(context.Background(), "", sessions, routes, leases) {
		t.Fatal("an empty profile ID was reported in use")
	}

	// A local Session has no Host route. A serial Session's Host reference does
	// not use a Provider even though the record carries a profile.
	if networkProfileRuntimeInUse(context.Background(), "net-1",
		lifecycleSessions{descriptions: []appsession.SessionDescription{
			{ID: "local-1", Kind: appsession.KindLocal},
			{ID: "serial-1", HostID: "host-serial", Kind: appsession.KindSerial},
		}}, routes, nil) {
		t.Fatal("a non-network Session counted as Provider use")
	}

	// An unavailable optional reader fails soft: the warning is advisory.
	if networkProfileRuntimeInUse(context.Background(), "net-1", nil, routes, nil) {
		t.Fatal("a nil Session reader reported use")
	}
	if networkProfileRuntimeInUse(context.Background(), "net-1", sessions,
		&lifecycleRoutes{err: errors.New("vault locked")}, nil) {
		t.Fatal("a route lookup failure reported use")
	}
}

func TestNetworkProfileIDForHostFollowsTheFirstJumpWithoutReadingSecrets(t *testing.T) {
	vault := &fakeRouteVault{hosts: map[model.HostID]domainvault.Host{
		"target": {
			ID: "target", NetworkProfileID: "net-target",
			HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"jump-1"}},
		},
		"jump-1":     {ID: "jump-1", NetworkProfileID: "net-first"},
		"plain":      {ID: "plain", NetworkProfileID: "net-plain"},
		"jump-plain": {ID: "jump-plain"},
		"target-2": {
			ID: "target-2", NetworkProfileID: "net-target-2",
			HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"jump-plain"}},
		},
		"broken": {
			ID: "broken", NetworkProfileID: "net-broken",
			HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"gone"}},
		},
	}}
	routes := vaultRoutes{vault: vault}

	for id, want := range map[model.HostID]string{
		"target":   "net-first",
		"plain":    "net-plain",
		"target-2": "net-target-2",
	} {
		got, err := routes.NetworkProfileIDForHost(context.Background(), id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got != want {
			t.Fatalf("%s profile = %q, want %q", id, got, want)
		}
	}
	if _, err := routes.NetworkProfileIDForHost(context.Background(), "missing"); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("missing Host error = %v, want ErrNotFound", err)
	}
	if _, err := routes.NetworkProfileIDForHost(context.Background(), "broken"); err == nil {
		t.Fatal("a missing jump Host was ignored")
	}
	if _, err := (vaultRoutes{}).NetworkProfileIDForHost(context.Background(), "plain"); err == nil {
		t.Fatal("a route source without a Vault resolved a profile")
	}
}
