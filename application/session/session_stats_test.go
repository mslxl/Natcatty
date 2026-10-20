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

func TestSessionStatisticsCountOutputInputCommandsAndBells(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	bells := make(chan model.SessionID, 2)
	commands := make(chan string, 2)
	service := newTestService(factory,
		WithBellHook(func(id model.SessionID) { bells <- id }),
		WithCommandHook(func(_ model.SessionID, _ string, command string) { commands <- command }),
	)
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if _, err := session.TerminalConn().Write([]byte("echo hi\r")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "typed input", func() bool { return factory.transport.input() == "echo hi\r" })
	select {
	case command := <-commands:
		if command != "echo hi" {
			t.Fatalf("command hook = %q", command)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command hook was not called")
	}

	factory.transport.send("done\x07")
	if got := readOutput(t, session); got != "done\x07" {
		t.Fatalf("output = %q", got)
	}
	select {
	case <-bells:
	case <-time.After(3 * time.Second):
		t.Fatal("bell hook was not called")
	}

	eventually(t, "statistics", func() bool {
		stats := session.Snapshot().Stats
		return stats.OutputBytes == int64(len("done\x07")) &&
			stats.InputBytes == int64(len("echo hi\r")) &&
			stats.Commands == 1 && stats.Bells == 1 &&
			stats.Notifications == 0 && stats.FailedCommands == 0
	})
}

func TestSessionStatisticsCountFailedCommands(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	session, err := newTestService(factory).OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.TerminalConn().Write([]byte("false\r")); err != nil {
		t.Fatal(err)
	}
	factory.transport.send("\x1b]133;D;2\x07")
	_ = readOutput(t, session)
	eventually(t, "failed command", func() bool {
		return session.Snapshot().Stats.FailedCommands == 1
	})

	if _, err := session.TerminalConn().Write([]byte("true\r")); err != nil {
		t.Fatal(err)
	}
	factory.transport.send("\x1b]133;D;0\x07")
	_ = readOutput(t, session)
	eventually(t, "successful command", func() bool {
		return session.Snapshot().Stats.Commands == 2
	})
	if got := session.Snapshot().Stats.FailedCommands; got != 1 {
		t.Fatalf("failed commands = %d, want 1", got)
	}
}

func TestSessionStatisticsCountNotificationsAndTransfers(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory, WithNotificationHook(func(model.SessionID, TerminalNotification) {}))
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	factory.transport.send("\x1b]9;Build finished\x07")
	factory.transport.send("**\x18BpayloadOO")
	eventually(t, "notification and transfer counters", func() bool {
		stats := session.Snapshot().Stats
		return stats.Notifications == 1 && stats.Transfers == 1
	})
}

func TestSessionBellInATitleSequenceIsNotCounted(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	bells := make(chan struct{}, 1)
	service := newTestService(factory, WithBellHook(func(model.SessionID) { bells <- struct{}{} }))
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	factory.transport.send("\x1b]0;title\x07visible")
	if got := readOutput(t, session); got != "\x1b]0;title\x07visible" {
		t.Fatalf("output = %q", got)
	}
	eventually(t, "output statistics", func() bool { return session.Snapshot().Stats.OutputBytes > 0 })
	if got := session.Snapshot().Stats.Bells; got != 0 {
		t.Fatalf("OSC terminator counted as a bell: %d", got)
	}
	select {
	case <-bells:
		t.Fatal("an OSC terminator fired the bell hook")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSessionHighlightRewritesOutputButNotTheLog(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	rules := []domainterminal.KeywordHighlightRule{
		{ID: "error", Patterns: []string{`ERROR`}, Color: "#FF0000", Enabled: true},
	}
	service := newTestService(factory, WithHighlightPolicy(func() (bool, []domainterminal.KeywordHighlightRule) {
		return true, rules
	}))
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if err := session.StartLogging(LogOptions{
		Directory: t.TempDir(),
		Format:    domainterminal.LogFormatRaw,
		Label:     "host",
	}); err != nil {
		t.Fatal(err)
	}

	factory.transport.send("ERROR ok")
	got := readOutput(t, session)
	want := "\x1b[38;2;255;0;0mERROR\x1b[39m ok"
	if got != want {
		t.Fatalf("highlighted output = %q, want %q", got, want)
	}
	status := session.StopLogging()
	data, err := os.ReadFile(status.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ERROR ok" {
		t.Fatalf("log = %q, want raw output", string(data))
	}
}

// TestSessionHighlightReassemblesAKeywordSplitAcrossChunks proves the carry:
// a keyword divided between two transport reads is still colored, and a
// prompt without a trailing newline is flushed after the idle delay.
func TestSessionHighlightReassemblesAKeywordSplitAcrossChunks(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory, WithHighlightPolicy(func() (bool, []domainterminal.KeywordHighlightRule) {
		return true, []domainterminal.KeywordHighlightRule{
			{ID: "error", Patterns: []string{`\bERROR\b`}, Color: "#FF0000", Enabled: true},
		}
	}))
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	factory.transport.send("ERR")
	factory.transport.send("OR\n")
	want := "\x1b[38;2;255;0;0mERROR\x1b[39m\n"
	if got := readOutput(t, session); got != want {
		t.Fatalf("split keyword output = %q, want %q", got, want)
	}

	// A prompt has no newline; it is colored after the idle flush instead of
	// waiting for the next output.
	factory.transport.send("prefix ERROR")
	if got := readOutput(t, session); !strings.Contains(got, "\x1b[38;2;255;0;0mERROR\x1b[39m") {
		t.Fatalf("prompt output = %q, want colored ERROR", got)
	}
}

func TestSessionHighlightPolicyAppliesToLiveSessions(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory, WithHighlightPolicy(func() (bool, []domainterminal.KeywordHighlightRule) {
		return false, nil
	}))
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	factory.transport.send("ERROR")
	if got := readOutput(t, session); got != "ERROR" {
		t.Fatalf("disabled highlight changed output to %q", got)
	}

	service.SetHighlightPolicy(true, []domainterminal.KeywordHighlightRule{
		{ID: "error", Patterns: []string{`ERROR`}, Color: "#00FF00", Enabled: true},
	})
	factory.transport.send("ERROR")
	if got := readOutput(t, session); !strings.Contains(got, "\x1b[38;2;0;255;0mERROR\x1b[39m") {
		t.Fatalf("live highlight did not apply: %q", got)
	}
}
