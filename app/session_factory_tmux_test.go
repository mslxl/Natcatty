package app

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/tmux"
)

// openTmux opens one SSH Session through the production factory and the fake
// wire server, with the tmux modifier enabled.
func (f *remoteCommandFixture) openTmux(t *testing.T, hostID model.HostID, cols, rows int) appsession.Transport {
	t.Helper()
	transport, err := f.factory.Open(context.Background(), appsession.StartSpec{
		Kind:           appsession.KindSSH,
		Label:          "tmux fixture",
		HostID:         hostID,
		Hostname:       "example.com",
		Port:           22,
		Username:       "root",
		Cols:           cols,
		Rows:           rows,
		AutoAttachTmux: true,
		Reconnect:      &appsession.ReconnectPolicy{},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

func tmuxProbeCommand(hostID model.HostID) string {
	return tmux.ProbeCommand(tmux.SessionName(hostID))
}

func tmuxWrapperCommand(hostID model.HostID) string {
	return tmux.WrapperCommand(tmux.SessionName(hostID))
}

// tmuxExec answers the probe with decision and reports a clean detach from the
// terminal wrapper.
func tmuxExec(t *testing.T, hostID model.HostID, decision tmux.Decision) func(string, xssh.Channel) int {
	t.Helper()
	return tmuxExecStatus(t, hostID, decision, tmux.DetachedExitStatus)
}

// tmuxExecStatus is tmuxExec with a chosen wrapper exit status, so a test can
// drive the ordinary-exit branches.
func tmuxExecStatus(t *testing.T, hostID model.HostID, decision tmux.Decision, wrapperStatus int) func(string, xssh.Channel) int {
	t.Helper()
	probe := tmuxProbeCommand(hostID)
	wrapper := tmuxWrapperCommand(hostID)
	return func(command string, channel xssh.Channel) int {
		switch command {
		case probe:
			_, _ = io.WriteString(channel, tmux.ProbeAnswer(decision)+"\n")
			return 0
		case wrapper:
			return wrapperStatus
		default:
			t.Errorf("unexpected exec command %q", command)
			return 99
		}
	}
}

func awaitNotice(t *testing.T, transport appsession.Transport, want appsession.NoticeCode) appsession.Notice {
	t.Helper()
	notifier, ok := transport.(appsession.Notifier)
	if !ok {
		t.Fatalf("transport %T does not report notices", transport)
	}
	select {
	case notice := <-notifier.Notices():
		if notice.Code != want {
			t.Fatalf("notice = %q, want %q", notice.Code, want)
		}
		return notice
	case <-time.After(time.Second):
		t.Fatalf("no %q notice arrived", want)
		return appsession.Notice{}
	}
}

func assertNoNotice(t *testing.T, transport appsession.Transport) {
	t.Helper()
	notifier, ok := transport.(appsession.Notifier)
	if !ok {
		return
	}
	select {
	case notice := <-notifier.Notices():
		t.Fatalf("unexpected notice %q", notice.Code)
	default:
	}
}

func TestSSHTmuxCreateRunsTheWrapperAndClassifiesDetach(t *testing.T) {
	hostID := model.HostID("host-1")
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Exec: tmuxExec(t, hostID, tmux.DecisionCreate),
	})
	transport := fixture.openTmux(t, hostID, 100, 30)

	if server := fixture.sshServer(t); server.ShellOpened() {
		t.Fatal("a tmux Session opened a login shell")
	}
	assertNoNotice(t, transport)
	if output := readUntilEOF(t, transport); output != "" {
		t.Fatalf("terminal output = %q, want none", output)
	}
	end := endOf(t, transport)
	if end.Reason != appsession.EndReasonDetached {
		t.Fatalf("end = %+v, want a detach", end)
	}
	requests := fixture.sshServer(t).ExecRequests()
	if len(requests) != 2 {
		t.Fatalf("exec requests = %q, want the probe and the wrapper", requests)
	}
	if requests[0] != tmuxProbeCommand(hostID) {
		t.Fatalf("first exec = %q, want the probe", requests[0])
	}
	if requests[1] != tmuxWrapperCommand(hostID) {
		t.Fatalf("second exec = %q, want the wrapper", requests[1])
	}
}

func TestSSHTmuxAttachReportsRestoredAndRunsTheWrapper(t *testing.T) {
	hostID := model.HostID("host-2")
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Exec: tmuxExec(t, hostID, tmux.DecisionAttach),
	})
	transport := fixture.openTmux(t, hostID, 80, 24)

	awaitNotice(t, transport, appsession.NoticeTmuxRestored)
	if output := readUntilEOF(t, transport); output != "" {
		t.Fatalf("terminal output = %q, want none", output)
	}
	if end := endOf(t, transport); end.Reason != appsession.EndReasonDetached {
		t.Fatalf("end = %+v, want a detach", end)
	}
}

