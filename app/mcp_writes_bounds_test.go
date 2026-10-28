package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
)

// boundsMarkerPattern finds the wrapper's random marker in the fake shell's
// input, matching the real marker protocol.
var boundsMarkerPattern = regexp.MustCompile(`__NCMCP_[a-z0-9]+_[0-9a-f]+__`)

// writeShellTransport is a deterministic shell PTY for bound tests. It answers
// the first wrapper with a start marker, the requested output, and an end
// marker, exactly as a real shell would, without ConPTY's rendering cost.
type writeShellTransport struct {
	mu       sync.Mutex
	output   int
	response []byte
	offset   int
	started  bool
	ready    chan struct{}
	done     chan struct{}
	once     sync.Once
}

func newWriteShellTransport(output int) *writeShellTransport {
	return &writeShellTransport{
		output: output, ready: make(chan struct{}, 1), done: make(chan struct{}),
	}
}

func (t *writeShellTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started {
		return len(p), nil
	}
	marker := boundsMarkerPattern.Find(p)
	if marker == nil {
		return len(p), nil
	}
	t.started = true
	t.response = []byte("\n" + string(marker) + "_S\r\n" +
		strings.Repeat("x", t.output) + "\r\n" + string(marker) + "_E:0\r\n")
	select {
	case t.ready <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (t *writeShellTransport) Read(p []byte) (int, error) {
	for {
		t.mu.Lock()
		if t.offset < len(t.response) {
			n := copy(p, t.response[t.offset:])
			t.offset += n
			t.mu.Unlock()
			return n, nil
		}
		t.mu.Unlock()
		select {
		case <-t.ready:
		case <-t.done:
			return 0, io.EOF
		}
	}
}

func (t *writeShellTransport) Resize(int, int) error { return nil }

func (t *writeShellTransport) Close() error {
	t.once.Do(func() { close(t.done) })
	return nil
}

type writeShellFactory struct{ transport *writeShellTransport }

func (f writeShellFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

// shellCommandFor returns a command that prints a marker, waits, and then
// prints another marker after the timeout should have fired. The local shell is
// platform-dependent; the test fixture opens the real default shell.
func shellCommandFor(shell, before, action string) string {
	switch shell {
	case "powershell":
		return fmt.Sprintf("Write-Output %s; %s; Write-Output after", before, action)
	case "cmd":
		return fmt.Sprintf("echo %s & %s & echo after", before, action)
	case "fish":
		return fmt.Sprintf("printf '%%s\\n' %s; %s; printf '%%s\\n' after", before, action)
	default:
		return fmt.Sprintf("printf '%s\\n'; %s; printf '%s\\n' after", before, action, before)
	}
}

// shellEcho prints one literal word in the fixture's real shell. The local
// shell differs per platform (PowerShell, cmd, fish, POSIX sh), so the test
// builds its command for the shell that is actually running.
func shellEcho(shell, text string) string {
	switch shell {
	case "powershell":
		return "Write-Output " + text
	case "cmd":
		return "echo " + text
	default:
		return fmt.Sprintf("printf '%%s\\n' %s", text)
	}
}

func TestMCPWriteCommandTimeoutReturnsPartialOutputAndReleasesTheSession(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	shell, _ := fixture.sessions.ShellKind(fixture.localSession)
	var slow string
	switch shell {
	case "powershell":
		slow = "Start-Sleep -Seconds 3"
	case "cmd":
		slow = "timeout /t 3 /nobreak >nul"
	case "fish":
		slow = "sleep 3"
	default:
		slow = "sleep 3"
	}
	fixture.executor.toolTimeout = time.Second
	timedOut := callMCPTool(t, fixture, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession),
		"command":   shellCommandFor(shell, "before-timeout", slow),
	})
	requireMCPWriteCode(t, "terminal_execute", timedOut, appmcp.CodeCommandTimeout)
	if stdout, _ := timedOut["stdout"].(string); !strings.Contains(stdout, "before-timeout") {
		t.Fatalf("timeout partial stdout = %q", stdout)
	}
	if timedOut["timedOut"] != true {
		t.Fatalf("timeout result = %#v", timedOut)
	}
	if activity := fixture.policy.Snapshot().LastActivity; activity == nil ||
		activity.Status != appmcp.ActivityTimeout {
		t.Fatalf("timeout activity = %#v; want %q", activity, appmcp.ActivityTimeout)
	}
	// The deadline released the Session: the next command completes normally.
	fixture.executor.toolTimeout = 30 * time.Second
	after := callMCPTool(t, fixture, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession), "command": shellEcho(shell, "after-timeout"),
	})
	requireMCPWriteOK(t, "terminal_execute", after)
	if stdout, _ := after["stdout"].(string); !strings.Contains(stdout, "after-timeout") {
		t.Fatalf("stdout after a timeout = %q", stdout)
	}
}

