// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
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

	for _, testCase := range labCases(t) {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			runRealMoshCase(t, host, testCase.sshPort, user, password, testCase.serverPath, testCase.udpPort, testCase.marker)
		})
	}
}

// labCase describes one real mosh-server instance in the lab. The parity gate
// runs one cell per mosh-server version and points the generic environment at
// that cell's SSH endpoint, wrapper binary, and published UDP port, so the
// same test code proves every matrix entry.
type labCase struct {
	name       string
	version    string
	sshPort    int
	serverPath string
	udpPort    string
	marker     string
}

// labCases returns the stock servers this test run must cover. The legacy
// defaults are the mosh-ssh-132/mosh-ssh-140 containers used by earlier
// tickets; NATCATTY_MOSH_SERVER_VERSION narrows a gate cell to one version and
// NATCATTY_MOSH_SSH_PORT/SERVER_PATH/UDP_PORT override the cell endpoint.
func labCases(t *testing.T) []labCase {
	t.Helper()
	cases := []labCase{
		{
			name:       "mosh-server 1.3.2",
			version:    "1.3.2",
			sshPort:    envInt("NATCATTY_MOSH_132_SSH_PORT", 2224),
			serverPath: envOr("NATCATTY_MOSH_132_SERVER_PATH", "/usr/local/bin/mosh-60111"),
			udpPort:    envOr("NATCATTY_MOSH_132_UDP_PORT", "60111"),
			marker:     "NATCATTY_MOSH_OK_132",
		},
		{
			name:       "mosh-server 1.4.0",
			version:    "1.4.0",
			sshPort:    envInt("NATCATTY_MOSH_140_SSH_PORT", 2225),
			serverPath: envOr("NATCATTY_MOSH_140_SERVER_PATH", "/usr/local/bin/mosh-60121"),
			udpPort:    envOr("NATCATTY_MOSH_140_UDP_PORT", "60121"),
			marker:     "NATCATTY_MOSH_OK_140",
		},
	}
	if value := os.Getenv("NATCATTY_MOSH_SSH_PORT"); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("NATCATTY_MOSH_SSH_PORT = %q: %v", value, err)
		}
		for i := range cases {
			cases[i].sshPort = port
		}
	}
	if value := os.Getenv("NATCATTY_MOSH_SERVER_PATH"); value != "" {
		for i := range cases {
			cases[i].serverPath = value
		}
	}
	if value := os.Getenv("NATCATTY_MOSH_UDP_PORT"); value != "" {
		for i := range cases {
			cases[i].udpPort = value
		}
	}
	if want := os.Getenv("NATCATTY_MOSH_SERVER_VERSION"); want != "" {
		var selected []labCase
		for _, testCase := range cases {
			if testCase.version == want {
				selected = append(selected, testCase)
			}
		}
		if len(selected) == 0 {
			t.Fatalf("NATCATTY_MOSH_SERVER_VERSION = %q does not name a stock server", want)
		}
		return selected
	}
	return cases
}

// TestInProcessAdapterLabServerVersion proves the cell really runs the pinned
// stock mosh-server version the matrix claims. The gate sets
// NATCATTY_MOSH_EXPECT_VERSION and the cell's wrapper path, then this test
// asks the server for its version over the same SSH bootstrap the Session
// uses. A mismatch fails the cell; no cell is accepted on trust.
func TestInProcessAdapterLabServerVersion(t *testing.T) {
	if os.Getenv("NATCATTY_MOSH_INTEGRATION") == "" {
		t.Skip("set NATCATTY_MOSH_INTEGRATION=1 to run against stock mosh-server")
	}
	expected := os.Getenv("NATCATTY_MOSH_EXPECT_VERSION")
	if expected == "" {
		if version := os.Getenv("NATCATTY_MOSH_SERVER_VERSION"); version != "" {
			expected = version
		}
	}
	if expected == "" {
		t.Fatal("NATCATTY_MOSH_EXPECT_VERSION or NATCATTY_MOSH_SERVER_VERSION must name the pinned version")
	}
	host := envOr("NATCATTY_MOSH_SSH_HOST", "127.0.0.1")
	user := envOr("NATCATTY_MOSH_SSH_USER", "tester")
	password := envOr("NATCATTY_MOSH_SSH_PASSWORD", "testpass")
	cases := labCases(t)
	if len(cases) != 1 {
		t.Fatalf("version cell selected %d servers, want 1", len(cases))
	}
	testCase := cases[0]
	if testCase.version != expected {
		t.Fatalf("cell runs mosh-server %s, expected %s", testCase.version, expected)
	}
	out, err := sshRunOutput(host, testCase.sshPort, user, password,
		moshmodel.ShellQuote(testCase.serverPath)+" --version")
	if err != nil {
		t.Fatalf("read mosh-server version through %s: %v\n%s", testCase.serverPath, err, out)
	}
	if !strings.Contains(out, "mosh "+expected) {
		t.Fatalf("mosh-server version output %q does not name mosh %s", out, expected)
	}
}

