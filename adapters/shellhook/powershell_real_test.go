package shellhook

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

// availablePowerShell returns a real PowerShell to exercise the hook. The
// hook's strings and quoting are covered without it; these tests prove the
// script itself runs, loads the profile, emits the private sequence, and
// cleans up after itself.
func availablePowerShell() string {
	if configured := os.Getenv("NETCATTY_TEST_PWSH"); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured
		}
	}
	for _, name := range []string{"pwsh", "pwsh.exe", "powershell.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

// powerShellStartupEnv creates an isolated home whose profile prints a marker,
// installs its own PSReadLine history handler, and leaves a distinctive prompt.
// The hook must leave that handler installed and still call the prompt.
func powerShellStartupEnv(t *testing.T, home string) []string {
	t.Helper()
	body := `[Console]::WriteLine('netcatty-user-startup-loaded')
# The hook must survive a profile that turns on strict mode.
Set-StrictMode -Version Latest
$global:NETCATTY_TEST_HISTORY_COUNT = 0
$global:NETCATTY_TEST_USER_PROMPT_RAN = 0
Import-Module PSReadLine -ErrorAction SilentlyContinue
Set-PSReadLineOption -AddToHistoryHandler {
    param([string]$line)
    $global:NETCATTY_TEST_HISTORY_COUNT = $global:NETCATTY_TEST_HISTORY_COUNT + 1
    return $true
}
function global:prompt {
    $global:NETCATTY_TEST_USER_PROMPT_RAN = $global:NETCATTY_TEST_USER_PROMPT_RAN + 1
    # A nested shell launched by a test may drop this variable, which makes the
    # outer prompt distinguishable from the child's on screen.
    if ($env:NETCATTY_TEST_OUTER_PROMPT -eq 'outer') { $netcatty_prefix = 'OUTERPROMPT' }
    else { $netcatty_prefix = 'USERPROMPT' }
    "$netcatty_prefix$($global:NETCATTY_TEST_USER_PROMPT_RAN)> "
}
`
	powerShellProfiles(t, home, body)
	return powerShellEnv(home)
}

// powerShellProfiles writes one profile body where every supported PowerShell
// edition looks for it. Only the edition in use reads its own file.
func powerShellProfiles(t *testing.T, home, body string) {
	t.Helper()
	for _, profile := range []string{
		filepath.Join(home, ".config", "powershell", "profile.ps1"),
		filepath.Join(home, "Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1"),
		filepath.Join(home, "Documents", "WindowsPowerShell", "Microsoft.PowerShell_profile.ps1"),
	} {
		if err := os.MkdirAll(filepath.Dir(profile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// powerShellEnv isolates a PowerShell's profile and PSReadLine data.
func powerShellEnv(home string) []string {
	return []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		// PSReadLine resolves its history through the OS user's data directory
		// on macOS, not through HOME; without this the tests would read and
		// write the developer's real console history.
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
	}
}

// answeringFactory gives a local Session the part of a terminal UI that the
// application layer deliberately does not provide: PSReadLine asks for the
// cursor position at startup and reads the answer from its input, so a test
// that types into a bare PTY must answer like xterm.js would.
type answeringFactory struct{ inner localpty.Factory }

func (f answeringFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	inner, err := f.inner.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &answeringTransport{inner: inner}, nil
}

type answeringTransport struct{ inner appsession.Transport }

func (t *answeringTransport) Read(p []byte) (int, error) {
	n, err := t.inner.Read(p)
	if n > 0 && bytes.Contains(p[:n], []byte("\x1b[6n")) {
		_, _ = t.inner.Write([]byte("\x1b[1;1R"))
	}
	return n, err
}

func (t *answeringTransport) Write(p []byte) (int, error) { return t.inner.Write(p) }
func (t *answeringTransport) Close() error                { return t.inner.Close() }
func (t *answeringTransport) Resize(cols, rows int) error { return t.inner.Resize(cols, rows) }
func (t *answeringTransport) Wait() (int, error) {
	if waiter, ok := t.inner.(appsession.ExitWaiter); ok {
		return waiter.Wait()
	}
	return 0, nil
}

// openHookedPowerShellSession opens an interactive PowerShell Session with the
// hook planner and the terminal stand-in, then waits for the first prompt.
func openHookedPowerShellSession(t *testing.T, path, home, workDir string) (*appsession.Service, *appsession.Session, <-chan appsession.CommandEvent) {
	t.Helper()
	for _, key := range []string{
		"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
		"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
		"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
		"NETCATTY_HOOK_START",
	} {
		unsetEnv(t, key)
	}
	env := append(startupEnv(t, "pwsh", home),
		"SHELL="+path,
		// The profile turns the outer prompt into OUTERPROMPT; a nested shell
		// launched by the test drops the variable and prints USERPROMPT.
		"NETCATTY_TEST_OUTER_PROMPT=outer")
	events := make(chan appsession.CommandEvent, 32)
	service := appsession.NewService(answeringFactory{},
		appsession.WithCaptureHook(NewPlanner(WithDefaultShell(func() (string, error) {
			return path, nil
		}))),
		appsession.WithCommandCompletionHook(func(event appsession.CommandEvent) {
			events <- event
		}),
	)
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: []string{path},
		Dir:     workDir,
		Env:     env,
		Cols:    100, Rows: 40,
	})
	if err != nil {
		t.Fatalf("open the real PowerShell: %v", err)
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
		t.Fatalf("PowerShell never became ready: snapshot=%+v screen=%q",
			session.Snapshot(), text.Text)
	}
	return service, session, events
}

func TestRealPowerShellLocalHookCapturesTierOne(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	env := powerShellStartupEnv(t, home)
	plan := localPlan(t, pwsh, env)
	process := startShell(t, plan.Spec.Command[0], plan.Spec.Command, workDir,
		append(env, plan.Spec.Env...))

	ready := process.waitFor(t, "the hook handshake and the user's prompt", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07") &&
			strings.Contains(output, "USERPROMPT1> ")
	})
	// The outer bootstrap runs with -NoProfile; the interactive child is the
	// user's shell and must be the only profile load, so a profile side effect
	// happens exactly once locally.
	if count := strings.Count(ready, "netcatty-user-startup-loaded"); count != 1 {
		t.Fatalf("the user's profile ran %d times, want exactly once: %q", count, ready)
	}
	if !strings.Contains(ready, "USERPROMPT1> ") {
		t.Fatalf("the user's prompt was replaced: %q", ready)
	}
	if strings.Contains(ready, "1337;NetcattyHookSkip") {
		t.Fatalf("the hook refused to install: %q", ready)
	}

	typed := "echo netcatty_pwsh_42"
	process.typeLine(t, typed)
	result := "netcatty_pwsh_42"
	output := process.waitFor(t, "the private sequence "+typed, func(output string) bool {
		_, _, command, ok := findNetcattyExec(output)
		return ok && command == typed
	})
	hookShell, cwd, command, ok := findNetcattyExec(output)
	if !ok || hookShell != "powershell" || command != typed {
		t.Fatalf("private sequence = shell %q command %q ok %t", hookShell, command, ok)
	}
	expectedCWD := workDir
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		expectedCWD = resolved
	}
	if cwd != workDir && cwd != expectedCWD {
		t.Fatalf("cwd = %q, want %q", cwd, workDir)
	}
	process.waitFor(t, "the command output", func(output string) bool {
		return strings.Contains(output, result)
	})
	if count := countNetcattyExec(process.snapshotString()); count != 1 {
		t.Fatalf("capture sequence count = %d, want 1", count)
	}

	// The profile's own AddToHistoryHandler still runs: the hook never
	// replaces it, so the count reaches 2 for the second accepted line.
	process.typeLine(t, "echo history-$global:NETCATTY_TEST_HISTORY_COUNT")
	process.waitFor(t, "the profile's history handler", func(output string) bool {
		return strings.Contains(output, "history-2")
	})

	// The hook reports the shell's real exit status, not merely success.
	exitCommand := "/bin/sh -c 'exit 7'"
	if runtime.GOOS == "windows" {
		exitCommand = "cmd /c exit 7"
	}
	process.typeLine(t, exitCommand)
	process.waitFor(t, "the exit status in the prompt mark", func(output string) bool {
		return strings.Contains(output, "\x1b]133;D;7\x07")
	})

	// LASTEXITCODE persists across cmdlets in PowerShell. A later successful
	// cmdlet must report 0, not the native command's stale 7.
	afterFailure := "Write-Output netcatty_ok_after_failure"
	process.typeLine(t, afterFailure)
	process.waitFor(t, "the successful command's exit status", func(output string) bool {
		index := strings.LastIndex(output, "\x1b]1337;NetcattyExec;")
		if index < 0 {
			return false
		}
		_, _, command, ok := findNetcattyExec(output[index:])
		return ok && command == afterFailure &&
			strings.Contains(output[index:], "\x1b]133;D;0\x07")
	})

	// A failing cmdlet after a failed native command must not report the
	// native command's stale LASTEXITCODE. The history handler remembers the
	// status before the line runs; an unchanged value is not this command's
	// exit code.
	process.typeLine(t, exitCommand)
	process.waitFor(t, "the second native failure", func(output string) bool {
		return strings.Count(output, "\x1b]133;D;7\x07") >= 2
	})
	failingCmdlet := "Get-Item /netcatty-missing-path"
	process.typeLine(t, failingCmdlet)
	process.waitFor(t, "the failing cmdlet's own status", func(output string) bool {
		index := strings.LastIndex(output, "\x1b]1337;NetcattyExec;")
		if index < 0 {
			return false
		}
		_, _, command, ok := findNetcattyExec(output[index:])
		if !ok || command != failingCmdlet {
			return false
		}
		segment := output[index:]
		return strings.Contains(segment, "\x1b]133;D;1\x07") &&
			!strings.Contains(segment, "\x1b]133;D;7\x07")
	})

	// An empty accepted line must not replay the previous capture.
	before := process.snapshotString()
	beforeCaptures := countNetcattyExec(before)
	beforePrompts := strings.Count(before, "\x1b]133;A")
	process.typeLine(t, "")
	process.waitFor(t, "the empty-line prompt", func(output string) bool {
		return strings.Count(output, "\x1b]133;A") > beforePrompts &&
			countNetcattyExec(output) == beforeCaptures
	})
	if count := countNetcattyExec(process.snapshotString()); count != beforeCaptures {
		t.Fatalf("an empty line replayed a command: %d, want %d", count, beforeCaptures)
	}
}

