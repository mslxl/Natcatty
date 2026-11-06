package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	appsession "github.com/mslxl/natcatty/application/session"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func packagedSmokeTestEnv(overrides map[string]string) func(string) string {
	values := map[string]string{
		packagedSmokeEnabledEnv: "1",
		packagedSmokeModeEnv:    "launch",
		packagedSmokeResultEnv:  "/tmp/packaged-smoke-result.json",
		packagedSmokeProfileEnv: "/tmp/packaged-smoke-profile",
	}
	for name, value := range overrides {
		values[name] = value
	}
	return func(name string) string { return values[name] }
}

func packagedSmokeSessionEnv(overrides map[string]string) func(string) string {
	values := map[string]string{
		packagedSmokeModeEnv:     "session",
		packagedSmokeSSHPortEnv:  "2222",
		packagedSmokeETPortEnv:   "8822",
		packagedSmokePasswordEnv: "lab-secret",
	}
	for name, value := range overrides {
		values[name] = value
	}
	return packagedSmokeTestEnv(values)
}

func TestPackagedSmokeConfigFromEnv(t *testing.T) {
	tests := []struct {
		name       string
		getenv     func(string) string
		goos       string
		wantActive bool
		wantErr    bool
		check      func(*testing.T, packagedSmokeConfig)
	}{
		{
			name:   "unset",
			getenv: func(string) string { return "" },
			goos:   "darwin",
		},
		{
			name: "disabled ignores every other value",
			getenv: packagedSmokeTestEnv(map[string]string{
				packagedSmokeEnabledEnv:  "0",
				packagedSmokeModeEnv:     "not-a-mode",
				packagedSmokeSSHPortEnv:  "not-a-port",
				packagedSmokePasswordEnv: "",
			}),
			goos: "darwin",
		},
		{
			name: "the literal true is not the trigger",
			getenv: packagedSmokeTestEnv(map[string]string{
				packagedSmokeEnabledEnv: "true",
			}),
			goos: "darwin",
		},
		{
			name: "a padded trigger is not the trigger",
			getenv: packagedSmokeTestEnv(map[string]string{
				packagedSmokeEnabledEnv: " 1 ",
			}),
			goos: "darwin",
		},
		{
			name: "session secrets reject a padded truth value",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokeSessionSecretsEnv: " 1 ",
			}),
			goos:    "linux",
			wantErr: true,
		},
		{
			name: "the unsupported flag rejects a padded truth value",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokeAllowUnsupportedSessionEnv: " 1 ",
			}),
			goos:    "linux",
			wantErr: true,
		},
		{
			name: "missing mode",
			getenv: packagedSmokeTestEnv(map[string]string{
				packagedSmokeModeEnv: "",
			}),
			goos:    "darwin",
			wantErr: true,
		},
		{
			name: "unknown mode",
			getenv: packagedSmokeTestEnv(map[string]string{
				packagedSmokeModeEnv: "forever",
			}),
			goos:    "darwin",
			wantErr: true,
		},
		{
			name: "missing result path",
			getenv: packagedSmokeTestEnv(map[string]string{
				packagedSmokeResultEnv: "",
			}),
			goos:    "darwin",
			wantErr: true,
		},
		{
			name: "missing profile",
			getenv: packagedSmokeTestEnv(map[string]string{
				packagedSmokeProfileEnv: "",
			}),
			goos:    "darwin",
			wantErr: true,
		},
		{
			name:       "launch needs no lab values",
			getenv:     packagedSmokeTestEnv(nil),
			goos:       "windows",
			wantActive: true,
			check: func(t *testing.T, cfg packagedSmokeConfig) {
				if cfg.Mode != packagedSmokeLaunch {
					t.Fatalf("mode = %q", cfg.Mode)
				}
				if cfg.ResultPath != "/tmp/packaged-smoke-result.json" {
					t.Fatalf("result = %q", cfg.ResultPath)
				}
			},
		},
		{
			name: "launch ignores invalid session-only values",
			getenv: packagedSmokeTestEnv(map[string]string{
				packagedSmokeSSHPortEnv:        "not-a-port",
				packagedSmokeETPortEnv:         "70000",
				packagedSmokePasswordEnv:       "",
				packagedSmokeSessionSecretsEnv: "maybe",
			}),
			goos:       "linux",
			wantActive: true,
		},
		{
			name:       "session defaults",
			getenv:     packagedSmokeSessionEnv(map[string]string{packagedSmokeSSHPortEnv: "", packagedSmokeETPortEnv: ""}),
			goos:       "linux",
			wantActive: true,
			check: func(t *testing.T, cfg packagedSmokeConfig) {
				if cfg.Mode != packagedSmokeSession {
					t.Fatalf("mode = %q", cfg.Mode)
				}
				if cfg.SSHPort != 22 || cfg.ETPort != 2022 {
					t.Fatalf("ports = %d/%d, want 22/2022", cfg.SSHPort, cfg.ETPort)
				}
				if cfg.Password != "lab-secret" {
					t.Fatalf("secret was not preserved")
				}
			},
		},
		{
			name:       "session custom ports",
			getenv:     packagedSmokeSessionEnv(nil),
			goos:       "darwin",
			wantActive: true,
			check: func(t *testing.T, cfg packagedSmokeConfig) {
				if cfg.SSHPort != 2222 || cfg.ETPort != 8822 {
					t.Fatalf("ports = %d/%d, want 2222/8822", cfg.SSHPort, cfg.ETPort)
				}
			},
		},
		{
			name: "session missing secret",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokePasswordEnv: "",
			}),
			goos:    "linux",
			wantErr: true,
		},
		{
			name: "session zero ssh port",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokeSSHPortEnv: "0",
			}),
			goos:    "linux",
			wantErr: true,
		},
		{
			name: "session out of range ssh port",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokeSSHPortEnv: "65536",
			}),
			goos:    "linux",
			wantErr: true,
		},
		{
			name: "session non-numeric ssh port",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokeSSHPortEnv: "ssh",
			}),
			goos:    "linux",
			wantErr: true,
		},
		{
			name: "session zero et port",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokeETPortEnv: "0",
			}),
			goos:    "linux",
			wantErr: true,
		},
		{
			name: "session non-numeric et port",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokeETPortEnv: "et",
			}),
			goos:    "linux",
			wantErr: true,
		},
		{
			name: "session secrets accepted",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokeSessionSecretsEnv: "1",
			}),
			goos:       "linux",
			wantActive: true,
			check: func(t *testing.T, cfg packagedSmokeConfig) {
				if !cfg.SessionSecrets {
					t.Fatal("session secrets were not enabled")
				}
			},
		},
		{
			name: "session secrets rejects unknown truth values",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokeSessionSecretsEnv: "true",
			}),
			goos:    "linux",
			wantErr: true,
		},
		{
			name: "unsupported allow flag rejects unknown truth values",
			getenv: packagedSmokeSessionEnv(map[string]string{
				packagedSmokeAllowUnsupportedSessionEnv: "yes",
			}),
			goos:    "linux",
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, active, err := packagedSmokeConfigForGOOS(test.getenv, test.goos)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if active != test.wantActive {
				t.Fatalf("active = %v, want %v", active, test.wantActive)
			}
			if test.check != nil && active {
				test.check(t, cfg)
			}
		})
	}
}

