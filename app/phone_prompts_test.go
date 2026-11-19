package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	xssh "golang.org/x/crypto/ssh"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	"github.com/mslxl/natcatty/adapters/store"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// memoryFieldSecrets is the field-secret store's port in memory. The real
// adapter encrypts under the OS credential store; the test asserts the Vault
// receives the password under the Host's reference and that no plaintext
// reaches the disk, without depending on a host keyring.
type memoryFieldSecrets struct {
	mu     sync.Mutex
	values map[appvault.SecretRef]string
}

func newMemoryFieldSecrets() *memoryFieldSecrets {
	return &memoryFieldSecrets{values: map[appvault.SecretRef]string{}}
}

func (m *memoryFieldSecrets) Mode() appvault.SecretMode { return appvault.SecretModeOS }

func (m *memoryFieldSecrets) Set(_ context.Context, ref appvault.SecretRef, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[ref] = value
	return nil
}

func (m *memoryFieldSecrets) Get(_ context.Context, ref appvault.SecretRef) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.values[ref]
	if !ok {
		return "", appvault.ErrSecretNotFound
	}
	return value, nil
}

func (m *memoryFieldSecrets) Delete(_ context.Context, ref appvault.SecretRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, ref)
	return nil
}

func (m *memoryFieldSecrets) SetPassphrase(context.Context, string) error        { return nil }
func (m *memoryFieldSecrets) UnlockWithPassphrase(context.Context, string) error { return nil }
func (m *memoryFieldSecrets) UseSessionOnly(context.Context) error               { return nil }

func (m *memoryFieldSecrets) value(ref appvault.SecretRef) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.values[ref]
	return value, ok
}

// fakesshDialer starts one in-process SSH server per dial. The Options factory
// lets a test hold the host key stable across runs or change it, and the
// recorded servers expose what the client actually sent.
type fakesshDialer struct {
	mu      sync.Mutex
	options func() fakessh.Options
	servers []*fakessh.Server
}

func (d *fakesshDialer) dial(context.Context, string, string) (net.Conn, error) {
	client, server := fakessh.Pipe()
	started, err := fakessh.Start(server, d.options())
	if err != nil {
		_ = client.Close()
		_ = server.Close()
		return nil, err
	}
	d.mu.Lock()
	d.servers = append(d.servers, started)
	d.mu.Unlock()
	return client, nil
}

func (d *fakesshDialer) last() *fakessh.Server {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.servers) == 0 {
		return nil
	}
	return d.servers[len(d.servers)-1]
}

func (d *fakesshDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.servers)
}

func (d *fakesshDialer) closeAll() {
	d.mu.Lock()
	servers := append([]*fakessh.Server(nil), d.servers...)
	d.mu.Unlock()
	for _, server := range servers {
		_ = server.Close()
	}
}

// nopScreen is the test terminal. The app-level tests are about the prompts,
// so the screen only has to exist and name itself.
type nopScreen struct{}

func (nopScreen) View(c *ui.Context) ui.Element { return ui.Text(c, "terminal") }
func (nopScreen) Close() error                  { return nil }

func nopScreens(io.ReadWriteCloser) (phoneshell.TerminalScreen, error) {
	return nopScreen{}, nil
}

func fixtureSigner(t *testing.T) xssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	return signer
}

func encryptedKey(t *testing.T, passphrase string) (xssh.Signer, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("user signer: %v", err)
	}
	block, err := xssh.MarshalPrivateKeyWithPassphrase(private, "test", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	return signer, string(pem.EncodeToMemory(block))
}

// promptTestRig is one phone app over a SQLite Vault: the same store the phone
// root opens, with the Vault's field-secret port in memory.
type promptTestRig struct {
	userData string
	db       *store.Store
	secrets  *memoryFieldSecrets
	vault    *appvault.Service
	prompts  *phoneshell.Prompts
	runtime  *phoneSessionRuntime
	runtimes []*phoneSessionRuntime
	shell    *phoneshell.Shell
	tester   *ui.Tester
	dialer   *fakesshDialer
}

