package session

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

func readOutput(t *testing.T, session *Session) string {
	t.Helper()
	result := make(chan string, 1)
	go func() {
		buffer := make([]byte, 64<<10)
		n, err := session.TerminalConn().Read(buffer)
		if err != nil {
			result <- ""
			return
		}
		result <- string(buffer[:n])
	}()
	select {
	case output := <-result:
		return output
	case <-time.After(3 * time.Second):
		t.Fatal("timed out reading terminal output")
		return ""
	}
}

func TestSessionLoggingCapturesFilteredOutput(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory, WithClipboardPolicy(func() domainterminal.ClipboardAccess {
		return domainterminal.ClipboardOff
	}))
	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal, Label: "host"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	directory := t.TempDir()
	if err := session.StartLogging(LogOptions{
		Directory: directory,
		Format:    domainterminal.LogFormatRaw,
		Label:     "host",
	}); err != nil {
		t.Fatal(err)
	}
	if !session.Snapshot().Logging.Active {
		t.Fatal("logging did not report active")
	}

	factory.transport.send("hello\r\n\x1b]52;c;c2VjcmV0\x07\x1b]99;hidden\x07")
	eventually(t, "terminal output", func() bool { return session.Snapshot().Busy })
	if got := readOutput(t, session); got != "hello\r\n" {
		t.Fatalf("terminal output = %q", got)
	}
	if got := session.Snapshot().Clipboard; got == nil || got.Allowed {
		t.Fatalf("clipboard event = %+v", got)
	}
	status := session.StopLogging()
	if status.Active {
		t.Fatal("stopped log reports active")
	}
	data, err := os.ReadFile(status.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\r\n" {
		t.Fatalf("raw log = %q", string(data))
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "hidden") {
		t.Fatal("forbidden OSC data reached the log")
	}
}

func TestSessionAutoLoggingCapturesFirstOutputAndStopsOnExit(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	directory := t.TempDir()
	service := newTestService(factory, WithAutoLogging(func(spec StartSpec) *LogOptions {
		return &LogOptions{Directory: directory, Format: domainterminal.LogFormatText, Label: spec.Label}
	}))
	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal, Label: "auto-host"})
	if err != nil {
		t.Fatal(err)
	}
	factory.transport.send("auto-logged\r\n")
	eventually(t, "terminal output", func() bool { return session.Snapshot().Busy })
	_ = readOutput(t, session)

	_ = session.Close()
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Session did not end")
	}
	status := session.Snapshot().Logging
	if status.Active || status.Problem != "" {
		t.Fatalf("final log status = %+v", status)
	}
	data, err := os.ReadFile(status.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "auto-logged\n" {
		t.Fatalf("text log = %q", string(data))
	}
}

func TestSessionNotificationHookReceivesOSC99(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	notifications := make(chan TerminalNotification, 1)
	service := newTestService(factory, WithNotificationHook(func(_ model.SessionID, notification TerminalNotification) {
		notifications <- notification
	}))
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	factory.transport.send("]99;i=1;Build donevisible")
	select {
	case notification := <-notifications:
		if notification.Title != "Netcatty" || notification.Body != "Build done" {
			t.Fatalf("notification = %+v", notification)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no notification")
	}
	if got := readOutput(t, session); got != "visible" {
		t.Fatalf("notification OSC reached the terminal: %q", got)
	}
}

func TestSessionGateSuppressesTransferBytesAndInput(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory)
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	factory.transport.send("before**\x18BsecretOOafter")
	eventually(t, "the gated chunk", func() bool { return !session.Snapshot().Transfer.StartedAt.IsZero() })
	if got := readOutput(t, session); got != "beforeafter" {
		t.Fatalf("terminal output = %q", got)
	}
	transfer := session.Snapshot().Transfer
	if transfer.Active || transfer.End != TransferEndCompleted || transfer.Suppressed == 0 {
		t.Fatalf("transfer = %+v", transfer)
	}

	factory.transport.send("**\x18Bdata")
	eventually(t, "an active transfer", func() bool { return session.Snapshot().Transfer.Active })
	if _, err := session.TerminalConn().Write([]byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	if got := factory.transport.input(); got != "" {
		t.Fatalf("input passed the gate: %q", got)
	}
	if _, err := session.TerminalConn().Write([]byte{0x03}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the cancel bytes", func() bool {
		return factory.transport.input() == string([]byte{0x18, 0x18, 0x18, 0x18, 0x18})
	})
	if got := session.Snapshot().Transfer; got.Active || got.End != TransferEndCancelled {
		t.Fatalf("transfer after Ctrl+C = %+v", got)
	}
}

