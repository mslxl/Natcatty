package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	domainconn "github.com/mslxl/natcatty/domain/connection"
)

// remoteCommandPrompter trusts the fixture server's host key, so the command
// tests exercise the real dial, handshake, and channel path without a Vault.
type remoteCommandPrompter struct{}

func (remoteCommandPrompter) Confirm(context.Context, domainconn.Key) (appconnection.PromptDecision, error) {
	return appconnection.PromptDecision{Trust: true}, nil
}

// remoteCommandFixture wires the production Session factory to the in-process
// SSH server, so tests drive the application/session Factory/Transport seam
// with a real SSH handshake. The fixture is deliberately a fake at the wire
// endpoint only: PTY requests, exec requests, input, output, resize, and exit
// status all travel through x/crypto/ssh.
type remoteCommandFixture struct {
	manager *appconnection.Manager
	factory *transportFactory

	mu     sync.Mutex
	server *fakessh.Server
	conns  []net.Conn
}

func newRemoteCommandFixture(t *testing.T, options fakessh.Options) *remoteCommandFixture {
	t.Helper()
	options.Password = "s3cret"
	fixture := &remoteCommandFixture{}
	dial := func(context.Context, string, string) (net.Conn, error) {
		clientConn, serverConn := fakessh.Pipe()
		server, err := fakessh.Start(serverConn, options)
		if err != nil {
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, err
		}
		fixture.mu.Lock()
		fixture.server = server
		fixture.conns = append(fixture.conns, clientConn)
		fixture.mu.Unlock()
		return clientConn, nil
	}
	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(dial),
		sshadapter.WithTimeouts(5*time.Second, 5*time.Second),
	)
	fixture.manager = appconnection.NewManager(
		dialer,
		vaultPasswords{vault: factoryPasswords{value: "s3cret"}},
		&factoryKnownHosts{},
		appconnection.WithPrompter(remoteCommandPrompter{}),
	)
	fixture.factory = newTransportFactory(&factoryLocal{}, nil, nil, fixture.manager, nil)
	t.Cleanup(func() {
		fixture.manager.CloseAll()
		fixture.mu.Lock()
		server := fixture.server
		conns := append([]net.Conn(nil), fixture.conns...)
		fixture.mu.Unlock()
		if server != nil {
			_ = server.Close()
		}
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	return fixture
}

func (f *remoteCommandFixture) sshServer(t *testing.T) *fakessh.Server {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.server == nil {
		t.Fatal("the fixture did not dial an SSH server")
	}
	return f.server
}

func (f *remoteCommandFixture) open(t *testing.T, remoteCommand string, cols, rows int) appsession.Transport {
	t.Helper()
	transport, err := f.factory.Open(context.Background(), appsession.StartSpec{
		Kind:          appsession.KindSSH,
		Label:         "command fixture",
		HostID:        "host-1",
		Hostname:      "example.com",
		Port:          22,
		Username:      "root",
		Cols:          cols,
		Rows:          rows,
		RemoteCommand: remoteCommand,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

// awaitResult runs one blocking terminal operation and fails the test if it
// does not finish before the hard deadline. The deadline keeps a wedged
// command channel from hanging the suite.
func awaitResult[T any](t *testing.T, what string, work func() (T, error)) T {
	t.Helper()
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := work()
		done <- result{value: value, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("%s: %v", what, got.err)
		}
		return got.value
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not complete", what)
		var zero T
		return zero
	}
}

// readExactly reads n bytes under a hard deadline.
func readExactly(t *testing.T, reader io.Reader, n int) string {
	t.Helper()
	return awaitResult(t, fmt.Sprintf("reading %d bytes", n), func() (string, error) {
		buffer := make([]byte, n)
		_, err := io.ReadFull(reader, buffer)
		return string(buffer), err
	})
}

// readUntilEOF drains a terminal under a hard deadline, proving a finished
// command closes its channel instead of hanging the Session.
func readUntilEOF(t *testing.T, reader io.Reader) string {
	t.Helper()
	return awaitResult(t, "reading to EOF", func() (string, error) {
		data, err := io.ReadAll(reader)
		return string(data), err
	})
}

func waitForResize(t *testing.T, server *fakessh.Server, want fakessh.Size) {
	t.Helper()
	waitFor(t, "the remote resize", func() bool {
		resizes := server.Resizes()
		return len(resizes) > 0 && resizes[len(resizes)-1] == want
	})
}

func endOf(t *testing.T, transport appsession.Transport) appsession.End {
	t.Helper()
	reporter, ok := transport.(appsession.EndReporter)
	if !ok {
		t.Fatalf("transport %T does not report its end", transport)
	}
	return reporter.End()
}

// TestSSHSessionWithoutARemoteCommandKeepsTheLoginShell covers the unchanged
// login-shell path: a PTY, a shell request, no exec request, working input,
// output, and resize.
func TestSSHSessionWithoutARemoteCommandKeepsTheLoginShell(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseShell := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseShell)

	fixture := newRemoteCommandFixture(t, fakessh.Options{Shell: func(channel xssh.Channel) {
		input := make([]byte, len("ping"))
		if _, err := io.ReadFull(channel, input); err != nil {
			return
		}
		_, _ = channel.Write(input)
		<-release
		_, _ = channel.SendRequest("exit-status", false,
			xssh.Marshal(struct{ Status uint32 }{0}))
		_ = channel.Close()
	}})
	transport := fixture.open(t, "", 100, 30)
	server := fixture.sshServer(t)

	if !server.ShellOpened() {
		t.Fatal("an empty remote command did not open a login shell")
	}
	if commands := server.ExecRequests(); len(commands) != 0 {
		t.Fatalf("exec requests = %q, want none for a login shell", commands)
	}
	if size, ok := server.Pty(); !ok || size != (fakessh.Size{Cols: 100, Rows: 30}) {
		t.Fatalf("PTY = %+v (requested=%v), want 100x30", size, ok)
	}

	if _, err := transport.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := readExactly(t, transport, len("ping")); got != "ping" {
		t.Fatalf("output = %q, want ping", got)
	}

	if err := transport.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	waitForResize(t, server, fakessh.Size{Cols: 120, Rows: 40})

	releaseShell()
	if rest := readUntilEOF(t, transport); rest != "" {
		t.Fatalf("trailing output = %q, want none", rest)
	}
	end := endOf(t, transport)
	if end.Reason != appsession.EndReasonExited || !end.HasExitCode || end.ExitCode != 0 {
		t.Fatalf("end = %+v, want a clean exit with status 0", end)
	}
}

// TestSSHSessionRunsAnExplicitRemoteCommandUnderAPty covers the command path:
// the adapter asks for a PTY, sends an exec request instead of a shell, and
// keeps input, output, resize, and exit classification working.
func TestSSHSessionRunsAnExplicitRemoteCommandUnderAPty(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCommand := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseCommand)

	const command = "tmux new-session -A -s natcatty-host-1"
	fixture := newRemoteCommandFixture(t, fakessh.Options{Exec: func(got string, channel xssh.Channel) int {
		input := make([]byte, len("ping"))
		if _, err := io.ReadFull(channel, input); err != nil {
			return 3
		}
		_, _ = io.WriteString(channel, "got:"+string(input))
		<-release
		return 0
	}})
	transport := fixture.open(t, command, 100, 30)
	server := fixture.sshServer(t)

	if server.ShellOpened() {
		t.Fatal("a command Session opened a login shell")
	}
	if commands := server.ExecRequests(); !slices.Equal(commands, []string{command}) {
		t.Fatalf("exec requests = %q, want %q", commands, command)
	}
	if size, ok := server.Pty(); !ok || size != (fakessh.Size{Cols: 100, Rows: 30}) {
		t.Fatalf("PTY = %+v (requested=%v), want 100x30", size, ok)
	}

	if err := transport.Resize(132, 43); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	waitForResize(t, server, fakessh.Size{Cols: 132, Rows: 43})

	if _, err := transport.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := readExactly(t, transport, len("got:ping")); got != "got:ping" {
		t.Fatalf("output = %q, want got:ping", got)
	}

	releaseCommand()
	if rest := readUntilEOF(t, transport); rest != "" {
		t.Fatalf("trailing output = %q, want none", rest)
	}
	end := endOf(t, transport)
	if end.Reason != appsession.EndReasonExited || !end.HasExitCode || end.ExitCode != 0 {
		t.Fatalf("end = %+v, want a clean exit with status 0", end)
	}
}

// TestSSHSessionReportsAFailingRemoteCommandExit covers a command that fails:
// its exit status reaches the Session end classification, and the terminal
// reaches EOF instead of hanging.
func TestSSHSessionReportsAFailingRemoteCommandExit(t *testing.T) {
	fixture := newRemoteCommandFixture(t, fakessh.Options{Exec: func(string, xssh.Channel) int {
		return 7
	}})
	transport := fixture.open(t, "exit 7", 80, 24)

	if output := readUntilEOF(t, transport); output != "" {
		t.Fatalf("output = %q, want none", output)
	}
	end := endOf(t, transport)
	if end.Reason != appsession.EndReasonExited {
		t.Fatalf("end = %+v, want an exited classification", end)
	}
	if !end.HasExitCode || end.ExitCode != 7 {
		t.Fatalf("exit = %d (has=%v), want 7", end.ExitCode, end.HasExitCode)
	}
}

// TestSSHSessionCarriesTheRemoteCommandToTheTerminalContract pins the factory
// mapping at the pre-agreed seam: empty stays empty (login shell), and a
// non-empty command reaches the connection terminal spec unchanged.
func TestSSHSessionCarriesTheRemoteCommandToTheTerminalContract(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		command string
	}{
		{name: "login shell", command: ""},
		{name: "remote command", command: "tmux new-session -A -s natcatty-host-1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := &factoryClient{terminal: &factoryTerminal{}}
			dialer := &factoryDialer{client: client}
			manager := appconnection.NewManager(
				dialer,
				vaultPasswords{vault: factoryPasswords{value: "s3cret"}},
				&factoryKnownHosts{},
			)
			t.Cleanup(manager.CloseAll)
			factory := newTransportFactory(&factoryLocal{}, nil, nil, manager, nil)

			transport, err := factory.Open(context.Background(), appsession.StartSpec{
				Kind: appsession.KindSSH, HostID: "host-1", Hostname: "example.com",
				Port: 22, Username: "root", RemoteCommand: testCase.command,
			})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = transport.Close() })

			if client.spec.Command != testCase.command {
				t.Fatalf("terminal command = %q, want %q", client.spec.Command, testCase.command)
			}
		})
	}
}
