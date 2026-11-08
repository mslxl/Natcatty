package shellhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

// The SSH tests are the acceptance path for Hosts Netcatty reaches over SSH,
// including BSD, where platform/pty has no local implementation. They drive
// the real product stack on the client side: the shellhook planner rewrites
// the Session, the Session tap reads the remote stream, and the remote wrapper
// installs the same hook files the GUI sends. Only the transport is reduced to
// x/crypto/ssh with a PTY; the command the remote receives is the planner's
// own RemoteCommand.
//
// A lab is selected with one environment variable per shell:
//
//	NETCATTY_TEST_SSH_ZSH=user@host:2222
//	NETCATTY_TEST_SSH_ZSH_LOGIN=/usr/local/bin/zsh
//
// The *_LOGIN value is the account's login shell, the same value the product
// reads over its read-only SFTP probe. Authentication uses
// NETCATTY_TEST_SSH_KEY (a private key path) or NETCATTY_TEST_SSH_PASSWORD.
// NETCATTY_TEST_SSH_KNOWN_HOSTS names a known_hosts file; a disposable lab may
// instead set NETCATTY_TEST_SSH_INSECURE=1 to accept a fresh host key.
// NETCATTY_TEST_SSH_REQUIRED=1 turns a missing lab into a failure instead of a
// skip, so a release job cannot pass without running the shell it provisions.

type sshLab struct {
	shell      string
	loginShell string
	user       string
	address    string
	config     *xssh.ClientConfig
}

const sshLabEnvPrefix = "NETCATTY_TEST_SSH_"

// sshLabFor returns the configured lab for one shell. A missing target means
// the environment did not opt into that subtest; the caller skips.
func sshLabFor(t *testing.T, shell string) (sshLab, bool) {
	t.Helper()
	envName := sshLabEnvPrefix + strings.ToUpper(shell)
	target := os.Getenv(envName)
	if target == "" {
		// A release job sets NETCATTY_TEST_SSH_REQUIRED=1 so a missing lab
		// fails the run instead of letting the subtest skip and the job pass
		// without exercising the cell.
		if os.Getenv("NETCATTY_TEST_SSH_REQUIRED") == "1" {
			t.Fatalf("%s is required (NETCATTY_TEST_SSH_REQUIRED=1) but is not set", envName)
		}
		return sshLab{}, false
	}
	loginShell := os.Getenv(envName + "_LOGIN")
	if loginShell == "" {
		t.Fatalf("%s is set but %s_LOGIN is empty; name the account's login shell", envName, envName)
	}
	at := strings.LastIndex(target, "@")
	if at <= 0 || at == len(target)-1 {
		t.Fatalf("%s=%q is not user@host:port", envName, target)
	}
	user, address := target[:at], target[at+1:]
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "22")
	}
	config := &xssh.ClientConfig{
		User:            user,
		HostKeyCallback: xssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	}
	knownHosts := os.Getenv("NETCATTY_TEST_SSH_KNOWN_HOSTS")
	insecure := os.Getenv("NETCATTY_TEST_SSH_INSECURE") == "1"
	if !insecure {
		if knownHosts == "" {
			t.Fatal("refusing to connect without host-key verification; set NETCATTY_TEST_SSH_KNOWN_HOSTS or NETCATTY_TEST_SSH_INSECURE=1 for a disposable lab")
		}
		callback, err := knownhosts.New(knownHosts)
		if err != nil {
			t.Fatalf("read known_hosts: %v", err)
		}
		config.HostKeyCallback = callback
	}
	keyPath := os.Getenv("NETCATTY_TEST_SSH_KEY")
	password := os.Getenv("NETCATTY_TEST_SSH_PASSWORD")
	if keyPath == "" && password == "" {
		t.Fatalf("%s is set but neither NETCATTY_TEST_SSH_KEY nor NETCATTY_TEST_SSH_PASSWORD is", envName)
	}
	if keyPath != "" {
		data, err := os.ReadFile(keyPath)
		if err != nil {
			t.Fatalf("read the SSH key: %v", err)
		}
		signer, err := xssh.ParsePrivateKey(data)
		if err != nil {
			t.Fatalf("parse the SSH key: %v", err)
		}
		config.Auth = append(config.Auth, xssh.PublicKeys(signer))
	}
	if password != "" {
		config.Auth = append(config.Auth, xssh.Password(password))
	}
	lab := sshLab{
		shell:      shell,
		loginShell: loginShell,
		user:       user,
		address:    address,
		config:     config,
	}
	return lab, true
}