// The trigger is the only gate. A host that sets a stale result path, profile,
// or secret without NATCATTY_SMOKE=1 must keep the normal app untouched.
func TestPackagedSmokeIsInertWithoutTheTrigger(t *testing.T) {
	neverWritten := filepath.Join(t.TempDir(), "never-written.json")
	neverUsed := filepath.Join(t.TempDir(), "never-used-profile")
	getenv := func(name string) string {
		switch name {
		case packagedSmokeEnabledEnv:
			return ""
		case packagedSmokeModeEnv:
			return "session"
		case packagedSmokeResultEnv:
			return neverWritten
		case packagedSmokeProfileEnv:
			return neverUsed
		case packagedSmokePasswordEnv:
			return "not-read"
		default:
			return ""
		}
	}
	smoke, active, err := packagedSmokeFromEnv(getenv)
	if err != nil {
		t.Fatalf("disabled mode returned an error: %v", err)
	}
	if active || smoke != nil {
		t.Fatalf("disabled mode activated: active=%v smoke=%v", active, smoke)
	}
	if _, err := os.Stat(neverWritten); !os.IsNotExist(err) {
		t.Fatalf("disabled mode wrote a result: %v", err)
	}
	if _, err := os.Stat(neverUsed); !os.IsNotExist(err) {
		t.Fatalf("disabled mode created a profile: %v", err)
	}
}

func TestPackagedSmokeSessionRejectedOnWindows(t *testing.T) {
	getenv := packagedSmokeSessionEnv(nil)
	_, active, err := packagedSmokeConfigForGOOS(getenv, "windows")
	if err == nil {
		t.Fatal("session mode was accepted on Windows without an explicit override")
	}
	if active {
		t.Fatal("rejected configuration reported active")
	}
	if !strings.Contains(err.Error(), packagedSmokeAllowUnsupportedSessionEnv) {
		t.Fatalf("error does not name the escape hatch: %v", err)
	}

	allowed := packagedSmokeSessionEnv(map[string]string{
		packagedSmokeAllowUnsupportedSessionEnv: "1",
	})
	cfg, active, err := packagedSmokeConfigForGOOS(allowed, "windows")
	if err != nil || !active {
		t.Fatalf("explicitly allowed Windows session mode = (%v, %v), want active", active, err)
	}
	if !cfg.AllowUnsupportedSession {
		t.Fatal("the override was not recorded")
	}
}

func TestPackagedSmokeActivationFailureWritesAResult(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "nested", "result.json")
	getenv := packagedSmokeSessionEnv(map[string]string{
		packagedSmokeResultEnv: resultPath,
	})
	_, _, err := packagedSmokeConfigForGOOS(getenv, "windows")
	if err == nil {
		t.Fatal("Windows session mode was accepted")
	}
	writePackagedSmokeActivationFailure(getenv, err)

	var result packagedSmokeResult
	data, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatalf("activation failure did not write a result: %v", readErr)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode activation result: %v", err)
	}
	if result.Mode != "session" || result.Platform == "" || len(result.Checks) != 1 {
		t.Fatalf("activation result = %+v", result)
	}
	if check := result.Checks[0]; check.Name != "activation" || check.OK ||
		!strings.Contains(check.Error, packagedSmokeAllowUnsupportedSessionEnv) {
		t.Fatalf("activation check = %+v", check)
	}
}

func TestWritePackagedSmokeResultIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	first := packagedSmokeResult{
		Mode:     "session",
		Platform: "linux/arm64",
		Checks: []packagedSmokeCheck{
			{Name: "mosh_command", OK: false, Error: "exit status 1"},
			{Name: "et_command", OK: true},
		},
	}
	if err := writePackagedSmokeResult(path, first); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got packagedSmokeResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, data)
	}
	if got.Mode != first.Mode || got.Platform != first.Platform || len(got.Checks) != 2 {
		t.Fatalf("result = %+v, want %+v", got, first)
	}
	if got.Checks[0].Error != "exit status 1" || !got.Checks[1].OK {
		t.Fatalf("checks did not round-trip: %+v", got.Checks)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "result.json" {
		t.Fatalf("temporary files were left behind: %v", entries)
	}

	second := packagedSmokeResult{Mode: "launch", Platform: "darwin/arm64", Checks: []packagedSmokeCheck{{Name: "window_visible", OK: true}}}
	if err := writePackagedSmokeResult(path, second); err != nil {
		t.Fatalf("replace: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode replacement: %v", err)
	}
	if got.Mode != "launch" || len(got.Checks) != 1 || !got.Checks[0].OK {
		t.Fatalf("replacement = %+v", got)
	}
}

func TestWritePackagedSmokeResultRejectsAMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "result.json")
	err := writePackagedSmokeResult(path, packagedSmokeResult{Mode: "launch"})
	if err == nil {
		t.Fatal("write to a missing directory succeeded")
	}
	if _, statErr := os.Stat(filepath.Dir(path)); !os.IsNotExist(statErr) {
		t.Fatalf("the writer created the missing directory: %v", statErr)
	}
}

func TestPackagedSmokeHostDraft(t *testing.T) {
	cfg := packagedSmokeConfig{
		Mode:     packagedSmokeSession,
		SSHPort:  2222,
		ETPort:   8822,
		Password: "lab-secret",
	}
	mosh := cfg.hostDraft(packagedSmokeKinds[0], "alice")
	normalized, err := domainvault.NormalizeDraft(mosh)
	if err != nil {
		t.Fatalf("Mosh draft is invalid: %v", err)
	}
	if normalized.Protocol != domainvault.ProtocolMosh || normalized.Hostname != "127.0.0.1" ||
		normalized.Port != 2222 || normalized.Username != "alice" {
		t.Fatalf("Mosh draft = %+v", normalized)
	}
	if normalized.TransportImplementation != transportmodel.InProcess {
		t.Fatalf("Mosh implementation = %q, want in_process", normalized.TransportImplementation)
	}
	if normalized.ETPort != 0 {
		t.Fatalf("Mosh draft kept ETPort = %d", normalized.ETPort)
	}
	if normalized.Password != "lab-secret" {
		t.Fatal("Mosh draft lost the lab secret")
	}

	etDraft := cfg.hostDraft(packagedSmokeKinds[1], "alice")
	et, err := domainvault.NormalizeDraft(etDraft)
	if err != nil {
		t.Fatalf("ET draft is invalid: %v", err)
	}
	if et.Protocol != domainvault.ProtocolET || et.ETPort != 8822 {
		t.Fatalf("ET draft = %+v", et)
	}
	if et.TransportImplementation != transportmodel.InProcess {
		t.Fatalf("ET implementation = %q, want in_process", et.TransportImplementation)
	}
	// Password-only auth keeps the smoke from enumerating the real ~/.ssh keys.
	for _, draft := range []domainvault.Draft{normalized, et} {
		if draft.AuthMethod != domainvault.HostAuthPassword {
			t.Fatalf("%q AuthMethod = %q, want password", draft.Protocol, draft.AuthMethod)
		}
	}
	if got := packagedSmokeKinds[0].Name; got != "mosh" {
		t.Fatalf("first kind = %q", got)
	}
	if got := packagedSmokeKinds[1].Name; got != "et" {
		t.Fatalf("second kind = %q", got)
	}
	// The host-key probe offers the dialer's default preference order. That
	// only predicts the session's key while this fixture keeps the default
	// algorithm policy.
	for _, draft := range []domainvault.Draft{mosh, etDraft} {
		if draft.LegacyAlgorithms || draft.SkipEcdsaHostKey || draft.Algorithms != nil {
			t.Fatalf("draft %q overrides the algorithm policy; the smoke host-key probe assumes defaults", draft.Label)
		}
	}
}

