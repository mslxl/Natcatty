package app

// Ticket 35: a peer that allows one SSH channel per transport must still serve
// SFTP. The tests run the real Manager, Vault, host-key policy, SFTP adapter,
// and transfer selection against an in-process fakessh peer (a network-device
// style banner) whose SFTP subsystem is a real pkg/sftp server rooted in a
// temp directory. Every physical dial is counted, and each fixture server
// records the subsystems it was asked to open, so "the terminal transport was
// not borrowed" is observable rather than inferred.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	xssh "golang.org/x/crypto/ssh"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	"github.com/mslxl/natcatty/adapters/store"
	appconnection "github.com/mslxl/natcatty/application/connection"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// sftpDialNetwork is a fake TCP dialer over fakessh. Every dial gets its own
// server instance, which is what makes a companion transport distinguishable
// from a borrowed one.
type sftpDialNetwork struct {
	root          string
	serverVersion string
	// hostKey is the stable identity of the "same" server across dials.
	hostKey xssh.Signer
	// changedKey replaces hostKey from changedKeyFrom onward, modelling a
	// server whose key changed or a bastion that presented a different one.
	changedKey     xssh.Signer
	changedKeyFrom int
	// fail makes one dial fail, so lifecycle errors can be injected without a
	// second network implementation.
	fail map[int]error

	mu      sync.Mutex
	dials   int
	servers map[int]*fakessh.Server
	conns   []net.Conn
}

func (n *sftpDialNetwork) dial(context.Context, string, string) (net.Conn, error) {
	n.mu.Lock()
	n.dials++
	index := n.dials
	failure := n.fail[index]
	signer := n.hostKey
	if n.changedKey != nil && n.changedKeyFrom > 0 && index >= n.changedKeyFrom {
		signer = n.changedKey
	}
	n.mu.Unlock()
	if failure != nil {
		return nil, failure
	}

	client, server := fakessh.Pipe()
	root := n.root
	instance, err := fakessh.Start(server, fakessh.Options{
		ServerVersion: n.serverVersion,
		HostKey:       signer,
		Subsystems: map[string]func(xssh.Channel){
			"sftp": func(channel xssh.Channel) {
				session, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(root))
				if err != nil {
					_ = channel.Close()
					return
				}
				_ = session.Serve()
				_ = session.Close()
			},
		},
	})
	if err != nil {
		_ = client.Close()
		_ = server.Close()
		return nil, err
	}
	n.mu.Lock()
	n.servers[index] = instance
	n.conns = append(n.conns, client, server)
	n.mu.Unlock()
	return client, nil
}

func (n *sftpDialNetwork) dialCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.dials
}

func (n *sftpDialNetwork) serverAt(index int) *fakessh.Server {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.servers[index]
}

func (n *sftpDialNetwork) restoreHostKey() {
	n.mu.Lock()
	n.changedKey = nil
	n.changedKeyFrom = 0
	n.mu.Unlock()
}

func (n *sftpDialNetwork) close() {
	n.mu.Lock()
	servers := make([]*fakessh.Server, 0, len(n.servers))
	for _, server := range n.servers {
		servers = append(servers, server)
	}
	conns := n.conns
	n.conns = nil
	n.servers = map[int]*fakessh.Server{}
	n.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	for _, server := range servers {
		_ = server.Close()
	}
}

// sftpTestPrompter records every host-key prompt. The dedicated dial must
// never add one: the user did not start it.
type sftpTestPrompter struct {
	mu      sync.Mutex
	persist bool
	calls   []domainconn.Key
}

func (p *sftpTestPrompter) Confirm(_ context.Context, key domainconn.Key) (appconnection.PromptDecision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, key)
	return appconnection.PromptDecision{Trust: true, Persist: p.persist}, nil
}

func (p *sftpTestPrompter) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// sftpKnownHosts persists trust like the Vault store: an upsert is visible to
// the next dial, so a saved decision is not prompted for again.
type sftpKnownHosts struct {
	mu      sync.Mutex
	records []domainconn.KnownHost
	saved   []domainconn.KnownHost
}

func (k *sftpKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]domainconn.KnownHost(nil), k.records...), nil
}

