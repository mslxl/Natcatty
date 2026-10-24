package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type factoryPasswords struct {
	value string
	err   error
}

func (p factoryPasswords) Secret(context.Context, model.HostID, string) (string, error) {
	return p.value, p.err
}

type factoryKnownHosts struct {
	records []domainconn.KnownHost
	saved   []domainconn.KnownHost
}

func (k *factoryKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return k.records, nil
}

func (k *factoryKnownHosts) UpsertKnownHost(_ context.Context, record domainconn.KnownHost) error {
	k.saved = append(k.saved, record)
	return nil
}

type factoryTerminal struct {
	mu     sync.Mutex
	closed bool
}

func (t *factoryTerminal) Read([]byte) (int, error) { return 0, io.EOF }
func (t *factoryTerminal) Write(p []byte) (int, error) {
	return len(p), nil
}
func (t *factoryTerminal) Resize(int, int) error { return nil }
func (t *factoryTerminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return nil
}

type factoryClient struct {
	mu        sync.Mutex
	terminal  *factoryTerminal
	spec      appconnection.TerminalSpec
	closed    bool
	closeCall int
	openCalls int
}

func (c *factoryClient) OpenTerminal(_ context.Context, spec appconnection.TerminalSpec) (appconnection.Terminal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.openCalls++
	c.spec = spec
	return c.terminal, nil
}

func (c *factoryClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeCall++
	c.closed = true
	return nil
}

type factoryDialer struct {
	client  *factoryClient
	request appconnection.DialRequest
	calls   int
}

func (d *factoryDialer) Dial(_ context.Context, req appconnection.DialRequest) (appconnection.Client, error) {
	d.calls++
	d.request = req
	return d.client, nil
}

type factoryLocal struct {
	spec appsession.StartSpec
}

func (f *factoryLocal) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.spec = spec
	return &wiringTransport{closed: make(chan struct{})}, nil
}

func TestSSHSessionAcquiresAndReleasesALease(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{value: "s3cret"}}, &factoryKnownHosts{})
	t.Cleanup(manager.CloseAll)
	local := &factoryLocal{}
	factory := newTransportFactory(local, nil, manager, nil)

	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1", Hostname: "example.com",
		Port: 2222, Username: "root", Cols: 100, Rows: 30,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if dialer.calls != 1 {
		t.Fatalf("dial calls = %d, want 1", dialer.calls)
	}
	if dialer.request.Hostname != "example.com" || dialer.request.Port != 2222 ||
		dialer.request.Username != "root" || dialer.request.Password != "s3cret" {
		t.Fatalf("dial request = %+v, want the Host endpoint and saved password", dialer.request)
	}
	if client.spec.Cols != 100 || client.spec.Rows != 30 {
		t.Fatalf("terminal spec = %+v, want 100x30", client.spec)
	}
	if local.spec.Kind != "" {
		t.Fatalf("local factory was called for an SSH Session: %+v", local.spec)
	}
	if dialer.request.X11 != nil {
		t.Fatalf("dial request enabled X11 without a Host preference: %+v", dialer.request.X11)
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The shell channel closes, but the authenticated Connection is parked for
	// reuse rather than torn down.
	if client.closeCall != 0 {
		t.Fatalf("SSH client close calls = %d, want the transport parked", client.closeCall)
	}
	client.terminal.mu.Lock()
	terminalClosed := client.terminal.closed
	client.terminal.mu.Unlock()
	if !terminalClosed {
		t.Fatal("the terminal was not closed with the transport")
	}
	manager.CloseAll()
	if client.closeCall != 1 {
		t.Fatalf("SSH client close calls after CloseAll = %d, want 1", client.closeCall)
	}
}

func TestX11SessionsPassTheDisplayAndReportNotices(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{value: "s3cret"}}, &factoryKnownHosts{})
	t.Cleanup(manager.CloseAll)
	factory := newTransportFactory(&factoryLocal{}, nil, manager, nil)

	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1", Hostname: "example.com", Username: "root",
		X11Forwarding: true, X11Display: ":7",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	if dialer.request.X11 == nil {
		t.Fatal("the dial request did not enable X11")
	}
	if dialer.request.X11.Display != ":7" {
		t.Fatalf("X11 display = %q, want :7", dialer.request.X11.Display)
	}

	notifier, ok := transport.(appsession.Notifier)
	if !ok {
		t.Fatal("the Session transport does not expose notices")
	}
	dialer.request.X11.OnNotice(appconnection.X11Notice{
		Code:    appconnection.X11NoticeLocalDisplay,
		Display: "localhost:6000",
	})
	select {
	case notice := <-notifier.Notices():
		if notice.Code != appsession.NoticeX11LocalDisplay || notice.Target != "localhost:6000" {
			t.Fatalf("notice = %+v, want the local-display notice", notice)
		}
	default:
		t.Fatal("the X11 notice was not forwarded to the Session")
	}
}