// sshLabFactory opens the same terminal the SSH transport opens: a PTY and
// the planner's RemoteCommand, or the plain interactive login shell when the
// planned hook was withdrawn for this account's shell.
type sshLabFactory struct {
	lab sshLab
}

func (f sshLabFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.lab.config == nil {
		return nil, errors.New("ssh lab: unusable client config")
	}
	client, err := xssh.Dial("tcp", f.lab.address, f.lab.config)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", f.lab.address, err)
	}
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("open an SSH session: %w", err)
	}
	cols, rows := spec.Cols, spec.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	modes := xssh.TerminalModes{
		xssh.ECHO:          1,
		xssh.TTY_OP_ISPEED: 14400,
		xssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("request a remote PTY: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}

	command := spec.RemoteCommand
	withdrawn := false
	if spec.CaptureHookRemote && !RemoteLoginShellSupported(f.lab.loginShell) {
		// The transport withdraws the wrapper for xonsh, csh, tcsh, nushell,
		// and unknown login shells: they cannot parse the POSIX command.
		command = ""
		withdrawn = true
	}
	if command == "" {
		err = session.Shell()
	} else {
		err = session.Start(command)
	}
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("start the remote shell: %w", err)
	}
	return &sshLabTransport{
		client:    client,
		session:   session,
		stdin:     stdin,
		stdout:    stdout,
		expected:  spec.CaptureHookRemote && !withdrawn,
		withdrawn: withdrawn,
	}, nil
}

type sshLabTransport struct {
	client    *xssh.Client
	session   *xssh.Session
	stdin     io.WriteCloser
	stdout    io.Reader
	expected  bool
	withdrawn bool
	closed    bool
}

func (t *sshLabTransport) Read(p []byte) (int, error) { return t.stdout.Read(p) }

func (t *sshLabTransport) Write(p []byte) (int, error) { return t.stdin.Write(p) }

func (t *sshLabTransport) Resize(cols, rows int) error {
	return t.session.WindowChange(rows, cols)
}

func (t *sshLabTransport) Close() error {
	if t.closed {
		return nil
	}
	t.closed = true
	_ = t.stdin.Close()
	err := t.session.Close()
	_ = t.client.Close()
	return err
}

func (t *sshLabTransport) Wait() (int, error) {
	err := t.session.Wait()
	if err == nil {
		return 0, nil
	}
	var exit *xssh.ExitError
	if errors.As(err, &exit) {
		return exit.ExitStatus(), nil
	}
	return 0, err
}

func (t *sshLabTransport) CaptureHookExpected() bool { return t.expected }

func (t *sshLabTransport) CaptureHookWithdrawn() bool { return t.withdrawn }

// openSSHCaptureSession wires the planner, the Session tap, and the remote lab
// exactly as the application does for an SSH Session.
func openSSHCaptureSession(t *testing.T, lab sshLab, hookEnabled bool) (*appsession.Service, *appsession.Session, <-chan appsession.CommandEvent) {
	t.Helper()
	for _, key := range []string{
		"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
		"ITERM_SHELL_INTEGRATION_INSTALLED", "NETCATTY_HOOK",
		"NETCATTY_HOOK_DIR", "NETCATTY_USER_ZDOTDIR", "NETCATTY_HOOK_FILE",
	} {
		unsetEnv(t, key)
	}
	options := []Option{WithDefaultShell(func() (string, error) { return lab.loginShell, nil })}
	if !hookEnabled {
		options = append(options, WithEnabled(func() bool { return false }))
	}
	events := make(chan appsession.CommandEvent, 32)
	service := appsession.NewService(sshLabFactory{lab: lab},
		appsession.WithCaptureHook(NewPlanner(options...)),
		appsession.WithCommandCompletionHook(func(event appsession.CommandEvent) {
			events <- event
		}),
	)
	t.Cleanup(service.CloseAll)
	host, portText, err := net.SplitHostPort(lab.address)
	if err != nil {
		t.Fatalf("split %q: %v", lab.address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("port %q: %v", portText, err)
	}
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind:     appsession.KindSSH,
		Label:    "ssh capture lab",
		Hostname: host,
		Port:     port,
		Username: lab.user,
		Cols:     100,
		Rows:     40,
	})
	if err != nil {
		t.Fatalf("open the SSH session: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
		}
	})
	waitForSSHPrompt(t, service, session)
	return service, session, events
}

