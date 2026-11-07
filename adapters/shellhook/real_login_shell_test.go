package shellhook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

// loginShellReadyMarker is printed by the isolated startup files these tests
// install. It proves a login shell reached its command loop without receiving
// a remote command.
const loginShellReadyMarker = "netcatty-login-shell-ready"

// lookupShell returns the real shell at PATH or skips the test.
func lookupShell(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed", name)
	}
	return path
}

// loginShellEnv gives a non-POSIX login shell an isolated home whose startup
// file prints loginShellReadyMarker. Nothing is written to the user's home.
func loginShellEnv(t *testing.T, name, shellPath, home string) []string {
	t.Helper()
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"SHELL=" + shellPath,
	}
	switch name {
	case "xonsh":
		rcDir := filepath.Join(home, ".config", "xonsh", "rc.d")
		if err := os.MkdirAll(rcDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rcDir, "00-netcatty-test.xsh"),
			[]byte("print(\""+loginShellReadyMarker+"\")\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return append(env, "XONSHRC_DIR="+rcDir)
	default:
		// csh and tcsh read ~/.cshrc for every shell, including a login shell.
		if err := os.WriteFile(filepath.Join(home, ".cshrc"),
			[]byte("echo "+loginShellReadyMarker+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return env
	}
}

// TestNonPOSIXLoginShellsRejectThePOSIXWrapper reproduces both halves of the
// sshd contract for xonsh, csh, and tcsh. sshd runs a RemoteCommand through
// the user's login shell as `<login-shell> -c <command>`; the generated POSIX
// wrapper is a parse error there (xonsh reads Python, csh/tcsh reject the
// newline inside single quotes). The transport therefore withdraws the
// wrapper for these shells, and sshd's empty RemoteCommand starts the plain
// login shell, which must still give a usable prompt and run a typed command.
func TestNonPOSIXLoginShellsRejectThePOSIXWrapper(t *testing.T) {
	cases := []struct {
		name    string
		resolve func(*testing.T) string
		// parseError is the diagnostic sshd would see from `<shell> -c`.
		parseError string
	}{
		{name: "xonsh", resolve: xonshPath, parseError: "SyntaxError"},
		{name: "csh", resolve: func(t *testing.T) string { return lookupShell(t, "csh") }, parseError: "Unmatched"},
		{name: "tcsh", resolve: func(t *testing.T) string { return lookupShell(t, "tcsh") }, parseError: "Unmatched"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			shellPath := testCase.resolve(t)
			// The developer's shell integration must not change the result.
			for _, key := range []string{
				"NETCATTY_HOOK", "NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR",
				"NETCATTY_HOOK_FILE", "ATUIN_SESSION", "STARSHIP_SHELL",
				"STARSHIP_SESSION_KEY", "ITERM_SHELL_INTEGRATION_INSTALLED",
			} {
				unsetEnv(t, key)
			}
			unsetXonshHookEnv(t)

			plan, failed := NewPlanner(WithPlatform("freebsd")).Plan(appsession.StartSpec{
				Kind: appsession.KindSSH,
			})
			if failed {
				t.Fatal("the planner reported a hook failure")
			}
			if plan.Cleanup != nil {
				defer plan.Cleanup()
			}
			wrapper := plan.Spec.RemoteCommand
			if wrapper == "" {
				t.Fatal("the SSH planner produced no wrapper")
			}
			// This is the decision the SSH transport makes before opening the
			// terminal, from the login shell the SFTP probe found.
			if effective := RemoteCommandForLoginShell(wrapper, shellPath); effective != "" {
				t.Fatalf("the transport would send the POSIX wrapper to %s", shellPath)
			}

			home := t.TempDir()
			workDir := t.TempDir()
			env := loginShellEnv(t, testCase.name, shellPath, home)

			// sshd with the planned wrapper: `<login-shell> -c <wrapper>`.
			command := exec.Command(shellPath, "-c", wrapper)
			command.Dir = workDir
			command.Env = shellEnv(env)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("%s accepted the POSIX wrapper: %q", shellPath, output)
			}
			if _, ok := err.(*exec.ExitError); !ok {
				t.Fatalf("run %s -c: %v", shellPath, err)
			}
			if !strings.Contains(string(output), testCase.parseError) {
				t.Fatalf("%s -c output = %q, want a %q parse failure",
					shellPath, output, testCase.parseError)
			}

			// sshd with the withdrawn command: the plain login shell, no -c.
			// A Session must still open and accept input.
			process := startShell(t, shellPath, []string{"-l"}, workDir, env)
			process.waitFor(t, "the login-shell startup", func(output string) bool {
				return strings.Contains(output, loginShellReadyMarker)
			})
			marker := "netcatty_login_shell_usable_42"
			process.typeLine(t, "echo "+marker)
			process.waitFor(t, "the typed command output", func(output string) bool {
				// Once for the terminal echo of the typed line, once for the
				// command's own output.
				return strings.Count(output, marker) >= 2
			})
		})
	}
}

