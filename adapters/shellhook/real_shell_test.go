package shellhook

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/platform/pty"
)

// availableShells returns the real shells this machine can run. fish is not
// installed everywhere; NETCATTY_TEST_FISH points at a provisioned binary.
func availableShells() map[string]string {
	shells := map[string]string{}
	for _, name := range []string{"zsh", "bash"} {
		if path, err := exec.LookPath(name); err == nil {
			shells[name] = path
		}
	}
	fish := os.Getenv("NETCATTY_TEST_FISH")
	if fish == "" {
		fish, _ = exec.LookPath("fish")
	}
	if info, err := os.Stat(fish); fish != "" && err == nil && !info.IsDir() {
		shells["fish"] = fish
	}
	return shells
}

// startupEnv creates an isolated home with the user's own startup file and
// returns the extra environment the shell needs.
func startupEnv(t *testing.T, name, home string) []string {
	t.Helper()
	env := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}
	switch name {
	case "zsh":
		zdotdir := filepath.Join(home, ".config", "zsh")
		if err := os.MkdirAll(zdotdir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := `print -r -- netcatty-user-startup-loaded
__netcatty_test_user_precmd() { NETCATTY_TEST_USER_PROMPT=1 }
precmd_functions+=(__netcatty_test_user_precmd)
`
		if err := os.WriteFile(filepath.Join(zdotdir, ".zshrc"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return append(env, "ZDOTDIR="+zdotdir)
	case "bash":
		body := `echo netcatty-user-startup-loaded
__netcatty_test_user_prompt() { NETCATTY_TEST_USER_PROMPT=1; }
PROMPT_COMMAND=__netcatty_test_user_prompt
`
		if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return env
	case "fish":
		dir := filepath.Join(home, ".config", "fish")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := `echo netcatty-user-startup-loaded
function __netcatty_test_user_prompt --on-event fish_prompt
    set -g NETCATTY_TEST_USER_PROMPT 1
end
`
		if err := os.WriteFile(filepath.Join(dir, "config.fish"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return env
	case "pwsh", "powershell":
		return powerShellStartupEnv(t, home)
	}
	t.Fatalf("unknown shell %q", name)
	return nil
}

// shellEnv merges the process environment with overrides; a later entry wins.
// The developer's own shell integration variables are dropped so a test sees
// what a fresh Session would see.
func shellEnv(extra []string) []string {
	values := map[string]string{}
	order := make([]string, 0, len(extra)+1)
	add := func(entry string, filter bool) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return
		}
		if filter {
			switch {
			case strings.HasPrefix(key, "NETCATTY_"),
				strings.HasPrefix(key, "ATUIN_"),
				strings.HasPrefix(key, "STARSHIP_"),
				key == "ITERM_SHELL_INTEGRATION_INSTALLED":
				return
			}
		}
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = value
	}
	for _, entry := range os.Environ() {
		add(entry, true)
	}
	for _, entry := range extra {
		add(entry, false)
	}
	env := make([]string, 0, len(order))
	for _, key := range order {
		env = append(env, key+"="+values[key])
	}
	return env
}

// shellProcess runs an interactive shell and accumulates its output.
type shellProcess struct {
	terminal *pty.PTY
	mu       sync.Mutex
	output   strings.Builder
	closed   bool
	notify   chan struct{}
}

// startShell runs path in a PTY. args is the full argv passed to exec, so
// args[0] must name the program; passing a command's arguments without their
// program would silently shift every option by one.
func startShell(t *testing.T, path string, args []string, dir string, env []string) *shellProcess {
	t.Helper()
	terminal, err := pty.Start(pty.Options{
		Path: path,
		Args: args,
		Dir:  dir,
		Env:  shellEnv(append(env, "TERM=xterm-256color")),
		Cols: 100, Rows: 40,
	})
	if err != nil {
		t.Fatalf("start %s: %v", path, err)
	}
	process := &shellProcess{terminal: terminal, notify: make(chan struct{}, 1)}
	t.Cleanup(func() { _ = terminal.Close() })
	go process.readLoop()
	return process
}

func (p *shellProcess) readLoop() {
	buffer := make([]byte, 4096)
	// PSReadLine asks the terminal for the cursor position when it starts. A
	// real terminal answers; a bare PTY must too, or line editing never
	// becomes ready. The three-byte tail joins a query split across reads.
	tail := ""
	for {
		count, readErr := p.terminal.Read(buffer)
		if count > 0 {
			chunk := tail + string(buffer[:count])
			if queries := strings.Count(chunk, "\x1b[6n"); queries > 0 {
				for index := 0; index < queries; index++ {
					_, _ = p.terminal.Write([]byte("\x1b[1;1R"))
				}
			}
			if len(chunk) >= 3 {
				tail = chunk[len(chunk)-3:]
			} else {
				tail = chunk
			}
			p.mu.Lock()
			p.output.Write(buffer[:count])
			p.mu.Unlock()
			select {
			case p.notify <- struct{}{}:
			default:
			}
		}
		if readErr != nil {
			p.mu.Lock()
			p.closed = true
			p.mu.Unlock()
			select {
			case p.notify <- struct{}{}:
			default:
			}
			return
		}
	}
}

func (p *shellProcess) snapshot() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.output.String(), p.closed
}

// waitFor blocks on real output, never on a fixed delay.
func (p *shellProcess) waitFor(t *testing.T, what string, predicate func(string) bool) string {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		output, closed := p.snapshot()
		if predicate(output) {
			return output
		}
		if closed {
			t.Fatalf("the shell exited while waiting for %s; output: %q", what, output)
		}
		select {
		case <-p.notify:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s; output: %q", what, output)
		}
	}
}

func (p *shellProcess) typeLine(t *testing.T, line string) {
	t.Helper()
	if _, err := p.terminal.Write([]byte(line + "\r")); err != nil {
		t.Fatalf("write %q: %v", line, err)
	}
}

func decodeBase64URL(value string) (string, bool) {
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return string(decoded), true
		}
	}
	return "", false
}

// findNetcattyExec returns the first decoded private sequence in output.
func findNetcattyExec(output string) (shell, cwd, command string, ok bool) {
	const prefix = "\x1b]1337;NetcattyExec;"
	for {
		index := strings.Index(output, prefix)
		if index < 0 {
			return "", "", "", false
		}
		output = output[index+len(prefix):]
		end := strings.IndexByte(output, '\a')
		if end < 0 {
			return "", "", "", false
		}
		fields := strings.Split(output[:end], ";")
		if len(fields) == 3 {
			decodedCWD, cwdOK := decodeBase64URL(fields[1])
			decodedCommand, commandOK := decodeBase64URL(fields[2])
			if cwdOK && commandOK {
				return fields[0], decodedCWD, decodedCommand, true
			}
		}
		output = output[end+1:]
	}
}

func countNetcattyExec(output string) int {
	count := 0
	rest := output
	for {
		index := strings.Index(rest, "\x1b]1337;NetcattyExec;")
		if index < 0 {
			return count
		}
		count++
		rest = rest[index+len("\x1b]1337;NetcattyExec;"):]
	}
}

// netcattyExecCommands returns the decoded command of every private sequence
// in output, in order. A sequence whose payload does not decode is skipped.
func netcattyExecCommands(output string) []string {
	const prefix = "\x1b]1337;NetcattyExec;"
	commands := []string{}
	for {
		index := strings.Index(output, prefix)
		if index < 0 {
			return commands
		}
		output = output[index+len(prefix):]
		end := strings.IndexByte(output, '\a')
		if end < 0 {
			return commands
		}
		fields := strings.Split(output[:end], ";")
		if len(fields) == 3 {
			_, cwdOK := decodeBase64URL(fields[1])
			command, commandOK := decodeBase64URL(fields[2])
			if cwdOK && commandOK {
				commands = append(commands, command)
			}
		}
		output = output[end+1:]
	}
}

// loginArgs mirrors the local PTY adapter: a default shell is started as a
// login shell, which the planner leaves implicit.
func loginArgs(shellPath string) []string {
	return []string{"-" + filepath.Base(shellPath)}
}

func localPlan(t *testing.T, shellPath string, extraEnv []string) appsession.CaptureHookPlan {
	t.Helper()
	plan, failed := NewPlanner(WithDefaultShell(func() (string, error) {
		return shellPath, nil
	})).Plan(appsession.StartSpec{Kind: appsession.KindLocal, Dir: t.TempDir(), Env: extraEnv})
	if failed {
		t.Fatal("the planner reported a hook failure")
	}
	t.Cleanup(func() {
		if plan.Cleanup != nil {
			plan.Cleanup()
		}
	})
	return plan
}

