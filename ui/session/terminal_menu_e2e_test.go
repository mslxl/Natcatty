package sessionui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

// The terminal's context menu is one menu on every surface: the live screen,
// the blank space around finished commands, and finished commands' text. These
// tests open it where a user does and choose items from it.

// liveMenuWant is the menu the live screen and the flow's blank space show.
// Copy and Select All act on the pane's cell selection; the Session menu
// keeps every other command reachable.
func liveMenuWant(m locales.Messages) []string {
	return []string{
		m.SessionMenuCopy(), m.SessionMenuPaste(), m.SessionMenuSelectAll(), "-",
		m.TerminalMenuCopyWorkingDirectory(), m.SessionMenuClearBuffer(), "-",
		m.SessionMenuLabel(),
	}
}

// textMenuWant is the menu a text view shows: MyGo's editing items first, so
// Copy and Select All act on the view's own paragraph selection, then the
// terminal's commands. The application does not repeat Copy or Select All.
func textMenuWant(m locales.Messages) []string {
	return []string{
		m.SessionMenuCopy(), "-", m.SessionMenuSelectAll(), "-",
		m.SessionMenuPaste(), "-",
		m.TerminalMenuCopyWorkingDirectory(), m.SessionMenuClearBuffer(), "-",
		m.SessionMenuLabel(),
	}
}

func sameMenu(got, want []string) bool {
	return strings.Join(got, "|") == strings.Join(want, "|")
}

// frameUntil pumps frames until done reports true, so a test does not race the
// terminal's reader goroutine.
func frameUntil(t *testing.T, tester *ui.Tester, what string, done func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if done() {
			return
		}
		tester.Frame()
	}
	t.Fatalf("the terminal never drew %s", what)
}

func TestTerminalContextMenuOnTheLiveScreen(t *testing.T) {
	surface, transport, tester := newSelectionSurface(t)
	m := locales.In("en")
	transport.emit("\x1b]7;file://test/tmp/menu-dir\x07")
	transport.emit("live menu text")
	frameUntil(t, tester, "the live text and its working directory", func() bool {
		return surface.Dir() == "/tmp/menu-dir" && surface.terminal != nil &&
			strings.Contains(surface.terminal.Text(), "live menu text")
	})
	tester.SetClipboard("menu paste")

	tester.RightClickAt(360, 420)
	tester.Frame()
	if got, want := tester.Menu(), liveMenuWant(m); !sameMenu(got, want) {
		t.Fatalf("live context menu = %v, want %v", got, want)
	}

	// With nothing selected Copy is present but disabled, as the Session menu
	// shows it; Select All then makes it act.
	if err := tester.ChooseMenuItem(m.SessionMenuCopy()); err == nil {
		t.Fatal("Copy on the live screen was enabled with no selection")
	}
	if err := tester.ChooseMenuItem(m.SessionMenuSelectAll()); err != nil {
		t.Fatalf("Select All: %v", err)
	}
	tester.Frame()
	tester.RightClickAt(360, 420)
	tester.Frame()
	if err := tester.ChooseMenuItem(m.SessionMenuCopy()); err != nil {
		t.Fatalf("Copy after Select All: %v", err)
	}
	tester.Frame()
	if got := tester.Clipboard(); !strings.Contains(got, "live menu text") {
		t.Fatalf("Copy wrote %q, want the live screen's text", got)
	}

	// The Session submenu is the toolbar's own menu; on the live screen its
	// Copy and Select All act on the same selection, so the labels do not
	// contradict the working items above them.
	tester.SetClipboard("")
	tester.RightClickAt(360, 420)
	tester.Frame()
	if err := tester.ChooseMenuItem(m.SessionMenuLabel(), m.SessionMenuCopy()); err != nil {
		t.Fatalf("Copy from the Session submenu: %v", err)
	}
	tester.Frame()
	if got := tester.Clipboard(); !strings.Contains(got, "live menu text") {
		t.Fatalf("the Session submenu's Copy wrote %q, want the live screen's text", got)
	}

	// Paste from this menu goes through the Session's paste gate, the same
	// path as the Edit menu and the toolbar's Session menu. Copy replaced the
	// clipboard above, so put the paste text back.
	tester.SetClipboard("menu paste")
	tester.RightClickAt(360, 420)
	tester.Frame()
	if err := tester.ChooseMenuItem(m.SessionMenuPaste()); err != nil {
		t.Fatalf("Paste: %v", err)
	}
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	if got := transport.Input(); !strings.Contains(got, "menu paste") {
		t.Fatalf("Paste sent %q to the shell, want the clipboard text", got)
	}

	// Copy Working Directory copies what the shell reported.
	tester.RightClickAt(360, 420)
	tester.Frame()
	if err := tester.ChooseMenuItem(m.TerminalMenuCopyWorkingDirectory()); err != nil {
		t.Fatalf("Copy Working Directory: %v", err)
	}
	if got := tester.Clipboard(); got != "/tmp/menu-dir" {
		t.Fatalf("Copy Working Directory wrote %q, want %q", got, "/tmp/menu-dir")
	}
}

// Blank space around finished commands has no text of its own. It is where a
// right-click used to reach no menu at all; it now opens the same live menu.
func TestTerminalContextMenuOnBlankSpace(t *testing.T) {
	_, transport, tester := newSelectionSurface(t)
	m := locales.In("en")
	tester.SetClipboard("blank space paste")

	tester.RightClickAt(4, 200)
	tester.Frame()
	if got, want := tester.Menu(), liveMenuWant(m); !sameMenu(got, want) {
		t.Fatalf("blank-space context menu = %v, want %v", got, want)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuPaste()); err != nil {
		t.Fatalf("Paste: %v", err)
	}
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	if got := transport.Input(); !strings.Contains(got, "blank space paste") {
		t.Fatalf("Paste sent %q to the shell, want the clipboard text", got)
	}
}

// A finished command's text keeps MyGo's Copy and Select All for the text
// selection and gains the terminal's commands after them. Paste still reaches
// the shell: the right-click does not move the keyboard away from it.
func TestTerminalContextMenuOnFinishedOutput(t *testing.T) {
	surface, transport, tester := newSelectionSurface(t)
	emitEchoBlock(t, surface, transport, tester)
	m := locales.In("en")
	tester.SetClipboard("block menu paste")

	if err := tester.RightClick("123"); err != nil {
		t.Fatalf("right-click the output: %v (texts %v)", err, tester.Texts())
	}
	tester.Frame()
	if got, want := tester.Menu(), textMenuWant(m); !sameMenu(got, want) {
		t.Fatalf("output context menu = %v, want %v", got, want)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuPaste()); err != nil {
		t.Fatalf("Paste: %v", err)
	}
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	if got := transport.Input(); !strings.Contains(got, "block menu paste") {
		t.Fatalf("Paste sent %q to the shell, want the clipboard text", got)
	}
}