func TestRealPowerShellSkipsAndStaysUsable(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	cases := map[string][]string{
		// An existing integration owns the history; the hook must stay out.
		"existing integration": {"ATUIN_SESSION=1"},
		// A parent exported the marker but this shell has no hook functions.
		"inherited marker": {"NETCATTY_HOOK=1"},
	}
	for name, extra := range cases {
		name, extra := name, extra
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			env := append(powerShellStartupEnv(t, home), extra...)
			plan := localPlan(t, pwsh, env)
			process := startShell(t, plan.Spec.Command[0], plan.Spec.Command, workDir,
				append(env, plan.Spec.Env...))
			output := process.waitFor(t, "the skip marker", func(output string) bool {
				return strings.Contains(output, "\x1b]1337;NetcattyHookSkip;powershell\x07")
			})
			if strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07") {
				t.Fatalf("the hook announced a handshake after skipping: %q", output)
			}
			// The refusal must not leave the shell captive: the user can still
			// run a command and see it.
			process.typeLine(t, "echo netcatty_after_skip_42")
			process.waitFor(t, "the command output after the skip", func(output string) bool {
				return strings.Contains(output, "netcatty_after_skip_42")
			})
			if count := countNetcattyExec(process.snapshotString()); count != 0 {
				t.Fatalf("a skipped hook captured %d commands", count)
			}
		})
	}
}

