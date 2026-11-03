package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func TestSessionNameIsDeterministicAndPrefixed(t *testing.T) {
	tests := map[string]string{
		"01JABCDEFGHJKMNPQRSTVWXYZA": "natcatty-01JABCDEFGHJKMNPQRSTVWXYZA",
		"host-1":                     "natcatty-host-1",
		"host_1":                     "natcatty-host_5f1",
		"host.one":                   "natcatty-host_2eone",
		"host:one":                   "natcatty-host_3aone",
		"host one":                   "natcatty-host_20one",
		"host'one":                   "natcatty-host_27one",
		"host;rm -rf /":              "natcatty-host_3brm_20-rf_20_2f",
		"hôst":                       "natcatty-h_c3_b4st",
		"":                           "natcatty-_",
	}
	for id, want := range tests {
		if got := SessionName(model.HostID(id)); got != want {
			t.Errorf("SessionName(%q) = %q, want %q", id, got, want)
		}
		if got := SessionName(model.HostID(id)); got != want {
			t.Errorf("SessionName(%q) is not deterministic: %q then %q", id, want, got)
		}
	}
}

func TestSanitizeIDIsInjectiveForHostileIDs(t *testing.T) {
	ids := []string{"a", "A", "a-b", "a_b", "a_2db", "a.b", "a:b", "a\x00b", "a\nb", "", " ", "_", "-", "host"}
	seen := make(map[string]string, len(ids))
	for _, id := range ids {
		name := SanitizeID(id)
		for _, character := range name {
			safe := character == '-' || character == '_' ||
				(character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9')
			if !safe {
				t.Fatalf("SanitizeID(%q) = %q contains unsafe %q", id, name, character)
			}
		}
		if previous, duplicate := seen[name]; duplicate {
			t.Fatalf("SanitizeID maps %q and %q to the same name %q", previous, id, name)
		}
		seen[name] = id
	}
}

func TestQuoteProducesOneShellWord(t *testing.T) {
	tests := map[string]string{
		"natcatty-host-1": `'natcatty-host-1'`,
		"":                `''`,
		"it's":            `'it'\''s'`,
		"a; rm -rf /":     `'a; rm -rf /'`,
		"$(touch pwned)":  `'$(touch pwned)'`,
	}
	for value, want := range tests {
		if got := Quote(value); got != want {
			t.Errorf("Quote(%q) = %q, want %q", value, got, want)
		}
	}
}

// TestQuotedValuesSurviveAShell keeps the quoting rule honest against a real
// POSIX shell: the value comes back unchanged and no command substitution
// runs.
func TestQuotedValuesSurviveAShell(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("no POSIX shell on this platform")
	}
	for _, value := range []string{"plain", "it's", "a; echo injected", "$(echo injected)", "`echo injected`", "a\nb"} {
		command := exec.Command(shell, "-c", "printf '%s' "+Quote(value))
		output, err := command.Output()
		if err != nil {
			t.Fatalf("shell rejected %q: %v", value, err)
		}
		if string(output) != value {
			t.Errorf("shell round-trip of %q = %q", value, output)
		}
	}
}

func TestProbeCommandQuotesTheSessionName(t *testing.T) {
	command := ProbeCommand("natcatty-it's")
	if !strings.Contains(command, `tmux has-session -t '=natcatty-it'\''s'`) {
		t.Fatalf("probe does not quote the target exactly:\n%s", command)
	}
	for _, branch := range []string{"${TMUX:-}", "command -v tmux", "tmux -V", "has-session", probeNested, probeAttach, probeCreate, probeUnavailable} {
		if !strings.Contains(command, branch) {
			t.Errorf("probe command is missing %q:\n%s", branch, command)
		}
	}
}

