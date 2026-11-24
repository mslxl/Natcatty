package app

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	adaptervnc "github.com/mslxl/natcatty/adapters/vnc"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
)

// vncTestStream starts the in-process RFB server and returns the client end
// that a route seam hands to the adapter. No socket is opened.
func vncTestStream(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	serve := rfbtest.New(rfbtest.Config{Width: 2, Height: 2, Encoding: rfb.EncodingRaw})
	go func() { _ = serve.Serve(server) }()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client
}

func vncStartSpec() appsession.StartSpec {
	cfg := modelvnc.SessionConfig{Hostname: "desk.local", Port: 5901}.Normalized()
	return appsession.StartSpec{
		Kind: appsession.KindVNC, HostID: "vnc-desk",
		Hostname: "desk.local", Port: 5901, Username: "viewer", VNC: &cfg,
	}
}

// vncRoutedDecision reads the decision the adapter received from the app.
func vncRoutedDecision(t *testing.T, transport appsession.Transport) bool {
	t.Helper()
	reporter, ok := transport.(interface{ Routed() bool })
	if !ok {
		t.Fatal("the VNC transport does not report the routed decision")
	}
	return reporter.Routed()
}

// vncTestRoutes resolves a fixed route and records the Host identity.
type vncTestRoutes struct {
	route appconnection.Route
	err   error
	id    model.HostID
	calls int
}

func (f *vncTestRoutes) Route(_ context.Context, id model.HostID) (appconnection.Route, error) {
	f.calls++
	f.id = id
	return f.route, f.err
}

// vncTestProvider records the Provider dial it is asked to make.
type vncTestProvider struct {
	mu      sync.Mutex
	conn    net.Conn
	err     error
	calls   int
	address string
}

func (p *vncTestProvider) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.address = address
	if p.err != nil {
		return nil, p.err
	}
	return p.conn, nil
}

// vncTestNetworkFactory counts Provider opens.
type vncTestNetworkFactory struct {
	mu       sync.Mutex
	opens    int
	provider appconnection.NetworkProvider
	err      error
}

func (f *vncTestNetworkFactory) Open(context.Context, appconnection.NetworkRoute) (appconnection.NetworkProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	if f.err != nil {
		return nil, f.err
	}
	return f.provider, nil
}

func (f *vncTestNetworkFactory) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

// vncTestStreamDialer records the routed request and returns a scripted raw
// stream. Dial fails: a routed VNC Host must open a stream, not an SSH client.
type vncTestStreamDialer struct {
	mu     sync.Mutex
	req    appconnection.DialRequest
	conn   net.Conn
	err    error
	calls  int
	verify func(req appconnection.DialRequest) error
}

func (d *vncTestStreamDialer) Dial(context.Context, appconnection.DialRequest) (appconnection.Client, error) {
	return nil, errors.New("a routed VNC Host opened an SSH client instead of a stream")
}

func (d *vncTestStreamDialer) DialStream(_ context.Context, req appconnection.DialRequest) (net.Conn, error) {
	d.mu.Lock()
	d.calls++
	d.req = req
	verify, conn, err := d.verify, d.conn, d.err
	d.mu.Unlock()
	if verify != nil {
		if err := verify(req); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (d *vncTestStreamDialer) recorded() appconnection.DialRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.req
}

func (d *vncTestStreamDialer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// vncTestKnownHosts is an in-memory Known Host store.
type vncTestKnownHosts struct {
	records []domainconn.KnownHost
}

func (f *vncTestKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return append([]domainconn.KnownHost(nil), f.records...), nil
}

func (f *vncTestKnownHosts) UpsertKnownHost(context.Context, domainconn.KnownHost) error {
	return nil
}

// vncTestAuthSource authenticates every SSH hop with one password.
type vncTestAuthSource struct{}

func (vncTestAuthSource) AuthPlan(context.Context, appconnection.Request) (appconnection.AuthPlan, error) {
	return appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Mode: appconnection.AuthModePassword, Password: "pw",
	}), nil
}

// vncTestPrompter records a host-key prompt and returns a scripted decision.
type vncTestPrompter struct {
	mu       sync.Mutex
	keys     []domainconn.Key
	decision appconnection.PromptDecision
}

