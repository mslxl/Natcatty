package app

// Packaged-artifact smoke mode. This file ships in the normal, untagged app so
// the artifact that users receive is the artifact CI exercises. It is dormant
// unless every part of the environment contract is present.
//
// Environment contract (all values are read exactly once, before run):
//
//	NATCATTY_SMOKE=1
//	    Activates the mode. Any other value leaves the app completely inert.
//	NATCATTY_SMOKE_MODE=launch|session
//	    launch: prove the packaged app reached a visible main window.
//	    session: on macOS/Linux, also open one in-process Mosh Session and one
//	    in-process ET Session against a local lab server, run one fixed
//	    command, close both, and quit.
//	NATCATTY_SMOKE_RESULT=<path>
//	    Atomic JSON result: {mode, platform, checks:[{name,ok,error}]}. This
//	    file and the process exit code are authoritative; Windows GUI builds
//	    have no usable stdout.
//	NATCATTY_SMOKE_PROFILE=<dir>
//	    Isolated MyGo user-data directory. The smoke never reads or writes the
//	    real Vault or preferences. When an OS credential store is available it
//	    uses a profile-scoped service name, so the app's real entries are
//	    never read or written; each run can leave one orphan scoped entry.
//	NATCATTY_SMOKE_SSH_PORT=<port>
//	    Lab sshd port for session mode. Defaults to 22.
//	NATCATTY_SMOKE_ET_PORT=<port>
//	    Lab etserver port for session mode. Defaults to 2022.
//	NATCATTY_SMOKE_PASSWORD=<value>
//	    Password for the lab SSH account. Required in session mode; never
//	    written to the result or logs.
//	NATCATTY_SMOKE_SSH_USER=<name>
//	    Lab SSH account. Defaults to the current OS user. Set it when the
//	    local account cannot authenticate to the lab, for example a runner
//	    account protected by a Secure Token that refuses a password reset.
//	NATCATTY_SMOKE_SESSION_SECRETS=1
//	    Ask the Vault for session-only credential mode. Required on a headless
//	    Linux runner whose OS credential store is unavailable.
//	NATCATTY_SMOKE_ALLOW_UNSUPPORTED_SESSION=1
//	    Lab-only escape hatch for running session mode on a platform Phase 3A
//	    does not gate (currently Windows). It changes no result semantics.
//
// The mode never reads a command, executable, or script path from the
// environment. The only commands it runs are the fixed marker commands below.
// It opens no listener. Any missing or malformed required value fails before
// a window opens, and a failure after readiness writes the result, then exits
// non-zero without waiting for a quit confirmation.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	xssh "golang.org/x/crypto/ssh"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	appsession "github.com/mslxl/natcatty/application/session"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

const (
	packagedSmokeEnabledEnv                 = "NATCATTY_SMOKE"
	packagedSmokeModeEnv                    = "NATCATTY_SMOKE_MODE"
	packagedSmokeResultEnv                  = "NATCATTY_SMOKE_RESULT"
	packagedSmokeProfileEnv                 = "NATCATTY_SMOKE_PROFILE"
	packagedSmokeSSHPortEnv                 = "NATCATTY_SMOKE_SSH_PORT"
	packagedSmokeETPortEnv                  = "NATCATTY_SMOKE_ET_PORT"
	packagedSmokePasswordEnv                = "NATCATTY_SMOKE_PASSWORD"
	packagedSmokeSSHUserEnv                 = "NATCATTY_SMOKE_SSH_USER"
	packagedSmokeSessionSecretsEnv          = "NATCATTY_SMOKE_SESSION_SECRETS"
	packagedSmokeAllowUnsupportedSessionEnv = "NATCATTY_SMOKE_ALLOW_UNSUPPORTED_SESSION"

	packagedSmokeDefaultSSHPort = 22
	packagedSmokeDefaultETPort  = 2022

	// packagedSmokeTimeout is the internal watchdog. A CI runner must never
	// hang on a wedged dial, trust prompt, or remote server.
	packagedSmokeTimeout = 90 * time.Second
	// packagedSmokeCommandTimeout bounds one marker command below the
	// overall watchdog.
	packagedSmokeCommandTimeout = 30 * time.Second
	packagedSmokeDialTimeout    = 10 * time.Second
)

