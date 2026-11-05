// SPDX-License-Identifier: GPL-3.0-or-later

package wireguard

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

const (
	// 32 bytes of one value, in the hexadecimal form the adapter normalizes to.
	testPrivateKey = "0101010101010101010101010101010101010101010101010101010101010101"
	testPeerKey1   = "0202020202020202020202020202020202020202020202020202020202020202"
	testPeerKey2   = "0404040404040404040404040404040404040404040404040404040404040404"
	testPSK        = "0303030303030303030303030303030303030303030303030303030303030303"

	testResolvedEndpoint = "203.0.113.7:51820"
)

// fakeResolver is the system-resolver seam used for peer Endpoints only.
type fakeResolver struct {
	mu        sync.Mutex
	answers   map[string][]netip.Addr
	err       error
	calls     []string
	callCount int
}

func (r *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callCount++
	r.calls = append(r.calls, host)
	if r.err != nil {
		return nil, r.err
	}
	return append([]netip.Addr(nil), r.answers[host]...), nil
}

func (r *fakeResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callCount
}

// stubConn is a net.Conn the dial tests never read from or write to.
type stubConn struct{}

func (stubConn) Read([]byte) (int, error)         { return 0, errors.New("stub") }
func (stubConn) Write(p []byte) (int, error)      { return len(p), nil }
func (stubConn) Close() error                     { return nil }
func (stubConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (stubConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (stubConn) SetDeadline(time.Time) error      { return nil }
func (stubConn) SetReadDeadline(time.Time) error  { return nil }
func (stubConn) SetWriteDeadline(time.Time) error { return nil }

type stubAddr string

func (a stubAddr) Network() string { return "stub" }
func (a stubAddr) String() string  { return string(a) }

// fakeBackend records the calls the Provider makes and can fail on demand.
type fakeBackend struct {
	mu sync.Mutex

	config   backendConfig
	started  bool
	closed   bool
	startErr error
	status   backendStatus
	statusFn func(context.Context) (backendStatus, error)

	dialFn    func(context.Context, string, string) (net.Conn, error)
	dialCalls []string
	startHook func()

	endpointUpdates []map[string]string
	closeCalls      int
	startCalls      int
}

func newFakeBackend(config backendConfig) *fakeBackend {
	return &fakeBackend{
		config: config,
		status: backendStatus{Phase: appconnection.ProviderPhaseRunning},
	}
}

func (b *fakeBackend) Start(ctx context.Context) (backendStatus, error) {
	b.mu.Lock()
	b.startCalls++
	hook := b.startHook
	b.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err := ctx.Err(); err != nil {
		return backendStatus{Phase: appconnection.ProviderPhaseStopped}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.startErr != nil {
		return backendStatus{Phase: appconnection.ProviderPhaseError}, b.startErr
	}
	b.started = true
	return b.status, nil
}

func (b *fakeBackend) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	b.mu.Lock()
	b.dialCalls = append(b.dialCalls, network+" "+address)
	fn := b.dialFn
	b.mu.Unlock()
	if fn != nil {
		return fn(ctx, network, address)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return stubConn{}, nil
}

func (b *fakeBackend) Status(ctx context.Context) (backendStatus, error) {
	b.mu.Lock()
	fn := b.statusFn
	status := b.status
	b.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return status, nil
}

func (b *fakeBackend) SetEndpoints(_ context.Context, endpoints map[string]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	copied := make(map[string]string, len(endpoints))
	for key, value := range endpoints {
		copied[key] = value
	}
	b.endpointUpdates = append(b.endpointUpdates, copied)
	return nil
}

func (b *fakeBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeCalls++
	b.closed = true
	b.started = false
	return nil
}

func (b *fakeBackend) dialCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.dialCalls)
}

func (b *fakeBackend) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeCalls
}

func (b *fakeBackend) setStatus(status backendStatus) {
	b.mu.Lock()
	b.status = status
	b.mu.Unlock()
}

func (b *fakeBackend) setStatusFunc(fn func(context.Context) (backendStatus, error)) {
	b.mu.Lock()
	b.statusFn = fn
	b.mu.Unlock()
}

// fakeFactory creates fake backends and records their configurations.
type fakeFactory struct {
	mu       sync.Mutex
	backends []*fakeBackend
	configs  []backendConfig
	err      error
	setup    func(*fakeBackend)
}

func (f *fakeFactory) newBackend(config backendConfig) (backend, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	backend := newFakeBackend(config)
	if f.setup != nil {
		f.setup(backend)
	}
	f.backends = append(f.backends, backend)
	f.configs = append(f.configs, config)
	return backend, nil
}

func (f *fakeFactory) backend(index int) *fakeBackend {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.backends[index]
}

func (f *fakeFactory) backendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.backends)
}

func (f *fakeFactory) config(index int) backendConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.configs[index]
}