// TestRealXonshNonInteractiveChildStaysSilent is the redirected-output
// regression for the xonsh hook. A child such as `xonsh -c 'print(...)'`
// sources the session rc.d file whenever it inherits XONSHRC_DIR. It must stay
// completely silent on stdout, including when NETCATTY_HOOK is set: the skip
// marker that an interactive shell may legitimately emit would corrupt the
// child's redirected output.
func TestRealXonshNonInteractiveChildStaysSilent(t *testing.T) {
	path := xonshPath(t)
	for _, key := range []string{
		"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
		"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
		"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
	} {
		unsetEnv(t, key)
	}
	unsetXonshHookEnv(t)
	home := t.TempDir()
	plan := localPlan(t, path, []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
	})
	rcDir := ""
	for _, entry := range plan.Spec.Env {
		if value, ok := strings.CutPrefix(entry, "XONSHRC_DIR="); ok {
			rcDir = value
		}
	}
	if rcDir == "" {
		t.Fatal("the planner installed no XONSHRC_DIR")
	}
	directories := strings.Split(rcDir, string(os.PathListSeparator))
	hookDir := directories[len(directories)-1]
	hookFile := filepath.Join(hookDir, "hook.xsh")
	if contents, err := os.ReadFile(hookFile); err != nil {
		t.Fatalf("the planner wrote no xonsh hook: %v", err)
	} else if !strings.Contains(string(contents), "on_precommand") {
		t.Fatalf("the xonsh hook file has no handlers: %q", contents)
	}
	// A sibling startup file proves xonsh really loads this rc.d directory for
	// a -c child; otherwise the silence below would be vacuous.
	markerFile := filepath.Join(t.TempDir(), "xonsh-rc-ran")
	if err := os.WriteFile(filepath.Join(hookDir, "00-control.xsh"),
		[]byte(fmt.Sprintf("open(%q, \"w\").write(\"ran\")\n", markerFile)), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name  string
		extra []string
	}{
		{name: "without the hook environment"},
		{name: "inheriting NETCATTY_HOOK", extra: []string{"NETCATTY_HOOK=1"}},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			_ = os.Remove(markerFile)
			environment := append(append([]string{}, plan.Spec.Env...), testCase.extra...)
			command := exec.Command(path, "-c", `print("plain-output")`)
			command.Dir = t.TempDir()
			command.Env = shellEnv(environment)
			stdout, err := command.Output()
			if err != nil {
				message := ""
				if exit, ok := err.(*exec.ExitError); ok {
					message = string(exit.Stderr)
				}
				t.Fatalf("xonsh -c failed: %v (stderr %q)", err, message)
			}
			if string(stdout) != "plain-output\n" {
				t.Fatalf("xonsh -c stdout = %q, want only the program's output", stdout)
			}
			if strings.ContainsAny(string(stdout), "\x1b\x07") {
				t.Fatalf("xonsh -c leaked a control sequence: %q", stdout)
			}
			if _, err := os.Stat(markerFile); err != nil {
				t.Fatalf("xonsh -c did not source the rc.d directory: %v", err)
			}
		})
	}
}