// TestRealPowerShellRedirectedStdioSkips is the non-interactive half of the
// Windows OpenSSH regression. A PowerShell launched with redirected stdin and
// stdout and without the launcher marker is not a terminal, so the hook must
// announce the skip marker and return instead of installing handlers that
// would wait forever for an interactive prompt. The profile is the same
// isolated one used by the PTY tests.
func TestRealPowerShellRedirectedStdioSkips(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	hookPath := filepath.Join(t.TempDir(), "hook.ps1")
	if err := os.WriteFile(hookPath, []byte(powershellHookText), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(pwsh, "-NoLogo", "-File", hookPath)
	command.Env = shellEnv(append(powerShellStartupEnv(t, home),
		"NETCATTY_HOOK=", "NETCATTY_HOOK_START="))
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("the redirected hook exited with %v: %q", err, output.String())
	}
	text := output.String()
	if !strings.Contains(text, "\x1b]1337;NetcattyHookSkip;powershell\x07") {
		t.Fatalf("redirected PowerShell did not announce the skip: %q", text)
	}
	if strings.Contains(text, "\x1b]1337;NetcattyHook;powershell\x07") {
		t.Fatalf("redirected PowerShell announced a handshake: %q", text)
	}
	if strings.Contains(text, "\x1b]1337;NetcattyExec;powershell") {
		t.Fatalf("redirected PowerShell installed the preexec hook: %q", text)
	}
}

// TestRealPowerShellHookInstallsUnderAPTY is the interactive half: the same
// script, the same player, but now the child is a real PTY started with -File
// and the launcher marker, exactly like WindowsPowerShellRemoteCommand's child
// under Win32-OpenSSH. The hook must install there even though session 0
// reports [Environment]::UserInteractive = false on Windows; the marker and
// the non-redirected console are the only inputs.
func TestRealPowerShellHookInstallsUnderAPTY(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	hookPath := filepath.Join(t.TempDir(), "hook.ps1")
	if err := os.WriteFile(hookPath, []byte(powershellHookText), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(powerShellStartupEnv(t, home),
		"NETCATTY_HOOK=",
		"NETCATTY_HOOK_START=1",
		"NETCATTY_TEST_OUTER_PROMPT=outer")
	process := startShell(t, pwsh,
		[]string{pwsh, "-NoLogo", "-NoExit", "-ExecutionPolicy", "Bypass", "-File", hookPath},
		workDir, env)
	ready := process.waitFor(t, "the hook handshake", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07")
	})
	if !strings.Contains(ready, "netcatty-user-startup-loaded") {
		t.Fatalf("the launched shell did not load the profile: %q", ready)
	}
	if strings.Contains(ready, "\x1b]1337;NetcattyHookSkip;powershell\x07") {
		t.Fatalf("the PTY shell emitted the skip marker: %q", ready)
	}

	typed := "echo netcatty_pty_42"
	process.typeLine(t, typed)
	output := process.waitFor(t, "the private sequence", func(output string) bool {
		_, _, got, ok := findNetcattyExec(output)
		return ok && got == typed
	})
	if _, _, got, ok := findNetcattyExec(output); !ok || got != typed {
		t.Fatalf("PTY private sequence = %q (ok %t)", got, ok)
	}
}

// TestRealPowerShellDuplicateAndHistoryRecallCapture is the duplicate preexec
// regression. PSReadLine checks HistoryNoDuplicates before AddToHistoryHandler,
// so without the conditional Enter wrapper an immediate rerun (and an
// Up-arrow rerun, which is the same line) produced no NetcattyExec at all and
// the tap, which discards typed Enter while the hook is active, lost the row.
// Each accepted line must produce exactly one private sequence.
func TestRealPowerShellDuplicateAndHistoryRecallCapture(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	env := append(powerShellStartupEnv(t, home), "NETCATTY_TEST_OUTER_PROMPT=outer")
	plan := localPlan(t, pwsh, env)
	process := startShell(t, plan.Spec.Command[0], plan.Spec.Command, workDir,
		append(env, plan.Spec.Env...))
	process.waitFor(t, "the hook handshake", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07")
	})

	promptCount := func() int {
		output, _ := process.snapshot()
		return strings.Count(output, "OUTERPROMPT")
	}
	const line = "echo netcatty_duplicate_42"
	steps := []string{
		line + "\r",
		// The immediate duplicate is exactly the line PSReadLine deduplicates.
		line + "\r",
		// Up-arrow recalls the same line from history and Enter reruns it.
		"\x1b[A\r",
	}
	for index, send := range steps {
		before := promptCount()
		if _, err := process.terminal.Write([]byte(send)); err != nil {
			t.Fatalf("send %q: %v", send, err)
		}
		want := index + 1
		output := process.waitFor(t, "the captured duplicate", func(output string) bool {
			return countNetcattyExec(output) >= want && strings.Count(output, "OUTERPROMPT") > before
		})
		if got := countNetcattyExec(output); got != want {
			t.Fatalf("step %d emitted %d private sequences, want exactly one per accepted line: %q",
				index, got, output)
		}
		if _, _, command, ok := findNetcattyExec(output); !ok || command != line {
			t.Fatalf("step %d captured %q (ok %t), want %q", index, command, ok, line)
		}
	}
}