// startSmokeHostKeyServer serves SSH on an ephemeral loopback port with both an
// ECDSA P-256 host key and an Ed25519 host key. The server advertises both, so
// the client's preference order alone decides which key the handshake uses.
func startSmokeHostKeyServer(t *testing.T) (int, xssh.Signer, xssh.Signer) {
	t.Helper()
	_, ed25519Private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the Ed25519 host key: %v", err)
	}
	ed25519Signer, err := xssh.NewSignerFromKey(ed25519Private)
	if err != nil {
		t.Fatalf("convert the Ed25519 host key: %v", err)
	}
	ecdsaPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate the ECDSA host key: %v", err)
	}
	ecdsaSigner, err := xssh.NewSignerFromKey(ecdsaPrivate)
	if err != nil {
		t.Fatalf("convert the ECDSA host key: %v", err)
	}

	serverConfig := &xssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(ecdsaSigner)
	serverConfig.AddHostKey(ed25519Signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				serverConn, _, _, err := xssh.NewServerConn(conn, serverConfig)
				if err == nil {
					_ = serverConn.Close()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		wg.Wait()
	})
	return listener.Addr().(*net.TCPAddr).Port, ed25519Signer, ecdsaSigner
}

// TestDialPackagedSmokeHostKeyUsesTheDialerPreference is the regression test
// for the Phase 3A trust defect: x/crypto's default order negotiates the ECDSA
// key, while the app's dialer prefers Ed25519. The seeded Known Host must name
// the key the session will actually present.
func TestDialPackagedSmokeHostKeyUsesTheDialerPreference(t *testing.T) {
	port, ed25519Signer, ecdsaSigner := startSmokeHostKeyServer(t)

	knownHost, err := dialPackagedSmokeHostKey(context.Background(), "127.0.0.1", port, "smoke-user")
	if err != nil {
		t.Fatalf("dialPackagedSmokeHostKey: %v", err)
	}

	ed25519Type := ed25519Signer.PublicKey().Type()
	ed25519Fingerprint := domainconn.Fingerprint(ed25519Signer.PublicKey().Marshal())
	ecdsaFingerprint := domainconn.Fingerprint(ecdsaSigner.PublicKey().Marshal())
	if knownHost.KeyType != ed25519Type {
		t.Fatalf("captured key type = %q, want %q", knownHost.KeyType, ed25519Type)
	}
	if knownHost.Fingerprint != ed25519Fingerprint {
		t.Fatalf("captured fingerprint = %q, want the Ed25519 fingerprint %q", knownHost.Fingerprint, ed25519Fingerprint)
	}
	if knownHost.Fingerprint == ecdsaFingerprint {
		t.Fatal("captured the ECDSA host key; the probe did not use the dialer's preference order")
	}
	if knownHost.Hostname != "127.0.0.1" || knownHost.Port != port {
		t.Fatalf("captured endpoint = %s:%d, want 127.0.0.1:%d", knownHost.Hostname, knownHost.Port, port)
	}
}

func TestPackagedSmokeCommandsAreFixedMarkers(t *testing.T) {
	if got, want := packagedSmokeCommand(packagedSmokeKinds[0]), `printf 'NATCATTY_SMOKE_MOSH_OK\n'`; got != want {
		t.Fatalf("Mosh command = %q, want %q", got, want)
	}
	if got, want := packagedSmokeCommand(packagedSmokeKinds[1]), `printf 'NATCATTY_SMOKE_ET_OK\n'`; got != want {
		t.Fatalf("ET command = %q, want %q", got, want)
	}
}

type smokeVaultFake struct {
	mu             sync.Mutex
	drafts         []domainvault.Draft
	sessionSecrets bool
	secretsErr     error
	createErr      error
	block          <-chan struct{}
}

func (f *smokeVaultFake) UseSessionSecrets(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionSecrets = true
	return f.secretsErr
}

func (f *smokeVaultFake) Create(_ context.Context, draft domainvault.Draft) (domainvault.Host, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drafts = append(f.drafts, draft)
	if f.createErr != nil {
		return domainvault.Host{}, f.createErr
	}
	return domainvault.Host{
		ID:                      model.HostID("host-" + string(draft.Protocol)),
		Label:                   draft.Label,
		Hostname:                draft.Hostname,
		Port:                    draft.Port,
		Username:                draft.Username,
		Protocol:                draft.Protocol,
		ETPort:                  draft.ETPort,
		TransportImplementation: draft.TransportImplementation,
	}, nil
}

type smokeShellFake struct {
	mu           sync.Mutex
	opened       []domainvault.Host
	err          error
	failProtocol map[domainvault.Protocol]error
}

func (f *smokeShellFake) OpenHostForMCP(_ context.Context, host domainvault.Host) (model.SessionID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, host)
	if f.err != nil {
		return "", f.err
	}
	if err := f.failProtocol[host.Protocol]; err != nil {
		return "", err
	}
	return model.SessionID("session-" + string(host.Protocol)), nil
}

type smokeSessionsFake struct {
	mu       sync.Mutex
	commands []string
	closed   []model.SessionID
	command  func(model.SessionID, string) (appsession.CommandResult, error)
}

func (f *smokeSessionsFake) Command(_ context.Context, id model.SessionID, command string,
	_ appsession.CommandOptions) (appsession.CommandResult, error) {
	f.mu.Lock()
	f.commands = append(f.commands, command)
	f.mu.Unlock()
	if f.command != nil {
		return f.command(id, command)
	}
	marker := "NATCATTY_SMOKE_ET_OK"
	if strings.Contains(command, "MOSH") {
		marker = "NATCATTY_SMOKE_MOSH_OK"
	}
	return appsession.CommandResult{
		Stdout:      "prompt\n" + marker + "\n",
		ExitCode:    0,
		HasExitCode: true,
		OutputMode:  "shell",
	}, nil
}