type packagedSmokeMode string

const (
	packagedSmokeLaunch  packagedSmokeMode = "launch"
	packagedSmokeSession packagedSmokeMode = "session"
)

// packagedSmokeConfig is the validated environment contract. It contains the
// lab secret, so no String or JSON method is provided.
type packagedSmokeConfig struct {
	Mode           packagedSmokeMode
	ResultPath     string
	ProfileDir     string
	SSHPort        int
	ETPort         int
	Password       string
	SSHUser        string
	SessionSecrets bool
	// AllowUnsupportedSession permits session mode on a platform outside the
	// Phase 3A gate. It is documented as a lab escape hatch.
	AllowUnsupportedSession bool
}

// packagedSmokeCheck is one machine-readable assertion in the result file.
type packagedSmokeCheck struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// packagedSmokeResult is the atomic result contract shared with the driver.
type packagedSmokeResult struct {
	Mode     string               `json:"mode"`
	Platform string               `json:"platform"`
	Checks   []packagedSmokeCheck `json:"checks"`
}

// packagedSmokeFromEnv parses the environment contract. active is false when
// NATCATTY_SMOKE is not exactly "1": in that case every other variable is
// ignored, which keeps the normal app byte-for-byte inert. When the trigger is
// set, every required value must be valid or the app refuses to start.
func packagedSmokeFromEnv(getenv func(string) string) (*packagedSmoke, bool, error) {
	cfg, active, err := packagedSmokeConfigFromEnv(getenv)
	if err != nil || !active {
		return nil, active, err
	}
	return &packagedSmoke{cfg: cfg, secretService: packagedSmokeSecretService(cfg.ProfileDir)}, true, nil
}

// packagedSmokeSecretService names the OS credential-store service an isolated
// run uses. Hashing the profile path gives each driver run a fresh service: the
// smoke never reads an entry a previous build left behind (macOS would block on
// an access prompt) and never touches the user's real entries.
func packagedSmokeSecretService(profileDir string) string {
	sum := sha256.Sum256([]byte(profileDir))
	return "natcatty-packaged-smoke-" + hex.EncodeToString(sum[:8])
}

func packagedSmokeConfigFromEnv(getenv func(string) string) (packagedSmokeConfig, bool, error) {
	return packagedSmokeConfigForGOOS(getenv, runtime.GOOS)
}

// writePackagedSmokeActivationFailure gives GUI platforms a result file even
// when the environment contract itself is invalid (for example, session mode
// on Windows). It is a no-op unless the trigger is set and a result path was
// supplied, so the dormant app still writes nothing.
func writePackagedSmokeActivationFailure(getenv func(string) string, cause error) {
	if getenv(packagedSmokeEnabledEnv) != "1" {
		return
	}
	path := strings.TrimSpace(getenv(packagedSmokeResultEnv))
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "packaged smoke: create the result directory:", err)
		return
	}
	result := packagedSmokeResult{
		Mode:     strings.TrimSpace(getenv(packagedSmokeModeEnv)),
		Platform: packagedSmokePlatform(),
		Checks:   []packagedSmokeCheck{{Name: "activation", Error: cause.Error()}},
	}
	if err := writePackagedSmokeResult(path, result); err != nil {
		fmt.Fprintln(os.Stderr, "packaged smoke: write the activation failure:", err)
	}
}