// TestRealPowerShellProgrammaticAddToHistoryIsNotCaptured is the phantom-row
// regression. The old preexec was an AddToHistoryHandler, so a script or a
// user calling [PSConsoleReadLine]::AddToHistory('echo phantom') invoked it
// and emitted a capture for a command that never ran, while the real command
// could lose its row. PSConsoleHostReadLine only sees user-accepted lines, so
// the programmatic insert must leave the capture count unchanged and the next
// real command must still be captured.
func TestRealPowerShellProgrammaticAddToHistoryIsNotCaptured(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	env := append(powerShellStartupEnv(t, home), "NETCATTY_TEST_OUTER_PROMPT=outer")
	plan := localPlan(t, pwsh, env)
	process := startShell(t, plan.Spec.Command[0], plan.Spec.Command, workDir,
		append(env, plan.Spec.Env...))
	process.waitFor(t, "the hook handshake", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07")
	})
	promptCount := func() int {
		output, _ := process.snapshot()
		return strings.Count(output, "OUTERPROMPT")
	}

	first := "echo netcatty_real_first_42"
	process.typeLine(t, first)
	process.waitFor(t, "the first real capture", func(output string) bool {
		_, _, command, ok := lastNetcattyExec(output)
		return ok && command == first
	})

	insert := "[Microsoft.PowerShell.PSConsoleReadLine]::AddToHistory('echo phantom')"
	before := promptCount()
	process.typeLine(t, insert)
	process.waitFor(t, "the accepted AddToHistory call and the next prompt", func(output string) bool {
		_, _, command, ok := lastNetcattyExec(output)
		return ok && command == insert && strings.Count(output, "OUTERPROMPT") > before
	})
	commands := netcattyExecCommands(process.snapshotString())
	if len(commands) != 2 {
		t.Fatalf("captures after the programmatic insert = %q, want only the two accepted lines", commands)
	}
	for _, command := range commands {
		if command == "echo phantom" {
			t.Fatalf("the programmatic AddToHistory emitted a phantom capture: %q", commands)
		}
	}
	if commands[1] != insert {
		t.Fatalf("the accepted line's capture = %q, want %q", commands[1], insert)
	}

	// The line after the insert is still captured: the insert must neither
	// consume the next line nor leave the wrapper broken.
	real := "echo netcatty_after_phantom_42"
	process.typeLine(t, real)
	process.waitFor(t, "the real command after the insert", func(output string) bool {
		_, _, command, ok := lastNetcattyExec(output)
		return ok && command == real
	})
	commands = netcattyExecCommands(process.snapshotString())
	if len(commands) != 3 || commands[2] != real {
		t.Fatalf("captures after the real command = %q, want the insert and then %q", commands, real)
	}
}

// TestRealPowerShellCustomEnterBindingStillCapturesEveryLine proves the hook
// never depends on the Enter binding. The user rebinds Enter after the hook is
// installed; every accepted line, including an immediate duplicate and an
// Up-arrow recall, still passes through PSConsoleHostReadLine and is captured
// exactly once.
func TestRealPowerShellCustomEnterBindingStillCapturesEveryLine(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	env := append(powerShellStartupEnv(t, home), "NETCATTY_TEST_OUTER_PROMPT=outer")
	plan := localPlan(t, pwsh, env)
	process := startShell(t, plan.Spec.Command[0], plan.Spec.Command, workDir,
		append(env, plan.Spec.Env...))
	process.waitFor(t, "the hook handshake", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07")
	})
	promptCount := func() int {
		output, _ := process.snapshot()
		return strings.Count(output, "OUTERPROMPT")
	}

	bind := "Set-PSReadLineKeyHandler -Key Enter -ScriptBlock { param($key, $arg) [Microsoft.PowerShell.PSConsoleReadLine]::AcceptLine($key, $arg) }"
	const line = "echo netcatty_custom_enter_42"
	steps := []struct {
		send string
		want string
	}{
		{bind + "\r", bind},
		{line + "\r", line},
		{line + "\r", line},
		{"\x1b[A\r", line},
	}
	for index, step := range steps {
		before := promptCount()
		if _, err := process.terminal.Write([]byte(step.send)); err != nil {
			t.Fatalf("send %q: %v", step.send, err)
		}
		want := index + 1
		output := process.waitFor(t, "the captured custom-Enter line", func(output string) bool {
			return countNetcattyExec(output) >= want && strings.Count(output, "OUTERPROMPT") > before
		})
		if got := countNetcattyExec(output); got != want {
			t.Fatalf("step %d emitted %d captures, want one per accepted line: %q", index, got, output)
		}
		commands := netcattyExecCommands(output)
		if len(commands) != want || commands[want-1] != step.want {
			t.Fatalf("step %d captured %q, want %q", index, commands, step.want)
		}
	}
}

// TestRealPowerShellPromptSeesTheRealSuccessStatus is the $? regression: the
// prompt wrapper used to read Get-Variable and write the finish marks before
// calling the user's prompt, which reset $? to $true. The profile's prompt must
// see False after a failed command and True after a successful one.
func TestRealPowerShellPromptSeesTheRealSuccessStatus(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	powerShellProfiles(t, home, `# The user's prompt must see the executed command's real $?.
Set-StrictMode -Version Latest
function global:prompt { "STATUSPROMPT=$($?)> " }
`)
	env := powerShellEnv(home)
	plan := localPlan(t, pwsh, env)
	process := startShell(t, plan.Spec.Command[0], plan.Spec.Command, workDir,
		append(env, plan.Spec.Env...))
	process.waitFor(t, "the hook handshake and the first prompt", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07") &&
			strings.Contains(output, "STATUSPROMPT=True> ")
	})

	process.typeLine(t, "Get-Item /netcatty-missing-path 2>$null")
	process.waitFor(t, "the prompt after a failed command", func(output string) bool {
		return strings.Contains(output, "STATUSPROMPT=False> ")
	})
	output, _ := process.snapshot()
	beforeTrue := strings.Count(output, "STATUSPROMPT=True> ")
	process.typeLine(t, "Test-Path /netcatty-missing-path > $null")
	process.waitFor(t, "the prompt after a successful command", func(output string) bool {
		return strings.Count(output, "STATUSPROMPT=True> ") > beforeTrue
	})
}

// TestRealPowerShellMissingHookFallsBackToAPlainShell drives the refusal path
// without a policy engine: the planned hook file is deleted after the planner
// wrote it, which is what pwsh sees when a machine policy refuses the script.
// pwsh -NoExit -File <missing> exits, so the bootstrap must notice the missing
// state file, announce the skip, and start a usable login shell instead of
// ending the Session.
func TestRealPowerShellMissingHookFallsBackToAPlainShell(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	env := append(powerShellStartupEnv(t, home), "NETCATTY_TEST_OUTER_PROMPT=outer")
	plan, failed, dir := planWith(t, appsession.StartSpec{Kind: appsession.KindLocal},
		WithDefaultShell(func() (string, error) { return pwsh, nil }))
	if failed {
		t.Fatal("the planner reported a failure")
	}
	if err := os.Remove(filepath.Join(dir, "hook.ps1")); err != nil {
		t.Fatalf("remove the hook so PowerShell cannot load it: %v", err)
	}
	process := startShell(t, plan.Spec.Command[0], plan.Spec.Command, workDir,
		append(env, plan.Spec.Env...))
	ready := process.waitFor(t, "the skip and the fallback prompt", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHookSkip;powershell\x07") &&
			strings.Contains(output, "OUTERPROMPT1> ")
	})
	if strings.Contains(ready, "\x1b]1337;NetcattyHook;powershell\x07") {
		t.Fatalf("a missing hook announced a handshake: %q", ready)
	}
	process.typeLine(t, "echo netcatty_fallback_local_42")
	process.waitFor(t, "the fallback shell command output", func(output string) bool {
		return strings.Contains(output, "netcatty_fallback_local_42")
	})
}

