// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
)

// TestInProcessAdapterRealMoshServer exercises the in-process client against
// stock mosh-server 1.3.2 and 1.4.0 over a real SSH bootstrap and real UDP.
//
// It is gated so the normal suite needs no Docker:
//
//	NATCATTY_MOSH_INTEGRATION=1 go test ./adapters/mosh -run TestInProcessAdapterRealMoshServer -v
//
// The default topology matches the lab containers in the Mosh tickets:
// mosh-ssh-132 on 127.0.0.1:2224 and mosh-ssh-140 on 127.0.0.1:2225, user
// tester, password testpass, with wrappers forcing published UDP ports 60111
// and 60121.
func TestInProcessAdapterRealMoshServer(t *testing.T) {
	if os.Getenv("NATCATTY_MOSH_INTEGRATION") == "" {
		t.Skip("set NATCATTY_MOSH_INTEGRATION=1 to run against stock mosh-server")
	}
	host := envOr("NATCATTY_MOSH_SSH_HOST", "127.0.0.1")
	user := envOr("NATCATTY_MOSH_SSH_USER", "tester")
	password := envOr("NATCATTY_MOSH_SSH_PASSWORD", "testpass")

	cases := []struct {
		name       string
		sshPort    int
		serverPath string
		udpPort    string
		marker     string
	}{
		{
			name:       "mosh-server 1.3.2",
			sshPort:    envInt("NATCATTY_MOSH_132_SSH_PORT", 2224),
			serverPath: "/usr/local/bin/mosh-60111",
			udpPort:    "60111",
			marker:     "NATCATTY_MOSH_OK_132",
		},
		{
			name:       "mosh-server 1.4.0",
			sshPort:    envInt("NATCATTY_MOSH_140_SSH_PORT", 2225),
			serverPath: "/usr/local/bin/mosh-60121",
			udpPort:    "60121",
			marker:     "NATCATTY_MOSH_OK_140",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			runRealMoshCase(t, host, testCase.sshPort, user, password, testCase.serverPath, testCase.udpPort, testCase.marker)
		})
	}
}