// testConfig is a valid profile with one host-name peer and a tunnel DNS
// server. Tests override the parts they exercise.
func testConfig() Config {
	return Config{
		ProfileID:  "net-1",
		Label:      "Office",
		Kind:       domainvault.NetworkKindWireGuard,
		Addresses:  []string{"10.9.0.2/32"},
		DNS:        []string{"10.9.0.1"},
		PrivateKey: testPrivateKey,
		Peers: []Peer{{
			ID:                  "peer-1",
			PublicKey:           testPeerKey1,
			Endpoint:            "vpn.example.com:51820",
			AllowedIPs:          []string{"10.9.0.0/24", "0.0.0.0/0"},
			PersistentKeepalive: 25,
		}},
	}
}

func testResolver() *fakeResolver {
	return &fakeResolver{answers: map[string][]netip.Addr{
		"vpn.example.com": {netip.MustParseAddr("203.0.113.7")},
		"v6.example.com":  {netip.MustParseAddr("fd00::7")},
	}}
}

func newTestProvider(t *testing.T, config Config, factory *fakeFactory,
	resolver *fakeResolver, opts ...Option) *Provider {
	t.Helper()
	if factory == nil {
		factory = &fakeFactory{}
	}
	if resolver == nil {
		resolver = testResolver()
	}
	options := []Option{
		withBackendFactory(factory.newBackend),
		withEndpointResolver(resolver),
		withStatusPollInterval(20 * time.Millisecond),
	}
	provider, err := NewProvider(config, append(options, opts...)...)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

func waitForStatus(t *testing.T, events <-chan Status, timeout time.Duration,
	predicate func(Status) bool) Status {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case status := <-events:
			if predicate(status) {
				return status
			}
		case <-deadline:
			t.Fatalf("timed out after %s waiting for a provider status", timeout)
			return Status{}
		}
	}
}

func observeInto(provider *Provider) <-chan Status {
	events := make(chan Status, 1024)
	provider.observe = func(status Status) {
		select {
		case events <- status:
		default:
		}
	}
	return events
}

func TestNewProviderRejectsInvalidConfigWithoutEchoingSecrets(t *testing.T) {
	base := testConfig()
	tests := []struct {
		name   string
		mutate func(*Config)
		want   error
	}{
		{"no profile id", func(c *Config) { c.ProfileID = " " }, ErrInvalidConfig},
		{"wrong kind", func(c *Config) { c.Kind = domainvault.NetworkKindTailscale }, ErrInvalidConfig},
		{"no addresses", func(c *Config) { c.Addresses = nil }, ErrInvalidConfig},
		{"bad address", func(c *Config) { c.Addresses = []string{"not-an-ip"} }, ErrInvalidConfig},
		{"bad mtu", func(c *Config) { c.MTU = 100 }, ErrInvalidConfig},
		{"bad listen port", func(c *Config) { c.ListenPort = 70000 }, ErrInvalidConfig},
		{"missing private key", func(c *Config) { c.PrivateKey = "" }, ErrPrivateKeyMissing},
		{"bad private key", func(c *Config) { c.PrivateKey = "too-short" }, ErrInvalidConfig},
		{"no peers", func(c *Config) { c.Peers = nil }, ErrInvalidConfig},
		{"bad peer key", func(c *Config) { c.Peers[0].PublicKey = "nope" }, ErrInvalidConfig},
		{"duplicate peer key", func(c *Config) {
			c.Peers = append(c.Peers, Peer{ID: "peer-2", PublicKey: testPeerKey1,
				AllowedIPs: []string{"10.8.0.0/24"}})
		}, ErrInvalidConfig},
		{"bad psk", func(c *Config) { c.Peers[0].PresharedKey = "nope" }, ErrInvalidConfig},
		{"bad endpoint", func(c *Config) { c.Peers[0].Endpoint = "vpn.example.com" }, ErrInvalidConfig},
		{"zero endpoint port", func(c *Config) { c.Peers[0].Endpoint = "vpn.example.com:0" }, ErrInvalidConfig},
		{"no allowed ips", func(c *Config) { c.Peers[0].AllowedIPs = nil }, ErrInvalidConfig},
		{"bad allowed ip", func(c *Config) { c.Peers[0].AllowedIPs = []string{"x"} }, ErrInvalidConfig},
		{"bad dns", func(c *Config) { c.DNS = []string{"resolver.example.com"} }, ErrInvalidConfig},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := base
			config.Peers = append([]Peer(nil), base.Peers...)
			test.mutate(&config)
			provider, err := NewProvider(config, withEndpointResolver(testResolver()))
			if provider != nil {
				t.Fatal("an invalid config produced a Provider")
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), testPrivateKey) {
				t.Fatalf("the error echoes the Private Key: %v", err)
			}
			if strings.Contains(err.Error(), testPSK) {
				t.Fatalf("the error echoes a Preshared Key: %v", err)
			}
		})
	}
}