func (p *vncTestPrompter) Confirm(_ context.Context, key domainconn.Key) (appconnection.PromptDecision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys = append(p.keys, key)
	return p.decision, nil
}

// TestVNCDirectHostDialsWithoutARoute pins the no-route branch: the adapter's
// own dialer reaches the target, no Provider starts, and the stream is not
// marked as tunnel-encrypted.
func TestVNCDirectHostDialsWithoutARoute(t *testing.T) {
	client := vncTestStream(t)
	directCalls := 0
	inner := adaptervnc.Factory{Dial: func(_ context.Context, network, address string) (net.Conn, error) {
		directCalls++
		if network != "tcp" || address != "desk.local:5901" {
			t.Errorf("direct dial = %s %s, want tcp desk.local:5901", network, address)
		}
		return client, nil
	}}
	networkFactory := &vncTestNetworkFactory{provider: &vncTestProvider{
		err: errors.New("a Provider was used without a route"),
	}}
	routes := &vncTestRoutes{}
	factory := newVNCFactory(inner, routes,
		appconnection.NewNetworkProviderRegistry(networkFactory), nil)

	transport, err := factory.Open(context.Background(), vncStartSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if directCalls != 1 {
		t.Fatalf("direct dials = %d, want 1", directCalls)
	}
	if route := vncRoutedDecision(t, transport); route {
		t.Fatal("a direct VNC Host was marked as routed")
	}
	if opens := networkFactory.openCount(); opens != 0 {
		t.Fatalf("provider opens = %d, want none without a route", opens)
	}
}

// TestVNCUsesTheRoutedStreamForAJumpOrProxy proves a VNC Host routes through
// the Connection Manager: the jump is an SSH endpoint with its own
// credentials, either proxy type applies to the first endpoint, and the target
// is the VNC endpoint. The adapter's direct dialer is never tried.
func TestVNCUsesTheRoutedStreamForAJumpOrProxy(t *testing.T) {
	proxies := []struct {
		name  string
		proxy *appconnection.Proxy
	}{
		{"http", &appconnection.Proxy{
			Type: appconnection.ProxyHTTP, Host: "proxy.lab", Port: 8080,
		}},
		{"socks5", &appconnection.Proxy{
			Type: appconnection.ProxySOCKS5, Host: "proxy.lab", Port: 1080,
		}},
	}
	for _, tc := range proxies {
		t.Run(tc.name, func(t *testing.T) {
			dialer := &vncTestStreamDialer{conn: vncTestStream(t)}
			manager := appconnection.NewManager(dialer, nil, &vncTestKnownHosts{},
				appconnection.WithAuthSource(vncTestAuthSource{}))
			route := appconnection.Route{
				Jumps: []appconnection.Hop{{
					HostID: "bastion", Label: "bastion", Hostname: "bastion.lab",
					Port: 2222, Username: "ops",
				}},
				Proxy: tc.proxy,
			}
			directCalls := 0
			inner := adaptervnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
				directCalls++
				return nil, errors.New("direct dial is forbidden")
			}}
			factory := newVNCFactory(inner, &vncTestRoutes{route: route},
				appconnection.NewNetworkProviderRegistry(nil), manager)

			transport, err := factory.Open(context.Background(), vncStartSpec())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			_ = transport.Close()

			req := dialer.recorded()
			if req.Hostname != "desk.local" || req.Port != 5901 {
				t.Fatalf("target = %s:%d, want desk.local:5901", req.Hostname, req.Port)
			}
			if req.Username != "viewer" {
				t.Fatalf("target username = %q, want viewer", req.Username)
			}
			if len(req.Jumps) != 1 {
				t.Fatalf("jumps = %d, want the saved jump", len(req.Jumps))
			}
			jump := req.Jumps[0]
			if jump.Hostname != "bastion.lab" || jump.Port != 2222 || jump.Username != "ops" {
				t.Fatalf("jump = %s@%s:%d, want ops@bastion.lab:2222",
					jump.Username, jump.Hostname, jump.Port)
			}
			if req.Proxy == nil || *req.Proxy != *tc.proxy {
				t.Fatalf("proxy = %+v, want %+v", req.Proxy, tc.proxy)
			}
			if directCalls != 0 {
				t.Fatalf("direct dials = %d, want no fallback", directCalls)
			}
			if routed := vncRoutedDecision(t, transport); !routed {
				t.Fatal("a routed VNC Host was not marked as tunnel-encrypted")
			}
		})
	}
}