func packagedSmokeConfigForGOOS(getenv func(string) string, goos string) (packagedSmokeConfig, bool, error) {
	if getenv(packagedSmokeEnabledEnv) != "1" {
		return packagedSmokeConfig{}, false, nil
	}

	var cfg packagedSmokeConfig
	mode := packagedSmokeMode(strings.TrimSpace(getenv(packagedSmokeModeEnv)))
	switch mode {
	case packagedSmokeLaunch, packagedSmokeSession:
		cfg.Mode = mode
	case "":
		return packagedSmokeConfig{}, false, fmt.Errorf("packaged smoke: %s is required", packagedSmokeModeEnv)
	default:
		return packagedSmokeConfig{}, false, fmt.Errorf("packaged smoke: %s must be %q or %q",
			packagedSmokeModeEnv, packagedSmokeLaunch, packagedSmokeSession)
	}
	cfg.ResultPath = strings.TrimSpace(getenv(packagedSmokeResultEnv))
	if cfg.ResultPath == "" {
		return packagedSmokeConfig{}, false, fmt.Errorf("packaged smoke: %s is required", packagedSmokeResultEnv)
	}
	cfg.ProfileDir = strings.TrimSpace(getenv(packagedSmokeProfileEnv))
	if cfg.ProfileDir == "" {
		return packagedSmokeConfig{}, false, fmt.Errorf("packaged smoke: %s is required", packagedSmokeProfileEnv)
	}

	if cfg.Mode == packagedSmokeLaunch {
		// Launch mode proves window readiness only. Session-only values are
		// deliberately ignored so a launch smoke needs no lab credentials.
		return cfg, true, nil
	}

	sshPort, err := packagedSmokePort(getenv, packagedSmokeSSHPortEnv, packagedSmokeDefaultSSHPort)
	if err != nil {
		return packagedSmokeConfig{}, false, err
	}
	etPort, err := packagedSmokePort(getenv, packagedSmokeETPortEnv, packagedSmokeDefaultETPort)
	if err != nil {
		return packagedSmokeConfig{}, false, err
	}
	cfg.SSHPort = sshPort
	cfg.ETPort = etPort
	cfg.Password = getenv(packagedSmokePasswordEnv)
	if cfg.Password == "" {
		return packagedSmokeConfig{}, false, fmt.Errorf("packaged smoke: %s is required in session mode", packagedSmokePasswordEnv)
	}
	cfg.SSHUser = strings.TrimSpace(getenv(packagedSmokeSSHUserEnv))
	cfg.SessionSecrets, err = packagedSmokeBool(getenv, packagedSmokeSessionSecretsEnv)
	if err != nil {
		return packagedSmokeConfig{}, false, err
	}
	cfg.AllowUnsupportedSession, err = packagedSmokeBool(getenv, packagedSmokeAllowUnsupportedSessionEnv)
	if err != nil {
		return packagedSmokeConfig{}, false, err
	}
	if err := packagedSmokeSessionPlatform(goos, cfg.AllowUnsupportedSession); err != nil {
		return packagedSmokeConfig{}, false, err
	}
	return cfg, true, nil
}

func packagedSmokePort(getenv func(string) string, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 65535 {
		return 0, fmt.Errorf("packaged smoke: %s must be a TCP port (1-65535)", name)
	}
	return value, nil
}

func packagedSmokeBool(getenv func(string) string, name string) (bool, error) {
	switch getenv(name) {
	case "":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("packaged smoke: %s must be 1 or unset", name)
	}
}

// packagedSmokeSessionPlatform rejects session mode where Phase 3A has no
// native servers to drive. Launch mode stays available on every target.
func packagedSmokeSessionPlatform(goos string, allowUnsupported bool) error {
	if goos == "darwin" || goos == "linux" || allowUnsupported {
		return nil
	}
	return fmt.Errorf("packaged smoke: session mode is not supported on %s in Phase 3A; "+
		"use launch mode or set %s=1 for a lab-only run", goos, packagedSmokeAllowUnsupportedSessionEnv)
}

// afterReady is the callback run invokes once the main window exists. run
// populates deps from the live composition root so no dependency is exposed as
// a package global.
func (s *packagedSmoke) afterReady(win hostWindow, deps packagedSmokeDeps) {
	s.runner = newPackagedSmokeRunner(s.cfg, deps, s.writeResult, mygo.App.Quit)
	s.runner.start(win)
}

