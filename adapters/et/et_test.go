package et

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/bundle"
	appsession "github.com/mslxl/natcatty/application/session"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	"github.com/mslxl/natcatty/platform/pty"
)

type preparedEnv struct {
	home       string
	tempParent string
	binaryPath string
	spec       bundle.Spec
	factory    Factory
	aclCalls   int
	aclMu      sync.Mutex
}

func newEnv(t *testing.T) *preparedEnv {
	t.Helper()
	target, ok := bundle.HostTarget()
	if !ok {
		t.Skip("no bundle target for this platform")
	}
	root := t.TempDir()
	content := []byte("fake et")
	dir := filepath.Join(root, "et", target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "et"
	if runtime.GOOS == "windows" {
		name = "et.exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	spec := bundle.Spec{
		Kind:  bundle.ET,
		Roots: []string{root},
		Targets: map[string]bundle.Entry{
			target: {Binary: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content))},
		},
	}
	env := &preparedEnv{
		home:       t.TempDir(),
		tempParent: t.TempDir(),
		binaryPath: path,
		spec:       spec,
	}
	env.factory = Factory{
		Spec:      spec,
		BaseEnv:   func() []string { return []string{"PATH=/usr/bin:/bin", "LANG=en_US.UTF-8"} },
		TempDir:   func() string { return env.tempParent },
		HomeDir:   func() string { return env.home },
		UserName:  func() string { return "alice" },
		Program:   func() (string, error) { return `C:\Program Files\Natcatty\natcatty.exe`, nil },
		FindSSH:   func() (string, error) { return `C:\Windows\System32\OpenSSH\ssh.exe`, nil },
		ReadDir:   os.ReadDir,
		Exists:    func(string) bool { return false },
		HardenACL: func(string) { env.aclMu.Lock(); env.aclCalls++; env.aclMu.Unlock() },
	}
	return env
}

func baseConfig() etmodel.Config {
	return etmodel.Config{
		Hostname:       "example.test",
		Port:           2222,
		Username:       "alice",
		ETPort:         2022,
		Auth:           etmodel.Auth{Method: etmodel.AuthPassword, Password: "s3cret"},
		VerifyHostKeys: true,
		SessionID:      "session/1",
		Cols:           100,
		Rows:           40,
	}.Normalized()
}

func startSpec() appsession.StartSpec {
	return appsession.StartSpec{Kind: appsession.KindET, Cols: 100, Rows: 40}
}

func optionsOf(args []string) []string {
	var options []string
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--ssh-option" {
			options = append(options, args[index+1])
		}
	}
	return options
}

func hasOption(args []string, wanted string) bool {
	for _, option := range optionsOf(args) {
		if option == wanted {
			return true
		}
	}
	return false
}

func envEntry(environment []string, name string) string {
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, name+"="); ok {
			return value
		}
	}
	return ""
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func mapEntries(t *testing.T, path string) []struct {
	Type       string   `json:"type"`
	Matchers   []string `json:"matchers"`
	SecretFile string   `json:"secretFile"`
} {
	t.Helper()
	var entries []struct {
		Type       string   `json:"type"`
		Matchers   []string `json:"matchers"`
		SecretFile string   `json:"secretFile"`
	}
	if err := json.Unmarshal([]byte(readFile(t, path)), &entries); err != nil {
		t.Fatalf("decode askpass map: %v", err)
	}
	return entries
}