// TestInProcessAdapterLabLocalePropagation proves the client's locale
// environment reaches the remote shell through mosh-server, not only the
// generated bootstrap command. The marker is asserted on the command output,
// not the echoed input, by waiting for two occurrences (echo plus printf).
func TestInProcessAdapterLabLocalePropagation(t *testing.T) {
	if os.Getenv("NATCATTY_MOSH_INTEGRATION") == "" {
		t.Skip("set NATCATTY_MOSH_INTEGRATION=1 to run against stock mosh-server")
	}
	host := envOr("NATCATTY_MOSH_SSH_HOST", "127.0.0.1")
	user := envOr("NATCATTY_MOSH_SSH_USER", "tester")
	password := envOr("NATCATTY_MOSH_SSH_PASSWORD", "testpass")
	cases := labCases(t)
	if len(cases) != 1 {
		t.Fatalf("locale cell selected %d servers, want 1", len(cases))
	}
	testCase := cases[0]
	const locale = "de_DE.UTF-8"
	killLabServer(t, host, testCase)

	spec := labStartSpec(host, testCase, locale)
	factory := InProcessFactory{
		Bootstrap:        sshBootstrap{host: host, port: testCase.sshPort, user: user, password: password},
		ProbeTimeout:     800 * time.Millisecond,
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
	if _, err := transport.Write([]byte(`printf 'NATCATTY_LOCALE:%s\n' "$LANG"` + "\r")); err != nil {
		t.Fatalf("write locale probe: %v", err)
	}
	// The echoed input contains the literal %s, not the expanded locale, so
	// one occurrence is the remote shell's own output.
	output := readUntilCount(t, transport, "NATCATTY_LOCALE:"+locale, 1, 15*time.Second)
	if strings.Contains(output, "NATCATTY_LOCALE:C.UTF-8") {
		t.Fatalf("locale did not propagate; output = %q", output)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
}

// TestInProcessAdapterLabResizeAndShutdown is the matrix-cell resize/shutdown
// behavior: a scripted Session-size change must reach mosh-server's PTY and
// every state it draws must announce the same size, then Close must stop the
// server. The full lifecycle test above covers the same path for the legacy
// two-container setup; this one is cell-scoped so each matrix entry proves it.
func TestInProcessAdapterLabResizeAndShutdown(t *testing.T) {
	if os.Getenv("NATCATTY_MOSH_INTEGRATION") == "" {
		t.Skip("set NATCATTY_MOSH_INTEGRATION=1 to run against stock mosh-server")
	}
	host := envOr("NATCATTY_MOSH_SSH_HOST", "127.0.0.1")
	user := envOr("NATCATTY_MOSH_SSH_USER", "tester")
	password := envOr("NATCATTY_MOSH_SSH_PASSWORD", "testpass")
	cases := labCases(t)
	if len(cases) != 1 {
		t.Fatalf("resize cell selected %d servers, want 1", len(cases))
	}
	testCase := cases[0]
	runRealMoshCase(t, host, testCase.sshPort, user, password, testCase.serverPath, testCase.udpPort, testCase.marker)
}

// labStartSpec builds the Session start spec for a matrix cell.
func labStartSpec(host string, testCase labCase, locale string) appsession.StartSpec {
	return appsession.StartSpec{
		Kind:     appsession.KindMosh,
		Hostname: host,
		Port:     testCase.sshPort,
		Username: envOr("NATCATTY_MOSH_SSH_USER", "tester"),
		Cols:     80,
		Rows:     24,
		Env:      []string{"LANG=" + locale, "LC_ALL=" + locale},
		Mosh: &moshmodel.Config{
			Hostname:   host,
			Port:       testCase.sshPort,
			Username:   envOr("NATCATTY_MOSH_SSH_USER", "tester"),
			ServerPath: testCase.serverPath,
			Lang:       locale,
			Locales:    map[string]string{"LANG": locale, "LC_ALL": locale},
			Cols:       80,
			Rows:       24,
		},
	}
}

// killLabServer clears a stale mosh-server from the fixed UDP port the cell's
// wrapper selects. The pattern matches the wrapper's forced -p argument.
func killLabServer(t *testing.T, host string, testCase labCase) {
	t.Helper()
	user := envOr("NATCATTY_MOSH_SSH_USER", "tester")
	password := envOr("NATCATTY_MOSH_SSH_PASSWORD", "testpass")
	pattern := fmt.Sprintf("[m]osh-server.*-p %s", testCase.udpPort)
	sshRun(t, host, testCase.sshPort, user, password, fmt.Sprintf(
		`for p in $(pgrep -f '%s' 2>/dev/null); do kill "$p" 2>/dev/null; done; true`, pattern))
	t.Cleanup(func() {
		sshRunQuiet(host, testCase.sshPort, user, password, fmt.Sprintf(
			`for p in $(pgrep -f '%s' 2>/dev/null); do kill "$p" 2>/dev/null; done; true`, pattern))
	})
}

// setScreenSizeSink plays the renderer's role for a directly-read transport:
// it applies announced sizes so rendering is not blocked waiting for a view.
func setScreenSizeSink(t *testing.T, transport appsession.Transport) {
	t.Helper()
	sizer, ok := transport.(appsession.ScreenSizer)
	if !ok {
		t.Fatal("in-process transport does not expose the remote screen size")
	}
	sizer.SetScreenSizeHandler(func(int, int, uint64) {})
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
	// The in-process transport draws through stock's server-authoritative model:
	// when the peer announces a new screen size, rendering waits for the
	// consumer to apply that size. A Session installs this handler itself; this
	// test reads the transport directly, so it must play the renderer's role.
	sizer, ok := transport.(appsession.ScreenSizer)
	if !ok {
		t.Fatal("in-process transport does not expose the remote screen size")
	}
	remoteSizes := make(chan [2]int, 4)
	sizer.SetScreenSizeHandler(func(cols, rows int, _ uint64) {
		remoteSizes <- [2]int{cols, rows}
	})
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
	select {
	case size := <-remoteSizes:
		if size != [2]int{100, 31} {
			t.Fatalf("remote screen size = %v, want [100 31]", size)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the server-authoritative resize never reached the screen owner")
	}

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

// TestInProcessAdapterRealMoshRoaming drives stock mosh-server 1.4.0 through a
// local UDP relay, changes the relay's upstream source address mid-session,
// and checks that the same SSP session continues without a second SSH
// bootstrap. The relay also drops the first client datagram, proving the
// probe's RTO retransmission against a real server.
//
// The relay is the client-visible NAT: once it rebinds, the server must learn
// the new source address from the next authenticated datagram. Because the old
// upstream socket is closed, a server that failed to roam would send all
// further output into a dead port and the post-rebind command would never
// complete.
func TestInProcessAdapterRealMoshRoaming(t *testing.T) {
	if os.Getenv("NATCATTY_MOSH_INTEGRATION") == "" {
		t.Skip("set NATCATTY_MOSH_INTEGRATION=1 to run against stock mosh-server")
	}
	host := envOr("NATCATTY_MOSH_SSH_HOST", "127.0.0.1")
	user := envOr("NATCATTY_MOSH_SSH_USER", "tester")
	password := envOr("NATCATTY_MOSH_SSH_PASSWORD", "testpass")
	// The parity gate exports the generic cell endpoint. The _140_ names are
	// the legacy two-container defaults, kept as fallbacks for the standalone
	// integration command.
	sshPort := envInt("NATCATTY_MOSH_SSH_PORT", envInt("NATCATTY_MOSH_140_SSH_PORT", 2225))
	udpPort := envInt("NATCATTY_MOSH_UDP_PORT", envInt("NATCATTY_MOSH_140_UDP_PORT", 60121))
	serverPath := envOr("NATCATTY_MOSH_SERVER_PATH", envOr("NATCATTY_MOSH_140_SERVER_PATH", "/usr/local/bin/mosh-60121"))

	// Roaming needs mosh-server 1.4.0's address-learning behavior. Prove the
	// selected endpoint really is the pinned server before driving it.
	if want := os.Getenv("NATCATTY_MOSH_EXPECT_VERSION"); want != "" {
		out, err := sshRunOutput(host, sshPort, user, password,
			moshmodel.ShellQuote(serverPath)+" --version")
		if err != nil {
			t.Fatalf("read roaming server version through %s: %v\n%s", serverPath, err, out)
		}
		if !strings.Contains(out, "mosh "+want) {
			t.Fatalf("roaming server version %q does not name mosh %s", out, want)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pattern := fmt.Sprintf("[m]osh-server.*-p %d", udpPort)
	sshRun(t, host, sshPort, user, password, fmt.Sprintf(
		`for p in $(pgrep -f '%s' 2>/dev/null); do kill "$p" 2>/dev/null; done; true`, pattern))
	t.Cleanup(func() {
		sshRunQuiet(host, sshPort, user, password, fmt.Sprintf(
			`for p in $(pgrep -f '%s' 2>/dev/null); do kill "$p" 2>/dev/null; done; true`, pattern))
	})

	serverAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(udpPort)))
	if err != nil {
		t.Fatalf("resolve mosh-server UDP endpoint: %v", err)
	}
	proxy := startFaultProxy(t, serverAddr)
	// Lose the first probe datagram; the client must retransmit on its RTO.
	proxy.dropClientThrough.Store(1)

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
	bootstrap := &announcementRewritingBootstrap{
		inner:        sshBootstrap{host: host, port: sshPort, user: user, password: password},
		announcePort: proxy.Port(),
	}
	factory := InProcessFactory{
		Bootstrap:        bootstrap,
		ProbeTimeout:     DefaultProbeTimeout,
		HandshakeTimeout: 20 * time.Second,
	}
	transport, err := factory.Open(ctx, spec)
	if err != nil {
		t.Fatalf("Open through roaming relay: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = transport.Close()
		}
	})

	before := proxy.UpstreamAddr()
	if _, err := transport.Write([]byte("echo NATCATTY_ROAM_BEFORE\r")); err != nil {
		t.Fatalf("write before roaming: %v", err)
	}
	readUntilCount(t, transport, "NATCATTY_ROAM_BEFORE", 2, 15*time.Second)

	proxy.Rebind()
	after := proxy.UpstreamAddr()
	if before == nil || after == nil || before.String() == after.String() {
		t.Fatalf("relay source address did not change: %v -> %v", before, after)
	}
	if _, err := transport.Write([]byte("echo NATCATTY_ROAM_AFTER\r")); err != nil {
		t.Fatalf("write after roaming: %v", err)
	}
	readUntilCount(t, transport, "NATCATTY_ROAM_AFTER", 2, 15*time.Second)

	if calls := bootstrap.Calls(); calls != 1 {
		t.Fatalf("SSH bootstrap calls = %d, want 1; roaming must not re-handshake", calls)
	}
	if proxy.Drops() == 0 {
		t.Fatal("the relay never dropped the initial probe datagram")
	}
	if proxy.Rebinds() != 1 {
		t.Fatalf("relay rebinds = %d, want 1", proxy.Rebinds())
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v, want closed", end)
	}

	// The stock server must still exit on the shutdown sentinel after roaming.
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

// announcementRewritingBootstrap counts SSH bootstrap calls and rewrites the
// announced UDP port to a relay, dropping the MOSH IP line so the relay is the
// only probe candidate.
type announcementRewritingBootstrap struct {
	inner        Bootstrap
	announcePort int
	mu           sync.Mutex
	calls        int
}

func (b *announcementRewritingBootstrap) OpenBootstrap(ctx context.Context, spec appsession.StartSpec,
	cfg moshmodel.Config) (io.ReadWriteCloser, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	stream, err := b.inner.OpenBootstrap(ctx, spec, cfg)
	if err != nil {
		return nil, err
	}
	return &announcementRewriteStream{ReadWriteCloser: stream, port: b.announcePort}, nil
}

func (b *announcementRewritingBootstrap) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

var announcementPortPattern = regexp.MustCompile(`MOSH CONNECT [0-9]+ `)

// announcementRewriteStream buffers the bootstrap banner until the MOSH
// CONNECT line is complete, then drops MOSH IP lines and replaces the
// announced port. Output after the announcement passes through unchanged.
type announcementRewriteStream struct {
	io.ReadWriteCloser
	port int

	mu         sync.Mutex
	pending    []byte
	pendingErr error
	done       bool
}

func (s *announcementRewriteStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done {
		_ = s.capture()
	}
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}
	if s.pendingErr != nil {
		err := s.pendingErr
		s.pendingErr = nil
		return 0, err
	}
	return s.ReadWriteCloser.Read(p)
}

func (s *announcementRewriteStream) capture() error {
	for {
		if idx := bytes.Index(s.pending, []byte("MOSH CONNECT")); idx >= 0 {
			if length := bytes.IndexByte(s.pending[idx:], '\n'); length >= 0 {
				end := idx + length + 1
				head := string(s.pending[:end])
				tail := append([]byte(nil), s.pending[end:]...)
				lines := make([]string, 0, 4)
				for _, line := range strings.Split(head, "\n") {
					if strings.Contains(line, "MOSH IP") {
						continue
					}
					if strings.Contains(line, "MOSH CONNECT") {
						line = announcementPortPattern.ReplaceAllString(line,
							fmt.Sprintf("MOSH CONNECT %d ", s.port))
					}
					lines = append(lines, line)
				}
				s.pending = append([]byte(strings.Join(lines, "\n")), tail...)
				s.done = true
				return nil
			}
		}
		chunk := make([]byte, 4096)
		n, err := s.ReadWriteCloser.Read(chunk)
		if n > 0 {
			s.pending = append(s.pending, chunk[:n]...)
		}
		if err != nil {
			s.done = true
			s.pendingErr = err
			return err
		}
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
//
// mosh sends a framebuffer diff, so the screen can show a line contiguously
// while the byte stream carries cursor positioning inside it. mosh 1.4.0 does
// exactly that to an echoed command after a roaming event. Count against the
// de-escaped stream so the assertion follows what the terminal displays.
func readUntilCount(t *testing.T, transport appsession.Transport, marker string, count int, timeout time.Duration) string {
	t.Helper()
	result := make(chan string, 1)
	var mu sync.Mutex
	var collected []byte
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := transport.Read(buffer)
			if n > 0 {
				mu.Lock()
				collected = append(collected, buffer[:n]...)
				text := string(collected)
				mu.Unlock()
				if strings.Count(stripTerminalEscapes(text), marker) >= count {
					result <- text
					return
				}
			}
			if err != nil {
				mu.Lock()
				text := string(collected)
				mu.Unlock()
				result <- text
				return
			}
		}
	}()
	select {
	case text := <-result:
		if got := strings.Count(stripTerminalEscapes(text), marker); got < count {
			t.Fatalf("output %q contains %q %d times, want %d", text, marker, got, count)
		}
		return text
	case <-time.After(timeout):
		mu.Lock()
		defer mu.Unlock()
		text := string(collected)
		t.Fatalf("timed out waiting for %q to appear %d times; output = %q (de-escaped %q)",
			marker, count, text, stripTerminalEscapes(text))
		return ""
	}
}

// stripTerminalEscapes removes escape sequences from a terminal byte stream.
// It handles CSI, OSC/DCS/SOS/PM/APC strings, and two-byte escapes; the
// terminal's visible text is what remains.
func stripTerminalEscapes(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != 0x1b {
			out.WriteByte(text[i])
			continue
		}
		if i+1 >= len(text) {
			break
		}
		switch text[i+1] {
		case '[':
			// CSI: parameter and intermediate bytes, then one final byte.
			j := i + 2
			for j < len(text) && (text[j] < 0x40 || text[j] > 0x7e) {
				j++
			}
			if j < len(text) {
				j++
			}
			i = j - 1
		case ']', 'P', 'X', '^', '_':
			// Strings terminated by BEL or ST (ESC \).
			j := i + 2
			for ; j < len(text); j++ {
				if text[j] == 0x07 {
					j++
					break
				}
				if text[j] == 0x1b && j+1 < len(text) && text[j+1] == '\\' {
					j += 2
					break
				}
			}
			i = j - 1
		default:
			// Two-byte escape.
			i++
		}
	}
	return out.String()
}

// The echoed command and its output are both on screen even though mosh wraps
// part of the echo in cursor positioning.
func TestStripTerminalEscapesJoinsSplitText(t *testing.T) {
	raw := "echo NA\x1b[?25l\x1b[10;80HTCATTY_ROAM_AFTER\r\nNATCATTY_ROAM_AFTER\r\n"
	text := stripTerminalEscapes(raw)
	if got := strings.Count(text, "NATCATTY_ROAM_AFTER"); got != 2 {
		t.Fatalf("de-escaped %q contains the marker %d times, want 2", text, got)
	}
	// An unterminated CSI at the end must not resurrect bytes or hang.
	if got := stripTerminalEscapes("before\x1b[10;80"); got != "before" {
		t.Fatalf("unterminated CSI de-escaped to %q", got)
	}
	if got := stripTerminalEscapes("\x1b]0;title\x07text"); got != "text" {
		t.Fatalf("OSC de-escaped to %q", got)
	}
	if got := stripTerminalEscapes("\x1b]0;title\x1b\\text"); got != "text" {
		t.Fatalf("ST-terminated OSC de-escaped to %q", got)
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
