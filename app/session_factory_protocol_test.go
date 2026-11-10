package app

import (
	"context"
	"errors"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	protocolmodel "github.com/mslxl/natcatty/domain/model/protocol"
	telnetmodel "github.com/mslxl/natcatty/domain/model/telnet"
)

// factoryProvider is a plugin-owned transport factory. It records the spec it
// received so a test can prove the provider registry dispatched before the
// built-in Kind switch.
type factoryProvider struct {
	calls int
	spec  appsession.StartSpec
}

func (f *factoryProvider) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.calls++
	f.spec = spec
	return &factoryTerminal{}, nil
}

func TestProviderProtocolReachesItsRegisteredFactory(t *testing.T) {
	providers := appsession.NewProviderRegistry()
	provider := &factoryProvider{}
	id := protocolmodel.ID("x-acme-loop")
	if err := providers.Register(id, provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	factory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil).withProviders(providers)

	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Protocol: string(id), Label: "provider-session",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	if provider.spec.Protocol != string(id) || provider.spec.Label != "provider-session" {
		t.Fatalf("provider received spec %+v, want the provider protocol and label", provider.spec)
	}
}

func TestAnUnavailableProviderProtocolFailsClosed(t *testing.T) {
	// No provider is registered, and the local factory is present: a fallback
	// would be observable as a successful open.
	local := &factoryLocal{}
	factory := newTransportFactory(local, nil, nil, nil, nil)

	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Protocol: "x-acme-loop",
	})
	if !errors.Is(err, appsession.ErrProviderUnavailable) {
		t.Fatalf("Open error = %v, want ErrProviderUnavailable", err)
	}
	if local.spec.Kind != "" {
		t.Fatalf("the local factory opened a provider Session: %+v", local.spec)
	}
}

func TestAPluginKindWithoutAProviderProtocolFailsClosed(t *testing.T) {
	// The local factory is available and would succeed if the plugin Kind fell
	// through to the default branch. It must never be reached: an empty or
	// built-in protocol cannot identify a connection provider.
	for _, spec := range []appsession.StartSpec{
		{Kind: appsession.KindPlugin},
		{Kind: appsession.KindPlugin, Protocol: "ssh"},
		{Kind: appsession.KindPlugin, Protocol: "x-acme-missing"},
	} {
		local := &factoryLocal{}
		factory := newTransportFactory(local, nil, nil, nil, nil)
		_, err := factory.Open(context.Background(), spec)
		if !errors.Is(err, appsession.ErrProviderUnavailable) {
			t.Fatalf("Open(%+v) error = %v, want ErrProviderUnavailable", spec, err)
		}
		if local.spec.Kind != "" {
			t.Fatalf("Open(%+v) fell back to the local factory: %+v", spec, local.spec)
		}
	}
}

func TestAnUnknownProtocolIsRejectedAtTheFactory(t *testing.T) {
	factory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil)
	_, err := factory.Open(context.Background(), appsession.StartSpec{Protocol: "gopher"})
	if !errors.Is(err, protocolmodel.ErrInvalidProtocol) {
		t.Fatalf("Open error = %v, want ErrInvalidProtocol", err)
	}
}

func TestABuiltinProtocolSelectsItsKindFromTheRegistry(t *testing.T) {
	// A caller that supplies the protocol but no Kind still reaches the
	// registry-selected transport.
	telnet := &factoryTelnet{}
	factory := newTransportFactory(&factoryLocal{}, nil, telnet, nil, nil)
	cfg := telnetmodel.Config{Hostname: "console.local", Port: 2323}

	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Protocol: string(protocolmodel.Telnet), Telnet: &cfg,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if telnet.calls != 1 || telnet.spec.Kind != appsession.KindTelnet {
		t.Fatalf("telnet calls=%d spec.Kind=%q, want the Telnet transport",
			telnet.calls, telnet.spec.Kind)
	}
}

func TestReservedBuiltinProtocolsCannotBeRegisteredAsProviders(t *testing.T) {
	providers := appsession.NewProviderRegistry()
	for _, id := range []protocolmodel.ID{
		protocolmodel.SSH, protocolmodel.Telnet, protocolmodel.Local,
		protocolmodel.Mosh, protocolmodel.ET, protocolmodel.Serial,
	} {
		err := providers.Register(id, &factoryProvider{})
		if !errors.Is(err, protocolmodel.ErrReservedProtocol) {
			t.Fatalf("Register(%q) error = %v, want ErrReservedProtocol", id, err)
		}
	}
}