func (k *sftpKnownHosts) UpsertKnownHost(_ context.Context, record domainconn.KnownHost) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.records = append(k.records, record)
	k.saved = append(k.saved, record)
	return nil
}

type sftpFixtureOptions struct {
	serverVersion  string
	persistTrust   bool
	changedKeyFrom int
}

type sftpFixture struct {
	ctx      context.Context
	network  *sftpDialNetwork
	prompter *sftpTestPrompter
	known    *sftpKnownHosts
	vault    *appvault.Service
	manager  *appconnection.Manager
	opener   *sftpRemoteOpener
	host     domainvault.Host
}

func newSFTPFixture(t *testing.T, options sftpFixtureOptions) *sftpFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	directory := t.TempDir()
	remoteRoot := filepath.Join(directory, "remote")
	if err := os.MkdirAll(remoteRoot, 0o755); err != nil {
		t.Fatalf("create the remote root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remoteRoot, "read.txt"), []byte("dedicated sftp"), 0o644); err != nil {
		t.Fatalf("seed the remote file: %v", err)
	}

	stableKey := newSFTPHostSigner(t)
	var changedKey xssh.Signer
	if options.changedKeyFrom > 0 {
		changedKey = newSFTPHostSigner(t)
	}
	network := &sftpDialNetwork{
		root:           remoteRoot,
		serverVersion:  options.serverVersion,
		hostKey:        stableKey,
		changedKey:     changedKey,
		changedKeyFrom: options.changedKeyFrom,
		fail:           map[int]error{},
		servers:        map[int]*fakessh.Server{},
	}
	t.Cleanup(network.close)

	database, err := store.Open(ctx, filepath.Join(directory, "vault.db"))
	if err != nil {
		t.Fatalf("open the Vault database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	keyRing := &writeTestKeyRing{values: map[string][]byte{}}
	secrets, err := store.OpenSecretStore(ctx, database, keyRing,
		store.WithArgon2Params(1, 1024, 1))
	if err != nil {
		t.Fatalf("open the secret store: %v", err)
	}
	t.Cleanup(secrets.Close)
	vault := appvault.NewService(database, appvault.WithSecrets(secrets),
		appvault.WithKeys(database), appvault.WithGroups(database),
		appvault.WithNotes(database), appvault.WithSnippets(database),
		appvault.WithIdentities(database), appvault.WithProxyProfiles(database),
		appvault.WithKnownHosts(database), appvault.WithImportStore(database))

	prompter := &sftpTestPrompter{persist: options.persistTrust}
	known := &sftpKnownHosts{}
	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(network.dial),
		sshadapter.WithTimeouts(5*time.Second, 5*time.Second),
	)
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: vault}, known,
		appconnection.WithPrompter(prompter))
	t.Cleanup(manager.CloseAll)

	host, err := vault.Create(ctx, domainvault.Draft{
		Label: "single channel device", Hostname: "device.example", Port: 22,
		Username: "admin", Protocol: domainvault.ProtocolSSH,
		SFTPFileProtocol: domainvault.FileProtocolSFTP,
	})
	if err != nil {
		t.Fatalf("create the fixture Host: %v", err)
	}
	if err := vault.SetSecret(ctx, host.ID, appvault.FieldPassword, "s3cret"); err != nil {
		t.Fatalf("store the fixture password: %v", err)
	}

	return &sftpFixture{
		ctx:      ctx,
		network:  network,
		prompter: prompter,
		known:    known,
		vault:    vault,
		manager:  manager,
		opener:   &sftpRemoteOpener{manager: manager, vault: vault},
		host:     host,
	}
}

func newSFTPHostSigner(t *testing.T) xssh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the fixture host key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("build the fixture signer: %v", err)
	}
	return signer
}