func (f *smokeSessionsFake) Close(id model.SessionID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id)
	return true, nil
}

type smokePreferencesFake struct {
	mu        sync.Mutex
	updates   int
	confirm   bool
	updateErr error
}

func (f *smokePreferencesFake) Update(change func(*domainpreferences.Settings)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	settings := domainpreferences.Defaults()
	change(&settings)
	f.confirm = settings.Security.ConfirmCloseSessions
	return f.updateErr
}

type smokeKnownHostsFake struct {
	mu      sync.Mutex
	records []domainconn.KnownHost
}

func (f *smokeKnownHostsFake) UpsertKnownHost(_ context.Context, record domainconn.KnownHost) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, record)
	return nil
}

func smokeTestDeps(vault *smokeVaultFake, shell *smokeShellFake, sessions *smokeSessionsFake,
	prefs *smokePreferencesFake, hosts *smokeKnownHostsFake) packagedSmokeDeps {
	return packagedSmokeDeps{vault: vault, shell: shell, sessions: sessions, preferences: prefs, knownHosts: hosts}
}

func smokeTestHostKey(_ context.Context, hostname string, port int, _ string) (domainconn.KnownHost, error) {
	return domainconn.NewKnownHost("known-host-1", domainconn.Key{
		Hostname:    hostname,
		Port:        port,
		KeyType:     "ssh-ed25519",
		Fingerprint: "fingerprint",
		PublicKey:   "ssh-ed25519 AAAA",
	}, time.Now()), nil
}

func TestPackagedSmokeSessionSuccess(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "result.json")
	vault := &smokeVaultFake{}
	shell := &smokeShellFake{}
	sessions := &smokeSessionsFake{}
	prefs := &smokePreferencesFake{confirm: true}
	hosts := &smokeKnownHostsFake{}
	cfg := packagedSmokeConfig{
		Mode:           packagedSmokeSession,
		ResultPath:     resultPath,
		ProfileDir:     filepath.Join(dir, "profile"),
		SSHPort:        2222,
		ETPort:         8822,
		Password:       "lab-secret",
		SessionSecrets: true,
	}
	quit := make(chan struct{}, 1)
	var exitCode atomic.Int32
	runner := newPackagedSmokeRunner(cfg, smokeTestDeps(vault, shell, sessions, prefs, hosts),
		writePackagedSmokeResult, func() { quit <- struct{}{} })
	runner.username = "alice"
	runner.timeout = 5 * time.Second
	runner.dialHostKey = smokeTestHostKey
	runner.exit = func(code int) { exitCode.Store(int32(code)) }

	runner.start(nil)
	select {
	case <-quit:
	case <-time.After(5 * time.Second):
		t.Fatal("the successful session smoke did not quit")
	}
	if code := exitCode.Load(); code != 0 {
		t.Fatalf("successful smoke called exit(%d)", code)
	}

	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if bytes.Contains(data, []byte("lab-secret")) {
		t.Fatal("the result leaked the lab secret")
	}
	var result packagedSmokeResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Mode != "session" || result.Platform == "" {
		t.Fatalf("result header = %+v", result)
	}
	checks := map[string]bool{}
	for _, check := range result.Checks {
		if !check.OK {
			t.Fatalf("check %s failed: %s", check.Name, check.Error)
		}
		if _, seen := checks[check.Name]; seen {
			t.Fatalf("check %s was recorded twice", check.Name)
		}
		checks[check.Name] = true
	}
	for _, name := range []string{
		"session_secrets", "host_key", "known_host_trust",
		"mosh_host_create", "mosh_session_open", "mosh_command", "mosh_session_close",
		"et_host_create", "et_session_open", "et_command", "et_session_close",
		"close_confirmation_disabled",
	} {
		if !checks[name] {
			t.Fatalf("missing check %s in %+v", name, result.Checks)
		}
	}
	if !vault.sessionSecrets {
		t.Fatal("UseSessionSecrets was not requested")
	}
	if len(vault.drafts) != 2 {
		t.Fatalf("created %d Hosts, want 2", len(vault.drafts))
	}
	if vault.drafts[0].Protocol != domainvault.ProtocolMosh ||
		vault.drafts[1].Protocol != domainvault.ProtocolET {
		t.Fatalf("protocols = %q, %q", vault.drafts[0].Protocol, vault.drafts[1].Protocol)
	}
	for _, draft := range vault.drafts {
		if draft.TransportImplementation != transportmodel.InProcess {
			t.Fatalf("%s implementation = %q", draft.Protocol, draft.TransportImplementation)
		}
		if draft.Password != "lab-secret" {
			t.Fatalf("%s lost the secret", draft.Protocol)
		}
		if draft.Port != 2222 || draft.Hostname != "127.0.0.1" || draft.Username != "alice" {
			t.Fatalf("%s draft = %+v", draft.Protocol, draft)
		}
	}
	if vault.drafts[1].ETPort != 8822 {
		t.Fatalf("ET port = %d", vault.drafts[1].ETPort)
	}
	if len(hosts.records) != 1 || hosts.records[0].ID != "known-host-1" {
		t.Fatalf("Known Host records = %+v", hosts.records)
	}
	if len(sessions.commands) != 2 {
		t.Fatalf("commands = %q", sessions.commands)
	}
	if sessions.commands[0] != `printf 'NATCATTY_SMOKE_MOSH_OK\n'` ||
		sessions.commands[1] != `printf 'NATCATTY_SMOKE_ET_OK\n'` {
		t.Fatalf("commands = %q", sessions.commands)
	}
	if len(sessions.closed) != 2 || len(shell.opened) != 2 {
		t.Fatalf("closed = %v, opened = %v", sessions.closed, shell.opened)
	}
	if prefs.updates != 1 || prefs.confirm {
		t.Fatalf("close confirmation still enabled after %d updates", prefs.updates)
	}
}