// prepare creates the isolated profile and result directory before run. The
// profile directory is private; the result directory is whatever the caller
// chose and may already exist.
func (s *packagedSmoke) prepare() error {
	resultDir := filepath.Dir(s.cfg.ResultPath)
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		return fmt.Errorf("packaged smoke: create the result directory: %w", err)
	}
	if err := os.MkdirAll(s.cfg.ProfileDir, 0o700); err != nil {
		return fmt.Errorf("packaged smoke: create the isolated profile: %w", err)
	}
	return nil
}

// writeResult records that a result reached disk. Run uses didFinish to fail
// loudly when the app exited before the smoke could run (for example, when
// another instance owns the single-instance lock).
func (s *packagedSmoke) writeResult(path string, result packagedSmokeResult) error {
	if err := writePackagedSmokeResult(path, result); err != nil {
		return err
	}
	s.didWrite.Store(true)
	return nil
}

func (s *packagedSmoke) didFinish() bool { return s.didWrite.Load() }

// writeStartupFailure writes a result for a failure before readiness. It is
// best effort: the original error is what the process reports.
func (s *packagedSmoke) writeStartupFailure(err error) {
	result := packagedSmokeResult{
		Mode:     string(s.cfg.Mode),
		Platform: packagedSmokePlatform(),
		Checks:   []packagedSmokeCheck{{Name: "app_run", Error: err.Error()}},
	}
	if writeErr := s.writeResult(s.cfg.ResultPath, result); writeErr != nil {
		fmt.Fprintln(os.Stderr, "packaged smoke: write the result:", writeErr)
	}
}

type packagedSmoke struct {
	cfg           packagedSmokeConfig
	secretService string
	runner        *packagedSmokeRunner
	didWrite      atomic.Bool
}

