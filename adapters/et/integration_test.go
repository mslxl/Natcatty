// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
)

// The real-server tests run only with NATCATTY_ET_LAB=1. The lab addresses are
// the host-published SSH ports; the ET server itself is reached only through
// the SSH direct-tcpip channel.
const labEnabledVar = "NATCATTY_ET_LAB"

type labServer struct {
	name     string
	address  string
	username string
	password string
}

func labServers() []labServer {
	return []labServer{
		{
			name:     "etserver-7.1.1",
			address:  envOr("NATCATTY_ET_LAB_711", "127.0.0.1:2222"),
			username: envOr("NATCATTY_ET_LAB_USER", "tester"),
			password: envOr("NATCATTY_ET_LAB_PASSWORD", "testpass"),
		},
		{
			name:     "etserver-6.2.10",
			address:  envOr("NATCATTY_ET_LAB_6210", "127.0.0.1:2223"),
			username: envOr("NATCATTY_ET_LAB_USER", "tester"),
			password: envOr("NATCATTY_ET_LAB_PASSWORD", "testpass"),
		},
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func requireLab(t *testing.T) {
	t.Helper()
	if os.Getenv(labEnabledVar) == "" {
		t.Skipf("set %s=1 with the stock etserver lab running", labEnabledVar)
	}
}

type labPasswords struct{ value string }

func (p labPasswords) Password(context.Context, model.HostID) (string, error) {
	return p.value, nil
}

type labKnownHosts struct{}

func (labKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return nil, nil
}

func (labKnownHosts) UpsertKnownHost(context.Context, domainconn.KnownHost) error {
	return nil
}

type labTrust struct{}

func (labTrust) Confirm(context.Context, domainconn.Key) (appconnection.PromptDecision, error) {
	return appconnection.PromptDecision{Trust: true}, nil
}

func labManager(t *testing.T, password string) *appconnection.Manager {
	t.Helper()
	manager := appconnection.NewManager(
		sshadapter.NewDialer(), labPasswords{value: password}, labKnownHosts{},
		appconnection.WithPrompter(labTrust{}),
	)
	t.Cleanup(manager.CloseAll)
	return manager
}

func labSpec(t *testing.T, server labServer) appsession.StartSpec {
	t.Helper()
	host, portText, err := splitHostPort(server.address)
	if err != nil {
		t.Fatalf("%s address: %v", server.name, err)
	}
	port, _ := strconv.Atoi(portText)
	cfg := etmodel.Config{
		Hostname:  host,
		Port:      port,
		Username:  server.username,
		ETPort:    etmodel.DefaultETPort,
		Auth:      etmodel.Auth{Method: etmodel.AuthPassword},
		SessionID: "lab-" + server.name,
		Cols:      100,
		Rows:      30,
	}.Normalized()
	return appsession.StartSpec{
		Kind:     appsession.KindET,
		HostID:   model.HostID("lab-" + server.name),
		Hostname: host,
		Port:     port,
		Username: server.username,
		Cols:     100,
		Rows:     30,
		ET:       &cfg,
	}
}

func splitHostPort(address string) (string, string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", "", err
	}
	return host, port, nil
}

// outputCollector drains terminal output for the whole test, so a command
// that ends the Session can never block the ET session service behind an
// unread pipe.
type outputCollector struct {
	mu      sync.Mutex
	text    strings.Builder
	err     error
	updated chan struct{}
}

func newOutputCollector(transport appsession.Transport) *outputCollector {
	collector := &outputCollector{updated: make(chan struct{}, 1)}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := transport.Read(buffer)
			if n > 0 {
				collector.mu.Lock()
				collector.text.Write(buffer[:n])
				collector.mu.Unlock()
				collector.signal()
			}
			if err != nil {
				collector.mu.Lock()
				collector.err = err
				collector.mu.Unlock()
				collector.signal()
				return
			}
		}
	}()
	return collector
}

func (c *outputCollector) signal() {
	select {
	case c.updated <- struct{}{}:
	default:
	}
}

func (c *outputCollector) waitFor(t *testing.T, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		text, err := c.text.String(), c.err
		c.mu.Unlock()
		if strings.Contains(text, want) {
			return text
		}
		if err != nil {
			t.Fatalf("terminal output ended before %q: %v; output = %q", want, err, text)
		}
		select {
		case <-c.updated:
		case <-deadline:
			t.Fatalf("timed out waiting for %q; output = %q", want, text)
		}
	}
}

