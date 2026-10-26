package mosh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/bundle"
	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	"github.com/mslxl/natcatty/platform/pty"
)

const testKey = "abcdefghijklmnopqrstuv"

// fixtureBootstrap serves the SSH bootstrap from memory and records what the
// factory asked for.
type fixtureBootstrap struct {
	output string
	hold   bool
	err    error

	mu      sync.Mutex
	opened  int
	command string
	stream  *fixtureStream
}

func (b *fixtureBootstrap) OpenBootstrap(ctx context.Context, spec appsession.StartSpec, cfg moshmodel.Config) (io.ReadWriteCloser, error) {
	b.mu.Lock()
	b.opened++
	b.command = cfg.RemoteCommand()
	b.mu.Unlock()
	if b.err != nil {
		return nil, b.err
	}
	reader, writer := io.Pipe()
	stream := &fixtureStream{reader: reader, writer: writer}
	b.mu.Lock()
	b.stream = stream
	b.mu.Unlock()
	go func() {
		if b.output != "" {
			_, _ = writer.Write([]byte(b.output))
		}
		if !b.hold {
			_ = writer.Close()
		}
	}()
	return stream, nil
}

func (b *fixtureBootstrap) closed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stream != nil && b.stream.isClosed()
}

func (b *fixtureBootstrap) openCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.opened
}

func (b *fixtureBootstrap) commandText() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.command
}

type fixtureStream struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func (s *fixtureStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *fixtureStream) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

func (s *fixtureStream) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		_ = s.reader.Close()
		_ = s.writer.Close()
	})
	return nil
}

func (s *fixtureStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// fakeClient models the bundled mosh-client PTY.
type fakeClient struct {
	path string
	args []string
	env  []string
	cols int
	rows int

	mu      sync.Mutex
	writes  bytes.Buffer
	resizes [][2]int
	output  chan []byte
	exited  chan struct{}
	closed  chan struct{}
	code    int
	once    sync.Once
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		output: make(chan []byte, 16),
		exited: make(chan struct{}),
		closed: make(chan struct{}),
	}
}

func (c *fakeClient) Read(p []byte) (int, error) {
	select {
	case chunk, ok := <-c.output:
		if !ok {
			return 0, io.EOF
		}
		return copy(p, chunk), nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *fakeClient) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closed:
		return 0, io.ErrClosedPipe
	default:
	}
	return c.writes.Write(p)
}

func (c *fakeClient) Resize(cols, rows int) error {
	c.mu.Lock()
	c.resizes = append(c.resizes, [2]int{cols, rows})
	c.mu.Unlock()
	return nil
}

func (c *fakeClient) Wait() (int, error) {
	<-c.exited
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code, nil
}

func (c *fakeClient) Exited() <-chan struct{} { return c.exited }

func (c *fakeClient) Kill() error { return c.Close() }

func (c *fakeClient) Close() error {
	c.once.Do(func() {
		close(c.closed)
		close(c.exited)
	})
	return nil
}

func (c *fakeClient) emit(text string) { c.output <- []byte(text) }

func (c *fakeClient) exit(code int) {
	c.mu.Lock()
	c.code = code
	c.mu.Unlock()
	c.once.Do(func() {
		close(c.output)
		close(c.exited)
	})
}

func (c *fakeClient) written() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes.String()
}

func (c *fakeClient) size() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.resizes) == 0 {
		return 0, 0
	}
	last := c.resizes[len(c.resizes)-1]
	return last[0], last[1]
}

// harness builds a verified fake bundle and a factory with both seams.
type harness struct {
	root       string
	bootstrap  *fixtureBootstrap
	client     *fakeClient
	spawns     int
	spawnErr   error
	factory    Factory
	binaryPath string
}

