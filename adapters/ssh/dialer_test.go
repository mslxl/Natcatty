package ssh

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
)

func startFixture(t *testing.T, options fakessh.Options) (*fakessh.Server, func(context.Context, string, string) (net.Conn, error)) {
	t.Helper()
	clientConn, serverConn := fakessh.Pipe()
	server, err := fakessh.Start(serverConn, options)
	if err != nil {
		t.Fatalf("fakessh.Start: %v", err)
	}
	t.Cleanup(func() {
		_ = server.Close()
		_ = clientConn.Close()
	})
	return server, func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	}
}

func acceptEveryKey(context.Context, domainconn.Key) error { return nil }

func TestDialAuthenticatesWithPasswordAndOpensATerminal(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{Password: "s3cret"})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))

	var seen domainconn.Key
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "s3cret",
		VerifyHostKey: func(_ context.Context, key domainconn.Key) error {
			seen = key
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if server.PasswordAttempts() != 1 {
		t.Fatalf("password attempts = %d, want 1", server.PasswordAttempts())
	}
	username, password := server.LastCredentials()
	if username != "root" || password != "s3cret" {
		t.Fatalf("credentials = %q/%q, want root/s3cret", username, password)
	}
	if seen.Fingerprint != server.Fingerprint() {
		t.Fatalf("host key fingerprint = %q, want %q", seen.Fingerprint, server.Fingerprint())
	}
	if seen.KeyType != server.KeyType() {
		t.Fatalf("host key type = %q, want %q", seen.KeyType, server.KeyType())
	}
	if seen.Status != "" {
		t.Fatalf("adapter filled status %q; classification belongs to the application", seen.Status)
	}

	terminal, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	if size, ok := server.Pty(); !ok || size.Cols != 100 || size.Rows != 30 {
		t.Fatalf("PTY size = %+v, %v; want 100x30", size, ok)
	}
	if !server.ShellOpened() {
		t.Fatal("the adapter did not request a shell")
	}

	if _, err := terminal.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buffer := make([]byte, 5)
	if err := readFull(t, terminal, buffer); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buffer) != "hello" {
		t.Fatalf("output = %q, want hello", buffer)
	}

	if err := terminal.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	eventually(t, "the server to receive the resize", func() bool {
		resizes := server.Resizes()
		return len(resizes) > 0 && resizes[len(resizes)-1] == fakessh.Size{Cols: 120, Rows: 40}
	})

	if err := terminal.Close(); err != nil {
		t.Fatalf("terminal Close: %v", err)
	}
}

func TestWrongPasswordIsAnAuthError(t *testing.T) {
	_, dialFunc := startFixture(t, fakessh.Options{Password: "right"})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))

	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "wrong",
		VerifyHostKey: acceptEveryKey,
	})
	assertKind(t, err, appconnection.KindAuth)
}

func TestHostKeyRejectionAbortsBeforeAuthentication(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{Password: "s3cret"})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))

	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "s3cret",
		VerifyHostKey: func(context.Context, domainconn.Key) error {
			return &appconnection.Error{Kind: appconnection.KindHostKey, Cause: appconnection.ErrHostKeyRejected}
		},
	})
	assertKind(t, err, appconnection.KindHostKey)
	if !errors.Is(err, appconnection.ErrHostKeyRejected) {
		t.Fatalf("error = %v, want ErrHostKeyRejected", err)
	}
	if server.PasswordAttempts() != 0 {
		t.Fatalf("password attempts = %d after a rejected host key, want 0", server.PasswordAttempts())
	}
}

func TestDialTimeoutIsTyped(t *testing.T) {
	dialer := NewDialer(
		WithDialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		WithTimeouts(25*time.Millisecond, time.Second),
	)
	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "x",
		VerifyHostKey: acceptEveryKey,
	})
	assertKind(t, err, appconnection.KindTimeout)
}

func TestAuthTimeoutIsTyped(t *testing.T) {
	clientConn, serverConn := fakessh.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }),
		WithTimeouts(time.Second, 30*time.Millisecond),
	)
	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "x",
		VerifyHostKey: acceptEveryKey,
	})
	assertKind(t, err, appconnection.KindTimeout)
}

func TestAuthTimeoutCancelsTheHostKeyDecision(t *testing.T) {
	_, dialFunc := startFixture(t, fakessh.Options{Password: "s3cret"})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(time.Second, 30*time.Millisecond))

	cancelled := make(chan error, 1)
	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "s3cret",
		VerifyHostKey: func(ctx context.Context, _ domainconn.Key) error {
			<-ctx.Done()
			cancelled <- ctx.Err()
			return ctx.Err()
		},
	})
	assertKind(t, err, appconnection.KindTimeout)

	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("host-key callback context = %v, want cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the host-key callback was not released when authentication timed out")
	}
}

func TestNoCommonAlgorithmIsTypedUnsupported(t *testing.T) {
	_, dialFunc := startFixture(t, fakessh.Options{
		Password: "s3cret",
		Ciphers:  []string{"aes256-ctr"},
	})
	dialer := NewDialer(
		WithDialFunc(dialFunc),
		WithTimeouts(2*time.Second, 2*time.Second),
		WithAlgorithms([]string{"aes128-ctr"}, nil, nil, nil),
	)
	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "s3cret",
		VerifyHostKey: acceptEveryKey,
	})
	assertKind(t, err, appconnection.KindUnsupported)
}

func TestCancelledDialIsTyped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dialer := NewDialer(WithDialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		return nil, ctx.Err()
	}))
	_, err := dialer.Dial(ctx, appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "x",
		VerifyHostKey: acceptEveryKey,
	})
	assertKind(t, err, appconnection.KindCancelled)
}

func assertKind(t *testing.T, err error, want appconnection.Kind) {
	t.Helper()
	if err == nil {
		t.Fatal("Dial succeeded, want an error")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is %T, want *connection.Error", err, err)
	}
	if typed.Kind != want {
		t.Fatalf("error kind = %q, want %q (%v)", typed.Kind, want, err)
	}
}

func readFull(t *testing.T, reader interface{ Read([]byte) (int, error) }, buffer []byte) error {
	t.Helper()
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		_, err := ioReadFull(reader, buffer)
		done <- result{err}
	}()
	select {
	case got := <-done:
		return got.err
	case <-time.After(2 * time.Second):
		t.Fatal("read timed out")
		return nil
	}
}

func ioReadFull(reader interface{ Read([]byte) (int, error) }, buffer []byte) (int, error) {
	total := 0
	for total < len(buffer) {
		n, err := reader.Read(buffer[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func eventually(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