// TestSSHTmuxWrapperExitZeroIsAnOrdinaryExit covers leaving the shell inside
// tmux: the session is gone, so a zero wrapper status must not close the Tab
// as a detach.
func TestSSHTmuxWrapperExitZeroIsAnOrdinaryExit(t *testing.T) {
	hostID := model.HostID("host-wrapper-zero")
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Exec: tmuxExecStatus(t, hostID, tmux.DecisionCreate, 0),
	})
	transport := fixture.openTmux(t, hostID, 80, 24)
	_ = readUntilEOF(t, transport)
	if end := endOf(t, transport); end.Reason != appsession.EndReasonExited {
		t.Fatalf("end = %+v, want an ordinary exit when the session ended", end)
	}
}

// TestSSHTmuxDetachStatusIsIgnoredWithoutTheWrapper keeps the private status
// scoped to the wrapper: an explicit remote command that happens to exit with
// it is still an ordinary exit.
func TestSSHTmuxDetachStatusIsIgnoredWithoutTheWrapper(t *testing.T) {
	hostID := model.HostID("host-explicit-status")
	const command = "exit 97"
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Exec: func(got string, _ xssh.Channel) int {
			if got != command {
				t.Errorf("exec = %q, want the explicit command", got)
			}
			return tmux.DetachedExitStatus
		},
	})
	transport, err := fixture.factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: hostID, Hostname: "example.com",
		Port: 22, Username: "root", RemoteCommand: command, AutoAttachTmux: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	_ = readUntilEOF(t, transport)
	if end := endOf(t, transport); end.Reason != appsession.EndReasonExited {
		t.Fatalf("end = %+v, want an ordinary exit for an explicit command", end)
	}
}

func TestSSHTmuxNestedFallsBackToALoginShellWithOneNotice(t *testing.T) {
	hostID := model.HostID("host-3")
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Exec:  tmuxExec(t, hostID, tmux.DecisionNested),
		Shell: func(channel xssh.Channel) { _ = channel.Close() },
	})
	first := fixture.openTmux(t, hostID, 80, 24)
	awaitNotice(t, first, appsession.NoticeTmuxNested)
	if !fixture.sshServer(t).ShellOpened() {
		t.Fatal("the nested case did not fall back to a login shell")
	}
	_ = first.Close()

	second := fixture.openTmux(t, hostID, 80, 24)
	if !fixture.sshServer(t).ShellOpened() {
		t.Fatal("the second nested connection did not fall back to a login shell")
	}
	assertNoNotice(t, second)
}

func TestSSHTmuxUnavailableFallsBackToALoginShellWithOneNotice(t *testing.T) {
	hostID := model.HostID("host-4")
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Exec:  tmuxExec(t, hostID, tmux.DecisionUnavailable),
		Shell: func(channel xssh.Channel) { _ = channel.Close() },
	})
	first := fixture.openTmux(t, hostID, 80, 24)
	awaitNotice(t, first, appsession.NoticeTmuxUnavailable)
	if !fixture.sshServer(t).ShellOpened() {
		t.Fatal("the unavailable case did not fall back to a login shell")
	}
	_ = first.Close()

	second := fixture.openTmux(t, hostID, 80, 24)
	assertNoNotice(t, second)
	if !fixture.sshServer(t).ShellOpened() {
		t.Fatal("the second unavailable connection did not fall back to a login shell")
	}
	requests := fixture.sshServer(t).ExecRequests()
	if len(requests) != 1 || requests[0] != tmuxProbeCommand(hostID) {
		t.Fatalf("exec requests = %q, want one probe and no wrapper", requests)
	}
}

