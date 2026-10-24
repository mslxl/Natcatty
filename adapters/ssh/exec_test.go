package ssh

import (
	"context"
	"io"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

// dialExecFixture starts the in-process SSH server with an exec handler and
// returns an authenticated client.
func dialExecFixture(t *testing.T, handler func(string, xssh.Channel) int) *sshClient {
	t.Helper()
	server, dialFunc := startFixture(t, fakessh.Options{Password: "s3cret", Exec: handler})
	_ = server
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "tester", Password: "s3cret",
		VerifyHostKey: acceptEveryKey,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	concrete, ok := client.(*sshClient)
	if !ok {
		t.Fatalf("Dial returned %T", client)
	}
	return concrete
}

// TestExecRunsACommandAndReportsItsOutputAndStatus proves the non-interactive
// command path carries stdout, stderr, and the remote exit status.
func TestExecRunsACommandAndReportsItsOutputAndStatus(t *testing.T) {
	client := dialExecFixture(t, func(command string, channel xssh.Channel) int {
		_, _ = io.WriteString(channel, "home=/root\n")
		_, _ = io.WriteString(channel.Stderr(), "warning\n")
		return 7
	})

	result, err := client.Exec(context.Background(), "printf '%s\\n' \"$HOME\"")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.Stdout != "home=/root\n" || result.Stderr != "warning\n" {
		t.Fatalf("result = %+v", result)
	}
	if !result.HasExitCode || result.ExitCode != 7 {
		t.Fatalf("exit = %d (has=%v), want 7", result.ExitCode, result.HasExitCode)
	}
}

// TestOpenExecCarriesTheRawStream proves the SCP-style channel keeps stdin,
// stdout, and stderr separate and can be closed by the caller.
func TestOpenExecCarriesTheRawStream(t *testing.T) {
	client := dialExecFixture(t, func(command string, channel xssh.Channel) int {
		_, _ = io.WriteString(channel, "ready\n")
		buffer := make([]byte, 4)
		if _, err := io.ReadFull(channel, buffer); err != nil {
			return 1
		}
		_, _ = io.WriteString(channel, "pong")
		_, _ = io.WriteString(channel.Stderr(), "note")
		return 0
	})

	stream, err := client.OpenExec(context.Background(), "scp -t -- '/tmp'")
	if err != nil {
		t.Fatalf("OpenExec: %v", err)
	}
	defer stream.Close()

	ready := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(stream, ready); err != nil {
		t.Fatalf("read ready: %v", err)
	}
	if string(ready) != "ready\n" {
		t.Fatalf("ready = %q", ready)
	}
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	pong := make([]byte, len("pong"))
	if _, err := io.ReadFull(stream, pong); err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if string(pong) != "pong" {
		t.Fatalf("pong = %q", pong)
	}
	stderr, err := io.ReadAll(stream.Stderr())
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if string(stderr) != "note" {
		t.Fatalf("stderr = %q", stderr)
	}
	if code, err := stream.ExitCode(); err != nil || code != 0 {
		t.Fatalf("ExitCode = %d, %v", code, err)
	}
}

// TestExecCancellationBoundsTheCommand proves a hung command cannot outlive
// its context.
func TestExecCancellationBoundsTheCommand(t *testing.T) {
	released := make(chan struct{})
	client := dialExecFixture(t, func(command string, channel xssh.Channel) int {
		<-released
		return 0
	})
	t.Cleanup(func() { close(released) })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := client.Exec(ctx, "sleep 60"); err == nil {
		t.Fatal("a hung command outlived its context")
	}
}
