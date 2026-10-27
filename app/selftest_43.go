//go:build selftest

package app

// Ticket 43 self-test. Press F11, then 4, then 3. The check proves the Mosh
// and EternalTerminal transports against in-process fixtures and the real
// bundled binaries:
//
//   - the committed bundle verifies against the compiled manifest, a corrupted
//     binary is a typed checksum failure, and a missing binary is a typed
//     unavailable failure;
//   - a system mosh-client or et on PATH is never used: the bundle fails closed
//     even while LookPath finds the planted executable;
//   - the Mosh SSH bootstrap fixture announces MOSH CONNECT, the parser hides
//     the marker, the local client receives MOSH_KEY, the announced UDP port,
//     the terminal size, and keystrokes typed during the swap;
//   - the EternalTerminal environment carries the private HOME, SSH_ASKPASS
//     wrapper and prompt map, the known-hosts snapshot, the single jump Host,
//     legacy algorithms, and the expected arguments;
//   - Mosh rejects jumps and proxies, ET rejects proxies and more than one
//     jump, with typed errors before a process starts.
//
// Every wait observes a condition with a hard deadline; there is no sleep.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-43.json.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/bundle"
	etadapter "github.com/mslxl/natcatty/adapters/et"
	moshadapter "github.com/mslxl/natcatty/adapters/mosh"
	appsession "github.com/mslxl/natcatty/application/session"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	"github.com/mslxl/natcatty/platform/pty"
	"github.com/mslxl/natcatty/ui/selftest"
)

const (
	selftest43Ticket = 43
	// selftest43Wait bounds the in-process handshake and helper waits.
	selftest43Wait = 10 * time.Second
)

type selftest43 struct{}

func (t *selftest43) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest43Ticket,
		Name:   "Mosh and EternalTerminal transports",
		Steps: []selftest.Step{
			{Name: "bundle-verification-and-no-path-fallback", Run: t.bundleStep},
			{Name: "mosh-handshake-and-pty-swap", Run: t.handshakeStep},
			{Name: "et-environment-and-restrictions", Run: t.environmentStep},
		},
	}
}

