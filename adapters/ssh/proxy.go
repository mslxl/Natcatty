package ssh

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/platform/process"
)

// ProcessStartFunc spawns a ProxyCommand and returns a connection over its
// stdin and stdout. Tests replace it with an in-process pipe; production uses
// the platform process seam.
type ProcessStartFunc func(command string) (net.Conn, error)

// maxProxyResponse bounds the HTTP CONNECT response headers.
const maxProxyResponse = 64 << 10

// dialProxy opens a transport to the first endpoint through proxy. It owns the
// handshake and returns a connection ready for SSH bytes.
func (d *Dialer) dialProxy(ctx context.Context, proxy *appconnection.Proxy, targetHost string, targetPort int) (net.Conn, error) {
	if proxy == nil {
		return nil, &appconnection.Error{Kind: appconnection.KindInternal, Cause: appconnection.ErrInvalidProxy}
	}
	switch proxy.Type {
	case appconnection.ProxyCommand:
		return d.dialProxyCommand(ctx, proxy, targetHost, targetPort)
	case appconnection.ProxyHTTP, appconnection.ProxySOCKS5:
		return d.dialSocketProxy(ctx, proxy, targetHost, targetPort)
	default:
		return nil, &appconnection.Error{
			Kind:  appconnection.KindUnsupported,
			Cause: fmt.Errorf("%w: %q", appconnection.ErrUnsupportedProxy, proxy.Type),
		}
	}
}

// dialProxyCommand starts the process and returns its stdio immediately. There
// is no separate connect step to bound: the SSH handshake is the first I/O, so
// the authentication deadline and the caller's context bound it.
func (d *Dialer) dialProxyCommand(ctx context.Context, proxy *appconnection.Proxy, targetHost string, targetPort int) (net.Conn, error) {
	command, err := substituteProxyCommand(proxy.Command, targetHost, targetPort, runtime.GOOS)
	if err != nil {
		return nil, &appconnection.Error{Kind: appconnection.KindInternal, Cause: err}
	}
	if strings.TrimSpace(command) == "" {
		return nil, &appconnection.Error{Kind: appconnection.KindInternal, Cause: appconnection.ErrInvalidProxy}
	}
	start := d.processStart
	if start == nil {
		start = process.Start
	}
	conn, err := start(command)
	if err != nil {
		return nil, &appconnection.Error{
			Kind:  appconnection.KindNetwork,
			Cause: fmt.Errorf("%w: ProxyCommand failed to start: %v", appconnection.ErrProxyHandshake, err),
		}
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, classifyDialFailure(ctx, ctx, err)
	}
	return conn, nil
}

func (d *Dialer) dialSocketProxy(ctx context.Context, proxy *appconnection.Proxy, targetHost string, targetPort int) (net.Conn, error) {
	if proxy.Host == "" || proxy.Port <= 0 || proxy.Port > 65535 {
		return nil, &appconnection.Error{Kind: appconnection.KindInternal, Cause: appconnection.ErrInvalidProxy}
	}
	address := net.JoinHostPort(proxy.Host, strconv.Itoa(proxy.Port))
	conn, err := d.dial(ctx, "tcp", address)
	if err != nil {
		return nil, classifyDialFailure(ctx, ctx, err)
	}
	stop := watchCancellation(ctx, conn)
	success := false
	defer func() {
		if !success {
			stop()
			_ = conn.Close()
		}
	}()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	var handshakeErr error
	if proxy.Type == appconnection.ProxyHTTP {
		handshakeErr = httpConnect(conn, proxy, targetHost, targetPort)
	} else {
		handshakeErr = socks5Connect(conn, proxy, targetHost, targetPort)
	}
	if handshakeErr != nil {
		return nil, classifyProxyHandshake(ctx, handshakeErr)
	}
	// The handshake deadline must not survive into the SSH transport.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, &appconnection.Error{
			Kind:  appconnection.KindNetwork,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrProxyHandshake, err),
		}
	}
	stop()
	success = true
	return conn, nil
}

// watchCancellation closes conn when ctx is cancelled. The returned stop
// function disarms the watcher once the connection belongs to the transport.
func watchCancellation(ctx context.Context, conn net.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
	}
}

func httpConnect(conn net.Conn, proxy *appconnection.Proxy, targetHost string, targetPort int) error {
	authority := net.JoinHostPort(targetHost, strconv.Itoa(targetPort))
	request := "CONNECT " + authority + " HTTP/1.1\r\nHost: " + authority + "\r\n"
	if proxy.Username != "" && proxy.Password != "" {
		credentials := base64.StdEncoding.EncodeToString([]byte(proxy.Username + ":" + proxy.Password))
		request += "Proxy-Authorization: Basic " + credentials + "\r\n"
	}
	request += "\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		return proxyHandshakeError(err)
	}

	response, err := readHeaders(conn)
	if err != nil {
		return proxyHandshakeError(err)
	}
	status := strings.SplitN(response, "\r\n", 2)[0]
	if !strings.HasPrefix(status, "HTTP/1.1 200") && !strings.HasPrefix(status, "HTTP/1.0 200") {
		return proxyHandshakeError(fmt.Errorf("HTTP proxy refused %s", status))
	}
	return nil
}

// readHeaders reads through the blank line that ends an HTTP response. It
// reads one byte at a time so no byte after the headers is consumed; those
// bytes belong to the SSH handshake.
func readHeaders(conn net.Conn) (string, error) {
	headers := make([]byte, 0, 256)
	var b [1]byte
	for len(headers) < maxProxyResponse {
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return "", err
		}
		headers = append(headers, b[0])
		if n := len(headers); n >= 4 &&
			headers[n-4] == '\r' && headers[n-3] == '\n' &&
			headers[n-2] == '\r' && headers[n-1] == '\n' {
			return string(headers), nil
		}
	}
	return "", errors.New("HTTP proxy response headers are too large")
}