func TestRealShellCapture(t *testing.T) {
	shells := availableShells()
	if len(shells) == 0 {
		t.Skip("no zsh, bash, or fish available")
	}
	for name, path := range shells {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			workDir := t.TempDir()
			env := startupEnv(t, name, home)
			plan := localPlan(t, path, env)
			plan.Spec.Dir = workDir

			startArgs := plan.Spec.Command
			if len(startArgs) == 0 {
				startArgs = loginArgs(path)
			}
			process := startShell(t, path, startArgs, workDir, append(env, plan.Spec.Env...))

			ready := process.waitFor(t, "the first prompt", func(output string) bool {
				return strings.Contains(output, "\x1b]133;A")
			})
			if !strings.Contains(ready, "netcatty-user-startup-loaded") {
				t.Fatalf("the user's startup did not run: %q", ready)
			}

			typed := "echo netcatty_capture_marker_$((40+2))"
			result := "netcatty_capture_marker_42"
			if name == "fish" {
				typed = "echo netcatty_capture_marker_(math 40+2)"
			}
			process.typeLine(t, typed)
			output := process.waitFor(t, "the private sequence "+typed, func(output string) bool {
				_, _, command, ok := findNetcattyExec(output)
				return ok && command == typed
			})
			shellName, cwd, command, ok := findNetcattyExec(output)
			if !ok {
				t.Fatalf("no private sequence in %q", output)
			}
			if shellName != name {
				t.Fatalf("shell = %q, want %q", shellName, name)
			}
			if command != typed {
				t.Fatalf("command = %q, want %q", command, typed)
			}
			expectedCWD := workDir
			if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
				expectedCWD = resolved
			}
			if cwd != workDir && cwd != expectedCWD {
				t.Fatalf("cwd = %q, want %q", cwd, workDir)
			}
			output = process.waitFor(t, "the command output", func(output string) bool {
				return strings.Contains(output, result)
			})
			if countNetcattyExec(output) != 1 {
				t.Fatalf("capture sequence count = %d, want 1; output: %q", countNetcattyExec(output), output)
			}

			// The user's own prompt mechanism still runs alongside the hook.
			userTyped := "echo user-${NETCATTY_TEST_USER_PROMPT}"
			if name == "fish" {
				userTyped = "echo user-$NETCATTY_TEST_USER_PROMPT"
			}
			process.typeLine(t, userTyped)
			process.waitFor(t, "the user prompt variable", func(output string) bool {
				return strings.Contains(output, "user-1")
			})

			// An empty line runs no command, so it must not replay the previous
			// one at the next prompt.
			beforeOutput := process.snapshotString()
			beforePrompts := strings.Count(beforeOutput, "\x1b]133;A")
			beforeCaptures := countNetcattyExec(beforeOutput)
			process.typeLine(t, "")
			after := process.waitFor(t, "the empty-line prompt", func(output string) bool {
				return strings.Count(output, "\x1b]133;A") > beforePrompts &&
					countNetcattyExec(output) == beforeCaptures
			})
			if count := countNetcattyExec(after); count != beforeCaptures {
				t.Fatalf("an empty line replayed a command: %d captures, want %d; output: %q",
					count, beforeCaptures, after)
			}
		})
	}
}

func (p *shellProcess) snapshotString() string {
	output, _ := p.snapshot()
	return output
}

// TestRealShellSessionCapturesTierOneEndToEnd drives the real Session stack:
// the planner rewrites the StartSpec, the local PTY starts the shell, the hook
// emits the private sequence, and the tap publishes exactly one Tier 1
// completion per command with the shell's own command, cwd, and exit code.
func TestRealShellSessionCapturesTierOneEndToEnd(t *testing.T) {
	shells := availableShells()
	if len(shells) == 0 {
		t.Skip("no zsh, bash, or fish available")
	}
	for name, path := range shells {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			// The developer's shell integration is not part of the Session
			// under test; localpty inherits the process environment, so remove
			// the guards a nested shell would otherwise see. fish treats a
			// defined-but-empty variable as set, so empties are not enough.
			for _, key := range []string{
				"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
				"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
				"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
			} {
				unsetEnv(t, key)
			}
			home := t.TempDir()
			workDir := t.TempDir()
			// localpty resolves the default shell from SHELL, so name the shell
			// the planner was given; otherwise the test could inject zsh
			// startup into whatever shell the developer's environment names.
			env := append(startupEnv(t, name, home), "SHELL="+path)
			events := make(chan appsession.CommandEvent, 8)
			service := appsession.NewService(localpty.Factory{},
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
				t.Fatalf("open the real shell: %v", err)
			}
			// Wait for the command loop to finish before t.TempDir removes the
			// files a still-running shell could rewrite.
			t.Cleanup(func() {
				_ = session.Close()
				select {
				case <-session.Done():
				case <-time.After(5 * time.Second):
				}
			})

			if !waitCondition(func() bool { return session.Snapshot().CWD != "" }) {
				text, _ := service.ScreenText(session.ID())
				t.Fatalf("the first prompt never arrived: snapshot=%+v screen=%q",
					session.Snapshot(), text.Text)
			}
			expectedCWD := workDir
			if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
				expectedCWD = resolved
			}

			type exchange struct {
				typed string
				exit  int
			}
			exchanges := []exchange{
				{typed: "echo netcatty_e2e_$((40+2))"},
				{typed: "false", exit: 1},
				{typed: "echo netcatty_second_$((20+2))"},
			}
			warmupTyped, warmupResult := "echo netcatty_warmup_$((1+1))", "netcatty_warmup_2"
			if name == "fish" {
				exchanges = []exchange{
					{typed: "echo netcatty_e2e_(math 40+2)"},
					{typed: "false", exit: 1},
					{typed: "echo netcatty_second_(math 20+2)"},
				}
				warmupTyped, warmupResult = "echo netcatty_warmup_(math 1+1)", "netcatty_warmup_2"
			}
			// Run one command to completion first so the real exchanges start
			// from a shell whose line editor is ready to read them. The strict
			// assertion below also proves fish's first prompt does not publish
			// a duplicate fallback row.
			service.SendInput([]model.SessionID{session.ID()}, []byte(warmupTyped+"\r"))
			if !waitCondition(func() bool {
				text, _ := service.ScreenText(session.ID())
				return strings.Contains(text.Text, warmupResult)
			}) {
				text, _ := service.ScreenText(session.ID())
				t.Fatalf("the warm-up never ran; screen=%q", text.Text)
			}
			// The hook's own Tier 1 capture of the warm-up proves the line
			// editor is ready. Any other event here would be a duplicate.
			awaitTierOne(t, events, warmupTyped)
			for _, step := range exchanges {
				result := service.SendInput([]model.SessionID{session.ID()}, []byte(step.typed+"\r"))
				if result.DeliveredCount() != 1 {
					t.Fatalf("send %q: %+v", step.typed, result)
				}
				event := awaitTierOne(t, events, step.typed)
				if event.Command != step.typed || event.Shell != name ||
					event.CaptureTier != domainhistory.CaptureTierHook {
					t.Fatalf("completion = %+v", event)
				}
				if event.CWD != workDir && event.CWD != expectedCWD {
					t.Fatalf("cwd = %q, want %q", event.CWD, workDir)
				}
				if !event.HasExitCode || event.ExitCode != step.exit {
					t.Fatalf("exit = %d/%t, want %d", event.ExitCode, event.HasExitCode, step.exit)
				}
				if event.StartedAt.IsZero() || event.FinishedAt.Before(event.StartedAt) {
					t.Fatalf("times = %v..%v", event.StartedAt, event.FinishedAt)
				}
				select {
				case extra := <-events:
					t.Fatalf("the command was captured twice: %+v", extra)
				default:
				}
			}

			// The MCP write path types its own wrapped command into the same
			// PTY. The hook captures that wrapper like user input, but it must
			// never be journaled; the next user command is the next event.
			programmatic, err := service.Command(context.Background(), session.ID(),
				"echo netcatty_programmatic_42", appsession.CommandOptions{
					Timeout: 15 * time.Second,
				})
			if err != nil {
				t.Fatalf("programmatic command: %v", err)
			}
			if !strings.Contains(programmatic.Stdout, "netcatty_programmatic_42") {
				t.Fatalf("programmatic stdout = %q", programmatic.Stdout)
			}
			after := "echo netcatty_after_$((10+10))"
			if name == "fish" {
				after = "echo netcatty_after_(math 10+10)"
			}
			service.SendInput([]model.SessionID{session.ID()}, []byte(after+"\r"))
			// A wrapper event would arrive first and fail the exact match.
			awaitTierOne(t, events, after)
		})
	}
}

// nushellPath returns the real nushell this machine can run. Nushell ships as
// nu; NETCATTY_TEST_NU can name a provisioned binary.
func nushellPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("NETCATTY_TEST_NU")
	if path == "" {
		var err error
		path, err = exec.LookPath("nu")
		if err != nil {
			t.Skip("nushell is not available")
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		t.Skipf("nushell at %q is not executable", path)
	}
	return path
}

// nuStartupEnv isolates nushell's config directory and installs a user config
// whose marker proves the user startup ran before the hook. Nushell reads its
// user config from $XDG_CONFIG_HOME/nushell/config.nu.
func nuStartupEnv(t *testing.T, home string) []string {
	t.Helper()
	configDir := filepath.Join(home, ".config", "nushell")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "print \"netcatty-user-startup-loaded\"\n$env.NETCATTY_TEST_USER_PROMPT = 1\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.nu"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}
}