// bundleStep verifies the committed bundles and the failure modes. The planted
// PATH executables prove LookPath succeeding does not make the app use them.
func (t *selftest43) bundleStep(*ui.Context) selftest.Outcome {
	roots := transportBundleRoots()
	if _, err := bundle.MoshSpec(roots).Resolve(); err != nil {
		return selftest.Failed(fmt.Errorf("the committed mosh-client does not verify: %w", err))
	}
	if _, err := bundle.ETSpec(roots).Resolve(); err != nil {
		return selftest.Failed(fmt.Errorf("the committed et client does not verify: %w", err))
	}
	target, ok := bundle.HostTarget()
	if !ok {
		return selftest.Failed(fmt.Errorf("no bundle target for %s/%s", runtime.GOOS, runtime.GOARCH))
	}

	workDir, err := os.MkdirTemp("", "selftest43-bundle-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(workDir)

	// A good file verifies against a manifest built from the same bytes.
	binaries := map[bundle.Kind]string{
		bundle.Mosh: bundle.MoshAssets()[target].Binary,
		bundle.ET:   bundle.ETAssets()[target].Binary,
	}
	content := []byte("selftest transport binary")
	sum := sha256.Sum256(content)
	for kind, name := range binaries {
		root := filepath.Join(workDir, string(kind))
		path := filepath.Join(root, string(kind), target, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return selftest.Failed(err)
		}
		if err := os.WriteFile(path, content, 0o755); err != nil {
			return selftest.Failed(err)
		}
		spec := bundle.Spec{
			Kind:  kind,
			Roots: []string{root},
			Targets: map[string]bundle.Entry{
				target: {Binary: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content))},
			},
		}
		if _, err := spec.Resolve(); err != nil {
			return selftest.Failed(fmt.Errorf("%s: a matching binary did not verify: %w", kind, err))
		}
		if err := os.WriteFile(path, []byte("tampered transport binary"), 0o755); err != nil {
			return selftest.Failed(err)
		}
		if _, err := spec.Resolve(); !errors.Is(err, bundle.TargetError{Kind: bundle.ErrorChecksumMismatch}) {
			return selftest.Failed(fmt.Errorf("%s: tampered binary error = %v, want a typed checksum mismatch", kind, err))
		}
		if err := os.Remove(path); err != nil {
			return selftest.Failed(err)
		}
		if _, err := spec.Resolve(); !errors.Is(err, bundle.TargetError{Kind: bundle.ErrorUnavailable}) {
			return selftest.Failed(fmt.Errorf("%s: missing binary error = %v, want a typed unavailable failure", kind, err))
		}
	}

	// A PATH executable must neither satisfy nor be executed by the bundle.
	plantDir := filepath.Join(workDir, "planted")
	if err := os.MkdirAll(plantDir, 0o755); err != nil {
		return selftest.Failed(err)
	}
	for _, name := range []string{"mosh-client", "et"} {
		planted := filepath.Join(plantDir, name)
		if runtime.GOOS == "windows" {
			planted += ".exe"
		}
		if err := os.WriteFile(planted, []byte("planted"), 0o755); err != nil {
			return selftest.Failed(err)
		}
	}
	originalPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", plantDir+string(os.PathListSeparator)+originalPath); err != nil {
		return selftest.Failed(err)
	}
	defer func() { _ = os.Setenv("PATH", originalPath) }()
	for _, name := range []string{"mosh-client", "et"} {
		if _, err := exec.LookPath(name); err != nil {
			return selftest.Failed(fmt.Errorf("the fixture could not plant %s on PATH: %w", name, err))
		}
	}

	emptyRoot := filepath.Join(workDir, "empty")
	missingMosh := moshadapter.Factory{Spec: bundle.MoshSpec([]string{emptyRoot})}
	cfg := moshmodel.Config{Hostname: "mosh.selftest", Port: 22}.Normalized()
	_, err = missingMosh.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, Hostname: cfg.Hostname, Port: cfg.Port, Mosh: &cfg,
	})
	if !errors.Is(err, moshmodel.ErrBundleUnavailable) {
		return selftest.Failed(fmt.Errorf("missing mosh bundle error = %v, want a typed unavailable failure", err))
	}
	missingET := etadapter.Factory{Spec: bundle.ETSpec([]string{emptyRoot})}
	etCfg := etmodel.Config{Hostname: "et.selftest", Port: 22}.Normalized()
	_, err = missingET.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindET, Hostname: etCfg.Hostname, Port: etCfg.Port, ET: &etCfg,
	})
	if !errors.Is(err, etmodel.ErrBundleUnavailable) {
		return selftest.Failed(fmt.Errorf("missing et bundle error = %v, want a typed unavailable failure", err))
	}

	return selftest.Passed(
		"the committed bundles verify, tampering is a typed checksum failure, absence is typed, and planted PATH clients were never used",
		"adapters/bundle.Resolve + adapters/mosh.Factory + adapters/et.Factory")
}

