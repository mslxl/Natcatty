package app

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/mslxl/natcatty/adapters/shellhook"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

const captureHookPOSIXFallback = "exec /bin/sh -c 'source /tmp/hook.sh'"

// TestCaptureHookRemoteSelectsTheRemoteShellWrapper proves the factory's
// late-bound choice: the planner writes the POSIX wrapper before connecting,
// and the factory replaces it only after the handshake shows a Windows
// OpenSSH peer. The probe reads the DefaultShell OpenSSH registers for exec
// requests, and that shell decides between the PowerShell hook and the cmd
// skip marker.
func TestCaptureHookRemoteSelectsTheRemoteShellWrapper(t *testing.T) {
	const (
		pwshPath = `C:\Program Files\PowerShell\7\pwsh.exe`
		cmdPath  = `C:\Windows\System32\cmd.exe`
	)
	probeOutput := "HKEY_LOCAL_MACHINE\\SOFTWARE\\OpenSSH\r\n" +
		"    DefaultShell    REG_SZ    " + pwshPath + "\r\n"
	cmdProbeOutput := "    DefaultShell    REG_SZ    " + cmdPath + "\r\n"
	cases := []struct {
		name          string
		serverVersion string
		probeOutput   string
		probeFails    bool
		captureRemote bool
		want          string
		wantPrefix    string
		wantProbe     bool
		wantLogin     bool
		wantNotice    bool
	}{
		{
			name:          "windows openssh with powershell",
			serverVersion: "SSH-2.0-OpenSSH_for_Windows_9.5",
			probeOutput:   probeOutput,
			captureRemote: true,
			want:          shellhook.WindowsPowerShellRemoteCommand(pwshPath),
			wantProbe:     true,
		},
		{
			name:          "windows openssh with cmd",
			serverVersion: "SSH-2.0-OpenSSH_for_Windows_9.5",
			probeOutput:   cmdProbeOutput,
			captureRemote: true,
			want:          shellhook.WindowsCmdSkipCommand(cmdPath),
			wantProbe:     true,
			wantNotice:    true,
		},
		{
			name:          "windows openssh without a registered shell",
			serverVersion: "SSH-2.0-OpenSSH_for_Windows_9.5",
			probeOutput:   "",
			captureRemote: true,
			want:          shellhook.WindowsCmdSkipCommand("cmd.exe"),
			wantProbe:     true,
			wantNotice:    true,
		},
		{
			name:          "windows openssh with a POSIX shell",
			serverVersion: "SSH-2.0-OpenSSH_for_Windows_9.5",
			probeOutput:   "    DefaultShell    REG_SZ    C:\\Program Files\\Git\\bin\\bash.exe\r\n",
			captureRemote: true,
			wantPrefix:    "exec /bin/sh -c '",
			wantProbe:     true,
		},
		{
			name:          "windows openssh with an un-wrappable shell",
			serverVersion: "SSH-2.0-OpenSSH_for_Windows_9.5",
			probeOutput:   "    DefaultShell    REG_SZ    C:\\Python312\\python.exe\r\n",
			captureRemote: true,
			want:          "",
			wantProbe:     true,
			wantLogin:     true,
			wantNotice:    true,
		},
		{
			name:          "windows openssh probe failure opens the login shell",
			serverVersion: "SSH-2.0-OpenSSH_for_Windows_9.5",
			probeFails:    true,
			captureRemote: true,
			want:          "",
			wantProbe:     true,
			wantLogin:     true,
			wantNotice:    true,
		},
		{
			name:          "posix openssh keeps the planner wrapper",
			serverVersion: "SSH-2.0-OpenSSH_9.6",
			captureRemote: true,
			want:          captureHookPOSIXFallback,
			wantProbe:     false,
		},
		{
			name:          "capture hook disabled by the setting",
			serverVersion: "SSH-2.0-OpenSSH_for_Windows_9.5",
			captureRemote: false,
			want:          captureHookPOSIXFallback,
			wantProbe:     false,
		},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseCommand := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseCommand)

			fixture := newRemoteCommandFixture(t, fakessh.Options{
				ServerVersion: test.serverVersion,
				Subsystems:    passwdSFTP("root:x:0:0:root:/root:/bin/zsh\n"),
				Exec: func(command string, channel xssh.Channel) int {
					if command == shellhook.WindowsDefaultShellProbeCommand() {
						if test.probeFails {
							// Close without an exit status: the client sees a
							// missing status, not a parseable empty answer.
							_ = channel.Close()
							return 0
						}
						if test.probeOutput != "" {
							_, _ = io.WriteString(channel, test.probeOutput)
						}
						return 0
					}
					// The terminal exec stays open until the test ends, so the
					// Session does not race its own assertions.
					<-release
					return 0
				},
			})
			transport, err := fixture.factory.Open(context.Background(), appsession.StartSpec{
				Kind:              appsession.KindSSH,
				Label:             "capture hook fixture",
				HostID:            "host-1",
				Hostname:          "example.com",
				Port:              22,
				Username:          "root",
				Cols:              100,
				Rows:              30,
				RemoteCommand:     captureHookPOSIXFallback,
				CaptureHookRemote: test.captureRemote,
			})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = transport.Close() })

			notifier, _ := transport.(interface {
				Notices() <-chan appsession.Notice
			})
			notice, gotNotice := appsession.Notice{}, false
			if notifier != nil && notifier.Notices() != nil {
				select {
				case notice = <-notifier.Notices():
					gotNotice = true
				default:
				}
			}
			if gotNotice != test.wantNotice {
				t.Fatalf("notice = %+v (present %t), want present %t", notice, gotNotice, test.wantNotice)
			}
			if gotNotice && notice.Code != appsession.NoticeCaptureHookUnavailable {
				t.Fatalf("notice code = %q", notice.Code)
			}

			server := fixture.sshServer(t)
			requests := server.ExecRequests()
			if test.wantLogin {
				if len(requests) != 1 || requests[0] != shellhook.WindowsDefaultShellProbeCommand() {
					t.Fatalf("exec requests = %q, want only the probe", requests)
				}
				if !server.ShellOpened() {
					t.Fatal("an unknown remote shell did not fall back to the login shell")
				}
				return
			}
			if test.wantProbe {
				if len(requests) != 2 {
					t.Fatalf("exec requests = %q, want a probe and the terminal command", requests)
				}
				if requests[0] != shellhook.WindowsDefaultShellProbeCommand() {
					t.Fatalf("first exec = %q, want the DefaultShell probe", requests[0])
				}
				if test.wantPrefix != "" {
					if !strings.HasPrefix(requests[1], test.wantPrefix) {
						t.Fatalf("terminal exec does not start with %q: %q", test.wantPrefix, requests[1])
					}
				} else if requests[1] != test.want {
					t.Fatalf("terminal exec = %q, want the wrapper for the registered shell", requests[1])
				}
				return
			}
			if len(requests) != 1 {
				t.Fatalf("exec requests = %q, want no probe", requests)
			}
			if requests[0] != test.want {
				t.Fatalf("terminal exec = %q, want the planner's fallback", requests[0])
			}
		})
	}
}