// TestRealNushellSessionCapturesTierOneEndToEnd drives the real Session stack
// with nushell. Nushell's pre_execution hook has the exact command line and cwd
// and its pre_prompt hook has LAST_EXIT_CODE, so one Tier 1 row per command
// carries every captured field while the user's own config still runs first.
func TestRealNushellSessionCapturesTierOneEndToEnd(t *testing.T) {
	path := nushellPath(t)
	for _, key := range []string{
		"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
		"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
		"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
		"NETCATTY_NU_PENDING",
	} {
		unsetEnv(t, key)
	}
	home := t.TempDir()
	workDir := t.TempDir()
	subDir := filepath.Join(workDir, "sub")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(nuStartupEnv(t, home), "SHELL="+path)
	events := make(chan appsession.CommandEvent, 16)
	service := appsession.NewService(localpty.Factory{},
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
		Command: []string{path, "-l"},
		Dir:     workDir,
		Env:     env,
		Cols:    100, Rows: 40,
	})
	if err != nil {
		t.Fatalf("open nushell: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
		}
	})
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty-user-startup-loaded")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the nushell user config never ran: snapshot=%+v screen=%q",
			session.Snapshot(), text.Text)
	}
	expectedCWD := workDir
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		expectedCWD = resolved
	}
	expectedSub := subDir
	if resolved, err := filepath.EvalSymlinks(subDir); err == nil {
		expectedSub = resolved
	}

	// A literal command makes the expected capture independent of nushell
	// arithmetic syntax.
	warmup := "echo netcatty_nu_ready_2"
	service.SendInput([]model.SessionID{session.ID()}, []byte(warmup+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_nu_ready_2")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the nushell warm-up never ran; screen=%q", text.Text)
	}
	awaitTierOne(t, events, warmup)

	exchanges := []struct {
		typed string
		exit  int
	}{
		{typed: "echo netcatty_nu_42"},
		// Nushell's bare `false` is a boolean value, not a failing command;
		// ^false runs the external program so LAST_EXIT_CODE is a real 1.
		{typed: "^false", exit: 1},
		{typed: `echo $"netcatty_user_($env.NETCATTY_TEST_USER_PROMPT)"`},
	}
	for _, step := range exchanges {
		if result := service.SendInput([]model.SessionID{session.ID()}, []byte(step.typed+"\r")); result.DeliveredCount() != 1 {
			t.Fatalf("send %q: %+v", step.typed, result)
		}
		event := awaitTierOne(t, events, step.typed)
		if event.Shell != "nu" || event.CaptureTier != domainhistory.CaptureTierHook {
			t.Fatalf("completion = %+v", event)
		}
		if event.CWD != workDir && event.CWD != expectedCWD {
			t.Fatalf("cwd = %q, want %q", event.CWD, workDir)
		}
		if !event.HasExitCode || event.ExitCode != step.exit {
			t.Fatalf("exit = %d/%t, want %d", event.ExitCode, event.HasExitCode, step.exit)
		}
		if event.StartedAt.IsZero() || event.FinishedAt.Before(event.StartedAt) {
			t.Fatalf("times = %v..%v", event.StartedAt, event.FinishedAt)
		}
		select {
		case extra := <-events:
			t.Fatalf("the command was captured twice: %+v", extra)
		default:
		}
	}
	// The user's config set an environment variable, proving it ran and stayed
	// in effect after the hook appended itself.
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_user_1")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the user config did not take effect; screen=%q", text.Text)
	}

	// The pre_execution hook reports the cwd at the moment the command starts:
	// cd itself runs in workDir, and the next command runs in sub.
	service.SendInput([]model.SessionID{session.ID()}, []byte("cd sub\r"))
	cdEvent := awaitTierOne(t, events, "cd sub")
	if cdEvent.CWD != workDir && cdEvent.CWD != expectedCWD {
		t.Fatalf("cd cwd = %q, want %q", cdEvent.CWD, workDir)
	}
	afterCD := "echo netcatty_after_cd_42"
	service.SendInput([]model.SessionID{session.ID()}, []byte(afterCD+"\r"))
	cdAfter := awaitTierOne(t, events, afterCD)
	if cdAfter.CWD != subDir && cdAfter.CWD != expectedSub {
		t.Fatalf("cwd after cd = %q, want %q", cdAfter.CWD, subDir)
	}
	if !cdAfter.HasExitCode || cdAfter.ExitCode != 0 {
		t.Fatalf("exit after cd = %d/%t, want 0", cdAfter.ExitCode, cdAfter.HasExitCode)
	}
}

// xonshPath returns the real xonsh this machine can run. xonsh is not always
// installed; NETCATTY_TEST_XONSH can name a provisioned binary.
func xonshPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("NETCATTY_TEST_XONSH")
	if path == "" {
		var err error
		path, err = exec.LookPath("xonsh")
		if err != nil {
			t.Skip("xonsh is not available")
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		t.Skipf("xonsh at %q is not executable", path)
	}
	return path
}

// xonshStartupEnv isolates xonsh's config directory and installs a user rc.d
// file whose marker proves the user startup runs before the session hook,
// which the planner appends to the end of XONSHRC_DIR.
func xonshStartupEnv(t *testing.T, home string) []string {
	t.Helper()
	rcDir := filepath.Join(home, ".config", "xonsh", "rc.d")
	if err := os.MkdirAll(rcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "print(\"netcatty-user-startup-loaded\")\n$NETCATTY_TEST_USER_PROMPT = 1\n"
	if err := os.WriteFile(filepath.Join(rcDir, "00-netcatty-test.xsh"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}
}

// unsetXonshHookEnv removes the developer's xonsh startup environment so the
// Session sees only the isolated home the test supplies.
func unsetXonshHookEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"XONSHRC_DIR", "XONSH_CONFIG_DIR", "XONSH_SYS_CONFIG_DIR",
		"XONSHRC", "XONSH_LOGIN",
	} {
		unsetEnv(t, key)
	}
}

// TestRealXonshSessionCapturesTierOneEndToEnd drives the real Session stack
// with xonsh. The event handlers carry the exact command, cwd, return code,
// and finish time, so every command is one Tier 1 row while the user's rc.d
// startup still runs first.
func TestRealXonshSessionCapturesTierOneEndToEnd(t *testing.T) {
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
	workDir := t.TempDir()
	subDir := filepath.Join(workDir, "sub")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(xonshStartupEnv(t, home), "SHELL="+path)
	events := make(chan appsession.CommandEvent, 16)
	service := appsession.NewService(localpty.Factory{},
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
		Command: []string{path, "-l"},
		Dir:     workDir,
		Env:     env,
		Cols:    100, Rows: 40,
	})
	if err != nil {
		t.Fatalf("open xonsh: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
		}
	})
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty-user-startup-loaded")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the xonsh user startup never ran: snapshot=%+v screen=%q",
			session.Snapshot(), text.Text)
	}
	expectedCWD := workDir
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		expectedCWD = resolved
	}
	expectedSub := subDir
	if resolved, err := filepath.EvalSymlinks(subDir); err == nil {
		expectedSub = resolved
	}

	warmup := "echo netcatty_xonsh_ready_2"
	service.SendInput([]model.SessionID{session.ID()}, []byte(warmup+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_xonsh_ready_2")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the xonsh warm-up never ran; screen=%q", text.Text)
	}
	awaitTierOne(t, events, warmup)

	exchanges := []struct {
		typed string
		exit  int
	}{
		{typed: "echo netcatty_xonsh_42"},
		{typed: "false", exit: 1},
		{typed: `print("netcatty_user_" + str($NETCATTY_TEST_USER_PROMPT))`},
	}
	for _, step := range exchanges {
		if result := service.SendInput([]model.SessionID{session.ID()}, []byte(step.typed+"\r")); result.DeliveredCount() != 1 {
			t.Fatalf("send %q: %+v", step.typed, result)
		}
		event := awaitTierOne(t, events, step.typed)
		if event.Shell != "xonsh" || event.CaptureTier != domainhistory.CaptureTierHook {
			t.Fatalf("completion = %+v", event)
		}
		if event.CWD != workDir && event.CWD != expectedCWD {
			t.Fatalf("cwd = %q, want %q", event.CWD, workDir)
		}
		if !event.HasExitCode || event.ExitCode != step.exit {
			t.Fatalf("exit = %d/%t, want %d", event.ExitCode, event.HasExitCode, step.exit)
		}
		if event.StartedAt.IsZero() || event.FinishedAt.Before(event.StartedAt) {
			t.Fatalf("times = %v..%v", event.StartedAt, event.FinishedAt)
		}
		select {
		case extra := <-events:
			t.Fatalf("the command was captured twice: %+v", extra)
		default:
		}
	}
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_user_1")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the user rc did not take effect; screen=%q", text.Text)
	}

	service.SendInput([]model.SessionID{session.ID()}, []byte("cd sub\r"))
	cdEvent := awaitTierOne(t, events, "cd sub")
	if cdEvent.CWD != workDir && cdEvent.CWD != expectedCWD {
		t.Fatalf("cd cwd = %q, want %q", cdEvent.CWD, workDir)
	}
	afterCD := "echo netcatty_after_cd_42"
	service.SendInput([]model.SessionID{session.ID()}, []byte(afterCD+"\r"))
	cdAfter := awaitTierOne(t, events, afterCD)
	if cdAfter.CWD != subDir && cdAfter.CWD != expectedSub {
		t.Fatalf("cwd after cd = %q, want %q", cdAfter.CWD, subDir)
	}
}