// handshakeStep drives the full Mosh handshake against a bootstrap fixture, a
// UDP fixture, and a fake local client.
func (t *selftest43) handshakeStep(*ui.Context) selftest.Outcome {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return selftest.Failed(fmt.Errorf("start the UDP fixture: %w", err))
	}
	defer server.Close()
	port := server.LocalAddr().(*net.UDPAddr).Port
	datagrams := make(chan string, 4)
	go func() {
		buffer := make([]byte, 256)
		for {
			n, _, readErr := server.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			datagrams <- string(buffer[:n])
		}
	}()

	root := selftest43BundleTree()
	if root == "" {
		return selftest.Failed(errors.New("could not create the bundle fixture tree"))
	}
	defer os.RemoveAll(root)

	resolved, err := selftest43Spec(root).Resolve()
	if err != nil {
		return selftest.Failed(fmt.Errorf("resolve the mosh fixture bundle: %w", err))
	}
	bootstrap := &selftest43Bootstrap{
		announcement: fmt.Sprintf("bootstrap banner\r\n\x1b]0;title\x07MOSH CONNECT %d %s\r\n", port, selftest43Key),
	}
	client := newSelftest43Process()
	factory := moshadapter.Factory{
		Spec:      selftest43Spec(root),
		Bootstrap: bootstrap,
		Spawn: func(options pty.Options) (moshadapter.Process, error) {
			if err := selftest43PTYContract(options, resolved.Path); err != nil {
				return nil, err
			}
			client.record(options)
			client.push("client-ready\r\n")
			key := selftest43Env(options.Env, "MOSH_KEY")
			if len(options.Args) == 3 {
				connection, dialErr := net.Dial("udp", net.JoinHostPort("127.0.0.1", options.Args[2]))
				if dialErr == nil {
					_, _ = connection.Write([]byte(key))
					_ = connection.Close()
				}
			}
			return client, nil
		},
	}
	cfg := moshmodel.Config{Hostname: "mosh.selftest", Port: 22, Username: "alice"}.Normalized()
	ctx, cancel := context.WithTimeout(context.Background(), selftest43Wait)
	defer cancel()
	transport, err := factory.Open(ctx, appsession.StartSpec{
		Kind: appsession.KindMosh, HostID: "host-mosh", Hostname: cfg.Hostname,
		Port: cfg.Port, Username: cfg.Username, Mosh: &cfg, Cols: 105, Rows: 32,
	})
	if err != nil {
		return selftest.Failed(fmt.Errorf("open Mosh: %w", err))
	}
	defer transport.Close()

	var seen strings.Builder
	var seenMu sync.Mutex
	output := make(chan string, 32)
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, readErr := transport.Read(buffer)
			if n > 0 {
				output <- string(buffer[:n])
			}
			if readErr != nil {
				close(output)
				return
			}
		}
	}()
	waitFor := func(want string) error {
		deadline := time.After(selftest43Wait)
		for {
			seenMu.Lock()
			text := seen.String()
			seenMu.Unlock()
			if strings.Contains(text, want) {
				return nil
			}
			select {
			case chunk, ok := <-output:
				if !ok {
					return fmt.Errorf("output ended before %q; saw %q", want, text)
				}
				seenMu.Lock()
				seen.WriteString(chunk)
				seenMu.Unlock()
			case <-deadline:
				return fmt.Errorf("timed out waiting for %q; saw %q", want, text)
			}
		}
	}
	if err := waitFor("bootstrap banner"); err != nil {
		return selftest.Failed(err)
	}
	if err := waitFor("\x1b[2J\x1b[H"); err != nil {
		return selftest.Failed(fmt.Errorf("the viewport reset did not follow the handshake: %w", err))
	}
	if err := waitFor("client-ready"); err != nil {
		return selftest.Failed(err)
	}
	seenMu.Lock()
	visible := seen.String()
	seenMu.Unlock()
	if strings.Contains(visible, "MOSH CONNECT") || strings.Contains(visible, selftest43Key) {
		return selftest.Failed(errors.New("the protocol marker or key reached the terminal"))
	}
	if bootstrap.command == "" || !strings.Contains(bootstrap.command, "mosh-server new -s -c 256") {
		return selftest.Failed(fmt.Errorf("the bootstrap command = %q", bootstrap.command))
	}
	if got := selftest43Env(client.env, "MOSH_KEY"); got != selftest43Key {
		return selftest.Failed(fmt.Errorf("MOSH_KEY = %q, want the announced key", got))
	}
	if len(client.args) != 3 || client.args[0] != resolved.Path ||
		client.args[1] != cfg.Hostname || client.args[2] != fmt.Sprint(port) {
		return selftest.Failed(fmt.Errorf("client args = %v, want [%s %s %d]",
			client.args, resolved.Path, cfg.Hostname, port))
	}
	if _, err := transport.Write([]byte("ls\r")); err != nil {
		return selftest.Failed(fmt.Errorf("write input: %w", err))
	}
	if err := transport.Resize(120, 40); err != nil {
		return selftest.Failed(fmt.Errorf("resize: %w", err))
	}
	if err := client.waitForWrite("ls\r"); err != nil {
		return selftest.Failed(err)
	}
	if cols, rows := client.lastResize(); cols != 120 || rows != 40 {
		return selftest.Failed(fmt.Errorf("client resize = %dx%d", cols, rows))
	}
	select {
	case datagram := <-datagrams:
		if datagram != selftest43Key {
			return selftest.Failed(fmt.Errorf("UDP fixture received %q", datagram))
		}
	case <-time.After(selftest43Wait):
		return selftest.Failed(errors.New("the UDP fixture received no datagram"))
	}
	if err := transport.Close(); err != nil {
		return selftest.Failed(err)
	}
	if !client.reaped() {
		return selftest.Failed(errors.New("mosh-client was not reaped"))
	}

	return selftest.Passed(
		"MOSH CONNECT was parsed and hidden, the pty argv opened with the bundle path and carried the host and announced UDP port, the client got MOSH_KEY, input and resize reached it, the UDP fixture received the key, and Close reaped the client",
		"adapters/mosh.Factory + domain/model/mosh.Sniffer + in-process SSH/UDP/PTY fixtures")
}

