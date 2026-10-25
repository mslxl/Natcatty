// Package et implements the EternalTerminal Session transport. The bundled
// `et` client performs its own SSH bootstrap and ET handshake; this adapter
// gives it a private HOME with a generated ssh_config, known-hosts snapshot,
// key material, and an SSH_ASKPASS helper that answers only the saved password
// and passphrase prompts.
//
// The binary comes only from adapters/bundle and is verified before every
// launch. A missing bundle is a typed error; the adapter never runs a system
// `et`.
package et

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mslxl/natcatty/adapters/bundle"
	appsession "github.com/mslxl/natcatty/application/session"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	"github.com/mslxl/natcatty/platform/pty"
)

// AskpassFlag selects the helper mode in the app's own executable. The app's
// Run checks it before initializing the UI.
const AskpassFlag = "--natcatty-et-askpass"

// Process is the bundled et client's pseudo-terminal.
type Process interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Wait() (int, error)
	Exited() <-chan struct{}
	Kill() error
}

// Factory opens EternalTerminal Sessions.
type Factory struct {
	// Spec resolves and verifies the bundled et binary.
	Spec bundle.Spec
	// Spawn starts the client. Nil uses platform/pty.
	Spawn func(pty.Options) (Process, error)
	// BaseEnv supplies the environment. Nil uses os.Environ.
	BaseEnv func() []string
	// TempDir supplies the parent of the private HOME. Nil uses os.TempDir.
	TempDir func() string
	// HomeDir supplies the user's real home. Nil uses os.UserHomeDir.
	HomeDir func() string
	// UserName supplies the fallback username. Nil uses os/user.
	UserName func() string
	// Program supplies the askpass executable. Nil uses os.Executable.
	Program func() (string, error)
	// FindSSH resolves the real OpenSSH binary for the PATH wrapper. Nil uses
	// exec.LookPath.
	FindSSH func() (string, error)
	// ReadDir lists ~/.ssh for automatic identity discovery. Nil uses
	// os.ReadDir.
	ReadDir func(string) ([]os.DirEntry, error)
	// Exists reports whether an optional config file exists. Nil uses os.Stat.
	Exists func(string) bool
	// HardenACL restricts a generated secret file to the current user. Nil
	// uses icacls on Windows and is a no-op elsewhere.
	HardenACL func(string)
}

// Prepared is the private environment for one ET Session. Cleanup removes it.
type Prepared struct {
	UserHost   string
	Args       []string
	Env        []string
	Dir        string
	Home       string
	SSHOptions []string
	ConfigPath string
	// Artifacts is the private HOME; it holds credentials and must be removed
	// when the Session ends.
	Artifacts string
}

// Cleanup removes the private HOME and every credential in it.
func (p *Prepared) Cleanup() {
	if p == nil || p.Artifacts == "" {
		return
	}
	_ = os.RemoveAll(p.Artifacts)
	p.Artifacts = ""
}

// Open prepares the environment, starts the bundled client, and returns the
// transport. Preparation failures remove their partial artifacts.
func (f Factory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.ET == nil {
		return nil, fmt.Errorf("%w: no EternalTerminal configuration", etmodel.ErrInvalidConfig)
	}
	cfg := spec.ET.Normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	resolved, err := f.Spec.Resolve()
	if err != nil {
		return nil, translateBundleError(err)
	}
	prepared, err := f.Prepare(cfg, spec)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		prepared.Cleanup()
		return nil, err
	}
	spawn := f.Spawn
	if spawn == nil {
		spawn = func(options pty.Options) (Process, error) {
			terminal, err := pty.Start(options)
			if err != nil {
				return nil, err
			}
			return terminal, nil
		}
	}
	client, err := spawn(pty.Options{
		Path: resolved.Path,
		Args: prepared.Args,
		Dir:  prepared.Dir,
		Env:  prepared.Env,
		Cols: spec.Cols,
		Rows: spec.Rows,
	})
	if err != nil {
		prepared.Cleanup()
		return nil, fmt.Errorf("et: launch the bundled client: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = client.Close()
		prepared.Cleanup()
		return nil, err
	}
	tr := &transport{process: client, prepared: prepared, done: make(chan struct{}), processDone: make(chan struct{})}
	go tr.supervise()
	return tr, nil
}