func TestPackagedSmokeSessionReportsACommandFailure(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "result.json")
	vault := &smokeVaultFake{}
	shell := &smokeShellFake{}
	sessions := &smokeSessionsFake{command: func(_ model.SessionID, command string) (appsession.CommandResult, error) {
		if strings.Contains(command, "MOSH") {
			return appsession.CommandResult{Stdout: "NATCATTY_SMOKE_MOSH_OK", ExitCode: 7, HasExitCode: true}, nil
		}
		return appsession.CommandResult{Stdout: "NATCATTY_SMOKE_ET_OK", ExitCode: 0, HasExitCode: true}, nil
	}}
	prefs := &smokePreferencesFake{confirm: true}
	hosts := &smokeKnownHostsFake{}
	cfg := packagedSmokeConfig{
		Mode: packagedSmokeSession, ResultPath: resultPath, ProfileDir: filepath.Join(dir, "profile"),
		SSHPort: 2222, ETPort: 8822, Password: "lab-secret",
	}
	var exitCode atomic.Int32
	quitCalled := atomic.Bool{}
	runner := newPackagedSmokeRunner(cfg, smokeTestDeps(vault, shell, sessions, prefs, hosts),
		writePackagedSmokeResult, func() { quitCalled.Store(true) })
	runner.username = "alice"
	runner.timeout = 5 * time.Second
	runner.dialHostKey = smokeTestHostKey
	runner.exit = func(code int) { exitCode.Store(int32(code)) }

	runner.start(nil)
	deadline := time.Now().Add(5 * time.Second)
	for exitCode.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if code := exitCode.Load(); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if quitCalled.Load() {
		t.Fatal("a failed smoke entered the quit sequence")
	}
	var result packagedSmokeResult
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	checks := map[string]packagedSmokeCheck{}
	for _, check := range result.Checks {
		checks[check.Name] = check
	}
	if check := checks["mosh_command"]; check.OK || !strings.Contains(check.Error, "status 7") {
		t.Fatalf("mosh_command = %+v", check)
	}
	if check := checks["et_command"]; !check.OK {
		t.Fatalf("et_command = %+v", check)
	}
	if _, ok := checks["close_confirmation_disabled"]; ok {
		t.Fatal("a failed smoke asserted close-confirmation behavior it did not need")
	}
	if len(sessions.closed) != 2 {
		t.Fatalf("failed command did not close both Sessions: %v", sessions.closed)
	}
}

// runPackagedSmokeSession drives one session attempt with the supplied fakes
// and returns the written result, the exit code, and whether quit was called.
func runPackagedSmokeSession(t *testing.T, shell *smokeShellFake,
	sessions *smokeSessionsFake) (packagedSmokeResult, int32, bool) {
	t.Helper()
	dir := t.TempDir()
	cfg := packagedSmokeConfig{
		Mode: packagedSmokeSession, ResultPath: filepath.Join(dir, "result.json"),
		ProfileDir: filepath.Join(dir, "profile"), SSHPort: 2222, ETPort: 8822,
		Password: "lab-secret",
	}
	done := make(chan struct{}, 1)
	var exitCode atomic.Int32
	var quitCalled atomic.Bool
	runner := newPackagedSmokeRunner(cfg,
		smokeTestDeps(&smokeVaultFake{}, shell, sessions, &smokePreferencesFake{}, &smokeKnownHostsFake{}),
		writePackagedSmokeResult, func() {
			quitCalled.Store(true)
			done <- struct{}{}
		})
	runner.username = "alice"
	runner.timeout = 5 * time.Second
	runner.dialHostKey = smokeTestHostKey
	runner.exit = func(code int) {
		exitCode.Store(int32(code))
		done <- struct{}{}
	}

	runner.start(nil)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the session smoke did not finish")
	}
	var result packagedSmokeResult
	data, err := os.ReadFile(cfg.ResultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return result, exitCode.Load(), quitCalled.Load()
}

func smokeChecksByName(result packagedSmokeResult) map[string]packagedSmokeCheck {
	checks := make(map[string]packagedSmokeCheck, len(result.Checks))
	for _, check := range result.Checks {
		checks[check.Name] = check
	}
	return checks
}

