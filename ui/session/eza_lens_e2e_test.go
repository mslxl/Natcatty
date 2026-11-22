package sessionui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens"
)

// The zsh this app runs under may alias ls to eza (`alias ls=eza`, `ll='eza
// -l'`), so `ls -l` and `ll` print eza's long format: no link count, "." for
// regular files, humanised sizes and a day-first date. files.ls claims those
// programs, and when its parser only knew ls's columns every eza row was
// rejected: the block had no view and the flow drew it as plain cells — the
// lens, and its copy button, disappeared. The eza rows must build the table.
func TestLSEzaListingShowsTheTable(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	for i := 0; i < 4; i++ {
		tester.Frame()
	}

	transport.emit("\x1b]133;A\x07\x1b]133;B\x07✦ work ❯ ")
	transport.emit("ll\r\n")
	transport.emit("\x1b]1337;NetcattyExec;zsh;L3RtcC93b3Jr;bGw=\x07\x1b]133;C\x07")
	transport.emit("drwxr-xr-x@   - mslxl 22 Nov 21:54 Applications\r\n")
	transport.emit("drwxr-xr-x@   - mslxl 22 Nov 21:54 'Calibre Library'\r\n")
	transport.emit(".rw-r--r--@ 2.8k mslxl 21 Nov 13:58 build.log\r\n")
	transport.emit(".rw-r--r--@  11M mslxl 21 Nov 13:03 a.a\r\n")
	transport.emit("lrwxr-xr-x@   - mslxl 22 Nov 21:54 alink -> new.txt\r\n")
	transport.emit("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07✦ work ❯ ")
	waitForFinishedTerminalBlock(t, surface, 1)
	for i := 0; i < 8; i++ {
		tester.Frame()
	}

	b := surface.flow.store.Snapshot()[0]
	if b.LensID != "files.ls" {
		t.Fatalf("ll lens = %q, want files.ls", b.LensID)
	}
	if _, ok := b.Native.(lens.Table); !ok {
		t.Fatalf("ll native = %T (%q), want a lens.Table from the eza rows", b.Native, b.LensErr)
	}
	for _, want := range []string{"Mode", "Owner", "Size", "Usage", "Name", "Applications", "Calibre Library", "build.log", "a.a", "alink → new.txt"} {
		if !tester.HasText(want) {
			t.Fatalf("ll table is missing %q: %v", want, tester.Texts())
		}
	}
	// eza prints no link count; the table must not invent the column.
	if tester.HasText("Links") {
		t.Fatalf("the eza table shows a Links column: %v", tester.Texts())
	}
	// A claimed listing with a view is a block: the Native/Raw switch is there.
	if _, ok := tester.Find("Native"); !ok {
		t.Fatalf("ll's bar has no Native option: %v", tester.Texts())
	}
}
