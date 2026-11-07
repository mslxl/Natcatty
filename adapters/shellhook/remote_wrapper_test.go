package shellhook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeRemoteShell records what the remote wrapper handed it and exits with a
// recognizable status. It never touches the user's home.
const fakeRemoteShell = `#!/bin/sh
{
  printf "args=%s\n" "$*"
  printf "zdotdir=%s\n" "${ZDOTDIR:-}"
  printf "user_zdotdir=%s\n" "${NETCATTY_USER_ZDOTDIR:-}"
  printf "hook_dir=%s\n" "${NETCATTY_HOOK_DIR:-}"
  printf "hook_file=%s\n" "${NETCATTY_HOOK_FILE:-}"
  printf "files=%s\n" "$(ls -A "${ZDOTDIR:-${NETCATTY_HOOK_DIR:-/nonexistent}}" 2>/dev/null | tr "\n" " ")"
  printf "zsh_hook=%s\n" "$(grep -c NETCATTY_HOOK "${ZDOTDIR:-/nonexistent}/.zshrc" 2>/dev/null)"
  printf "bash_hook=%s\n" "$(grep -c NETCATTY_HOOK "${NETCATTY_HOOK_DIR:-/nonexistent}/bashrc" 2>/dev/null)"
  printf "fish_hook=%s\n" "$(grep -c fish_preexec "${NETCATTY_HOOK_FILE:-/nonexistent}" 2>/dev/null)"
} > "$NETCATTY_TEST_LOG"
exit 7
`

type remoteRun struct {
	status  int
	log     string
	command []string
}

func runRemoteWrapper(t *testing.T, shellName string, useFake bool) (remoteRun, string, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	shellPath := filepath.Join("/bin", shellName)
	if useFake {
		shellPath = filepath.Join(bin, shellName)
		if err := os.WriteFile(shellPath, []byte(fakeRemoteShell), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(root, "home")
	tempDir := filepath.Join(root, "tmp")
	for _, dir := range []string{home, tempDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(root, "observed.log")
	command := exec.Command("/bin/sh", "-c", remoteWrapperCommand())
	command.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"SHELL=" + shellPath,
		"TMPDIR=" + tempDir,
		"NETCATTY_TEST_LOG=" + logPath,
	}
	output, err := command.CombinedOutput()
	status := 0
	if exit, ok := err.(*exec.ExitError); ok {
		status = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run the wrapper: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("the fake shell did not run: %v\n%s", err, output)
	}
	run := remoteRun{status: status, log: string(contents),
		command: strings.Fields(strings.TrimPrefix(strings.SplitN(string(contents), "\n", 2)[0], "args="))}
	return run, home, tempDir
}

// positiveField reports that the fake shell counted one or more matches.
func positiveField(log, key string) bool {
	for _, line := range strings.Split(log, "\n") {
		if value, ok := strings.CutPrefix(line, key); ok {
			count, err := strconv.Atoi(value)
			return err == nil && count > 0
		}
	}
	return false
}

func assertEmptyDir(t *testing.T, dir, what string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s is not empty: %v", what, entries)
	}
}

func TestRemoteWrapperInstallsEachShellAndCleansUp(t *testing.T) {
	for _, name := range []string{"zsh", "bash", "fish"} {
		name := name
		t.Run(name, func(t *testing.T) {
			run, home, tempDir := runRemoteWrapper(t, name, true)
			if run.status != 7 {
				t.Fatalf("status = %d, want the shell's 7", run.status)
			}
			switch name {
			case "zsh":
				if strings.Join(run.command, " ") != "-l" {
					t.Fatalf("args = %v, want a login zsh", run.command)
				}
				if !strings.Contains(run.log, "user_zdotdir="+home+"\n") ||
					!positiveField(run.log, "zsh_hook=") {
					t.Fatalf("zsh startup was not installed: %q", run.log)
				}
			case "bash":
				if !strings.Contains(strings.Join(run.command, " "), "--rcfile") ||
					!strings.Contains(strings.Join(run.command, " "), "-i") {
					t.Fatalf("args = %v, want the hook rcfile", run.command)
				}
				if !positiveField(run.log, "bash_hook=") {
					t.Fatalf("bash startup was not installed: %q", run.log)
				}
			case "fish":
				if !strings.Contains(strings.Join(run.command, " "), "--init-command") {
					t.Fatalf("args = %v, want the hook init command", run.command)
				}
				if !positiveField(run.log, "fish_hook=") {
					t.Fatalf("fish startup was not installed: %q", run.log)
				}
			}
			assertEmptyDir(t, home, "the user's home")
			assertEmptyDir(t, tempDir, "the temporary directory")
		})
	}
}

// A shell Netcatty does not support is executed unchanged, with no temporary
// files and no hook.
func TestRemoteWrapperRunsAnUnsupportedShellUnchanged(t *testing.T) {
	run, home, tempDir := runRemoteWrapper(t, "ksh", true)
	if run.status != 7 {
		t.Fatalf("status = %d, want the shell's 7", run.status)
	}
	if strings.Join(run.command, " ") != "-l" {
		t.Fatalf("args = %v, want the login shell unchanged", run.command)
	}
	if !strings.Contains(run.log, "zdotdir=\n") ||
		!strings.Contains(run.log, "hook_dir=\n") ||
		!strings.Contains(run.log, "hook_file=\n") {
		t.Fatalf("an unsupported shell received hook state: %q", run.log)
	}
	assertEmptyDir(t, home, "the user's home")
	assertEmptyDir(t, tempDir, "the temporary directory")
}
