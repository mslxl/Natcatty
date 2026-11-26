package sessionui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/ui/terminal"

	"github.com/mslxl/natcatty/ui/terminal/appview"
	terminalsurface "github.com/mslxl/natcatty/ui/terminal/surface"
)

// A finished block's prompt row is the prompt the shell actually drew: the rows
// the shell integration's OSC 133;B..C span put on the screen, captured from
// the terminal mirror and painted by the live grid's own cell painter. A
// two-row, coloured prompt is shown as-is, with the echoed input; the app no
// longer invents a prompt from the shell's name (which put `PS ...>` in front
// of a zsh prompt) nor draws it in a font of its own.
func TestBlockShowsTheShellsOwnPromptAndInput(t *testing.T) {
	surface, transport := newScriptedSurface(t, WithTerminalFont(func() terminal.Font {
		return terminal.Font{Family: "Menlo, monospace", Size: 17}
	}))
	t.Cleanup(func() { terminalsurface.SetTerminalFont("", 0) })
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	// The Surface reports the Appearance font to the views that draw terminal
	// text themselves, unchanged: the comma-separated list is what the terminal
	// plugin takes.
	if family, size := terminalsurface.TerminalFont(); family != "Menlo, monospace" || size != 17 {
		t.Fatalf("configured terminal font = %q, %.1f; want Menlo, monospace at 17", family, size)
	}

	// A prompt of the user's own shape: two rows, orange and blue. The hook's
	// B marks the prompt's first row (the cursor there); C comes after the
	// echoed input.
	transport.emit("\x1b]133;A\x07\x1b]133;B\x07")
	transport.emit("\x1b[38;2;255;128;0m✦ work\x1b[0m \x1b[38;2;90;160;255m❯\x1b[0m\r\n")
	transport.emit("\x1b[2m/tmp/work\x1b[0m echo hi\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;L3RtcC93b3Jr;ZWNobyBoaQ\x07\x1b]133;C\x07")
	transport.emit("hi\r\n")
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07▸ ready ")
	waitForFinishedTerminalBlock(t, surface, 1)
	for i := 0; i < 8; i++ {
		tester.Frame()
	}

	// Both prompt rows and the echoed input are drawn above the block exactly
	// as they were on the grid.
	for _, want := range []string{"✦ work ❯", "/tmp/work echo hi", "▸ ready"} {
		if !tester.HasText(want) {
			t.Fatalf("the frame is missing %q: %s", want, strings.Join(tester.Texts(), " | "))
		}
	}
	for _, text := range tester.Texts() {
		if strings.Contains(text, "PS ") || strings.Contains(text, "/tmp/work %") {
			t.Fatalf("a prompt was invented instead of showing the shell's own: %q", text)
		}
	}

	// The captured rows are grid rows: they start at the live grid's left edge
	// and are one measured cell tall, so nothing shifts when the command moves
	// out of the live screen.
	block, ok := tester.Find("/tmp/work echo hi")
	if !ok {
		t.Fatalf("the captured input row is not drawn: %s", strings.Join(tester.Texts(), " | "))
	}
	live, ok := findLabelPrefix(tester, "▸ ready")
	if !ok {
		t.Fatalf("the live prompt row is not drawn: %s", strings.Join(tester.Texts(), " | "))
	}
	if block.X != live.X {
		t.Errorf("the block's prompt starts at x=%.1f, the live grid at x=%.1f", block.X, live.X)
	}
	if _, cellH := appview.CellSize(); cellH <= 0 || block.H < cellH-0.5 || block.H > cellH+0.5 {
		t.Errorf("the block's prompt row is %.1f DIP tall, want the grid cell %.1f", block.H, cellH)
	}
}

// findLabelPrefix finds the first label that starts with prefix. The live
// prompt row may carry the cursor's cells after the text, which Find's exact
// match would miss.
func findLabelPrefix(tester *ui.Tester, prefix string) (ui.Rect, bool) {
	for _, text := range tester.Texts() {
		if strings.HasPrefix(text, prefix) {
			return tester.Find(text)
		}
	}
	return ui.Rect{}, false
}