func runRealMoshCase(t *testing.T, host string, sshPort int, user, password, serverPath, udpPort, marker string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	pattern := fmt.Sprintf("[m]osh-server.*-p %s", udpPort)
	sshRun(t, host, sshPort, user, password, fmt.Sprintf(
		`for p in $(pgrep -f '%s' 2>/dev/null); do kill "$p" 2>/dev/null; done; true`, pattern))
	t.Cleanup(func() {
		sshRunQuiet(host, sshPort, user, password, fmt.Sprintf(
			`for p in $(pgrep -f '%s' 2>/dev/null); do kill "$p" 2>/dev/null; done; true`, pattern))
	})

	spec := appsession.StartSpec{
		Kind:     appsession.KindMosh,
		Hostname: host,
		Port:     sshPort,
		Username: user,
		Cols:     80,
		Rows:     24,
		Env:      []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8"},
		Mosh: &moshmodel.Config{
			Hostname:   host,
			Port:       sshPort,
			Username:   user,
			ServerPath: serverPath,
			Lang:       "C.UTF-8",
			Locales:    map[string]string{"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"},
			Cols:       80,
			Rows:       24,
		},
	}
	factory := InProcessFactory{
		Bootstrap:        sshBootstrap{host: host, port: sshPort, user: user, password: password},
		ProbeTimeout:     800 * time.Millisecond,
		HandshakeTimeout: 20 * time.Second,
	}
	transport, err := factory.Open(ctx, spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = transport.Close()
		}
	})

	// A usable interactive shell echoes the typed command and then its output.
	command := "echo " + marker
	if _, err := transport.Write([]byte(command + "\r")); err != nil {
		t.Fatalf("write command: %v", err)
	}
	readUntilCount(t, transport, marker, 2, 10*time.Second)

	// The resize must reach mosh-server and be applied to the remote PTY.
	if err := transport.Resize(100, 31); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if _, err := transport.Write([]byte("printf 'SIZE:%s\\n' \"$(stty size)\"\r")); err != nil {
		t.Fatalf("write resize probe: %v", err)
	}
	readUntil(t, transport, "SIZE:31 100", 10*time.Second)

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v, want closed", end)
	}

	// Closing must stop the stock mosh-server, not merely close our socket.
	deadline := time.Now().Add(3 * time.Second)
	for {
		running, runErr := remoteServerRunning(host, sshPort, user, password, pattern)
		if runErr != nil {
			t.Fatalf("check mosh-server: %v", runErr)
		}
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("mosh-server is still running after the client's shutdown")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// remoteServerRunning runs pgrep over a fresh SSH session. Exit status 1 means
// no matching process; any other failure is an SSH failure, not a result.
func remoteServerRunning(host string, port int, user, password, pattern string) (bool, error) {
	_, err := sshRunOutput(host, port, user, password, fmt.Sprintf(`pgrep -f '%s' >/dev/null`, pattern))
	if err == nil {
		return true, nil
	}
	var exitError *ssh.ExitError
	if errors.As(err, &exitError) {
		return false, nil
	}
	return false, err
}

// readUntilCount reads transport output until marker has appeared count times.
func readUntilCount(t *testing.T, transport appsession.Transport, marker string, count int, timeout time.Duration) string {
	t.Helper()
	result := make(chan string, 1)
	go func() {
		var collected []byte
		buffer := make([]byte, 4096)
		for {
			n, err := transport.Read(buffer)
			if n > 0 {
				collected = append(collected, buffer[:n]...)
				if strings.Count(string(collected), marker) >= count {
					result <- string(collected)
					return
				}
			}
			if err != nil {
				result <- string(collected)
				return
			}
		}
	}()
	select {
	case text := <-result:
		if got := strings.Count(text, marker); got < count {
			t.Fatalf("output %q contains %q %d times, want %d", text, marker, got, count)
		}
		return text
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %q to appear %d times", marker, count)
		return ""
	}
}

// sshBootstrap opens a real SSH exec channel for mosh-server.
type sshBootstrap struct {
	host     string
	port     int
	user     string
	password string
}

func (b sshBootstrap) OpenBootstrap(ctx context.Context, _ appsession.StartSpec, cfg moshmodel.Config) (io.ReadWriteCloser, error) {
	client, err := ssh.Dial("tcp", net.JoinHostPort(b.host, strconv.Itoa(b.port)), &ssh.ClientConfig{
		User:            b.user,
		Auth:            []ssh.AuthMethod{ssh.Password(b.password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	reader, writer := io.Pipe()
	var copies sync.WaitGroup
	copies.Add(2)
	go func() {
		defer copies.Done()
		_, _ = io.Copy(writer, stdout)
	}()
	go func() {
		defer copies.Done()
		_, _ = io.Copy(writer, stderr)
	}()
	go func() {
		copies.Wait()
		_ = writer.Close()
	}()
	if err := session.Start(cfg.RemoteCommand()); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	stream := &sshExecStream{reader: reader, stdin: stdin, session: session, client: client}
	context.AfterFunc(ctx, func() { _ = stream.Close() })
	return stream, nil
}

type sshExecStream struct {
	reader    *io.PipeReader
	stdin     io.WriteCloser
	session   *ssh.Session
	client    *ssh.Client
	closeOnce sync.Once
}

func (s *sshExecStream) Read(p []byte) (int, error) { return s.reader.Read(p) }

func (s *sshExecStream) Write(p []byte) (int, error) { return s.stdin.Write(p) }

func (s *sshExecStream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.reader.Close()
		_ = s.stdin.Close()
		_ = s.session.Close()
		_ = s.client.Close()
	})
	return nil
}

func sshRun(t *testing.T, host string, port int, user, password, command string) {
	t.Helper()
	if _, err := sshRunOutput(host, port, user, password, command); err != nil {
		t.Fatalf("ssh %s: %v", command, err)
	}
}

func sshRunQuiet(host string, port int, user, password, command string) {
	_, _ = sshRunOutput(host, port, user, password, command)
}

func sshRunOutput(host string, port int, user, password, command string) (string, error) {
	client, err := ssh.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)), &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return "", err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	output, err := session.CombinedOutput(command)
	return string(output), err
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