func TestClosingALiveShellKeepsTheSharedTransportOpen(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{value: "s3cret"}}, &factoryKnownHosts{},
		// The fake terminal has no live channel; the probe is covered in the
		// connection package's tests.
		appconnection.WithLivenessProbe(0))
	t.Cleanup(manager.CloseAll)
	factory := newTransportFactory(&factoryLocal{}, nil, manager, nil)
	spec := appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1", Hostname: "example.com", Username: "root",
	}

	first, err := factory.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	second, err := factory.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if dialer.calls != 1 {
		t.Fatalf("dial calls = %d, want one shared Connection", dialer.calls)
	}

	// Closing the first, still-live shell must not discard the transport the
	// second Session is using.
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if client.closeCall != 0 {
		t.Fatalf("closing a live shell closed the shared transport (%d closes)", client.closeCall)
	}

	if err := second.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if client.closeCall != 0 {
		t.Fatalf("the last shell release closed the parked transport (%d closes)", client.closeCall)
	}

	third, err := factory.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("third Open: %v", err)
	}
	if dialer.calls != 1 {
		t.Fatalf("the parked transport was not reused: dial calls = %d", dialer.calls)
	}
	_ = third.Close()
}

func TestClosingATransportThatEndedInErrorDiscardsIt(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{value: "s3cret"}}, &factoryKnownHosts{})
	t.Cleanup(manager.CloseAll)
	factory := newTransportFactory(&factoryLocal{}, nil, manager, nil)

	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1", Hostname: "example.com", Username: "root",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// A dropped transport classifies itself before the Session closes it.
	reporter, ok := transport.(appsession.EndReporter)
	if !ok {
		t.Fatal("the leased transport does not report its end")
	}
	if end := reporter.End(); end.Reason != appsession.EndReasonError {
		t.Fatalf("end = %+v, want an error classification", end)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if client.closeCall != 1 {
		t.Fatalf("SSH client close calls = %d, want the failed transport discarded", client.closeCall)
	}
}

func TestTestConnectionAuthenticatesAndReleasesWithoutAShell(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{value: "s3cret"}}, &factoryKnownHosts{})
	t.Cleanup(manager.CloseAll)

	host := domainvault.Host{
		ID: "host-1", Hostname: "example.com", Port: 2222, Username: "root",
		Protocol: domainvault.ProtocolSSH,
	}
	if err := testConnection(context.Background(), manager, nil, host); err != nil {
		t.Fatalf("testConnection: %v", err)
	}
	if dialer.calls != 1 {
		t.Fatalf("dial calls = %d, want one authenticated transport", dialer.calls)
	}
	if dialer.request.Hostname != "example.com" || dialer.request.Port != 2222 ||
		dialer.request.Username != "root" || dialer.request.Password != "s3cret" {
		t.Fatalf("dial request = %+v, want the Host endpoint and saved password", dialer.request)
	}
	if client.openCalls != 0 {
		t.Fatalf("Test connection opened %d shell channels, want none", client.openCalls)
	}
	// The lease was released, so the transport is parked rather than closed.
	if client.closed {
		t.Fatal("Test connection closed the transport instead of releasing its lease")
	}
	manager.CloseAll()
	if !client.closed {
		t.Fatal("the connection manager did not close the parked test transport")
	}
}

func TestSSHSessionWithoutASavedPasswordIsAnAuthFailure(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{err: appvault.ErrNotFound}}, &factoryKnownHosts{})
	factory := newTransportFactory(&factoryLocal{}, nil, manager, nil)

	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1", Hostname: "example.com", Username: "root",
	})
	if err == nil {
		t.Fatal("Open succeeded, want an auth failure")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
		t.Fatalf("error = %v, want an auth connection error", err)
	}
	if !errors.Is(err, appconnection.ErrNoCredentials) {
		t.Fatalf("error = %v, want ErrNoCredentials", err)
	}
	if dialer.calls != 0 {
		t.Fatalf("dial calls = %d, want 0 before credentials are available", dialer.calls)
	}
}

