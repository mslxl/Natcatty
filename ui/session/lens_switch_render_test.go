package sessionui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// A bare `ls` (no -l) is claimed by files.ls — Match says yes — but its View
// returns nil for a plain name listing, so the block has a LensID and no
// native view. The block used to keep its widget chrome anyway: a bar with a
// Native option that can never be selected (clicking it writes ViewNative, and
// EffectiveView falls back to Raw because Native is nil), plus a copy button
// and foot around what is really terminal output.
//
// A block with no lens view must be drawn as the plain cells it is: no bar, no
// switch, no foot. Only a command the lens actually renders is a card.
func TestLensBlockWithoutAViewIsPlainCells(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	for i := 0; i < 4; i++ {
		tester.Frame()
	}

	transport.emit("\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	transport.emit("ls\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;Qzpcd29yaw;bHM=\x07\x1b]133;C\x07")
	transport.emit("alpha.txt  beta.go\r\n")
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	waitForFinishedTerminalBlock(t, surface, 1)
	for i := 0; i < 8; i++ {
		tester.Frame()
	}
	blocks := surface.flow.store.Snapshot()
	t.Logf("block: LensID=%q Native=%T Mode=%v", blocks[0].LensID, blocks[0].Native, blocks[0].Mode)

	// No card chrome is drawn at all: no bar, no switch, no foot. The block is
	// plain terminal cells, and its text stays copyable by selection.
	if _, ok := tester.Find("Native"); ok {
		t.Fatalf("a bare ls still drew a Native option the lens cannot honour")
	}
	if _, ok := tester.Find("Raw"); ok {
		t.Fatalf("a bare ls still drew a Raw option the lens cannot honour")
	}
	if !tester.HasText("alpha.txt") {
		t.Fatalf("the raw listing is not drawn: %v", tester.Texts())
	}
}
