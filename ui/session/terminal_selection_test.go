package sessionui

import (
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// liveCopyMods is the chord the live screen takes Copy on: Command on macOS,
// Control+Shift elsewhere so Control+C stays the program's interrupt.
func liveCopyMods() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Cmd
	}
	return ui.Cmd | ui.Shift
}

// findLivePrefix finds the text of the last frame starting with prefix. The
// live prompt is built after the finished blocks, so it is the last match, and
// the cursor blink changes the row's trailing cells, which Tester.Find's exact
// match cannot follow.
func findLivePrefix(t *ui.Tester, prefix string) (ui.Rect, bool) {
	var rect ui.Rect
	found := false
	for _, text := range t.Texts() {
		if !strings.HasPrefix(text, prefix) {
			continue
		}
		if r, ok := t.Find(text); ok {
			rect, found = r, true
		}
	}
	return rect, found
}

// A two-line prompt must keep its first line after a command scrolls the
// screen. The shell's OSC 133;B marks the prompt's first line while the cursor
// sits on its last one; deriving the live boundary from the cursor cut the
// prompt in half and drew the cursor above the line being typed.
func TestMultiLinePromptStaysAnchoredAfterACommandScrolls(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	for i := 0; i < 6; i++ {
		tester.Frame()
	}

	transport.emit("\x1b]133;A\x07\x1b]133;B\x07~ via x\r\nat 10:37 > ")
	waitFor(t, func() bool { return strings.Contains(surface.terminal.Text(), "10:37") })

	// A command long enough to push the prompt into the scrollback, then a
	// fresh prompt drawn the same way.
	transport.emit("seq 40\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;Qzpcd29yaw;c2VxIDQw\x07\x1b]133;C\x07")
	transport.emit(strings.Repeat("line\r\n", 40))
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07~ via x\r\nat 10:39 > ")
	waitFor(t, func() bool { return strings.Count(surface.terminal.Text(), "10:39") > 0 })
	for i := 0; i < 8; i++ {
		tester.Frame()
	}

	cols, rows := surface.terminal.Size()
	frame := appview.ScreenFrame(surface.terminal, appview.Size{Cols: cols, Rows: rows}, false)
	want := -1
	for i, spans := range frame.Lines {
		var line strings.Builder
		for _, sp := range spans {
			line.WriteString(sp.Text)
		}
		if strings.HasPrefix(strings.TrimRight(line.String(), " "), "~ via x") {
			want = i
		}
	}
	if want < 0 {
		t.Fatalf("the replayed screen has no prompt row: %q", terminalLines(frame.Lines))
	}
	got, known := surface.flow.promptBoundary(appview.Size{Cols: cols, Rows: rows})
	if !known || got != want {
		t.Fatalf("prompt boundary = (%d, %v), want (%d, true) for the prompt's first line", got, known, want)
	}

	// The live area shows the prompt whole: both of its lines are drawn, and
	// the rows the finished block already draws are not drawn again below it.
	// A boundary in the mirror's screen coordinates would satisfy the first
	// check and replay the whole output a second time.
	text := strings.Join(tester.Texts(), "\n")
	if !strings.Contains(text, "~ via x") || !strings.Contains(text, "at 10:39 >") {
		t.Fatalf("the live area cropped the multi-line prompt: %q", text)
	}
	if got := strings.Count(text, "line\n"); got != 40 {
		t.Fatalf("the output rows were replayed %d times, want 40 once: %q", got, text)
	}
}

func terminalLines(lines [][]ui.Span) []string {
	out := make([]string, len(lines))
	for i, spans := range lines {
		var line strings.Builder
		for _, sp := range spans {
			line.WriteString(sp.Text)
		}
		out[i] = strings.TrimRight(line.String(), " ")
	}
	return out
}

// The terminal's live text can be dragged out and copied, and the keyboard
// stays on the program so the next key still reaches the shell. MyGo's
// paragraph selection failed both halves: it painted and copied only while its
// paragraph held the keyboard, which the live input box needs for typing.
func TestTerminalLiveTextCanBeSelectedAndCopied(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	for i := 0; i < 6; i++ {
		tester.Frame()
	}

	// The prompt text is deliberately one a finished block never draws: the
	// blocks build their own prompt from the working directory, so this
	// prefix names the live row alone.
	transport.emit("\x1b]133;A\x07\x1b]133;B\x07⟦live⟧> ")
	waitFor(t, func() bool { return strings.Contains(surface.terminal.Text(), "⟦live⟧>") })
	for i := 0; i < 6; i++ {
		tester.Frame()
	}

	r, ok := findLivePrefix(tester, "⟦live⟧>")
	if !ok {
		t.Fatalf("live prompt not found: %q", tester.Texts())
	}
	tester.Press(r.X+2, r.Y+r.H/2)
	tester.Move(r.X+r.W-2, r.Y+r.H/2)
	tester.Release(r.X+r.W-2, r.Y+r.H/2)

	tester.Key(liveCopyMods(), ui.KeyC)
	if got := tester.Clipboard(); got != "⟦live⟧>" {
		t.Fatalf("copied %q, want %q", got, "⟦live⟧>")
	}

	// The box kept the keyboard, so typing goes to the program rather than to
	// a paragraph editor that would swallow it.
	tester.Type("x")
	waitFor(t, func() bool { return strings.Contains(transport.Input(), "x") })
}