// Prepare builds the private HOME, config, key files, known-hosts snapshot,
// askpass helper, environment, and argv for one Session.
func (f Factory) Prepare(cfg etmodel.Config, spec appsession.StartSpec) (*Prepared, error) {
	home := f.homeDir()
	parent := os.TempDir()
	if f.TempDir != nil {
		parent = f.TempDir()
	}
	safeID := sanitizeID(cfg.SessionID)
	if safeID == "" {
		safeID = "session"
	}
	tempDir, err := os.MkdirTemp(parent, "et-ssh-home-"+safeID+"-")
	if err != nil {
		return nil, fmt.Errorf("et: create the private home: %w", err)
	}
	prepared := &Prepared{Artifacts: tempDir, Home: tempDir}
	fail := func(err error) (*Prepared, error) {
		prepared.Cleanup()
		return nil, err
	}
	sshDir := filepath.Join(tempDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return fail(fmt.Errorf("et: create the private .ssh: %w", err))
	}
	if err := os.Chmod(sshDir, 0o700); err != nil {
		return fail(fmt.Errorf("et: restrict the private .ssh: %w", err))
	}

	realSshDir := filepath.Join(home, ".ssh")
	defaultIdentities := f.discoverIdentities(realSshDir)
	askpass := etmodel.AskpassEntries(cfg)
	var options []string
	var configLines []string

	// Known-hosts policy. The destination hop receives these as --ssh-option
	// values; a jump hop receives its own copy in its Host block.
	knownHostsPath := cfg.KnownHostsPath
	if knownHostsPath == "" {
		knownHostsPath = filepath.Join(realSshDir, "known_hosts")
	}
	switch {
	case !cfg.VerifyHostKeys:
		empty := filepath.Join(sshDir, safeID+"-empty-known_hosts")
		if err := f.writeSecure(empty, nil); err != nil {
			return fail(err)
		}
		options = append(options,
			"UserKnownHostsFile="+normalizePath(empty, home),
			"GlobalKnownHostsFile="+normalizePath(empty, home),
			"StrictHostKeyChecking=no",
		)
	case cfg.KnownHostsContent != "":
		authoritative := filepath.Join(sshDir, safeID+"-authoritative-known_hosts")
		if err := f.writeSecure(authoritative, []byte(cfg.KnownHostsContent)); err != nil {
			return fail(err)
		}
		options = append(options,
			"UserKnownHostsFile="+normalizePath(authoritative, home),
			"GlobalKnownHostsFile="+normalizePath(authoritative, home),
			"StrictHostKeyChecking=accept-new",
		)
	default:
		options = append(options,
			"UserKnownHostsFile="+normalizePath(knownHostsPath, home),
			"StrictHostKeyChecking=accept-new",
		)
	}
	options = append(options, "LogLevel=ERROR")
	if cfg.Port != 0 && cfg.Port != etmodel.DefaultPort {
		options = append(options, fmt.Sprintf("Port=%d", cfg.Port))
	}

	directives, err := f.authDirectives(sshDir, safeID, "", cfg.Auth, home, &askpass)
	if err != nil {
		return fail(err)
	}
	options = append(options, directives.options...)
	configLines = append(configLines, directives.config...)

	// Automatic mode also offers the user's conventional keys.
	if cfg.Auth.Method == etmodel.AuthAuto {
		for _, path := range defaultIdentities {
			if !contains(directives.identities, path) {
				directives.identities = append(directives.identities, path)
				options = append(options, "IdentityFile="+normalizePath(path, home))
			}
		}
	}
	if cfg.LegacyAlgorithms {
		configLines = append(configLines, etmodel.LegacyAlgorithmLines("")...)
	}

	jumpConfig := make([]string, 0, 32)
	if cfg.Jump != nil {
		jump := cfg.Jump.Normalized()
		jumpUser := jump.Username
		if jumpUser == "" {
			jumpUser = f.userName()
		}
		jumpAuth, err := f.authDirectives(sshDir, safeID, "jump-", jump.Auth, home, &askpass)
		if err != nil {
			return fail(err)
		}
		jumpConfig = append(jumpConfig, "Host "+jump.Hostname)
		jumpConfig = append(jumpConfig, "  User "+jumpUser)
		jumpConfig = append(jumpConfig, fmt.Sprintf("  Port %d", jump.Port))
		for _, directive := range jumpAuth.config {
			jumpConfig = append(jumpConfig, "  "+directive)
		}
		for _, option := range jumpAuth.options {
			jumpConfig = append(jumpConfig, "  "+option)
		}
		if jump.Auth.Method == etmodel.AuthAuto {
			for _, path := range defaultIdentities {
				if !contains(jumpAuth.identities, path) {
					jumpConfig = append(jumpConfig, "  IdentityFile "+quoteValue(normalizePath(path, home)))
				}
			}
		}
		if cfg.VerifyHostKeys {
			jumpKnownHosts := optionsKnownHosts(options)
			if cfg.JumpKnownHostsContent != "" {
				jumpSnapshot := filepath.Join(sshDir, safeID+"-authoritative-jump-known_hosts")
				if err := f.writeSecure(jumpSnapshot, []byte(cfg.JumpKnownHostsContent)); err != nil {
					return fail(err)
				}
				jumpKnownHosts = normalizePath(jumpSnapshot, home)
			}
			jumpConfig = append(jumpConfig,
				"  UserKnownHostsFile "+quoteValue(jumpKnownHosts),
				"  GlobalKnownHostsFile "+quoteValue(jumpKnownHosts),
				"  StrictHostKeyChecking accept-new",
			)
		} else {
			empty := emptyKnownHosts(options)
			jumpConfig = append(jumpConfig,
				"  UserKnownHostsFile "+quoteValue(empty),
				"  GlobalKnownHostsFile "+quoteValue(empty),
				"  StrictHostKeyChecking no",
			)
		}
		jumpConfig = append(jumpConfig, "  LogLevel ERROR", "  KbdInteractiveAuthentication yes", "  NumberOfPasswordPrompts 1")
		if cfg.LegacyAlgorithms || jump.LegacyAlgorithms {
			jumpConfig = append(jumpConfig, etmodel.LegacyAlgorithmLines("  ")...)
		}
	}

	configPath := ""
	// A jump needs a config file even when the destination has no directives:
	// ProxyJump's ssh child reads the jump Host block, not --ssh-option.
	if cfg.Jump != nil || len(configLines) > 0 {
		lines := make([]string, 0, len(configLines)+len(jumpConfig)+8)
		if cfg.Jump != nil {
			lines = append(lines, "Host "+cfg.Hostname)
			lines = append(lines, "  ProxyJump "+cfg.Jump.Hostname)
			for _, line := range configLines {
				lines = append(lines, "  "+line)
			}
			lines = append(lines, jumpConfig...)
		} else {
			lines = append(lines, configLines...)
		}
		includes := f.includes(home)
		if len(includes) > 0 {
			lines = append(lines, "", "Match all", "# Preserve normal OpenSSH user/system configuration under -F.")
			for _, include := range includes {
				lines = append(lines, "Include "+quoteValue(include))
			}
		}
		configPath = filepath.Join(sshDir, "config")
		if err := f.writeSecure(configPath, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
			return fail(err)
		}
		prepared.ConfigPath = configPath
	}

	environment := f.baseEnv(spec.Env)
	if configPath != "" {
		environment = setEnv(environment, "HOME", tempDir)
		environment = setEnv(environment, "USERPROFILE", tempDir)
		if wrapper, err := f.sshWrapper(sshDir, configPath, home); err == nil && wrapper != "" {
			environment = prependPath(environment, wrapper)
		}
	}

	if len(askpass) > 0 {
		askpassEnv, err := f.writeAskpass(sshDir, safeID, askpass, environment)
		if err != nil {
			return fail(err)
		}
		for name, value := range askpassEnv {
			environment = setEnv(environment, name, value)
		}
	}

	environment = setEnv(environment, "TERM", etmodel.DefaultTerm)
	environment = setEnv(environment, "ET_NO_TELEMETRY", "1")

	sshOptions := append([]string(nil), options...)
	prepared.SSHOptions = sshOptions
	prepared.Env = environment
	prepared.Dir = home
	prepared.Args = buildArgs(cfg, sshOptions, f.userName())
	prepared.UserHost = cfg.UserHost(f.userName())
	return prepared, nil
}