// TestVNCUsesTheNetworkProviderWhenItHasOnlyAProfile proves a Network Profile
// alone supplies the one VNC dial and marks the stream as tunnel-encrypted,
// with no fallback to the adapter's direct dialer.
func TestVNCUsesTheNetworkProviderWhenItHasOnlyAProfile(t *testing.T) {
	provider := &vncTestProvider{conn: vncTestStream(t)}
	networkFactory := &vncTestNetworkFactory{provider: provider}
	registry := appconnection.NewNetworkProviderRegistry(networkFactory)
	network := &appconnection.NetworkRoute{
		ProfileID: "net-desktop", Kind: "tailscale", Label: "Lab", Fingerprint: "fp-vnc",
	}
	routes := &vncTestRoutes{route: appconnection.Route{Network: network}}
	directCalls := 0
	inner := adaptervnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		directCalls++
		return nil, errors.New("direct dial is forbidden")
	}}
	factory := newVNCFactory(inner, routes, registry, nil)

	transport, err := factory.Open(context.Background(), vncStartSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()

	if routes.calls != 1 || routes.id != "vnc-desk" {
		t.Fatalf("route resolution = %d for %q, want the target Host", routes.calls, routes.id)
	}
	provider.mu.Lock()
	calls, address := provider.calls, provider.address
	provider.mu.Unlock()
	if calls != 1 || address != "desk.local:5901" {
		t.Fatalf("provider dials = %d at %q, want one at desk.local:5901", calls, address)
	}
	if directCalls != 0 {
		t.Fatalf("direct dials = %d, want no fallback", directCalls)
	}
	if routed := vncRoutedDecision(t, transport); !routed {
		t.Fatal("a Network Profile VNC Host was not marked as tunnel-encrypted")
	}
}