// openLab opens an in-process ET Session through the Connection Manager.
func openLab(t *testing.T, factory InProcessFactory, spec appsession.StartSpec) appsession.Transport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	transport, err := factory.Open(ctx, spec)
	if err != nil {
		t.Fatalf("Open %s: %v", spec.Hostname, err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

func writeCommand(t *testing.T, transport appsession.Transport, command string) {
	t.Helper()
	if _, err := transport.Write([]byte(command + "\n")); err != nil {
		t.Fatalf("Write %q: %v", command, err)
	}
}

// TestInProcessAgainstStockServers proves an interactive shell, output,
// resize, and exit classification against each stock etserver.
func TestInProcessAgainstStockServers(t *testing.T) {
	requireLab(t)
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			manager := labManager(t, server.password)
			factory := InProcessFactory{Manager: manager}
			spec := labSpec(t, server)
			transport := openLab(t, factory, spec)
			output := newOutputCollector(transport)

			writeCommand(t, transport, `printf 'ET_READY_%s\n' "$((6*7))"`)
			output.waitFor(t, "ET_READY_42", 20*time.Second)

			if err := transport.Resize(100, 33); err != nil {
				t.Fatalf("Resize: %v", err)
			}
			writeCommand(t, transport, "stty size")
			output.waitFor(t, "33 100", 20*time.Second)

			writeCommand(t, transport, "exit 7")
			code, err := waitExit(t, transport)
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			end := transport.(appsession.EndReporter).End()
			if end.Reason != appsession.EndReasonExited {
				t.Fatalf("%s End = %+v, want exited", server.name, end)
			}
			// et 7.1 propagates the shell's status (TERMINAL_EXIT_STATUS);
			// 6.2.10 has no such packet, and stock et reports no status there.
			wantStatus := server.name == "etserver-7.1.1"
			if end.HasExitCode != wantStatus {
				t.Fatalf("%s HasExitCode = %v, want %v (End = %+v)",
					server.name, end.HasExitCode, wantStatus, end)
			}
			if wantStatus && end.ExitCode != 7 {
				t.Fatalf("%s exit = %d, want 7", server.name, end.ExitCode)
			}
			t.Logf("%s ended: %+v (Wait reported %d)", server.name, end, code)
		})
	}
}

func waitExit(t *testing.T, transport appsession.Transport) (int, error) {
	t.Helper()
	waiter, ok := transport.(appsession.ExitWaiter)
	if !ok {
		t.Fatal("the ET transport does not report an exit")
	}
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := waiter.Wait()
		done <- result{code: code, err: err}
	}()
	select {
	case got := <-done:
		return got.code, got.err
	case <-time.After(30 * time.Second):
		t.Fatal("the ET Session did not end after exit")
		return 0, nil
	}
}