func TestPreparePasswordAuth(t *testing.T) {
	env := newEnv(t)
	cfg := baseConfig()
	prepared, err := env.factory.Prepare(cfg, startSpec())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer prepared.Cleanup()

	if !filepath.IsAbs(prepared.Artifacts) || !strings.Contains(filepath.Base(prepared.Artifacts), "et-ssh-home-session_1") {
		t.Fatalf("artifacts = %q", prepared.Artifacts)
	}
	if prepared.ConfigPath == "" {
		t.Fatal("no config file for password auth")
	}
	config := readFile(t, prepared.ConfigPath)
	for _, want := range []string{
		"IdentityAgent none",
		"PreferredAuthentications password,keyboard-interactive",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(config, "Include") {
		t.Fatalf("included a config that does not exist:\n%s", config)
	}

	for _, wanted := range []string{
		"StrictHostKeyChecking=accept-new",
		"LogLevel=ERROR",
		"Port=2222",
		"PubkeyAuthentication=no",
		"KbdInteractiveAuthentication=yes",
		"NumberOfPasswordPrompts=1",
	} {
		if !hasOption(prepared.Args, wanted) {
			t.Fatalf("args missing %q: %#v", wanted, prepared.Args)
		}
	}
	if !strings.HasPrefix(prepared.Args[len(prepared.Args)-1], "alice@example.test") {
		t.Fatalf("destination missing: %#v", prepared.Args)
	}

	if envEntry(prepared.Env, "HOME") != prepared.Home ||
		envEntry(prepared.Env, "USERPROFILE") != prepared.Home {
		t.Fatalf("HOME/USERPROFILE not redirected: %#v", prepared.Env)
	}
	if envEntry(prepared.Env, "ET_NO_TELEMETRY") != "1" || envEntry(prepared.Env, "TERM") != "xterm-256color" {
		t.Fatalf("et environment missing: %#v", prepared.Env)
	}
	if envEntry(prepared.Env, "SSH_ASKPASS_REQUIRE") != "force" {
		t.Fatalf("SSH_ASKPASS_REQUIRE = %q", envEntry(prepared.Env, "SSH_ASKPASS_REQUIRE"))
	}
	if envEntry(prepared.Env, "DISPLAY") != "netcatty:0" {
		t.Fatalf("DISPLAY = %q", envEntry(prepared.Env, "DISPLAY"))
	}
	askpassPath := envEntry(prepared.Env, "SSH_ASKPASS")
	if askpassPath == "" || !strings.Contains(filepath.Base(askpassPath), "netcatty-et-askpass") {
		t.Fatalf("SSH_ASKPASS = %q", askpassPath)
	}
	if wrapper := readFile(t, askpassPath); !strings.Contains(wrapper, AskpassFlag) {
		t.Fatalf("askpass wrapper = %q", wrapper)
	}
	if path := envEntry(prepared.Env, "PATH"); !strings.Contains(path, filepath.Join(prepared.Home, ".ssh", "bin")) {
		t.Fatalf("PATH lacks the ssh wrapper: %q", path)
	}

	mapPath := envEntry(prepared.Env, "NETCATTY_ET_ASKPASS_MAP")
	entries := mapEntries(t, mapPath)
	if len(entries) != 1 || entries[0].Type != "password" || readFile(t, entries[0].SecretFile) != "s3cret\n" {
		t.Fatalf("askpass entries = %#v", entries)
	}
	foundMatcher := false
	for _, matcher := range entries[0].Matchers {
		if matcher == "alice@example.test's password" {
			foundMatcher = true
		}
	}
	if !foundMatcher {
		t.Fatalf("matchers = %#v", entries[0].Matchers)
	}
	var answer bytes.Buffer
	if err := Askpass("alice@example.test's password: ", mapPath, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.String() != "s3cret\n" {
		t.Fatalf("askpass answer = %q", answer.String())
	}
	var none bytes.Buffer
	if err := Askpass("Verification code: ", mapPath, &none); err != nil {
		t.Fatal(err)
	}
	if none.String() != "" {
		t.Fatalf("a second factor was answered: %q", none.String())
	}

	configDir := filepath.Join(prepared.Home, ".ssh")
	wrapperPath := filepath.Join(configDir, "bin", "ssh.cmd")
	if runtime.GOOS != "windows" {
		wrapperPath = filepath.Join(configDir, "bin", "ssh")
	}
	wrapper := readFile(t, wrapperPath)
	if !strings.Contains(wrapper, "-F") || !strings.Contains(wrapper, prepared.ConfigPath) {
		t.Fatalf("ssh wrapper = %q", wrapper)
	}
	if env.aclCalls == 0 {
		t.Fatal("the ACL seam was not used")
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{entries[0].SecretFile, mapPath, prepared.ConfigPath} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("%s mode = %o", filepath.Base(path), info.Mode().Perm())
			}
		}
	}

	prepared.Cleanup()
	if _, err := os.Stat(prepared.Home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private home survived cleanup: %v", err)
	}
}