func TestNewProviderAcceptsBase64AndHexKeys(t *testing.T) {
	key, _ := decodeKey(testPrivateKey)
	privateKeyBase64 := base64.StdEncoding.EncodeToString(key[:])

	config := testConfig()
	config.PrivateKey = privateKeyBase64
	config.Peers[0].PresharedKey = testPSK
	provider, err := NewProvider(config, withEndpointResolver(testResolver()))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if provider.plan.privateKeyHex != testPrivateKey {
		t.Fatalf("private key hex = %q, want the hex of the base64 key", provider.plan.privateKeyHex)
	}
	if provider.plan.peers[0].presharedKeyHex != testPSK {
		t.Fatalf("preshared key hex = %q, want %q", provider.plan.peers[0].presharedKeyHex, testPSK)
	}
	// The plan keeps endpoint metadata but no secrets are needed to start.
	if provider.plan.peers[0].endpointHost != "vpn.example.com" ||
		provider.plan.peers[0].endpointPort != 51820 ||
		provider.plan.peers[0].endpointIsIP {
		t.Fatalf("peer endpoint = %+v, want vpn.example.com:51820 as a host name",
			provider.plan.peers[0])
	}
}

func TestValidateForNetstack(t *testing.T) {
	if err := ValidateForNetstack(testConfig()); err != nil {
		t.Fatalf("ValidateForNetstack rejected a valid config: %v", err)
	}
	config := testConfig()
	config.Peers = nil
	if err := ValidateForNetstack(config); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("ValidateForNetstack error = %v, want ErrInvalidConfig", err)
	}
}

func TestProviderStartResolvesHostnameEndpointsAndAppliesTheProfile(t *testing.T) {
	config := testConfig()
	config.Peers[0].PresharedKey = testPSK
	config.ListenPort = 51820
	config.MTU = 1300
	factory := &fakeFactory{}
	resolver := testResolver()
	provider := newTestProvider(t, config, factory, resolver)

	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if factory.backendCount() != 1 {
		t.Fatalf("created %d backends, want 1", factory.backendCount())
	}
	backendConfig := factory.config(0)
	if len(backendConfig.LocalAddresses) != 1 ||
		backendConfig.LocalAddresses[0] != netip.MustParseAddr("10.9.0.2") {
		t.Fatalf("local addresses = %v, want 10.9.0.2", backendConfig.LocalAddresses)
	}
	if len(backendConfig.DNSServers) != 1 ||
		backendConfig.DNSServers[0] != netip.MustParseAddr("10.9.0.1") {
		t.Fatalf("dns servers = %v, want 10.9.0.1", backendConfig.DNSServers)
	}
	if backendConfig.MTU != 1300 || backendConfig.ListenPort != 51820 {
		t.Fatalf("mtu/port = %d/%d, want 1300/51820", backendConfig.MTU, backendConfig.ListenPort)
	}
	if backendConfig.PrivateKeyHex != testPrivateKey {
		t.Fatal("the backend did not receive the private key in hexadecimal form")
	}
	if len(backendConfig.Peers) != 1 {
		t.Fatalf("backend peers = %d, want 1", len(backendConfig.Peers))
	}
	peer := backendConfig.Peers[0]
	if peer.Endpoint != testResolvedEndpoint {
		t.Fatalf("endpoint = %q, want the resolved %q", peer.Endpoint, testResolvedEndpoint)
	}
	if peer.PresharedKeyHex != testPSK {
		t.Fatal("the backend did not receive the preshared key")
	}
	if peer.PersistentKeepalive != 25 {
		t.Fatalf("keepalive = %d, want 25", peer.PersistentKeepalive)
	}
	if len(peer.AllowedIPs) != 2 || peer.AllowedIPs[0] != netip.MustParsePrefix("10.9.0.0/24") ||
		peer.AllowedIPs[1] != netip.MustParsePrefix("0.0.0.0/0") {
		t.Fatalf("allowed ips = %v", peer.AllowedIPs)
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != "vpn.example.com" {
		t.Fatalf("resolver calls = %v, want one lookup of the peer endpoint", resolver.calls)
	}
	if provider.Phase() != appconnection.ProviderPhaseRunning {
		t.Fatalf("phase = %q, want running", provider.Phase())
	}
}

func TestProviderStartFailsClosedWhenAnEndpointCannotResolve(t *testing.T) {
	config := testConfig()
	factory := &fakeFactory{}
	resolver := &fakeResolver{err: errors.New("no such host")}
	provider := newTestProvider(t, config, factory, resolver)

	err := provider.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded with an unresolvable peer endpoint")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("error = %v, want KindProvider", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "net-1" ||
		typed.Provider.Kind != string(domainvault.NetworkKindWireGuard) ||
		typed.Provider.Phase != appconnection.ProviderPhaseError {
		t.Fatalf("ProviderInfo = %+v, want the failed profile in the error phase", typed.Provider)
	}
	if !errors.Is(err, ErrEndpointResolution) {
		t.Fatalf("error = %v, want ErrEndpointResolution", err)
	}
	if factory.backendCount() != 0 {
		t.Fatal("a backend was created for an unresolved endpoint")
	}
	if provider.Phase() != appconnection.ProviderPhaseError {
		t.Fatalf("phase = %q, want error", provider.Phase())
	}
	if provider.Status().Err == nil {
		t.Fatal("the status does not carry the failure")
	}
}

func TestProviderStartRejectsAnEndpointWithNoLocalAddressFamily(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "[fd00::7]:51820"
	factory := &fakeFactory{}
	provider := newTestProvider(t, config, factory, nil)

	err := provider.Start(context.Background())
	if !errors.Is(err, ErrEndpointResolution) {
		t.Fatalf("error = %v, want ErrEndpointResolution", err)
	}
	if factory.backendCount() != 0 {
		t.Fatal("a backend was created without an IPv6 local address")
	}
}

func TestProviderStartClosesTheBackendOnFailureAndRestarts(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	failure := errors.New("device refused to start")
	factory := &fakeFactory{setup: func(b *fakeBackend) { b.startErr = failure }}
	provider := newTestProvider(t, config, factory, nil)

	if err := provider.Start(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("Start error = %v, want the backend failure", err)
	}
	if factory.backend(0).closeCount() != 1 {
		t.Fatalf("the failed backend was closed %d times, want 1", factory.backend(0).closeCount())
	}
	if provider.Phase() != appconnection.ProviderPhaseError {
		t.Fatalf("phase = %q, want error", provider.Phase())
	}

	// A later Start builds a fresh device and succeeds.
	factory.mu.Lock()
	factory.setup = nil
	factory.mu.Unlock()
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if factory.backendCount() != 2 {
		t.Fatalf("created %d backends, want a fresh one after the failure", factory.backendCount())
	}
	if provider.Phase() != appconnection.ProviderPhaseRunning {
		t.Fatalf("phase after restart = %q, want running", provider.Phase())
	}
}

func TestProviderConcurrentStartsShareOneBackend(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	provider, err := NewProvider(config,
		withEndpointResolver(testResolver()),
		withStatusPollInterval(time.Hour),
		withBackendFactory(func(config backendConfig) (backend, error) {
			b := newFakeBackend(config)
			b.mu.Lock()
			b.startHook = func() {
				once.Do(func() { close(entered) })
				<-release
			}
			b.mu.Unlock()
			return b, nil
		}))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	first := make(chan error, 1)
	go func() { first <- provider.Start(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first Start never reached the backend")
	}

	var waiters sync.WaitGroup
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		waiters.Add(1)
		go func() {
			defer waiters.Done()
			results <- provider.Start(context.Background())
		}()
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first Start: %v", err)
	}
	waiters.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent Start: %v", err)
		}
	}
}