func newHarness(t *testing.T, output string) *harness {
	t.Helper()
	root := t.TempDir()
	target, ok := bundle.HostTarget()
	if !ok {
		t.Skip("no bundle target for this platform")
	}
	content := []byte("fake mosh-client")
	dir := filepath.Join(root, "mosh", target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mosh-client")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	spec := bundle.Spec{
		Kind:  bundle.Mosh,
		Roots: []string{root},
		Targets: map[string]bundle.Entry{
			target: {Binary: "mosh-client", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content))},
		},
	}
	bootstrap := &fixtureBootstrap{output: output}
	client := newFakeClient()
	h := &harness{root: root, bootstrap: bootstrap, client: client, binaryPath: path}
	h.factory = Factory{
		Spec:      spec,
		Bootstrap: bootstrap,
		Spawn: func(options pty.Options) (Process, error) {
			h.spawns++
			if h.spawnErr != nil {
				return nil, h.spawnErr
			}
			client.path = options.Path
			client.args = append([]string(nil), options.Args...)
			client.env = append([]string(nil), options.Env...)
			client.cols, client.rows = options.Cols, options.Rows
			return client, nil
		},
		BaseEnv:          func() []string { return []string{"PATH=/usr/bin", "LANG=en_US.UTF-8", "TERM=screen"} },
		HomeDir:          func() string { return "" },
		HandshakeTimeout: 2 * time.Second,
	}
	return h
}

func testSpec() appsession.StartSpec {
	return appsession.StartSpec{
		Kind:     appsession.KindMosh,
		Label:    "mosh test",
		Hostname: "example.test",
		Port:     2222,
		Username: "alice",
		Cols:     100,
		Rows:     40,
		Mosh: &moshmodel.Config{
			Hostname: "example.test",
			Port:     2222,
			Username: "alice",
		},
	}
}

// collector reads a transport continuously so consecutive assertions never
// compete for the same stream.
type collector struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	err     error
	updated chan struct{}
}

func newCollector(reader io.Reader) *collector {
	c := &collector{updated: make(chan struct{}, 1)}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := reader.Read(buffer)
			if n > 0 {
				c.mu.Lock()
				c.buffer.Write(buffer[:n])
				c.mu.Unlock()
				c.signal()
			}
			if err != nil {
				c.mu.Lock()
				c.err = err
				c.mu.Unlock()
				c.signal()
				return
			}
		}
	}()
	return c
}

func (c *collector) signal() {
	select {
	case c.updated <- struct{}{}:
	default:
	}
}

func (c *collector) text() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buffer.String(), c.err
}

