package app

// The phone speaks every remote transport the desktop does. These tests drive
// the production phone composition root (the real Vault, Connection Manager,
// Transport factory, and Session service) against in-process wire endpoints:
// a fake SSH server, a real in-process mosh-server, an in-process etserver,
// and a TCP Telnet service. No remote host is involved.

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	ettest "github.com/mslxl/natcatty/adapters/et/ettest"
	moshtest "github.com/mslxl/natcatty/adapters/mosh/moshtest"
	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	"github.com/mslxl/natcatty/adapters/store"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

const phoneTestPassword = "phone-transport-pw"

// phoneTransportEnv is the phone's real storage and Vault over a test
// user-data directory. The SQLite database is the file the phone root opens,
// so a test can close it, reopen the same path, and model the next app run.
type phoneTransportEnv struct {
	userData string
	keyRing  *writeTestKeyRing
	db       *store.Store
	secrets  *store.SecretStore
	vault    *appvault.Service
	prompts  *phoneshell.Prompts
	dialer   *fakesshDialer
	runtime  *phoneSessionRuntime
	stop     sync.Once
}

func newPhoneTransportEnv(t *testing.T, options func() fakessh.Options) *phoneTransportEnv {
	t.Helper()
	return newPhoneTransportEnvAt(t, t.TempDir(), &writeTestKeyRing{values: map[string][]byte{}}, options)
}

// newPhoneTransportEnvAt opens a phone environment over an existing user-data
// directory and keyring, which is what the second run of a relaunch test sees.
func newPhoneTransportEnvAt(t *testing.T, userData string, keyRing *writeTestKeyRing,
	options func() fakessh.Options) *phoneTransportEnv {

	t.Helper()
	env := &phoneTransportEnv{
		userData: userData,
		keyRing:  keyRing,
		dialer:   &fakesshDialer{options: options},
	}
	env.open(t)
	t.Cleanup(env.shutdown)
	return env
}

// open builds the Vault, prompts, and Session runtime over the environment's
// user-data directory. A relaunch test opens a second environment over the
// same directory after shutting the first down.
func (e *phoneTransportEnv) open(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(e.userData, "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	secrets, err := store.OpenSecretStore(ctx, db, e.keyRing,
		store.WithArgon2Params(1, 1024, 1))
	if err != nil {
		_ = db.Close()
		t.Fatalf("open secret store: %v", err)
	}
	e.db, e.secrets = db, secrets
	e.vault = newPhoneVault(db, secrets)
	e.prompts = newPhonePrompts(e.vault)
	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(e.dialer.dial),
		sshadapter.WithTimeouts(5*time.Second, 5*time.Second),
	)
	e.runtime = newPhoneSessionRuntimeWithDialer(
		dialer, e.vault, db, e.userData, e.prompts, nil, secrets)
}

// shutdown ends one app run: the Session runtime, the encrypted store, and
// the database. The user-data directory and the keyring stay, as they do
// across a phone relaunch. It is idempotent so cleanup can run after closeRun.
func (e *phoneTransportEnv) shutdown() {
	e.stop.Do(func() {
		if e.runtime != nil {
			e.runtime.Close()
		}
		if e.dialer != nil {
			e.dialer.closeAll()
		}
		if e.secrets != nil {
			e.secrets.Close()
		}
		if e.db != nil {
			_ = e.db.Close()
		}
	})
}

// addHost saves a Host with the password the fake SSH server accepts, and
// trusts the fixture's host key. The key trust is what lets Open reach the
// endpoint without a UI prompt.
func (e *phoneTransportEnv) addHost(t *testing.T, host domainvault.Host, signer xssh.Signer) {
	t.Helper()
	ctx := context.Background()
	if err := e.db.Save(ctx, host); err != nil {
		t.Fatalf("save Host %s: %v", host.ID, err)
	}
	if err := e.secrets.Set(ctx,
		appvault.HostSecretRef(host.ID, appvault.FieldPassword), phoneTestPassword); err != nil {
		t.Fatalf("store password for %s: %v", host.ID, err)
	}
	if signer == nil {
		return
	}
	public := signer.PublicKey()
	now := time.Now()
	record := domainconn.KnownHost{
		ID:           "known-" + string(host.ID),
		Hostname:     host.Hostname,
		Port:         host.Port,
		KeyType:      public.Type(),
		PublicKey:    string(xssh.MarshalAuthorizedKey(public)),
		Fingerprint:  xssh.FingerprintSHA256(public),
		DiscoveredAt: now,
		LastSeen:     now,
	}
	if err := e.db.UpsertKnownHost(ctx, record); err != nil {
		t.Fatalf("trust fixture host key for %s: %v", host.ID, err)
	}
}