func TestProviderDialLazilyStartsAndPassesTCPThroughTheTunnel(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	factory := &fakeFactory{}
	provider := newTestProvider(t, config, factory, nil)
	if provider.Phase() != appconnection.ProviderPhaseStopped {
		t.Fatalf("a new Provider is %q, want stopped", provider.Phase())
	}

	conn, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = conn.Close()
	if factory.backendCount() != 1 {
		t.Fatalf("the dial started %d backends, want 1", factory.backendCount())
	}
	if got := factory.backend(0).dialCalls; len(got) != 1 || got[0] != "tcp 10.9.0.10:22" {
		t.Fatalf("backend dials = %v", got)
	}
}

func TestProviderDialRejectsNetworksThePortDoesNotCarry(t *testing.T) {
	provider := newTestProvider(t, testConfig(), nil, nil)
	for _, network := range []string{"udp", "udp4", "unix", ""} {
		_, err := provider.DialContext(context.Background(), network, "10.9.0.10:53")
		var typed *appconnection.Error
		if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
			t.Fatalf("%q: error = %v, want KindUnsupported", network, err)
		}
		if !errors.Is(err, ErrUnsupportedNetwork) {
			t.Fatalf("%q: error = %v, want ErrUnsupportedNetwork", network, err)
		}
		if typed.Provider == nil || typed.Provider.Kind != string(domainvault.NetworkKindWireGuard) {
			t.Fatalf("%q: ProviderInfo = %+v", network, typed.Provider)
		}
	}
}

