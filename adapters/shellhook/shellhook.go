// Package shellhook builds and injects the session-scoped shell hook that
// raises command capture to Tier 1. It owns the per-shell startup scripts and
// the one-time rewrites of a Session StartSpec; it performs no I/O beyond
// creating its own temporary directory, never edits or creates files in the
// user's home directory, and never runs a binary.
//
// The hook sources the user's normal startup first, then detects an existing
// OSC 133 integration and skips itself when one is active. A script that
// deliberately installs nothing announces the skip, so the Session immediately
// keeps its native or typed fallback instead of reserving a first command for
// a hook that will never speak. A Session whose hook cannot be planned opens
// unchanged and falls back to Tier 2 or 3 as well.
//
// A machine policy can refuse the -File script before its first statement runs
// (Group Policy script execution, AppLocker, or WDAC). -ExecutionPolicy Bypass
// overrides process and user policy but not a machine policy, and the script
// cannot report a refusal to load itself. The PowerShell launchers therefore
// run the same file once in a short-lived -NoProfile -NonInteractive child
// before starting the interactive shell. That child prints a private marker
// when the script reaches a decision and exits without it when the script is
// refused, unreadable, or broken. On failure the launcher announces the skip
// marker itself and starts a plain shell, so the Session stays usable, keeps
// Tier 2/3, and never waits for a refused -NoExit child to exit. The check
// costs one short-lived process per PowerShell Session and adds no profile
// load. POSIX hooks report their own refusals; cmd.exe has no hook to check.
package shellhook

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	appsession "github.com/mslxl/natcatty/application/session"
)

const (
	shellZsh        = "zsh"
	shellBash       = "bash"
	shellFish       = "fish"
	shellPowerShell = "powershell"
	shellNu         = "nu"
	shellXonsh      = "xonsh"
)

// Planner rewrites a Session StartSpec so its interactive shell installs the
// Netcatty hook. It is stateless apart from its injected seams and safe for
// concurrent use.
type Planner struct {
	enabled      func() bool
	defaultShell func() (string, error)
	makeTempDir  func() (string, error)
	platform     string
}

// Option customizes a Planner.
type Option func(*Planner)

// WithEnabled supplies the HistoryCaptureHookEnabled setting. A nil
// predicate enables the hook.
func WithEnabled(predicate func() bool) Option {
	return func(planner *Planner) { planner.enabled = predicate }
}

// WithDefaultShell replaces the platform shell resolver. The default mirrors
// the local PTY adapter's resolution so the planner can name the shell the
// adapter would start.
func WithDefaultShell(resolve func() (string, error)) Option {
	return func(planner *Planner) {
		if resolve != nil {
			planner.defaultShell = resolve
		}
	}
}

// WithTempDir replaces the temporary-directory factory. Tests use it to force
// a failure or to observe cleanup.
func WithTempDir(create func() (string, error)) Option {
	return func(planner *Planner) {
		if create != nil {
			planner.makeTempDir = create
		}
	}
}

// WithPlatform replaces the local operating system the planner sees. It exists
// so the capture matrix can assert the same POSIX mechanism on every BSD, and
// that Windows stays unsupported, without cross-compiling.
func WithPlatform(goos string) Option {
	return func(planner *Planner) {
		if goos != "" {
			planner.platform = goos
		}
	}
}

// NewPlanner returns a Planner with the platform defaults.
func NewPlanner(opts ...Option) *Planner {
	planner := &Planner{
		defaultShell: systemDefaultShell,
		makeTempDir:  systemTempDir,
		platform:     runtime.GOOS,
	}
	for _, opt := range opts {
		opt(planner)
	}
	return planner
}

