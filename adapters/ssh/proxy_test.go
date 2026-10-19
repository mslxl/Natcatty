package ssh

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

func startLoopbackProxy(t *testing.T, handle func(net.Conn)) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	return address.IP.String(), address.Port
}

func readProxyHeaders(conn net.Conn) (string, error) {
	var builder strings.Builder
	var last [4]byte
	for builder.Len() < maxProxyResponse {
		var b [1]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return "", err
		}
		builder.WriteByte(b[0])
		copy(last[:], last[1:])
		last[3] = b[0]
		if last == [4]byte{'\r', '\n', '\r', '\n'} {
			return builder.String(), nil
		}
	}
	return "", errors.New("headers too large")
}

func waitString(t *testing.T, values <-chan string) string {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy did not receive a request")
		return ""
	}
}

func TestHTTPConnectProxyTunnelsTheFirstEndpoint(t *testing.T) {
	fixture := startChainFixture(t, fakessh.Options{Password: "pw"})
	requests := make(chan string, 1)
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		headers, err := readProxyHeaders(conn)
		if err != nil {
			return
		}
		requests <- headers
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		bridge(conn, fixture.client)
	})

	dialer := NewDialer(WithTimeouts(5*time.Second, 5*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy: &appconnection.Proxy{
			Type: appconnection.ProxyHTTP, Host: host, Port: port,
			Username: "user", Password: "pass",
		},
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial through HTTP CONNECT: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	headers := waitString(t, requests)
	if first := strings.SplitN(headers, "\r\n", 2)[0]; first != "CONNECT target.internal:22 HTTP/1.1" {
		t.Fatalf("request line = %q", first)
	}
	if !strings.Contains(headers, "Host: target.internal:22\r\n") {
		t.Fatalf("missing Host header: %q", headers)
	}
	credentials := base64.StdEncoding.EncodeToString([]byte("user:pass"))
	if !strings.Contains(headers, "Proxy-Authorization: Basic "+credentials) {
		t.Fatalf("missing proxy credentials: %q", headers)
	}
	if username, password := fixture.server.LastCredentials(); username != "root" || password != "pw" {
		t.Fatalf("credentials = %q/%q", username, password)
	}
}

func TestHTTPConnectProxyTargetsTheFirstJumpNotTheDestination(t *testing.T) {
	target := startChainFixture(t, fakessh.Options{Password: "target"})
	jump := startChainFixture(t, fakessh.Options{
		Password: "jump",
		ForwardDial: func(string, int) (net.Conn, error) {
			return target.client, nil
		},
	})
	requests := make(chan string, 1)
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		headers, err := readProxyHeaders(conn)
		if err != nil {
			return
		}
		requests <- headers
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		bridge(conn, jump.client)
	})

	dialer := NewDialer(WithTimeouts(5*time.Second, 5*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "app", Password: "target",
		Jumps: []appconnection.DialHop{{
			Label: "bastion", Hostname: "bastion.internal", Port: 2222, Username: "ops",
			Password: "jump", VerifyHostKey: acceptKey,
		}},
		Proxy:         &appconnection.Proxy{Type: appconnection.ProxyHTTP, Host: host, Port: port},
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial through HTTP CONNECT: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	headers := waitString(t, requests)
	if first := strings.SplitN(headers, "\r\n", 2)[0]; first != "CONNECT bastion.internal:2222 HTTP/1.1" {
		t.Fatalf("request line = %q, want the first jump", first)
	}
	if forwards := jump.server.Forwards(); len(forwards) != 1 ||
		forwards[0].Host != "target.internal" || forwards[0].Port != 22 {
		t.Fatalf("jump forwards = %+v", forwards)
	}
}

func TestHTTPConnectProxyWithoutCredentialsSendsNoAuthorization(t *testing.T) {
	fixture := startChainFixture(t, fakessh.Options{Password: "pw"})
	requests := make(chan string, 1)
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		headers, err := readProxyHeaders(conn)
		if err != nil {
			return
		}
		requests <- headers
		_, _ = io.WriteString(conn, "HTTP/1.0 200 OK\r\n\r\n")
		bridge(conn, fixture.client)
	})

	dialer := NewDialer(WithTimeouts(5*time.Second, 5*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy:         &appconnection.Proxy{Type: appconnection.ProxyHTTP, Host: host, Port: port},
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial through HTTP CONNECT: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if headers := waitString(t, requests); strings.Contains(headers, "Proxy-Authorization") {
		t.Fatalf("unexpected authorization header: %q", headers)
	}
}

type socksRequest struct {
	methods  []byte
	username string
	password string
	host     string
	port     int
}

func TestSOCKS5ProxyTunnelsTheFirstEndpointWithAuthentication(t *testing.T) {
	fixture := startChainFixture(t, fakessh.Options{Password: "pw"})
	requests := make(chan socksRequest, 1)
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		request, err := serveSOCKS5(conn, true)
		if err != nil {
			return
		}
		requests <- request
		bridge(conn, fixture.client)
	})

	dialer := NewDialer(WithTimeouts(5*time.Second, 5*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy: &appconnection.Proxy{
			Type: appconnection.ProxySOCKS5, Host: host, Port: port,
			Username: "user", Password: "pass",
		},
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial through SOCKS5: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	var request socksRequest
	select {
	case request = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy did not receive a request")
	}
	if request.username != "user" || request.password != "pass" {
		t.Fatalf("credentials = %q/%q", request.username, request.password)
	}
	if request.host != "target.internal" || request.port != 22 {
		t.Fatalf("target = %s:%d", request.host, request.port)
	}
	if len(request.methods) != 2 || request.methods[0] != 0 || request.methods[1] != 2 {
		t.Fatalf("offered methods = %v, want no-auth and user/password", request.methods)
	}
}

func TestSOCKS5ProxyOffersOnlyNoAuthWithoutCredentials(t *testing.T) {
	fixture := startChainFixture(t, fakessh.Options{Password: "pw"})
	requests := make(chan socksRequest, 1)
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		request, err := serveSOCKS5(conn, false)
		if err != nil {
			return
		}
		requests <- request
		bridge(conn, fixture.client)
	})

	dialer := NewDialer(WithTimeouts(5*time.Second, 5*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy:         &appconnection.Proxy{Type: appconnection.ProxySOCKS5, Host: host, Port: port},
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial through SOCKS5: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	var request socksRequest
	select {
	case request = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy did not receive a request")
	}
	if len(request.methods) != 1 || request.methods[0] != 0 {
		t.Fatalf("offered methods = %v, want no-auth only", request.methods)
	}
	if request.host != "target.internal" || request.port != 22 {
		t.Fatalf("target = %s:%d", request.host, request.port)
	}
}

// serveSOCKS5 runs a minimal SOCKS5 server handshake and leaves the connection
// positioned at the start of the tunnel.
func serveSOCKS5(conn net.Conn, withAuth bool) (socksRequest, error) {
	var request socksRequest
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return request, err
	}
	if header[0] != 0x05 {
		return request, fmt.Errorf("version %d", header[0])
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return request, err
	}
	request.methods = methods

	if withAuth {
		if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
			return request, err
		}
		var auth [2]byte
		if _, err := io.ReadFull(conn, auth[:]); err != nil {
			return request, err
		}
		username := make([]byte, int(auth[1]))
		if _, err := io.ReadFull(conn, username); err != nil {
			return request, err
		}
		var size [1]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return request, err
		}
		password := make([]byte, int(size[0]))
		if _, err := io.ReadFull(conn, password); err != nil {
			return request, err
		}
		request.username, request.password = string(username), string(password)
		if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
			return request, err
		}
	} else {
		if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
			return request, err
		}
	}

	var connect [5]byte
	if _, err := io.ReadFull(conn, connect[:]); err != nil {
		return request, err
	}
	if connect[3] != 0x03 {
		return request, fmt.Errorf("address type %d, want domain", connect[3])
	}
	host := make([]byte, int(connect[4]))
	if _, err := io.ReadFull(conn, host); err != nil {
		return request, err
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return request, err
	}
	request.host = string(host)
	request.port = int(binary.BigEndian.Uint16(port[:]))
	_, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return request, err
}

