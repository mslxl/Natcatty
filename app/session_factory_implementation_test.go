package app

import (
	"context"
	"errors"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeImplementationHosts is the Host record source the factory uses to read a
// per-Host override.
type fakeImplementationHosts struct {
	items map[model.HostID]domainvault.Host
	err   error
}

func (h fakeImplementationHosts) Get(_ context.Context, id model.HostID) (domainvault.Host, error) {
	if h.err != nil {
		return domainvault.Host{}, h.err
	}
	host, ok := h.items[id]
	if !ok {
		return domainvault.Host{}, errors.New("missing host")
	}
	return host, nil
}

// fakeImplementationDefaults is the persisted global choice.
type fakeImplementationDefaults struct {
	settings domainpreferences.Settings
}

func (d fakeImplementationDefaults) Settings() domainpreferences.Settings { return d.settings }

// implementationFactory counts opens so a test can prove which transport,
// and whether any transport, was routed to.
type implementationFactory struct {
	calls int
	spec  appsession.StartSpec
}

func (f *implementationFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.calls++
	f.spec = spec
	return &factoryTerminal{}, nil
}

func newImplementationFactory(hosts fakeImplementationHosts, defaults domainpreferences.Settings) *transportFactory {
	return newTransportFactory(&factoryLocal{}, nil, &factoryTelnet{}, nil, nil).withClients(
		&implementationFactory{}, &implementationFactory{},
	).withImplementations(hosts, fakeImplementationDefaults{settings: defaults})
}

func settingsWithImplementation(value transportmodel.Implementation) domainpreferences.Settings {
	settings := domainpreferences.Defaults()
	settings.Transport.Implementation = value
	return settings
}

func TestMoshAndETDefaultToTheBundledImplementation(t *testing.T) {
	hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{
		"mosh": {ID: "mosh", Protocol: domainvault.ProtocolMosh},
		"et":   {ID: "et", Protocol: domainvault.ProtocolET},
	}}
	cases := []struct {
		kind appsession.Kind
		id   model.HostID
	}{
		{appsession.KindMosh, "mosh"},
		{appsession.KindET, "et"},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			factory := newImplementationFactory(hosts, domainpreferences.Defaults())
			transport, err := factory.Open(context.Background(), appsession.StartSpec{
				Kind: c.kind, HostID: c.id,
			})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			_ = transport.Close()
			opened := factory.mosh.(*implementationFactory)
			other := factory.et.(*implementationFactory)
			if c.kind == appsession.KindET {
				opened, other = other, opened
			}
			if opened.calls != 1 {
				t.Fatalf("bundled %s opens = %d, want 1", c.kind, opened.calls)
			}
			if other.calls != 0 {
				t.Fatalf("the other client factory opened a %s Session", c.kind)
			}
		})
	}
}

func TestHostOverrideSelectsTheBundledImplementation(t *testing.T) {
	// The global choice is the in-process client, but the Host overrides it
	// back to the bundled client, which is the rollback path during the beta.
	hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{
		"mosh": {ID: "mosh", Protocol: domainvault.ProtocolMosh,
			TransportImplementation: transportmodel.Bundled},
	}}
	factory := newImplementationFactory(hosts, settingsWithImplementation(transportmodel.InProcess))
	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, HostID: "mosh",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if factory.mosh.(*implementationFactory).calls != 1 {
		t.Fatalf("bundled Mosh opens = %d, want the Host override to win",
			factory.mosh.(*implementationFactory).calls)
	}
}

func TestInProcessSelectionFailsClosedWithATypedError(t *testing.T) {
	cases := []struct {
		name   string
		kind   appsession.Kind
		host   *domainvault.Host
		global transportmodel.Implementation
	}{
		{"mosh Host override", appsession.KindMosh, &domainvault.Host{
			ID: "mosh", TransportImplementation: transportmodel.InProcess}, transportmodel.Bundled},
		{"et Host override", appsession.KindET, &domainvault.Host{
			ID: "et", TransportImplementation: transportmodel.InProcess}, transportmodel.Bundled},
		{"mosh global default", appsession.KindMosh, nil, transportmodel.InProcess},
		{"et global default", appsession.KindET, nil, transportmodel.InProcess},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{}}
			spec := appsession.StartSpec{Kind: c.kind}
			if c.host != nil {
				hosts.items[c.host.ID] = *c.host
				spec.HostID = c.host.ID
			}
			factory := newImplementationFactory(hosts, settingsWithImplementation(c.global))
			_, err := factory.Open(context.Background(), spec)
			if !errors.Is(err, transportmodel.ErrImplementationUnavailable) {
				t.Fatalf("Open error = %v, want ErrImplementationUnavailable", err)
			}
			if factory.mosh.(*implementationFactory).calls != 0 ||
				factory.et.(*implementationFactory).calls != 0 {
				t.Fatal("the factory silently opened the bundled client for an in-process selection")
			}
		})
	}
}

