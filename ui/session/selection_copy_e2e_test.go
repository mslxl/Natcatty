package sessionui

import (
	"image"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// A press on a finished command's output hands the keyboard back to the
// shell, so typing goes on into the terminal. The text the press selected
// must stay highlighted and stay copyable even though the focus is back in
// the live box: MyGo keeps a paragraph selection shown, and Copy acts on it,
// while it exists (Netcatty patch). Without that, every command's output
// could be dragged over but not copied.

func newSelectionSurface(t *testing.T) (*Surface, *scriptedTransport, *ui.Tester) {
	t.Helper()
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	return surface, transport, tester
}

// emitEchoBlock runs `echo 123`, a command no lens claims: its output is a
// plain cell, the way most commands render.
func emitEchoBlock(t *testing.T, surface *Surface, transport *scriptedTransport, tester *ui.Tester) {
	t.Helper()
	transport.emit("\x1b]133;A\x07\x1b]133;B\x07✦ work ❯ ")
	transport.emit("echo 123\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;L3RtcC93b3Jr;ZWNobyAxMjM=\x07\x1b]133;C\x07")
	transport.emit("123\r\n")
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07✦ work ❯ ")
	waitForFinishedTerminalBlock(t, surface, 1)
	for i := 0; i < 8; i++ {
		tester.Frame()
	}
}

// changedPixels counts the pixels of r that differ between two frames. A
// painted text selection repaints that paragraph's cells; nothing else in a
// finished block changes between frames.
func changedPixels(a, b *image.RGBA, r ui.Rect) int {
	diff := 0
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				diff++
			}
		}
	}
	return diff
}

// dragOver drags the pointer across the row at y, from the left edge of r to
// its right edge, with a frame between the steps as the user's own pace has.
func dragOver(tester *ui.Tester, r ui.Rect) {
	y := r.Y + r.H/2
	tester.Press(r.X+1, y)
	tester.Frame()
	tester.Move(r.X+r.W-1, y)
	tester.Frame()
	tester.Release(r.X+r.W-1, y)
	tester.Frame()
}

func TestPlainCellSelectionCopiesWithCopyChord(t *testing.T) {
	surface, transport, tester := newSelectionSurface(t)
	emitEchoBlock(t, surface, transport, tester)
	for i := 0; i < 8; i++ {
		tester.Frame()
	}

	r, ok := tester.Find("123")
	if !ok {
		t.Fatalf("no output row: %v", tester.Texts())
	}
	before := tester.Image()
	dragOver(tester, r)
	if diff := changedPixels(before, tester.Image(), r); diff == 0 {
		t.Fatal("the drag painted no selection over the output: the pane's refocus blanked it out")
	}
	// The Copy chord: Command on macOS, Control+Shift elsewhere, where a
	// terminal cannot take plain Control+C from the program.
	tester.Key(liveCopyMods(), ui.KeyC)
	tester.Frame()
	if got := tester.Clipboard(); got != "123" {
		t.Fatalf("the Copy chord copied %q, want the selected output %q", got, "123")
	}

	// The pane's refocus is why the selection survives a press: typing must
	// still reach the shell, and the press on the live screen ends the
	// block's selection instead.
	tester.ClickAt(360, 420)
	tester.Frame()
	if diff := changedPixels(before, tester.Image(), r); diff != 0 {
		t.Fatalf("the block kept its selection after a press on the live screen: %d pixels", diff)
	}
	tester.Type("echo hi")
	if !strings.Contains(transport.Input(), "echo hi") {
		t.Fatalf("typing after the press reached the shell as %q", transport.Input())
	}
}

func TestPlainCellSelectionCopiesWithEditMenu(t *testing.T) {
	surface, transport, tester := newSelectionSurface(t)
	emitEchoBlock(t, surface, transport, tester)
	for i := 0; i < 8; i++ {
		tester.Frame()
	}

	r, ok := tester.Find("123")
	if !ok {
		t.Fatalf("no output row: %v", tester.Texts())
	}
	dragOver(tester, r)
	tester.Command("copy")
	tester.Frame()
	if got := tester.Clipboard(); got != "123" {
		t.Fatalf("Edit › Copy copied %q, want the selected output %q", got, "123")
	}
}

// A lens block's table is selectable text too: dragging across a row and
// copying must give the row's text, whether or not the card has a copy
// button of its own.
func TestLensTableSelectionCopies(t *testing.T) {
	surface, transport, tester := newSelectionSurface(t)
	transport.emit("\x1b]133;A\x07\x1b]133;B\x07✦ work ❯ ")
	transport.emit("ll\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;L3RtcC93b3Jr;bGw=\x07\x1b]133;C\x07")
	transport.emit("drwxr-xr-x@   - mslxl 22 Nov 21:54 Applications\r\n")
	transport.emit("drwxr-xr-x@   - mslxl 22 Nov 21:54 'Calibre Library'\r\n")
	transport.emit(".rw-r--r--@ 2.8k mslxl 21 Nov 13:58 build.log\r\n")
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07✦ work ❯ ")
	waitForFinishedTerminalBlock(t, surface, 1)
	for i := 0; i < 8; i++ {
		tester.Frame()
	}

	r, ok := tester.Find("Applications")
	if !ok {
		t.Fatalf("no table row: %v", tester.Texts())
	}
	dragOver(tester, r)
	tester.Key(liveCopyMods(), ui.KeyC)
	tester.Frame()
	if got := tester.Clipboard(); !strings.Contains(got, "Applications") {
		t.Fatalf("the Copy chord copied %q, want the selected table row", got)
	}
}