func TestProviderDialWithoutTunnelDNSFailsBeforeSendingAnything(t *testing.T) {
	config := testConfig()
	config.DNS = nil
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	factory := &fakeFactory{}
	provider := newTestProvider(t, config, factory, nil)

	conn, err := provider.DialContext(context.Background(), "tcp", "internal.example.com:22")
	if conn != nil {
		t.Fatal("a dial without tunnel DNS returned a connection")
	}
	if !errors.Is(err, ErrDNSUnavailable) {
		t.Fatalf("error = %v, want ErrDNSUnavailable", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("error = %v, want KindProvider", err)
	}
	if factory.backend(0).dialCount() != 0 {
		t.Fatal("a host-name target reached the netstack without tunnel DNS")
	}

	// An IP-literal target still works: it needs no resolver at all.
	conn, err = provider.DialContext(context.Background(), "tcp", "10.9.0.10:22")
	if err != nil {
		t.Fatalf("IP-literal dial: %v", err)
	}
	_ = conn.Close()
	if factory.backend(0).dialCount() != 1 {
		t.Fatalf("dials = %d, want the IP literal to reach the tunnel", factory.backend(0).dialCount())
	}
}

func TestProviderDialWithTunnelDNSHandsTheHostNameToTheNetstack(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	factory := &fakeFactory{}
	provider := newTestProvider(t, config, factory, nil)

	conn, err := provider.DialContext(context.Background(), "tcp", "internal.example.com:22")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = conn.Close()
	if got := factory.backend(0).dialCalls; len(got) != 1 ||
		got[0] != "tcp internal.example.com:22" {
		t.Fatalf("backend dials = %v, want the host name handed to the tunnel resolver", got)
	}
}

func TestProviderDialClassifiesFailures(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	factory := &fakeFactory{}
	provider := newTestProvider(t, config, factory, nil)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	backend := factory.backend(0)
	backend.mu.Lock()
	backend.dialFn = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	}
	backend.mu.Unlock()
	_, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22")
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindNetwork {
		t.Fatalf("error = %v, want KindNetwork", err)
	}
	if typed.Provider == nil || typed.Provider.Phase != appconnection.ProviderPhaseRunning {
		t.Fatalf("ProviderInfo = %+v, want the running profile", typed.Provider)
	}

	deadlineCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = provider.DialContext(deadlineCtx, "tcp", "10.9.0.10:22")
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindTimeout {
		t.Fatalf("expired deadline error = %v, want KindTimeout", err)
	}

	cancelledCtx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	_, err = provider.DialContext(cancelledCtx, "tcp", "10.9.0.10:22")
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindCancelled {
		t.Fatalf("cancelled error = %v, want KindCancelled", err)
	}
}

func TestProviderDialAfterRuntimeFailureRestartsTheTunnel(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	failure := errors.New("tunnel died")
	factory := &fakeFactory{}
	provider := newTestProvider(t, config, factory, nil,
		withStatusPollInterval(5*time.Millisecond))
	events := observeInto(provider)

	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	factory.backend(0).setStatusFunc(func(context.Context) (backendStatus, error) {
		return backendStatus{}, failure
	})
	waitForStatus(t, events, 5*time.Second, func(status Status) bool {
		return status.Phase == appconnection.ProviderPhaseError
	})
	if provider.Phase() != appconnection.ProviderPhaseError {
		t.Fatalf("phase = %q, want error", provider.Phase())
	}

	// The next dial builds a fresh device rather than reusing the dead one.
	conn, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22")
	if err != nil {
		t.Fatalf("Dial after restart: %v", err)
	}
	_ = conn.Close()
	if factory.backendCount() != 2 {
		t.Fatalf("created %d backends, want a fresh device after the failure", factory.backendCount())
	}
	if provider.Phase() != appconnection.ProviderPhaseRunning {
		t.Fatalf("phase = %q, want running", provider.Phase())
	}
}

func TestProviderCloseIsIdempotentAndReleasesTheDeviceOnce(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	factory := &fakeFactory{}
	provider := newTestProvider(t, config, factory, nil)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if factory.backend(0).closeCount() != 1 {
		t.Fatalf("the backend was closed %d times, want 1", factory.backend(0).closeCount())
	}
	if provider.Phase() != appconnection.ProviderPhaseStopped {
		t.Fatalf("phase = %q, want stopped", provider.Phase())
	}
	if err := provider.Start(context.Background()); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("Start after Close = %v, want ErrProviderClosed", err)
	}
	_, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22")
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("Dial after Close = %v, want KindProvider", err)
	}
	if !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("Dial after Close = %v, want ErrProviderClosed", err)
	}
}

