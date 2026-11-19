package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/mslxl/natcatty/adapters/shellhook"
	appsession "github.com/mslxl/natcatty/application/session"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// writeConfiguredShell creates an executable file whose name is a shell the
// capture-hook planner recognizes. LookPath only resolves the path; nothing is
// executed.
func writeConfiguredShell(t *testing.T) string {
	t.Helper()
	name := "bash"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLocalSessionPlansTheConfiguredShellBeforeTheCaptureHook proves the real
// Service.Open path used by workspace restore, split, and RetryPane carries
// the configured custom shell into the capture-hook plan. The planner must
// wrap the user's shell, not the OS default; the old ordering planned the
// default first and the transport factory then saw a non-empty command and
// skipped the custom shell.
func TestLocalSessionPlansTheConfiguredShellBeforeTheCaptureHook(t *testing.T) {
	executable := writeConfiguredShell(t)
	settings := domainpreferences.Defaults()
	settings.Appearance.LocalShell = domainpreferences.LocalShell{
		Path: executable,
		Args: "-l --flag",
	}
	local := &factoryLocal{}
	service := appsession.NewService(
		newTransportFactory(local, nil, nil, nil, nil),
		appsession.WithLocalShell(func() []string {
			command, _ := settings.Appearance.LocalShell.Command(appsession.LocalShellExists)
			return command
		}),
		appsession.WithCaptureHook(shellhook.NewPlanner(
			// Force the POSIX hook even on a Windows test host; the configured
			// path is an existing executable, so LocalShellExists accepts it.
			shellhook.WithPlatform("linux"),
			shellhook.WithDefaultShell(func() (string, error) {
				return "/bin/definitely-not-the-configured-shell", nil
			}),
		)),
	)
	t.Cleanup(service.CloseAll)

	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if len(local.spec.Command) == 0 {
		t.Fatal("the transport received no command")
	}
	if local.spec.Command[0] != executable {
		t.Fatalf("launched argv = %q, want the configured shell %q", local.spec.Command, executable)
	}
	if slices.Contains(local.spec.Command, "/bin/definitely-not-the-configured-shell") {
		t.Fatalf("the OS default shell won over the configured shell: %q", local.spec.Command)
	}
	// The hook still planned around the configured shell: bash gets --rcfile
	// and the user's non-login arguments survive.
	if !slices.Contains(local.spec.Command, "--rcfile") {
		t.Fatalf("the capture hook did not plan the configured shell: %q", local.spec.Command)
	}
	if !slices.Contains(local.spec.Command, "--flag") {
		t.Fatalf("the configured arguments were lost: %q", local.spec.Command)
	}
}

// TestLocalSessionWithoutAConfiguredShellKeepsThePlannerDefault proves the
// resolver is additive: with no custom shell the planner still wraps the OS
// login shell as before.
func TestLocalSessionWithoutAConfiguredShellKeepsThePlannerDefault(t *testing.T) {
	local := &factoryLocal{}
	service := appsession.NewService(
		newTransportFactory(local, nil, nil, nil, nil),
		appsession.WithLocalShell(func() []string { return nil }),
		appsession.WithCaptureHook(shellhook.NewPlanner(
			shellhook.WithPlatform("linux"),
			shellhook.WithDefaultShell(func() (string, error) {
				// bash is planned into an explicit command, so the test can see
				// which shell won. zsh's hook only sets ZDOTDIR and leaves the
				// transport to start the OS login shell.
				return "/bin/bash", nil
			}),
		)),
	)
	t.Cleanup(service.CloseAll)

	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if len(local.spec.Command) == 0 || local.spec.Command[0] != "/bin/bash" {
		t.Fatalf("launched argv = %q, want the planner's OS shell", local.spec.Command)
	}
}
