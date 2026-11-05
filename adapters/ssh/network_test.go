package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

// networkDialProvider is the adapter test's NetworkProvider. It records every
// address so a test can prove the first endpoint and the proxy server dial go
// through it, and can script the transport or an error.
type networkDialProvider struct {
	mu        sync.Mutex
	networks  []string
	addresses []string
	calls     int
	dialFn    func(ctx context.Context, network, address string) (net.Conn, error)
	err       error
}

func (p *networkDialProvider) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	p.mu.Lock()
	p.calls++
	p.networks = append(p.networks, network)
	p.addresses = append(p.addresses, address)
	fn, err := p.dialFn, p.err
	p.mu.Unlock()
	if fn != nil {
		return fn(ctx, network, address)
	}
	if err != nil {
		return nil, err
	}
	return nil, errors.New("network provider has no scripted transport")
}

func (p *networkDialProvider) recorded() (int, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, append([]string(nil), p.addresses...)
}

func networkDialRoute() *appconnection.NetworkRoute {
	return &appconnection.NetworkRoute{
		ProfileID: "profile-a", Kind: "tailscale", Label: "Office", Fingerprint: "fp-1",
	}
}

// directDialCounter fails a test if the adapter falls back to the original
// dialer while a Network Route is selected.
func directDialCounter(t *testing.T) (DialFunc, *int) {
	t.Helper()
	calls := new(int)
	return func(context.Context, string, string) (net.Conn, error) {
		*calls++
		t.Errorf("the direct dialer was called with a Network Provider selected")
		return nil, errors.New("direct dial is forbidden")
	}, calls
}

func TestNetworkProviderCarriesTheFirstEndpoint(t *testing.T) {
	fixture := startChainFixture(t, fakessh.Options{Password: "pw"})
	provider := &networkDialProvider{dialFn: func(context.Context, string, string) (net.Conn, error) {
		return fixture.client, nil
	}}
	direct, directCalls := directDialCounter(t)
	dialer := NewDialer(WithDialFunc(direct), WithTimeouts(5*time.Second, 5*time.Second))

	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Network:       networkDialRoute(),
		Provider:      provider,
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial through a Network Provider: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	calls, addresses := provider.recorded()
	if calls != 1 || len(addresses) != 1 || addresses[0] != "target.internal:22" {
		t.Fatalf("provider dials = %d %v, want one dial to target.internal:22", calls, addresses)
	}
	if *directCalls != 0 {
		t.Fatal("the first endpoint was dialed directly")
	}
	if username, password := fixture.server.LastCredentials(); username != "root" || password != "pw" {
		t.Fatalf("credentials = %q/%q", username, password)
	}
}

func TestHTTPConnectProxyServerIsDialedThroughTheProvider(t *testing.T) {
	fixture := startChainFixture(t, fakessh.Options{Password: "pw"})
	requests := make(chan string, 1)
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		headers, err := readProxyHeaders(conn)
		if err != nil {
			return
		}
		requests <- headers
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		bridge(conn, fixture.client)
	})

	provider := &networkDialProvider{dialFn: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	direct, directCalls := directDialCounter(t)
	dialer := NewDialer(WithDialFunc(direct), WithTimeouts(5*time.Second, 5*time.Second))

	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy:         &appconnection.Proxy{Type: appconnection.ProxyHTTP, Host: host, Port: port},
		Network:       networkDialRoute(),
		Provider:      provider,
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial through HTTP CONNECT over a Network Provider: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	calls, addresses := provider.recorded()
	want := net.JoinHostPort(host, strconv.Itoa(port))
	if calls != 1 || len(addresses) != 1 || addresses[0] != want {
		t.Fatalf("provider dials = %d %v, want the proxy server %s", calls, addresses, want)
	}
	if *directCalls != 0 {
		t.Fatal("the proxy server address was dialed directly")
	}
	headers := waitString(t, requests)
	if first := strings.SplitN(headers, "\r\n", 2)[0]; first != "CONNECT target.internal:22 HTTP/1.1" {
		t.Fatalf("request line = %q", first)
	}
}