func TestSSHSessionWithALockedSecretStoreIsNotAnAuthFailure(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	locked := &appvault.Error{Kind: appvault.KindPermission, Cause: appvault.ErrSecretsLocked}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{err: locked}}, &factoryKnownHosts{})
	factory := newTransportFactory(&factoryLocal{}, nil, manager, nil)

	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1", Hostname: "example.com", Username: "root",
	})
	if err == nil {
		t.Fatal("Open succeeded, want a Vault failure")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindInternal {
		t.Fatalf("error = %v, want an internal connection error", err)
	}
	if errors.Is(err, appconnection.ErrNoCredentials) {
		t.Fatalf("error = %v must not claim the password is missing", err)
	}
	if dialer.calls != 0 {
		t.Fatalf("dial calls = %d before the Vault could be read", dialer.calls)
	}
}

func TestLocalSessionStillUsesTheLocalFactory(t *testing.T) {
	local := &factoryLocal{}
	factory := newTransportFactory(local, nil, nil, nil)

	transport, err := factory.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if local.spec.Kind != appsession.KindLocal {
		t.Fatalf("local factory spec = %+v, want a local Session", local.spec)
	}
}

type factoryRoutes struct {
	route appconnection.Route
	err   error
	id    model.HostID
}

func (r *factoryRoutes) Route(_ context.Context, id model.HostID) (appconnection.Route, error) {
	r.id = id
	return r.route, r.err
}

func TestSSHSessionUsesTheResolvedChainAndProxy(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{value: "pw"}}, &factoryKnownHosts{})
	routes := &factoryRoutes{route: appconnection.Route{
		Jumps: []appconnection.Hop{{
			HostID: "jump-1", Label: "bastion", Hostname: "bastion.example.com", Port: 2222, Username: "ops",
		}},
		Proxy: &appconnection.Proxy{Type: appconnection.ProxySOCKS5, Host: "proxy", Port: 1080},
	}}
	factory := newTransportFactory(&factoryLocal{}, nil, manager, routes)

	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "target", Hostname: "target.example.com",
		Port: 22, Username: "root",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()

	if routes.id != "target" {
		t.Fatalf("route lookup = %q", routes.id)
	}
	if len(dialer.request.Jumps) != 1 {
		t.Fatalf("jumps = %d, want 1", len(dialer.request.Jumps))
	}
	hop := dialer.request.Jumps[0]
	if hop.Label != "bastion" || hop.Hostname != "bastion.example.com" || hop.Port != 2222 || hop.Username != "ops" {
		t.Fatalf("hop = %+v", hop)
	}
	if hop.Password != "pw" {
		t.Fatalf("hop password = %q, want the Vault-backed value", hop.Password)
	}
	if dialer.request.Proxy == nil || dialer.request.Proxy.Type != appconnection.ProxySOCKS5 ||
		dialer.request.Proxy.Host != "proxy" || dialer.request.Proxy.Port != 1080 {
		t.Fatalf("proxy = %+v", dialer.request.Proxy)
	}
	if dialer.request.Hostname != "target.example.com" || dialer.request.Username != "root" {
		t.Fatalf("target = %+v", dialer.request)
	}
}

func TestSSHSessionRouteFailurePreventsDialing(t *testing.T) {
	dialer := &factoryDialer{client: &factoryClient{terminal: &factoryTerminal{}}}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{value: "pw"}}, &factoryKnownHosts{})
	routes := &factoryRoutes{err: &appconnection.Error{
		Kind: appconnection.KindInternal, Cause: appvault.ErrNotFound,
	}}
	factory := newTransportFactory(&factoryLocal{}, nil, manager, routes)

	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "target", Hostname: "target.example.com", Username: "root",
	})
	if err == nil {
		t.Fatal("Open succeeded with a failed route lookup")
	}
	if dialer.calls != 0 {
		t.Fatalf("dial calls = %d, want 0", dialer.calls)
	}
}

type fakeRouteVault struct {
	hosts   map[model.HostID]domainvault.Host
	secrets map[string]string
}

func (v *fakeRouteVault) Get(_ context.Context, id model.HostID) (domainvault.Host, error) {
	host, ok := v.hosts[id]
	if !ok {
		return domainvault.Host{}, appvault.ErrNotFound
	}
	return host, nil
}

func (v *fakeRouteVault) Secret(_ context.Context, id model.HostID, field string) (string, error) {
	value, ok := v.secrets[string(id)+"/"+field]
	if !ok {
		return "", appvault.ErrNotFound
	}
	return value, nil
}

