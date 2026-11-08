package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	xssh "golang.org/x/crypto/ssh"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appsession "github.com/mslxl/natcatty/application/session"
)

// passwdHandler serves one in-memory /etc/passwd over the SFTP request
// server. Only the read path the login-shell probe needs is implemented.
type passwdHandler struct{ data []byte }

func (h passwdHandler) Fileread(request *sftp.Request) (io.ReaderAt, error) {
	if request.Filepath != "/etc/passwd" {
		return nil, os.ErrNotExist
	}
	return bytes.NewReader(h.data), nil
}

// passwdSFTP serves passwd on the "sftp" subsystem, with the SFTP root at the
// test account's home so Getwd works the same way as a real server.
func passwdSFTP(passwd string) map[string]func(xssh.Channel) {
	return map[string]func(xssh.Channel){
		"sftp": func(channel xssh.Channel) {
			server := sftp.NewRequestServer(channel,
				sftp.Handlers{FileGet: passwdHandler{data: []byte(passwd)}},
				sftp.WithStartDirectory("/home/tester"))
			defer server.Close()
			_ = server.Serve()
		},
	}
}

const testPasswdPrefix = "root:x:0:0:root:/root:/bin/sh\n"

func passwdFor(shell string) string {
	return testPasswdPrefix + "tester:x:1000:1000::/home/tester:" + shell + "\n"
}

// openCaptureHookSSH opens an SSH Session whose StartSpec carries the
// capture wrapper marker, exactly as the shellhook planner leaves it.
func openCaptureHookSSH(t *testing.T, fixture *remoteCommandFixture, remoteCommand string) appsession.Transport {
	t.Helper()
	transport, err := fixture.factory.Open(context.Background(), appsession.StartSpec{
		Kind:              appsession.KindSSH,
		Label:             "capture hook fixture",
		HostID:            "host-1",
		Hostname:          "example.com",
		Port:              22,
		Username:          "tester",
		Cols:              100,
		Rows:              40,
		RemoteCommand:     remoteCommand,
		CaptureHookRemote: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

// An SSH login shell that cannot parse a POSIX command must never receive the
// wrapper. The transport reads the account's login shell over SFTP, which does
// not execute the shell, and opens the plain interactive login shell instead.
func TestSSHCaptureHookWithdrawsForNonPOSIXLoginShells(t *testing.T) {
	for _, testCase := range []struct{ name, shell string }{
		{name: "xonsh", shell: "/usr/local/bin/xonsh"},
		{name: "csh", shell: "/bin/csh"},
		{name: "tcsh", shell: "/bin/tcsh"},
		{name: "nushell", shell: "/opt/homebrew/bin/nu"},
		{name: "unknown", shell: "/usr/local/bin/myshell"},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRemoteCommandFixture(t, fakessh.Options{
				Subsystems: passwdSFTP(passwdFor(testCase.shell)),
				Shell: func(channel xssh.Channel) {
					_, _ = channel.Write([]byte("plain-login-shell\r\n"))
				},
			})
			transport := openCaptureHookSSH(t, fixture, "exec /bin/sh -c 'posix wrapper'")

			server := fixture.sshServer(t)
			if !server.ShellOpened() {
				t.Fatal("the terminal did not request the interactive login shell")
			}
			if requests := server.ExecRequests(); len(requests) != 0 {
				t.Fatalf("exec requests = %v, want the plain login shell", requests)
			}
			if subsystems := server.SubsystemRequests(); len(subsystems) != 1 || subsystems[0] != "sftp" {
				t.Fatalf("subsystem requests = %v, want one sftp probe", subsystems)
			}
			withdrawn, ok := transport.(appsession.CaptureHookWithdrawn)
			if !ok || !withdrawn.CaptureHookWithdrawn() {
				t.Fatalf("transport %T did not report the withdrawn hook", transport)
			}
		})
	}
}

// A POSIX login shell keeps the wrapper: the probe found its account entry and
// the terminal runs the planned command unchanged.
func TestSSHCaptureHookKeepsTheWrapperForPOSIXLoginShells(t *testing.T) {
	for _, testCase := range []struct{ name, shell string }{
		{name: "bash", shell: "/bin/bash"},
		{name: "zsh", shell: "/usr/local/bin/zsh"},
		{name: "fish", shell: "/usr/local/bin/fish"},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			const command = "exec /bin/sh -c 'posix wrapper'"
			fixture := newRemoteCommandFixture(t, fakessh.Options{
				Subsystems: passwdSFTP(passwdFor(testCase.shell)),
				Exec:       func(string, xssh.Channel) int { return 0 },
			})
			transport := openCaptureHookSSH(t, fixture, command)

			server := fixture.sshServer(t)
			if server.ShellOpened() {
				t.Fatal("the terminal opened the plain login shell despite a POSIX account shell")
			}
			if requests := server.ExecRequests(); len(requests) != 1 || requests[0] != command {
				t.Fatalf("exec requests = %v, want the planned wrapper", requests)
			}
			if withdrawn, ok := transport.(appsession.CaptureHookWithdrawn); ok && withdrawn.CaptureHookWithdrawn() {
				t.Fatal("a POSIX login shell reported the hook withdrawn")
			}
		})
	}
}

