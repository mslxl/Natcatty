package ssh

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
)

type forwardFixture struct {
	server      *fakessh.Server
	connections *appconnection.Manager
	forwarding  *appforwarding.Manager
	repo        *staticRuleRepo
}

// startForwardFixture wires the real SSH dial adapter and the real shared
// Connection Manager into the forwarding manager, so local, remote, and
// dynamic forwarding all exercise x/crypto/ssh and the in-process fixture.
func startForwardFixture(t *testing.T, options fakessh.Options, rule domainforward.Rule) *forwardFixture {
	t.Helper()
	clientConn, serverConn := fakessh.Pipe()
	server, err := fakessh.Start(serverConn, options)
	if err != nil {
		t.Fatalf("fakessh.Start: %v", err)
	}

	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }),
		WithTimeouts(2*time.Second, 2*time.Second),
	)
	connections := appconnection.NewManager(
		dialer,
		forwardPasswords{},
		forwardKnownHosts{},
		appconnection.WithPrompter(forwardPrompter{}),
		appconnection.WithIdleTTL(10*time.Second),
	)
	repo := &staticRuleRepo{rule: rule}
	forwarding := appforwarding.NewManager(repo, &forwardConnector{connections: connections})
	t.Cleanup(func() {
		forwarding.Close()
		connections.CloseAll()
		_ = server.Close()
		_ = clientConn.Close()
	})
	return &forwardFixture{server: server, connections: connections, forwarding: forwarding, repo: repo}
}

func (f *forwardFixture) start(t *testing.T, rule domainforward.Rule) {
	t.Helper()
	if err := f.forwarding.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func (f *forwardFixture) wait(t *testing.T, id model.ForwardingRuleID, status domainforward.Status) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, view := range f.forwarding.Snapshot().Rules {
			if view.Rule.ID == id && view.Status == status {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("rule %s never reached %q; snapshot = %+v", id, status, f.forwarding.Snapshot())
}

type staticRuleRepo struct {
	mu   sync.Mutex
	rule domainforward.Rule
}

func (r *staticRuleRepo) ListForwardingRules(context.Context) ([]domainforward.Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []domainforward.Rule{r.rule}, nil
}

func (r *staticRuleRepo) GetForwardingRule(_ context.Context, id model.ForwardingRuleID) (domainforward.Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rule.ID != id {
		return domainforward.Rule{}, appforwarding.ErrNotFound
	}
	return r.rule, nil
}

func (r *staticRuleRepo) SaveForwardingRule(_ context.Context, rule domainforward.Rule) error {
	r.mu.Lock()
	r.rule = rule
	r.mu.Unlock()
	return nil
}

func (r *staticRuleRepo) DeleteForwardingRule(context.Context, model.ForwardingRuleID) error {
	return appforwarding.ErrNotFound
}

type forwardConnector struct {
	connections *appconnection.Manager
}

func (c *forwardConnector) Connect(ctx context.Context, hostID model.HostID) (appforwarding.Connection, error) {
	lease, err := c.connections.Acquire(ctx, appconnection.Request{
		HostID:   hostID,
		Hostname: "fixture",
		Port:     22,
		Username: "root",
		// A negative interval disables keepalive for the short-lived fixture.
		Keepalive: &appconnection.KeepalivePolicy{Interval: -1},
		LeaseKind: appconnection.LeaseChannel,
	})
	if err != nil {
		return nil, mapConnectionFailure(err)
	}
	forwarding, err := lease.Forwarding()
	if err != nil {
		_ = lease.Discard()
		return nil, err
	}
	return &leasedConnection{lease: lease, ForwardingClient: forwarding}, nil
}

type leasedConnection struct {
	lease *appconnection.Lease
	appconnection.ForwardingClient
}

func (c *leasedConnection) Release() { _ = c.lease.Release() }
func (c *leasedConnection) Discard() { _ = c.lease.Discard() }

// mapConnectionFailure is the adapter between the connection layer's typed
// error and the forwarding runtime's projection. Production keeps the same
// mapping in the composition root.
func mapConnectionFailure(err error) error {
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		return err
	}
	kind := appforwarding.ErrorNetwork
	switch typed.Kind {
	case appconnection.KindAuth:
		kind = appforwarding.ErrorAuth
	case appconnection.KindHostKey:
		kind = appforwarding.ErrorHostKey
	case appconnection.KindTimeout:
		kind = appforwarding.ErrorTimeout
	case appconnection.KindUnsupported:
		kind = appforwarding.ErrorUnsupported
	case appconnection.KindCancelled:
		kind = appforwarding.ErrorCancelled
	case appconnection.KindInternal:
		kind = appforwarding.ErrorInternal
	}
	var hop *appforwarding.Hop
	if typed.Hop != nil {
		hop = &appforwarding.Hop{Index: typed.Hop.Index, Total: typed.Hop.Total, Label: typed.Hop.Label}
	}
	return appforwarding.NewRuntimeError(kind, hop, typed.Error(), err)
}

type forwardPasswords struct{}

func (forwardPasswords) Password(context.Context, model.HostID) (string, error) { return "pw", nil }

type forwardKnownHosts struct{}

func (forwardKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return nil, nil
}

func (forwardKnownHosts) UpsertKnownHost(context.Context, domainconn.KnownHost) error { return nil }

type forwardPrompter struct{}

func (forwardPrompter) Confirm(context.Context, domainconn.Key) (appconnection.PromptDecision, error) {
	return appconnection.PromptDecision{Trust: true}, nil
}

func echoListener(t *testing.T) (net.Listener, string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo target: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(conn, conn)
				_ = conn.Close()
			}()
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	return listener, address.IP.String(), address.Port
}

func fixturePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func forwardRule(id string, kind domainforward.Type, localPort int, remoteHost string, remotePort int) domainforward.Rule {
	return domainforward.Rule{
		ID: model.ForwardingRuleID(id), Label: id, Type: kind,
		BindAddress: "127.0.0.1", LocalPort: localPort,
		RemoteHost: remoteHost, RemotePort: remotePort,
		HostID: "host-1", OrderKey: 1000,
	}
}

func dialAndEcho(t *testing.T, address string, payload string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buffer) != payload {
		t.Fatalf("echo = %q, want %q", buffer, payload)
	}
}

func TestLocalForwardingThroughRealSSHTransport(t *testing.T) {
	_, targetHost, targetPort := echoListener(t)
	port := fixturePort(t)
	rule := forwardRule("local", domainforward.TypeLocal, port, targetHost, targetPort)

	fixture := startForwardFixture(t, fakessh.Options{
		Password: "pw",
		ForwardDial: func(host string, port int) (net.Conn, error) {
			return net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		},
	}, rule)
	fixture.start(t, rule)
	fixture.wait(t, rule.ID, domainforward.StatusActive)

	dialAndEcho(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), "local-forward")

	forwards := fixture.server.Forwards()
	if len(forwards) != 1 || forwards[0].Host != targetHost || forwards[0].Port != targetPort {
		t.Fatalf("direct-tcpip requests = %+v, want %s:%d", forwards, targetHost, targetPort)
	}
}

func TestDynamicForwardingThroughRealSSHTransport(t *testing.T) {
	_, targetHost, targetPort := echoListener(t)
	port := fixturePort(t)
	rule := forwardRule("dynamic", domainforward.TypeDynamic, port, "", 0)

	fixture := startForwardFixture(t, fakessh.Options{
		Password: "pw",
		ForwardDial: func(host string, port int) (net.Conn, error) {
			return net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		},
	}, rule)
	fixture.start(t, rule)
	fixture.wait(t, rule.ID, domainforward.StatusActive)

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial SOCKS listener: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	if greeting[1] != 0 {
		t.Fatalf("greeting = %v, want no-auth", greeting)
	}

	ip := net.ParseIP(targetHost).To4()
	request := []byte{5, 1, 0, 1}
	request = append(request, ip...)
	request = binary.BigEndian.AppendUint16(request, uint16(targetPort))
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("request: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply[1] != 0 {
		t.Fatalf("SOCKS reply = %v, want success", reply)
	}

	if _, err := conn.Write([]byte("dynamic-forward")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buffer := make([]byte, len("dynamic-forward"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buffer) != "dynamic-forward" {
		t.Fatalf("echo = %q", buffer)
	}

	forwards := fixture.server.Forwards()
	if len(forwards) != 1 || forwards[0].Host != targetHost || forwards[0].Port != targetPort {
		t.Fatalf("direct-tcpip requests = %+v, want %s:%d", forwards, targetHost, targetPort)
	}
}

func TestRemoteForwardingThroughRealSSHTransport(t *testing.T) {
	_, targetHost, targetPort := echoListener(t)
	port := fixturePort(t)
	rule := forwardRule("remote", domainforward.TypeRemote, port, targetHost, targetPort)

	fixture := startForwardFixture(t, fakessh.Options{Password: "pw"}, rule)
	fixture.start(t, rule)
	fixture.wait(t, rule.ID, domainforward.StatusActive)

	dialAndEcho(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), "remote-forward")

	requests := fixture.server.RemoteForwards()
	if len(requests) != 1 || requests[0].Addr != "127.0.0.1" || requests[0].Port != port {
		t.Fatalf("tcpip-forward requests = %+v, want 127.0.0.1:%d", requests, port)
	}

	if err := fixture.forwarding.Stop(context.Background(), rule.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	fixture.wait(t, rule.ID, domainforward.StatusInactive)
}

func TestForwardingReportsAuthenticationFailureWithoutRetrying(t *testing.T) {
	rule := forwardRule("auth", domainforward.TypeLocal, fixturePort(t), "127.0.0.1", 9)
	// The fixture requires "correct"; the connector offers "pw".
	fixture := startForwardFixture(t, fakessh.Options{Password: "correct"}, rule)
	fixture.start(t, rule)

	deadline := time.Now().Add(5 * time.Second)
	for {
		var view appforwarding.View
		for _, candidate := range fixture.forwarding.Snapshot().Rules {
			if candidate.Rule.ID == rule.ID {
				view = candidate
			}
		}
		if view.Status == domainforward.StatusError {
			if view.Error != appforwarding.ErrorAuth {
				t.Fatalf("error = %q, want auth", view.Error)
			}
			if view.Attempts != 0 {
				t.Fatalf("attempts = %d, want auth to be permanent", view.Attempts)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rule never reported a failure: %+v", view)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
