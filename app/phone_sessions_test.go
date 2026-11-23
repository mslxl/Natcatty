package app

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
	"time"

	fileapplock "github.com/mslxl/natcatty/adapters/applock"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appapplock "github.com/mslxl/natcatty/application/applock"
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

	factory := newPhoneTransportFactory(nil, vaultRoutes{}, nil, nil)
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
	factory := newPhoneTransportFactory(manager, vaultRoutes{vault: vault}, providers, nil)

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

// TestPhoneRootInstallsTheSessionActivityHook covers the App Lock idle rule on
// the phone. Terminal widgets take their own input, so the gate never sees a
// keystroke typed there; the desktop root instead treats a Session change as
// the activity. The phone composition root is build-tagged for phones, so this
// guard parses it rather than calling it: without the hook a long terminal use
// locks App Lock mid-session.
func TestPhoneRootInstallsTheSessionActivityHook(t *testing.T) {
	root, err := parser.ParseFile(token.NewFileSet(), "run_mobile.go", nil, 0)
	if err != nil {
		t.Fatalf("parse the phone composition root: %v", err)
	}
	installed := false
	ast.Inspect(root, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || callName(call) != "installSessionActivityHook" || len(call.Args) != 2 {
			return true
		}
		// The hook must watch the Session runtime's shared service and record
		// activity on the same App Lock service the gate renders.
		if !selectorIs(call.Args[0], "sessionRuntime", "service") ||
			!selectorIs(call.Args[1], "appLock", "RecordActivity") {
			return true
		}
		installed = true
		return false
	})
	if !installed {
		t.Fatal("the phone composition root does not install the Session activity hook " +
			"(want installSessionActivityHook(sessionRuntime.service, appLock.RecordActivity))")
	}
}

// callName returns the name a call expression invokes, or "".
func callName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// selectorIs reports whether expr is base.name for the identifier base.
func selectorIs(expr ast.Expr, base, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == base
}

// TestPhoneSessionChangesCountAsAppLockActivity proves the hook the phone root
// installs counts a Session change as App Lock activity, including remote
// output alone. The desktop root's hook is unfiltered, so output from a
// long-running program postpones the idle lock there too; the phone matches
// that rule rather than filtering for typed input the hook cannot see.
func TestPhoneSessionChangesCountAsAppLockActivity(t *testing.T) {
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ctx := context.Background()

	// Enable App Lock through the real adapter and load it again, as the phone
	// root does before the first frame.
	path := filepath.Join(t.TempDir(), "app-lock-settings.json")
	seed := appapplock.NewService(fileapplock.Open(path), appapplock.WithClock(clock))
	if err := seed.Enable(ctx, "correct horse"); err != nil {
		t.Fatalf("enable App Lock: %v", err)
	}
	if err := seed.SetTimeout(ctx, 1); err != nil {
		t.Fatalf("set App Lock timeout: %v", err)
	}
	lock := appapplock.NewService(fileapplock.Open(path), appapplock.WithClock(clock))
	if err := lock.Load(ctx); err != nil {
		t.Fatalf("load App Lock: %v", err)
	}
	if err := lock.Unlock("correct horse"); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	// This is the wiring the phone composition root performs.
	env := newPhoneTransportEnv(t, func() fakessh.Options { return fakessh.Options{} })
	installSessionActivityHook(env.runtime.service, lock.RecordActivity)

	port := startTelnetEcho(t)
	host := domainvault.Host{
		ID: "console", Label: "console", Hostname: "127.0.0.1", Port: port,
		Protocol: domainvault.ProtocolTelnet,
	}
	env.addHost(t, host, nil)

	stale, ok := lock.IdleDeadline()
	if !ok {
		t.Fatal("the unlocked App Lock has no idle deadline")
	}

	// Thirty seconds of idling, then the server sends "console ready" on its
	// own: the only activity is remote output.
	now = now.Add(30 * time.Second)
	handle := env.openHost(t, host)
	t.Cleanup(func() { _ = handle.End() })

	awaitCondition(t, "remote output to count as App Lock activity", 5*time.Second, func() bool {
		deadline, ok := lock.IdleDeadline()
		return ok && deadline.Equal(now.Add(time.Minute))
	})

	// The old deadline no longer locks.
	if lock.LockIfIdle(stale.Add(time.Second)) {
		t.Fatalf("the gate locked at the old deadline %v despite the remote output", stale)
	}
	// Once the output stops, the configured timeout still closes the gate.
	if !lock.LockIfIdle(now.Add(time.Minute + time.Second)) {
		t.Fatal("the gate stayed open past the moved idle deadline")
	}
}