// refusingPowerShellShim writes an executable named pwsh that behaves like a
// PowerShell blocked by a machine policy: it refuses every -File invocation.
// When the refused invocation carries -NoExit it leaves an ordinary
// interactive PowerShell running, exactly like the real shell does when the
// script cannot load. Without -File it is the real PowerShell, so it also
// serves as the fallback the launcher must start.
func refusingPowerShellShim(t *testing.T, pwsh string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pwsh")
	quoted := "'" + strings.ReplaceAll(pwsh, "'", `'\''`) + "'"
	script := "#!/bin/sh\n" +
		"file=''\n" +
		"noninteractive=''\n" +
		"for arg in \"$@\"; do\n" +
		"  [ \"$arg\" = '-File' ] && file=1\n" +
		"  [ \"$arg\" = '-NonInteractive' ] && noninteractive=1\n" +
		"done\n" +
		"if [ -n \"$file\" ]; then\n" +
		"  echo 'hook.ps1 cannot be loaded because running scripts is disabled on this system.' >&2\n" +
		"  if [ -n \"$noninteractive\" ]; then exit 1; fi\n" +
		"  exec " + quoted + " -NoLogo -NoExit\n" +
		"fi\n" +
		"exec " + quoted + " \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("create the refusing PowerShell shim: %v", err)
	}
	return path
}

// TestRealPowerShellBrokenHookFallsBackBeforeTheInteractiveChild is the
// pre-launch-check regression. A script that cannot run must never be started
// with -NoExit and then waited on: that child stays interactive at an ordinary
// prompt, never emits the skip marker, and every command would be silently
// uncaptured until the Session closed. The launcher must find out first, in a
// short-lived -NoProfile -NonInteractive child, and then start a plain shell.
func TestRealPowerShellBrokenHookFallsBackBeforeTheInteractiveChild(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fixture uses a POSIX shell shim and POSIX file modes")
	}
	mutations := map[string]struct {
		mutate func(t *testing.T, hookPath string)
		detail string
	}{
		"top-level throw": {detail: "NetcattyHookValidateOK must not appear", mutate: func(t *testing.T, hookPath string) {
			t.Helper()
			body, err := os.ReadFile(hookPath)
			if err != nil {
				t.Fatal(err)
			}
			body = append([]byte("throw 'netcatty-hook-boom'\n"), body...)
			if err := os.WriteFile(hookPath, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		"parse error": {mutate: func(t *testing.T, hookPath string) {
			t.Helper()
			if err := os.WriteFile(hookPath, []byte("if ($true) {\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		"unreadable script": {mutate: func(t *testing.T, hookPath string) {
			t.Helper()
			if err := os.Chmod(hookPath, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(hookPath, 0o600) })
		}},
	}
	for name, testCase := range mutations {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			env := append(powerShellStartupEnv(t, home), "NETCATTY_TEST_OUTER_PROMPT=outer")
			plan, failed, dir := planWith(t, appsession.StartSpec{Kind: appsession.KindLocal},
				WithDefaultShell(func() (string, error) { return pwsh, nil }))
			if failed {
				t.Fatal("the planner reported a failure")
			}
			testCase.mutate(t, filepath.Join(dir, "hook.ps1"))
			process := startShell(t, plan.Spec.Command[0], plan.Spec.Command, workDir,
				append(env, plan.Spec.Env...))
			ready := process.waitFor(t, "the skip and the fallback prompt", func(output string) bool {
				return strings.Contains(output, "\x1b]1337;NetcattyHookSkip;powershell\x07") &&
					strings.Contains(output, "OUTERPROMPT1> ")
			})
			if strings.Contains(ready, "\x1b]1337;NetcattyHook;powershell\x07") {
				t.Fatalf("a broken hook announced a handshake: %q", ready)
			}
			// The failed hook never reached the point of producing a capture.
			if commands := netcattyExecCommands(ready); len(commands) != 0 {
				t.Fatalf("a broken hook emitted captures: %q", commands)
			}
			// The outer bootstrap and the pre-launch check are -NoProfile; the
			// fallback shell is the only profile load.
			if count := strings.Count(ready, "netcatty-user-startup-loaded"); count != 1 {
				t.Fatalf("the user's profile ran %d times, want the fallback's single load: %q", count, ready)
			}
			process.typeLine(t, "echo netcatty_broken_fallback_42")
			process.waitFor(t, "the fallback shell command output", func(output string) bool {
				return strings.Contains(output, "netcatty_broken_fallback_42")
			})
		})
	}

	// Policy-style refusal: the shell refuses every -File, but a refused
	// -NoExit invocation would stay interactive. The launcher must detect the
	// refusal before starting that child and open the plain shell.
	t.Run("policy refusal", func(t *testing.T) {
		stub := refusingPowerShellShim(t, pwsh)
		home := t.TempDir()
		workDir := t.TempDir()
		env := append(powerShellStartupEnv(t, home), "NETCATTY_TEST_OUTER_PROMPT=outer")
		plan, failed, _ := planWith(t, appsession.StartSpec{Kind: appsession.KindLocal},
			WithDefaultShell(func() (string, error) { return stub, nil }))
		if failed {
			t.Fatal("the planner reported a failure")
		}
		process := startShell(t, plan.Spec.Command[0], plan.Spec.Command, workDir,
			append(env, plan.Spec.Env...))
		ready := process.waitFor(t, "the skip and the plain fallback shell", func(output string) bool {
			return strings.Contains(output, "\x1b]1337;NetcattyHookSkip;powershell\x07") &&
				strings.Contains(output, "OUTERPROMPT1> ")
		})
		if strings.Contains(ready, "\x1b]1337;NetcattyHook;powershell\x07") {
			t.Fatalf("a refused hook announced a handshake: %q", ready)
		}
		// The refusal text would reach the Session only if the refused -NoExit
		// child had been started; the pre-launch check captures it in its pipe.
		if strings.Contains(ready, "cannot be loaded") {
			t.Fatalf("the refused -File child was started anyway: %q", ready)
		}
		if count := strings.Count(ready, "netcatty-user-startup-loaded"); count != 1 {
			t.Fatalf("the user's profile ran %d times, want the fallback's single load: %q", count, ready)
		}
		process.typeLine(t, "echo netcatty_policy_fallback_42")
		process.waitFor(t, "the plain shell's command output", func(output string) bool {
			return strings.Contains(output, "netcatty_policy_fallback_42")
		})
	})
}

// TestRealPowerShellRemoteWrapperReplacesADeadChild is the same refusal on the
// SSH path without a policy engine: validation succeeds (the stub prints the
// marker), but the interactive child exits before loading the hook, so the
// state file never appears. The wrapper must announce the skip instead of
// leaving the SSH Session to end with the child.
func TestRealPowerShellRemoteWrapperReplacesADeadChild(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	// The pre-launch check succeeds, so the state-file guard is what catches
	// the interactive child exiting without loading the hook.
	stub := filepath.Join(t.TempDir(), "refused-shell")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = '-NonInteractive' ]; then echo NetcattyHookValidateOK; exit 0; fi\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(powerShellStartupEnv(t, home), "TMPDIR="+t.TempDir())
	command := WindowsPowerShellRemoteCommand(stub)
	process := startShell(t, pwsh, []string{pwsh, "-NoLogo", "-NoProfile", "-Command", command}, workDir, env)
	output := process.waitFor(t, "the wrapper's skip marker", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHookSkip;powershell\x07")
	})
	if strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07") {
		t.Fatalf("a dead hook child announced a handshake: %q", output)
	}
}