func newPromptTestRig(t *testing.T, host domainvault.Host, options func() fakessh.Options) *promptTestRig {
	t.Helper()
	rig := &promptTestRig{userData: t.TempDir()}
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(rig.userData, "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rig.db = db
	if err := db.Save(ctx, host); err != nil {
		t.Fatalf("save Host: %v", err)
	}
	rig.secrets = newMemoryFieldSecrets()
	rig.vault = appvault.NewService(db,
		appvault.WithKeys(db),
		appvault.WithIdentities(db),
		appvault.WithGroups(db),
		appvault.WithProxyProfiles(db),
		appvault.WithNetworkProfiles(db),
		appvault.WithKnownHosts(db),
		appvault.WithSecrets(rig.secrets),
	)
	rig.dialer = &fakesshDialer{options: options}
	rig.start(t)
	t.Cleanup(func() {
		rig.shell.Close()
		for _, runtime := range rig.runtimes {
			runtime.Close()
		}
		rig.dialer.closeAll()
		_ = db.Close()
	})
	return rig
}

// start wires the production prompts, runtime, and shell over the rig's Vault.
// Calling it again models the next app run: a new Connection Manager with no
// pooled transport, but the same Vault and Known Hosts.
func (r *promptTestRig) start(t *testing.T) {
	t.Helper()
	r.prompts = newPhonePrompts(r.vault)
	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(r.dialer.dial),
		sshadapter.WithTimeouts(2*time.Second, 2*time.Second),
	)
	r.runtime = newPhoneSessionRuntimeWithDialer(dialer, r.vault, r.db, r.userData, r.prompts)
	r.runtimes = append(r.runtimes, r.runtime)
	r.shell = phoneshell.New(
		phoneshell.WithVault(r.vault),
		phoneshell.WithHosts(r.vault),
		phoneshell.WithSessionOpener(r.runtime),
		phoneshell.WithPrompts(r.prompts),
		phoneshell.WithScreenFactory(nopScreens),
	)
	if err := r.shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.shell.Select(phoneshell.Hosts)
	r.tester = ui.NewTester(func(c *ui.Context) {
		r.shell.View(r.shell.Selected())(c)
	}, 390, 844)
	r.tester.Frame()
}

func (r *promptTestRig) open(t *testing.T, label string) {
	t.Helper()
	if err := r.tester.Click("Open " + label); err != nil {
		t.Fatalf("open %s: %v; texts: %q", label, err, r.tester.Texts())
	}
	r.tester.Frame()
}

func (r *promptTestRig) click(t *testing.T, label string) {
	t.Helper()
	if err := r.tester.Click(label); err != nil {
		t.Fatalf("click %q: %v; texts: %q", label, err, r.tester.Texts())
	}
	r.tester.Frame()
}

func (r *promptTestRig) await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; texts: %q", what, r.tester.Texts())
		}
		r.tester.Frame()
		r.tester.Advance(20 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	r.tester.Frame()
}

func (r *promptTestRig) awaitText(t *testing.T, text string) {
	t.Helper()
	r.await(t, text, func() bool { return r.tester.HasText(text) })
}

func (r *promptTestRig) awaitReady(t *testing.T) {
	t.Helper()
	r.await(t, "the terminal", func() bool { return r.tester.HasText("terminal") })
}

func (r *promptTestRig) assertIdle(t *testing.T) {
	t.Helper()
	if pending := r.prompts.HostKey.Pending(); pending != 0 {
		t.Fatalf("host-key prompt still pending: %d", pending)
	}
	if pending := r.prompts.Auth.Pending(); pending != 0 {
		t.Fatalf("credential prompt still pending: %d", pending)
	}
}

func phonePromptHost() domainvault.Host {
	return domainvault.Host{
		ID: "host-1", Label: "prod box", Hostname: "prod.example.com", Port: 2222,
		Username: "alice", Protocol: domainvault.ProtocolSSH,
	}
}