func TestProviderStatusMapsPeersAndNeverCarriesSecrets(t *testing.T) {
	config := testConfig()
	config.Peers = append(config.Peers, Peer{
		ID:         "peer-2",
		PublicKey:  testPeerKey2,
		AllowedIPs: []string{"10.8.0.0/24"},
	})
	config.Peers[0].PresharedKey = testPSK
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	handshake := time.Unix(1_700_000_000, 123)
	factory := &fakeFactory{setup: func(b *fakeBackend) {
		b.status = backendStatus{
			Phase:      appconnection.ProviderPhaseRunning,
			ListenPort: 4242,
			Peers: []backendPeerStatus{
				{PublicKeyHex: testPeerKey2, RxBytes: 5, TxBytes: 6},
				{PublicKeyHex: testPeerKey1, Endpoint: "203.0.113.7:51820",
					LastHandshake: handshake, RxBytes: 100, TxBytes: 200},
			},
		}
	}}
	provider := newTestProvider(t, config, factory, nil)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	status := provider.Status()
	if status.ListenPort != 4242 || status.Phase != appconnection.ProviderPhaseRunning {
		t.Fatalf("status = %+v, want the running device on port 4242", status)
	}
	// Configured order is preserved, not the UAPI map's order.
	if len(status.Peers) != 2 || status.Peers[0].ID != "peer-1" || status.Peers[1].ID != "peer-2" {
		t.Fatalf("peers = %+v, want peer-1 then peer-2", status.Peers)
	}
	first := status.Peers[0]
	if first.Endpoint != "203.0.113.7:51820" || first.Handshake != HandshakeEstablished ||
		!first.LastHandshake.Equal(handshake) || first.RxBytes != 100 || first.TxBytes != 200 {
		t.Fatalf("peer-1 status = %+v", first)
	}
	if status.Peers[1].Handshake != HandshakePending {
		t.Fatalf("peer-2 handshake = %q, want pending", status.Peers[1].Handshake)
	}
	if status.Handshake != HandshakeEstablished || !status.LastHandshake.Equal(handshake) {
		t.Fatalf("aggregate handshake = %q at %v", status.Handshake, status.LastHandshake)
	}
	if status.RxBytes != 105 || status.TxBytes != 206 {
		t.Fatalf("aggregate bytes = %d/%d, want 105/206", status.RxBytes, status.TxBytes)
	}
	if status.Endpoint() != "203.0.113.7:51820" {
		t.Fatalf("Endpoint() = %q, want the first peer with an endpoint", status.Endpoint())
	}

	rendered := fmt.Sprintf("%+v %s", status, status.Err)
	if strings.Contains(rendered, testPrivateKey) || strings.Contains(rendered, testPSK) {
		t.Fatalf("the status leaks key material: %s", rendered)
	}
}

func TestStatusEndpointPrefersTheMostRecentlyHandshakenPeer(t *testing.T) {
	older := time.Unix(1_700_000_000, 0)
	newer := older.Add(time.Minute)
	status := Status{Peers: []PeerStatus{
		{ID: "peer-1", Endpoint: "198.51.100.1:51820",
			Handshake: HandshakeEstablished, LastHandshake: older},
		{ID: "peer-2", Endpoint: "203.0.113.9:51820",
			Handshake: HandshakeEstablished, LastHandshake: newer},
	}}
	if got := status.Endpoint(); got != "203.0.113.9:51820" {
		t.Fatalf("Endpoint() = %q, want the most recently handshaken peer", got)
	}

	// Before any handshake the first configured Endpoint is the best guess.
	pending := Status{Peers: []PeerStatus{
		{ID: "peer-1", Endpoint: "198.51.100.1:51820", Handshake: HandshakePending},
		{ID: "peer-2", Endpoint: "203.0.113.9:51820", Handshake: HandshakePending},
	}}
	if got := pending.Endpoint(); got != "198.51.100.1:51820" {
		t.Fatalf("pending Endpoint() = %q, want the first configured endpoint", got)
	}

	// A peer that has handshaken beats a configured peer that has not, no
	// matter its position in the profile.
	mixed := Status{Peers: []PeerStatus{
		{ID: "peer-1", Endpoint: "198.51.100.1:51820", Handshake: HandshakePending},
		{ID: "peer-2", Endpoint: "203.0.113.9:51820",
			Handshake: HandshakeEstablished, LastHandshake: older},
	}}
	if got := mixed.Endpoint(); got != "203.0.113.9:51820" {
		t.Fatalf("mixed Endpoint() = %q, want the handshaken peer", got)
	}
}

func TestProviderStatusObserverReportsLiveChanges(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	factory := &fakeFactory{}
	provider := newTestProvider(t, config, factory, nil,
		withStatusPollInterval(5*time.Millisecond))
	events := observeInto(provider)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForStatus(t, events, 5*time.Second, func(status Status) bool {
		return status.Phase == appconnection.ProviderPhaseRunning
	})

	handshake := time.Unix(1_700_000_100, 0)
	factory.backend(0).setStatus(backendStatus{
		Phase: appconnection.ProviderPhaseRunning,
		Peers: []backendPeerStatus{{
			PublicKeyHex:  testPeerKey1,
			Endpoint:      "203.0.113.7:51820",
			LastHandshake: handshake,
			RxBytes:       10,
			TxBytes:       20,
		}},
	})
	status := waitForStatus(t, events, 5*time.Second, func(status Status) bool {
		return status.Handshake == HandshakeEstablished
	})
	if !status.LastHandshake.Equal(handshake) || status.RxBytes != 10 || status.TxBytes != 20 {
		t.Fatalf("live status = %+v", status)
	}
}