// TestRealPowerShellRemoteWrapperPolicyRefusalFallsBackBeforeTheChild is the
// SSH half of the pre-launch-check regression: the DefaultShell refuses -File
// but would stay interactive under -NoExit. The wrapper must learn that from
// the short-lived -NoProfile -NonInteractive check and open the plain shell,
// rather than start a refused -NoExit child that never announces the skip.
func TestRealPowerShellRemoteWrapperPolicyRefusalFallsBackBeforeTheChild(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the refusing shim is a POSIX shell script")
	}
	stub := refusingPowerShellShim(t, pwsh)
	home := t.TempDir()
	workDir := t.TempDir()
	env := append(powerShellStartupEnv(t, home), "TMPDIR="+t.TempDir())
	command := WindowsPowerShellRemoteCommand(stub)
	process := startShell(t, pwsh, []string{pwsh, "-NoLogo", "-NoProfile", "-Command", command}, workDir, env)
	output := process.waitFor(t, "the wrapper's skip and the plain shell", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHookSkip;powershell\x07") &&
			strings.Contains(output, "USERPROMPT1> ")
	})
	if strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07") {
		t.Fatalf("a refused hook announced a handshake: %q", output)
	}
	if strings.Contains(output, "cannot be loaded") {
		t.Fatalf("the wrapper started the refused -File child: %q", output)
	}
	// The test's outer process is -NoProfile; only the fallback shell loads
	// the profile, which also proves the pre-launch check did not.
	if count := strings.Count(output, "netcatty-user-startup-loaded"); count != 1 {
		t.Fatalf("the user's profile ran %d times, want the fallback's single load: %q", count, output)
	}
	process.typeLine(t, "echo netcatty_remote_policy_42")
	process.waitFor(t, "the fallback shell's command output", func(output string) bool {
		return strings.Contains(output, "netcatty_remote_policy_42")
	})
}

// TestRealPowerShellRemoteWrapperBootstrapProfileLoad documents the one
// profile cost the SSH shape cannot avoid. Win32-OpenSSH starts the
// DefaultShell as `<shell> -c "<command>"`; PowerShell loads the user's
// profile before that command, and the interactive child must load it again
// because it is the user's shell. The command text cannot suppress the outer
// load (sshd owns its argv), so the count is asserted as exactly two: bounded,
// and the child's load is the one that reaches the Session. The pre-launch
// hook check runs with -NoProfile, so it must not add a third load.
func TestRealPowerShellRemoteWrapperBootstrapProfileLoad(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	countPath := filepath.Join(t.TempDir(), "profile-loads.txt")
	body := "[IO.File]::AppendAllText(" + quotePowerShell(countPath) + ", 'load' + [char]10)\n" +
		// The wrapper runs in this profile-loaded outer shell; strict mode must
		// not break its own statements.
		"Set-StrictMode -Version Latest\n" +
		"function global:prompt { 'PROFILEPROMPT> ' }\n"
	powerShellProfiles(t, home, body)
	env := append(powerShellEnv(home), "TMPDIR="+t.TempDir())
	command := WindowsPowerShellRemoteCommand(pwsh)
	process := startShell(t, pwsh, []string{pwsh, "-NoLogo", "-Command", command}, workDir, env)
	process.waitFor(t, "the wrapped hook handshake", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07")
	})
	content, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatalf("read the profile load counter: %v", err)
	}
	loads := strings.Count(string(content), "load")
	if loads != 2 {
		t.Fatalf("the profile ran %d times, want the sshd bootstrap plus the interactive child: %q",
			loads, content)
	}
}