// Every uncertain answer is safe: no SFTP subsystem, no matching account, or
// an ambiguous account database keeps the plain login shell.
func TestSSHCaptureHookFallsBackWhenTheLoginShellIsUnknown(t *testing.T) {
	cases := map[string]fakessh.Options{
		"sftp unavailable": {
			Shell: func(xssh.Channel) {},
		},
		"account absent": {
			Subsystems: passwdSFTP(testPasswdPrefix),
			Shell:      func(xssh.Channel) {},
		},
		"unreadable shell": {
			Subsystems: passwdSFTP(passwdFor("")),
			Shell:      func(xssh.Channel) {},
		},
	}
	for name, options := range cases {
		name, options := name, options
		t.Run(name, func(t *testing.T) {
			fixture := newRemoteCommandFixture(t, options)
			transport := openCaptureHookSSH(t, fixture, "exec /bin/sh -c 'posix wrapper'")
			server := fixture.sshServer(t)
			if !server.ShellOpened() {
				t.Fatal("the terminal did not fall back to the login shell")
			}
			withdrawn, ok := transport.(appsession.CaptureHookWithdrawn)
			if !ok || !withdrawn.CaptureHookWithdrawn() {
				t.Fatal("an unknown login shell did not withdraw the hook")
			}
		})
	}
}

// A server that accepts the "sftp" subsystem but never sends its protocol
// version wedges the SFTP handshake, which reads the stream without a
// context. Open must still fall back to the plain login shell after the
// bounded probe, and the subsystem channel and probe goroutine must be
// released before it returns so repeated opens cannot exhaust server channels.
func TestSSHCaptureHookWedgedSFTPSubsystemIsBoundedAndReleased(t *testing.T) {
	closed := make(chan struct{})
	var closeOnce sync.Once
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Subsystems: map[string]func(xssh.Channel){
			"sftp": func(channel xssh.Channel) {
				// Accept the subsystem, then never write the version banner.
				_, _ = io.Copy(io.Discard, channel)
				closeOnce.Do(func() { close(closed) })
			},
		},
		Shell: func(channel xssh.Channel) {
			_, _ = io.WriteString(channel, "plain-login-shell\r\n")
		},
	})
	// Keep the budget independent of the implementation constants: this test
	// guards the caller-visible bound, so widening the probe must not widen
	// the test silently.
	const openBudget = 7 * time.Second
	started := time.Now()
	transport := openCaptureHookSSH(t, fixture, "exec /bin/sh -c 'posix wrapper'")
	if elapsed := time.Since(started); elapsed > openBudget {
		t.Fatalf("Open took %v, want the 5s probe bound plus a bounded drain", elapsed)
	}
	server := fixture.sshServer(t)
	if !server.ShellOpened() {
		t.Fatal("the terminal did not fall back to the plain login shell")
	}
	if requests := server.ExecRequests(); len(requests) != 0 {
		t.Fatalf("exec requests = %v, want none", requests)
	}
	if withdrawn, ok := transport.(appsession.CaptureHookWithdrawn); !ok || !withdrawn.CaptureHookWithdrawn() {
		t.Fatal("a wedged probe did not withdraw the hook")
	}
	// The server's copy ends when the client closes the subsystem channel.
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the wedged SFTP subsystem is still open after Open returned")
	}
	// Open waits for the probe goroutine, so it must not still be blocked in
	// the handshake. runtime.Stack names the frame; poll with a hard deadline
	// against scheduling jitter.
	deadline := time.Now().Add(2 * time.Second)
	for goroutineRunning("readRemoteLoginShell") {
		if time.Now().After(deadline) {
			t.Fatal("the login-shell probe goroutine is still running after Open returned")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// goroutineRunning reports whether any goroutine's stack mentions name.
func goroutineRunning(name string) bool {
	buffer := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buffer, true)
		if n < len(buffer) {
			return strings.Contains(string(buffer[:n]), name)
		}
		buffer = make([]byte, len(buffer)*2)
	}
}

// The passwd lookup selects one account and never guesses on ambiguity.
func TestPasswdLoginShell(t *testing.T) {
	passwd := []byte(
		"# comment\n" +
			"root:x:0:0:root:/root:/bin/sh\n" +
			"tester:x:1000:1000::/home/tester:/usr/local/bin/xonsh\n" +
			"broken:x:1001:1001::/home/broken\n" +
			"shared:x:1002:1002::/srv/shared:/bin/bash\n" +
			"shared2:x:1003:1003::/srv/shared:/bin/zsh\n")
	if shell, ok := passwdLoginShell(passwd, "tester", ""); !ok || shell != "/usr/local/bin/xonsh" {
		t.Fatalf("tester shell = %q/%t", shell, ok)
	}
	if shell, ok := passwdLoginShell(passwd, "", "/home/tester"); !ok || shell != "/usr/local/bin/xonsh" {
		t.Fatalf("home shell = %q/%t", shell, ok)
	}
	if _, ok := passwdLoginShell(passwd, "absent", ""); ok {
		t.Fatal("an absent account resolved a shell")
	}
	if _, ok := passwdLoginShell(passwd, "", "/srv/shared"); ok {
		t.Fatal("an ambiguous home resolved a shell")
	}
	if _, ok := passwdLoginShell(passwd, "", ""); ok {
		t.Fatal("an empty lookup resolved a shell")
	}
}