// TestMCPWriteReportsANativeExitCodeFromTheRealLocalShell proves the PowerShell
// wrapper propagates a failing native command's status. The outcome drives the
// error result, the Failed activity state, and the user-visible code.
func TestMCPWriteReportsANativeExitCodeFromTheRealLocalShell(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	shell, _ := fixture.sessions.ShellKind(fixture.localSession)
	command := "sh -c 'exit 3'"
	if shell == "powershell" || shell == "cmd" {
		command = "cmd /c exit 3"
	}
	result := callMCPTool(t, fixture, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession),
		"command":   command,
	})
	if result["ok"] != false {
		t.Fatalf("a failing command reported ok: %#v", result)
	}
	if got := fmt.Sprint(result["exitCode"]); got != "3" {
		t.Fatalf("exitCode = %v, want 3 (%#v)", result["exitCode"], result)
	}
	if got := fmt.Sprint(result["error"]); !strings.Contains(got, "exited with code 3") {
		t.Fatalf("error = %q, want the exit code message", got)
	}
	if code, _ := result["code"].(string); code != "" {
		t.Fatalf("an ordinary exit failure carried code %q: %#v", code, result)
	}
}

func TestMCPWriteForegroundOutputIsBoundedAndReportsTruncation(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	production := appmcp.ForegroundOutputLimit + 4096
	transport := newWriteShellTransport(production)
	sessions := appsession.NewService(writeShellFactory{transport: transport})
	t.Cleanup(sessions.CloseAll)
	session, err := sessions.Open(fixture.ctx, appsession.StartSpec{
		Kind: appsession.KindSSH, Label: "bound shell", Command: []string{"sh"},
		Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open the deterministic shell: %v", err)
	}
	executor := newMCPWriteExecutor(fixture.policy, sessions, fixture.vault,
		fixture.forwarding, nil, nil)
	result := executor.Execute(fixture.ctx, "terminal_execute", map[string]any{
		"sessionId": string(session.ID()), "command": "generate-output",
	}, appmcp.Decision{})
	if result["ok"] != true {
		t.Fatalf("terminal_execute: %#v", result)
	}
	stdout, _ := result["stdout"].(string)
	truncated, _ := result["outputTruncated"].(bool)
	base, _ := result["outputBaseOffset"].(int)
	total, _ := result["totalOutputChars"].(int)
	if !truncated || base <= 0 {
		t.Fatalf("truncation flags = %#v", result)
	}
	if len(stdout) > appmcp.ForegroundOutputLimit {
		t.Fatalf("foreground stdout = %d bytes, cap %d", len(stdout), appmcp.ForegroundOutputLimit)
	}
	if total < production {
		t.Fatalf("totalOutputChars = %d, want at least %d", total, production)
	}
	if base+len(stdout) > total {
		t.Fatalf("base %d + stdout %d exceeds total %d", base, len(stdout), total)
	}
	if activity := fixture.policy.Snapshot().LastActivity; activity == nil ||
		activity.Status != appmcp.ActivityTruncated {
		t.Fatalf("truncation activity = %#v; want %q", activity, appmcp.ActivityTruncated)
	}
}

func TestMCPWriteSFTPReadAndListBounds(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	sessionID := string(fixture.sshSession)

	// Exactly at the read ceiling succeeds; one byte over is refused.
	exact := bytes.Repeat([]byte("y"), appmcp.SFTPReadLimit)
	fixture.remote.files["/remote/exact.bin"] = exact
	read := callMCPTool(t, fixture, "sftp_read_file", map[string]any{
		"sessionId": sessionID, "path": "/remote/exact.bin",
	})
	requireMCPWriteOK(t, "sftp_read_file", read)
	if content, _ := read["content"].(string); len(content) != appmcp.SFTPReadLimit {
		t.Fatalf("exact read = %d bytes, want %d", len(content), appmcp.SFTPReadLimit)
	}
	fixture.remote.files["/remote/over.bin"] = append(exact, 'z')
	over := callMCPTool(t, fixture, "sftp_read_file", map[string]any{
		"sessionId": sessionID, "path": "/remote/over.bin",
	})
	if over["ok"] == true {
		t.Fatalf("over-limit read succeeded: %#v", over)
	}
	if errorText, _ := over["error"].(string); !strings.Contains(errorText, "too large") &&
		!strings.Contains(errorText, "10 MB") {
		t.Fatalf("over-limit read error = %q", errorText)
	}

	// Exactly at the listing ceiling is complete; one more entry truncates the
	// listing and sets the explicit marker.
	fixture.remote.dirs["/remote/many"] = true
	for index := 0; index < appmcp.SFTPListLimit; index++ {
		fixture.remote.files[fmt.Sprintf("/remote/many/f%05d", index)] = []byte("x")
	}
	listed := callMCPTool(t, fixture, "sftp_list", map[string]any{
		"sessionId": sessionID, "path": "/remote/many",
	})
	requireMCPWriteOK(t, "sftp_list", listed)
	entries, _ := listed["entries"].([]any)
	if len(entries) != appmcp.SFTPListLimit || listed["truncated"] == true {
		t.Fatalf("at-limit list = %d entries, truncated=%v", len(entries), listed["truncated"])
	}
	fixture.remote.files[fmt.Sprintf("/remote/many/f%05d", appmcp.SFTPListLimit)] = []byte("x")
	overList := callMCPTool(t, fixture, "sftp_list", map[string]any{
		"sessionId": sessionID, "path": "/remote/many",
	})
	requireMCPWriteOK(t, "sftp_list", overList)
	if entries, _ := overList["entries"].([]any); len(entries) != appmcp.SFTPListLimit ||
		overList["truncated"] != true {
		t.Fatalf("over-limit list = %d entries, truncated=%v", len(entries), overList["truncated"])
	}
}