// directiveSet is one endpoint's generated SSH configuration.
type directiveSet struct {
	options    []string
	config     []string
	identities []string
}

// authDirectives builds the key, agent, certificate, password, and preferred
// method settings for one endpoint. Comma-free values stay as --ssh-option
// values; anything with commas or spaces goes into the config file.
func (f Factory) authDirectives(sshDir, safeID, tag string, auth etmodel.Auth, home string, askpass *[]etmodel.AskpassPrompt) (directiveSet, error) {
	var set directiveSet
	if auth.UseSSHAgent {
		if auth.AgentSocket != "" {
			set.config = append(set.config, "IdentityAgent "+quoteValue(auth.AgentSocket))
		}
	} else {
		set.config = append(set.config, "IdentityAgent none")
	}

	if auth.PrivateKey != "" {
		keyPath := filepath.Join(sshDir, safeID+"-"+tag+"key")
		if err := f.writeSecure(keyPath, []byte(auth.PrivateKey)); err != nil {
			return set, err
		}
		set.identities = append(set.identities, keyPath)
		*askpass = etmodel.AddPassphraseEntry(*askpass, keyPath, auth.Passphrase)
	}
	if auth.Certificate != "" {
		certPath := filepath.Join(sshDir, safeID+"-"+tag+"cert.pub")
		if err := f.writeSecure(certPath, []byte(auth.Certificate)); err != nil {
			return set, err
		}
		set.options = append(set.options, "CertificateFile="+normalizePath(certPath, home))
	}
	for _, identity := range auth.IdentityFilePaths {
		path := identity
		if auth.UseSSHAgent {
			path = publicIdentitySelector(path)
		}
		set.identities = append(set.identities, path)
	}
	for _, identity := range set.identities {
		set.options = append(set.options, "IdentityFile="+normalizePath(identity, home))
	}

	hasPublicKey := len(set.identities) > 0
	if (auth.Method == etmodel.AuthKey || auth.Method == etmodel.AuthCertificate) && !hasPublicKey {
		set.options = append(set.options, "IdentityFile=none")
	}
	if auth.Method != etmodel.AuthAuto && !auth.UseSSHAgent && hasPublicKey || auth.UseSSHAgent && auth.IdentitiesOnly {
		set.options = append(set.options, "IdentitiesOnly=yes")
	}
	// Passwords are written by writeAskpass together with their prompt
	// matchers; authDirectives never writes a second, unreferenced copy.
	if auth.Method == etmodel.AuthPassword {
		set.options = append(set.options, "PubkeyAuthentication=no")
	}
	if preferred := etmodel.PreferredAuthentications(auth, auth.HasPassword(), hasPublicKey || auth.UseSSHAgent); preferred != "" {
		if strings.Contains(preferred, ",") {
			set.config = append(set.config, "PreferredAuthentications "+preferred)
		} else {
			set.options = append(set.options, "PreferredAuthentications="+preferred)
		}
	}
	set.options = append(set.options,
		"KbdInteractiveAuthentication=yes",
		"NumberOfPasswordPrompts=1",
	)
	return set, nil
}