// TestRealPowerShellRemoteWrapperCleansUp drives the Windows OpenSSH
// RemoteCommand shape on a real PowerShell: the wrapper writes a temp script,
// starts an interactive child that loads the profile, and removes the script
// directory when the child exits.
func TestRealPowerShellRemoteWrapperCleansUp(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	remoteTmp := t.TempDir()
	env := append(powerShellStartupEnv(t, home),
		"TMPDIR="+remoteTmp,
		"TMP="+remoteTmp,
		"TEMP="+remoteTmp,
	)
	command := WindowsPowerShellRemoteCommand(pwsh)
	// Start the outer wrapper exactly as Win32-OpenSSH does: the profile loads
	// (and turns StrictMode on) before the command text runs.
	process := startShell(t, pwsh, []string{pwsh, "-NoLogo", "-Command", command},
		workDir, env)

	ready := process.waitFor(t, "the wrapped hook handshake and prompt", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHook;powershell\x07") &&
			strings.Contains(output, "USERPROMPT1> ")
	})
	if !strings.Contains(ready, "netcatty-user-startup-loaded") {
		t.Fatalf("the wrapped shell did not load the profile: %q", ready)
	}
	if !strings.Contains(ready, "USERPROMPT1> ") {
		t.Fatalf("the wrapped shell replaced the prompt: %q", ready)
	}

	typed := "echo netcatty_remote_42"
	process.typeLine(t, typed)
	output := process.waitFor(t, "the remote private sequence", func(output string) bool {
		_, _, got, ok := findNetcattyExec(output)
		return ok && got == typed
	})
	if _, _, got, ok := findNetcattyExec(output); !ok || got != typed {
		t.Fatalf("remote private sequence = %q (ok %t)", got, ok)
	}
	process.waitFor(t, "the remote command output", func(output string) bool {
		return strings.Contains(output, "netcatty_remote_42")
	})

	// Exit the inner shell; the wrapper's finally block then removes the
	// session directory. .NET may create its own .dotnet directory in TMPDIR,
	// so cleanup is observable as the absence of the wrapper's own directory.
	process.typeLine(t, "exit")
	deadline := time.Now().Add(20 * time.Second)
	for {
		entries, err := os.ReadDir(remoteTmp)
		if err != nil {
			t.Fatalf("read the remote temporary directory: %v", err)
		}
		leftover := ""
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "netcatty-hook-") {
				leftover = entry.Name()
			}
		}
		if leftover == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the remote wrapper left %s behind", leftover)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestRealPowerShellRemoteWrapperSurvivesAnUnusableTempPath drives the failure
// path the coordinator called out: the SSH wrapper cannot create its temporary
// directory because the peer's temp path is a regular file. The wrapper must
// announce the private skip marker, start an ordinary login shell so the
// Session survives, and clean up without throwing.
func TestRealPowerShellRemoteWrapperSurvivesAnUnusableTempPath(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("regular file"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(powerShellStartupEnv(t, home),
		"TMPDIR="+blocked,
		"TMP="+blocked,
		"TEMP="+blocked,
	)
	command := WindowsPowerShellRemoteCommand(pwsh)
	process := startShell(t, pwsh, []string{pwsh, "-NoLogo", "-NoProfile", "-Command", command},
		workDir, env)

	ready := process.waitFor(t, "the skip marker and the fallback prompt", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHookSkip;powershell\x07") &&
			strings.Contains(output, "USERPROMPT1> ")
	})
	if strings.Contains(ready, "\x1b]1337;NetcattyHook;powershell\x07") {
		t.Fatalf("the failed wrapper still announced a hook: %q", ready)
	}

	// The fallback is a usable login shell: the user's profile loaded and a
	// command runs.
	process.typeLine(t, "echo netcatty_fallback_42")
	process.waitFor(t, "the fallback shell command output", func(output string) bool {
		return strings.Contains(output, "netcatty_fallback_42")
	})

	process.typeLine(t, "exit")
	deadline := time.Now().Add(20 * time.Second)
	for {
		output, closed := process.snapshot()
		if closed {
			if strings.Contains(output, "Remove-Item") {
				t.Fatalf("the wrapper's cleanup threw: %q", output)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the wrapper did not exit after the fallback shell closed")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if info, err := os.Stat(blocked); err != nil || info.IsDir() {
		t.Fatalf("the wrapper's cleanup damaged the blocked temp path: %v", err)
	}
}

// A planned local PowerShell Session must carry the hook environment the
// script sets, so a nested shell can detect it and skip instead of nesting a
// second hook.
func TestPowerShellPlanKeepsTheStartSpecUsable(t *testing.T) {
	plan, failed, dir := planWith(t, appsession.StartSpec{
		Kind: appsession.KindLocal, Dir: t.TempDir(),
	}, WithDefaultShell(func() (string, error) { return powershellPath, nil }))
	if failed {
		t.Fatal("the planner reported a failure")
	}
	if plan.Spec.Dir == "" || len(plan.Spec.Command) == 0 {
		t.Fatalf("the plan lost the session's startup: %+v", plan.Spec)
	}
	if plan.Cleanup == nil {
		t.Fatal("the plan has no cleanup for its session directory")
	}
	plan.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup left %s behind: %v", dir, err)
	}
}

// TestRealPowerShellNestedCallOperatorHandsCaptureToTheChild is the call
// operator regression: "& 'C:\Program Files\PowerShell\7\pwsh.exe'" (here a
// POSIX path containing a space) must be recognized as an interactive nested
// PowerShell. The outer row is finalized once with unknown finish, commands
// typed at the child prompt are captured without the outer hook, and the outer
// hook works again after the child exits.
func TestRealPowerShellNestedCallOperatorHandsCaptureToTheChild(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the launcher fixture uses a POSIX shell script")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	launcherDir := filepath.Join(t.TempDir(), "Power Shell")
	if err := os.MkdirAll(launcherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(launcherDir, "pwsh")
	script := "#!/bin/sh\nunset NETCATTY_TEST_OUTER_PROMPT\nexec '" +
		strings.ReplaceAll(pwsh, "'", `'\''`) + "' \"$@\"\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatalf("create the nested launcher: %v", err)
	}
	service, session, events := openHookedPowerShellSession(t, pwsh, home, workDir)
	warmUpHookedSession(t, service, session, events, "pwsh")

	outerCommand := "& '" + launcher + "'"
	service.SendInput([]model.SessionID{session.ID()}, []byte(outerCommand+"\r"))
	outerEvent := awaitCommandEvent(t, events, outerCommand, nil)
	if outerEvent.CaptureTier != domainhistory.CaptureTierHook {
		t.Fatalf("nested call operator tier = %d, want the outer hook's Tier 1: %+v",
			outerEvent.CaptureTier, outerEvent)
	}
	if outerEvent.HasExitCode || !outerEvent.FinishedAt.IsZero() {
		t.Fatalf("the outer shell command invented a finish: %+v", outerEvent)
	}

	one := `echo nested_one_$(1+1)`
	two := `echo nested_two_$(2+2)`
	for _, command := range []string{one, two} {
		service.SendInput([]model.SessionID{session.ID()}, []byte(command+"\r"))
		event := awaitCommandEvent(t, events, command, nil)
		// The child emits no OSC 133/7 and its hook skipped, so the marks that
		// made the outer shell Tier 2 (and the hook that made it Tier 1) must
		// not leak into the child's captured tier.
		if event.CaptureTier != domainhistory.CaptureTierFallback {
			t.Fatalf("inner command %q tier = %d, want the child's Tier 3: %+v",
				command, event.CaptureTier, event)
		}
	}

	outerPromptCount := func() int {
		text, _ := service.ScreenText(session.ID())
		return strings.Count(text.Text, "OUTERPROMPT")
	}
	beforeExit := outerPromptCount()
	service.SendInput([]model.SessionID{session.ID()}, []byte("exit\r"))
	awaitCommandEvent(t, events, "exit", nil)
	// The exit row is finished by the child's own OSC 133 D, which can arrive
	// before the outer hook's handshake. Only the outer shell prints
	// OUTERPROMPT, and it does so after the handshake in the same prompt call;
	// the Session renders output only after the tap observed it. A new
	// OUTERPROMPT on screen is therefore the observable that Tier 1 is back.
	// No fixed sleep is needed.
	if !waitCondition(func() bool { return outerPromptCount() > beforeExit }) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the outer prompt never returned; screen=%q", text.Text)
	}
	back := `echo nested_back_$(3+3)`
	service.SendInput([]model.SessionID{session.ID()}, []byte(back+"\r"))
	backEvent := awaitCommandEvent(t, events, back, nil)
	if backEvent.CaptureTier != domainhistory.CaptureTierHook {
		t.Fatalf("the outer hook did not return after the nested shell: %+v", backEvent)
	}
}