// TestRealXonshWithoutHookKeepsNativeFallback starts xonsh with the hook
// disabled. Xonsh emits OSC 7 at its first interactive prompt, so the typed
// command starts as Tier 2 with the cwd the shell reported. It does not emit
// OSC 133 and its default prompt is not the heuristic's shape, so the command
// is finalized at Session close with no exit or finish. The row must still be
// captured exactly once.
func TestRealXonshWithoutHookKeepsNativeFallback(t *testing.T) {
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
	workDir := t.TempDir()
	env := append(xonshStartupEnv(t, home), "SHELL="+path)
	events := make(chan appsession.CommandEvent, 16)
	service := appsession.NewService(localpty.Factory{},
		appsession.WithCaptureHook(NewPlanner(
			WithDefaultShell(func() (string, error) { return path, nil }),
			WithEnabled(func() bool { return false }),
		)),
		appsession.WithCommandCompletionHook(func(event appsession.CommandEvent) {
			events <- event
		}),
	)
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: []string{path, "-l"},
		Dir:     workDir,
		Env:     env,
		Cols:    100, Rows: 40,
	})
	if err != nil {
		t.Fatalf("open xonsh: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
		}
	})
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty-user-startup-loaded")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the xonsh user startup never ran: %q", text.Text)
	}
	// The rc marker is printed while xonsh loads its startup files, before the
	// shell builds its interactive prompt. Wait for the OSC 7 that precedes the
	// first prompt, so the typed command starts with the reported cwd.
	if !waitCondition(func() bool { return session.Snapshot().CWD != "" }) {
		t.Fatalf("xonsh never reported its cwd: snapshot=%+v", session.Snapshot())
	}
	expectedCWD := workDir
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		expectedCWD = resolved
	}
	typed := "echo netcatty_xonsh_typed_42"
	service.SendInput([]model.SessionID{session.ID()}, []byte(typed+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_xonsh_typed_42")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the typed-fallback command never ran: %q", text.Text)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	event := awaitEvent(t, events)
	if event.Command != typed {
		t.Fatalf("completion = %+v, want %q", event, typed)
	}
	if event.CaptureTier != domainhistory.CaptureTierNativeOSC {
		t.Fatalf("fallback tier = %d, want Tier 2 from OSC 7: %+v", event.CaptureTier, event)
	}
	if event.CWD != workDir && event.CWD != expectedCWD {
		t.Fatalf("the OSC 7 cwd = %q, want %q: %+v", event.CWD, workDir, event)
	}
	if event.HasExitCode {
		t.Fatalf("the typed fallback invented an exit code: %+v", event)
	}
	if !event.FinishedAt.IsZero() {
		t.Fatalf("the typed fallback invented a finish: %+v", event)
	}
	select {
	case extra := <-events:
		t.Fatalf("the fallback was captured twice: %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestRealNushellWithoutHookKeepsNativeCapture starts nushell with the hook
// disabled, as the planner does when the setting is off. Nushell's own OSC
// 133/7 integration is the fallback: the command is still captured once, as
// Tier 2, with the cwd and finish the shell reports. The exit code is whatever
// nushell's native D carries; the test records the observed fidelity.
func TestRealNushellWithoutHookKeepsNativeCapture(t *testing.T) {
	path := nushellPath(t)
	for _, key := range []string{
		"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
		"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
		"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
		"NETCATTY_NU_PENDING",
	} {
		unsetEnv(t, key)
	}
	home := t.TempDir()
	workDir := t.TempDir()
	env := append(nuStartupEnv(t, home), "SHELL="+path)
	events := make(chan appsession.CommandEvent, 16)
	service := appsession.NewService(localpty.Factory{},
		appsession.WithCaptureHook(NewPlanner(
			WithDefaultShell(func() (string, error) { return path, nil }),
			WithEnabled(func() bool { return false }),
		)),
		appsession.WithCommandCompletionHook(func(event appsession.CommandEvent) {
			events <- event
		}),
	)
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: []string{path, "-l"},
		Dir:     workDir,
		Env:     env,
		Cols:    100, Rows: 40,
	})
	if err != nil {
		t.Fatalf("open nushell: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
		}
	})
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty-user-startup-loaded")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the nushell user config never ran: %q", text.Text)
	}
	// The user config marker prints while nushell is still starting, before
	// the first prompt. Wait for the OSC 7 that precedes that prompt so the
	// typed command starts with the reported cwd; typing earlier races the
	// native capture and records Tier 2 without a cwd under load.
	if !waitCondition(func() bool { return session.Snapshot().CWD != "" }) {
		t.Fatalf("nushell never reported its cwd: snapshot=%+v", session.Snapshot())
	}

	expectedCWD := workDir
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		expectedCWD = resolved
	}
	typed := "echo netcatty_nu_native_42"
	service.SendInput([]model.SessionID{session.ID()}, []byte(typed+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_nu_native_42")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the native-capture command never ran: %q", text.Text)
	}
	event := awaitEvent(t, events)
	if event.Command != typed {
		t.Fatalf("completion = %+v, want %q", event, typed)
	}
	if event.CaptureTier != domainhistory.CaptureTierNativeOSC {
		t.Fatalf("native capture tier = %d, want Tier 2: %+v", event.CaptureTier, event)
	}
	if event.CWD != workDir && event.CWD != expectedCWD {
		t.Fatalf("native cwd = %q, want %q", event.CWD, workDir)
	}
	if event.FinishedAt.IsZero() {
		t.Fatalf("the native prompt never reported a finish: %+v", event)
	}
	if event.HasExitCode {
		t.Fatalf("the native fallback invented an exit code: %+v", event)
	}
	t.Logf("nushell native fidelity: tier=%d cwd=%q exit=%d/%t duration=%v",
		event.CaptureTier, event.CWD, event.ExitCode, event.HasExitCode,
		event.FinishedAt.Sub(event.StartedAt))
	select {
	case extra := <-events:
		t.Fatalf("the native command was captured twice: %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}
}

// lastNetcattyExec returns the most recent decodable private sequence, so a
// test can wait for a capture after an earlier command has already emitted
// one.
func lastNetcattyExec(output string) (shell, cwd, command string, ok bool) {
	const prefix = "\x1b]1337;NetcattyExec;"
	for _, part := range strings.Split(output, prefix)[1:] {
		end := strings.IndexByte(part, '\a')
		if end < 0 {
			continue
		}
		fields := strings.Split(part[:end], ";")
		if len(fields) != 3 {
			continue
		}
		decodedCWD, cwdOK := decodeBase64URL(fields[1])
		decodedCommand, commandOK := decodeBase64URL(fields[2])
		if cwdOK && commandOK {
			shell, cwd, command, ok = fields[0], decodedCWD, decodedCommand, true
		}
	}
	return shell, cwd, command, ok
}

// TestRealBashCapturesACommandHiddenFromHistory covers HISTCONTROL=ignorespace
// and HISTIGNORE: bash keeps the hidden command out of history, so a hook that
// reads only `history 1` would record the previous command instead. The
// capture must carry the command the user actually ran.
func TestRealBashCapturesACommandHiddenFromHistory(t *testing.T) {
	path, available := availableShells()["bash"]
	if !available {
		t.Skip("bash is not available")
	}
	cases := []struct {
		name    string
		setting string
		hidden  string
		want    string
	}{
		{
			name:    "ignorespace",
			setting: "HISTCONTROL=ignorespace",
			hidden:  " echo netcatty_hidden_$((40+2))",
			want:    "echo netcatty_hidden_$((40+2))",
		},
		{
			name:    "histignore",
			setting: "HISTIGNORE=*netcatty_hidden*",
			hidden:  "echo netcatty_hidden_$((40+2))",
			want:    "echo netcatty_hidden_$((40+2))",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			env := append(startupEnv(t, "bash", home), testCase.setting)
			plan := localPlan(t, path, env)
			plan.Spec.Dir = workDir
			process := startShell(t, path, plan.Spec.Command, workDir, append(env, plan.Spec.Env...))
			process.waitFor(t, "the first prompt", func(output string) bool {
				return strings.Contains(output, "\x1b]133;A")
			})

			// Seed history with a visible command; a stale `history 1` read
			// for the hidden command would replay exactly this one.
			previous := "echo netcatty_previous_$((40+2))"
			process.typeLine(t, previous)
			process.waitFor(t, "the previous command's capture", func(output string) bool {
				_, _, command, ok := lastNetcattyExec(output)
				return ok && command == previous
			})

			before := len(process.snapshotString())
			process.typeLine(t, testCase.hidden)
			output := process.waitFor(t, "the hidden command's capture", func(output string) bool {
				_, _, command, ok := lastNetcattyExec(output)
				return ok && command == testCase.want
			})
			// The private sequence is preexec; wait for the result to prove the
			// hidden command ran rather than just being reported.
			process.waitFor(t, "the hidden command's output", func(output string) bool {
				return strings.Contains(output[before:], "netcatty_hidden_42")
			})
			if _, _, command, ok := lastNetcattyExec(output); !ok || command != testCase.want {
				t.Fatalf("captured command = %q/%t after %q, want %q",
					command, ok, testCase.hidden, testCase.want)
			}
		})
	}
}

// TestRealBashSourcesTheProfileAndBashrcOnce starts bash with a profile that
// sources .bashrc, as most login setups do. The startup script must run the
// interactive rc exactly once while still sourcing the profile.
func TestRealBashSourcesTheProfileAndBashrcOnce(t *testing.T) {
	path, available := availableShells()["bash"]
	if !available {
		t.Skip("bash is not available")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	env := startupEnv(t, "bash", home)
	rcBody := `echo netcatty-user-startup-loaded
__netcatty_test_rc_runs=$(( ${__netcatty_test_rc_runs:-0} + 1 ))
__netcatty_test_user_prompt() { NETCATTY_TEST_USER_PROMPT=1; }
PROMPT_COMMAND=__netcatty_test_user_prompt
`
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(rcBody), 0o600); err != nil {
		t.Fatal(err)
	}
	profileBody := "echo netcatty-profile-loaded\n. \"$HOME/.bashrc\"\n"
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(profileBody), 0o600); err != nil {
		t.Fatal(err)
	}

	plan := localPlan(t, path, env)
	plan.Spec.Dir = workDir
	process := startShell(t, path, plan.Spec.Command, workDir, append(env, plan.Spec.Env...))
	output := process.waitFor(t, "the first prompt", func(output string) bool {
		return strings.Contains(output, "\x1b]133;A")
	})
	for _, marker := range []string{"netcatty-profile-loaded", "netcatty-user-startup-loaded"} {
		if !strings.Contains(output, marker) {
			t.Fatalf("startup marker %q is missing from %q", marker, output)
		}
	}

	process.typeLine(t, "echo netcatty_rc_count=$__netcatty_test_rc_runs")
	output = process.waitFor(t, "the .bashrc run count", func(output string) bool {
		// The echoed input also contains the marker; the result starts on its
		// own line, right after the hook's preexec sequence.
		return strings.Contains(output, "\nnetcatty_rc_count=") ||
			strings.Contains(output, "\anetcatty_rc_count=")
	})
	if !strings.Contains(output, "netcatty_rc_count=1") {
		t.Fatalf(".bashrc did not run exactly once: %q", output)
	}

	// The startup path must leave the hook working, not just the startup files.
	typed := "echo netcatty_after_startup_$((20+2))"
	process.typeLine(t, typed)
	process.waitFor(t, "the hook's capture", func(output string) bool {
		_, _, command, ok := lastNetcattyExec(output)
		return ok && command == typed
	})
}