// A Windows OpenSSH peer whose registered shell cannot be resolved opens a
// plain login shell instead of the planned wrapper. The factory must tell the
// Session that no hook was installed: otherwise the tap reserves the first
// typed command for a handshake that never comes, drops every later command
// behind it, and publishes at most one unfinished capture at close. Both
// commands must be captured through the ordinary Tier 2/3 path before the
// Session ends.
func TestNoHookWindowsOpenSSHCapturesBothCommandsBeforeClose(t *testing.T) {
	const prompt = `C:\Windows\System32> `
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		ServerVersion: "SSH-2.0-OpenSSH_for_Windows_9.5",
		Exec: func(command string, channel xssh.Channel) int {
			if command == shellhook.WindowsDefaultShellProbeCommand() {
				// An unreadable registry key: the client sees a closed channel
				// with no exit status, so the shell stays unknown.
				_ = channel.Close()
				return 0
			}
			<-release
			return 0
		},
		Shell: func(channel xssh.Channel) {
			_, _ = io.WriteString(channel, prompt)
			reader := bufio.NewReader(channel)
			for {
				line, err := reader.ReadString('\r')
				if err != nil {
					return
				}
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				// Answer after the fallback's prompt guard window so the
				// shell's own prompt is what finishes each capture.
				time.Sleep(80 * time.Millisecond)
				_, _ = io.WriteString(channel, "OUT:"+line+"\r\n"+prompt)
			}
		},
	})
	events := make(chan appsession.CommandEvent, 8)
	service := appsession.NewService(
		fixture.factory,
		appsession.WithCaptureHook(shellhook.NewPlanner()),
		appsession.WithCommandCompletionHook(func(event appsession.CommandEvent) {
			events <- event
		}),
	)
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind:     appsession.KindSSH,
		Label:    "no hook fixture",
		HostID:   "host-1",
		Hostname: "example.com",
		Port:     22,
		Username: "root",
		Cols:     100,
		Rows:     30,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if !fixture.sshServer(t).ShellOpened() {
		t.Fatal("an unresolved remote shell did not open a login shell")
	}

	waitForCommand := func(want string) appsession.CommandEvent {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case event := <-events:
				if event.Command != want {
					t.Fatalf("captured %q, want %q", event.Command, want)
				}
				return event
			case <-timeout:
				t.Fatalf("command %q was not captured before close", want)
			}
		}
	}
	service.SendInput([]model.SessionID{session.ID()}, []byte("echo one\r"))
	first := waitForCommand("echo one")
	service.SendInput([]model.SessionID{session.ID()}, []byte("echo two\r"))
	second := waitForCommand("echo two")

	for _, event := range []appsession.CommandEvent{first, second} {
		if event.CaptureTier == domainhistory.CaptureTierHook {
			t.Fatalf("command %q used the hook that was never installed", event.Command)
		}
		if event.FinishedAt.IsZero() {
			t.Fatalf("command %q has no finish time", event.Command)
		}
	}
}