// TestRealPowerShellNestedShellWithSpacesAndNoExit is the nested-classification
// regression: a quoted option value containing spaces and a -NoExit invocation
// of -Command must both be recognized as an interactive nested shell. The
// argument splitter used to break the quoted value into words, and -NoExit
// with -Command looked non-interactive, so the tap kept its hook tier and
// discarded every line typed at the child's prompt.
func TestRealPowerShellNestedShellWithSpacesAndNoExit(t *testing.T) {
	pwsh := availablePowerShell()
	if pwsh == "" {
		t.Skip("no PowerShell available; set NETCATTY_TEST_PWSH")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the launcher fixture uses a POSIX shell script")
	}
	spaceDir := filepath.Join(t.TempDir(), "work dir with a space")
	if err := os.MkdirAll(spaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(launcher string) string{
		// -WorkingDirectory's quoted value contains spaces; the tap must keep
		// it as one word instead of splitting it with strings.Fields.
		"quoted working directory": func(launcher string) string {
			return "& '" + launcher + "' -NoLogo -WorkingDirectory '" + spaceDir + "'"
		},
		// -NoExit before -Command keeps the prompt after the command runs.
		"no exit command": func(launcher string) string {
			return "& '" + launcher + "' -NoLogo -NoExit -Command 'Write-Output netcatty_noexit_42'"
		},
	}
	for name, build := range cases {
		name, build := name, build
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			launcherDir := filepath.Join(t.TempDir(), "Power Shell")
			if err := os.MkdirAll(launcherDir, 0o700); err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(launcherDir, "pwsh")
			script := "#!/bin/sh\nunset NETCATTY_TEST_OUTER_PROMPT\nexec '" +
				strings.ReplaceAll(pwsh, "'", `'\''`) + "' \"$@\"\n"
			if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
				t.Fatalf("create the nested launcher: %v", err)
			}
			service, session, events := openHookedPowerShellSession(t, pwsh, home, workDir)
			warmUpHookedSession(t, service, session, events, "pwsh")

			outerCommand := build(launcher)
			service.SendInput([]model.SessionID{session.ID()}, []byte(outerCommand+"\r"))
			outerEvent := awaitCommandEvent(t, events, outerCommand, nil)
			if outerEvent.CaptureTier != domainhistory.CaptureTierHook {
				t.Fatalf("nested command tier = %d, want the outer hook's Tier 1: %+v",
					outerEvent.CaptureTier, outerEvent)
			}
			if outerEvent.HasExitCode || !outerEvent.FinishedAt.IsZero() {
				t.Fatalf("the outer shell command invented a finish: %+v", outerEvent)
			}

			inner := "echo netcatty_nested_space_42"
			service.SendInput([]model.SessionID{session.ID()}, []byte(inner+"\r"))
			innerEvent := awaitCommandEvent(t, events, inner, nil)
			if innerEvent.CaptureTier != domainhistory.CaptureTierFallback {
				t.Fatalf("inner command tier = %d, want the child's Tier 3: %+v",
					innerEvent.CaptureTier, innerEvent)
			}
			if !waitCondition(func() bool {
				text, _ := service.ScreenText(session.ID())
				return strings.Contains(text.Text, "netcatty_nested_space_42")
			}) {
				text, _ := service.ScreenText(session.ID())
				t.Fatalf("the inner command never ran: %q", text.Text)
			}

			outerPromptCount := func() int {
				text, _ := service.ScreenText(session.ID())
				return strings.Count(text.Text, "OUTERPROMPT")
			}
			beforeExit := outerPromptCount()
			service.SendInput([]model.SessionID{session.ID()}, []byte("exit\r"))
			awaitCommandEvent(t, events, "exit", nil)
			if !waitCondition(func() bool { return outerPromptCount() > beforeExit }) {
				text, _ := service.ScreenText(session.ID())
				t.Fatalf("the outer prompt never returned; screen=%q", text.Text)
			}
			back := "echo netcatty_nested_back_42"
			service.SendInput([]model.SessionID{session.ID()}, []byte(back+"\r"))
			backEvent := awaitCommandEvent(t, events, back, nil)
			if backEvent.CaptureTier != domainhistory.CaptureTierHook {
				t.Fatalf("the outer hook did not return after the nested shell: %+v", backEvent)
			}
		})
	}
}