// writeAskpass writes the prompt map and the wrapper ssh executes. The helper
// is this app's own executable in AskpassFlag mode, so no interpreter has to
// be on PATH.
func (f Factory) writeAskpass(sshDir, safeID string, entries []etmodel.AskpassPrompt, environment []string) (map[string]string, error) {
	program, err := f.program()
	if err != nil {
		return nil, fmt.Errorf("et: resolve the askpass helper: %w", err)
	}
	type diskEntry struct {
		Type       string   `json:"type"`
		Matchers   []string `json:"matchers"`
		SecretFile string   `json:"secretFile"`
	}
	disk := make([]diskEntry, 0, len(entries))
	for index, entry := range entries {
		if entry.Secret == "" {
			continue
		}
		secretPath := filepath.Join(sshDir, fmt.Sprintf("%s-askpass-%d.txt", safeID, index))
		if err := f.writeSecure(secretPath, []byte(entry.Secret+"\n")); err != nil {
			return nil, err
		}
		disk = append(disk, diskEntry{Type: entry.Type, Matchers: entry.Matchers, SecretFile: secretPath})
	}
	if len(disk) == 0 {
		return nil, nil
	}
	encoded, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("et: encode the askpass map: %w", err)
	}
	mapPath := filepath.Join(sshDir, "netcatty-et-askpass-map.json")
	if err := f.writeSecure(mapPath, append(encoded, '\n')); err != nil {
		return nil, err
	}

	wrapperPath := filepath.Join(sshDir, "netcatty-et-askpass.cmd")
	content := "@echo off\r\n\"" + strings.ReplaceAll(program, "\"", "\"\"") + "\" " + AskpassFlag + " %*\r\n"
	if runtime.GOOS != "windows" {
		wrapperPath = filepath.Join(sshDir, "netcatty-et-askpass.sh")
		content = "#!/bin/sh\nexec " + shellQuote(program) + " " + AskpassFlag + " \"$@\"\n"
	}
	if err := f.writeSecure(wrapperPath, []byte(content)); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(wrapperPath, 0o700)
	}
	display := envValue(environment, "DISPLAY")
	if display == "" {
		display = "netcatty:0"
	}
	return map[string]string{
		"SSH_ASKPASS":             wrapperPath,
		"SSH_ASKPASS_REQUIRE":     "force",
		"DISPLAY":                 display,
		"NETCATTY_ET_ASKPASS_MAP": mapPath,
	}, nil
}