func TestParseProbe(t *testing.T) {
	tests := []struct {
		output string
		want   Decision
		ok     bool
	}{
		{output: probeCreate + "\n", want: DecisionCreate, ok: true},
		{output: "welcome\n" + probeAttach + "\n", want: DecisionAttach, ok: true},
		{output: "  " + probeNested + "  \r\n", want: DecisionNested, ok: true},
		{output: probeUnavailable, want: DecisionUnavailable, ok: true},
		{output: "", ok: false},
		{output: "login banner only\n", ok: false},
		{output: probeCreate + "\n" + probeAttach + "\n", ok: false},
	}
	for _, testCase := range tests {
		got, ok := ParseProbe(testCase.output)
		if ok != testCase.ok || got != testCase.want {
			t.Errorf("ParseProbe(%q) = %q, %v; want %q, %v", testCase.output, got, ok, testCase.want, testCase.ok)
		}
	}
}

func TestWrapperCommandFallsBackAndNeverNests(t *testing.T) {
	command := WrapperCommand("natcatty-host-1")
	for _, fragment := range []string{
		`if [ -n "${TMUX:-}" ]`,
		`exec "${SHELL:-/bin/sh}" -l`,
		`command -v tmux`,
		`tmux new-session -A -s 'natcatty-host-1'`,
		`status=$?`,
		`tmux has-session -t '=natcatty-host-1'`,
		`exit 97`,
		`exit "$status"`,
	} {
		if !strings.Contains(command, fragment) {
			t.Errorf("wrapper command is missing %q:\n%s", fragment, command)
		}
	}
}

// shellFixture creates an executable fake tmux on a private PATH. The fake
// records every invocation so a test can prove the nested branch never runs
// it. newSessionExit and hasSessionExit select the exit status of those
// subcommands.
func shellFixture(t *testing.T, newSessionExit, hasSessionExit int) (shell, path, marker string) {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("no POSIX shell on this platform")
	}
	directory := t.TempDir()
	marker = filepath.Join(directory, "tmux-ran")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + Quote(marker) + "\n" +
		"if [ \"$1\" = \"-V\" ]; then printf 'tmux 3.4\\n'; exit 0; fi\n" +
		"if [ \"$1\" = \"new-session\" ]; then exit " + itoa(newSessionExit) + "; fi\n" +
		"if [ \"$1\" = \"has-session\" ]; then exit " + itoa(hasSessionExit) + "; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(directory, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	return shell, directory, marker
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	return "1"
}

func runShell(t *testing.T, shell, path, command string, environment ...string) string {
	t.Helper()
	output, status := runShellStatus(t, shell, path, command, environment...)
	if status != 0 {
		t.Fatalf("shell command exited %d\n%s", status, output)
	}
	return output
}

// runShellStatus runs the command and returns its combined output and exit
// status without treating a non-zero status as a test failure.
func runShellStatus(t *testing.T, shell, path, command string, environment ...string) (string, int) {
	t.Helper()
	process := exec.Command(shell, "-c", command)
	process.Env = append([]string{"PATH=" + path, "HOME=" + t.TempDir()}, environment...)
	output, err := process.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("shell command failed: %v\n%s", err, output)
	}
	return string(output), exit.ExitCode()
}

func TestProbeCommandClassifiesARealShell(t *testing.T) {
	shell, path, marker := shellFixture(t, 0, 1)
	session := "natcatty-host-1"

	if got := runShell(t, shell, path, ProbeCommand(session), "TMUX=/tmp/tmux,1,0"); strings.TrimSpace(got) != probeNested {
		t.Fatalf("nested probe = %q, want %q", got, probeNested)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the nested branch ran tmux")
	}

	if got := runShell(t, shell, "/nonexistent", ProbeCommand(session)); strings.TrimSpace(got) != probeUnavailable {
		t.Fatalf("missing-tmux probe = %q, want %q", got, probeUnavailable)
	}
	if got := runShell(t, shell, path, ProbeCommand(session)); strings.TrimSpace(got) != probeCreate {
		t.Fatalf("no-session probe = %q, want %q", got, probeCreate)
	}
}

