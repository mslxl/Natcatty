package app

import (
	"context"
	"errors"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
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
	).withInProcessET(
		&implementationFactory{},
	).withImplementations(hosts, fakeImplementationDefaults{settings: defaults})
}

func settingsWithImplementation(value transportmodel.Implementation) domainpreferences.Settings {
	settings := domainpreferences.Defaults()
	settings.Transport.Implementation = value
	// Every value supplied through this helper stands for a persisted user or
	// migrated choice, not the release default.
	settings.Transport.ImplementationExplicit = true
	return settings
}

func TestMoshAndETFollowTheReleaseDefault(t *testing.T) {
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
			bundledMosh := &implementationFactory{}
			bundledET := &implementationFactory{}
			inProcessMosh := &implementationFactory{}
			inProcessET := &implementationFactory{}
			factory := newTransportFactory(&factoryLocal{}, nil, &factoryTelnet{}, nil, nil).
				withClients(bundledMosh, bundledET).
				withInProcess(inProcessMosh, inProcessET).
				withImplementations(hosts, fakeImplementationDefaults{
					settings: domainpreferences.Defaults(),
				})
			transport, err := factory.Open(context.Background(), appsession.StartSpec{
				Kind: c.kind, HostID: c.id,
			})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			_ = transport.Close()

			bundled := bundledMosh
			inProcess := inProcessMosh
			if c.kind == appsession.KindET {
				bundled, inProcess = bundledET, inProcessET
			}
			if transportmodel.CurrentDefault() == transportmodel.Bundled {
				if bundled.calls != 1 || inProcess.calls != 0 {
					t.Fatalf("bundled default: bundled=%d in-process=%d, want 1/0",
						bundled.calls, inProcess.calls)
				}
				return
			}
			if inProcess.calls != 1 || bundled.calls != 0 {
				t.Fatalf("in-process default: in-process=%d bundled=%d, want 1/0",
					inProcess.calls, bundled.calls)
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

// TestBundledStaysSelectableAfterTheDefaultSwitch is the retention guarantee:
// once the release default is the in-process client, a Host whose persisted
// override is bundled still opens the bundled client, and a Host saved before
// the setting existed inherits the new default without being re-saved.
func TestBundledStaysSelectableAfterTheDefaultSwitch(t *testing.T) {
	hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{
		"mosh-bundled": {ID: "mosh-bundled", Protocol: domainvault.ProtocolMosh,
			TransportImplementation: transportmodel.Bundled},
		"et-bundled": {ID: "et-bundled", Protocol: domainvault.ProtocolET,
			TransportImplementation: transportmodel.Bundled},
		"et-old": {ID: "et-old", Protocol: domainvault.ProtocolET},
	}}
	bundledMosh := &implementationFactory{}
	bundledET := &implementationFactory{}
	inProcessET := &implementationFactory{}
	factory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil).
		withClients(bundledMosh, bundledET).
		withInProcessET(inProcessET).
		withImplementations(hosts, fakeImplementationDefaults{
			settings: settingsWithImplementation(transportmodel.InProcess),
		})

	for _, spec := range []appsession.StartSpec{
		{Kind: appsession.KindMosh, HostID: "mosh-bundled"},
		{Kind: appsession.KindET, HostID: "et-bundled"},
	} {
		transport, err := factory.Open(context.Background(), spec)
		if err != nil {
			t.Fatalf("%s with a bundled override: %v", spec.Kind, err)
		}
		_ = transport.Close()
	}
	if bundledMosh.calls != 1 || bundledET.calls != 1 {
		t.Fatalf("bundled opens after the switch: mosh=%d et=%d, want 1 each",
			bundledMosh.calls, bundledET.calls)
	}

	// A Host saved before ticket 21 has an empty override. It inherits the
	// switched global default directly, with no edit or migration.
	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindET, HostID: "et-old",
	})
	if err != nil {
		t.Fatalf("old Host after the switch: %v", err)
	}
	_ = transport.Close()
	if inProcessET.calls != 1 {
		t.Fatalf("old Host in-process opens = %d, want it to inherit the new default",
			inProcessET.calls)
	}
}

func TestMoshInProcessSelectionFailsClosedWithATypedError(t *testing.T) {
	cases := []struct {
		name   string
		host   *domainvault.Host
		global transportmodel.Implementation
	}{
		{"mosh Host override", &domainvault.Host{
			ID: "mosh", TransportImplementation: transportmodel.InProcess}, transportmodel.Bundled},
		{"mosh global default", nil, transportmodel.InProcess},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{}}
			spec := appsession.StartSpec{Kind: appsession.KindMosh}
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
				factory.et.(*implementationFactory).calls != 0 ||
				factory.etInProcess.(*implementationFactory).calls != 0 {
				t.Fatal("the factory silently opened a client for an in-process selection")
			}
		})
	}
}

func TestETInProcessSelectionRoutesToThePureGoClient(t *testing.T) {
	cases := []struct {
		name   string
		host   *domainvault.Host
		global transportmodel.Implementation
	}{
		{"et Host override", &domainvault.Host{
			ID: "et", TransportImplementation: transportmodel.InProcess}, transportmodel.Bundled},
		{"et global default", nil, transportmodel.InProcess},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{}}
			spec := appsession.StartSpec{Kind: appsession.KindET}
			if c.host != nil {
				hosts.items[c.host.ID] = *c.host
				spec.HostID = c.host.ID
			}
			factory := newImplementationFactory(hosts, settingsWithImplementation(c.global))
			transport, err := factory.Open(context.Background(), spec)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			_ = transport.Close()
			if factory.etInProcess.(*implementationFactory).calls != 1 {
				t.Fatalf("in-process ET opens = %d, want 1",
					factory.etInProcess.(*implementationFactory).calls)
			}
			if factory.et.(*implementationFactory).calls != 0 {
				t.Fatal("the bundled ET client opened for an in-process selection")
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
				factory.et.(*implementationFactory).calls != 0 ||
				factory.etInProcess.(*implementationFactory).calls != 0 {
				t.Fatal("the factory silently fell back for an unknown selection")
			}
		})
	}
}