// sshWrapper writes an ssh shim that always supplies the generated config.
// OpenSSH reads the user config from the account home, not $HOME, so this is
// the enforceable way to reach both the destination and the ProxyJump child.
func (f Factory) sshWrapper(sshDir, configPath, home string) (string, error) {
	realSSH, err := f.findSSH()
	if err != nil || realSSH == "" {
		// The destination's --ssh-option values still apply; only the jump
		// stanza loses its guaranteed path.
		return "", err
	}
	if !filepath.IsAbs(realSSH) {
		return "", errors.New("et: OpenSSH path is not absolute")
	}
	wrapperDir := filepath.Join(sshDir, "bin")
	if err := os.MkdirAll(wrapperDir, 0o700); err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		path := filepath.Join(wrapperDir, "ssh.cmd")
		content := "@echo off\r\n\"" + strings.ReplaceAll(realSSH, "\"", "\"\"") + "\" -F \"" +
			strings.ReplaceAll(configPath, "\"", "\"\"") + "\" %*\r\n"
		if err := f.writeSecure(path, []byte(content)); err != nil {
			return "", err
		}
		return wrapperDir, nil
	}
	path := filepath.Join(wrapperDir, "ssh")
	content := "#!/bin/sh\nexec " + shellQuote(realSSH) + " -F " + shellQuote(configPath) + " \"$@\"\n"
	if err := f.writeSecure(path, []byte(content)); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o700)
	return wrapperDir, nil
}

// writeSecure creates a 0600 file and applies the platform's ACL hardening.
func (f Factory) writeSecure(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("et: write %s: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return fmt.Errorf("et: write %s: %w", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("et: write %s: %w", filepath.Base(path), err)
	}
	_ = os.Chmod(path, 0o600)
	if f.HardenACL != nil {
		f.HardenACL(path)
	} else {
		hardenACL(path)
	}
	return nil
}

// hardenACL removes inherited access on Windows so another local account
// cannot read a generated secret.
func hardenACL(path string) {
	if runtime.GOOS != "windows" {
		return
	}
	name := ""
	if current, err := user.Current(); err == nil {
		name = current.Username
	}
	if name == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "icacls", path, "/inheritance:r", "/grant:r", name+":F").Run()
}

// discoverIdentities lists conventional private keys in the user's ~/.ssh.
func (f Factory) discoverIdentities(sshDir string) []string {
	readDir := f.ReadDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	entries, err := readDir(sshDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !sshKeyName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.IsDir() {
			continue
		}
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool {
		return identityRank(names[i]) < identityRank(names[j]) || identityRank(names[i]) == identityRank(names[j]) && names[i] < names[j]
	})
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, filepath.Join(sshDir, name))
	}
	return paths
}

var identityOrder = []string{"id_ed25519", "id_ecdsa", "id_ecdsa_sk", "id_rsa", "id_dsa", "identity"}

func identityRank(name string) int {
	for index, candidate := range identityOrder {
		if name == candidate {
			return index
		}
	}
	return len(identityOrder)
}

// sshKeyName reports the conventional private-key names, excluding public
// keys, config, and known-hosts files.
func sshKeyName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".pub") {
		return false
	}
	if name == "config" || name == "known_hosts" || name == "authorized_keys" ||
		strings.HasSuffix(name, "_known_hosts") {
		return false
	}
	return strings.HasPrefix(name, "id_") || name == "identity"
}