func (c *collector) waitFor(t *testing.T, want string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		text, err := c.text()
		if strings.Contains(text, want) {
			return text
		}
		if err != nil {
			t.Fatalf("terminal stream ended before %q: %v; output = %q", want, err, text)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for %q; output = %q", want, text)
		}
		select {
		case <-c.updated:
		case <-time.After(remaining):
		}
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// assertClientArgv checks the pty contract: Path is the resolved bundle and
// Args[0] is that same program, followed by the business arguments.
func (h *harness) assertClientArgv(t *testing.T, want ...string) {
	t.Helper()
	expected := append([]string{h.binaryPath}, want...)
	if h.client.path != h.binaryPath {
		t.Fatalf("client Path = %q, want %q", h.client.path, h.binaryPath)
	}
	if len(h.client.args) != len(expected) {
		t.Fatalf("client Args = %#v, want %#v", h.client.args, expected)
	}
	for index := range expected {
		if h.client.args[index] != expected[index] {
			t.Fatalf("client Args = %#v, want %#v", h.client.args, expected)
		}
	}
}

// TestClientArgvStartsWithBundledExecutable is the regression for ticket 43's
// real-binary failure: mosh-client read the host as its own name and printed
// Usage instead of connecting.
func TestClientArgvStartsWithBundledExecutable(t *testing.T) {
	h := newHarness(t, "MOSH CONNECT 60006 "+testKey+"\n")
	transport, err := h.factory.Open(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer transport.Close()
	h.assertClientArgv(t, "example.test", "60006")
	if key := envValue(h.client.env, "MOSH_KEY"); key != testKey {
		t.Fatalf("MOSH_KEY = %q", key)
	}
}

func TestHandshakeSwapsToBundledClient(t *testing.T) {
	h := newHarness(t, "Last login: today\r\n"+
		"MOSH IP 203.0.113.9\r\n"+
		"MOSH CONNECT 60001 "+testKey+"==\r\n")
	transport, err := h.factory.Open(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer transport.Close()

	if h.bootstrap.openCount() != 1 {
		t.Fatalf("bootstrap opens = %d", h.bootstrap.openCount())
	}
	if !strings.HasPrefix(h.bootstrap.commandText(), "sh -c '") {
		t.Fatalf("bootstrap command = %q", h.bootstrap.commandText())
	}
	if !strings.Contains(h.bootstrap.commandText(), "exec mosh-server new -s -c 256") {
		t.Fatalf("bootstrap command = %q", h.bootstrap.commandText())
	}
	if !h.bootstrap.closed() {
		t.Fatal("the bootstrap channel stayed open after the handshake")
	}
	if h.spawns != 1 {
		t.Fatalf("spawns = %d", h.spawns)
	}
	h.assertClientArgv(t, "203.0.113.9", "60001")
	clientEnv := strings.Join(h.client.env, "\n")
	for _, want := range []string{
		"MOSH_KEY=" + testKey + "==",
		"TERM=xterm-256color",
		"MOSH_NO_TERM_INIT=1",
		"MOSH_FALLBACK_HOST=example.test",
		"LANG=en_US.UTF-8",
	} {
		if !strings.Contains(clientEnv, want+"\n") && !strings.HasSuffix(clientEnv, want) {
			t.Fatalf("client env missing %q:\n%s", want, clientEnv)
		}
	}
	if h.client.cols != 100 || h.client.rows != 40 {
		t.Fatalf("client size = %dx%d", h.client.cols, h.client.rows)
	}

	reader := newCollector(transport)
	text := reader.waitFor(t, "Last login: today")
	if strings.Contains(text, "MOSH CONNECT") || strings.Contains(text, testKey) {
		t.Fatalf("protocol leaked into the terminal: %q", text)
	}
	if !strings.Contains(text, viewportReset) {
		t.Fatalf("viewport reset missing: %q", text)
	}

	h.client.emit("mosh frame\r\n")
	text = reader.waitFor(t, "mosh frame")
	if !strings.Contains(text, viewportReset) {
		t.Fatalf("client output lost the viewport reset: %q", text)
	}

	if _, err := transport.Write([]byte("ls\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := h.client.written(); got != "ls\r" {
		t.Fatalf("client input = %q", got)
	}
	if err := transport.Resize(132, 50); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if cols, rows := h.client.size(); cols != 132 || rows != 50 {
		t.Fatalf("resize = %dx%d", cols, rows)
	}

	h.client.exit(0)
	waiter, ok := transport.(appsession.ExitWaiter)
	if !ok {
		t.Fatal("transport does not report an exit code")
	}
	code, _ := waiter.Wait()
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonExited || !end.HasExitCode || end.ExitCode != 0 {
		t.Fatalf("end = %+v", end)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestHandshakeParsesUnterminatedAnnouncement(t *testing.T) {
	h := newHarness(t, "banner\r\nMOSH CONNECT 60002 "+testKey)
	transport, err := h.factory.Open(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer transport.Close()
	h.assertClientArgv(t, "example.test", "60002")
	if strings.Contains(strings.Join(h.client.env, "\n"), "MOSH_FALLBACK_HOST") {
		t.Fatal("fallback host set without a MOSH IP")
	}
}

func TestHandshakeFailureIsTypedAndReaped(t *testing.T) {
	h := newHarness(t, "welcome\r\nMOSH CONNECT 60003 short\r\n")
	transport, err := h.factory.Open(context.Background(), testSpec())
	if transport != nil {
		transport.Close()
	}
	if !errors.Is(err, moshmodel.ErrHandshake) {
		t.Fatalf("err = %v, want ErrHandshake", err)
	}
	if h.spawns != 0 {
		t.Fatalf("spawned the client without a handshake: %d", h.spawns)
	}
	if !h.bootstrap.closed() {
		t.Fatal("bootstrap stayed open after a failed handshake")
	}
}

func TestHandshakeTimeout(t *testing.T) {
	h := newHarness(t, "")
	h.bootstrap.hold = true
	h.factory.HandshakeTimeout = 150 * time.Millisecond
	start := time.Now()
	transport, err := h.factory.Open(context.Background(), testSpec())
	if transport != nil {
		transport.Close()
	}
	if !errors.Is(err, moshmodel.ErrHandshake) {
		t.Fatalf("err = %v, want ErrHandshake", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if !h.bootstrap.closed() {
		t.Fatal("bootstrap stayed open after the timeout")
	}
	if h.spawns != 0 {
		t.Fatal("spawned after a timeout")
	}
}

func TestOpenCancellation(t *testing.T) {
	h := newHarness(t, "")
	h.bootstrap.hold = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		transport appsession.Transport
		err       error
	}
	done := make(chan result, 1)
	go func() {
		transport, err := h.factory.Open(ctx, testSpec())
		done <- result{transport, err}
	}()
	waitFor(t, "the bootstrap to open", func() bool { return h.bootstrap.openCount() == 1 })
	cancel()
	select {
	case got := <-done:
		if got.transport != nil {
			got.transport.Close()
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Open did not return after cancellation")
	}
	if !h.bootstrap.closed() {
		t.Fatal("bootstrap stayed open after cancellation")
	}
}

func TestMissingBundleIsTypedAndNeverFallsBackToPath(t *testing.T) {
	h := newHarness(t, "")
	// Prove a system binary with the same name is visible through PATH.
	pathDir := t.TempDir()
	systemName := "mosh-client"
	if runtime.GOOS == "windows" {
		systemName += ".exe"
	}
	systemBinary := filepath.Join(pathDir, systemName)
	if err := os.WriteFile(systemBinary, []byte("system mosh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := exec.LookPath(systemName); err != nil {
		t.Fatalf("fixture PATH binary not visible: %v", err)
	}

	h.factory.Spec.Roots = []string{t.TempDir()}
	transport, err := h.factory.Open(context.Background(), testSpec())
	if transport != nil {
		transport.Close()
	}
	if !errors.Is(err, moshmodel.ErrBundleUnavailable) {
		t.Fatalf("err = %v, want ErrBundleUnavailable", err)
	}
	if h.spawns != 0 {
		t.Fatalf("spawned %d clients with no bundle", h.spawns)
	}
	if h.bootstrap.openCount() != 0 {
		t.Fatal("bootstrapped even though the client bundle was missing")
	}
	if data, readErr := os.ReadFile(systemBinary); readErr != nil || string(data) != "system mosh" {
		t.Fatalf("the PATH binary was executed or changed: %q %v", data, readErr)
	}
}

func TestCorruptBundleIsTyped(t *testing.T) {
	h := newHarness(t, "")
	if err := os.WriteFile(h.binaryPath, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := h.factory.Open(context.Background(), testSpec())
	if !errors.Is(err, moshmodel.ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if h.spawns != 0 || h.bootstrap.openCount() != 0 {
		t.Fatal("opened a Session with a corrupt bundle")
	}
}

func TestUnsupportedPlatformIsTyped(t *testing.T) {
	h := newHarness(t, "")
	h.factory.Spec.Targets = map[string]bundle.Entry{}
	_, err := h.factory.Open(context.Background(), testSpec())
	if !errors.Is(err, moshmodel.ErrPlatformUnsupported) {
		t.Fatalf("err = %v, want ErrPlatformUnsupported", err)
	}
}

func TestBootstrapAndSpawnFailures(t *testing.T) {
	h := newHarness(t, "")
	h.bootstrap.err = errors.New("no route")
	if _, err := h.factory.Open(context.Background(), testSpec()); err == nil ||
		!strings.Contains(err.Error(), "no route") {
		t.Fatalf("bootstrap error = %v", err)
	}

	h = newHarness(t, "MOSH CONNECT 60004 "+testKey+"\n")
	h.spawnErr = errors.New("exec denied")
	_, err := h.factory.Open(context.Background(), testSpec())
	if err == nil || !strings.Contains(err.Error(), "exec denied") {
		t.Fatalf("spawn error = %v", err)
	}
}

// bundledMoshSpec returns the committed Windows x64 bundle for a real-binary
// test, or skips when the artifact is not present in this checkout.
func bundledMoshSpec(t *testing.T) (bundle.Spec, string) {
	t.Helper()
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skipf("the committed mosh-client is a Windows x64 binary; this host is %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "resources"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, string(bundle.Mosh), "win32-x64", "mosh-client.exe")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("the bundled mosh-client is absent: %v", err)
	}
	return bundle.MoshSpec([]string{root}), path
}

// TestBundledMoshClientUsesContractArgv runs the committed mosh-client with
// exactly the pty.Options the adapter builds. Before the ticket-43 rework its
// argv was [host, port], so the Windows command line was
// "mosh-client.exe <port>" and the client printed Usage and sent no UDP.
func TestBundledMoshClientUsesContractArgv(t *testing.T) {
	spec, exe := bundledMoshSpec(t)
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	port := server.LocalAddr().(*net.UDPAddr).Port

	type datagram struct {
		size int
		from net.Addr
	}
	received := make(chan datagram, 1)
	go func() {
		buffer := make([]byte, 2048)
		n, from, readErr := server.ReadFrom(buffer)
		if readErr != nil {
			return
		}
		received <- datagram{size: n, from: from}
	}()

	// A real mosh-client base64-decodes MOSH_KEY: sixteen bytes, unpadded.
	const realKey = "MDEyMzQ1Njc4OWFiY2RlZg"
	bootstrap := &fixtureBootstrap{
		output: "welcome\r\nMOSH CONNECT " + strconv.Itoa(port) + " " + realKey + "\r\n",
		hold:   true,
	}
	var spawned pty.Options
	factory := Factory{
		Spec:      spec,
		Bootstrap: bootstrap,
		Spawn: func(options pty.Options) (Process, error) {
			spawned = options
			terminal, startErr := pty.Start(options)
			if startErr != nil {
				return nil, startErr
			}
			return terminal, nil
		},
		BaseEnv:          os.Environ,
		HomeDir:          func() string { return t.TempDir() },
		HandshakeTimeout: 15 * time.Second,
	}
	cfg := moshmodel.Config{Hostname: "127.0.0.1", Port: 22, Username: "alice"}.Normalized()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	transport, err := factory.Open(ctx, appsession.StartSpec{
		Kind: appsession.KindMosh, Hostname: cfg.Hostname, Port: cfg.Port,
		Username: cfg.Username, Mosh: &cfg, Cols: 100, Rows: 40,
	})
	if err != nil {
		t.Fatalf("Open the bundled client: %v", err)
	}
	defer transport.Close()

	if spawned.Path != exe || len(spawned.Args) != 3 ||
		spawned.Args[0] != exe || spawned.Args[1] != "127.0.0.1" ||
		spawned.Args[2] != strconv.Itoa(port) {
		t.Fatalf("adapter pty argv = Path %q Args %#v, want [%s 127.0.0.1 %d]",
			spawned.Path, spawned.Args, exe, port)
	}
	if key := envValue(spawned.Env, "MOSH_KEY"); key != realKey {
		t.Fatalf("adapter MOSH_KEY = %q", key)
	}

	reader := newCollector(transport)
	exited := make(chan struct{})
	go func() {
		if waiter, ok := transport.(appsession.ExitWaiter); ok {
			_, _ = waiter.Wait()
			close(exited)
		}
	}()
	select {
	case datagram := <-received:
		if datagram.size == 0 {
			t.Fatalf("the bundled client sent an empty datagram from %v", datagram.from)
		}
		// The UDP listener is loopback-only, so the datagram proves the
		// advertised host and port reached the client as argv[1] and argv[2].
		if host, _, splitErr := net.SplitHostPort(datagram.from.String()); splitErr != nil || host != "127.0.0.1" {
			t.Fatalf("datagram source = %v", datagram.from)
		}
		t.Logf("real mosh-client sent %d UDP bytes from %v to 127.0.0.1:%d", datagram.size, datagram.from, port)
	case <-exited:
		text, _ := reader.text()
		t.Fatalf("the bundled client exited without sending UDP; output = %q", text)
	case <-time.After(20 * time.Second):
		text, _ := reader.text()
		t.Fatalf("the bundled client sent no UDP datagram within 20s; output = %q", text)
	}
	_ = transport.Close()
	text, readErr := waitForCollectorEnd(t, reader)
	if readErr == nil {
		t.Fatal("the terminal stream did not end after Close")
	}
	if strings.Contains(text, "Usage:") {
		t.Fatalf("the bundled client printed Usage instead of connecting: %q", text)
	}
}

// waitForCollectorEnd waits for the transport reader to report EOF after the
// process was closed, so the full output can be inspected.
func waitForCollectorEnd(t *testing.T, reader *collector) (string, error) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		text, err := reader.text()
		if err != nil {
			return text, err
		}
		select {
		case <-reader.updated:
		case <-deadline:
			return text, nil
		}
	}
}

func TestCheckRoute(t *testing.T) {
	if err := CheckRoute(0, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckRoute(1, false); !errors.Is(err, moshmodel.ErrJumpUnsupported) {
		t.Fatalf("jump error = %v", err)
	}
	if err := CheckRoute(0, true); !errors.Is(err, moshmodel.ErrProxyUnsupported) {
		t.Fatalf("proxy error = %v", err)
	}
}

func TestLocaleFromEnvironment(t *testing.T) {
	h := newHarness(t, "MOSH CONNECT 60005 "+testKey+"\n")
	h.factory.BaseEnv = func() []string {
		return []string{"LC_ALL=de_DE.UTF-8", "LANG=de_DE.UTF-8"}
	}
	transport, err := h.factory.Open(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer transport.Close()
	command := h.bootstrap.commandText()
	if !strings.Contains(command, "LC_ALL=de_DE.UTF-8") || strings.Contains(command, "LANG=en_US.UTF-8") {
		t.Fatalf("bootstrap command = %q", command)
	}
	if !strings.Contains(strings.Join(h.client.env, "\n"), "LANG=de_DE.UTF-8") {
		t.Fatalf("client env = %#v", h.client.env)
	}
}

// The UDP fixture proves the announced port and key reach a real UDP endpoint
// and the client's reply flows back through the transport, standing in for
// mosh-client's network loop.
func TestUDPFixtureRoundTrip(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("udp fixture: %v", err)
	}
	defer server.Close()
	port := server.LocalAddr().(*net.UDPAddr).Port
	received := make(chan string, 1)
	go func() {
		buffer := make([]byte, 256)
		n, peer, readErr := server.ReadFrom(buffer)
		if readErr != nil {
			received <- ""
			return
		}
		key := string(buffer[:n])
		received <- key
		_, _ = server.WriteTo([]byte("udp:"+key), peer)
	}()

	h := newHarness(t, "MOSH CONNECT "+strconv.Itoa(port)+" "+testKey+"\n")
	h.factory.Spawn = func(options pty.Options) (Process, error) {
		h.spawns++
		client := newFakeClient()
		go func() {
			key := envValue(options.Env, "MOSH_KEY")
			connection, dialErr := net.Dial("udp", server.LocalAddr().String())
			if dialErr != nil {
				client.exit(1)
				return
			}
			defer connection.Close()
			if _, writeErr := connection.Write([]byte(key)); writeErr != nil {
				client.exit(1)
				return
			}
			response := make([]byte, 256)
			n, readErr := connection.Read(response)
			if readErr != nil {
				client.exit(1)
				return
			}
			client.emit(string(response[:n]))
		}()
		return client, nil
	}
	transport, err := h.factory.Open(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer transport.Close()
	reader := newCollector(transport)
	text := reader.waitFor(t, "udp:"+testKey)
	if !strings.Contains(text, testKey) {
		t.Fatalf("output = %q", text)
	}
	select {
	case key := <-received:
		if key != testKey {
			t.Fatalf("UDP fixture received %q", key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the UDP fixture received no datagram")
	}
}