func TestHTTPProxyRefusalIsATypedNetworkError(t *testing.T) {
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		if _, err := readProxyHeaders(conn); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\n\r\n")
	})
	dialer := NewDialer(WithTimeouts(5*time.Second, 5*time.Second))
	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy:         &appconnection.Proxy{Type: appconnection.ProxyHTTP, Host: host, Port: port},
		VerifyHostKey: acceptKey,
	})
	assertProxyFailure(t, err, appconnection.KindNetwork)
}

func TestSOCKS5RefusalIsATypedNetworkError(t *testing.T) {
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		var greeting [2]byte
		if _, err := io.ReadFull(conn, greeting[:]); err != nil {
			return
		}
		methods := make([]byte, int(greeting[1]))
		if _, err := io.ReadFull(conn, methods); err != nil {
			return
		}
		_, _ = conn.Write([]byte{0x05, 0x00})
		var connect [5]byte
		if _, err := io.ReadFull(conn, connect[:]); err != nil {
			return
		}
		target := make([]byte, int(connect[4])+2)
		if _, err := io.ReadFull(conn, target); err != nil {
			return
		}
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	})
	dialer := NewDialer(WithTimeouts(5*time.Second, 5*time.Second))
	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy:         &appconnection.Proxy{Type: appconnection.ProxySOCKS5, Host: host, Port: port},
		VerifyHostKey: acceptKey,
	})
	assertProxyFailure(t, err, appconnection.KindNetwork)
}