// nestedSession is a real hooked outer shell driven through the Session stack.
type nestedSession struct {
	service *appsession.Service
	session *appsession.Session
	events  chan appsession.CommandEvent
	workDir string
}

// openNestedSession starts the outer shell with the real hook, waits for its
// first prompt, and proves the hook is authoritative with one warm-up command.
func openNestedSession(t *testing.T, outerName, outerPath string, env []string) *nestedSession {
	t.Helper()
	events := make(chan appsession.CommandEvent, 16)
	service := appsession.NewService(localpty.Factory{},
		appsession.WithCaptureHook(NewPlanner(WithDefaultShell(func() (string, error) {
			return outerPath, nil
		}))),
		appsession.WithCommandCompletionHook(func(event appsession.CommandEvent) {
			events <- event
		}),
	)
	t.Cleanup(service.CloseAll)
	workDir := t.TempDir()
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: []string{outerPath},
		Dir:     workDir,
		Env:     env,
		Cols:    100, Rows: 40,
	})
	if err != nil {
		t.Fatalf("open the outer %s: %v", outerName, err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
		}
	})
	if !waitCondition(func() bool { return session.Snapshot().CWD != "" }) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the %s prompt never arrived: snapshot=%+v screen=%q",
			outerName, session.Snapshot(), text.Text)
	}
	warmup := "echo netcatty_nested_warmup_2"
	service.SendInput([]model.SessionID{session.ID()}, []byte(warmup+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_nested_warmup_2")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the outer warm-up never ran; screen=%q", text.Text)
	}
	awaitTierOne(t, events, warmup)
	return &nestedSession{service: service, session: session, events: events, workDir: workDir}
}

// runNestedShellCase types the nested shell into the hooked outer shell,
// captures one inner command, exits the nested shell, and proves the outer
// shell's hook is restored. The inner prompt marker is printed by the nested
// shell's own startup file; the outer event proves the handoff happened.
func (n *nestedSession) runNestedShellCase(t *testing.T, name, startCommand, readyMarker, innerCommand string) {
	t.Helper()
	service, session, events := n.service, n.session, n.events
	service.SendInput([]model.SessionID{session.ID()}, []byte(startCommand+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, readyMarker)
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the nested %s never ran its startup: %q", name, text.Text)
	}

	// The outer command is finalized when the nested prompt appears, before
	// the nested shell exits. Its finish and exit are therefore unknown; it
	// must not borrow the nested shell's later exit or duration.
	outer := awaitEvent(t, events)
	if outer.Command != startCommand || outer.Shell != "zsh" ||
		outer.CaptureTier != domainhistory.CaptureTierHook {
		t.Fatalf("outer completion = %+v", outer)
	}
	if outer.HasExitCode {
		t.Fatalf("the outer command borrowed an exit code: %+v", outer)
	}
	if !outer.FinishedAt.IsZero() {
		t.Fatalf("the outer command borrowed a finish time: %+v", outer)
	}
	if outer.CWD == "" {
		t.Fatalf("the outer hook never reported its cwd: %+v", outer)
	}

	// An inner command has no hook. It must still be captured, as Tier 3 with
	// no cwd or exit code, instead of being lost behind the outer hook.
	service.SendInput([]model.SessionID{session.ID()}, []byte(innerCommand+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, innerCommand)
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the nested %s command never ran: %q", name, text.Text)
	}
	inner := awaitEvent(t, events)
	if inner.Command != innerCommand || inner.CaptureTier != domainhistory.CaptureTierFallback {
		t.Fatalf("inner completion = %+v, want Tier 3 for %q", inner, innerCommand)
	}
	if inner.CWD != "" {
		t.Fatalf("the inner command borrowed a cwd: %+v", inner)
	}
	if inner.HasExitCode {
		t.Fatalf("the inner command invented an exit code: %+v", inner)
	}

	// Leaving the nested shell resumes the outer hook. The last inner line is
	// captured once and never borrows a real exit code. Its finish is whatever
	// the last nested output allowed: the outer handshake finalizes it with an
	// unknown finish, unless a prompt-like shutdown line already settled it.
	service.SendInput([]model.SessionID{session.ID()}, []byte("exit\r"))
	left := awaitEvent(t, events)
	if left.Command != "exit" || left.CaptureTier != domainhistory.CaptureTierFallback {
		t.Fatalf("nested exit completion = %+v", left)
	}
	if left.HasExitCode {
		t.Fatalf("the nested exit invented an exit code: %+v", left)
	}
	t.Logf("nested %s exit fidelity: finish=%v", name, left.FinishedAt)

	// The next command belongs to the outer shell again and has its own Tier 1
	// finish and exit, proving the handshake restored the hook.
	after := "echo netcatty_outer_after_nested_42"
	service.SendInput([]model.SessionID{session.ID()}, []byte(after+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_outer_after_nested_42")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the outer shell did not resume: %q", text.Text)
	}
	restored := awaitTierOne(t, events, after)
	if !restored.HasExitCode || restored.ExitCode != 0 {
		t.Fatalf("the restored outer hook lost its exit: %+v", restored)
	}
	if restored.FinishedAt.IsZero() || restored.FinishedAt.Before(restored.StartedAt) {
		t.Fatalf("the restored outer hook lost its finish: %+v", restored)
	}
}