// includes returns the real user and system ssh_config paths that exist.
func (f Factory) includes(home string) []string {
	exists := f.Exists
	if exists == nil {
		exists = func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && !info.IsDir()
		}
	}
	var found []string
	if path := filepath.Join(home, ".ssh", "config"); exists(path) {
		found = append(found, path)
	}
	if runtime.GOOS == "windows" {
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		if path := filepath.Join(programData, "ssh", "ssh_config"); exists(path) {
			found = append(found, path)
		}
	} else if exists("/etc/ssh/ssh_config") {
		found = append(found, "/etc/ssh/ssh_config")
	}
	return found
}

func (f Factory) homeDir() string {
	if f.HomeDir != nil {
		return f.HomeDir()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func (f Factory) userName() string {
	if f.UserName != nil {
		return f.UserName()
	}
	if current, err := user.Current(); err == nil && current.Username != "" {
		return current.Username
	}
	if name := os.Getenv("USERNAME"); name != "" {
		return name
	}
	return os.Getenv("USER")
}

func (f Factory) program() (string, error) {
	if f.Program != nil {
		return f.Program()
	}
	return os.Executable()
}

func (f Factory) findSSH() (string, error) {
	if f.FindSSH != nil {
		return f.FindSSH()
	}
	return exec.LookPath("ssh")
}

func (f Factory) baseEnv(extra []string) []string {
	base := os.Environ()
	if f.BaseEnv != nil {
		base = f.BaseEnv()
	}
	return mergeEnv(base, extra)
}

// buildArgs assembles the et argv from the prepared options.
func buildArgs(cfg etmodel.Config, sshOptions []string, fallbackUser string) []string {
	args := make([]string, 0, 4+2*len(sshOptions))
	if cfg.ETPort != 0 && cfg.ETPort != etmodel.DefaultETPort {
		args = append(args, "-p", fmt.Sprint(cfg.ETPort))
	}
	for _, option := range sshOptions {
		args = append(args, "--ssh-option", option)
	}
	if cfg.Jump != nil {
		jump := cfg.Jump.Normalized()
		jumpUser := jump.Username
		if jumpUser == "" {
			jumpUser = fallbackUser
		}
		args = append(args, "--jumphost", targetUser(jumpUser, jump.Hostname), "--jport", fmt.Sprint(jump.ETPort))
	}
	args = append(args, cfg.UserHost(fallbackUser))
	return args
}

func targetUser(username, hostname string) string {
	if username == "" {
		return hostname
	}
	return username + "@" + hostname
}

// optionsKnownHosts returns the destination UserKnownHostsFile value.
func optionsKnownHosts(options []string) string {
	for _, option := range options {
		if value, ok := strings.CutPrefix(option, "UserKnownHostsFile="); ok {
			return value
		}
	}
	return ""
}

func emptyKnownHosts(options []string) string {
	for _, option := range options {
		if value, ok := strings.CutPrefix(option, "GlobalKnownHostsFile="); ok {
			return value
		}
	}
	return ""
}

// translateBundleError maps bundle failures onto the ET vocabulary.
func translateBundleError(err error) error {
	var typed *bundle.Error
	if errors.As(err, &typed) {
		switch typed.Kind {
		case bundle.ErrorChecksumMismatch:
			return fmt.Errorf("%w: %v", etmodel.ErrChecksumMismatch, err)
		case bundle.ErrorUnsupportedPlatform:
			return fmt.Errorf("%w: %v", etmodel.ErrPlatformUnsupported, err)
		default:
			return fmt.Errorf("%w: %v", etmodel.ErrBundleUnavailable, err)
		}
	}
	return err
}

// transport is one running ET Session.
type transport struct {
	process  Process
	prepared *Prepared

	mu       sync.Mutex
	closed   bool
	hasEnd   bool
	end      appsession.End
	exitCode int

	done        chan struct{}
	processDone chan struct{}
	closeOnce   sync.Once
	finishOnce  sync.Once
}

func (t *transport) Read(p []byte) (int, error)  { return t.process.Read(p) }
func (t *transport) Write(p []byte) (int, error) { return t.process.Write(p) }
func (t *transport) Resize(cols, rows int) error { return t.process.Resize(cols, rows) }

// supervise waits for the client. It deliberately does not read: the Session's
// reader owns the byte stream, and a second reader would steal output.
func (t *transport) supervise() {
	code, waitErr := t.process.Wait()
	defer close(t.processDone)
	end := appsession.End{Reason: appsession.EndReasonExited, ExitCode: code, HasExitCode: true}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	switch {
	case closed:
		end = appsession.End{Reason: appsession.EndReasonClosed}
	case waitErr != nil || code != 0:
		end = appsession.End{Reason: appsession.EndReasonError, ExitCode: code, HasExitCode: true}
	}
	t.finish(end, code)
}

func (t *transport) finish(end appsession.End, exitCode int) {
	t.finishOnce.Do(func() {
		t.mu.Lock()
		if !t.hasEnd {
			t.hasEnd = true
			t.end = end
			t.exitCode = exitCode
		}
		t.mu.Unlock()
		t.prepared.Cleanup()
		close(t.done)
	})
}

func (t *transport) Wait() (int, error) {
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitCode, nil
}

func (t *transport) End() appsession.End {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hasEnd {
		return t.end
	}
	return appsession.End{Reason: appsession.EndReasonError}
}

func (t *transport) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		_ = t.process.Close()
		t.finish(appsession.End{Reason: appsession.EndReasonClosed}, 0)
	})
	select {
	case <-t.processDone:
	case <-time.After(2 * time.Second):
		_ = t.process.Kill()
		select {
		case <-t.processDone:
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

// Askpass is the helper invoked by ssh. It loads the prompt map, picks the
// saved secret, and writes it without a trailing newline duplication.
func Askpass(prompt string, mapPath string, output io.Writer) error {
	if mapPath == "" {
		return nil
	}
	raw, err := os.ReadFile(mapPath)
	if err != nil {
		return nil
	}
	var entries []struct {
		Type       string   `json:"type"`
		Matchers   []string `json:"matchers"`
		SecretFile string   `json:"secretFile"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	prompts := make([]etmodel.AskpassPrompt, 0, len(entries))
	for _, entry := range entries {
		secret, err := os.ReadFile(entry.SecretFile)
		if err != nil {
			continue
		}
		prompts = append(prompts, etmodel.AskpassPrompt{
			Type:     entry.Type,
			Matchers: entry.Matchers,
			Secret:   strings.TrimRight(string(secret), "\r\n"),
		})
	}
	chosen := etmodel.PickAskpassEntry(prompt, prompts)
	if chosen == nil || chosen.Secret == "" {
		return nil
	}
	_, err = io.WriteString(output, chosen.Secret+"\n")
	return err
}

// IsAskpassInvocation reports whether the process was started as the helper.
func IsAskpassInvocation(args []string) bool {
	for _, arg := range args[1:] {
		if arg == AskpassFlag {
			return true
		}
	}
	return false
}

// RunAskpass runs the helper for the current process and returns an exit code.
func RunAskpass(args []string, output io.Writer) int {
	prompt := ""
	for _, arg := range args[1:] {
		if arg == AskpassFlag {
			continue
		}
		prompt += arg + " "
	}
	_ = Askpass(strings.TrimSpace(prompt), os.Getenv("NETCATTY_ET_ASKPASS_MAP"), output)
	return 0
}

// normalizePath makes a path absolute, expands a home prefix, and uses forward
// slashes so the generated ssh_config is readable on every platform.
func normalizePath(path, home string) string {
	raw := strings.TrimSpace(path)
	if home != "" {
		switch {
		case raw == "~":
			raw = home
		case strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`):
			raw = filepath.Join(home, raw[2:])
		}
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		absolute = raw
	}
	return filepath.ToSlash(absolute)
}

func quoteValue(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func publicIdentitySelector(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".pub") {
		return path
	}
	return path + ".pub"
}

func sanitizeID(id string) string {
	var builder strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func envValue(environment []string, name string) string {
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, name+"="); ok {
			return value
		}
	}
	return ""
}

func setEnv(environment []string, name, value string) []string {
	prefix := name + "="
	for index, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			environment[index] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

func mergeEnv(base, extra []string) []string {
	result := append([]string(nil), base...)
	for _, entry := range extra {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		result = setEnv(result, name, value)
	}
	return result
}

// prependPath adds a directory to PATH, creating PATH when absent.
func prependPath(environment []string, directory string) []string {
	for index, entry := range environment {
		if strings.HasPrefix(entry, "PATH=") || strings.HasPrefix(entry, "Path=") {
			current := entry[strings.IndexByte(entry, '=')+1:]
			if current == "" {
				environment[index] = entry[:strings.IndexByte(entry, '=')+1] + directory
			} else {
				environment[index] = entry[:strings.IndexByte(entry, '=')+1] + directory + string(os.PathListSeparator) + current
			}
			return environment
		}
	}
	return append(environment, "PATH="+directory)
}
