//go:build darwin || linux

package integration

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestScriptsPercentEncodeCommandsAsBytes is the regression for non-ASCII
// commands reaching a block garbled. The OSC 133;C field is decoded with a
// byte-wise URL unescaper, but both hand-written encoders walked the line as
// characters under a UTF-8 locale and encoded code points (中 became %4E2D),
// while bash's printf additionally reported a byte above 0x7F as a negative
// number (é became %FFFFFFFFFFFFFFE9). A typed `echo 中文` therefore reached
// the lens as something else. The payload must be UTF-8 bytes in every locale.
func TestScriptsPercentEncodeCommandsAsBytes(t *testing.T) {
	commands := []string{
		"echo 中文",
		"ls /tmp/中文 目录",
		`printf '%s;%s\n' a b`,
		"echo a-b_c.d~e f",
		"echo 100%",
	}
	locales := []struct {
		name string
		env  []string
	}{
		{"utf8", []string{"LANG=en_US.UTF-8"}},
		{"c", []string{"LC_ALL=C"}},
	}
	shells := []struct {
		name string
		run  func(t *testing.T, command string, env []string) string
	}{
		{"bash", runBashEncoder},
		{"zsh", runZshEncoder},
	}
	for _, shell := range shells {
		for _, locale := range locales {
			for index, command := range commands {
				name := fmt.Sprintf("%s/%s/%d", shell.name, locale.name, index)
				t.Run(name, func(t *testing.T) {
					got := strings.TrimSpace(shell.run(t, "__natcatty_urlencode "+quote(command), locale.env))
					want := percentEncode(command)
					if got != want {
						t.Fatalf("encoding %q = %q, want %q", command, got, want)
					}
					decoded, err := url.PathUnescape(got)
					if err != nil {
						t.Fatalf("the terminal cannot decode %q: %v", got, err)
					}
					if decoded != command {
						t.Fatalf("decoding %q = %q, want %q", got, decoded, command)
					}
				})
			}
		}
	}
}

// runBashEncoder runs the production rcfile in an interactive bash and
// evaluates one command. The shell has no terminal, so bash warns that job
// control is off; the encoder does not depend on it.
func runBashEncoder(t *testing.T, command string, env []string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed here")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(dir, "bash-rcfile")
	if err := os.WriteFile(rc, []byte(BashScript()), 0o600); err != nil {
		t.Fatal(err)
	}
	return runShell(t, bash, []string{"--noprofile", "--rcfile", rc, "-i", "-c", command}, shellEnv(home, env))
}

// runZshEncoder runs the production .zshenv in an interactive zsh and evaluates
// one command. -d skips the system rc files; the script restores the user's
// ZDOTDIR from KITTY_ZDOTDIR, which the test points at an empty home.
func runZshEncoder(t *testing.T, command string, env []string) string {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed here")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	zdot := filepath.Join(dir, "zdot")
	for _, path := range []string{home, zdot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(zdot, ".zshenv"), []byte(ZshScript()), 0o600); err != nil {
		t.Fatal(err)
	}
	shellEnv := append(shellEnv(home, env), "ZDOTDIR="+zdot, "KITTY_ZDOTDIR="+home)
	return runShell(t, zsh, []string{"-d", "-i", "-c", command}, shellEnv)
}

// shellEnv is the minimal environment the generated scripts need, plus the
// locale under test.
func shellEnv(home string, extra []string) []string {
	return append([]string{
		"HOME=" + home,
		"PATH=/usr/bin:/bin",
		"TERM=xterm-256color",
	}, extra...)
}

func runShell(t *testing.T, path string, args, env []string) string {
	t.Helper()
	command := exec.Command(path, args...)
	command.Env = env
	output, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s %q: %v; stderr %q", filepath.Base(path), args, err, exit.Stderr)
		}
		t.Fatalf("%s %q: %v", filepath.Base(path), args, err)
	}
	return string(output)
}

// quote wraps command as one POSIX shell word.
func quote(command string) string {
	return "'" + strings.ReplaceAll(command, "'", `'\''`) + "'"
}

// percentEncode is the encoding the scripts owe the terminal: every byte
// outside the unreserved set becomes %XX with uppercase hex.
func percentEncode(s string) string {
	var out strings.Builder
	for index := 0; index < len(s); index++ {
		c := s[index]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '~', c == '_', c == '-':
			out.WriteByte(c)
		default:
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}
	return out.String()
}