// acquireTerminal stands in for an interactive Session: one lease that keeps
// the terminal transport occupied while the SFTP pane opens.
func (f *sftpFixture) acquireTerminal(t *testing.T) *appconnection.Lease {
	t.Helper()
	lease, err := f.manager.Acquire(f.ctx, appconnection.Request{
		HostID:    f.host.ID,
		Hostname:  f.host.Hostname,
		Port:      f.host.Port,
		Username:  f.host.Username,
		LeaseKind: appconnection.LeaseShell,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		t.Fatalf("acquire the terminal transport: %v", err)
	}
	return lease
}

func (f *sftpFixture) openRemote() (apptransfer.FileSystem, error) {
	return f.opener.OpenRemote(f.ctx, f.host.ID, "")
}

func subsystemRequests(server *fakessh.Server) []string {
	if server == nil {
		return nil
	}
	return server.SubsystemRequests()
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// TestSingleChannelHostServesSFTPOverACompanionTransport is acceptance bullet
// one: SFTP works for a network-device style single-channel peer, on a second
// physical transport, without touching the terminal's one channel.
func TestSingleChannelHostServesSFTPOverACompanionTransport(t *testing.T) {
	fixture := newSFTPFixture(t, sftpFixtureOptions{serverVersion: "SSH-2.0-TERM-SSHD"})
	terminal := fixture.acquireTerminal(t)
	defer terminal.Release()

	files, err := fixture.openRemote()
	if err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	defer files.Close()

	if dials := fixture.network.dialCount(); dials != 2 {
		t.Fatalf("physical dials = %d, want 2 (terminal + companion)", dials)
	}
	if names := subsystemRequests(fixture.network.serverAt(1)); len(names) != 0 {
		t.Fatalf("the terminal transport opened subsystems %v", names)
	}
	if names := subsystemRequests(fixture.network.serverAt(2)); !containsName(names, "sftp") {
		t.Fatalf("the companion transport opened subsystems %v, want sftp", names)
	}
	if !terminal.Alive() {
		t.Fatal("the terminal transport died while the companion opened")
	}
	if prompts := fixture.prompter.count(); prompts != 1 {
		t.Fatalf("host-key prompts = %d, want only the terminal's", prompts)
	}

	root, err := files.Root(fixture.ctx)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	entries, err := files.List(fixture.ctx, root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, entry := range entries {
		if files.Base(entry.Name) == "read.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the companion SFTP did not list read.txt: %+v", entries)
	}
}

// TestCompanionDialTrustFailureFailsTheTransferNotTheTerminal is acceptance
// bullet three for trust: a changed key on the companion dial fails with an
// actionable error, never prompts behind the user's back, and leaves the
// terminal lease alive. Restoring the key lets the same terminal open again.
func TestCompanionDialTrustFailureFailsTheTransferNotTheTerminal(t *testing.T) {
	fixture := newSFTPFixture(t, sftpFixtureOptions{
		serverVersion:  "SSH-2.0-TERM-SSHD",
		changedKeyFrom: 2,
	})
	terminal := fixture.acquireTerminal(t)
	defer terminal.Release()

	_, err := fixture.openRemote()
	if !errors.Is(err, apptransfer.ErrDedicatedDial) {
		t.Fatalf("error = %v, want ErrDedicatedDial", err)
	}
	if !errors.Is(err, appconnection.ErrHostKeyUnverified) {
		t.Fatalf("error = %v, want the inherited-trust refusal", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "reconnect the terminal") {
		t.Fatalf("error = %v, want a reconnect instruction", err)
	}
	if prompts := fixture.prompter.count(); prompts != 1 {
		t.Fatalf("host-key prompts = %d, want the companion dial to stay unattended", prompts)
	}
	if !terminal.Alive() {
		t.Fatal("the failed companion dial took the terminal transport down")
	}
	if dials := fixture.network.dialCount(); dials != 2 {
		t.Fatalf("physical dials = %d, want 2", dials)
	}

	// The failure is not cached: once the peer presents the accepted key
	// again, the next Transfer dials a fresh companion and succeeds.
	fixture.network.restoreHostKey()
	files, err := fixture.openRemote()
	if err != nil {
		t.Fatalf("OpenRemote after the trust failure: %v", err)
	}
	defer files.Close()
	if dials := fixture.network.dialCount(); dials != 3 {
		t.Fatalf("physical dials after retry = %d, want 3", dials)
	}
	if !terminal.Alive() {
		t.Fatal("the retry disturbed the terminal transport")
	}
}

// TestCompanionDialLifecycleFailureIsReportedAgainstTheTransfer proves a
// transport-level companion failure (a timeout or a refused dial) fails the
// Transfer with ErrDedicatedDial and leaves the terminal usable.
func TestCompanionDialLifecycleFailureIsReportedAgainstTheTransfer(t *testing.T) {
	fixture := newSFTPFixture(t, sftpFixtureOptions{serverVersion: "SSH-2.0-TERM-SSHD"})
	terminal := fixture.acquireTerminal(t)
	defer terminal.Release()

	fixture.network.mu.Lock()
	fixture.network.fail[2] = context.DeadlineExceeded
	fixture.network.mu.Unlock()

	_, err := fixture.openRemote()
	if !errors.Is(err, apptransfer.ErrDedicatedDial) {
		t.Fatalf("error = %v, want ErrDedicatedDial", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindTimeout {
		t.Fatalf("error = %v, want a typed timeout", err)
	}
	if !terminal.Alive() {
		t.Fatal("a timed-out companion dial took the terminal transport down")
	}

	// Timeout recovery: nothing was parked, so the next attempt dials again.
	fixture.network.mu.Lock()
	delete(fixture.network.fail, 2)
	fixture.network.mu.Unlock()
	files, err := fixture.openRemote()
	if err != nil {
		t.Fatalf("OpenRemote after the timeout: %v", err)
	}
	defer files.Close()
	if dials := fixture.network.dialCount(); dials != 3 {
		t.Fatalf("physical dials after recovery = %d, want 3", dials)
	}
}

// TestDedicatedRoutingSurvivesTrustSaveAndReconnect is acceptance bullet two.
// Saving the terminal's host trust and reconnecting the terminal both keep the
// companion route: the new terminal transport still never receives the SFTP
// subsystem.
func TestDedicatedRoutingSurvivesTrustSaveAndReconnect(t *testing.T) {
	fixture := newSFTPFixture(t, sftpFixtureOptions{
		serverVersion: "SSH-2.0-TERM-SSHD",
		persistTrust:  true,
	})
	terminal := fixture.acquireTerminal(t)
	if prompts := fixture.prompter.count(); prompts != 1 {
		t.Fatalf("terminal host-key prompts = %d, want 1", prompts)
	}
	if len(fixture.known.saved) != 1 {
		t.Fatalf("saved Known Hosts = %d, want 1", len(fixture.known.saved))
	}

	files, err := fixture.openRemote()
	if err != nil {
		t.Fatalf("OpenRemote before the reconnect: %v", err)
	}
	if dials := fixture.network.dialCount(); dials != 2 {
		t.Fatalf("physical dials = %d, want 2", dials)
	}
	if names := subsystemRequests(fixture.network.serverAt(1)); len(names) != 0 {
		t.Fatalf("the saved-trust terminal transport opened %v", names)
	}
	if names := subsystemRequests(fixture.network.serverAt(2)); !containsName(names, "sftp") {
		t.Fatalf("the companion transport opened %v, want sftp", names)
	}
	if err := files.Close(); err != nil {
		t.Fatalf("close the SFTP filesystem: %v", err)
	}

	// The GUI reconnects the terminal: the old transport is gone and a new
	// physical dial takes its place.
	if err := terminal.Release(); err != nil {
		t.Fatalf("release the first terminal lease: %v", err)
	}
	reconnected := fixture.acquireTerminal(t)
	defer reconnected.Release()
	if dials := fixture.network.dialCount(); dials != 3 {
		t.Fatalf("physical dials after the terminal reconnect = %d, want 3", dials)
	}

	files, err = fixture.openRemote()
	if err != nil {
		t.Fatalf("OpenRemote after the reconnect: %v", err)
	}
	defer files.Close()
	if dials := fixture.network.dialCount(); dials != 4 {
		t.Fatalf("physical dials after the second SFTP open = %d, want 4", dials)
	}
	if names := subsystemRequests(fixture.network.serverAt(3)); len(names) != 0 {
		t.Fatalf("the reconnected terminal transport opened %v", names)
	}
	if names := subsystemRequests(fixture.network.serverAt(4)); !containsName(names, "sftp") {
		t.Fatalf("the second companion transport opened %v, want sftp", names)
	}
	if prompts := fixture.prompter.count(); prompts != 1 {
		t.Fatalf("host-key prompts = %d, want the persisted trust to suppress the rest", prompts)
	}
}