// waitForSSHPrompt waits for the remote shell's own prompt. The default
// account prompts end in a shape the tap's prompt detector recognizes, so the
// test never types before the line editor is reading.
func waitForSSHPrompt(t *testing.T, service *appsession.Service, session *appsession.Session) {
	t.Helper()
	// A recognizable prompt covers zsh, bash, and fish. Nushell's default
	// prompt ends with a timestamp, so its OSC 7 is the readiness signal for
	// the shells whose own integration reports a cwd before the first prompt.
	if !waitCondition(func() bool {
		if session.Snapshot().CWD != "" {
			return true
		}
		text, _ := service.ScreenText(session.ID())
		return screenEndsWithPrompt(text.Text)
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the remote shell never showed a prompt: snapshot=%+v screen=%q",
			session.Snapshot(), text.Text)
	}
}

func sendSSHCommand(t *testing.T, service *appsession.Service, session *appsession.Session, line string) {
	t.Helper()
	if result := service.SendInput([]model.SessionID{session.ID()}, []byte(line+"\r")); result.DeliveredCount() != 1 {
		t.Fatalf("send %q: %+v", line, result)
	}
}

// TestRealSSHRemoteHookCapturesTierOne is the SSH acceptance check for zsh,
// bash, and fish. It asserts the planner's wrapper, the remote hook's private
// sequence, and the Session tap together produce one Tier 1 row per command
// with the exact command, cwd, exit code, and measured duration.
func TestRealSSHRemoteHookCapturesTierOne(t *testing.T) {
	for _, shell := range []string{"zsh", "bash", "fish"} {
		shell := shell
		lab, ok := sshLabFor(t, shell)
		if !ok {
			t.Run(shell, func(t *testing.T) {
				t.Skipf("set NETCATTY_TEST_SSH_%s=user@host:port to run the SSH %s cell", strings.ToUpper(shell), shell)
			})
			continue
		}
		t.Run(shell, func(t *testing.T) {
			if !RemoteLoginShellSupported(lab.loginShell) {
				t.Fatalf("login shell %q must be zsh, bash, or fish for a Tier 1 SSH cell", lab.loginShell)
			}
			service, session, events := openSSHCaptureSession(t, lab, true)

			warmup := "echo netcatty_ssh_ready_2"
			sendSSHCommand(t, service, session, warmup)
			if !waitCondition(func() bool {
				text, _ := service.ScreenText(session.ID())
				return strings.Contains(text.Text, "netcatty_ssh_ready_2")
			}) {
				text, _ := service.ScreenText(session.ID())
				t.Fatalf("the SSH warm-up never ran; screen=%q", text.Text)
			}
			warm := awaitTierOne(t, events, warmup)
			home := warm.CWD
			if home == "" {
				t.Fatalf("the SSH hook reported no cwd: %+v", warm)
			}

			exchanges := []struct {
				typed string
				exit  int
				cwd   string
			}{
				{typed: "echo netcatty_ssh_42", cwd: home},
				{typed: "false", exit: 1, cwd: home},
				{typed: "cd /tmp", cwd: home},
				{typed: "echo netcatty_ssh_after_cd_42", cwd: "/tmp"},
			}
			for _, step := range exchanges {
				sendSSHCommand(t, service, session, step.typed)
				event := awaitTierOne(t, events, step.typed)
				if event.Shell != shell {
					t.Fatalf("completion shell = %q, want %q: %+v", event.Shell, shell, event)
				}
				if event.CWD != step.cwd {
					t.Fatalf("%q cwd = %q, want %q: %+v", step.typed, event.CWD, step.cwd, event)
				}
				if !event.HasExitCode || event.ExitCode != step.exit {
					t.Fatalf("%q exit = %d/%t, want %d", step.typed, event.ExitCode, event.HasExitCode, step.exit)
				}
				if event.StartedAt.IsZero() || event.FinishedAt.Before(event.StartedAt) {
					t.Fatalf("%q times = %v..%v", step.typed, event.StartedAt, event.FinishedAt)
				}
				select {
				case extra := <-events:
					t.Fatalf("the command was captured twice: %+v", extra)
				default:
				}
			}
		})
	}
}