func TestProxyCancellationIsTypedCancelled(t *testing.T) {
	arrived := make(chan struct{}, 1)
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		if _, err := readProxyHeaders(conn); err == nil {
			arrived <- struct{}{}
		}
		// Hold the tunnel open without answering; cancellation closes it.
		_, _ = io.Copy(io.Discard, conn)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-arrived
		cancel()
	}()
	dialer := NewDialer(WithTimeouts(5*time.Second, 5*time.Second))
	_, err := dialer.Dial(ctx, appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy:         &appconnection.Proxy{Type: appconnection.ProxyHTTP, Host: host, Port: port},
		VerifyHostKey: acceptKey,
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindCancelled {
		t.Fatalf("error = %v, want a typed cancellation", err)
	}
}

func TestStalledProxyHandshakeIsTypedTimeout(t *testing.T) {
	host, port := startLoopbackProxy(t, func(conn net.Conn) {
		if _, err := readProxyHeaders(conn); err == nil {
			_, _ = io.Copy(io.Discard, conn)
		}
	})

	dialer := NewDialer(WithTimeouts(150*time.Millisecond, 5*time.Second))
	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy:         &appconnection.Proxy{Type: appconnection.ProxyHTTP, Host: host, Port: port},
		VerifyHostKey: acceptKey,
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindTimeout {
		t.Fatalf("error = %v, want a typed timeout", err)
	}
}

func TestUnknownProxyTypeIsUnsupported(t *testing.T) {
	dialer := NewDialer(WithTimeouts(time.Second, time.Second))
	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root",
		Proxy:         &appconnection.Proxy{Type: "ftp", Host: "proxy", Port: 21},
		VerifyHostKey: acceptKey,
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
		t.Fatalf("error = %v, want unsupported", err)
	}
	if !errors.Is(err, appconnection.ErrUnsupportedProxy) {
		t.Fatalf("error = %v, want ErrUnsupportedProxy", err)
	}
}

func assertProxyFailure(t *testing.T, err error, kind appconnection.Kind) {
	t.Helper()
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != kind {
		t.Fatalf("error = %v, want kind %s", err, kind)
	}
	if !errors.Is(err, appconnection.ErrProxyHandshake) {
		t.Fatalf("error = %v, want a proxy handshake failure", err)
	}
}

