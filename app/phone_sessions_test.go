package app

import (
	"context"
	"errors"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestPhoneRuntimeRefusesLocalAndSerialSessions proves the phone composition
// root registers no local PTY and no serial adapter: the Host->StartSpec
// mapping still names a Local shell for the desktop, but the phone's opener
// and transport registry fail closed, and a serial Session has no Host record
// that could reach the phone's list in the first place.
func TestPhoneRuntimeRefusesLocalAndSerialSessions(t *testing.T) {
	runtime := &phoneSessionRuntime{}

	local := domainvault.Host{
		ID: "local", Label: "desktop shell", Hostname: "localhost",
		Protocol: domainvault.ProtocolLocal,
	}
	if _, err := runtime.OpenHost(context.Background(), local); !errors.Is(err, errPhoneLocalSession) {
		t.Fatalf("OpenHost(local) error = %v, want errPhoneLocalSession", err)
	}
	// The mapping still names the local kind, so the refusal is the phone's,
	// not a domain change the desktop would inherit.
	if kind := appsession.HostSpec(local).Kind; kind != appsession.KindLocal {
		t.Fatalf("HostSpec(local).Kind = %q, want local", kind)
	}
	// A Local Host is not remote, so the phone's Hosts list never shows it.
	if local.IsRemote() {
		t.Fatal("a Local Host reports IsRemote, so the phone would list it")
	}

	factory := newPhoneTransportFactory(nil, vaultRoutes{}, nil)
	if _, err := factory.Open(context.Background(), appsession.HostSpec(local)); err == nil {
		t.Fatal("the phone transport registry opened a local Session")
	}
	// A serial Session is not a saved Host at all; it reaches the factory only
	// as a StartSpec, and the registry must still refuse it.
	if _, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSerial, Label: "serial console",
	}); err == nil {
		t.Fatal("the phone transport registry opened a serial Session")
	}
	// The opener's guard covers both kinds, including a serial spec HostSpec
	// cannot produce because a serial Session is not a saved Host.
	if !phoneRefusesKind(appsession.KindLocal) || !phoneRefusesKind(appsession.KindSerial) {
		t.Fatal("the phone opener does not refuse a local or serial Session")
	}
	for _, kind := range []appsession.Kind{
		appsession.KindSSH, appsession.KindTelnet, appsession.KindMosh, appsession.KindET,
	} {
		if phoneRefusesKind(kind) {
			t.Fatalf("the phone opener refuses the remote kind %q", kind)
		}
	}
}

// TestPhoneHostOpenUsesJumpsProxyAndNetwork proves a phone opens a Host
// through the same application stack the desktop does: the shared HostSpec,
// the Vault's route resolution, the Connection Manager, and the Network
// Provider registry all contribute to one dial.
func TestPhoneHostOpenUsesJumpsProxyAndNetwork(t *testing.T) {
	dialer := &factoryDialer{client: &factoryClient{terminal: &factoryTerminal{}}}
	manager := factoryManagerWithProviders(dialer)
	t.Cleanup(manager.CloseAll)

	vault := &fakeRouteVault{
		hosts: map[model.HostID]domainvault.Host{
			"target": {
				ID: "target", Label: "prod box", Hostname: "target.internal", Port: 22, Username: "deploy",
				Protocol:         domainvault.ProtocolSSH,
				HostChain:        &domainvault.HostChain{HostIDs: []model.HostID{"jump-1"}},
				Proxy:            &domainvault.ProxyConfig{Type: domainvault.ProxySOCKS5, Host: "proxy.internal", Port: 1080},
				NetworkProfileID: "net-1",
			},
			"jump-1": {
				ID: "jump-1", Label: "bastion", Hostname: "bastion.internal", Port: 2222, Username: "ops",
			},
		},
		networkProfiles: map[string]domainvault.NetworkProfile{
			"net-1": {ID: "net-1", Label: "office tailnet", Kind: domainvault.NetworkKindTailscale},
		},
		secrets: map[string]string{},
	}
	target := vault.hosts["target"]

	providers := appconnection.NewNetworkProviderRegistry(factoryNetworkFactory{})
	factory := newPhoneTransportFactory(manager, vaultRoutes{vault: vault}, providers)

	transport, err := factory.Open(context.Background(), appsession.HostSpec(target))
	if err != nil {
		t.Fatalf("open %q on the phone: %v", target.Label, err)
	}
	_ = transport.Close()

	request := dialer.request
	if len(request.Jumps) != 1 || request.Jumps[0].Hostname != "bastion.internal" ||
		request.Jumps[0].Port != 2222 || request.Jumps[0].Username != "ops" {
		t.Fatalf("jumps = %+v, want the Host chain's bastion", request.Jumps)
	}
	if request.Proxy == nil || request.Proxy.Type != appconnection.ProxySOCKS5 ||
		request.Proxy.Host != "proxy.internal" || request.Proxy.Port != 1080 {
		t.Fatalf("proxy = %+v, want the Host's SOCKS5 proxy", request.Proxy)
	}
	if request.Network == nil || request.Network.ProfileID != "net-1" ||
		request.Network.Kind != string(domainvault.NetworkKindTailscale) {
		t.Fatalf("network = %+v, want the Host's Network Profile", request.Network)
	}
	if request.Provider == nil {
		t.Fatal("the Network Provider was not opened for the dial")
	}
	if request.Hostname != "target.internal" || request.Username != "deploy" {
		t.Fatalf("target = %+v, want the Host's endpoint", request)
	}
}