// TestLabAppRestartReattach pins the stock server behavior the feature was
// designed against: etserver 7.1.1 reattaches the same remote shell by reset,
// so it starts blank but keeps shell state. etserver 6.2.10 cannot negotiate
// the challenge or reset. A fresh session against it stays memory-only (there
// is nothing a restart could reattach), and a pair persisted by an older build
// is still refused with stock's upgrade sentence; it must not silently start a
// second shell.
func TestLabAppRestartReattach(t *testing.T) {
	requireLab(t)
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			store := SecretSessionStore{Secrets: newFakeFieldSecrets()}
			spec := labSpec(t, server)
			spec.ResumeKey = "lab-resume-" + strings.ReplaceAll(server.name, ".", "-")

			firstManager := labManager(t, server.password)
			first := openLab(t, InProcessFactory{Manager: firstManager, Sessions: store}, spec)
			firstOutput := newOutputCollector(first)
			writeCommand(t, first, `ET_RESUME_MARK=42; printf 'ET_MARKED_%s\n' "$ET_RESUME_MARK"`)
			firstOutput.waitFor(t, "ET_MARKED_42", 20*time.Second)
			saved, err := store.Load(context.Background(), spec.ResumeKey)
			if server.name == "etserver-7.1.1" {
				if err != nil || saved.ID == "" || len(saved.Passkey) != 32 {
					t.Fatalf("saved session record is incomplete: %v", err)
				}
			} else if !errors.Is(err, appvault.ErrSecretNotFound) {
				t.Fatalf("a pre-v7 session was persisted (%+v, err %v); a restart could never reattach it",
					saved, err)
			}

			// An app shutdown preserves the pair; the transport is closed as
			// the process would close it.
			first.(appsession.ResumePreserver).PreserveResume()
			if err := first.Close(); err != nil {
				t.Fatalf("close first process: %v", err)
			}
			firstManager.CloseAll()

			if server.name != "etserver-7.1.1" {
				// Simulate a pair an older build saved before persistence
				// became capability-aware. It belongs to a real running shell,
				// so the refusal below is the app-restart decision, not a
				// missing-session artifact.
				seedManager := labManager(t, server.password)
				seedLease := acquireLabLease(t, seedManager, spec)
				id, passkey := fixedLabCredentials(t)
				credentials := startLabTerminal(t, seedLease, id, passkey)
				forwarding, err := seedLease.Forwarding()
				if err != nil {
					t.Fatalf("Forwarding: %v", err)
				}
				live, err := dialLab(t, forwarding, spec, credentials, false)
				if err != nil {
					t.Fatalf("legacy seed dial: %v", err)
				}
				startLabSession(t, live)
				sendETInput(t, live, "echo $((1010+2020))\n")
				readETUntilAfter(t, live, "3030", "$ ", 20*time.Second)
				if err := store.Save(context.Background(), spec.ResumeKey,
					SessionRecord{ID: id, Passkey: passkey}); err != nil {
					t.Fatalf("seed a legacy saved pair: %v", err)
				}
				saved, err = store.Load(context.Background(), spec.ResumeKey)
				if err != nil {
					t.Fatalf("load seeded pair: %v", err)
				}
				_ = live.Close()
				seedManager.CloseAll()
			}

			secondManager := labManager(t, server.password)
			factory := InProcessFactory{Manager: secondManager, Sessions: store}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			second, err := factory.Open(ctx, spec)

			after, loadErr := store.Load(context.Background(), spec.ResumeKey)
			if server.name == "etserver-7.1.1" {
				if loadErr != nil || after.ID == "" || len(after.Passkey) != 32 {
					t.Fatalf("saved session record after restart is incomplete: %v", loadErr)
				}
			} else if loadErr != nil {
				t.Fatalf("a refused legacy reattach lost the saved pair: %v", loadErr)
			}

			if server.name == "etserver-7.1.1" {
				if err != nil {
					t.Fatalf("reattach on %s: %v", server.name, err)
				}
				t.Cleanup(func() { _ = second.Close() })
				secondOutput := newOutputCollector(second)
				writeCommand(t, second, `printf 'ET_RESUMED_%s\n' "${ET_RESUME_MARK-unset}"`)
				text := secondOutput.waitFor(t, "ET_RESUMED_42", 20*time.Second)
				if after != saved {
					t.Fatal("reattach changed the saved credential pair")
				}
				if strings.Contains(text, "ET_MARKED_42") {
					t.Fatal("reattach replayed output from before the restart")
				}
				return
			}

			// etserver 6.2.10 answers without the v7 challenge, so stock
			// cannot authenticate a reattach or reset its sequence/keystream.
			if !errors.Is(err, etmodel.ErrReattachUnsupported) {
				t.Fatalf("legacy reattach = %v, want ErrReattachUnsupported", err)
			}
			if !strings.Contains(err.Error(), "does not support session reattach; upgrade etserver") {
				t.Fatalf("legacy reattach error = %q, want stock's upgrade sentence", err)
			}
			if after != saved {
				t.Fatal("a refused legacy reattach cleared or changed the saved credentials")
			}
		})
	}
}

// TestLabNetworkChangeReconnectsWithoutBootstrap is the stock-server form of
// the roaming contract: the SSH transport carrying the ET link disappears and
// the client acquires a new lease, redials etserver, and resumes the same
// remote shell. The shell's environment variable proves no second etterminal
// bootstrap created a new shell.
func TestLabNetworkChangeReconnectsWithoutBootstrap(t *testing.T) {
	requireLab(t)
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			manager := labManager(t, server.password)
			factory := InProcessFactory{Manager: manager}
			var mu sync.Mutex
			var leases []ConnectionLease
			factory.Acquire = func(ctx context.Context, req appconnection.Request) (ConnectionLease, error) {
				lease, err := manager.Acquire(ctx, req)
				if err != nil {
					return nil, err
				}
				mu.Lock()
				leases = append(leases, lease)
				mu.Unlock()
				return lease, nil
			}
			spec := labSpec(t, server)
			transport := openLab(t, factory, spec)
			output := newOutputCollector(transport)

			writeCommand(t, transport, `ET_NETWORK_MARK=42; printf 'ET_NET_%s\n' "$ET_NETWORK_MARK"`)
			output.waitFor(t, "ET_NET_42", 20*time.Second)

			// A network change: the leased SSH transport dies, so its exec and
			// direct-tcpip channels can no longer carry the ET link. The
			// etserver session itself stays alive; the Connection Manager is
			// still able to acquire the replacement.
			mu.Lock()
			first := leases[0]
			mu.Unlock()
			_ = first.Discard()

			// Input queued while the link is down is delivered after the
			// reconnect. A fresh bootstrap would not inherit the variable.
			writeCommand(t, transport, `printf 'ET_SURVIVED_%s\n' "${ET_NETWORK_MARK-unset}"`)
			output.waitFor(t, "ET_SURVIVED_42", 30*time.Second)
		})
	}
}

