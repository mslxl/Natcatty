// Package localpty implements the session transport port for local shells. It
// resolves the platform shell and opens it in a pseudo-terminal through
// platform/pty; the application layer sees only a Transport.
package localpty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mslxl/natcatty/application/lens/integration"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/platform/processcwd"
	"github.com/mslxl/natcatty/platform/pty"
)

// Factory opens local shells. It has no state and is safe for concurrent use.
type Factory struct{}

// Open starts the shell of spec, or the platform's default shell when spec
// names none, in a pseudo-terminal. A zero Cols or Rows uses the PTY's default
// size; the Service normally supplies one.
func (Factory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	env := environment(spec.Env)
	var path string
	var args []string
	if len(spec.Command) == 0 {
		// The default Local Terminal owns the interactive shell, so install the
		// same OSC 133/7 integration used by the Lens capture path. Explicit
		// commands are left untouched: callers use those for one-shot scripts,
		// fixtures, and shells that provide their own integration.
		shell, err := defaultShell()
		if err != nil {
			return nil, err
		}
		// Name the shell the Session actually runs, not the one it replaced, so
		// a nested $SHELL and the tools that read it agree with the terminal.
		env = append(env, "SHELL="+shell)
		path, args, env = integration.Shell([]string{shell}, env)
		// integration.Shell names the program separately; the PTY wants argv,
		// including argv[0]. Without it a bash reads --rcfile as the name of a
		// script and exits at once instead of starting the integrated shell.
		args = append([]string{path}, args...)
	} else {
		var err error
		path, args, err = command(spec)
		if err != nil {
			return nil, err
		}
	}

	terminal, err := pty.Start(pty.Options{
		Path: path,
		Args: args,
		Dir:  workingDir(spec.Dir),
		Env:  env,
		Cols: spec.Cols,
		Rows: spec.Rows,
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = terminal.Close()
		return nil, err
	}
	return &transport{pty: terminal}, nil
}

// command resolves the program to start. An explicit Command keeps its
// arguments; the default shell is a login shell on Unix, as terminal apps
// start it.
func command(spec appsession.StartSpec) (string, []string, error) {
	if len(spec.Command) > 0 {
		path, err := exec.LookPath(spec.Command[0])
		if err != nil {
			return "", nil, fmt.Errorf("local shell: %w", err)
		}
		return path, spec.Command, nil
	}

	shell, err := defaultShell()
	if err != nil {
		return "", nil, err
	}
	args := []string{shell}
	if runtime.GOOS != "windows" {
		args[0] = "-" + filepath.Base(shell)
	}
	return shell, args, nil
}

func defaultShell() (string, error) {
	if runtime.GOOS == "windows" {
		for _, candidate := range []string{"pwsh.exe", "powershell.exe", os.Getenv("COMSPEC"), "cmd.exe"} {
			if candidate == "" {
				continue
			}
			if path, err := exec.LookPath(candidate); err == nil {
				return path, nil
			}
		}
		return "", errors.New("local shell: no shell found")
	}

	if shell := os.Getenv("SHELL"); shell != "" {
		if path, err := exec.LookPath(shell); err == nil {
			return editableShell(path), nil
		}
	}
	fallback := "/bin/sh"
	if runtime.GOOS == "darwin" {
		fallback = "/bin/zsh"
	}
	path, err := exec.LookPath(fallback)
	if err != nil {
		return "", fmt.Errorf("local shell: %w", err)
	}
	return path, nil
}

// editableShell replaces a bash that cannot edit a command line with one that
// can. Nix's dev shells export a bash built without readline as $SHELL
// (`nix develop` does), and such a shell is unusable as a terminal: Tab
// inserts a literal tab because no completion exists, the \[ \] markers of a
// PS1 print literally because readline is what decodes them, and the prompt
// never redraws. Nothing the terminal sends can repair that, so the Session
// runs a bash that has line editing instead.
func editableShell(path string) string {
	if runtime.GOOS == "windows" || !isBash(path) || bashHasLineEditing(path) {
		return path
	}
	if replacement := interactiveBash(path); replacement != "" {
		return replacement
	}
	return path
}

func isBash(path string) bool {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	return name == "bash"
}

// bashHasLineEditing asks a bash whether it was built with readline by probing
// the programmable-completion option, which that build enables together with
// it; `shopt` answers without starting an interactive session.
func bashHasLineEditing(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--noprofile", "--norc", "-c", "shopt -q progcomp")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	return cmd.Run() == nil
}

// interactiveBash finds a bash with line editing: the first one on PATH (the
// broken build is normally a single early entry there, with the user's profile
// bash further along), then the usual profile locations.
func interactiveBash(skip string) string {
	name := "bash.exe"
	if runtime.GOOS != "windows" {
		name = "bash"
	}
	var candidates []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir != "" {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}
	if user := os.Getenv("USER"); user != "" {
		candidates = append(candidates, filepath.Join("/etc/profiles/per-user", user, "bin", name))
	}
	candidates = append(candidates,
		"/run/current-system/sw/bin/"+name,
		"/opt/homebrew/bin/"+name,
		"/usr/local/bin/"+name,
		"/usr/bin/"+name,
		"/bin/"+name,
	)
	for _, candidate := range candidates {
		if candidate == skip {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if bashHasLineEditing(candidate) {
			return candidate
		}
	}
	return ""
}

// workingDir returns spec's directory when it is a directory, else the user's
// home directory, matching the original's fallback.
func workingDir(dir string) string {
	if dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// environment starts from the app's environment and applies the terminal's
// variables, the UTF-8 locale default, and then the Session's overrides.
func environment(extra []string) []string {
	env := os.Environ()
	set := func(key, value string) {
		for index, entry := range env {
			if strings.HasPrefix(entry, key+"=") {
				env[index] = key + "=" + value
				return
			}
		}
		env = append(env, key+"="+value)
	}
	get := func(key string) string {
		for _, entry := range env {
			if value, ok := strings.CutPrefix(entry, key+"="); ok {
				return value
			}
		}
		return ""
	}

	set("TERM", "xterm-256color")
	set("COLORTERM", "truecolor")
	set("TERM_PROGRAM", "Natcatty")
	set("NATCATTY_LENSES", "1")
	// Keep command output parseable for Natcatty's command-lens renderer:
	// disable interactive pagers and force line-oriented build progress.
	set("PAGER", "cat")
	set("GIT_PAGER", "cat")
	set("GH_PAGER", "cat")
	set("MANPAGER", "cat")
	set("SYSTEMD_PAGER", "cat")
	set("AWS_PAGER", "")
	set("BAT_PAGING", "never")
	set("PSQL_PAGER", "cat")
	set("BUILDKIT_PROGRESS", "plain")
	set("CLICOLOR", "1")
	if runtime.GOOS != "windows" && !hasLocale(get("LANG"), get("LC_ALL"), get("LC_CTYPE")) {
		set("LANG", "en_US.UTF-8")
	}
	for _, entry := range extra {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			set(key, value)
		}
	}
	return env
}

// hasLocale reports whether the environment already names a locale. C and
// POSIX are the unset defaults, so they do not count; a configured non-UTF-8
// locale is the user's choice and is left alone.
func hasLocale(values ...string) bool {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && value != "C" && value != "POSIX" {
			return true
		}
	}
	return false
}

// transport adapts a PTY to the Session transport port.
type transport struct {
	pty *pty.PTY

	closeOnce sync.Once
	closeErr  error
}

// WorkingDirectory reports the shell process's current directory. It is the
// fallback for a local shell that never reports OSC 7; a remote transport has
// no such capability.
func (t *transport) WorkingDirectory() (string, error) {
	return processcwd.Of(t.pty.PID())
}

func (t *transport) Read(p []byte) (int, error)  { return t.pty.Read(p) }
func (t *transport) Write(p []byte) (int, error) { return t.pty.Write(p) }
func (t *transport) Resize(cols, rows int) error { return t.pty.Resize(cols, rows) }
func (t *transport) Wait() (int, error)          { return t.pty.Wait() }

const (
	// closeGrace bounds how long Close waits for a hung-up shell to exit
	// before killing it.
	closeGrace = 2 * time.Second
	// closeReapTimeout bounds the wait for the kill to be reaped, so a process
	// that cannot be reaped cannot wedge Session teardown forever.
	closeReapTimeout = 2 * time.Second
)

// Close hangs up the shell and waits for the process to exit, bounded by
// closeGrace; a shell that ignores the hangup is killed and reaped, bounded by
// closeReapTimeout. Session teardown calls Close before publishing its end, so
// returning only after the shell exits means Done implies the shell's writes
// have completed. Close is idempotent: concurrent and repeated calls share one
// teardown and never signal a reaped process.
func (t *transport) Close() error {
	t.closeOnce.Do(func() {
		t.closeErr = t.pty.Close()
		if waitExited(t.pty.Exited(), closeGrace) {
			return
		}
		_ = t.pty.Kill()
		_ = waitExited(t.pty.Exited(), closeReapTimeout)
	})
	return t.closeErr
}

// waitExited reports whether exited closed within timeout.
func waitExited(exited <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-exited:
		return true
	case <-timer.C:
		return false
	}
}