// TestRealFishPrivateModeStaysUncaptured starts fish in private mode with the
// hook file explicitly sourced. The planner rejects --private, but the script
// itself must also refuse to install in a private shell.
func TestRealFishPrivateModeStaysUncaptured(t *testing.T) {
	path, available := availableShells()["fish"]
	if !available {
		t.Skip("fish is not available")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	env := startupEnv(t, "fish", home)
	plan := localPlan(t, path, env)
	// pty.Start takes the full argv, so argv[0] names the program and the
	// options follow it.
	process := startShell(t, path, []string{
		path, "--private", "--init-command",
		`echo netcatty-private-init; source "$NETCATTY_HOOK_FILE"`,
	}, workDir, append(env, plan.Spec.Env...))
	output := process.waitFor(t, "the first prompt", func(output string) bool {
		return strings.Contains(output, "netcatty-private-init")
	})
	process.typeLine(t, "echo netcatty_private_(math 40+2)")
	output = process.waitFor(t, "the command output", func(output string) bool {
		return strings.Contains(output, "netcatty_private_42")
	})
	if count := countNetcattyExec(output); count != 0 {
		t.Fatalf("private-mode fish emitted %d captures: %q", count, output)
	}
	if strings.Contains(output, "\x1b]1337;NetcattyHook;") {
		t.Fatalf("private-mode fish announced the hook: %q", output)
	}
	if !strings.Contains(output, "\x1b]1337;NetcattyHookSkip;fish\x07") {
		t.Fatalf("private-mode fish did not announce its skip: %q", output)
	}
}

// TestRealShellSessionCapturesMultilineCommandsOnce sends a quoted command in
// two writes with Enter between them. The hook-active handshake makes the
// typed Enter a continuation rather than a partial command, and the shell
// reports the whole logical command exactly once.
func TestRealShellSessionCapturesMultilineCommandsOnce(t *testing.T) {
	shells := availableShells()
	if len(shells) == 0 {
		t.Skip("no zsh, bash, or fish available")
	}
	for name, path := range shells {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{
				"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
				"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
				"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
			} {
				unsetEnv(t, key)
			}
			home := t.TempDir()
			workDir := t.TempDir()
			env := append(startupEnv(t, name, home), "SHELL="+path)
			events := make(chan appsession.CommandEvent, 8)
			service := appsession.NewService(localpty.Factory{},
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
				t.Fatalf("open the real shell: %v", err)
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
				t.Fatalf("the first prompt never arrived: snapshot=%+v screen=%q",
					session.Snapshot(), text.Text)
			}

			warmUpHookedSession(t, service, session, events, name)

			first, second := `echo "netcatty_multi_one`, `netcatty_multi_two"`
			service.SendInput([]model.SessionID{session.ID()}, []byte(first+"\r"))
			select {
			case event := <-events:
				t.Fatalf("the unfinished first line was captured: %+v", event)
			case <-time.After(500 * time.Millisecond):
			}
			service.SendInput([]model.SessionID{session.ID()}, []byte(second+"\r"))
			event := awaitEvent(t, events)
			if event.CaptureTier != domainhistory.CaptureTierHook {
				t.Fatalf("multiline capture tier = %d, want the hook's Tier 1: %+v",
					event.CaptureTier, event)
			}
			if !strings.Contains(event.Command, "netcatty_multi_one") ||
				!strings.Contains(event.Command, "netcatty_multi_two") {
				t.Fatalf("multiline capture = %q, want the whole logical command", event.Command)
			}
			select {
			case extra := <-events:
				t.Fatalf("the multiline command was captured more than once: %+v", extra)
			default:
			}
		})
	}
}

// openLocalSession starts a local Session with the hook planner and waits for
// the caller's readiness condition. The event channel carries every
// completion. The developer's shell integration guards are removed first
// because localpty inherits the process environment.
func openLocalSession(t *testing.T, name, path, home, workDir string,
	ready func(*appsession.Service, *appsession.Session) bool,
	prepare func(home string), extraEnv ...string) (*appsession.Service, *appsession.Session, <-chan appsession.CommandEvent) {
	t.Helper()
	for _, key := range []string{
		"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
		"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
		"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
	} {
		unsetEnv(t, key)
	}
	env := append(startupEnv(t, name, home), "SHELL="+path)
	if prepare != nil {
		// The user's rc file runs after /etc/profile and before the hook, so a
		// test can still override what the profile restored.
		prepare(home)
	}
	env = append(env, extraEnv...)
	events := make(chan appsession.CommandEvent, 32)
	service := appsession.NewService(localpty.Factory{},
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
		t.Fatalf("open the real shell: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
		}
	})
	if !waitCondition(func() bool { return ready(service, session) }) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the shell never became ready: snapshot=%+v screen=%q",
			session.Snapshot(), text.Text)
	}
	return service, session, events
}

// openHookedSession starts a local Session whose hook installs, and waits for
// the first shell prompt.
func openHookedSession(t *testing.T, name, path, home, workDir string,
	extraEnv ...string) (*appsession.Service, *appsession.Session, <-chan appsession.CommandEvent) {
	t.Helper()
	return openLocalSession(t, name, path, home, workDir,
		func(_ *appsession.Service, session *appsession.Session) bool {
			return session.Snapshot().CWD != ""
		}, nil, extraEnv...)
}

// waitForShellInputReady waits out fish's one-time terminal Device Attribute
// timeout. Until it expires fish does not read typed input; a line sent earlier
// is buffered, and fish's prompt redraw when the wait ends can finalize that
// line before the hook's first prompt handshake is seen. The timeout prints a
// warning, so the warning is the observable signal; if a terminal answers the
// query there is no warning and the wait simply expires.
func waitForShellInputReady(t *testing.T, service *appsession.Service,
	session *appsession.Session, name string) {
	t.Helper()
	if name != "fish" {
		return
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		text, _ := service.ScreenText(session.ID())
		if strings.Contains(text.Text, "Primary Device Attribute query") {
			time.Sleep(200 * time.Millisecond)
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// warmUpHookedSession runs one command and waits for its Tier 1 event, so the
// shell's line editor is ready before a test makes timing assertions.
func warmUpHookedSession(t *testing.T, service *appsession.Service, session *appsession.Session,
	events <-chan appsession.CommandEvent, name string) {
	t.Helper()
	waitForShellInputReady(t, service, session, name)
	warmup := "echo netcatty_ready_$((1+1))"
	if name == "fish" {
		warmup = "echo netcatty_ready_(math 1+1)"
	}
	service.SendInput([]model.SessionID{session.ID()}, []byte(warmup+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_ready_2")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the warm-up never ran; screen=%q", text.Text)
	}
	awaitTierOne(t, events, warmup)
}

// awaitCommandEvent discards unrelated completions (a nested shell's outer
// command can finish while its inner commands run) until command arrives.
func awaitCommandEvent(t *testing.T, events <-chan appsession.CommandEvent,
	command string, onEvent func(appsession.CommandEvent)) appsession.CommandEvent {
	t.Helper()
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if onEvent != nil {
				onEvent(event)
			}
			if event.Command == command {
				return event
			}
		case <-deadline.C:
			t.Fatalf("no completion for %q arrived", command)
			return appsession.CommandEvent{}
		}
	}
}

// TestRealShellSessionIgnoresPromptLikeOutputUntilTheHookFinishes is the D1
// regression: a command whose output ends in a prompt-like line (12%) must not
// be finalized by the prompt heuristic. Exactly one Tier 1 event with the real
// exit code and the command's real duration must arrive at OSC 133 D.
func TestRealShellSessionIgnoresPromptLikeOutputUntilTheHookFinishes(t *testing.T) {
	shells := availableShells()
	if len(shells) == 0 {
		t.Skip("no zsh, bash, or fish available")
	}
	for name, path := range shells {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			service, session, events := openHookedSession(t, name, path, home, workDir)
			warmUpHookedSession(t, service, session, events, name)

			command := `printf '12%%\n'; sleep 2; echo finished_$((40+2))`
			if name == "fish" {
				command = `printf '12%%\n'; sleep 2; echo finished_(math 40+2)`
			}
			service.SendInput([]model.SessionID{session.ID()}, []byte(command+"\r"))
			event := awaitEvent(t, events)
			if event.Command != command {
				t.Fatalf("completion = %+v, want %q", event, command)
			}
			if event.CaptureTier != domainhistory.CaptureTierHook {
				t.Fatalf("capture tier = %d, want the hook's Tier 1", event.CaptureTier)
			}
			if !event.HasExitCode || event.ExitCode != 0 {
				t.Fatalf("exit = %d/%t, want a real 0", event.ExitCode, event.HasExitCode)
			}
			if duration := event.FinishedAt.Sub(event.StartedAt); duration < 1500*time.Millisecond {
				t.Fatalf("duration = %v, want the command's real duration", duration)
			}
			select {
			case extra := <-events:
				t.Fatalf("prompt-like output produced a second row: %+v", extra)
			case <-time.After(300 * time.Millisecond):
			}
		})
	}
}