// Plan applies the hook to a local or SSH Session. Every other transport and
// every startup Netcatty does not own is returned unchanged. The bool reports
// that the hook could not be installed; the caller keeps the Session usable
// and reports one capability notice per Host.
func (p *Planner) Plan(spec appsession.StartSpec) (appsession.CaptureHookPlan, bool) {
	unchanged := appsession.CaptureHookPlan{Spec: spec}
	if p == nil {
		return unchanged, false
	}
	if p.enabled != nil && !p.enabled() {
		return unchanged, false
	}
	switch spec.Kind {
	case appsession.KindLocal, "":
		return p.planLocal(spec)
	case appsession.KindSSH:
		if spec.RemoteCommand != "" || spec.AutoAttachTmux || spec.NetworkDevice {
			// The transport either runs a command of the user's choosing or
			// attaches to an already-running tmux session, so Netcatty does not
			// control the interactive shell startup.
			return unchanged, false
		}
		spec.RemoteCommand = remoteWrapperCommand()
		// The transport learns the peer's platform only after connecting. A
		// Windows OpenSSH peer needs a PowerShell wrapper instead of the POSIX
		// one, so the factory may choose the final command then. On POSIX
		// peers it must also prove the login shell can parse a POSIX command;
		// the wrapper is withdrawn for xonsh, csh, tcsh, nu, and unknown
		// shells.
		spec.CaptureHookRemote = true
		return appsession.CaptureHookPlan{Spec: spec, HookExpected: true}, false
	default:
		// ET, Telnet, and serial own their terminal bootstrap and fall back to
		// Tier 2 or 3.
		return unchanged, false
	}
}