func smokeMarkerFor(command string) string {
	if strings.Contains(command, "MOSH") {
		return packagedSmokeKinds[0].Marker
	}
	return packagedSmokeKinds[1].Marker
}

func TestPackagedSmokeCommandChecksRequireAnExitCodeAndMarker(t *testing.T) {
	tests := []struct {
		name      string
		command   func(model.SessionID, string) (appsession.CommandResult, error)
		wantError string
	}{
		{
			name: "missing exit code",
			command: func(_ model.SessionID, command string) (appsession.CommandResult, error) {
				return appsession.CommandResult{Stdout: smokeMarkerFor(command), HasExitCode: false}, nil
			},
			wantError: "no exit code",
		},
		{
			name: "missing marker",
			command: func(model.SessionID, string) (appsession.CommandResult, error) {
				return appsession.CommandResult{Stdout: "prompt\n", ExitCode: 0, HasExitCode: true}, nil
			},
			wantError: "did not contain",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessions := &smokeSessionsFake{command: test.command}
			result, exit, quit := runPackagedSmokeSession(t, &smokeShellFake{}, sessions)
			if exit != 1 || quit {
				t.Fatalf("exit = %d, quit = %v; want a failure without quit", exit, quit)
			}
			checks := smokeChecksByName(result)
			for _, kind := range []string{"mosh", "et"} {
				check := checks[kind+"_command"]
				if check.OK || !strings.Contains(check.Error, test.wantError) {
					t.Fatalf("%s_command = %+v, want %q", kind, check, test.wantError)
				}
			}
			// A bad command result must not skip the other transport or the
			// Session close.
			if len(sessions.commands) != 2 || len(sessions.closed) != 2 {
				t.Fatalf("commands = %v, closed = %v", sessions.commands, sessions.closed)
			}
		})
	}
}

func TestPackagedSmokeAttemptsETAfterAMoshOpenFailure(t *testing.T) {
	shell := &smokeShellFake{failProtocol: map[domainvault.Protocol]error{
		domainvault.ProtocolMosh: errors.New("mosh handshake refused"),
	}}
	sessions := &smokeSessionsFake{}
	result, exit, quit := runPackagedSmokeSession(t, shell, sessions)
	if exit != 1 || quit {
		t.Fatalf("exit = %d, quit = %v; want a failure without quit", exit, quit)
	}
	checks := smokeChecksByName(result)
	if check := checks["mosh_host_create"]; !check.OK {
		t.Fatalf("mosh_host_create = %+v", check)
	}
	if check := checks["mosh_session_open"]; check.OK ||
		!strings.Contains(check.Error, "mosh handshake refused") {
		t.Fatalf("mosh_session_open = %+v", check)
	}
	if _, ok := checks["mosh_command"]; ok {
		t.Fatal("ran a command without a Mosh Session")
	}
	if _, ok := checks["mosh_session_close"]; ok {
		t.Fatal("closed a Mosh Session that never opened")
	}
	for _, name := range []string{"et_host_create", "et_session_open", "et_command", "et_session_close"} {
		if check := checks[name]; !check.OK {
			t.Fatalf("%s = %+v", name, check)
		}
	}
	if len(sessions.commands) != 1 || sessions.commands[0] != packagedSmokeCommand(packagedSmokeKinds[1]) {
		t.Fatalf("commands = %v, want only the ET marker", sessions.commands)
	}
	if len(shell.opened) != 2 {
		t.Fatalf("opened %d Hosts, want both transports attempted", len(shell.opened))
	}
}

func TestRunOptionsKeepTheRealMachineOutOfSmokeMode(t *testing.T) {
	smoke := runOptions{packagedSmoke: true}
	if smoke.importsShellHistory() {
		t.Fatal("smoke mode would import the real shell history")
	}
	if smoke.registersURLScheme() {
		t.Fatal("smoke mode would take over the real ssh URL handler")
	}
	normal := runOptions{}
	if !normal.importsShellHistory() || !normal.registersURLScheme() {
		t.Fatal("the normal app must keep importing history and registering the handler")
	}
}

func TestPackagedSmokeSecretServiceIsScopedToTheProfile(t *testing.T) {
	first := packagedSmokeSecretService("/tmp/profile-a")
	if first != packagedSmokeSecretService("/tmp/profile-a") {
		t.Fatal("the secret service is not stable for one profile")
	}
	if first == packagedSmokeSecretService("/tmp/profile-b") {
		t.Fatal("two profiles share a credential-store service")
	}
	if !strings.HasPrefix(first, "natcatty-packaged-smoke-") || len(first) == len("natcatty-packaged-smoke-") {
		t.Fatalf("secret service = %q", first)
	}
}

func TestPackagedSmokeFromEnvScopesTheCredentialStore(t *testing.T) {
	getenv := packagedSmokeTestEnv(nil)
	smoke, active, err := packagedSmokeFromEnv(getenv)
	if err != nil || !active {
		t.Fatalf("active = %v, error = %v", active, err)
	}
	want := packagedSmokeSecretService(getenv(packagedSmokeProfileEnv))
	if smoke.secretService != want {
		t.Fatalf("secret service = %q, want %q", smoke.secretService, want)
	}
}