// TestRealNestedShellFallsBackToTypedCapture is the D2 regression: the outer
// zsh has the hook, but a bash started inside it does not. The inner command
// must still be captured as Tier 2/3, and the outer hook must work again after
// the nested shell exits.
func TestRealNestedShellFallsBackToTypedCapture(t *testing.T) {
	outer, outerOK := availableShells()["zsh"]
	inner, innerOK := availableShells()["bash"]
	if !outerOK || !innerOK {
		t.Skip("zsh and bash are required")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	// The outer session is zsh, so the isolated home only has zsh startup
	// files. Give the nested bash its own rc marker to detect its prompt.
	if err := os.WriteFile(filepath.Join(home, ".bashrc"),
		[]byte("echo netcatty-inner-startup-loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, session, events := openHookedSession(t, "zsh", outer, home, workDir)
	warmUpHookedSession(t, service, session, events, "zsh")

	service.SendInput([]model.SessionID{session.ID()}, []byte(inner+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty-inner-startup-loaded")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the nested bash never reached a prompt: %q", text.Text)
	}
	// Wait for the tap to see the inner prompt and hand capture over before
	// typing at it; the startup marker precedes the prompt on the wire.
	awaitCommandEvent(t, events, inner, nil)

	nested := "echo nested_$((40+2))"
	service.SendInput([]model.SessionID{session.ID()}, []byte(nested+"\r"))
	nestedEvent := awaitCommandEvent(t, events, nested, nil)
	if nestedEvent.CaptureTier == domainhistory.CaptureTierHook {
		t.Fatalf("the nested command claims Tier 1: %+v", nestedEvent)
	}

	service.SendInput([]model.SessionID{session.ID()}, []byte("exit\r"))
	// The outer shell's D for the nested /bin/bash command finishes the typed
	// exit line and proves the nested shell returned to the hooked session.
	awaitCommandEvent(t, events, "exit", nil)
	// The outer prompt handshake follows that D in the same prompt cycle.
	time.Sleep(300 * time.Millisecond)
	back := "echo back_$((40+2))"
	service.SendInput([]model.SessionID{session.ID()}, []byte(back+"\r"))
	backEvent := awaitCommandEvent(t, events, back, nil)
	if backEvent.CaptureTier != domainhistory.CaptureTierHook {
		t.Fatalf("the outer command lost Tier 1 after the nested shell: %+v", backEvent)
	}
}

// TestRealZshWithoutEncoderKeepsTheTypedFallback is the D4 regression: with no
// base64/tr on PATH the hook installs nothing (including its prompt
// handshake). A planned hook that never speaks leaves the typed capture
// tentative; Session close must finalize it once with an unknown finish rather
// than lose it.
func TestRealZshWithoutEncoderKeepsTheTypedFallback(t *testing.T) {
	path, available := availableShells()["zsh"]
	if !available {
		t.Skip("zsh is not available")
	}
	for _, key := range []string{
		"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
		"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
		"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
	} {
		unsetEnv(t, key)
	}
	home := t.TempDir()
	workDir := t.TempDir()
	emptyPath := t.TempDir()
	env := append(startupEnv(t, "zsh", home), "SHELL="+path, "PATH="+emptyPath)
	events := make(chan appsession.CommandEvent, 8)
	service := appsession.NewService(localpty.Factory{},
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
		t.Fatalf("open zsh without an encoder: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
		}
	})
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty-user-startup-loaded")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("zsh did not finish its startup: %q", text.Text)
	}
	typed := "echo netcatty_noencoder_$((40+2))"
	service.SendInput([]model.SessionID{session.ID()}, []byte(typed+"\r"))
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_noencoder_42")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the encoder-less command never ran: %q", text.Text)
	}
	// The encoder-less hook announces its skip, so the held capture is released
	// to the typed fallback instead of waiting for Session close. An empty
	// Enter redraws the prompt that finishes it.
	service.SendInput([]model.SessionID{session.ID()}, []byte("\r"))
	event := awaitEvent(t, events)
	if event.Command != typed {
		t.Fatalf("completion = %+v, want %q", event, typed)
	}
	if event.CaptureTier == domainhistory.CaptureTierHook {
		t.Fatalf("the fallback claimed Tier 1: %+v", event)
	}
	if event.FinishedAt.IsZero() {
		t.Fatalf("the skipped hook held the command until close: %+v", event)
	}
	if event.HasExitCode {
		t.Fatalf("the typed fallback invented an exit code: %+v", event)
	}
	select {
	case extra := <-events:
		t.Fatalf("the fallback was published twice: %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case extra := <-events:
		t.Fatalf("Session close published a duplicate: %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestRealBashWithoutEncoderSurvivesSetE starts bash with set -e in its rc and
// no base64/tr on PATH. The encoder probe fails; the hook must return without
// exiting the interactive shell.
func TestRealBashWithoutEncoderSurvivesSetE(t *testing.T) {
	path, available := availableShells()["bash"]
	if !available {
		t.Skip("bash is not available")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	emptyPath := t.TempDir()
	env := startupEnv(t, "bash", home)
	// The user's rc runs before the hook and leaves no encoder on PATH. The
	// profile may restore PATH earlier, so the test sets it here.
	rc := "set -e\nPATH='" + emptyPath + "'\necho netcatty-bash-sete-loaded\n"
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := localPlan(t, path, env)
	plan.Spec.Dir = workDir
	process := startShell(t, path, plan.Spec.Command, workDir, append(env, plan.Spec.Env...))
	output := process.waitFor(t, "the bash startup", func(output string) bool {
		return strings.Contains(output, "netcatty-bash-sete-loaded")
	})
	process.typeLine(t, "echo netcatty_bash_survived_$((40+2))")
	output = process.waitFor(t, "the command output", func(output string) bool {
		return strings.Contains(output, "netcatty_bash_survived_42")
	})
	if strings.Contains(output, "\x1b]1337;NetcattyHook;") || countNetcattyExec(output) != 0 {
		t.Fatalf("the encoder-less bash emitted hook sequences: %q", output)
	}
	if !strings.Contains(output, "\x1b]1337;NetcattyHookSkip;bash\x07") {
		t.Fatalf("the encoder-less bash did not announce its skip: %q", output)
	}
}

// TestRealZshWithoutEncoderEmitsNoHandshake checks the raw terminal stream: a
// hook that cannot encode the private sequence must install nothing, including
// the prompt handshake that would make the tap suppress its Tier 2/3 fallback.
// The shell itself still works without the missing utilities.
func TestRealZshWithoutEncoderEmitsNoHandshake(t *testing.T) {
	path, available := availableShells()["zsh"]
	if !available {
		t.Skip("zsh is not available")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	emptyPath := t.TempDir()
	env := append(startupEnv(t, "zsh", home), "PATH="+emptyPath)
	plan := localPlan(t, path, env)
	plan.Spec.Dir = workDir
	process := startShell(t, path, plan.Spec.Command, workDir, append(env, plan.Spec.Env...))
	output := process.waitFor(t, "the skipped hook", func(output string) bool {
		return strings.Contains(output, "\x1b]1337;NetcattyHookSkip;zsh\x07")
	})
	if !strings.Contains(output, "netcatty-user-startup-loaded") {
		t.Fatalf("the user's startup did not run: %q", output)
	}
	if strings.Contains(output, "\x1b]1337;NetcattyHook;") {
		t.Fatalf("the hook announced itself without an encoder: %q", output)
	}
	if count := countNetcattyExec(output); count != 0 {
		t.Fatalf("the hook emitted %d private sequences without an encoder: %q", count, output)
	}
	if !strings.Contains(output, "\x1b]1337;NetcattyHookSkip;zsh\x07") {
		t.Fatalf("the encoder-less zsh did not announce its skip: %q", output)
	}

	process.typeLine(t, "echo netcatty_noencoder_$((40+2))")
	output = process.waitFor(t, "the command output", func(output string) bool {
		return strings.Contains(output, "netcatty_noencoder_42")
	})
	if strings.Contains(output, "\x1b]1337;NetcattyHook;") || countNetcattyExec(output) != 0 {
		t.Fatalf("the encoder-less shell emitted hook sequences: %q", output)
	}
}

// collectEventsUntil gathers completions until every wanted command has at
// least one, so a test can prove a wrong earlier row also arrived.
func collectEventsUntil(t *testing.T, events <-chan appsession.CommandEvent,
	timeout time.Duration, wanted ...string) []appsession.CommandEvent {
	t.Helper()
	remaining := make(map[string]bool, len(wanted))
	for _, command := range wanted {
		remaining[command] = true
	}
	collected := []appsession.CommandEvent{}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for len(remaining) > 0 {
		select {
		case event := <-events:
			collected = append(collected, event)
			delete(remaining, event.Command)
		case <-timer.C:
			t.Fatalf("timed out waiting for %v; collected %+v", wanted, collected)
		}
	}
	return collected
}

// countEvents returns the completions for one command.
func countEvents(events []appsession.CommandEvent, command string) []appsession.CommandEvent {
	var matches []appsession.CommandEvent
	for _, event := range events {
		if event.Command == command {
			matches = append(matches, event)
		}
	}
	return matches
}

// TestRealShellSessionCapturesAnEarlyCommandOnce sends a command immediately
// after Open, before the first prompt and before the hook's first handshake.
// The shell's own prompt marks must not publish a fallback row; exactly one
// Tier 1 row arrives when the hook reports the command.
func TestRealShellSessionCapturesAnEarlyCommandOnce(t *testing.T) {
	shells := availableShells()
	if len(shells) == 0 {
		t.Skip("no zsh, bash, or fish available")
	}
	for name, path := range shells {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{
				"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
				"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
				"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
			} {
				unsetEnv(t, key)
			}
			home := t.TempDir()
			workDir := t.TempDir()
			env := append(startupEnv(t, name, home), "SHELL="+path)
			events := make(chan appsession.CommandEvent, 8)
			service := appsession.NewService(localpty.Factory{},
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
				t.Fatalf("open the real shell: %v", err)
			}
			t.Cleanup(func() {
				_ = session.Close()
				select {
				case <-session.Done():
				case <-time.After(5 * time.Second):
				}
			})

			// No wait for the first prompt or handshake: the line is already
			// sitting in the terminal input queue when the shell starts.
			typed := "echo early_probe_$((40+2))"
			if name == "fish" {
				typed = "echo early_probe_(math 40+2)"
			}
			if result := service.SendInput([]model.SessionID{session.ID()}, []byte(typed+"\r")); result.DeliveredCount() != 1 {
				t.Fatalf("send %q: %+v", typed, result)
			}
			event := awaitEvent(t, events)
			if event.Command != typed {
				t.Fatalf("first completion = %+v, want only %q", event, typed)
			}
			if event.CaptureTier != domainhistory.CaptureTierHook {
				t.Fatalf("early capture tier = %d, want the hook's Tier 1: %+v",
					event.CaptureTier, event)
			}
			if !event.HasExitCode || event.ExitCode != 0 {
				t.Fatalf("early exit = %d/%t, want 0", event.ExitCode, event.HasExitCode)
			}
			if event.StartedAt.IsZero() || event.FinishedAt.Before(event.StartedAt) {
				t.Fatalf("early times = %v..%v", event.StartedAt, event.FinishedAt)
			}
			select {
			case extra := <-events:
				t.Fatalf("the early command produced a second row: %+v", extra)
			case <-time.After(500 * time.Millisecond):
			}
		})
	}
}

// TestRealShellSessionCapturesCommandsWhenTheHookSkips is the fifth-round
// regression. When the startup script deliberately installs nothing (an
// existing integration or a missing encoder) it sends the private skip marker
// instead of the prompt handshake. The tap must release the first command to
// the ordinary Tier 2/3 path: two commands typed at separate prompts are both
// captured exactly once, before Session close, and no close-time row is added.
func TestRealShellSessionCapturesCommandsWhenTheHookSkips(t *testing.T) {
	shells := availableShells()
	if len(shells) == 0 {
		t.Skip("no zsh, bash, or fish available")
	}
	for name, path := range shells {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			for _, scenario := range []string{"existing integration", "missing encoder"} {
				scenario := scenario
				t.Run(scenario, func(t *testing.T) {
					home := t.TempDir()
					workDir := t.TempDir()
					emptyPath := t.TempDir()
					extraEnv := []string{}
					var prepare func(string)
					if scenario == "existing integration" {
						extraEnv = append(extraEnv, "ATUIN_SESSION=existing")
					} else if name == "bash" {
						// /etc/profile can restore PATH, so the user rc (which
						// runs after it and before the hook) leaves it empty.
						prepare = func(home string) {
							rcPath := filepath.Join(home, ".bashrc")
							body, err := os.ReadFile(rcPath)
							if err != nil {
								t.Fatal(err)
							}
							body = append(body, []byte("\nPATH='"+emptyPath+"'\n")...)
							if err := os.WriteFile(rcPath, body, 0o600); err != nil {
								t.Fatal(err)
							}
						}
					} else {
						extraEnv = append(extraEnv, "PATH="+emptyPath)
					}

					service, session, events := openLocalSession(t, name, path, home, workDir,
						func(service *appsession.Service, session *appsession.Session) bool {
							text, _ := service.ScreenText(session.ID())
							return strings.Contains(text.Text, "netcatty-user-startup-loaded")
						}, prepare, extraEnv...)
					waitForShellInputReady(t, service, session, name)

					first := "echo netcatty_skip_one_$((40+2))"
					second := "echo netcatty_skip_two_$((20+2))"
					firstResult, secondResult := "netcatty_skip_one_42", "netcatty_skip_two_22"
					if name == "fish" {
						first = "echo netcatty_skip_one_(math 40+2)"
						second = "echo netcatty_skip_two_(math 20+2)"
					}

					service.SendInput([]model.SessionID{session.ID()}, []byte(first+"\r"))
					if !waitCondition(func() bool {
						text, _ := service.ScreenText(session.ID())
						return strings.Contains(text.Text, firstResult)
					}) {
						text, _ := service.ScreenText(session.ID())
						t.Fatalf("the first skipped-hook command never ran: %q", text.Text)
					}
					firstEvent := awaitCommandEvent(t, events, first, nil)
					if firstEvent.CaptureTier == domainhistory.CaptureTierHook {
						t.Fatalf("the first command claims Tier 1 without a hook: %+v", firstEvent)
					}
					if firstEvent.FinishedAt.IsZero() {
						t.Fatalf("the first command was only finalized at close: %+v", firstEvent)
					}

					service.SendInput([]model.SessionID{session.ID()}, []byte(second+"\r"))
					if !waitCondition(func() bool {
						text, _ := service.ScreenText(session.ID())
						return strings.Contains(text.Text, secondResult)
					}) {
						text, _ := service.ScreenText(session.ID())
						t.Fatalf("the second skipped-hook command never ran: %q", text.Text)
					}
					secondEvent := awaitCommandEvent(t, events, second, nil)
					if secondEvent.CaptureTier == domainhistory.CaptureTierHook {
						t.Fatalf("the second command claims Tier 1 without a hook: %+v", secondEvent)
					}
					if secondEvent.FinishedAt.IsZero() {
						t.Fatalf("the second command was only finalized at close: %+v", secondEvent)
					}

					select {
					case extra := <-events:
						t.Fatalf("a skipped-hook command was captured twice: %+v", extra)
					case <-time.After(300 * time.Millisecond):
					}
					if err := session.Close(); err != nil {
						t.Fatalf("close: %v", err)
					}
					select {
					case extra := <-events:
						t.Fatalf("Session close published a lost skipped-hook command: %+v", extra)
					case <-time.After(300 * time.Millisecond):
					}
				})
			}
		})
	}
}

// TestRealShellSessionCapturesTypeAheadOnce is the B1 regression: a line typed
// while a hooked command runs waits in the terminal input queue. It must not
// become a Tier 2 row or inherit the running command's exit; after the command
// ends, the hook reports it exactly once with its own real exit code.
func TestRealShellSessionCapturesTypeAheadOnce(t *testing.T) {
	shells := availableShells()
	if len(shells) == 0 {
		t.Skip("no zsh, bash, or fish available")
	}
	for name, path := range shells {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			service, session, events := openHookedSession(t, name, path, home, workDir)
			warmUpHookedSession(t, service, session, events, name)

			const running = "sleep 3"
			if result := service.SendInput([]model.SessionID{session.ID()}, []byte(running+"\r")); result.DeliveredCount() != 1 {
				t.Fatalf("send %q: %+v", running, result)
			}
			// Enter was delivered; the shell starts sleep immediately. Type
			// ahead while it runs.
			time.Sleep(300 * time.Millisecond)

			typed := "echo netcatty_typeahead_$((40+2))"
			if name == "fish" {
				typed = "echo netcatty_typeahead_(math 40+2)"
			}
			sentAt := time.Now()
			service.SendInput([]model.SessionID{session.ID()}, []byte(typed+"\r"))

			collected := collectEventsUntil(t, events, 30*time.Second, running, typed)
			runningRows := countEvents(collected, running)
			if len(runningRows) != 1 || runningRows[0].CaptureTier != domainhistory.CaptureTierHook ||
				!runningRows[0].HasExitCode || runningRows[0].ExitCode != 0 {
				t.Fatalf("running command rows = %+v, want one Tier 1 row with exit 0", runningRows)
			}
			typedRows := countEvents(collected, typed)
			if len(typedRows) != 1 {
				t.Fatalf("type-ahead rows = %+v, want exactly one", typedRows)
			}
			row := typedRows[0]
			if row.CaptureTier != domainhistory.CaptureTierHook {
				t.Fatalf("type-ahead capture tier = %d, want the hook's Tier 1: %+v",
					row.CaptureTier, row)
			}
			if !row.HasExitCode || row.ExitCode != 0 {
				t.Fatalf("type-ahead exit = %d/%t, want the queued command's own 0",
					row.ExitCode, row.HasExitCode)
			}
			if row.StartedAt.Sub(sentAt) < time.Second {
				t.Fatalf("the type-ahead row started after %v, before the running command ended",
					row.StartedAt.Sub(sentAt))
			}
			select {
			case extra := <-events:
				t.Fatalf("type-ahead produced an extra row: %+v", extra)
			case <-time.After(300 * time.Millisecond):
			}
		})
	}
}

