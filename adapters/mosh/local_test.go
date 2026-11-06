// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

// Supplementary native-server test. The matrix's macOS and FreeBSD cells use
// a real sshd supplied by the runner (see adapters/mosh/lab). A developer
// host without passwordless sudo cannot start an sshd, but it can still run
// the platform's own stock mosh-server directly. This test is that local
// evidence: real stock mosh-server, real UDP, real terminal model, with the
// SSH bootstrap replaced by executing the same remote script locally.
//
// It is opt-in and never substitutes for a matrix cell:
//
//	NATCATTY_MOSH_LOCAL_SERVER=/opt/homebrew/bin/mosh-server \
//	NATCATTY_MOSH_LOCAL_VERSION=1.4.0 \
//	  go test ./adapters/mosh -run TestLocalMoshServer -v

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
)

// localExecBootstrap runs the mosh-server command that would normally run over
// SSH on this host. mosh-server prints MOSH CONNECT and daemonizes; the stream
// exposes that announcement to the same announce() parser the SSH bootstrap
// uses.
type localExecBootstrap struct{}

func (b localExecBootstrap) OpenBootstrap(ctx context.Context, _ appsession.StartSpec,
	cfg moshmodel.Config) (io.ReadWriteCloser, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.RemoteScript())
	// Stock mosh-server adopts the client-supplied -l locale variables only
	// when the host's own locale is not UTF-8. Pin the server-side locale to C
	// (like the lab wrappers) so the test does not depend on the developer's
	// shell locale and still exercises the client handoff.
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "LANG" || name == "LC_ALL" {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env, "LANG=C", "LC_ALL=C")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	var copies sync.WaitGroup
	copies.Add(2)
	pipe := func(source io.Reader) {
		defer copies.Done()
		_, _ = io.Copy(writer, source)
	}
	go pipe(stdout)
	go pipe(stderr)
	go func() {
		copies.Wait()
		_ = writer.Close()
	}()
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	return &localExecStream{reader: reader, cmd: cmd}, nil
}

type localExecStream struct {
	reader    *io.PipeReader
	cmd       *exec.Cmd
	closeOnce sync.Once
}

func (s *localExecStream) Read(p []byte) (int, error) { return s.reader.Read(p) }

func (s *localExecStream) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func (s *localExecStream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.reader.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	})
	return nil
}

// TestLocalMoshServer drives the host's stock mosh-server through the real
// in-process transport: bootstrap announcement, terminal output, remote
// resize, locale propagation, and shutdown. It is supplementary local
// evidence, not a matrix cell (the gate pins matrix cells to the lab
// topology), but it exercises the exact binary a Homebrew install provides.
func TestLocalMoshServer(t *testing.T) {
	serverPath := os.Getenv("NATCATTY_MOSH_LOCAL_SERVER")
	if serverPath == "" {
		t.Skip("set NATCATTY_MOSH_LOCAL_SERVER to the host's stock mosh-server binary")
	}
	expected := os.Getenv("NATCATTY_MOSH_LOCAL_VERSION")
	if expected == "" {
		t.Fatal("NATCATTY_MOSH_LOCAL_VERSION must name the stock version under test")
	}
	versionOut, err := exec.Command(serverPath, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("%s --version: %v\n%s", serverPath, err, versionOut)
	}
	if !strings.Contains(string(versionOut), "mosh "+expected) {
		t.Fatalf("%s --version = %q, want mosh %s", serverPath, versionOut, expected)
	}

	const locale = "de_DE.UTF-8"
	spec := inProcessTestSpec()
	spec.Mosh.ServerPath = serverPath
	spec.Mosh.Locales = map[string]string{"LANG": locale, "LC_ALL": locale}
	spec.Mosh.Lang = locale
	spec.Env = []string{"LANG=" + locale, "LC_ALL=" + locale}

	factory := InProcessFactory{
		Bootstrap:        localExecBootstrap{},
		ProbeTimeout:     2 * time.Second,
		HandshakeTimeout: 20 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
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
	setScreenSizeSink(t, transport)

	if _, err := transport.Write([]byte(`printf 'NATCATTY_LOCAL_LOCALE:%s\n' "$LANG"` + "\r")); err != nil {
		t.Fatalf("write locale probe: %v", err)
	}
	readUntilCount(t, transport, "NATCATTY_LOCAL_LOCALE:"+locale, 1, 15*time.Second)

	if err := transport.Resize(101, 33); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if _, err := transport.Write([]byte("printf 'NATCATTY_LOCAL_SIZE:%s\\n' \"$(stty size)\"\r")); err != nil {
		t.Fatalf("write resize probe: %v", err)
	}
	// The echoed command line contains the literal $(stty size), not the
	// expanded marker, so the remote shell's output is the only occurrence.
	readUntilCount(t, transport, "NATCATTY_LOCAL_SIZE:33 101", 1, 15*time.Second)

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v, want closed", end)
	}

	// Close sends the shutdown sentinel; the stock server must exit. pgrep
	// matches the server path that only this test launched.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := exec.Command("pgrep", "-f", serverPath).Run()
		if err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("stock mosh-server is still running after the client's shutdown")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