func TestPhoneHostKeyLifecycleAgainstAServer(t *testing.T) {
	keyA := fixtureSigner(t)
	keyB := fixtureSigner(t)
	knownHostKey := keyA
	const hostPassword = "fixture-pw"

	rig := newPromptTestRig(t, phonePromptHost(), func() fakessh.Options {
		return fakessh.Options{HostKey: knownHostKey, Password: hostPassword}
	})
	// The lifecycle test is about the host key, so give the Host a stored
	// password: authentication then completes without a credential prompt.
	if err := rig.secrets.Set(context.Background(),
		appvault.HostSecretRef("host-1", appvault.FieldPassword), hostPassword); err != nil {
		t.Fatalf("store host password: %v", err)
	}

	// First contact: the fingerprint is shown and accepting records a Known
	// Host through the Manager's repository.
	rig.open(t, "prod box")
	rig.awaitText(t, "Unknown host key")
	server := rig.dialer.last()
	if server == nil {
		t.Fatal("no server was dialed")
	}
	fingerprint := server.Fingerprint()
	if !rig.tester.HasText(server.KeyType() + " fingerprint: SHA256:" + fingerprint) {
		t.Fatalf("the server fingerprint is not shown; texts: %q", rig.tester.Texts())
	}
	if fingerprint == "" {
		t.Fatal("the server reported an empty fingerprint")
	}
	rig.click(t, "Trust and save")
	rig.awaitReady(t)
	rig.assertIdle(t)

	known, err := rig.db.ListKnownHosts(context.Background())
	if err != nil {
		t.Fatalf("list known hosts: %v", err)
	}
	found := false
	for _, entry := range known {
		if entry.Hostname == "prod.example.com" && entry.Port == 2222 && entry.Fingerprint == fingerprint {
			found = true
		}
	}
	if !found {
		t.Fatalf("accepted key was not recorded as a Known Host: %+v", known)
	}
	rig.shell.Close()

	// The next app run has a fresh Manager and no pooled transport. The same
	// Host connects without a prompt because the decision was persisted.
	rig.start(t)
	rig.open(t, "prod box")
	rig.awaitReady(t)
	rig.assertIdle(t)
	if rig.dialer.count() != 2 {
		t.Fatalf("dial count = %d, want a fresh second dial", rig.dialer.count())
	}
	if rig.tester.HasText("Unknown host key") || rig.tester.HasText("Host key changed") {
		t.Fatalf("a Known Host prompted again; texts: %q", rig.tester.Texts())
	}
	rig.shell.Close()

	// A different key for the same endpoint is a changed key: it warns with the
	// old fingerprint instead of trusting silently, and rejecting leaves no Tab.
	knownHostKey = keyB
	rig.start(t)
	rig.open(t, "prod box")
	rig.awaitText(t, "Host key changed")
	if rig.tester.HasText("Unknown host key") {
		t.Fatalf("a changed key was presented as unknown; texts: %q", rig.tester.Texts())
	}
	if !rig.tester.HasText("Previously trusted fingerprint: SHA256:" + fingerprint) {
		t.Fatalf("the previously trusted fingerprint is missing; texts: %q", rig.tester.Texts())
	}
	rig.click(t, "Reject")
	rig.awaitText(t, "No sessions yet.")
	rig.assertIdle(t)
	if got := rig.shell.Router(phoneshell.Sessions).Path(); got != "/" {
		t.Fatalf("Sessions path = %q, want its root after rejecting", got)
	}
}

func TestPhonePasswordPromptAgainstAServerPersistsThroughTheCredentialStore(t *testing.T) {
	var logs bytes.Buffer
	previousLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLog) })

	const password = "vN7-qplx-2fHs"
	rig := newPromptTestRig(t, phonePromptHost(), func() fakessh.Options {
		return fakessh.Options{Password: password}
	})

	rig.open(t, "prod box")
	rig.awaitText(t, "Unknown host key")
	rig.click(t, "Trust and save")

	rig.awaitText(t, "Password required")
	if !rig.tester.HasText("Sign in as alice@prod.example.com.") {
		t.Fatalf("the username and Host are not named; texts: %q", rig.tester.Texts())
	}
	rig.tester.Type(password)
	rig.tester.Frame()
	rig.click(t, "Continue")
	rig.awaitReady(t)
	rig.assertIdle(t)

	server := rig.dialer.last()
	if _, got := server.LastCredentials(); got != password {
		t.Fatalf("server saw password %q, want the answered one", got)
	}
	ref := appvault.HostSecretRef("host-1", appvault.FieldPassword)
	if got, ok := rig.secrets.value(ref); !ok || got != password {
		t.Fatalf("credential store %+v = %q, %v; want the answered password", ref, got, ok)
	}
	host, err := rig.db.Get(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("read Host: %v", err)
	}
	if !host.HasPassword {
		t.Error("the Host was not marked as having a stored password")
	}

	// The secret never reaches a written file (settings, Vault record, WAL) or
	// the captured log. The in-memory store is the only custodian.
	assertTreeDoesNotContain(t, rig.userData, password)
	if strings.Contains(logs.String(), password) {
		t.Fatalf("the password was logged: %q", logs.String())
	}
}

