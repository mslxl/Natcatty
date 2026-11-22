package sessionui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// `pwd` is claimed by files.pwd: its native view shows the working directory as
// a path with a `working directory` badge, and the bar carries the Native/Raw
// switch. If the block has no native view the lens never saw a path line.
func TestPwdShowsItsLensView(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	for i := 0; i < 4; i++ {
		tester.Frame()
	}

	transport.emit("\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	transport.emit("pwd\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;Qzpcd29yaw;cHdk\x07\x1b]133;C\x07")
	transport.emit("/Users/m/work\r\n")
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	waitForFinishedTerminalBlock(t, surface, 1)
	for i := 0; i < 8; i++ {
		tester.Frame()
	}
	b := surface.flow.store.Snapshot()[0]
	t.Logf("pwd block: LensID=%q Native=%T LensErr=%q", b.LensID, b.Native, b.LensErr)

	if !tester.HasText("working directory") {
		t.Fatalf("pwd did not show its lens view: %v", tester.Texts())
	}
	if _, ok := tester.Find("Native"); !ok {
		t.Fatalf("pwd's bar has no Native option: %v", tester.Texts())
	}
}