func TestUnknownSelectionFailsClosedWithATypedError(t *testing.T) {
	cases := []struct {
		name   string
		kind   appsession.Kind
		host   *domainvault.Host
		global transportmodel.Implementation
	}{
		{"stale global", appsession.KindMosh, nil, transportmodel.Implementation("bundled_v2")},
		{"stale Host override", appsession.KindMosh, &domainvault.Host{
			ID: "mosh", TransportImplementation: transportmodel.Implementation("future_client")}, transportmodel.Bundled},
		{"stale ET override over a valid global", appsession.KindET, &domainvault.Host{
			ID: "et", TransportImplementation: transportmodel.Implementation("in_process_v2")}, transportmodel.InProcess},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{}}
			spec := appsession.StartSpec{Kind: c.kind}
			if c.host != nil {
				hosts.items[c.host.ID] = *c.host
				spec.HostID = c.host.ID
			}
			factory := newImplementationFactory(hosts, settingsWithImplementation(c.global))
			_, err := factory.Open(context.Background(), spec)
			if !errors.Is(err, transportmodel.ErrUnknownImplementation) {
				t.Fatalf("Open error = %v, want ErrUnknownImplementation", err)
			}
			if factory.mosh.(*implementationFactory).calls != 0 ||
				factory.et.(*implementationFactory).calls != 0 {
				t.Fatal("the factory silently fell back for an unknown selection")
			}
		})
	}
}

func TestAMissingBundledClientFailsClosedWithATypedError(t *testing.T) {
	factory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil).withImplementations(
		fakeImplementationHosts{items: map[model.HostID]domainvault.Host{
			"mosh": {ID: "mosh"},
		}},
		fakeImplementationDefaults{settings: domainpreferences.Defaults()},
	)
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, HostID: "mosh",
	})
	if !errors.Is(err, transportmodel.ErrImplementationUnavailable) {
		t.Fatalf("Open error = %v, want ErrImplementationUnavailable", err)
	}
}

func TestOtherProtocolsIgnoreTheImplementationSelection(t *testing.T) {
	// A stale or in-process global choice concerns Mosh and ET only. SSH,
	// Telnet, and a local shell must keep opening unchanged.
	hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{
		"ssh": {ID: "ssh", Protocol: domainvault.ProtocolSSH, Hostname: "example.com", Username: "root"},
	}}
	defaults := settingsWithImplementation(transportmodel.Implementation("stale"))
	local := &factoryLocal{}
	telnet := &factoryTelnet{}
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{value: "pw"}}, &factoryKnownHosts{})
	t.Cleanup(manager.CloseAll)
	factory := newTransportFactory(local, nil, telnet, manager, nil).
		withImplementations(hosts, fakeImplementationDefaults{settings: defaults})

	if transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindLocal,
	}); err != nil {
		t.Fatalf("Local Open: %v", err)
	} else {
		_ = transport.Close()
	}
	if transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindTelnet, Hostname: "console.local", Port: 2323,
	}); err != nil {
		t.Fatalf("Telnet Open: %v", err)
	} else {
		_ = transport.Close()
	}
	if transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "ssh", Hostname: "example.com", Username: "root",
	}); err != nil {
		t.Fatalf("SSH Open: %v", err)
	} else {
		_ = transport.Close()
	}
	if local.spec.Kind != appsession.KindLocal || telnet.calls != 1 || dialer.calls != 1 {
		t.Fatalf("local=%+v telnet calls=%d dial calls=%d, want each protocol unchanged",
			local.spec, telnet.calls, dialer.calls)
	}
}