// TestVNCJumpHostKeyRejectionMatchesSSH proves the routed request carries the
// same per-hop trust callback an SSH Session uses: a changed key the user
// rejects aborts the VNC connection before the stream is handed over.
func TestVNCJumpHostKeyRejectionMatchesSSH(t *testing.T) {
	known := &vncTestKnownHosts{records: []domainconn.KnownHost{{
		ID: "kh-old", Hostname: "bastion.lab", Port: 2222,
		KeyType: "ssh-ed25519", Fingerprint: "old-fingerprint",
	}}}
	prompter := &vncTestPrompter{decision: appconnection.PromptDecision{Trust: false}}
	dialer := &vncTestStreamDialer{
		conn: vncTestStream(t),
		verify: func(req appconnection.DialRequest) error {
			for _, jump := range req.Jumps {
				if jump.VerifyHostKey == nil {
					return errors.New("the routed jump has no host-key verifier")
				}
				if err := jump.VerifyHostKey(context.Background(), domainconn.Key{
					Hostname: jump.Hostname, Port: jump.Port,
					KeyType: "ssh-ed25519", Fingerprint: "new-fingerprint",
				}); err != nil {
					return err
				}
			}
			return nil
		},
	}
	manager := appconnection.NewManager(dialer, nil, known,
		appconnection.WithAuthSource(vncTestAuthSource{}),
		appconnection.WithPrompter(prompter))
	route := appconnection.Route{Jumps: []appconnection.Hop{{
		HostID: "bastion", Label: "bastion", Hostname: "bastion.lab",
		Port: 2222, Username: "ops",
	}}}
	factory := newVNCFactory(adaptervnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("direct dial is forbidden")
	}}, &vncTestRoutes{route: route}, appconnection.NewNetworkProviderRegistry(nil), manager)

	_, err := factory.Open(context.Background(), vncStartSpec())
	if !errors.Is(err, appconnection.ErrHostKeyRejected) {
		t.Fatalf("Open = %v, want ErrHostKeyRejected", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindHostKey {
		t.Fatalf("Open = %v, want a typed host-key failure", err)
	}
	prompter.mu.Lock()
	prompts := append([]domainconn.Key(nil), prompter.keys...)
	prompter.mu.Unlock()
	if len(prompts) != 1 || prompts[0].Status != domainconn.StatusChanged {
		t.Fatalf("prompts = %+v, want one changed-key prompt", prompts)
	}
}

// TestVNCMalformedHostOpensNoRoute checks validation happens before any route
// seam: even a route with jumps, a proxy and a Network Profile starts no SSH
// chain and opens no Provider.
func TestVNCMalformedHostOpensNoRoute(t *testing.T) {
	dialer := &vncTestStreamDialer{}
	provider := &vncTestProvider{err: errors.New("unexpected provider dial")}
	networkFactory := &vncTestNetworkFactory{provider: provider}
	manager := appconnection.NewManager(dialer, nil, &vncTestKnownHosts{},
		appconnection.WithAuthSource(vncTestAuthSource{}))
	route := appconnection.Route{
		Jumps: []appconnection.Hop{{HostID: "bastion", Hostname: "bastion.lab", Port: 22}},
		Proxy: &appconnection.Proxy{Type: appconnection.ProxyHTTP, Host: "proxy.lab", Port: 8080},
		Network: &appconnection.NetworkRoute{
			ProfileID: "net-desktop", Kind: "tailscale", Fingerprint: "fp-vnc",
		},
	}
	directCalls := 0
	inner := adaptervnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		directCalls++
		return nil, errors.New("unexpected direct dial")
	}}
	routes := &vncTestRoutes{route: route}
	factory := newVNCFactory(inner, routes,
		appconnection.NewNetworkProviderRegistry(networkFactory), manager)

	cfg := modelvnc.SessionConfig{Hostname: "", Port: 5901}.Normalized()
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindVNC, HostID: "vnc-desk", VNC: &cfg,
	})
	if !errors.Is(err, modelvnc.ErrInvalidConfig) {
		t.Fatalf("Open = %v, want ErrInvalidConfig", err)
	}
	if dialer.callCount() != 0 {
		t.Fatalf("routed stream dials = %d, want none", dialer.callCount())
	}
	if opens := networkFactory.openCount(); opens != 0 {
		t.Fatalf("provider opens = %d, want none", opens)
	}
	provider.mu.Lock()
	dials := provider.calls
	provider.mu.Unlock()
	if dials != 0 {
		t.Fatalf("provider dials = %d, want none", dials)
	}
	if directCalls != 0 {
		t.Fatalf("direct dials = %d, want none", directCalls)
	}
}

// TestVNCRouteEncryptionCoversSecureWebSocket proves a wss endpoint suppresses
// the plaintext warning the same way a tunnel does, while plain ws does not.
func TestVNCRouteEncryptionCoversSecureWebSocket(t *testing.T) {
	webSocket := func(secure bool) modelvnc.SessionConfig {
		configuration := modelvnc.DefaultConfig()
		configuration.WebSocket = true
		configuration.WebSocketSecure = secure
		return modelvnc.SessionConfig{
			Hostname: "desk.local", Port: modelvnc.DefaultWebSocketPort,
			Configuration: configuration,
		}.Normalized()
	}
	if !routeIsEncrypted(appconnection.Route{}, webSocket(true)) {
		t.Fatal("a wss endpoint was treated as plaintext")
	}
	if routeIsEncrypted(appconnection.Route{}, webSocket(false)) {
		t.Fatal("a ws endpoint was treated as encrypted")
	}
	raw := modelvnc.SessionConfig{Hostname: "desk.local", Port: 5901}.Normalized()
	if routeIsEncrypted(appconnection.Route{}, raw) {
		t.Fatal("a direct raw RFB endpoint was treated as encrypted")
	}
	if !routeIsEncrypted(appconnection.Route{Jumps: []appconnection.Hop{{Hostname: "bastion"}}}, raw) {
		t.Fatal("a jump chain was treated as plaintext")
	}
}