func TestVaultRoutesResolveTheChainAndFirstHopProxy(t *testing.T) {
	vault := &fakeRouteVault{
		hosts: map[model.HostID]domainvault.Host{
			"target": {
				ID: "target", Label: "Target", Hostname: "target.internal", Port: 22, Username: "app",
				HostChain:        &domainvault.HostChain{HostIDs: []model.HostID{"jump-1", "jump-2"}},
				Proxy:            &domainvault.ProxyConfig{Type: domainvault.ProxyHTTP, Host: "ignored", Port: 3128},
				LegacyAlgorithms: true,
				SkipEcdsaHostKey: true,
				Algorithms: &domainvault.AlgorithmOverrides{
					KeyExchanges: []string{"diffie-hellman-group14-sha1"},
				},
			},
			"jump-1": {
				ID: "jump-1", Label: "Bastion", Hostname: "bastion.internal", Port: 2222, Username: "ops",
				Proxy: &domainvault.ProxyConfig{
					Type: domainvault.ProxySOCKS5, Host: "proxy.internal", Port: 1080,
					Username: "proxy-user", HasPassword: true,
				},
				Algorithms: &domainvault.AlgorithmOverrides{Ciphers: []string{"aes128-cbc"}},
			},
			"jump-2": {
				ID: "jump-2", Hostname: "gateway.internal", Port: 22, Username: "deploy",
			},
		},
		secrets: map[string]string{"jump-1/" + appvault.FieldProxyPassword: "proxy-pass"},
	}

	route, err := (vaultRoutes{vault: vault}).Route(context.Background(), "target")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(route.Jumps) != 2 {
		t.Fatalf("jumps = %+v", route.Jumps)
	}
	if route.Jumps[0].HostID != "jump-1" || route.Jumps[0].Label != "Bastion" ||
		route.Jumps[0].Hostname != "bastion.internal" || route.Jumps[0].Port != 2222 ||
		route.Jumps[0].Username != "ops" {
		t.Fatalf("first jump = %+v", route.Jumps[0])
	}
	if route.Jumps[1].HostID != "jump-2" || route.Jumps[1].Label != "" ||
		route.Jumps[1].Hostname != "gateway.internal" {
		t.Fatalf("second jump = %+v", route.Jumps[1])
	}
	if route.Proxy == nil || route.Proxy.Type != appconnection.ProxySOCKS5 ||
		route.Proxy.Host != "proxy.internal" || route.Proxy.Port != 1080 ||
		route.Proxy.Username != "proxy-user" || route.Proxy.Password != "proxy-pass" {
		t.Fatalf("proxy = %+v", route.Proxy)
	}
	if !route.Algorithms.Legacy || !route.Algorithms.SkipECDSAHostKey ||
		!slices.Equal(route.Algorithms.KeyExchanges, []string{"diffie-hellman-group14-sha1"}) {
		t.Fatalf("target algorithms = %+v", route.Algorithms)
	}
	if !slices.Equal(route.Jumps[0].Algorithms.Ciphers, []string{"aes128-cbc"}) ||
		route.Jumps[0].Algorithms.Legacy || route.Jumps[0].Algorithms.SkipECDSAHostKey {
		t.Fatalf("first jump algorithms = %+v", route.Jumps[0].Algorithms)
	}
	if route.Jumps[1].Algorithms.Legacy || route.Jumps[1].Algorithms.SkipECDSAHostKey ||
		len(route.Jumps[1].Algorithms.Ciphers) != 0 {
		t.Fatalf("second jump algorithms = %+v", route.Jumps[1].Algorithms)
	}
}

// resolvingRouteVault adds the Vault's credential resolution to the route
// fake, so the fingerprint tests can rotate credentials.
type resolvingRouteVault struct {
	*fakeRouteVault
	resolved map[model.HostID]appvault.ResolvedAuth
}

func (v resolvingRouteVault) ResolveAuth(_ context.Context, id model.HostID) (appvault.ResolvedAuth, error) {
	return v.resolved[id], nil
}