func TestPrepareKeyPassphraseCertificateAndAlgorithms(t *testing.T) {
	env := newEnv(t)
	cfg := baseConfig()
	cfg.Auth = etmodel.Auth{
		Method:            etmodel.AuthKey,
		PrivateKey:        "PRIVATE KEY",
		Passphrase:        "key-pass",
		Certificate:       "ssh-ed25519-cert AAAA",
		IdentityFilePaths: []string{"~/keys/custom"},
		UseSSHAgent:       true,
		IdentitiesOnly:    true,
		AgentSocket:       `/tmp/agent socket`,
	}
	cfg.LegacyAlgorithms = true
	prepared, err := env.factory.Prepare(cfg, startSpec())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer prepared.Cleanup()

	config := readFile(t, prepared.ConfigPath)
	for _, want := range []string{
		`IdentityAgent "/tmp/agent socket"`,
		"KexAlgorithms +diffie-hellman-group14-sha1,diffie-hellman-group1-sha1",
		"Ciphers +aes128-cbc,aes256-cbc,3des-cbc",
		"HostKeyAlgorithms +ssh-rsa,ssh-dss",
		"PubkeyAcceptedAlgorithms +ssh-rsa,ssh-dss",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
	if !hasOption(prepared.Args, "PreferredAuthentications=publickey") {
		t.Fatalf("preferred auth not a single value: %#v", optionsOf(prepared.Args))
	}
	if !hasOption(prepared.Args, "IdentitiesOnly=yes") {
		t.Fatalf("IdentitiesOnly missing: %#v", optionsOf(prepared.Args))
	}
	keyOption := ""
	for _, option := range optionsOf(prepared.Args) {
		if strings.HasPrefix(option, "IdentityFile=") && strings.HasSuffix(option, "-key") {
			keyOption = option
		}
		if strings.Contains(option, "custom.pub") {
			if !strings.Contains(option, "keys/custom.pub") {
				t.Fatalf("agent identity did not use the .pub selector: %q", option)
			}
		}
	}
	if keyOption == "" {
		t.Fatalf("private key option missing: %#v", optionsOf(prepared.Args))
	}
	keyPath := strings.TrimPrefix(strings.ReplaceAll(keyOption, "\\", "/"), "IdentityFile=")
	if readFile(t, keyPath) != "PRIVATE KEY" {
		t.Fatalf("key content = %q", readFile(t, keyPath))
	}
	certFound := false
	for _, option := range optionsOf(prepared.Args) {
		if strings.HasPrefix(option, "CertificateFile=") {
			certFound = true
		}
	}
	if !certFound {
		t.Fatalf("certificate option missing: %#v", optionsOf(prepared.Args))
	}

	mapPath := envEntry(prepared.Env, "NETCATTY_ET_ASKPASS_MAP")
	entries := mapEntries(t, mapPath)
	if len(entries) != 1 || entries[0].Type != "passphrase" {
		t.Fatalf("askpass entries = %#v", entries)
	}
	var answer bytes.Buffer
	if err := Askpass("Enter passphrase for key '"+keyPath+"': ", mapPath, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.String() != "key-pass\n" {
		t.Fatalf("passphrase answer = %q", answer.String())
	}
}

func TestPrepareJump(t *testing.T) {
	env := newEnv(t)
	cfg := baseConfig()
	cfg.KnownHostsContent = "example.test ssh-ed25519 AAAA-target\n"
	cfg.JumpKnownHostsContent = "jump.test ssh-ed25519 AAAA-jump\n"
	cfg.Jump = &etmodel.Hop{
		Hostname: "jump.test",
		Port:     2200,
		ETPort:   2023,
		Username: "bob",
		Auth:     etmodel.Auth{Method: etmodel.AuthPassword, Password: "jump-secret"},
	}
	cfg = cfg.Normalized()
	prepared, err := env.factory.Prepare(cfg, startSpec())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer prepared.Cleanup()

	joined := strings.Join(prepared.Args, " ")
	if !strings.Contains(joined, "--jumphost bob@jump.test --jport 2023") {
		t.Fatalf("jump args missing: %v", prepared.Args)
	}
	if strings.Count(joined, "--jumphost") != 1 {
		t.Fatalf("jump args duplicated: %v", prepared.Args)
	}
	config := readFile(t, prepared.ConfigPath)
	for _, want := range []string{
		"Host example.test",
		"  ProxyJump jump.test",
		"Host jump.test",
		"  User bob",
		"  Port 2200",
		"  PubkeyAuthentication=no",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
	targetSnapshot := filepath.Join(prepared.Home, ".ssh", "session_1-authoritative-known_hosts")
	jumpSnapshot := filepath.Join(prepared.Home, ".ssh", "session_1-authoritative-jump-known_hosts")
	if readFile(t, targetSnapshot) != cfg.KnownHostsContent {
		t.Fatalf("target snapshot = %q", readFile(t, targetSnapshot))
	}
	if readFile(t, jumpSnapshot) != cfg.JumpKnownHostsContent {
		t.Fatalf("jump snapshot = %q", readFile(t, jumpSnapshot))
	}
	if !hasOption(prepared.Args, "UserKnownHostsFile="+filepath.ToSlash(targetSnapshot)) {
		t.Fatalf("target known-hosts option missing: %#v", optionsOf(prepared.Args))
	}
	if !strings.Contains(config, filepath.ToSlash(jumpSnapshot)) {
		t.Fatalf("jump known-hosts path missing:\n%s", config)
	}

	mapPath := envEntry(prepared.Env, "NETCATTY_ET_ASKPASS_MAP")
	entries := mapEntries(t, mapPath)
	if len(entries) != 2 {
		t.Fatalf("askpass entries = %#v", entries)
	}
	var targetAnswer, jumpAnswer bytes.Buffer
	_ = Askpass("alice@example.test's password:", mapPath, &targetAnswer)
	_ = Askpass("bob@jump.test's password:", mapPath, &jumpAnswer)
	if targetAnswer.String() != "s3cret\n" || jumpAnswer.String() != "jump-secret\n" {
		t.Fatalf("answers = %q / %q", targetAnswer.String(), jumpAnswer.String())
	}
}

func TestPrepareKnownHostsPolicies(t *testing.T) {
	env := newEnv(t)
	cfg := baseConfig()

	cfg.VerifyHostKeys = false
	prepared, err := env.factory.Prepare(cfg, startSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !hasOption(prepared.Args, "StrictHostKeyChecking=no") {
		t.Fatalf("verify=false options = %#v", optionsOf(prepared.Args))
	}
	empty := ""
	for _, option := range optionsOf(prepared.Args) {
		if value, ok := strings.CutPrefix(option, "GlobalKnownHostsFile="); ok {
			empty = strings.ReplaceAll(value, "/", string(filepath.Separator))
		}
	}
	if empty == "" {
		t.Fatalf("no empty global known-hosts option: %#v", optionsOf(prepared.Args))
	}
	if info, err := os.Stat(empty); err != nil || info.Size() != 0 {
		t.Fatalf("empty known-hosts = %v %v", info, err)
	}
	prepared.Cleanup()

	cfg = baseConfig()
	cfg.KnownHostsContent = "example.test ssh-ed25519 AAAA\n"
	prepared, err = env.factory.Prepare(cfg, startSpec())
	if err != nil {
		t.Fatal(err)
	}
	if hasOption(prepared.Args, "StrictHostKeyChecking=no") {
		t.Fatal("verify=true used StrictHostKeyChecking=no")
	}
	if !hasOption(prepared.Args, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("options = %#v", optionsOf(prepared.Args))
	}
	prepared.Cleanup()

	cfg = baseConfig()
	cfg.KnownHostsPath = filepath.Join(env.home, ".ssh", "known_hosts")
	prepared, err = env.factory.Prepare(cfg, startSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !hasOption(prepared.Args, "UserKnownHostsFile="+filepath.ToSlash(cfg.KnownHostsPath)) {
		t.Fatalf("known-hosts path = %#v", optionsOf(prepared.Args))
	}
	prepared.Cleanup()
}

func TestPrepareIncludesRealConfig(t *testing.T) {
	env := newEnv(t)
	configPath := filepath.Join(env.home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("Host *\n  ServerAliveInterval 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env.factory.Exists = func(path string) bool { return path == configPath }
	cfg := baseConfig()
	prepared, err := env.factory.Prepare(cfg, startSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Cleanup()
	config := readFile(t, prepared.ConfigPath)
	unescaped := strings.ReplaceAll(config, `\\`, `\`)
	unescaped = filepath.ToSlash(unescaped)
	if !strings.Contains(config, "Match all") || !strings.Contains(config, "Include ") ||
		!strings.Contains(unescaped, filepath.ToSlash(configPath)) {
		t.Fatalf("include missing:\n%s", config)
	}
	if strings.Index(config, "Include") < strings.Index(config, "PreferredAuthentications") {
		t.Fatalf("Include must follow the session directives:\n%s", config)
	}
}

func TestOpenProcessLifecycle(t *testing.T) {
	env := newEnv(t)
	client := newFakeProcess()
	var spawned pty.Options
	env.factory.Spawn = func(options pty.Options) (Process, error) {
		spawned = options
		return client, nil
	}
	transport, err := env.factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindET, Cols: 90, Rows: 30,
		ET: ptr(baseConfig()),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if spawned.Path != env.binaryPath {
		t.Fatalf("spawn path = %q", spawned.Path)
	}
	if len(spawned.Args) == 0 || spawned.Args[0] != spawned.Path {
		t.Fatalf("spawn Args = %#v, want argv[0] = %q", spawned.Args, spawned.Path)
	}
	if !hasOption(spawned.Args, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("spawn args = %#v", spawned.Args)
	}
	if envEntry(spawned.Env, "ET_NO_TELEMETRY") != "1" {
		t.Fatalf("spawn env = %#v", spawned.Env)
	}
	if spawned.Cols != 90 || spawned.Rows != 30 {
		t.Fatalf("spawn size = %dx%d", spawned.Cols, spawned.Rows)
	}
	artifacts := ""
	for _, entry := range spawned.Env {
		if strings.HasPrefix(entry, "HOME=") {
			artifacts = strings.TrimPrefix(entry, "HOME=")
		}
	}
	if artifacts == "" {
		t.Fatal("no private HOME in the spawn environment")
	}

	client.emit("et output")
	expectRead(t, transport, "et output")
	if _, err := transport.Write([]byte("typing")); err != nil {
		t.Fatal(err)
	}
	if got := client.written(); got != "typing" {
		t.Fatalf("input = %q", got)
	}
	if err := transport.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if cols, rows := client.size(); cols != 120 || rows != 40 {
		t.Fatalf("resize = %dx%d", cols, rows)
	}

	client.exit(0)
	waiter := transport.(appsession.ExitWaiter)
	if code, _ := waiter.Wait(); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonExited {
		t.Fatalf("end = %+v", end)
	}
	if _, err := os.Stat(artifacts); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifacts survived exit: %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestClientArgvStartsWithBundledExecutable is the regression for ticket 43's
// real-binary failure: et read port 8737 as the destination because Args[0]
// was not the executable.
func TestClientArgvStartsWithBundledExecutable(t *testing.T) {
	env := newEnv(t)
	client := newFakeProcess()
	var spawned pty.Options
	env.factory.Spawn = func(options pty.Options) (Process, error) {
		spawned = options
		return client, nil
	}
	cfg := baseConfig()
	cfg.ETPort = 8737
	cfg = cfg.Normalized()
	transport, err := env.factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindET, ET: &cfg,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer transport.Close()
	if spawned.Path != env.binaryPath {
		t.Fatalf("spawn Path = %q, want %q", spawned.Path, env.binaryPath)
	}
	if len(spawned.Args) == 0 || spawned.Args[0] != env.binaryPath {
		t.Fatalf("spawn Args = %#v, want argv[0] = %q", spawned.Args, env.binaryPath)
	}
	joined := strings.Join(spawned.Args, " ")
	if !strings.Contains(joined, "-p 8737") || spawned.Args[len(spawned.Args)-1] != "alice@example.test" {
		t.Fatalf("spawn Args = %#v", spawned.Args)
	}
}

// bundledETSpec returns the committed Windows x64 bundle for a real-binary
// test, or skips when the artifact is not present in this checkout.
func bundledETSpec(t *testing.T) (bundle.Spec, string) {
	t.Helper()
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skipf("the committed et client is a Windows x64 binary; this host is %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "resources"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, string(bundle.ET), "win32-x64", "et.exe")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("the bundled et client is absent: %v", err)
	}
	return bundle.ETSpec([]string{root}), path
}

// TestBundledETClientUsesContractArgv runs the committed et client with the
// adapter's actual pty.Options against a closed local SSH port. Before the
// ticket-43 rework argv[0] was "-p", so et read 8737 as the destination and
// reported "8737:2022" instead of the Host's address and ET port.
func TestBundledETClientUsesContractArgv(t *testing.T) {
	spec, exe := bundledETSpec(t)

	// Reserve a local port, then close it: the ET client's SSH bootstrap must
	// fail fast and the transport must classify the non-zero exit as an error.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	var spawned pty.Options
	factory := Factory{
		Spec:      spec,
		BaseEnv:   os.Environ,
		TempDir:   func() string { return t.TempDir() },
		HomeDir:   func() string { return t.TempDir() },
		UserName:  func() string { return "" },
		ReadDir:   func(string) ([]os.DirEntry, error) { return nil, os.ErrNotExist },
		Exists:    func(string) bool { return false },
		HardenACL: func(string) {},
		Spawn: func(options pty.Options) (Process, error) {
			spawned = options
			terminal, startErr := pty.Start(options)
			if startErr != nil {
				return nil, startErr
			}
			return terminal, nil
		},
	}
	cfg := etmodel.Config{
		Hostname:       "127.0.0.1",
		Port:           closedPort,
		ETPort:         8737,
		Auth:           etmodel.Auth{Method: etmodel.AuthAuto},
		VerifyHostKeys: false,
		SessionID:      "real-binary-43",
		Cols:           100,
		Rows:           40,
	}.Normalized()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	transport, err := factory.Open(ctx, appsession.StartSpec{Kind: appsession.KindET, ET: &cfg})
	if err != nil {
		t.Fatalf("Open the bundled client: %v", err)
	}
	defer transport.Close()

	if spawned.Path != exe || len(spawned.Args) == 0 || spawned.Args[0] != exe {
		t.Fatalf("adapter pty argv = Path %q Args %#v, want argv[0] = %q", spawned.Path, spawned.Args, exe)
	}
	if len(spawned.Args) < 3 || spawned.Args[1] != "-p" || spawned.Args[2] != "8737" ||
		spawned.Args[len(spawned.Args)-1] != "127.0.0.1" {
		t.Fatalf("adapter pty args = %#v, want [%s -p 8737 ... 127.0.0.1]", spawned.Args, exe)
	}

	reader := newProcessCollector(transport)
	// Wait for the complete failure line so a partial read cannot split the
	// address. The line is ET's typed connection failure; the transport must
	// then classify the non-zero exit as EndReasonError, not a normal exit.
	want := "Could not reach the ET server: 127.0.0.1:8737"
	text := reader.waitFor(t, want)
	if strings.Contains(text, "8737:2022") {
		t.Fatalf("et consumed the ET port as its destination: %q", text)
	}
	waiter, ok := transport.(appsession.ExitWaiter)
	if !ok {
		t.Fatal("the ET transport does not report an exit status")
	}
	exited := make(chan int, 1)
	go func() {
		code, _ := waiter.Wait()
		exited <- code
	}()
	select {
	case code := <-exited:
		if code == 0 {
			t.Fatalf("et exited normally after a connection failure: %q", text)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("et did not exit after a connection failure: %q", text)
	}
	reporter, ok := transport.(appsession.EndReporter)
	if !ok {
		t.Fatal("the ET transport does not classify its end")
	}
	if end := reporter.End(); end.Reason != appsession.EndReasonError || !end.HasExitCode || end.ExitCode == 0 {
		t.Fatalf("transport end = %+v, want a typed error with a non-zero exit", end)
	}
	t.Logf("real et client output: %q", strings.TrimSpace(text))
}

// processCollector drains a transport so waits on its output see every chunk.
type processCollector struct {
	mu      sync.Mutex
	text    string
	err     error
	updated chan struct{}
}

func newProcessCollector(reader io.Reader) *processCollector {
	c := &processCollector{updated: make(chan struct{}, 1)}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := reader.Read(buffer)
			if n > 0 {
				c.mu.Lock()
				c.text += string(buffer[:n])
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

func (c *processCollector) signal() {
	select {
	case c.updated <- struct{}{}:
	default:
	}
}

func (c *processCollector) snapshot() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text, c.err
}

func (c *processCollector) waitFor(t *testing.T, want string) string {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		text, err := c.snapshot()
		if strings.Contains(text, want) {
			return text
		}
		if err != nil {
			t.Fatalf("et output ended before %q: %v; output = %q", want, err, text)
		}
		select {
		case <-c.updated:
		case <-deadline:
			t.Fatalf("timed out waiting for %q; output = %q", want, text)
		}
	}
}

func TestOpenCloseReapsAndCleans(t *testing.T) {
	env := newEnv(t)
	client := newFakeProcess()
	env.factory.Spawn = func(pty.Options) (Process, error) { return client, nil }
	transport, err := env.factory.Open(context.Background(), appsession.StartSpec{ET: ptr(baseConfig())})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if !client.isClosed() {
		t.Fatal("Close did not close the client")
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v", end)
	}
	entries, err := os.ReadDir(env.tempParent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp homes survived Close: %#v", entries)
	}
}

func TestBundleFailuresAreTypedAndNoPathFallback(t *testing.T) {
	env := newEnv(t)
	pathDir := t.TempDir()
	name := "et"
	if runtime.GOOS == "windows" {
		name = "et.exe"
	}
	pathBinary := filepath.Join(pathDir, name)
	if err := os.WriteFile(pathBinary, []byte("system et"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("fixture PATH binary not visible: %v", err)
	}

	originalRoots := append([]string(nil), env.factory.Spec.Roots...)
	env.factory.Spec.Roots = []string{t.TempDir()}
	spawned := 0
	env.factory.Spawn = func(pty.Options) (Process, error) { spawned++; return newFakeProcess(), nil }
	_, err := env.factory.Open(context.Background(), appsession.StartSpec{ET: ptr(baseConfig())})
	if !errors.Is(err, etmodel.ErrBundleUnavailable) {
		t.Fatalf("err = %v, want ErrBundleUnavailable", err)
	}
	if spawned != 0 {
		t.Fatal("spawned a client without a bundle")
	}
	if entries, _ := os.ReadDir(env.tempParent); len(entries) != 0 {
		t.Fatalf("prepared artifacts for a missing bundle: %#v", entries)
	}

	env.factory.Spec.Roots = originalRoots
	if err := os.WriteFile(env.binaryPath, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := env.factory.Open(context.Background(), appsession.StartSpec{ET: ptr(baseConfig())}); !errors.Is(err, etmodel.ErrChecksumMismatch) {
		t.Fatalf("checksum err = %v", err)
	}

	env.factory.Spec.Targets = map[string]bundle.Entry{}
	if _, err := env.factory.Open(context.Background(), appsession.StartSpec{ET: ptr(baseConfig())}); !errors.Is(err, etmodel.ErrPlatformUnsupported) {
		t.Fatalf("platform err = %v", err)
	}
}

func TestSpawnFailureCleansArtifacts(t *testing.T) {
	env := newEnv(t)
	env.factory.Spawn = func(pty.Options) (Process, error) { return nil, errors.New("denied") }
	_, err := env.factory.Open(context.Background(), appsession.StartSpec{ET: ptr(baseConfig())})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(env.tempParent); len(entries) != 0 {
		t.Fatalf("artifacts survived a spawn failure: %#v", entries)
	}
}

func TestAskpassUnreadableMapAndMissingSecret(t *testing.T) {
	var output bytes.Buffer
	if err := Askpass("Password:", filepath.Join(t.TempDir(), "missing.json"), &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "" {
		t.Fatalf("output = %q", output.String())
	}
	dir := t.TempDir()
	mapPath := filepath.Join(dir, "map.json")
	missing := filepath.Join(dir, "missing-secret")
	encoded, _ := json.Marshal([]map[string]any{{
		"type": "password", "matchers": []string{"host"}, "secretFile": missing,
	}})
	if err := os.WriteFile(mapPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Askpass("Password:", mapPath, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestIsAskpassInvocation(t *testing.T) {
	if !IsAskpassInvocation([]string{"natcatty.exe", AskpassFlag, "Password:"}) {
		t.Fatal("flag not detected")
	}
	if IsAskpassInvocation([]string{"natcatty.exe"}) {
		t.Fatal("false positive")
	}
}

// fakeProcess is the bundled et client's pseudo-terminal.
type fakeProcess struct {
	mu      sync.Mutex
	writes  bytes.Buffer
	resizes [][2]int
	output  chan []byte
	exited  chan struct{}
	closed  chan struct{}
	code    int
	once    sync.Once
}

func newFakeProcess() *fakeProcess {
	return &fakeProcess{
		output: make(chan []byte, 16),
		exited: make(chan struct{}),
		closed: make(chan struct{}),
	}
}

func (p *fakeProcess) Read(buffer []byte) (int, error) {
	select {
	case chunk, ok := <-p.output:
		if !ok {
			return 0, io.EOF
		}
		return copy(buffer, chunk), nil
	case <-p.closed:
		return 0, io.EOF
	}
}

func (p *fakeProcess) Write(buffer []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writes.Write(buffer)
}

func (p *fakeProcess) Resize(cols, rows int) error {
	p.mu.Lock()
	p.resizes = append(p.resizes, [2]int{cols, rows})
	p.mu.Unlock()
	return nil
}

func (p *fakeProcess) Wait() (int, error) {
	<-p.exited
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code, nil
}

func (p *fakeProcess) Exited() <-chan struct{} { return p.exited }
func (p *fakeProcess) Kill() error             { return p.Close() }

func (p *fakeProcess) Close() error {
	p.once.Do(func() {
		close(p.closed)
		close(p.exited)
	})
	return nil
}

func (p *fakeProcess) emit(text string) { p.output <- []byte(text) }

func (p *fakeProcess) exit(code int) {
	p.mu.Lock()
	p.code = code
	p.mu.Unlock()
	p.once.Do(func() {
		close(p.output)
		close(p.exited)
	})
}

func (p *fakeProcess) written() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writes.String()
}

func (p *fakeProcess) size() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.resizes) == 0 {
		return 0, 0
	}
	last := p.resizes[len(p.resizes)-1]
	return last[0], last[1]
}

func (p *fakeProcess) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

func expectRead(t *testing.T, reader io.Reader, want string) {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		buffer := make([]byte, 1024)
		n, err := reader.Read(buffer)
		done <- result{buffer[:n], err}
	}()
	select {
	case got := <-done:
		if !strings.Contains(string(got.data), want) {
			t.Fatalf("Read = %q err %v, want %q", got.data, got.err, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for output")
	}
}

func ptr[T any](value T) *T { return &value }
