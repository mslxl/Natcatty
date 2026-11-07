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
package shellhook

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	appsession "github.com/mslxl/natcatty/application/session"
)

const (
	shellZsh  = "zsh"
	shellBash = "bash"
	shellFish = "fish"
)

// Planner rewrites a Session StartSpec so its interactive shell installs the
// Netcatty hook. It is stateless apart from its injected seams and safe for
// concurrent use.
type Planner struct {
	enabled      func() bool
	defaultShell func() (string, error)
	makeTempDir  func() (string, error)
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

// NewPlanner returns a Planner with the platform defaults.
func NewPlanner(opts ...Option) *Planner {
	planner := &Planner{
		defaultShell: systemDefaultShell,
		makeTempDir:  systemTempDir,
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
	if p == nil || runtime.GOOS == "windows" {
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
// explicit command that is not an interactive zsh, bash, or fish is left
// alone; a non-interactive invocation must not receive a hook.
func (p *Planner) localShell(spec appsession.StartSpec) (localShellInfo, bool) {
	if len(spec.Command) > 0 {
		path := spec.Command[0]
		args := append([]string(nil), spec.Command[1:]...)
		name, ok := supportedShell(filepath.Base(path))
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
	name, ok := supportedShell(filepath.Base(path))
	if !ok {
		return localShellInfo{}, false
	}
	return localShellInfo{
		name:        name,
		path:        path,
		userZDOTDIR: zdotdirFor(spec),
	}, true
}

// supportedShell maps a program name onto the hook's shell vocabulary.
func supportedShell(name string) (string, bool) {
	name = strings.TrimPrefix(name, "-")
	switch name {
	case shellZsh, shellBash, shellFish:
		return name, true
	default:
		return "", false
	}
}

// interactiveShellArgs rejects invocations that run a command or a script, or
// that tell the shell not to read a startup file. A user-supplied startup file
// is preserved by skipping injection instead of overriding it.
func interactiveShellArgs(name string, args []string) bool {
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

// systemTempDir creates the one private directory a local Session needs.
func systemTempDir() (string, error) {
	return os.MkdirTemp("", "netcatty-hook-")
}

// systemDefaultShell mirrors the local PTY adapter's resolution. Keeping the
// rule here lets the planner run before the adapter without importing it.
func systemDefaultShell() (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("shellhook: no POSIX shell on Windows")
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
