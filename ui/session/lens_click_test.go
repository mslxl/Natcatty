package sessionui

import (
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// A finished Lens block is a sibling of the live terminal input, and its body
// text is selectable, so a press used to move keyboard focus to the block's
// paragraph. Keys then had nowhere to go: the terminal's handler only runs for
// its own focused box. A press must give the keyboard back to the shell while
// the block still selects text and its buttons still work.
func TestClickingAFinishedLensBlockKeepsTypingInTheTerminal(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	// Shell integration marks: the prompt boundary is known only for the size
	// it was recorded at, and the scripted terminal settles its size over the
	// first frames.
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	// A Markdown file renders through Markdown's Selectable view, the case
	// where a press moves keyboard focus into the block's text.
	transport.emit("\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	transport.emit("\x1b]1337;NetcattyExec;bash;Qzpcd29yaw;Y2F0IFJFQURNRS5tZA==\x07")
	transport.emit("\x1b]133;C\x07# Title\r\n\r\nSelectable paragraph text\r\n\x1b]133;D;0\x07")
	transport.emit("\x1b]133;A\x07\x1b]133;B\x07PS C:\\work> ")
	waitForTerminalBlock(t, surface, 1)

	// Wait for the block to be rendered and for the one-shot focus recovery
	// to land: on the frame the block appears the terminal's own Focused
	// callback still reports the previous state.
	var block ui.Rect
	for i := 0; i < 12; i++ {
		tester.Frame()
		if r, ok := tester.Find("Selectable paragraph text"); ok && surface.terminalLiveFocused {
			block = r
			break
		}
	}
	if block == (ui.Rect{}) {
		t.Fatalf("the Markdown block is not in the frame with the terminal focused: %v", tester.Texts())
	}
	tester.ClickAt(block.X+2, block.Y+block.H/2)
	tester.Frame()

	// On macOS the window only turns a key into text for the element whose
	// input is active, so the press must leave the terminal as that target.
	if runtime.GOOS == "darwin" {
		if active, _ := tester.TextInputActive(); !active {
			t.Fatal("clicking a Lens block left the terminal without platform text input")
		}
	}

	tester.TypeKey(0, ui.KeyX, "after block click")
	if !waitFor(t, func() bool {
		return strings.Contains(transport.Input(), "after block click")
	}) {
		t.Fatalf("input after clicking the Lens block never reached the transport: %q", transport.Input())
	}
}