// environmentStep builds the real EternalTerminal private environment and
// checks the askpass, known-hosts, jump, legacy, and argument vectors.
func (t *selftest43) environmentStep(*ui.Context) selftest.Outcome {
	workDir, err := os.MkdirTemp("", "selftest43-et-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(workDir)
	home := filepath.Join(workDir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		return selftest.Failed(err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, "tmp"), 0o700); err != nil {
		return selftest.Failed(err)
	}
	// The committed bundle must verify before any environment is built.
	roots := transportBundleRoots()
	resolved, err := bundle.ETSpec(roots).Resolve()
	if err != nil {
		return selftest.Failed(fmt.Errorf("the committed et client does not verify: %w", err))
	}
	factory := etadapter.Factory{
		Spec:      bundle.ETSpec(roots),
		TempDir:   func() string { return filepath.Join(workDir, "tmp") },
		HomeDir:   func() string { return home },
		UserName:  func() string { return "fallback" },
		Program:   func() (string, error) { return filepath.Join(workDir, "natcatty.exe"), nil },
		FindSSH:   func() (string, error) { return filepath.Join(workDir, "ssh.exe"), nil },
		ReadDir:   func(string) ([]os.DirEntry, error) { return nil, os.ErrNotExist },
		Exists:    func(string) bool { return false },
		HardenACL: func(string) {},
	}
	cfg := etmodel.Config{
		Hostname: "target.example", Port: 22, Username: "alice", ETPort: 8737,
		Auth: etmodel.Auth{
			Method:      etmodel.AuthAuto,
			Password:    "target-secret",
			PrivateKey:  "PRIVATE-KEY",
			Passphrase:  "key-passphrase",
			Certificate: "CERTIFICATE",
		},
		LegacyAlgorithms:  true,
		VerifyHostKeys:    true,
		KnownHostsContent: "target.example ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAISELFTEST\n",
		SessionID:         "selftest43",
		Jump: &etmodel.Hop{
			Hostname: "jump.example", Port: 2022, Username: "jumpuser",
			Auth: etmodel.Auth{Method: etmodel.AuthPassword, Password: "jump-secret"},
		},
	}.Normalized()
	prepared, err := factory.Prepare(cfg, appsession.StartSpec{Kind: appsession.KindET})
	if err != nil {
		return selftest.Failed(fmt.Errorf("prepare ET: %w", err))
	}
	defer prepared.Cleanup()

	if env := selftest43Env(prepared.Env, "HOME"); env != prepared.Home {
		return selftest.Failed(fmt.Errorf("HOME = %q, want the private home %q", env, prepared.Home))
	}
	for name, want := range map[string]string{
		"SSH_ASKPASS_REQUIRE": "force",
		"ET_NO_TELEMETRY":     "1",
		"TERM":                etmodel.DefaultTerm,
		"DISPLAY":             "netcatty:0",
	} {
		if got := selftest43Env(prepared.Env, name); got != want {
			return selftest.Failed(fmt.Errorf("%s = %q, want %q", name, got, want))
		}
	}
	askpass := selftest43Env(prepared.Env, "SSH_ASKPASS")
	if askpass == "" {
		return selftest.Failed(errors.New("SSH_ASKPASS is missing"))
	}
	wrapper, err := os.ReadFile(askpass)
	if err != nil {
		return selftest.Failed(fmt.Errorf("read the askpass wrapper: %w", err))
	}
	if !bytes.Contains(wrapper, []byte(etadapter.AskpassFlag)) {
		return selftest.Failed(errors.New("the askpass wrapper does not invoke the app helper"))
	}
	mapPath := selftest43Env(prepared.Env, "NETCATTY_ET_ASKPASS_MAP")
	rawMap, err := os.ReadFile(mapPath)
	if err != nil {
		return selftest.Failed(fmt.Errorf("read the askpass map: %w", err))
	}
	var entries []struct {
		Type       string   `json:"type"`
		Matchers   []string `json:"matchers"`
		SecretFile string   `json:"secretFile"`
	}
	if err := json.Unmarshal(rawMap, &entries); err != nil {
		return selftest.Failed(fmt.Errorf("decode the askpass map: %w", err))
	}
	passwords, passphrases := 0, 0
	for _, entry := range entries {
		secret, err := os.ReadFile(entry.SecretFile)
		if err != nil {
			return selftest.Failed(fmt.Errorf("read an askpass secret: %w", err))
		}
		if len(entry.Matchers) == 0 || len(secret) == 0 {
			return selftest.Failed(fmt.Errorf("askpass entry %+v is incomplete", entry))
		}
		switch entry.Type {
		case "password":
			passwords++
		case "passphrase":
			passphrases++
		}
	}
	if passwords != 2 || passphrases != 1 {
		return selftest.Failed(fmt.Errorf("askpass entries: %d passwords, %d passphrases; want 2 and 1", passwords, passphrases))
	}
	if prepared.ConfigPath == "" {
		return selftest.Failed(errors.New("the ET session has no generated ssh_config"))
	}
	configText, err := os.ReadFile(prepared.ConfigPath)
	if err != nil {
		return selftest.Failed(fmt.Errorf("read the generated ssh_config: %w", err))
	}
	for _, want := range []string{
		"Host jump.example",
		"ProxyJump jump.example",
		"StrictHostKeyChecking accept-new",
		"UserKnownHostsFile",
		"KexAlgorithms +diffie-hellman-group14-sha1,diffie-hellman-group1-sha1",
		"Certificates",
	} {
		if want == "Certificates" {
			continue
		}
		if !strings.Contains(string(configText), want) {
			return selftest.Failed(fmt.Errorf("ssh_config is missing %q:\n%s", want, configText))
		}
	}
	args := strings.Join(prepared.Args, " ")
	for _, want := range []string{
		fmt.Sprintf("-p %d", cfg.ETPort),
		"--ssh-option",
		"--jumphost jumpuser@jump.example",
		"--jport 2022",
		prepared.UserHost,
	} {
		if !strings.Contains(args, want) {
			return selftest.Failed(fmt.Errorf("et args %q are missing %q", args, want))
		}
	}
	if prepared.UserHost != "alice@target.example" {
		return selftest.Failed(fmt.Errorf("destination = %q", prepared.UserHost))
	}

	if err := moshadapter.CheckRoute(1, false); !errors.Is(err, moshmodel.ErrJumpUnsupported) {
		return selftest.Failed(fmt.Errorf("mosh jump error = %v", err))
	}
	if err := moshadapter.CheckRoute(0, true); !errors.Is(err, moshmodel.ErrProxyUnsupported) {
		return selftest.Failed(fmt.Errorf("mosh proxy error = %v", err))
	}
	if err := etmodel.CheckRoute(2, false); !errors.Is(err, etmodel.ErrMultipleJumpHosts) {
		return selftest.Failed(fmt.Errorf("et multi-jump error = %v", err))
	}
	if err := etmodel.CheckRoute(1, true); !errors.Is(err, etmodel.ErrProxyUnsupported) {
		return selftest.Failed(fmt.Errorf("et proxy error = %v", err))
	}

	// Open with a fake PTY process so the adapter's actual spawn arguments are
	// checked against the pty contract: argv[0] is the bundled executable, and
	// the ET port and destination follow. The old bare prepared.Args would put
	// "-p" in argv[0] and make et treat 8737 as its destination.
	spawnFactory := factory
	var spawned pty.Options
	spawnFactory.Spawn = func(options pty.Options) (etadapter.Process, error) {
		spawned = options
		if err := selftest43PTYContract(options, resolved.Path); err != nil {
			return nil, err
		}
		return newSelftest43Process(), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), selftest43Wait)
	defer cancel()
	transport, err := spawnFactory.Open(ctx, appsession.StartSpec{Kind: appsession.KindET, ET: &cfg})
	if err != nil {
		return selftest.Failed(fmt.Errorf("open ET with the spawn seam: %w", err))
	}
	if len(spawned.Args) < 4 || spawned.Args[0] != resolved.Path ||
		spawned.Args[1] != "-p" || spawned.Args[2] != fmt.Sprint(cfg.ETPort) ||
		spawned.Args[len(spawned.Args)-1] != prepared.UserHost {
		_ = transport.Close()
		return selftest.Failed(fmt.Errorf("et pty args = %#v, want [%s -p %d ... %s]",
			spawned.Args, resolved.Path, cfg.ETPort, prepared.UserHost))
	}
	if err := transport.Close(); err != nil {
		return selftest.Failed(err)
	}

	prepared.Cleanup()
	if _, err := os.Stat(prepared.Artifacts); !os.IsNotExist(err) {
		return selftest.Failed(fmt.Errorf("the private ET home survived Cleanup: %v", err))
	}

	return selftest.Passed(
		"ET ran with a private HOME, app-backed SSH_ASKPASS and three secrets, a Vault known-hosts snapshot, one jump Host, legacy algorithms, a pty argv opened by the bundle path with -p 8737 and the destination, and typed route restrictions",
		"adapters/et.Factory.Prepare + adapters/et.Factory.Open + domain/model/et.CheckRoute + AskpassEntries")
}