// TestRealShellSessionCapturesASlowMultilineOnce is the B2 regression: the
// user pauses 1.5s at PS2. Nothing may finalize the first line; the hook
// reports the complete logical command exactly once with its real exit.
func TestRealShellSessionCapturesASlowMultilineOnce(t *testing.T) {
	shells := availableShells()
	if len(shells) == 0 {
		t.Skip("no zsh, bash, or fish available")
	}
	for name, path := range shells {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			service, session, events := openHookedSession(t, name, path, home, workDir)
			warmUpHookedSession(t, service, session, events, name)

			first := `echo "netcatty_slow_one`
			second := `netcatty_slow_two"`
			service.SendInput([]model.SessionID{session.ID()}, []byte(first+"\r"))
			time.Sleep(1500 * time.Millisecond)
			select {
			case event := <-events:
				t.Fatalf("the unfinished first line was captured during the pause: %+v", event)
			case <-time.After(300 * time.Millisecond):
			}
			service.SendInput([]model.SessionID{session.ID()}, []byte(second+"\r"))
			event := awaitEvent(t, events)
			if event.CaptureTier != domainhistory.CaptureTierHook {
				t.Fatalf("slow multiline tier = %d, want Tier 1: %+v", event.CaptureTier, event)
			}
			if !strings.Contains(event.Command, "netcatty_slow_one") ||
				!strings.Contains(event.Command, "netcatty_slow_two") {
				t.Fatalf("slow multiline capture = %q, want the whole command", event.Command)
			}
			if !event.HasExitCode || event.ExitCode != 0 {
				t.Fatalf("slow multiline exit = %d/%t, want 0", event.ExitCode, event.HasExitCode)
			}
			select {
			case extra := <-events:
				t.Fatalf("slow multiline was captured more than once: %+v", extra)
			case <-time.After(300 * time.Millisecond):
			}
		})
	}
}

