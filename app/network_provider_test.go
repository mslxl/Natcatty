package app

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptertailscale "github.com/mslxl/natcatty/adapters/tailscale"
	adapterwireguard "github.com/mslxl/natcatty/adapters/wireguard"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// fakeNetworkVault scripts the Vault reads the factory performs.
type fakeNetworkVault struct {
	profile    domainvault.NetworkProfile
	profileErr error
	secret     string
	secretErr  error

	peerSecret    string
	peerSecretErr error
	peerLookups   int
	peerIDs       []string
	peerProfiles  []string

	profileIDs    []string
	secretIDs     []string
	secretFields  []string
	secretLookups int
}

func (v *fakeNetworkVault) NetworkProfile(_ context.Context, id string) (domainvault.NetworkProfile, error) {
	v.profileIDs = append(v.profileIDs, id)
	if v.profileErr != nil {
		return domainvault.NetworkProfile{}, v.profileErr
	}
	return v.profile, nil
}

func (v *fakeNetworkVault) NetworkProfileSecret(_ context.Context, id, field string) (string, error) {
	v.secretLookups++
	v.secretIDs = append(v.secretIDs, id)
	v.secretFields = append(v.secretFields, field)
	if v.secretErr != nil {
		return "", v.secretErr
	}
	return v.secret, nil
}

func (v *fakeNetworkVault) NetworkPeerPresharedKey(_ context.Context, profileID, peerID string) (string, error) {
	v.peerLookups++
	v.peerProfiles = append(v.peerProfiles, profileID)
	v.peerIDs = append(v.peerIDs, peerID)
	if v.peerSecretErr != nil {
		return "", v.peerSecretErr
	}
	return v.peerSecret, nil
}

// stubProvider stands in for the adapter so the factory can be tested without
// constructing a tsnet node.
type stubProvider struct {
	dials int
}

func (p *stubProvider) DialContext(context.Context, string, string) (net.Conn, error) {
	p.dials++
	return nil, errors.New("stub provider does not dial")
}

func tailscaleTestProfile() domainvault.NetworkProfile {
	return domainvault.NetworkProfile{
		ID:    "net-1",
		Label: "Office",
		Kind:  domainvault.NetworkKindTailscale,
		Tailscale: &domainvault.TailscaleNetwork{
			Hostname:   "natcatty-office",
			ControlURL: "https://headscale.example.com",
			Ephemeral:  true,
			Tags:       []string{"tag:prod"},
			HasAuthKey: true,
		},
	}
}

