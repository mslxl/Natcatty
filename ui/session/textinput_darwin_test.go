//go:build darwin

package sessionui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// AppKit only interprets a key into text for a view whose input context is
// active, so a terminal that declared no text input received the key down and
// no characters: the Local Terminal could not be typed into at all. This is
// the regression for that report.
//
// It asserts the declaration, not typing, because the headless Tester cannot
// tell the two apart: Type, TypeKey and Compose inject InputText directly and
// never run the window's key-to-text step, so they pass with or without the
// declaration. That is exactly why the missing declaration shipped unnoticed
// while every typing test was green; only the active-input state is visible
// to a test here, so that is what this test checks.
func TestTerminalTakesPlatformTextInput(t *testing.T) {
	surface, _ := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	active, noIME := tester.TextInputActive()
	if !active {
		t.Fatal("the focused terminal does not take platform text input")
	}
	if noIME {
		t.Fatal("the terminal turned input methods off")
	}
}
