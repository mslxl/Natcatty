package app

import (
	"context"
	"strings"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

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

// fakePreferenceSource is the persisted preferences the factory reads for the
// Mosh prediction display.
type fakePreferenceSource struct {
	settings domainpreferences.Settings
}

func (s fakePreferenceSource) Settings() domainpreferences.Settings { return s.settings }

func newInProcessFactory(mosh, et appsession.Factory) *transportFactory {
	return newTransportFactory(&factoryLocal{}, nil, &factoryTelnet{}, nil, nil).
		withInProcess(mosh, et)
}

// TestMoshAlwaysUsesTheInProcessClient proves a Mosh Session cannot be routed
// anywhere else: the removed bundled client leaves no stored value, Host lookup,
// or fallback path behind.
func TestMoshAlwaysUsesTheInProcessClient(t *testing.T) {
	mosh := &implementationFactory{}
	et := &implementationFactory{}
	factory := newInProcessFactory(mosh, et)
	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, HostID: "mosh",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if mosh.calls != 1 {
		t.Fatalf("in-process Mosh opens = %d, want 1", mosh.calls)
	}
	if et.calls != 0 {
		t.Fatal("a Mosh Session reached the EternalTerminal client")
	}
}

func TestMoshWithoutAnInProcessClientFailsClosed(t *testing.T) {
	et := &implementationFactory{}
	factory := newInProcessFactory(nil, et)
	_, err := factory.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindMosh})
	if err == nil || !strings.Contains(err.Error(), "in-process Mosh") {
		t.Fatalf("Open error = %v, want a missing in-process Mosh error", err)
	}
	if et.calls != 0 {
		t.Fatal("Mosh silently opened another transport")
	}
}

func TestETAlwaysUsesTheInProcessClient(t *testing.T) {
	mosh := &implementationFactory{}
	et := &implementationFactory{}
	factory := newInProcessFactory(mosh, et)
	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindET, HostID: "et",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if et.calls != 1 {
		t.Fatalf("in-process ET opens = %d, want 1", et.calls)
	}
	if mosh.calls != 0 {
		t.Fatal("an EternalTerminal Session reached the Mosh client")
	}
}

func TestETWithoutAnInProcessClientFailsClosed(t *testing.T) {
	mosh := &implementationFactory{}
	factory := newInProcessFactory(mosh, nil)
	_, err := factory.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindET})
	if err == nil || !strings.Contains(err.Error(), "in-process EternalTerminal") {
		t.Fatalf("Open error = %v, want a missing in-process ET error", err)
	}
	if mosh.calls != 0 {
		t.Fatal("ET silently opened another transport")
	}
}

func TestOtherProtocolsAreUnchanged(t *testing.T) {
	local := &factoryLocal{}
	telnet := &factoryTelnet{}
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{value: "pw"}}, &factoryKnownHosts{})
	t.Cleanup(manager.CloseAll)
	factory := newTransportFactory(local, nil, telnet, manager, nil)

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
// local-echo setting reaches the in-process client's configuration while the
// caller's StartSpec is not mutated.
func TestMoshSessionReceivesThePersistedPredictionMode(t *testing.T) {
	settings := domainpreferences.Defaults()
	settings.Transport.MoshPredictionDisplay = moshmodel.PredictionNever
	mosh := &implementationFactory{}
	factory := newInProcessFactory(mosh, &implementationFactory{}).withPreferences(
		fakePreferenceSource{settings: settings})

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

	if mosh.spec.Mosh == nil {
		t.Fatal("the in-process factory received no Mosh configuration")
	}
	if got := mosh.spec.Mosh.PredictionDisplay; got != moshmodel.PredictionNever {
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
	mosh := &implementationFactory{}
	factory := newInProcessFactory(mosh, &implementationFactory{}).withPreferences(
		fakePreferenceSource{settings: domainpreferences.Defaults()})
	spec := appsession.StartSpec{Kind: appsession.KindMosh, Mosh: &moshmodel.Config{Hostname: "example.test"}}
	transport, err := factory.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if got := mosh.spec.Mosh.PredictionDisplay; got != moshmodel.PredictionAdaptive {
		t.Fatalf("default opened display = %q, want adaptive", got)
	}

	inProcess := &implementationFactory{}
	bare := newInProcessFactory(inProcess, &implementationFactory{})
	explicit := &moshmodel.Config{Hostname: "example.test", PredictionDisplay: moshmodel.PredictionAlways}
	transport, err = bare.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindMosh, Mosh: explicit})
	if err != nil {
		t.Fatalf("Open without a global Mosh preference: %v", err)
	}
	_ = transport.Close()
	if got := inProcess.spec.Mosh.PredictionDisplay; got != moshmodel.PredictionAlways {
		t.Fatalf("explicit opened display = %q, want always", got)
	}
}