func TestProviderRedactsSecretsFromErrorsAndLogs(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	config.Peers[0].PresharedKey = testPSK
	var logs []string
	var logsMu sync.Mutex
	factory := &fakeFactory{setup: func(b *fakeBackend) {
		b.dialFn = func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, fmt.Errorf("dial with private %s and psk %s failed", testPrivateKey, testPSK)
		}
	}}
	provider := newTestProvider(t, config, factory, nil, WithLogf(func(format string, args ...any) {
		logsMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logsMu.Unlock()
	}))

	_, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22")
	if err == nil {
		t.Fatal("the dial unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), testPrivateKey) {
		t.Fatalf("the error carries the Private Key: %v", err)
	}
	if strings.Contains(err.Error(), testPSK) {
		t.Fatalf("the error carries the Preshared Key: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("the error was not redacted: %v", err)
	}

	// The device logger is wrapped by the same redactor.
	backendConfig := factory.config(0)
	if backendConfig.Logf == nil {
		t.Fatal("the device logger is not wired")
	}
	backendConfig.Logf("configured with %s and %s", testPrivateKey, testPSK)
	logsMu.Lock()
	joined := strings.Join(logs, "\n")
	logsMu.Unlock()
	if strings.Contains(joined, testPrivateKey) || strings.Contains(joined, testPSK) {
		t.Fatalf("the device log leaks key material: %s", joined)
	}
	if !strings.Contains(joined, "[redacted]") {
		t.Fatalf("the device log was not redacted: %s", joined)
	}
}

// TestProviderWritesNoPlaintextFiles pins WireGuard's no-persistence model:
// starting a device with a Private Key and Preshared Key creates no file in the
// process working directory. The netstack backend keeps its state in memory;
// there is no TUN device and no state directory to protect.
func TestProviderWritesNoPlaintextFiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	config := testConfig()
	config.Peers[0].PresharedKey = testPSK
	provider := newTestProvider(t, config, &fakeFactory{}, nil)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var written []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			written = append(written, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk working directory: %v", err)
	}
	if len(written) != 0 {
		t.Fatalf("WireGuard wrote plaintext files: %v", written)
	}
	// The keys remain in memory for the running device; no exported field or
	// status string renders them.
	rendered := fmt.Sprintf("%+v", provider.Status())
	if strings.Contains(rendered, testPrivateKey) || strings.Contains(rendered, testPSK) {
		t.Fatalf("the live status carries key material: %s", rendered)
	}
}

func TestProviderRefreshesAHostNameEndpointAfterItsTTL(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "vpn.example.com:51820"
	resolver := testResolver()
	factory := &fakeFactory{}
	clock := time.Unix(1_700_000_000, 0)
	var clockMu sync.Mutex
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	provider := newTestProvider(t, config, factory, resolver,
		withEndpointTTL(30*time.Second), withClock(now))
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if resolver.count() != 1 {
		t.Fatalf("initial resolutions = %d, want 1", resolver.count())
	}

	// Within the TTL the cached resolution is reused.
	if _, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22"); err != nil {
		t.Fatalf("Dial within TTL: %v", err)
	}
	if resolver.count() != 1 {
		t.Fatalf("resolutions within TTL = %d, want the cached 1", resolver.count())
	}

	resolver.mu.Lock()
	resolver.answers["vpn.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.8")}
	resolver.mu.Unlock()
	clockMu.Lock()
	clock = clock.Add(31 * time.Second)
	clockMu.Unlock()

	if _, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22"); err != nil {
		t.Fatalf("Dial after TTL: %v", err)
	}
	if resolver.count() != 2 {
		t.Fatalf("resolutions after TTL = %d, want a refresh", resolver.count())
	}
	updates := factory.backend(0).endpointUpdates
	if len(updates) != 1 || updates[0][testPeerKey1] != "203.0.113.8:51820" {
		t.Fatalf("endpoint updates = %v, want the re-resolved address", updates)
	}
}

func TestProviderReResolvesAHostNameEndpointAfterAFailedDial(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "vpn.example.com:51820"
	resolver := testResolver()
	factory := &fakeFactory{setup: func(b *fakeBackend) {
		b.dialFn = func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("network is unreachable")
		}
	}}
	clock := time.Unix(1_700_000_000, 0)
	var clockMu sync.Mutex
	provider := newTestProvider(t, config, factory, resolver,
		withEndpointTTL(time.Hour), withClock(func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return clock
		}))
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if resolver.count() != 1 {
		t.Fatalf("initial resolutions = %d, want 1", resolver.count())
	}

	// A dial within the TTL still reuses the cached resolution...
	if _, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22"); err == nil {
		t.Fatal("the failing dial succeeded")
	}
	if resolver.count() != 1 {
		t.Fatalf("resolutions during the failing dial = %d, want the cached 1",
			resolver.count())
	}
	// ...but its failure marks the address stale, so the next attempt
	// re-resolves instead of reusing a dead peer address for the rest of the TTL.
	if _, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22"); err == nil {
		t.Fatal("the second failing dial succeeded")
	}
	if resolver.count() != 2 {
		t.Fatalf("resolutions after the second dial = %d, want a refresh", resolver.count())
	}
}