func TestPackagedSmokeWatchdogFinishesAWedgedRun(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "result.json")
	block := make(chan struct{})
	vault := &smokeVaultFake{block: block}
	cfg := packagedSmokeConfig{
		Mode: packagedSmokeSession, ResultPath: resultPath, ProfileDir: filepath.Join(dir, "profile"),
		SSHPort: 2222, ETPort: 8822, Password: "lab-secret",
	}
	var exitCode atomic.Int32
	runner := newPackagedSmokeRunner(cfg, smokeTestDeps(vault, &smokeShellFake{}, &smokeSessionsFake{},
		&smokePreferencesFake{}, &smokeKnownHostsFake{}), writePackagedSmokeResult, func() {})
	runner.username = "alice"
	runner.timeout = 30 * time.Millisecond
	runner.dialHostKey = smokeTestHostKey
	runner.exit = func(code int) {
		exitCode.Store(int32(code))
		select {
		case <-block:
		default:
			close(block)
		}
	}

	runner.start(nil)
	deadline := time.Now().Add(5 * time.Second)
	for exitCode.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if code := exitCode.Load(); code != 1 {
		t.Fatalf("watchdog exit = %d, want 1", code)
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var result packagedSmokeResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "watchdog" {
			found = true
			if check.OK || !strings.Contains(check.Error, "did not finish") {
				t.Fatalf("watchdog check = %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("no watchdog check in %+v", result.Checks)
	}
}

func TestPackagedSmokeLaunchMode(t *testing.T) {
	dir := t.TempDir()
	quit := atomic.Int32{}
	newRunner := func(visible bool, exitCode *atomic.Int32) *packagedSmokeRunner {
		cfg := packagedSmokeConfig{
			Mode:       packagedSmokeLaunch,
			ResultPath: filepath.Join(dir, "result.json"),
			ProfileDir: filepath.Join(dir, "profile"),
		}
		runner := newPackagedSmokeRunner(cfg, packagedSmokeDeps{}, writePackagedSmokeResult,
			func() { quit.Add(1) })
		runner.timeout = 5 * time.Second
		runner.exit = func(code int) { exitCode.Store(int32(code)) }
		return runner
	}

	var visibleExit atomic.Int32
	visible := &fakeWindow{shown: true}
	newRunner(true, &visibleExit).start(visible)
	if visibleExit.Load() != 0 || quit.Load() != 1 {
		t.Fatalf("visible launch: exit=%d quit=%d", visibleExit.Load(), quit.Load())
	}
	var result packagedSmokeResult
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Checks) != 2 || result.Checks[0].Name != "window_visible" ||
		!result.Checks[0].OK || result.Checks[1].Name != "close_confirmation_disabled" || !result.Checks[1].OK {
		t.Fatalf("visible result = %+v", result.Checks)
	}

	quit.Store(0)
	var hiddenExit atomic.Int32
	hidden := &fakeWindow{shown: false}
	newRunner(false, &hiddenExit).start(hidden)
	if hiddenExit.Load() != 1 {
		t.Fatalf("hidden launch exit = %d, want 1", hiddenExit.Load())
	}
	if quit.Load() != 0 {
		t.Fatalf("hidden launch entered the quit sequence %d times", quit.Load())
	}
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(dir, "result.json")), &result); err != nil {
		t.Fatalf("decode hidden result: %v", err)
	}
	if len(result.Checks) != 1 || result.Checks[0].Name != "window_visible" || result.Checks[0].OK {
		t.Fatalf("hidden result = %+v", result.Checks)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func TestPackagedSmokeUsernameFallbackIsNotACommand(t *testing.T) {
	// Usernames are data only. This guards the fixed-command contract: the
	// user value is passed to the SSH config and the Host draft, never parsed
	// as a shell command or a path.
	username := packagedSmokeUsername()
	if strings.ContainsAny(username, "\r\n\x00") {
		t.Fatalf("username contains control characters: %q", username)
	}
	if got := packagedSmokeCommand(packagedSmokeKinds[0]); strings.Contains(got, username) && username != "" {
		t.Fatalf("the marker command incorporated the username: %q", got)
	}
}

func TestPackagedSmokePreferencesNilIsAccepted(t *testing.T) {
	// The runner is also usable from tests and future modes without a
	// preferences service; a nil dependency is not a crash.
	directory := t.TempDir()
	cfg := packagedSmokeConfig{Mode: packagedSmokeLaunch, ResultPath: filepath.Join(directory, "result.json")}
	runner := newPackagedSmokeRunner(cfg, packagedSmokeDeps{}, writePackagedSmokeResult, func() {})
	runner.timeout = time.Second
	runner.exit = func(int) { t.Fatal("unexpected exit") }
	runner.start(&fakeWindow{shown: true})
}

var _ packagedSmokeVault = (*smokeVaultFake)(nil)
var _ packagedSmokeShell = (*smokeShellFake)(nil)
var _ packagedSmokeSessions = (*smokeSessionsFake)(nil)
var _ packagedSmokePreferences = (*smokePreferencesFake)(nil)
var _ packagedSmokeKnownHosts = (*smokeKnownHostsFake)(nil)