// TestInProcessWrongPasswordIsATypedAuthError proves the SSH bootstrap keeps
// the Connection Manager's auth classification.
func TestInProcessWrongPasswordIsATypedAuthError(t *testing.T) {
	requireLab(t)
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			manager := labManager(t, "definitely-not-the-password")
			factory := InProcessFactory{Manager: manager}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err := factory.Open(ctx, labSpec(t, server))
			var typed *appconnection.Error
			if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
				t.Fatalf("Open error = %v, want a typed auth error", err)
			}
		})
	}
}

// TestInProcessHandshakeFailureIsTyped points the ET route at a closed port on
// the SSH endpoint. The bootstrap succeeds and the direct-tcpip dial fails, so
// the error is the ET handshake's, not an SSH auth failure.
func TestInProcessHandshakeFailureIsTyped(t *testing.T) {
	requireLab(t)
	server := labServers()[0]
	manager := labManager(t, server.password)
	factory := InProcessFactory{Manager: manager}
	spec := labSpec(t, server)
	spec.ET.ETPort = 1 // nothing listens there in the container
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err := factory.Open(ctx, spec)
	if !errors.Is(err, etmodel.ErrHandshakeFailed) {
		t.Fatalf("Open error = %v, want ErrHandshakeFailed", err)
	}
	if errors.Is(err, etmodel.ErrUnsupportedServer) {
		t.Fatalf("a refused port was classified as an unsupported server: %v", err)
	}
}

// TestInProcessUnsupportedServerIsTyped runs a one-connection fake ET server
// on the SSH endpoint and points the real forwarding route at it. The fake
// answers MISMATCHED_PROTOCOL, which the adapter must report as its typed
// unsupported-server error.
func TestInProcessUnsupportedServerIsTyped(t *testing.T) {
	requireLab(t)
	server := labServers()[0]
	manager := labManager(t, server.password)
	spec := labSpec(t, server)

	const fakePort = 2025
	const script = `$|=1; use IO::Socket::INET; my $s=IO::Socket::INET->new(LocalAddr=>"127.0.0.1",LocalPort=>2025,Listen=>5,ReuseAddr=>1) or die $!; print "READY\n"; my $c=$s->accept; sysread($c,my $b,8); my $n=unpack("Q<",$b); sysread($c,$b,$n); print $c pack("Q<",2), "\x08\x04"; close $c; close $s;`
	stop := startFakeServer(t, manager, spec, script, fakePort)
	defer stop()

	factory := InProcessFactory{Manager: manager}
	spec.ET.ETPort = fakePort
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err := factory.Open(ctx, spec)
	if !errors.Is(err, etmodel.ErrUnsupportedServer) {
		t.Fatalf("Open error = %v, want ErrUnsupportedServer", err)
	}
}

// startFakeServer runs a remote command through an exec channel and waits
// until it prints READY. The returned stop closes the channel and kills any
// process whose command line names the fake port.
func startFakeServer(t *testing.T, manager *appconnection.Manager, spec appsession.StartSpec,
	script string, port int) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lease, err := manager.Acquire(ctx, appconnection.Request{
		HostID:    spec.HostID,
		Hostname:  spec.Hostname,
		Port:      spec.Port,
		Username:  spec.Username,
		LeaseKind: appconnection.LeaseShell,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		t.Fatalf("Acquire for the fake server: %v", err)
	}
	stream, err := lease.OpenExec(ctx, "perl -e '"+script+"'")
	if err != nil {
		_ = lease.Discard()
		t.Fatalf("start the fake ET server: %v", err)
	}
	ready := make(chan error, 1)
	go func() {
		buffer := make([]byte, 64)
		var text strings.Builder
		for {
			n, err := stream.Read(buffer)
			text.Write(buffer[:n])
			if strings.Contains(text.String(), "READY") {
				ready <- nil
				return
			}
			if err != nil {
				ready <- fmt.Errorf("fake server output %q: %w", text.String(), err)
				return
			}
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = stream.Close()
			_ = lease.Discard()
			t.Fatalf("fake ET server did not start: %v", err)
		}
	case <-time.After(15 * time.Second):
		_ = stream.Close()
		_ = lease.Discard()
		t.Fatal("timed out waiting for the fake ET server")
	}
	return func() {
		_ = stream.Close()
		killCtx, killCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer killCancel()
		if cleanupLease, err := manager.Acquire(killCtx, appconnection.Request{
			HostID:    spec.HostID,
			Hostname:  spec.Hostname,
			Port:      spec.Port,
			Username:  spec.Username,
			LeaseKind: appconnection.LeaseShell,
			Keepalive: &appconnection.DefaultKeepalive,
		}); err == nil {
			_, _ = cleanupLease.Exec(killCtx, fmt.Sprintf("pkill -f 'LocalPort=>%d'", port))
			_ = cleanupLease.Release()
		}
		_ = lease.Discard()
	}
}