func TestProviderRefreshFailureFailsClosed(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "vpn.example.com:51820"
	resolver := testResolver()
	factory := &fakeFactory{}
	clock := time.Unix(1_700_000_000, 0)
	var clockMu sync.Mutex
	provider := newTestProvider(t, config, factory, resolver,
		withEndpointTTL(0), withClock(func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return clock
		}))
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	resolver.mu.Lock()
	resolver.err = errors.New("resolver offline")
	resolver.mu.Unlock()

	_, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22")
	if !errors.Is(err, ErrEndpointResolution) {
		t.Fatalf("error = %v, want ErrEndpointResolution", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("error = %v, want KindProvider", err)
	}
	if factory.backend(0).dialCount() != 0 {
		t.Fatal("the target was dialed after the endpoint refresh failed")
	}
}

func TestDecodeKeyAndParsing(t *testing.T) {
	if _, ok := decodeKey(testPrivateKey); !ok {
		t.Fatal("a 64-character hex key was rejected")
	}
	decoded, _ := decodeKey(testPrivateKey)
	if base64.StdEncoding.EncodeToString(decoded[:]) != base64.StdEncoding.EncodeToString(mustHex(t, testPrivateKey)) {
		t.Fatal("hex and base64 decoding disagree")
	}
	if _, ok := decodeKey("abc"); ok {
		t.Fatal("a short key was accepted")
	}
	if host, port, isIP, err := parseEndpoint("vpn.example.com:51820"); err != nil ||
		host != "vpn.example.com" || port != 51820 || isIP {
		t.Fatalf("parseEndpoint = %q %d %v %v", host, port, isIP, err)
	}
	if host, port, isIP, err := parseEndpoint("[fd00::7]:51820"); err != nil ||
		host != "fd00::7" || port != 51820 || !isIP {
		t.Fatalf("parseEndpoint v6 = %q %d %v %v", host, port, isIP, err)
	}
	if _, _, _, err := parseEndpoint("vpn.example.com"); err == nil {
		t.Fatal("an endpoint without a port was accepted")
	}
	if _, _, _, err := parseEndpoint("vpn.example.com:0"); err == nil {
		t.Fatal("an endpoint with port zero was accepted")
	}
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	return decoded
}

// TestProviderDialChecksALivePhaseEvenWhenBackendStatusFails is the regression
// for a dial racing device death: netstackBackend.Status returns
// (PhaseError, "device stopped unexpectedly"), so requiring statusErr == nil
// before reading the phase classified the dead tunnel as KindNetwork with a
// stale running phase.
func TestProviderDialChecksALivePhaseEvenWhenBackendStatusFails(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	factory := &fakeFactory{}
	provider := newTestProvider(t, config, factory, nil)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	backend := factory.backend(0)
	backend.mu.Lock()
	backend.dialFn = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("wireguard: device is not running")
	}
	backend.statusFn = func(context.Context) (backendStatus, error) {
		return backendStatus{Phase: appconnection.ProviderPhaseError},
			errors.New("wireguard: device stopped unexpectedly")
	}
	backend.mu.Unlock()

	conn, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22")
	if conn != nil {
		t.Fatal("a dial racing device death returned a connection")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not typed", err)
	}
	if typed.Kind != appconnection.KindProvider {
		t.Fatalf("Kind = %q, want provider: %v", typed.Kind, err)
	}
	if typed.Provider == nil || typed.Provider.Phase != appconnection.ProviderPhaseError {
		t.Fatalf("ProviderInfo = %+v, want the live error phase", typed.Provider)
	}
	if typed.Provider.ProfileID != config.ProfileID {
		t.Fatalf("ProfileID = %q, want %q", typed.Provider.ProfileID, config.ProfileID)
	}
	if provider.Phase() != appconnection.ProviderPhaseError {
		t.Fatalf("Provider phase = %q, want error", provider.Phase())
	}
	if provider.Status().Err == nil {
		t.Fatal("the Provider status does not carry the failure")
	}
	if factory.backendCount() != 1 {
		t.Fatalf("a failed dial opened %d backends; it must not retry outside the tunnel",
			factory.backendCount())
	}
}

// TestProviderConnClassifiesIOFailuresAfterClose proves an active connection
// reports KindProvider on its next I/O when its Provider was closed, instead of
// an untyped transport error that could invite a direct retry.
func TestProviderConnClassifiesIOFailuresAfterClose(t *testing.T) {
	config := testConfig()
	config.Peers[0].Endpoint = "198.51.100.9:51820"
	factory := &fakeFactory{}
	provider := newTestProvider(t, config, factory, nil)

	conn, err := provider.DialContext(context.Background(), "tcp", "10.9.0.10:22")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	// A healthy Provider leaves an ordinary transport error unclassified.
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("the stub connection did not fail its read")
	} else {
		var typed *appconnection.Error
		if errors.As(err, &typed) {
			t.Fatalf("a healthy Provider classified its transport error: %v", err)
		}
	}

	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = conn.Read(make([]byte, 1))
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("read after Close = %v, want KindProvider", err)
	}
	if typed.Provider == nil || typed.Provider.Phase != appconnection.ProviderPhaseStopped {
		t.Fatalf("ProviderInfo = %+v, want the stopped phase", typed.Provider)
	}
}
