//go:build darwin || linux

package localpty

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/application/lens/integration"
	"github.com/mslxl/natcatty/platform/pty"
)

// bashOutput collects what the shell writes, so a test can wait for a fragment.
type bashOutput struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (o *bashOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *bashOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// startIntegratedBash runs a real interactive bash with the production rcfile in
// an isolated HOME, so the only startup files are the test's own .bashrc.
func startIntegratedBash(t *testing.T, home string) (*pty.PTY, *bashOutput) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed here")
	}
	rcfile := filepath.Join(home, "natcatty-rc")
	if err := os.WriteFile(rcfile, []byte(integration.BashScript()), 0o600); err != nil {
		t.Fatal(err)
	}
	shell, err := pty.Start(pty.Options{
		Path: bash,
		Args: []string{bash, "--rcfile", rcfile},
		Dir:  home,
		Env: []string{
			"HOME=" + home,
			"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
			"TERM=xterm-256color",
			"LC_ALL=C",
			"HISTFILE=/dev/null",
			"PROMPT_COMMAND=",
			"PS1=",
		},
		Cols: 100,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("start bash: %v", err)
	}
	output := &bashOutput{}
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := shell.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_, _ = shell.Write([]byte("exit\n"))
		_ = shell.Close()
	})
	return shell, output
}

// waitForBashOutput waits until the shell's output contains fragment.
func waitForBashOutput(t *testing.T, output *bashOutput, fragment string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), fragment) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the shell never wrote %q; output %q", fragment, output.String())
}

// TestBashIntegrationSendsNonASCIICommandsAsUTF8Bytes is the end-to-end
// regression for the OSC 133;C payload. startIntegratedBash runs under
// LC_ALL=C, where bash's printf reported every byte above 0x7F as a negative
// number, so `echo 中文` reached the lens as %FFFFFFFFFFFFFFE4… instead of the
// UTF-8 bytes it typed. The payload must carry the line exactly.
func TestBashIntegrationSendsNonASCIICommandsAsUTF8Bytes(t *testing.T) {
	home := t.TempDir()
	shell, output := startIntegratedBash(t, home)
	waitForBashOutput(t, output, "\x1b]133;B\x07")

	if _, err := shell.Write([]byte("echo 中文\n")); err != nil {
		t.Fatal(err)
	}
	want := "\x1b]133;C;echo%20%E4%B8%AD%E6%96%87;echo%20%E4%B8%AD%E6%96%87\x07"
	waitForBashOutput(t, output, want)
}

// TestBashIntegrationLayersOnTheUsersHooks is the regression for a shell the
// integration took over: the script sourced the user's .bashrc and then
// replaced PROMPT_COMMAND and the DEBUG trap outright. Users' prompts are
// built in those hooks — starship keeps its precmd in PROMPT_COMMAND and its
// timing in the DEBUG trap, bash-preexec owns the DEBUG trap — so the prompt
// froze and preexec hooks stopped running, while the terminal's own marks kept
// working and hid the damage. The integration must run first with the command's
// status and then leave the user's hooks in place.
func TestBashIntegrationLayersOnTheUsersHooks(t *testing.T) {
	home := t.TempDir()
	bashrc := `__user_precmd() {
  PS1="USER:$?> "
}
PROMPT_COMMAND=__user_precmd
__user_preexec() {
  printf 'preexec\n' >> "$HOME/preexec.log"
}
trap '__user_preexec' DEBUG
`
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(bashrc), 0o600); err != nil {
		t.Fatal(err)
	}
	shell, output := startIntegratedBash(t, home)

	// The user's precmd must still draw the prompt.
	waitForBashOutput(t, output, "USER:")

	// It must also see the status of the command that just ran, not the status
	// of the integration's own printf.
	if _, err := shell.Write([]byte("false\n")); err != nil {
		t.Fatal(err)
	}
	waitForBashOutput(t, output, "USER:1>")
	if !strings.Contains(output.String(), "\x1b]133;D;1\x07") {
		t.Fatalf("the integration lost the exit status; output %q", output.String())
	}

	// The user's DEBUG hook must still run alongside the integration's preexec.
	// The log is cleared first: the rcfile's own startup runs commands, which
	// would otherwise fill it before a user command ever executes.
	if err := os.Remove(filepath.Join(home, "preexec.log")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := shell.Write([]byte("echo NATCAT_LAYER\n")); err != nil {
		t.Fatal(err)
	}
	waitForBashOutput(t, output, "\x1b]133;C;echo%20NATCAT_LAYER;echo%20NATCAT_LAYER\x07")
	log, err := os.ReadFile(filepath.Join(home, "preexec.log"))
	if err != nil || len(log) == 0 {
		t.Fatalf("the user's DEBUG trap never ran (err=%v, log=%q); output %q", err, log, output.String())
	}

	// The terminal's own directory mark still comes with each prompt.
	if !strings.Contains(output.String(), "\x1b]7;file://") {
		t.Fatalf("the integration stopped reporting the working directory; output %q", output.String())
	}
}