func TestSessionTracksOSC7AndOSC133(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory)
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if _, err := session.TerminalConn().Write([]byte("echo hi\r")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "typed input", func() bool { return factory.transport.input() == "echo hi\r" })
	if command := session.Snapshot().Command; !command.Running || command.Text != "echo hi" {
		t.Fatalf("command = %+v", command)
	}

	factory.transport.send("\x1b]7;file://host/tmp/work\x07\x1b]133;D;7\x07result\r\n")
	eventually(t, "OSC 133 completion", func() bool { return !session.Snapshot().Command.Running })
	_ = readOutput(t, session)
	snapshot := session.Snapshot()
	if snapshot.CWD != "/tmp/work" {
		t.Fatalf("cwd = %q", snapshot.CWD)
	}
	if !snapshot.Command.HasExitCode || snapshot.Command.ExitCode != 7 {
		t.Fatalf("command = %+v", snapshot.Command)
	}

	session.AcknowledgeActivity()
	if session.Snapshot().Busy {
		t.Fatal("activity was not acknowledged")
	}
}

// A fast command's prompt can arrive while the fallback is still inside its
// guard window, and no further output follows. The Session must settle the
// command without more output; otherwise Running sticks and autocomplete stays
// suppressed forever.
func TestSessionFastPromptFinishesTheCommandWithoutFurtherOutput(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory)
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if _, err := session.TerminalConn().Write([]byte("echo world\r")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "typed input", func() bool { return factory.transport.input() == "echo world\r" })

	factory.transport.send("world\r\nPS C:\\work> ")
	eventually(t, "the fast command to finish", func() bool {
		return !session.Snapshot().Command.Running
	})
	_ = readOutput(t, session)
}

func TestSessionClipboardPolicySnapshot(t *testing.T) {
	service := NewService(&fakeFactory{transport: newFakeTransport()}, WithClipboardPolicy(func() domainterminal.ClipboardAccess {
		return domainterminal.ClipboardPrompt
	}))
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if got := session.ClipboardAccess(); got != domainterminal.ClipboardPrompt {
		t.Fatalf("clipboard access = %q", got)
	}
}

func TestSetClipboardPolicyAppliesToLiveSessions(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory, WithClipboardPolicy(func() domainterminal.ClipboardAccess {
		return domainterminal.ClipboardOff
	}))
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	service.SetClipboardPolicy(domainterminal.ClipboardReadWrite)
	if got := session.ClipboardAccess(); got != domainterminal.ClipboardReadWrite {
		t.Fatalf("live clipboard access = %q, want read-write", got)
	}
	factory.transport.send("\x1b]52;c;c2VjcmV0\x07allowed")
	eventually(t, "allowed clipboard event", func() bool {
		event := session.Snapshot().Clipboard
		return event != nil && event.Allowed
	})
	if got := readOutput(t, session); !strings.Contains(got, "]52;") || !strings.HasSuffix(got, "allowed") {
		t.Fatalf("forwarded clipboard output = %q", got)
	}

	service.SetClipboardPolicy(domainterminal.ClipboardOff)
	factory.transport.send("\x1b]52;c;c2VjcmV0\x07blocked")
	eventually(t, "blocked clipboard event", func() bool {
		event := session.Snapshot().Clipboard
		return event != nil && !event.Allowed
	})
	if got := readOutput(t, session); got != "blocked" {
		t.Fatalf("blocked clipboard output = %q", got)
	}
}

func TestSessionLoggingRejectsEmptyDirectory(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory)
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if err := session.StartLogging(LogOptions{}); err == nil {
		t.Fatal("empty log directory was accepted")
	}
}

func TestSessionLogOmitsAllowedClipboardPayload(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory, WithClipboardPolicy(func() domainterminal.ClipboardAccess {
		return domainterminal.ClipboardWriteOnly
	}))
	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal, Label: "host"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	secret := "c2VjcmV0LXBheWxvYWQ="
	if err := session.StartLogging(LogOptions{
		Directory: t.TempDir(),
		Format:    domainterminal.LogFormatRaw,
		Label:     "host",
	}); err != nil {
		t.Fatal(err)
	}
	factory.transport.send("before\x1b]52;c;" + secret + "\x07after")
	eventually(t, "terminal output", func() bool { return session.Snapshot().Busy })
	output := readOutput(t, session)
	if !strings.Contains(output, secret) {
		t.Fatalf("an allowed clipboard write did not reach the terminal: %q", output)
	}
	status := session.StopLogging()
	data, err := os.ReadFile(status.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("clipboard payload reached the log: %q", string(data))
	}
	if string(data) != "beforeafter" {
		t.Fatalf("raw log = %q", string(data))
	}
}
