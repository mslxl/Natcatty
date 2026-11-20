package connection

import (
	"context"
	"errors"
	"net"
	"testing"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
)

// streamChainDialer records the routed request and simulates the adapter's
// per-hop host-key verification and authentication callbacks. The target has
// no callback because a routed stream does not handshake it.
type streamChainDialer struct {
	request DialRequest
	conn    net.Conn
	err     error
	verify  map[string]domainconn.Key
}

func (d *streamChainDialer) Dial(context.Context, DialRequest) (Client, error) {
	return nil, errors.New("DialStream was expected")
}

func (d *streamChainDialer) DialStream(_ context.Context, req DialRequest) (net.Conn, error) {
	d.request = req
	if d.err != nil {
		return nil, d.err
	}
	for i, jump := range req.Jumps {
		key := d.verify["jump"]
		key.Hostname = jump.Hostname
		key.Port = jump.Port
		if jump.VerifyHostKey != nil {
			if err := jump.VerifyHostKey(context.Background(), key); err != nil {
				return nil, err
			}
		}
		if jump.OnAuth != nil {
			jump.OnAuth("password-" + string(rune('1'+i)))
		}
	}
	return d.conn, nil
}

func streamManager(t *testing.T, dialer Dialer, source AuthSource) *Manager {
	t.Helper()
	return NewManager(dialer, nil, &fakeKnownHosts{}, WithAuthSource(source),
		WithPrompter(&fakePrompter{decision: PromptDecision{Trust: true}}))
}

// TestDialStreamRoutesEveryJumpAndLeavesTheTargetAlone pins the routed shape:
// the Telnet target is the last endpoint, each jump carries its own verified
// host key and credentials, and the proxy applies to the first endpoint.
func TestDialStreamRoutesEveryJumpAndLeavesTheTargetAlone(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	source := &hopAuthSource{plans: map[model.HostID]AuthPlan{
		"jump-1": BuildAuthPlan(AuthSpec{Mode: AuthModePassword, Password: "1"}),
		"jump-2": BuildAuthPlan(AuthSpec{Mode: AuthModePassword, Password: "2"}),
		"target": BuildAuthPlan(AuthSpec{Mode: AuthModePassword, Password: "3"}),
	}}
	dialer := &streamChainDialer{conn: client, verify: map[string]domainconn.Key{
		"jump": {Fingerprint: "jump-key"},
	}}
	manager := streamManager(t, dialer, source)

	req := chainRequest()
	req.Algorithms = AlgorithmPolicy{Legacy: true}
	stream, err := manager.DialStream(context.Background(), req)
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	got := dialer.request
	if got.Hostname != "target.example.com" || got.Port != 22 {
		t.Fatalf("target = %s:%d, want target.example.com:22", got.Hostname, got.Port)
	}
	if len(got.Jumps) != 2 {
		t.Fatalf("jumps = %d, want 2", len(got.Jumps))
	}
	first, second := got.Jumps[0], got.Jumps[1]
	if first.Hostname != "bastion.example.com" || first.Port != 2222 ||
		first.Username != "ops" || first.Password != "1" {
		t.Fatalf("first jump = %s@%s:%d password %q",
			first.Username, first.Hostname, first.Port, first.Password)
	}
	if first.VerifyHostKey == nil || second.VerifyHostKey == nil {
		t.Fatal("a jump has no host-key verification callback")
	}
	// The target's Legacy policy widens its hops additively, exactly as Dial
	// does; the target itself has no handshake to configure.
	if !first.Algorithms.Legacy || !second.Algorithms.Legacy {
		t.Fatalf("jump algorithms = %+v / %+v, want legacy inherited", first.Algorithms, second.Algorithms)
	}
	if got.Proxy == nil || got.Proxy.Host != "proxy.example.com" {
		t.Fatalf("proxy = %+v, want the first endpoint's proxy", got.Proxy)
	}
}

// TestDialStreamTagsTheFailingJump keeps the hop's position in the chain so
// the surface can say which SSH endpoint refused.
func TestDialStreamTagsTheFailingJump(t *testing.T) {
	source := &hopAuthSource{
		errAt: "jump-2",
		err:   &Error{Kind: KindAuth, Cause: ErrAuthentication},
	}
	manager := streamManager(t, &streamChainDialer{}, source)

	_, err := manager.DialStream(context.Background(), chainRequest())
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindAuth {
		t.Fatalf("DialStream = %v, want a typed auth failure", err)
	}
	if typed.Hop == nil || typed.Hop.Index != 2 || typed.Hop.Total != 3 ||
		typed.Hop.Label != "inner.example.com:22" {
		t.Fatalf("hop = %+v, want 2/3 inner.example.com:22", typed.Hop)
	}
}

// TestDialStreamRequiresAStreamDialer fails closed when the configured dialer
// can only open SSH Clients; a routed non-SSH Host must not fall back to a
// direct dial.
func TestDialStreamRequiresAStreamDialer(t *testing.T) {
	manager := NewManager(&chainDialer{}, nil, &fakeKnownHosts{}, WithAuthSource(&hopAuthSource{}))

	_, err := manager.DialStream(context.Background(), Request{
		HostID: "console", Hostname: "console.local", Port: 23,
	})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindUnsupported {
		t.Fatalf("DialStream = %v, want a typed unsupported failure", err)
	}
}

// TestDialStreamTagsAProviderFailure names the Network Profile behind a
// failed routed stream while keeping the transport's failure kind.
func TestDialStreamTagsAProviderFailure(t *testing.T) {
	provider := &fakeNetworkProvider{}
	registry := NewNetworkProviderRegistry(&fakeProviderFactory{
		openFn: func(context.Context, NetworkRoute) (NetworkProvider, error) {
			return provider, nil
		},
	})
	dialer := &streamChainDialer{err: ErrNetwork}
	manager := NewManager(dialer, nil, &fakeKnownHosts{},
		WithNetworkProviders(registry), WithAuthSource(&hopAuthSource{}))

	network := &NetworkRoute{ProfileID: "net-console", Kind: "tailscale", Label: "Lab"}
	_, err := manager.DialStream(context.Background(), Request{
		HostID: "console", Hostname: "console.local", Port: 23, Network: network,
	})
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("DialStream = %v, want a typed failure", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "net-console" ||
		typed.Provider.Label != "Lab" {
		t.Fatalf("provider info = %+v, want the selected profile", typed.Provider)
	}
}