func TestAMissingBundledClientFailsClosedWithATypedError(t *testing.T) {
	settings := domainpreferences.Defaults()
	settings.Transport.ChooseImplementation(transportmodel.Bundled)
	factory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil).withImplementations(
		fakeImplementationHosts{items: map[model.HostID]domainvault.Host{
			"mosh": {ID: "mosh"},
		}},
		fakeImplementationDefaults{settings: settings},
	)
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, HostID: "mosh",
	})
	if !errors.Is(err, transportmodel.ErrImplementationUnavailable) {
		t.Fatalf("Open error = %v, want ErrImplementationUnavailable", err)
	}
}

func TestInProcessMoshSelectionUsesTheInProcessFactory(t *testing.T) {
	hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{
		"mosh": {ID: "mosh", Protocol: domainvault.ProtocolMosh},
		"et":   {ID: "et", Protocol: domainvault.ProtocolET},
	}}
	bundledMosh := &implementationFactory{}
	bundledET := &implementationFactory{}
	inProcessMosh := &implementationFactory{}
	factory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil).
		withClients(bundledMosh, bundledET).
		withInProcess(inProcessMosh, nil).
		withImplementations(hosts, fakeImplementationDefaults{
			settings: settingsWithImplementation(transportmodel.InProcess),
		})

	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, HostID: "mosh",
	})
	if err != nil {
		t.Fatalf("Mosh Open: %v", err)
	}
	_ = transport.Close()
	if inProcessMosh.calls != 1 || bundledMosh.calls != 0 || bundledET.calls != 0 {
		t.Fatalf("in-process Mosh opens = %d, bundled Mosh = %d, ET = %d; want 1, 0, 0",
			inProcessMosh.calls, bundledMosh.calls, bundledET.calls)
	}

	// This factory deliberately leaves the in-process ET client unwired, so
	// ET must keep failing closed with the typed unavailable error.
	if _, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindET, HostID: "et",
	}); !errors.Is(err, transportmodel.ErrImplementationUnavailable) {
		t.Fatalf("ET Open error = %v, want ErrImplementationUnavailable", err)
	}
	if bundledET.calls != 0 {
		t.Fatal("ET silently opened the bundled client")
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

// TestMoshSessionReceivesThePersistedPredictionMode checks that the global
// local-echo setting reaches the configuration both client implementations
// receive, while the caller's StartSpec is not mutated.
func TestMoshSessionReceivesThePersistedPredictionMode(t *testing.T) {
	settings := domainpreferences.Defaults()
	// Prediction passes through whichever client is selected; pin the bundled
	// client so this test inspects the bundled factory regardless of the
	// release default.
	settings.Transport.ChooseImplementation(transportmodel.Bundled)
	settings.Transport.MoshPredictionDisplay = moshmodel.PredictionNever
	hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{}}
	factory := newImplementationFactory(hosts, settings)

	callerConfig := &moshmodel.Config{
		Hostname:          "example.test",
		PredictionDisplay: moshmodel.PredictionAlways,
	}
	spec := appsession.StartSpec{Kind: appsession.KindMosh, Mosh: callerConfig}
	transport, err := factory.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()

	opened := factory.mosh.(*implementationFactory)
	if opened.spec.Mosh == nil {
		t.Fatal("the bundled factory received no Mosh configuration")
	}
	if got := opened.spec.Mosh.PredictionDisplay; got != moshmodel.PredictionNever {
		t.Fatalf("opened prediction display = %q, want the persisted never", got)
	}
	if callerConfig.PredictionDisplay != moshmodel.PredictionAlways {
		t.Fatalf("the caller's configuration was mutated to %q", callerConfig.PredictionDisplay)
	}
}

// TestMoshSessionDefaultsToAdaptivePrediction checks an empty persisted value
// yields stock's adaptive mode, and that a caller without global preferences
// (tests and MCP entry points) keeps exactly what it supplied.
func TestMoshSessionDefaultsToAdaptivePrediction(t *testing.T) {
	settings := domainpreferences.Defaults()
	settings.Transport.ChooseImplementation(transportmodel.Bundled)
	hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{}}
	factory := newImplementationFactory(hosts, settings)
	spec := appsession.StartSpec{Kind: appsession.KindMosh, Mosh: &moshmodel.Config{Hostname: "example.test"}}
	transport, err := factory.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if got := factory.mosh.(*implementationFactory).spec.Mosh.PredictionDisplay; got != moshmodel.PredictionAdaptive {
		t.Fatalf("default opened display = %q, want adaptive", got)
	}

	bundled := &implementationFactory{}
	inProcess := &implementationFactory{}
	bare := newTransportFactory(&factoryLocal{}, nil, &factoryTelnet{}, nil, nil).
		withClients(bundled, &implementationFactory{}).
		withInProcess(inProcess, &implementationFactory{})
	explicit := &moshmodel.Config{Hostname: "example.test", PredictionDisplay: moshmodel.PredictionAlways}
	transport, err = bare.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindMosh, Mosh: explicit})
	if err != nil {
		t.Fatalf("Open without a global Mosh preference: %v", err)
	}
	_ = transport.Close()
	opened := bundled
	if transportmodel.CurrentDefault() == transportmodel.InProcess {
		opened = inProcess
	}
	if got := opened.spec.Mosh.PredictionDisplay; got != moshmodel.PredictionAlways {
		t.Fatalf("explicit opened display = %q, want always", got)
	}
}