func TestSOCKS5ProxyServerIsDialedThroughTheProvider(t *testing.T) {
	fixture := startChainFixture(t, fakessh.Options{Password: "pw"})
	requests := make(chan socksRequest, 1)
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		request, err := serveSOCKS5(conn, false)
		if err != nil {
			return
		}
		requests <- request
		bridge(conn, fixture.client)
	})

	provider := &networkDialProvider{dialFn: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	direct, directCalls := directDialCounter(t)
	dialer := NewDialer(WithDialFunc(direct), WithTimeouts(5*time.Second, 5*time.Second))

	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy:         &appconnection.Proxy{Type: appconnection.ProxySOCKS5, Host: host, Port: port},
		Network:       networkDialRoute(),
		Provider:      provider,
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial through SOCKS5 over a Network Provider: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	calls, addresses := provider.recorded()
	want := net.JoinHostPort(host, strconv.Itoa(port))
	if calls != 1 || len(addresses) != 1 || addresses[0] != want {
		t.Fatalf("provider dials = %d %v, want the proxy server %s", calls, addresses, want)
	}
	if *directCalls != 0 {
		t.Fatal("the proxy server address was dialed directly")
	}
	select {
	case request := <-requests:
		if request.host != "target.internal" || request.port != 22 {
			t.Fatalf("SOCKS5 target = %s:%d", request.host, request.port)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the SOCKS5 proxy did not receive a request")
	}
}

func TestLaterJumpHopsNeverUseTheProvider(t *testing.T) {
	target := startChainFixture(t, fakessh.Options{Password: "target-pw"})
	outer := startChainFixture(t, fakessh.Options{
		Password: "bastion-pw",
		ForwardDial: func(string, int) (net.Conn, error) {
			return target.client, nil
		},
	})
	provider := &networkDialProvider{dialFn: func(context.Context, string, string) (net.Conn, error) {
		return outer.client, nil
	}}
	direct, directCalls := directDialCounter(t)
	dialer := NewDialer(WithDialFunc(direct), WithTimeouts(5*time.Second, 5*time.Second))

	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "target-pw",
		Jumps: []appconnection.DialHop{{
			Label: "bastion", Hostname: "bastion.internal", Port: 22, Username: "ops",
			Password: "bastion-pw", VerifyHostKey: acceptKey,
		}},
		Network:       networkDialRoute(),
		Provider:      provider,
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial a jump chain over a Network Provider: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	calls, addresses := provider.recorded()
	if calls != 1 || len(addresses) != 1 || addresses[0] != "bastion.internal:22" {
		t.Fatalf("provider dials = %d %v, want only the first hop", calls, addresses)
	}
	if *directCalls != 0 {
		t.Fatal("the first hop was dialed directly")
	}
	if username, password := target.server.LastCredentials(); username != "root" || password != "target-pw" {
		t.Fatalf("target credentials = %q/%q, want the forwarded hop", username, password)
	}
}

func TestProviderFailuresKeepTheirClassificationAndNeverFallBack(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want appconnection.Kind
	}{
		{
			name: "provider startup",
			err: &appconnection.Error{
				Kind:  appconnection.KindProvider,
				Cause: errors.New("tunnel is down"),
			},
			want: appconnection.KindProvider,
		},
		{
			name: "deadline",
			err:  context.DeadlineExceeded,
			want: appconnection.KindTimeout,
		},
		{
			name: "target refused",
			err:  errors.New("dial tcp: connect: connection refused"),
			want: appconnection.KindNetwork,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &networkDialProvider{err: test.err}
			direct, directCalls := directDialCounter(t)
			dialer := NewDialer(WithDialFunc(direct), WithTimeouts(time.Second, time.Second))

			_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
				Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
				Network:       networkDialRoute(),
				Provider:      provider,
				VerifyHostKey: acceptKey,
			})
			if err == nil {
				t.Fatal("Dial succeeded with a failing provider")
			}
			var typed *appconnection.Error
			if !errors.As(err, &typed) {
				t.Fatalf("error %v is not typed", err)
			}
			if typed.Kind != test.want {
				t.Fatalf("kind = %s, want %s (%v)", typed.Kind, test.want, err)
			}
			if *directCalls != 0 {
				t.Fatal("a provider failure fell back to a direct dial")
			}
			if calls, _ := provider.recorded(); calls != 1 {
				t.Fatalf("provider calls = %d, want exactly one", calls)
			}
		})
	}
}

func TestProxyCommandWithANetworkProfileSpawnsNoProcess(t *testing.T) {
	processStarted := false
	dialer := NewDialer(
		WithProcessStart(func(string) (net.Conn, error) {
			processStarted = true
			return nil, errors.New("process must not start")
		}),
		WithTimeouts(time.Second, time.Second),
	)

	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root",
		Proxy: &appconnection.Proxy{
			Type: appconnection.ProxyCommand, Command: "ssh -W %h:%p bastion",
		},
		Network:       networkDialRoute(),
		VerifyHostKey: acceptKey,
	})
	if err == nil {
		t.Fatal("Dial accepted ProxyCommand with a Network Profile")
	}
	if processStarted {
		t.Fatal("the ProxyCommand process started before the conflict was rejected")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not typed", err)
	}
	if typed.Kind != appconnection.KindUnsupported {
		t.Fatalf("kind = %s, want unsupported", typed.Kind)
	}
	if !errors.Is(err, appconnection.ErrProxyCommandNetworkConflict) {
		t.Fatalf("error = %v, want the ProxyCommand conflict sentinel", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "profile-a" {
		t.Fatalf("ProviderInfo = %+v, want profile-a", typed.Provider)
	}
	message := err.Error()
	for _, want := range []string{"ProxyCommand", "Network Profile", "profile-a"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not mention %q", message, want)
		}
	}
}

