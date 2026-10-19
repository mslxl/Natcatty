package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
)

// chainFixture is one hop's server plus the client end of its connection.
type chainFixture struct {
	server *fakessh.Server
	client net.Conn
}

func startChainFixture(t *testing.T, options fakessh.Options) *chainFixture {
	t.Helper()
	clientConn, serverConn := fakessh.Pipe()
	server, err := fakessh.Start(serverConn, options)
	if err != nil {
		t.Fatalf("fakessh.Start: %v", err)
	}
	fixture := &chainFixture{server: server, client: clientConn}
	t.Cleanup(func() {
		_ = server.Close()
		_ = clientConn.Close()
	})
	return fixture
}

func waitServer(t *testing.T, server *fakessh.Server) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		server.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the fixture server did not shut down with the transport")
	}
}

func acceptKey(context.Context, domainconn.Key) error { return nil }

func TestDialTraversesAThreeEndpointChain(t *testing.T) {
	target := startChainFixture(t, fakessh.Options{Password: "database-pw"})
	middle := startChainFixture(t, fakessh.Options{
		Password: "gateway-pw",
		ForwardDial: func(string, int) (net.Conn, error) {
			return target.client, nil
		},
	})
	outer := startChainFixture(t, fakessh.Options{
		Password: "bastion-pw",
		ForwardDial: func(string, int) (net.Conn, error) {
			return middle.client, nil
		},
	})

	var (
		mu     sync.Mutex
		seen   []domainconn.Key
		events []appconnection.ChainProgress
	)
	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) { return outer.client, nil }),
		WithTimeouts(2*time.Second, 2*time.Second),
	)
	verify := func(_ context.Context, key domainconn.Key) error {
		mu.Lock()
		seen = append(seen, key)
		mu.Unlock()
		return nil
	}

	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "database.internal", Port: 2222, Username: "app", Password: "database-pw",
		Jumps: []appconnection.DialHop{
			{
				Label: "bastion", Hostname: "bastion.example.com", Port: 22, Username: "ops",
				Password:      "bastion-pw",
				VerifyHostKey: verify,
			},
			{
				Hostname: "gateway.internal", Port: 22, Username: "deploy",
				Password:      "gateway-pw",
				VerifyHostKey: verify,
			},
		},
		VerifyHostKey: verify,
		OnProgress: func(progress appconnection.ChainProgress) {
			mu.Lock()
			events = append(events, progress)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if got := outer.server.PasswordAttempts(); got != 1 {
		t.Fatalf("outer password attempts = %d", got)
	}
	if username, password := outer.server.LastCredentials(); username != "ops" || password != "bastion-pw" {
		t.Fatalf("outer credentials = %q/%q", username, password)
	}
	if username, password := middle.server.LastCredentials(); username != "deploy" || password != "gateway-pw" {
		t.Fatalf("middle credentials = %q/%q", username, password)
	}
	if username, password := target.server.LastCredentials(); username != "app" || password != "database-pw" {
		t.Fatalf("target credentials = %q/%q", username, password)
	}

	if forwards := outer.server.Forwards(); len(forwards) != 1 ||
		forwards[0].Host != "gateway.internal" || forwards[0].Port != 22 {
		t.Fatalf("outer forwards = %+v", forwards)
	}
	if forwards := middle.server.Forwards(); len(forwards) != 1 ||
		forwards[0].Host != "database.internal" || forwards[0].Port != 2222 {
		t.Fatalf("middle forwards = %+v", forwards)
	}

	mu.Lock()
	if len(seen) != 3 {
		t.Fatalf("host-key callbacks = %d, want one per endpoint", len(seen))
	}
	if seen[0].Hostname != "bastion.example.com" || seen[0].Port != 22 ||
		seen[1].Hostname != "gateway.internal" || seen[1].Port != 22 ||
		seen[2].Hostname != "database.internal" || seen[2].Port != 2222 {
		t.Fatalf("host-key callbacks = %+v", seen)
	}
	var connected, forwarding int
	for _, event := range events {
		if event.Total != 3 {
			t.Fatalf("event total = %d", event.Total)
		}
		switch event.Status {
		case appconnection.ChainConnected:
			connected++
		case appconnection.ChainForwarding:
			forwarding++
		}
	}
	mu.Unlock()
	if connected != 3 {
		t.Fatalf("connected events = %d, want 3", connected)
	}
	if forwarding != 2 {
		t.Fatalf("forwarding events = %d, want 2", forwarding)
	}

	terminal, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{})
	if err != nil {
		t.Fatalf("OpenTerminal through the chain: %v", err)
	}
	if _, err := terminal.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(terminal, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(echo) != "ping" {
		t.Fatalf("echo = %q", echo)
	}
	_ = terminal.Close()

	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitServer(t, target.server)
	waitServer(t, middle.server)
	waitServer(t, outer.server)
}

