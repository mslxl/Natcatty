package sessionui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/ui/terminal"

	"github.com/mslxl/natcatty/ui/terminal/appview"
	terminalsurface "github.com/mslxl/natcatty/ui/terminal/surface"
)

// The live rows must not move across a command's markers. A two-row prompt
// and the echoed input are on screen while the command runs; at OSC 133;D the
// finished block's captured prompt band replaces exactly those rows, the
// output starts one cell row below the last of them, and the replay below is
// cropped at the cursor so the command is not drawn twice. Each regression
// here moved text by a few DIP at every command, which read as drift.
func TestLiveRowsStayPutAcrossCommandMarks(t *testing.T) {
	surface, transport := newScriptedSurface(t, WithTerminalFont(func() terminal.Font {
		return terminal.Font{Family: "Menlo, monospace", Size: 13}
	}))
	t.Cleanup(func() { terminalsurface.SetTerminalFont("", 0) })
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 1100, 720)
	tester.SetLocale("en")
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	pump := func(what string, ok func() bool) {
		t.Helper()
		pumpFrames(t, tester, what, ok)
	}
	promptRow := func(t *testing.T) ui.Rect {
		t.Helper()
		r, ok := findLabelPrefix(tester, "✦ work ❯")
		if !ok {
			t.Fatalf("the prompt row is not drawn: %v", tester.Texts())
		}
		return r
	}
	typedRow := func(t *testing.T) ui.Rect {
		t.Helper()
		r, ok := findLabelPrefix(tester, "/tmp/work echo hello")
		if !ok {
			t.Fatalf("the echoed input row is not drawn: %v", tester.Texts())
		}
		return r
	}

	// The prompt's first row is marked by B; the input follows on its second.
	transport.emit("\x1b]133;A\x07\x1b]133;B\x07")
	transport.emit("✦ work ❯\r\n")
	transport.emit("/tmp/work ")
	pump("the prompt", func() bool { return tester.HasText("✦ work ❯") })
	transport.emit("echo hello\r\n")
	pump("the echoed input", func() bool { return tester.HasText("/tmp/work echo hello") })
	promptIdle := promptRow(t)
	typedIdle := typedRow(t)

	// The command starts: the live area keeps the prompt's own rows, so the
	// band that will replace them starts at the same place.
	transport.emit("\x1b]1337;NetcattyExec;zsh;L3RtcC93b3Jr;ZWNobyBoZWxsbw\x07\x1b]133;C\x07")
	pump("the running command", func() bool { return len(surface.flow.store.Snapshot()) == 1 })
	if got := promptRow(t); got.Y != promptIdle.Y {
		t.Errorf("the prompt row moved %.1f DIP while the command runs", got.Y-promptIdle.Y)
	}
	if got := typedRow(t); got.Y != typedIdle.Y {
		t.Errorf("the input row moved %.1f DIP while the command runs", got.Y-typedIdle.Y)
	}

	// The command ends: the block's band takes the rows' place, and the
	// output starts one cell row below the input, as it did on the grid.
	transport.emit("hello\r\n")
	pump("the output", func() bool { return tester.HasText("hello") })
	transport.emit("\x1b]133;D;0\x07")
	waitForFinishedTerminalBlock(t, surface, 1)
	for i := 0; i < 6; i++ {
		tester.Frame()
	}
	if got := promptRow(t); got.Y != promptIdle.Y {
		t.Errorf("the prompt moved %.1f DIP when the command finished", got.Y-promptIdle.Y)
	}
	if got := typedRow(t); got.Y != typedIdle.Y {
		t.Errorf("the input moved %.1f DIP when the command finished", got.Y-typedIdle.Y)
	}
	output, ok := tester.Find("hello")
	if !ok {
		t.Fatalf("the block body is not drawn: %v", tester.Texts())
	}
	if _, cellH := appview.CellSize(); cellH > 0 {
		if want := typedIdle.Y + cellH; output.Y < want-0.5 || output.Y > want+0.5 {
			t.Errorf("the output starts at y=%.1f, want one cell row below the input at %.1f", output.Y, want)
		}
	}
	// The finished command is in its block alone: the replay below is cropped
	// at the cursor until the next prompt replaces it.
	for _, text := range []string{"✦ work ❯", "/tmp/work echo hello", "hello"} {
		if n := countText(tester.Texts(), text); n != 1 {
			t.Errorf("%q is drawn %d times after the command ends, want once: %v", text, n, tester.Texts())
		}
	}

	// The next prompt starts below the block, and the block does not move.
	transport.emit("\x1b]133;A\x07\x1b]133;B\x07▸ ready ")
	pump("the next prompt", func() bool { return tester.HasText("▸ ready") })
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	if got := promptRow(t); got.Y != promptIdle.Y {
		t.Errorf("the prompt moved %.1f DIP at the next prompt", got.Y-promptIdle.Y)
	}
	if got := typedRow(t); got.Y != typedIdle.Y {
		t.Errorf("the input moved %.1f DIP at the next prompt", got.Y-typedIdle.Y)
	}
	next, ok := findLabelPrefix(tester, "▸ ready")
	if !ok {
		t.Fatalf("the next prompt is not drawn: %v", tester.Texts())
	}
	if next.Y <= output.Y {
		t.Errorf("the next prompt is at y=%.1f, not below the block's output at %.1f", next.Y, output.Y)
	}
}

// pumpFrames repaints until ok holds, so a test can observe a state between
// two OSC markers, which waitForFinishedTerminalBlock cannot.
func pumpFrames(t *testing.T, tester *ui.Tester, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		tester.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: %s", what, strings.Join(tester.Texts(), " | "))
}

// countText counts the labels of the last frame showing exactly text.
func countText(texts []string, text string) int {
	n := 0
	for _, got := range texts {
		if got == text {
			n++
		}
	}
	return n
}