// TestRealSSHRemoteTierThreeFallback is the SSH acceptance check for the Tier
// 3 fallback. The hook is disabled, so the shell is a plain login shell; the
// tap captures the typed command and finishes it at the next recognizable
// prompt with an observed duration, but no cwd or exit code.
func TestRealSSHRemoteTierThreeFallback(t *testing.T) {
	lab, ok := sshLabFor(t, "tier3")
	if !ok {
		t.Skip("set NETCATTY_TEST_SSH_TIER3=user@host:port to run the SSH Tier 3 cell")
	}
	if !strings.HasSuffix(lab.loginShell, "zsh") {
		t.Fatalf("the Tier 3 SSH cell requires a zsh login shell, got %q", lab.loginShell)
	}
	service, session, events := openSSHCaptureSession(t, lab, false)
	typed := "echo netcatty_ssh_tier3_42"
	sendSSHCommand(t, service, session, typed)
	if !waitCondition(func() bool {
		text, _ := service.ScreenText(session.ID())
		return strings.Contains(text.Text, "netcatty_ssh_tier3_42")
	}) {
		text, _ := service.ScreenText(session.ID())
		t.Fatalf("the unhooked SSH command never ran; screen=%q", text.Text)
	}
	event := awaitCommandEvent(t, events, typed, nil)
	if event.CaptureTier != domainhistory.CaptureTierFallback {
		t.Fatalf("capture tier = %d, want the typed Tier 3 fallback: %+v", event.CaptureTier, event)
	}
	if event.CWD != "" {
		t.Fatalf("the unhooked SSH fallback invented a cwd: %+v", event)
	}
	if event.HasExitCode {
		t.Fatalf("the unhooked SSH fallback invented an exit code: %+v", event)
	}
	if event.FinishedAt.IsZero() || event.FinishedAt.Before(event.StartedAt) {
		t.Fatalf("the SSH prompt fallback did not measure a duration: %+v", event)
	}
}

// TestRealSSHRemoteNativeFallback covers nushell and xonsh, whose login
// shells never receive the POSIX wrapper. The transport opens the plain login
// shell and the tap captures the shell's own OSC 133/7: cwd from OSC 7, no
// Netcatty exit code. Nushell's prompt also reports a finish; xonsh has no
// OSC 133, so its finish stays unknown.
func TestRealSSHRemoteNativeFallback(t *testing.T) {
	for _, shell := range []string{"nu", "xonsh"} {
		shell := shell
		lab, ok := sshLabFor(t, shell)
		if !ok {
			t.Run(shell, func(t *testing.T) {
				t.Skipf("set NETCATTY_TEST_SSH_%s=user@host:port to run the SSH %s cell", strings.ToUpper(shell), shell)
			})
			continue
		}
		t.Run(shell, func(t *testing.T) {
			if RemoteLoginShellSupported(lab.loginShell) {
				t.Fatalf("login shell %q must not accept the POSIX wrapper", lab.loginShell)
			}
			service, session, events := openSSHCaptureSession(t, lab, true)
			typed := "echo netcatty_ssh_native_42"
			sendSSHCommand(t, service, session, typed)
			if !waitCondition(func() bool {
				text, _ := service.ScreenText(session.ID())
				return strings.Contains(text.Text, "netcatty_ssh_native_42")
			}) {
				text, _ := service.ScreenText(session.ID())
				t.Fatalf("the native SSH command never ran; screen=%q", text.Text)
			}
			if shell == "xonsh" {
				// xonsh has no OSC 133 and its prompt is not prompt-detected,
				// so the Session close is what finalizes the native capture.
				if err := session.Close(); err != nil {
					t.Fatalf("close the xonsh SSH session: %v", err)
				}
			}
			event := awaitCommandEvent(t, events, typed, nil)
			if event.CaptureTier != domainhistory.CaptureTierNativeOSC {
				t.Fatalf("native SSH tier = %d, want Tier 2: %+v", event.CaptureTier, event)
			}
			if event.CWD == "" {
				t.Fatalf("the native SSH capture reported no cwd: %+v", event)
			}
			if event.HasExitCode {
				t.Fatalf("the native SSH capture invented an exit code: %+v", event)
			}
			if shell == "nu" && event.FinishedAt.IsZero() {
				t.Fatalf("nushell's native prompt reported no finish: %+v", event)
			}
			if shell == "xonsh" && !event.FinishedAt.IsZero() {
				t.Fatalf("xonsh reported a finish it cannot know: %+v", event)
			}
			select {
			case extra := <-events:
				t.Fatalf("the native command was captured twice: %+v", extra)
			case <-time.After(300 * time.Millisecond):
			}
		})
	}
}