func TestChainFailureAtAHopClosesEarlierHopsAndNamesTheHop(t *testing.T) {
	middle := startChainFixture(t, fakessh.Options{Password: "right"})
	outer := startChainFixture(t, fakessh.Options{
		Password:    "right",
		ForwardDial: func(string, int) (net.Conn, error) { return middle.client, nil },
	})
	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) { return outer.client, nil }),
		WithTimeouts(2*time.Second, 2*time.Second),
	)

	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "app", Password: "wrong",
		Jumps: []appconnection.DialHop{{
			Label: "gateway", Hostname: "gateway.internal", Port: 22, Username: "ops",
			Password: "right", VerifyHostKey: acceptKey,
		}},
		VerifyHostKey: acceptKey,
	})
	if err == nil {
		t.Fatal("Dial succeeded with a refused target password")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
		t.Fatalf("error = %v, want an auth error", err)
	}
	if typed.Hop == nil || typed.Hop.Index != 2 || typed.Hop.Total != 2 || typed.Hop.Label != "target.internal" {
		t.Fatalf("hop = %+v", typed.Hop)
	}
	waitServer(t, outer.server)
	if attempts := middle.server.PasswordAttempts(); attempts != 1 {
		t.Fatalf("target attempts = %d, want the refused login", attempts)
	}
}

func TestChainCancellationClosesEveryHop(t *testing.T) {
	blockedClient, blockedServer := net.Pipe()
	t.Cleanup(func() {
		_ = blockedClient.Close()
		_ = blockedServer.Close()
	})
	forwarded := make(chan struct{}, 1)
	middle := startChainFixture(t, fakessh.Options{
		Password: "right",
		ForwardDial: func(string, int) (net.Conn, error) {
			forwarded <- struct{}{}
			return blockedClient, nil
		},
	})
	outer := startChainFixture(t, fakessh.Options{
		Password:    "right",
		ForwardDial: func(string, int) (net.Conn, error) { return middle.client, nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) { return outer.client, nil }),
		WithTimeouts(2*time.Second, 2*time.Second),
	)
	result := make(chan error, 1)
	go func() {
		_, err := dialer.Dial(ctx, appconnection.DialRequest{
			Hostname: "blackhole.internal", Port: 22, Username: "app", Password: "right",
			Jumps: []appconnection.DialHop{
				{Label: "bastion", Hostname: "bastion.internal", Port: 22, Username: "ops",
					Password: "right", VerifyHostKey: acceptKey},
				{Label: "gateway", Hostname: "gateway.internal", Port: 22, Username: "deploy",
					Password: "right", VerifyHostKey: acceptKey},
			},
			VerifyHostKey: acceptKey,
		})
		result <- err
	}()

	select {
	case <-forwarded:
	case <-time.After(5 * time.Second):
		t.Fatal("the chain never reached the final endpoint")
	}
	cancel()

	select {
	case err := <-result:
		var typed *appconnection.Error
		if !errors.As(err, &typed) || typed.Kind != appconnection.KindCancelled {
			t.Fatalf("error = %v, want cancellation", err)
		}
		if typed.Hop == nil || typed.Hop.Index != 3 || typed.Hop.Total != 3 {
			t.Fatalf("hop = %+v", typed.Hop)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dial did not return after cancellation")
	}
	waitServer(t, outer.server)
	waitServer(t, middle.server)
}

func TestProxyCommandRunsOnceForTheFirstEndpointOnly(t *testing.T) {
	target := startChainFixture(t, fakessh.Options{Password: "target"})
	jump := startChainFixture(t, fakessh.Options{
		Password: "jump",
		ForwardDial: func(string, int) (net.Conn, error) {
			return target.client, nil
		},
	})
	proxyClient, proxyServer := net.Pipe()
	t.Cleanup(func() {
		_ = proxyClient.Close()
		_ = proxyServer.Close()
	})
	go bridge(proxyServer, jump.client)

	var (
		mu      sync.Mutex
		starts  int
		command string
	)
	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
			t.Error("the direct dialer must not run when a proxy is configured")
			return nil, errors.New("unexpected direct dial")
		}),
		WithProcessStart(func(run string) (net.Conn, error) {
			mu.Lock()
			starts++
			command = run
			mu.Unlock()
			return proxyClient, nil
		}),
		WithTimeouts(2*time.Second, 2*time.Second),
	)

	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "app", Password: "target",
		Jumps: []appconnection.DialHop{{
			Label: "gateway", Hostname: "gateway.internal", Port: 22, Username: "ops",
			Password: "jump", VerifyHostKey: acceptKey,
		}},
		Proxy: &appconnection.Proxy{
			Type: appconnection.ProxyCommand, Command: "ssh -W %h:%p bastion",
		},
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	mu.Lock()
	defer mu.Unlock()
	if starts != 1 {
		t.Fatalf("proxy process starts = %d, want 1", starts)
	}
	// The proxy reaches the first endpoint only, so the tokens name the first
	// jump, not the final target.
	if !strings.Contains(command, "gateway.internal") || !strings.Contains(command, "22") ||
		strings.Contains(command, "target.internal") {
		t.Fatalf("substituted command = %q", command)
	}
	if forwards := jump.server.Forwards(); len(forwards) != 1 || forwards[0].Host != "target.internal" {
		t.Fatalf("jump forwards = %+v", forwards)
	}
}

func bridge(left, right net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(left, right)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(right, left)
		done <- struct{}{}
	}()
	<-done
	_ = left.Close()
	_ = right.Close()
	<-done
}