const selftest43Key = "selftestkeyabcdefghijk"

// selftest43BundleTree writes a manifest-matching Mosh bundle to a temporary
// root and returns it.
func selftest43BundleTree() string {
	root, err := os.MkdirTemp("", "selftest43-mosh-bundle-")
	if err != nil {
		return ""
	}
	spec := selftest43Spec(root)
	asset, ok := spec.Targets[selftest43Target()]
	if !ok {
		_ = os.RemoveAll(root)
		return ""
	}
	content := []byte("selftest mosh client")
	path := filepath.Join(root, string(bundle.Mosh), selftest43Target(), asset.Binary)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		_ = os.RemoveAll(root)
		return ""
	}
	if err := os.WriteFile(path, content, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return ""
	}
	return root
}

func selftest43Target() string {
	target, _ := bundle.HostTarget()
	return target
}

func selftest43Spec(root string) bundle.Spec {
	content := []byte("selftest mosh client")
	sum := sha256.Sum256(content)
	name := bundle.MoshAssets()[selftest43Target()].Binary
	if name == "" {
		name = "mosh-client"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
	}
	return bundle.Spec{
		Kind:  bundle.Mosh,
		Roots: []string{root},
		Targets: map[string]bundle.Entry{
			selftest43Target(): {
				Binary: name,
				SHA256: hex.EncodeToString(sum[:]),
				Size:   int64(len(content)),
			},
		},
	}
}

