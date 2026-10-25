// Package launch opens local files with the operating system's associated
// application or with one chosen application. It is the only place in the
// tree that knows the platform's launch command; the application layer sees a
// small Launcher port instead.
package launch

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// Runner starts one command. The default starts it detached and returns as
// soon as it is running, so opening a file never blocks the UI thread until
// the editor exits.
type Runner func(ctx context.Context, name string, args ...string) error

// Launcher implements the application's launcher port.
type Launcher struct {
	goos      string
	run       Runner
	shellOpen func(path string) error
}

// Option customizes a Launcher.
type Option func(*Launcher)

// WithGOOS overrides the platform, so tests can exercise every command shape.
func WithGOOS(goos string) Option {
	return func(l *Launcher) { l.goos = goos }
}

// WithRunner replaces the command runner.
func WithRunner(run Runner) Option {
	return func(l *Launcher) { l.run = run }
}

// WithShellOpen replaces the Windows ShellExecute seam.
func WithShellOpen(open func(path string) error) Option {
	return func(l *Launcher) { l.shellOpen = open }
}

// New builds the platform launcher.
func New(options ...Option) *Launcher {
	launcher := &Launcher{goos: runtime.GOOS, run: runDetached, shellOpen: shellOpen}
	for _, option := range options {
		option(launcher)
	}
	return launcher
}

// OpenSystem opens path with the system's associated application: ShellExecute
// on Windows, `open` on macOS, and `xdg-open` elsewhere.
func (l *Launcher) OpenSystem(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("launch: empty path")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.goos == "windows" {
		return l.shellOpen(path)
	}
	if l.goos == "darwin" {
		return l.run(ctx, "open", path)
	}
	return l.run(ctx, "xdg-open", path)
}

// OpenApplication runs application with path. macOS uses `open -a` so a bundle
// name or path both work; other platforms run the executable directly.
func (l *Launcher) OpenApplication(ctx context.Context, application, path string) error {
	application = strings.TrimSpace(application)
	if application == "" {
		return errors.New("launch: empty application")
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("launch: empty path")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.goos == "darwin" {
		return l.run(ctx, "open", "-a", application, path)
	}
	return l.run(ctx, application, path)
}

// runDetached starts the command and reaps it in the background. Waiting in
// the caller would keep the UI on the hook until the editor exits; not
// waiting at all would leak the child on Unix.
func runDetached(_ context.Context, name string, args ...string) error {
	command := exec.Command(name, args...)
	hideConsole(command)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