func (e *phoneTransportEnv) openHost(t *testing.T, host domainvault.Host) *phoneSessionHandle {
	t.Helper()
	handle, err := e.runtime.OpenHost(context.Background(), host)
	if err != nil {
		t.Fatalf("OpenHost(%s): %v", host.ID, err)
	}
	real, ok := handle.(*phoneSessionHandle)
	if !ok {
		t.Fatalf("OpenHost returned %T, want the application handle", handle)
	}
	return real
}

// readPhoneUntil collects a Session view's output until it contains marker.
func readPhoneUntil(t *testing.T, r io.Reader, marker string, timeout time.Duration) string {
	t.Helper()
	result := make(chan string, 1)
	go func() {
		var collected bytes.Buffer
		buffer := make([]byte, 4096)
		for {
			n, err := r.Read(buffer)
			if n > 0 {
				collected.Write(buffer[:n])
				if strings.Contains(collected.String(), marker) {
					result <- collected.String()
					return
				}
			}
			if err != nil {
				result <- collected.String()
				return
			}
		}
	}()
	select {
	case text := <-result:
		if !strings.Contains(text, marker) {
			t.Fatalf("output %q does not contain %q", text, marker)
		}
		return text
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %q", marker)
		return ""
	}
}

func awaitResize(t *testing.T, resizes <-chan [2]int, want [2]int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case got := <-resizes:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("the mosh peer never saw resize %v", want)
		}
	}
}

