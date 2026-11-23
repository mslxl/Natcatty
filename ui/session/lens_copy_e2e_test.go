package sessionui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/model"
)

// A lens block's hover bar carries the Native/Raw switch and the copy button.
// Both are drawn only while the card is hovered; the whole user path through
// the real Surface must reach them. A Disabled(!hovered) bar used to leave the
// hit chain between the pointer's move and its click, so a switch could never
// be confirmed and a click on the copy icon (then a sibling of its button,
// painting outside it) never landed.
func TestLensBlockBarSwitchAndCopy(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	for i := 0; i < 4; i++ {
		tester.Frame()
	}

	transport.emit("\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	transport.emit("ls -l\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;Qzpcd29yaw;bHMgLWw=\x07\x1b]133;C\x07")
	transport.emit("total 8\r\n-rw-r--r--  1 m  staff  123 Nov 22 10:00 file.txt\r\n")
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	waitForFinishedTerminalBlock(t, surface, 1)
	for i := 0; i < 8; i++ {
		tester.Frame()
	}

	raw, ok := tester.Find("Raw")
	if !ok {
		t.Fatalf("no Raw option: %v", tester.Texts())
	}
	native, ok := tester.Find("Native")
	if !ok {
		t.Fatalf("no Native option: %v", tester.Texts())
	}

	// Copy: the icon sits inside its 22 DIP button, right of Raw.
	copyX := raw.X + raw.W + 4 + 11
	copyY := raw.Y + raw.H/2
	tester.Move(copyX, copyY)
	tester.Frame()
	tester.ClickAt(copyX, copyY)
	tester.Frame()
	if got := tester.Clipboard(); !strings.Contains(got, "file.txt") {
		t.Fatalf("copy button left the clipboard at %q, want the raw output", got)
	}

	// Switch to Raw and back.
	tester.Move(raw.X+raw.W/2, raw.Y+raw.H/2)
	tester.Frame()
	tester.ClickAt(raw.X+raw.W/2, raw.Y+raw.H/2)
	tester.Frame()
	store := surface.flow.store
	if got := store.EffectiveView(store.Block(0)); got != model.ViewRaw {
		t.Fatalf("Raw click left the block in mode %v", got)
	}
	tester.Move(native.X+native.W/2, native.Y+native.H/2)
	tester.Frame()
	tester.ClickAt(native.X+native.W/2, native.Y+native.H/2)
	tester.Frame()
	if got := store.EffectiveView(store.Block(0)); got != model.ViewNative {
		t.Fatalf("Native click left the block in mode %v", got)
	}
}