func TestFactoryMapsTheTailscaleProfileAndSecret(t *testing.T) {
	vault := &fakeNetworkVault{profile: tailscaleTestProfile(), secret: "tskey-auth-example"}
	base := filepath.Join(t.TempDir(), "network-providers")
	var cfg adaptertailscale.Config
	var optionCount int
	provider := &stubProvider{}
	factory := &networkProviderFactory{
		vault:     vault,
		stateBase: base,
		observe:   func(adaptertailscale.Status) {},
		logf:      func(string, ...any) {},
		openTailscale: func(_ context.Context, got adaptertailscale.Config,
			options ...adaptertailscale.Option) (appconnection.NetworkProvider, error) {
			cfg = got
			optionCount = len(options)
			return provider, nil
		},
	}

	opened, err := factory.Open(context.Background(), appconnection.NetworkRoute{
		ProfileID: "net-1", Kind: string(domainvault.NetworkKindTailscale), Fingerprint: "fp",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != provider {
		t.Fatalf("Open returned %T, want the adapter provider", opened)
	}
	if cfg.ProfileID != "net-1" || cfg.Label != "Office" ||
		cfg.Kind != domainvault.NetworkKindTailscale {
		t.Fatalf("Config identity = %+v", cfg)
	}
	if cfg.StateBaseDir != base {
		t.Fatalf("StateBaseDir = %q, want %q", cfg.StateBaseDir, base)
	}
	if cfg.Hostname != "natcatty-office" || cfg.ControlURL != "https://headscale.example.com" ||
		!cfg.Ephemeral || len(cfg.AdvertiseTags) != 1 || cfg.AdvertiseTags[0] != "tag:prod" {
		t.Fatalf("Config tailnet fields = %+v", cfg)
	}
	if cfg.AuthKey != "tskey-auth-example" {
		t.Fatal("the Vault Auth Key did not reach the adapter configuration")
	}
	if optionCount != 2 {
		t.Fatalf("adapter options = %d, want status observer and logger", optionCount)
	}
	if len(vault.secretFields) != 1 || vault.secretFields[0] != appvault.FieldNetworkAuthKey {
		t.Fatalf("secret fields = %v", vault.secretFields)
	}
}

func TestFactoryUsesInteractiveLoginWhenTheSecretIsAbsent(t *testing.T) {
	vault := &fakeNetworkVault{
		profile:   tailscaleTestProfile(),
		secretErr: appvault.ErrNotFound,
	}
	var authKey string
	opened := false
	factory := &networkProviderFactory{
		vault: vault,
		openTailscale: func(_ context.Context, got adaptertailscale.Config,
			_ ...adaptertailscale.Option) (appconnection.NetworkProvider, error) {
			opened = true
			authKey = got.AuthKey
			return &stubProvider{}, nil
		},
	}
	if _, err := factory.Open(context.Background(), appconnection.NetworkRoute{
		ProfileID: "net-1", Kind: string(domainvault.NetworkKindTailscale),
	}); err != nil {
		t.Fatalf("Open without a stored key: %v", err)
	}
	if !opened {
		t.Fatal("the adapter did not open for the interactive-login path")
	}
	if authKey != "" {
		t.Fatalf("AuthKey = %q, want the interactive-login path", authKey)
	}
}

func TestFactoryFailsClosedOnVaultProblems(t *testing.T) {
	tests := []struct {
		name  string
		vault *fakeNetworkVault
		kind  appconnection.Kind
	}{
		{
			name:  "missing profile",
			vault: &fakeNetworkVault{profileErr: appvault.ErrNotFound},
			kind:  appconnection.KindProvider,
		},
		{
			name: "locked secret",
			vault: &fakeNetworkVault{
				profile:   tailscaleTestProfile(),
				secretErr: errors.New("the field secret store is locked"),
			},
			kind: appconnection.KindProvider,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			openCalls := 0
			factory := &networkProviderFactory{
				vault: test.vault,
				openTailscale: func(context.Context, adaptertailscale.Config,
					...adaptertailscale.Option) (appconnection.NetworkProvider, error) {
					openCalls++
					return &stubProvider{}, nil
				},
			}
			_, err := factory.Open(context.Background(), appconnection.NetworkRoute{
				ProfileID: "net-1", Kind: string(domainvault.NetworkKindTailscale),
			})
			var typed *appconnection.Error
			if !errors.As(err, &typed) {
				t.Fatalf("error %v is not typed", err)
			}
			if typed.Kind != test.kind {
				t.Fatalf("Kind = %q, want %q", typed.Kind, test.kind)
			}
			if typed.Provider == nil || typed.Provider.ProfileID != "net-1" ||
				typed.Provider.Kind != string(domainvault.NetworkKindTailscale) {
				t.Fatalf("ProviderInfo = %+v", typed.Provider)
			}
			if openCalls != 0 {
				t.Fatalf("adapter opened %d times after a Vault failure", openCalls)
			}
		})
	}
}

func TestFactoryRejectsAProfileOfAnotherKind(t *testing.T) {
	profile := tailscaleTestProfile()
	profile.Kind = domainvault.NetworkKindWireGuard
	factory := &networkProviderFactory{vault: &fakeNetworkVault{profile: profile}}
	_, err := factory.Open(context.Background(), appconnection.NetworkRoute{
		ProfileID: "net-1", Kind: string(domainvault.NetworkKindTailscale),
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("error = %v, want KindProvider", err)
	}
}

func wireguardTestProfile() domainvault.NetworkProfile {
	return domainvault.NetworkProfile{
		ID:    "wg-1",
		Label: "Office WG",
		Kind:  domainvault.NetworkKindWireGuard,
		WireGuard: &domainvault.WireGuardNetwork{
			Addresses:     []string{"10.0.0.2/32", "fd00::2/128"},
			DNS:           []string{"10.0.0.1"},
			MTU:           1280,
			ListenPort:    51820,
			HasPrivateKey: true,
			Peers: []domainvault.WireGuardPeer{
				{
					ID:                  "peer-1",
					PublicKey:           "peer-public-1",
					Endpoint:            "vpn.example.com:51820",
					AllowedIPs:          []string{"10.0.0.0/24"},
					PersistentKeepalive: 25,
					HasPresharedKey:     true,
				},
				{
					ID:         "peer-2",
					PublicKey:  "peer-public-2",
					AllowedIPs: []string{"10.1.0.0/24"},
				},
			},
		},
	}
}

func TestFactoryMapsTheWireGuardProfileAndSecrets(t *testing.T) {
	vault := &fakeNetworkVault{
		profile:    wireguardTestProfile(),
		secret:     "private-key-plaintext",
		peerSecret: "preshared-key-plaintext",
	}
	var cfg adapterwireguard.Config
	var optionCount int
	provider := &stubProvider{}
	factory := &networkProviderFactory{
		vault:            vault,
		observeWireGuard: func(adapterwireguard.Status) {},
		logf:             func(string, ...any) {},
		openWireGuard: func(_ context.Context, got adapterwireguard.Config,
			options ...adapterwireguard.Option) (appconnection.NetworkProvider, error) {
			cfg = got
			optionCount = len(options)
			return provider, nil
		},
	}

	opened, err := factory.Open(context.Background(), appconnection.NetworkRoute{
		ProfileID: "wg-1", Kind: string(domainvault.NetworkKindWireGuard), Fingerprint: "fp",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != provider {
		t.Fatalf("Open returned %T, want the adapter provider", opened)
	}
	if cfg.ProfileID != "wg-1" || cfg.Label != "Office WG" ||
		cfg.Kind != domainvault.NetworkKindWireGuard {
		t.Fatalf("Config identity = %+v", cfg)
	}
	if len(cfg.Addresses) != 2 || cfg.Addresses[0] != "10.0.0.2/32" ||
		len(cfg.DNS) != 1 || cfg.DNS[0] != "10.0.0.1" ||
		cfg.MTU != 1280 || cfg.ListenPort != 51820 {
		t.Fatalf("Config network fields = %+v", cfg)
	}
	if cfg.PrivateKey != "private-key-plaintext" {
		t.Fatal("the Vault Private Key did not reach the adapter configuration")
	}
	if len(cfg.Peers) != 2 {
		t.Fatalf("peers = %d, want 2", len(cfg.Peers))
	}
	first := cfg.Peers[0]
	if first.ID != "peer-1" || first.PublicKey != "peer-public-1" ||
		first.PresharedKey != "preshared-key-plaintext" ||
		first.Endpoint != "vpn.example.com:51820" ||
		len(first.AllowedIPs) != 1 || first.PersistentKeepalive != 25 {
		t.Fatalf("peer-1 = %+v", first)
	}
	if cfg.Peers[1].PresharedKey != "" {
		t.Fatal("a peer without a saved Preshared Key received one")
	}
	if vault.peerLookups != 1 || vault.peerIDs[0] != "peer-1" ||
		vault.peerProfiles[0] != "wg-1" {
		t.Fatalf("peer secret reads = %v (profiles %v), want only peer-1",
			vault.peerIDs, vault.peerProfiles)
	}
	if len(vault.secretFields) != 1 || vault.secretFields[0] != appvault.FieldNetworkPrivateKey {
		t.Fatalf("profile secret fields = %v", vault.secretFields)
	}
	if optionCount != 2 {
		t.Fatalf("adapter options = %d, want status observer and logger", optionCount)
	}
}

func TestFactoryWireGuardFailsClosedOnVaultProblems(t *testing.T) {
	tests := []struct {
		name  string
		vault *fakeNetworkVault
	}{
		{
			name:  "missing private key",
			vault: &fakeNetworkVault{profile: wireguardTestProfile(), secretErr: appvault.ErrNotFound},
		},
		{
			name: "locked private key",
			vault: &fakeNetworkVault{
				profile:   wireguardTestProfile(),
				secretErr: errors.New("the field secret store is locked"),
			},
		},
		{
			name: "missing peer preshared key",
			vault: &fakeNetworkVault{
				profile:       wireguardTestProfile(),
				secret:        "private-key-plaintext",
				peerSecretErr: appvault.ErrNotFound,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			openCalls := 0
			factory := &networkProviderFactory{
				vault: test.vault,
				openWireGuard: func(context.Context, adapterwireguard.Config,
					...adapterwireguard.Option) (appconnection.NetworkProvider, error) {
					openCalls++
					return &stubProvider{}, nil
				},
			}
			_, err := factory.Open(context.Background(), appconnection.NetworkRoute{
				ProfileID: "wg-1", Kind: string(domainvault.NetworkKindWireGuard),
			})
			var typed *appconnection.Error
			if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
				t.Fatalf("error = %v, want KindProvider", err)
			}
			if typed.Provider == nil || typed.Provider.ProfileID != "wg-1" ||
				typed.Provider.Kind != string(domainvault.NetworkKindWireGuard) ||
				typed.Provider.Phase != appconnection.ProviderPhaseError {
				t.Fatalf("ProviderInfo = %+v", typed.Provider)
			}
			if openCalls != 0 {
				t.Fatalf("adapter opened %d times after a Vault failure", openCalls)
			}
		})
	}
}

func TestFactoryWireGuardRejectsAProfileOfAnotherKind(t *testing.T) {
	profile := wireguardTestProfile()
	profile.Kind = domainvault.NetworkKindTailscale
	factory := &networkProviderFactory{
		vault: &fakeNetworkVault{profile: profile, secret: "must-not-be-used"},
	}
	_, err := factory.Open(context.Background(), appconnection.NetworkRoute{
		ProfileID: "wg-1", Kind: string(domainvault.NetworkKindWireGuard),
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("error = %v, want KindProvider", err)
	}
}

func TestFactoryFailsClosedForUnknownKinds(t *testing.T) {
	factory := &networkProviderFactory{
		vault: &fakeNetworkVault{profile: tailscaleTestProfile(), secret: "must-not-be-read"},
	}
	_, err := factory.Open(context.Background(), appconnection.NetworkRoute{
		ProfileID: "x-1", Kind: "someday-net",
	})
	if !errors.Is(err, appconnection.ErrNetworkProviderKind) {
		t.Fatalf("unknown-kind error = %v, want ErrNetworkProviderKind", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("unknown-kind error = %v, want KindProvider", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "x-1" {
		t.Fatalf("ProviderInfo = %+v", typed.Provider)
	}

	_, err = factory.Open(context.Background(), appconnection.NetworkRoute{})
	if !errors.As(err, &typed) || typed.Provider == nil || typed.Provider.ProfileID != "" {
		t.Fatalf("empty-route error = %v", err)
	}
}

func TestFactoryPreservesAdapterKindAndPhaseOnStartFailure(t *testing.T) {
	factory := &networkProviderFactory{
		vault: &fakeNetworkVault{profile: tailscaleTestProfile(), secret: "tskey-auth-example"},
		openTailscale: func(context.Context, adaptertailscale.Config,
			...adaptertailscale.Option) (appconnection.NetworkProvider, error) {
			return nil, &appconnection.Error{
				Kind:  appconnection.KindCancelled,
				Cause: context.Canceled,
				Provider: &appconnection.ProviderInfo{
					Phase: appconnection.ProviderPhaseNeedsLogin,
				},
			}
		},
	}
	_, err := factory.Open(context.Background(), appconnection.NetworkRoute{
		ProfileID: "net-1", Kind: string(domainvault.NetworkKindTailscale),
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not typed", err)
	}
	if typed.Kind != appconnection.KindCancelled {
		t.Fatalf("Kind = %q, want cancelled", typed.Kind)
	}
	if typed.Provider == nil || typed.Provider.Phase != appconnection.ProviderPhaseNeedsLogin ||
		typed.Provider.ProfileID != "net-1" || typed.Provider.Label != "Office" {
		t.Fatalf("ProviderInfo = %+v", typed.Provider)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v does not preserve the cause", err)
	}
}

func TestStatusSinkPublishesToTheVaultSurface(t *testing.T) {
	state := vaultstate.NewState(nil)
	sink := &networkProviderStatusSink{vault: func() *vaultstate.State { return state }}
	sink.Observe(adaptertailscale.Status{
		ProfileID:    "net-1",
		Label:        "Office",
		Kind:         string(domainvault.NetworkKindTailscale),
		Phase:        appconnection.ProviderPhaseRunning,
		TailscaleIPs: []string{"100.64.0.2"},
		DNSName:      "natcatty-office.tailnet.ts.net",
	})
	state.FlushNetworkProviderStatus()
	status, ok := state.NetworkStatus("net-1")
	if !ok {
		t.Fatal("the Vault surface did not receive the provider status")
	}
	if status.Phase != appconnection.ProviderPhaseRunning ||
		status.Address() != "natcatty-office.tailnet.ts.net" ||
		status.Label != "Office" || len(status.TailscaleIPs) != 1 {
		t.Fatalf("status = %+v", status)
	}

	// A nil vault resolver and a nil history service must both be harmless:
	// the sink runs on the provider's dial goroutine during shutdown too.
	(&networkProviderStatusSink{}).Observe(adaptertailscale.Status{ProfileID: "net-1"})
}

func TestConnectionLogFromProviderStatus(t *testing.T) {
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	base := adaptertailscale.Status{
		ProfileID:    "net-1",
		Label:        "Office",
		Kind:         string(domainvault.NetworkKindTailscale),
		TailscaleIPs: []string{"100.64.0.2"},
		DNSName:      "natcatty-office.tailnet.ts.net",
		AuthURL:      "https://login.tailscale.com/a/example",
	}

	for _, phase := range []appconnection.ProviderPhase{
		appconnection.ProviderPhaseStarting, appconnection.ProviderPhaseStopping,
	} {
		status := base
		status.Phase = phase
		if _, ok := connectionLogFromProviderStatus(status, at); ok {
			t.Fatalf("phase %q was recorded; transient progress should not be", phase)
		}
	}

	for _, test := range []struct {
		phase    appconnection.ProviderPhase
		status   domainhistory.ConnectionStatus
		withAddr bool
	}{
		{appconnection.ProviderPhaseRunning, domainhistory.ConnectionConnected, true},
		{appconnection.ProviderPhaseNeedsLogin, domainhistory.ConnectionFailed, true},
		{appconnection.ProviderPhaseError, domainhistory.ConnectionFailed, true},
		{appconnection.ProviderPhaseStopped, domainhistory.ConnectionFailed, true},
	} {
		status := base
		status.Phase = test.phase
		status.Err = errors.New("tailscale: machine authorization required")
		log, ok := connectionLogFromProviderStatus(status, at)
		if !ok {
			t.Fatalf("phase %q was not recorded", test.phase)
		}
		if log.Status != test.status {
			t.Fatalf("phase %q status = %q, want %q", test.phase, log.Status, test.status)
		}
		if !log.IsProvider() || log.IsLocal() || log.IsUnsaved() {
			t.Fatalf("phase %q row was misclassified: %+v", test.phase, log)
		}
		if log.NetworkProfileID != "net-1" || log.NetworkProfileLabel != "Office" ||
			log.NetworkProviderPhase != string(test.phase) ||
			log.NetworkProviderKind != string(domainvault.NetworkKindTailscale) {
			t.Fatalf("phase %q metadata = %+v", test.phase, log)
		}
		if log.HostLabel != "Office" || log.Kind != string(domainvault.NetworkKindTailscale) {
			t.Fatalf("phase %q display fields = %+v", test.phase, log)
		}
		if log.NetworkAddress != "natcatty-office.tailnet.ts.net" {
			t.Fatalf("phase %q address = %q", test.phase, log.NetworkAddress)
		}
		if log.StartedAt != at || log.EndedAt != at {
			t.Fatalf("phase %q timestamps = %v/%v", test.phase, log.StartedAt, log.EndedAt)
		}
		// The Auth URL is for the login flow, never for the persisted log.
		for _, value := range []string{log.HostLabel, log.Hostname, log.Error,
			log.NetworkAddress, log.NetworkProfileLabel} {
			if strings.Contains(value, base.AuthURL) {
				t.Fatalf("phase %q row leaked the Auth URL: %+v", test.phase, log)
			}
		}
	}

	noAddress := base
	noAddress.Phase = appconnection.ProviderPhaseRunning
	noAddress.DNSName = ""
	log, ok := connectionLogFromProviderStatus(noAddress, at)
	if !ok || log.NetworkAddress != "100.64.0.2" {
		t.Fatalf("address fallback = %q, want the first tailnet IP", log.NetworkAddress)
	}
}

func TestConnectionLogProviderRowsAreNormalizedAsProviderRows(t *testing.T) {
	log := domainhistory.NormalizeConnectionLog(domainhistory.ConnectionLog{
		Kind:                 string(domainvault.NetworkKindTailscale),
		Status:               domainhistory.ConnectionConnected,
		StartedAt:            time.Now(),
		NetworkProfileID:     " net-1 ",
		NetworkProfileLabel:  " Office ",
		NetworkProviderKind:  " tailscale ",
		NetworkProviderPhase: " running ",
		NetworkAddress:       " natcatty-office.tailnet.ts.net ",
	})
	if log.HostLabel != "Office" || log.NetworkProfileLabel != "Office" {
		t.Fatalf("provider labels = %q/%q", log.HostLabel, log.NetworkProfileLabel)
	}
	if log.UnsavedHost || log.IsUnsaved() || log.IsLocal() {
		t.Fatalf("provider row was classified as a Host attempt: %+v", log)
	}
	if log.NetworkProviderPhase != "running" || log.NetworkAddress != "natcatty-office.tailnet.ts.net" {
		t.Fatalf("provider metadata was not trimmed: %+v", log)
	}
}

func TestWireGuardStatusSinkPublishesLiveDetail(t *testing.T) {
	state := vaultstate.NewState(nil)
	sink := &networkProviderStatusSink{vault: func() *vaultstate.State { return state }}
	handshake := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	sink.ObserveWireGuard(adapterwireguard.Status{
		ProfileID:     "wg-1",
		Label:         "Office WG",
		Kind:          string(domainvault.NetworkKindWireGuard),
		Phase:         appconnection.ProviderPhaseRunning,
		Handshake:     adapterwireguard.HandshakeEstablished,
		LastHandshake: handshake,
		RxBytes:       1536,
		TxBytes:       512,
		Peers: []adapterwireguard.PeerStatus{{
			ID: "peer-1", Endpoint: "203.0.113.7:51820",
		}},
	})
	state.FlushNetworkProviderStatus()
	status, ok := state.NetworkStatus("wg-1")
	if !ok {
		t.Fatal("the Vault surface did not receive the WireGuard status")
	}
	if status.Kind != string(domainvault.NetworkKindWireGuard) ||
		status.Phase != appconnection.ProviderPhaseRunning {
		t.Fatalf("status identity = %+v", status)
	}
	if status.Address() != "203.0.113.7:51820" {
		t.Fatalf("address = %q, want the current WireGuard peer endpoint", status.Address())
	}
	if !status.HandshakeEstablished || !status.LastHandshake.Equal(handshake) {
		t.Fatalf("handshake fields = %+v", status)
	}
	if status.RxBytes != 1536 || status.TxBytes != 512 {
		t.Fatalf("counter fields = %+v", status)
	}

	// A nil vault and nil history are harmless during shutdown.
	(&networkProviderStatusSink{}).ObserveWireGuard(adapterwireguard.Status{ProfileID: "wg-1"})
}

func TestWireGuardLogStateIgnoresCounterOnlyPolls(t *testing.T) {
	sink := &networkProviderStatusSink{}
	status := adapterwireguard.Status{
		ProfileID: "wg-1",
		Phase:     appconnection.ProviderPhaseRunning,
		Handshake: adapterwireguard.HandshakePending,
		RxBytes:   1,
	}
	if !sink.shouldRecordWireGuard(status) {
		t.Fatal("the first running status was not recorded")
	}
	status.RxBytes = 99
	status.TxBytes = 42
	if sink.shouldRecordWireGuard(status) {
		t.Fatal("a counter-only poll was recorded as a new event")
	}
	status.Handshake = adapterwireguard.HandshakeEstablished
	if !sink.shouldRecordWireGuard(status) {
		t.Fatal("the first completed handshake was not recorded")
	}
	status.RxBytes = 1000
	status.TxBytes = 900
	if sink.shouldRecordWireGuard(status) {
		t.Fatal("a counter update after the handshake was recorded")
	}
	status.Phase = appconnection.ProviderPhaseError
	status.Err = errors.New("tunnel died")
	if !sink.shouldRecordWireGuard(status) {
		t.Fatal("the error phase was not recorded")
	}
	status.Phase = appconnection.ProviderPhaseStarting
	if sink.shouldRecordWireGuard(status) {
		t.Fatal("a transient phase was recorded")
	}
	if sink.shouldRecordWireGuard(adapterwireguard.Status{}) {
		t.Fatal("a status without a profile was recorded")
	}
}

func TestConnectionLogFromWireGuardStatus(t *testing.T) {
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	handshake := at.Add(-30 * time.Second)
	base := adapterwireguard.Status{
		ProfileID:     "wg-1",
		Label:         "Office WG",
		Kind:          string(domainvault.NetworkKindWireGuard),
		Handshake:     adapterwireguard.HandshakeEstablished,
		LastHandshake: handshake,
		RxBytes:       1536,
		TxBytes:       512,
		Peers: []adapterwireguard.PeerStatus{{
			ID: "peer-1", Endpoint: "203.0.113.7:51820",
		}},
	}

	for _, phase := range []appconnection.ProviderPhase{
		appconnection.ProviderPhaseStarting, appconnection.ProviderPhaseStopping,
	} {
		status := base
		status.Phase = phase
		if _, ok := connectionLogFromWireGuardStatus(status, at); ok {
			t.Fatalf("phase %q was recorded; transient progress should not be", phase)
		}
	}

	for _, test := range []struct {
		phase  appconnection.ProviderPhase
		status domainhistory.ConnectionStatus
	}{
		{appconnection.ProviderPhaseRunning, domainhistory.ConnectionConnected},
		{appconnection.ProviderPhaseError, domainhistory.ConnectionFailed},
		{appconnection.ProviderPhaseStopped, domainhistory.ConnectionFailed},
	} {
		status := base
		status.Phase = test.phase
		log, ok := connectionLogFromWireGuardStatus(status, at)
		if !ok {
			t.Fatalf("phase %q was not recorded", test.phase)
		}
		if log.Status != test.status {
			t.Fatalf("phase %q status = %q, want %q", test.phase, log.Status, test.status)
		}
		if !log.IsProvider() || log.IsLocal() || log.IsUnsaved() {
			t.Fatalf("phase %q row was misclassified: %+v", test.phase, log)
		}
		if log.NetworkProfileID != "wg-1" || log.NetworkProfileLabel != "Office WG" ||
			log.NetworkProviderKind != string(domainvault.NetworkKindWireGuard) ||
			log.NetworkProviderPhase != string(test.phase) {
			t.Fatalf("phase %q metadata = %+v", test.phase, log)
		}
		if log.NetworkAddress != "203.0.113.7:51820" ||
			!log.NetworkLastHandshake.Equal(handshake) ||
			log.NetworkRxBytes != 1536 || log.NetworkTxBytes != 512 {
			t.Fatalf("phase %q WireGuard detail = %+v", test.phase, log)
		}
		if log.StartedAt != at || log.EndedAt != at {
			t.Fatalf("phase %q timestamps = %v/%v", test.phase, log.StartedAt, log.EndedAt)
		}
	}

	// A status with no configured Endpoint still produces a valid row with an
	// empty address; it must not invent one.
	noEndpoint := base
	noEndpoint.Phase = appconnection.ProviderPhaseRunning
	noEndpoint.Peers = nil
	log, ok := connectionLogFromWireGuardStatus(noEndpoint, at)
	if !ok || log.NetworkAddress != "" {
		t.Fatalf("address fallback = %q, want empty", log.NetworkAddress)
	}
}

// fakeProviderRegistry scripts the two lifecycle calls the interactive-login
// controller performs. It records the call order so a test can prove that a
// retry retires the old attempt before opening a new one.
type fakeProviderRegistry struct {
	mu       sync.Mutex
	acquires int
	routes   []appconnection.NetworkRoute
	cancels  []string
	events   []string
	openFn   func(context.Context, appconnection.NetworkRoute, int) (appconnection.NetworkProvider, error)
}

func (r *fakeProviderRegistry) Acquire(ctx context.Context,
	route appconnection.NetworkRoute) (appconnection.NetworkProvider, error) {
	r.mu.Lock()
	r.acquires++
	index := r.acquires
	r.routes = append(r.routes, route)
	r.events = append(r.events, "acquire")
	fn := r.openFn
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, route, index)
	}
	return &stubProvider{}, nil
}

func (r *fakeProviderRegistry) Cancel(profileID string) error {
	r.mu.Lock()
	r.cancels = append(r.cancels, profileID)
	r.events = append(r.events, "cancel")
	r.mu.Unlock()
	return nil
}

func (r *fakeProviderRegistry) snapshot() (int, []appconnection.NetworkRoute, []string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acquires, append([]appconnection.NetworkRoute(nil), r.routes...),
		append([]string(nil), r.cancels...), append([]string(nil), r.events...)
}

func waitForAppSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestNetworkProviderControllerResolvesTheProfileRoute(t *testing.T) {
	profile := tailscaleTestProfile()
	vault := &fakeNetworkVault{profile: profile}
	registry := &fakeProviderRegistry{}
	controller := &networkProviderController{vault: vault, registry: registry}

	if err := controller.StartNetworkProvider(context.Background(), profile.ID); err != nil {
		t.Fatalf("StartNetworkProvider: %v", err)
	}
	acquires, routes, _, _ := registry.snapshot()
	if acquires != 1 || len(routes) != 1 {
		t.Fatalf("registry acquires = %d, want one", acquires)
	}
	route := routes[0]
	if route.ProfileID != profile.ID || route.Kind != string(profile.Kind) ||
		route.Label != profile.Label {
		t.Fatalf("route = %+v, want the profile identity", route)
	}
	if route.Fingerprint != domainvault.NetworkProfileFingerprint(profile) {
		t.Fatalf("route fingerprint = %q, want the profile fingerprint", route.Fingerprint)
	}
	if vault.secretLookups != 0 {
		t.Fatal("the login controller read the Auth Key; only the Provider factory may")
	}
	controller.mu.Lock()
	pending := len(controller.starts)
	controller.mu.Unlock()
	if pending != 0 {
		t.Fatalf("in-flight starts = %d, want the settled attempt removed", pending)
	}
}

func TestNetworkProviderControllerRepeatedStartCancelsThePreviousAttempt(t *testing.T) {
	profile := tailscaleTestProfile()
	firstStarted := make(chan struct{})
	firstCanceled := make(chan error, 1)
	registry := &fakeProviderRegistry{}
	registry.openFn = func(ctx context.Context, _ appconnection.NetworkRoute,
		index int) (appconnection.NetworkProvider, error) {
		if index == 1 {
			close(firstStarted)
			<-ctx.Done()
			firstCanceled <- ctx.Err()
			return nil, ctx.Err()
		}
		return &stubProvider{}, nil
	}
	controller := &networkProviderController{
		vault:    &fakeNetworkVault{profile: profile},
		registry: registry,
	}

	results := make(chan error, 2)
	go func() { results <- controller.StartNetworkProvider(context.Background(), profile.ID) }()
	waitForAppSignal(t, firstStarted, "the first provider open")
	go func() { results <- controller.StartNetworkProvider(context.Background(), profile.ID) }()

	select {
	case err := <-firstCanceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first attempt context error = %v, want cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a repeated Start did not cancel the older attempt")
	}

	var errs []error
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			errs = append(errs, err)
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for a start result")
		}
	}
	canceled, succeeded := 0, 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else if errors.Is(err, context.Canceled) {
			canceled++
		}
	}
	if canceled != 1 || succeeded != 1 {
		t.Fatalf("start results = %v, want one cancelled and one successful attempt", errs)
	}
	acquires, _, _, _ := registry.snapshot()
	if acquires != 2 {
		t.Fatalf("registry acquires = %d, want a fresh open after the cancelled attempt", acquires)
	}
}

func TestNetworkProviderControllerCancelStopsTheStartAndRegistryEntry(t *testing.T) {
	profile := tailscaleTestProfile()
	started := make(chan struct{})
	registry := &fakeProviderRegistry{}
	registry.openFn = func(ctx context.Context, _ appconnection.NetworkRoute,
		_ int) (appconnection.NetworkProvider, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	controller := &networkProviderController{
		vault:    &fakeNetworkVault{profile: profile},
		registry: registry,
	}

	startResult := make(chan error, 1)
	go func() { startResult <- controller.StartNetworkProvider(context.Background(), profile.ID) }()
	waitForAppSignal(t, started, "the provider open")

	if err := controller.CancelNetworkProvider(context.Background(), profile.ID); err != nil {
		t.Fatalf("CancelNetworkProvider: %v", err)
	}
	if err := <-startResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("start error = %v, want cancellation", err)
	}
	_, _, cancels, _ := registry.snapshot()
	if len(cancels) != 1 || cancels[0] != profile.ID {
		t.Fatalf("registry cancels = %v, want [%s]", cancels, profile.ID)
	}
}

func TestNetworkProviderControllerRetryCancelsThenStarts(t *testing.T) {
	profile := tailscaleTestProfile()
	started := make(chan struct{})
	registry := &fakeProviderRegistry{}
	registry.openFn = func(ctx context.Context, _ appconnection.NetworkRoute,
		index int) (appconnection.NetworkProvider, error) {
		if index == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &stubProvider{}, nil
	}
	controller := &networkProviderController{
		vault:    &fakeNetworkVault{profile: profile},
		registry: registry,
	}

	go func() { _ = controller.StartNetworkProvider(context.Background(), profile.ID) }()
	waitForAppSignal(t, started, "the first provider open")

	if err := controller.RetryNetworkProvider(context.Background(), profile.ID); err != nil {
		t.Fatalf("RetryNetworkProvider: %v", err)
	}
	_, routes, cancels, events := registry.snapshot()
	if len(cancels) != 1 || cancels[0] != profile.ID {
		t.Fatalf("registry cancels = %v, want the retried profile", cancels)
	}
	if len(events) != 3 || events[0] != "acquire" || events[1] != "cancel" || events[2] != "acquire" {
		t.Fatalf("lifecycle order = %v, want acquire, cancel, acquire", events)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %d, want a fresh route for the retry", len(routes))
	}
}

func TestNetworkProviderControllerFailsClosedWithoutAProfileOrRegistry(t *testing.T) {
	registry := &fakeProviderRegistry{}
	controller := &networkProviderController{
		vault:    &fakeNetworkVault{profileErr: appvault.ErrNotFound},
		registry: registry,
	}
	err := controller.StartNetworkProvider(context.Background(), "net-missing")
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("missing-profile error = %v, want KindProvider", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "net-missing" ||
		typed.Provider.Phase != appconnection.ProviderPhaseError {
		t.Fatalf("ProviderInfo = %+v, want the missing profile", typed.Provider)
	}
	if acquires, _, _, _ := registry.snapshot(); acquires != 0 {
		t.Fatalf("registry acquires = %d, want none without a profile", acquires)
	}

	unwired := &networkProviderController{vault: &fakeNetworkVault{profile: tailscaleTestProfile()}}
	err = unwired.StartNetworkProvider(context.Background(), "net-1")
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("unwired error = %v, want KindProvider", err)
	}
	if err := unwired.CancelNetworkProvider(context.Background(), "net-1"); err != nil {
		t.Fatalf("CancelNetworkProvider without a registry: %v", err)
	}
}