// writePackagedSmokeResult serializes and atomically replaces path. The
// temporary file lives in the destination directory, so the rename cannot
// cross filesystems. A short result contains no secrets, but its error text
// may name local endpoints, so the file is private.
func writePackagedSmokeResult(path string, result packagedSmokeResult) error {
	if path == "" {
		return errors.New("packaged smoke: no result path")
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("packaged smoke: encode the result: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".packaged-smoke-*.json.tmp")
	if err != nil {
		return fmt.Errorf("packaged smoke: create the result file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("packaged smoke: replace the result file: %w", err)
	}
	tmpPath = ""
	return nil
}

func packagedSmokePlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// packagedSmokeKind is one fixed lab Session. Marker is the exact output the
// command must produce; commands are never read from the environment.
type packagedSmokeKind struct {
	Name     string
	Label    string
	Protocol domainvault.Protocol
	Marker   string
}

var packagedSmokeKinds = []packagedSmokeKind{
	{
		Name:     "mosh",
		Label:    "packaged smoke mosh",
		Protocol: domainvault.ProtocolMosh,
		Marker:   "NATCATTY_SMOKE_MOSH_OK",
	},
	{
		Name:     "et",
		Label:    "packaged smoke et",
		Protocol: domainvault.ProtocolET,
		Marker:   "NATCATTY_SMOKE_ET_OK",
	},
}

// hostDraft builds the session-mode Host fixture. The per-Host
// TransportImplementation is explicit so the InProcess client runs even while
// the release default is still Bundled.
//
// AuthMethod is password-only, so the lab password is the only credential
// offered and the default ~/.ssh keys are never enumerated.
//
// The fixture deliberately leaves the algorithm policy at its defaults:
// Algorithms is nil, LegacyAlgorithms is false, and SkipEcdsaHostKey is false.
// dialPackagedSmokeHostKey relies on that assumption when it probes with
// sshadapter.DefaultHostKeyAlgorithms(): the captured key must be the one the
// real session negotiates. A Host override added here would have to be
// mirrored by the probe.
func (c packagedSmokeConfig) hostDraft(kind packagedSmokeKind, username string) domainvault.Draft {
	draft := domainvault.Draft{
		Label:                   kind.Label,
		Hostname:                "127.0.0.1",
		Port:                    c.SSHPort,
		Username:                username,
		Protocol:                kind.Protocol,
		Password:                c.Password,
		AuthMethod:              domainvault.HostAuthPassword,
		TransportImplementation: transportmodel.InProcess,
	}
	if kind.Protocol == domainvault.ProtocolET {
		draft.ETPort = c.ETPort
	}
	return draft
}

// packagedSmokeCommand is the only command the smoke runs. The marker is a
// compile-time constant, not environment input.
func packagedSmokeCommand(kind packagedSmokeKind) string {
	return "printf '" + kind.Marker + "\\n'"
}

// login is the SSH account. An explicit override exists because a local
// account can be unable to authenticate (a Secure Token blocks a scripted
// password reset), while the lab account is still perfectly usable.
func (c packagedSmokeConfig) login() string {
	if c.SSHUser != "" {
		return c.SSHUser
	}
	return packagedSmokeUsername()
}

func packagedSmokeUsername() string {
	if current, err := user.Current(); err == nil && strings.TrimSpace(current.Username) != "" {
		return strings.TrimSpace(current.Username)
	}
	if name := strings.TrimSpace(os.Getenv("USERNAME")); name != "" {
		return name
	}
	return strings.TrimSpace(os.Getenv("USER"))
}

// packagedSmokeVault is the Vault surface session mode consumes.
type packagedSmokeVault interface {
	Create(context.Context, domainvault.Draft) (domainvault.Host, error)
	UseSessionSecrets(context.Context) error
}

// packagedSmokeShell is the shell seam that opens a Host and returns its live
// Session id after the transport handshake.
type packagedSmokeShell interface {
	OpenHostForMCP(context.Context, domainvault.Host) (model.SessionID, error)
}

// packagedSmokeSessions is the programmatic execution and close surface.
type packagedSmokeSessions interface {
	Command(context.Context, model.SessionID, string, appsession.CommandOptions) (appsession.CommandResult, error)
	Close(model.SessionID) (bool, error)
}

type packagedSmokePreferences interface {
	Update(func(*domainpreferences.Settings)) error
}

type packagedSmokeKnownHosts interface {
	UpsertKnownHost(context.Context, domainconn.KnownHost) error
}

// packagedSmokeDeps is populated by run from the live composition root. It is
// passed to the callback rather than stored globally.
type packagedSmokeDeps struct {
	vault       packagedSmokeVault
	preferences packagedSmokePreferences
	shell       packagedSmokeShell
	sessions    packagedSmokeSessions
	knownHosts  packagedSmokeKnownHosts
}

// packagedSmokeRunner owns one smoke attempt. All mutable state is guarded so
// the session goroutine and the watchdog cannot write the result twice.
type packagedSmokeRunner struct {
	cfg      packagedSmokeConfig
	deps     packagedSmokeDeps
	username string
	timeout  time.Duration

	dialHostKey func(context.Context, string, int, string) (domainconn.KnownHost, error)
	writeResult func(string, packagedSmokeResult) error
	quit        func()
	exit        func(int)

	mu     sync.Mutex
	done   bool
	timer  *time.Timer
	result packagedSmokeResult
}

func newPackagedSmokeRunner(cfg packagedSmokeConfig, deps packagedSmokeDeps,
	writeResult func(string, packagedSmokeResult) error, quit func()) *packagedSmokeRunner {
	return &packagedSmokeRunner{
		cfg:         cfg,
		deps:        deps,
		username:    cfg.login(),
		timeout:     packagedSmokeTimeout,
		dialHostKey: dialPackagedSmokeHostKey,
		writeResult: writeResult,
		quit:        quit,
		exit:        os.Exit,
	}
}

// start installs the watchdog and dispatches the selected mode. Launch runs
// on the readiness thread; session work runs on its own goroutine.
func (r *packagedSmokeRunner) start(win hostWindow) {
	r.mu.Lock()
	r.result = packagedSmokeResult{Mode: string(r.cfg.Mode), Platform: packagedSmokePlatform()}
	r.timer = time.AfterFunc(r.timeout, func() {
		r.fail("watchdog", fmt.Errorf("packaged smoke did not finish within %s", r.timeout))
	})
	r.mu.Unlock()
	if r.cfg.Mode == packagedSmokeLaunch {
		r.runLaunch(win)
		return
	}
	go r.runSession(context.Background())
}

func (r *packagedSmokeRunner) runLaunch(win hostWindow) {
	var err error
	if win == nil || win.IsDestroyed() || !win.IsVisible() {
		err = errors.New("packaged smoke: the main window is not visible")
	}
	r.record("window_visible", err)
	r.finish(true)
}

func (r *packagedSmokeRunner) runSession(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if r.cfg.SessionSecrets {
		r.record("session_secrets", r.deps.vault.UseSessionSecrets(ctx))
	}
	if strings.TrimSpace(r.username) == "" {
		r.record("username", errors.New("packaged smoke: no local username for the lab SSH account"))
		r.finish(false)
		return
	}
	knownHost, err := r.dialHostKey(ctx, "127.0.0.1", r.cfg.SSHPort, r.username)
	if err != nil {
		r.record("host_key", err)
		r.finish(false)
		return
	}
	r.record("host_key", nil)
	if err := r.deps.knownHosts.UpsertKnownHost(ctx, knownHost); err != nil {
		r.record("known_host_trust", err)
		r.finish(false)
		return
	}
	r.record("known_host_trust", nil)

	// Attempt both transports even when one fails: CI evidence should name
	// every broken path in one run. Each kind records its own checks.
	for _, kind := range packagedSmokeKinds {
		r.runKind(ctx, kind)
	}
	r.finish(true)
}

func (r *packagedSmokeRunner) runKind(ctx context.Context, kind packagedSmokeKind) {
	host, err := r.deps.vault.Create(ctx, r.cfg.hostDraft(kind, r.username))
	r.record(kind.Name+"_host_create", err)
	if err != nil {
		return
	}

	id, err := r.deps.shell.OpenHostForMCP(ctx, host)
	r.record(kind.Name+"_session_open", err)
	if err != nil {
		return
	}

	result, commandErr := r.deps.sessions.Command(ctx, id, packagedSmokeCommand(kind),
		appsession.CommandOptions{Timeout: packagedSmokeCommandTimeout})
	if commandErr == nil {
		switch {
		case !result.HasExitCode:
			commandErr = errors.New("packaged smoke: the command reported no exit code")
		case result.ExitCode != 0:
			commandErr = fmt.Errorf("packaged smoke: the command exited with status %d", result.ExitCode)
		case !strings.Contains(result.Stdout, kind.Marker):
			commandErr = fmt.Errorf("packaged smoke: the command output did not contain %s", kind.Marker)
		}
	}
	r.record(kind.Name+"_command", commandErr)

	_, closeErr := r.deps.sessions.Close(id)
	r.record(kind.Name+"_session_close", closeErr)
}

// record appends one check. It is safe from the watchdog. A late check from a
// worker that outlived the result write is dropped: once done is set the
// result is final and no further mutation is allowed.
func (r *packagedSmokeRunner) record(name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	r.appendCheckLocked(name, err)
}

func (r *packagedSmokeRunner) appendCheckLocked(name string, err error) {
	check := packagedSmokeCheck{Name: name, OK: err == nil}
	if err != nil {
		check.Error = err.Error()
	}
	r.result.Checks = append(r.result.Checks, check)
}

// fail records a failure and finishes the run.
func (r *packagedSmokeRunner) fail(name string, err error) {
	r.record(name, err)
	r.finish(false)
}

// finish writes the result exactly once, disables the close confirmation on a
// successful run, and ends the process. A failed run exits directly instead of
// entering the quit sequence: a wedged modal or UI thread must not hang CI.
func (r *packagedSmokeRunner) finish(ok bool) {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return
	}
	r.done = true
	timer := r.timer
	r.timer = nil
	r.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}

	// A call site passes true for "the attempt reached its end". The recorded
	// checks decide whether the run actually passed. Only a run that is still
	// green needs the close confirmation disabled; a failed run exits
	// directly.
	ok = ok && r.checksOK()
	if ok {
		err := r.disableCloseConfirmation()
		r.mu.Lock()
		r.appendCheckLocked("close_confirmation_disabled", err)
		r.mu.Unlock()
		ok = err == nil
	}

	// done prevents any further record, so this snapshot and the final
	// all-checks verdict are stable.
	r.mu.Lock()
	ok = ok && r.checksOKLocked()
	result := packagedSmokeResult{
		Mode:     r.result.Mode,
		Platform: r.result.Platform,
		Checks:   append([]packagedSmokeCheck(nil), r.result.Checks...),
	}
	r.mu.Unlock()

	writeErr := r.writeResult(r.cfg.ResultPath, result)
	if writeErr != nil {
		fmt.Fprintln(os.Stderr, "packaged smoke: write the result:", writeErr)
	}
	if !ok || writeErr != nil {
		r.exit(1)
		return
	}
	r.quit()
}

func (r *packagedSmokeRunner) disableCloseConfirmation() error {
	if r.deps.preferences == nil {
		return nil
	}
	return r.deps.preferences.Update(func(settings *domainpreferences.Settings) {
		settings.Security.ConfirmCloseSessions = false
	})
}

func (r *packagedSmokeRunner) checksOK() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checksOKLocked()
}