func TestProbeCommandDetectsAnExistingSession(t *testing.T) {
	shell, path, _ := shellFixture(t, 0, 0)
	if got := runShell(t, shell, path, ProbeCommand("natcatty-host-1")); strings.TrimSpace(got) != probeAttach {
		t.Fatalf("existing-session probe = %q, want %q", got, probeAttach)
	}
}

func TestWrapperRunsTmuxAndDoesNotRunItWhenNested(t *testing.T) {
	shell, path, marker := shellFixture(t, 0, 1)
	session := "natcatty-host-1"

	runShell(t, shell, path, WrapperCommand(session))
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the wrapper did not run tmux: %v", err)
	}
	if !strings.Contains(string(data), "new-session -A -s "+session) {
		t.Fatalf("tmux arguments = %q, want new-session -A -s %s", data, session)
	}

	if err := os.Remove(marker); err != nil {
		t.Fatalf("remove marker: %v", err)
	}
	runShell(t, shell, path, WrapperCommand(session), "TMUX=/tmp/tmux,1,0")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the wrapper ran tmux while already inside tmux")
	}
}

// TestWrapperReportsADetachOnlyWhileTheSessionSurvives pins the exit-status
// protocol: a clean client exit with a live session is a detach, while exiting
// the shell inside tmux destroys the session and stays an ordinary exit.
func TestWrapperReportsADetachOnlyWhileTheSessionSurvives(t *testing.T) {
	session := "natcatty-host-1"

	shell, path, _ := shellFixture(t, 0, 0)
	if _, status := runShellStatus(t, shell, path, WrapperCommand(session)); status != DetachedExitStatus {
		t.Fatalf("detached wrapper status = %d, want %d", status, DetachedExitStatus)
	}

	shell, path, _ = shellFixture(t, 0, 1)
	if _, status := runShellStatus(t, shell, path, WrapperCommand(session)); status != 0 {
		t.Fatalf("wrapper status after the session ended = %d, want 0", status)
	}
}

// TestWrapperFallsBackWhenTmuxCannotStart proves a failed new-session still
// yields a usable login shell and never claims a detach.
func TestWrapperFallsBackWhenTmuxCannotStart(t *testing.T) {
	shell, path, marker := shellFixture(t, 1, 1)
	output, status := runShellStatus(t, shell, path, WrapperCommand("natcatty-host-1"))
	if status != 0 {
		t.Fatalf("fallback wrapper status = %d, want the login shell's 0\n%s", status, output)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the wrapper did not try tmux: %v", err)
	}
	if !strings.Contains(string(data), "new-session") {
		t.Fatalf("tmux invocations = %q, want new-session", data)
	}
	if strings.Contains(string(data), "has-session") {
		t.Fatalf("tmux invocations = %q, want no has-session after a failed start", data)
	}
}

func TestNoticeStateAnnouncesFallbacksOncePerHost(t *testing.T) {
	state := NewNoticeState()
	host := model.HostID("host-1")
	other := model.HostID("host-2")

	if !state.ShouldAnnounce(host, NoticeUnavailable) {
		t.Fatal("the first unavailable notice for a Host was suppressed")
	}
	if state.ShouldAnnounce(host, NoticeUnavailable) {
		t.Fatal("unavailable notice was announced twice for one Host")
	}
	if !state.ShouldAnnounce(host, NoticeNested) {
		t.Fatal("nested and unavailable share one notice slot")
	}
	if !state.ShouldAnnounce(other, NoticeUnavailable) {
		t.Fatal("a different Host lost its first unavailable notice")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if !state.ShouldAnnounce(host, NoticeRestored) {
			t.Fatalf("restored notice was suppressed on attach %d", attempt+1)
		}
	}
	var nilState *NoticeState
	if !nilState.ShouldAnnounce(host, NoticeUnavailable) {
		t.Fatal("a nil notice state suppressed a notice")
	}
}
