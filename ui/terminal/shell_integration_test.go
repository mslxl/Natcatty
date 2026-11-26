package terminal

import (
	"fmt"
	"strings"
	"testing"
)

func TestShellIntegrationMarkedPromptResize(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), ShellIntegration: true})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Resize(100, 20)
	term.Feed([]byte("history\r\n\x1b]133;A\a\r\n\x1b[Aresize-prompt\x1b[90Gclock\r\n> pending-command\x1b]133;B\a"))
	// The rows stay, shrink by one, grow or shrink by more.
	for _, size := range [][2]int{{80, 20}, {100, 19}, {40, 18}, {120, 24}, {80, 23}, {60, 23}} {
		cols, rows := size[0], size[1]
		term.Resize(cols, rows)
		want := "history\nresize-prompt" + strings.Repeat(" ", cols-18) + "clock\n> pending-command"
		if got := term.Text(); got != "history" {
			t.Fatalf("at %dx%d, the prompt was left: %q", cols, rows, got)
		}
		term.Feed([]byte(fmt.Sprintf("\r\x1b[A\x1b]133;A\aresize-prompt\x1b[%dGclock\r\n> pending-command\x1b]133;B\a", cols-4)))
		if got := term.Text(); got != want {
			t.Fatalf("at %dx%d: %q", cols, rows, got)
		}
	}
	term.Feed([]byte("\x1b]133;C\a\r\n"))
	output := strings.Repeat("long command output ", 20)
	term.Feed([]byte(output))
	term.Resize(40, 10)
	term.Resize(120, 30)
	if got := term.Text(); !strings.Contains(got, strings.TrimSpace(output)) {
		t.Fatalf("resizing command output lost text: %q", got)
	}
}

func TestShellIntegrationPreservesAlternateScreen(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), ShellIntegration: true})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Resize(100, 20)
	term.Feed([]byte("\x1b]133;A\aprompt\x1b]133;B\a\x1b[?1049halt-program"))
	term.Resize(40, 20)
	if got := term.Text(); !strings.Contains(got, "alt-program") {
		t.Fatalf("prompt tracking cleared a full-screen program: %q", got)
	}
}

func TestShellIntegrationPromptTracker(t *testing.T) {
	var p promptTracker
	for _, part := range []string{"\x1b]13", "3;A;redraw=1\x1b", "\\prompt\x1b]133;B\a"} {
		p.feed([]byte(part))
	}
	if !p.atPrompt {
		t.Fatal("fragmented prompt was missed")
	}
	for _, data := range []string{"\x1b]2;title\a", "\x1b]133;Command\a", "\x1bP\x1b]133;C\a\x1b\\"} {
		p.feed([]byte(data))
		if !p.atPrompt {
			t.Fatalf("unrelated or opaque control data changed prompt state: %q", data)
		}
	}
	p.feed([]byte("\x1b]133;C\a"))
	if p.atPrompt {
		t.Fatal("command output was marked as a prompt")
	}
	p.feed([]byte("\x1b]133;A;" + strings.Repeat("x", 1024) + "\a"))
	if p.atPrompt || len(p.body) > 64 {
		t.Fatal("oversized OSC was retained or accepted")
	}
	p.feed([]byte("\x1b]133;A\x18ignored\a"))
	if p.atPrompt {
		t.Fatal("canceled OSC was accepted")
	}
	p.feed([]byte("\x1b]133;A\a\x1bc"))
	if p.atPrompt {
		t.Fatal("terminal reset retained prompt state")
	}
	p.feed([]byte("\x1b]133;N\a"))
	if !p.atPrompt {
		t.Fatal("a new command's prompt was missed")
	}
}