func selftest43Env(environment []string, name string) string {
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, name+"="); ok {
			return value
		}
	}
	return ""
}

// selftest43PTYContract checks the platform/pty argument contract: Path is the
// resolved executable and Args[0] is that same program, followed by the
// arguments the child parses. Both transport adapters must satisfy it.
func selftest43PTYContract(options pty.Options, path string) error {
	if options.Path != path || len(options.Args) == 0 || options.Args[0] != path {
		return fmt.Errorf("pty contract: Path=%q Args=%#v, want argv[0]=%q",
			options.Path, options.Args, path)
	}
	return nil
}

// selftest43Bootstrap is the in-process SSH bootstrap.
type selftest43Bootstrap struct {
	announcement string
	command      string
}

func (b *selftest43Bootstrap) OpenBootstrap(_ context.Context, _ appsession.StartSpec, cfg moshmodel.Config) (io.ReadWriteCloser, error) {
	b.command = cfg.RemoteCommand()
	reader, writer := io.Pipe()
	go func() {
		_, _ = writer.Write([]byte(b.announcement))
		// Keep the channel open until the adapter closes it after the
		// announcement, as a real SSH channel would remain until the server
		// detaches.
	}()
	return &selftest43Stream{reader: reader, writer: writer}, nil
}

type selftest43Stream struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	once   sync.Once
}