func (r *packagedSmokeRunner) checksOKLocked() bool {
	for _, check := range r.result.Checks {
		if !check.OK {
			return false
		}
	}
	return true
}

// dialPackagedSmokeHostKey reads the lab server's host key without
// authenticating and turns it into a Known Host trust record. The host key is
// captured during the handshake, before authentication, so an auth failure is
// not an error for this step. The endpoint is fixed to loopback by the caller.
func dialPackagedSmokeHostKey(ctx context.Context, hostname string, port int, username string) (domainconn.KnownHost, error) {
	address := net.JoinHostPort(hostname, strconv.Itoa(port))
	dialer := net.Dialer{Timeout: packagedSmokeDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return domainconn.KnownHost{}, fmt.Errorf("packaged smoke: dial %s: %w", address, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(packagedSmokeDialTimeout))

	var captured xssh.PublicKey
	config := &xssh.ClientConfig{
		User: username,
		HostKeyCallback: func(_ string, _ net.Addr, key xssh.PublicKey) error {
			captured = key
			return nil
		},
		// Use the dialer's preference order, not x/crypto's default: the
		// Known Host record must name the key the real session negotiates.
		HostKeyAlgorithms: sshadapter.DefaultHostKeyAlgorithms(),
	}
	client, _, _, handshakeErr := xssh.NewClientConn(conn, address, config)
	if client != nil {
		_ = client.Close()
	}
	if captured == nil {
		if handshakeErr == nil {
			handshakeErr = errors.New("the server presented no host key")
		}
		return domainconn.KnownHost{}, fmt.Errorf("packaged smoke: read the host key from %s: %w", address, handshakeErr)
	}

	id, err := packagedSmokeKnownHostID()
	if err != nil {
		return domainconn.KnownHost{}, err
	}
	return domainconn.NewKnownHost(id, domainconn.Key{
		Hostname:    hostname,
		Port:        port,
		KeyType:     captured.Type(),
		Fingerprint: domainconn.Fingerprint(captured.Marshal()),
		PublicKey:   domainconn.PublicKey(captured.Type(), captured.Marshal()),
	}, time.Now()), nil
}

func packagedSmokeKnownHostID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("packaged smoke: generate a Known Host id: %w", err)
	}
	return "packaged-smoke-" + hex.EncodeToString(raw[:]), nil
}