func TestVaultRoutesFingerprintResolvedCredentials(t *testing.T) {
	vault := resolvingRouteVault{
		fakeRouteVault: &fakeRouteVault{
			hosts: map[model.HostID]domainvault.Host{
				"target": {ID: "target", Hostname: "target.internal", Port: 22, Username: "app"},
			},
			secrets: map[string]string{},
		},
		resolved: map[model.HostID]appvault.ResolvedAuth{
			"target": {Password: "first"},
		},
	}
	routes := vaultRoutes{vault: vault}

	first, err := routes.Route(context.Background(), "target")
	if err != nil {
		t.Fatalf("first Route: %v", err)
	}
	vault.resolved["target"] = appvault.ResolvedAuth{Password: "rotated"}
	second, err := routes.Route(context.Background(), "target")
	if err != nil {
		t.Fatalf("second Route: %v", err)
	}
	if first.ConfigFingerprint == second.ConfigFingerprint {
		t.Fatal("a rotated password did not change the reuse fingerprint")
	}

	vault.resolved["target"] = appvault.ResolvedAuth{
		Password: "rotated",
		Key:      &appvault.ResolvedKey{PrivateKey: "key-material", Passphrase: "passphrase"},
	}
	third, err := routes.Route(context.Background(), "target")
	if err != nil {
		t.Fatalf("third Route: %v", err)
	}
	if third.ConfigFingerprint == second.ConfigFingerprint {
		t.Fatal("a newly resolved Key did not change the reuse fingerprint")
	}
}

func TestVaultRoutesLoadAProxyPasswordTheRecordDoesNotAdvertise(t *testing.T) {
	// A session-only secret is not reflected by HasPassword on the persisted
	// Host, but the route still has to use it.
	vault := &fakeRouteVault{
		hosts: map[model.HostID]domainvault.Host{
			"target": {
				ID: "target", Hostname: "target.internal", Port: 22, Username: "app",
				Proxy: &domainvault.ProxyConfig{
					Type: domainvault.ProxyHTTP, Host: "proxy.internal", Port: 3128,
					Username: "user",
				},
			},
		},
		secrets: map[string]string{"target/" + appvault.FieldProxyPassword: "session-pass"},
	}

	route, err := (vaultRoutes{vault: vault}).Route(context.Background(), "target")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if route.Proxy == nil || route.Proxy.Password != "session-pass" {
		t.Fatalf("proxy = %+v, want the stored session password", route.Proxy)
	}
}

func TestVaultRoutesRejectAStaleProxyPasswordReference(t *testing.T) {
	vault := &fakeRouteVault{
		hosts: map[model.HostID]domainvault.Host{
			"target": {
				ID: "target", Hostname: "target.internal", Port: 22, Username: "app",
				Proxy: &domainvault.ProxyConfig{
					Type: domainvault.ProxyHTTP, Host: "proxy.internal", Port: 3128,
					Username: "user", HasPassword: true,
				},
			},
		},
		secrets: map[string]string{},
	}

	_, err := (vaultRoutes{vault: vault}).Route(context.Background(), "target")
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindInternal {
		t.Fatalf("error = %v, want an internal connection error", err)
	}
	if !errors.Is(err, appconnection.ErrInvalidProxy) {
		t.Fatalf("error = %v, want the invalid-proxy cause", err)
	}
}

func TestVaultRoutesFallBackToTheTargetProxyWithoutAFirstHopProxy(t *testing.T) {
	vault := &fakeRouteVault{
		hosts: map[model.HostID]domainvault.Host{
			"target": {
				ID: "target", Hostname: "target.internal", Port: 22, Username: "app",
				HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"jump-1"}},
				Proxy:     &domainvault.ProxyConfig{Type: domainvault.ProxyCommand, Command: "ssh -W %h:%p bastion"},
			},
			"jump-1": {ID: "jump-1", Hostname: "bastion.internal", Port: 22, Username: "ops"},
		},
		secrets: map[string]string{},
	}

	route, err := (vaultRoutes{vault: vault}).Route(context.Background(), "target")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if route.Proxy == nil || route.Proxy.Type != appconnection.ProxyCommand ||
		route.Proxy.Command != "ssh -W %h:%p bastion" {
		t.Fatalf("proxy = %+v", route.Proxy)
	}
}

func TestVaultRoutesFailWhenAJumpHostIsMissing(t *testing.T) {
	vault := &fakeRouteVault{hosts: map[model.HostID]domainvault.Host{
		"target": {
			ID: "target", Hostname: "target.internal", Port: 22, Username: "app",
			HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"gone"}},
		},
	}, secrets: map[string]string{}}

	_, err := (vaultRoutes{vault: vault}).Route(context.Background(), "target")
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindInternal {
		t.Fatalf("error = %v, want an internal connection error", err)
	}
}
