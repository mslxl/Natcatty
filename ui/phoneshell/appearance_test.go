package phoneshell

// Appearance on the phone: the terminal is built with the shared Appearance
// document and restyled when that document changes. The composition root
// installs the same provider; these tests observe the effect on the screen
// seam rather than the settings value, so a control that writes JSON nobody
// consumes would fail here.

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/terminal"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

func themeForTest(t *testing.T, id string) *terminal.Theme {
	t.Helper()
	scheme, ok := domaintheme.LookupTerminalTheme(id)
	if !ok {
		t.Fatalf("terminal scheme %q is not in the catalog", id)
	}
	return uitheme.Terminal(scheme)
}

// TestPhoneTerminalIsBuiltWithTheAppearance proves the constructed terminal
// really carries the font, colors and cursor the Appearance document names:
// the real libghostty-vt terminal is inspected through its own options.
func TestPhoneTerminalIsBuiltWithTheAppearance(t *testing.T) {
	dark := themeForTest(t, "netcatty-dark")
	appearance := TerminalAppearance{
		Font:   terminal.Font{Family: "monospace", Size: 17},
		Theme:  dark,
		Cursor: terminal.CursorBar,
		Blink:  false,
	}
	conn := newScreenConn(false)
	screen, err := newTerminalScreen(conn, appearance)
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	defer screen.Close()
	term := screen.(*ghosttyScreen).term

	if got := term.Font(); got.Family != "monospace" || got.Size != 17 {
		t.Fatalf("font = %+v, want monospace 17", got)
	}
	if got := term.Theme(); got == nil || got.Background != dark.Background {
		t.Fatalf("theme background = %+v, want %v", got, dark.Background)
	}
	if style, blink := term.Cursor(); style != terminal.CursorBar || blink {
		t.Fatalf("cursor = %v blink=%v, want bar, steady", style, blink)
	}
}

// TestPhoneTerminalReappliesAnAppearanceChange proves SetAppearance reaches a
// live terminal, which is the path the Settings change hook uses.
func TestPhoneTerminalReappliesAnAppearanceChange(t *testing.T) {
	conn := newScreenConn(false)
	screen, err := newTerminalScreen(conn, TerminalAppearance{
		Font:   terminal.Font{Family: "monospace", Size: 13},
		Theme:  themeForTest(t, "netcatty-dark"),
		Cursor: terminal.CursorBlock,
		Blink:  true,
	})
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	defer screen.Close()
	term := screen.(*ghosttyScreen).term

	light := themeForTest(t, "netcatty-light")
	screen.SetAppearance(TerminalAppearance{
		Font:   terminal.Font{Family: "monospace", Size: 22},
		Theme:  light,
		Cursor: terminal.CursorUnderline,
		Blink:  false,
	})

	if got := term.Font(); got.Size != 22 {
		t.Fatalf("font size after re-apply = %v, want 22", got.Size)
	}
	if got := term.Theme(); got == nil || got.Background != light.Background {
		t.Fatalf("theme background after re-apply = %+v, want %v", got, light.Background)
	}
	if style, blink := term.Cursor(); style != terminal.CursorUnderline || blink {
		t.Fatalf("cursor after re-apply = %v blink=%v, want underline, steady", style, blink)
	}
}

// TestAppearancePageRestylesTheLiveTerminal drives the Settings page over a
// live fake screen: the tap writes the shared service and the shell restyles
// the screen on the same frame, which is what "changes apply immediately"
// means when a terminal is live.
func TestAppearancePageRestylesTheLiveTerminal(t *testing.T) {
	settings := newFakeSettings()
	settings.prefs.Appearance.TerminalFontSize = 13
	settings.prefs.Appearance.TerminalCursorStyle = domainpreferences.CursorBlock
	shell := New(WithSettings(settings))

	screen := &fakeScreen{handle: newFakeHandle("")}
	shell.sessions.tabs = []*phoneTab{{
		id:     "live",
		status: tabReady,
		screen: screen,
	}}

	// Draw the page directly, so the Sessions lifecycle does not hibernate
	// the screen this test is watching.
	tt := ui.NewTester(func(c *ui.Context) {
		shell.settings.draw(c, page{path: "/appearance"})
	}, 390, 844)
	tt.Frame()

	if err := tt.Click("Increase Terminal font size"); err != nil {
		t.Fatalf("click font size increase: %v", err)
	}
	if got := screen.appearance.Font.Size; got != 14 {
		t.Fatalf("live screen font size = %v, want 14", got)
	}
	if err := tt.Click("Cursor: Bar"); err != nil {
		t.Fatalf("click Cursor: Bar: %v", err)
	}
	if got := screen.appearance.Cursor; got != terminal.CursorBar {
		t.Fatalf("live screen cursor = %v, want bar", got)
	}
}

// TestTerminalAppearanceMapping pins the shared document's mapping: the font
// family gets the monospace tail, the stored scheme is resolved, and the
// inverted steady flag becomes the blink option.
func TestTerminalAppearanceMapping(t *testing.T) {
	appearance := domainpreferences.Appearance{
		TerminalFontFamily:     "Fira Code",
		TerminalFontSize:       15,
		TerminalTheme:          "netcatty-light",
		TerminalCursorStyle:    domainpreferences.CursorUnderline,
		TerminalCursorSteady:   true,
		FollowAppTerminalTheme: false,
	}
	got := TerminalAppearanceFromAppearance(appearance, true)
	if got.Font.Family != "Fira Code, monospace" || got.Font.Size != 15 {
		t.Fatalf("font = %+v, want Fira Code, monospace at 15", got.Font)
	}
	if got.Cursor != terminal.CursorUnderline || got.Blink {
		t.Fatalf("cursor = %v blink=%v, want underline, steady", got.Cursor, got.Blink)
	}
	light := themeForTest(t, "netcatty-light")
	if got.Theme == nil || got.Theme.Background != light.Background {
		t.Fatalf("theme background = %+v, want the stored light scheme", got.Theme)
	}
}