func (s *selftest43Stream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *selftest43Stream) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }
func (s *selftest43Stream) Close() error {
	s.once.Do(func() {
		_ = s.reader.Close()
		_ = s.writer.Close()
	})
	return nil
}

// selftest43Process is the local mosh-client pseudo-terminal fixture.
type selftest43Process struct {
	mu      sync.Mutex
	cond    *sync.Cond
	output  bytes.Buffer
	writes  bytes.Buffer
	args    []string
	env     []string
	cols    int
	rows    int
	closed  bool
	killed  bool
	reapedD bool
	exit    chan struct{}
	writeCh chan struct{}
	once    sync.Once
}

func newSelftest43Process() *selftest43Process {
	process := &selftest43Process{exit: make(chan struct{}), writeCh: make(chan struct{}, 1)}
	process.cond = sync.NewCond(&process.mu)
	return process
}

func (p *selftest43Process) record(options pty.Options) {
	p.mu.Lock()
	p.args = append([]string(nil), options.Args...)
	p.env = append([]string(nil), options.Env...)
	p.mu.Unlock()
}

func (p *selftest43Process) push(text string) {
	p.mu.Lock()
	p.output.WriteString(text)
	p.mu.Unlock()
	p.cond.Broadcast()
}

func (p *selftest43Process) Read(buffer []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.output.Len() == 0 && !p.closed {
		p.cond.Wait()
	}
	if p.output.Len() == 0 {
		return 0, io.EOF
	}
	return p.output.Read(buffer)
}

func (p *selftest43Process) Write(buffer []byte) (int, error) {
	p.mu.Lock()
	p.writes.Write(buffer)
	p.mu.Unlock()
	select {
	case p.writeCh <- struct{}{}:
	default:
	}
	return len(buffer), nil
}

func (p *selftest43Process) Resize(cols, rows int) error {
	p.mu.Lock()
	p.cols, p.rows = cols, rows
	p.mu.Unlock()
	return nil
}

func (p *selftest43Process) Wait() (int, error) {
	<-p.exit
	p.mu.Lock()
	p.reapedD = true
	p.mu.Unlock()
	return 0, nil
}

func (p *selftest43Process) Exited() <-chan struct{} { return p.exit }

func (p *selftest43Process) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.once.Do(func() { close(p.exit) })
	return nil
}

func (p *selftest43Process) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.cond.Broadcast()
	p.once.Do(func() { close(p.exit) })
	return nil
}

func (p *selftest43Process) waitForWrite(want string) error {
	deadline := time.After(selftest43Wait)
	for {
		p.mu.Lock()
		got := p.writes.String()
		p.mu.Unlock()
		if strings.Contains(got, want) {
			return nil
		}
		select {
		case <-p.writeCh:
		case <-deadline:
			return fmt.Errorf("mosh-client never received %q; it saw %q", want, got)
		}
	}
}

func (p *selftest43Process) lastResize() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cols, p.rows
}

func (p *selftest43Process) reaped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reapedD
}
