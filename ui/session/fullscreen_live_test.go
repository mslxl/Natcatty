package sessionui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// fullScreenRows draws a full-screen program's 24-row-style screen in the
// terminal's own grid: a title row, a body, and the command line the program
// takes input on, on the bottom row, where the cursor is.
func fullScreenRows(t *testing.T, surface *Surface, title, status string) string {
	t.Helper()
	waitFor(t, func() bool {
		cols, rows := surface.terminal.Size()
		return cols > 20 && rows > 8
	})
	_, rows := surface.terminal.Size()
	var b strings.Builder
	b.WriteString(title)
	for i := 1; i < rows-1; i++ {
		b.WriteString("\r\n~")
	}
	b.WriteString(fmt.Sprintf("\x1b[%d;1H", rows))
	b.WriteString(status)
	// Ghostty's formatter ends a full-screen program's snapshot with the
	// modes the program set: the kitty keyboard flags (CSI = 3 ; 1 u) and the
	// ASCII charset (ESC ( B). The parser must skip both whole. Read as
	// restore-cursor, the flag set put the cursor back at the top-left, and
	// the charset escape's "B" was drawn there, overwriting the first letter
	// of the screen's own top row.
	b.WriteString("\x1b[?2026h\x1b[=3;1u\x1b(B")
	return b.String()
}

// A full-screen program (vim, less, nano, emacs) runs on the alternate screen,
// whose snapshot has no scrollback. The shell integration's prompt row names a
// row the replay does not have, so cropping the live area to it or to the
// cursor kept only the bottom rows: the program's screen above the cursor was
// not drawn at all, and typing on the bottom rows ran out of the window with
// no scroll reaching the missing rows.
func TestFullScreenProgramKeepsItsWholeScreenAndTakesInput(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	for i := 0; i < 6; i++ {
		tester.Frame()
	}

	// Output first so the prompt row the mirror records is well below the
	// screen's top: a full-screen snapshot has no scrollback to hold it.
	transport.emit("\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	waitFor(t, func() bool { return strings.Contains(surface.terminal.Text(), "PS C:\\work>") })
	transport.emit("seq 60\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;Qzp9d29yaw;c2VxIDYw\x07\x1b]133;C\x07")
	var out strings.Builder
	for i := 0; i < 60; i++ {
		out.WriteString(fmt.Sprintf("out-%02d\r\n", i))
	}
	transport.emit(out.String())
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	waitFor(t, func() bool { return strings.Contains(surface.terminal.Text(), "out-59") })

	// vim: the program takes the alternate screen and draws its whole UI.
	transport.emit("vim\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;Qzp9d29yaw;dmlt\x07\x1b]133;C\x07")
	transport.emit("\x1b[?1049h\x1b[2J\x1b[H")
	transport.emit(fullScreenRows(t, surface, "VIM TITLE", ":set nocompatible"))
	// The status row is written last: waiting for it means the whole screen
	// reached the terminal, not just its title.
	waitFor(t, func() bool {
		return strings.Contains(surface.terminal.Text(), ":set nocompatible")
	})
	for i := 0; i < 4; i++ {
		tester.Frame()
	}

	text := strings.Join(tester.Texts(), "\n")
	for _, want := range []string{"VIM TITLE", ":set nocompatible"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the full-screen program's row %q is missing: %q", want, text)
		}
	}
	if strings.Index(text, "VIM TITLE") > strings.Index(text, ":set nocompatible") {
		t.Fatalf("the full-screen rows are not in screen order: %q", text)
	}
	bottom, ok := findLivePrefix(tester, ":set nocompatible")
	if !ok {
		t.Fatalf("the program's command line is not laid out: %q", text)
	}
	if bottom.Y+bottom.H > 600 {
		t.Fatalf("the program's command line is below the window: %+v", bottom)
	}

	// The keyboard still reaches the program: the live input box owns it.
	tester.Type("i")
	waitFor(t, func() bool { return strings.Contains(transport.Input(), "i") })
}

// The program leaves the alternate screen when it quits, and the shell's next
// prompt marker crops the live area again: the finished output must not be
// replayed under the new prompt.
func TestLeavingAFullScreenProgramRestoresThePromptTail(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	for i := 0; i < 6; i++ {
		tester.Frame()
	}

	transport.emit("\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	transport.emit("seq 60\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;Qzp9d29yaw;c2VxIDYw\x07\x1b]133;C\x07")
	var out strings.Builder
	for i := 0; i < 60; i++ {
		out.WriteString(fmt.Sprintf("out-%02d\r\n", i))
	}
	transport.emit(out.String())
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	waitFor(t, func() bool { return strings.Contains(surface.terminal.Text(), "out-59") })

	transport.emit("vim\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;Qzp9d29yaw;dmlt\x07\x1b]133;C\x07")
	transport.emit("\x1b[?1049h\x1b[2J\x1b[H")
	transport.emit(fullScreenRows(t, surface, "VIM TITLE", ":q"))
	waitFor(t, func() bool { return strings.Contains(surface.terminal.Text(), "VIM TITLE") })
	tester.Frame()

	// Quit: the alternate screen goes, and the shell draws its next prompt.
	transport.emit("\x1b[?1049l")
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ꜛ")
	waitFor(t, func() bool { return strings.Contains(surface.terminal.Text(), "PS C:\\work> ꜛ") })
	for i := 0; i < 4; i++ {
		tester.Frame()
	}

	text := strings.Join(tester.Texts(), "\n")
	if !strings.Contains(text, "PS C:\\work> ꜛ") {
		t.Fatalf("the shell's next prompt is missing: %q", text)
	}
	// The finished command's rows belong to its block once; a live area that
	// still started above them would replay them below the new prompt.
	if got := strings.Count(text, "out-59"); got != 1 {
		t.Fatalf("the finished output is rendered %d times, want once: %q", got, text)
	}
}