func awaitCondition(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// startMoshBootstrap is the fake SSH server's exec handler: it prints the
// announcement for the server behind the relay and counts one bootstrap.
func startMoshBootstrap(server *moshtest.Server, relay *moshtest.Proxy,
	bootstraps *atomic.Int32) func(string, xssh.Channel) int {

	return func(command string, channel xssh.Channel) int {
		if !strings.Contains(command, "mosh-server") {
			return 1
		}
		bootstraps.Add(1)
		_, _ = io.WriteString(channel, server.AnnouncementOn(relay.Port()))
		return 0
	}
}

// TestPhoneMoshRunsInteractiveProgramSurvivesNetworkChangeAndResizes proves a
// Mosh Host connects from the phone, runs a program, keeps its Session across
// a network change modelled at the transport seam, and that its size reaches
// the Mosh peer. A physical Wi-Fi toggle is not available in this environment:
// the relay drops datagrams (leaving coverage) and then rebinds the server-side
// source address (returning on a new local path). The client keeps one
// unconnected UDP socket throughout.
func TestPhoneMoshRunsInteractiveProgramSurvivesNetworkChangeAndResizes(t *testing.T) {
	server := moshtest.New(t)
	relay := moshtest.NewProxy(t, server.Addr())
	var bootstraps atomic.Int32
	signer := fixtureSigner(t)
	env := newPhoneTransportEnv(t, func() fakessh.Options {
		return fakessh.Options{
			HostKey:  signer,
			Password: phoneTestPassword,
			Exec:     startMoshBootstrap(server, relay, &bootstraps),
		}
	})
	host := domainvault.Host{
		ID: "udp", Label: "udp box", Hostname: "127.0.0.1", Port: 2222,
		Username: "deploy", Protocol: domainvault.ProtocolMosh,
	}
	env.addHost(t, host, signer)

	handle := env.openHost(t, host)
	conn, err := handle.Attach()
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	view, ok := conn.(interface {
		Resize(cols, rows int) error
		ScreenSizeFromRemote() bool
		SetScreenSizeSink(func(cols, rows int))
	})
	if !ok {
		t.Fatalf("the phone view %T lost the terminal's Resize or sink methods", conn)
	}
	if !view.ScreenSizeFromRemote() {
		t.Fatal("a Mosh view does not report a peer-owned screen size")
	}

	// The interactive program runs on the far side: typed input is sent, and
	// the server's output reaches the phone's view.
	readPhoneUntil(t, conn, "fake$ ", 10*time.Second)
	if _, err := conn.Write([]byte("echo hi\r")); err != nil {
		t.Fatalf("write typed input: %v", err)
	}
	readPhoneUntil(t, conn, "hi\r\n", 10*time.Second)
	if got := bootstraps.Load(); got != 1 {
		t.Fatalf("mosh bootstrap execs = %d, want 1", got)
	}

	// The keyboard inset and rotation both change the room the terminal has.
	// The terminal's ResizeByConn calls this Resize; the assertion is on the
	// Mosh peer, not on the view.
	if err := view.Resize(80, 18); err != nil {
		t.Fatalf("Resize with the keyboard up: %v", err)
	}
	awaitResize(t, server.Resizes(), [2]int{80, 18}, 5*time.Second)
	if err := view.Resize(140, 42); err != nil {
		t.Fatalf("Resize after rotation: %v", err)
	}
	awaitResize(t, server.Resizes(), [2]int{140, 42}, 5*time.Second)

	// Leaving coverage: the relay drops every datagram. The Session and its
	// unconnected UDP flow stay alive.
	before := server.ClientAddress()
	relay.Blackhole(true)
	if _, err := conn.Write([]byte(" ")); err != nil {
		t.Fatalf("write while out of coverage: %v", err)
	}
	awaitCondition(t, "the relay to drop datagrams", 5*time.Second, func() bool {
		return relay.Drops() > 0
	})
	// Returning on a new local path: the relay's upstream is rebound, so the
	// server sees a new client source address.
	relay.Rebind()
	relay.Blackhole(false)
	if _, err := conn.Write([]byte("echo hi\r")); err != nil {
		t.Fatalf("write after the network change: %v", err)
	}
	readPhoneUntil(t, conn, "hi\r\n", 15*time.Second)
	after := server.ClientAddress()
	if before == nil || after == nil || before.String() == after.String() {
		t.Fatalf("the server source address did not change: %v -> %v", before, after)
	}
	if got := bootstraps.Load(); got != 1 {
		t.Fatalf("the network change re-opened the SSH bootstrap: %d execs", got)
	}
	if relay.Rebinds() != 1 {
		t.Fatalf("relay rebinds = %d, want 1", relay.Rebinds())
	}

	// Hibernation keeps the transport: the view detaches, then Resume replays
	// and input continues on the same Mosh session.
	if err := conn.Close(); err != nil {
		t.Fatalf("close the view: %v", err)
	}
	if !handle.Hibernate() {
		t.Fatal("Hibernate returned false for a running Mosh Session")
	}
	resumed, err := handle.Resume()
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, err := resumed.Write([]byte("echo hi\r")); err != nil {
		t.Fatalf("write through the resumed view: %v", err)
	}
	readPhoneUntil(t, resumed, "hi\r\n", 15*time.Second)
	if got := bootstraps.Load(); got != 1 {
		t.Fatalf("resuming reopened the Mosh bootstrap: %d execs", got)
	}
	_ = resumed.Close()
	if err := handle.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
}

// TestPhoneTelnetHostConnectsAndIsUsable opens a Telnet Host through the phone
// runtime against a real TCP service in this process and types through it.
func TestPhoneTelnetHostConnectsAndIsUsable(t *testing.T) {
	env := newPhoneTransportEnv(t, func() fakessh.Options { return fakessh.Options{} })
	port := startTelnetEcho(t)
	host := domainvault.Host{
		ID: "console", Label: "console", Hostname: "127.0.0.1", Port: port,
		Protocol: domainvault.ProtocolTelnet,
	}
	env.addHost(t, host, nil)

	handle := env.openHost(t, host)
	conn, err := handle.Attach()
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	readPhoneUntil(t, conn, "console ready", 5*time.Second)
	if _, err := conn.Write([]byte("status\r")); err != nil {
		t.Fatalf("write to the Telnet service: %v", err)
	}
	readPhoneUntil(t, conn, "status", 5*time.Second)
	_ = conn.Close()
	if err := handle.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
}

// TestPhoneTelnetRestoresUnderTheSameHostAfterRelaunch is the transport half
// of dormant restore: the saved Tab reopens its Host after a relaunch and the
// service is usable again. The shell's per-transport restore rule is asserted
// in ui/phoneshell.
func TestPhoneTelnetRestoresUnderTheSameHostAfterRelaunch(t *testing.T) {
	port := startTelnetEcho(t)
	userData := t.TempDir()
	keyRing := &writeTestKeyRing{values: map[string][]byte{}}
	host := domainvault.Host{
		ID: "console", Label: "console", Hostname: "127.0.0.1", Port: port,
		Protocol: domainvault.ProtocolTelnet,
	}

	first := newPhoneTransportEnvAt(t, userData, keyRing,
		func() fakessh.Options { return fakessh.Options{} })
	first.addHost(t, host, nil)
	handle := first.openHost(t, host)
	conn, err := handle.Attach()
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	readPhoneUntil(t, conn, "console ready", 5*time.Second)
	_ = conn.Close()
	if err := handle.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	first.shutdown()

	second := newPhoneTransportEnvAt(t, userData, keyRing,
		func() fakessh.Options { return fakessh.Options{} })
	handle2 := second.openHost(t, host)
	conn2, err := handle2.Attach()
	if err != nil {
		t.Fatalf("Attach after relaunch: %v", err)
	}
	readPhoneUntil(t, conn2, "console ready", 5*time.Second)
	_ = conn2.Close()
	if err := handle2.End(); err != nil {
		t.Fatalf("End after relaunch: %v", err)
	}
}

// startTelnetEcho runs a TCP service that greets each connection and echoes
// it back. It returns the port.
func startTelnetEcho(t *testing.T) int {
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
			go func() {
				defer conn.Close()
				_, _ = io.WriteString(conn, "console ready\r\n")
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

const (
	testETID      = "abcdEFGH12345678"
	testETPasskey = "0123456789abcdefABCDEF0123456789"
)

// TestPhoneETResumesAfterRelaunchWithoutBootstrap is the app-restart contract:
// the first run saves the ET credentials in the phone's encrypted field store
// under the user-data directory, quitting preserves them, and the next run
// reattaches the still-running shell without running the etterminal bootstrap
// (which is what would ask the key again).
func TestPhoneETResumesAfterRelaunchWithoutBootstrap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server := ettest.New(testETID, testETPasskey)
	t.Cleanup(server.Close)
	server.AnswerStart(ctx)

	signer := fixtureSigner(t)
	var firstBootstraps atomic.Int32
	etOptions := func(command string, channel xssh.Channel) int {
		if !strings.Contains(command, "etterminal") {
			return 1
		}
		firstBootstraps.Add(1)
		_, _ = io.WriteString(channel, "IDPASSKEY:"+testETID+"/"+testETPasskey+"\r\n")
		return 0
	}
	userData := t.TempDir()
	keyRing := &writeTestKeyRing{values: map[string][]byte{}}
	first := newPhoneTransportEnvAt(t, userData, keyRing, func() fakessh.Options {
		return fakessh.Options{
			HostKey:  signer,
			Password: phoneTestPassword,
			Exec:     etOptions,
			ForwardDial: func(host string, port int) (net.Conn, error) {
				return server.DialContext(context.Background(), "tcp",
					net.JoinHostPort(host, "2022"))
			},
		}
	})
	host := domainvault.Host{
		ID: "et", Label: "et box", Hostname: "127.0.0.1", Port: 2222,
		Username: "deploy", Protocol: domainvault.ProtocolET, ETPort: 2022,
	}
	first.addHost(t, host, signer)
	resumeKey := "et-resume-phone-restart"
	handle, err := first.runtime.OpenHostWithResume(ctx, host, resumeKey)
	if err != nil {
		t.Fatalf("OpenHostWithResume: %v", err)
	}
	real := handle.(*phoneSessionHandle)
	conn, err := real.Attach()
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := server.SendText(ctx, "before the restart\r\n"); err != nil {
		t.Fatalf("send before: %v", err)
	}
	readPhoneUntil(t, conn, "before the restart", 10*time.Second)
	_ = conn.Close()

	// The credentials live only in the encrypted database. Every file the
	// phone can read under its user-data directory is checked, including a
	// settings document placed there deliberately.
	if err := os.WriteFile(filepath.Join(userData, "preferences.json"),
		[]byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("write settings document: %v", err)
	}
	assertNoPlaintext(t, userData, testETPasskey)

	// Quitting the app preserves the credentials; an explicit Tab close is
	// what discards them.
	if err := real.EndPreservingResume(); err != nil {
		t.Fatalf("EndPreservingResume: %v", err)
	}
	// The record the next launch needs is in the same store.
	if _, err := first.secrets.Get(ctx,
		appvault.ETSessionSecretRef(resumeKey, appvault.FieldETSessionKey)); err != nil {
		t.Fatalf("preserved ET credentials are not in the user-data store: %v", err)
	}
	first.shutdown()

	// Second app run: same user-data directory and keyring, fresh DB handle,
	// fresh runtime, and a new SSH server. The resume must not run
	// etterminal, which would be the key-entry/bootstrap round trip.
	var secondBootstraps atomic.Int32
	second := newPhoneTransportEnvAt(t, userData, keyRing, func() fakessh.Options {
		return fakessh.Options{
			HostKey:  signer,
			Password: phoneTestPassword,
			Exec: func(command string, channel xssh.Channel) int {
				secondBootstraps.Add(1)
				return 1
			},
			ForwardDial: func(host string, port int) (net.Conn, error) {
				return server.DialContext(context.Background(), "tcp",
					net.JoinHostPort(host, "2022"))
			},
		}
	})
	server.AnswerStart(ctx)
	handle2, err := second.runtime.OpenHostWithResume(ctx, host, resumeKey)
	if err != nil {
		t.Fatalf("reattach OpenHostWithResume: %v", err)
	}
	real2 := handle2.(*phoneSessionHandle)
	conn2, err := real2.Attach()
	if err != nil {
		t.Fatalf("Attach after relaunch: %v", err)
	}
	if err := server.SendText(ctx, "after the restart\r\n"); err != nil {
		t.Fatalf("send after: %v", err)
	}
	readPhoneUntil(t, conn2, "after the restart", 10*time.Second)
	if got := firstBootstraps.Load(); got != 1 {
		t.Fatalf("first run bootstraps = %d, want 1", got)
	}
	if got := secondBootstraps.Load(); got != 0 {
		t.Fatalf("the relaunch ran the etterminal bootstrap %d time(s), want a resume", got)
	}
	_ = conn2.Close()
	if err := real2.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	assertNoPlaintext(t, userData, testETPasskey)
}

// assertNoPlaintext walks every file under dir and fails if the secret appears
// verbatim. The ET id and passkey must stay inside the encrypted store.
func assertNoPlaintext(t *testing.T, dir, secret string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("%s contains ET key material in plaintext", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

// runPhoneLifecycleParity drives one Session through the same sequence and
// asserts the same outcomes. Every remote transport must attach, hibernate
// without disconnecting, replay what the remote produced while hidden, resume
// a usable view, and end.
func runPhoneLifecycleParity(t *testing.T, handle *phoneSessionHandle, banner, away string,
	sendAway func(string), input, echo string) {

	t.Helper()
	conn, err := handle.Attach()
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	readPhoneUntil(t, conn, banner, 10*time.Second)
	if err := conn.Close(); err != nil {
		t.Fatalf("close the view before hibernating: %v", err)
	}
	if !handle.Hibernate() {
		t.Fatal("Hibernate returned false for a running Session")
	}
	// The remote keeps producing output while the phone shows no view.
	sendAway(away)
	resumed, err := handle.Resume()
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	readPhoneUntil(t, resumed, away, 10*time.Second)
	if input != "" {
		if _, err := resumed.Write([]byte(input)); err != nil {
			t.Fatalf("write through the resumed view: %v", err)
		}
		if echo != "" {
			readPhoneUntil(t, resumed, echo, 10*time.Second)
		}
	}
	if err := resumed.Close(); err != nil {
		t.Fatalf("close the resumed view: %v", err)
	}
	if err := handle.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	awaitCondition(t, "the Session to end", 5*time.Second, func() bool {
		select {
		case <-handle.Done():
			return true
		default:
			return false
		}
	})
}

// TestPhoneSessionLifecycleIsTheSameForEveryRemoteTransport asserts the
// lifecycle per transport rather than inferring it from SSH: SSH, Telnet,
// Mosh, and EternalTerminal each hibernate, replay hidden output, resume, take
// input, and end with the same application handle.
func TestPhoneSessionLifecycleIsTheSameForEveryRemoteTransport(t *testing.T) {
	t.Run("ssh", func(t *testing.T) {
		signer := fixtureSigner(t)
		var mu sync.Mutex
		var channel xssh.Channel
		env := newPhoneTransportEnv(t, func() fakessh.Options {
			return fakessh.Options{
				HostKey:  signer,
				Password: phoneTestPassword,
				Shell: func(ch xssh.Channel) {
					mu.Lock()
					channel = ch
					mu.Unlock()
					_, _ = io.WriteString(ch, "ssh-ready\r\n")
					_, _ = io.Copy(ch, ch)
				},
			}
		})
		host := domainvault.Host{
			ID: "ssh", Label: "ssh box", Hostname: "127.0.0.1", Port: 2222,
			Username: "deploy", Protocol: domainvault.ProtocolSSH,
		}
		env.addHost(t, host, signer)
		handle := env.openHost(t, host)
		runPhoneLifecycleParity(t, handle, "ssh-ready", "ssh-away\r\n", func(text string) {
			mu.Lock()
			defer mu.Unlock()
			if channel == nil {
				t.Error("the SSH shell channel was never opened")
				return
			}
			_, _ = io.WriteString(channel, text)
		}, "ssh-input\r", "ssh-input")
	})

	t.Run("telnet", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		var mu sync.Mutex
		var service net.Conn
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			service = conn
			mu.Unlock()
			_, _ = io.WriteString(conn, "telnet-ready\r\n")
			_, _ = io.Copy(conn, conn)
		}()
		env := newPhoneTransportEnv(t, func() fakessh.Options { return fakessh.Options{} })
		host := domainvault.Host{
			ID: "telnet", Label: "telnet box",
			Hostname: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port,
			Protocol: domainvault.ProtocolTelnet,
		}
		env.addHost(t, host, nil)
		handle := env.openHost(t, host)
		runPhoneLifecycleParity(t, handle, "telnet-ready", "telnet-away\r\n", func(text string) {
			mu.Lock()
			defer mu.Unlock()
			if service == nil {
				t.Error("the Telnet service never accepted a connection")
				return
			}
			_, _ = io.WriteString(service, text)
		}, "telnet-input\r", "telnet-input")
	})

	t.Run("mosh", func(t *testing.T) {
		server := moshtest.New(t)
		relay := moshtest.NewProxy(t, server.Addr())
		var bootstraps atomic.Int32
		signer := fixtureSigner(t)
		env := newPhoneTransportEnv(t, func() fakessh.Options {
			return fakessh.Options{
				HostKey:  signer,
				Password: phoneTestPassword,
				Exec:     startMoshBootstrap(server, relay, &bootstraps),
			}
		})
		host := domainvault.Host{
			ID: "mosh", Label: "mosh box", Hostname: "127.0.0.1", Port: 2222,
			Username: "deploy", Protocol: domainvault.ProtocolMosh,
		}
		env.addHost(t, host, signer)
		handle := env.openHost(t, host)
		runPhoneLifecycleParity(t, handle, "fake$ ", "mosh-away\r\n", server.Send,
			"echo hi\r", "hi\r\n")
	})

	t.Run("et", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		server := ettest.New(testETID, testETPasskey)
		t.Cleanup(server.Close)
		server.AnswerStart(ctx)
		signer := fixtureSigner(t)
		env := newPhoneTransportEnv(t, func() fakessh.Options {
			return fakessh.Options{
				HostKey:  signer,
				Password: phoneTestPassword,
				Exec: func(command string, channel xssh.Channel) int {
					if !strings.Contains(command, "etterminal") {
						return 1
					}
					_, _ = io.WriteString(channel,
						"IDPASSKEY:"+testETID+"/"+testETPasskey+"\r\n")
					return 0
				},
				ForwardDial: func(host string, port int) (net.Conn, error) {
					return server.DialContext(context.Background(), "tcp",
						net.JoinHostPort(host, "2022"))
				},
			}
		})
		host := domainvault.Host{
			ID: "et", Label: "et box", Hostname: "127.0.0.1", Port: 2222,
			Username: "deploy", Protocol: domainvault.ProtocolET, ETPort: 2022,
		}
		env.addHost(t, host, signer)
		handle := env.openHost(t, host)
		if err := server.SendText(ctx, "et-ready\r\n"); err != nil {
			t.Fatalf("send ET banner: %v", err)
		}
		runPhoneLifecycleParity(t, handle, "et-ready", "et-away\r\n", func(text string) {
			if err := server.SendText(ctx, text); err != nil {
				t.Errorf("send ET output while hibernated: %v", err)
			}
		}, "et-input\r", "")
	})
}