// TestRealNestedShellCapturesBackToBackInnerCommands starts a different shell
// inside each hooked shell. The outer command is finalized once with unknown
// finish when the inner prompt appears; two commands typed back-to-back at the
// inner prompt are both captured as Tier 2/3, and the outer hook works again
// after the inner shell exits.
func TestRealNestedShellCapturesBackToBackInnerCommands(t *testing.T) {
	shells := availableShells()
	for _, outerName := range []string{"zsh", "bash", "fish"} {
		outer, available := shells[outerName]
		if !available {
			continue
		}
		innerName := ""
		for _, candidate := range []string{"bash", "zsh", "fish"} {
			if candidate != outerName {
				if _, ok := shells[candidate]; ok {
					innerName = candidate
					break
				}
			}
		}
		if innerName == "" {
			continue
		}
		inner := shells[innerName]
		t.Run(outerName+"_to_"+innerName, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			service, session, events := openHookedSession(t, outerName, outer, home, workDir)
			warmUpHookedSession(t, service, session, events, outerName)

			service.SendInput([]model.SessionID{session.ID()}, []byte(inner+"\r"))
			outerEvent := awaitCommandEvent(t, events, inner, nil)
			if outerEvent.CaptureTier != domainhistory.CaptureTierHook {
				t.Fatalf("nested shell command tier = %d, want the outer hook's Tier 1: %+v",
					outerEvent.CaptureTier, outerEvent)
			}
			if outerEvent.HasExitCode || !outerEvent.FinishedAt.IsZero() {
				t.Fatalf("the outer shell command invented a finish: %+v", outerEvent)
			}

			one := "echo nested_one_$((1+1))"
			two := "echo nested_two_$((2+2))"
			service.SendInput([]model.SessionID{session.ID()}, []byte(one+"\r"))
			service.SendInput([]model.SessionID{session.ID()}, []byte(two+"\r"))
			for _, command := range []string{one, two} {
				event := awaitCommandEvent(t, events, command, nil)
				if event.CaptureTier == domainhistory.CaptureTierHook {
					t.Fatalf("inner command %q claims Tier 1: %+v", command, event)
				}
			}

			service.SendInput([]model.SessionID{session.ID()}, []byte("exit\r"))
			awaitCommandEvent(t, events, "exit", nil)
			// The outer prompt handshake follows the D that finished the exit
			// row and can be delayed under load or the race detector. Probe
			// with real commands until the hook speaks rather than assuming a
			// fixed settling delay.
			deadline := time.Now().Add(20 * time.Second)
			for attempt := 1; ; attempt++ {
				value := attempt * 3
				back := fmt.Sprintf("echo nested_back_$((%d+%d))", value, value)
				if outerName == "fish" {
					back = fmt.Sprintf("echo nested_back_(math %d+%d)", value, value)
				}
				service.SendInput([]model.SessionID{session.ID()}, []byte(back+"\r"))
				backEvent := awaitCommandEvent(t, events, back, nil)
				if backEvent.CaptureTier == domainhistory.CaptureTierHook {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the outer hook did not return after the nested shell: %+v", backEvent)
				}
			}
		})
	}
}

// TestRealZshTmuxAttachPathFallsBackToTypedCapture simulates tmux attach with
// a fake tmux that execs an unhooked bash. tmux is not installed here, so this
// exercises the attach command classification and nested fallback without it.
func TestRealZshTmuxAttachPathFallsBackToTypedCapture(t *testing.T) {
	outer, outerOK := availableShells()["zsh"]
	inner, innerOK := availableShells()["bash"]
	if !outerOK || !innerOK {
		t.Skip("zsh and bash are required")
	}
	home := t.TempDir()
	workDir := t.TempDir()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"attach\" ] || [ \"$1\" = \"a\" ]; then exec " + inner + " --norc --noprofile; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	service, session, events := openHookedSession(t, "zsh", outer, home, workDir,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	warmUpHookedSession(t, service, session, events, "zsh")

	service.SendInput([]model.SessionID{session.ID()}, []byte("tmux attach\r"))
	outerEvent := awaitCommandEvent(t, events, "tmux attach", nil)
	if outerEvent.CaptureTier != domainhistory.CaptureTierHook {
		t.Fatalf("tmux attach tier = %d, want the outer hook's Tier 1: %+v",
			outerEvent.CaptureTier, outerEvent)
	}
	if outerEvent.HasExitCode || !outerEvent.FinishedAt.IsZero() {
		t.Fatalf("the tmux attach command invented a finish: %+v", outerEvent)
	}
	innerTyped := "echo tmux_inner_$((40+2))"
	service.SendInput([]model.SessionID{session.ID()}, []byte(innerTyped+"\r"))
	event := awaitCommandEvent(t, events, innerTyped, nil)
	if event.CaptureTier == domainhistory.CaptureTierHook {
		t.Fatalf("the simulated attached shell claims Tier 1: %+v", event)
	}
}

// awaitTierOne returns the hook's authoritative capture for a typed command.
// A shell that marks its own prompt (fish does) must not produce a second,
// fallback row: any other event is a regression, not something to skip.
func awaitTierOne(t *testing.T, events <-chan appsession.CommandEvent, command string) appsession.CommandEvent {
	t.Helper()
	event := awaitEvent(t, events)
	if event.Command != command {
		t.Fatalf("completion for %q arrived while waiting for %q: %+v",
			event.Command, command, event)
	}
	if event.CaptureTier != domainhistory.CaptureTierHook {
		t.Fatalf("completion for %q is Tier %d, want the hook's Tier 1: %+v",
			command, event.CaptureTier, event)
	}
	return event
}

func awaitEvent(t *testing.T, events <-chan appsession.CommandEvent) appsession.CommandEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(20 * time.Second):
		t.Fatal("no completion event arrived")
		return appsession.CommandEvent{}
	}
}

// waitCondition polls real Session state with a hard deadline, so a slow shell
// is tolerated without a fixed sleep deciding the outcome.
func waitCondition(condition func() bool) bool {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// unsetEnv removes a variable for the rest of a sequential test and restores
// it afterwards.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	value, present := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(key, value)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func TestRealShellSkipsWhenAnIntegrationIsActive(t *testing.T) {
	shells := availableShells()
	if len(shells) == 0 {
		t.Skip("no zsh, bash, or fish available")
	}
	for name, path := range shells {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			workDir := t.TempDir()
			env := append(startupEnv(t, name, home), "ATUIN_SESSION=existing")
			plan := localPlan(t, path, env)
			plan.Spec.Dir = workDir

			startArgs := plan.Spec.Command
			if len(startArgs) == 0 {
				startArgs = loginArgs(path)
			}
			process := startShell(t, path, startArgs, workDir, append(env, plan.Spec.Env...))
			process.waitFor(t, "the user's startup", func(output string) bool {
				return strings.Contains(output, "netcatty-user-startup-loaded")
			})

			typed := "echo existing_$((40+2))"
			if name == "fish" {
				typed = "echo existing_(math 40+2)"
			}
			process.typeLine(t, typed)
			output := process.waitFor(t, "the command output", func(output string) bool {
				return strings.Contains(output, "existing_42")
			})
			if count := countNetcattyExec(output); count != 0 {
				t.Fatalf("an existing integration did not suppress the hook: %d sequences in %q", count, output)
			}
			if strings.Contains(output, "\x1b]1337;NetcattyHook;"+name) {
				t.Fatalf("an existing integration still got the handshake: %q", output)
			}
			if !strings.Contains(output, "\x1b]1337;NetcattyHookSkip;"+name+"\x07") {
				t.Fatalf("an existing integration did not announce the skip: %q", output)
			}
		})
	}
}