// planLocal installs startup files in a session-scoped temporary directory.
// The user's own files are only sourced, never written.
func (p *Planner) planLocal(spec appsession.StartSpec) (appsession.CaptureHookPlan, bool) {
	unchanged := appsession.CaptureHookPlan{Spec: spec}
	shell, ok := p.localShell(spec)
	if !ok {
		return unchanged, false
	}
	if p.platform == "windows" && shell.name != shellPowerShell {
		// The Windows hook is the PowerShell session script. An explicit
		// zsh/bash/fish startup keeps its POSIX shell unchanged, exactly as
		// the planner did before PowerShell support.
		return unchanged, false
	}
	dir, err := p.makeTempDir()
	if err != nil {
		return unchanged, true
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	switch shell.name {
	case shellZsh:
		if err := writeZshStartup(dir); err != nil {
			cleanup()
			return unchanged, true
		}
		spec.Env = append(spec.Env,
			"NETCATTY_HOOK_DIR="+dir,
			"NETCATTY_USER_ZDOTDIR="+shell.userZDOTDIR,
			"ZDOTDIR="+dir,
		)
	case shellBash:
		rcPath := filepath.Join(dir, "bashrc")
		if err := os.WriteFile(rcPath, []byte(bashStartupScript()), 0o600); err != nil {
			cleanup()
			return unchanged, true
		}
		spec.Command = bashCommand(shell.path, shell.args, rcPath)
	case shellFish:
		hookPath := filepath.Join(dir, "hook.fish")
		if err := os.WriteFile(hookPath, []byte(fishHookText), 0o600); err != nil {
			cleanup()
			return unchanged, true
		}
		spec.Env = append(spec.Env, "NETCATTY_HOOK_FILE="+hookPath)
		spec.Command = fishCommand(shell.path, shell.args, shell.explicit)
	case shellPowerShell:
		hookPath := filepath.Join(dir, "hook.ps1")
		if err := os.WriteFile(hookPath, []byte(powershellHookText), 0o600); err != nil {
			cleanup()
			return unchanged, true
		}
		// The hook must not gate on [Environment]::UserInteractive: Win32-OpenSSH
		// starts an interactive shell in session 0 with a non-visible window
		// station, where that property is false. This marker is the launcher's
		// statement that the shell it starts is interactive. The state file
		// lets the bootstrap tell "the hook loaded and the user exited" apart
		// from "the hook never loaded" (a machine policy refusing -File), in
		// which case it starts a plain shell instead of ending the Session.
		spec.Env = append(spec.Env,
			"NETCATTY_HOOK_START=1",
			"NETCATTY_HOOK_STATE="+filepath.Join(dir, "hook-loaded"),
		)
		spec.Command = powershellCommand(shell.path, shell.args, hookPath)
	case shellNu:
		hookPath := filepath.Join(dir, "hook.nu")
		if err := os.WriteFile(hookPath, []byte(nuHookText), 0o600); err != nil {
			cleanup()
			return unchanged, true
		}
		// Nushell loads the user config before -e runs, so the hook is appended
		// after the user's own pre_execution and pre_prompt hooks.
		spec.Command = nuCommand(shell.path, shell.args, shell.explicit, hookPath)
	case shellXonsh:
		hookPath := filepath.Join(dir, "hook.xsh")
		if err := os.WriteFile(hookPath, []byte(xonshHookText), 0o600); err != nil {
			cleanup()
			return unchanged, true
		}
		// The hook file lives in a session rc.d directory appended to the end
		// of XONSHRC_DIR, so every user startup file still runs first.
		spec.Env = append(spec.Env, "XONSHRC_DIR="+xonshRCDir(spec, dir))
	default:
		cleanup()
		return unchanged, false
	}
	return appsession.CaptureHookPlan{Spec: spec, HookExpected: true, Cleanup: cleanup}, false
}

// localShellInfo is the interactive shell a local Session will start.
type localShellInfo struct {
	name string
	path string
	args []string
	// explicit reports that the StartSpec named the command, so the planner
	// must preserve its login choice rather than add the default login flag.
	explicit bool
	// userZDOTDIR is the ZDOTDIR the user's zsh startup expects.
	userZDOTDIR string
}

// localShell resolves and validates the shell a local Session will start. An
// explicit command that is not an interactive zsh, bash, fish, nushell, or
// xonsh is left alone; a non-interactive invocation must not receive a hook.
func (p *Planner) localShell(spec appsession.StartSpec) (localShellInfo, bool) {
	if len(spec.Command) > 0 {
		path := spec.Command[0]
		args := append([]string(nil), spec.Command[1:]...)
		name, ok := supportedShell(shellName(path))
		if !ok || !interactiveShellArgs(name, args) {
			return localShellInfo{}, false
		}
		return localShellInfo{
			name:        name,
			path:        path,
			args:        args,
			explicit:    true,
			userZDOTDIR: zdotdirFor(spec),
		}, true
	}

	path, err := p.defaultShell()
	if err != nil || path == "" {
		return localShellInfo{}, false
	}
	name, ok := supportedShell(shellName(path))
	if !ok {
		return localShellInfo{}, false
	}
	return localShellInfo{
		name:        name,
		path:        path,
		userZDOTDIR: zdotdirFor(spec),
	}, true
}

// supportedShell maps a program name onto the hook's shell vocabulary. The
// Windows executable suffix is optional and the name is case-insensitive,
// because a Windows OpenSSH DefaultShell and a local Windows path both spell
// PowerShell with different casing and a .exe suffix.
func supportedShell(name string) (string, bool) {
	name = strings.TrimPrefix(name, "-")
	lower := strings.ToLower(name)
	lower = strings.TrimSuffix(lower, ".exe")
	switch lower {
	case shellZsh, shellBash, shellFish, shellNu, shellXonsh:
		return lower, true
	case "pwsh", shellPowerShell:
		return shellPowerShell, true
	default:
		return "", false
	}
}

// shellName returns the final path element for a local path. Windows paths
// arrive with backslashes even on a test host that does not use them.
func shellName(path string) string {
	path = strings.ReplaceAll(path, `\`, "/")
	return path[strings.LastIndex(path, "/")+1:]
}

// RemoteLoginShellSupported reports whether an SSH login shell can receive the
// POSIX remote wrapper. Only zsh, bash, and fish qualify: their command
// language accepts the wrapper and Netcatty has a hook for them. Xonsh, csh,
// tcsh, nushell, and every unknown shell must open as the plain login shell
// and use Tier 2/3, because sshd runs the RemoteCommand through the login
// shell with -c and a POSIX script is a parse error there.
func RemoteLoginShellSupported(shellPath string) bool {
	name := filepath.Base(strings.TrimSpace(shellPath))
	name = strings.TrimPrefix(name, "-")
	switch name {
	case shellZsh, shellBash, shellFish:
		return true
	default:
		return false
	}
}

// RemoteCommandForLoginShell returns the command an SSH transport should run
// for the detected login shell. A supported shell receives the planned
// wrapper; every other answer, including an empty one when the login shell
// could not be established, keeps the plain interactive login shell.
func RemoteCommandForLoginShell(planned, loginShell string) string {
	if RemoteLoginShellSupported(loginShell) {
		return planned
	}
	return ""
}

// interactiveShellArgs rejects invocations that run a command or a script, or
// that tell the shell not to read a startup file. A user-supplied startup file
// is preserved by skipping injection instead of overriding it.
func interactiveShellArgs(name string, args []string) bool {
	if name == shellPowerShell {
		return interactivePowerShellArgs(args)
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-c" || arg == "--command" || arg == "-s":
			return false
		case arg == "--norc" || arg == "--noprofile" || arg == "--no-rcs":
			return false
		case arg == "-f" && name == shellZsh:
			return false
		case arg == "--no-config" || (arg == "-N" && name == shellFish):
			return false
		case name == shellFish && (arg == "--private" || fishPrivateShortFlag(arg)):
			// Private mode is explicitly out of scope: its history is not
			// persisted and the spec says never to install the hook there.
			return false
		case arg == "--rcfile" || strings.HasPrefix(arg, "--rcfile=") ||
			arg == "--init-file" || strings.HasPrefix(arg, "--init-file="):
			return false
		case name == shellNu && nuNonInteractiveArg(arg):
			// -c runs and exits, -e is the slot the hook needs, -n skips the
			// user config, and the remaining flags do not start a shell.
			return false
		case name == shellXonsh && xonshNonInteractiveArg(arg):
			// -c runs and exits, -n/--no-rc and --rc replace or skip the user
			// startup the hook must preserve, and the rest print and exit.
			return false
		case optionConsumesNextArg(name, arg):
			// The next word is this option's value, not a script path.
			index++
		case !strings.HasPrefix(arg, "-"):
			// A bare word names a script file; that shell is not interactive.
			return false
		}
	}
	return true
}

// nuNonInteractiveArg reports a Nushell flag that runs a command, skips the
// user config, or starts a non-shell mode. A combined short cluster carrying c,
// e, or n is rejected too, because -e must stay reserved for the hook.
func nuNonInteractiveArg(arg string) bool {
	switch arg {
	case "-c", "--commands", "-e", "--execute", "-n", "--no-config-file",
		"--stdin", "--mcp", "-h", "--help", "-v", "--version":
		return true
	}
	// The GNU long options also accept an attached value. -e is reserved for
	// the hook's own source command, so a user-supplied --execute= must be
	// rejected exactly like --execute.
	if strings.HasPrefix(arg, "--commands=") || strings.HasPrefix(arg, "--execute=") {
		return true
	}
	return shortOptionCarries(arg, "cen")
}

// xonshNonInteractiveArg reports an xonsh flag that runs a command, replaces or
// skips the user startup, or prints and exits.
func xonshNonInteractiveArg(arg string) bool {
	switch arg {
	case "-c", "--command", "-n", "--no-rc", "--norc", "--rc",
		"-V", "--version", "-h", "--help":
		return true
	}
	if strings.HasPrefix(arg, "--rc=") || strings.HasPrefix(arg, "--command=") {
		return true
	}
	return shortOptionCarries(arg, "cn")
}

// shortOptionCarries reports whether a short-option cluster such as -iln
// contains one of the rejected letters. Long options never qualify.
func shortOptionCarries(arg, letters string) bool {
	if len(arg) < 3 || !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return false
	}
	for _, letter := range arg[1:] {
		if strings.ContainsRune(letters, letter) {
			return true
		}
	}
	return false
}

// fishPrivateShortFlag reports whether a short-option word carries fish's -P
// private-mode flag, including a combined cluster such as -lP.
func fishPrivateShortFlag(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || arg == "-" {
		return false
	}
	return strings.Contains(arg[1:], "P")
}

// optionConsumesNextArg reports whether arg is an option whose value is the
// following word. Without this, `zsh -o extendedglob` or `bash -O extglob`
// would look like a bare script operand and silently lose the hook.
func optionConsumesNextArg(name, arg string) bool {
	switch {
	case name == shellFish && (arg == "-C" || arg == "--init-command"):
		return true
	case name == shellZsh && (arg == "-o" || arg == "--emulate"):
		return true
	case name == shellBash && arg == "-O":
		return true
	case name == shellNu && (arg == "-m" || arg == "--table-mode" ||
		arg == "--error-style" || arg == "-I" || arg == "--include-path" ||
		arg == "--config" || arg == "--env-config" || arg == "--config-home" ||
		arg == "--log-level" || arg == "--log-target" ||
		arg == "--mcp-transport" || arg == "--mcp-port" || arg == "--mcp-host"):
		return true
	case name == shellXonsh && (arg == "-D" || arg == "-st" || arg == "--shell-type"):
		return true
	}
	// A short-option cluster may end in a value-taking option, as in
	// `zsh -xo extendedglob`. Long options never take an attached value here.
	if len(arg) < 2 || strings.HasPrefix(arg, "--") {
		return false
	}
	switch name {
	case shellZsh:
		return arg[len(arg)-1] == 'o'
	case shellBash:
		return arg[len(arg)-1] == 'O'
	case shellFish:
		return arg[len(arg)-1] == 'C'
	case shellNu:
		return arg[len(arg)-1] == 'm' || arg[len(arg)-1] == 'I'
	}
	return false
}

// bashCommand starts bash non-login with the hook rc file. The login and
// profile files are sourced by that rc file, because a login bash ignores
// --rcfile. The GNU long options must come before the short ones: bash rejects
// a plan such as ["bash","-i"] rewritten to "bash -i --rcfile ... " with exit
// status 2, so --rcfile is placed immediately after the program name and an
// explicit -i is left in place (a repeated -i is harmless).
func bashCommand(path string, args []string, rcPath string) []string {
	command := make([]string, 0, len(args)+4)
	command = append(command, path, "--rcfile", rcPath)
	for _, arg := range args {
		switch arg {
		case "-l", "--login":
			continue
		}
		command = append(command, arg)
	}
	return append(command, "-i")
}

// fishCommand appends the hook to fish's startup command. Fish sources the
// user's config before --init-command runs, so the hook sees the user's own
// event handlers and can detect an existing integration.
func fishCommand(path string, args []string, explicit bool) []string {
	command := make([]string, 0, len(args)+4)
	command = append(command, path)
	if !explicit {
		command = append(command, "-l")
	}
	command = append(command, args...)
	return append(command, "--init-command", `source "$NETCATTY_HOOK_FILE"`)
}

// zdotdirFor returns the ZDOTDIR the user's zsh startup expects. Both ZDOTDIR
// and the HOME it falls back to come from the StartSpec's environment first,
// matching the local transport, and from the process environment second.
func zdotdirFor(spec appsession.StartSpec) string {
	zdotdir, home := os.Getenv("ZDOTDIR"), os.Getenv("HOME")
	for _, entry := range spec.Env {
		key, candidate, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case "ZDOTDIR":
			zdotdir = candidate
		case "HOME":
			home = candidate
		}
	}
	if zdotdir != "" {
		return zdotdir
	}
	if home != "" {
		return home
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

// nuCommand starts Nushell with the hook file as the last -e command. Nushell
// runs -e after loading the user env and config, then enters its interactive
// loop, so the user hooks are already installed when the hook appends to them.
func nuCommand(path string, args []string, explicit bool, hookPath string) []string {
	command := make([]string, 0, len(args)+5)
	command = append(command, path)
	if !explicit {
		command = append(command, "-l")
	}
	command = append(command, args...)
	return append(command, "-e", fmt.Sprintf("source %q", hookPath))
}

// xonshRCDir appends the session directory to XONSHRC_DIR. Xonsh loads
// XONSHRC first and then every .xsh or .py file in XONSHRC_DIR in sorted
// order, so putting the hook in a directory of its own keeps every user
// startup file ahead of it. When the user already set XONSHRC_DIR it is
// preserved; otherwise xonsh's documented default directories are reproduced.
func xonshRCDir(spec appsession.StartSpec, hookDir string) string {
	if value, set := lookupEnv(spec, "XONSHRC_DIR"); set && value != "" {
		return strings.Join([]string{value, hookDir}, string(os.PathListSeparator))
	}
	systemConfig := envValue(spec, "XONSH_SYS_CONFIG_DIR")
	if systemConfig == "" {
		systemConfig = "/etc/xonsh"
	}
	configDir := envValue(spec, "XONSH_CONFIG_DIR")
	if configDir == "" {
		configHome := envValue(spec, "XDG_CONFIG_HOME")
		if configHome == "" {
			if home := envValue(spec, "HOME"); home != "" {
				configHome = filepath.Join(home, ".config")
			}
		}
		if configHome != "" {
			configDir = filepath.Join(configHome, "xonsh")
		}
	}
	dirs := []string{filepath.Join(systemConfig, "rc.d")}
	if configDir != "" {
		dirs = append(dirs, filepath.Join(configDir, "rc.d"))
	}
	dirs = append(dirs, hookDir)
	return strings.Join(dirs, string(os.PathListSeparator))
}

// lookupEnv returns the last value of key in the StartSpec environment,
// falling back to the process environment. set reports that either source
// defined it, so an explicitly empty value is not mistaken for unset.
func lookupEnv(spec appsession.StartSpec, key string) (value string, set bool) {
	value, set = os.LookupEnv(key)
	for _, entry := range spec.Env {
		candidateKey, candidate, ok := strings.Cut(entry, "=")
		if ok && candidateKey == key {
			value, set = candidate, true
		}
	}
	return value, set
}

func envValue(spec appsession.StartSpec, key string) string {
	value, _ := lookupEnv(spec, key)
	return value
}

// systemTempDir creates the one private directory a local Session needs.
func systemTempDir() (string, error) {
	return os.MkdirTemp("", "netcatty-hook-")
}

// systemDefaultShell mirrors the local PTY adapter's resolution. Keeping the
// rule here lets the planner run before the adapter without importing it. On
// Windows the adapter prefers PowerShell and falls back to cmd.exe; the
// planner accepts only the PowerShell candidates and leaves cmd alone.
func systemDefaultShell() (string, error) {
	if runtime.GOOS == "windows" {
		for _, candidate := range []string{"pwsh.exe", "powershell.exe", os.Getenv("COMSPEC"), "cmd.exe"} {
			if candidate == "" {
				continue
			}
			if path, err := exec.LookPath(candidate); err == nil {
				return path, nil
			}
		}
		return "", errors.New("shellhook: no shell found on Windows")
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		if path, err := exec.LookPath(shell); err == nil {
			return path, nil
		}
	}
	fallback := "/bin/sh"
	if runtime.GOOS == "darwin" {
		fallback = "/bin/zsh"
	}
	path, err := exec.LookPath(fallback)
	if err != nil {
		return "", err
	}
	return path, nil
}
