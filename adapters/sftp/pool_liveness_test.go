package sftp

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pkg/sftp"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
)

// The application layer must not import the SFTP protocol library, so the
// end-to-end proof that a killed SFTP server forces a fresh dial lives here:
// the real Connection Manager and pool, the real pkg/sftp server and client,
// and the real adapter.

type livenessPasswords struct{}

func (livenessPasswords) Password(context.Context, model.HostID) (string, error) {
	return "", nil
}

type livenessKnownHosts struct{}

func (livenessKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return nil, nil
}

func (livenessKnownHosts) UpsertKnownHost(context.Context, domainconn.KnownHost) error {
	return nil
}

// livenessServer models sshd and sftp-server. Every dial through the Manager
// gets its own transport and its own SFTP session; kill resets all of them and
// makes dials fail until restart, exactly like killing the server processes
// while the app still holds a pooled transport.
type livenessServer struct {
	root string

	mu      sync.Mutex
	lost    bool
	dials   int
	clients []*livenessClient
}

func (s *livenessServer) Dial(context.Context, appconnection.DialRequest) (appconnection.Client, error) {
	s.mu.Lock()
	s.dials++
	lost := s.lost
	s.mu.Unlock()
	if lost {
		return nil, errors.New("ssh: connect: connection refused")
	}
	client := &livenessClient{server: s}
	s.mu.Lock()
	s.clients = append(s.clients, client)
	s.mu.Unlock()
	return client, nil
}

func (s *livenessServer) kill() {
	s.mu.Lock()
	s.lost = true
	clients := append([]*livenessClient(nil), s.clients...)
	s.mu.Unlock()
	for _, client := range clients {
		client.kill()
	}
}

func (s *livenessServer) restart() {
	s.mu.Lock()
	s.lost = false
	s.mu.Unlock()
}

func (s *livenessServer) dialCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dials
}

func (s *livenessServer) client(index int) *livenessClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clients[index]
}

type livenessClient struct {
	server *livenessServer

	mu      sync.Mutex
	closed  bool
	dead    bool
	streams []net.Conn
}

var (
	_ appconnection.Client          = (*livenessClient)(nil)
	_ appconnection.KeepaliveClient = (*livenessClient)(nil)
	_ appconnection.VersionedClient = (*livenessClient)(nil)
	_ appconnection.LivenessClient  = (*livenessClient)(nil)
	_ appconnection.SubsystemClient = (*livenessClient)(nil)
)

func (c *livenessClient) OpenTerminal(context.Context, appconnection.TerminalSpec) (appconnection.Terminal, error) {
	return nil, errors.New("the modelled server has no interactive terminal")
}

func (c *livenessClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	streams := c.streams
	c.streams = nil
	c.mu.Unlock()
	closeLivenessStreams(streams)
	return nil
}

func (c *livenessClient) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && !c.dead
}

func (c *livenessClient) Keepalive() error {
	if !c.Alive() {
		return appconnection.ErrClosed
	}
	return nil
}

func (c *livenessClient) RemoteVersion() string { return "SSH-2.0-OpenSSH_9.6" }

func (c *livenessClient) OpenSubsystem(_ context.Context, name string) (appconnection.Subsystem, error) {
	if name != "sftp" {
		return nil, errors.New("the modelled server has no " + name + " subsystem")
	}
	clientConn, serverConn := net.Pipe()
	server, err := sftp.NewServer(serverConn, sftp.WithServerWorkingDirectory(c.server.root))
	if err != nil {
		closeLivenessStreams([]net.Conn{clientConn, serverConn})
		return nil, err
	}
	go func() {
		_ = server.Serve()
		_ = server.Close()
	}()
	c.mu.Lock()
	if c.closed || c.dead {
		c.mu.Unlock()
		closeLivenessStreams([]net.Conn{clientConn, serverConn})
		return nil, errors.New("ssh: connection lost")
	}
	c.streams = append(c.streams, clientConn, serverConn)
	c.mu.Unlock()
	return clientConn, nil
}

// kill resets the physical peer without marking the Connection's own closed
// flag, so only the pool's liveness check can notice.
func (c *livenessClient) kill() {
	c.mu.Lock()
	c.dead = true
	streams := c.streams
	c.streams = nil
	c.mu.Unlock()
	closeLivenessStreams(streams)
}

func (c *livenessClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func closeLivenessStreams(streams []net.Conn) {
	for _, stream := range streams {
		_ = stream.Close()
	}
}

func livenessRequest() appconnection.Request {
	return appconnection.Request{
		HostID:    model.HostID("host-1"),
		Hostname:  "127.0.0.1",
		Port:      22,
		Username:  "user",
		LeaseKind: appconnection.LeaseChannel,
	}
}

// openRemote turns one lease into the adapter's filesystem, mirroring the
// app's sftpRemoteSession: a clean close releases the transport and a failed
// one discards it.
func openRemote(t *testing.T, lease *appconnection.Lease) *Remote {
	t.Helper()
	stream, err := lease.Subsystem(context.Background(), "sftp")
	if err != nil {
		t.Fatalf("Subsystem: %v", err)
	}
	remote, err := NewRemote(stream, func(cause error) error {
		if cause != nil {
			return lease.Discard()
		}
		return lease.Release()
	})
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	return remote
}

func assertListsProbe(t *testing.T, remote *Remote, want string) {
	t.Helper()
	ctx := context.Background()
	root, err := remote.Root(ctx)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	entries, err := remote.List(ctx, root)
	if err != nil {
		t.Fatalf("List %q: %v", root, err)
	}
	for _, entry := range entries {
		if entry.Name == want {
			return
		}
	}
	t.Fatalf("List %q = %+v, want %q", root, entries, want)
}

// TestManagerDialsFreshAfterTheSFTPServerDies is the kill/restart/acquire
// regression: the pane keeps its lease on a pooled transport, sshd and
// sftp-server are killed, the same endpoint comes back, and the next Acquire
// must dial a new connection whose real SFTP session lists the file.
func TestManagerDialsFreshAfterTheSFTPServerDies(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "probe.txt"), []byte("probe"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	server := &livenessServer{root: root}
	manager := appconnection.NewManager(server, livenessPasswords{}, livenessKnownHosts{})
	t.Cleanup(manager.CloseAll)

	held, err := manager.Acquire(context.Background(), livenessRequest())
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	first := openRemote(t, held)
	assertListsProbe(t, first, "probe.txt")
	if got := server.dialCount(); got != 1 {
		t.Fatalf("dial count = %d, want 1", got)
	}

	server.kill()
	server.restart()

	fresh, err := manager.Acquire(context.Background(), livenessRequest())
	if err != nil {
		t.Fatalf("Acquire after the peer died: %v", err)
	}
	if got := server.dialCount(); got != 2 {
		t.Fatalf("dial count = %d, want 2: the pool reused the dead transport", got)
	}
	if !server.client(0).isClosed() {
		t.Fatal("the dead transport was not retired and closed")
	}
	if fresh == held {
		t.Fatal("Acquire returned the dead lease")
	}
	second := openRemote(t, fresh)
	assertListsProbe(t, second, "probe.txt")
}