// TestRealHookNestedNushellCapturesInnerCommands runs a real hooked zsh, starts
// nushell inside it, and proves the inner commands are captured as Tier 3 and
// the outer row keeps its own unknown finish and exit.
func TestRealHookNestedNushellCapturesInnerCommands(t *testing.T) {
	outerPath, ok := availableShells()["zsh"]
	if !ok {
		t.Skip("zsh is not available")
	}
	nushell := nushellPath(t)
	for _, key := range []string{
		"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
		"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
		"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
		"NETCATTY_NU_PENDING",
	} {
		unsetEnv(t, key)
	}
	unsetXonshHookEnv(t)
	home := t.TempDir()
	env := append(startupEnv(t, "zsh", home), nuStartupEnv(t, home)...)
	env = append(env, "SHELL="+outerPath)
	nested := openNestedSession(t, "zsh", outerPath, env)
	nested.runNestedShellCase(t, "nushell",
		nushell+" -l", "netcatty-user-startup-loaded",
		"echo netcatty_nested_inner_42")
}

// TestRealHookNestedXonshCapturesInnerCommands is the same handoff for xonsh,
// whose default prompt ends with "@" and whose native OSC 7 must not leak into
// the outer capture.
func TestRealHookNestedXonshCapturesInnerCommands(t *testing.T) {
	outerPath, ok := availableShells()["zsh"]
	if !ok {
		t.Skip("zsh is not available")
	}
	xonsh := xonshPath(t)
	for _, key := range []string{
		"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
		"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
		"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
	} {
		unsetEnv(t, key)
	}
	unsetXonshHookEnv(t)
	home := t.TempDir()
	env := append(startupEnv(t, "zsh", home), xonshStartupEnv(t, home)...)
	env = append(env, "SHELL="+outerPath,
		"XONSHRC_DIR="+filepath.Join(home, ".config", "xonsh", "rc.d"))
	nested := openNestedSession(t, "zsh", outerPath, env)
	nested.runNestedShellCase(t, "xonsh",
		xonsh+" -l", "netcatty-user-startup-loaded",
		"echo netcatty_nested_inner_42")
}