func TestPhoneKeyboardInteractivePromptAgainstAServerAnswersEveryRound(t *testing.T) {
	rig := newPromptTestRig(t, func() domainvault.Host {
		host := phonePromptHost()
		host.RequiresMFA = true
		return host
	}(), func() fakessh.Options {
		return fakessh.Options{
			KeyboardInteractive: func(_ xssh.ConnMetadata, challenge xssh.KeyboardInteractiveChallenge) (*xssh.Permissions, error) {
				if _, err := challenge("Authentication", "First factor",
					[]string{"Verification code"}, []bool{false}); err != nil {
					return nil, err
				}
				if _, err := challenge("Authentication", "Second factor",
					[]string{"One-time password"}, []bool{false}); err != nil {
					return nil, err
				}
				return nil, nil
			},
		}
	})

	rig.open(t, "prod box")
	rig.awaitText(t, "Unknown host key")
	rig.click(t, "Trust and save")

	rig.awaitText(t, "Verification code")
	rig.tester.Type("123456")
	rig.tester.Frame()
	rig.click(t, "Continue")

	rig.awaitText(t, "One-time password")
	rig.tester.Type("654321")
	rig.tester.Frame()
	rig.click(t, "Continue")
	rig.awaitReady(t)
	rig.assertIdle(t)

	rounds := rig.dialer.last().KeyboardInteractiveRounds()
	if len(rounds) != 2 {
		t.Fatalf("server rounds = %d, want 2: %+v", len(rounds), rounds)
	}
	if strings.Join(rounds[0].Answers, ",") != "123456" {
		t.Fatalf("first round answers = %v", rounds[0].Answers)
	}
	if strings.Join(rounds[1].Answers, ",") != "654321" {
		t.Fatalf("second round answers = %v", rounds[1].Answers)
	}
}

func TestPhonePassphrasePromptAgainstAnEncryptedKey(t *testing.T) {
	const passphrase = "open-sesame-42"
	userSigner, keyPEM := encryptedKey(t, passphrase)
	rig := newPromptTestRig(t, func() domainvault.Host {
		host := phonePromptHost()
		host.AuthMethod = domainvault.HostAuthKey
		host.KeyID = "key-1"
		return host
	}(), func() fakessh.Options {
		return fakessh.Options{PublicKeys: []xssh.PublicKey{userSigner.PublicKey()}}
	})

	ctx := context.Background()
	if err := rig.secrets.Set(ctx, appvault.KeySecretRef("key-1", appvault.FieldPrivateKey), keyPEM); err != nil {
		t.Fatalf("store key material: %v", err)
	}
	if err := rig.db.SaveKey(ctx, domainvault.Key{
		ID: "key-1", Label: "work", Type: domainvault.KeyTypeED25519, HasPrivateKey: true,
	}); err != nil {
		t.Fatalf("save Key: %v", err)
	}

	rig.open(t, "prod box")

	// The transport prepares the encrypted Key before the handshake, so the
	// passphrase is asked before the host key.
	rig.awaitText(t, "Key passphrase required")
	if !rig.tester.HasText("Enter the passphrase for SSH Key “work”.") {
		t.Fatalf("the Key label is not named; texts: %q", rig.tester.Texts())
	}
	rig.tester.Type(passphrase)
	rig.tester.Frame()
	rig.click(t, "Continue")

	rig.awaitText(t, "Unknown host key")
	rig.click(t, "Trust and save")
	rig.awaitReady(t)
	rig.assertIdle(t)

	server := rig.dialer.last()
	if server.PublicKeyAttempts() == 0 {
		t.Fatal("the server saw no public-key attempt")
	}
	if !server.ShellOpened() {
		t.Fatal("the encrypted Key did not reach a shell")
	}
}

// assertTreeDoesNotContain reads every file under root, including the SQLite
// database and its journal, and fails if any contains the secret in plaintext.
func assertTreeDoesNotContain(t *testing.T, root, secret string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("%s contains the secret in plaintext", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
}
