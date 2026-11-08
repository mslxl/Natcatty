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

	appsession "github.com/mslxl/natcatty/application/session"
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

	path, args, err := command(spec)
	if err != nil {
		return nil, err
	}

	terminal, err := pty.Start(pty.Options{
		Path: path,
		Args: args,
		Dir:  workingDir(spec.Dir),
		Env:  environment(spec.Env),
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
			return path, nil
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
