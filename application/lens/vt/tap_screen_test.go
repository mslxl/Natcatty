package vt

import (
	"testing"

	"github.com/mslxl/natcatty/application/lens/model"
)

// procps top does not take the alternate screen: it clears and repaints the
// main one inside the command. The Pane has to know, because the shell's
// prompt row then names a line the program has overwritten (see
// ScreenCleared).
func TestTapReportsAMainScreenClearWhileACommandRuns(t *testing.T) {
	store := model.NewStore(nil)
	tap := New(store).WithCols(40)
	tap.Feed([]byte("\x1b]133;A\x07\x1b]133;B\x07PS> "))
	tap.Feed([]byte("\x1b]133;C;top\x07"))
	if tap.ScreenCleared() {
		t.Fatal("the screen is reported cleared before the command painted")
	}
	tap.Feed([]byte("\x1b[H\x1b[2Jtop row"))
	if !tap.ScreenCleared() {
		t.Fatal("a main-screen clear inside the command was not reported")
	}
	tap.Feed([]byte("\x1b]133;D;0\x07"))
	if tap.ScreenCleared() {
		t.Fatal("the clear outlived the command it belonged to")
	}
}

// A full-screen program on the alternate screen clears that screen, not the
// main one: the Pane draws an alternate screen whole already, and a clear
// there must not leak into the main screen's crop.
func TestTapIgnoresAnAlternateScreenClear(t *testing.T) {
	store := model.NewStore(nil)
	tap := New(store).WithCols(40)
	tap.Feed([]byte("\x1b]133;C;vim\x07"))
	tap.Feed([]byte("\x1b[?1049h\x1b[H\x1b[2JVIM"))
	if tap.ScreenCleared() {
		t.Fatal("an alternate-screen clear was reported as a main-screen one")
	}
	tap.Feed([]byte("\x1b[?1049l\x1b]133;D;0\x07"))
	if tap.ScreenCleared() {
		t.Fatal("the alternate screen left a clear behind")
	}
}

// Every command starts on the screen the shell left: a clear in one command
// does not carry into the next.
func TestTapResetsTheClearForTheNextCommand(t *testing.T) {
	store := model.NewStore(nil)
	tap := New(store).WithCols(40)
	tap.Feed([]byte("\x1b]133;C;top\x07\x1b[H\x1b[2Jtop row\x1b]133;D;0\x07"))
	tap.Feed([]byte("\x1b]133;C;echo hi\x07"))
	if tap.ScreenCleared() {
		t.Fatal("the previous command's clear carried into the next command")
	}
}
