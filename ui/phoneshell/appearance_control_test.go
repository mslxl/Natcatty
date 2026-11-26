package phoneshell

// The Session header's quick appearance bar and the global Lens terminal
// mode, driven through the same draw path the phone uses. The appearance test
// observes the live screen rather than only the settings value, so a write
// nobody applies fails here.

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// modeScreen is a fake terminal screen that also follows the shared Lens
// terminal mode, which is the seam the real ghostty screen implements.
type modeScreen struct {
	*fakeScreen
	lens bool
}

func (s *modeScreen) SetLensMode(on bool) { s.lens = on }

// liveSessionTab records one ready Pane whose screen is the given fake.
func liveSessionTab(screen TerminalScreen) []*phoneTab {
	return []*phoneTab{{
		id:    "live",
		label: "live",
		focus: "pane-live",
		panes: []*phonePane{{
			id:     "pane-live",
			label:  "live",
			status: tabReady,
			screen: screen,
		}},
	}}
}

func sessionTester(t *testing.T, shell *Shell) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		shell.sessions.drawSession(c, "live", nil, nil, nil)
	}, 390, 520)
	tt.Frame()
	return tt
}

func TestSessionFollowsTheGlobalLensTerminalMode(t *testing.T) {
	settings := newFakeSettings()
	shell := New(WithSettings(settings))
	screen := &modeScreen{fakeScreen: &fakeScreen{handle: newFakeHandle("")}}
	shell.sessions.tabs = liveSessionTab(screen)

	tt := sessionTester(t, shell)
	if !screen.lens {
		t.Fatal("Lens mode is off while RawTerminalMode is off")
	}

	// The global switch is the inverse: turning raw mode on leaves Lens.
	settings.term.RawTerminalMode = true
	tt.Frame()
	if screen.lens {
		t.Fatal("Lens mode stayed on after the global raw mode was enabled")
	}

	settings.term.RawTerminalMode = false
	tt.Frame()
	if !screen.lens {
		t.Fatal("Lens mode did not return when the global raw mode was cleared")
	}
}

func TestQuickAppearanceRestylesAndPersistsToSharedSettings(t *testing.T) {
	settings := newFakeSettings()
	settings.prefs.Appearance.TerminalFontSize = 13
	settings.prefs.Appearance.TerminalTheme = "netcatty-dark"
	settings.prefs.Appearance.FollowAppTerminalTheme = false
	shell := New(WithSettings(settings))
	screen := &fakeScreen{
		handle:     newFakeHandle(""),
		appearance: TerminalAppearanceFromAppearance(settings.prefs.Appearance, false),
	}
	shell.sessions.tabs = liveSessionTab(screen)

	tt := sessionTester(t, shell)
	if err := tt.Click("Terminal appearance for live"); err != nil {
		t.Fatalf("open the quick appearance bar: %v", err)
	}
	if !tt.HasText("Font size: 13") {
		t.Fatalf("the bar does not show the current size; texts: %v", tt.Texts())
	}

	if err := tt.Click("Larger terminal font"); err != nil {
		t.Fatalf("increase the terminal font: %v", err)
	}
	if got := settings.prefs.Appearance.TerminalFontSize; got != 14 {
		t.Fatalf("shared font size = %v, want 14", got)
	}
	if got := screen.appearance.Font.Size; got != 14 {
		t.Fatalf("live screen font size = %v, want 14 on the same tap", got)
	}

	before := settings.prefs.Appearance.TerminalTheme
	if err := tt.Click("Next color scheme"); err != nil {
		t.Fatalf("cycle the color scheme: %v", err)
	}
	if got := settings.prefs.Appearance.TerminalTheme; got == before {
		t.Fatalf("shared color scheme did not change from %q", before)
	}
	if settings.prefs.Appearance.FollowAppTerminalTheme {
		t.Fatal("a manual scheme kept following the app theme")
	}
	want := themeForTest(t, settings.prefs.Appearance.TerminalTheme)
	if screen.appearance.Theme == nil || screen.appearance.Theme.Background != want.Background {
		t.Fatalf("live screen scheme = %+v, want %v on the same tap", screen.appearance.Theme, want.Background)
	}

	if err := tt.Click("Smaller terminal font"); err != nil {
		t.Fatalf("decrease the terminal font: %v", err)
	}
	if got := settings.prefs.Appearance.TerminalFontSize; got != 13 {
		t.Fatalf("shared font size after decrease = %v, want 13", got)
	}
}

func TestQuickAppearanceReportsAFailedWrite(t *testing.T) {
	settings := newFakeSettings()
	settings.prefs.Appearance.TerminalFontSize = 13
	shell := New(WithSettings(settings))
	screen := &fakeScreen{handle: newFakeHandle("")}
	shell.sessions.tabs = liveSessionTab(screen)

	tt := sessionTester(t, shell)
	if err := tt.Click("Terminal appearance for live"); err != nil {
		t.Fatalf("open the quick appearance bar: %v", err)
	}
	settings.updateErr = errors.New("read-only settings document")
	if err := tt.Click("Larger terminal font"); err != nil {
		t.Fatalf("increase the terminal font: %v", err)
	}
	tt.Frame()
	if got := settings.prefs.Appearance.TerminalFontSize; got != 13 {
		t.Fatalf("font size = %v despite the failed write", got)
	}
	if !tt.HasText("Could not save the terminal appearance.") {
		t.Fatalf("no failure notice; texts: %v", tt.Texts())
	}
}

// The bar clamps at the same bounds the Settings stepper enforces.
func TestQuickAppearanceKeepsTheSharedFontBounds(t *testing.T) {
	settings := newFakeSettings()
	settings.prefs.Appearance.TerminalFontSize = float64(domainpreferences.MaxTerminalFontSize)
	shell := New(WithSettings(settings))
	screen := &fakeScreen{handle: newFakeHandle("")}
	shell.sessions.tabs = liveSessionTab(screen)

	tt := sessionTester(t, shell)
	if err := tt.Click("Terminal appearance for live"); err != nil {
		t.Fatalf("open the quick appearance bar: %v", err)
	}
	if err := tt.Click("Larger terminal font"); err != nil {
		t.Fatalf("increase the terminal font: %v", err)
	}
	if got := settings.prefs.Appearance.TerminalFontSize; got != float64(domainpreferences.MaxTerminalFontSize) {
		t.Fatalf("font size = %v, want the maximum %d", got, domainpreferences.MaxTerminalFontSize)
	}
}
