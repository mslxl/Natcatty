package quake

import (
	"errors"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/dropdown"
	"github.com/mslxl/natcatty/ui/locales"
)

// viewSurface is a Surface a view test can see and inspect. It draws its name
// so the tester can find it and measure the body's rectangle.
type viewSurface struct {
	name          string
	closes        int
	focusedFrames int
	idleFrames    int
}

func (s *viewSurface) View(c *ui.Context) ui.Element {
	s.idleFrames++
	return ui.Text(c, s.name).Fill()
}

func (s *viewSurface) ViewFocused(c *ui.Context, focused bool) ui.Element {
	if focused {
		s.focusedFrames++
	} else {
		s.idleFrames++
	}
	return ui.Text(c, s.name).Fill()
}

func (s *viewSurface) Close() { s.closes++ }

// viewFixture drives a State over the shared Session service with a fake
// transport and surfaces named surface-1, surface-2, ...
type viewFixture struct {
	state    *State
	opener   *testOpener
	surfaces []*viewSurface
}

func newViewFixture(t *testing.T) *viewFixture {
	t.Helper()
	service := appsession.NewService(&testFactory{})
	t.Cleanup(service.CloseAll)
	fixture := &viewFixture{opener: &testOpener{service: service}}
	fixture.state = NewState(fixture.opener, func(*appsession.Session) (Surface, error) {
		surface := &viewSurface{name: fmt.Sprintf("surface-%d", len(fixture.surfaces)+1)}
		fixture.surfaces = append(fixture.surfaces, surface)
		return surface, nil
	})
	t.Cleanup(fixture.state.Shutdown)
	return fixture
}

// panel is the Panel's root content as the app draws it: the dropdown frame
// supplies the hide control, and the Quake bar and body route inside it.
func panel(state *State, onHide func()) func(*ui.Context) {
	return dropdown.View(onHide, Bar(state), View(state))
}

func renderPanel(t *testing.T, state *State, width, height int) *ui.Tester {
	t.Helper()
	tester := ui.NewTester(panel(state, nil), width, height)
	tester.SetLocale("en")
	tester.Frame()
	return tester
}

// TestBarListsTabsAndSelectsTheClickedOne covers the compact top bar: one
// entry per Quake Tab, and clicking another Tab switches the active terminal.
func TestBarListsTabsAndSelectsTheClickedOne(t *testing.T) {
	fixture := newViewFixture(t)
	state := fixture.state
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	m := locales.In("en")

	tester := renderPanel(t, state, 800, 400)
	for _, want := range []string{m.LocalSessionTab(1), m.LocalSessionTab(2), "surface-2"} {
		if !tester.HasText(want) {
			t.Fatalf("the panel is missing %q: %v", want, tester.Texts())
		}
	}
	if tester.HasText("surface-1") {
		t.Fatal("an inactive Tab rendered its terminal")
	}

	if err := tester.Click(m.LocalSessionTab(1)); err != nil {
		t.Fatalf("clicking the first Tab: %v", err)
	}
	tester.Frame()
	if state.ActiveIndex() != 0 {
		t.Fatalf("active = %d after clicking the first Tab, want 0", state.ActiveIndex())
	}
	if !tester.HasText("surface-1") {
		t.Fatalf("the first Tab's terminal did not replace the second's: %v", tester.Texts())
	}
	if tester.HasText("surface-2") {
		t.Fatal("the previously active terminal stayed mounted")
	}
}

// TestAddButtonOpensALocalShellTab covers the + control in the compact bar.
func TestAddButtonOpensALocalShellTab(t *testing.T) {
	fixture := newViewFixture(t)
	state := fixture.state
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	m := locales.In("en")
	tester := renderPanel(t, state, 800, 400)

	if err := tester.Click(m.MenuNewTab()); err != nil {
		t.Fatalf("clicking the add control: %v", err)
	}
	tester.Frame()
	if state.TabCount() != 2 {
		t.Fatalf("Tabs = %d after clicking +, want 2", state.TabCount())
	}
	if fixture.opener.opens != 2 {
		t.Fatalf("opens = %d, want a second local Shell", fixture.opener.opens)
	}
	if !tester.HasText(m.LocalSessionTab(2)) {
		t.Fatalf("the new Tab is missing from the bar: %v", tester.Texts())
	}
}