func TestNetworkRouteWithoutAProviderFailsClosed(t *testing.T) {
	var (
		directCalls  int
		processCalls int
	)
	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
			directCalls++
			return nil, errors.New("direct dial is forbidden")
		}),
		WithProcessStart(func(string) (net.Conn, error) {
			processCalls++
			return nil, errors.New("process must not start")
		}),
		WithTimeouts(time.Second, time.Second),
	)

	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Network:       networkDialRoute(),
		VerifyHostKey: acceptKey,
	})
	if err == nil {
		t.Fatal("Dial accepted a Network Route without a Provider")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not typed", err)
	}
	if typed.Kind != appconnection.KindProvider {
		t.Fatalf("kind = %s, want provider", typed.Kind)
	}
	if !errors.Is(err, appconnection.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
	if typed.Provider == nil {
		t.Fatal("error carries no ProviderInfo")
	}
	want := appconnection.ProviderInfo{
		ProfileID: "profile-a", Label: "Office", Kind: "tailscale",
		Phase: appconnection.ProviderPhaseStopped,
	}
	if *typed.Provider != want {
		t.Fatalf("ProviderInfo = %+v, want %+v", *typed.Provider, want)
	}
	if directCalls != 0 {
		t.Fatalf("direct dials = %d, want none", directCalls)
	}
	if processCalls != 0 {
		t.Fatalf("process starts = %d, want none", processCalls)
	}
}

func TestProxyCommandConflictWinsOverTheMissingProviderGuard(t *testing.T) {
	tests := []struct {
		name     string
		provider appconnection.NetworkProvider
	}{
		{name: "provider present", provider: &networkDialProvider{}},
		{name: "provider missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				directCalls  int
				processCalls int
			)
			dialer := NewDialer(
				WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
					directCalls++
					return nil, errors.New("direct dial is forbidden")
				}),
				WithProcessStart(func(string) (net.Conn, error) {
					processCalls++
					return nil, errors.New("process must not start")
				}),
				WithTimeouts(time.Second, time.Second),
			)

			_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
				Hostname: "target.internal", Port: 22, Username: "root",
				Proxy: &appconnection.Proxy{
					Type: appconnection.ProxyCommand, Command: "ssh -W %h:%p bastion",
				},
				Network:       networkDialRoute(),
				Provider:      test.provider,
				VerifyHostKey: acceptKey,
			})
			if err == nil {
				t.Fatal("Dial accepted ProxyCommand with a Network Profile")
			}
			var typed *appconnection.Error
			if !errors.As(err, &typed) {
				t.Fatalf("error %v is not typed", err)
			}
			if typed.Kind != appconnection.KindUnsupported {
				t.Fatalf("kind = %s, want unsupported", typed.Kind)
			}
			if !errors.Is(err, appconnection.ErrProxyCommandNetworkConflict) {
				t.Fatalf("error = %v, want the ProxyCommand conflict", err)
			}
			if errors.Is(err, appconnection.ErrProviderUnavailable) {
				t.Fatalf("error = %v, the provider guard ran before the conflict", err)
			}
			if typed.Provider == nil || typed.Provider.ProfileID != "profile-a" {
				t.Fatalf("ProviderInfo = %+v, want profile-a", typed.Provider)
			}
			if directCalls != 0 {
				t.Fatalf("direct dials = %d, want none", directCalls)
			}
			if processCalls != 0 {
				t.Fatalf("process starts = %d, want none", processCalls)
			}
		})
	}
}

func TestNoNetworkProviderUsesTheOriginalDialer(t *testing.T) {
	fixture := startChainFixture(t, fakessh.Options{Password: "pw"})
	var (
		mu        sync.Mutex
		addresses []string
	)
	dialer := NewDialer(
		WithDialFunc(func(_ context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			addresses = append(addresses, network+" "+address)
			mu.Unlock()
			return fixture.client, nil
		}),
		WithTimeouts(5*time.Second, 5*time.Second),
	)

	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial without a Network Provider: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	mu.Lock()
	defer mu.Unlock()
	if len(addresses) != 1 || addresses[0] != "tcp target.internal:22" {
		t.Fatalf("direct dials = %v, want the original dialer once", addresses)
	}
}