// TestSSHTmuxProbeFailureFallsBackToTheLoginShell covers a shell that cannot
// run the probe: exec is refused (or the answer is unusable), and the Session
// still opens a plain login shell on the same connection.
func TestSSHTmuxProbeFailureFallsBackToTheLoginShell(t *testing.T) {
	hostID := model.HostID("host-5")
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Shell: func(channel xssh.Channel) { _ = channel.Close() },
	})
	transport := fixture.openTmux(t, hostID, 80, 24)
	awaitNotice(t, transport, appsession.NoticeTmuxUnavailable)
	if !fixture.sshServer(t).ShellOpened() {
		t.Fatal("a failed probe did not fall back to the login shell")
	}
}

func TestSSHTmuxGarbageProbeAnswerFallsBack(t *testing.T) {
	hostID := model.HostID("host-6")
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Exec: func(command string, channel xssh.Channel) int {
			if command == tmuxProbeCommand(hostID) {
				_, _ = io.WriteString(channel, "not a decision\n")
				return 0
			}
			return 99
		},
		Shell: func(channel xssh.Channel) { _ = channel.Close() },
	})
	transport := fixture.openTmux(t, hostID, 80, 24)
	awaitNotice(t, transport, appsession.NoticeTmuxUnavailable)
	if !fixture.sshServer(t).ShellOpened() {
		t.Fatal("a garbage probe answer did not fall back to the login shell")
	}
}

func TestSSHTmuxDoesNotOverrideAnExplicitRemoteCommand(t *testing.T) {
	hostID := model.HostID("host-7")
	const command = "printf explicit"
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Exec: func(got string, _ xssh.Channel) int {
			if got != command {
				t.Errorf("exec = %q, want the explicit command", got)
			}
			return 0
		},
	})
	transport, err := fixture.factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: hostID, Hostname: "example.com",
		Port: 22, Username: "root", RemoteCommand: command, AutoAttachTmux: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	_ = readUntilEOF(t, transport)
	requests := fixture.sshServer(t).ExecRequests()
	if len(requests) != 1 || requests[0] != command {
		t.Fatalf("exec requests = %q, want only %q", requests, command)
	}
	if end := endOf(t, transport); end.Reason != appsession.EndReasonExited {
		t.Fatalf("end = %+v, want an ordinary exit for an explicit command", end)
	}
}

func TestSSHTmuxIsSkippedForNetworkDeviceMode(t *testing.T) {
	fixture := newRemoteCommandFixture(t, fakessh.Options{
		Shell: func(channel xssh.Channel) { _ = channel.Close() },
	})
	transport, err := fixture.factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-8", Hostname: "example.com",
		Port: 22, Username: "root", AutoAttachTmux: true, NetworkDevice: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	if !fixture.sshServer(t).ShellOpened() {
		t.Fatal("network-device mode opened a tmux command")
	}
	if requests := fixture.sshServer(t).ExecRequests(); len(requests) != 0 {
		t.Fatalf("exec requests = %q, want none", requests)
	}
}

func TestTmuxWrapperSanitizesAHostileHostID(t *testing.T) {
	hostID := model.HostID("host-1'; touch pwned; echo '")
	session := tmux.SessionName(hostID)
	if strings.ContainsAny(session, " ';\t\n") {
		t.Fatalf("session name = %q, want only safe characters", session)
	}
	command := tmux.WrapperCommand(session)
	if strings.Contains(command, "touch pwned") {
		t.Fatalf("wrapper command contains unquoted host input:\n%s", command)
	}
	if !strings.Contains(command, tmux.Quote(session)) {
		t.Fatalf("wrapper command does not quote the session name:\n%s", command)
	}
}