// TestClosingTheLastTabHidesThePanel proves the close control ends the Tab and
// reports the empty workspace to the app, which hides the Panel.
func TestClosingTheLastTabHidesThePanel(t *testing.T) {
	fixture := newViewFixture(t)
	state := fixture.state
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	hides := 0
	state.empty = func() { hides++ }
	m := locales.In("en")
	tester := ui.NewTester(panel(state, nil), 800, 400)
	tester.SetLocale("en")
	tester.Frame()

	if err := tester.Click(m.MenuCloseTab()); err != nil {
		t.Fatalf("clicking the close control: %v", err)
	}
	if state.TabCount() != 0 {
		t.Fatalf("Tabs = %d after closing the last one, want 0", state.TabCount())
	}
	if hides != 1 {
		t.Fatalf("panel hide requests = %d, want 1", hides)
	}
	if fixture.surfaces[0].closes != 1 {
		t.Fatalf("the closed Tab's surface closes = %d, want 1", fixture.surfaces[0].closes)
	}
}

// TestActiveTerminalTakesTheKeyboardEveryFrame proves focus is re-requested
// on every frame, because MyGo prunes an unmounted subtree's element state.
func TestActiveTerminalTakesTheKeyboardEveryFrame(t *testing.T) {
	fixture := newViewFixture(t)
	state := fixture.state
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	tester := renderPanel(t, state, 800, 400)
	first, second := fixture.surfaces[0], fixture.surfaces[1]
	if second.focusedFrames == 0 {
		t.Fatal("the active Tab's terminal never received focus")
	}
	if first.focusedFrames != 0 {
		t.Fatalf("an inactive Tab's terminal received focus %d times", first.focusedFrames)
	}

	beforeSecond := second.focusedFrames
	state.SelectTab(0)
	tester.Frame()
	if first.focusedFrames == 0 {
		t.Fatal("the newly active Tab's terminal did not receive focus")
	}
	if second.focusedFrames != beforeSecond {
		t.Fatal("the inactive Tab's terminal kept receiving focus after the switch")
	}
}

// TestActiveTerminalFillsThePanelBodyAndFollowsResize proves the terminal is
// mounted in the panel's remaining area and remeasures with the Panel.
func TestActiveTerminalFillsThePanelBodyAndFollowsResize(t *testing.T) {
	fixture := newViewFixture(t)
	state := fixture.state
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	tester := renderPanel(t, state, 800, 400)
	rect, ok := tester.Find("surface-1")
	if !ok {
		t.Fatalf("the active terminal is not in the tree: %v", tester.Texts())
	}
	// The frame's bar is 36 DIPs; the terminal fills the rest of the Panel.
	if rect.Y < 36 {
		t.Fatalf("terminal top = %v, want below the 36 DIP bar", rect.Y)
	}
	if rect.W != 800 {
		t.Fatalf("terminal width = %v, want the full 800 DIP Panel", rect.W)
	}
	if rect.H != 400-36 {
		t.Fatalf("terminal height = %v, want the body's 364 DIPs", rect.H)
	}

	tester.SetSize(500, 300)
	tester.Frame()
	rect, ok = tester.Find("surface-1")
	if !ok {
		t.Fatal("the active terminal disappeared after a resize")
	}
	if rect.W != 500 || rect.H != 300-36 {
		t.Fatalf("terminal rect after resize = %v x %v, want 500 x 264", rect.W, rect.H)
	}
}

// TestViewShowsAProblemWhenTheShellCannotOpen keeps a failed first show
// explainable instead of blank.
func TestViewShowsAProblemWhenTheShellCannotOpen(t *testing.T) {
	opener := &testOpener{err: errors.New("no local shell")}
	state := NewState(opener, func(*appsession.Session) (Surface, error) {
		return &viewSurface{name: "unused"}, nil
	})
	t.Cleanup(state.Shutdown)
	if err := state.Activate(); err == nil {
		t.Fatal("Activate hid a failed local Shell")
	}
	tester := renderPanel(t, state, 800, 400)
	if !tester.HasText(locales.In("en").PaneOpenFailed()) {
		t.Fatalf("the panel did not explain the failed local Shell: %v", tester.Texts())
	}
}