func socks5Connect(conn net.Conn, proxy *appconnection.Proxy, targetHost string, targetPort int) error {
	withAuth := proxy.Username != "" && proxy.Password != ""
	methods := []byte{0x00}
	if withAuth {
		methods = []byte{0x00, 0x02}
	}
	greeting := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err := conn.Write(greeting); err != nil {
		return proxyHandshakeError(err)
	}

	var selection [2]byte
	if _, err := io.ReadFull(conn, selection[:]); err != nil {
		return proxyHandshakeError(err)
	}
	if selection[0] != 0x05 {
		return proxyHandshakeError(errors.New("invalid SOCKS5 version"))
	}
	switch selection[1] {
	case 0x00:
	case 0x02:
		if !withAuth {
			return proxyHandshakeError(errors.New("SOCKS5 proxy requires authentication"))
		}
		if err := socks5Authenticate(conn, proxy.Username, proxy.Password); err != nil {
			return err
		}
	default:
		return proxyHandshakeError(errors.New("SOCKS5 authentication method not supported"))
	}

	host := []byte(targetHost)
	if len(host) == 0 || len(host) > 255 {
		return proxyHandshakeError(errors.New("SOCKS5 target host is not a valid domain name"))
	}
	request := make([]byte, 0, 7+len(host))
	request = append(request, 0x05, 0x01, 0x00, 0x03, byte(len(host)))
	request = append(request, host...)
	request = binary.BigEndian.AppendUint16(request, uint16(targetPort))
	if _, err := conn.Write(request); err != nil {
		return proxyHandshakeError(err)
	}

	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return proxyHandshakeError(err)
	}
	if reply[0] != 0x05 {
		return proxyHandshakeError(errors.New("invalid SOCKS5 reply version"))
	}
	if reply[1] != 0x00 {
		return proxyHandshakeError(fmt.Errorf("SOCKS5 error: %s", socks5ErrorText(reply[1])))
	}
	var addressLength int
	switch reply[3] {
	case 0x01:
		addressLength = 4
	case 0x04:
		addressLength = 16
	case 0x03:
		var size [1]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return proxyHandshakeError(err)
		}
		addressLength = int(size[0])
	default:
		return proxyHandshakeError(errors.New("SOCKS5 address type not supported"))
	}
	skip := make([]byte, addressLength+2)
	if _, err := io.ReadFull(conn, skip); err != nil {
		return proxyHandshakeError(err)
	}
	return nil
}

func socks5Authenticate(conn net.Conn, username, password string) error {
	if len(username) > 255 || len(password) > 255 {
		return proxyHandshakeError(errors.New("SOCKS5 credentials are too long"))
	}
	request := make([]byte, 0, 3+len(username)+len(password))
	request = append(request, 0x01, byte(len(username)))
	request = append(request, username...)
	request = append(request, byte(len(password)))
	request = append(request, password...)
	if _, err := conn.Write(request); err != nil {
		return proxyHandshakeError(err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return proxyHandshakeError(err)
	}
	if reply[0] != 0x01 || reply[1] != 0x00 {
		return proxyHandshakeError(errors.New("SOCKS5 authentication failed"))
	}
	return nil
}

func socks5ErrorText(code byte) string {
	switch code {
	case 0x01:
		return "General failure"
	case 0x02:
		return "Connection not allowed"
	case 0x03:
		return "Network unreachable"
	case 0x04:
		return "Host unreachable"
	case 0x05:
		return "Connection refused"
	case 0x06:
		return "TTL expired"
	case 0x07:
		return "Command not supported"
	case 0x08:
		return "Address type not supported"
	default:
		return "Unknown"
	}
}

// classifyProxyHandshake keeps cancellation and deadline failures distinct
// from a proxy that refused or malformed the tunnel handshake. The watcher
// closes the connection on cancellation, so the low-level error alone looks
// like a network failure.
func classifyProxyHandshake(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return &appconnection.Error{Kind: appconnection.KindCancelled, Cause: err}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &appconnection.Error{
			Kind:  appconnection.KindTimeout,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrDialTimeout, err),
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &appconnection.Error{
			Kind:  appconnection.KindTimeout,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrProxyHandshake, err),
		}
	}
	var typed *appconnection.Error
	if errors.As(err, &typed) {
		return err
	}
	return proxyHandshakeError(err)
}

func proxyHandshakeError(cause error) error {
	return &appconnection.Error{
		Kind:  appconnection.KindNetwork,
		Cause: fmt.Errorf("%w: %w", appconnection.ErrProxyHandshake, cause),
	}
}

// substituteProxyCommand expands the original's three tokens. Every other
// percent sequence is left alone, and substituted values are shell-quoted for
// goos so a Hostname or port cannot inject shell syntax.
func substituteProxyCommand(command, targetHost string, targetPort int, goos string) (string, error) {
	quotedHost, err := quoteShellArg(targetHost, goos)
	if err != nil {
		return "", err
	}
	quotedPort, err := quoteShellArg(strconv.Itoa(targetPort), goos)
	if err != nil {
		return "", err
	}
	return strings.NewReplacer(
		"%%", "%",
		"%h", quotedHost,
		"%p", quotedPort,
	).Replace(command), nil
}

func quoteShellArg(value, goos string) (string, error) {
	if goos == "windows" {
		return quoteWindowsArg(value)
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'", nil
}

func quoteWindowsArg(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n\"%!") {
		return "", fmt.Errorf("%w: ProxyCommand target contains characters that cannot be safely substituted on Windows", appconnection.ErrInvalidProxy)
	}
	return `"` + value + `"`, nil
}