func TestSubstituteProxyCommandExpandsOnlyTheSupportedTokens(t *testing.T) {
	tests := []struct {
		name    string
		command string
		host    string
		port    int
		goos    string
		want    string
	}{
		{
			name: "posix", command: "ssh -W %h:%p bastion", host: "example.com", port: 22,
			goos: "linux", want: "ssh -W 'example.com':'22' bastion",
		},
		{
			name: "escaped percent", command: "echo 100%% %h", host: "example.com", port: 22,
			goos: "linux", want: "echo 100% 'example.com'",
		},
		{
			name: "unknown token is left alone", command: "ssh %x %h", host: "example.com", port: 22,
			goos: "linux", want: "ssh %x 'example.com'",
		},
		{
			name: "posix single quote", command: "%h", host: "a'b", port: 22,
			goos: "linux", want: `'a'\''b'`,
		},
		{
			name: "windows", command: "%h:%p", host: "example.com", port: 2222,
			goos: "windows", want: `"example.com":"2222"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := substituteProxyCommand(test.command, test.host, test.port, test.goos)
			if err != nil {
				t.Fatalf("substitute: %v", err)
			}
			if got != test.want {
				t.Fatalf("command = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSubstituteProxyCommandRejectsUnsafeWindowsTargets(t *testing.T) {
	for _, host := range []string{`a"b`, "a%b", "a!b", "a\nb"} {
		if _, err := substituteProxyCommand("ssh %h", host, 22, "windows"); err == nil {
			t.Errorf("host %q was accepted", host)
		}
	}
}

// TestProxyCommandHelperProcess is the child side of the ProxyCommand test. It
// connects to the target on stdin/stdout and is otherwise inert, so a normal
// test run skips it.
func TestProxyCommandHelperProcess(t *testing.T) {
	if os.Getenv("NC_PROXY_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(2)
	}
	if os.Args[separator+1] == "stall" {
		// Stay alive without touching stdout so cancellation must terminate
		// the process rather than waiting for a protocol response.
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if separator+2 >= len(os.Args) {
		os.Exit(2)
	}
	host := os.Args[separator+1]
	port, err := strconv.Atoi(os.Args[separator+2])
	if err != nil {
		os.Exit(2)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "proxy helper dial: %v", err)
		os.Exit(3)
	}
	go func() { _, _ = io.Copy(conn, os.Stdin) }()
	_, _ = io.Copy(os.Stdout, conn)
	os.Exit(0)
}

func TestProxyCommandRunsTheConfiguredProcess(t *testing.T) {
	if runtime.GOOS == "js" {
		t.Skip("process spawning is unavailable")
	}
	t.Setenv("NC_PROXY_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	quoted, err := quoteShellArg(executable, runtime.GOOS)
	if err != nil {
		t.Fatalf("quote executable: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	servers := make(chan *fakessh.Server, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		server, err := fakessh.Start(conn, fakessh.Options{Password: "pw"})
		if err != nil {
			_ = conn.Close()
			return
		}
		servers <- server
	}()
	address := listener.Addr().(*net.TCPAddr)

	command := quoted + " -test.run=TestProxyCommandHelperProcess -- %h %p"
	dialer := NewDialer(WithTimeouts(10*time.Second, 10*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: address.IP.String(), Port: address.Port, Username: "root", Password: "pw",
		Proxy:         &appconnection.Proxy{Type: appconnection.ProxyCommand, Command: command},
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial through ProxyCommand: %v", err)
	}
	var server *fakessh.Server
	select {
	case server = <-servers:
	case <-time.After(5 * time.Second):
		_ = client.Close()
		t.Fatal("the ProxyCommand process never reached the SSH server")
	}
	if username, password := server.LastCredentials(); username != "root" || password != "pw" {
		t.Fatalf("credentials = %q/%q", username, password)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitServer(t, server)
}

func TestProxyCommandCancellationStopsTheSpawnedProcess(t *testing.T) {
	if runtime.GOOS == "js" {
		t.Skip("process spawning is unavailable")
	}
	t.Setenv("NC_PROXY_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	quoted, err := quoteShellArg(executable, runtime.GOOS)
	if err != nil {
		t.Fatalf("quote executable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Give the process time to start, then cancel while the handshake is
	// waiting for its stdout.
	time.AfterFunc(300*time.Millisecond, cancel)

	dialer := NewDialer(WithTimeouts(5*time.Second, 5*time.Second))
	started := time.Now()
	_, err = dialer.Dial(ctx, appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root", Password: "pw",
		Proxy: &appconnection.Proxy{
			Type:    appconnection.ProxyCommand,
			Command: quoted + " -test.run=TestProxyCommandHelperProcess -- stall",
		},
		VerifyHostKey: acceptKey,
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindCancelled {
		t.Fatalf("error = %v, want a typed cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Dial took %s after cancellation", elapsed)
	}
}
