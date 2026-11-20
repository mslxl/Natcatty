package ssh

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
)

// TestDialStreamHandsTheTargetRawWithoutAHandshake proves the routed Telnet
// shape: with no jump the seam returns the target's TCP stream untouched, so
// the Telnet negotiator sees the server's bytes instead of an SSH banner
// exchange.
func TestDialStreamHandsTheTargetRawWithoutAHandshake(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) { return client, nil }),
		WithTimeouts(2*time.Second, 2*time.Second),
	)

	stream, err := dialer.DialStream(context.Background(), appconnection.DialRequest{
		Hostname: "console.local", Port: 23,
	})
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	go func() { _, _ = server.Write([]byte("login: ")) }()
	greeting := make([]byte, len("login: "))
	if _, err := io.ReadFull(stream, greeting); err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	if string(greeting) != "login: " {
		t.Fatalf("greeting = %q, want the raw target bytes", greeting)
	}

	writeErr := make(chan error, 1)
	go func() {
		_, err := stream.Write([]byte("root\r\n"))
		writeErr <- err
	}()
	echo := make([]byte, len("root\r\n"))
	if _, err := io.ReadFull(server, echo); err != nil {
		t.Fatalf("target read: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("stream write: %v", err)
	}
	if string(echo) != "root\r\n" {
		t.Fatalf("target received %q, want the raw Telnet bytes", echo)
	}
}

// TestDialStreamAuthenticatesTheJumpAndForwardsToTheTarget covers the routed
// chain: the jump is a real SSH endpoint with its own credentials and host-key
// check, it forwards to the Telnet endpoint, and the returned stream is the
// raw forwarded connection. Closing the stream closes the jump.
func TestDialStreamAuthenticatesTheJumpAndForwardsToTheTarget(t *testing.T) {
	targetClient, targetServer := net.Pipe()
	t.Cleanup(func() {
		_ = targetClient.Close()
		_ = targetServer.Close()
	})
	jump := startChainFixture(t, fakessh.Options{
		Password: "jump-secret",
		ForwardDial: func(string, int) (net.Conn, error) {
			return targetClient, nil
		},
	})
	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) { return jump.client, nil }),
		WithTimeouts(2*time.Second, 2*time.Second),
	)
	verified := 0
	stream, err := dialer.DialStream(context.Background(), appconnection.DialRequest{
		Hostname: "console.local", Port: 23,
		Jumps: []appconnection.DialHop{{
			Label: "bastion", Hostname: "bastion.local", Port: 22, Username: "ops",
			Password: "jump-secret",
			VerifyHostKey: func(context.Context, domainconn.Key) error {
				verified++
				return nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}

	if got := jump.server.PasswordAttempts(); got != 1 {
		t.Fatalf("jump password attempts = %d, want one authenticated jump", got)
	}
	if username, password := jump.server.LastCredentials(); username != "ops" || password != "jump-secret" {
		t.Fatalf("jump credentials = %q/%q, want the hop's own", username, password)
	}
	if verified != 1 {
		t.Fatalf("host-key callbacks = %d, want one per jump", verified)
	}
	forwards := jump.server.Forwards()
	if len(forwards) != 1 || forwards[0].Host != "console.local" || forwards[0].Port != 23 {
		t.Fatalf("jump forwards = %+v, want the Telnet endpoint", forwards)
	}

	go func() { _, _ = targetServer.Write([]byte("ok")) }()
	reply := make([]byte, 2)
	if _, err := io.ReadFull(stream, reply); err != nil {
		t.Fatalf("read through the stream: %v", err)
	}
	if string(reply) != "ok" {
		t.Fatalf("stream payload = %q, want the raw target bytes", reply)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("stream Close: %v", err)
	}
	waitServer(t, jump.server)
}
